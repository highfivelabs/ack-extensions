// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

package cfn

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// FieldName is the Kubernetes field name of a CloudFormation property:
// lower camel case, with a leading acronym lowered as one word
// (SAMLProvider to samlProvider, S3Logs to s3Logs, ARN to arn, Id to id).
func FieldName(cfnName string) string {
	r := []rune(cfnName)
	if len(r) == 0 {
		return cfnName
	}
	// The leading run of upper-case letters and digits.
	n := 0
	for n < len(r) && (unicode.IsUpper(r[n]) || unicode.IsDigit(r[n])) {
		n++
	}
	switch {
	case n == 0:
		return cfnName
	case n == len(r):
		return strings.ToLower(cfnName)
	case n == 1:
		return string(unicode.ToLower(r[0])) + string(r[1:])
	default:
		// "SAMLProvider": the run is SAMLP; the last upper-case letter
		// starts the next word.
		last := n - 1
		if unicode.IsDigit(r[last]) {
			last = n
		}
		return strings.ToLower(string(r[:last])) + string(r[last:])
	}
}

// NameMap maps one object's CloudFormation property names to Kubernetes
// field names and back.
type NameMap struct {
	ToField map[string]string
	ToCFN   map[string]string
}

// Names builds the NameMap of an object's properties, failing when two
// properties would get the same field name.
func Names(props map[string]*Prop) (NameMap, error) {
	m := NameMap{ToField: map[string]string{}, ToCFN: map[string]string{}}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := FieldName(k)
		if other, ok := m.ToCFN[f]; ok {
			return m, fmt.Errorf("properties %q and %q both map to field %q", other, k, f)
		}
		m.ToField[k] = f
		m.ToCFN[f] = k
	}
	return m, nil
}

// KindPlural is a Kubernetes plural for a kind (lower case).
func KindPlural(kind string) string {
	l := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(l, "y") && !strings.HasSuffix(l, "ay") && !strings.HasSuffix(l, "ey") && !strings.HasSuffix(l, "oy"):
		return strings.TrimSuffix(l, "y") + "ies"
	case strings.HasSuffix(l, "s"), strings.HasSuffix(l, "x"), strings.HasSuffix(l, "ch"), strings.HasSuffix(l, "sh"):
		return l + "es"
	}
	return l + "s"
}
