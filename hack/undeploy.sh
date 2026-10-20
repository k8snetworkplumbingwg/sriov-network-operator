#!/bin/bash

set -eo pipefail

source "$(dirname "$0")/common"

repo_dir="$(dirname "$0")/.."
export NAMESPACE=${1:-${NAMESPACE}}
namespace=(-n "${NAMESPACE}")

pushd "${repo_dir}/deploy"
# Keep the controller and its grants available until its config finalizer completes.
if ! envsubst < sriovoperatorconfig.yaml | ${OPERATOR_EXEC} delete --ignore-not-found "${namespace[@]}" --wait=true --timeout=120s -f -; then
  echo "Config finalization failed; operator and grants were preserved. Restore the operator and its permissions if stopped, resolve finalization errors, then retry undeploy. Do not remove finalizers manually." >&2
  exit 1
fi
files="operator.yaml operands_role_binding.yaml operands_clusterrolebinding.yaml operands_role.yaml operands_clusterrole.yaml operands_service_account.yaml service_account.yaml role.yaml role_binding.yaml clusterrole.yaml clusterrolebinding.yaml configmap.yaml"
for file in ${files}; do
  delete_options=()
  if [ "${file}" = operator.yaml ]; then
    delete_options+=(--cascade=foreground --wait=true)
  fi
  envsubst < "${file}" | ${OPERATOR_EXEC} delete --ignore-not-found "${namespace[@]}" "${delete_options[@]}" -f -
done
# Remove optional grants even when integration is disabled or its old subject is unknown.
${OPERATOR_EXEC} delete rolebinding --ignore-not-found "${namespace[@]}" prometheus-k8s
${OPERATOR_EXEC} delete role --ignore-not-found "${namespace[@]}" prometheus-k8s
${OPERATOR_EXEC} delete cm --ignore-not-found "${namespace[@]}" device-plugin-config
${OPERATOR_EXEC} delete MutatingWebhookConfiguration --ignore-not-found "${namespace[@]}" network-resources-injector-config sriov-operator-webhook-config
${OPERATOR_EXEC} delete ValidatingWebhookConfiguration --ignore-not-found "${namespace[@]}" sriov-operator-webhook-config
popd
