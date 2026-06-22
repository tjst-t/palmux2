#!/usr/bin/env bash
# check-no-baked-keys.sh — enforce PalmuxOS's "no baked SSH keys" image invariant.
#
# A DISTRIBUTED appliance image MUST ship with ZERO baked SSH authorized_keys /
# passwords (baking the author's pubkey would be a backdoor into every deployed
# PalmuxOS — see docs/nixos-appliance-design.md §アクセスと鍵). The design
# deliberately enforces this as a BUILD-TIME grep of the shipped artifact, NOT an
# eval assertion: an assertion cannot tell the shipped-image build apart from an
# operator's own rebuild (which legitimately DOES add their key), so it would
# wrongly break valid operator customization.
#
# This script greps a BUILT NixOS system toplevel (the closure the image is made
# from) for any baked SSH public key. Run it in CI after building the appliance:
#
#   sys=$(nix build --no-link --print-out-paths \
#           .#appliance-qcow2.config.system.build.toplevel)
#   scripts/check-no-baked-keys.sh "$sys"
#
# Exit 0 = clean (no baked keys). Exit 1 = a key was baked → fail the build.
set -euo pipefail

sys="${1:?usage: check-no-baked-keys.sh <nixos-system-toplevel-store-path>}"

if [ ! -e "$sys/etc" ]; then
  echo "check-no-baked-keys: '$sys' has no etc/ — not a NixOS system toplevel?" >&2
  exit 2
fi

fail=0

# 1) Static per-user authorized_keys rendered by services.openssh
#    (users.users.<u>.openssh.authorizedKeys.keys → /etc/ssh/authorized_keys.d/<u>).
akd="$sys/etc/ssh/authorized_keys.d"
if [ -d "$akd" ]; then
  for f in "$akd"/*; do
    [ -e "$f" ] || continue
    if [ -s "$f" ]; then
      echo "check-no-baked-keys: BAKED KEY in authorized_keys.d/$(basename "$f"):" >&2
      sed 's/^/    /' "$f" >&2
      fail=1
    fi
  done
fi

# 2) Any SSH public key material anywhere in the shipped /etc (covers
#    authorizedKeysFiles pointing elsewhere, environment.etc drop-ins, etc).
if hits=$(grep -rIl -e 'ssh-ed25519 ' -e 'ssh-rsa ' -e 'ecdsa-sha2-' "$sys/etc/" 2>/dev/null); then
  if [ -n "$hits" ]; then
    echo "check-no-baked-keys: SSH public key material found under shipped /etc:" >&2
    echo "$hits" | sed 's/^/    /' >&2
    fail=1
  fi
fi

if [ "$fail" -ne 0 ]; then
  echo "check-no-baked-keys: FAIL — the distributed image must ship with zero baked SSH keys." >&2
  exit 1
fi

echo "check-no-baked-keys: OK — no baked SSH keys in $sys"
