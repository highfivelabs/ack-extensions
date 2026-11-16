// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/highfivelabs/ack-extensions/pkg/ackiam"
)

// moduleDir is a Go module's directory in the module cache.
func moduleDir(t *testing.T, module string) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	if err != nil {
		t.Fatalf("go list -m %s: %v", module, err)
	}
	return strings.TrimSpace(string(out))
}

func readClusterRole(t *testing.T, path string) *rbacv1.ClusterRole {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &role); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &role
}

// startACKControllersEnv is startACKEnv plus ACK's rds CRDs (its chart's
// crds/ at the pinned version, as ACK's chart installs them) and ACK's
// rds-controller ClusterRole as config/ack-controllers installs it.
func startACKControllersEnv(t *testing.T) (client.Client, discovery.DiscoveryInterface) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run through task test")
	}
	root := repoRoot(t)
	te := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(root, "config/context"), filepath.Join(root, "config/crds/bases"),
			filepath.Join(moduleDir(t, "github.com/aws-controllers-k8s/rds-controller"), "helm/crds"),
		},
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
	for _, p := range []string{"config/rbac/rds-manager-role.yaml", "config/rbac/kms-manager-role.yaml", "config/rbac/scheduler-manager-role.yaml", "config/ack-controllers/rds-controller-role.yaml"} {
		if err := c.Create(ctx, readClusterRole(t, filepath.Join(root, p))); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"team-a", "team-b", "team-c", sysNS, ns} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}); err != nil {
			t.Fatal(err)
		}
	}
	return c, dc
}

func TestACKControllers(t *testing.T) {
	c, dc := startACKControllersEnv(t)
	ctx := context.Background()
	fake, err := NewFakeAWS("http://moto.fake-aws.svc.cluster.local:5000", "10.96.0.1")
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]string{"rds": "example.test/rds-manager:dev", "kms": "example.test/kms-manager:dev", "scheduler": "example.test/scheduler-manager:dev"}
	ackImages := map[string]string{
		"rds": "public.ecr.aws/aws-controllers-k8s/rds-controller:1.14.2@sha256:d28c83ea3b7cb941d82b97a5a7ff87501dc9185773e84f2c3327eb4d61aea789",
		// No kms CRDs of ACK's are installed: kms runs ours alone.
		"kms": "public.ecr.aws/aws-controllers-k8s/kms-controller:1.7.0",
	}
	r := &ACKContextReconciler{
		Client: c, Namespace: sysNS, Images: images, ACKImages: ackImages,
		Kinds: DiscoveryKinds(dc), ACKKinds: DiscoveryACKKinds(dc), FakeAWS: fake,
	}
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
	dep := func(name string) (*appsv1.Deployment, bool) {
		var d appsv1.Deployment
		err := c.Get(ctx, types.NamespacedName{Namespace: sysNS, Name: name}, &d)
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return &d, true
	}
	reconciles := func(d *appsv1.Deployment) string {
		for _, a := range d.Spec.Template.Spec.Containers[0].Args {
			if v, ok := strings.CutPrefix(a, "--reconcile-resources="); ok {
				return v
			}
		}
		return ""
	}
	setKinds := func(t *testing.T, env string, ctls ...corev1alpha1.ACKController) {
		t.Helper()
		var a corev1alpha1.ACKContext
		if err := c.Get(ctx, types.NamespacedName{Namespace: env, Name: corev1alpha1.ContextName}, &a); err != nil {
			t.Fatal(err)
		}
		a.Spec.Controllers = ctls
		if err := c.Update(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	ackA, oursA := corev1alpha1.ACKControllerName("rds", "team-a"), corev1alpha1.ManagerName("rds", "team-a")

	if err := c.Create(ctx, ackContext("team-a", corev1alpha1.ACKController{Service: "rds"}, corev1alpha1.ACKController{Service: "kms"}, corev1alpha1.ACKController{Service: "scheduler"})); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, ackContext("team-b", corev1alpha1.ACKController{Service: "rds", Kinds: []string{"DBParameterGroup"}})); err != nil {
		t.Fatal(err)
	}

	t.Run("ACK's own controller beside ours, per environment", func(t *testing.T) {
		a := pass(t, "team-a")
		d, ok := dep(ackA)
		if !ok {
			t.Fatalf("no %s", ackA)
		}
		spec := d.Spec.Template.Spec
		ctr := spec.Containers[0]
		if spec.ServiceAccountName != ackA || ctr.Image != ackImages["rds"] || !slices.Equal(ctr.Command, []string{"./bin/controller"}) {
			t.Fatalf("%s runs as %s, image %s, command %v", ackA, spec.ServiceAccountName, ctr.Image, ctr.Command)
		}
		for _, want := range []string{"--watch-namespace=team-a", "--enable-carm=false", "--aws-region=us-west-2", "--leader-election-namespace=team-a"} {
			if !slices.Contains(ctr.Args, want) {
				t.Fatalf("%s args lack %s: %v", ackA, want, ctr.Args)
			}
		}
		if got := reconciles(d); got != "" {
			t.Fatalf("%s names kinds although the context does not: %s", ackA, got)
		}
		if err := fake.Verify(&ctr); err != nil {
			t.Fatalf("%s could reach real AWS: %v", ackA, err)
		}
		// Neither Deployment's selector matches the other's pods.
		ours, ok := dep(oursA)
		if !ok {
			t.Fatalf("no %s", oursA)
		}
		for _, p := range []struct{ sel, pod map[string]string }{
			{ours.Spec.Selector.MatchLabels, d.Spec.Template.Labels}, {d.Spec.Selector.MatchLabels, ours.Spec.Template.Labels},
		} {
			match := true
			for k, v := range p.sel {
				if p.pod[k] != v {
					match = false
				}
			}
			if match {
				t.Fatalf("selector %v matches the other controller's pods %v", p.sel, p.pod)
			}
		}
		var rb rbacv1.RoleBinding
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: ackEnvironmentBinding("rds")}, &rb); err != nil {
			t.Fatal(err)
		}
		if rb.RoleRef.Name != ACKControllerRole("rds") || rb.Subjects[0].Name != ackA || rb.Subjects[0].Namespace != sysNS {
			t.Fatalf("binding: %+v", rb)
		}
		var sa corev1.ServiceAccount
		if err := c.Get(ctx, types.NamespacedName{Namespace: sysNS, Name: ackA}, &sa); err != nil {
			t.Fatal(err)
		}
		for k := range sa.Annotations {
			if strings.HasPrefix(k, "eks.amazonaws.com/") {
				t.Fatalf("an IRSA annotation on %s: %s", ackA, k)
			}
		}
		// kms: ACK's image but none of ACK's kms kinds installed, so ours
		// alone; scheduler: ACK has no controller.
		for _, n := range []string{corev1alpha1.ACKControllerName("kms", "team-a"), corev1alpha1.ACKControllerName("scheduler", "team-a")} {
			if _, ok := dep(n); ok {
				t.Fatalf("%s runs", n)
			}
		}
		for _, n := range []string{corev1alpha1.ManagerName("kms", "team-a"), corev1alpha1.ManagerName("scheduler", "team-a")} {
			if _, ok := dep(n); !ok {
				t.Fatalf("no %s", n)
			}
		}
		var got []string
		for _, m := range a.Status.Managers {
			got = append(got, m.Service+"/"+m.Controller+"="+m.ServiceAccount)
		}
		want := []string{
			"kms/ack-extensions=" + sysNS + "/" + corev1alpha1.ManagerName("kms", "team-a"),
			"rds/ack-extensions=" + sysNS + "/" + oursA,
			"rds/ack=" + sysNS + "/" + ackA,
			"scheduler/ack-extensions=" + sysNS + "/" + corev1alpha1.ManagerName("scheduler", "team-a"),
		}
		if !slices.Equal(got, want) {
			t.Fatalf("status.managers:\n got %v\nwant %v", got, want)
		}
		if len(a.Status.PodIdentityAssociations) != 4 || len(a.Status.Refused) != 0 {
			t.Fatalf("associations %+v, refused %+v", a.Status.PodIdentityAssociations, a.Status.Refused)
		}
	})

	t.Run("ACK's controller's rights in the environment only", func(t *testing.T) {
		if !can(t, c, ackA, "create", "rds.services.k8s.aws", "dbinstances", "team-a") {
			t.Fatal("team-a's ACK rds-controller cannot create DBInstances in team-a")
		}
		if !can(t, c, ackA, "create", "coordination.k8s.io", "leases", "team-a") {
			t.Fatal("team-a's ACK rds-controller cannot take its lease in team-a")
		}
		for _, n := range []string{"team-b", sysNS} {
			if can(t, c, ackA, "create", "rds.services.k8s.aws", "dbinstances", n) || can(t, c, ackA, "get", "", "secrets", n) {
				t.Fatalf("team-a's ACK rds-controller has rights in %s", n)
			}
		}
		if can(t, c, ackA, "list", "", "namespaces", "") {
			t.Fatal("team-a's ACK rds-controller may list namespaces")
		}
		if can(t, c, ackA, "create", "rds.aws.hfl.dev", "optiongroups", "team-a") {
			t.Fatal("ACK's controller may create our OptionGroups")
		}
	})

	t.Run("kinds choose the controllers", func(t *testing.T) {
		pass(t, "team-b")
		ackB, oursB := corev1alpha1.ACKControllerName("rds", "team-b"), corev1alpha1.ManagerName("rds", "team-b")
		check := func(t *testing.T, wantACK, wantOurs string) {
			t.Helper()
			for _, w := range []struct{ name, want string }{{ackB, wantACK}, {oursB, wantOurs}} {
				d, ok := dep(w.name)
				switch {
				case w.want == "" && ok:
					t.Fatalf("%s runs (%s)", w.name, reconciles(d))
				case w.want != "" && !ok:
					t.Fatalf("no %s", w.name)
				case ok && reconciles(d) != w.want:
					t.Fatalf("%s reconciles %q, want %q", w.name, reconciles(d), w.want)
				}
			}
		}
		check(t, "DBParameterGroup", "")
		setKinds(t, "team-b", corev1alpha1.ACKController{Service: "rds", Kinds: []string{"OptionGroup"}})
		pass(t, "team-b")
		check(t, "", "OptionGroup")
		var rb rbacv1.RoleBinding
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-b", Name: ackEnvironmentBinding("rds")}, &rb); !apierrors.IsNotFound(err) {
			t.Fatalf("ACK's binding left in team-b: %v", err)
		}
		setKinds(t, "team-b", corev1alpha1.ACKController{Service: "rds", Kinds: []string{"OptionGroup", "DBParameterGroup"}})
		b := pass(t, "team-b")
		check(t, "DBParameterGroup", "OptionGroup")
		setKinds(t, "team-b", corev1alpha1.ACKController{Service: "rds", Kinds: []string{"OptionGroup", "DBParameterGroup", "NoSuchKind"}})
		b = pass(t, "team-b")
		if len(b.Status.Refused) != 1 || b.Status.Refused[0].Reason != ReasonUnknownKinds {
			t.Fatalf("refused: %+v", b.Status.Refused)
		}
		check(t, "DBParameterGroup", "OptionGroup")
	})

	t.Run("refused where ACK's controller installed the classic way watches the namespace", func(t *testing.T) {
		d, sa := ackController("rds")
		if err := c.Create(ctx, sa); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		a := pass(t, "team-a")
		if len(a.Status.Refused) != 1 || a.Status.Refused[0].Service != "rds" || a.Status.Refused[0].Reason != ReasonNamespaceClaimed {
			t.Fatalf("refused: %+v", a.Status.Refused)
		}
		for _, n := range []string{ackA, oursA} {
			if _, ok := dep(n); ok {
				t.Fatalf("%s still runs beside ACK's controller that watches every namespace", n)
			}
		}
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
		if len(a.Status.Refused) != 0 {
			t.Fatalf("still refused: %+v", a.Status.Refused)
		}
		for _, n := range []string{ackA, oursA} {
			if _, ok := dep(n); !ok {
				t.Fatalf("no %s once ACK's classic controller watches only legacy", n)
			}
		}
	})

	t.Run("the context deleted", func(t *testing.T) {
		var a corev1alpha1.ACKContext
		if err := c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: corev1alpha1.ContextName}, &a); err != nil {
			t.Fatal(err)
		}
		if err := c.Delete(ctx, &a); err != nil {
			t.Fatal(err)
		}
		pass(t, "team-a")
		for _, n := range []string{ackA, oursA} {
			if _, ok := dep(n); ok {
				t.Fatalf("%s left after the context was deleted", n)
			}
			var sa corev1.ServiceAccount
			if err := c.Get(ctx, types.NamespacedName{Namespace: sysNS, Name: n}, &sa); !apierrors.IsNotFound(err) {
				t.Fatalf("ServiceAccount %s left: %v", n, err)
			}
		}
		if _, ok := dep(corev1alpha1.ACKControllerName("rds", "team-b")); !ok {
			t.Fatal("team-b's ACK controller went with team-a's context")
		}
	})
}

// TestACKControllerPins: what hack/ack-controllers.sh wrote agrees with
// upstream.yaml's pins, and every ACK controller the operator may run has
// its ClusterRole, the operator's right to bind it and an IAM
// recommendation (docs/plan.md, section 7.7). Offline; task operator-e2e
// checks the digests against the registry.
func TestACKControllerPins(t *testing.T) {
	root := repoRoot(t)
	// upstream.yaml: name -> version.
	pins := map[string]string{}
	f, err := os.Open(filepath.Join(root, "upstream.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cur := ""
	for sc := bufio.NewScanner(f); sc.Scan(); {
		fs := strings.Fields(sc.Text())
		switch {
		case len(fs) >= 3 && fs[0] == "-" && fs[1] == "name:":
			cur = strings.Join(fs[2:], " ")
		case len(fs) == 2 && fs[0] == "version:" && cur != "":
			if _, ok := pins[cur]; !ok {
				pins[cur] = fs[1]
			}
		}
	}
	op, err := os.ReadFile(filepath.Join(root, "config/operator/operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)- name: ACK_IMAGE_([A-Z0-9]+)\n\s+value: public\.ecr\.aws/aws-controllers-k8s/([a-z0-9]+)-controller:([0-9.]+)@sha256:[0-9a-f]{64}\n`)
	images := re.FindAllStringSubmatch(string(op), -1)
	if n := strings.Count(string(op), "- name: ACK_IMAGE_"); n != len(images) {
		t.Fatalf("%d ACK_IMAGE_ entries, %d of them public.ecr.aws/aws-controllers-k8s/<service>-controller:<version>@sha256:<digest>", n, len(images))
	}
	rbac, err := os.ReadFile(filepath.Join(root, "config/operator/rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	kust, err := os.ReadFile(filepath.Join(root, "config/ack-controllers/kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	recs, err := ackiam.ACKServices(root)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, m := range images {
		env, svc, version := strings.ToLower(m[1]), m[2], m[3]
		seen = append(seen, svc)
		if env != svc {
			t.Errorf("ACK_IMAGE_%s names %s's controller", m[1], svc)
		}
		if OrganizationScoped[svc] {
			t.Errorf("%s: organization-scoped, never run per environment", svc)
		}
		if pin := pins[svc+"-controller"]; pin != "v"+version {
			t.Errorf("%s: image %s, upstream.yaml pins %q", svc, version, pin)
		}
		role := readClusterRole(t, filepath.Join(root, "config/ack-controllers", svc+"-controller-role.yaml"))
		if role.Name != ACKControllerRole(svc) {
			t.Errorf("%s: ClusterRole %s, want %s", svc, role.Name, ACKControllerRole(svc))
		}
		b, _ := os.ReadFile(filepath.Join(root, "config/ack-controllers", svc+"-controller-role.yaml"))
		if !strings.Contains(string(b), "aws-controllers-k8s/"+svc+"-controller v"+version+" ") {
			t.Errorf("%s: ClusterRole not copied at v%s", svc, version)
		}
		if !strings.Contains(string(kust), "- "+svc+"-controller-role.yaml") {
			t.Errorf("%s: ClusterRole not in config/ack-controllers/kustomization.yaml", svc)
		}
		if !strings.Contains(string(rbac), `"`+ACKControllerRole(svc)+`"`) {
			t.Errorf("%s: the operator may not bind %s (config/operator/rbac.yaml)", svc, ACKControllerRole(svc))
		}
		if _, ok, err := ackiam.Recommended(root, svc); err != nil || !ok {
			t.Errorf("%s: no IAM recommendation (%v)", svc, err)
		}
		if res, err := ackiam.ACKResources(root, svc); err != nil || len(res) == 0 {
			t.Errorf("%s: no kinds in its ClusterRole (%v)", svc, err)
		}
	}
	slices.Sort(seen)
	if !slices.Equal(seen, recs) {
		t.Errorf("ACK images for %v, IAM recommendations for %v", seen, recs)
	}
	if len(seen) == 0 {
		t.Fatal("no ACK_IMAGE_ entries in config/operator/operator.yaml")
	}
}
