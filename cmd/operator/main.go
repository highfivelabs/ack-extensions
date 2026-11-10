// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// The operator: runs our managers beside ACK's service controllers, as
// the same AWS identity, and a Cloud Control manager in each namespace
// that has a CloudControlContext, as that namespace's own identity
// (pkg/operator).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ccv1alpha1 "github.com/highfivelabs/ack-extensions/cloudcontrol/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/pkg/operator"
)

// repeated is a flag given any number of times.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// checkEndpoints refuses a fake AWS endpoint for the managers without the
// identity endpoint too: ACK's runtime asks STS who it is at start, at
// --aws-identity-endpoint-url or AWS_ENDPOINT_URL, else at real AWS.
func checkEndpoints(args []string, env []corev1.EnvVar) error {
	var endpoint, identity bool
	for _, a := range args {
		endpoint = endpoint || strings.HasPrefix(a, "--aws-endpoint-url")
		identity = identity || strings.HasPrefix(a, "--aws-identity-endpoint-url")
	}
	for _, e := range env {
		identity = identity || e.Name == "AWS_ENDPOINT_URL" || e.Name == "AWS_ENDPOINT_URL_STS"
	}
	if endpoint && !identity {
		return fmt.Errorf("--cloudcontrol-manager-arg=--aws-endpoint-url needs --aws-identity-endpoint-url (or AWS_ENDPOINT_URL), or the managers ask real AWS STS who they are")
	}
	return nil
}

func envVars(kvs []string) ([]corev1.EnvVar, error) {
	var out []corev1.EnvVar
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--cloudcontrol-manager-env: %q is not NAME=value", kv)
		}
		out = append(out, corev1.EnvVar{Name: k, Value: v})
	}
	return out, nil
}

// imagesFromEnv reads MANAGER_IMAGE_<SERVICE> (one whole image reference
// each, so `ko resolve` can fill them in config/operator).
func imagesFromEnv() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if svc, ok := strings.CutPrefix(k, "MANAGER_IMAGE_"); ok && v != "" {
			out[strings.ToLower(svc)] = v
		}
	}
	return out
}

func parseImages(s string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		if env := imagesFromEnv(); len(env) > 0 {
			return env, nil
		}
	}
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("--manager-images: %q is not service=image", kv)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no manager images: set --manager-images or MANAGER_IMAGE_<SERVICE>")
	}
	return out, nil
}

func main() {
	var images, identity, metrics, probes string
	var ccArgs, ccEnv repeated
	flag.StringVar(&images, "manager-images", "", "service=image of each manager to run, comma-separated (iam=...,rds=...); default: the MANAGER_IMAGE_<SERVICE> environment variables")
	flag.StringVar(&identity, "identity", operator.IdentityOwn, `default identity of a manager: "own" (its own ServiceAccount carrying ACK's role annotations) or "ack" (ACK's ServiceAccount)`)
	flag.StringVar(&metrics, "metrics-bind-address", ":8080", "metrics address")
	flag.StringVar(&probes, "health-probe-bind-address", ":8081", "health probe address")
	flag.Var(&ccArgs, "cloudcontrol-manager-arg", "an argument added to every Cloud Control manager (repeatable); for a fake AWS's endpoint in tests, never needed on EKS")
	flag.Var(&ccEnv, "cloudcontrol-manager-env", "NAME=value added to every Cloud Control manager's environment (repeatable); for a fake AWS's dummy credentials in tests, never needed on EKS")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setup := ctrl.Log.WithName("setup")

	imgs, err := parseImages(images)
	if err != nil {
		setup.Error(err, "bad flags")
		os.Exit(2)
	}
	if identity != operator.IdentityOwn && identity != operator.IdentityACK {
		setup.Error(fmt.Errorf("--identity %q", identity), "bad flags")
		os.Exit(2)
	}

	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = ccv1alpha1.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metrics},
		HealthProbeBindAddress: probes,
	})
	if err != nil {
		setup.Error(err, "manager")
		os.Exit(1)
	}
	r := &operator.Reconciler{
		Client:  mgr.GetClient(),
		Options: operator.Options{Images: imgs, Identity: identity},
		// Core events, which the operator may already create (config/operator/rbac.yaml).
		Recorder: mgr.GetEventRecorderFor(operator.Name + "-operator"), //nolint:staticcheck
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setup.Error(err, "controller")
		os.Exit(1)
	}
	// Cloud Control: on when its manager image is given.
	if img := imgs[operator.ImageCloudControl]; img != "" {
		env, err := envVars(ccEnv)
		if err != nil {
			setup.Error(err, "bad flags")
			os.Exit(2)
		}
		if err := checkEndpoints(ccArgs, env); err != nil {
			setup.Error(err, "bad flags")
			os.Exit(2)
		}
		// The managers run in the operator's own namespace (POD_NAMESPACE,
		// from the downward API in config/operator).
		cc := &operator.ContextReconciler{Client: mgr.GetClient(), Namespace: os.Getenv("POD_NAMESPACE"), Image: img, ExtraArgs: ccArgs, ExtraEnv: env}
		if err := cc.SetupWithManager(mgr); err != nil {
			setup.Error(err, "cloudcontrol controller")
			os.Exit(1)
		}
	}
	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)
	setup.Info("starting", "images", imgs, "identity", identity)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setup.Error(err, "manager stopped")
		os.Exit(1)
	}
}
