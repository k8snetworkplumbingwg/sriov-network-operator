#!/usr/bin/env bash
# Fresh and existing releases use the same command; adoption requires explicit opt-in.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
argv_file=$(mktemp)
cluster_file=$(mktemp)
trap 'rm -f "$argv_file" "$cluster_file"' EXIT
export argv_file cluster_file
make() { return 0; }
helm() { printf '%s\n' "$@" > "$argv_file"; }
mock_cluster() {
  printf '%s\n' "$*" >> "$cluster_file"
  [ "${MIGRATION_FAIL:-false}" != true ]
}
export OPERATOR_EXEC=mock_cluster
export -f make helm mock_cluster
for release_state in fresh existing; do
  for namespace in default custom-sriov; do
    for opt_in in unset false true; do
      : > "$cluster_file"
      (
        unset HELM_MODE NAMESPACE HELM_TAKE_OWNERSHIP
        if [ "$namespace" != default ]; then export NAMESPACE="$namespace"; fi
        if [ "$opt_in" != unset ]; then export HELM_TAKE_OWNERSHIP="$opt_in"; fi
        # Helm handles release discovery; the installer must always pass --install.
        bash hack/deploy-operator-helm.sh > /dev/null 2>&1
      )
      expected_namespace=$namespace
      if [ "$namespace" = default ]; then expected_namespace=sriov-network-operator; fi
      mapfile -t argv < "$argv_file"
      [ "${argv[0]}" = upgrade ]
      [ "${argv[1]}" = --install ]
      [ "${argv[2]}" = -n ]
      [ "${argv[3]}" = "$expected_namespace" ]
      for flag in --create-namespace --wait sriov-network-operator ./deployment/sriov-network-operator-chart; do
        grep -Fxq -- "$flag" "$argv_file"
      done
      actual=$(grep -c -- '^--take-ownership$' "$argv_file" || true)
      expected=0
      if [ "$opt_in" = true ]; then
        expected=1
        [ -s "$cluster_file" ]
        grep -Fq -- "$expected_namespace" "$cluster_file"
      else
        [ ! -s "$cluster_file" ]
      fi
      [ "$actual" = "$expected" ]
      echo "PASS: release=$release_state namespace=$namespace opt_in=$opt_in"
    done
  done
done

# A failed normalization must prevent Helm from adopting anything.
: > "$argv_file"
if HELM_TAKE_OWNERSHIP=true MIGRATION_FAIL=true bash hack/deploy-operator-helm.sh > /dev/null 2>&1; then
  echo "FAIL: migration failure was ignored" >&2
  exit 1
fi
[ ! -s "$argv_file" ]
echo "PASS: migration failure prevents Helm invocation"
