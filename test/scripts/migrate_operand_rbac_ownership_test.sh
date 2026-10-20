#!/usr/bin/env bash
# Stateful API mock exercises the real helper; GC assertions model owner matching,
# not live Kubernetes garbage collection.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
python3 test/scripts/migrate_operand_rbac_ownership_test.py
