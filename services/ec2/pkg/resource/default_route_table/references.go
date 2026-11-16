// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package default_route_table

import (
	"context"
	"fmt"

	ackec2 "github.com/aws-controllers-k8s/ec2-controller/apis/v1alpha1"
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	ackrt "github.com/aws-controllers-k8s/runtime/pkg/runtime"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// vpcRef names ACK's VPC, and a route's gatewayRef, natGatewayRef,
// transitGatewayRef, vpcEndpointRef and vpcPeeringConnectionRef ACK's
// InternetGateway, NATGateway, TransitGateway, VPCEndpoint and
// VPCPeeringConnection (ec2.services.k8s.aws), resolved as ACK's RouteTable
// resolves them (its generated references.go): the object must be synced
// and not terminal, and its ID in status set.

// +kubebuilder:rbac:groups=ec2.services.k8s.aws,resources=vpcs;internetgateways;natgateways;transitgateways;vpcendpoints;vpcpeeringconnections,verbs=get;list;watch

func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.VPCRef != nil {
		ko.Spec.VPCID = nil
	}
	for _, r := range ko.Spec.Routes {
		if r == nil {
			continue
		}
		if r.GatewayRef != nil {
			r.GatewayID = nil
		}
		if r.NATGatewayRef != nil {
			r.NATGatewayID = nil
		}
		if r.TransitGatewayRef != nil {
			r.TransitGatewayID = nil
		}
		if r.VPCEndpointRef != nil {
			r.VPCEndpointID = nil
		}
		if r.VPCPeeringConnectionRef != nil {
			r.VPCPeeringConnectionID = nil
		}
	}
	return &resource{ko}
}

// ackStatus checks an ACK object's conditions as ACK's references do.
func ackStatus(kind, namespace, name string, conds []*ackv1alpha1.Condition) error {
	synced := false
	for _, cond := range conds {
		if cond.Type == ackv1alpha1.ConditionTypeTerminal && cond.Status == corev1.ConditionTrue {
			return ackerr.ResourceReferenceTerminalFor(kind, namespace, name)
		}
		if cond.Type == ackv1alpha1.ConditionTypeResourceSynced && cond.Status == corev1.ConditionTrue {
			synced = true
		}
	}
	if !synced {
		return ackerr.ResourceReferenceNotSyncedFor(kind, namespace, name)
	}
	return nil
}

func (rm *resourceManager) refName(namespace string, ref *ackv1alpha1.AWSResourceReferenceWrapper, field string) (types.NamespacedName, error) {
	if ref.From == nil || ref.From.Name == nil || *ref.From.Name == "" {
		return types.NamespacedName{}, fmt.Errorf("provided resource reference is nil or empty: %s", field)
	}
	ns, _, err := ackrt.ValidateCrossNamespaceReference(rm.cfg.EnableCrossNamespace, namespace, ref.From.Namespace, *ref.From.Name)
	if err != nil {
		return types.NamespacedName{}, err
	}
	return types.NamespacedName{Namespace: ns, Name: *ref.From.Name}, nil
}

// resolve reads one ACK object a reference names and returns the ID in
// its status.
func (rm *resourceManager) resolve(ctx context.Context, apiReader client.Reader, namespace string, ref *ackv1alpha1.AWSResourceReferenceWrapper, field string) (*string, error) {
	nn, err := rm.refName(namespace, ref, field)
	if err != nil {
		return nil, err
	}
	var (
		obj   client.Object
		kind  string
		id    func() *string
		conds func() []*ackv1alpha1.Condition
	)
	switch field {
	case "VPCRef":
		o := &ackec2.VPC{}
		obj, kind, id, conds = o, "VPC", func() *string { return o.Status.VPCID }, func() []*ackv1alpha1.Condition { return o.Status.Conditions }
	case "Routes.GatewayRef":
		o := &ackec2.InternetGateway{}
		obj, kind, id, conds = o, "InternetGateway", func() *string { return o.Status.InternetGatewayID }, func() []*ackv1alpha1.Condition { return o.Status.Conditions }
	case "Routes.NATGatewayRef":
		o := &ackec2.NATGateway{}
		obj, kind, id, conds = o, "NATGateway", func() *string { return o.Status.NATGatewayID }, func() []*ackv1alpha1.Condition { return o.Status.Conditions }
	case "Routes.TransitGatewayRef":
		o := &ackec2.TransitGateway{}
		obj, kind, id, conds = o, "TransitGateway", func() *string { return o.Status.TransitGatewayID }, func() []*ackv1alpha1.Condition { return o.Status.Conditions }
	case "Routes.VPCEndpointRef":
		o := &ackec2.VPCEndpoint{}
		obj, kind, id, conds = o, "VPCEndpoint", func() *string { return o.Status.VPCEndpointID }, func() []*ackv1alpha1.Condition { return o.Status.Conditions }
	case "Routes.VPCPeeringConnectionRef":
		o := &ackec2.VPCPeeringConnection{}
		obj, kind, id, conds = o, "VPCPeeringConnection", func() *string { return o.Status.VPCPeeringConnectionID }, func() []*ackv1alpha1.Condition { return o.Status.Conditions }
	default:
		return nil, fmt.Errorf("no reference %s", field)
	}
	if err := apiReader.Get(ctx, nn, obj); err != nil {
		return nil, err
	}
	if err := ackStatus(kind, nn.Namespace, nn.Name, conds()); err != nil {
		return nil, err
	}
	if id() == nil {
		return nil, ackerr.ResourceReferenceMissingTargetFieldFor(kind, nn.Namespace, nn.Name, "Status ID")
	}
	return id(), nil
}

func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko.DeepCopy()
	has := ko.Spec.VPCRef != nil
	if ko.Spec.VPCRef != nil && ko.Spec.VPCID != nil {
		return &resource{ko}, has, ackerr.ResourceReferenceAndIDNotSupportedFor("VPCID", "VPCRef")
	}
	if ref := ko.Spec.VPCRef; ref != nil && ref.From != nil {
		id, err := rm.resolve(ctx, apiReader, ko.GetNamespace(), ref, "VPCRef")
		if err != nil {
			return &resource{ko}, has, err
		}
		ko.Spec.VPCID = id
	}
	for _, r := range ko.Spec.Routes {
		if r == nil {
			continue
		}
		for _, f := range []struct {
			name string
			ref  *ackv1alpha1.AWSResourceReferenceWrapper
			to   **string
		}{
			{"Routes.GatewayRef", r.GatewayRef, &r.GatewayID},
			{"Routes.NATGatewayRef", r.NATGatewayRef, &r.NATGatewayID},
			{"Routes.TransitGatewayRef", r.TransitGatewayRef, &r.TransitGatewayID},
			{"Routes.VPCEndpointRef", r.VPCEndpointRef, &r.VPCEndpointID},
			{"Routes.VPCPeeringConnectionRef", r.VPCPeeringConnectionRef, &r.VPCPeeringConnectionID},
		} {
			if f.ref == nil || f.ref.From == nil {
				continue
			}
			has = true
			if *f.to != nil {
				return &resource{ko}, has, fmt.Errorf("a route gives both %s and its ID", f.name)
			}
			id, err := rm.resolve(ctx, apiReader, ko.GetNamespace(), f.ref, f.name)
			if err != nil {
				return &resource{ko}, has, err
			}
			*f.to = id
		}
	}
	return &resource{ko}, has, nil
}
