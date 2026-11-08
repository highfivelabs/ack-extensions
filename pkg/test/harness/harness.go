// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package harness runs one service's controller the way it runs in a
// cluster, against a fake AWS, for end-to-end tests:
//
//   - the fake AWS is moto in server mode (Apache-2.0, a Python package
//     pinned in hack/moto-requirements.txt and installed by hack/moto.sh);
//     MOTO_URL points at one already running instead;
//   - the Kubernetes API is envtest's real kube-apiserver and etcd, with
//     our CRDs, ACK's core CRDs (services.k8s.aws) and the CRDs of the ACK
//     controllers our kinds reference, all read from the Go modules at the
//     versions go.mod pins;
//   - the controller is the service's real binary
//     (services/<svc>/cmd/controller), built once and started with the
//     flags ACK's Helm chart passes, pointed at moto with
//     --aws-endpoint-url and fake credentials.
//
// Nothing here talks to real AWS: the process gets only the moto
// endpoint and the credentials "testing"/"testing".
package harness

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Region is the AWS region every test uses.
const Region = "us-west-2"

// Env is a running test environment.
type Env struct {
	Client  client.Client
	MotoURL string
	AWS     aws.Config
	Rest    *rest.Config
	logs    *syncBuffer
}

// RepoRoot is the repository root, found from this package's directory.
func RepoRoot(t testing.TB) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if dir == filepath.Dir(dir) {
			t.Fatalf("no go.mod above %s", wd)
		}
	}
}

func moduleDir(t testing.TB, root, module string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m %s: %v", module, err)
	}
	return strings.TrimSpace(string(out))
}

// Start brings up moto, envtest and the controller of one service
// ("iam", "rds"), and registers their teardown with t.Cleanup.
func Start(t *testing.T, service string, scheme *runtime.Scheme) *Env {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run through make test")
	}
	root := RepoRoot(t)

	motoURL := os.Getenv("MOTO_URL")
	if motoURL == "" {
		motoURL = startMoto(t, root)
	}

	crds := []string{
		filepath.Join(root, "config", "crds", "bases"),
		filepath.Join(moduleDir(t, root, "github.com/aws-controllers-k8s/runtime"), "config", "crd", "bases"),
		filepath.Join(moduleDir(t, root, "github.com/aws-controllers-k8s/iam-controller"), "config", "crd", "bases"),
		filepath.Join(moduleDir(t, root, "github.com/aws-controllers-k8s/rds-controller"), "config", "crd", "bases"),
	}
	te := &envtest.Environment{CRDDirectoryPaths: crds, ErrorIfCRDPathMissing: true}
	cfg, err := te.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() { _ = te.Stop() })

	if scheme == nil {
		scheme = runtime.NewScheme()
	}
	_ = clientgoscheme.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	kubeconfig := writeKubeconfig(t, cfg)
	bin := buildController(t, root, service)
	logs := runController(t, bin, kubeconfig, motoURL)

	awsCfg := aws.Config{
		Region:       Region,
		Credentials:  credentials.NewStaticCredentialsProvider("testing", "testing", ""),
		BaseEndpoint: aws.String(motoURL),
	}
	return &Env{Client: c, MotoURL: motoURL, AWS: awsCfg, Rest: cfg, logs: logs}
}

// Logs is what the controller wrote so far.
func (e *Env) Logs() string {
	e.logs.mu.Lock()
	defer e.logs.mu.Unlock()
	return e.logs.b.String()
}

func freePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startMoto(t *testing.T, root string) string {
	t.Helper()
	server := filepath.Join(root, ".cache", "moto-venv", "bin", "moto_server")
	if _, err := os.Stat(server); err != nil {
		t.Fatalf("%s is missing; run hack/moto.sh (or set MOTO_URL)", server)
	}
	port := freePort(t)
	var out bytes.Buffer
	cmd := exec.Command(server, "-H", "127.0.0.1", "-p", fmt.Sprint(port))
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url + "/moto-api/"); err == nil {
			resp.Body.Close()
			return url
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("moto did not start on %s:\n%s", url, out.String())
	return ""
}

func writeKubeconfig(t *testing.T, cfg *rest.Config) string {
	t.Helper()
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

var (
	buildOnce sync.Map // service -> *builtBinary
)

type builtBinary struct {
	once sync.Once
	path string
	err  error
}

func buildController(t *testing.T, root, service string) string {
	t.Helper()
	v, _ := buildOnce.LoadOrStore(service, &builtBinary{})
	b := v.(*builtBinary)
	b.once.Do(func() {
		dir, err := os.MkdirTemp("", "ack-ext-"+service)
		if err != nil {
			b.err = err
			return
		}
		b.path = filepath.Join(dir, service+"-controller")
		cmd := exec.Command("go", "build", "-o", b.path, "./services/"+service+"/cmd/controller")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if b.err != nil {
		t.Fatal(b.err)
	}
	return b.path
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

// runController starts the binary with the arguments ACK's Helm chart
// gives a controller (templates/helm/templates/deployment.yaml), and an
// environment holding only fake credentials and the kubeconfig.
func runController(t *testing.T, bin, kubeconfig, motoURL string) *syncBuffer {
	t.Helper()
	sb := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin,
		"--aws-region", Region,
		"--aws-endpoint-url", motoURL,
		"--aws-identity-endpoint-url", motoURL,
		"--allow-unsafe-aws-endpoint-urls",
		"--log-level", "debug",
		"--enable-development-logging",
		"--deletion-policy", "delete",
		"--reconcile-default-resync-seconds", "5",
		"--metrics-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--healthz-addr", fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		"--enable-carm=false",
	)
	cmd.Env = []string{
		"KUBECONFIG=" + kubeconfig,
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"AWS_ACCESS_KEY_ID=testing",
		"AWS_SECRET_ACCESS_KEY=testing",
		"AWS_EC2_METADATA_DISABLED=true",
		"ACK_SYSTEM_NAMESPACE=default",
	}
	cmd.Stdout, cmd.Stderr = sb, sb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			sb.mu.Lock()
			t.Logf("controller output:\n%s", sb.b.String())
			sb.mu.Unlock()
		}
	})
	return sb
}

// Eventually polls f until it returns nil or the timeout passes.
func Eventually(t *testing.T, timeout time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = f(); last == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s: not within %s: %v", what, timeout, last)
}
