// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// ---- AWS::SecretsManager::ResourcePolicy on moto's Secrets Manager ----

// SecretPolicy is the backend of a secret's resource policy, identified by
// the secret's ARN. moto 5.2.5 implements Put/Get/DeleteResourcePolicy; it
// does not implement BlockPublicPolicy (accepted, not checked). A secret
// has at most one policy, so a create on a secret that has one fails
// AlreadyExists. The read gives SecretId as the secret's ARN and the policy
// as a JSON object, which is what this backend takes the handler to do;
// both are unverified on real AWS, where a SecretId written as a name may
// read back as the ARN.
type SecretPolicy struct{ SM *secretsmanager.Client }

// NewSecretPolicy makes the backend.
func NewSecretPolicy(cfg aws.Config) *SecretPolicy {
	return &SecretPolicy{SM: secretsmanager.NewFromConfig(cfg)}
}

func policyString(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", InvalidRequest("ResourcePolicy: %v", err)
	}
	return string(b), nil
}

func (b *SecretPolicy) put(ctx context.Context, m map[string]any) (string, error) {
	pol, err := policyString(m["ResourcePolicy"])
	if err != nil {
		return "", err
	}
	in := &secretsmanager.PutResourcePolicyInput{SecretId: aws.String(str(m, "SecretId")), ResourcePolicy: aws.String(pol)}
	if v, ok := m["BlockPublicPolicy"].(bool); ok {
		in.BlockPublicPolicy = aws.Bool(v)
	}
	out, err := b.SM.PutResourcePolicy(ctx, in)
	if err != nil {
		return "", InvalidRequest("%v", err)
	}
	return aws.ToString(out.ARN), nil
}

func (b *SecretPolicy) Create(ctx context.Context, m map[string]any) (string, error) {
	if out, err := b.SM.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(str(m, "SecretId"))}); err == nil && out.ResourcePolicy != nil {
		return "", &HandlerError{"AlreadyExists", "secret " + aws.ToString(out.ARN) + " has a resource policy"}
	}
	return b.put(ctx, m)
}

func (b *SecretPolicy) Read(ctx context.Context, arn string) (map[string]any, error) {
	out, err := b.SM.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(arn)})
	if err != nil {
		if code(err) == "ResourceNotFoundException" {
			return nil, NotFound("secret %s", arn)
		}
		return nil, err
	}
	if out.ResourcePolicy == nil {
		return nil, NotFound("secret %s has no resource policy", arn)
	}
	var pol any = aws.ToString(out.ResourcePolicy)
	var obj map[string]any
	if json.Unmarshal([]byte(aws.ToString(out.ResourcePolicy)), &obj) == nil {
		pol = obj
	}
	return map[string]any{"Id": arn, "SecretId": aws.ToString(out.ARN), "ResourcePolicy": pol}, nil
}

func (b *SecretPolicy) Update(ctx context.Context, arn string, _, next map[string]any) error {
	m := map[string]any{}
	for k, v := range next {
		m[k] = v
	}
	m["SecretId"] = arn
	_, err := b.put(ctx, m)
	return err
}

func (b *SecretPolicy) Delete(ctx context.Context, arn string) error {
	if _, err := b.Read(ctx, arn); err != nil {
		return err
	}
	_, err := b.SM.DeleteResourcePolicy(ctx, &secretsmanager.DeleteResourcePolicyInput{SecretId: aws.String(arn)})
	return err
}
