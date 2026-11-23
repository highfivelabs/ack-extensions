// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package object

import (
	"context"
	"encoding/base64"
	"fmt"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/s3/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=s3.services.k8s.aws,resources=buckets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets,verbs=get;list;watch

// ackBucketGVK is ACK's Bucket, which bucketRef names. s3-controller is not
// a Go module here (UPSTREAM.md), so the Bucket is read as unstructured, as
// the Bucket extension reads it.
var ackBucketGVK = schema.GroupVersionKind{Group: "s3.services.k8s.aws", Version: "v1alpha1", Kind: "Bucket"}

// Two references are resolved on every reconcile and never written into
// the stored spec, as ACK's generated references are:
//
//   - bucketRef: ACK's Bucket, synced; its spec.name becomes
//     spec.bucketName;
//   - contentFrom: the ConfigMap's or Secret's key; its value becomes
//     spec.contentBase64 (bytes, whatever the source holds).

// ClearResolvedReferences removes what ResolveReferences filled in, so it
// is not written into the stored spec.
func (rm *resourceManager) ClearResolvedReferences(res acktypes.AWSResource) acktypes.AWSResource {
	ko := rm.concreteResource(res).ko.DeepCopy()
	if ko.Spec.BucketRef != nil {
		ko.Spec.BucketName = nil
	}
	if ko.Spec.ContentFrom != nil {
		ko.Spec.ContentBase64 = nil
	}
	return &resource{ko}
}

// ResolveReferences fills spec.bucketName from bucketRef and
// spec.contentBase64 from contentFrom.
func (rm *resourceManager) ResolveReferences(
	ctx context.Context,
	apiReader client.Reader,
	res acktypes.AWSResource,
) (acktypes.AWSResource, bool, error) {
	ko := rm.concreteResource(res).ko.DeepCopy()
	has := ko.Spec.BucketRef != nil || ko.Spec.ContentFrom != nil
	if ko.Spec.BucketRef != nil && ko.Spec.BucketRef.From != nil {
		name, err := resolveBucket(ctx, apiReader, ko.Namespace, ko.Spec.BucketRef.From)
		if err != nil {
			return &resource{ko}, has, err
		}
		ko.Spec.BucketName = aws.String(name)
	}
	if ko.Spec.ContentFrom != nil {
		b, err := resolveContent(ctx, apiReader, ko.Namespace, ko.Spec.ContentFrom)
		if err != nil {
			return &resource{ko}, has, err
		}
		ko.Spec.ContentBase64 = aws.String(base64.StdEncoding.EncodeToString(b))
	}
	return &resource{ko}, has, nil
}

// resolveBucket is the name of ACK's Bucket ref names, once it is synced.
func resolveBucket(ctx context.Context, apiReader client.Reader, ns string, ref *ackv1alpha1.AWSResourceReference) (string, error) {
	name := aws.ToString(ref.Name)
	if ref.Namespace != nil && *ref.Namespace != "" {
		ns = *ref.Namespace
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ackBucketGVK)
	if err := apiReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
		return "", err
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	synced := false
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		switch {
		case m["type"] == string(ackv1alpha1.ConditionTypeTerminal) && m["status"] == string(corev1.ConditionTrue):
			return "", ackerr.ResourceReferenceTerminalFor("Bucket", ns, name)
		case m["type"] == string(ackv1alpha1.ConditionTypeResourceSynced) && m["status"] == string(corev1.ConditionTrue):
			synced = true
		}
	}
	if !synced {
		return "", ackerr.ResourceReferenceNotSyncedFor("Bucket", ns, name)
	}
	bucket, _, _ := unstructured.NestedString(u.Object, "spec", "name")
	if bucket == "" {
		return "", ackerr.ResourceReferenceMissingTargetFieldFor("Bucket", ns, name, "Spec.Name")
	}
	return bucket, nil
}

// resolveContent is the value of the ConfigMap's or Secret's key. A
// ConfigMap's key is read from data, then binaryData.
func resolveContent(ctx context.Context, apiReader client.Reader, ns string, src *svcapitypes.ObjectContentSource) ([]byte, error) {
	switch {
	case src.ConfigMapKeyRef != nil:
		cm := &corev1.ConfigMap{}
		if err := apiReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: src.ConfigMapKeyRef.Name}, cm); err != nil {
			return nil, err
		}
		if v, ok := cm.Data[src.ConfigMapKeyRef.Key]; ok {
			return []byte(v), nil
		}
		if v, ok := cm.BinaryData[src.ConfigMapKeyRef.Key]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("ConfigMap %s/%s has no key %q", ns, src.ConfigMapKeyRef.Name, src.ConfigMapKeyRef.Key)
	case src.SecretKeyRef != nil:
		s := &corev1.Secret{}
		if err := apiReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: src.SecretKeyRef.Name}, s); err != nil {
			return nil, err
		}
		if v, ok := s.Data[src.SecretKeyRef.Key]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("Secret %s/%s has no key %q", ns, src.SecretKeyRef.Name, src.SecretKeyRef.Key)
	}
	return nil, fmt.Errorf("contentFrom names neither a ConfigMap nor a Secret")
}
