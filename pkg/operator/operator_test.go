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
		t.Skip("KUBEBUILDER_ASSETS is not set; run through make test")
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
