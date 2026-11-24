// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ObjectContentSource is a key of a ConfigMap or a Secret in the object's
// namespace whose value is the object's content.
// +kubebuilder:validation:XValidation:rule="has(self.configMapKeyRef) != has(self.secretKeyRef)",message="exactly one of configMapKeyRef and secretKeyRef"
type ObjectContentSource struct {
	// A key of a ConfigMap (its data, or its binaryData).
	// +kubebuilder:validation:Optional
	ConfigMapKeyRef *ObjectKeyRef `json:"configMapKeyRef,omitempty"`
	// A key of a Secret.
	// +kubebuilder:validation:Optional
	SecretKeyRef *ObjectKeyRef `json:"secretKeyRef,omitempty"`
}

// ObjectKeyRef names a key of a ConfigMap or Secret in the object's
// namespace.
type ObjectKeyRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// ObjectSpec is one object in an S3 bucket, put with PutObject, read with
// HeadObject and deleted with DeleteObject: Terraform's aws_s3_object.
// S3 objects have no CloudFormation type and ACK's s3-controller has no
// kind for them (docs/plan.md open question 27, answered 2026-11-23).
//
// The content is one of content (text), contentBase64 (bytes) or
// contentFrom (a key of a ConfigMap or Secret, put again when its value
// changes). An object is small: the API server bounds an object, about
// 1.5 MB, so inline content is bounded too.
// +kubebuilder:validation:XValidation:rule="has(self.bucketName) != has(self.bucketRef)",message="exactly one of bucketName and bucketRef"
// +kubebuilder:validation:XValidation:rule="(has(self.content) ? 1 : 0) + (has(self.contentBase64) ? 1 : 0) + (has(self.contentFrom) ? 1 : 0) == 1",message="exactly one of content, contentBase64 and contentFrom"
type ObjectSpec struct {
	// The bucket's name. Immutable.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	BucketName *string `json:"bucketName,omitempty"`
	// ACK's Bucket (s3.services.k8s.aws) to put the object in: its
	// spec.name, once it is synced.
	// +kubebuilder:validation:Optional
	BucketRef *ackv1alpha1.AWSResourceReferenceWrapper `json:"bucketRef,omitempty"`
	// The object's key. Immutable: another key is another object.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable once set"
	Key *string `json:"key"`
	// The content, as text (Terraform's content).
	// +kubebuilder:validation:Optional
	Content *string `json:"content,omitempty"`
	// The content, base64-encoded (Terraform's content_base64).
	// +kubebuilder:validation:Optional
	ContentBase64 *string `json:"contentBase64,omitempty"`
	// The content from a key of a ConfigMap or Secret. A changed value is
	// put again (the SHA-256 of what was put is kept in the annotation
	// s3.aws.hfl.dev/last-content-sha256).
	// +kubebuilder:validation:Optional
	ContentFrom *ObjectContentSource `json:"contentFrom,omitempty"`
	// +kubebuilder:validation:Optional
	ContentType *string `json:"contentType,omitempty"`
	// +kubebuilder:validation:Optional
	CacheControl *string `json:"cacheControl,omitempty"`
	// +kubebuilder:validation:Optional
	ContentDisposition *string `json:"contentDisposition,omitempty"`
	// +kubebuilder:validation:Optional
	ContentEncoding *string `json:"contentEncoding,omitempty"`
	// +kubebuilder:validation:Optional
	ContentLanguage *string `json:"contentLanguage,omitempty"`
	// User-defined metadata (x-amz-meta-*), keys in lower case as S3 keeps
	// them.
	// +kubebuilder:validation:Optional
	Metadata map[string]*string `json:"metadata,omitempty"`
	// AES256, aws:kms or aws:kms:dsse; without it the bucket's default
	// encryption applies.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=AES256;"aws:kms";"aws:kms:dsse"
	ServerSideEncryption *string `json:"serverSideEncryption,omitempty"`
	// The KMS key for aws:kms, by ID or ARN.
	// +kubebuilder:validation:Optional
	KMSKeyID *string `json:"kmsKeyID,omitempty"`
	// STANDARD, STANDARD_IA, ...; without it STANDARD.
	// +kubebuilder:validation:Optional
	StorageClass *string `json:"storageClass,omitempty"`
	// The object's tags (PutObjectTagging), at most 10.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxItems=10
	Tags []*Tag `json:"tags,omitempty"`
}

// ObjectStatus defines the observed state of Object.
type ObjectStatus struct {
	// +kubebuilder:validation:Optional
	ACKResourceMetadata *ackv1alpha1.ResourceMetadata `json:"ackResourceMetadata"`
	// +kubebuilder:validation:Optional
	Conditions []*ackv1alpha1.Condition `json:"conditions"`
	// The ETag of the content put; a different ETag read means the object
	// was changed outside Kubernetes, and it is put again.
	// +kubebuilder:validation:Optional
	ETag *string `json:"eTag,omitempty"`
	// The version put, in a bucket with versioning.
	// +kubebuilder:validation:Optional
	VersionID *string `json:"versionID,omitempty"`
}

// Object is the Schema for the Objects API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=aws;hfl,shortName=hflobject
// +kubebuilder:printcolumn:name="BUCKET",type=string,JSONPath=`.spec.bucketName`
// +kubebuilder:printcolumn:name="KEY",type=string,JSONPath=`.spec.key`
// +kubebuilder:printcolumn:name="SYNCED",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"ACK.ResourceSynced\")].status"
// +kubebuilder:printcolumn:name="AGE",type="date",priority=0,JSONPath=".metadata.creationTimestamp"
type Object struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ObjectSpec   `json:"spec,omitempty"`
	Status            ObjectStatus `json:"status,omitempty"`
}

// ObjectList contains a list of Object.
// +kubebuilder:object:root=true
type ObjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Object `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Object{}, &ObjectList{})
}
