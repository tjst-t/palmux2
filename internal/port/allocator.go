// Package port implements palmux's built-in port allocator.
//
// This replaces the previous dependency on the external `portman` CLI.
// Allocations are persisted to a JSON file (default: ~/.config/palmux/ports.json)
// in a format derived from portman's `list --json` shape so existing consumers
// (the dashboard reader in internal/portman) keep working when pointed at it.
//
// Concurrency model: a single ports.json may be touched by both `palmux serve`
// and one-shot `palmux port` CLI invocations. To keep them safe, every read /
// modify / write cycle takes a POSIX file lock (flock LOCK_EX) on the file
// itself for the duration of the cycle. This satisfies the bootstrap
// constraint in §6.5 of the workspace-runtime design: `palmux port` must work
// when `palmux serve` is not running, AND when it is.
package port

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// ScopeGlobal is the conventional scope for allocations that are not tied to
// a specific workspace (e.g., the host palmux server itself, the host
// palmux-agent push channel, etc.).
const ScopeGlobal = "global"

// Mapping is one persisted port lease. The JSON field names match the output
// of `portman list --json` so that the dashboard reader and humans poking at
// the file see familiar keys. We add `scope` (palmux extension) and drop
// portman-specific fields we don't need (port_end / port_count for ranges).
type Mapping struct {
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	Project  string `json:"project,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Port     int    `json:"port"`
	Hostname string `json:"hostname,omitempty"`
	Expose   bool   `json:"expose,omitempty"`
	Status   string `json:"status,omitempty"`
	URL      string `json:"url,omitempty"`

	// AllocatedAt is set on first allocation and preserved across re-Allocate
	// calls (idempotency).
	AllocatedAt time.Time `json:"allocated_at,omitempty"`
}

// Key returns the (scope, name) identity used for idempotent lookups.
func (m Mapping) Key() Key { return Key{Scope: m.Scope, Name: m.Name} }

// Key is the (scope, name) identity for a Mapping.
type Key struct {
	Scope string
	Name  string
}

// Request is the input to Allocate. Scope and Name are required; the rest are
// metadata persisted alongside the port (so callers don't have to keep them
// out-of-band).
type Request struct {
	Scope    string
	Name     string
	Project  string
	Worktree string
	Hostname string
	Expose   bool
}

// fileFormat is the on-disk shape. We version it from day one so future format
// changes don't have to guess what they're reading.
type fileFormat struct {
	Version int       `json:"version"`
	Leases  []Mapping `json:"leases"`
}

const currentVersion = 1

// Allocator owns one ports.json file. It is safe for concurrent use within a
// single process; cross-process safety is provided by flock.
type Allocator struct {
	path string

	// mu protects in-process callers from racing on the same file. The flock
	// in withLock provides cross-process safety; this mutex avoids torn
	// reads when two goroutines in the same process call Allocate at once
	// (flock is per-process on Linux, so two goroutines in one process can
	// hold LOCK_EX simultaneously which would corrupt the file).
	mu sync.Mutex
}

// New builds an Allocator that persists to path. The directory is created on
// first write. If path is empty, DefaultPath() is used.
func New(path string) *Allocator {
	if path == "" {
		path = DefaultPath()
	}
	return &Allocator{path: path}
}

// Path returns the persistence file path.
func (a *Allocator) Path() string { return a.path }

// DefaultPath returns the canonical ports.json path:
//
//	~/.config/palmux/ports.json
//
// (or $XDG_CONFIG_HOME/palmux/ports.json if XDG_CONFIG_HOME is set).
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "palmux", "ports.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Last-resort fallback: relative path. The caller will see permission
		// errors before silent failure.
		return filepath.Join(".palmux", "ports.json")
	}
	return filepath.Join(home, ".config", "palmux", "ports.json")
}

// Allocate returns a port for (Scope, Name). If a mapping already exists, the
// same port is returned (idempotent). Otherwise a free port is picked, the
// mapping is persisted, and the new port is returned.
//
// The req.Project / req.Worktree / req.Hostname / req.Expose are only
// recorded on first allocation; subsequent Allocate calls do not overwrite
// metadata. Use Update for that.
func (a *Allocator) Allocate(ctx context.Context, req Request) (Mapping, error) {
	if err := req.validate(); err != nil {
		return Mapping{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var result Mapping
	err := a.withLock(func(file *os.File) error {
		st, err := readState(file)
		if err != nil {
			return err
		}
		if existing, ok := st.find(req.Scope, req.Name); ok {
			result = *existing
			return nil
		}
		port, err := pickFreePort(st.usedPorts())
		if err != nil {
			return fmt.Errorf("pick free port: %w", err)
		}
		m := Mapping{
			Scope:       req.Scope,
			Name:        req.Name,
			Project:     req.Project,
			Worktree:    req.Worktree,
			Port:        port,
			Hostname:    req.Hostname,
			Expose:      req.Expose,
			Status:      "allocated",
			AllocatedAt: time.Now().UTC(),
		}
		st.Leases = append(st.Leases, m)
		st.sort()
		if err := writeState(file, st); err != nil {
			return err
		}
		result = m
		return nil
	})
	if err != nil {
		return Mapping{}, err
	}
	_ = ctx // Reserved for future cancellation; the on-disk fast path is
	// well under any reasonable deadline so we don't plumb ctx through
	// flock today.
	return result, nil
}

// Free removes the (scope, name) mapping if present. Calling Free on a
// non-existent mapping returns nil — the call is idempotent.
func (a *Allocator) Free(ctx context.Context, scope, name string) error {
	if err := validateKey(scope, name); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = ctx
	return a.withLock(func(file *os.File) error {
		st, err := readState(file)
		if err != nil {
			return err
		}
		next := st.Leases[:0]
		for _, m := range st.Leases {
			if m.Scope == scope && m.Name == name {
				continue
			}
			next = append(next, m)
		}
		st.Leases = next
		return writeState(file, st)
	})
}

// List returns all mappings. If scope is empty, every mapping is returned;
// otherwise only mappings in that scope are returned. The result is sorted
// by (scope, name).
func (a *Allocator) List(ctx context.Context, scope string) ([]Mapping, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = ctx
	var out []Mapping
	err := a.withLock(func(file *os.File) error {
		st, err := readState(file)
		if err != nil {
			return err
		}
		st.sort()
		for _, m := range st.Leases {
			if scope != "" && m.Scope != scope {
				continue
			}
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get looks up a single mapping. The bool is false when no mapping exists.
func (a *Allocator) Get(ctx context.Context, scope, name string) (Mapping, bool, error) {
	if err := validateKey(scope, name); err != nil {
		return Mapping{}, false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = ctx
	var result Mapping
	var found bool
	err := a.withLock(func(file *os.File) error {
		st, err := readState(file)
		if err != nil {
			return err
		}
		if m, ok := st.find(scope, name); ok {
			result = *m
			found = true
		}
		return nil
	})
	if err != nil {
		return Mapping{}, false, err
	}
	return result, found, nil
}

// withLock opens (or creates) the ports.json file, takes an exclusive flock,
// runs fn with the open *os.File positioned for read/write, and releases the
// lock when fn returns. The directory is created on demand.
func (a *Allocator) withLock(fn func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return fmt.Errorf("ports dir: %w", err)
	}
	file, err := os.OpenFile(a.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open ports.json: %w", err)
	}
	defer file.Close()
	if err := flockExclusive(file); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer func() { _ = flockUnlock(file) }()
	return fn(file)
}

// readState reads and parses ports.json from an already-locked file. Empty
// files are treated as a fresh state.
func readState(file *os.File) (*fileFormat, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if info.Size() == 0 {
		return &fileFormat{Version: currentVersion}, nil
	}
	dec := json.NewDecoder(file)
	var st fileFormat
	if err := dec.Decode(&st); err != nil {
		return nil, fmt.Errorf("decode ports.json: %w", err)
	}
	if st.Version == 0 {
		st.Version = currentVersion
	}
	return &st, nil
}

// writeState rewrites the ports.json file in place. We truncate first because
// the new content may be shorter than the old.
func writeState(file *os.File, st *fileFormat) error {
	st.Version = currentVersion
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		return fmt.Errorf("encode ports.json: %w", err)
	}
	return nil
}

func (st *fileFormat) find(scope, name string) (*Mapping, bool) {
	for i := range st.Leases {
		if st.Leases[i].Scope == scope && st.Leases[i].Name == name {
			return &st.Leases[i], true
		}
	}
	return nil, false
}

func (st *fileFormat) usedPorts() map[int]struct{} {
	out := make(map[int]struct{}, len(st.Leases))
	for _, m := range st.Leases {
		out[m.Port] = struct{}{}
	}
	return out
}

func (st *fileFormat) sort() {
	sort.Slice(st.Leases, func(i, j int) bool {
		if st.Leases[i].Scope != st.Leases[j].Scope {
			return st.Leases[i].Scope < st.Leases[j].Scope
		}
		return st.Leases[i].Name < st.Leases[j].Name
	})
}

// pickFreePort finds a free TCP port the kernel is willing to give us, while
// excluding any port already recorded as in-use in our state.
//
// We rely on `net.Listen("tcp", ":0")` to surface a port that's currently
// free at the OS level. There is a TOCTOU window between close() and the
// caller binding to the port — the AC explicitly accepts that, with retry on
// failure.
func pickFreePort(used map[int]struct{}) (int, error) {
	const maxAttempts = 32
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if _, dup := used[port]; dup {
			// Already reserved by another lease. The kernel handed it to us
			// because no one is currently listening, but we treat our state
			// file as authoritative. Try again.
			continue
		}
		return port, nil
	}
	if lastErr != nil {
		return 0, lastErr
	}
	return 0, errors.New("could not find a free port after 32 attempts")
}

func (r Request) validate() error {
	return validateKey(r.Scope, r.Name)
}

func validateKey(scope, name string) error {
	if scope == "" {
		return errors.New("port: scope is required")
	}
	if name == "" {
		return errors.New("port: name is required")
	}
	return nil
}

// flockExclusive takes LOCK_EX on the file. Linux-specific (syscall.Flock is
// available on every platform palmux targets — Linux amd64/arm64 — so we
// keep it in the main file rather than building per-OS).
func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func flockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
