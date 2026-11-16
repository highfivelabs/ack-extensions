// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package operator

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ccv1alpha1 "github.com/highfivelabs/ack-extensions/cloudcontrol/apis/v1alpha1"
	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/registry"
)

// RECCC: the manager's memory follows the kinds it serves. Measured on
// kind (docs/cloudcontrol.md section 11): 51 MB resident for 2 kinds,
// 533 MB for all 1,345; the old fixed 256Mi limit killed the manager of a
// context naming no kinds.
func TestManagerMemory(t *testing.T) {
	const mi = 1 << 20
	for _, c := range []struct {
		kinds          int
		minReq, minLim int64 // at least, in MiB
		maxReq         int64
	}{
		{kinds: 2, minReq: 64, minLim: 256, maxReq: 64},
		{kinds: 0, minReq: 533, minLim: 533 * 3 / 2, maxReq: 1024}, // every kind
		{kinds: len(registry.Types), minReq: 533, minLim: 533 * 3 / 2, maxReq: 1024},
	} {
		req, lim := ManagerMemory(c.kinds)
		if req.Value() < c.minReq*mi || req.Value() > c.maxReq*mi {
			t.Errorf("%d kinds: request %s, want %d..%d Mi", c.kinds, req.String(), c.minReq, c.maxReq)
		}
		if lim.Value() < c.minLim*mi || lim.Cmp(req) < 0 {
			t.Errorf("%d kinds: limit %s, want at least %d Mi and the request", c.kinds, lim.String(), c.minLim)
		}
		if lim.Value()%mi != 0 || req.Value()%mi != 0 {
			t.Errorf("%d kinds: %s / %s not whole MiB", c.kinds, req.String(), lim.String())
		}
	}

	// The Deployment carries them.
	cc := &ccv1alpha1.CloudControlContext{ObjectMeta: metav1.ObjectMeta{Name: ccv1alpha1.ContextName, Namespace: "env"}}
	_, wantLim := ManagerMemory(0)
	for _, o := range (&ContextReconciler{Image: "x"}).PlanContext(cc, nil) {
		if d, ok := o.(*appsv1.Deployment); ok {
			got := d.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
			if got.Cmp(wantLim) != 0 {
				t.Errorf("Deployment memory limit %s, want %s", got.String(), wantLim.String())
			}
			return
		}
	}
	t.Fatal("no Deployment planned")
}
