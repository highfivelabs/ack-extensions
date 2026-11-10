// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/highfivelabs/ack-extensions/apis/core/v1alpha1"
)

func TestWatchNamespaces(t *testing.T) {
	dep := func(args []string, env ...corev1.EnvVar) *appsv1.Deployment {
		return &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "controller", Args: args, Env: env}},
		}}}}
	}
	val := func(n, v string) corev1.EnvVar { return corev1.EnvVar{Name: n, Value: v} }
	for _, tc := range []struct {
		name string
		d    *appsv1.Deployment
		all  bool
		want []string
	}{
		{"no flag", dep([]string{"--aws-region=us-west-2"}), true, nil},
		{"ACK's chart, empty", dep(ackArgs, val("ACK_WATCH_NAMESPACE", "")), true, nil},
		{"ACK's chart, two", dep(ackArgs, val("ACK_WATCH_NAMESPACE", "team-a, team-b")), false, []string{"team-a", "team-b"}},
		{"with =", dep([]string{"--watch-namespace=team-c"}), false, []string{"team-c"}},
		{"unset variable", dep(ackArgs), true, nil},
		{"from a ConfigMap", dep(ackArgs, corev1.EnvVar{Name: "ACK_WATCH_NAMESPACE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "ns"}}}), true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			all, got := WatchNamespaces(tc.d)
			var names []string
			for n := range got {
				names = append(names, n)
			}
			slices.Sort(names)
			if all != tc.all || !slices.Equal(names, tc.want) {
				t.Fatalf("got all=%v %v, want all=%v %v", all, names, tc.all, tc.want)
			}
		})
	}
}

// startACKEnv is envtest with ACKContext's CRD, our kinds' CRDs and the
// managers' ClusterRoles, as config/default installs them.
func startACKEnv(t *testing.T) (client.Client, discovery.DiscoveryInterface) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run through task test")
	}
	root := repoRoot(t)
	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(root, "config/context"), filepath.Join(root, "config/crds/bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := te.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = te.Stop() })
	sch := k8sruntime.NewScheme()
	_ = scheme.AddToScheme(sch)
	_ = corev1alpha1.AddToScheme(sch)
	c, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		t.Fatal(err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, svc := range []string{"rds", "scheduler"} {
		b, err := os.ReadFile(filepath.Join(root, "config/rbac", svc+"-manager-role.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var role rbacv1.ClusterRole
		if err := yaml.Unmarshal(b, &role); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, &role); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"team-a", "team-b", sysNS, ns} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}); err != nil {
			t.Fatal(err)
		}
	}
	return c, dc
}

func ackContext(env string, ctls ...corev1alpha1.ACKController) *corev1alpha1.ACKContext {
	return &corev1alpha1.ACKContext{
		ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.ContextName, Namespace: env},
		Spec:       corev1alpha1.ACKContextSpec{RoleARN: "arn:aws:iam::111122223333:role/" + env + "-ack", Region: "us-west-2", Controllers: ctls},
	}
}

func TestACKContext(t *testing.T) {
	c, dc := startACKEnv(t)
	ctx := context.Background()
	fake, err := NewFakeAWS("http://moto.fake-aws.svc.cluster.local:5000", "10.96.0.1")
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]string{
		"rds": "example.test/rds-manager:dev", "scheduler": "example.test/scheduler-manager:dev",
		"organizations": "example.test/organizations-manager:dev", ImageCloudControl: "example.test/cloudcontrol-manager:dev",
	}
	r := &ACKContextReconciler{Client: c, Namespace: sysNS, Images: images, Kinds: DiscoveryKinds(dc), FakeAWS: fake}
	pass := func(t *testing.T, env string) *corev1alpha1.ACKContext {
		t.Helper()
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: env, Name: corev1alpha1.ContextName}}); err != nil {
			t.Fatal(err)
		}
		var ac corev1alpha1.ACKContext
		if err := c.Get(ctx, types.NamespacedName{Namespace: env, Name: corev1alpha1.ContextName}, &ac); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return &ac
	}
	manager := func(svc, env string) (*appsv1.Deployment, bool) {
		var d appsv1.Deployment
		err := c.Get(ctx, types.NamespacedName{Namespace: sysNS, Name: corev1alpha1.ManagerName(svc, env)}, &d)
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return &d, true
	}
	refusal := func(ac *corev1alpha1.ACKContext, svc string) string {
		for _, f := range ac.Status.Refused {
			if f.Service == svc {
				return f.Reason
			}
		}
		return ""
	}
	sched := corev1alpha1.ACKController{Service: "scheduler", Kinds: []string{"ScheduleGroup"}}

	t.Run("one per namespace, named ack", func(t *testing.T) {
		other := ackContext("team-a", sched)
		other.Name = "other"
		err := c.Create(ctx, other)
		if err == nil || !strings.Contains(err.Error(), "must be named ack") {
			t.Fatalf("a second context in team-a: %v", err)
		}
	})

	if err := c.Create(ctx, ackContext("team-a", sched,
		corev1alpha1.ACKController{Service: "rds"},
		corev1alpha1.ACKController{Service: "organizations"},
		corev1alpha1.ACKController{Service: "s3"},
		corev1alpha1.ACKController{Service: ImageCloudControl},
	)); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, ackContext("team-b", corev1alpha1.ACKController{Service: "scheduler"})); err != nil {
		t.Fatal(err)
	}

	t.Run("a manager per controller per namespace", func(t *testing.T) {
		a := pass(t, "team-a")
		b := pass(t, "team-b")
		if !slices.Contains(a.Finalizers, FinalizerACKContext) {
			t.Fatalf("no finalizer: %v", a.Finalizers)
		}
		for _, w := range []struct{ svc, env string }{{"scheduler", "team-a"}, {"rds", "team-a"}, {"scheduler", "team-b"}} {
			d, ok := manager(w.svc, w.env)
			if !ok {
				t.Fatalf("no %s manager for %s", w.svc, w.env)
			}
			spec := d.Spec.Template.Spec
			if spec.ServiceAccountName != corev1alpha1.ManagerName(w.svc, w.env) || spec.Containers[0].Image != images[w.svc] {
				t.Fatalf("%s/%s runs as %s, image %s", w.svc, w.env, spec.ServiceAccountName, spec.Containers[0].Image)
			}
			if !slices.Contains(spec.Containers[0].Args, "--watch-namespace="+w.env) || !slices.Contains(spec.Containers[0].Args, "--enable-carm=false") {
				t.Fatalf("%s/%s args: %v", w.svc, w.env, spec.Containers[0].Args)
			}
			if err := fake.Verify(&spec.Containers[0]); err != nil {
				t.Fatalf("%s/%s could reach real AWS: %v", w.svc, w.env, err)
			}
			var sa corev1.ServiceAccount
			if err := c.Get(ctx, types.NamespacedName{Namespace: sysNS, Name: spec.ServiceAccountName}, &sa); err != nil {
				t.Fatal(err)
			}
			for k := range sa.Annotations {
				if strings.HasPrefix(k, "eks.amazonaws.com/") {
					t.Fatalf("an IRSA annotation on %s: %s", sa.Name, k)
				}
			}
		}
		d, _ := manager("scheduler", "team-a")
		if !slices.Contains(d.Spec.Template.Spec.Containers[0].Args, "--reconcile-resources=ScheduleGroup") {
			t.Fatalf("scheduler/team-a serves more than ScheduleGroup: %v", d.Spec.Template.Spec.Containers[0].Args)
		}
		d, _ = manager("rds", "team-a")
		for _, a := range d.Spec.Template.Spec.Containers[0].Args {
			if strings.HasPrefix(a, "--reconcile-resources") {
				t.Fatalf("rds/team-a names its kinds although the context does not: %v", a)
			}
		}
		if _, ok := manager("organizations", "team-a"); ok {
			t.Fatal("an organizations manager per environment")
		}
		for svc, want := range map[string]string{"organizations": ReasonOrganizationScoped, "s3": ReasonUnknownService, ImageCloudControl: ReasonUnknownService} {
			if got := refusal(a, svc); got != want {
				t.Fatalf("%s refused for %q, want %q: %+v", svc, got, want, a.Status.Refused)
			}
		}
		if got := len(a.Status.PodIdentityAssociations); got != 2 {
			t.Fatalf("team-a associations: %+v", a.Status.PodIdentityAssociations)
		}
		pia := b.Status.PodIdentityAssociations
		if len(pia) != 1 || pia[0] != (corev1alpha1.PodIdentityAssociation{Namespace: sysNS, ServiceAccount: "ack-extensions-scheduler-team-b", RoleARN: "arn:aws:iam::111122223333:role/team-b-ack"}) {
			t.Fatalf("team-b associations: %+v", pia)
		}
		cond := meta.FindStatusCondition(a.Status.Conditions, conditionReady)
		if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != ReasonControllersRefused {
			t.Fatalf("team-a Ready: %+v", cond)
		}
	})

	t.Run("rights in the environment only", func(t *testing.T) {
		sa := corev1alpha1.ManagerName("scheduler", "team-a")
		if !can(t, c, sa, "create", "scheduler.aws.hfl.dev", "schedulegroups", "team-a") {
			t.Fatal("team-a's scheduler manager cannot create ScheduleGroups in team-a")
		}
		if !can(t, c, sa, "create", "coordination.k8s.io", "leases", "team-a") {
			t.Fatal("team-a's scheduler manager cannot take its lease in team-a")
		}
		for _, n := range []string{"team-b", sysNS} {
			if can(t, c, sa, "create", "scheduler.aws.hfl.dev", "schedulegroups", n) || can(t, c, sa, "get", "", "secrets", n) {
				t.Fatalf("team-a's scheduler manager has rights in %s", n)
			}
		}
		if can(t, c, sa, "list", "", "namespaces", "") {
			t.Fatal("team-a's scheduler manager may list namespaces")
		}
	})

	t.Run("refused where a manager beside ACK's controller watches the namespace", func(t *testing.T) {
		d, sa := ackController("rds")
		if err := c.Create(ctx, sa); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		a := pass(t, "team-a")
		if got := refusal(a, "rds"); got != ReasonNamespaceClaimed {
			t.Fatalf("rds refused for %q: %+v", got, a.Status.Refused)
		}
		if _, ok := manager("rds", "team-a"); ok {
			t.Fatal("team-a's rds manager still runs beside one that watches every namespace")
		}
		if _, ok := manager("scheduler", "team-a"); !ok {
			t.Fatal("team-a's scheduler manager went with rds")
		}
		// ACK's controller narrowed to another namespace: team-a's back.
		if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
			t.Fatal(err)
		}
		for i, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "ACK_WATCH_NAMESPACE" {
				d.Spec.Template.Spec.Containers[0].Env[i].Value = "legacy"
			}
		}
		if err := c.Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		a = pass(t, "team-a")
		if got := refusal(a, "rds"); got != "" {
			t.Fatalf("rds still refused: %+v", a.Status.Refused)
		}
		if _, ok := manager("rds", "team-a"); !ok {
			t.Fatal("no rds manager for team-a once ACK's controller watches only legacy")
		}
	})

	t.Run("an unknown kind leaves the manager alone", func(t *testing.T) {
		var a corev1alpha1.ACKContext
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-b", Name: corev1alpha1.ContextName}, &a); err != nil {
			t.Fatal(err)
		}
		a.Spec.Controllers[0].Kinds = []string{"ScheduleGroup", "NoSuchKind"}
		if err := c.Update(ctx, &a); err != nil {
			t.Fatal(err)
		}
		got := pass(t, "team-b")
		if refusal(got, "scheduler") != ReasonUnknownKinds {
			t.Fatalf("refused: %+v", got.Status.Refused)
		}
		if _, ok := manager("scheduler", "team-b"); !ok {
			t.Fatal("team-b's manager removed over a typo")
		}
	})

	t.Run("a controller taken out, then the context deleted", func(t *testing.T) {
		var a corev1alpha1.ACKContext
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: corev1alpha1.ContextName}, &a); err != nil {
			t.Fatal(err)
		}
		a.Spec.Controllers = []corev1alpha1.ACKController{sched}
		if err := c.Update(ctx, &a); err != nil {
			t.Fatal(err)
		}
		pass(t, "team-a")
		if _, ok := manager("rds", "team-a"); ok {
			t.Fatal("rds manager left after rds was taken out")
		}
		var rb rbacv1.RoleBinding
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: environmentBinding("rds")}, &rb); !apierrors.IsNotFound(err) {
			t.Fatalf("rds RoleBinding left in team-a: %v", err)
		}
		if err := c.Delete(ctx, &a); err != nil {
			t.Fatal(err)
		}
		pass(t, "team-a")
		if _, ok := manager("scheduler", "team-a"); ok {
			t.Fatal("scheduler manager left after the context was deleted")
		}
		var sa corev1.ServiceAccount
		if err := c.Get(ctx, types.NamespacedName{Namespace: sysNS, Name: corev1alpha1.ManagerName("scheduler", "team-a")}, &sa); !apierrors.IsNotFound(err) {
			t.Fatalf("scheduler ServiceAccount left: %v", err)
		}
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: corev1alpha1.ContextName}, &a); !apierrors.IsNotFound(err) {
			t.Fatalf("the context is still there (finalizer not removed): %v", err)
		}
		if _, ok := manager("scheduler", "team-b"); !ok {
			t.Fatal("team-b's manager went with team-a's context")
		}
	})
}
