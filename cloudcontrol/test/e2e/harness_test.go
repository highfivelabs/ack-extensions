// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package e2e runs the generic Cloud Control manager the way it runs in a
// cluster: envtest's real kube-apiserver with the generated CRDs and ACK's
// core CRDs; the manager's real binary started as the operator starts it
// for a CloudControlContext (pkg/operator: the arguments, watching only the
// environment's namespace), authenticated as the environment's
// ServiceAccount and holding only the rights the operator's RoleBinding
// gives it; and the fake Cloud Control (cloudcontrol/test/fake) doing the
// work in moto. Nothing here may reach AWS (fake/guard.go says how that is
// enforced).
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	ccv1alpha1 "github.com/highfivelabs/ack-extensions/cloudcontrol/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/fake"
	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	"github.com/highfivelabs/ack-extensions/pkg/operator"
)

const region = "us-west-2"

// envNS is the environment the manager serves; otherNS is a namespace it
// must leave alone.
const (
	envNS   = "team-a"
	otherNS = "team-b"
)

// callLog is what the tests read of Cloud Control: the fake, or on a live
// run the recorder in front of real Cloud Control (live_test.go).
type callLog interface {
	Calls() []fake.Call
	WriteOnly(typeName, identifier string) map[string]any
}

type env struct {
	c     client.Client
	cc    callLog
	ccURL string
	// moto is the SDK configuration the tests check the services with:
	// moto's, or on a live run the real account's.
	moto    aws.Config
	account string
	live    bool
	logs    *syncBuffer
	ctx     context.Context
	schema  *runtime.Scheme
}

// Target is where the end-to-end tests run, CLOUDCONTROL_E2E_TARGET:
// "fake" (the default, and what task check sets) or "real", which only a
// binary built with -tags live (task test-live, docs/live-runs.md) can
// start; built without it, startReal is nil and "real" fails.
func target(t *testing.T) string {
	switch v := os.Getenv("CLOUDCONTROL_E2E_TARGET"); v {
	case "", "fake":
		return "fake"
	case "real":
		if startReal == nil {
			t.Fatal("CLOUDCONTROL_E2E_TARGET=real: this test binary was built without -tags live; run task test-live (docs/live-runs.md)")
		}
		return "real"
	default:
		t.Fatalf("CLOUDCONTROL_E2E_TARGET=%q: fake or real", v)
		return ""
	}
}

// liveRun is what a live run's starter gives the harness: the recorder in
// front of real Cloud Control (a loopback URL, so the manager still runs
// under pkg/fakeaws), the real account's SDK configuration for the tests'
// own checks, and the manager's extra flags (the run's tag).
type liveRun struct {
	cc        callLog
	url       string
	cfg       aws.Config
	account   string
	extraArgs []string
}

// startReal is set by live_test.go, compiled only with -tags live.
var startReal func(t *testing.T) liveRun

func repoRoot(t *testing.T) string {
	wd, _ := os.Getwd()
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if dir == filepath.Dir(dir) {
			t.Fatalf("no go.mod above %s", wd)
		}
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startMoto(t *testing.T, root string) string {
	if u := os.Getenv("MOTO_URL"); u != "" {
		return u
	}
	server := filepath.Join(root, ".cache", "moto-venv", "bin", "moto_server")
	if _, err := os.Stat(server); err != nil {
		t.Fatalf("%s is missing; run hack/moto.sh (or set MOTO_URL)", server)
	}
	port := freePort(t)
	var out syncBuffer
	cmd := exec.Command(server, "-H", "127.0.0.1", "-p", fmt.Sprint(port))
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = fakeaws.Isolated("PATH=" + os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	hc := fake.HTTPClient()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if resp, err := hc.Get(url + "/moto-api/"); err == nil {
			resp.Body.Close()
			return url
		}
	}
	t.Fatalf("moto did not start on %s:\n%s", url, out.String())
	return ""
}

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func buildManager(t *testing.T, root string) string {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cloudcontrol-manager")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "manager")
		cmd := exec.Command("go", "build", "-o", binPath, "./cloudcontrol/cmd/manager")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

func writeKubeconfig(t *testing.T, cfg *rest.Config) string {
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func moduleDir(t *testing.T, root, module string) string {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m %s: %v", module, err)
	}
	return strings.TrimSpace(string(out))
}

// startFake brings up moto and the fake Cloud Control over it: the fake's
// URL is the manager's one endpoint.
func startFake(t *testing.T, root string) (*fake.Server, string, aws.Config) {
	t.Helper()
	motoURL := startMoto(t, root)
	if err := fake.LocalOnly(motoURL); err != nil {
		t.Fatal(err)
	}
	motoCfg, err := fake.AWSConfig(motoURL, region)
	if err != nil {
		t.Fatal(err)
	}

	cc := fake.New()
	for _, ty := range registry.Types {
		var b fake.Backend
		switch ty.TypeName {
		case "AWS::IAM::SAMLProvider":
			b = fake.NewSAMLProvider(motoCfg)
		case "AWS::EC2::TransitGatewayRouteTable":
			b = fake.NewTransitGatewayRouteTable(motoCfg)
		case "AWS::GuardDuty::Detector":
			b = fake.NewDetector(motoCfg)
		case "AWS::SSO::PermissionSet":
			b = fake.NewPermissionSet(motoCfg)
		case "AWS::SSO::Assignment":
			b = fake.NewAssignment(motoCfg)
		case "AWS::EC2::TransitGatewayRoute":
			b = fake.NewTransitGatewayRoute(motoCfg)
		case "AWS::EC2::TransitGatewayRouteTableAssociation":
			b = fake.NewTransitGatewayRouteTableAssociation(motoCfg)
		case "AWS::EC2::TransitGatewayRouteTablePropagation":
			b = fake.NewTransitGatewayRouteTablePropagation(motoCfg)
		case "AWS::EC2::TransitGatewayPeeringAttachment":
			b = fake.NewTransitGatewayPeeringAttachment(motoCfg)
		case "AWS::Route53::QueryLoggingConfig":
			b = fake.NewQueryLoggingConfig(motoCfg)
		case "AWS::Route53Resolver::ResolverDNSSECConfig":
			b = fake.NewResolverDNSSECConfig(motoCfg)
		default:
			if ty.Unproven != "" {
				// Marked in types.yaml: moto lacks the API.
				continue
			}
			t.Fatalf("no fake backend for %s (or mark it unproven in cloudcontrol/types.yaml)", ty.TypeName)
		}
		cc.Register(ty.Schema(), b)
	}
	// The fake passes everything that is not Cloud Control (STS) to moto,
	// so its URL is the manager's one endpoint.
	if err := cc.Upstream(motoURL); err != nil {
		t.Fatal(err)
	}
	ccURL := cc.Start()
	t.Cleanup(cc.Close)

	return cc, ccURL, motoCfg
}

// start brings up moto, the fake Cloud Control over it, envtest and the
// manager. It refuses to start unless both endpoints are local.
func start(t *testing.T) *env {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run through task cloudcontrol:test")
	}
	root := repoRoot(t)
	var (
		cc        callLog
		ccURL     string
		motoCfg   aws.Config
		account   = "123456789012" // moto's
		extraArgs []string
	)
	live := target(t) == "real"
	if live {
		r := startReal(t)
		cc, ccURL, motoCfg, account, extraArgs = r.cc, r.url, r.cfg, r.account, r.extraArgs
	} else {
		cc, ccURL, motoCfg = startFake(t, root)
	}
	endpoint, err := fakeaws.Parse(ccURL, fakeaws.Loopback)
	if err != nil {
		t.Fatal(err)
	}

	crdDir := filepath.Join(root, "cloudcontrol", "config", "crds")
	files, _ := filepath.Glob(filepath.Join(crdDir, "*_*.yaml"))
	paths := append(files, filepath.Join(moduleDir(t, root, "github.com/aws-controllers-k8s/runtime"), "config", "crd", "bases"))
	te := &envtest.Environment{CRDDirectoryPaths: paths, ErrorIfCRDPathMissing: true}
	cfg, err := te.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() { _ = te.Stop() })
	sch := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(sch)
	c, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		t.Fatal(err)
	}

	// The environment, as the operator makes it for a CloudControlContext:
	// the ServiceAccount and the RoleBinding to the generated ClusterRole.
	ctx := context.Background()
	for _, n := range []string{envNS, otherNS} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}); err != nil {
			t.Fatal(err)
		}
	}
	roleYAML, err := os.ReadFile(filepath.Join(root, "cloudcontrol", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(roleYAML, &role); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &role); err != nil {
		t.Fatal(err)
	}
	cctx := &ccv1alpha1.CloudControlContext{
		ObjectMeta: metav1.ObjectMeta{Name: ccv1alpha1.ContextName, Namespace: envNS, UID: "00000000-0000-0000-0000-000000000001"},
		Spec:       ccv1alpha1.CloudControlContextSpec{RoleARN: "arn:aws:iam::" + account + ":role/team-a", Region: region},
	}
	var kinds []string
	for _, ty := range registry.Types {
		kinds = append(kinds, ty.Ref())
	}
	op := &operator.ContextReconciler{Image: "unused"}
	var managerArgs []string
	for _, o := range op.PlanContext(cctx, kinds) {
		switch o := o.(type) {
		case *rbacv1.RoleBinding:
			o.OwnerReferences = nil
			if err := c.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
		case *appsv1.Deployment:
			managerArgs = o.Spec.Template.Spec.Containers[0].Args
		}
	}
	// The manager authenticates as the environment's ServiceAccount in the
	// operator's namespace would.
	sys := ccv1alpha1.DefaultSystemNamespace
	user, err := te.AddUser(envtest.User{
		Name:   "system:serviceaccount:" + sys + ":" + ccv1alpha1.ManagerName(envNS),
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + sys, "system:authenticated"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	bin := buildManager(t, root)
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(ctx)
	// The operator's arguments, then the test's: local ports (a later flag
	// wins); fakeaws.Command adds the one endpoint's flags and environment
	// and refuses to start anything that could reach real AWS.
	args := append(append([]string{}, managerArgs...),
		"--log-level", "debug",
		"--enable-development-logging",
		"--deletion-policy", "delete",
		"--reconcile-default-resync-seconds", "5",
		"--metrics-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--healthz-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
	)
	args = append(args, extraArgs...)
	cmd, err := fakeaws.Command(ctx, endpoint, bin, args,
		"KUBECONFIG="+writeKubeconfig(t, user.Config()),
		"HOME="+t.TempDir(),
		"PATH="+os.Getenv("PATH"),
		"ACK_SYSTEM_NAMESPACE="+envNS,
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("manager output:\n%s", logs.String())
			for _, c := range cc.Calls() {
				t.Logf("cloud control call: %s %s %s %s", c.Action, c.TypeName, c.Identifier, c.Body)
			}
		}
	})
	return &env{c: c, cc: cc, ccURL: ccURL, moto: motoCfg, account: account, live: live, logs: logs, ctx: context.Background(), schema: sch}
}

// ---- object helpers ----

func gvk(kind string) schema.GroupVersionKind {
	for _, t := range registry.Types {
		if t.Kind == kind {
			return t.GVK()
		}
	}
	panic("no kind " + kind)
}

func obj(kind, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(gvk(kind))
	u.SetNamespace(envNS)
	u.SetName(name)
	return u
}

func (e *env) get(t *testing.T, u *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	out := &unstructured.Unstructured{}
	out.SetGroupVersionKind(u.GroupVersionKind())
	if err := e.c.Get(e.ctx, client.ObjectKeyFromObject(u), out); err != nil {
		t.Fatalf("get %s: %v", u.GetName(), err)
	}
	return out
}

func eventually(t *testing.T, timeout time.Duration, what string, f func() error) {
	t.Helper()
	var last error
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if last = f(); last == nil {
			return
		}
	}
	t.Fatalf("%s: not within %s: %v", what, timeout, last)
}

func condition(u *unstructured.Unstructured, typ ackv1alpha1.ConditionType) (string, string) {
	cs, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range cs {
		m, _ := c.(map[string]any)
		if m["type"] == string(typ) {
			s, _ := m["status"].(string)
			msg, _ := m["message"].(string)
			return s, msg
		}
	}
	return "", ""
}

func (e *env) waitSynced(t *testing.T, u *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	var got *unstructured.Unstructured
	eventually(t, 90*time.Second, u.GetName()+" synced and ready", func() error {
		got = e.get(t, u)
		if s, msg := condition(got, ackv1alpha1.ConditionTypeTerminal); s == "True" {
			t.Fatalf("%s is terminal: %s", u.GetName(), msg)
		}
		s, msg := condition(got, ackv1alpha1.ConditionTypeResourceSynced)
		r, _ := condition(got, ackv1alpha1.ConditionTypeReady)
		if s != "True" || r != "True" {
			return fmt.Errorf("synced=%q ready=%q: %s", s, r, msg)
		}
		return nil
	})
	return got
}

// update reads the object, changes it and writes it, again on a conflict
// (the manager writes the same object).
func (e *env) update(t *testing.T, u *unstructured.Unstructured, change func(*unstructured.Unstructured)) error {
	t.Helper()
	var err error
	for i := 0; i < 20; i++ {
		cur := e.get(t, u)
		change(cur)
		if err = e.c.Update(e.ctx, cur); err == nil || !apierrors.IsConflict(err) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

func (e *env) waitGone(t *testing.T, u *unstructured.Unstructured) {
	t.Helper()
	eventually(t, 90*time.Second, u.GetName()+" deleted", func() error {
		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(u.GroupVersionKind())
		err := e.c.Get(e.ctx, client.ObjectKeyFromObject(u), cur)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("still there (err %v)", err)
	})
}

func (e *env) calls(action, typeName string) []fake.Call {
	var out []fake.Call
	for _, c := range e.cc.Calls() {
		if c.Action == action && (typeName == "" || c.TypeName == typeName) {
			out = append(out, c)
		}
	}
	return out
}

func secret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: envNS}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}
