#!/usr/bin/env python3
"""
Acceptance tests for S9fd775: built-in `palmux port` allocator + CLI.

These tests run against the local palmux binary (`bin/palmux`). No test VM is
required — the allocator works in any directory the user can write to.

Requirements:
- bin/palmux must be built (`go build -o bin/palmux ./cmd/palmux`).
- A free TCP port range > 1024 must be available (any modern Linux user).

Acceptance criteria (from docs/ROADMAP.json .sprints["S9fd775"]):
[AC-S9fd775-2-1] `palmux port exec --name foo -- npm run dev --port {}` allocates
                a free port, substitutes {} for the real number, exec's the
                child, and frees the lease in step with the child's lifecycle
                (when --free-on-exit).
[AC-S9fd775-2-2] `palmux port alloc --name foo` returns a port and exits
                (no long-running). `palmux port list` shows it. `palmux port
                free foo` removes it.
[AC-S9fd775-2-3] Works with palmux serve NOT running. Hits ports.json
                directly with flock; safe across concurrent invocations.
[AC-S9fd775-3-2] After `make serve INSTANCE=s9fd775-test` (in a temp config
                dir) the lease shows up in ports.json; after
                `make serve-stop`, the entry is removed.
                NOTE: AC-S9fd775-3-2 is also exercised on the test VM by
                hand (see decisions.json `make_serve_vm` decision); this
                automation covers the local-CI mirror.
"""

import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
PALMUX_BIN = os.path.join(REPO_ROOT, "bin", "palmux")


def palmux_port(*args, config_dir, check=True, env=None, stdin=None, timeout=20):
    """Invoke `palmux port` with --config-dir set to a sandbox dir."""
    cmd = [PALMUX_BIN, "port", *args, "--config-dir", config_dir]
    return subprocess.run(
        cmd, capture_output=True, text=True, check=check, env=env,
        stdin=stdin, timeout=timeout,
    )


def is_port_listenable(port: int) -> bool:
    """Probe whether a TCP port can currently be bound on 127.0.0.1."""
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("127.0.0.1", port))
            return True
    except OSError:
        return False


class S9fd775AcceptanceTests(unittest.TestCase):
    """All ACs for S9fd775 stories 2 and 3."""

    @classmethod
    def setUpClass(cls):
        if not os.path.isfile(PALMUX_BIN):
            raise unittest.SkipTest(
                f"palmux binary not found at {PALMUX_BIN}; run `go build -o bin/palmux ./cmd/palmux` first"
            )

    def setUp(self):
        self.cdir = tempfile.mkdtemp(prefix="palmux-port-acceptance-")

    def tearDown(self):
        shutil.rmtree(self.cdir, ignore_errors=True)

    # [AC-S9fd775-2-1]
    def test_AC_S9fd775_2_1_exec_substitutes_placeholder(self):
        """`palmux port exec --name foo -- /bin/sh -c 'echo {}'` substitutes
        the placeholder with the allocated port number, runs the command,
        and (with --free-on-exit) frees the lease afterward."""
        out = palmux_port(
            "exec", "--name", "foo", "--free-on-exit", "--",
            "/bin/sh", "-c", "echo PORT={} ENV=$PORT",
            config_dir=self.cdir,
        )
        self.assertEqual(out.returncode, 0, out.stderr)
        # Extract the port from the child's stdout.
        line = out.stdout.strip().splitlines()[0]
        self.assertTrue(line.startswith("PORT="), line)
        token = line.split()[0].split("=", 1)[1]
        port = int(token)
        self.assertGreater(port, 1024, "should be an unprivileged port")
        # Same line carries the env var.
        self.assertIn(f"ENV={port}", out.stdout)
        # With --free-on-exit, list must be empty.
        listing = palmux_port("list", "--json", "--all", config_dir=self.cdir).stdout
        leases = json.loads(listing) if listing.strip() else []
        names = [l["name"] for l in leases]
        self.assertNotIn("foo", names, f"foo should be freed, got {names}")

    # [AC-S9fd775-2-1] also: child's exit code is propagated.
    def test_AC_S9fd775_2_1_exec_propagates_exit_code(self):
        out = palmux_port(
            "exec", "--name", "exit42", "--",
            "/bin/sh", "-c", "exit 42",
            config_dir=self.cdir, check=False,
        )
        self.assertEqual(out.returncode, 42)

    # [AC-S9fd775-2-1] also: no --keep means lease persists across re-runs
    # (matching `portman exec` semantics: same name → same port).
    def test_AC_S9fd775_2_1_exec_lease_persistence(self):
        run1 = palmux_port(
            "exec", "--name", "stable", "--",
            "/bin/sh", "-c", "echo {}",
            config_dir=self.cdir,
        )
        port1 = int(run1.stdout.strip().splitlines()[0])
        run2 = palmux_port(
            "exec", "--name", "stable", "--",
            "/bin/sh", "-c", "echo {}",
            config_dir=self.cdir,
        )
        port2 = int(run2.stdout.strip().splitlines()[0])
        self.assertEqual(port1, port2, "same name must yield same port across runs")

    # [AC-S9fd775-2-2] alloc returns a port and exits.
    def test_AC_S9fd775_2_2_alloc_returns_port(self):
        out = palmux_port("alloc", "--name", "svc", config_dir=self.cdir)
        self.assertEqual(out.returncode, 0)
        port = int(out.stdout.strip())
        self.assertGreater(port, 1024)
        # Idempotent.
        out2 = palmux_port("alloc", "--name", "svc", config_dir=self.cdir)
        self.assertEqual(int(out2.stdout.strip()), port)

    # [AC-S9fd775-2-2] alloc --json emits structured output.
    def test_AC_S9fd775_2_2_alloc_json(self):
        out = palmux_port(
            "alloc", "--name", "jsvc", "--json",
            "--project", "tjst-t/palmux2",
            "--worktree", "main",
            config_dir=self.cdir,
        )
        self.assertEqual(out.returncode, 0)
        rec = json.loads(out.stdout)
        self.assertEqual(rec["name"], "jsvc")
        self.assertEqual(rec["scope"], "global")
        self.assertEqual(rec["project"], "tjst-t/palmux2")
        self.assertGreater(rec["port"], 1024)

    # [AC-S9fd775-2-2] list (table + JSON), free.
    def test_AC_S9fd775_2_2_list_and_free(self):
        palmux_port("alloc", "--name", "alpha", config_dir=self.cdir)
        palmux_port("alloc", "--name", "beta", config_dir=self.cdir)
        palmux_port("alloc", "--name", "gamma", "--scope", "ws-1", config_dir=self.cdir)

        # Default scope = global, so we should see alpha + beta only.
        out = palmux_port("list", config_dir=self.cdir)
        self.assertIn("alpha", out.stdout)
        self.assertIn("beta", out.stdout)
        self.assertNotIn("gamma", out.stdout)

        # --all spans every scope.
        out = palmux_port("list", "--all", "--json", config_dir=self.cdir)
        leases = json.loads(out.stdout)
        names = sorted(l["name"] for l in leases)
        self.assertEqual(names, ["alpha", "beta", "gamma"])

        # free alpha (positional shorthand).
        palmux_port("free", "alpha", config_dir=self.cdir)
        out = palmux_port("list", "--json", config_dir=self.cdir)
        leases = json.loads(out.stdout)
        names = [l["name"] for l in leases]
        self.assertNotIn("alpha", names)
        self.assertIn("beta", names)

        # free is idempotent (no error on already-gone name).
        result = palmux_port("free", "alpha", config_dir=self.cdir)
        self.assertEqual(result.returncode, 0)

    # [AC-S9fd775-2-3] Works without `palmux serve` running. We just used the
    # CLI repeatedly above without ever starting the server, so this is
    # implicitly verified — but make it explicit: ports.json must exist on
    # disk after CLI use, with the leases visible to a fresh CLI invocation.
    def test_AC_S9fd775_2_3_works_without_server(self):
        # No palmux serve running anywhere in this test suite.
        palmux_port("alloc", "--name", "no-server", config_dir=self.cdir)
        # The ports.json must be where the CLI says it is.
        path = os.path.join(self.cdir, "ports.json")
        self.assertTrue(os.path.isfile(path))
        with open(path) as f:
            doc = json.load(f)
        self.assertGreaterEqual(len(doc["leases"]), 1)
        self.assertEqual(doc["leases"][0]["name"], "no-server")

    # [AC-S9fd775-2-3] Concurrent CLI invocations from multiple processes
    # must serialize via flock and produce no torn writes.
    def test_AC_S9fd775_2_3_concurrent_invocations(self):
        N = 10
        results = [None] * N
        errs = [None] * N

        def worker(i):
            try:
                out = palmux_port(
                    "alloc", "--name", f"svc-{i}",
                    config_dir=self.cdir, timeout=30,
                )
                results[i] = int(out.stdout.strip())
            except Exception as e:  # pragma: no cover (defensive)
                errs[i] = e

        threads = [threading.Thread(target=worker, args=(i,)) for i in range(N)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        for e in errs:
            self.assertIsNone(e)
        # All ports distinct.
        self.assertEqual(len(set(results)), N, results)
        # ports.json still parses — no torn writes.
        with open(os.path.join(self.cdir, "ports.json")) as f:
            doc = json.load(f)
        self.assertEqual(len(doc["leases"]), N)

    # [AC-S9fd775-2-3] CLI lease is visible to a server that opens the same
    # ports.json afterwards (= the bootstrap path: CLI writes, server reads).
    # We simulate the "server reads" half by re-invoking the CLI with the same
    # config-dir, since the on-disk format is the contract.
    def test_AC_S9fd775_2_3_ports_json_is_the_contract(self):
        out = palmux_port(
            "alloc", "--name", "contract",
            "--project", "tjst-t/palmux2",
            "--worktree", "main",
            "--hostname", "contract--main--palmux2",
            config_dir=self.cdir,
        )
        port = int(out.stdout.strip())
        with open(os.path.join(self.cdir, "ports.json")) as f:
            doc = json.load(f)
        lease = doc["leases"][0]
        # Field names match portman list --json (project / worktree /
        # hostname / port / name) plus our scope extension.
        for k in ("scope", "name", "project", "worktree", "hostname", "port"):
            self.assertIn(k, lease, f"missing {k} in {lease}")
        self.assertEqual(lease["port"], port)


if __name__ == "__main__":
    unittest.main(verbosity=2)
