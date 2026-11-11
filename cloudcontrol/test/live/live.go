// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

// Package live is the only code in this repository that may reach real
// AWS, for task test-live (docs/live-runs.md). It is compiled only with
// -tags live, which task check never passes (hack/noaws checks that), and
// it refuses to do anything unless:
//
//   - ACK_EXT_LIVE is exactly OptIn (an explicit opt-in, per run);
//   - LIVE_ACCOUNT_ID is the sandbox account, AWS_SANDBOX_ACCOUNT_ID
//     (from the environment, never from the repository; .env.example);
//   - the credentials are federated (AWS_ROLE_ARN and
//     AWS_WEB_IDENTITY_TOKEN_FILE, from the offload VM's Google service
//     account; hack/live/credentials.sh), never stored keys or a profile;
//   - STS says they are that account's.
package live

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// OptIn is the value ACK_EXT_LIVE must have.
const OptIn = "create-real-aws-resources"

// RunTag is the tag key every resource a live run makes carries (ACK's
// --resource-tags on the manager), with the run's ID as its value; the
// sweeper deletes by it.
const RunTag = "hfl.dev/live-run"

var runID = regexp.MustCompile(`^ackx-[0-9]{8}-[0-9a-f]{4}$`)

// Run is a checked live run.
type Run struct {
	ID, Account, Region string
	Config              aws.Config
}

// SandboxAccountEnv names the environment variable that holds the only
// AWS account live tests may touch.
const SandboxAccountEnv = "AWS_SANDBOX_ACCOUNT_ID"

// Allowed reports whether account is the sandbox account named by
// AWS_SANDBOX_ACCOUNT_ID; it is an error when that variable is unset.
func Allowed(account string) (bool, error) {
	want := strings.TrimSpace(os.Getenv(SandboxAccountEnv))
	if want == "" {
		return false, fmt.Errorf("%s is not set: the sandbox account's 12-digit ID (see .env.example)", SandboxAccountEnv)
	}
	return account != "" && account == want, nil
}

// Check refuses unless every condition in the package comment holds and
// LIVE_RUN_ID is a run's ID, and returns the run with an SDK configuration
// for the real account.
func Check(ctx context.Context, root string) (*Run, error) {
	r, err := Account(ctx, root)
	if err != nil {
		return nil, err
	}
	r.ID = os.Getenv("LIVE_RUN_ID")
	if !runID.MatchString(r.ID) {
		return nil, fmt.Errorf("live: LIVE_RUN_ID %q is not ackx-<MMDDhhmm>-<hex4>", r.ID)
	}
	return r, nil
}

// Account is Check without a run (the sweeper's).
func Account(ctx context.Context, _ string) (*Run, error) {
	if v := os.Getenv("ACK_EXT_LIVE"); v != OptIn {
		return nil, fmt.Errorf("live: ACK_EXT_LIVE is %q; a live run creates real AWS resources and needs ACK_EXT_LIVE=%s", v, OptIn)
	}
	acct := os.Getenv("LIVE_ACCOUNT_ID")
	ok, err := Allowed(acct)
	if err != nil {
		return nil, fmt.Errorf("live: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("live: account %q is not %s", acct, SandboxAccountEnv)
	}
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_ENDPOINT_URL"} {
		if os.Getenv(k) != "" {
			return nil, fmt.Errorf("live: %s is set; live runs use federated credentials only (hack/live/credentials.sh)", k)
		}
	}
	for _, k := range []string{"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE"} {
		if os.Getenv(k) == "" {
			return nil, fmt.Errorf("live: %s is not set; source hack/live/credentials.sh on an offload VM run with --sa", k)
		}
	}
	region := os.Getenv("LIVE_REGION")
	if region == "" {
		region = "us-west-2"
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("live: %w", err)
	}
	who, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("live: who am I: %w", err)
	}
	if got := aws.ToString(who.Account); got != acct {
		return nil, fmt.Errorf("live: the credentials are account %s's, not %s's", got, acct)
	}
	return &Run{Account: acct, Region: region, Config: cfg}, nil
}

// ResourceTags is the manager's --resource-tags on a live run: ACK's
// defaults and the run's tag.
func ResourceTags(id string) string {
	return "services.k8s.aws/controller-version=%CONTROLLER_SERVICE%-%CONTROLLER_VERSION%,services.k8s.aws/namespace=%K8S_NAMESPACE%," + RunTag + "=" + id
}
