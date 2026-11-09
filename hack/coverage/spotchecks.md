Hand-checked on 2026-11-09 at the pins above, with `hack/coverage/run.sh -explain <service>/<Resource>`
against the real CRDs (an independent Ruby script re-counted each CRD's spec paths: all agreed
with the parser) and the CloudFormation schemas. For each resource I read every attribute
marked `official` or `both` (is it the same property?) and every attribute marked `none`
beside the CRD paths that matched nothing (did ACK cover it under another name?).

| Resource | ACK kind (release) | Writable attributes | Marked official | Wrong | Missed | Sample |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| `rds/DBInstance` | `DBInstance` (v1.14.2) | 91 | 61 | 0 | 1 | tuned |
| `ec2/VPC` | `VPC` (v1.23.1) | 17 | 8 | 0 | 0 | tuned |
| `ec2/SecurityGroup` | `SecurityGroup` (v1.23.1) | 23 | 23 | 0 | 0 | tuned |
| `s3/Bucket` | `Bucket` (v1.15.0) | 153 | 115 | 0 | 3 | tuned |
| `lambda/Function` | `Function` (v1.19.0) | 53 | 39 | 0 | 0 | tuned |
| `iam/Role` | `Role` (v1.11.3) | 11 | 10 | 0 | 1 | tuned |
| `dynamodb/Table` | `Table` (v1.14.2) | 64 | 34 | 0 | 3 | tuned |
| `eks/Cluster` | `Cluster` (v1.26.0) | 48 | 40 | 0 | 1 | tuned |
| `sqs/Queue` | `Queue` (v1.9.0) | 17 | 17 | 0 | 0 | tuned |
| `sns/Topic` | `Topic` (v1.13.0) | 19 | 12 | 0 | 3 | tuned |
| `kms/Key` | `Key` (v1.7.0) | 13 | 10 | 0 | 0 | tuned |
| `ecr/Repository` | `Repository` (v1.11.1) | 13 | 12 | 0 | 0 | tuned |
| `elasticache/ReplicationGroup` | `ReplicationGroup` (v1.12.0) | 49 | 44 | 0 | 0 | tuned |
| `apigatewayv2/Api` | `API` (v1.8.0) | 26 | 21 | 0 | 0 | tuned |
| `ecs/Service` | `Service` (v1.9.2) | 121 | 70 | 0 | 0 | blind |
| `efs/FileSystem` | `FileSystem` (v1.8.0) | 20 | 18 | 0 | 1 | blind |
| `cloudtrail/Trail` | `Trail` (v1.9.1) | 31 | 13 | 0 | 0 | blind |
| `athena/WorkGroup` | `WorkGroup` (v1.10.0) | 74 | 19 | 0 | 0 | blind, then aliased (marked 9 and missed 10 before the alias) |
| `route53/HostedZone` | `HostedZone` (v1.8.0) | 9 | 7 | 0 | 0 | blind |
| `opensearchservice/Domain` | `Domain` (v1.9.0) | 87 | 62 | 0 | 0 | blind |
| `kinesis/Stream` | `Stream` (v1.7.1) | 12 | 5 | 0 | 0 | blind |
| `ec2/Subnet` | `Subnet` (v1.23.1) | 20 | 15 | 0 | 0 | blind |
| `cloudfront/Distribution` | `Distribution` (v1.10.1) | 133 | 116 | 0 | 2 | blind |
| `secretsmanager/Secret` | `Secret` (v1.8.2) | 19 | 8 | 0 | 0 | blind |

- **Agreement.** Of 779 attributes marked official in these 24 resources, none was wrong
  (0.0%); 15 attributes marked `none` were covered by ACK under a name the matcher could not
  see (about 1.9% of the 794 that ACK actually covers). The 14 "tuned" resources are where the
  aliases in `mappings.yaml` for `s3/Bucket`, `ec2/SecurityGroup`, `ecr/Repository`,
  `iam/Role` and `ec2/VPC` came from, so their numbers flatter the method. The 10 blind
  resources, picked before I looked at any of their rows, are the honest figure for a resource
  nobody has aliased: as first run, 323 marked official, 0 wrong and 13 missed (recall 96%);
  10 of the 13 misses were one resource, `athena/WorkGroup`, whose CRD calls CloudFormation's
  `WorkGroupConfiguration` just `configuration` and whose CloudFormation schema repeats the
  subtree as `WorkGroupConfigurationUpdates`, which defeats the unique-tail match. I added that
  alias, after which the ten stand at 333 marked, 0 wrong, 3 missed. Ten resources of
  hundreds is a small sample: read the rate as "the error is a few percent of the official
  bar, in the direction of understating it".
- **The loose tiers.** I also read about 250 matches made by `name-variant`, `name-suffix`,
  `primary-name`, `tail-k` and `leaf-name` across all services (an even sample of each tier,
  see `TestAudit` in `hack/coverage/devaids_test.go`). One was wrong, a `leaf-name` pairing of
  two `AttributeName`s in different lists, which the nesting-depth rule now prevents. Two
  others are arguable (`apigateway/Deployment` `StageDescription.TracingEnabled` with
  `tracingEnabled`; `bedrockagentcore/BrowserCustom` `CertificateLocation.SecretArn` with
  `location.secretsManager.secretARN`). I put the precision of the loose tiers at about 98%.
- **Where it misses.** Structural renames where ACK follows the SDK's shape and CloudFormation
  its own: SNS delivery-status logging (six flat ACK fields for one CloudFormation list), S3
  analytics destinations, DynamoDB's time-to-live, CloudFront's `Items`/`Quantity` wrappers
  below the first level, and any property where one side is a JSON document and the other a
  nested object. These are the cases to add to `mappings.yaml`.
- **Ours.** Every one of our kinds was checked by hand against its CRD (`-explain` on each
  resource they map to). Every match is the same property; the spec paths that matched
  nothing are selectors and fields CloudFormation has no property for (`registryID`,
  `statementID`, `replicaRegion`, `policyName`, ...).
