// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package key

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/kms"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// fakeKMS records the calls that change the key; the state is one key's.
type fakeKMS struct {
	state      svcsdktypes.KeyState
	rotationOn bool
	period     *int32
	calls      []string
}

func (f *fakeKMS) DescribeKey(context.Context, *svcsdk.DescribeKeyInput, ...func(*svcsdk.Options)) (*svcsdk.DescribeKeyOutput, error) {
	return &svcsdk.DescribeKeyOutput{KeyMetadata: &svcsdktypes.KeyMetadata{KeyState: f.state}}, nil
}

func (f *fakeKMS) GetKeyRotationStatus(context.Context, *svcsdk.GetKeyRotationStatusInput, ...func(*svcsdk.Options)) (*svcsdk.GetKeyRotationStatusOutput, error) {
	return &svcsdk.GetKeyRotationStatusOutput{KeyRotationEnabled: f.rotationOn, RotationPeriodInDays: f.period}, nil
}

func (f *fakeKMS) EnableKeyRotation(_ context.Context, in *svcsdk.EnableKeyRotationInput, _ ...func(*svcsdk.Options)) (*svcsdk.EnableKeyRotationOutput, error) {
	f.calls = append(f.calls, "EnableKeyRotation "+aws.ToString(in.KeyId)+" "+itoa(in.RotationPeriodInDays))
	f.rotationOn, f.period = true, in.RotationPeriodInDays
	return &svcsdk.EnableKeyRotationOutput{}, nil
}

func (f *fakeKMS) EnableKey(context.Context, *svcsdk.EnableKeyInput, ...func(*svcsdk.Options)) (*svcsdk.EnableKeyOutput, error) {
	f.calls = append(f.calls, "EnableKey")
	f.state = svcsdktypes.KeyStateEnabled
	return &svcsdk.EnableKeyOutput{}, nil
}

func (f *fakeKMS) DisableKey(context.Context, *svcsdk.DisableKeyInput, ...func(*svcsdk.Options)) (*svcsdk.DisableKeyOutput, error) {
	f.calls = append(f.calls, "DisableKey")
	f.state = svcsdktypes.KeyStateDisabled
	return &svcsdk.DisableKeyOutput{}, nil
}

func itoa(p *int32) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(int(*p))
}

func TestParse(t *testing.T) {
	cases := []struct {
		ann     map[string]string
		want    Wanted
		wantErr string
	}{
		{nil, Wanted{}, ""},
		{map[string]string{AnnotationRotationPeriod: "180"}, Wanted{RotationPeriod: 180}, ""},
		{map[string]string{AnnotationRotationPeriod: " 90 "}, Wanted{RotationPeriod: 90}, ""},
		{map[string]string{AnnotationRotationPeriod: "2560"}, Wanted{RotationPeriod: 2560}, ""},
		{map[string]string{AnnotationRotationPeriod: "89"}, Wanted{}, "outside 90 to 2560"},
		{map[string]string{AnnotationRotationPeriod: "2561"}, Wanted{}, "outside 90 to 2560"},
		{map[string]string{AnnotationRotationPeriod: "a year"}, Wanted{}, "not a whole number"},
		{map[string]string{AnnotationEnabled: "false"}, Wanted{Enabled: aws.Bool(false)}, ""},
		{map[string]string{AnnotationEnabled: "True"}, Wanted{Enabled: aws.Bool(true)}, ""},
		{map[string]string{AnnotationEnabled: "maybe"}, Wanted{}, "neither true nor false"},
		// One bad annotation does not drop the other.
		{map[string]string{AnnotationEnabled: "false", AnnotationRotationPeriod: "1"}, Wanted{Enabled: aws.Bool(false)}, "outside"},
	}
	for _, c := range cases {
		got, err := Parse(c.ann)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("Parse(%v) error %v, want %q", c.ann, err, c.wantErr)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%v) = %+v, want %+v", c.ann, got, c.want)
		}
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	t.Run("the period is sent with EnableKeyRotation", func(t *testing.T) {
		f := &fakeKMS{state: svcsdktypes.KeyStateEnabled, rotationOn: true, period: aws.Int32(365)}
		done, retry, err := Apply(ctx, f, "k1", true, Wanted{RotationPeriod: 180})
		if err != nil || retry {
			t.Fatal(err, retry)
		}
		if want := []string{"EnableKeyRotation k1 180"}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
		if len(done) != 1 || done[0].Reason != ReasonRotationPeriodSet {
			t.Fatalf("done %+v", done)
		}
		// A second pass reads the period and calls nothing.
		f.calls = nil
		if done, _, _ := Apply(ctx, f, "k1", true, Wanted{RotationPeriod: 180}); len(f.calls) != 0 || len(done) != 0 {
			t.Fatalf("second pass: calls %v, done %+v", f.calls, done)
		}
	})
	t.Run("no period without rotation on", func(t *testing.T) {
		f := &fakeKMS{state: svcsdktypes.KeyStateEnabled}
		if _, _, err := Apply(ctx, f, "k1", false, Wanted{RotationPeriod: 180}); err != nil || len(f.calls) != 0 {
			t.Fatalf("calls %v (%v)", f.calls, err)
		}
	})
	t.Run("disable, then enable", func(t *testing.T) {
		f := &fakeKMS{state: svcsdktypes.KeyStateEnabled}
		if _, _, err := Apply(ctx, f, "k1", false, Wanted{Enabled: aws.Bool(false)}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(ctx, f, "k1", false, Wanted{Enabled: aws.Bool(false)}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(ctx, f, "k1", false, Wanted{Enabled: aws.Bool(true)}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"DisableKey", "EnableKey"}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
	})
	t.Run("a disabled key gets no period", func(t *testing.T) {
		f := &fakeKMS{state: svcsdktypes.KeyStateEnabled}
		done, _, err := Apply(ctx, f, "k1", true, Wanted{Enabled: aws.Bool(false), RotationPeriod: 90})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"DisableKey"}; !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls %v, want %v", f.calls, want)
		}
		if done[len(done)-1].Reason != ReasonRotationPeriodIgnored {
			t.Fatalf("done %+v", done)
		}
	})
	t.Run("a key pending deletion is left alone; one being made is retried", func(t *testing.T) {
		f := &fakeKMS{state: svcsdktypes.KeyStatePendingDeletion}
		if _, retry, err := Apply(ctx, f, "k1", true, Wanted{Enabled: aws.Bool(true), RotationPeriod: 90}); err != nil || retry || len(f.calls) != 0 {
			t.Fatalf("calls %v retry %v (%v)", f.calls, retry, err)
		}
		f = &fakeKMS{state: svcsdktypes.KeyStateCreating}
		if _, retry, err := Apply(ctx, f, "k1", true, Wanted{RotationPeriod: 90}); err != nil || !retry || len(f.calls) != 0 {
			t.Fatalf("calls %v retry %v (%v)", f.calls, retry, err)
		}
	})
}
