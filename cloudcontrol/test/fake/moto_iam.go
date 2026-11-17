// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// ---- AWS::IAM::ServerCertificate on moto's IAM ----

// ServerCertificate is the IAM server certificate backend, identified by
// ServerCertificateName. moto 5.2.5 implements Upload/Get/Update/Delete
// ServerCertificate and Tag/UntagServerCertificate. PrivateKey,
// CertificateBody and CertificateChain are write-only and create-only.
type ServerCertificate struct {
	IAM *iam.Client
	mu  sync.Mutex
	n   int
}

// NewServerCertificate makes the backend.
func NewServerCertificate(cfg aws.Config) *ServerCertificate {
	return &ServerCertificate{IAM: iam.NewFromConfig(cfg)}
}

func iamTags(v any) []iamtypes.Tag {
	var out []iamtypes.Tag
	for k, val := range tagsOf(v) {
		out = append(out, iamtypes.Tag{Key: aws.String(k), Value: aws.String(val)})
	}
	return out
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
	if tags := iamTags(m["Tags"]); len(tags) > 0 {
		in.Tags = tags
	}
	if _, err := b.IAM.UploadServerCertificate(ctx, in); err != nil {
		if code(err) == "EntityAlreadyExists" {
			return "", &HandlerError{"AlreadyExists", err.Error()}
		}
		return "", InvalidRequest("%v", err)
	}
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
	if len(sc.Tags) > 0 {
		tm := map[string]string{}
		for _, t := range sc.Tags {
			tm[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		m["Tags"] = tagList(tm)
	}
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
	oldTags, newTags := tagsOf(prev["Tags"]), tagsOf(next["Tags"])
	var remove []string
	for k := range oldTags {
		if _, ok := newTags[k]; !ok {
			remove = append(remove, k)
		}
	}
	if len(remove) > 0 {
		if _, err := b.IAM.UntagServerCertificate(ctx, &iam.UntagServerCertificateInput{
			ServerCertificateName: aws.String(name),
			TagKeys:               remove,
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	if tags := iamTags(next["Tags"]); len(tags) > 0 {
		if _, err := b.IAM.TagServerCertificate(ctx, &iam.TagServerCertificateInput{
			ServerCertificateName: aws.String(name),
			Tags:                  tags,
		}); err != nil {
			return InvalidRequest("%v", err)
		}
	}
	return nil
}

func (b *ServerCertificate) Delete(ctx context.Context, name string) error {
	if _, err := b.Read(ctx, name); err != nil {
		return err
	}
	_, err := b.IAM.DeleteServerCertificate(ctx, &iam.DeleteServerCertificateInput{ServerCertificateName: aws.String(name)})
	return err
}
