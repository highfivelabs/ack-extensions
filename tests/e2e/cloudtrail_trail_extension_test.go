// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/highfivelabs/ack-extensions/pkg/test/harness"
	"github.com/highfivelabs/ack-extensions/services/cloudtrail/pkg/extend/trail"
)

// The Trail extension (docs/extensions.md; docs/plan.md open question 26,
// answered 2026-11-23): our cloudtrail manager, beside ACK's
// cloudtrail-controller, starts and stops a Trail's logging and puts its
// selectors from annotations in our group.
//
// ACK's cloudtrail-controller is not a Go module here (UPSTREAM.md), so the
// test plays ACK's side, as TestBucketExtension does: it makes the trail in
// the fake and ACK's Trail (its CRD from hack/ack-crds, cloudtrail-controller
// v1.10.0) with the status ACK writes once it has made it.
//
// moto 5.2.5 keeps advanced event selectors put through PutEventSelectors,
// but does not clear them when event selectors are put afterwards (real
// CloudTrail does), and appends insight selectors instead of replacing them:
// the switch back from advanced selectors and turning Insights off are
// shown by the unit tests of services/cloudtrail/pkg/extend/trail only
// (docs/plan.md section 4.4).
func TestTrailExtension(t *testing.T) {
	env, _ := harness.StartControllers(t, "cloudtrail", nil, harness.Controller{})
	ctx := context.Background()
	api := cloudtrail.NewFromConfig(env.AWS)
	s3api := s3.NewFromConfig(env.AWS, func(o *s3.Options) { o.UsePathStyle = true })

	// Bucket names are unique across all of AWS: a live run takes its ID.
	bucket := "audit-trail-logs"
	if id := os.Getenv("LIVE_RUN_ID"); harness.IsLive() && id != "" {
		bucket += "-" + id
	}
	if _, err := s3api.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket:                    aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(harness.Region)},
	}); err != nil {
		t.Fatal(err)
	}
	// Real CloudTrail needs the bucket to let it write: the documented
	// policy for a trail's bucket.
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},"Action":"s3:GetBucketAcl","Resource":"arn:aws:s3:::%[1]s"},`+
		`{"Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},"Action":"s3:PutObject","Resource":"arn:aws:s3:::%[1]s/AWSLogs/%[2]s/*","Condition":{"StringEquals":{"s3:x-amz-acl":"bucket-owner-full-control"}}}]}`,
		bucket, env.Account)
	if _, err := s3api.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(policy)}); err != nil {
		t.Fatal(err)
	}
	var trails []string
	t.Cleanup(func() {
		for _, n := range trails {
			if _, err := api.DeleteTrail(ctx, &cloudtrail.DeleteTrailInput{Name: aws.String(n)}); err != nil {
				t.Logf("cleanup: trail %s: %v", n, err)
			}
		}
		// A live trail has written logs into the bucket.
		if out, err := s3api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)}); err == nil {
			for _, v := range out.Versions {
				_, _ = s3api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId})
			}
		}
		if _, err := s3api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Logf("cleanup: bucket %s: %v", bucket, err)
		}
	})

	// newTrail makes ACK's Trail and, when made, the trail itself and the
	// status ACK writes. It returns the trail's ARN.
	newTrail := func(t *testing.T, name string, annotations map[string]string, made bool) string {
		t.Helper()
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(trail.GVK)
		u.SetNamespace("default")
		u.SetName(name)
		u.SetAnnotations(annotations)
		_ = unstructured.SetNestedField(u.Object, name, "spec", "name")
		_ = unstructured.SetNestedField(u.Object, bucket, "spec", "s3BucketName")
		if err := env.Client.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		if !made {
			return ""
		}
		out, err := api.CreateTrail(ctx, &cloudtrail.CreateTrailInput{Name: aws.String(name), S3BucketName: aws.String(bucket)})
		if err != nil {
			t.Fatal(err)
		}
		trails = append(trails, name)
		arn := aws.ToString(out.TrailARN)
		harness.Eventually(t, 30*time.Second, name+" status", func() error {
			if err := env.Client.Get(ctx, client.ObjectKeyFromObject(u), u); err != nil {
				return err
			}
			_ = unstructured.SetNestedMap(u.Object, map[string]any{
				"ackResourceMetadata": map[string]any{"arn": arn, "ownerAccountID": env.Account, "region": harness.Region},
			}, "status")
			return env.Client.Status().Update(ctx, u)
		})
		return arn
	}
	annotate := func(t *testing.T, name string, set map[string]string) {
		t.Helper()
		harness.Eventually(t, 30*time.Second, "annotate "+name, func() error {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(trail.GVK)
			if err := env.Client.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, u); err != nil {
				return err
			}
			a := u.GetAnnotations()
			if a == nil {
				a = map[string]string{}
			}
			for k, v := range set {
				if v == "" {
					delete(a, k)
				} else {
					a[k] = v
				}
			}
			u.SetAnnotations(a)
			return env.Client.Update(ctx, u)
		})
	}
	logging := func(arn string) (bool, error) {
		st, err := api.GetTrailStatus(ctx, &cloudtrail.GetTrailStatusInput{Name: aws.String(arn)})
		if err != nil {
			return false, err
		}
		return aws.ToBool(st.IsLogging), nil
	}
	waitLogging := func(t *testing.T, arn string, want bool) {
		t.Helper()
		harness.Eventually(t, 60*time.Second, fmt.Sprintf("logging %v", want), func() error {
			on, err := logging(arn)
			if err != nil {
				return err
			}
			if on != want {
				return fmt.Errorf("logging is %v", on)
			}
			return nil
		})
	}

	arn := newTrail(t, "audit", map[string]string{
		trail.AnnotationIsLogging:        "true",
		trail.AnnotationEventSelectors:   `[{"ReadWriteType":"WriteOnly","IncludeManagementEvents":true,"DataResources":[{"Type":"AWS::S3::Object","Values":["arn:aws:s3:::` + bucket + `/"]}]}]`,
		trail.AnnotationInsightSelectors: `[{"InsightType":"ApiCallRateInsight"}]`,
	}, true)

	t.Run("logging started and selectors put once ACK has made the trail", func(t *testing.T) {
		waitLogging(t, arn, true)
		waitEvent(t, env, "default", "audit", trail.ReasonLoggingStarted)
		waitEvent(t, env, "default", "audit", trail.ReasonSelectorsSet)
		waitEvent(t, env, "default", "audit", trail.ReasonInsightsSet)
		sel, err := api.GetEventSelectors(ctx, &cloudtrail.GetEventSelectorsInput{TrailName: aws.String(arn)})
		if err != nil {
			t.Fatal(err)
		}
		if len(sel.EventSelectors) != 1 || sel.EventSelectors[0].ReadWriteType != cloudtrailtypes.ReadWriteTypeWriteOnly ||
			len(sel.EventSelectors[0].DataResources) != 1 || aws.ToString(sel.EventSelectors[0].DataResources[0].Type) != "AWS::S3::Object" {
			t.Fatalf("event selectors %+v", sel.EventSelectors)
		}
		ins, err := api.GetInsightSelectors(ctx, &cloudtrail.GetInsightSelectorsInput{TrailName: aws.String(arn)})
		if err != nil {
			t.Fatal(err)
		}
		if len(ins.InsightSelectors) != 1 || ins.InsightSelectors[0].InsightType != cloudtrailtypes.InsightTypeApiCallRateInsight {
			t.Fatalf("insight selectors %+v", ins.InsightSelectors)
		}
	})

	t.Run("logging stopped outside Kubernetes is started again", func(t *testing.T) {
		if _, err := api.StopLogging(ctx, &cloudtrail.StopLoggingInput{Name: aws.String(arn)}); err != nil {
			t.Fatal(err)
		}
		// The harness gives the managers a five-second resync.
		waitLogging(t, arn, true)
	})

	t.Run("advanced event selectors replace the event selectors", func(t *testing.T) {
		annotate(t, "audit", map[string]string{
			trail.AnnotationEventSelectors:         "",
			trail.AnnotationAdvancedEventSelectors: `[{"Name":"management writes","FieldSelectors":[{"Field":"eventCategory","Equals":["Management"]},{"Field":"readOnly","Equals":["false"]}]}]`,
		})
		harness.Eventually(t, 60*time.Second, "advanced selectors", func() error {
			sel, err := api.GetEventSelectors(ctx, &cloudtrail.GetEventSelectorsInput{TrailName: aws.String(arn)})
			if err != nil {
				return err
			}
			if len(sel.EventSelectors) != 0 || len(sel.AdvancedEventSelectors) != 1 || len(sel.AdvancedEventSelectors[0].FieldSelectors) != 2 ||
				aws.ToString(sel.AdvancedEventSelectors[0].Name) != "management writes" {
				return fmt.Errorf("event selectors %+v, advanced %+v", sel.EventSelectors, sel.AdvancedEventSelectors)
			}
			return nil
		})
	})

	t.Run("is-logging false stops logging", func(t *testing.T) {
		annotate(t, "audit", map[string]string{trail.AnnotationIsLogging: "false"})
		waitLogging(t, arn, false)
		waitEvent(t, env, "default", "audit", trail.ReasonLoggingStopped)
	})

	t.Run("an annotation that is not valid is an Event, and the others still hold", func(t *testing.T) {
		annotate(t, "audit", map[string]string{trail.AnnotationInsightSelectors: `[{"InsightType":"Everything"}]`})
		msg := waitEvent(t, env, "default", "audit", trail.ReasonInvalidAnnotation)
		if !strings.Contains(msg, trail.AnnotationInsightSelectors) {
			t.Fatalf("event %q", msg)
		}
		if on, err := logging(arn); err != nil || on {
			t.Fatalf("logging %v (%v)", on, err)
		}
	})

	t.Run("nothing is done before ACK has made the trail", func(t *testing.T) {
		newTrail(t, "unmade", map[string]string{trail.AnnotationIsLogging: "true"}, false)
		out, err := api.CreateTrail(ctx, &cloudtrail.CreateTrailInput{Name: aws.String("unmade"), S3BucketName: aws.String(bucket)})
		if err != nil {
			t.Fatal(err)
		}
		trails = append(trails, "unmade")
		// Long enough for the manager to have seen it (several resyncs).
		time.Sleep(10 * time.Second)
		if on, err := logging(aws.ToString(out.TrailARN)); err != nil || on {
			t.Fatalf("logging %v (%v) on a trail ACK's object does not name", on, err)
		}
	})
}
