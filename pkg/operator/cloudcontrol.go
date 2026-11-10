// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ccv1alpha1 "github.com/highfivelabs/ack-extensions/cloudcontrol/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// Cloud Control: one manager per environment (the builder, 2026-11-09).
//
// "I think the scoping should be at the environment level, so each
// environment gets an identity attached, and not through Workload Identity
// but instead through Pod Identity. [...] Loosely, this follows the KCC
// model of a GSA and KSA workload identity bound that is enabled within a
// namespace to run KCC, and really that namespace has a scope that it works
// within."
//
// So a CloudControlContext in a namespace (an environment) makes:
//
//   - in the operator's namespace, as Config Connector runs each
//     namespace's manager in cnrm-system: the ServiceAccount
//     ack-extensions-cloudcontrol-<environment>, with no IRSA annotation (an
//     EKS Pod Identity association, made outside the cluster, binds it to
//     the environment's role), and the manager's Deployment, watching only
//     the environment's namespace and serving the kinds the context
//     enables. Nobody who may only create pods in the environment's
//     namespace can run as the role, so Kubernetes admission policy on the
//     kinds cannot be stepped around with the role's credentials;
//   - in the environment's namespace, owned by the context: a RoleBinding
//     of that ServiceAccount to the manager's generated ClusterRole, so its
//     Kubernetes rights hold there only (its leader-election lease and
//     events are there too).
//
// The context carries a finalizer, since an owner reference cannot cross
// namespaces: deleting it deletes the manager and its ServiceAccount.
// Unlike the ACK side of the operator, nothing is copied from an ACK
// controller: the context says the role and the region.
const (
	// CloudControlManager names the RoleBinding in each environment and
	// the manager's ClusterRole.
	CloudControlManager = Name + "-cloudcontrol-manager"
	// ImageCloudControl is the Images key of the Cloud Control manager's
	// image (MANAGER_IMAGE_CLOUDCONTROL).
	ImageCloudControl = "cloudcontrol"
	// LabelComponent marks what the Cloud Control side made, so the ACK
	// side's clean-up leaves it alone.
	LabelComponent = "aws.hfl.dev/component"
	// LabelEnvironment names the environment a manager serves.
	LabelEnvironment = "aws.hfl.dev/environment"
	// FinalizerContext keeps a context until its manager is gone.
	FinalizerContext   = "cloudcontrol.aws.hfl.dev/manager"
	componentCC        = "cloudcontrol"
	annotationRoleARN  = "aws.hfl.dev/role-arn"
	conditionReady     = "Ready"
	reasonUnknownKinds = "UnknownKinds"
)

// ContextReconciler runs a Cloud Control manager for each namespace that
// has a CloudControlContext.
type ContextReconciler struct {
	Client client.Client
	// Namespace is the operator's own, where the managers run.
	Namespace string
	// Image is the manager's image.
	Image string
	// FakeAWS, for tests and kind only, points every manager at a fake AWS
	// (every endpoint, fake credentials, a dead proxy), set from the
	// operator's flag, never from a context: an environment cannot point
	// its manager elsewhere.
	FakeAWS *FakeAWS
}

func (r *ContextReconciler) systemNamespace() string {
	if r.Namespace == "" {
		return ccv1alpha1.DefaultSystemNamespace
	}
	return r.Namespace
}

// SetupWithManager reconciles each context, and again when what it made
// changes.
func (r *ContextReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toContext := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		env := o.GetLabels()[LabelEnvironment]
		if o.GetLabels()[LabelComponent] != componentCC || env == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: env, Name: ccv1alpha1.ContextName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(Name+"-cloudcontrol").
		For(&ccv1alpha1.CloudControlContext{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&appsv1.Deployment{}, toContext).
		Watches(&corev1.ServiceAccount{}, toContext).
		Complete(r)
}

// ContextKinds checks a context's kinds against the registry and returns
// them sorted as Kind.service; empty means every kind.
func ContextKinds(cc *ccv1alpha1.CloudControlContext) ([]string, error) {
	var out, unknown []string
	for _, k := range cc.Spec.Kinds {
		if registry.ByRef(k) == nil {
			unknown = append(unknown, k)
			continue
		}
		out = append(out, k)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("no such Cloud Control kind: %s", strings.Join(unknown, ", "))
	}
	sort.Strings(out)
	return out, nil
}

func ccLabels(env string) map[string]string {
	return map[string]string{
		LabelManagedBy:           managedBy,
		LabelComponent:           componentCC,
		LabelEnvironment:         env,
		"app.kubernetes.io/name": Name + "-cloudcontrol-manager",
	}
}

func ccOwner(cc *ccv1alpha1.CloudControlContext) []metav1.OwnerReference {
	return []metav1.OwnerReference{*metav1.NewControllerRef(cc, ccv1alpha1.GroupVersion.WithKind("CloudControlContext"))}
}

// ManagerArgs is the manager's command line for a context: ACK's runtime
// flags, scoped to the environment's namespace.
func (r *ContextReconciler) ManagerArgs(cc *ccv1alpha1.CloudControlContext, kinds []string) []string {
	policy := cc.Spec.DeletionPolicy
	if policy == "" {
		policy = "delete"
	}
	args := []string{
		"--watch-namespace=" + cc.Namespace,
		"--aws-region=" + cc.Spec.Region,
		"--deletion-policy=" + policy,
		"--enable-leader-election=true",
		"--leader-election-namespace=" + cc.Namespace,
		// One watch namespace: ACK's runtime turns CARM off anyway; the
		// environment's role is the identity.
		"--enable-carm=false",
		"--metrics-addr=:8080",
		"--healthz-addr=:8081",
	}
	if len(kinds) > 0 {
		args = append(args, "--cloudcontrol-kinds="+strings.Join(kinds, ","))
	}
	return args
}

// PlanContext is what should exist for a context: the RoleBinding in its
// namespace, the ServiceAccount and the manager in the operator's.
func (r *ContextReconciler) PlanContext(cc *ccv1alpha1.CloudControlContext, kinds []string) []client.Object {
	env, sys := cc.Namespace, r.systemNamespace()
	name := ccv1alpha1.ManagerName(env)
	lbl := ccLabels(env)
	podLabels := map[string]string{LabelComponent: componentCC, LabelEnvironment: env}
	vars := []corev1.EnvVar{
		// ACK's runtime looks for its own ConfigMaps (CARM, off here) in
		// this namespace.
		{Name: "ACK_SYSTEM_NAMESPACE", Value: env},
	}
	return []client.Object{
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: CloudControlManager, Namespace: env, Labels: lbl, OwnerReferences: ccOwner(cc)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: CloudControlManager},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: sys}},
		},
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: sys, Labels: lbl,
				// Informational: EKS Pod Identity reads no annotation; the
				// association names this ServiceAccount.
				Annotations: map[string]string{annotationRoleARN: cc.Spec.RoleARN},
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
							Image: r.Image,
							Args:  r.ManagerArgs(cc, kinds),
							Env:   vars,
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

// removeManager deletes what a context made in the operator's namespace.
func (r *ContextReconciler) removeManager(ctx context.Context, env string) error {
	sys, name := r.systemNamespace(), ccv1alpha1.ManagerName(env)
	for _, o := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sys}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sys}},
	} {
		if err := r.Client.Delete(ctx, o, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// Reconcile makes the plan for a context exist and reports it.
func (r *ContextReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var cc ccv1alpha1.CloudControlContext
	if err := r.Client.Get(ctx, req.NamespacedName, &cc); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Gone without the finalizer running (it was removed by
			// hand): remove the manager anyway.
			return reconcile.Result{}, r.removeManager(ctx, req.Namespace)
		}
		return reconcile.Result{}, err
	}
	if !cc.DeletionTimestamp.IsZero() {
		if err := r.removeManager(ctx, cc.Namespace); err != nil {
			return reconcile.Result{}, err
		}
		if controllerutil.RemoveFinalizer(&cc, FinalizerContext) {
			return reconcile.Result{}, r.Client.Update(ctx, &cc)
		}
		return reconcile.Result{}, nil
	}
	if controllerutil.AddFinalizer(&cc, FinalizerContext) {
		if err := r.Client.Update(ctx, &cc); err != nil {
			return reconcile.Result{}, err
		}
	}
	name := ccv1alpha1.ManagerName(cc.Namespace)
	status := ccv1alpha1.CloudControlContextStatus{
		ObservedGeneration: cc.Generation,
		ServiceAccount:     r.systemNamespace() + "/" + name,
		PodIdentityAssociation: &ccv1alpha1.PodIdentityAssociation{
			Namespace: r.systemNamespace(), ServiceAccount: name, RoleARN: cc.Spec.RoleARN,
		},
		Conditions: cc.Status.Conditions,
	}
	kinds, err := ContextKinds(&cc)
	if err != nil {
		// Leave whatever runs alone until the context is fixed; say why.
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type: conditionReady, Status: metav1.ConditionFalse, Reason: reasonUnknownKinds,
			Message: err.Error(), ObservedGeneration: cc.Generation,
		})
		return reconcile.Result{}, r.writeStatus(ctx, &cc, status)
	}
	status.Kinds = kinds
	if len(kinds) == 0 {
		for _, t := range registry.Types {
			status.Kinds = append(status.Kinds, t.Ref())
		}
		sort.Strings(status.Kinds)
	}
	var errs []string
	for _, o := range r.PlanContext(&cc, kinds) {
		if d, ok := o.(*appsv1.Deployment); ok {
			if err := r.FakeAWS.Fake(&d.Spec.Template.Spec.Containers[0]); err != nil {
				// Refused: nothing that could reach real AWS is made.
				errs = append(errs, fmt.Sprintf("Deployment %s/%s: %v", d.Namespace, d.Name, err))
				continue
			}
		}
		if err := applyObject(ctx, r.Client, o); err != nil {
			errs = append(errs, fmt.Sprintf("%s %s/%s: %v", kindOf(o), o.GetNamespace(), o.GetName(), err))
		}
	}
	cond := metav1.Condition{Type: conditionReady, ObservedGeneration: cc.Generation}
	var dep appsv1.Deployment
	switch {
	case len(errs) > 0:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "ApplyFailed", strings.Join(errs, "; ")
	case r.Client.Get(ctx, types.NamespacedName{Namespace: r.systemNamespace(), Name: name}, &dep) != nil:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "RollingOut", "the manager's Deployment is not readable yet"
	case dep.Status.ObservedGeneration >= dep.Generation && dep.Status.AvailableReplicas >= 1 && dep.Status.UpdatedReplicas >= 1:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "ManagerAvailable",
			fmt.Sprintf("the manager runs as %s/%s, watching %s; its AWS role comes from the EKS Pod Identity association in status", r.systemNamespace(), name, cc.Namespace)
	default:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "RollingOut", "the manager's Deployment is rolling out"
	}
	meta.SetStatusCondition(&status.Conditions, cond)
	if err := r.writeStatus(ctx, &cc, status); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return reconcile.Result{}, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return reconcile.Result{}, nil
}

func (r *ContextReconciler) writeStatus(ctx context.Context, cc *ccv1alpha1.CloudControlContext, st ccv1alpha1.CloudControlContextStatus) error {
	cc.Status = st
	if err := r.Client.Status().Update(ctx, cc); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	return nil
}
