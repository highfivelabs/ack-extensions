// Copyright 2026 High Five Labs
// SPDX-License-Identifier: Apache-2.0

// Package ackiam writes the IAM permissions of the ACK-generated kinds
// here, for an environment's role (an ACKContext, docs/plan.md section
// 7.6), the way cloudcontrol/pkg/iamgen does for the Cloud Control kinds.
//
// ACK's generator writes no IAM policy (each ACK controller ships a
// curated one, config/iam/recommended-inline-policy). But the code it
// generates names every AWS operation it calls, so the permissions are
// read from it: each kind's package (services/<service>/pkg/resource/<kind>,
// and each extension of an ACK kind, services/<service>/pkg/extend/<kind>)
// and the service's own packages it imports (ec2's tags) are scanned for
// calls on the service's SDK client (rm.sdkapi.<Operation>, a client or
// api variable's <Operation>, svcsdk.New<Operation>Paginator), and each name is
// kept only if the SDK has that operation. IAM's action is the
// operation's name in the service's IAM namespace, unless IAM names it
// otherwise (actions: S3 Control's account-level operations). What a call needs
// beyond its own action (iam:PassRole for a role handed to the service,
// the protected resource's action for a WAF association) is not in the
// code: Curated lists it per kind, from AWS's documentation.
package ackiam

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/highfivelabs/ack-extensions/cloudcontrol/pkg/iamgen"
)

// Module is this repository's Go module path.
const Module = "github.com/highfivelabs/ack-extensions"

// RuntimeActions are what ACK's runtime itself calls, whatever the kind:
// who it is, at start-up.
var RuntimeActions = []string{"sts:GetCallerIdentity"}

// prefixes are the services whose IAM namespace is not their name.
var prefixes = map[string]string{
	"applicationautoscaling": "application-autoscaling",
	"cloudwatchlogs":         "logs",
	"efs":                    "elasticfilesystem",
	"eventbridge":            "events",
	"s3control":              "s3",
}

// actions are the operations whose IAM action is not their own name, by
// service: S3 Control's public access block is authorized as the
// account's (s3:GetAccountPublicAccessBlock, and
// s3:PutAccountPublicAccessBlock for both the put and the delete; the
// S3 API reference for each operation).
//
// S3's object operations are authorized by actions of other names too
// (the S3 API reference and the Service Authorization Reference):
// ListObjectVersions as s3:ListBucketVersions, ListMultipartUploads as
// s3:ListBucketMultipartUploads, and DeleteObjects as the delete of each
// object it names, s3:DeleteObjectVersion when the version is named, as
// the Bucket extension always names it. HeadObject, which our Object
// reads an object with, is authorized as s3:GetObject.
//
// So are most of a bucket's configuration operations, which our copy of
// ACK's Bucket calls (section 6.38; the Service Authorization Reference,
// "Amazon S3", each action's API operations): a configuration's Get*, its
// Put* and its Delete* are authorized as the configuration's Get and Put
// actions; HeadBucket as s3:ListBucket, ListBuckets as
// s3:ListAllMyBuckets.
var actions = map[string]map[string]string{
	"s3": {
		"ListObjectVersions":   "ListBucketVersions",
		"ListMultipartUploads": "ListBucketMultipartUploads",
		"DeleteObjects":        "DeleteObjectVersion",
		"HeadObject":           "GetObject",

		"HeadBucket":  "ListBucket",
		"ListBuckets": "ListAllMyBuckets",

		"GetBucketAccelerateConfiguration": "GetAccelerateConfiguration",
		"PutBucketAccelerateConfiguration": "PutAccelerateConfiguration",

		"ListBucketAnalyticsConfigurations":  "GetAnalyticsConfiguration",
		"PutBucketAnalyticsConfiguration":    "PutAnalyticsConfiguration",
		"DeleteBucketAnalyticsConfiguration": "PutAnalyticsConfiguration",

		"GetBucketCors":    "GetBucketCORS",
		"PutBucketCors":    "PutBucketCORS",
		"DeleteBucketCors": "PutBucketCORS",

		"GetBucketEncryption":    "GetEncryptionConfiguration",
		"PutBucketEncryption":    "PutEncryptionConfiguration",
		"DeleteBucketEncryption": "PutEncryptionConfiguration",

		"ListBucketIntelligentTieringConfigurations":  "GetIntelligentTieringConfiguration",
		"PutBucketIntelligentTieringConfiguration":    "PutIntelligentTieringConfiguration",
		"DeleteBucketIntelligentTieringConfiguration": "PutIntelligentTieringConfiguration",

		"ListBucketInventoryConfigurations":  "GetInventoryConfiguration",
		"PutBucketInventoryConfiguration":    "PutInventoryConfiguration",
		"DeleteBucketInventoryConfiguration": "PutInventoryConfiguration",

		"GetBucketLifecycleConfiguration": "GetLifecycleConfiguration",
		"PutBucketLifecycleConfiguration": "PutLifecycleConfiguration",
		"DeleteBucketLifecycle":           "PutLifecycleConfiguration",

		"ListBucketMetricsConfigurations":  "GetMetricsConfiguration",
		"PutBucketMetricsConfiguration":    "PutMetricsConfiguration",
		"DeleteBucketMetricsConfiguration": "PutMetricsConfiguration",

		"GetBucketNotificationConfiguration": "GetBucketNotification",
		"PutBucketNotificationConfiguration": "PutBucketNotification",

		"DeleteBucketOwnershipControls": "PutBucketOwnershipControls",

		"GetPublicAccessBlock":    "GetBucketPublicAccessBlock",
		"PutPublicAccessBlock":    "PutBucketPublicAccessBlock",
		"DeletePublicAccessBlock": "PutBucketPublicAccessBlock",

		"GetBucketReplication":    "GetReplicationConfiguration",
		"PutBucketReplication":    "PutReplicationConfiguration",
		"DeleteBucketReplication": "PutReplicationConfiguration",

		"DeleteBucketTagging": "PutBucketTagging",

		"GetObjectLockConfiguration": "GetBucketObjectLockConfiguration",
		"PutObjectLockConfiguration": "PutBucketObjectLockConfiguration",
	},
	"s3control": {
		"GetPublicAccessBlock":    "GetAccountPublicAccessBlock",
		"PutPublicAccessBlock":    "PutAccountPublicAccessBlock",
		"DeletePublicAccessBlock": "PutAccountPublicAccessBlock",
	},
}

// Action is the IAM action of a service's SDK operation.
func Action(service, operation string) string {
	if a, ok := actions[service][operation]; ok {
		operation = a
	}
	return Prefix(service) + ":" + operation
}

// Prefix is a service's IAM namespace.
func Prefix(service string) string {
	if p, ok := prefixes[service]; ok {
		return p
	}
	return service
}

// Curated are the actions a kind's calls need beyond their own, by
// Kind.service, from AWS's documentation of each operation; not checked
// on real AWS (moto does not enforce IAM).
var Curated = map[string][]string{
	// AddRoleToDBInstance and AddRoleToDBCluster hand the role to RDS.
	"DBRoleAssociation.rds": {"iam:PassRole"},
	// A schedule's target role is handed to EventBridge Scheduler.
	"Schedule.scheduler": {"iam:PassRole"},
	// A connection's credentials are kept in a Secrets Manager secret that
	// EventBridge makes, as the caller, under events!connection/.
	"Connection.eventbridge": {"secretsmanager:CreateSecret", "secretsmanager:DescribeSecret", "secretsmanager:UpdateSecret", "secretsmanager:PutSecretValue", "secretsmanager:DeleteSecret"},
	// The Bucket extension deletes with BypassGovernanceRetention, which
	// needs its own action (S3 Object Lock, governance mode). Our copy of
	// ACK's Bucket (section 6.38): a replication rule's role is handed to
	// S3, and a directory bucket's calls are authorized in s3express
	// (its tags through S3 Control's ListTagsForResource, TagResource and
	// UntagResource, which the scan, reading only S3's client, misses).
	"Bucket.s3": {
		"s3:BypassGovernanceRetention", "iam:PassRole",
		"s3express:CreateBucket", "s3express:DeleteBucket",
		"s3express:GetEncryptionConfiguration", "s3express:PutEncryptionConfiguration",
		"s3express:GetLifecycleConfiguration", "s3express:PutLifecycleConfiguration",
		"s3express:GetBucketPolicy", "s3express:PutBucketPolicy", "s3express:DeleteBucketPolicy",
		"s3express:ListTagsForResource", "s3express:TagResource", "s3express:UntagResource",
	},
	// ReplicateKey makes the replica: kms:CreateKey in the replica's region.
	"ReplicaKey.kms": {"kms:CreateKey"},
	// The first replication rule makes ECR's service-linked role.
	"ReplicationConfiguration.ecr": {"iam:CreateServiceLinkedRole"},
	// AssociateWebACL and DisassociateWebACL also need the protected
	// resource's own action.
	"WebACLAssociation.wafv2": {
		"elasticloadbalancing:SetWebAcl", "apigateway:SetWebACL", "appsync:SetWebACL",
		"cognito-idp:AssociateWebACL", "cognito-idp:DisassociateWebACL", "cognito-idp:GetWebACLForResource",
		"apprunner:AssociateWebAcl", "apprunner:DisassociateWebAcl", "apprunner:DescribeWebAclForService",
		"ec2:AssociateVerifiedAccessInstanceWebAcl", "ec2:DisassociateVerifiedAccessInstanceWebAcl",
	},
}

// sensitive are actions that reach beyond the resource, listed in the
// report as cloudcontrol/pkg/iamgen lists its own.
var sensitive = []string{"iam:PassRole", "iam:CreateServiceLinkedRole", "iam:CreateAccessKey", "iam:CreateLoginProfile", "iam:UpdateLoginProfile", "iam:Put", "iam:Attach", "kms:CreateGrant", "kms:PutKeyPolicy", "kms:Decrypt", "secretsmanager:", "sts:AssumeRole", "organizations:"}

// Kind is one kind's permissions.
type Kind struct {
	Service string
	Kind    string
	// Generated are the actions of the SDK operations its code calls.
	Generated []string
	// Curated are the actions Curated adds.
	Curated []string
}

// Ref is the kind as Kind.service.
func (k Kind) Ref() string { return k.Kind + "." + k.Service }

// Actions are every action the kind needs, sorted.
func (k Kind) Actions() []string {
	return dedupe(append(append([]string(nil), k.Generated...), k.Curated...))
}

// Sensitive are the kind's actions that reach beyond the resource.
func (k Kind) Sensitive() []string {
	var out []string
	for _, a := range k.Actions() {
		for _, p := range sensitive {
			if strings.HasPrefix(a, p) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

var (
	reKind      = regexp.MustCompile(`Kind:\s+"([A-Za-z0-9]+)"`)
	reSDK       = regexp.MustCompile(`svcsdk\s+"([^"]+)"`)
	reCall      = regexp.MustCompile(`\b(?:rm\.sdkapi|client|api)\.([A-Z][A-Za-z0-9]+)\(`)
	rePaginator = regexp.MustCompile(`\bsvcsdk\.New([A-Z][A-Za-z0-9]+)Paginator\(`)
	reOp        = regexp.MustCompile(`^api_op_([A-Za-z0-9]+)\.go$`)
)

// goFiles are the non-test Go files of dir, read.
func goFiles(dir string) ([][]byte, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// operations are the operations of each SDK package (import path), read
// from its api_op_<Operation>.go files.
func operations(root string, pkgs []string) (map[string]map[string]bool, error) {
	cmd := exec.Command("go", append([]string{"list", "-f", "{{.ImportPath}} {{.Dir}}"}, pkgs...)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %v: %s", strings.Join(pkgs, " "), err, stderr.String())
	}
	ops := map[string]map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, dir, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		ops[path] = map[string]bool{}
		for _, e := range ents {
			if m := reOp.FindStringSubmatch(e.Name()); m != nil {
				ops[path][m[1]] = true
			}
		}
	}
	return ops, nil
}

type scanned struct {
	service, kind, sdk string
	names              map[string]bool
}

// Scan reads every kind's permissions under root/services, sorted by
// Kind.service.
func Scan(root string) ([]Kind, error) {
	svcs, err := os.ReadDir(filepath.Join(root, "services"))
	if err != nil {
		return nil, err
	}
	var found []scanned
	sdks := map[string]bool{}
	for _, s := range svcs {
		if !s.IsDir() {
			continue
		}
		svc := s.Name()
		local := regexp.MustCompile(`"` + regexp.QuoteMeta(Module+"/services/"+svc+"/pkg/") + `([^"]+)"`)
		// Our kinds (pkg/resource/<kind>), then the extensions of ACK's
		// own kinds (pkg/extend/<kind>, docs/extensions.md), whose calls
		// the same manager makes as the same identity.
		var dirs []string
		for _, sub := range []string{"resource", "extend"} {
			base := filepath.Join(root, "services", svc, "pkg", sub)
			ents, err := os.ReadDir(base)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			for _, e := range ents {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(base, e.Name()))
				}
			}
		}
		for _, dir := range dirs {
			files, err := goFiles(dir)
			if err != nil {
				return nil, err
			}
			sc := scanned{service: svc, names: map[string]bool{}}
			// The kind's own GroupKind, which the generator writes in
			// descriptor.go (references.go names other kinds); an
			// extension names ACK's kind in its GroupVersionKind.
			if d, err := os.ReadFile(filepath.Join(dir, "descriptor.go")); err == nil {
				if m := reKind.FindSubmatch(d); m != nil {
					sc.kind = string(m[1])
				}
			} else {
				for _, b := range files {
					if m := reKind.FindSubmatch(b); m != nil {
						sc.kind = string(m[1])
						break
					}
				}
			}
			seen := map[string]bool{}
			for i := 0; i < len(files); i++ {
				b := files[i]
				if m := reSDK.FindSubmatch(b); m != nil && sc.sdk == "" {
					sc.sdk = string(m[1])
				}
				for _, m := range reCall.FindAllSubmatch(b, -1) {
					sc.names[string(m[1])] = true
				}
				for _, m := range rePaginator.FindAllSubmatch(b, -1) {
					sc.names[string(m[1])] = true
				}
				// The service's own packages the kind imports (not other
				// kinds): their calls are the kind's too.
				for _, m := range local.FindAllSubmatch(b, -1) {
					p := string(m[1])
					if strings.HasPrefix(p, "resource") || seen[p] {
						continue
					}
					seen[p] = true
					more, err := goFiles(filepath.Join(root, "services", svc, "pkg", filepath.FromSlash(p)))
					if err != nil {
						return nil, err
					}
					files = append(files, more...)
				}
			}
			if sc.kind == "" || sc.sdk == "" {
				return nil, fmt.Errorf("%s: no GroupKind or svcsdk import found", dir)
			}
			sdks[sc.sdk] = true
			// A kind of ours that is also extended (s3's Bucket: our
			// fixed copy of ACK's, and the extension acting on both) is
			// one kind: its calls are merged, so its statement is one.
			merged := false
			for i := range found {
				if found[i].service == sc.service && found[i].kind == sc.kind && found[i].sdk == sc.sdk {
					for n := range sc.names {
						found[i].names[n] = true
					}
					merged = true
					break
				}
			}
			if !merged {
				found = append(found, sc)
			}
		}
	}
	var pkgs []string
	for p := range sdks {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	ops, err := operations(root, pkgs)
	if err != nil {
		return nil, err
	}
	var out []Kind
	for _, sc := range found {
		k := Kind{Service: sc.service, Kind: sc.kind}
		for n := range sc.names {
			if ops[sc.sdk][n] {
				k.Generated = append(k.Generated, Action(sc.service, n))
			}
		}
		if len(k.Generated) == 0 {
			return nil, fmt.Errorf("%s: no call of an operation of %s found", k.Ref(), sc.sdk)
		}
		k.Generated = dedupe(k.Generated)
		k.Curated = dedupe(Curated[k.Ref()])
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out, nil
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// Policy is one policy for the kinds: the runtime's statement, then one
// statement per kind, kept apart so each action's reason stays visible.
func Policy(kinds []Kind) iamgen.Policy {
	p := iamgen.Policy{Version: "2012-10-17", Statement: []iamgen.Statement{{
		Sid: "ACKRuntime", Effect: "Allow", Action: append([]string(nil), RuntimeActions...), Resource: "*",
	}}}
	for _, k := range kinds {
		p.Statement = append(p.Statement, iamgen.Statement{
			Sid: nonAlnum.ReplaceAllString(strings.ToUpper(k.Service[:1])+k.Service[1:]+k.Kind, ""), Effect: "Allow",
			Action: k.Actions(), Resource: "*",
		})
	}
	return p
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
