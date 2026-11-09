// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package operator runs one of our managers beside each ACK service
// controller it finds, acting as the same AWS identity:
//
//   - It finds ACK controllers by their Helm chart's label
//     (app.kubernetes.io/name: <service>-chart), or by the annotation
//     aws.hfl.dev/ack-service on a Deployment that was installed some other
//     way.
//   - For each one whose service we extend (applicationautoscaling, autoscaling, ec2, eventbridge, iam, kms, lambda, rds, wafv2), it runs our manager
//     in the same namespace with the controller's own arguments and
//     environment (region, endpoint, watch namespace and selectors, CARM,
//     feature gates, deletion policy, credentials file), so the
//     cross-account role maps (ConfigMaps in that namespace) and
//     IAMRoleSelectors resolve exactly as they do for ACK.
//   - Identity "own" (the default): our manager runs as its own
//     ServiceAccount carrying the ACK ServiceAccount's eks.amazonaws.com/*
//     annotations (IRSA's role ARN). AWS then needs one more grant per role:
//     the role's trust policy must allow our ServiceAccount (IRSA), or an
//     EKS Pod Identity association must name it. Our ServiceAccount is
//     bound only to our manager's role.
//   - Identity "ack": our manager runs as ACK's ServiceAccount: no AWS
//     change at all, but our manager then holds every Kubernetes right ACK's
//     controller holds, and ACK's controller our manager's.
//
// Everything it makes carries the managed-by label and is deleted when its
// ACK controller goes.
package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Name prefixes every object the operator makes. It is the project's name
// in one place.
const Name = "ack-extensions"

const (
	// LabelManagedBy marks what the operator made.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	managedBy      = Name + "-operator"
	// LabelService is our service (applicationautoscaling, autoscaling, ec2, eventbridge, iam, kms, lambda, rds, wafv2).
	LabelService = "aws.hfl.dev/service"
	// LabelSource is the ACK controller Deployment, namespace.name.
	LabelSource = "aws.hfl.dev/source"
	// AnnotationACKService names the ACK service of a Deployment that does
	// not carry the Helm chart's label.
	AnnotationACKService = "aws.hfl.dev/ack-service"
	// AnnotationIdentity on an ACK controller's Deployment overrides the
	// operator's default identity for its manager: "own" or "ack".
	AnnotationIdentity = "aws.hfl.dev/identity"

	IdentityOwn = "own"
	IdentityACK = "ack"
)

// Options configure the operator.
type Options struct {
	// Images is the manager image of each service we extend.
	Images map[string]string
	// Identity is the default identity, IdentityOwn or IdentityACK.
	Identity string
}

// Reconciler makes the desired managers exist and nothing else of ours.
type Reconciler struct {
	Client  client.Client
	Options Options
}

var allKey = reconcile.Request{NamespacedName: types.NamespacedName{Name: "all"}}

// SetupWithManager watches what changes the desired state and reconciles
// it all in one pass.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	all := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{allKey}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(Name+"-operator").
		Watches(&appsv1.Deployment{}, all).
		Watches(&corev1.ServiceAccount{}, all).
		Watches(&rbacv1.ClusterRoleBinding{}, all).
		Complete(r)
}

// ManagerRole is the ClusterRole of a service's manager
// (config/rbac/<service>-manager-role.yaml).
func ManagerRole(service string) string { return Name + "-" + service + "-manager" }

func managerName(service string) string { return Name + "-" + service + "-manager" }

// ACKService is the ACK service a Deployment runs the controller of, or "".
func ACKService(d *appsv1.Deployment) string {
	if s := d.Annotations[AnnotationACKService]; s != "" {
		return s
	}
	if d.Labels[LabelManagedBy] == managedBy {
		return ""
	}
	if n := d.Labels["app.kubernetes.io/name"]; strings.HasSuffix(n, "-chart") {
		return strings.TrimSuffix(n, "-chart")
	}
	return ""
}

func controllerContainer(d *appsv1.Deployment) *corev1.Container {
	cs := d.Spec.Template.Spec.Containers
	for i := range cs {
		if cs[i].Name == "controller" {
			return &cs[i]
		}
	}
	if len(cs) > 0 {
		return &cs[0]
	}
	return nil
}

// Flags of ACK's controller that name ACK's own kinds; ours would refuse
// them (the runtime checks them against the kinds it serves).
var kindFlags = map[string]bool{
	"--reconcile-resources":                     true,
	"--reconcile-resource-resync-seconds":       true,
	"--reconcile-resource-max-concurrent-syncs": true,
}

// ManagerArgs is ACK's controller's arguments without those that name its
// kinds. Everything else is the runtime's own flags, which our manager
// shares.
func ManagerArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := a
		if k, _, ok := strings.Cut(a, "="); ok {
			name = k
		}
		if kindFlags[name] {
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func usedVolumes(pod *corev1.PodSpec, c *corev1.Container) []corev1.Volume {
	used := map[string]bool{}
	for _, m := range c.VolumeMounts {
		used[m.Name] = true
	}
	var out []corev1.Volume
	for _, v := range pod.Volumes {
		if used[v.Name] {
			out = append(out, *v.DeepCopy())
		}
	}
	return out
}

func labels(service string, src *appsv1.Deployment) map[string]string {
	return map[string]string{
		LabelManagedBy:           managedBy,
		LabelService:             service,
		LabelSource:              src.Namespace + "." + src.Name,
		"app.kubernetes.io/name": managerName(service),
	}
}

func owner(src *appsv1.Deployment) []metav1.OwnerReference {
	return []metav1.OwnerReference{*metav1.NewControllerRef(src, appsv1.SchemeGroupVersion.WithKind("Deployment"))}
}

// identityAnnotations are what IRSA reads from a ServiceAccount.
func identityAnnotations(sa *corev1.ServiceAccount) map[string]string {
	out := map[string]string{}
	for k, v := range sa.Annotations {
		if strings.HasPrefix(k, "eks.amazonaws.com/") {
			out[k] = v
		}
	}
	return out
}

type desired struct {
	objs []client.Object
	// keys of every object, kind/namespace/name, for the cleanup pass
	keep map[string]bool
}

func key(kind, ns, name string) string { return kind + "/" + ns + "/" + name }

func (d *desired) add(kind string, o client.Object) {
	d.objs = append(d.objs, o)
	d.keep[key(kind, o.GetNamespace(), o.GetName())] = true
}

// Plan is what should exist for the ACK controller Deployment src of
// service, whose ServiceAccount is ackSA.
func (r *Reconciler) Plan(src *appsv1.Deployment, service string, ackSA *corev1.ServiceAccount) ([]client.Object, error) {
	c := controllerContainer(src)
	if c == nil {
		return nil, fmt.Errorf("%s/%s has no container", src.Namespace, src.Name)
	}
	identity := r.Options.Identity
	if v := src.Annotations[AnnotationIdentity]; v != "" {
		identity = v
	}
	if identity != IdentityOwn && identity != IdentityACK {
		return nil, fmt.Errorf("%s/%s: identity %q is neither %q nor %q", src.Namespace, src.Name, identity, IdentityOwn, IdentityACK)
	}
	ns, name := src.Namespace, managerName(service)
	lbl := labels(service, src)
	var objs []client.Object

	saName := ackSA.Name
	if identity == IdentityOwn {
		saName = name
		objs = append(objs, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns, Labels: lbl, OwnerReferences: owner(src),
				Annotations: identityAnnotations(ackSA),
			},
			ImagePullSecrets: ackSA.ImagePullSecrets,
		})
	}

	objs = append(objs,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-" + ns, Labels: lbl},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: ManagerRole(service)},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: ns}},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-leader-election", Namespace: ns, Labels: lbl, OwnerReferences: owner(src)},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
				{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"}},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-leader-election", Namespace: ns, Labels: lbl, OwnerReferences: owner(src)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name + "-leader-election"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: ns}},
		},
	)

	image := r.Options.Images[service]
	if image == "" {
		return nil, fmt.Errorf("no manager image for service %q", service)
	}
	pod := src.Spec.Template.Spec.DeepCopy()
	mc := corev1.Container{
		Name:            "manager",
		Image:           image,
		Args:            ManagerArgs(c.Args),
		Env:             c.Env,
		EnvFrom:         c.EnvFrom,
		VolumeMounts:    c.VolumeMounts,
		Resources:       c.Resources,
		SecurityContext: c.SecurityContext,
		Ports:           c.Ports,
		LivenessProbe:   c.LivenessProbe,
		ReadinessProbe:  c.ReadinessProbe,
	}
	podLabels := map[string]string{"app.kubernetes.io/name": name, LabelService: service}
	one := int32(1)
	objs = append(objs, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: lbl, OwnerReferences: owner(src)},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: podLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					Containers:         []corev1.Container{mc},
					Volumes:            usedVolumes(pod, c),
					SecurityContext:    pod.SecurityContext,
					NodeSelector:       pod.NodeSelector,
					Tolerations:        pod.Tolerations,
					Affinity:           pod.Affinity,
					PriorityClassName:  pod.PriorityClassName,
					ImagePullSecrets:   pod.ImagePullSecrets,
					HostNetwork:        pod.HostNetwork,
					DNSPolicy:          pod.DNSPolicy,
				},
			},
		},
	})
	return objs, nil
}

func kindOf(o client.Object) string {
	switch o.(type) {
	case *corev1.ServiceAccount:
		return "ServiceAccount"
	case *rbacv1.ClusterRoleBinding:
		return "ClusterRoleBinding"
	case *rbacv1.Role:
		return "Role"
	case *rbacv1.RoleBinding:
		return "RoleBinding"
	case *appsv1.Deployment:
		return "Deployment"
	}
	return fmt.Sprintf("%T", o)
}

// Reconcile is one pass over the whole cluster.
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	lg := log.FromContext(ctx)
	var deps appsv1.DeploymentList
	if err := r.Client.List(ctx, &deps); err != nil {
		return reconcile.Result{}, err
	}
	want := &desired{keep: map[string]bool{}}
	sort.Slice(deps.Items, func(i, j int) bool {
		return deps.Items[i].Namespace+"/"+deps.Items[i].Name < deps.Items[j].Namespace+"/"+deps.Items[j].Name
	})
	var errs []string
	for i := range deps.Items {
		src := &deps.Items[i]
		service := ACKService(src)
		if service == "" {
			continue
		}
		if _, ours := r.Options.Images[service]; !ours {
			continue
		}
		if !src.DeletionTimestamp.IsZero() {
			continue
		}
		saName := src.Spec.Template.Spec.ServiceAccountName
		if saName == "" {
			saName = "default"
		}
		var ackSA corev1.ServiceAccount
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: src.Namespace, Name: saName}, &ackSA); err != nil {
			// Wait for ACK's ServiceAccount rather than run as none.
			lg.Info("waiting for ACK's ServiceAccount", "deployment", src.Namespace+"/"+src.Name, "serviceAccount", saName, "err", err.Error())
			continue
		}
		objs, err := r.Plan(src, service, &ackSA)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, o := range objs {
			want.add(kindOf(o), o)
		}
	}
	for _, o := range want.objs {
		if err := r.apply(ctx, o); err != nil {
			errs = append(errs, fmt.Sprintf("%s %s/%s: %v", kindOf(o), o.GetNamespace(), o.GetName(), err))
		}
	}
	if err := r.prune(ctx, want.keep); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return reconcile.Result{}, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return reconcile.Result{}, nil
}

// apply makes the live object match o in the fields the operator owns.
func (r *Reconciler) apply(ctx context.Context, o client.Object) error {
	live := o.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, live, func() error {
		live.SetLabels(o.GetLabels())
		live.SetOwnerReferences(o.GetOwnerReferences())
		switch want := o.(type) {
		case *corev1.ServiceAccount:
			l := live.(*corev1.ServiceAccount)
			l.Annotations = want.Annotations
			l.ImagePullSecrets = want.ImagePullSecrets
		case *rbacv1.ClusterRoleBinding:
			l := live.(*rbacv1.ClusterRoleBinding)
			if l.RoleRef.Name != "" && l.RoleRef != want.RoleRef {
				return fmt.Errorf("roleRef is immutable; delete %s to change it", l.Name)
			}
			l.RoleRef, l.Subjects = want.RoleRef, want.Subjects
		case *rbacv1.Role:
			live.(*rbacv1.Role).Rules = want.Rules
		case *rbacv1.RoleBinding:
			l := live.(*rbacv1.RoleBinding)
			l.RoleRef, l.Subjects = want.RoleRef, want.Subjects
		case *appsv1.Deployment:
			l := live.(*appsv1.Deployment)
			l.Spec.Replicas = want.Spec.Replicas
			l.Spec.Selector = want.Spec.Selector
			l.Spec.Template = want.Spec.Template
		}
		return nil
	})
	if apierrors.IsInvalid(err) {
		return fmt.Errorf("invalid: %w", err)
	}
	return err
}

// prune deletes what the operator made that no ACK controller needs now.
func (r *Reconciler) prune(ctx context.Context, keep map[string]bool) error {
	sel := client.MatchingLabels{LabelManagedBy: managedBy}
	lists := []struct {
		kind string
		list client.ObjectList
	}{
		{"Deployment", &appsv1.DeploymentList{}},
		{"ServiceAccount", &corev1.ServiceAccountList{}},
		{"RoleBinding", &rbacv1.RoleBindingList{}},
		{"Role", &rbacv1.RoleList{}},
		{"ClusterRoleBinding", &rbacv1.ClusterRoleBindingList{}},
	}
	var errs []string
	for _, l := range lists {
		if err := r.Client.List(ctx, l.list, sel); err != nil {
			return err
		}
		items, err := metaItems(l.list)
		if err != nil {
			return err
		}
		for _, o := range items {
			if keep[key(l.kind, o.GetNamespace(), o.GetName())] {
				continue
			}
			if err := r.Client.Delete(ctx, o, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("prune: %s", strings.Join(errs, "; "))
	}
	return nil
}

func metaItems(l client.ObjectList) ([]client.Object, error) {
	var out []client.Object
	switch t := l.(type) {
	case *appsv1.DeploymentList:
		for i := range t.Items {
			out = append(out, &t.Items[i])
		}
	case *corev1.ServiceAccountList:
		for i := range t.Items {
			out = append(out, &t.Items[i])
		}
	case *rbacv1.RoleBindingList:
		for i := range t.Items {
			out = append(out, &t.Items[i])
		}
	case *rbacv1.RoleList:
		for i := range t.Items {
			out = append(out, &t.Items[i])
		}
	case *rbacv1.ClusterRoleBindingList:
		for i := range t.Items {
			out = append(out, &t.Items[i])
		}
	default:
		return nil, fmt.Errorf("unknown list %T", l)
	}
	return out, nil
}
