// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

// The guard: nothing in these tests may reach real AWS. The Upjet spike in
// highfivelabs/catalog (docs/terraform-leverage.md, spike 1) sent signed
// requests to the public Organizations endpoint because an endpoint block
// was mistyped, and nothing failed until AWS answered. Three locks stand in
// the way, each enough on its own; they live in pkg/fakeaws, shared with
// the ACK kinds' harness and the operator, and these are their names here:
//
//  1. LocalOnly refuses any endpoint that is not a loopback address or is
//     an AWS domain; the harness checks the fake's and moto's URLs with it
//     before anything starts;
//  2. HTTPClient dials loopback addresses only, by the literal host, before
//     any name is resolved; every SDK client the tests and the fake make
//     uses it, so a missing endpoint override fails at the dial;
//  3. the manager runs as a separate process made by fakeaws.Command from
//     one endpoint: every endpoint it has at the fake, fake credentials,
//     and HTTP(S)_PROXY at a closed local port, verified before it starts.

// FakeAccessKeyID, FakeSecretAccessKey and DeadProxy are fakeaws's.
const (
	FakeAccessKeyID     = fakeaws.AccessKeyID
	FakeSecretAccessKey = fakeaws.SecretAccessKey
	DeadProxy           = fakeaws.DeadProxy
)

// LocalOnly returns an error unless raw is an http(s) URL on a loopback
// address, outside every AWS domain.
func LocalOnly(raw string) error {
	_, err := fakeaws.Parse(raw, fakeaws.Loopback)
	return err
}

// HTTPClient is fakeaws.HTTPClient.
func HTTPClient() *http.Client { return fakeaws.HTTPClient() }

// AWSConfig is an SDK configuration for endpoint, which must be local,
// with the fake credentials and the guarded HTTP client.
func AWSConfig(endpoint, region string) (aws.Config, error) {
	return fakeaws.Config(endpoint, region)
}

// ChildEnv is the whole environment of a process the tests start that is
// not an AWS client: fake credentials, no shared config or profile, and a
// dead proxy for any host that is not loopback.
func ChildEnv(extra ...string) []string { return fakeaws.Isolated(extra...) }
