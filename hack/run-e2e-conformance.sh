#!/bin/bash

here="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
root="$(readlink --canonicalize "$here/..")"

GOPATH="${GOPATH:-$HOME/go}"
JUNIT_OUTPUT="${JUNIT_OUTPUT:-/tmp/artifacts}"
export PATH=$PATH:$GOPATH/bin

# GINKGO_ARGS holds extra ginkgo CLI flags, e.g. GINKGO_ARGS='--focus=DRA --fail-fast'.
# It is split on whitespace and quotes inside it are not interpreted, so an argument cannot
# contain spaces or quoting: write a focus regex as --focus=\[sriov\].DRA rather than
# --focus='[sriov] DRA'.
# -d '' makes read consume the whole value, newlines included; it then returns non-zero at EOF.
read -r -d '' -a ginkgo_args <<< "${GINKGO_ARGS:-}" || true

${root}/bin/ginkgo --timeout=3h -output-dir=$JUNIT_OUTPUT --junit-report "unit_report.xml" -v "${ginkgo_args[@]}" "$SUITE" -- -report=$JUNIT_OUTPUT
