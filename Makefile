# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# ack-extensions: kinds AWS Controllers for Kubernetes (ACK) does not have
# yet, built on ACK's runtime and generator. Tests run against moto, a
# fake AWS; nothing here talks to real AWS.

# The project's name, for a rename: the Go module path, the images and
# pkg/operator.Name carry it too.
NAME := ack-extensions

export GOTOOLCHAIN ?= local

BIN := $(CURDIR)/bin
SERVICES := iam lambda rds
ENVTEST_K8S_VERSION := 1.35.x
# Pinned tool versions (2026-11-07). controller-gen is the version ACK's
# code generator requires (scripts/lib/common.sh).
CONTROLLER_GEN := sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0
GOIMPORTS := golang.org/x/tools/cmd/goimports@v0.38.0
SETUP_ENVTEST := sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.0.0-20260305142021-f9589b9f2b9d
GO_LICENSES := github.com/google/go-licenses@v1.6.0

.PHONY: all tools upstream upstream-check generate build test check licenses operator-e2e

all: check

$(BIN)/controller-gen:
	GOBIN=$(BIN) go install $(CONTROLLER_GEN)
$(BIN)/goimports:
	GOBIN=$(BIN) go install $(GOIMPORTS)
$(BIN)/setup-envtest:
	GOBIN=$(BIN) go install $(SETUP_ENVTEST)
$(BIN)/go-licenses:
	GOBIN=$(BIN) go install $(GO_LICENSES)

# ACK's code generator at the pin plus patches/code-generator/, then the
# other tools and the fake AWS.
tools: upstream $(BIN)/controller-gen $(BIN)/goimports $(BIN)/setup-envtest $(BIN)/go-licenses
	hack/moto.sh

upstream:
	hack/upstream.sh

upstream-check:
	hack/upstream.sh --check

generate: upstream-check $(BIN)/controller-gen $(BIN)/goimports
	hack/generate.sh

build:
	@mkdir -p $(BIN)
	$(foreach s,$(SERVICES),go build -o $(BIN)/$(s)-manager ./services/$(s)/cmd/controller &&) true
	go build -o $(BIN)/operator ./cmd/operator

# Unit tests, the operator against envtest, and every kind end to end:
# envtest's kube-apiserver, the service's real controller binary, moto.
# The envtest tests skip without KUBEBUILDER_ASSETS (so a bare `go test`
# still runs the unit tests); here the assets must be found, or the target
# fails rather than pass with the end-to-end tests skipped. The download
# is retried: GitHub's release downloads sometimes answer 5xx.
test: $(BIN)/setup-envtest
	hack/moto.sh
	@assets=""; for i in 1 2 3 4 5; do \
		assets="$$($(BIN)/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(BIN)/envtest -p path)" && [ -n "$$assets" ] && break; \
		assets=""; echo "setup-envtest failed (attempt $$i); retrying"; sleep $$((i * 10)); \
	done; \
	[ -n "$$assets" ] || { echo "no envtest assets for $(ENVTEST_K8S_VERSION): refusing to run with the end-to-end tests skipped"; exit 1; }; \
	echo "KUBEBUILDER_ASSETS=$$assets go test ./... -count=1"; \
	KUBEBUILDER_ASSETS="$$assets" go test ./... -count=1

# Everything that must hold before a commit.
check: upstream-check
	@test -z "$$(gofmt -l services pkg cmd tests)" || { gofmt -l services pkg cmd tests; echo "gofmt needed"; exit 1; }
	$(MAKE) generate
	@git diff --exit-code -- services config || { echo "generated files are stale; commit the output of hack/generate.sh"; exit 1; }
	@test -z "$$(git ls-files --others --exclude-standard -- services config)" || { git ls-files --others --exclude-standard -- services config; echo "files under services/ or config/ are not committed"; exit 1; }
	# ACK's generated code fails vet's composites check (unkeyed fields in
	# metav1.Time and acktypes.VersionInfo literals), as ACK's own
	# controllers do; every other analyzer runs.
	go vet -composites=false ./...
	hack/check-spdx.sh
	$(MAKE) build test

# docs/third-party-licenses.csv; fails on a licence outside the accepted set.
licenses: $(BIN)/go-licenses
	hack/licenses.sh

# The operator on kind beside ACK's real rds-controller (tests/operator/run.sh).
operator-e2e:
	tests/operator/run.sh
