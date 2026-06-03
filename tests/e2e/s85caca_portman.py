#!/usr/bin/env python3
"""
tests/e2e/s85caca_portman.py — Sprint S85caca acceptance E2E.

portman <-> Caddy dynamic subdomain routing (model B).

- Driver: this dev box (Python urllib/ssl/socket for HTTP/TLS, ssh for VM-side checks).
- Target: ubuntu@192.168.1.43 (palmux-deploy-test.tjstkm.net).
- Secrets: ~/.config/palmux-deploy-test/secrets.env
           (CLOUDFLARE_API_TOKEN, ACME_EMAIL, BASIC_AUTH_USER, BASIC_AUTH_PASSWORD).

Coverage:
  [AC-S85caca-1-1]  caddy.json exists; caddy.service uses caddy.json; TLS automation subjects
  [AC-S85caca-1-2]  palmux2 and portman dashboard reachable via HTTPS; LE cert
  [AC-S85caca-1-3]  edge basic auth enforced; hash is placeholder in caddy.json, real value in palmux.env
  [AC-S85caca-1-4]  port binding: palmux2 127.0.0.1:8080, portman 127.0.0.1:8090, caddy :443
  [AC-S85caca-2-1]  portman config files + systemd units
  [AC-S85caca-2-2]  portman exec --expose publishes a live HTTPS route
  [AC-S85caca-2-3]  after kill + gc + sync, exposed route returns 502/404
  [AC-S85caca-2-4]  idempotency re-run + backward-compat guard

Run:
  python3 tests/e2e/s85caca_portman.py                # all (deploy + verify)
  python3 tests/e2e/s85caca_portman.py S85caca-1-1    # single AC
  python3 tests/e2e/s85caca_portman.py --story 1      # all of Story-1
  python3 tests/e2e/s85caca_portman.py --no-deploy    # skip installer, verify existing state
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import pathlib
import re
import shlex
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request


VM_HOST = os.environ.get("VM_HOST", "ubuntu@192.168.1.43")
VM_IP = os.environ.get("VM_IP", "192.168.1.43")
DOMAIN = os.environ.get("DOMAIN", "palmux-deploy-test.tjstkm.net")
SECRETS_FILE = pathlib.Path("~/.config/palmux-deploy-test/secrets.env").expanduser()
REPO_PATH = pathlib.Path(__file__).resolve().parents[2]

# Global flag set by --no-deploy
NO_DEPLOY = False


# ---------------------------------------------------------------------------
# helpers (mirrored from sfccb3f_installer.py)
# ---------------------------------------------------------------------------


def ssh(cmd: str, *, check: bool = True, timeout: int = 1800) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", VM_HOST, cmd],
        check=check,
        capture_output=True,
        text=True,
        timeout=timeout,
    )


def rsync_repo() -> None:
    subprocess.run(
        ["ssh", VM_HOST, "rm -rf /tmp/palmux2-src && mkdir -p /tmp/palmux2-src"],
        check=True,
    )
    subprocess.run(
        [
            "rsync", "-az", "--delete",
            "--exclude=.git",
            "--exclude=node_modules",
            "--exclude=frontend/dist",
            "--exclude=frontend/node_modules",
            "--exclude=bin",
            "--exclude=tmp",
            f"{REPO_PATH}/",
            f"{VM_HOST}:/tmp/palmux2-src/",
        ],
        check=True,
    )


def load_secrets() -> dict[str, str]:
    if not SECRETS_FILE.exists():
        raise SystemExit(
            f"missing {SECRETS_FILE} — create it with CLOUDFLARE_API_TOKEN, ACME_EMAIL, "
            "BASIC_AUTH_USER, BASIC_AUTH_PASSWORD."
        )
    out: dict[str, str] = {}
    for line in SECRETS_FILE.read_text().splitlines():
        s = line.strip()
        if not s or s.startswith("#") or "=" not in s:
            continue
        k, v = s.split("=", 1)
        out[k.strip()] = v.strip()
    return out


def _tls_cert_issuer(host: str, port: int = 443, timeout: int = 10) -> dict:
    ctx = ssl.create_default_context()
    with socket.create_connection((host, port), timeout=timeout) as sock:
        with ctx.wrap_socket(sock, server_hostname=host) as ssock:
            return ssock.getpeercert()


def _https_get(
    url: str,
    *,
    user: str | None = None,
    password: str | None = None,
    timeout: int = 30,
) -> tuple[int, str, dict]:
    req = urllib.request.Request(url, headers={"User-Agent": "s85caca-e2e"})
    if user is not None and password is not None:
        token = base64.b64encode(f"{user}:{password}".encode()).decode()
        req.add_header("Authorization", f"Basic {token}")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", errors="replace"), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", errors="replace"), dict(e.headers)


def wait_for_https(
    url: str,
    *,
    user: str | None = None,
    password: str | None = None,
    expected_status: int = 200,
    timeout: int = 180,
) -> tuple[int, str, dict]:
    """Poll until HTTPS URL responds with expected_status or timeout."""
    end = time.monotonic() + timeout
    last_err: Exception | None = None
    last_status: int | None = None
    while time.monotonic() < end:
        try:
            status, body, headers = _https_get(url, user=user, password=password, timeout=10)
            last_status = status
            if status == expected_status:
                return status, body, headers
        except Exception as e:
            last_err = e
        time.sleep(5)
    raise TimeoutError(
        f"{url} did not respond with {expected_status} within {timeout}s; "
        f"last_status={last_status} last_err={last_err!r}"
    )


def _discover_demo_hostname() -> str | None:
    """Find the published hostname of the demo expose lease.

    Ground truth is the route portman registered in Caddy: its @id is
    "portman-<hostname>". Query the admin API first; fall back to `portman list`.
    Returns the subdomain (e.g. "demo--wt--portman-e2e-repo") or None.
    """
    p = ssh(
        "curl -sf localhost:2019/config/apps/http/servers/srv0/routes/ 2>/dev/null || echo '[]'",
        check=False,
    )
    try:
        for route in json.loads(p.stdout):
            rid = route.get("@id", "")
            if rid.startswith("portman-demo--wt--"):
                return rid[len("portman-"):]
    except (json.JSONDecodeError, TypeError, AttributeError):
        pass

    # Fallback: parse `portman list`. The hostname token is the one matching
    # our deterministic name/worktree (demo--wt--), to avoid matching log noise.
    p = ssh("PORTMAN_CONFIG_DIR=/etc/portman portman list 2>/dev/null || echo ''", check=False)
    for line in p.stdout.splitlines():
        for tok in line.split():
            tok = tok.strip()
            if tok.startswith("demo--wt--"):
                return tok.split(".")[0] if DOMAIN in tok else tok
    return None


# ---------------------------------------------------------------------------
# Deploy helper
# ---------------------------------------------------------------------------


def _install_cmd_model_b(secrets: dict[str, str]) -> str:
    user = secrets.get("BASIC_AUTH_USER", "")
    pw = secrets.get("BASIC_AUTH_PASSWORD", "")
    # shlex.quote every value: the whole string is parsed by the remote shell
    # via `ssh VM_HOST <cmd>`, so a secret containing spaces/quotes/$ must be
    # POSIX-quoted or it corrupts the env assignment (e.g. a password with a ').
    env_parts = [
        "PALMUX_FLAKE_REF=path:/tmp/palmux2-src",
        "PORTMAN_ROUTING=1",
        f"DOMAIN={shlex.quote(DOMAIN)}",
        f"CLOUDFLARE_API_TOKEN={shlex.quote(secrets['CLOUDFLARE_API_TOKEN'])}",
        f"ACME_EMAIL={shlex.quote(secrets['ACME_EMAIL'])}",
    ]
    if user:
        env_parts.append(f"BASIC_AUTH_USER={shlex.quote(user)}")
    if pw:
        env_parts.append(f"BASIC_AUTH_PASSWORD={shlex.quote(pw)}")
    return "cd /tmp && " + " ".join(env_parts) + " bash /tmp/palmux2-src/scripts/install.sh"


def deploy() -> None:
    """Rsync repo to VM and run installer with PORTMAN_ROUTING=1."""
    secrets = load_secrets()
    rsync_repo()
    cmd = _install_cmd_model_b(secrets)
    p = ssh(cmd, timeout=3600)
    assert p.returncode == 0, (
        f"install.sh failed: rc={p.returncode}\n"
        f"--- stdout tail ---\n{p.stdout[-3000:]}\n"
        f"--- stderr tail ---\n{p.stderr[-3000:]}"
    )


# ---------------------------------------------------------------------------
# Story S85caca-1 — Caddy model B: caddy.json + TLS + auth + ports
# ---------------------------------------------------------------------------


def test_AC_S85caca_1_1() -> None:
    """[AC-S85caca-1-1] caddy active; caddy.json exists; ExecStart uses caddy.json; TLS subjects include base + wildcard."""
    # caddy service active
    p = ssh("sudo systemctl is-active caddy")
    assert p.stdout.strip() == "active", f"caddy not active: {p.stdout!r}"

    # /etc/caddy/caddy.json exists
    ssh("sudo test -f /etc/caddy/caddy.json")

    # caddy.service ExecStart references caddy.json (not Caddyfile)
    p = ssh("sudo systemctl cat caddy | grep ExecStart=")
    assert "caddy.json" in p.stdout, (
        f"ExecStart does not reference caddy.json: {p.stdout!r}"
    )
    assert "Caddyfile" not in p.stdout, (
        f"ExecStart still references Caddyfile in model-B mode: {p.stdout!r}"
    )

    # Parse caddy.json: TLS subjects must include DOMAIN and *.DOMAIN
    p = ssh("sudo cat /etc/caddy/caddy.json")
    cfg = json.loads(p.stdout)
    subjects = (
        cfg.get("apps", {})
        .get("tls", {})
        .get("automation", {})
        .get("policies", [{}])[0]
        .get("subjects", [])
    )
    assert DOMAIN in subjects, f"TLS subjects missing {DOMAIN!r}: {subjects}"
    assert f"*.{DOMAIN}" in subjects, f"TLS subjects missing *.{DOMAIN!r}: {subjects}"

    # Admin API reachable
    p = ssh("curl -sf localhost:2019/config/ | head -1")
    assert p.stdout.strip() and p.returncode == 0, (
        f"Caddy admin API not reachable: {p.stdout!r} {p.stderr!r}"
    )


def test_AC_S85caca_1_2() -> None:
    """[AC-S85caca-1-2] palmux2 at palmux2.<base> and portman dashboard at <base> reachable via HTTPS; LE cert."""
    secrets = load_secrets()
    user = secrets.get("BASIC_AUTH_USER") or None
    pw = secrets.get("BASIC_AUTH_PASSWORD") or None

    # palmux2 UI
    palmux2_url = f"https://palmux2.{DOMAIN}/"
    status, body, _ = wait_for_https(palmux2_url, user=user, password=pw, timeout=300)
    assert status == 200, f"palmux2 URL status={status}"
    lo = body.lower()
    assert any(m in lo for m in ("palmux", '<div id="root"', "<title")), (
        f"palmux2 body does not contain UI marker: {body[:500]!r}"
    )

    # portman dashboard at base domain
    dashboard_url = f"https://{DOMAIN}/"
    status, body, _ = wait_for_https(dashboard_url, user=user, password=pw, timeout=120)
    assert status == 200, f"portman dashboard URL status={status}"
    # portman dashboard serves its own HTML; check for any HTML marker
    lo = body.lower()
    assert any(m in lo for m in ("<html", "<title", "portman", "dashboard", "<!doctype")), (
        f"portman dashboard body does not look like HTML: {body[:500]!r}"
    )

    # TLS cert: Let's Encrypt issuer
    cert = _tls_cert_issuer(f"palmux2.{DOMAIN}")
    issuer_parts = {k: v for tup in cert.get("issuer", ()) for k, v in tup}
    org = issuer_parts.get("organizationName", "")
    cn = issuer_parts.get("commonName", "")
    assert any(s in (org + cn) for s in ("Let's Encrypt", "R3", "R10", "E5", "E6", "E7", "E8")), (
        f"cert not from Let's Encrypt: issuer={issuer_parts}"
    )


def test_AC_S85caca_1_3() -> None:
    """[AC-S85caca-1-3] edge basic auth enforced; caddy.json has placeholder, real hash only in palmux.env."""
    secrets = load_secrets()
    user = secrets.get("BASIC_AUTH_USER")
    pw = secrets.get("BASIC_AUTH_PASSWORD")
    if not user or not pw:
        raise AssertionError(
            "secrets.env must have BASIC_AUTH_USER and BASIC_AUTH_PASSWORD for AC-S85caca-1-3"
        )

    palmux2_url = f"https://palmux2.{DOMAIN}/"
    dashboard_url = f"https://{DOMAIN}/"

    # Without credentials -> 401
    for url in [palmux2_url, dashboard_url]:
        status, _, headers = _https_get(url, timeout=15)
        assert status == 401, f"expected 401 without auth at {url}, got {status}"
        www = headers.get("Www-Authenticate") or headers.get("WWW-Authenticate") or ""
        assert "Basic" in www, f"missing Basic challenge at {url}: {www!r}"

    # With correct credentials -> 200
    for url in [palmux2_url, dashboard_url]:
        status, _, _ = _https_get(url, user=user, password=pw, timeout=15)
        assert status == 200, f"expected 200 with auth at {url}, got {status}"

    # caddy.json must contain the literal {env.BASIC_AUTH_HASH} placeholder, NOT the real hash
    p = ssh("sudo cat /etc/caddy/caddy.json")
    caddy_json_content = p.stdout
    assert "{env.BASIC_AUTH_HASH}" in caddy_json_content, (
        "caddy.json does not contain {env.BASIC_AUTH_HASH} placeholder"
    )
    # No literal bcrypt hash (starts with $2a$, $2b$, or $2y$)
    assert not re.search(r'\$2[aby]\$', caddy_json_content), (
        f"literal bcrypt hash leaked into /etc/caddy/caddy.json"
    )
    # Plaintext password must not appear
    assert pw not in caddy_json_content, (
        "plaintext password found in /etc/caddy/caddy.json"
    )

    # Real bcrypt hash must be in palmux.env (root:caddy 0640)
    p = ssh("sudo stat -c '%a %U:%G' /etc/caddy/palmux.env")
    assert p.stdout.strip() == "640 root:caddy", (
        f"palmux.env must be mode 640 owned root:caddy, got: {p.stdout.strip()!r}"
    )
    p = ssh(r"sudo grep -cE '^BASIC_AUTH_HASH=\$2[aby]\$' /etc/caddy/palmux.env")
    assert p.stdout.strip() == "1", (
        f"real bcrypt hash not found in /etc/caddy/palmux.env: {p.stdout!r}"
    )


def test_AC_S85caca_1_4() -> None:
    """[AC-S85caca-1-4] port bindings: palmux2 127.0.0.1:8080, portman 127.0.0.1:8090, caddy :443 only."""
    p = ssh("ss -ltnp")
    lines = p.stdout

    # palmux2 on 127.0.0.1:8080 (NOT 0.0.0.0)
    assert "127.0.0.1:8080" in lines, (
        f"palmux2 not bound to 127.0.0.1:8080:\n{lines}"
    )
    # Should NOT be bound to 0.0.0.0:8080
    # grep for 0.0.0.0:8080 specifically
    assert not re.search(r'0\.0\.0\.0:8080', lines), (
        f"palmux2 bound to 0.0.0.0:8080 (should be localhost-only):\n{lines}"
    )

    # portman dashboard on 127.0.0.1:8090 (NOT 0.0.0.0)
    assert "127.0.0.1:8090" in lines, (
        f"portman dashboard not bound to 127.0.0.1:8090:\n{lines}"
    )
    assert not re.search(r'0\.0\.0\.0:8090', lines), (
        f"portman dashboard bound to 0.0.0.0:8090 (should be localhost-only):\n{lines}"
    )

    # caddy on :443 (any addr, both IPv4 and IPv6 ok)
    assert re.search(r'[*\[:]*:443', lines), (
        f"caddy not bound to :443:\n{lines}"
    )


# ---------------------------------------------------------------------------
# Story S85caca-2 — portman config + dynamic expose
# ---------------------------------------------------------------------------


def test_AC_S85caca_2_1() -> None:
    """[AC-S85caca-2-1] portman config files correct; portman-serve active; portman-sync enabled; portman-gc.timer active."""
    # /etc/portman/config.toml: required fields
    p = ssh("cat /etc/portman/config.toml")
    toml_content = p.stdout
    assert 'type = "caddy"' in toml_content, f"config.toml missing type=caddy: {toml_content!r}"
    assert "caddy_api" in toml_content, f"config.toml missing caddy_api: {toml_content!r}"
    assert f'domain_suffix = "{DOMAIN}"' in toml_content, (
        f"config.toml missing domain_suffix={DOMAIN!r}: {toml_content!r}"
    )
    assert "host_pattern" in toml_content, f"config.toml missing host_pattern: {toml_content!r}"

    # /etc/portman/services.json: palmux2 permanent with expose=true
    p = ssh("cat /etc/portman/services.json")
    services = json.loads(p.stdout)
    permanents = services.get("permanent", [])
    palmux2_perm = next((s for s in permanents if s.get("name") == "palmux2"), None)
    assert palmux2_perm is not None, f"palmux2 not in permanent services: {permanents}"
    assert palmux2_perm.get("expose") is True, (
        f"palmux2 permanent service does not have expose=true: {palmux2_perm}"
    )

    # portman-serve.service active
    p = ssh("sudo systemctl is-active portman-serve.service")
    assert p.stdout.strip() == "active", (
        f"portman-serve.service not active: {p.stdout!r}"
    )

    # portman-sync.service enabled (and has After=caddy.service + BindsTo=caddy.service)
    p = ssh("sudo systemctl is-enabled portman-sync.service", check=False)
    assert p.stdout.strip() in ("enabled", "static"), (
        f"portman-sync.service not enabled: {p.stdout!r}"
    )
    p = ssh("sudo systemctl cat portman-sync.service")
    unit_content = p.stdout
    assert "After=caddy.service" in unit_content, (
        f"portman-sync.service missing After=caddy.service: {unit_content!r}"
    )
    assert "BindsTo=caddy.service" in unit_content, (
        f"portman-sync.service missing BindsTo=caddy.service: {unit_content!r}"
    )

    # portman-gc.timer active
    p = ssh("sudo systemctl is-active portman-gc.timer")
    assert p.stdout.strip() == "active", (
        f"portman-gc.timer not active: {p.stdout!r}"
    )


def test_AC_S85caca_2_2() -> None:
    """[AC-S85caca-2-2] portman exec --expose publishes live HTTPS route (Directory listing reachable)."""
    secrets = load_secrets()
    user = secrets.get("BASIC_AUTH_USER") or None
    pw = secrets.get("BASIC_AUTH_PASSWORD") or None

    # Create a throwaway git repo in a deterministic temp dir
    repo_dir = "/tmp/portman-e2e-repo"
    p = ssh(
        f"rm -rf {repo_dir} && git init {repo_dir} && "
        f"cd {repo_dir} && git commit --allow-empty -m init"
    )
    assert p.returncode == 0, f"git init failed: {p.stderr!r}"

    # Launch python3 -m http.server via portman exec --expose in background.
    # portman will pick a port from its configured range, register the route,
    # and print the hostname it registered.
    # We use nohup + setsid to detach; capture PID for later cleanup.
    launch_cmd = (
        f"cd {repo_dir} && "
        "PORTMAN_CONFIG_DIR=/etc/portman "
        "nohup portman exec --name demo --worktree wt --expose -- "
        "python3 -m http.server {} "
        f"> /tmp/portman-e2e-demo.log 2>&1 & echo $!"
    )
    p = ssh(launch_cmd, timeout=30)
    # Save PID for cleanup
    demo_pid = p.stdout.strip().splitlines()[-1].strip()
    # Wait for route to register
    time.sleep(5)

    # Discover the published hostname. The ground truth is the route portman
    # actually registered in Caddy (its @id is "portman-<hostname>"), so query
    # the admin API first; fall back to parsing `portman list`.
    hostname = _discover_demo_hostname()
    portman_list_output = ssh(
        "PORTMAN_CONFIG_DIR=/etc/portman portman list 2>/dev/null || echo FAILED"
    ).stdout
    assert hostname is not None, (
        "Could not discover demo expose hostname from Caddy admin API or portman "
        f"list. demo.log:\n{ssh('cat /tmp/portman-e2e-demo.log 2>/dev/null || true', check=False).stdout}"
        f"\nportman list:\n{portman_list_output}"
    )
    assert hostname.startswith("demo--wt--"), (
        f"discovered hostname {hostname!r} does not match expected demo--wt--<repo>"
    )

    # Build FQDN for the expose route
    fqdn = f"{hostname}.{DOMAIN}"
    expose_url = f"https://{fqdn}/"

    # Fetch the exposed route (with basic auth if configured)
    status, body, _ = wait_for_https(expose_url, user=user, password=pw, timeout=60)
    assert status == 200, f"exposed route {expose_url} status={status}"
    # python3 -m http.server returns a directory listing
    assert "Directory listing" in body or "directory" in body.lower(), (
        f"expected directory listing from http.server, got: {body[:300]!r}"
    )

    # Store state for AC-2-3
    p = ssh(f"echo {demo_pid} > /tmp/portman-e2e-pid && echo {hostname} > /tmp/portman-e2e-hostname")
    assert p.returncode == 0


def test_AC_S85caca_2_3() -> None:
    """[AC-S85caca-2-3] after killing demo, portman gc + sync → exposed route returns 502 or 404."""
    secrets = load_secrets()
    user = secrets.get("BASIC_AUTH_USER") or None
    pw = secrets.get("BASIC_AUTH_PASSWORD") or None

    # Retrieve saved PID and hostname from AC-2-2 (which runs first in a full run)
    p = ssh("cat /tmp/portman-e2e-pid 2>/dev/null && cat /tmp/portman-e2e-hostname 2>/dev/null", check=False)
    lines = [l.strip() for l in p.stdout.strip().splitlines() if l.strip()]
    if len(lines) >= 2:
        demo_pid, hostname = lines[0], lines[1]
    else:
        demo_pid, hostname = None, _discover_demo_hostname()

    # AC-2-3 verifies that releasing a lease REMOVES its route — that requires a
    # live demo route to exist first (created by AC-2-2). If we can't find one,
    # the precondition is unmet: fail loudly rather than silently passing.
    assert hostname is not None, (
        "AC-S85caca-2-3 requires the demo expose route from AC-S85caca-2-2. "
        "No demo route found (run the full Story 2, not just 2-3)."
    )

    # Kill the demo process (so the upstream is down and the lease goes stale)
    if demo_pid:
        ssh(f"kill {demo_pid} 2>/dev/null || true", check=False)
    ssh("pkill -f 'portman exec.*demo' 2>/dev/null; pkill -f 'http.server' 2>/dev/null; true", check=False)
    time.sleep(2)

    # Reconcile: gc removes stale leases, sync reconciles Caddy routes.
    ssh("PORTMAN_CONFIG_DIR=/etc/portman portman gc", check=False)
    ssh("PORTMAN_CONFIG_DIR=/etc/portman portman sync", check=False)

    fqdn = f"{hostname}.{DOMAIN}"
    expose_url = f"https://{fqdn}/"
    # Poll: Caddy admin reload is eventually-consistent, so allow a short window
    # for the route to disappear. Route gone → 404 (no route) or 502/503 (upstream
    # down). 200 would mean the route is still live.
    deadline = time.monotonic() + 30
    status = None
    while time.monotonic() < deadline:
        status, _, _ = _https_get(expose_url, user=user, password=pw, timeout=10)
        if status in (404, 502, 503):
            break
        time.sleep(3)
    assert status in (404, 502, 503), (
        f"expected 404/502/503 after lease release, got {status} for {expose_url} "
        "(route was not removed)"
    )

    # Cleanup temp files
    ssh("rm -f /tmp/portman-e2e-pid /tmp/portman-e2e-hostname /tmp/portman-e2e-demo.log", check=False)
    ssh("rm -rf /tmp/portman-e2e-repo", check=False)


def test_AC_S85caca_2_4() -> None:
    """[AC-S85caca-2-4] idempotency: re-run installer; model-B artifacts gated by PORTMAN_ROUTING=1 in script."""
    secrets = load_secrets()

    # --- Part 1: Idempotency re-run ---
    if not NO_DEPLOY:
        rsync_repo()
        cmd = _install_cmd_model_b(secrets)
        p = ssh(cmd, timeout=3600)
        assert p.returncode == 0, (
            f"idempotency re-run failed: rc={p.returncode}\n{p.stderr[-2000:]}"
        )

    # caddy still active after re-run
    p = ssh("sudo systemctl is-active caddy")
    assert p.stdout.strip() == "active", f"caddy not active after re-run: {p.stdout!r}"

    # portman-serve still active after re-run
    p = ssh("sudo systemctl is-active portman-serve.service")
    assert p.stdout.strip() == "active", (
        f"portman-serve not active after re-run: {p.stdout!r}"
    )

    # palmux2 route still returns 200
    user = secrets.get("BASIC_AUTH_USER") or None
    pw = secrets.get("BASIC_AUTH_PASSWORD") or None
    status, _, _ = _https_get(f"https://palmux2.{DOMAIN}/", user=user, password=pw, timeout=30)
    assert status == 200, f"palmux2 route not 200 after re-run: status={status}"

    # No duplicate routes in admin API (portman-palmux2 should appear exactly once)
    p = ssh("curl -sf localhost:2019/config/apps/http/servers/srv0/routes/ 2>/dev/null || echo '[]'")
    try:
        routes = json.loads(p.stdout)
        palmux2_routes = [r for r in routes if r.get("@id") == "portman-palmux2"]
        assert len(palmux2_routes) <= 1, (
            f"duplicate portman-palmux2 routes found: {len(palmux2_routes)}"
        )
    except (json.JSONDecodeError, TypeError):
        pass  # If admin API isn't returning JSON, route check is best-effort

    # --- Part 2: Backward-compat guard (structural, no VM wipe needed) ---
    # Verify that every model-B artifact in install.sh is gated under PORTMAN_ROUTING=1.
    # We check this structurally by reading scripts/install.sh from the synced source on VM.
    p = ssh("cat /tmp/palmux2-src/scripts/install.sh")
    script_content = p.stdout

    # Model-B artifact markers that must appear only inside a PORTMAN_ROUTING=1 block
    model_b_artifacts = [
        "caddy.json",
        "/etc/portman",
        "portman-serve.service",
        "portman-gc.timer",
        "edge-basic-auth",
    ]

    # Verify PORTMAN_ROUTING variable is declared
    assert 'PORTMAN_ROUTING=' in script_content, (
        "PORTMAN_ROUTING env var not declared in install.sh"
    )
    # Verify the main portman routing block guard exists
    assert 'if [ "$PORTMAN_ROUTING" = "1" ]' in script_content, (
        'install.sh missing guard: if [ "$PORTMAN_ROUTING" = "1" ]'
    )

    # Structural gate (comment-aware, non-tautological): every model-B artifact
    # must (a) actually appear in real (non-comment) code — so dropping the
    # feature fails the test — and (b) have NO real-code occurrence before the
    # first PORTMAN_ROUTING guard, i.e. it is never emitted ungated. An
    # install.sh that wrote caddy.json unconditionally (before the guard) fails.
    src_lines = script_content.splitlines()

    def _is_comment(ln: str) -> bool:
        return ln.lstrip().startswith("#")

    guard_lines = [i for i, ln in enumerate(src_lines)
                   if 'if [ "$PORTMAN_ROUTING" = "1" ]' in ln and not _is_comment(ln)]
    assert guard_lines, 'install.sh has no real-code PORTMAN_ROUTING=1 guard'
    first_guard = min(guard_lines)

    for artifact in model_b_artifacts:
        code_lines = [i for i, ln in enumerate(src_lines)
                      if artifact in ln and not _is_comment(ln)]
        assert code_lines, (
            f"model-B artifact '{artifact}' not found in real code of install.sh "
            "(feature missing or only mentioned in comments)"
        )
        assert min(code_lines) > first_guard, (
            f"model-B artifact '{artifact}' appears in ungated code "
            f"(line {min(code_lines)+1}, before the PORTMAN_ROUTING guard at "
            f"line {first_guard+1}) — it must be gated by PORTMAN_ROUTING=1"
        )

    # The default (non-portman) Caddyfile path must still be present and intact,
    # so an install with PORTMAN_ROUTING unset keeps the legacy single-site setup.
    assert "/etc/caddy/Caddyfile" in script_content and "reverse_proxy 127.0.0.1:8080" in script_content, (
        "default Caddyfile path (reverse_proxy 127.0.0.1:8080) missing from install.sh"
    )

    # Preflight check for PORTMAN_ROUTING=1 without DOMAIN → installer dies
    # (test with a benign missing-DOMAIN case against VM where /tmp/palmux2-src exists)
    p = ssh(
        "PORTMAN_ROUTING=1 bash /tmp/palmux2-src/scripts/install.sh",
        check=False, timeout=30
    )
    assert p.returncode != 0, (
        "Expected non-zero rc when PORTMAN_ROUTING=1 but DOMAIN is missing"
    )
    combined = p.stdout + p.stderr
    assert "PORTMAN_ROUTING=1 requires DOMAIN" in combined, (
        f"Expected DOMAIN-required error message: {combined[-500:]!r}"
    )


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------


TESTS: list[tuple[str, callable]] = [
    ("S85caca-1-1", test_AC_S85caca_1_1),
    ("S85caca-1-2", test_AC_S85caca_1_2),
    ("S85caca-1-3", test_AC_S85caca_1_3),
    ("S85caca-1-4", test_AC_S85caca_1_4),
    ("S85caca-2-1", test_AC_S85caca_2_1),
    ("S85caca-2-2", test_AC_S85caca_2_2),
    ("S85caca-2-3", test_AC_S85caca_2_3),
    ("S85caca-2-4", test_AC_S85caca_2_4),
]


def main(argv: list[str]) -> int:
    global NO_DEPLOY

    parser = argparse.ArgumentParser(
        description="S85caca portman routing E2E test suite",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "examples:\n"
            "  python3 tests/e2e/s85caca_portman.py              # deploy + all tests\n"
            "  python3 tests/e2e/s85caca_portman.py --no-deploy  # skip deploy, verify existing state\n"
            "  python3 tests/e2e/s85caca_portman.py S85caca-1-1  # single AC\n"
            "  python3 tests/e2e/s85caca_portman.py --story 1    # all of Story-1\n"
        ),
    )
    parser.add_argument(
        "ac_id",
        nargs="?",
        metavar="AC_ID",
        help="run a single AC (e.g. S85caca-1-1)",
    )
    parser.add_argument(
        "--story",
        metavar="N",
        help="run all ACs for story N (e.g. --story 1)",
    )
    parser.add_argument(
        "--no-deploy",
        action="store_true",
        help="skip installer deploy step; verify against already-deployed VM",
    )
    args = parser.parse_args(argv[1:])
    NO_DEPLOY = args.no_deploy

    # Select tests
    selected = TESTS
    if args.ac_id:
        selected = [(t, f) for t, f in TESTS if t == args.ac_id]
        if not selected:
            print(f"no such AC: {args.ac_id}", file=sys.stderr)
            return 2
    elif args.story:
        prefix = f"S85caca-{args.story}-"
        selected = [(t, f) for t, f in TESTS if t.startswith(prefix)]

    # Deploy once before running tests (unless --no-deploy)
    if not NO_DEPLOY and selected:
        print("\n=== deploy ===", flush=True)
        try:
            deploy()
            print("deploy: OK")
        except Exception as e:
            print(f"deploy: FAILED: {e!r}", file=sys.stderr)
            return 1

    results: list[tuple[str, str, str | None]] = []
    for tag, fn in selected:
        print(f"\n=== [AC-{tag}] ===", flush=True)
        t0 = time.monotonic()
        try:
            fn()
            dt = time.monotonic() - t0
            results.append((tag, "PASS", f"{dt:.1f}s"))
            print(f"[AC-{tag}] PASS ({dt:.1f}s)")
        except NotImplementedError as e:
            results.append((tag, "TODO", str(e)))
            print(f"[AC-{tag}] TODO: {e}")
        except AssertionError as e:
            results.append((tag, "FAIL", str(e)[:500]))
            print(f"[AC-{tag}] FAIL: {e}", file=sys.stderr)
        except Exception as e:
            results.append((tag, "ERROR", repr(e)[:500]))
            print(f"[AC-{tag}] ERROR: {e!r}", file=sys.stderr)

    print("\n========== SUMMARY ==========")
    for tag, status, msg in results:
        m = f" — {msg}" if msg else ""
        print(f"  {status:5} AC-{tag}{m}")
    nonpass = [r for r in results if r[1] in ("FAIL", "ERROR")]
    return 0 if not nonpass else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
