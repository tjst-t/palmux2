# palmux-workspace:default — base container image for palmux Workspace runtime
#
# Spec references:
#   - workspace-runtime-design.md §14.1  (custom image, monthly maintenance budget)
#   - workspace-runtime-design.md §14.7  (Ubuntu only, macOS unsupported)
#   - workspace-runtime-design.md §14.10.1 (GHCR distribution, LXD `docker:` launch)
#   - workspace-runtime-design.md §14.10.2 (build pipeline lives in this repo)
#   - workspace-runtime-design.md §14.10.4 (UID 1000 ubuntu user, idmap 1:1)
#   - workspace-runtime-design.md §14.10.6 (DO NOT bake palmux-agent — push at runtime)
#
# Target size: ≤ 1 GB
# Build: docker build -t ghcr.io/tjst-t/palmux-workspace:default images/workspace-default
# Launch via LXD: lxc launch docker:ghcr.io/tjst-t/palmux-workspace:default <name>

FROM ubuntu:24.04

# noninteractive everything that runs during build
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# ---------------------------------------------------------------------------
# Layer 1: base utilities + systemd + apt prereqs for the third-party repos
# we add below. Kept as one RUN to keep the layer small once we clean up.
# ---------------------------------------------------------------------------
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        gnupg \
        lsb-release \
        sudo \
        locales \
        tzdata \
        systemd \
        systemd-sysv \
        dbus \
        iproute2 \
        iputils-ping \
        less \
        unzip \
        xz-utils \
        bzip2 \
        zip \
        nano \
        vim-tiny \
        openssh-client \
        rsync \
        jq; \
    locale-gen en_US.UTF-8; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# Layer 2: dev tooling installable straight from Ubuntu archives
# (git / tmux / ansible-core / make).
#
# Trimmed for size (was 1.04 GB → ~430 MB):
#   - `ansible-core` (≈90 MB) instead of full `ansible` (≈350 MB) — devs can
#     `pip install` extra collections when needed
#   - dropped `ansible-lint` (≈200 MB) — lints belong on the developer host
#     CI, not in the runtime image
#   - dropped `build-essential` (≈350 MB) and `python3-pip` / `python3-venv`
#     (≈80 MB) — devs install these on demand inside the container if a
#     project needs to compile native extensions; default image stays runtime-
#     focused
# ---------------------------------------------------------------------------
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        git \
        tmux \
        ansible-core \
        python3 \
        make; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# Layer 3: third-party CLIs that need their own apt repos.
#
# - GitHub CLI (gh) — official cli.github.com debian repo (apt is reproducible)
# - Tailscale       — official pkgs.tailscale.com repo (apt is reproducible)
#
# claude CLI is installed via its own `curl | install` script in Layer 4
# because it does not have a stable apt repo. That's documented for users
# who want to override.
# ---------------------------------------------------------------------------
RUN set -eux; \
    install -d -m 0755 /etc/apt/keyrings; \
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        -o /etc/apt/keyrings/githubcli-archive-keyring.gpg; \
    chmod a+r /etc/apt/keyrings/githubcli-archive-keyring.gpg; \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list; \
    curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg \
        -o /usr/share/keyrings/tailscale-archive-keyring.gpg; \
    curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list \
        -o /etc/apt/sources.list.d/tailscale.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        gh \
        tailscale; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------------------------
# Layer 4: claude CLI.
#
# Claude Code ships an official install script at
#   https://claude.ai/install.sh (claude.com/install.sh)
# It installs to /usr/local/bin/claude (system) when run as root and to
# ~/.local/bin/claude when run unprivileged. We install system-wide so all
# users in the container (the ubuntu user, plus root) see the same binary.
#
# If the install script is unreachable at build time the build fails fast
# (set -e). We do NOT pin a version because Anthropic ships fast and the
# weekly cron rebuild is the version bump mechanism.
# ---------------------------------------------------------------------------
RUN set -eux; \
    curl -fsSL https://claude.ai/install.sh -o /tmp/claude-install.sh; \
    chmod +x /tmp/claude-install.sh; \
    /tmp/claude-install.sh || ( \
        echo "claude install script failed — see https://docs.claude.com/en/docs/claude-code/setup for manual install"; \
        exit 1; \
    ); \
    rm -f /tmp/claude-install.sh; \
    if [ -x /root/.local/bin/claude ]; then \
        install -m 0755 /root/.local/bin/claude /usr/local/bin/claude; \
    fi; \
    /usr/local/bin/claude --version || claude --version

# ---------------------------------------------------------------------------
# Layer 5: pre-create the ubuntu user (UID 1000, GID 1000) so palmux's
# idmap "both 1000 1000" maps cleanly to a real user from the moment the
# container starts. The official ubuntu:24.04 image has a `ubuntu` user
# at UID 1000 already, but it is cloud-init-managed in the cloud-image
# variant — pre-creating here makes the contract explicit.
#
# The user gets passwordless sudo so palmux-agent operations and ad-hoc
# `lxc exec <inst> -- bash` work without prompts.
# ---------------------------------------------------------------------------
RUN set -eux; \
    if ! id -u ubuntu >/dev/null 2>&1; then \
        groupadd -g 1000 ubuntu; \
        useradd -u 1000 -g 1000 -m -s /bin/bash ubuntu; \
    fi; \
    install -d -m 0755 /etc/sudoers.d; \
    echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-ubuntu; \
    chmod 0440 /etc/sudoers.d/90-ubuntu; \
    install -d -m 0755 -o ubuntu -g ubuntu /home/ubuntu/.config; \
    install -d -m 0755 -o ubuntu -g ubuntu /home/ubuntu/.local/bin

# ---------------------------------------------------------------------------
# Layer 6: container-friendly systemd tweaks.
#
# When LXD launches an OCI image with systemd as PID 1, a handful of
# units fail noisily because they expect physical hardware. We mask the
# obvious offenders so the journal is clean. None of these are needed in
# a Workspace runtime.
# ---------------------------------------------------------------------------
RUN set -eux; \
    systemctl mask \
        systemd-udev-trigger.service \
        systemd-modules-load.service \
        systemd-binfmt.service \
        sys-kernel-debug.mount \
        sys-kernel-tracing.mount \
        kmod-static-nodes.service \
    || true

# ---------------------------------------------------------------------------
# Image metadata (OCI labels — readable via `lxc image info` / `docker
# inspect`). The version label is rewritten by the CI workflow so each
# build is traceable to a git ref.
# ---------------------------------------------------------------------------
LABEL org.opencontainers.image.title="palmux-workspace" \
      org.opencontainers.image.description="Base container image for palmux Workspace runtime (claude, gh, git, tmux, tailscale, ansible pre-installed)" \
      org.opencontainers.image.source="https://github.com/tjst-t/palmux2" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="palmux" \
      org.opencontainers.image.version="default"

# Default CMD = systemd, so `lxc launch docker:...` boots a real init
# system (matching the LXD container runtime contract).
CMD ["/sbin/init"]
