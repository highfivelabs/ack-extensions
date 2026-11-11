// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/highfivelabs/ack-extensions/apis/core/v1alpha1"
)

// ACK-generated managers: one per controller per environment (the
// builder, 2026-11-10: "ACK's per-controller managers should follow the
// same environment-scoped Pod Identity model", one manager per controller
// per namespace).
//
// An ACKContext in a namespace (an environment) lists controllers
// (services). For each, the operator makes, as for a CloudControlContext:
//
//   - in its own namespace: the ServiceAccount
//     ack-extensions-<service>-<environment>, with no IRSA annotation (an
//     EKS Pod Identity association, made outside the cluster, binds it to
//     the environment's role), and the manager's Deployment, watching only
//     the environment's namespace and serving the kinds the context
//     enables;
//   - in the environment's namespace, owned by the context: a RoleBinding
//     of that ServiceAccount to the service manager's ClusterRole
//     (config/rbac), and a leader-election Role and RoleBinding, so its
//     Kubernetes rights hold there only.
//
// It refuses, with the reason in status, a controller that is unknown,
// organization-scoped (the Organizations kinds keep their own install,
// docs/plan.md section 7.5), or whose kinds in that namespace are already
// served by a manager beside an ACK controller that watches it (two
// managers, as two identities, would reconcile the same objects).
const (
	// componentACK marks what an ACKContext made, so the beside side's
	// clean-up and the Cloud Control side leave it alone.
	componentACK = "ack"
	// FinalizerACKContext keeps a context until its managers are gone.
	FinalizerACKContext = "core.aws.hfl.dev/managers"

	ReasonUnknownService     = "UnknownService"
	ReasonUnknownKinds       = "UnknownKinds"
	ReasonOrganizationScoped = "OrganizationScoped"
	ReasonNamespaceClaimed   = "NamespaceClaimed"
	// ReasonControllersRefused is Ready's reason when a controller is
	// refused; status.refused says which and why.
	ReasonControllersRefused = "ControllersRefused"
)

// KindsFunc returns the kinds served in a service's API group
// (<service>.aws.hfl.dev), or none if the group is not served.
type KindsFunc func(ctx context.Context, service string) ([]string, error)

// DiscoveryKinds reads a service's kinds from the API server: the kinds
// of the CRDs installed in its group.
func DiscoveryKinds(dc discovery.DiscoveryInterface) KindsFunc {
	return func(_ context.Context, service string) ([]string, error) {
		rl, err := dc.ServerResourcesForGroupVersion(service + ".aws.hfl.dev/v1alpha1")
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var out []string
		for _, r := range rl.APIResources {
			if !strings.Contains(r.Name, "/") {
				out = append(out, r.Kind)
			}
		}
		sort.Strings(out)
		return out, nil
	}
}

// ACKContextReconciler runs, for each namespace with an ACKContext, one
// manager per controller the context enables.
type ACKContextReconciler struct {
	Client client.Client
	// Namespace is the operator's own, where the managers run.
	Namespace string
	// Images is the manager image of each service (Options.Images).
	Images map[string]string
	// Kinds says which kinds a service serves (DiscoveryKinds).
	Kinds KindsFunc
	// FakeAWS, for tests and kind only, points every manager at a fake AWS,
	// set from the operator's flag, never from a context.
	FakeAWS *FakeAWS
}

func (r *ACKContextReconciler) systemNamespace() string {
	if r.Namespace == "" {
		return corev1alpha1.DefaultSystemNamespace
	}
	return r.Namespace
}

// SetupWithManager reconciles each context, again when what it made
// changes, and every context when an ACK controller's Deployment changes
// (it may now watch, or no longer watch, a context's namespace).
func (r *ACKContextReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toContext := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		l := o.GetLabels()
		if l[LabelComponent] == componentACK && l[LabelEnvironment] != "" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: l[LabelEnvironment], Name: corev1alpha1.ContextName}}}
		}
		d, ok := o.(*appsv1.Deployment)
		if !ok || ACKService(d) == "" {
			return nil
		}
		var all corev1alpha1.ACKContextList
		if err := r.Client.List(ctx, &all); err != nil {
			return nil
		}
		var out []reconcile.Request
		for _, c := range all.Items {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: c.Namespace, Name: c.Name}})
		}
		return out
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(Name+"-ackcontext").
		For(&corev1alpha1.ACKContext{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&rbacv1.Role{}).
		Watches(&appsv1.Deployment{}, toContext).
		Watches(&corev1.ServiceAccount{}, toContext).
		Complete(r)
}

var varRef = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)

// WatchNamespaces is what an ACK controller's Deployment watches, read as
// ACK's runtime reads it: --watch-namespace, a comma-separated list, after
// Kubernetes expands $(VAR) from the container's environment. all is true
// when it watches every namespace: no flag, an empty value, or a value
// that cannot be read here (a variable set from a Secret or ConfigMap).
func WatchNamespaces(d *appsv1.Deployment) (all bool, namespaces map[string]bool) {
	c := controllerContainer(d)
	if c == nil {
		return true, nil
	}
	env := map[string]string{}
	for _, e := range c.Env {
		if e.ValueFrom == nil {
			env[e.Name] = e.Value
		}
	}
	val, found := "", false
	for i, a := range c.Args {
		if v, ok := strings.CutPrefix(a, "--watch-namespace="); ok {
			val, found = v, true
		} else if a == "--watch-namespace" && i+1 < len(c.Args) {
			val, found = c.Args[i+1], true
		}
	}
	if !found {
		return true, nil
	}
	unknown := false
	val = varRef.ReplaceAllStringFunc(val, func(m string) string {
		v, ok := env[varRef.FindStringSubmatch(m)[1]]
		if !ok {
			unknown = true
		}
		return v
	})
	if unknown {
		return true, nil
	}
	namespaces = map[string]bool{}
	for _, n := range strings.Split(val, ",") {
		if n = strings.TrimSpace(n); n != "" {
			namespaces[n] = true
		}
	}
	if len(namespaces) == 0 {
		return true, nil
	}
	return false, namespaces
}

// ClaimedBy is the ACK controller Deployment (namespace/name) beside which
// a manager of service runs that watches namespace env, or "". deps are
// every Deployment in the cluster; images the operator's manager images.
func ClaimedBy(deps []appsv1.Deployment, images map[string]string, service, env string) string {
	for i := range deps {
		d := &deps[i]
		ack := ACKService(d)
		if ack == "" || !d.DeletionTimestamp.IsZero() || !slices.Contains(servicesBeside(images, ack), service) {
			continue
		}
		if all, nss := WatchNamespaces(d); all || nss[env] {
			return d.Namespace + "/" + d.Name
		}
	}
	return ""
}

func ackLabels(service, env string) map[string]string {
	return map[string]string{
		LabelManagedBy:           managedBy,
		LabelComponent:           componentACK,
		LabelEnvironment:         env,
		LabelService:             service,
		"app.kubernetes.io/name": managerName(service),
	}
}

func ackOwner(ac *corev1alpha1.ACKContext) []metav1.OwnerReference {
	return []metav1.OwnerReference{*metav1.NewControllerRef(ac, corev1alpha1.GroupVersion.WithKind("ACKContext"))}
}

// environmentBinding names the RoleBinding, in the environment, of a
// service's manager to its ClusterRole; with "-leader-election", its
// leader-election Role and RoleBinding. Not the beside manager's names, so
// an ACK controller installed in the environment's namespace keeps its own.
func environmentBinding(service string) string { return Name + "-" + service + "-environment" }

// ManagerArgsFor is a manager's command line for a context: ACK's runtime
// flags, scoped to the environment's namespace; --reconcile-resources only
// when the context names the kinds.
func ManagerArgsFor(ac *corev1alpha1.ACKContext, kinds []string) []string {
	policy := ac.Spec.DeletionPolicy
	if policy == "" {
		policy = "delete"
	}
	args := []string{
		"--watch-namespace=" + ac.Namespace,
		"--aws-region=" + ac.Spec.Region,
		"--deletion-policy=" + policy,
		"--enable-leader-election=true",
		"--leader-election-namespace=" + ac.Namespace,
		// One watch namespace: the environment's role is the identity.
		"--enable-carm=false",
		"--metrics-addr=:8080",
		"--healthz-addr=:8081",
	}
	if len(kinds) > 0 {
		args = append(args, "--reconcile-resources="+strings.Join(kinds, ","))
	}
	return args
}

// PlanController is what should exist for one controller of a context:
// the ServiceAccount and the manager in the operator's namespace, the
// RoleBinding and leader-election Role and RoleBinding in the context's.
// kinds are the kinds the context names (empty: every kind).
func (r *ACKContextReconciler) PlanController(ac *corev1alpha1.ACKContext, service string, kinds []string) []client.Object {
	env, sys := ac.Namespace, r.systemNamespace()
	name := corev1alpha1.ManagerName(service, env)
	lbl := ackLabels(service, env)
	podLabels := map[string]string{LabelComponent: componentACK, LabelEnvironment: env, LabelService: service}
	sa := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: sys}}
	le := environmentBinding(service) + "-leader-election"
	return []client.Object{
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: environmentBinding(service), Namespace: env, Labels: lbl, OwnerReferences: ackOwner(ac)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: ManagerRole(service)},
			Subjects:   sa,
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: le, Namespace: env, Labels: lbl, OwnerReferences: ackOwner(ac)},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: le, Namespace: env, Labels: lbl, OwnerReferences: ackOwner(ac)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: le},
			Subjects:   sa,
		},
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: sys, Labels: lbl,
				// Informational: EKS Pod Identity reads no annotation; the
				// association names this ServiceAccount.
				Annotations: map[string]string{annotationRoleARN: ac.Spec.RoleARN},
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sys, Labels: lbl},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](1),
				Selector: &metav1.LabelSelector{MatchLabels: podLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
					Spec: corev1.PodSpec{
						ServiceAccountName: name,
						SecurityContext: &corev1.PodSecurityContext{
							RunAsNonRoot:   ptr.To(true),
							SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Containers: []corev1.Container{{
							Name:  "manager",
							Image: r.Images[service],
							Args:  ManagerArgsFor(ac, kinds),
							// ACK's runtime looks for its own ConfigMaps (CARM,
							// off here) in this namespace.
							Env:   []corev1.EnvVar{{Name: "ACK_SYSTEM_NAMESPACE", Value: env}},
							Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 8080}, {Name: "health", ContainerPort: 8081}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt(8081)}}},
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt(8081)}}},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
							},
						}},
					},
				},
			},
		},
	}
}

// removeManagers deletes what a context made for the services not in
// keep: the managers and ServiceAccounts in the operator's namespace, and
// the RoleBindings and Roles in the environment's (on the context's
// deletion those also go by their owner reference).
func (r *ACKContextReconciler) removeManagers(ctx context.Context, env string, keep map[string]bool) error {
	sel := client.MatchingLabels{LabelManagedBy: managedBy, LabelComponent: componentACK, LabelEnvironment: env}
	lists := []struct {
		ns   string
		list client.ObjectList
	}{
		{r.systemNamespace(), &appsv1.DeploymentList{}},
		{r.systemNamespace(), &corev1.ServiceAccountList{}},
		{env, &rbacv1.RoleBindingList{}},
		{env, &rbacv1.RoleList{}},
	}
	for _, l := range lists {
		if err := r.Client.List(ctx, l.list, sel, client.InNamespace(l.ns)); err != nil {
			return err
		}
		items, err := metaItems(l.list)
		if err != nil {
			return err
		}
		for _, o := range items {
			if keep[o.GetLabels()[LabelService]] {
				continue
			}
			if err := r.Client.Delete(ctx, o, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

// Reconcile makes the plan for a context exist and reports it.
func (r *ACKContextReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var ac corev1alpha1.ACKContext
	if err := r.Client.Get(ctx, req.NamespacedName, &ac); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Gone without the finalizer running: remove the managers anyway.
			return reconcile.Result{}, r.removeManagers(ctx, req.Namespace, nil)
		}
		return reconcile.Result{}, err
	}
	if !ac.DeletionTimestamp.IsZero() {
		if err := r.removeManagers(ctx, ac.Namespace, nil); err != nil {
			return reconcile.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&ac, FinalizerACKContext) {
			return reconcile.Result{}, r.Client.Update(ctx, &ac)
		}
		return reconcile.Result{}, nil
	}
	if controllerutil.AddFinalizer(&ac, FinalizerACKContext) {
		if err := r.Client.Update(ctx, &ac); err != nil {
			return reconcile.Result{}, err
		}
	}
	var deps appsv1.DeploymentList
	if err := r.Client.List(ctx, &deps); err != nil {
		return reconcile.Result{}, err
	}
	sort.Slice(deps.Items, func(i, j int) bool {
		return deps.Items[i].Namespace+"/"+deps.Items[i].Name < deps.Items[j].Namespace+"/"+deps.Items[j].Name
	})
	env, sys := ac.Namespace, r.systemNamespace()
	status := corev1alpha1.ACKContextStatus{ObservedGeneration: ac.Generation, Conditions: ac.Status.Conditions}
	keep := map[string]bool{}
	var errs []string
	refuse := func(svc, reason, msg string) {
		status.Refused = append(status.Refused, corev1alpha1.ACKRefusal{Service: svc, Reason: reason, Message: msg})
	}
	ctls := slices.Clone(ac.Spec.Controllers)
	sort.Slice(ctls, func(i, j int) bool { return ctls[i].Service < ctls[j].Service })
	for _, ctl := range ctls {
		svc := ctl.Service
		if OrganizationScoped[svc] {
			refuse(svc, ReasonOrganizationScoped, fmt.Sprintf("the %s kinds act on the whole organization and keep an organization-scoped install of their own, beside ACK's %s-controller alone in its namespace (docs/plan.md, section 7.5); no %s manager runs per environment", svc, svc, svc))
			continue
		}
		if svc == ImageCloudControl {
			refuse(svc, ReasonUnknownService, "Cloud Control kinds are enabled by a CloudControlContext, not an ACKContext")
			continue
		}
		if _, ok := r.Images[svc]; !ok {
			refuse(svc, ReasonUnknownService, fmt.Sprintf("the operator has no manager for service %q", svc))
			continue
		}
		served, err := r.Kinds(ctx, svc)
		if err != nil {
			// Leave whatever runs alone until the kinds can be read.
			keep[svc] = true
			errs = append(errs, fmt.Sprintf("%s: reading its kinds: %v", svc, err))
			continue
		}
		if len(served) == 0 {
			refuse(svc, ReasonUnknownService, fmt.Sprintf("no kinds of %s.aws.hfl.dev are installed", svc))
			continue
		}
		var unknown []string
		for _, k := range ctl.Kinds {
			if !slices.Contains(served, k) {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			// Leave whatever runs alone until the context is fixed.
			keep[svc] = true
			sort.Strings(unknown)
			refuse(svc, ReasonUnknownKinds, fmt.Sprintf("no such %s kind: %s (served: %s)", svc, strings.Join(unknown, ", "), strings.Join(served, ", ")))
			continue
		}
		if by := ClaimedBy(deps.Items, r.Images, svc, env); by != "" {
			refuse(svc, ReasonNamespaceClaimed, fmt.Sprintf("namespace %s is already served by the %s manager beside ACK's controller %s, which watches it; two managers would reconcile the same objects as two identities. Narrow that controller's --watch-namespace (ACK's chart: watchNamespace) or leave %s out of this context", env, svc, by, svc))
			continue
		}
		kinds := slices.Clone(ctl.Kinds)
		sort.Strings(kinds)
		var errsHere []string
		for _, o := range r.PlanController(&ac, svc, kinds) {
			if d, ok := o.(*appsv1.Deployment); ok {
				if err := r.FakeAWS.Fake(&d.Spec.Template.Spec.Containers[0]); err != nil {
					// Refused: nothing that could reach real AWS is made.
					errsHere = append(errsHere, fmt.Sprintf("Deployment %s/%s: %v", d.Namespace, d.Name, err))
					continue
				}
			}
			if err := applyObject(ctx, r.Client, o); err != nil {
				errsHere = append(errsHere, fmt.Sprintf("%s %s/%s: %v", kindOf(o), o.GetNamespace(), o.GetName(), err))
			}
		}
		errs = append(errs, errsHere...)
		keep[svc] = true
		name := corev1alpha1.ManagerName(svc, env)
		shown := kinds
		if len(shown) == 0 {
			shown = served
		}
		status.Managers = append(status.Managers, corev1alpha1.ACKManager{Service: svc, ServiceAccount: sys + "/" + name, Kinds: shown})
		status.PodIdentityAssociations = append(status.PodIdentityAssociations, corev1alpha1.PodIdentityAssociation{
			Namespace: sys, ServiceAccount: name, RoleARN: ac.Spec.RoleARN,
		})
	}
	if err := r.removeManagers(ctx, env, keep); err != nil {
		errs = append(errs, err.Error())
	}
	cond := metav1.Condition{Type: conditionReady, ObservedGeneration: ac.Generation}
	var refusals []string
	for _, f := range status.Refused {
		refusals = append(refusals, f.Service+": "+f.Message)
	}
	switch {
	case len(errs) > 0:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "ApplyFailed", strings.Join(errs, "; ")
	case len(status.Refused) > 0:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, ReasonControllersRefused, strings.Join(refusals, "; ")
	default:
		var waiting []string
		for _, m := range status.Managers {
			var dep appsv1.Deployment
			err := r.Client.Get(ctx, types.NamespacedName{Namespace: sys, Name: corev1alpha1.ManagerName(m.Service, env)}, &dep)
			if err != nil || dep.Status.ObservedGeneration < dep.Generation || dep.Status.AvailableReplicas < 1 || dep.Status.UpdatedReplicas < 1 {
				waiting = append(waiting, m.Service)
			}
		}
		if len(waiting) > 0 {
			cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "RollingOut", "rolling out: "+strings.Join(waiting, ", ")
		} else {
			var names []string
			for _, m := range status.Managers {
				names = append(names, m.ServiceAccount)
			}
			cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "ManagersAvailable",
				fmt.Sprintf("the managers run as %s, watching %s; their AWS role comes from the EKS Pod Identity associations in status", strings.Join(names, ", "), env)
		}
	}
	meta.SetStatusCondition(&status.Conditions, cond)
	ac.Status = status
	if err := r.Client.Status().Update(ctx, &ac); err != nil {
		errs = append(errs, fmt.Sprintf("status: %v", err))
	}
	if len(errs) > 0 {
		return reconcile.Result{}, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return reconcile.Result{}, nil
}
