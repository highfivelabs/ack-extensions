// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

// Package live is the only code in this repository that may reach real
// AWS, for task test-live (docs/live-runs.md). It is compiled only with
// -tags live, which task check never passes (hack/noaws checks that), and
// it refuses to do anything unless:
//
//   - ACK_EXT_LIVE is exactly OptIn (an explicit opt-in, per run);
//   - LIVE_ACCOUNT_ID is listed in hack/live/accounts.txt (a reviewed
//     change; empty until the builder's sandbox account exists);
//   - the credentials are federated (AWS_ROLE_ARN and
//     AWS_WEB_IDENTITY_TOKEN_FILE, from the offload VM's Google service
//     account; hack/live/credentials.sh), never stored keys or a profile;
//   - STS says they are that account's.
package live

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// Allowed reports whether account is in the allowlist file.
func Allowed(root, account string) (bool, error) {
	f, err := os.Open(filepath.Join(root, "hack", "live", "accounts.txt"))
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if i := strings.Index(l, "#"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if l != "" && l == account {
			return true, nil
		}
	}
	return false, sc.Err()
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
func Account(ctx context.Context, root string) (*Run, error) {
	if v := os.Getenv("ACK_EXT_LIVE"); v != OptIn {
		return nil, fmt.Errorf("live: ACK_EXT_LIVE is %q; a live run creates real AWS resources and needs ACK_EXT_LIVE=%s", v, OptIn)
	}
	acct := os.Getenv("LIVE_ACCOUNT_ID")
	ok, err := Allowed(root, acct)
	if err != nil {
		return nil, fmt.Errorf("live: reading the allowlist: %w", err)
	}
	if acct == "" || !ok {
		return nil, fmt.Errorf("live: account %q is not in hack/live/accounts.txt", acct)
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
