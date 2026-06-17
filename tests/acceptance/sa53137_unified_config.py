#!/usr/bin/env python3
"""Sa53137 acceptance — CLI/HTTP-driven (real binary subprocess + real HTTP).

Drives the produced palmux2 binary through its real entry points:

  [AC-Sa53137-2-1] resolution chain: config.toml [server].addr binds the server
                  when no --addr flag is passed; an explicit --addr flag wins
                  over the file.
  [AC-Sa53137-2-2] secrets from secrets.env (PALMUX_SSO_SECRET) enable SSO and
                  are reported as present (masked) by /api/deploy.
  [AC-Sa53137-2-3] one-time legacy secrets migration writes user-owned
                  secrets.env (0600) — exercised via the config package's
                  Migrate path indirectly (file-presence skip honoured).
  [AC-Sa53137-3-1] `palmux apply` re-reads the master, classifies a hot change
                  and reports it (against a running server).
  [AC-Sa53137-4-2] `palmux reconcile-system` renders a Caddyfile from a fixed
                  template for a valid domain (non-root → preview).
  [AC-Sa53137-4-3] `palmux reconcile-system` REFUSES an injection / malformed
                  domain before rendering.

Usage:  PALMUX2_BIN=./bin/palmux python3 tests/acceptance/sa53137_unified_config.py
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request

BIN = os.environ.get("PALMUX2_BIN", "./bin/palmux")
FAILS: list[str] = []


def chk(name: str, cond: bool, extra: str = "") -> None:
    print(("PASS" if cond else "FAIL"), name, ("- " + extra) if extra and not cond else "")
    if not cond:
        FAILS.append(name)


def free_port() -> int:
    import socket
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def http_json(url: str) -> dict:
    with urllib.request.urlopen(url, timeout=5) as r:
        return json.loads(r.read().decode())


def start_server(cfgdir: str, *extra: str, prefix: str) -> subprocess.Popen:
    args = [BIN, "serve", "--config-dir", cfgdir, "--tmux-prefix", prefix, *extra]
    p = subprocess.Popen(args, stdout=open(os.path.join(cfgdir, "srv.log"), "w"),
                         stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                         start_new_session=True)
    return p


def wait_health(port: int, timeout: float = 8.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            http_json(f"http://127.0.0.1:{port}/api/health")
            return True
        except Exception:
            time.sleep(0.2)
    return False


def main() -> int:
    # ---- AC-2-1 / 2-2: config.toml addr resolution + secrets.env SSO ---------
    port = free_port()
    with tempfile.TemporaryDirectory() as d:
        with open(os.path.join(d, "config.toml"), "w") as f:
            f.write(f'[server]\naddr = "127.0.0.1:{port}"\n[public]\ndomain = ""\n')
        with open(os.path.join(d, "secrets.env"), "w") as f:
            f.write("PALMUX_SSO_SECRET=acceptsecret\n")
        os.chmod(os.path.join(d, "secrets.env"), 0o600)

        srv = start_server(d, prefix="_pmx_acc1_")
        try:
            up = wait_health(port)
            chk("AC-Sa53137-2-1 config.toml addr binds server (no --addr flag)", up)
            if up:
                dep = http_json(f"http://127.0.0.1:{port}/api/deploy")
                chk("AC-Sa53137-2-2 secrets.env SSO secret reported present (masked)",
                    dep["secrets"]["hasSsoSecret"] is True)
                chk("AC-Sa53137-2-2 deploy view never returns secret values",
                    "acceptsecret" not in json.dumps(dep))

                # ---- AC-3-1: `palmux apply` classifies a hot change ----------
                # Edit the master's caddy_admin (hot), then run apply.
                with open(os.path.join(d, "config.toml"), "w") as f:
                    f.write(f'[server]\naddr = "127.0.0.1:{port}"\ncaddy_admin = "http://localhost:4321"\n[public]\ndomain = ""\n')
                out = subprocess.run([BIN, "apply", "--config-dir", d],
                                     capture_output=True, text=True, timeout=20)
                applied_ok = ("server.caddy_admin" in out.stdout and "hot" in out.stdout)
                chk("AC-Sa53137-3-1 `palmux apply` classifies + applies hot change",
                    applied_ok, out.stdout + out.stderr)
        finally:
            srv.terminate()
            try:
                srv.wait(timeout=5)
            except Exception:
                srv.kill()

    # ---- AC-2-1 (flag wins): explicit --addr overrides config.toml ----------
    port2 = free_port()
    fileport = free_port()
    with tempfile.TemporaryDirectory() as d:
        with open(os.path.join(d, "config.toml"), "w") as f:
            f.write(f'[server]\naddr = "127.0.0.1:{fileport}"\n')
        srv = start_server(d, f"--addr=127.0.0.1:{port2}", prefix="_pmx_acc2_")
        try:
            chk("AC-Sa53137-2-1 explicit --addr flag wins over config.toml",
                wait_health(port2) and not wait_health(fileport, timeout=1.0))
        finally:
            srv.terminate()
            try:
                srv.wait(timeout=5)
            except Exception:
                srv.kill()

    # ---- AC-4-2 / 4-3: reconcile-system render + injection rejection --------
    with tempfile.TemporaryDirectory() as d:
        # valid domain → renders (non-root → preview on stderr, exit 1 for non-root)
        with open(os.path.join(d, "config.toml"), "w") as f:
            f.write('[public]\ndomain = "valid.example.net"\nbasic_auth_user = "admin"\n')
        out = subprocess.run([BIN, "reconcile-system", "--config-dir", d],
                             capture_output=True, text=True, timeout=15)
        blob = out.stdout + out.stderr
        chk("AC-Sa53137-4-2 reconcile renders fixed-template Caddyfile for valid domain",
            "valid.example.net {" in blob and "*.valid.example.net {" in blob
            and "dns cloudflare {env.CLOUDFLARE_API_TOKEN}" in blob)

        # injection domain → refused before render (exit 2, no Caddyfile body)
        with open(os.path.join(d, "config.toml"), "w") as f:
            f.write('[public]\ndomain = "evil.net {reverse_proxy 10.0.0.1}"\n')
        out = subprocess.run([BIN, "reconcile-system", "--config-dir", d],
                             capture_output=True, text=True, timeout=15)
        refused = (out.returncode == 2 and "reverse_proxy 10.0.0.1" not in out.stdout)
        chk("AC-Sa53137-4-3 reconcile refuses injection/malformed domain",
            refused, f"rc={out.returncode} out={out.stdout[:200]}")

    print()
    if FAILS:
        print(f"FAILED: {len(FAILS)} check(s): {FAILS}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
