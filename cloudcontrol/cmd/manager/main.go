// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Command manager serves every Cloud Control kind in the registry with one
// generic resource manager on ACK's runtime. It takes ACK's controller flags
// unchanged (region, endpoint, watch namespace and selectors, CARM, feature
// gates, deletion policy, resync), so it runs the way ACK's controllers do.
// Its shape is ACK's generated cmd/controller/main.go
// (services/organizations/cmd/controller/main.go here).
package main

import (
	"context"
	"os"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	ackrtutil "github.com/aws-controllers-k8s/runtime/pkg/util"
	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlrt "sigs.k8s.io/controller-runtime"
	ctrlrtcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlrthealthz "sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlrtmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlrtwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/resource"
)

const (
	apiGroup = "cloudcontrol.aws.hfl.dev"
	alias    = "cloudcontrol"
)

var (
	// Set with -ldflags at release.
	gitCommit, gitVersion, buildDate = "unknown", "dev", "unknown"

	scheme   = runtime.NewScheme()
	setupLog = ctrlrt.Log.WithName("setup")
)

func main() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = ackv1alpha1.AddToScheme(scheme)

	var cfg ackcfg.Config
	cfg.BindFlags()
	flag.Parse()
	cfg.SetupLogger()

	factories := resource.Factories()
	gvks := make([]schema.GroupVersionKind, 0, len(factories))
	for _, f := range factories {
		gvks = append(gvks, f.ResourceDescriptor().GroupVersionKind())
	}
	fail := func(err error, msg string) {
		setupLog.Error(err, msg, "aws.service", alias)
		os.Exit(1)
	}
	if err := cfg.Validate(context.Background(), ackcfg.WithGVKs(gvks)); err != nil {
		fail(err, "invalid configuration")
	}
	host, port, err := ackrtutil.GetHostPort(cfg.WebhookServerAddr)
	if err != nil {
		fail(err, "unable to parse the webhook server address")
	}
	namespaces, err := cfg.GetWatchNamespaces()
	if err != nil {
		fail(err, "unable to parse watch namespaces")
	}
	watch := map[string]ctrlrtcache.Config{}
	for _, ns := range namespaces {
		watch[ns] = ctrlrtcache.Config{}
	}
	selectors, err := cfg.ParseWatchSelectors()
	if err != nil {
		fail(err, "unable to parse watch selectors")
	}
	mgr, err := ctrlrt.NewManager(ctrlrt.GetConfigOrDie(), ctrlrt.Options{
		Scheme:                  scheme,
		Cache:                   ctrlrtcache.Options{Scheme: scheme, DefaultNamespaces: watch, DefaultLabelSelector: selectors},
		WebhookServer:           &ctrlrtwebhook.DefaultServer{Options: ctrlrtwebhook.Options{Port: port, Host: host}},
		Metrics:                 metricsserver.Options{BindAddress: cfg.MetricsAddr},
		LeaderElection:          cfg.EnableLeaderElection,
		LeaderElectionID:        "ack-" + apiGroup,
		LeaderElectionNamespace: cfg.LeaderElectionNamespace,
		HealthProbeBindAddress:  cfg.HealthzAddr,
		LivenessEndpointName:    "/healthz",
		ReadinessEndpointName:   "/readyz",
	})
	if err != nil {
		fail(err, "unable to create the controller manager")
	}
	setupLog.Info("serving Cloud Control kinds", "kinds", len(gvks), "version", gitVersion)
	sc := ackrt.NewServiceController(alias, apiGroup, acktypes.VersionInfo{
		GitCommit: gitCommit, GitVersion: gitVersion, BuildDate: buildDate,
	}).WithLogger(ctrlrt.Log).WithResourceManagerFactories(factories).WithPrometheusRegistry(ctrlrtmetrics.Registry)
	if err := sc.BindControllerManager(mgr, cfg); err != nil {
		fail(err, "unable to bind the service controller")
	}
	if err := mgr.AddHealthzCheck("health", ctrlrthealthz.Ping); err != nil {
		fail(err, "unable to set up the health check")
	}
	if err := mgr.AddReadyzCheck("check", ctrlrthealthz.Ping); err != nil {
		fail(err, "unable to set up the ready check")
	}
	if err := mgr.Start(ctrlrt.SetupSignalHandler()); err != nil {
		fail(err, "unable to start the controller manager")
	}
}
