// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package resource

import (
	"fmt"
	"sync"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	ackmetrics "github.com/aws-controllers-k8s/runtime/pkg/metrics"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/go-logr/logr"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// factory makes managers for one type, one per account, region and role,
// as ACK's generated factories do.
type factory struct {
	t  *registry.Type
	mu sync.RWMutex
	rm map[string]*manager
}

var _ acktypes.AWSResourceManagerFactory = &factory{}

// Factories is one factory per registered type: the whole list of kinds
// the manager serves.
func Factories() []acktypes.AWSResourceManagerFactory {
	var out []acktypes.AWSResourceManagerFactory
	for _, t := range registry.Types {
		out = append(out, &factory{t: t, rm: map[string]*manager{}})
	}
	return out
}

func (f *factory) ResourceDescriptor() acktypes.AWSResourceDescriptor { return &descriptor{t: f.t} }

func (f *factory) ManagerFor(
	cfg ackcfg.Config,
	clientcfg aws.Config,
	log logr.Logger,
	_ *ackmetrics.Metrics,
	rr acktypes.Reconciler,
	id ackv1alpha1.AWSAccountID,
	region ackv1alpha1.AWSRegion,
	roleARN ackv1alpha1.AWSResourceName,
) (acktypes.AWSResourceManager, error) {
	key := fmt.Sprintf("%s/%s/%s", id, region, roleARN)
	f.mu.RLock()
	m, ok := f.rm[key]
	f.mu.RUnlock()
	if ok {
		return m, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m = &manager{
		t: f.t, s: f.t.Schema(), cfg: cfg, log: log, rr: rr, account: id, region: region,
		api: cloudcontrol.NewFromConfig(clientcfg),
	}
	f.rm[key] = m
	return m, nil
}

func (f *factory) IsAdoptable() bool { return true }

func (f *factory) RequeueOnSuccessSeconds() int { return 0 }
