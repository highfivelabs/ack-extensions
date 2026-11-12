// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Command manager serves the Cloud Control kinds in the registry with one
// generic resource manager on ACK's runtime. It takes ACK's controller flags
// unchanged (region, endpoint, watch namespace and selectors, CARM, feature
// gates, deletion policy, resync), so it runs the way ACK's controllers do.
// Its shape is ACK's generated cmd/controller/main.go
// (services/organizations/cmd/controller/main.go here).
//
// The operator runs one per namespace that enables Cloud Control (a
// CloudControlContext), with --watch-namespace set to that namespace, the
// namespace's own ServiceAccount and AWS role, and --cloudcontrol-kinds
// set to the kinds the context enables (pkg/operator, docs/cloudcontrol.md).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	ackrtutil "github.com/aws-controllers-k8s/runtime/pkg/util"
	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrlrt "sigs.k8s.io/controller-runtime"
	ctrlrtcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlrtconfig "sigs.k8s.io/controller-runtime/pkg/config"
	ctrlrthealthz "sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlrtmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlrtwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/resource"
	"github.com/highfivelabs/ack-extensions/pkg/slot"
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
	kinds := flag.StringSlice("cloudcontrol-kinds", nil,
		"the kinds to serve, as Kind.service (SAMLProvider.iam), comma-separated; default: every kind in the registry. "+
			"Group-qualified, unlike --reconcile-resources, because two services can have kinds of the same name.")
	slotFlag := slot.BindFlags(flag.CommandLine)
	flag.Parse()
	cfg.SetupLogger()

	fail := func(err error, msg string) {
		setupLog.Error(err, msg, "aws.service", alias)
		os.Exit(1)
	}
	types, err := selectTypes(*kinds)
	if err != nil {
		fail(err, "invalid --cloudcontrol-kinds")
	}
	factories := resource.FactoriesFor(types)
	gvks := make([]schema.GroupVersionKind, 0, len(factories))
	for _, f := range factories {
		gvks = append(gvks, f.ResourceDescriptor().GroupVersionKind())
	}
	// The version-slot guard (docs/slot-moves.md): its tag joins ACK's
	// default tags before Validate reads their keys.
	slotName, err := slot.Setup(&cfg, *slotFlag)
	if err != nil {
		fail(err, "invalid slot")
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
		// Controller names come from the kind alone, and two services can
		// have kinds of the same name (a Detector, a Policy); the groups
		// tell them apart.
		Controller: ctrlrtconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		fail(err, "unable to create the controller manager")
	}
	setupLog.Info("serving Cloud Control kinds", "kinds", len(gvks), "watchNamespace", cfg.WatchNamespace, "version", gitVersion)
	sc := ackrt.NewServiceController(alias, apiGroup, acktypes.VersionInfo{
		GitCommit: gitCommit, GitVersion: gitVersion, BuildDate: buildDate,
	}).WithLogger(ctrlrt.Log).WithResourceManagerFactories(slot.Wrap(factories, slotName, mgr, resource.SlotTags)).WithPrometheusRegistry(ctrlrtmetrics.Registry)
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

// selectTypes is the registry's types named by refs (Kind.service), or all
// of them when refs is empty. An unknown kind is an error, not a silent skip.
func selectTypes(refs []string) ([]*registry.Type, error) {
	if len(refs) == 0 {
		return registry.Types, nil
	}
	var out []*registry.Type
	var unknown []string
	seen := map[string]bool{}
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		t := registry.ByRef(r)
		if t == nil {
			unknown = append(unknown, r)
			continue
		}
		out = append(out, t)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("no such Cloud Control kind: %s", strings.Join(unknown, ", "))
	}
	return out, nil
}
