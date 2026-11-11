// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

//go:build live

// sweep deletes what live runs left in the sandbox account
// (docs/live-runs.md), through Cloud Control for every served type that
// takes tags: a resource is a live run's when it carries the tag
// hfl.dev/live-run. Only with the live package's checks (opt-in,
// allowlisted account, federated credentials).
//
//	go run -tags live ./hack/live/sweep            delete leftovers of runs older than 2h
//	go run -tags live ./hack/live/sweep --run ID   delete everything of run ID
//	go run -tags live ./hack/live/sweep --list     list every live-run resource
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/test/live"
)

func main() {
	run := flag.String("run", "", "delete everything of this run")
	list := flag.Bool("list", false, "list only")
	older := flag.Duration("older", 2*time.Hour, "without --run: delete runs started longer ago than this")
	flag.Parse()
	if err := sweep(context.Background(), *run, *list, *older); err != nil {
		fmt.Fprintln(os.Stderr, "sweep:", err)
		os.Exit(1)
	}
}

// started is when a run began, from its ID (ackx-MMDDhhmm-xxxx, UTC).
func started(id string, now time.Time) (time.Time, bool) {
	p := strings.Split(id, "-")
	if len(p) != 3 || len(p[1]) != 8 {
		return time.Time{}, false
	}
	t, err := time.Parse("0102150405", p[1]+"00")
	if err != nil {
		return time.Time{}, false
	}
	t = t.AddDate(now.Year(), 0, 0)
	if t.After(now) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, true
}

func sweep(ctx context.Context, run string, list bool, older time.Duration) error {
	r, err := live.Account(ctx, ".")
	if err != nil {
		return err
	}
	cc := cloudcontrol.NewFromConfig(r.Config)
	now := time.Now().UTC()
	found, deleted, failed := 0, 0, 0
	for _, ty := range registry.Types {
		// The live-test role holds only the live kinds' permissions (plan
		// question 21), and no live run makes any other kind.
		if !ty.Live {
			continue
		}
		if ty.NeverDelete {
			fmt.Printf("sweep: %s is never deleted; not swept\n", ty.TypeName)
			continue
		}
		tp := ty.Schema().TagProperty()
		if tp == "" {
			fmt.Printf("sweep: %s takes no tags; a run's tests delete their own\n", ty.TypeName)
			continue
		}
		field := strings.TrimPrefix(tp, "/properties/")
		p := cloudcontrol.NewListResourcesPaginator(cc, &cloudcontrol.ListResourcesInput{TypeName: aws.String(ty.TypeName)})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				fmt.Printf("sweep: %s: list: %v\n", ty.TypeName, err)
				failed++
				break
			}
			for _, d := range page.ResourceDescriptions {
				var props map[string]any
				_ = json.Unmarshal([]byte(aws.ToString(d.Properties)), &props)
				id := runOf(props[field])
				if id == "" {
					continue
				}
				found++
				due := run != "" && id == run
				if run == "" {
					if t, ok := started(id, now); ok && now.Sub(t) > older {
						due = true
					}
				}
				fmt.Printf("sweep: %s %s (run %s)%s\n", ty.TypeName, aws.ToString(d.Identifier), id, map[bool]string{true: ": deleting", false: ""}[due && !list])
				if !due || list {
					continue
				}
				if err := del(ctx, cc, ty.TypeName, aws.ToString(d.Identifier)); err != nil {
					fmt.Printf("sweep: %s %s: %v\n", ty.TypeName, aws.ToString(d.Identifier), err)
					failed++
					continue
				}
				deleted++
			}
		}
	}
	fmt.Printf("sweep: %d live-test resource(s) found, %d deleted, %d failure(s)\n", found, deleted, failed)
	if failed > 0 {
		return fmt.Errorf("%d failure(s)", failed)
	}
	return nil
}

// runOf is the run tag's value in a tag list, or "".
func runOf(tags any) string {
	l, _ := tags.([]any)
	for _, t := range l {
		m, _ := t.(map[string]any)
		if k, _ := m["Key"].(string); k == live.RunTag {
			v, _ := m["Value"].(string)
			return v
		}
	}
	return ""
}

func del(ctx context.Context, cc *cloudcontrol.Client, typeName, id string) error {
	out, err := cc.DeleteResource(ctx, &cloudcontrol.DeleteResourceInput{TypeName: aws.String(typeName), Identifier: aws.String(id)})
	if err != nil {
		return err
	}
	tok := out.ProgressEvent.RequestToken
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Second) {
		st, err := cc.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{RequestToken: tok})
		if err != nil {
			return err
		}
		switch st.ProgressEvent.OperationStatus {
		case cctypes.OperationStatusSuccess:
			return nil
		case cctypes.OperationStatusFailed, cctypes.OperationStatusCancelComplete:
			if st.ProgressEvent.ErrorCode == cctypes.HandlerErrorCodeNotFound {
				return nil
			}
			return fmt.Errorf("%s: %s", st.ProgressEvent.ErrorCode, aws.ToString(st.ProgressEvent.StatusMessage))
		}
	}
	return fmt.Errorf("delete not finished within 10 minutes")
}
