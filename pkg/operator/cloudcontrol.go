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
// So a CloudControlContext in a namespace (an environment) makes, in that
// namespace and owned by the context:
//
//   - the ServiceAccount ack-extensions-cloudcontrol, with no IRSA
//     annotation: an EKS Pod Identity association (made outside the
//     cluster) binds it to the environment's role;
//   - a RoleBinding of that ServiceAccount to the manager's generated
//     ClusterRole (cloudcontrol/config/rbac), so its Kubernetes rights hold
//     in this namespace only;
//   - the manager's Deployment, watching only this namespace, serving the
//     kinds the context enables.
//
// Unlike the ACK side of the operator, nothing is copied from an ACK
// controller: the context says the role and the region.
const (
	// CloudControlManager names the manager's Deployment and RoleBinding in
	// each namespace, and its ClusterRole.
	CloudControlManager = Name + "-cloudcontrol-manager"
	// ImageCloudControl is the Images key of the Cloud Control manager's
	// image (MANAGER_IMAGE_CLOUDCONTROL).
	ImageCloudControl = "cloudcontrol"
	// LabelComponent marks what the Cloud Control side made, so the ACK
	// side's clean-up leaves it alone.
	LabelComponent     = "aws.hfl.dev/component"
	componentCC        = "cloudcontrol"
	annotationRoleARN  = "aws.hfl.dev/role-arn"
	conditionReady     = "Ready"
	reasonUnknownKinds = "UnknownKinds"
)

// ContextReconciler runs a Cloud Control manager in each namespace that has
// a CloudControlContext.
type ContextReconciler struct {
	Client client.Client
	// Image is the manager's image.
	Image string
	// ExtraArgs and ExtraEnv are added to every manager. They exist for a
	// fake AWS (an endpoint and dummy credentials in tests and on kind),
	// set from the operator's flags by the platform team, never from a
	// context: a namespace cannot point its manager elsewhere.
	ExtraArgs []string
	ExtraEnv  []corev1.EnvVar
}

// SetupWithManager reconciles each context, and again when what it owns
// changes.
func (r *ContextReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(Name + "-cloudcontrol").
		For(&ccv1alpha1.CloudControlContext{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.RoleBinding{}).
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

func ccLabels(ns string) map[string]string {
	return map[string]string{
		LabelManagedBy:           managedBy,
		LabelComponent:           componentCC,
		LabelSource:              ns + "." + ccv1alpha1.ContextName,
		"app.kubernetes.io/name": CloudControlManager,
	}
}

func ccOwner(cc *ccv1alpha1.CloudControlContext) []metav1.OwnerReference {
	return []metav1.OwnerReference{*metav1.NewControllerRef(cc, ccv1alpha1.GroupVersion.WithKind("CloudControlContext"))}
}

// ManagerArgs is the manager's command line for a context: ACK's runtime
// flags, scoped to the namespace.
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
	return append(args, r.ExtraArgs...)
}

// PlanContext is what should exist in the context's namespace.
func (r *ContextReconciler) PlanContext(cc *ccv1alpha1.CloudControlContext, kinds []string) []client.Object {
	ns := cc.Namespace
	lbl := ccLabels(ns)
	sa := ccv1alpha1.ManagerServiceAccount
	podLabels := map[string]string{"app.kubernetes.io/name": CloudControlManager, LabelComponent: componentCC}
	env := append([]corev1.EnvVar{
		{Name: "ACK_SYSTEM_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
	}, r.ExtraEnv...)
	return []client.Object{
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: sa, Namespace: ns, Labels: lbl, OwnerReferences: ccOwner(cc),
				// Informational: EKS Pod Identity reads no annotation; the
				// association names this ServiceAccount.
				Annotations: map[string]string{annotationRoleARN: cc.Spec.RoleARN},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: CloudControlManager, Namespace: ns, Labels: lbl, OwnerReferences: ccOwner(cc)},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: CloudControlManager},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: ns}},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: CloudControlManager, Namespace: ns, Labels: lbl, OwnerReferences: ccOwner(cc)},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](1),
				Selector: &metav1.LabelSelector{MatchLabels: podLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
					Spec: corev1.PodSpec{
						ServiceAccountName: sa,
						SecurityContext: &corev1.PodSecurityContext{
							RunAsNonRoot:   ptr.To(true),
							SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Containers: []corev1.Container{{
							Name:  "manager",
							Image: r.Image,
							Args:  r.ManagerArgs(cc, kinds),
							Env:   env,
							Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 8080}, {Name: "health", ContainerPort: 8081}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstrPort(8081)}}},
							ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstrPort(8081)}}},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: mustQuantity("10m"), corev1.ResourceMemory: mustQuantity("64Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: mustQuantity("256Mi")},
							},
						}},
					},
				},
			},
		},
	}
}

// Reconcile makes the context's namespace match its plan and reports it.
func (r *ContextReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var cc ccv1alpha1.CloudControlContext
	if err := r.Client.Get(ctx, req.NamespacedName, &cc); err != nil {
		// Gone: what it owned goes with it (owner references).
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !cc.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	sa := ccv1alpha1.ManagerServiceAccount
	status := ccv1alpha1.CloudControlContextStatus{
		ObservedGeneration: cc.Generation,
		ServiceAccount:     sa,
		PodIdentityAssociation: &ccv1alpha1.PodIdentityAssociation{
			Namespace: cc.Namespace, ServiceAccount: sa, RoleARN: cc.Spec.RoleARN,
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
		if err := applyObject(ctx, r.Client, o); err != nil {
			errs = append(errs, fmt.Sprintf("%s %s/%s: %v", kindOf(o), o.GetNamespace(), o.GetName(), err))
		}
	}
	cond := metav1.Condition{Type: conditionReady, ObservedGeneration: cc.Generation}
	var dep appsv1.Deployment
	switch {
	case len(errs) > 0:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "ApplyFailed", strings.Join(errs, "; ")
	case r.Client.Get(ctx, types.NamespacedName{Namespace: cc.Namespace, Name: CloudControlManager}, &dep) != nil:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "RollingOut", "the manager's Deployment is not readable yet"
	case dep.Status.ObservedGeneration >= dep.Generation && dep.Status.AvailableReplicas >= 1 && dep.Status.UpdatedReplicas >= 1:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "ManagerAvailable",
			fmt.Sprintf("the manager runs in %s as %s; the role comes from the EKS Pod Identity association in status", cc.Namespace, sa)
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

func intstrPort(p int) intstr.IntOrString { return intstr.FromInt(p) }

func mustQuantity(s string) resource.Quantity { return resource.MustParse(s) }
