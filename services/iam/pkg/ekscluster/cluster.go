// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package ekscluster is ACK's eks Cluster (eks.services.k8s.aws/v1alpha1,
// eks-controller v1.26.0, apis/v1alpha1/cluster.go), reduced to what
// iam.aws.hfl.dev OpenIDConnectProvider.spec.urlRef reads.
package ekscluster

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Cluster is ACK's eks Cluster reduced to its conditions and its OIDC
// issuer (status.identity.oidc.issuer). It mirrors the fields instead of
// importing eks-controller's type, because every eks-controller release
// needs aws-sdk-go-v2 v1.45.1 or later, past the version this repository
// keeps (UPSTREAM.md). It lives outside apis/ so controller-gen makes no
// CRD of it; services/iam/apis/v1alpha1 aliases it for the generated
// references code, and it is registered in the scheme under eks's group
// and kind (AddToScheme, cmd/controller/handwritten.go).
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            ClusterStatus `json:"status,omitempty"`
}

// ClusterStatus is the part of ACK's eks ClusterStatus a reference reads.
type ClusterStatus struct {
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata,omitempty"`
	Conditions          []*ackv1alpha1.Condition      `json:"conditions"`
	Identity            *ClusterIdentity              `json:"identity,omitempty"`
}

// ClusterIdentity is ACK's eks Identity: the cluster's identity provider.
type ClusterIdentity struct {
	OIDC *ClusterOIDC `json:"oidc,omitempty"`
}

// ClusterOIDC is ACK's eks OIDC: the issuer URL of the cluster's OpenID
// Connect identity provider (https://oidc.eks.<region>.amazonaws.com/id/<id>).
type ClusterOIDC struct {
	Issuer *string `json:"issuer,omitempty"`
}

// The manager reads the Cluster a reference names.
// +kubebuilder:rbac:groups=eks.services.k8s.aws,resources=clusters,verbs=get;list

// GVK is the group, version and kind Cluster is read as.
var GVK = schema.GroupVersionKind{Group: "eks.services.k8s.aws", Version: "v1alpha1", Kind: "Cluster"}

// ClusterList is a list of Cluster, for the scheme.
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

// AddToScheme registers Cluster and ClusterList under eks's group.
func AddToScheme(s *runtime.Scheme) error {
	gv := GVK.GroupVersion()
	s.AddKnownTypeWithName(GVK, &Cluster{})
	s.AddKnownTypeWithName(gv.WithKind("ClusterList"), &ClusterList{})
	metav1.AddToGroupVersion(s, gv)
	return nil
}

// DeepCopyInto copies the receiver into out.
func (in *ClusterOIDC) DeepCopyInto(out *ClusterOIDC) {
	*out = *in
	if in.Issuer != nil {
		v := *in.Issuer
		out.Issuer = &v
	}
}

// DeepCopyInto copies the receiver into out.
func (in *ClusterIdentity) DeepCopyInto(out *ClusterIdentity) {
	*out = *in
	if in.OIDC != nil {
		out.OIDC = new(ClusterOIDC)
		in.OIDC.DeepCopyInto(out.OIDC)
	}
}

// DeepCopyInto copies the receiver into out.
func (in *ClusterStatus) DeepCopyInto(out *ClusterStatus) {
	*out = *in
	if in.ACKResourceMetadata != nil {
		out.ACKResourceMetadata = in.ACKResourceMetadata.DeepCopy()
	}
	if in.Conditions != nil {
		out.Conditions = make([]*ackv1alpha1.Condition, len(in.Conditions))
		for i, c := range in.Conditions {
			if c != nil {
				out.Conditions[i] = c.DeepCopy()
			}
		}
	}
	if in.Identity != nil {
		out.Identity = new(ClusterIdentity)
		in.Identity.DeepCopyInto(out.Identity)
	}
}

// DeepCopyInto copies the receiver into out.
func (in *Cluster) DeepCopyInto(out *Cluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy copies the receiver.
func (in *Cluster) DeepCopy() *Cluster {
	if in == nil {
		return nil
	}
	out := new(Cluster)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject copies the receiver as a runtime.Object.
func (in *Cluster) DeepCopyObject() runtime.Object { return in.DeepCopy() }

// DeepCopyObject copies the receiver as a runtime.Object.
func (in *ClusterList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(ClusterList)
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Cluster, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
	return out
}
