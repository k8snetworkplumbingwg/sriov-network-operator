#!/usr/bin/env bash
# Verify ownership adoption requires an explicit opt-in on upgrades only.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
argv_file=$(mktemp)
trap 'rm -f "$argv_file"' EXIT
export argv_file
make() { return 0; }
helm() { printf '%s\n' "$@" > "$argv_file"; }
mock_cluster() { return 0; }
export OPERATOR_EXEC=mock_cluster
export -f make helm mock_cluster
for mode in install upgrade; do
  for opt_in in unset false true; do
    (
      export HELM_MODE="$mode"
      if [ "$opt_in" = unset ]; then
        unset HELM_TAKE_OWNERSHIP
      else
        export HELM_TAKE_OWNERSHIP="$opt_in"
      fi
      bash hack/deploy-operator-helm.sh > /dev/null 2>&1
    )
    actual=$(grep -c -- '^--take-ownership$' "$argv_file" || true)
    expected=0
    if [ "$mode" = upgrade ] && [ "$opt_in" = true ]; then
      expected=1
    fi
    if [ "$actual" != "$expected" ]; then
      echo "FAIL: mode=$mode opt_in=$opt_in ownership flags=$actual expected=$expected" >&2
      exit 1
    fi
    [ "$(head -n 1 "$argv_file")" = "$mode" ]
    echo "PASS: mode=$mode opt_in=$opt_in"
  done
done
