#!/usr/bin/env bash
# Sprint S4b9df4 regression suite runner.
# Runs all 17 existing Claude-tab E2E tests + (optionally) 5 new S4b9df4 tests
# + go test + go build + npm build + npm lint sequentially against a running
# palmux2 instance.
# Outputs per-test logs into docs/sprint-logs/S4b9df4/${OUT_TAG}-stdout/
# then assembles JSON summary.
#
# Usage:  PORT=8200 OUT_TAG=baseline ./scripts/sprint-regression-S4b9df4.sh
#         (set INCLUDE_NEW=1 once the 5 new files exist to include them)
set -uo pipefail

PORT="${PORT:-8200}"
OUT_TAG="${OUT_TAG:-baseline}"
INCLUDE_NEW="${INCLUDE_NEW:-0}"
LOG_DIR="docs/sprint-logs/S4b9df4/${OUT_TAG}-stdout"
mkdir -p "$LOG_DIR"

# S4b9df4-0: legacy tests hardcode pre-S1e8d02 branch IDs (`autopilot--S001-refine--08f1` etc.)
# that no longer exist as worktrees on dev. Override to a real branch on the test repo so
# the tests can attach to a real WS / REST surface. The test repo `tjst-t--palmux2--2d59`
# always has at least one open branch (the worktree the dev server runs in).
TEST_REPO_ID="${TEST_REPO_ID:-tjst-t--palmux2--2d59}"
# Pick the FIRST openBranch from /api/repos/{id}/branches (= primary worktree).
TEST_BRANCH_ID="${TEST_BRANCH_ID:-$(curl -s "http://127.0.0.1:${PORT}/api/repos/${TEST_REPO_ID}/branches" \
  | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0]['id'])" 2>/dev/null || echo "")}"

if [ -z "$TEST_BRANCH_ID" ]; then
  echo "ERROR: could not determine TEST_BRANCH_ID — is dev server up on port $PORT?"
  exit 1
fi

echo "==> Using TEST_REPO_ID=$TEST_REPO_ID  TEST_BRANCH_ID=$TEST_BRANCH_ID"

# Map each legacy test's branch env var to the live branch.
export S001_REPO_ID="$TEST_REPO_ID" S001_BRANCH_ID="$TEST_BRANCH_ID"
export S004_REPO_ID="$TEST_REPO_ID" S004_BRANCH_ID="$TEST_BRANCH_ID"
export S005_REPO_ID="$TEST_REPO_ID" S005_BRANCH_ID="$TEST_BRANCH_ID"
export S006_REPO_ID="$TEST_REPO_ID" S006_BRANCH_ID="$TEST_BRANCH_ID"
export S007_REPO_ID="$TEST_REPO_ID" S007_BRANCH_ID="$TEST_BRANCH_ID"
export S008_REPO_ID="$TEST_REPO_ID" S008_BRANCH_ID="$TEST_BRANCH_ID"
export S009_REPO_ID="$TEST_REPO_ID" S009_BRANCH_ID="$TEST_BRANCH_ID"
export S009_FIX_REPO_ID="$TEST_REPO_ID" S009_FIX_BRANCH_ID="$TEST_BRANCH_ID"
export S009_FIX4_REPO_ID="$TEST_REPO_ID" S009_FIX4_BRANCH_ID="$TEST_BRANCH_ID"

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

if [ "$INCLUDE_NEW" = "1" ]; then
  E2E_TESTS+=(
    s4b9df4_topbar_buttons
    s4b9df4_keyboard_shortcuts
    s4b9df4_scroll_follow
    s4b9df4_rewind_flow
    s4b9df4_permission_flow
  )
fi

# Per-test timeouts (seconds). Some lifecycle tests need long durations.
declare -A TIMEOUTS=(
  [s009_fix_lifecycle]=240
  [s009_fix_lifecycle_v2]=240
  [s009_fix_periodic_check]=240
  [s009_fix4_ui_monitor]=240
  [s017_long_session]=180
)

# Phase 1: backend go test
echo "==> go test ./internal/tab/claudeagent/..."
go_test_log="$LOG_DIR/go_test.log"
start=$(date +%s)
go test -count=1 ./internal/tab/claudeagent/... > "$go_test_log" 2>&1
go_test_status=$?
end=$(date +%s)
go_test_dur=$((end - start))
[ "$go_test_status" -eq 0 ] && go_test_result=pass || go_test_result=fail
echo "    $go_test_result (${go_test_dur}s)"

# Phase 2: backend go build sanity
echo "==> go build ./..."
go_build_log="$LOG_DIR/go_build.log"
start=$(date +%s)
go build ./... > "$go_build_log" 2>&1
go_build_status=$?
end=$(date +%s)
go_build_dur=$((end - start))
[ "$go_build_status" -eq 0 ] && go_build_result=pass || go_build_result=fail
echo "    $go_build_result (${go_build_dur}s)"

# Phase 3: frontend build sanity
echo "==> npm --prefix frontend run build"
fe_build_log="$LOG_DIR/fe_build.log"
start=$(date +%s)
npm --prefix frontend run build > "$fe_build_log" 2>&1
fe_build_status=$?
end=$(date +%s)
fe_build_dur=$((end - start))
[ "$fe_build_status" -eq 0 ] && fe_build_result=pass || fe_build_result=fail
echo "    $fe_build_result (${fe_build_dur}s)"

# Phase 4: frontend lint
echo "==> npm --prefix frontend run lint"
fe_lint_log="$LOG_DIR/fe_lint.log"
start=$(date +%s)
npm --prefix frontend run lint > "$fe_lint_log" 2>&1
fe_lint_status=$?
end=$(date +%s)
fe_lint_dur=$((end - start))
[ "$fe_lint_status" -eq 0 ] && fe_lint_result=pass || fe_lint_result=fail
echo "    $fe_lint_result (${fe_lint_dur}s)"

# Phase 5: bundle sizes (only if FE build succeeded)
bundle_json="null"
if [ "$fe_build_status" -eq 0 ]; then
  index_js=$(ls frontend/dist/assets/index-*.js 2>/dev/null | head -1)
  files_js=$(ls frontend/dist/assets/files-view-*.js 2>/dev/null | head -1)
  total_js=0
  total_files=0
  for f in frontend/dist/assets/*.js; do
    sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
    total_js=$((total_js + sz))
    total_files=$((total_files + 1))
  done
  index_sz=$([ -n "$index_js" ] && stat -c%s "$index_js" 2>/dev/null || echo 0)
  files_sz=$([ -n "$files_js" ] && stat -c%s "$files_js" 2>/dev/null || echo 0)
  bundle_json=$(cat <<EOF
{"index_js": $index_sz, "files_view_js": $files_sz, "total_js_bytes": $total_js, "total_js_files": $total_files, "index_js_path": "$index_js", "files_view_js_path": "$files_js"}
EOF
)
fi

# Phase 6: E2E tests
echo
echo "==> Python E2E tests against http://127.0.0.1:$PORT"
results_e2e_json="$LOG_DIR/e2e_results.json"
echo "[" > "$results_e2e_json"
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
  if [ "$first" -eq 1 ]; then first=0; else echo "," >> "$results_e2e_json"; fi
  printf '  {"name":"%s","result":"%s","exit":%d,"duration_s":%d}' \
    "$name" "$result" "$status" "$dur" >> "$results_e2e_json"
done
echo "" >> "$results_e2e_json"
echo "]" >> "$results_e2e_json"

# Phase 7: assemble overall summary
summary_json="docs/sprint-logs/S4b9df4/regression-${OUT_TAG}.json"
e2e_pass=$(grep -c '"result":"pass"' "$results_e2e_json" || true)
e2e_fail=$(grep -c '"result":"fail"' "$results_e2e_json" || true)
e2e_timeout=$(grep -c '"result":"timeout"' "$results_e2e_json" || true)
e2e_total=${#E2E_TESTS[@]}

cat > "$summary_json" <<EOF
{
  "sprint": "S4b9df4",
  "tag": "$OUT_TAG",
  "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "port": $PORT,
  "include_new_tests": $([ "$INCLUDE_NEW" = "1" ] && echo true || echo false),
  "phases": {
    "go_test": {"result": "$go_test_result", "exit": $go_test_status, "duration_s": $go_test_dur, "log": "$go_test_log"},
    "go_build": {"result": "$go_build_result", "exit": $go_build_status, "duration_s": $go_build_dur, "log": "$go_build_log"},
    "fe_build": {"result": "$fe_build_result", "exit": $fe_build_status, "duration_s": $fe_build_dur, "log": "$fe_build_log"},
    "fe_lint": {"result": "$fe_lint_result", "exit": $fe_lint_status, "duration_s": $fe_lint_dur, "log": "$fe_lint_log"}
  },
  "bundle_sizes": $bundle_json,
  "e2e_summary": {"total": $e2e_total, "pass": $e2e_pass, "fail": $e2e_fail, "timeout": $e2e_timeout},
  "e2e_results_file": "$results_e2e_json"
}
EOF

echo
echo "Summary: $summary_json"
echo "  go_test: $go_test_result | go_build: $go_build_result | fe_build: $fe_build_result | fe_lint: $fe_lint_result"
echo "  E2E: pass=$e2e_pass fail=$e2e_fail timeout=$e2e_timeout total=$e2e_total"
