// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package account_alias

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
)

// aliasEndpoint is IAM for one account whose alias is current. Like the
// AWS Emulator, CreateAccountAlias refuses an alias that already exists
// with EntityAlreadyExists, the error IAM's API reference lists for it
// (https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateAccountAlias.html),
// even when it is this account's own; moto accepts it. A new alias
// replaces the current one.
func aliasEndpoint(t *testing.T, current *string, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the request: %v", err)
		}
		action, alias := r.Form.Get("Action"), r.Form.Get("AccountAlias")
		*seen = append(*seen, strings.TrimSpace(action+" "+alias))
		w.Header().Set("Content-Type", "text/xml")
		const ns = `xmlns="https://iam.amazonaws.com/doc/2010-05-08/"`
		switch action {
		case "ListAccountAliases":
			members := ""
			if *current != "" {
				members = "<member>" + *current + "</member>"
			}
			fmt.Fprintf(w, `<ListAccountAliasesResponse %s><ListAccountAliasesResult><IsTruncated>false</IsTruncated><AccountAliases>%s</AccountAliases></ListAccountAliasesResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></ListAccountAliasesResponse>`, ns, members)
		case "CreateAccountAlias":
			if alias == *current {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprintf(w, `<ErrorResponse %s><Error><Type>Sender</Type><Code>EntityAlreadyExists</Code><Message>The account alias %s already exists.</Message></Error><RequestId>2</RequestId></ErrorResponse>`, ns, alias)
				return
			}
			*current = alias
			fmt.Fprintf(w, `<CreateAccountAliasResponse %s><ResponseMetadata><RequestId>3</RequestId></ResponseMetadata></CreateAccountAliasResponse>`, ns)
		default:
			t.Errorf("unexpected action %q", action)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func aliasManager(t *testing.T, url string) *resourceManager {
	t.Helper()
	cfg, err := fakeaws.Config(url, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	return &resourceManager{
		metrics:      ackmetrics.NewMetrics("iam"),
		sdkapi:       svcsdk.NewFromConfig(cfg),
		awsAccountID: "123456789012",
		awsRegion:    "us-west-2",
		awsPartition: "aws",
	}
}

// A new object for the alias the account already has (set by hand, or
// left by an object deleted with deletion-policy retain) takes it over
// instead of failing on IAM's EntityAlreadyExists.
func TestCreateAdoptsTheAccountsOwnAlias(t *testing.T) {
	current, seen := "acme-prod", []string{}
	rm := aliasManager(t, aliasEndpoint(t, &current, &seen).URL)

	created, err := rm.sdkCreate(context.Background(), alias("acme-prod"))
	if err != nil {
		t.Fatalf("sdkCreate: %v (requests: %v)", err, seen)
	}
	if !made(created.ko) {
		t.Fatal("no ARN in the status")
	}
	if want := "arn:aws:iam::123456789012:account-alias/acme-prod"; string(*created.ko.Status.ACKResourceMetadata.ARN) != want {
		t.Fatalf("ARN %q, want %q", *created.ko.Status.ACKResourceMetadata.ARN, want)
	}
}

// EntityAlreadyExists for an alias the account does not have is another
// account's alias (aliases are unique across AWS): still an error.
func TestCreateRefusesAnotherAccountsAlias(t *testing.T) {
	current, seen := "acme-prod", []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seen = append(seen, r.Form.Get("Action"))
		w.Header().Set("Content-Type", "text/xml")
		const ns = `xmlns="https://iam.amazonaws.com/doc/2010-05-08/"`
		if r.Form.Get("Action") == "ListAccountAliases" {
			fmt.Fprintf(w, `<ListAccountAliasesResponse %s><ListAccountAliasesResult><IsTruncated>false</IsTruncated><AccountAliases><member>%s</member></AccountAliases></ListAccountAliasesResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></ListAccountAliasesResponse>`, ns, current)
			return
		}
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `<ErrorResponse %s><Error><Type>Sender</Type><Code>EntityAlreadyExists</Code><Message>The account alias taken already exists.</Message></Error><RequestId>2</RequestId></ErrorResponse>`, ns)
	}))
	t.Cleanup(srv.Close)
	rm := aliasManager(t, srv.URL)

	if _, err := rm.sdkCreate(context.Background(), alias("taken")); err == nil || !isAPIError(err, entityAlreadyExists) {
		t.Fatalf("sdkCreate: %v, want EntityAlreadyExists (requests: %v)", err, seen)
	}
}
