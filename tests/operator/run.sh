#!/usr/bin/env bash
# Copyright 2026 High Five Labs
# SPDX-License-Identifier: Apache-2.0
#
# The operator on kind, beside ACK's real rds-controller, against the fake
# AWS (moto) in the cluster:
#
#   1. a kind cluster (reused if it exists), moto in fake-aws;
#   2. ACK's rds-controller installed from its own Helm chart, at the
#      version go.mod pins, the way its docs say: a credentials Secret, and
#      an IRSA role ARN on its ServiceAccount (IRSA itself needs EKS; the
#      annotation is what our operator must carry over);
#   3. ours (config/default) built by ko into the cluster;
#   4. checks: the CRDs, our manager beside ACK's with ACK's arguments,
#      credentials and role ARN, its narrow rights, gap kinds reconciling
#      against moto, and the switch to sharing ACK's ServiceAccount;
#   5. ACK's real organizations-controller, from its own chart: refused
#      beside the rds-controller, run alone in its own namespace with no
#      IRSA annotation (EKS Pod Identity binds it on EKS), and a Policy
#      reconciling against moto through its manager.
#   6. Cloud Control: a CloudControlContext in one namespace (an
#      environment) runs a manager for it in the operator's namespace, as
#      the environment's own ServiceAccount (no IRSA annotation: EKS Pod
#      Identity binds it, which kind cannot show), with rights in the
#      environment's namespace only; it takes up that namespace's objects
#      and leaves another namespace's alone; it goes with the context.
#
# Needs docker, kind, kubectl, helm, ko, crane, and hack/moto.sh (boto3
# for the fixtures). Nothing touches real AWS.
set -o errexit -o nounset -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
export GOTOOLCHAIN=local
CLUSTER="${CLUSTER:-ack-extensions-operator}"
NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.37.0}"
ACK_NS=ack-system
ACK_ROLE="arn:aws:iam::123456789012:role/ack-rds-controller"
go mod download github.com/aws-controllers-k8s/rds-controller
ACK_RDS_VERSION="$(go list -m -f '{{.Version}}' github.com/aws-controllers-k8s/rds-controller)"
ACK_RDS_DIR="$(go list -m -f '{{.Dir}}' github.com/aws-controllers-k8s/rds-controller)"
ACK_RDS_IMAGE="public.ecr.aws/aws-controllers-k8s/rds-controller:${ACK_RDS_VERSION#v}"
go mod download github.com/aws-controllers-k8s/organizations-controller
ACK_ORG_VERSION="$(go list -m -f '{{.Version}}' github.com/aws-controllers-k8s/organizations-controller)"
ACK_ORG_DIR="$(go list -m -f '{{.Dir}}' github.com/aws-controllers-k8s/organizations-controller)"
ACK_ORG_IMAGE="public.ecr.aws/aws-controllers-k8s/organizations-controller:${ACK_ORG_VERSION#v}"
ORG_NS=ack-organizations-system
MOTO_DIGEST="$(sed -n 's/^#  *\(sha256:[0-9a-f]\{64\}\)$/\1/p' config/fake-aws/moto.yaml)"
MOTO_IMAGE="$(sed -n 's/^ *image: \(docker.io\/motoserver\/moto:.*\)$/\1/p' config/fake-aws/moto.yaml)"
PY="${ROOT}/.cache/moto-venv/bin/python"
arch="$(go env GOARCH)"

# Docker Desktop's credential helper blocks on the keychain; every image
# here is public, so pull and push with an empty Docker config.
DOCKER_CONFIG="$(mktemp -d)"
echo '{}' >"${DOCKER_CONFIG}/config.json"
export DOCKER_CONFIG
work="$(mktemp -d)"
pf_pid=""
cleanup() { [[ -n "${pf_pid}" ]] && kill "${pf_pid}" 2>/dev/null || true; rm -rf "${work}" "${DOCKER_CONFIG}"; }
trap cleanup EXIT

ok() { echo "ok: $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
retry() { local n=0; until "$@"; do n=$((n + 1)); [[ ${n} -ge 6 ]] && return 1; sleep $((n * 5)); done; }
# fake_py: the only way Python (boto3) runs here (hack/noaws checks it):
# an environment built from nothing, fake credentials, every endpoint at the
# port-forward to moto, and every other host behind a closed proxy port.
fake_py() {
  env -i PATH="${PATH}" HOME="${work}" \
    AWS_ACCESS_KEY_ID=testing AWS_SECRET_ACCESS_KEY=testing \
    AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null AWS_EC2_METADATA_DISABLED=true \
    AWS_ENDPOINT_URL=http://127.0.0.1:15000 \
    HTTP_PROXY=http://127.0.0.1:9 HTTPS_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost \
    "${PY}" -I "$@"
}
# verify_no_real_aws: every AWS client in the cluster (ACK's controller,
# our managers, the Cloud Control managers) has every endpoint at the fake
# and the dead proxy (pkg/fakeaws Verify); fails if there is none.
verify_no_real_aws() {
  kubectl get deploy -A -o json >"${work}/deployments.json"
  go run ./hack/noaws verify-deployments <"${work}/deployments.json" || fail "an AWS client in the cluster could reach real AWS"
}
wait_for() { # wait_for <seconds> <description> <command...>
  local t="$1" what="$2"; shift 2
  local end=$((SECONDS + t))
  until "$@" >/dev/null 2>&1; do
    [[ ${SECONDS} -ge ${end} ]] && fail "${what} (not within ${t}s)"
    sleep 2
  done
  ok "${what}"
}

[[ -x "${PY}" ]] || hack/moto.sh

echo "== cluster ${CLUSTER}"
kind get clusters | grep -qx "${CLUSTER}" || kind create cluster --name "${CLUSTER}" --image "${NODE_IMAGE}" --wait 120s
kubectl config use-context "kind-${CLUSTER}" >/dev/null

echo "== images"
[[ -n "${MOTO_DIGEST}" && -n "${MOTO_IMAGE}" ]] || fail "could not read moto's image and digest from config/fake-aws/moto.yaml"
got="$(retry crane digest "${MOTO_IMAGE}")"
[[ "${got}" == "${MOTO_DIGEST}" ]] || fail "${MOTO_IMAGE} is now ${got}, config/fake-aws/moto.yaml pins ${MOTO_DIGEST}"
retry crane pull --platform "linux/${arch}" "${MOTO_IMAGE}" "${work}/moto.tar"
kind load image-archive --name "${CLUSTER}" "${work}/moto.tar" >/dev/null
retry crane pull --platform "linux/${arch}" "${ACK_RDS_IMAGE}" "${work}/ack-rds.tar"
kind load image-archive --name "${CLUSTER}" "${work}/ack-rds.tar" >/dev/null
retry crane pull --platform "linux/${arch}" "${ACK_ORG_IMAGE}" "${work}/ack-org.tar"
kind load image-archive --name "${CLUSTER}" "${work}/ack-org.tar" >/dev/null
ok "moto, ${ACK_RDS_IMAGE} and ${ACK_ORG_IMAGE} loaded"

echo "== fake AWS"
kubectl apply -f config/fake-aws/moto.yaml >/dev/null
kubectl -n fake-aws rollout status deploy/moto --timeout=180s >/dev/null
ok "moto ready in fake-aws"
# The one setting: every AWS endpoint of everything in the cluster is this
# (pkg/fakeaws). The Kubernetes API is the only other address reached.
FAKE_AWS="http://moto.fake-aws.svc.cluster.local:5000"
KUBE_HOST="$(kubectl get svc kubernetes -n default -o jsonpath='{.spec.clusterIP}')"

echo "== ACK's rds-controller ${ACK_RDS_VERSION}, from its own chart"
kubectl create namespace "${ACK_NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "${ACK_NS}" create secret generic aws-creds \
  --from-literal=credentials=$'[default]\naws_access_key_id = testing\naws_secret_access_key = testing\n' \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
helm upgrade --install ack-rds "${ACK_RDS_DIR}/helm" -n "${ACK_NS}" \
  --set aws.region=us-west-2 \
  --set aws.endpoint_url="${FAKE_AWS}" \
  --set aws.identity_endpoint_url="${FAKE_AWS}" \
  --set aws.allow_unsafe_aws_endpoint_urls=true \
  --set aws.credentials.secretName=aws-creds \
  --set-string "serviceAccount.annotations.eks\.amazonaws\.com/role-arn=${ACK_ROLE}" \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 180s >/dev/null
ACK_DEPLOY="$(kubectl -n "${ACK_NS}" get deploy -l app.kubernetes.io/name=rds-chart -o jsonpath='{.items[0].metadata.name}')"
# ACK's chart sets the endpoints; the rest of the fake's environment (the
# dead proxy, AWS_ENDPOINT_URL for every other client) from the same URL.
# shellcheck disable=SC2046
kubectl -n "${ACK_NS}" set env "deploy/${ACK_DEPLOY}" $(go run ./hack/noaws env "${FAKE_AWS}" "${KUBE_HOST}") >/dev/null
kubectl -n "${ACK_NS}" rollout status "deploy/${ACK_DEPLOY}" --timeout=180s >/dev/null
ok "ACK's controller ${ACK_NS}/${ACK_DEPLOY} running as ${ACK_NS}/ack-rds-controller"

echo "== ours: config/default by ko"
kubectl kustomize config/default \
  | KO_DOCKER_REPO=kind.local KIND_CLUSTER_NAME="${CLUSTER}" ko resolve --platform="linux/${arch}" -f - >"${work}/ours.yaml"
kubectl apply --server-side -f "${work}/ours.yaml" >/dev/null
# The operator in fake-AWS mode, one flag: every manager it makes (beside
# ACK's controllers and per environment) gets every endpoint at the fake,
# fake credentials and the dead proxy, and is refused if it could still
# reach real AWS (pkg/operator FakeAWS). A first run here, 2026-11-09, gave
# the Cloud Control manager only the service endpoint, so ACK's runtime
# asked real STS who it was (with the dummy key; refused by AWS).
kubectl -n ack-extensions-system patch deploy ack-extensions-operator --type=json -p '[
  {"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--fake-aws-endpoint='"${FAKE_AWS}"'"}
]' >/dev/null
kubectl -n ack-extensions-system rollout status deploy/ack-extensions-operator --timeout=180s >/dev/null
for crd in scheduledactions.applicationautoscaling.aws.hfl.dev scalingpolicies.autoscaling.aws.hfl.dev scheduledactions.autoscaling.aws.hfl.dev customergateways.ec2.aws.hfl.dev volumeattachments.ec2.aws.hfl.dev volumes.ec2.aws.hfl.dev vpngateways.ec2.aws.hfl.dev replicationconfigurations.ecr.aws.hfl.dev apidestinations.eventbridge.aws.hfl.dev connections.eventbridge.aws.hfl.dev rolepolicyattachments.iam.aws.hfl.dev accountpasswordpolicies.iam.aws.hfl.dev accountaliases.iam.aws.hfl.dev sshpublickeys.iam.aws.hfl.dev loginprofiles.iam.aws.hfl.dev usergroupmemberships.iam.aws.hfl.dev replicakeys.kms.aws.hfl.dev permissions.lambda.aws.hfl.dev delegatedadministrators.organizations.aws.hfl.dev organizations.organizations.aws.hfl.dev policies.organizations.aws.hfl.dev policyattachments.organizations.aws.hfl.dev dbproxytargets.rds.aws.hfl.dev dbroleassociations.rds.aws.hfl.dev optiongroups.rds.aws.hfl.dev schedulegroups.scheduler.aws.hfl.dev schedules.scheduler.aws.hfl.dev webaclassociations.wafv2.aws.hfl.dev vpnconnections.ec2.aws.hfl.dev cloudcontrolcontexts.cloudcontrol.aws.hfl.dev samlproviders.iam.cloudcontrol.aws.hfl.dev transitgatewayroutetables.ec2.cloudcontrol.aws.hfl.dev detectors.guardduty.cloudcontrol.aws.hfl.dev permissionsets.sso.cloudcontrol.aws.hfl.dev assignments.sso.cloudcontrol.aws.hfl.dev instanceaccesscontrolattributeconfigurations.sso.cloudcontrol.aws.hfl.dev transitgatewayroutes.ec2.cloudcontrol.aws.hfl.dev transitgatewayroutetableassociations.ec2.cloudcontrol.aws.hfl.dev transitgatewayroutetablepropagations.ec2.cloudcontrol.aws.hfl.dev transitgatewaypeeringattachments.ec2.cloudcontrol.aws.hfl.dev enabledcontrols.controltower.cloudcontrol.aws.hfl.dev enabledbaselines.controltower.cloudcontrol.aws.hfl.dev; do
  kubectl get crd "${crd}" >/dev/null || fail "CRD ${crd} missing"
done
ok "CRDs installed: scheduledactions.applicationautoscaling, scalingpolicies.autoscaling, scheduledactions.autoscaling, customergateways.ec2, volumeattachments.ec2, volumes.ec2, vpngateways.ec2, replicationconfigurations.ecr, apidestinations.eventbridge, connections.eventbridge, rolepolicyattachments.iam, accountpasswordpolicies.iam, accountaliases.iam, sshpublickeys.iam, loginprofiles.iam, usergroupmemberships.iam, replicakeys.kms, permissions.lambda, delegatedadministrators.organizations, organizations.organizations, policies.organizations, policyattachments.organizations, dbproxytargets.rds, dbroleassociations.rds, optiongroups.rds, schedulegroups.scheduler, schedules.scheduler, webaclassociations.wafv2, vpnconnections.ec2 (*.aws.hfl.dev); cloudcontrolcontexts.cloudcontrol, samlproviders.iam, transitgatewayroutetables.ec2, detectors.guardduty, permissionsets.sso, assignments.sso, instanceaccesscontrolattributeconfigurations.sso, transitgatewayroutes.ec2, transitgatewayroutetableassociations.ec2, transitgatewayroutetablepropagations.ec2, transitgatewaypeeringattachments.ec2, enabledcontrols.controltower, enabledbaselines.controltower (*.cloudcontrol.aws.hfl.dev); operator running"

echo "== retain-only kinds: config/admission's ValidatingAdmissionPolicy"
# An Organization is never deleted from Kubernetes (the controller has no
# DeleteOrganization); the policy makes every one say so with ACK's
# deletion-policy annotation. No organizations manager runs here, so the
# objects get no finalizer and nothing reconciles them.
org_json() { printf '{"apiVersion":"organizations.aws.hfl.dev/v1alpha1","kind":"Organization","metadata":{"name":"org","namespace":"default"%s},"spec":{"featureSet":"ALL"}}' "$1"; }
org_json "" >"${work}/org-bare.json"
org_json ',"annotations":{"services.k8s.aws/deletion-policy":"delete"}' >"${work}/org-delete.json"
org_json ',"annotations":{"services.k8s.aws/deletion-policy":"retain"}' >"${work}/org-retain.json"
refused() { # refused <what> <command...>: the command fails with the policy's message
  local what="$1" out
  shift
  if out="$("$@" 2>&1)"; then fail "${what}: accepted"; fi
  [[ "${out}" == *"must carry the annotation services.k8s.aws/deletion-policy: retain"* ]] || fail "${what}: ${out}"
  ok "${what}: refused by ack-extensions-retain-only"
}
policy_in_force() { ! kubectl apply --dry-run=server -f "${work}/org-bare.json"; }
wait_for 60 "the retain-only policy is in force" policy_in_force
refused "an Organization without the annotation" kubectl apply --dry-run=server -f "${work}/org-bare.json"
refused "an Organization with deletion-policy delete" kubectl apply --dry-run=server -f "${work}/org-delete.json"
kubectl apply -f "${work}/org-retain.json" >/dev/null || fail "an Organization with deletion-policy retain was refused"
ok "an Organization with deletion-policy retain is accepted"
refused "the annotation changed to delete" kubectl -n default annotate organizations.organizations.aws.hfl.dev org services.k8s.aws/deletion-policy=delete --overwrite
refused "the annotation removed" kubectl -n default annotate organizations.organizations.aws.hfl.dev org services.k8s.aws/deletion-policy-
kubectl delete -f "${work}/org-retain.json" --wait=true >/dev/null || fail "the Organization could not be deleted"
ok "the Organization object deleted (only the object: the controller has no DeleteOrganization)"

echo "== identity own: our manager beside ACK's, as ACK's role"
wait_for 180 "manager ${ACK_NS}/ack-extensions-rds-manager rolled out" \
  kubectl -n "${ACK_NS}" rollout status deploy/ack-extensions-rds-manager --timeout=5s
pod_sa="$(kubectl -n "${ACK_NS}" get pod -l app.kubernetes.io/name=ack-extensions-rds-manager -o jsonpath='{.items[0].spec.serviceAccountName}')"
[[ "${pod_sa}" == ack-extensions-rds-manager ]] || fail "manager pod runs as ${pod_sa}"
sa_role="$(kubectl -n "${ACK_NS}" get sa ack-extensions-rds-manager -o jsonpath='{.metadata.annotations.eks\.amazonaws\.com/role-arn}')"
[[ "${sa_role}" == "${ACK_ROLE}" ]] || fail "our ServiceAccount's role ARN is '${sa_role}'"
ok "manager pod runs as ${ACK_NS}/ack-extensions-rds-manager, annotated with ACK's role ${sa_role}"
ack_secret="$(kubectl -n "${ACK_NS}" get deploy "${ACK_DEPLOY}" -o jsonpath='{.spec.template.spec.volumes[?(@.secret)].secret.secretName}')"
our_secret="$(kubectl -n "${ACK_NS}" get deploy ack-extensions-rds-manager -o jsonpath='{.spec.template.spec.volumes[?(@.secret)].secret.secretName}')"
[[ -n "${ack_secret}" && "${ack_secret}" == "${our_secret}" ]] || fail "credentials Secret: ACK '${ack_secret}', ours '${our_secret}'"
ack_region="$(kubectl -n "${ACK_NS}" get deploy "${ACK_DEPLOY}" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="AWS_REGION")].value}')"
our_region="$(kubectl -n "${ACK_NS}" get deploy ack-extensions-rds-manager -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="AWS_REGION")].value}')"
[[ "${ack_region}" == "${our_region}" ]] || fail "region: ACK ${ack_region}, ours ${our_region}"
ok "same credentials Secret (${our_secret}), region (${our_region}) and endpoint as ACK's controller"
verify_no_real_aws
for svc in applicationautoscaling autoscaling ec2 ecr eventbridge iam kms lambda organizations scheduler wafv2; do
  kubectl get deploy -A -l "aws.hfl.dev/service=${svc}" -o name | grep -q . && fail "a ${svc} manager without an ACK ${svc} controller"
done
ok "no applicationautoscaling, autoscaling, ec2, ecr, eventbridge, iam, kms, lambda, organizations, scheduler or wafv2 manager: none of their ACK controllers (EventBridge's, for scheduler) is installed"

can() { kubectl auth can-i "$1" "$2" --as="system:serviceaccount:${ACK_NS}:$3" ${4:+-n "$4"} 2>/dev/null; }
[[ "$(can create dbproxytargets.rds.aws.hfl.dev ack-extensions-rds-manager default)" == yes ]] || fail "our manager cannot create dbproxytargets"
[[ "$(can delete dbinstances.rds.services.k8s.aws ack-extensions-rds-manager default)" == no ]] || fail "our manager may delete ACK's DBInstances"
[[ "$(can get dbproxies.rds.services.k8s.aws ack-extensions-rds-manager default)" == yes ]] || fail "our manager cannot read ACK's DBProxies (references)"
# ACK's iam CRDs are not installed here (no iam-controller), so `kubectl
# auth can-i` cannot resolve roles.iam.services.k8s.aws through discovery
# and would ask about a core resource of that literal name instead. A
# SubjectAccessReview names the group and resource itself.
sar() { # sar <verb> <group> <resource> <serviceaccount> <namespace>: prints true or false
  local allowed
  allowed="$(kubectl create -o jsonpath='{.status.allowed}' -f - <<EOF
apiVersion: authorization.k8s.io/v1
kind: SubjectAccessReview
spec:
  user: system:serviceaccount:${ACK_NS}:$4
  groups: [system:serviceaccounts, "system:serviceaccounts:${ACK_NS}", system:authenticated]
  resourceAttributes: {verb: $1, group: $2, resource: $3, namespace: $5}
EOF
)"
  [[ "${allowed}" == true ]] && echo true || echo false
}
[[ "$(sar get iam.services.k8s.aws roles ack-extensions-rds-manager default)" == true ]] || fail "our manager cannot read ACK's Roles (references)"
[[ "$(sar delete iam.services.k8s.aws roles ack-extensions-rds-manager default)" == false ]] || fail "our manager may delete ACK's Roles"
[[ "$(can create dbproxytargets.rds.aws.hfl.dev ack-rds-controller default)" == no ]] || fail "ACK's controller may create our kinds"
ok "rights: ours manages *.rds.aws.hfl.dev and only reads ACK's rds kinds and Roles; ACK's controller has nothing of ours"

echo "== gap kinds reconcile against moto"
kubectl -n fake-aws port-forward svc/moto 15000:5000 >/dev/null 2>&1 &
pf_pid=$!
wait_for 30 "port-forward to moto" curl -fsS http://127.0.0.1:15000/moto-api/
fake_py - <<'PYEOF'
import boto3
kw = dict(endpoint_url="http://127.0.0.1:15000", region_name="us-west-2", aws_access_key_id="testing", aws_secret_access_key="testing")
ec2, rds = boto3.client("ec2", **kw), boto3.client("rds", **kw)
if not rds.describe_db_proxies()["DBProxies"]:
    vpc = ec2.create_vpc(CidrBlock="10.30.0.0/16")["Vpc"]["VpcId"]
    subnets = [ec2.create_subnet(VpcId=vpc, CidrBlock=f"10.30.{i}.0/24", AvailabilityZone=az)["Subnet"]["SubnetId"] for i, az in enumerate(["us-west-2a", "us-west-2b"])]
    rds.create_db_instance(DBInstanceIdentifier="app-db", Engine="postgres", DBInstanceClass="db.t4g.micro", AllocatedStorage=20, MasterUsername="app", MasterUserPassword="not-a-real-password")
    rds.create_db_proxy(DBProxyName="app-proxy", EngineFamily="POSTGRESQL", RoleArn="arn:aws:iam::123456789012:role/proxy", VpcSubnetIds=subnets,
                        Auth=[{"AuthScheme": "SECRETS", "SecretArn": "arn:aws:secretsmanager:us-west-2:123456789012:secret:app-db"}])
print("fixtures: app-proxy and app-db in moto")
PYEOF
cat <<'YAML' | kubectl apply -f - >/dev/null
apiVersion: rds.aws.hfl.dev/v1alpha1
kind: DBProxyTarget
metadata: {name: app-db, namespace: default}
spec: {dbProxyName: app-proxy, dbInstanceIdentifier: app-db}
---
apiVersion: rds.aws.hfl.dev/v1alpha1
kind: OptionGroup
metadata: {name: app-mysql, namespace: default}
spec: {name: app-mysql, description: app options, engineName: mysql, majorEngineVersion: "8.0", options: [{optionName: MARIADB_AUDIT_PLUGIN}]}
---
apiVersion: rds.aws.hfl.dev/v1alpha1
kind: DBRoleAssociation
metadata: {name: app-db-s3-import, namespace: default}
spec: {dbInstanceIdentifier: app-db, roleARN: "arn:aws:iam::123456789012:role/app-db-s3", featureName: s3Import}
YAML
synced() { [[ "$(kubectl get "$1" -o jsonpath='{.status.conditions[?(@.type=="ACK.ResourceSynced")].status}')" == True ]]; }
wait_for 120 "DBProxyTarget default/app-db synced through the manager on kind" synced dbproxytarget.rds.aws.hfl.dev/app-db
wait_for 180 "OptionGroup default/app-mysql synced through the manager on kind" synced optiongroup.rds.aws.hfl.dev/app-mysql
wait_for 120 "DBRoleAssociation default/app-db-s3-import synced through the manager on kind" synced dbroleassociation.rds.aws.hfl.dev/app-db-s3-import
fake_py - <<'PYEOF'
import boto3
kw = dict(endpoint_url="http://127.0.0.1:15000", region_name="us-west-2", aws_access_key_id="testing", aws_secret_access_key="testing")
rds = boto3.client("rds", **kw)
t = [x["RdsResourceId"] for x in rds.describe_db_proxy_targets(DBProxyName="app-proxy")["Targets"]]
o = [x["OptionName"] for x in rds.describe_option_groups(OptionGroupName="app-mysql")["OptionGroupsList"][0]["Options"]]
assert t == ["app-db"], t
assert o == ["MARIADB_AUDIT_PLUGIN"], o
r = [(x["RoleArn"].split("/")[-1], x["FeatureName"]) for x in rds.describe_db_instances(DBInstanceIdentifier="app-db")["DBInstances"][0]["AssociatedRoles"]]
assert r == [("app-db-s3", "s3Import")], r
print("ok: in moto: app-proxy targets", t, "; app-mysql options", o, "; app-db roles", r)
PYEOF

echo "== identity ack: share ACK's ServiceAccount"
kubectl -n "${ACK_NS}" annotate deploy "${ACK_DEPLOY}" aws.hfl.dev/identity=ack --overwrite >/dev/null
runs_as_ack() { [[ "$(kubectl -n "${ACK_NS}" get deploy ack-extensions-rds-manager -o jsonpath='{.spec.template.spec.serviceAccountName}')" == ack-rds-controller ]]; }
wait_for 60 "manager switched to ${ACK_NS}/ack-rds-controller" runs_as_ack
gone() { ! kubectl -n "${ACK_NS}" get sa ack-extensions-rds-manager >/dev/null 2>&1; }
wait_for 60 "our ServiceAccount removed" gone
kubectl -n "${ACK_NS}" rollout status deploy/ack-extensions-rds-manager --timeout=120s >/dev/null
[[ "$(can create dbproxytargets.rds.aws.hfl.dev ack-rds-controller default)" == yes ]] || fail "ACK's ServiceAccount was not bound to our role"
ok "shared: ACK's ServiceAccount now also manages our kinds"
kubectl -n "${ACK_NS}" annotate deploy "${ACK_DEPLOY}" aws.hfl.dev/identity- >/dev/null
wait_for 60 "back to identity own" bash -c "[[ \$(kubectl -n ${ACK_NS} get deploy ack-extensions-rds-manager -o jsonpath='{.spec.template.spec.serviceAccountName}') == ack-extensions-rds-manager ]]"

echo "== organization-scoped: ACK's organizations-controller ${ACK_ORG_VERSION} runs alone (docs/plan.md 7.5)"
# Installed the way plan 7.5 says, but for the namespace: no IRSA annotation
# on its ServiceAccount (EKS Pod Identity binds the role, outside the
# cluster), the fake AWS's credentials Secret standing in for it here.
org_deploy() { kubectl -n "$1" get deploy -l app.kubernetes.io/name=organizations-chart -o jsonpath='{.items[0].metadata.name}'; }
ack_org() { # ack_org <namespace>: install ACK's organizations-controller there
  kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$1" create secret generic aws-creds \
    --from-literal=credentials=$'[default]\naws_access_key_id = testing\naws_secret_access_key = testing\n' \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  helm upgrade --install ack-organizations "${ACK_ORG_DIR}/helm" -n "$1" \
    --set aws.region=us-west-2 \
    --set aws.endpoint_url="${FAKE_AWS}" \
    --set aws.identity_endpoint_url="${FAKE_AWS}" \
    --set aws.allow_unsafe_aws_endpoint_urls=true \
    --set aws.credentials.secretName=aws-creds \
    --set image.pullPolicy=IfNotPresent \
    --wait --timeout 180s >/dev/null
  # The rest of the fake's environment (the dead proxy, AWS_ENDPOINT_URL),
  # as for ACK's rds-controller above.
  # shellcheck disable=SC2046
  kubectl -n "$1" set env "deploy/$(org_deploy "$1")" $(go run ./hack/noaws env "${FAKE_AWS}" "${KUBE_HOST}") >/dev/null
  kubectl -n "$1" rollout status "deploy/$(org_deploy "$1")" --timeout=180s >/dev/null
}
ack_org "${ACK_NS}"
ORG_DEPLOY="$(org_deploy "${ACK_NS}")"
ok "ACK's organizations-controller ${ACK_NS}/${ORG_DEPLOY} running beside ACK's rds-controller"
refused_event() {
  kubectl -n "${ACK_NS}" get events --field-selector "involvedObject.kind=Deployment,involvedObject.name=${ORG_DEPLOY},reason=SharedNamespace,type=Warning" \
    -o jsonpath='{.items[*].message}' | grep -q 'account-level rds controller'
}
wait_for 60 "refused: Warning SharedNamespace on ${ACK_NS}/${ORG_DEPLOY}" refused_event
kubectl -n "${ACK_NS}" get deploy,sa -l aws.hfl.dev/service=organizations -o name | grep -q . && fail "an organizations manager runs beside ACK's rds-controller"
kubectl -n "${ACK_NS}" rollout status deploy/ack-extensions-rds-manager --timeout=30s >/dev/null || fail "the rds manager stopped"
ok "no organizations manager in ${ACK_NS}; the rds manager still runs"

helm uninstall ack-organizations -n "${ACK_NS}" --wait >/dev/null
ack_org "${ORG_NS}"
ORG_DEPLOY="$(org_deploy "${ORG_NS}")"
ok "ACK's organizations-controller moved to ${ORG_NS}/${ORG_DEPLOY}, alone"
wait_for 180 "manager ${ORG_NS}/ack-extensions-organizations-manager rolled out" \
  kubectl -n "${ORG_NS}" rollout status deploy/ack-extensions-organizations-manager --timeout=5s
org_sa_ann="$(kubectl -n "${ORG_NS}" get sa ack-extensions-organizations-manager -o jsonpath='{.metadata.annotations}')"
[[ "${org_sa_ann}" != *eks.amazonaws.com* ]] || fail "our organizations ServiceAccount carries ${org_sa_ann}"
ok "its ServiceAccount ${ORG_NS}/ack-extensions-organizations-manager carries no IRSA annotation (Pod Identity: one association for it on EKS)"
fake_py - <<'PYEOF'
import boto3
kw = dict(endpoint_url="http://127.0.0.1:15000", region_name="us-west-2", aws_access_key_id="testing", aws_secret_access_key="testing")
org = boto3.client("organizations", **kw)
try:
    org.describe_organization()
except org.exceptions.AWSOrganizationsNotInUseException:
    org.create_organization(FeatureSet="ALL")
print("fixtures: an organization in moto")
PYEOF
cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: organizations.aws.hfl.dev/v1alpha1
kind: Policy
metadata: {name: deny-leave, namespace: ${ORG_NS}}
spec:
  name: deny-leave
  description: no account leaves the organization
  type_: SERVICE_CONTROL_POLICY
  content: '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"organizations:LeaveOrganization","Resource":"*"}]}'
YAML
synced_in() { [[ "$(kubectl -n "$1" get "$2" -o jsonpath='{.status.conditions[?(@.type=="ACK.ResourceSynced")].status}')" == True ]]; }
wait_for 120 "Policy ${ORG_NS}/deny-leave synced through the organizations manager on kind" synced_in "${ORG_NS}" policy.organizations.aws.hfl.dev/deny-leave
fake_py - <<'PYEOF'
import boto3
kw = dict(endpoint_url="http://127.0.0.1:15000", region_name="us-west-2", aws_access_key_id="testing", aws_secret_access_key="testing")
org = boto3.client("organizations", **kw)
names = [p["Name"] for p in org.list_policies(Filter="SERVICE_CONTROL_POLICY")["Policies"]]
assert "deny-leave" in names, names
print("ok: in moto: service control policies", names)
PYEOF
kubectl -n "${ORG_NS}" delete policy.organizations.aws.hfl.dev deny-leave --wait=true >/dev/null

echo "== Cloud Control: one manager per environment (namespace), as that environment's identity"
CC_ROLE="arn:aws:iam::123456789012:role/team-a-cloudcontrol"
CC_SA=ack-extensions-cloudcontrol-team-a
OP_NS=ack-extensions-system
for n in team-a team-b; do kubectl create namespace "${n}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null; done
kubectl get deploy -A -l aws.hfl.dev/component=cloudcontrol -o name | grep -q . && fail "a Cloud Control manager without a CloudControlContext"
if kubectl apply -f - >/dev/null 2>"${work}/cc-name.err" <<YAML
apiVersion: cloudcontrol.aws.hfl.dev/v1alpha1
kind: CloudControlContext
metadata: {name: not-cloudcontrol, namespace: team-a}
spec: {roleARN: "${CC_ROLE}", region: us-west-2}
YAML
then fail "a CloudControlContext not named cloudcontrol was accepted"; fi
grep -q "must be named cloudcontrol" "${work}/cc-name.err" || fail "unexpected refusal: $(cat "${work}/cc-name.err")"
ok "a context not named cloudcontrol is refused: one per namespace"
cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: cloudcontrol.aws.hfl.dev/v1alpha1
kind: CloudControlContext
metadata: {name: cloudcontrol, namespace: team-a}
spec:
  roleARN: "${CC_ROLE}"
  region: us-west-2
  kinds: [SAMLProvider.iam, Detector.guardduty]
YAML
cc_ready() { [[ "$(kubectl -n team-a get cloudcontrolcontext cloudcontrol -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" == True ]]; }
cc_debug() {
  echo "-- context"; kubectl -n team-a get cloudcontrolcontext cloudcontrol -o yaml || true
  echo "-- manager"; kubectl -n "${OP_NS}" get deploy,pod -l aws.hfl.dev/environment=team-a -o wide || true
  kubectl -n "${OP_NS}" describe pod -l aws.hfl.dev/environment=team-a | tail -30 || true
  kubectl -n "${OP_NS}" logs -l aws.hfl.dev/environment=team-a --tail=60 --all-containers || true
  kubectl -n "${OP_NS}" logs -l aws.hfl.dev/environment=team-a --tail=60 --previous 2>/dev/null || true
  echo "-- operator"; kubectl -n "${OP_NS}" logs deploy/ack-extensions-operator --tail=40 || true
}
end=$((SECONDS + 240))
until cc_ready; do
  [[ ${SECONDS} -ge ${end} ]] && { cc_debug; fail "CloudControlContext team-a/cloudcontrol Ready: its manager rolled out (not within 240s)"; }
  sleep 2
done
ok "CloudControlContext team-a/cloudcontrol Ready: its manager rolled out"
cc_pod_sa="$(kubectl -n "${OP_NS}" get pod -l aws.hfl.dev/environment=team-a -o jsonpath='{.items[0].spec.serviceAccountName}')"
[[ "${cc_pod_sa}" == "${CC_SA}" ]] || fail "the Cloud Control manager runs as ${cc_pod_sa}"
kubectl get pod -n team-a -o name | grep -q . && fail "a pod in team-a: the manager belongs in ${OP_NS}"
kubectl -n "${OP_NS}" get sa "${CC_SA}" -o jsonpath='{.metadata.annotations}' | grep -q 'eks.amazonaws.com' && fail "an IRSA annotation on ${CC_SA}: the identity is EKS Pod Identity"
cc_args="$(kubectl -n "${OP_NS}" get deploy "${CC_SA}" -o jsonpath='{.spec.template.spec.containers[0].args}')"
cc_env="$(kubectl -n "${OP_NS}" get deploy "${CC_SA}" -o jsonpath='{.spec.template.spec.containers[0].env}')"
grep -q '127.0.0.1:9' <<<"${cc_env}" || fail "the manager has no dead proxy: ${cc_env}"
for a in --watch-namespace=team-a --aws-region=us-west-2 --cloudcontrol-kinds=Detector.guardduty,SAMLProvider.iam; do
  grep -q -- "${a}" <<<"${cc_args}" || fail "manager args lack ${a}: ${cc_args}"
done
pia="$(kubectl -n team-a get cloudcontrolcontext cloudcontrol -o jsonpath='{.status.podIdentityAssociation.namespace}/{.status.podIdentityAssociation.serviceAccount} -> {.status.podIdentityAssociation.roleARN}')"
[[ "${pia}" == "${OP_NS}/${CC_SA} -> ${CC_ROLE}" ]] || fail "status.podIdentityAssociation: ${pia}"
ok "team-a's manager runs in ${OP_NS} as ${CC_SA} (no IRSA annotation), watching team-a; the association it needs: ${pia}"
ccsar() { # ccsar <verb> <group> <resource> <namespace>: may team-a's manager?
  local allowed
  allowed="$(kubectl create -o jsonpath='{.status.allowed}' -f - <<SAR
apiVersion: authorization.k8s.io/v1
kind: SubjectAccessReview
spec:
  user: system:serviceaccount:${OP_NS}:${CC_SA}
  groups: [system:serviceaccounts, "system:serviceaccounts:${OP_NS}", system:authenticated]
  resourceAttributes: {verb: $1, group: "$2", resource: $3, namespace: "$4"}
SAR
)"
  [[ "${allowed}" == true ]] && echo true || echo false
}
[[ "$(ccsar create iam.cloudcontrol.aws.hfl.dev samlproviders team-a)" == true ]] || fail "team-a's manager cannot create SAMLProviders in team-a"
[[ "$(ccsar get "" secrets team-a)" == true ]] || fail "team-a's manager cannot read team-a's Secrets (Secret references)"
[[ "$(ccsar create iam.cloudcontrol.aws.hfl.dev samlproviders team-b)" == false ]] || fail "team-a's manager may create SAMLProviders in team-b"
[[ "$(ccsar get "" secrets team-b)" == false ]] || fail "team-a's manager may read team-b's Secrets"
[[ "$(ccsar list "" namespaces "")" == false ]] || fail "team-a's manager may list namespaces"
[[ "$(ccsar create rbac.authorization.k8s.io rolebindings team-a)" == false ]] || fail "team-a's manager may create RoleBindings"
[[ "$(ccsar get "" secrets "${OP_NS}")" == false ]] || fail "team-a's manager may read the operator namespace's Secrets"
ok "rights: team-a's manager manages Cloud Control kinds and reads Secrets in team-a only; nothing in team-b or ${OP_NS}, nothing cluster-wide"
for n in team-a team-b; do
  cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: guardduty.cloudcontrol.aws.hfl.dev/v1alpha1
kind: Detector
metadata: {name: account-detector, namespace: ${n}}
spec: {enable: true}
YAML
done
taken() { kubectl -n team-a get detectors.guardduty.cloudcontrol.aws.hfl.dev account-detector -o jsonpath='{.metadata.finalizers}' | grep -q 'finalizers.guardduty.cloudcontrol.aws.hfl.dev'; }
verify_no_real_aws
wait_for 120 "team-a's Detector taken up by team-a's manager (moto has no Cloud Control API; the request fails there, recoverably)" taken
sleep 15
[[ -z "$(kubectl -n team-b get detectors.guardduty.cloudcontrol.aws.hfl.dev account-detector -o jsonpath='{.metadata.finalizers}{.status}')" ]] || fail "team-b's Detector was touched without a context in team-b"
ok "team-b's Detector left alone: no context there, no manager"
kubectl -n team-a delete cloudcontrolcontext cloudcontrol >/dev/null
cc_gone() { ! kubectl -n "${OP_NS}" get deploy "${CC_SA}" >/dev/null 2>&1 && ! kubectl -n "${OP_NS}" get sa "${CC_SA}" >/dev/null 2>&1 && ! kubectl -n team-a get rolebinding ack-extensions-cloudcontrol-manager >/dev/null 2>&1 && ! kubectl -n team-a get cloudcontrolcontext cloudcontrol >/dev/null 2>&1; }
wait_for 120 "the context deleted: its manager, ServiceAccount and RoleBinding go with it" cc_gone
# The manager is gone, so nothing finishes the Detector's delete: drop its
# finalizer (the resource was never made; moto has no Cloud Control API).
for n in team-a team-b; do
  kubectl -n "${n}" patch detectors.guardduty.cloudcontrol.aws.hfl.dev account-detector --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null
  kubectl -n "${n}" delete detectors.guardduty.cloudcontrol.aws.hfl.dev account-detector --wait=false >/dev/null 2>&1 || true
done

echo "all checks passed (cluster ${CLUSTER} left running; kind delete cluster --name ${CLUSTER})"
