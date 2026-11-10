// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package access_key

// The secret access key. CreateAccessKey returns it once and never again,
// so it goes into a Kubernetes Secret in the object's namespace, owned by
// the object, and nowhere else: never into the object (the generator
// ignores the field), never into a log, an error, an event or the status.
// The Secret is made once and never written over; rotation is a new
// AccessKey (docs/plan.md, the AccessKey section).
//
// The Secret is also the record of which key the object made: it holds the
// key's ID beside the secret, so an object whose status lost the ID (a
// status write that failed after the create) finds its key again instead
// of making a second one.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	ackcompare "github.com/aws-controllers-k8s/runtime/pkg/compare"
	ackerr "github.com/aws-controllers-k8s/runtime/pkg/errors"
	svcsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	svcsdktypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlrt "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/iam/apis/v1alpha1"
)

// The manager may make and delete the Secrets it writes keys into; ACK's
// runtime only reads and patches Secrets.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;delete

const (
	// KeyIDField and SecretKeyField are the Secret's two keys, the names the
	// AWS SDKs read from the environment, so a pod can take both with envFrom.
	KeyIDField     = "AWS_ACCESS_KEY_ID"
	SecretKeyField = "AWS_SECRET_ACCESS_KEY"
)

var (
	kubeOnce sync.Once
	kube     client.Client
	kubeErr  error
)

// kubeClient is a client of the manager's own cluster, from the same
// configuration (--kubeconfig, KUBECONFIG or in-cluster) as the manager's.
// ACK's runtime gives the resource manager no client that can make a Secret.
func kubeClient() (client.Client, error) {
	kubeOnce.Do(func() {
		cfg, err := ctrlrt.GetConfig()
		if err != nil {
			kubeErr = err
			return
		}
		kube, kubeErr = client.New(cfg, client.Options{})
	})
	return kube, kubeErr
}

// secretName is the spec's, or the object's name when the spec has none.
func secretName(ko *svcapitypes.AccessKey) string {
	if ko.Spec.SecretName != nil && *ko.Spec.SecretName != "" {
		return *ko.Spec.SecretName
	}
	return ko.Name
}

// ownedBy says whether the object owns the Secret.
func ownedBy(s *corev1.Secret, ko *svcapitypes.AccessKey) bool {
	for _, ref := range s.OwnerReferences {
		if ref.UID == ko.UID {
			return true
		}
	}
	return false
}

// getSecret reads the object's Secret; nil when there is none.
func getSecret(ctx context.Context, ko *svcapitypes.AccessKey) (*corev1.Secret, error) {
	c, err := kubeClient()
	if err != nil {
		return nil, err
	}
	s := &corev1.Secret{}
	err = c.Get(ctx, types.NamespacedName{Namespace: ko.Namespace, Name: secretName(ko)}, s)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// keyIDFromSecret is the ID of the key the object made, from the Secret the
// object owns; empty when there is none. The read uses it when the status
// has no ID.
func (rm *resourceManager) keyIDFromSecret(ctx context.Context, ko *svcapitypes.AccessKey) (string, error) {
	s, err := getSecret(ctx, ko)
	if err != nil || s == nil || !ownedBy(s, ko) {
		return "", err
	}
	return string(s.Data[KeyIDField]), nil
}

// secretFree refuses a create whose Secret exists already: a key is never
// written over another. When the object owns that Secret, its key was
// deleted outside Kubernetes (or never read back); either way only a new
// object, or deleting the Secret, makes a new key.
func (rm *resourceManager) secretFree(ctx context.Context, ko *svcapitypes.AccessKey) error {
	s, err := getSecret(ctx, ko)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	if ownedBy(s, ko) {
		return ackerr.NewTerminalError(fmt.Errorf(
			"Secret %s/%s holds access key %s, which IAM no longer has; it is never written over: delete the Secret to make a new key, or make a new AccessKey",
			s.Namespace, s.Name, string(s.Data[KeyIDField])))
	}
	return ackerr.NewTerminalError(fmt.Errorf(
		"Secret %s/%s exists and is not this AccessKey's; it is never written over: name another in spec.secretName",
		s.Namespace, s.Name))
}

// newSecret is the Secret holding the key, owned by the object.
func newSecret(ko *svcapitypes.AccessKey, keyID, secret string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName(ko),
			Namespace: ko.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "ack-extensions-iam-manager"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: svcapitypes.GroupVersion.String(),
				Kind:       "AccessKey",
				Name:       ko.Name,
				UID:        ko.UID,
				Controller: func() *bool { b := true; return &b }(),
			}},
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: func() *bool { b := true; return &b }(),
		Data: map[string][]byte{
			KeyIDField:     []byte(keyID),
			SecretKeyField: []byte(secret),
		},
	}
}

// storeKey writes the new key into the object's Secret and clears the
// secret from the response. If the Secret cannot be made, the key is
// deleted, since nobody could ever read its secret again, and the create is
// tried again later. Its errors never carry the secret, and never wrap an
// AWS error (the runtime would take the object as unmanaged).
func (rm *resourceManager) storeKey(ctx context.Context, ko *svcapitypes.AccessKey, key *svcsdktypes.AccessKey) error {
	if key == nil || key.AccessKeyId == nil || key.SecretAccessKey == nil {
		return errors.New("CreateAccessKey returned no key")
	}
	s := newSecret(ko, *key.AccessKeyId, *key.SecretAccessKey)
	key.SecretAccessKey = nil
	c, err := kubeClient()
	if err == nil {
		err = c.Create(ctx, s)
	}
	clear(s.Data)
	if err == nil {
		return nil
	}
	cause := fmt.Sprintf("the access key's Secret %s/%s could not be made (%s)", ko.Namespace, secretName(ko), err)
	_, derr := rm.sdkapi.DeleteAccessKey(ctx, &svcsdk.DeleteAccessKeyInput{AccessKeyId: key.AccessKeyId, UserName: key.UserName})
	rm.metrics.RecordAPICall("DELETE", "DeleteAccessKey", derr)
	if derr != nil {
		// Its secret is lost, so the key is unusable; making another could
		// leak keys up to IAM's limit of two a user.
		return ackerr.NewTerminalError(fmt.Errorf("%s, and the key %s could not be deleted (%s): delete it by hand", cause, *key.AccessKeyId, derr.Error()))
	}
	if apierrors.IsAlreadyExists(err) {
		return ackerr.NewTerminalError(fmt.Errorf("%s; the key was deleted; it is never written over: name another in spec.secretName", cause))
	}
	return fmt.Errorf("%s; the key was deleted and is made again", cause)
}

// deleteSecret deletes the object's Secret, if the object owns it.
func (rm *resourceManager) deleteSecret(ctx context.Context, ko *svcapitypes.AccessKey) error {
	s, err := getSecret(ctx, ko)
	if err != nil || s == nil || !ownedBy(s, ko) {
		return err
	}
	c, err := kubeClient()
	if err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, s))
}

// customUpdateAccessKey refuses every change: the fields are immutable (the
// CRD refuses a changed userName or secretName; this, a userRef naming
// another user).
func (rm *resourceManager) customUpdateAccessKey(
	ctx context.Context,
	desired *resource,
	latest *resource,
	delta *ackcompare.Delta,
) (*resource, error) {
	paths := make([]string, 0, len(delta.Differences))
	for _, d := range delta.Differences {
		b, _ := d.Path.MarshalJSON()
		paths = append(paths, string(b))
	}
	return nil, ackerr.NewTerminalError(fmt.Errorf(
		"an AccessKey cannot change (%v): rotate by making a new AccessKey and deleting this one", paths))
}
