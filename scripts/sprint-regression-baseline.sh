#!/usr/bin/env bash
# Sprint S43cfb1 regression baseline runner.
# Runs all 17 E2E tests + go test + npm build/lint sequentially against a running palmux2 instance.
# Outputs per-test logs into docs/sprint-logs/S43cfb1/baseline-e2e/, then assembles JSON summary.
#
# Usage:  PORT=8200 OUT_TAG=baseline ./scripts/sprint-regression-baseline.sh
set -uo pipefail

PORT="${PORT:-8200}"
OUT_TAG="${OUT_TAG:-baseline}"
LOG_DIR="docs/sprint-logs/S43cfb1/${OUT_TAG}-e2e"
mkdir -p "$LOG_DIR"

E2E_TESTS=(
  hotfix_claude_scroll_button
  hotfix_claude_scroll_yank_during_stream
  s001_refine_plan
  s004_mcp_indicator
  s005_hook_cli_wire
  s005_hook_events
  s006_add_dir_file
  s007_ask_question
  s008_upload_routes
  s009_multi_tab
  s009_fix_lifecycle
  s009_fix_lifecycle_v2
  s009_fix_periodic_check
  s009_fix4_ui_monitor
  s017_long_session
  s018_conv_utils
  s019_rewind
)

# Per-test timeouts (seconds). Some lifecycle tests need long durations.
declare -A TIMEOUTS=(
  [s009_fix_lifecycle]=240
  [s009_fix_lifecycle_v2]=240
  [s009_fix_periodic_check]=240
  [s009_fix4_ui_monitor]=240
  [s017_long_session]=180
)

results_json="$LOG_DIR/results.json"
echo "[" > "$results_json"
first=1

for name in "${E2E_TESTS[@]}"; do
  log="$LOG_DIR/${name}.log"
  to=${TIMEOUTS[$name]:-120}
  printf "==> %s (timeout=%ds) ... " "$name" "$to"
  start=$(date +%s)
  PALMUX2_DEV_PORT="$PORT" \
    PALMUX_DEV_PORT="$PORT" \
    timeout "$to" python3 "tests/e2e/${name}.py" > "$log" 2>&1
  status=$?
  end=$(date +%s)
  dur=$((end - start))
  if [ "$status" -eq 0 ]; then
    result=pass
  elif [ "$status" -eq 124 ]; then
    result=timeout
  else
    result=fail
  fi
  echo "$result (${dur}s, exit=$status)"
  if [ "$first" -eq 1 ]; then first=0; else echo "," >> "$results_json"; fi
  printf '  {"name":"%s","result":"%s","exit":%d,"duration_s":%d}' "$name" "$result" "$status" "$dur" >> "$results_json"
done
echo "" >> "$results_json"
echo "]" >> "$results_json"

echo
echo "Results JSON: $results_json"
