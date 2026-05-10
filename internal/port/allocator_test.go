package port

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// [AC-S9fd775-1-1] Allocate / Free / List + idempotent same (scope, name)
func TestAllocate_Idempotent_Global(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	got1, err := a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "palmux2"})
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}
	if got1.Port == 0 {
		t.Fatalf("expected non-zero port, got %d", got1.Port)
	}
	got2, err := a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "palmux2"})
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if got1.Port != got2.Port {
		t.Errorf("expected idempotent port, got %d then %d", got1.Port, got2.Port)
	}
	if got1.AllocatedAt != got2.AllocatedAt {
		t.Errorf("expected stable AllocatedAt, got %v then %v", got1.AllocatedAt, got2.AllocatedAt)
	}
}

// [AC-S9fd775-1-1] Different (scope, name) get different ports
func TestAllocate_DifferentScopesDifferentPorts(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	gA, _ := a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "svc"})
	gB, _ := a.Allocate(ctx, Request{Scope: "ws-1", Name: "svc"})

	if gA.Port == gB.Port {
		t.Errorf("two different scopes should not share a port (got %d)", gA.Port)
	}
}

// [AC-S9fd775-1-1] Free + List
func TestList_AndFree(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	_, _ = a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "alpha"})
	_, _ = a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "beta"})
	_, _ = a.Allocate(ctx, Request{Scope: "ws-1", Name: "alpha"})

	all, err := a.List(ctx, "")
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(all) = %d, want 3", len(all))
	}

	scoped, err := a.List(ctx, ScopeGlobal)
	if err != nil {
		t.Fatalf("List(global): %v", err)
	}
	if len(scoped) != 2 {
		t.Errorf("List(global) = %d, want 2", len(scoped))
	}

	if err := a.Free(ctx, ScopeGlobal, "alpha"); err != nil {
		t.Fatalf("Free: %v", err)
	}
	// Free is idempotent.
	if err := a.Free(ctx, ScopeGlobal, "alpha"); err != nil {
		t.Fatalf("Free idempotent: %v", err)
	}

	scoped, _ = a.List(ctx, ScopeGlobal)
	if len(scoped) != 1 || scoped[0].Name != "beta" {
		t.Errorf("after Free, List(global) = %+v, want only beta", scoped)
	}

	// After Free, re-allocating the same name yields a (potentially) new port —
	// the API is idempotent only while the lease is live.
	got, err := a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "alpha"})
	if err != nil {
		t.Fatalf("Allocate after Free: %v", err)
	}
	if got.Port == 0 {
		t.Errorf("re-Allocate returned zero port")
	}
}

// [AC-S9fd775-1-2] Persistence at the configured path, portman-shaped JSON.
func TestPersistence_PathAndShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	a := New(path)
	ctx := context.Background()

	_, err := a.Allocate(ctx, Request{
		Scope:    ScopeGlobal,
		Name:     "palmux2",
		Project:  "tjst-t/palmux2",
		Worktree: "main",
		Hostname: "palmux2--main--palmux2",
		Expose:   true,
	})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ports.json should exist at %s: %v", path, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc struct {
		Version int                      `json:"version"`
		Leases  []map[string]interface{} `json:"leases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, string(raw))
	}
	if doc.Version == 0 {
		t.Errorf("expected version field set, got 0")
	}
	if len(doc.Leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(doc.Leases))
	}
	want := map[string]interface{}{
		"name":     "palmux2",
		"project":  "tjst-t/palmux2",
		"worktree": "main",
		"hostname": "palmux2--main--palmux2",
		"expose":   true,
	}
	for k, v := range want {
		if doc.Leases[0][k] != v {
			t.Errorf("lease[%q] = %v, want %v", k, doc.Leases[0][k], v)
		}
	}
	// scope is the palmux extension; verify it's present too.
	if doc.Leases[0]["scope"] != ScopeGlobal {
		t.Errorf("lease[scope] = %v, want %s", doc.Leases[0]["scope"], ScopeGlobal)
	}
	if _, ok := doc.Leases[0]["port"]; !ok {
		t.Errorf("lease[port] missing")
	}
}

// [AC-S9fd775-1-2] DefaultPath + override via custom path.
func TestDefaultPath_Override(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-fake-9fd775")
	got := DefaultPath()
	want := "/tmp/xdg-fake-9fd775/palmux/ports.json"
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/test-user")
	got = DefaultPath()
	want = "/home/test-user/.config/palmux/ports.json"
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

// [AC-S9fd775-1-2] Round-trip across allocators (= simulates restart).
func TestPersistence_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	ctx := context.Background()

	a := New(path)
	first, err := a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "svc"})
	if err != nil {
		t.Fatalf("first Allocate: %v", err)
	}

	// Fresh Allocator instance loads the same file.
	b := New(path)
	second, err := b.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "svc"})
	if err != nil {
		t.Fatalf("second Allocate: %v", err)
	}
	if first.Port != second.Port {
		t.Errorf("port not persisted: first=%d second=%d", first.Port, second.Port)
	}

	// Get without allocating returns the stored mapping.
	got, ok, err := b.Get(ctx, ScopeGlobal, "svc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got.Port != first.Port {
		t.Errorf("Get got %+v ok=%v, want port %d", got, ok, first.Port)
	}
}

// [AC-S9fd775-1-3] Concurrent Allocate from many goroutines yields distinct,
// idempotent results. flock + the in-process mutex must serialize them.
func TestAllocate_ConcurrentInProcess(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]int, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			m, err := a.Allocate(ctx, Request{
				Scope: ScopeGlobal,
				Name:  "svc-" + strconv.Itoa(i),
			})
			results[i] = m.Port
			errs[i] = err
		}(i)
	}
	wg.Wait()

	seen := make(map[int]bool)
	for i, p := range results {
		if errs[i] != nil {
			t.Fatalf("Allocate %d: %v", i, errs[i])
		}
		if p == 0 {
			t.Fatalf("Allocate %d: zero port", i)
		}
		if seen[p] {
			t.Errorf("port %d allocated to two scopes", p)
		}
		seen[p] = true
	}
}

// [AC-S9fd775-1-3] Concurrent Allocate from multiple processes via a child
// process. We launch palmux's test helper as a subprocess and have it allocate
// its own port, while the parent does the same. Both must succeed and end up
// with distinct entries in ports.json.
//
// We can't easily fork a child without an external binary, so instead we
// simulate the multi-process case by holding flock in goroutine A while
// goroutine B tries to acquire it: B must block until A releases. We assert
// the two writes interleave correctly (no torn JSON).
func TestAllocate_FlockSerializesWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	a := New(path)
	b := New(path)
	ctx := context.Background()

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N * 2)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_, _ = a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "a-" + strconv.Itoa(i)})
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _ = b.Allocate(ctx, Request{Scope: "ws-1", Name: "b-" + strconv.Itoa(i)})
		}(i)
	}
	wg.Wait()

	// File must still parse cleanly (no torn writes).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc fileFormat
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("ports.json corrupted after concurrent writes: %v\n%s", err, string(raw))
	}
	if len(doc.Leases) != N*2 {
		t.Errorf("expected %d leases, got %d", N*2, len(doc.Leases))
	}

	// All ports distinct.
	seen := make(map[int]bool)
	for _, m := range doc.Leases {
		if seen[m.Port] {
			t.Errorf("port %d duplicated", m.Port)
		}
		seen[m.Port] = true
	}
}

// [AC-S9fd775-1-4] pickFreePort returns ports the kernel currently isn't
// using, and avoids ports recorded as in-use in our state.
func TestPickFreePort_AvoidsRecorded(t *testing.T) {
	used := map[int]struct{}{}
	// Pre-populate `used` with a port we know is free, force pickFreePort
	// to skip it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	taken := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	used[taken] = struct{}{}

	for i := 0; i < 5; i++ {
		p, err := pickFreePort(used)
		if err != nil {
			t.Fatalf("pickFreePort: %v", err)
		}
		if p == taken {
			t.Fatalf("got recorded port %d back", p)
		}
		if p < 1024 {
			t.Errorf("got privileged port %d (kernel ephemeral range starts higher)", p)
		}
	}
}

// [AC-S9fd775-1-4] End-to-end: the port returned by Allocate must actually
// be bindable (modulo a small TOCTOU window the AC explicitly tolerates).
func TestAllocate_PortIsBindable(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		m, err := a.Allocate(ctx, Request{Scope: ScopeGlobal, Name: "svc-" + strconv.Itoa(i)})
		if err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(m.Port))
		if err != nil {
			// TOCTOU is acceptable per AC, but it should be rare. Don't
			// fail hard on a single retry.
			ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(m.Port))
			if err != nil {
				t.Fatalf("port %d not bindable: %v", m.Port, err)
			}
		}
		_ = ln.Close()
	}
}

// validate that Allocate rejects empty scope or name.
func TestAllocate_Validation(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	if _, err := a.Allocate(ctx, Request{Scope: "", Name: "x"}); err == nil {
		t.Errorf("expected error for empty scope")
	}
	if _, err := a.Allocate(ctx, Request{Scope: "global", Name: ""}); err == nil {
		t.Errorf("expected error for empty name")
	}
}

func newTestAllocator(t *testing.T) *Allocator {
	t.Helper()
	dir := t.TempDir()
	return New(filepath.Join(dir, "ports.json"))
}
