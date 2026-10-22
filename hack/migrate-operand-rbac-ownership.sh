#!/usr/bin/env bash
# Run with installation credentials before replacing legacy operator permissions.
set -euo pipefail
namespace=${1:?Usage: migrate-operand-rbac-ownership.sh NAMESPACE}
exec_cmd=${OPERATOR_EXEC:-kubectl}
command -v jq >/dev/null || { echo "Ownership migration requires jq" >&2; exit 1; }
die() { echo "Ownership migration: $*" >&2; exit 1; }
get() { ${exec_cmd} -n "$namespace" get "$@" --ignore-not-found -o json; }
crd=$(${exec_cmd} get crd sriovoperatorconfigs.sriovnetwork.openshift.io --ignore-not-found -o json)
config=""
if [[ -n "$crd" ]]; then
  jq -e '.kind == "CustomResourceDefinition" and .metadata.name == "sriovoperatorconfigs.sriovnetwork.openshift.io"' <<<"$crd" >/dev/null || die "invalid configuration CRD response"
  config=$(get sriovoperatorconfigs.sriovnetwork.openshift.io default)
fi
config_uid=""
if [[ -n "$config" ]]; then
  config_uid=$(jq -er --arg ns "$namespace" '
    select(.apiVersion == "sriovnetwork.openshift.io/v1" and .kind == "SriovOperatorConfig"
      and .metadata.name == "default" and .metadata.namespace == $ns
      and (.metadata.uid | type == "string" and length > 0)) | .metadata.uid' <<<"$config") ||
    die "invalid default SriovOperatorConfig response"
fi
# Exact names from deploy/operands_{service_account,role,role_binding}.yaml,
# including the optional Prometheus declarations even when metrics is disabled.
inventory=(
  ServiceAccount/sriov-network-config-daemon ServiceAccount/network-resources-injector-sa
  ServiceAccount/operator-webhook-sa ServiceAccount/metrics-exporter-sa ServiceAccount/sriov-device-plugin
  Role/network-resources-injector Role/sriov-network-config-daemon Role/operator-webhook-sa
  Role/metrics-exporter-role Role/sriov-plugin Role/sriov-device-plugin-pod-access Role/prometheus-k8s
  RoleBinding/sriov-network-config-daemon RoleBinding/operator-webhook-sa
  RoleBinding/network-resources-injector-role-binding RoleBinding/metrics-exporter-rb
  RoleBinding/sriov-device-plugin RoleBinding/sriov-device-plugin-pod-access RoleBinding/prometheus-k8s
)
# Validate identity and every configuration-like reference before any mutation.
inspect() {
  local object=$1 kind=$2 name=$3
  jq -e --arg ns "$namespace" --arg kind "$kind" --arg name "$name" --arg uid "$config_uid" '
    def expected: .apiVersion == "sriovnetwork.openshift.io/v1" and
      .kind == "SriovOperatorConfig" and .name == "default" and .uid == $uid and $uid != "";
    .kind == $kind and .apiVersion == (if $kind == "ServiceAccount" then "v1" else "rbac.authorization.k8s.io/v1" end)
    and .metadata.namespace == $ns and .metadata.name == $name
    and (.metadata.uid | type == "string" and length > 0)
    and (.metadata.resourceVersion | type == "string" and length > 0)
    and ((.metadata.ownerReferences // []) | type == "array")
    and all(.metadata.ownerReferences[]?;
      if (.kind == "SriovOperatorConfig" or (.apiVersion | startswith("sriovnetwork.openshift.io/")))
      then expected else true end)' <<<"$object" >/dev/null ||
    die "unexpected identity or configuration owner on $kind/$name; inspect ownership manually"
}
quiescent() {
  local deployment pods current_config
  current_config=$(get sriovoperatorconfigs.sriovnetwork.openshift.io default)
  [[ -n "$current_config" ]] || die "configuration disappeared; inspect ownership and rerun"
  jq -e --arg uid "$config_uid" '.metadata.uid == $uid and .metadata.deletionTimestamp == null' <<<"$current_config" >/dev/null || die "configuration changed or is terminating; inspect ownership and rerun"
  deployment=$(get deployment sriov-network-operator)
  if [[ -n "$deployment" ]]; then
    jq -e '.kind == "Deployment" and .metadata.name == "sriov-network-operator"
      and .spec.replicas == 0' <<<"$deployment" >/dev/null || die "$recovery"
  fi
  # Inspect all namespace pods, including terminating pods and orphaned old replicas.
  pods=$(${exec_cmd} -n "$namespace" get pods -o json)
  jq -e '.kind == "PodList" and (.items | type == "array") and
    all(.items[]; .spec.serviceAccountName != "sriov-network-operator"
      and .metadata.labels.name != "sriov-network-operator")' <<<"$pods" >/dev/null || die "$recovery"
}
recovery="stop the old operator: ${exec_cmd} -n $namespace scale deployment/sriov-network-operator --replicas=0; wait for ALL operator pods to terminate, then rerun migration and installation. Do not delete SriovOperatorConfig."
objects=()
needed=false
for resource in "${inventory[@]}"; do
  object=$(get "$resource")
  objects+=("$object")
  [[ -n "$object" ]] || continue
  inspect "$object" "${resource%/*}" "${resource#*/}"
  if jq -e '.metadata.ownerReferences | length > 0' <<<"$object" >/dev/null &&
     jq -e --arg uid "$config_uid" 'any(.metadata.ownerReferences[]?; .uid == $uid and .kind == "SriovOperatorConfig")' <<<"$object" >/dev/null; then
    needed=true
  fi
done
[[ "$needed" == true ]] || exit 0
quiescent
for i in "${!inventory[@]}"; do
  resource=${inventory[$i]}
  object=${objects[$i]}
  [[ -n "$object" ]] || continue
  for attempt in 1 2 3; do
    inspect "$object" "${resource%/*}" "${resource#*/}"
    patch=$(jq -c --arg uid "$config_uid" '
      .metadata as $m |
      [range(0; ($m.ownerReferences // [] | length)) |
        select($m.ownerReferences[.].uid == $uid and $m.ownerReferences[.].kind == "SriovOperatorConfig")] |
      reverse | map({op:"remove",path:("/metadata/ownerReferences/" + tostring)}) |
      if length == 0 then [] else
        [{op:"test",path:"/metadata/uid",value:$m.uid},
         {op:"test",path:"/metadata/resourceVersion",value:$m.resourceVersion}] + . end' <<<"$object")
    [[ "$patch" != '[]' ]] || break
    quiescent
    if result=$(${exec_cmd} -n "$namespace" patch "$resource" --type=json -p "$patch" 2>&1); then
      break
    fi
    # Retry only API conflicts/test failures; access and transport failures are fatal.
    if [[ "$attempt" == 3 || ! "$result" =~ Conflict|conflict|test[[:space:]]failed|test[[:space:]]operation ]]; then
      die "patch $resource failed: $result; resolve the error and rerun (completed objects are safe)"
    fi
    object=$(get "$resource")
    [[ -n "$object" ]] || die "$resource disappeared during migration; inspect and rerun"
  done
done
