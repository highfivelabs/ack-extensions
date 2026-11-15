// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package extend runs controllers that extend ACK's own kinds: a field ACK's
// kind lacks is set on ACK's object as an annotation in our group
// (<service>.aws.hfl.dev/<field>), and our manager beside ACK's controller
// acts on it (docs/extensions.md; docs/plan.md open question 1, answered
// 2026-11-13). ACK's controller, its CRD and its status are left alone:
// our controller reads ACK's object, calls AWS, records Events on the
// object and, where a delete needs it, holds a finalizer of its own.
//
// A service's hand-written code registers its extensions (Register, in an
// init function of cmd/controller/handwritten.go); the generated main.go
// calls Setup once ACK's service controller is bound to the manager
// (patches/code-generator/0004).
package extend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	ackv1alpha1 "github.com/aws-controllers-k8s/runtime/apis/core/v1alpha1"
	ackcfg "github.com/aws-controllers-k8s/runtime/pkg/config"
	acktypes "github.com/aws-controllers-k8s/runtime/pkg/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Env is what an extension's controller gets: the manager, ACK's service
// controller (for AWS configuration made the way ACK makes it) and the
// configuration ACK's flags gave the manager.
type Env struct {
	Manager ctrl.Manager
	Service acktypes.ServiceController
	Config  ackcfg.Config
}

// SetupFunc adds one extension's controller to the manager.
type SetupFunc func(Env) error

var (
	mu  sync.Mutex
	reg = map[string]SetupFunc{}
)

// Register adds an extension, by a name unique in the process.
func Register(name string, fn SetupFunc) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := reg[name]; dup {
		panic("extend: " + name + " registered twice")
	}
	reg[name] = fn
}

// Registered are the names of the registered extensions.
func Registered() []string {
	mu.Lock()
	defer mu.Unlock()
	var out []string
	for n := range reg {
		out = append(out, n)
	}
	return out
}

// Setup adds every registered extension's controller to the manager.
func Setup(mgr ctrl.Manager, sc acktypes.ServiceController, cfg ackcfg.Config) error {
	mu.Lock()
	defer mu.Unlock()
	env := Env{Manager: mgr, Service: sc, Config: cfg}
	for name, fn := range reg {
		if err := fn(env); err != nil {
			return fmt.Errorf("extension %s: %w", name, err)
		}
	}
	return nil
}

// Resync is how often an extension rereads AWS for an object that has not
// changed: ACK's --reconcile-default-resync-seconds, as ACK's own
// reconciler does, or ACK's default of ten hours.
func (e Env) Resync() time.Duration {
	if e.Config.ReconcileDefaultResyncSeconds > 0 {
		return time.Duration(e.Config.ReconcileDefaultResyncSeconds) * time.Second
	}
	return 10 * time.Hour
}

// AWSConfig is an AWS configuration for one Region, as the manager's own
// identity, made by ACK's runtime as for any of its kinds (the endpoint,
// credentials and HTTP settings ACK's flags give).
func (e Env) AWSConfig(ctx context.Context, region string, gvk schema.GroupVersionKind) (aws.Config, error) {
	var endpoint *string
	if e.Config.EndpointURL != "" {
		endpoint = aws.String(e.Config.EndpointURL)
	}
	return e.Service.NewAWSConfig(ctx, ackv1alpha1.AWSRegion(region), endpoint, "", gvk, nil)
}

// ACKMetadata is what ACK's controller recorded of the AWS resource behind
// its object: status.ackResourceMetadata's region and owner account.
// Empty until ACK has made (or adopted) the resource.
type ACKMetadata struct {
	Region         string
	OwnerAccountID string
}

// Made reports whether ACK has made (or adopted) the resource.
func (m ACKMetadata) Made() bool { return m.Region != "" }

// ACKMetadataOf reads status.ackResourceMetadata from ACK's typed metadata.
func ACKMetadataOf(md *ackv1alpha1.ResourceMetadata) ACKMetadata {
	if md == nil || md.Region == nil {
		return ACKMetadata{}
	}
	out := ACKMetadata{Region: string(*md.Region)}
	if md.OwnerAccountID != nil {
		out.OwnerAccountID = string(*md.OwnerAccountID)
	}
	return out
}

// OtherAccount is a reason to leave an object alone: ACK made its
// resource in another account, through a cross-account role map (CARM),
// which these extensions do not follow yet; the manager's own identity
// cannot reach it. Empty when the account is the manager's own.
func (e Env) OtherAccount(md ACKMetadata) string {
	if md.OwnerAccountID == "" || e.Config.AccountID == "" || md.OwnerAccountID == e.Config.AccountID {
		return ""
	}
	return fmt.Sprintf("the resource is in account %s, reached through a cross-account role; this extension acts only in the manager's own account, %s", md.OwnerAccountID, e.Config.AccountID)
}

// DeletionPolicy is the deletion policy ACK's controller applies to an
// object, found as ACK's reconciler finds it: the object's
// services.k8s.aws/deletion-policy annotation, then its namespace's
// <service>.services.k8s.aws/deletion-policy, then the controller's
// --deletion-policy flag (which our manager is given as ACK's controller
// is).
func (e Env) DeletionPolicy(ctx context.Context, obj metav1.Object, service string) (ackv1alpha1.DeletionPolicy, error) {
	if p, ok := obj.GetAnnotations()[ackv1alpha1.AnnotationDeletionPolicy]; ok {
		return ackv1alpha1.DeletionPolicy(p), nil
	}
	ns := &corev1.Namespace{}
	if err := e.Manager.GetAPIReader().Get(ctx, types.NamespacedName{Name: obj.GetNamespace()}, ns); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	if p, ok := ns.Annotations[service+"."+ackv1alpha1.AnnotationDeletionPolicy]; ok {
		return ackv1alpha1.DeletionPolicy(p), nil
	}
	if e.Config.DeletionPolicy == "" {
		return ackv1alpha1.DeletionPolicyDelete, nil
	}
	return e.Config.DeletionPolicy, nil
}

// ReadOnly reports ACK's services.k8s.aws/read-only annotation: ACK
// changes nothing in AWS for the object, and neither does an extension.
func ReadOnly(obj metav1.Object) bool {
	return strings.EqualFold(obj.GetAnnotations()[ackv1alpha1.AnnotationReadOnly], "true")
}
