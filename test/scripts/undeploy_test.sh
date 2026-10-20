#!/usr/bin/env bash
# Check rendered deletion identities and idempotency without a cluster.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
export deletion_log="$tmp_dir/deletions" cluster_state="$tmp_dir/state" operator_state="$tmp_dir/operator-state"

# Parse only top-level kind and metadata fields, never subjects or roleRef.
identities() {
  awk -v ns="$1" '
    function emit() {
      if (kind != "" && name != "") {
        scope = (kind == "ClusterRole" || kind == "ClusterRoleBinding") ? "-" : (namespace == "" ? ns : namespace)
        print kind, name, scope
      }
    }
    /^---/ { emit(); kind = name = namespace = ""; metadata = 0 }
    /^kind:/ { kind = $2 }
    /^metadata:/ { metadata = 1; next }
    /^[^ ]/ { metadata = 0 }
    metadata && /^  name:/ { name = $2 }
    metadata && /^  namespace:/ { namespace = $2 }
    END { emit() }
  '
}
mock_cluster() {
  [ "$1" = delete ]
  shift
  local namespace="" from_file=false ignore=false cascade="" wait=true timeout="" explicit_wait=false
  local -a positional=()
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --ignore-not-found) ignore=true; shift ;;
      --cascade=*)
        cascade=${1#*=}
        case "$cascade" in
          foreground|background|orphan) ;;
          *) echo "FAIL: invalid cascade mode: $cascade" >&2; return 1 ;;
        esac
        shift
        ;;
      --wait=*)
        explicit_wait=true
        wait=${1#*=}
        case "$wait" in
          true|false) ;;
          *) echo "FAIL: invalid wait value: $wait" >&2; return 1 ;;
        esac
        shift
        ;;
      --timeout=*) timeout=${1#*=}; shift ;;
      -n) namespace=$2; shift 2 ;;
      -f) [ "$2" = - ]; from_file=true; shift 2 ;;
      *) positional+=("$1"); shift ;;
    esac
  done
  [ "$ignore" = true ]
  [ "$wait" = true ] || { echo "FAIL: deletion disabled waiting" >&2; return 1; }
  local requested kind name scope operator_requested=false
  if [ "$from_file" = true ]; then
    requested=$(identities "$namespace")
  else
    kind=${positional[0]}
    case "$kind" in
      role) kind=Role ;;
      rolebinding) kind=RoleBinding ;;
      cm) kind=ConfigMap ;;
    esac
    requested=""
    for name in "${positional[@]:1}"; do
      requested+="$kind $name $namespace"$'\n'
    done
  fi
  if grep -Eq '^Deployment sriov-network-operator ' <<< "$requested"; then
    operator_requested=true
    [ "$explicit_wait" = true ] || { echo "FAIL: operator wait was not explicit" >&2; return 1; }
    [ "$cascade" = foreground ] || { echo "FAIL: operator deletion was not foreground" >&2; return 1; }
  else
    [ "$cascade" != foreground ] || { echo "FAIL: foreground cascade used for a non-operator resource" >&2; return 1; }
  fi
  if grep -Eq '^SriovOperatorConfig default ' <<< "$requested"; then
    [ "$explicit_wait" = true ] && [ "$timeout" = 120s ] || return 1
    printf 'config-requested\n' >> "$deletion_log"
    if grep -q '^SriovOperatorConfig default ' "$cluster_state"; then
      [ "$(cat "$operator_state")" = running ] || return 1
      case "${FINALIZATION_MODE:-success}" in
        failure|timeout) echo "simulated config finalization ${FINALIZATION_MODE}" >&2; return 1 ;;
        delayed)
          # A deletion request leaves the object terminating until the controller finishes.
          printf 'finalizing\n' >> "$deletion_log"
          grep -q '^SriovOperatorConfig default ' "$cluster_state"
          [ "$(cat "$operator_state")" = running ]
          ;;
      esac
    fi
    printf 'config-complete\n' >> "$deletion_log"
  elif [ "$operator_requested" = true ]; then
    grep -qx config-complete "$deletion_log" || return 1
    if grep -q '^SriovOperatorConfig default ' "$cluster_state"; then
      echo "FAIL: operator stopped before config finalization" >&2
      return 1
    fi
  elif [ "$(cat "$operator_state")" = running ]; then
    echo "FAIL: grant cleanup started while the operator was still running" >&2
    return 1
  fi
  while read -r kind name scope; do
    [ -n "$kind" ] || continue
    printf '%s %s %s\n' "$kind" "$name" "$scope" >> "$deletion_log"
    # Missing resources succeed only because --ignore-not-found was supplied.
    awk -v target="$kind $name $scope" '$0 != target' "$cluster_state" > "$cluster_state.next"
    mv "$cluster_state.next" "$cluster_state"
    if [ "$kind" = Deployment ] && [ "$name" = sriov-network-operator ]; then
      printf 'stopped\n' > "$operator_state"
    fi
  done <<< "$requested"
}
export -f identities mock_cluster
export OPERATOR_EXEC=mock_cluster REMOTE_REGISTRY=false
unset METRICS_EXPORTER_PROMETHEUS_OPERATOR_SERVICE_ACCOUNT METRICS_EXPORTER_PROMETHEUS_OPERATOR_NAMESPACE

for namespace_case in default environment argument; do
  case "$namespace_case" in
    default) unset NAMESPACE; expected_namespace=sriov-network-operator; args=() ;;
    environment) export NAMESPACE=environment-ns; expected_namespace=$NAMESPACE; args=() ;;
    argument) export NAMESPACE=wrong-ns; expected_namespace=argument-ns; args=("$expected_namespace") ;;
  esac
  for metrics in unset false true; do
    if [ "$metrics" = unset ]; then
      unset METRICS_EXPORTER_PROMETHEUS_OPERATOR_ENABLED
    else
      export METRICS_EXPORTER_PROMETHEUS_OPERATOR_ENABLED=$metrics
    fi
    # Derive the complete install inventory, including both optional manifests.
    for manifest in deploy/operands_*.yaml; do
      NAMESPACE=$expected_namespace envsubst < "$manifest" | identities "$expected_namespace"
    done | sort > "$tmp_dir/expected"
    NAMESPACE=$expected_namespace envsubst < deploy/operator.yaml | identities "$expected_namespace" >> "$tmp_dir/expected"
    grep -qx "Deployment sriov-network-operator $expected_namespace" "$tmp_dir/expected"
    grep -qx 'ClusterRole sriov-network-config-daemon -' "$tmp_dir/expected"
    grep -qx 'ClusterRoleBinding sriov-network-config-daemon -' "$tmp_dir/expected"
    for installation in full partial absent stopped; do
      if [ "$installation" = full ]; then
        cp "$tmp_dir/expected" "$cluster_state"
      elif [ "$installation" = absent ]; then
        : > "$cluster_state"
      elif [ "$installation" = stopped ]; then
        cp "$tmp_dir/expected" "$cluster_state"
        printf 'SriovOperatorConfig default %s\n' "$expected_namespace" >> "$cluster_state"
      else
        # Include the dangerous daemon grants and operator in a partial install.
        grep sriov-network-config-daemon "$tmp_dir/expected" > "$cluster_state"
        grep '^Deployment sriov-network-operator ' "$tmp_dir/expected" >> "$cluster_state"
      fi
      if [ "$installation" = full ]; then
        printf 'SriovOperatorConfig default %s\n' "$expected_namespace" >> "$cluster_state"
      fi
      if [ "$installation" != stopped ] && grep -q '^Deployment sriov-network-operator ' "$cluster_state"; then
        printf 'running\n' > "$operator_state"
      else
        printf 'stopped\n' > "$operator_state"
      fi
      # Failure must preserve the controller and every remaining grant.
      if [ "$installation" = full ] || [ "$installation" = stopped ]; then
        for mode in failure timeout; do
          export FINALIZATION_MODE=$mode
          cp "$cluster_state" "$tmp_dir/before"
          cp "$operator_state" "$tmp_dir/operator-before"
          : > "$deletion_log"
          if bash hack/undeploy.sh "${args[@]}" > "$tmp_dir/failure" 2>&1; then
            echo "FAIL: finalization failure was ignored" >&2
            exit 1
          fi
          cmp "$cluster_state" "$tmp_dir/before"
          cmp "$operator_state" "$tmp_dir/operator-before"
          [ "$(cat "$deletion_log")" = config-requested ]
          grep -q 'operator and grants were preserved' "$tmp_dir/failure"
        done
        # Recover by restoring the controller, never stripping a finalizer.
        printf 'running\n' > "$operator_state"
      fi
      export FINALIZATION_MODE=delayed
      for attempt in 1 2; do
        : > "$deletion_log"
        bash hack/undeploy.sh "${args[@]}" > /dev/null
        [ ! -s "$cluster_state" ]
        # Every installed identity must be requested exactly once on every run.
        while IFS= read -r identity; do
          [ "$(grep -Fxc "$identity" "$deletion_log")" = 1 ]
        done < "$tmp_dir/expected"
        if grep -Eq '\$|wrong-ns' "$deletion_log"; then
          echo "FAIL: unresolved or incorrect namespace" >&2
          exit 1
        fi
      done
    done
    echo "PASS: namespace=$namespace_case metrics=$metrics full/partial/absent/stopped/repeated"
  done
done
