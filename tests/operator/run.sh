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
#      against moto, and the switch to sharing ACK's ServiceAccount.
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
ok "moto and ${ACK_RDS_IMAGE} loaded"

echo "== fake AWS"
kubectl apply -f config/fake-aws/moto.yaml >/dev/null
kubectl -n fake-aws rollout status deploy/moto --timeout=180s >/dev/null
ok "moto ready in fake-aws"
MOTO="http://moto.fake-aws.svc.cluster.local:5000"

echo "== ACK's rds-controller ${ACK_RDS_VERSION}, from its own chart"
kubectl create namespace "${ACK_NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "${ACK_NS}" create secret generic aws-creds \
  --from-literal=credentials=$'[default]\naws_access_key_id = testing\naws_secret_access_key = testing\n' \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
helm upgrade --install ack-rds "${ACK_RDS_DIR}/helm" -n "${ACK_NS}" \
  --set aws.region=us-west-2 \
  --set aws.endpoint_url="${MOTO}" \
  --set aws.identity_endpoint_url="${MOTO}" \
  --set aws.allow_unsafe_aws_endpoint_urls=true \
  --set aws.credentials.secretName=aws-creds \
  --set-string "serviceAccount.annotations.eks\.amazonaws\.com/role-arn=${ACK_ROLE}" \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 180s >/dev/null
ACK_DEPLOY="$(kubectl -n "${ACK_NS}" get deploy -l app.kubernetes.io/name=rds-chart -o jsonpath='{.items[0].metadata.name}')"
ok "ACK's controller ${ACK_NS}/${ACK_DEPLOY} running as ${ACK_NS}/ack-rds-controller"

echo "== ours: config/default by ko"
kubectl kustomize config/default \
  | KO_DOCKER_REPO=kind.local KIND_CLUSTER_NAME="${CLUSTER}" ko resolve --platform="linux/${arch}" -f - >"${work}/ours.yaml"
kubectl apply --server-side -f "${work}/ours.yaml" >/dev/null
kubectl -n ack-extensions-system rollout status deploy/ack-extensions-operator --timeout=180s >/dev/null
for crd in scheduledactions.applicationautoscaling.aws.hfl.dev scalingpolicies.autoscaling.aws.hfl.dev scheduledactions.autoscaling.aws.hfl.dev customergateways.ec2.aws.hfl.dev vpngateways.ec2.aws.hfl.dev apidestinations.eventbridge.aws.hfl.dev connections.eventbridge.aws.hfl.dev rolepolicyattachments.iam.aws.hfl.dev replicakeys.kms.aws.hfl.dev permissions.lambda.aws.hfl.dev dbproxytargets.rds.aws.hfl.dev dbroleassociations.rds.aws.hfl.dev optiongroups.rds.aws.hfl.dev; do
  kubectl get crd "${crd}" >/dev/null || fail "CRD ${crd} missing"
done
ok "CRDs installed: scheduledactions.applicationautoscaling, scalingpolicies.autoscaling, scheduledactions.autoscaling, customergateways.ec2, vpngateways.ec2, apidestinations.eventbridge, connections.eventbridge, rolepolicyattachments.iam, replicakeys.kms, permissions.lambda, dbproxytargets.rds, dbroleassociations.rds, optiongroups.rds (*.aws.hfl.dev); operator running"

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
for svc in applicationautoscaling autoscaling ec2 eventbridge iam kms lambda; do
  kubectl get deploy -A -l "aws.hfl.dev/service=${svc}" -o name | grep -q . && fail "a ${svc} manager without an ACK ${svc} controller"
done
ok "no applicationautoscaling, autoscaling, ec2, eventbridge, iam, kms or lambda manager: none of their ACK controllers is installed"

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
[[ "$(sar delete iam.services.k8s.aws roles ack-extensions-rds-manager default)" == false ]] || fail "our manager may delete ACK's Roles"[[ "$(can create dbproxytargets.rds.aws.hfl.dev ack-rds-controller default)" == no ]] || fail "ACK's controller may create our kinds"
ok "rights: ours manages *.rds.aws.hfl.dev and only reads ACK's rds kinds and Roles; ACK's controller has nothing of ours"

echo "== gap kinds reconcile against moto"
kubectl -n fake-aws port-forward svc/moto 15000:5000 >/dev/null 2>&1 &
pf_pid=$!
wait_for 30 "port-forward to moto" curl -fsS http://127.0.0.1:15000/moto-api/
"${PY}" -I - <<'PYEOF'
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
"${PY}" -I - <<'PYEOF'
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

echo "all checks passed (cluster ${CLUSTER} left running; kind delete cluster --name ${CLUSTER})"
