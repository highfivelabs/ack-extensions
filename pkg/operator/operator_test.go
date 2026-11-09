// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const ns = "ack-system"
const roleARN = "arn:aws:iam::111122223333:role/ack-rds-controller"

// ackArgs are the arguments ACK's Helm chart gives a controller
// (code-generator templates/helm/templates/deployment.yaml).
var ackArgs = []string{
	"--aws-region", "$(AWS_REGION)",
	"--aws-endpoint-url", "$(AWS_ENDPOINT_URL)",
	"--log-level", "$(ACK_LOG_LEVEL)",
	"--resource-tags", "$(ACK_RESOURCE_TAGS)",
	"--watch-namespace", "$(ACK_WATCH_NAMESPACE)",
	"--watch-selectors", "$(ACK_WATCH_SELECTORS)",
	"--reconcile-resources", "$(RECONCILE_RESOURCES)",
	"--deletion-policy", "$(DELETION_POLICY)",
	"--reconcile-resource-resync-seconds", "$(RECONCILE_RESOURCE_RESYNC_SECONDS_DBINSTANCE)",
	"--feature-gates", "$(FEATURE_GATES)",
	"--enable-carm=true",
	"--enable-cross-namespace=false",
}

func ackController(service string) (*appsv1.Deployment, *corev1.ServiceAccount) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: "ack-" + service + "-controller", Namespace: ns,
		Annotations: map[string]string{
			"eks.amazonaws.com/role-arn":               roleARN,
			"eks.amazonaws.com/sts-regional-endpoints": "true",
			"meta.helm.sh/release-name":                "ack-" + service,
		},
	}}
	sel := map[string]string{"app.kubernetes.io/name": service + "-chart"}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "ack-" + service + "-controller-" + service + "-chart", Namespace: ns, Labels: sel},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sel},
				Spec: corev1.PodSpec{
					ServiceAccountName: sa.Name,
					Containers: []corev1.Container{{
						Name: "controller", Image: "public.ecr.aws/aws-controllers-k8s/" + service + "-controller:1.0.0",
						Args: ackArgs,
						Env: []corev1.EnvVar{
							{Name: "ACK_SYSTEM_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
							{Name: "AWS_REGION", Value: "eu-west-1"},
							{Name: "ACK_WATCH_NAMESPACE", Value: ""},
							{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/var/run/secrets/aws/credentials"},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "aws-credentials", MountPath: "/var/run/secrets/aws", ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{
						{Name: "aws-credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "aws-creds"}}},
						{Name: "unused", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
	return d, sa
}

func TestManagerArgs(t *testing.T) {
	got := ManagerArgs(append(append([]string{}, ackArgs...), "--reconcile-resources=DBInstance"))
	for _, a := range got {
		if strings.Contains(a, "RECONCILE_RESOURCE") || strings.HasPrefix(a, "--reconcile-resource") {
			t.Fatalf("kept %q: %v", a, got)
		}
	}
	want := []string{"--aws-region", "$(AWS_REGION)", "--aws-endpoint-url", "$(AWS_ENDPOINT_URL)", "--log-level", "$(ACK_LOG_LEVEL)",
		"--resource-tags", "$(ACK_RESOURCE_TAGS)", "--watch-namespace", "$(ACK_WATCH_NAMESPACE)", "--watch-selectors", "$(ACK_WATCH_SELECTORS)",
		"--deletion-policy", "$(DELETION_POLICY)", "--feature-gates", "$(FEATURE_GATES)", "--enable-carm=true", "--enable-cross-namespace=false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func startEnv(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run through task test")
	}
	te := &envtest.Environment{}
	cfg, err := te.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = te.Stop() })
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReconcile(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	r := &Reconciler{Client: c, Options: Options{
		Images:   map[string]string{"rds": "example.test/rds-manager:dev", "iam": "example.test/iam-manager:dev"},
		Identity: IdentityOwn,
	}}
	pass := func(t *testing.T) {
		t.Helper()
		if _, err := r.Reconcile(ctx, allKey); err != nil {
			t.Fatal(err)
		}
	}
	rdsDep, rdsSA := ackController("rds")
	s3Dep, s3SA := ackController("s3")

	t.Run("nothing before ACK's ServiceAccount exists", func(t *testing.T) {
		if err := c.Create(ctx, rdsDep); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, s3Dep); err != nil {
			t.Fatal(err)
		}
		pass(t)
		var deps appsv1.DeploymentList
		if err := c.List(ctx, &deps, client.MatchingLabels{LabelManagedBy: managedBy}); err != nil {
			t.Fatal(err)
		}
		if len(deps.Items) != 0 {
			t.Fatalf("made %d managers before ACK's ServiceAccount exists", len(deps.Items))
		}
	})

	t.Run("own identity: our ServiceAccount carries ACK's role", func(t *testing.T) {
		if err := c.Create(ctx, rdsSA); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, s3SA); err != nil {
			t.Fatal(err)
		}
		pass(t)
		var sa corev1.ServiceAccount
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-rds-manager"}, &sa); err != nil {
			t.Fatal(err)
		}
		if sa.Annotations["eks.amazonaws.com/role-arn"] != roleARN || sa.Annotations["eks.amazonaws.com/sts-regional-endpoints"] != "true" {
			t.Fatalf("annotations %v", sa.Annotations)
		}
		if _, ok := sa.Annotations["meta.helm.sh/release-name"]; ok {
			t.Fatalf("copied a non-identity annotation: %v", sa.Annotations)
		}
		var d appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-rds-manager"}, &d); err != nil {
			t.Fatal(err)
		}
		pod := d.Spec.Template.Spec
		if pod.ServiceAccountName != "ack-extensions-rds-manager" {
			t.Fatalf("runs as %s", pod.ServiceAccountName)
		}
		mc := pod.Containers[0]
		if mc.Image != "example.test/rds-manager:dev" {
			t.Fatalf("image %s", mc.Image)
		}
		if !reflect.DeepEqual(mc.Args, ManagerArgs(ackArgs)) {
			t.Fatalf("args %v", mc.Args)
		}
		if !reflect.DeepEqual(mc.Env, rdsDep.Spec.Template.Spec.Containers[0].Env) {
			t.Fatalf("env %v", mc.Env)
		}
		if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "aws-credentials" {
			t.Fatalf("volumes %v", pod.Volumes)
		}
		if len(d.OwnerReferences) != 1 || d.OwnerReferences[0].Name != rdsDep.Name {
			t.Fatalf("owner %v", d.OwnerReferences)
		}
		var crb rbacv1.ClusterRoleBinding
		if err := c.Get(ctx, types.NamespacedName{Name: "ack-extensions-rds-manager-" + ns}, &crb); err != nil {
			t.Fatal(err)
		}
		if crb.RoleRef.Name != "ack-extensions-rds-manager" || crb.Subjects[0].Name != "ack-extensions-rds-manager" {
			t.Fatalf("binding %v -> %v", crb.Subjects, crb.RoleRef)
		}
		// ACK's s3 controller is not one we extend.
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-s3-manager"}, &appsv1.Deployment{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("s3: %v", err)
		}
	})

	t.Run("ack identity: ACK's ServiceAccount, ours removed", func(t *testing.T) {
		var d appsv1.Deployment
		if err := c.Get(ctx, client.ObjectKeyFromObject(rdsDep), &d); err != nil {
			t.Fatal(err)
		}
		d.Annotations = map[string]string{AnnotationIdentity: IdentityACK}
		if err := c.Update(ctx, &d); err != nil {
			t.Fatal(err)
		}
		pass(t)
		var m appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-rds-manager"}, &m); err != nil {
			t.Fatal(err)
		}
		if m.Spec.Template.Spec.ServiceAccountName != rdsSA.Name {
			t.Fatalf("runs as %s, want ACK's %s", m.Spec.Template.Spec.ServiceAccountName, rdsSA.Name)
		}
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-rds-manager"}, &corev1.ServiceAccount{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("our ServiceAccount should be gone: %v", err)
		}
		var crb rbacv1.ClusterRoleBinding
		if err := c.Get(ctx, types.NamespacedName{Name: "ack-extensions-rds-manager-" + ns}, &crb); err != nil {
			t.Fatal(err)
		}
		if crb.Subjects[0].Name != rdsSA.Name {
			t.Fatalf("binding subject %v", crb.Subjects)
		}
	})

	t.Run("ACK's controller removed takes our manager with it", func(t *testing.T) {
		if err := c.Delete(ctx, rdsDep); err != nil {
			t.Fatal(err)
		}
		pass(t)
		for _, l := range []client.ObjectList{&appsv1.DeploymentList{}, &corev1.ServiceAccountList{}, &rbacv1.RoleList{}, &rbacv1.RoleBindingList{}, &rbacv1.ClusterRoleBindingList{}} {
			if err := c.List(ctx, l, client.MatchingLabels{LabelManagedBy: managedBy}); err != nil {
				t.Fatal(err)
			}
			items, _ := metaItems(l)
			for _, o := range items {
				if o.GetDeletionTimestamp().IsZero() {
					t.Errorf("%T %s/%s left behind", o, o.GetNamespace(), o.GetName())
				}
			}
		}
	})
}

// A service ACK has no controller for runs beside the controller Beside
// names, and only there.
func TestOurServices(t *testing.T) {
	r := &Reconciler{Options: Options{Images: map[string]string{"eventbridge": "e", "scheduler": "s", "rds": "r"}}}
	for ack, want := range map[string][]string{
		"eventbridge": {"eventbridge", "scheduler"},
		"rds":         {"rds"},
		"s3":          nil,
		// Should ACK ship a Scheduler controller, the manager runs beside
		// it as for any other service.
		"scheduler": {"scheduler"},
	} {
		if got := r.ourServices(ack); !reflect.DeepEqual(got, want) {
			t.Errorf("beside ACK's %s: %v, want %v", ack, got, want)
		}
	}
	only := &Reconciler{Options: Options{Images: map[string]string{"scheduler": "s"}}}
	if got := only.ourServices("eventbridge"); !reflect.DeepEqual(got, []string{"scheduler"}) {
		t.Errorf("scheduler alone beside ACK's eventbridge: %v", got)
	}
}

// Beside ACK's EventBridge controller the operator runs the scheduler
// manager with that controller's identity.
func TestReconcileBeside(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	r := &Reconciler{Client: c, Options: Options{
		Images:   map[string]string{"eventbridge": "example.test/eventbridge-manager:dev", "scheduler": "example.test/scheduler-manager:dev"},
		Identity: IdentityOwn,
	}}
	dep, sa := ackController("eventbridge")
	if err := c.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, allKey); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"eventbridge", "scheduler"} {
		var d appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-" + svc + "-manager"}, &d); err != nil {
			t.Fatalf("%s manager: %v", svc, err)
		}
		if img := d.Spec.Template.Spec.Containers[0].Image; img != "example.test/"+svc+"-manager:dev" {
			t.Fatalf("%s manager image %s", svc, img)
		}
		if len(d.OwnerReferences) != 1 || d.OwnerReferences[0].Name != dep.Name {
			t.Fatalf("%s manager owner %v", svc, d.OwnerReferences)
		}
		var own corev1.ServiceAccount
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "ack-extensions-" + svc + "-manager"}, &own); err != nil {
			t.Fatal(err)
		}
		if own.Annotations["eks.amazonaws.com/role-arn"] != roleARN {
			t.Fatalf("%s manager's role %v, want ACK EventBridge's", svc, own.Annotations)
		}
	}
	if err := c.Delete(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, allKey); err != nil {
		t.Fatal(err)
	}
	var left appsv1.DeploymentList
	if err := c.List(ctx, &left, client.MatchingLabels{LabelManagedBy: managedBy}); err != nil {
		t.Fatal(err)
	}
	if len(left.Items) != 0 {
		t.Fatalf("%d managers left after ACK's EventBridge controller went", len(left.Items))
	}
}

func TestAccountLevelBeside(t *testing.T) {
	for _, c := range []struct {
		service string
		in      map[string]bool
		want    []string
	}{
		{"organizations", map[string]bool{"organizations": true}, nil},
		{"organizations", map[string]bool{"organizations": true, "rds": true, "s3": true}, []string{"rds", "s3"}},
		// An account-level controller beside the organizations-controller is
		// not itself refused.
		{"rds", map[string]bool{"organizations": true, "rds": true}, nil},
	} {
		if got := AccountLevelBeside(c.service, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s among %v: %v, want %v", c.service, c.in, got, c.want)
		}
	}
}

// An organization-scoped ACK controller gets our manager only while it is
// the only ACK controller in its namespace (docs/plan.md, section 7.5).
func TestReconcileOrganizationScoped(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	const orgNS = "ack-organizations-system"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: orgNS}}); err != nil {
		t.Fatal(err)
	}
	rec := record.NewFakeRecorder(100)
	r := &Reconciler{Client: c, Recorder: rec, Options: Options{
		Images:   map[string]string{"organizations": "example.test/organizations-manager:dev", "rds": "example.test/rds-manager:dev"},
		Identity: IdentityOwn,
	}}
	pass := func(t *testing.T) {
		t.Helper()
		if _, err := r.Reconcile(ctx, allKey); err != nil {
			t.Fatal(err)
		}
	}
	in := func(d *appsv1.Deployment, sa *corev1.ServiceAccount) (*appsv1.Deployment, *corev1.ServiceAccount) {
		d.Namespace, sa.Namespace = orgNS, orgNS
		return d, sa
	}
	exists := func(t *testing.T, svc string) bool {
		t.Helper()
		var d appsv1.Deployment
		err := c.Get(ctx, types.NamespacedName{Namespace: orgNS, Name: "ack-extensions-" + svc + "-manager"}, &d)
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return d.DeletionTimestamp.IsZero()
	}
	drain := func() []string {
		var out []string
		for {
			select {
			case e := <-rec.Events:
				out = append(out, e)
			default:
				return out
			}
		}
	}
	orgDep, orgSA := in(ackController("organizations"))
	rdsDep, rdsSA := in(ackController("rds"))
	s3Dep, s3SA := in(ackController("s3"))
	for _, o := range []client.Object{orgSA, orgDep} {
		if err := c.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("alone in its namespace: our manager runs", func(t *testing.T) {
		pass(t)
		if !exists(t, "organizations") {
			t.Fatal("no organizations manager")
		}
		if ev := drain(); len(ev) != 0 {
			t.Fatalf("events %v", ev)
		}
	})

	t.Run("beside an account-level controller: refused, and ours removed", func(t *testing.T) {
		for _, o := range []client.Object{rdsSA, rdsDep} {
			if err := c.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		pass(t)
		if exists(t, "organizations") {
			t.Fatal("the organizations manager still runs beside ACK's rds controller")
		}
		var sa corev1.ServiceAccount
		if err := c.Get(ctx, types.NamespacedName{Namespace: orgNS, Name: "ack-extensions-organizations-manager"}, &sa); !apierrors.IsNotFound(err) && sa.DeletionTimestamp.IsZero() {
			t.Fatalf("our organizations ServiceAccount is left: %v", err)
		}
		if !exists(t, "rds") {
			t.Fatal("the account-level rds manager should still run")
		}
		ev := drain()
		if len(ev) != 1 || !strings.HasPrefix(ev[0], "Warning "+ReasonSharedNamespace+" ") ||
			!strings.Contains(ev[0], "account-level rds controller") || !strings.Contains(ev[0], "section 7.5") {
			t.Fatalf("events %v", ev)
		}
	})

	t.Run("an ACK controller we do not extend counts too", func(t *testing.T) {
		if err := c.Delete(ctx, rdsDep); err != nil {
			t.Fatal(err)
		}
		for _, o := range []client.Object{s3SA, s3Dep} {
			if err := c.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		pass(t)
		if exists(t, "organizations") {
			t.Fatal("the organizations manager runs beside ACK's s3 controller")
		}
		if ev := drain(); len(ev) != 1 || !strings.Contains(ev[0], "account-level s3 controller") {
			t.Fatalf("events %v", ev)
		}
	})

	t.Run("alone again: our manager is back", func(t *testing.T) {
		if err := c.Delete(ctx, s3Dep); err != nil {
			t.Fatal(err)
		}
		pass(t)
		if !exists(t, "organizations") {
			t.Fatal("no organizations manager after the account-level controllers left")
		}
		if ev := drain(); len(ev) != 0 {
			t.Fatalf("events %v", ev)
		}
	})
}
