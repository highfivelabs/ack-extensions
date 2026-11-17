// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/macie2"
	macietypes "github.com/aws/aws-sdk-go-v2/service/macie2/types"
)

// ---- AWS::Macie::Session on moto's Macie2 ----

// MacieSession is the account's Macie session singleton, identified by
// AwsAccountId. moto 5.2.5 implements EnableMacie, GetMacieSession, and
// DisableMacie and has no UpdateMacieSession.
type MacieSession struct {
	M       *macie2.Client
	Account string
}

// NewMacieSession makes the backend.
func NewMacieSession(cfg aws.Config, account string) *MacieSession {
	return &MacieSession{M: macie2.NewFromConfig(cfg), Account: account}
}

func (b *MacieSession) Create(ctx context.Context, m map[string]any) (string, error) {
	in := &macie2.EnableMacieInput{}
	if f := str(m, "FindingPublishingFrequency"); f != "" {
		in.FindingPublishingFrequency = macietypes.FindingPublishingFrequency(f)
	}
	if s := str(m, "Status"); s != "" {
		in.Status = macietypes.MacieStatus(s)
	}
	if _, err := b.M.EnableMacie(ctx, in); err != nil {
		return "", InvalidRequest("%v", err)
	}
	return b.Account, nil
}

func (b *MacieSession) Read(ctx context.Context, id string) (map[string]any, error) {
	out, err := b.M.GetMacieSession(ctx, &macie2.GetMacieSessionInput{})
	if err != nil {
		if c := code(err); c == "ResourceNotFoundException" || c == "AccessDeniedException" {
			return nil, NotFound("macie session %s", id)
		}
		return nil, err
	}
	m := map[string]any{
		"AwsAccountId": b.Account,
		"Status":       string(out.Status),
	}
	if out.FindingPublishingFrequency != "" {
		m["FindingPublishingFrequency"] = string(out.FindingPublishingFrequency)
	}
	if out.ServiceRole != nil {
		m["ServiceRole"] = aws.ToString(out.ServiceRole)
	}
	return m, nil
}

func (b *MacieSession) Update(context.Context, string, map[string]any, map[string]any) error {
	return InvalidRequest("moto 5.2.5 has no UpdateMacieSession")
}

func (b *MacieSession) Delete(ctx context.Context, id string) error {
	if _, err := b.Read(ctx, id); err != nil {
		return err
	}
	_, err := b.M.DisableMacie(ctx, &macie2.DisableMacieInput{})
	return err
}
