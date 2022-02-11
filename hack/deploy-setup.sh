#!/bin/bash
# This script inits a cluster to allow sriov-network-operator
# to deploy.  It assumes it is capable of login as a
# user who has the cluster-admin role

# set -euxo pipefail

source "$(dirname $0)/common"

load_manifest() {
  local repo=$1
  local namespace=${2:-}
  export NAMESPACE=${namespace}
  if [ -n "${namespace}" ] ; then
    namespace="-n ${namespace}"
  fi

  pushd ${repo}/deploy
    if ! ${OPERATOR_EXEC} get ns $2 > /dev/null 2>&1 && test -f namespace.yaml ; then
      
      envsubst< namespace.yaml | ${OPERATOR_EXEC} apply -f -
    fi
    files=
    if [[ "${CLUSTER_TYPE}" == "kubernetes" && "${ENABLE_ADMISSION_CONTROLLER}" == "true" && "${ENABLE_CERT_MANAGER}" == "true" ]]; then
        ${OPERATOR_EXEC} apply -f https://github.com/jetstack/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml
        files="cert-manager.yaml "
    fi

    files="${files}service_account.yaml role.yaml role_binding.yaml clusterrole.yaml clusterrolebinding.yaml configmap.yaml operator.yaml"
    for m in ${files}; do
      if [ "$(echo ${EXCLUSIONS[@]} | grep -o ${m} | wc -w | xargs)" == "0" ] ; then
        envsubst< ${m} | ${OPERATOR_EXEC} apply ${namespace:-} --validate=false -f -
      fi
    done

  popd
}

# This is required for when running the operator locally using go run
rm -rf /tmp/_working_dir
mkdir /tmp/_working_dir
source hack/env.sh

load_manifest ${repo_dir} $1
