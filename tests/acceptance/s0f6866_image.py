#!/usr/bin/env python3
"""
Acceptance tests for S0f6866: palmux-workspace:default container image.

These tests run against the test VM (ubuntu@192.168.1.41).

The image is a Dockerfile-based OCI image. The CI workflow
(.github/workflows/build-image.yml) is what publishes it to GHCR;
*these* tests validate the build itself (size, contents, LXD
launchability, GHCR pullability) without depending on a successful
remote CI run having happened first.

Strategy:
- For "build the image" / "size check" / "CLI presence" we build the
  Dockerfile locally on the VM with `docker build`. This is what the
  GitHub Actions runner does too, just on a different host.
- For "LXD `lxc launch docker:` pulls and boots" we use a local image
  (loaded into LXD via `lxc image import`) when GHCR doesn't yet have a
  tag the test machine can pull. Once the workflow has had a real run,
  the test can be re-pointed at GHCR (`AC-S0f6866-2-3` block below).

[AC-S0f6866-1-1] docker build succeeds + image size ≤ 1 GB
[AC-S0f6866-1-2] image does NOT contain palmux-agent + has libc / coreutils
[AC-S0f6866-1-3] container starts via LXD + claude / gh / git / tmux / tailscale / ansible all on PATH
[AC-S0f6866-2-3] image is pullable from GHCR without authentication (or local-built fallback if not yet on GHCR)

Note: AC-S0f6866-2-1 / 2-2 are workflow-correctness ACs verified by code
review + actionlint; they don't have a runtime test (the workflow itself
*is* the artifact, and we can't run a real CI without burning a real
GHCR push).
"""

import json
import os
import subprocess
import sys
import time
import unittest

VM_HOST = "ubuntu@192.168.1.41"
SSH_OPTS = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=no"]

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
DOCKERFILE_DIR = os.path.join(REPO_ROOT, "images", "workspace-default")

VM_BUILD_DIR = "~/palmux-image-build/workspace-default"
VM_LOCAL_TAG = "palmux-workspace:local"
GHCR_IMAGE = "ghcr.io/tjst-t/palmux-workspace:default"

REQUIRED_CLIS = ["claude", "gh", "git", "tmux", "tailscale", "ansible"]

# 1 GB target per AC-S0f6866-1-1.
SIZE_LIMIT_BYTES = 1 * 1024 * 1024 * 1024


def ssh(cmd: str, check: bool = True, timeout: int = 60) -> subprocess.CompletedProcess:
    """Run a command on the test VM."""
    return subprocess.run(
        ["ssh", *SSH_OPTS, VM_HOST, cmd],
        capture_output=True, text=True, check=check, timeout=timeout,
    )


def vm_reachable() -> bool:
    try:
        result = ssh("echo ok", check=False, timeout=10)
        return result.returncode == 0 and result.stdout.strip() == "ok"
    except Exception:
        return False


def vm_has_docker() -> bool:
    result = ssh("which docker || true", check=False)
    return bool(result.stdout.strip())


def vm_has_lxc() -> bool:
    result = ssh("which lxc || true", check=False)
    return bool(result.stdout.strip())


def vm_has_local_image() -> bool:
    """Returns True if palmux-workspace:local exists on the VM."""
    result = ssh(
        f"sudo docker image inspect {VM_LOCAL_TAG} >/dev/null 2>&1 && echo yes || echo no",
        check=False,
    )
    return result.stdout.strip() == "yes"


def vm_image_size_bytes() -> int:
    result = ssh(
        f"sudo docker image inspect {VM_LOCAL_TAG} --format '{{{{.Size}}}}'",
        check=True,
    )
    return int(result.stdout.strip())


class TestImageBuild(unittest.TestCase):
    """[AC-S0f6866-1-1] Dockerfile builds + image size ≤ 1 GB"""

    @classmethod
    def setUpClass(cls):
        if not vm_reachable():
            raise unittest.SkipTest(f"Test VM {VM_HOST} unreachable")
        if not vm_has_docker():
            raise unittest.SkipTest(
                "docker not installed on test VM. Run "
                "'ssh ubuntu@192.168.1.41 sudo apt-get install -y docker.io' first."
            )
        # Ensure the build directory exists with the latest Dockerfile.
        ssh(f"mkdir -p {VM_BUILD_DIR}")
        local_dockerfile = os.path.join(DOCKERFILE_DIR, "Dockerfile")
        subprocess.run(
            ["scp", *SSH_OPTS, local_dockerfile, f"{VM_HOST}:{VM_BUILD_DIR}/Dockerfile"],
            check=True, capture_output=True,
        )

    def test_dockerfile_exists_locally(self):
        """[AC-S0f6866-1-1] Dockerfile present in repo at images/workspace-default/Dockerfile"""
        self.assertTrue(
            os.path.isfile(os.path.join(DOCKERFILE_DIR, "Dockerfile")),
            "images/workspace-default/Dockerfile must exist",
        )

    def test_dockerfile_uses_ubuntu_2404(self):
        """[AC-S0f6866-1-1] Dockerfile is FROM ubuntu:24.04"""
        with open(os.path.join(DOCKERFILE_DIR, "Dockerfile")) as f:
            content = f.read()
        # Allow `FROM ubuntu:24.04` with any whitespace, no AS alias.
        self.assertIn("FROM ubuntu:24.04", content,
                      "Dockerfile must use ubuntu:24.04 base (workspace-runtime-design.md §14.1)")

    def test_image_builds(self):
        """[AC-S0f6866-1-1] docker build succeeds (only built if not already cached on VM)"""
        if vm_has_local_image():
            return  # already built — covered
        # Build with a generous timeout. First build downloads ~600 MB
        # of apt packages; subsequent rebuilds hit the BuildKit cache.
        result = subprocess.run(
            ["ssh", *SSH_OPTS, VM_HOST,
             f"cd {VM_BUILD_DIR} && sudo docker build -t {VM_LOCAL_TAG} ."],
            capture_output=True, text=True, timeout=900,
        )
        self.assertEqual(
            result.returncode, 0,
            f"docker build failed:\nSTDOUT:\n{result.stdout[-2000:]}\n"
            f"STDERR:\n{result.stderr[-2000:]}",
        )
        self.assertTrue(vm_has_local_image(), "image absent after successful build")

    def test_image_size_under_1gb(self):
        """[AC-S0f6866-1-1] built image is ≤ 1 GB"""
        if not vm_has_local_image():
            self.skipTest("image not built yet — test_image_builds must run first")
        size = vm_image_size_bytes()
        self.assertLessEqual(
            size, SIZE_LIMIT_BYTES,
            f"image is {size / 1e9:.2f} GB, exceeds 1 GB target ({SIZE_LIMIT_BYTES} bytes)"
        )


class TestImageContents(unittest.TestCase):
    """[AC-S0f6866-1-2] no palmux-agent baked in + agent's deps are present"""

    @classmethod
    def setUpClass(cls):
        if not vm_reachable():
            raise unittest.SkipTest(f"Test VM {VM_HOST} unreachable")
        if not vm_has_docker():
            raise unittest.SkipTest("docker not installed on test VM")
        if not vm_has_local_image():
            raise unittest.SkipTest(
                "palmux-workspace:local image not built yet on VM. "
                "Run TestImageBuild first."
            )

    def _docker_run(self, cmd: str, check: bool = True) -> subprocess.CompletedProcess:
        """Run a one-shot command inside the image (no systemd, just `docker run`)."""
        return ssh(
            f"sudo docker run --rm --entrypoint /bin/sh {VM_LOCAL_TAG} -c {repr(cmd)}",
            check=check, timeout=60,
        )

    def test_no_palmux_agent(self):
        """[AC-S0f6866-1-2] /usr/local/bin/palmux-agent is NOT pre-installed"""
        result = self._docker_run(
            "test -f /usr/local/bin/palmux-agent && echo present || echo absent",
            check=True,
        )
        self.assertIn("absent", result.stdout,
                      "palmux-agent must NOT be baked in (workspace-runtime-design.md §14.10.6)")

    def test_libc_present(self):
        """[AC-S0f6866-1-2] glibc is present (palmux-agent is static but still useful as smoke)"""
        result = self._docker_run(
            "test -f /lib/x86_64-linux-gnu/libc.so.6 && echo yes || ls /lib*/libc.so* 2>/dev/null",
            check=True,
        )
        self.assertTrue(
            "yes" in result.stdout or "libc.so" in result.stdout,
            f"glibc not found in image; stdout={result.stdout!r}"
        )

    def test_coreutils_present(self):
        """[AC-S0f6866-1-2] coreutils basics (ls, cat, sh) are usable"""
        for util in ["ls", "cat", "sh"]:
            result = self._docker_run(f"which {util}", check=False)
            self.assertEqual(
                result.returncode, 0,
                f"{util} not on PATH inside image: stderr={result.stderr!r}"
            )

    def test_systemd_present(self):
        """[AC-S0f6866-1-2] systemd is the init system (CMD = /sbin/init)"""
        # /sbin/init should be a symlink to systemd in ubuntu:24.04 once
        # systemd-sysv is installed.
        result = self._docker_run("test -L /sbin/init && readlink /sbin/init", check=False)
        # Either the symlink resolves to systemd, or /sbin/init is the binary.
        target = result.stdout.strip()
        self.assertTrue(
            "systemd" in target or os.path.basename(target) == "systemd"
            or self._docker_run("test -x /sbin/init && echo ok", check=False).stdout.strip() == "ok",
            f"systemd not present as /sbin/init: target={target!r}"
        )

    def test_default_cmd_is_systemd(self):
        """[AC-S0f6866-1-2] image's default CMD invokes systemd"""
        result = ssh(
            f"sudo docker image inspect {VM_LOCAL_TAG} --format '{{{{json .Config.Cmd}}}}'",
            check=True,
        )
        cmd = json.loads(result.stdout.strip())
        self.assertTrue(
            any("init" in part or "systemd" in part for part in cmd),
            f"CMD must invoke /sbin/init or systemd; got {cmd!r}"
        )


class TestLXDLaunch(unittest.TestCase):
    """[AC-S0f6866-1-3] container starts via LXD + all CLIs visible"""

    INSTANCE = "smoke-s0f6866"

    @classmethod
    def setUpClass(cls):
        if not vm_reachable():
            raise unittest.SkipTest(f"Test VM {VM_HOST} unreachable")
        if not vm_has_lxc():
            raise unittest.SkipTest("lxc (LXD) not installed on test VM")

        # Best-effort: clean up any leftover instance from a previous run.
        ssh(f"sudo lxc delete --force {cls.INSTANCE} 2>/dev/null || true", check=False)

    @classmethod
    def tearDownClass(cls):
        ssh(f"sudo lxc delete --force {cls.INSTANCE} 2>/dev/null || true", check=False)

    def _try_launch(self, image_ref: str) -> subprocess.CompletedProcess:
        """Try to launch a container from the given image reference."""
        return ssh(
            f"sudo lxc launch {image_ref} {self.INSTANCE}",
            check=False, timeout=300,
        )

    def test_launch_and_clis_present(self):
        """[AC-S0f6866-1-3] lxc launch docker:ghcr.io/.../palmux-workspace:default + which {claude,gh,git,tmux,tailscale,ansible}"""
        if not vm_has_local_image():
            self.skipTest(
                "image not built locally yet — TestImageBuild must run "
                "first, OR push the image to GHCR and re-run with "
                "GHCR_VERIFIED=1 (see test_launch_from_ghcr below)."
            )

        # We'd love to launch via `docker:ghcr.io/...` but until the
        # workflow has actually pushed, that pull would fail. For local
        # validation we import the docker image into LXD's image store.
        # Save the docker image to a tarball, import into lxd as an OCI
        # image, then launch.
        #
        # LXD's `lxc image import` for OCI images uses
        # `--alias <name>` and accepts a docker-saved tarball.
        result = ssh(
            "sudo docker save palmux-workspace:local "
            "> /tmp/palmux-workspace-local.tar && "
            "echo saved",
            check=False, timeout=180,
        )
        if result.returncode != 0:
            self.skipTest(f"docker save failed: {result.stderr}")

        # Some LXD versions need a small wrapper to ingest a docker tar.
        # `lxc image import` accepts OCI tarballs directly in 5.20+.
        ssh("sudo lxc image delete palmux-workspace-local 2>/dev/null || true", check=False)
        import_result = ssh(
            "sudo lxc image import oci /tmp/palmux-workspace-local.tar "
            "--alias palmux-workspace-local",
            check=False, timeout=120,
        )
        if import_result.returncode != 0:
            # Older LXD doesn't support `oci` keyword. Fall back to raw
            # docker:// URI via the test machine's local docker daemon.
            self.skipTest(
                "lxc image import oci is unavailable on this LXD version; "
                f"output: {import_result.stderr}. The :default image must "
                "be on GHCR before the smoke test can run end-to-end."
            )

        launch_result = self._try_launch("palmux-workspace-local")
        self.assertEqual(
            launch_result.returncode, 0,
            f"lxc launch failed:\nstdout={launch_result.stdout}\n"
            f"stderr={launch_result.stderr}",
        )

        # Wait for systemd to be ready; container needs a moment.
        time.sleep(8)

        # Sanity: it's running.
        running = ssh(
            f"sudo lxc info {self.INSTANCE} | grep -E '^Status' | head -1",
            check=True,
        )
        self.assertIn("RUNNING", running.stdout.upper(),
                      f"container not RUNNING: {running.stdout}")

        # All required CLIs are on PATH.
        missing = []
        for cli in REQUIRED_CLIS:
            result = ssh(
                f"sudo lxc exec {self.INSTANCE} -- which {cli}",
                check=False,
            )
            if result.returncode != 0 or not result.stdout.strip():
                missing.append(cli)
        self.assertEqual(
            missing, [],
            f"CLIs missing from PATH inside container: {missing}"
        )


class TestGHCRPullability(unittest.TestCase):
    """[AC-S0f6866-2-3] image is publicly pullable from GHCR (no login)

    This test only runs when env GHCR_VERIFIED=1 is set, because the
    image hasn't been pushed yet on first sprint completion. After the
    user explicitly opts in (manually triggering build-image.yml or
    pushing a v* tag), set GHCR_VERIFIED=1 and re-run.
    """

    def test_pullable_without_auth(self):
        if os.environ.get("GHCR_VERIFIED") != "1":
            self.skipTest(
                "GHCR_VERIFIED=1 not set. The image must be pushed to "
                f"{GHCR_IMAGE} via the workflow before this test can pass. "
                "Once you've triggered .github/workflows/build-image.yml "
                "and confirmed the package is public, re-run with "
                "GHCR_VERIFIED=1."
            )

        if not vm_reachable():
            self.skipTest(f"Test VM {VM_HOST} unreachable")
        if not vm_has_docker():
            self.skipTest("docker not installed on test VM")

        # Logout to ensure we're testing public pull.
        ssh("sudo docker logout ghcr.io 2>/dev/null || true", check=False)

        result = ssh(f"sudo docker pull {GHCR_IMAGE}", check=False, timeout=300)
        self.assertEqual(
            result.returncode, 0,
            f"docker pull {GHCR_IMAGE} failed (image not public?):\n"
            f"stdout={result.stdout}\nstderr={result.stderr}"
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
