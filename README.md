# ack-extensions

Kubernetes kinds for AWS that [AWS Controllers for Kubernetes](https://github.com/aws-controllers-k8s)
(ACK) does not have yet. They are built the way ACK builds its own
controllers, with ACK's code generator and runtime, and they run beside
ACK's controllers as the same AWS identity.

ack-extensions is not affiliated with or endorsed by Amazon Web Services.

## Kinds

| Kind | What it does | Where it comes from |
| --- | --- | --- |
| `applicationautoscaling.aws.hfl.dev/v1alpha1` `ScheduledAction` | A scheduled change of an Application Auto Scaling target's capacity (an ECS service, a DynamoDB table, Aurora replicas), put with `PutScheduledAction`. ACK's applicationautoscaling-controller ignores the resource. | Generated, as ACK's applicationautoscaling-controller generates its own Put-shaped `ScalingPolicy`; no ACK pull request adds it |
| `autoscaling.aws.hfl.dev/v1alpha1` `ScalingPolicy` | An EC2 Auto Scaling group's scaling policy (target tracking, step, simple or predictive), put with `PutScalingPolicy` and updated in place. ACK's `AutoScalingGroup` has none, and a group without policies does not scale. | Generated, configured as ACK's applicationautoscaling-controller generates its own Put-shaped `ScalingPolicy`, with its server-defaults delta hook copied; no ACK pull request adds it |
| `autoscaling.aws.hfl.dev/v1alpha1` `ScheduledAction` | A scheduled change of an EC2 Auto Scaling group's capacity, once or on a recurrence, put with `PutScheduledUpdateGroupAction`. ACK's `AutoScalingGroup` has none. | Generated, as `ScalingPolicy`; no ACK pull request adds it |
| `ec2.aws.hfl.dev/v1alpha1` `CustomerGateway` | The on-premises end of a site-to-site VPN (address, BGP ASN, type), with tags. ACK's ec2-controller ignores it. | Generated, with ACK ec2-controller's own tag patterns copied from its `InternetGateway`; no ACK pull request adds it |
| `ec2.aws.hfl.dev/v1alpha1` `VPNGateway` | The AWS end of a site-to-site VPN, attached to a VPC (moved when `spec.vpc` changes, detached before delete), with tags. ACK's ec2-controller ignores it. | Generated, with the VPC attachment and tag hooks of ACK ec2-controller's `InternetGateway` copied; no ACK pull request adds it |
| `ec2.aws.hfl.dev/v1alpha1` `Volume` | An EBS volume: created, grown or changed in type, IOPS and throughput with `ModifyVolume`, tagged, and deleted. ACK's ec2-controller ignores it. | Generated (with generator patch 0002), with ACK ec2-controller's tag patterns; no ACK pull request adds it |
| `ec2.aws.hfl.dev/v1alpha1` `VolumeAttachment` | An EBS volume attached to an instance as a device, by ID or by reference to our `Volume`; detached on delete and attached again if detached outside Kubernetes. ACK's ec2-controller has no attachment. | Hand-written on iam-controller PR #184's skeleton; no ACK pull request adds it |
| `eventbridge.aws.hfl.dev/v1alpha1` `APIDestination` | An EventBridge API destination: the endpoint, method and rate limit a rule sends events to, through a `Connection` it references. ACK's eventbridge-controller ignores it. | Generated; no ACK pull request adds it |
| `ecr.aws.hfl.dev/v1alpha1` `ReplicationConfiguration` | An ECR registry's replication rules, put whole and put back when changed outside Kubernetes; delete puts no rules. ACK's ecr-controller has no replication. | ecr-controller PR #121 (API and behaviour), on PR #184's hand-written skeleton |
| `eventbridge.aws.hfl.dev/v1alpha1` `Connection` | An EventBridge connection: the credentials (basic, API key, OAuth) an API destination calls an endpoint with, its secrets read from Kubernetes Secrets. ACK's eventbridge-controller ignores it. | Generated; no ACK pull request adds it |
| `iam.aws.hfl.dev/v1alpha1` `RolePolicyAttachment` | Attaches one managed policy to one IAM role, and detaches it on delete. It is for a role ACK does not manage (made by another team, by Terraform, or by EKS for a node group). | Copied from ACK iam-controller pull request #184 (open) |
| `iam.aws.hfl.dev/v1alpha1` `UserGroupMembership` | An IAM user in a list of groups, by name or by reference to ACK's `User` and `Group`s. Not exclusive: only the listed groups are added or removed, and a group taken off the list is removed. ACK's iam-controller has no membership. | Hand-written on iam-controller PR #184's skeleton; no ACK pull request adds it |
| `rds.aws.hfl.dev/v1alpha1` `OptionGroup` | An RDS option group (MySQL, MariaDB, SQL Server and Oracle options such as the audit plugin), with its options added and removed through `ModifyOptionGroup`. | Generated from ACK rds-controller pull request #257 (open) |
| `rds.aws.hfl.dev/v1alpha1` `DBProxyTarget` | Registers an RDS instance or an Aurora cluster with an RDS Proxy. ACK's `DBProxy` makes a proxy but never registers what it fronts, so without this the proxy accepts no connections. It becomes synced once RDS Proxy reports every target `AVAILABLE`. | New: no ACK code or pull request covers it |
| `kms.aws.hfl.dev/v1alpha1` `ReplicaKey` | A multi-Region KMS key's replica in another Region: created with `ReplicateKey` in the primary key's Region, then read, changed (policy, tags) and deleted in its own Region, never touching the primary key. | Copied from ACK kms-controller pull request #112 (open), with its Region handling corrected: the pull request's read, update and delete act on the primary key |
| `lambda.aws.hfl.dev/v1alpha1` `Permission` | One statement of a Lambda function's resource-based policy (`AddPermission`), so that S3, an ALB, EventBridge or SNS may invoke the function; removed on delete, and added back if removed outside Kubernetes. ACK has permissions only inside an `Alias`. | New: the calls and policy reading are copied from ACK's `Alias`; no ACK pull request adds the kind |
| `rds.aws.hfl.dev/v1alpha1` `DBRoleAssociation` | Associates an IAM role with an Aurora cluster or an RDS instance for one feature (`s3Import`, `s3Export`, `Lambda`, `S3_INTEGRATION`), and removes it on delete. ACK's `DBCluster` and `DBInstance` only report associated roles. It becomes synced once RDS reports the association `ACTIVE`. | New: no ACK code or pull request covers it |

Each kind can reference ACK's own objects, the way ACK's kinds reference
each other: `RolePolicyAttachment.spec.roleRef` names an
`iam.services.k8s.aws` `Role`, and `DBProxyTarget.spec.dbProxyRef`,
`dbInstanceRef` and `dbClusterRef` name ACK's `DBProxy`, `DBInstance` and
`DBCluster`; `DBRoleAssociation.spec.dbClusterRef`, `dbInstanceRef` and
`roleRef` name ACK's `DBCluster`, `DBInstance` and `iam.services.k8s.aws`
`Role`; `Permission.spec.functionRef` names ACK's `lambda.services.k8s.aws`
`Function`; `ReplicaKey.spec.keyRef` names ACK's `kms.services.k8s.aws`
`Key`.

ACK's annotations mean what they mean in ACK, because the reconciler is
ACK's runtime: `services.k8s.aws/deletion-policy: retain`,
`services.k8s.aws/region`, adoption, the cross-account role maps (CARM)
and `IAMRoleSelector`.

## Where it sits

| | ACK | ack-extensions |
| --- | --- | --- |
| API groups | `<service>.services.k8s.aws` | `<service>.aws.hfl.dev` |
| Example | `rds.services.k8s.aws` `DBProxy` | `rds.aws.hfl.dev` `DBProxyTarget` |
| Process | one controller per service (`ack-rds-controller`) | one manager per service (`ack-extensions-rds-manager`), beside ACK's |
| Code | ACK's generator and runtime | ACK's generator (with one patch) and runtime, as Go modules |

The groups never clash with ACK's, so both run on one cluster, and a kind
moves from our group to ACK's when ACK ships it.

## Install

ack-extensions needs ACK's controller for the same service installed
first: the operator runs our manager beside it, with its settings.

```sh
kubectl kustomize config/default | KO_DOCKER_REPO=<registry> ko resolve -f - | kubectl apply --server-side -f -
```

This installs our CRDs, each manager's ClusterRole, and the operator in
`ack-extensions-system`. For each ACK controller the operator finds (by
its chart's label, `app.kubernetes.io/name: <service>-chart`), it runs our
manager in the same namespace with the controller's own arguments,
environment and credentials.

### AWS identity

By default (`--identity=own`) our manager runs as its own ServiceAccount,
`ack-extensions-<service>-manager`, which carries the ACK ServiceAccount's
`eks.amazonaws.com/role-arn` annotation. It acts as the same IAM role, but
holds only our kinds' rights in the cluster. AWS needs one more grant per
role:

- IRSA: add our ServiceAccount to the role's trust policy, beside ACK's
  (`system:serviceaccount:<namespace>:ack-extensions-<service>-manager`).
- EKS Pod Identity: one more association, for our ServiceAccount, to the
  same role.
- A credentials Secret (`aws.credentials.secretName` in ACK's chart): no
  step; our manager mounts the same Secret.

With `--identity=ack`, or the annotation `aws.hfl.dev/identity: ack` on
ACK's controller Deployment, our manager runs as ACK's ServiceAccount
instead. That needs no AWS change, but our manager then holds every
Kubernetes right ACK's controller holds, including full control of ACK's
own kinds. `docs/plan.md`, section 7, compares the two.

## Develop

```sh
make tools         # ACK's generator at the pin, controller-gen, goimports, setup-envtest, moto
make check         # generated files current, gofmt, vet, SPDX lines, build, tests
make operator-e2e  # the operator on kind beside ACK's real rds-controller, against moto
```

The tests use moto, a fake AWS, in server mode. Nothing here uses real
AWS credentials. See `CONTRIBUTING.md`, and `docs/plan.md` for how the
repository is built, kept in step with ACK, and tested.

## Licence

Apache-2.0 (`LICENSE`). Files copied or generated from ACK keep Amazon's
copyright and licence header (`NOTICE`, `UPSTREAM.md`).
