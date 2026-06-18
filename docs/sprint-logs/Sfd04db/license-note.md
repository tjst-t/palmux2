# Sfd04db — Browser license note (ungoogled-chromium)

## Why the swap (problem)

The `palmux-ws` image previously bundled **Google Chrome** (`google-chrome-stable`
.deb from `dl.google.com`). The built image is published as a **public GitHub
Release asset**, i.e. we redistribute the bundled browser. Google Chrome is
proprietary and its Terms of Service restrict redistribution of the binary, so
shipping it inside a publicly downloadable image is a legal risk. Additionally,
Chrome shows a "Sign in to Chrome" promo on every launch.

## What we ship now

**ungoogled-chromium** `149.0.7827.114-1.1xtradeb1.2404.1` (noble / Ubuntu 24.04,
amd64).

### License (redistributable)

- ungoogled-chromium is a set of patches on top of **Chromium**, the open-source
  base of Chrome. Chromium itself is licensed under **BSD-3-Clause** (with
  bundled third-party components under their own permissive/open licenses — see
  Chromium's `LICENSE` / `about:credits`).
- ungoogled-chromium's own modifications are released under **BSD-3-Clause** as
  well (`ungoogled-software/ungoogled-chromium`, `LICENSE`).
- BSD-3-Clause permits redistribution of source and binaries, including bundled
  in a larger work, provided the copyright/license notice is retained. The .deb
  carries its copyright file under `/usr/share/doc/ungoogled-chromium/copyright`.
- ungoogled-chromium removes Google web-service integration and telemetry, so
  there is **no Google sign-in / sync / "Sign in to Chrome" promo** and no
  Google ToS attached to the browser. Result: the built image contains only
  redistributable open-source software.

## Source provenance (supply chain)

- **Where**: the **XtraDeb PPA** (`ppa:xtradeb/apps`), a Launchpad-hosted apt
  repository: `https://ppa.launchpadcontent.net/xtradeb/apps/ubuntu noble main`.
- **Why this source**: it is the only actively-maintained, **noble-targeting,
  redistributable** ungoogled-chromium `.deb` source.
  - The official `ungoogled-chromium-binaries` site only lists a 2022-era
    v100 "unportable" Debian/Ubuntu build (won't run modern sites; security
    risk) and explicitly flags its community binaries as non-reproducible.
  - The openSUSE OBS personal repo `home:ungoogled_chromium` does not publish a
    noble target and its directory is currently empty.
- **Authenticity**: the PPA's `InRelease` is GPG-signed (key fingerprint
  `5301FA4FD93244FBC6F6149982BB6851C64F6880`). `build.sh` fetches that key by
  fingerprint from `keyserver.ubuntu.com`, **verifies the returned key carries
  that exact fingerprint** before trusting it, then installs via apt (which
  verifies the signed `Release → Packages → .deb` SHA256 chain).
- **Pinning**: `build.sh` pins the exact version
  `149.0.7827.114-1.1xtradeb1.2404.1` and additionally **re-verifies the
  downloaded `.deb`'s SHA256** against a hardcoded pin
  (`2f8bf341245d134b717f84f1788eec44951a84031620a2c4fb619b7c0fff0997`, captured
  from the signed Packages index) before installing — belt-and-suspenders on top
  of apt's own signature check, so a silently rotated pool entry cannot slip in.
- **Sibling package**: `ungoogled-chromium` Depends on
  `ungoogled-chromium-common (= <same version>)`; both are installed at the
  pinned version. Runtime libs (libnss3, libgtk-3-0t64, libgbm1, …) come from
  the ubuntu noble base archive via `apt-get install`.

## No-sign-in determinism

Even though ungoogled-chromium is sign-in-free by construction, Sfd04db also:

1. adds `--no-first-run --no-default-browser-check` to `launchChromium`
   (`internal/tab/browser/browser.go`), and
2. bakes a Chromium **managed policy** at
   `/etc/chromium/policies/managed/palmux.json` (the Debian/Chromium policy path,
   not google-chrome's `/etc/opt/chrome/...`) with
   `{"BrowserSignin":0,"SyncDisabled":true,"PromotionalTabsEnabled":false}`.

so the browser opens straight to a usable window across the swapped browser.

## To upgrade later

Bump `UGCHROMIUM_VERSION` AND `UGCHROMIUM_DEB_SHA256` together in `build.sh`
(both are env-overridable). Capture the new SHA256 from the signed Packages index:

```
curl -fsSL https://ppa.launchpadcontent.net/xtradeb/apps/ubuntu/dists/noble/main/binary-amd64/Packages.xz \
  | unxz | awk '/^Package: ungoogled-chromium$/{f=1} f{print} /^$/{if(f)exit}' \
  | grep -E '^(Version|SHA256):'
```
