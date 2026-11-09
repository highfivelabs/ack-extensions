// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package connection

import (
	"github.com/aws/aws-sdk-go-v2/aws"

	svcapitypes "github.com/highfivelabs/ack-extensions/services/eventbridge/apis/v1alpha1"
)

// keepSecrets copies, from the spec as written into the spec as read, what
// DescribeConnection never returns: the password, API key and OAuth client
// secret (Secret references here), and the value of every HTTP parameter
// marked secret. Without it each read would differ from the spec and the
// connection would be updated on every resync. A non-secret field read
// back (a username, an endpoint, a parameter's key) is still compared.
func keepSecrets(desired, latest *svcapitypes.Connection) {
	d, l := desired.Spec.AuthParameters, latest.Spec.AuthParameters
	if d == nil || l == nil {
		return
	}
	if d.BasicAuthParameters != nil && l.BasicAuthParameters != nil {
		l.BasicAuthParameters.Password = d.BasicAuthParameters.Password
	}
	if d.APIKeyAuthParameters != nil && l.APIKeyAuthParameters != nil {
		l.APIKeyAuthParameters.APIKeyValue = d.APIKeyAuthParameters.APIKeyValue
	}
	if d.OAuthParameters != nil && l.OAuthParameters != nil {
		if d.OAuthParameters.ClientParameters != nil && l.OAuthParameters.ClientParameters != nil {
			l.OAuthParameters.ClientParameters.ClientSecret = d.OAuthParameters.ClientParameters.ClientSecret
		}
		keepSecretParameters(d.OAuthParameters.OAuthHTTPParameters, l.OAuthParameters.OAuthHTTPParameters)
	}
	keepSecretParameters(d.InvocationHTTPParameters, l.InvocationHTTPParameters)
}

// keepSecretParameters copies the value of each parameter marked secret
// from desired to latest, matched by key.
func keepSecretParameters(d, l *svcapitypes.ConnectionHTTPParameters) {
	if d == nil || l == nil {
		return
	}
	secret := func(isSecret *bool, key *string, values map[string]*string) (*string, bool) {
		if !aws.ToBool(isSecret) {
			return nil, false
		}
		v, ok := values[aws.ToString(key)]
		return v, ok
	}
	bodies := map[string]*string{}
	for _, p := range d.BodyParameters {
		if p != nil && aws.ToBool(p.IsValueSecret) {
			bodies[aws.ToString(p.Key)] = p.Value
		}
	}
	for _, p := range l.BodyParameters {
		if p == nil {
			continue
		}
		if v, ok := secret(p.IsValueSecret, p.Key, bodies); ok {
			p.Value = v
		}
	}
	headers := map[string]*string{}
	for _, p := range d.HeaderParameters {
		if p != nil && aws.ToBool(p.IsValueSecret) {
			headers[aws.ToString(p.Key)] = p.Value
		}
	}
	for _, p := range l.HeaderParameters {
		if p == nil {
			continue
		}
		if v, ok := secret(p.IsValueSecret, p.Key, headers); ok {
			p.Value = v
		}
	}
	queries := map[string]*string{}
	for _, p := range d.QueryStringParameters {
		if p != nil && aws.ToBool(p.IsValueSecret) {
			queries[aws.ToString(p.Key)] = p.Value
		}
	}
	for _, p := range l.QueryStringParameters {
		if p == nil {
			continue
		}
		if v, ok := secret(p.IsValueSecret, p.Key, queries); ok {
			p.Value = v
		}
	}
}
