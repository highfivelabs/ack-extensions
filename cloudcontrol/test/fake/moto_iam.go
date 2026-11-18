// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// ---- AWS::IAM::ServerCertificate on moto's IAM ----

// ServerCertificate is the IAM server certificate backend, identified by
// ServerCertificateName. moto 5.2.5 implements Upload/Get/Update/Delete
// ServerCertificate, and has no TagServerCertificate/UntagServerCertificate,
// so this backend keeps Tags itself (like SAMLProvider). PrivateKey,
// CertificateBody and CertificateChain are write-only and create-only.
type ServerCertificate struct {
	IAM  *iam.Client
	mu   sync.Mutex
	tags map[string]any
	n    int
}

// NewServerCertificate makes the backend.
func NewServerCertificate(cfg aws.Config) *ServerCertificate {
	return &ServerCertificate{IAM: iam.NewFromConfig(cfg), tags: map[string]any{}}
}

func (b *ServerCertificate) Create(ctx context.Context, m map[string]any) (string, error) {
	name := str(m, "ServerCertificateName")
	if name == "" {
		b.mu.Lock()
		b.n++
		name = fmt.Sprintf("servercert-%d", b.n)
		b.mu.Unlock()
	}
	in := &iam.UploadServerCertificateInput{
		ServerCertificateName: aws.String(name),
		CertificateBody:       aws.String(str(m, "CertificateBody")),
		PrivateKey:            aws.String(str(m, "PrivateKey")),
	}
	if cc := str(m, "CertificateChain"); cc != "" {
		in.CertificateChain = aws.String(cc)
	}
	if p := str(m, "Path"); p != "" {
		in.Path = aws.String(p)
	}
	if _, err := b.IAM.UploadServerCertificate(ctx, in); err != nil {
		if code(err) == "EntityAlreadyExists" {
			return "", &HandlerError{"AlreadyExists", err.Error()}
		}
		return "", InvalidRequest("%v", err)
	}
	b.mu.Lock()
	if t, ok := m["Tags"]; ok {
		b.tags[name] = t
	}
	b.mu.Unlock()
	return name, nil
}

func (b *ServerCertificate) Read(ctx context.Context, name string) (map[string]any, error) {
	out, err := b.IAM.GetServerCertificate(ctx, &iam.GetServerCertificateInput{ServerCertificateName: aws.String(name)})
	if err != nil {
		if code(err) == "NoSuchEntity" {
			return nil, NotFound("%s", name)
		}
		return nil, err
	}
	sc := out.ServerCertificate
	meta := sc.ServerCertificateMetadata
	m := map[string]any{
		"ServerCertificateName": aws.ToString(meta.ServerCertificateName),
		"Arn":                   aws.ToString(meta.Arn),
	}
	if meta.Path != nil {
		m["Path"] = aws.ToString(meta.Path)
	}
	b.mu.Lock()
	if t, ok := b.tags[name]; ok {
		m["Tags"] = t
	}
	b.mu.Unlock()
	return m, nil
}

func (b *ServerCertificate) Update(ctx context.Context, name string, prev, next map[string]any) error {
	if str(prev, "Path") != str(next, "Path") {
		p := str(next, "Path")
		if p == "" {
			p = "/"
		}
		if _, err := b.IAM.UpdateServerCertificate(ctx, &iam.UpdateServerCertificateInput{
			ServerCertificateName: aws.String(name),
			NewPath:               aws.String(p),
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	b.mu.Lock()
	if t, ok := next["Tags"]; ok {
		b.tags[name] = t
	} else {
		delete(b.tags, name)
	}
	b.mu.Unlock()
	return nil
}

func (b *ServerCertificate) Delete(ctx context.Context, name string) error {
	if _, err := b.Read(ctx, name); err != nil {
		return err
	}
	if _, err := b.IAM.DeleteServerCertificate(ctx, &iam.DeleteServerCertificateInput{ServerCertificateName: aws.String(name)}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.tags, name)
	b.mu.Unlock()
	return nil
}
