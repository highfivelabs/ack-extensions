// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	ccv1alpha1 "github.com/highfivelabs/ack-extensions/cloudcontrol/apis/v1alpha1"
)

const envRole = "arn:aws:iam::111122223333:role/team-a-prod-cloudcontrol"

func repoRoot(t *testing.T) string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..")
}

// startCCEnv is envtest with CloudControlContext's CRD and the Cloud
// Control kinds' CRDs and ClusterRole, as config/default installs them.
func startCCEnv(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run through task test")
	}
	root := repoRoot(t)
	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(root, "cloudcontrol/config/context"), filepath.Join(root, "cloudcontrol/config/crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := te.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = te.Stop() })
	sch := k8sruntime.NewScheme()
	_ = scheme.AddToScheme(sch)
	_ = ccv1alpha1.AddToScheme(sch)
	c, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "cloudcontrol/config/rbac/role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &role); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Create(ctx, &role); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"team-a-prod", "team-b-prod"} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func ccContext(ns string, kinds ...string) *ccv1alpha1.CloudControlContext {
	return &ccv1alpha1.CloudControlContext{
		ObjectMeta: metav1.ObjectMeta{Name: ccv1alpha1.ContextName, Namespace: ns},
		Spec:       ccv1alpha1.CloudControlContextSpec{RoleARN: envRole, Region: "us-west-2", Kinds: kinds},
	}
}

// can asks the API server whether the namespace's manager ServiceAccount
// may do verb on resource in ns.
func can(t *testing.T, c client.Client, saNS, verb, group, resource, ns string) bool {
	t.Helper()
	sar := &authv1.SubjectAccessReview{Spec: authv1.SubjectAccessReviewSpec{
		User:   "system:serviceaccount:" + saNS + ":" + ccv1alpha1.ManagerServiceAccount,
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + saNS, "system:authenticated"},
		ResourceAttributes: &authv1.ResourceAttributes{
			Namespace: ns, Verb: verb, Group: group, Resource: resource,
		},
	}}
	if err := c.Create(context.Background(), sar); err != nil {
		t.Fatal(err)
	}
	return sar.Status.Allowed
}

func TestCloudControlContext(t *testing.T) {
	c := startCCEnv(t)
	ctx := context.Background()
	r := &ContextReconciler{
		Client: c, Image: "example.test/cloudcontrol-manager:dev",
		ExtraArgs: []string{"--aws-endpoint-url=http://moto.fake-aws:5000"},
		ExtraEnv:  []corev1.EnvVar{{Name: "AWS_ACCESS_KEY_ID", Value: "testing"}},
	}
	key := types.NamespacedName{Namespace: "team-a-prod", Name: ccv1alpha1.ContextName}
	pass := func(t *testing.T) {
		t.Helper()
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	getCC := func(t *testing.T) *ccv1alpha1.CloudControlContext {
		t.Helper()
		var cc ccv1alpha1.CloudControlContext
		if err := c.Get(ctx, key, &cc); err != nil {
			t.Fatal(err)
		}
		return &cc
	}

	t.Run("one per namespace, named cloudcontrol", func(t *testing.T) {
		bad := ccContext("team-a-prod")
		bad.Name = "other"
		err := c.Create(ctx, bad)
		if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "must be named cloudcontrol") {
			t.Fatalf("a context named %q: %v, want a refusal", bad.Name, err)
		}
		bad = ccContext("team-a-prod")
		bad.Spec.RoleARN = "arn:aws:iam::not-an-account:role/x"
		if err := c.Create(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatalf("a malformed role ARN: %v, want a refusal", err)
		}
	})

	t.Run("an unknown kind is reported, nothing is made", func(t *testing.T) {
		if err := c.Create(ctx, ccContext("team-a-prod", "SAMLProvider.iam", "Nope.iam")); err != nil {
			t.Fatal(err)
		}
		pass(t)
		cond := meta.FindStatusCondition(getCC(t).Status.Conditions, "Ready")
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonUnknownKinds || !strings.Contains(cond.Message, "Nope.iam") {
			t.Fatalf("Ready condition %+v", cond)
		}
		var deps appsv1.DeploymentList
		if err := c.List(ctx, &deps, client.InNamespace("team-a-prod")); err != nil || len(deps.Items) != 0 {
			t.Fatalf("made %d Deployments for a context with an unknown kind (%v)", len(deps.Items), err)
		}
	})

	t.Run("the namespace's manager, its ServiceAccount and its binding", func(t *testing.T) {
		cc := getCC(t)
		cc.Spec.Kinds = []string{"SAMLProvider.iam", "Detector.guardduty"}
		cc.Spec.DeletionPolicy = "retain"
		if err := c.Update(ctx, cc); err != nil {
			t.Fatal(err)
		}
		pass(t)

		var sa corev1.ServiceAccount
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a-prod", Name: ccv1alpha1.ManagerServiceAccount}, &sa); err != nil {
			t.Fatal(err)
		}
		for k := range sa.Annotations {
			if strings.HasPrefix(k, "eks.amazonaws.com/") {
				t.Errorf("an IRSA annotation %s: identity is EKS Pod Identity, not IRSA", k)
			}
		}
		if sa.Annotations[annotationRoleARN] != envRole {
			t.Errorf("ServiceAccount annotations %v", sa.Annotations)
		}

		var rb rbacv1.RoleBinding
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a-prod", Name: CloudControlManager}, &rb); err != nil {
			t.Fatal(err)
		}
		if rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != CloudControlManager || len(rb.Subjects) != 1 || rb.Subjects[0].Name != ccv1alpha1.ManagerServiceAccount {
			t.Errorf("RoleBinding %+v", rb)
		}

		var dep appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a-prod", Name: CloudControlManager}, &dep); err != nil {
			t.Fatal(err)
		}
		pod := dep.Spec.Template.Spec
		if pod.ServiceAccountName != ccv1alpha1.ManagerServiceAccount {
			t.Errorf("runs as %q", pod.ServiceAccountName)
		}
		args := pod.Containers[0].Args
		for _, want := range []string{
			"--watch-namespace=team-a-prod", "--aws-region=us-west-2", "--deletion-policy=retain",
			"--leader-election-namespace=team-a-prod", "--enable-carm=false",
			"--cloudcontrol-kinds=Detector.guardduty,SAMLProvider.iam",
			"--aws-endpoint-url=http://moto.fake-aws:5000",
		} {
			if !slices.Contains(args, want) {
				t.Errorf("args lack %s: %v", want, args)
			}
		}
		if pod.Containers[0].Image != r.Image {
			t.Errorf("image %s", pod.Containers[0].Image)
		}
		for _, o := range []metav1.Object{&sa, &rb, &dep} {
			if len(o.GetOwnerReferences()) != 1 || o.GetOwnerReferences()[0].Kind != "CloudControlContext" {
				t.Errorf("%s not owned by the context: %v", o.GetName(), o.GetOwnerReferences())
			}
			if o.GetLabels()[LabelComponent] != componentCC {
				t.Errorf("%s labels %v", o.GetName(), o.GetLabels())
			}
		}

		st := getCC(t).Status
		if st.PodIdentityAssociation == nil || *st.PodIdentityAssociation != (ccv1alpha1.PodIdentityAssociation{Namespace: "team-a-prod", ServiceAccount: ccv1alpha1.ManagerServiceAccount, RoleARN: envRole}) {
			t.Errorf("status.podIdentityAssociation %+v", st.PodIdentityAssociation)
		}
		if !slices.Equal(st.Kinds, []string{"Detector.guardduty", "SAMLProvider.iam"}) {
			t.Errorf("status.kinds %v", st.Kinds)
		}
		// No pods run on envtest, so the manager is never available here.
		if cond := meta.FindStatusCondition(st.Conditions, "Ready"); cond == nil || cond.Reason != "RollingOut" {
			t.Errorf("Ready %+v", cond)
		}
	})

	t.Run("its Kubernetes rights hold in its namespace only", func(t *testing.T) {
		for _, tc := range []struct {
			verb, group, resource, ns string
			want                      bool
		}{
			{"create", "iam.cloudcontrol.aws.hfl.dev", "samlproviders", "team-a-prod", true},
			{"patch", "guardduty.cloudcontrol.aws.hfl.dev", "detectors/status", "team-a-prod", true},
			{"get", "", "secrets", "team-a-prod", true},
			{"create", "coordination.k8s.io", "leases", "team-a-prod", true},
			{"create", "iam.cloudcontrol.aws.hfl.dev", "samlproviders", "team-b-prod", false},
			{"get", "", "secrets", "team-b-prod", false},
			{"get", "", "secrets", "kube-system", false},
			{"list", "", "namespaces", "", false},
			{"create", "cloudcontrol.aws.hfl.dev", "cloudcontrolcontexts", "team-a-prod", false},
			{"create", "rbac.authorization.k8s.io", "rolebindings", "team-a-prod", false},
		} {
			if got := can(t, c, "team-a-prod", tc.verb, tc.group, tc.resource, tc.ns); got != tc.want {
				t.Errorf("can %s %s.%s in %q = %v, want %v", tc.verb, tc.resource, tc.group, tc.ns, got, tc.want)
			}
		}
	})

	t.Run("every kind when the context names none", func(t *testing.T) {
		cc := getCC(t)
		cc.Spec.Kinds = nil
		if err := c.Update(ctx, cc); err != nil {
			t.Fatal(err)
		}
		pass(t)
		var dep appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a-prod", Name: CloudControlManager}, &dep); err != nil {
			t.Fatal(err)
		}
		for _, a := range dep.Spec.Template.Spec.Containers[0].Args {
			if strings.HasPrefix(a, "--cloudcontrol-kinds") {
				t.Errorf("kept %s", a)
			}
		}
		if got := getCC(t).Status.Kinds; len(got) != 3 {
			t.Errorf("status.kinds %v", got)
		}
	})

	t.Run("the ACK side leaves the Cloud Control objects alone", func(t *testing.T) {
		ack := &Reconciler{Client: c, Options: Options{Images: map[string]string{"rds": "example.test/rds:dev"}, Identity: IdentityOwn}}
		if _, err := ack.Reconcile(ctx, allKey); err != nil {
			t.Fatal(err)
		}
		var dep appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a-prod", Name: CloudControlManager}, &dep); err != nil {
			t.Fatalf("the ACK side's clean-up removed the Cloud Control manager: %v", err)
		}
	})
}
