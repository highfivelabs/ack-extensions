// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package option_group

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/highfivelabs/ack-extensions/pkg/fakeaws"
	svcapitypes "github.com/highfivelabs/ack-extensions/services/rds/apis/v1alpha1"
)

// describeOptionGroupsEndpoint answers DescribeOptionGroups with the
// parameter rule in AWS's RDS API reference: OptionGroupName "can't be
// supplied together with EngineName or MajorEngineVersion"
// (https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DescribeOptionGroups.html).
// Moto does not check it; AWS and aws-emulator refuse the request.
func describeOptionGroupsEndpoint(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the request: %v", err)
		}
		if got := r.Form.Get("Action"); got != "DescribeOptionGroups" {
			t.Errorf("Action %q, want DescribeOptionGroups", got)
		}
		name := r.Form.Get("OptionGroupName")
		engine := r.Form.Get("EngineName")
		major := r.Form.Get("MajorEngineVersion")
		*seen = append(*seen, fmt.Sprintf("OptionGroupName=%q EngineName=%q MajorEngineVersion=%q", name, engine, major))
		w.Header().Set("Content-Type", "text/xml")
		if name != "" && (engine != "" || major != "") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>InvalidParameterCombination</Code><Message>OptionGroupName cannot be supplied with EngineName or MajorEngineVersion</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
			return
		}
		fmt.Fprintf(w, `<DescribeOptionGroupsResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeOptionGroupsResult><OptionGroupsList><OptionGroup><OptionGroupName>%s</OptionGroupName><EngineName>mysql</EngineName><MajorEngineVersion>8.0</MajorEngineVersion><OptionGroupDescription>d</OptionGroupDescription><Options/></OptionGroup></OptionGroupsList></DescribeOptionGroupsResult><ResponseMetadata><RequestId>2</RequestId></ResponseMetadata></DescribeOptionGroupsResponse>`, name)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The read names the group only: a spec that also has an engine and a
// major version must not send them beside OptionGroupName.
func TestReadNamesTheGroupOnly(t *testing.T) {
	var seen []string
	srv := describeOptionGroupsEndpoint(t, &seen)
	cfg, err := fakeaws.Config(srv.URL, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	rm := &resourceManager{
		metrics: ackmetrics.NewMetrics("rds"),
		sdkapi:  svcsdk.NewFromConfig(cfg),
	}
	r := &resource{ko: &svcapitypes.OptionGroup{Spec: svcapitypes.OptionGroupSpec{
		Name:               aws.String("app-mysql"),
		EngineName:         aws.String("mysql"),
		MajorEngineVersion: aws.String("8.0"),
	}}}

	latest, err := rm.sdkFind(context.Background(), r)
	if err != nil {
		t.Fatalf("sdkFind: %v (requests: %v)", err, seen)
	}
	want := `OptionGroupName="app-mysql" EngineName="" MajorEngineVersion=""`
	if len(seen) != 1 || seen[0] != want {
		t.Fatalf("requests %v, want one: %s", seen, want)
	}
	ko := latest.ko
	if aws.ToString(ko.Spec.EngineName) != "mysql" || aws.ToString(ko.Spec.MajorEngineVersion) != "8.0" {
		t.Fatalf("engine %q %q not read back from the group", aws.ToString(ko.Spec.EngineName), aws.ToString(ko.Spec.MajorEngineVersion))
	}
	// The spec handed to the read is not changed by it.
	if aws.ToString(r.ko.Spec.EngineName) != "mysql" || aws.ToString(r.ko.Spec.MajorEngineVersion) != "8.0" {
		t.Fatal("the read cleared the desired spec's engine")
	}
}
