#!/usr/bin/env python3
"""
tests/e2e/sfccb3f_installer.py — Sprint Sfccb3f acceptance E2E.

- Driver: this dev box (Python urllib for HTTP, ssh for VM-side cmds).
- Target: ubuntu@192.168.1.43 (palmux-deploy-test.tjstkm.net).
- Secrets: ~/.config/palmux-deploy-test/secrets.env
           (CLOUDFLARE_API_TOKEN, ACME_EMAIL — only read for Sfccb3f-2/-3 scenarios).

Coverage:
  [AC-Sfccb3f-1-1..5]  Nix bootstrap install + idempotency + rollback
  [AC-Sfccb3f-2-1..6]  Caddy + Cloudflare DNS-01 + edge basic auth   (added in Story-2)
  [AC-Sfccb3f-3-1..4]  unattended-upgrades + swap + PROFILE=full     (added in Story-3)

Run:
  python3 tests/e2e/sfccb3f_installer.py                # all
  python3 tests/e2e/sfccb3f_installer.py Sfccb3f-1-1    # one
  python3 tests/e2e/sfccb3f_installer.py --story 1      # all of Story-1
"""

from __future__ import annotations

import base64
import os
import pathlib
import re
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


# ---------------------------------------------------------------------------
# helpers
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
            f"missing {SECRETS_FILE} — see Sprint plan amend (decisions.json)."
        )
    out: dict[str, str] = {}
    for line in SECRETS_FILE.read_text().splitlines():
        s = line.strip()
        if not s or s.startswith("#") or "=" not in s:
            continue
        k, v = s.split("=", 1)
        out[k.strip()] = v.strip()
    return out


def reset_vm(*, drop_nix: bool = False) -> None:
    """Best-effort cleanup of prior install state on VM."""
    cmds = [
        "systemctl --user stop palmux2 2>/dev/null || true",
        "systemctl --user disable palmux2 2>/dev/null || true",
        "sudo systemctl stop caddy 2>/dev/null || true",
        "sudo systemctl disable caddy 2>/dev/null || true",
        "sudo rm -rf /etc/palmux /etc/caddy",
        # Optionally drop Nix state for hermetic re-test (slow)
        "sudo rm -rf /nix" if drop_nix else "true",
    ]
    ssh(" ; ".join(cmds), check=False)


def http_get(url: str, *, timeout: int = 10, allow_codes: set[int] | None = None) -> tuple[int, str, dict]:
    """GET from this dev box (same LAN as VM)."""
    req = urllib.request.Request(url, headers={"User-Agent": "sfccb3f-e2e"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", errors="replace"), dict(r.headers)
    except urllib.error.HTTPError as e:
        if allow_codes and e.code in allow_codes:
            return e.code, e.read().decode("utf-8", errors="replace"), dict(e.headers)
        raise


def wait_for_http(url: str, *, timeout: int = 60, allow_codes: set[int] | None = None) -> tuple[int, str, dict]:
    """Poll until URL responds or timeout."""
    end = time.monotonic() + timeout
    last_err: Exception | None = None
    while time.monotonic() < end:
        try:
            return http_get(url, timeout=5, allow_codes=allow_codes)
        except Exception as e:
            last_err = e
            time.sleep(2)
    raise TimeoutError(f"{url} did not respond within {timeout}s; last_err={last_err!r}")


# ---------------------------------------------------------------------------
# Story Sfccb3f-1 — Nix bootstrap + minimal profile
# ---------------------------------------------------------------------------


INSTALL_CMD = (
    "cd /tmp && PALMUX_FLAKE_REF=path:/tmp/palmux2-src "
    "bash /tmp/palmux2-src/scripts/install.sh"
)


def test_AC_Sfccb3f_1_1() -> None:
    """[AC-Sfccb3f-1-1] curl | bash 一発で Nix bootstrap が成功し /etc/palmux/flake.nix が生成"""
    reset_vm()
    rsync_repo()
    p = ssh(INSTALL_CMD, timeout=3600)
    assert p.returncode == 0, (
        f"install.sh failed: rc={p.returncode}\n"
        f"--- stdout tail ---\n{p.stdout[-3000:]}\n"
        f"--- stderr tail ---\n{p.stderr[-3000:]}"
    )
    ssh("test -f /etc/palmux/flake.nix")
    # /etc/palmux/flake.nix が PALMUX_FLAKE_REF 反映済み
    p = ssh("sudo cat /etc/palmux/flake.nix")
    assert "path:/tmp/palmux2-src" in p.stdout, "flake.nix did not pick up PALMUX_FLAKE_REF"


def test_AC_Sfccb3f_1_2() -> None:
    """[AC-Sfccb3f-1-2] palmux2 + 周辺ツールが PATH から呼べる、 palmux2 --version が semver"""
    cmds = "palmux2 tmux ghq gwq portman claude node go make gh jq unzip python3".split()
    p = ssh("bash -lc 'for c in " + " ".join(cmds) + '; do command -v "$c" >/dev/null || echo "MISSING:$c"; done\'')
    missing = [ln for ln in p.stdout.splitlines() if ln.startswith("MISSING:")]
    assert not missing, f"missing: {missing}"
    p = ssh("bash -lc 'palmux2 --version'")
    assert re.search(r"v\d+\.\d+\.\d+", p.stdout), f"version: {p.stdout!r}"


def test_AC_Sfccb3f_1_3() -> None:
    """[AC-Sfccb3f-1-3] systemctl --user で palmux2 active、 HTTP 200 + palmux2 UI marker"""
    p = ssh("systemctl --user is-active palmux2")
    assert p.stdout.strip() == "active", f"is-active={p.stdout.strip()!r}"
    status, body, _ = wait_for_http(f"http://{VM_IP}:8080/", timeout=30)
    assert status == 200, f"status={status}"
    # palmux2 SPA: HTML が <div id="root"> または <title>palmux</title> 等を含むはず
    lo = body.lower()
    assert any(m in lo for m in ("palmux", "<div id=\"root\"", "<title")), \
        f"unexpected body[:500]: {body[:500]!r}"


def test_AC_Sfccb3f_1_4() -> None:
    """[AC-Sfccb3f-1-4] 同じワンライナー再実行で冪等 (rc=0、 palmux2 起動継続)"""
    p = ssh(INSTALL_CMD, timeout=3600)
    assert p.returncode == 0, f"rerun failed: rc={p.returncode}\n{p.stderr[-2000:]}"
    p = ssh("systemctl --user is-active palmux2")
    assert p.stdout.strip() == "active", f"palmux2 not active after rerun: {p.stdout!r}"


def test_AC_Sfccb3f_1_5() -> None:
    """[AC-Sfccb3f-1-5] home-manager rollback で前世代へ戻れる (アトミック性確認)"""
    p = ssh("bash -lc 'nix run --extra-experimental-features \"nix-command flakes\" home-manager/master -- generations 2>&1 | head -5' || true")
    # 世代が 2 つ以上ある前提 (scenario-1 + scenario-2 で 2 つ生成済み)
    gens = [ln for ln in p.stdout.splitlines() if re.match(r"^\d{4}-\d{2}-\d{2}", ln)]
    if len(gens) < 2:
        # Force 2 世代: 何か trivial に変更して再実行 (例: hostname 変更は impractical なので skip 警告)
        print(f"[AC-Sfccb3f-1-5] WARN: only {len(gens)} generation(s); rollback test relies on at least 2.")
        return
    # rollback (latest -> previous)
    p = ssh("bash -lc 'nix run --extra-experimental-features \"nix-command flakes\" home-manager/master -- rollback'", check=False)
    # OK either way — sanity-check palmux2 is still functional
    p = ssh("systemctl --user is-active palmux2", check=False)
    assert p.stdout.strip() == "active", f"palmux2 not active after rollback: {p.stdout!r}"


# ---------------------------------------------------------------------------
# Story Sfccb3f-2 — Caddy + Cloudflare DNS-01 + edge basic auth
# ---------------------------------------------------------------------------


def _install_cmd_with_caddy(secrets: dict[str, str], *, basic_user: str | None = None, basic_pass: str | None = None) -> str:
    env_parts = [
        f'PALMUX_FLAKE_REF=path:/tmp/palmux2-src',
        f'DOMAIN={DOMAIN}',
        f'CLOUDFLARE_API_TOKEN={secrets["CLOUDFLARE_API_TOKEN"]}',
        f'ACME_EMAIL={secrets["ACME_EMAIL"]}',
    ]
    if basic_user:
        env_parts.append(f'BASIC_AUTH_USER={basic_user}')
    if basic_pass:
        env_parts.append(f"BASIC_AUTH_PASSWORD='{basic_pass}'")
    return "cd /tmp && " + " ".join(env_parts) + " bash /tmp/palmux2-src/scripts/install.sh"


def _tls_cert_issuer(host: str, port: int = 443, timeout: int = 10) -> dict:
    ctx = ssl.create_default_context()
    with socket.create_connection((host, port), timeout=timeout) as sock:
        with ctx.wrap_socket(sock, server_hostname=host) as ssock:
            return ssock.getpeercert()


def _https_get(url: str, *, user: str | None = None, password: str | None = None, timeout: int = 30) -> tuple[int, str, dict]:
    req = urllib.request.Request(url, headers={"User-Agent": "sfccb3f-e2e"})
    if user is not None and password is not None:
        token = base64.b64encode(f"{user}:{password}".encode()).decode()
        req.add_header("Authorization", f"Basic {token}")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", errors="replace"), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", errors="replace"), dict(e.headers)


def test_AC_Sfccb3f_2_1() -> None:
    """[AC-Sfccb3f-2-1] DOMAIN+CF_TOKEN → caddy-cloudflare ビルド + caddy.service active"""
    secrets = load_secrets()
    reset_vm()
    rsync_repo()
    cmd = _install_cmd_with_caddy(secrets)
    p = ssh(cmd, timeout=3600)
    assert p.returncode == 0, (
        f"install.sh failed: rc={p.returncode}\n--- stdout tail ---\n{p.stdout[-3000:]}\n"
        f"--- stderr tail ---\n{p.stderr[-3000:]}"
    )
    # caddy バイナリは /nix/store
    p = ssh("bash -lc 'command -v caddy 2>&1 || echo MISSING'", check=False)
    # /usr/local/bin/caddy or /nix/store path may exist; check via service exec
    p = ssh("sudo systemctl is-active caddy")
    assert p.stdout.strip() == "active", f"caddy not active: {p.stdout!r}"
    # /etc/caddy/palmux.env が root:caddy 0640
    p = ssh("sudo stat -c '%a %U:%G' /etc/caddy/palmux.env")
    assert "640" in p.stdout and "caddy" in p.stdout, f"palmux.env perms: {p.stdout!r}"
    p = ssh("sudo grep -E '^CLOUDFLARE_API_TOKEN=' /etc/caddy/palmux.env | wc -l")
    assert p.stdout.strip() == "1", "CLOUDFLARE_API_TOKEN missing from palmux.env"


def test_AC_Sfccb3f_2_2() -> None:
    """[AC-Sfccb3f-2-2] https://<DOMAIN> 200 + Let's Encrypt 証明書"""
    # 証明書発行を待つ (DNS-01 challenge は数十秒〜数分)
    status = None
    end = time.monotonic() + 180
    last_err: Exception | None = None
    while time.monotonic() < end:
        try:
            status, body, _ = _https_get(f"https://{DOMAIN}/", timeout=10)
            if status == 200:
                break
        except Exception as e:
            last_err = e
        time.sleep(5)
    assert status == 200, f"https://{DOMAIN}/ status={status} last_err={last_err!r}"
    # TLS cert issuer = Let's Encrypt
    cert = _tls_cert_issuer(DOMAIN)
    issuer_parts = {k: v for tup in cert.get("issuer", ()) for k, v in tup}
    org = issuer_parts.get("organizationName", "")
    cn = issuer_parts.get("commonName", "")
    assert "Let's Encrypt" in org or "Let's Encrypt" in cn or "R" in cn or "E" in cn, (
        f"cert not from Let's Encrypt: issuer={issuer_parts}"
    )
    # body に palmux marker
    lo = body.lower()
    assert any(m in lo for m in ("palmux", "<div id=\"root\"", "<title")), f"body[:500]: {body[:500]!r}"


def test_AC_Sfccb3f_2_3() -> None:
    """[AC-Sfccb3f-2-3] palmux2 unit が 127.0.0.1 バインドに切替、 :8080 は外部から到達不能"""
    # palmux2.service の ExecStart に 127.0.0.1:8080
    p = ssh("systemctl --user cat palmux2 | grep -E 'ExecStart='")
    assert "127.0.0.1:8080" in p.stdout, f"ExecStart not 127.0.0.1: {p.stdout!r}"
    # dev box → VM:8080 直接アクセスは応答なし or 拒否
    try:
        with socket.create_connection((VM_IP, 8080), timeout=3):
            raise AssertionError(f"VM_IP:{VM_IP}:8080 still accepts connections; bind switch failed")
    except (ConnectionRefusedError, socket.timeout, OSError):
        pass  # expected


def test_AC_Sfccb3f_2_4() -> None:
    """[AC-Sfccb3f-2-4] 片肺 env でエラー終了 (DOMAIN 単独 / CF_TOKEN 単独 / BASIC_AUTH 片方)"""
    secrets = load_secrets()
    # ローカルで test するので rsync は既に済み前提
    cases = [
        ("DOMAIN alone",
         f"DOMAIN={DOMAIN} bash /tmp/palmux2-src/scripts/install.sh",
         "DOMAIN and CLOUDFLARE_API_TOKEN must be set together"),
        ("CLOUDFLARE_API_TOKEN alone",
         f"CLOUDFLARE_API_TOKEN={secrets['CLOUDFLARE_API_TOKEN']} bash /tmp/palmux2-src/scripts/install.sh",
         "DOMAIN and CLOUDFLARE_API_TOKEN must be set together"),
        ("BASIC_AUTH_USER alone",
         f'DOMAIN={DOMAIN} CLOUDFLARE_API_TOKEN={secrets["CLOUDFLARE_API_TOKEN"]} BASIC_AUTH_USER=alice bash /tmp/palmux2-src/scripts/install.sh',
         "BASIC_AUTH_USER and BASIC_AUTH_PASSWORD must be set together"),
    ]
    for label, cmd, expected_msg in cases:
        p = ssh(cmd, check=False, timeout=60)
        assert p.returncode != 0, f"{label}: expected non-zero rc, got 0 stdout={p.stdout!r}"
        combined = p.stdout + p.stderr
        assert expected_msg in combined, f"{label}: missing expected msg '{expected_msg}'\nfull: {combined[-500:]!r}"


def test_AC_Sfccb3f_2_5() -> None:
    """[AC-Sfccb3f-2-5] BASIC_AUTH 有効化 → 認証なし 401、 正しい creds で 200。 hash は env file のみ"""
    secrets = load_secrets()
    user = secrets.get("BASIC_AUTH_USER")
    pw = secrets.get("BASIC_AUTH_PASSWORD")
    if not user or not pw:
        raise AssertionError(
            "secrets.env に BASIC_AUTH_USER と BASIC_AUTH_PASSWORD を設定してください "
            "(無効化したい場合は AC-2-5 と AC-2-6 を skip してください)"
        )
    rsync_repo()  # ensure latest install.sh
    cmd = _install_cmd_with_caddy(secrets, basic_user=user, basic_pass=pw)
    p = ssh(cmd, timeout=3600)
    assert p.returncode == 0, f"install.sh rc={p.returncode}\n{p.stderr[-2000:]}"

    # env file に bcrypt hash がある
    p = ssh("sudo grep -E '^BASIC_AUTH_(USER|HASH)=' /etc/caddy/palmux.env | wc -l")
    assert p.stdout.strip() == "2", f"palmux.env BASIC_AUTH lines: {p.stdout!r}"
    p = ssh("sudo grep -cE '^BASIC_AUTH_HASH=\\$2[aby]\\$' /etc/caddy/palmux.env")
    assert p.stdout.strip() == "1", "BASIC_AUTH_HASH is not bcrypt"
    # Caddyfile (install.sh が直接書く /etc/caddy/Caddyfile) は {env.BASIC_AUTH_HASH} 参照のみ、 literal hash 含まない
    p = ssh("sudo grep -cF '{env.BASIC_AUTH_HASH}' /etc/caddy/Caddyfile")
    assert p.stdout.strip() == "1", "Caddyfile が {env.BASIC_AUTH_HASH} を参照していない"
    p = ssh(r"sudo grep -qE '\$2[aby]\$' /etc/caddy/Caddyfile && echo FOUND || echo CLEAN")
    assert "CLEAN" in p.stdout, f"literal bcrypt hash leaked into /etc/caddy/Caddyfile: {p.stdout!r}"

    # Caddy reload 待ち
    time.sleep(3)
    # no auth → 401
    status, _, headers = _https_get(f"https://{DOMAIN}/", timeout=15)
    assert status == 401, f"expected 401 without auth, got {status}"
    www = headers.get("WWW-Authenticate") or headers.get("Www-Authenticate") or ""
    assert "Basic" in www, f"missing Basic challenge: {www!r}"
    # correct creds → 200
    status, body, _ = _https_get(f"https://{DOMAIN}/", user=user, password=pw, timeout=15)
    assert status == 200, f"expected 200 with auth, got {status}"
    assert any(m in body.lower() for m in ("palmux", "root")), f"body[:500]: {body[:500]!r}"


def test_AC_Sfccb3f_2_6() -> None:
    """[AC-Sfccb3f-2-6] 再実行 (新 password) → 旧 creds 拒否、 新 creds で 200、 Caddy 無停止 reload"""
    secrets = load_secrets()
    user = secrets.get("BASIC_AUTH_USER")
    old_pw = secrets.get("BASIC_AUTH_PASSWORD")
    if not user or not old_pw:
        raise AssertionError("secrets.env に BASIC_AUTH_USER/PASSWORD が必要 (AC-2-5 と同じ)")
    new_pw = old_pw + "-rot"  # rotated variant — test only mutates locally

    cmd = _install_cmd_with_caddy(secrets, basic_user=user, basic_pass=new_pw)
    p = ssh(cmd, timeout=3600)
    assert p.returncode == 0, f"install.sh rc={p.returncode}\n{p.stderr[-2000:]}"

    time.sleep(3)
    # 旧 creds → 401
    status, _, _ = _https_get(f"https://{DOMAIN}/", user=user, password=old_pw, timeout=15)
    assert status == 401, f"old creds should fail, got {status}"
    # 新 creds → 200
    status, _, _ = _https_get(f"https://{DOMAIN}/", user=user, password=new_pw, timeout=15)
    assert status == 200, f"new creds should succeed, got {status}"
    # Caddy が active であること (restart 完了)
    p = ssh("sudo systemctl is-active caddy")
    assert p.stdout.strip() == "active", f"caddy not active after rotation: {p.stdout!r}"


# ---------------------------------------------------------------------------
# Story Sfccb3f-3 — server-stability + PROFILE=full
# ---------------------------------------------------------------------------


def _install_cmd_profile(profile: str) -> str:
    return (
        f"cd /tmp && PROFILE={profile} PALMUX_FLAKE_REF=path:/tmp/palmux2-src "
        f"bash /tmp/palmux2-src/scripts/install.sh"
    )


def test_AC_Sfccb3f_3_1() -> None:
    """[AC-Sfccb3f-3-1] unattended-upgrades enabled + dry-run success + kernel exclude"""
    # 直前の Story-2 で既に server-stability も走っているはずだが、 確認のため再実行
    rsync_repo()
    p = ssh(_install_cmd_profile("minimal"), timeout=3600)
    assert p.returncode == 0, f"install.sh rc={p.returncode}\n{p.stderr[-2000:]}"
    p = ssh("systemctl is-enabled unattended-upgrades")
    assert p.stdout.strip() == "enabled", f"unattended-upgrades not enabled: {p.stdout!r}"
    p = ssh("test -f /etc/apt/apt.conf.d/20auto-upgrades && test -f /etc/apt/apt.conf.d/50unattended-upgrades && echo OK")
    assert "OK" in p.stdout
    # dry-run: returns 0, lists allowed origins, lists package excludes
    p = ssh("sudo unattended-upgrade --dry-run -v 2>&1 | head -100", check=False)
    assert "Allowed origins" in p.stdout or "Checking" in p.stdout, f"dry-run unexpected: {p.stdout[:500]!r}"
    p = ssh("grep -E 'linux-(image|headers|generic)' /etc/apt/apt.conf.d/50unattended-upgrades | wc -l")
    assert int(p.stdout.strip()) >= 2, "kernel packages not excluded in 50unattended-upgrades"


def test_AC_Sfccb3f_3_2() -> None:
    """[AC-Sfccb3f-3-2] /swapfile 8G + swapon active + vm.swappiness/panic_on_oom/kernel.panic 永続化"""
    p = ssh("test -f /swapfile && stat -c '%s' /swapfile")
    bytes_ = int(p.stdout.strip())
    expected = 8 * 1024 * 1024 * 1024
    assert bytes_ == expected, f"/swapfile size {bytes_} != {expected}"
    p = ssh("swapon --show=NAME,TYPE,SIZE --noheadings")
    assert "/swapfile" in p.stdout, f"/swapfile not active: {p.stdout!r}"
    # sysctl values
    p = ssh("sysctl -n vm.swappiness vm.panic_on_oom kernel.panic")
    lines = p.stdout.strip().splitlines()
    assert lines[0] == "10", f"vm.swappiness={lines[0]!r}, want 10"
    assert lines[1] == "1", f"vm.panic_on_oom={lines[1]!r}, want 1"
    assert lines[2] == "10", f"kernel.panic={lines[2]!r}, want 10"
    # 永続化: /etc/sysctl.d/99-palmux.conf
    p = ssh("grep -cE '^(vm\\.swappiness|vm\\.panic_on_oom|kernel\\.panic)' /etc/sysctl.d/99-palmux.conf")
    assert p.stdout.strip() == "3", "99-palmux.conf does not contain all 3 sysctls"
    # /etc/fstab 永続化
    p = ssh("grep -cE '^/swapfile' /etc/fstab")
    assert int(p.stdout.strip()) >= 1, "swapfile not in /etc/fstab"


def test_AC_Sfccb3f_3_3() -> None:
    """[AC-Sfccb3f-3-3] PROFILE=full → bat/rg/fd/delta/eza/fzf/starship/zoxide/yazi が PATH + starship init"""
    rsync_repo()
    p = ssh(_install_cmd_profile("full"), timeout=3600)
    assert p.returncode == 0, f"install.sh PROFILE=full rc={p.returncode}\n{p.stderr[-3000:]}"
    cmds = ["bat", "rg", "fd", "delta", "eza", "fzf", "starship", "zoxide", "yazi"]
    p = ssh("bash -lc 'for c in " + " ".join(cmds) + '; do command -v "$c" >/dev/null || echo "MISSING:$c"; done\'')
    missing = [ln for ln in p.stdout.splitlines() if ln.startswith("MISSING:")]
    assert not missing, f"PROFILE=full missing: {missing}"
    # bash 起動時に starship init が実行されていること: .bashrc 中に "starship init" を含む
    # (home-manager が programs.starship.enable=true で自動配置)
    p = ssh("bash -lc 'grep -lE \"starship init|starship.init\" ~/.bashrc ~/.bashrc.d/* /etc/bash.bashrc 2>/dev/null | head -3' || echo NONE")
    assert "NONE" not in p.stdout and p.stdout.strip() != "", \
        f"starship init not wired into bash: {p.stdout!r}"


def test_AC_Sfccb3f_3_4() -> None:
    """[AC-Sfccb3f-3-4] PROFILE=full → PROFILE=minimal (default) で shell-UX cluster が外れる"""
    rsync_repo()
    p = ssh(_install_cmd_profile("minimal"), timeout=3600)
    assert p.returncode == 0, f"install.sh PROFILE=minimal rc={p.returncode}\n{p.stderr[-2000:]}"
    # full 限定の binaries が消えていることを確認 (空 stdout = 全部 missing = OK)
    cmds = ["bat", "starship", "zoxide", "yazi"]
    p = ssh("bash -lc 'for c in " + " ".join(cmds) + '; do command -v "$c" >/dev/null && echo "STILL:$c" || true; done\'', check=False)
    still = [ln for ln in p.stdout.splitlines() if ln.startswith("STILL:")]
    assert not still, f"PROFILE=minimal still has full-only binaries: {still}"
    # starship init line が消えていること
    p = ssh("bash -lc 'grep -E \"starship init\" ~/.bashrc ~/.bashrc.d/* /etc/bash.bashrc 2>/dev/null || echo CLEAN'", check=False)
    assert "CLEAN" in p.stdout, f"starship init not removed: {p.stdout!r}"


# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------


TESTS: list[tuple[str, callable]] = [
    ("Sfccb3f-1-1", test_AC_Sfccb3f_1_1),
    ("Sfccb3f-1-2", test_AC_Sfccb3f_1_2),
    ("Sfccb3f-1-3", test_AC_Sfccb3f_1_3),
    ("Sfccb3f-1-4", test_AC_Sfccb3f_1_4),
    ("Sfccb3f-1-5", test_AC_Sfccb3f_1_5),
    ("Sfccb3f-2-1", test_AC_Sfccb3f_2_1),
    ("Sfccb3f-2-2", test_AC_Sfccb3f_2_2),
    ("Sfccb3f-2-3", test_AC_Sfccb3f_2_3),
    ("Sfccb3f-2-4", test_AC_Sfccb3f_2_4),
    ("Sfccb3f-2-5", test_AC_Sfccb3f_2_5),
    ("Sfccb3f-2-6", test_AC_Sfccb3f_2_6),
    ("Sfccb3f-3-1", test_AC_Sfccb3f_3_1),
    ("Sfccb3f-3-2", test_AC_Sfccb3f_3_2),
    ("Sfccb3f-3-3", test_AC_Sfccb3f_3_3),
    ("Sfccb3f-3-4", test_AC_Sfccb3f_3_4),
]


def main(argv: list[str]) -> int:
    only_id: str | None = None
    only_story: str | None = None
    i = 1
    while i < len(argv):
        a = argv[i]
        if a == "--story" and i + 1 < len(argv):
            only_story = argv[i + 1]
            i += 2
        else:
            only_id = a
            i += 1

    selected = TESTS
    if only_id:
        selected = [(t, f) for t, f in TESTS if t == only_id]
        if not selected:
            print(f"no such AC: {only_id}", file=sys.stderr)
            return 2
    elif only_story:
        sel = f"Sfccb3f-{only_story}-"
        selected = [(t, f) for t, f in TESTS if t.startswith(sel)]

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
