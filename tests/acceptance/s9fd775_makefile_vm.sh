#!/usr/bin/env bash
# AC-S9fd775-3-1 / AC-S9fd775-3-2: exercise the Makefile-equivalent
# `serve` / `serve-stop` flow on the test VM (ubuntu@192.168.1.41).
#
# This script is normally executed via SSH from
# tests/acceptance/s9fd775_makefile_vm.py — but it's a plain shell script
# so it's also runnable by hand for debugging.
#
# Steps:
#   1. Allocate ports for palmux2-vm-acceptest, palmux2-vm-acceptest-api,
#      palmux2-vm-acceptest-frontend via `palmux port alloc`.
#   2. Boot palmux as if `make serve INSTANCE=vm-acceptest` had run:
#      * --addr 0.0.0.0:<allocated-port>
#      * --config-dir ./tmp
#      * --tmux-prefix=_pmx_vm-acceptest_
#   3. Curl http://localhost:<port>/auth → expect HTTP 302.
#   4. Stop the server (SIGTERM via PID file).
#   5. Free the lease via `palmux port free`.
#   6. Verify ports.json no longer mentions the lease.
#
# Exit 0 = pass, non-zero = fail.

set -euo pipefail

cd "$(dirname "$0")"
ROOT="$HOME/palmux-s9fd775-vm"
PALMUX="$ROOT/palmux"
TMP="$ROOT/tmp"

if [ ! -x "$PALMUX" ]; then
  echo "FAIL: $PALMUX not present (run scp first)" >&2
  exit 2
fi

# Clean state.
mkdir -p "$TMP"
rm -f "$TMP/ports.json" "$TMP/palmux-vm-acceptest.pid" "$TMP/palmux-vm-acceptest.log"

INSTANCE=vm-acceptest
NAME=palmux2-${INSTANCE}

# AC-S9fd775-3-1 (allocate ports via palmux port alloc, just like the
# updated Makefile does).
PORT=$("$PALMUX" port alloc --config-dir "$TMP" --name "$NAME")
echo "==> allocated port: $PORT"
if [ -z "$PORT" ] || [ "$PORT" -lt 1024 ]; then
  echo "FAIL: bad port $PORT" >&2
  exit 1
fi

# AC-S9fd775-3-2 — start the server, talk to it, stop it.
nohup "$PALMUX" \
  --addr "0.0.0.0:$PORT" \
  --config-dir "$TMP" \
  --tmux-prefix=_pmx_${INSTANCE}_ \
  > "$TMP/palmux-${INSTANCE}.log" 2>&1 &
SERVER_PID=$!
echo "$SERVER_PID" > "$TMP/palmux-${INSTANCE}.pid"
echo "==> palmux PID: $SERVER_PID"

# Wait up to 5s for it to come online.
for i in $(seq 1 50); do
  if curl -sf -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/auth" 2>/dev/null | grep -q '^[23]'; then
    break
  fi
  sleep 0.1
done

CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/auth" || echo 000)
echo "==> /auth -> HTTP $CODE"
if [ "$CODE" != "302" ] && [ "$CODE" != "200" ]; then
  echo "FAIL: server not responding (got $CODE)" >&2
  echo "---- log ----"
  cat "$TMP/palmux-${INSTANCE}.log" || true
  kill -9 "$SERVER_PID" 2>/dev/null || true
  exit 1
fi

# Verify the lease is in ports.json.
if ! grep -q "\"$NAME\"" "$TMP/ports.json"; then
  echo "FAIL: ports.json missing $NAME entry" >&2
  cat "$TMP/ports.json"
  kill -9 "$SERVER_PID" 2>/dev/null || true
  exit 1
fi

# Stop the server (mirrors `make serve-stop`).
kill -TERM "$SERVER_PID"
for i in $(seq 1 50); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.1
done
kill -0 "$SERVER_PID" 2>/dev/null && kill -KILL "$SERVER_PID"
rm -f "$TMP/palmux-${INSTANCE}.pid"

# Free the lease.
"$PALMUX" port free --config-dir "$TMP" --name "$NAME"

# Verify ports.json no longer has the entry.
if grep -q "\"$NAME\"" "$TMP/ports.json"; then
  echo "FAIL: ports.json still has $NAME after free" >&2
  cat "$TMP/ports.json"
  exit 1
fi

echo "PASS: AC-S9fd775-3-1 + AC-S9fd775-3-2 verified on test VM"
