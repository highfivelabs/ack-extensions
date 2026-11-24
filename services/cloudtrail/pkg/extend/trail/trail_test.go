// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package trail

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/smithy-go"
)

// fakeCloudTrail is one trail as real CloudTrail keeps it: a new trail has
// the default event selector, putting event selectors clears the advanced
// ones and the other way round, insight selectors are replaced, and a
// trail without Insights answers InsightNotEnabledException.
type fakeCloudTrail struct {
	logging  bool
	events   []svcsdktypes.EventSelector
	advanced []svcsdktypes.AdvancedEventSelector
	insights []svcsdktypes.InsightSelector
	calls    []string
}

func newFake() *fakeCloudTrail {
	return &fakeCloudTrail{events: []svcsdktypes.EventSelector{{
		ReadWriteType: svcsdktypes.ReadWriteTypeAll, IncludeManagementEvents: aws.Bool(true),
		DataResources: []svcsdktypes.DataResource{}, ExcludeManagementEventSources: []string{},
	}}}
}

func (f *fakeCloudTrail) GetTrailStatus(context.Context, *svcsdk.GetTrailStatusInput, ...func(*svcsdk.Options)) (*svcsdk.GetTrailStatusOutput, error) {
	return &svcsdk.GetTrailStatusOutput{IsLogging: aws.Bool(f.logging)}, nil
}

func (f *fakeCloudTrail) StartLogging(_ context.Context, in *svcsdk.StartLoggingInput, _ ...func(*svcsdk.Options)) (*svcsdk.StartLoggingOutput, error) {
	f.calls = append(f.calls, "StartLogging "+aws.ToString(in.Name))
	f.logging = true
	return &svcsdk.StartLoggingOutput{}, nil
}

func (f *fakeCloudTrail) StopLogging(_ context.Context, in *svcsdk.StopLoggingInput, _ ...func(*svcsdk.Options)) (*svcsdk.StopLoggingOutput, error) {
	f.calls = append(f.calls, "StopLogging "+aws.ToString(in.Name))
	f.logging = false
	return &svcsdk.StopLoggingOutput{}, nil
}

func (f *fakeCloudTrail) GetEventSelectors(context.Context, *svcsdk.GetEventSelectorsInput, ...func(*svcsdk.Options)) (*svcsdk.GetEventSelectorsOutput, error) {
	return &svcsdk.GetEventSelectorsOutput{EventSelectors: f.events, AdvancedEventSelectors: f.advanced}, nil
}

func (f *fakeCloudTrail) PutEventSelectors(_ context.Context, in *svcsdk.PutEventSelectorsInput, _ ...func(*svcsdk.Options)) (*svcsdk.PutEventSelectorsOutput, error) {
	if len(in.EventSelectors) > 0 && len(in.AdvancedEventSelectors) > 0 {
		return nil, &smithy.GenericAPIError{Code: "InvalidParameterCombinationException"}
	}
	if in.AdvancedEventSelectors != nil {
		f.calls = append(f.calls, "PutEventSelectors advanced "+marshal(in.AdvancedEventSelectors))
		f.advanced, f.events = in.AdvancedEventSelectors, nil
	} else {
		f.calls = append(f.calls, "PutEventSelectors "+marshal(in.EventSelectors))
		f.events, f.advanced = in.EventSelectors, nil
	}
	return &svcsdk.PutEventSelectorsOutput{}, nil
}

func (f *fakeCloudTrail) GetInsightSelectors(context.Context, *svcsdk.GetInsightSelectorsInput, ...func(*svcsdk.Options)) (*svcsdk.GetInsightSelectorsOutput, error) {
	if len(f.insights) == 0 {
		return nil, &smithy.GenericAPIError{Code: "InsightNotEnabledException"}
	}
	return &svcsdk.GetInsightSelectorsOutput{InsightSelectors: f.insights}, nil
}

func (f *fakeCloudTrail) PutInsightSelectors(_ context.Context, in *svcsdk.PutInsightSelectorsInput, _ ...func(*svcsdk.Options)) (*svcsdk.PutInsightSelectorsOutput, error) {
	f.calls = append(f.calls, "PutInsightSelectors "+marshal(in.InsightSelectors))
	f.insights = in.InsightSelectors
	return &svcsdk.PutInsightSelectorsOutput{}, nil
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		ann     map[string]string
		check   func(Wanted) bool
		wantErr string
	}{
		{"none", nil, func(w Wanted) bool { return !w.Any() }, ""},
		{"logging", map[string]string{AnnotationIsLogging: " True "}, func(w Wanted) bool { return aws.ToBool(w.IsLogging) }, ""},
		{"logging not a bool", map[string]string{AnnotationIsLogging: "on"}, func(w Wanted) bool { return !w.Any() }, "neither true nor false"},
		{"event selectors", map[string]string{AnnotationEventSelectors: `[{"ReadWriteType":"WriteOnly","DataResources":[{"Type":"AWS::S3::Object","Values":["arn:aws:s3"]}]}]`},
			func(w Wanted) bool {
				return len(w.EventSelectors) == 1 && w.EventSelectors[0].ReadWriteType == "WriteOnly" && aws.ToString(w.EventSelectors[0].DataResources[0].Type) == "AWS::S3::Object"
			}, ""},
		{"an unknown field", map[string]string{AnnotationEventSelectors: `[{"ReadWrite":"All"}]`}, func(w Wanted) bool { return w.EventSelectors == nil }, "unknown field"},
		{"a bad read/write type", map[string]string{AnnotationEventSelectors: `[{"ReadWriteType":"Sometimes"}]`}, func(w Wanted) bool { return w.EventSelectors == nil }, "not one of"},
		{"not JSON", map[string]string{AnnotationEventSelectors: `ReadWriteType: All`}, func(w Wanted) bool { return w.EventSelectors == nil }, AnnotationEventSelectors},
		{"advanced", map[string]string{AnnotationAdvancedEventSelectors: `[{"Name":"s3 writes","FieldSelectors":[{"Field":"eventCategory","Equals":["Data"]},{"Field":"resources.type","Equals":["AWS::S3::Object"]}]}]`},
			func(w Wanted) bool {
				return len(w.AdvancedEventSelectors) == 1 && len(w.AdvancedEventSelectors[0].FieldSelectors) == 2
			}, ""},
		{"advanced without a field", map[string]string{AnnotationAdvancedEventSelectors: `[{"FieldSelectors":[{"Equals":["Data"]}]}]`}, func(w Wanted) bool { return w.AdvancedEventSelectors == nil }, "has no Field"},
		{"both kinds of selector", map[string]string{AnnotationEventSelectors: `[{}]`, AnnotationAdvancedEventSelectors: `[{"FieldSelectors":[{"Field":"eventCategory","Equals":["Management"]}]}]`},
			func(w Wanted) bool { return w.EventSelectors == nil && w.AdvancedEventSelectors == nil }, "not both"},
		{"insights", map[string]string{AnnotationInsightSelectors: `[{"InsightType":"ApiCallRateInsight"}]`}, func(w Wanted) bool { return len(w.InsightSelectors) == 1 }, ""},
		{"insights off", map[string]string{AnnotationInsightSelectors: `[]`}, func(w Wanted) bool { return w.InsightSelectors != nil && len(w.InsightSelectors) == 0 }, ""},
		{"a bad insight type", map[string]string{AnnotationInsightSelectors: `[{"InsightType":"Everything"}]`, AnnotationIsLogging: "true"},
			func(w Wanted) bool { return w.InsightSelectors == nil && aws.ToBool(w.IsLogging) }, "not one of"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := Parse(c.ann)
			if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Errorf("error %v, want %q", err, c.wantErr)
			}
			if !c.check(w) {
				t.Errorf("parsed %+v", w)
			}
		})
	}
}

func parse(t *testing.T, ann map[string]string) Wanted {
	t.Helper()
	w, err := Parse(ann)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	const arn = "arn:aws:cloudtrail:us-west-2:123456789012:trail/audit"

	t.Run("logging started, then stopped, and nothing when as asked", func(t *testing.T) {
		f := newFake()
		on := parse(t, map[string]string{AnnotationIsLogging: "true"})
		for i := 0; i < 2; i++ {
			if _, err := Apply(ctx, f, arn, on); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Apply(ctx, f, arn, parse(t, map[string]string{AnnotationIsLogging: "false"})); err != nil {
			t.Fatal(err)
		}
		if want := []string{"StartLogging " + arn, "StopLogging " + arn}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
	})

	t.Run("the default event selector written out is no change", func(t *testing.T) {
		f := newFake()
		done, err := Apply(ctx, f, arn, parse(t, map[string]string{AnnotationEventSelectors: `[{}]`}))
		if err != nil || len(f.calls) != 0 || len(done) != 0 {
			t.Fatalf("calls %v done %v (%v)", f.calls, done, err)
		}
	})

	t.Run("event selectors put once", func(t *testing.T) {
		f := newFake()
		w := parse(t, map[string]string{AnnotationEventSelectors: `[{"ReadWriteType":"WriteOnly","DataResources":[{"Type":"AWS::S3::Object","Values":["arn:aws:s3"]}]}]`})
		for i := 0; i < 2; i++ {
			if _, err := Apply(ctx, f, arn, w); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "PutEventSelectors [") {
			t.Fatalf("calls %v", f.calls)
		}
	})

	// moto 5.2.5 keeps advanced selectors too, but real CloudTrail clears
	// the event selectors on such a put and refuses both in one call: the
	// switch from one kind to the other is shown here.
	t.Run("advanced selectors replace event selectors, and back", func(t *testing.T) {
		f := newFake()
		adv := parse(t, map[string]string{AnnotationAdvancedEventSelectors: `[{"Name":"mgmt","FieldSelectors":[{"Field":"eventCategory","Equals":["Management"]},{"Field":"readOnly","Equals":["false"]}]}]`})
		for i := 0; i < 2; i++ {
			done, err := Apply(ctx, f, arn, adv)
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 && (len(done) != 1 || !strings.Contains(done[0].Message, "advanced")) {
				t.Fatalf("done %+v", done)
			}
		}
		if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "PutEventSelectors advanced ") || f.events != nil {
			t.Fatalf("calls %v events %v", f.calls, f.events)
		}
		if _, err := Apply(ctx, f, arn, parse(t, map[string]string{AnnotationEventSelectors: `[{"ReadWriteType":"All"}]`})); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 2 || f.advanced != nil {
			t.Fatalf("calls %v advanced %v", f.calls, f.advanced)
		}
	})

	t.Run("insight selectors put, kept, then turned off", func(t *testing.T) {
		f := newFake()
		on := parse(t, map[string]string{AnnotationInsightSelectors: `[{"InsightType":"ApiCallRateInsight"},{"InsightType":"ApiErrorRateInsight"}]`})
		for i := 0; i < 2; i++ {
			if _, err := Apply(ctx, f, arn, on); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Apply(ctx, f, arn, parse(t, map[string]string{AnnotationInsightSelectors: `[]`})); err != nil {
			t.Fatal(err)
		}
		// Off again: InsightNotEnabledException reads as none, no call.
		if _, err := Apply(ctx, f, arn, parse(t, map[string]string{AnnotationInsightSelectors: `[]`})); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 2 || f.calls[1] != "PutInsightSelectors []" {
			t.Fatalf("calls %v", f.calls)
		}
	})
}
