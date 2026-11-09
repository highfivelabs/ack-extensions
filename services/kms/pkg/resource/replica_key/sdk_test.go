// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package replica_key

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	ackkmsapitypes "github.com/aws-controllers-k8s/kms-controller/apis/v1alpha1"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/kms"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/kms/apis/v1alpha1"
)

const (
	primaryRegion = "us-west-2"
	replicaRegion = "eu-west-1"
	keyID         = "mrk-1234abcd12ab34cd56ef1234567890ab"
	primaryARN    = "arn:aws:kms:us-west-2:123456789012:key/" + keyID
	replicaARN    = "arn:aws:kms:eu-west-1:123456789012:key/" + keyID
)

// call is one request to the stand-in: the operation (X-Amz-Target) and
// the Region the request was signed for, which is the Region the SDK sends
// it to.
type call struct{ op, region, keyID string }

// kmsStandIn answers the KMS JSON API for one primary key in us-west-2
// with one replica in eu-west-1, and records every call. A replica and its
// primary share a key ID, as on AWS; the Region a request is sent to
// decides which key it acts on.
type kmsStandIn struct {
	mu    sync.Mutex
	calls []call
	// primaryType is the MultiRegionKeyType the primary key reports:
	// PRIMARY on AWS; moto can report REPLICA (docs/plan.md section 4.4).
	primaryType string
}

var credentialRegion = regexp.MustCompile(`Credential=[^/]+/[^/]+/([^/]+)/kms/`)

func (s *kmsStandIn) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var in struct{ KeyId string }
	_ = json.Unmarshal(body, &in)
	op := strings.TrimPrefix(req.Header.Get("X-Amz-Target"), "TrentService.")
	region := ""
	if m := credentialRegion.FindStringSubmatch(req.Header.Get("Authorization")); m != nil {
		region = m[1]
	}
	s.mu.Lock()
	s.calls = append(s.calls, call{op, region, in.KeyId})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	switch op {
	case "DescribeKey":
		if region == primaryRegion {
			typ := s.primaryType
			if typ == "" {
				typ = "PRIMARY"
			}
			fmt.Fprintf(w, `{"KeyMetadata":{"KeyId":%q,"Arn":%q,"KeyState":"Enabled","Enabled":true,"MultiRegion":true,
				"MultiRegionConfiguration":{"MultiRegionKeyType":%q,"PrimaryKey":{"Arn":%q,"Region":%q},
				"ReplicaKeys":[{"Arn":%q,"Region":%q}]}}}`, keyID, primaryARN, typ, primaryARN, primaryRegion, replicaARN, replicaRegion)
			return
		}
		fmt.Fprintf(w, `{"KeyMetadata":{"KeyId":%q,"Arn":%q,"KeyState":"Enabled","Enabled":true,"MultiRegion":true,
			"MultiRegionConfiguration":{"MultiRegionKeyType":"REPLICA","PrimaryKey":{"Arn":%q,"Region":%q}}}}`, keyID, replicaARN, primaryARN, primaryRegion)
	case "ListResourceTags":
		fmt.Fprint(w, `{"Tags":[{"TagKey":"team","TagValue":"payments"}],"Truncated":false}`)
	case "GetKeyPolicy":
		fmt.Fprint(w, `{"Policy":"{\"Version\": \"2012-10-17\", \"Statement\": []}","PolicyName":"default"}`)
	case "ScheduleKeyDeletion":
		fmt.Fprintf(w, `{"KeyId":%q,"KeyState":"PendingDeletion","PendingWindowInDays":7}`, in.KeyId)
	default:
		fmt.Fprint(w, `{}`)
	}
}

func standInManager(t *testing.T, s *kmsStandIn) *resourceManager {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:           primaryRegion,
		Credentials:      credentials.NewStaticCredentialsProvider("testing", "testing", ""),
		BaseEndpoint:     aws.String(srv.URL),
		RetryMaxAttempts: 1,
	}
	return &resourceManager{
		clientcfg:    cfg,
		metrics:      ackmetrics.NewMetrics("kms"),
		awsRegion:    primaryRegion,
		awsAccountID: "123456789012",
		awsPartition: "aws",
		sdkapi:       svcsdk.NewFromConfig(cfg),
	}
}

func replica() *svcapitypes.ReplicaKey {
	ko := &svcapitypes.ReplicaKey{}
	ko.Spec.KeyID = aws.String(keyID)
	ko.Spec.ReplicaRegion = aws.String(replicaRegion)
	return ko
}

// Only the primary key's DescribeKey (to find the replica) may go to the
// primary key's Region; everything that touches the replica goes to the
// replica's.
func onlyDescribeInPrimary(t *testing.T, calls []call) {
	t.Helper()
	for _, c := range calls {
		if c.region == "" {
			t.Errorf("%s: no Region in its signature", c.op)
		}
		if c.region == primaryRegion && c.op != "DescribeKey" {
			t.Errorf("%s reached the primary key's Region %s with key %s", c.op, primaryRegion, c.keyID)
		}
	}
}

// Deleting a ReplicaKey must never reach the primary key's Region: there,
// the replica's key ID names the primary key, and ScheduleKeyDeletion
// would schedule the primary key for deletion.
func TestDeleteNeverReachesThePrimaryRegion(t *testing.T) {
	for _, c := range []struct {
		name       string
		withStatus bool
	}{
		{"with the replica's ARN in status", true},
		{"found through the primary key", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &kmsStandIn{}
			rm := standInManager(t, s)
			ko := replica()
			if c.withStatus {
				ko.Status.ReplicaKeyMetadata = &ackkmsapitypes.KeyMetadata{KeyID: aws.String(keyID), ARN: aws.String(replicaARN)}
			}
			if _, err := rm.sdkDelete(context.Background(), &resource{ko}); err != nil {
				t.Fatal(err)
			}
			onlyDescribeInPrimary(t, s.calls)
			var deletes []call
			for _, c := range s.calls {
				if c.op == "ScheduleKeyDeletion" {
					deletes = append(deletes, c)
				}
			}
			if len(deletes) != 1 || deletes[0].region != replicaRegion || deletes[0].keyID != replicaARN {
				t.Fatalf("ScheduleKeyDeletion calls %+v, want one, in %s, for %s", deletes, replicaRegion, replicaARN)
			}
		})
	}
}

func TestFindReadsTheReplicaInItsRegion(t *testing.T) {
	s := &kmsStandIn{}
	rm := standInManager(t, s)
	ko := replica()
	ko.Spec.Policy = aws.String(`{"Version":"2012-10-17","Statement":[]}`)
	got, err := rm.sdkFind(context.Background(), &resource{ko})
	if err != nil {
		t.Fatal(err)
	}
	onlyDescribeInPrimary(t, s.calls)
	md := got.ko.Status.ReplicaKeyMetadata
	if md == nil || aws.ToString(md.ARN) != replicaARN || aws.ToString(md.MultiRegionConfiguration.MultiRegionKeyType) != "REPLICA" {
		t.Fatalf("status.replicaKeyMetadata = %+v, want the replica's", md)
	}
	if len(got.ko.Spec.Tags) != 1 || aws.ToString(got.ko.Spec.Tags[0].TagKey) != "team" {
		t.Fatalf("spec.tags = %+v", got.ko.Spec.Tags)
	}
	// KMS stores the policy reformatted; it is the same policy.
	if d := newResourceDelta(&resource{ko}, got); d.DifferentAt("Spec.Policy") {
		t.Fatalf("a reformatted policy reads as drift: %s vs %s", aws.ToString(ko.Spec.Policy), aws.ToString(got.ko.Spec.Policy))
	}
}

func TestSyncedWhenEnabled(t *testing.T) {
	for state, want := range map[string]bool{"Creating": false, "Enabled": true, "Disabled": false, "PendingDeletion": false} {
		if got := replicaEnabled(&ackkmsapitypes.KeyMetadata{KeyState: aws.String(state)}); got != want {
			t.Errorf("%s: synced %v, want %v", state, got, want)
		}
	}
	if replicaEnabled(nil) {
		t.Error("synced with no metadata")
	}
}

func TestTagsAreASet(t *testing.T) {
	a := []*ackkmsapitypes.Tag{{TagKey: aws.String("b"), TagValue: aws.String("2")}, {TagKey: aws.String("a"), TagValue: aws.String("1")}}
	b := tagsFromMap(map[string]string{"a": "1", "b": "2"})
	ra, rb := &resource{replica()}, &resource{replica()}
	ra.ko.Spec.Tags, rb.ko.Spec.Tags = a, b
	if d := newResourceDelta(ra, rb); d.DifferentAt("Spec.Tags") {
		t.Fatal("the same tags in another order differ")
	}
	rb.ko.Spec.Tags = tagsFromMap(map[string]string{"a": "1"})
	if d := newResourceDelta(ra, rb); !d.DifferentAt("Spec.Tags") {
		t.Fatal("a removed tag is no difference")
	}
}

// The primary key is the key whose primary is in the manager's Region,
// whatever MultiRegionKeyType it reports; a key whose primary is
// elsewhere is refused.
func TestThePrimaryKeyIsFoundByItsRegion(t *testing.T) {
	s := &kmsStandIn{primaryType: "REPLICA"}
	rm := standInManager(t, s)
	if _, err := rm.sdkFind(context.Background(), &resource{replica()}); err != nil {
		t.Fatalf("a primary key reported as REPLICA (moto) is not found: %v", err)
	}
	rm.awsRegion = "ap-south-1"
	rm.sdkapi = svcsdk.NewFromConfig(rm.clientcfg, func(o *svcsdk.Options) { o.Region = "ap-south-1" })
	if _, err := rm.sdkFind(context.Background(), &resource{replica()}); err == nil || !strings.Contains(err.Error(), "not a multi-Region primary key") {
		t.Fatalf("a key whose primary is in us-west-2, read from ap-south-1: %v, want refused", err)
	}
}
