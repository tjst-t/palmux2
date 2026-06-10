package main

// runRuntime implements the `palmux runtime` subcommand group.
//
// Subcommands:
//
//	palmux runtime install  — download the latest palmux-ws image from GitHub
//	                          Releases (or a local file/URL) and import it into
//	                          incus. Also performs best-effort host prerequisite
//	                          setup (subuid/subgid root:1000:1, Docker FORWARD
//	                          hint). Supports --dry-run.
//
//	palmux runtime doctor   — check-only: report image presence, subuid/subgid
//	                          state, Docker FORWARD policy.
//
// Design notes:
//   - All incus commands are run with Stdin = nil (</dev/null semantics) to
//     avoid the incus YAML-stdin gotcha.
//   - Prereq fixes (subuid/subgid) need root. When not root we print the exact
//     manual commands and continue (best-effort).
//   - Docker FORWARD detection is best-effort: we look for `docker0` and
//     check `iptables -C FORWARD -j DROP` (if iptables is available).
//   - The GitHub API call respects GITHUB_TOKEN for rate-limit headroom.
//   - --image-url and --image-file override the GitHub download so this works
//     offline or from a local build.

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runRuntime dispatches `palmux runtime <subcommand>`.
// Returns an exit code (0 = success).
func runRuntime(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: palmux runtime <install|doctor> [flags]\n")
		return 1
	}
	switch args[0] {
	case "install":
		return runRuntimeInstall(args[1:])
	case "doctor":
		return runRuntimeDoctor(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown runtime subcommand %q. Available: install, doctor\n", args[0])
		return 1
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// runtime install
// ─────────────────────────────────────────────────────────────────────────────

const (
	ghReleasesAPI = "https://api.github.com/repos/tjst-t/palmux2/releases/latest"
	assetPattern  = "palmux-ws"
)

func runRuntimeInstall(args []string) int {
	// Parse flags manually (no pflag dependency inside this subcommand, keeping
	// it consistent with hook.go style).
	var (
		imageURL  string
		imageFile string
		dryRun    bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--image-url":
			if i+1 < len(args) {
				imageURL = args[i+1]
				i++
			}
		case "--image-file":
			if i+1 < len(args) {
				imageFile = args[i+1]
				i++
			}
		case "--dry-run":
			dryRun = true
		}
	}

	log := slog.Default()
	log.Info("palmux runtime install starting")

	// ── 1. verify incus is installed ─────────────────────────────────────────
	if err := requireBin("incus"); err != nil {
		fmt.Fprintf(os.Stderr,
			"ERROR: incus not found on PATH.\n"+
				"Install it with:\n"+
				"  # Ubuntu (zabbly PPA recommended for latest stable):\n"+
				"  sudo apt-get install -y incus\n"+
				"  # or: https://linuxcontainers.org/incus/docs/main/installing/\n")
		return 1
	}

	// ── 2. resolve image tarball path ────────────────────────────────────────
	var tarballPath string
	var tempFile string // non-empty → we created it and must remove it

	switch {
	case imageFile != "":
		// Offline / local build path.
		abs, err := filepath.Abs(imageFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: resolving --image-file %q: %v\n", imageFile, err)
			return 1
		}
		if _, err := os.Stat(abs); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: --image-file %q: %v\n", abs, err)
			return 1
		}
		tarballPath = abs
		fmt.Printf("Using local image: %s\n", tarballPath)

	case imageURL != "":
		// User-supplied URL — download to temp.
		var err error
		tarballPath, err = downloadToTemp(imageURL, dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR downloading %s: %v\n", imageURL, err)
			return 1
		}
		if !dryRun {
			tempFile = tarballPath
		}

	default:
		// Default: latest GitHub Release asset.
		assetURL, assetName, err := latestReleaseAssetURL()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: could not resolve latest release asset: %v\n"+
				"Tip: set GITHUB_TOKEN to avoid rate limits, or use --image-url / --image-file\n", err)
			return 1
		}
		fmt.Printf("Latest release asset: %s\n", assetName)
		if dryRun {
			fmt.Printf("[dry-run] would download: %s\n", assetURL)
			tarballPath = "/tmp/palmux-ws.tar.gz (not downloaded)"
		} else {
			tarballPath, err = downloadToTemp(assetURL, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR downloading %s: %v\n", assetURL, err)
				return 1
			}
			tempFile = tarballPath
		}
	}

	defer func() {
		if tempFile != "" {
			_ = os.Remove(tempFile)
		}
	}()

	// ── 3. prereq setup (best-effort) ────────────────────────────────────────
	setupSubuidSubgid(dryRun)
	checkDockerForward()

	// ── 4. import the image ───────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("Importing palmux-ws image into incus ...")

	// Remove old alias (idempotent).
	deleteAliasCmd := []string{"incus", "image", "alias", "delete", "palmux-ws"}
	if dryRun {
		fmt.Printf("[dry-run] would run: %s\n", strings.Join(deleteAliasCmd, " "))
	} else {
		c := exec.Command(deleteAliasCmd[0], deleteAliasCmd[1:]...) //nolint:gosec
		c.Stdin = nil
		_ = c.Run() // ignore exit code — alias may not exist
	}

	importCmd := []string{"incus", "image", "import", tarballPath, "--alias", "palmux-ws"}
	if dryRun {
		fmt.Printf("[dry-run] would run: %s\n", strings.Join(importCmd, " "))
		printSuccessSummary(dryRun)
		return 0
	}

	c := exec.Command(importCmd[0], importCmd[1:]...) //nolint:gosec
	c.Stdin = nil
	var importOut strings.Builder
	c.Stdout = io.MultiWriter(os.Stdout, &importOut)
	c.Stderr = io.MultiWriter(os.Stderr, &importOut)
	if err := c.Run(); err != nil {
		// Idempotency: the image content may already be in the incus store
		// (e.g. left by build.sh, which publishes then exports). `incus image
		// import` refuses a duplicate fingerprint, but the fix is just to point
		// the `palmux-ws` alias at the existing image. For a unified tarball the
		// fingerprint is the SHA-256 of the file.
		if strings.Contains(importOut.String(), "already exists") {
			if fp, ferr := sha256File(tarballPath); ferr == nil {
				_ = exec.Command("incus", "image", "alias", "delete", "palmux-ws").Run() //nolint:gosec,errcheck
				al := exec.Command("incus", "image", "alias", "create", "palmux-ws", fp) //nolint:gosec
				al.Stdin = nil
				if al.Run() == nil {
					fmt.Println("  (image already in store — aliased existing fingerprint to palmux-ws)")
					printSuccessSummary(false)
					return 0
				}
				fmt.Fprintf(os.Stderr, "ERROR: image exists but could not alias it; run: incus image alias create palmux-ws %s\n", fp)
				return 1
			}
		}
		fmt.Fprintf(os.Stderr, "ERROR: incus image import failed: %v\n", err)
		return 1
	}

	printSuccessSummary(false)
	return 0
}

// sha256File returns the hex SHA-256 of a file (= the incus fingerprint of a
// unified image tarball).
func sha256File(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// printSuccessSummary prints the post-install guidance.
func printSuccessSummary(dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "[dry-run] "
	}
	fmt.Printf("\n%s✓ palmux-ws image ready in incus\n", prefix)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Verify host prerequisites (run once):")
	fmt.Println("       palmux runtime doctor")
	fmt.Println("  2. In palmux, open a repository and switch a Workspace to")
	fmt.Println("     runtime: incus-container (Header › runtime chip)")
	fmt.Println()
	fmt.Println("Host prerequisites (if not already done):")
	fmt.Println("  • /etc/subuid and /etc/subgid must each have:  root:1000:1")
	fmt.Println("    sudo sh -c 'echo root:1000:1 >> /etc/subuid && echo root:1000:1 >> /etc/subgid'")
	fmt.Println("    sudo systemctl restart incus")
	fmt.Println("  • If Docker is installed, allow incusbr0 forwarding:")
	fmt.Println("    sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT")
}

// ─────────────────────────────────────────────────────────────────────────────
// runtime doctor
// ─────────────────────────────────────────────────────────────────────────────

func runRuntimeDoctor(args []string) int {
	_ = args // no flags yet
	fmt.Println("palmux runtime doctor")
	fmt.Println()

	ok := true

	// ── incus present? ────────────────────────────────────────────────────────
	if err := requireBin("incus"); err != nil {
		fmt.Println("  ✗ incus: not found on PATH")
		fmt.Println("    Install: sudo apt-get install -y incus")
		ok = false
	} else {
		out, err := incusRun("version")
		if err != nil {
			fmt.Printf("  ✗ incus: found but `incus version` failed: %v\n", err)
			ok = false
		} else {
			ver := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
			fmt.Printf("  ✓ incus: %s\n", ver)
		}
	}

	// ── incus daemon socket reachable (group membership)? ─────────────────────
	// `incus version` works client-only, but any real call hits
	// /var/lib/incus/unix.socket which is owned by the `incus-admin` group.
	out, err := incusRun("image", "alias", "list", "-f", "json")
	if err != nil && strings.Contains(err.Error(), "permission denied") {
		fmt.Println("  ✗ incus socket: permission denied — your user is not in the incus-admin group")
		fmt.Println("    Fix: sudo usermod -aG incus-admin $USER   (then log out/in, or `newgrp incus-admin`)")
		fmt.Println("    palmux itself needs this to launch incus-container workspaces.")
		fmt.Println("\nSocket access is required for the remaining checks — fix the above first.")
		return 1
	}

	// ── palmux-ws image imported? ─────────────────────────────────────────────
	if err != nil {
		fmt.Printf("  ✗ palmux-ws image: could not list aliases (%v)\n", err)
		ok = false
	} else {
		type alias struct {
			Name string `json:"name"`
		}
		var aliases []alias
		if jsonErr := json.Unmarshal([]byte(out), &aliases); jsonErr == nil {
			found := false
			for _, a := range aliases {
				if a.Name == "palmux-ws" {
					found = true
					break
				}
			}
			if found {
				fmt.Println("  ✓ palmux-ws image: imported")
			} else {
				fmt.Println("  ✗ palmux-ws image: NOT found — run: palmux runtime install")
				ok = false
			}
		}
	}

	// ── subuid / subgid root:1000:1 ───────────────────────────────────────────
	for _, file := range []string{"/etc/subuid", "/etc/subgid"} {
		has, checkErr := fileContainsSubIDLine(file, "root", 1000, 1)
		if checkErr != nil {
			fmt.Printf("  ✗ %s: read error (%v)\n", file, checkErr)
			ok = false
			continue
		}
		if has {
			fmt.Printf("  ✓ %s: root:1000:1 present\n", file)
		} else {
			fmt.Printf("  ✗ %s: root:1000:1 MISSING\n", file)
			fmt.Printf("    Fix: echo 'root:1000:1' | sudo tee -a %s\n", file)
			fmt.Println("    Then: sudo systemctl restart incus")
			ok = false
		}
	}

	// ── Docker FORWARD policy ─────────────────────────────────────────────────
	if dockerRunning() {
		if forwardDrop() {
			fmt.Println("  ✗ Docker: running and iptables FORWARD policy may block incusbr0")
			fmt.Println("    Fix: sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT")
			ok = false
		} else {
			fmt.Println("  ✓ Docker: running, FORWARD policy appears OK")
		}
	} else {
		fmt.Println("  ✓ Docker: not running (no FORWARD conflict)")
	}

	fmt.Println()
	if ok {
		fmt.Println("All checks passed.")
	} else {
		fmt.Println("Some checks failed — see hints above.")
	}
	if !ok {
		return 1
	}
	return 0
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// requireBin returns an error if the named binary is not found on PATH.
func requireBin(name string) error {
	_, err := exec.LookPath(name)
	return err
}

// incusRun runs `incus <args>` with Stdin=nil and returns stdout.
func incusRun(args ...string) (string, error) {
	c := exec.Command("incus", args...) //nolint:gosec
	c.Stdin = nil
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		// Include stderr so callers can detect "permission denied" / "not found".
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("incus %s: %s", strings.Join(args, " "), detail)
	}
	return string(out), nil
}

// latestReleaseAssetURL queries the GitHub Releases API and returns the
// download URL + name of the first asset whose name contains "palmux-ws".
func latestReleaseAssetURL() (url, name string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ghReleasesAPI, nil)
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GET %s: %w", ghReleasesAPI, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var release struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", fmt.Errorf("decode release JSON: %w", err)
	}
	// Prefer the stable "palmux-ws.tar.gz"; fall back to any asset matching pattern.
	var fallbackURL, fallbackName string
	for _, a := range release.Assets {
		if strings.Contains(a.Name, assetPattern) && strings.HasSuffix(a.Name, ".tar.gz") {
			if a.Name == "palmux-ws.tar.gz" {
				return a.BrowserDownloadURL, a.Name, nil
			}
			if fallbackURL == "" {
				fallbackURL = a.BrowserDownloadURL
				fallbackName = a.Name
			}
		}
	}
	if fallbackURL != "" {
		return fallbackURL, fallbackName, nil
	}
	return "", "", fmt.Errorf("no %q asset found in latest release (checked %d assets)", assetPattern, len(release.Assets))
}

// downloadToTemp downloads url to a temp file and returns the file path.
func downloadToTemp(url string, dryRun bool) (string, error) {
	if dryRun {
		fmt.Printf("[dry-run] would download: %s\n", url)
		return "/tmp/palmux-ws.tar.gz (not downloaded)", nil
	}
	fmt.Printf("Downloading %s ...\n", url)
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	f, err := os.CreateTemp("", "palmux-ws-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("download: %w", err)
	}
	fmt.Printf("Downloaded %d bytes to %s\n", n, f.Name())
	return f.Name(), nil
}

// setupSubuidSubgid checks /etc/subuid and /etc/subgid for root:1000:1 and
// adds the line if missing.  Requires root; if not root, prints manual steps.
func setupSubuidSubgid(dryRun bool) {
	fmt.Println()
	fmt.Println("Checking host prerequisites ...")

	needsRestart := false
	for _, file := range []string{"/etc/subuid", "/etc/subgid"} {
		has, err := fileContainsSubIDLine(file, "root", 1000, 1)
		if err != nil {
			fmt.Printf("  WARNING: could not read %s: %v\n", file, err)
			continue
		}
		if has {
			fmt.Printf("  ✓ %s: root:1000:1 already present\n", file)
			continue
		}
		fmt.Printf("  ✗ %s: root:1000:1 missing\n", file)
		if dryRun {
			fmt.Printf("    [dry-run] would add 'root:1000:1' to %s\n", file)
			continue
		}
		if os.Geteuid() == 0 {
			if addErr := appendLine(file, "root:1000:1"); addErr != nil {
				fmt.Printf("    ERROR adding line to %s: %v\n", file, addErr)
				fmt.Printf("    Manual fix: echo 'root:1000:1' | sudo tee -a %s\n", file)
			} else {
				fmt.Printf("    ✓ added root:1000:1 to %s\n", file)
				needsRestart = true
			}
		} else {
			fmt.Printf("    Not running as root — please run manually:\n")
			fmt.Printf("      echo 'root:1000:1' | sudo tee -a %s\n", file)
		}
	}

	if needsRestart {
		fmt.Println("  Restarting incus to pick up new subuid/subgid mapping ...")
		if dryRun {
			fmt.Println("  [dry-run] would run: sudo systemctl restart incus")
		} else {
			c := exec.Command("systemctl", "restart", "incus") //nolint:gosec
			c.Stdin = nil
			if err := c.Run(); err != nil {
				fmt.Printf("  WARNING: systemctl restart incus failed: %v\n", err)
				fmt.Println("  Please restart incus manually: sudo systemctl restart incus")
			} else {
				fmt.Println("  ✓ incus restarted")
			}
		}
	} else if !dryRun {
		fmt.Println()
		fmt.Println("  If subuid/subgid changes were needed but skipped (not root), run:")
		fmt.Println("    sudo systemctl restart incus   (after editing both files)")
	}
}

// fileContainsSubIDLine reports whether the given sub-ID file has a line
// exactly matching <user>:<start>:<count>.
func fileContainsSubIDLine(path, user string, start, count int) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	target := fmt.Sprintf("%s:%d:%d", user, start, count)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == target {
			return true, nil
		}
	}
	return false, sc.Err()
}

// appendLine appends a line to file (creates it if absent).
func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// checkDockerForward warns if Docker appears to be running and FORWARD policy
// could block incusbr0.  Best-effort, non-fatal.
func checkDockerForward() {
	if !dockerRunning() {
		return
	}
	fmt.Println()
	fmt.Println("  Docker detected — checking FORWARD policy ...")
	if forwardDrop() {
		fmt.Println("  WARNING: Docker iptables FORWARD policy may block incusbr0 internet access.")
		fmt.Println("  Fix: sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT")
		fmt.Println("  (Or stop Docker: sudo systemctl stop docker)")
	} else {
		fmt.Println("  ✓ Docker running, FORWARD policy appears OK")
	}
}

// dockerRunning returns true if docker0 interface is visible.
func dockerRunning() bool {
	c := exec.Command("ip", "link", "show", "docker0") //nolint:gosec
	c.Stdin = nil
	return c.Run() == nil
}

// forwardDrop returns true if `iptables -C FORWARD -j DROP` exits 0
// (i.e. there is a DROP rule in FORWARD).
func forwardDrop() bool {
	c := exec.Command("iptables", "-C", "FORWARD", "-j", "DROP") //nolint:gosec
	c.Stdin = nil
	return c.Run() == nil
}
