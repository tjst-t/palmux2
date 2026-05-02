#!/usr/bin/env bash
# Run all Palmux2 mobile E2E scenarios in sequence (S022-2-3).
#
# Usage:
#   PALMUX2_DEV_PORT_OVERRIDE=8214 ./tests/e2e/mobile/run.sh
#
# Prerequisites:
#   - dev server is up:   make serve INSTANCE=dev
#   - dist is built:      make build  (or `make serve INSTANCE=dev` does it)
#   - playwright + chromium installed:
#       pip install playwright && playwright install chromium
#
# Exit code 0 = all PASS. Any non-zero = at least one scenario failed.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

scenarios=(
  "${DIR}/m001_homepage_smoke.py"
  "${DIR}/m002_bundle_size.py"
  "${DIR}/m003_bottomsheet_modal.py"
  "${DIR}/m004_tap_targets.py"
  "${DIR}/m005_gesture_audit.py"
  "${DIR}/m006_drawer_mobile.py"
)

failed=0
for s in "${scenarios[@]}"; do
  name="$(basename "$s")"
  if python3 "$s"; then
    echo "PASS: $name"
  else
    echo "FAIL: $name" >&2
    failed=$((failed + 1))
  fi
  echo
done

if [ "$failed" -gt 0 ]; then
  echo ""
  echo "$failed scenario(s) failed."
  exit 1
fi

echo "All ${#scenarios[@]} mobile scenarios PASS."
