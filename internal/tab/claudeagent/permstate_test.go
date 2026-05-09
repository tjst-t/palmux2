package claudeagent

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestPermissionState_RegisterResolveBasic exercises the happy path:
// a waiter is registered, the user resolves it, and the recorded
// canUseToolResponse arrives on the channel.
func TestPermissionState_RegisterResolveBasic(t *testing.T) {
	p := NewPermissionState()
	ch, err := p.RegisterWaiter("perm-1")
	if err != nil {
		t.Fatalf("RegisterWaiter: %v", err)
	}
	if !p.HasWaiter("perm-1") {
		t.Fatal("HasWaiter must be true immediately after RegisterWaiter")
	}
	resp := canUseToolResponse{Subtype: "can_use_tool", Behavior: "allow"}
	if !p.ResolveWaiter("perm-1", resp) {
		t.Fatal("ResolveWaiter must return true when waiter exists")
	}
	select {
	case got := <-ch:
		if got.Behavior != "allow" {
			t.Fatalf("got %+v want allow", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected resp on channel within 100ms")
	}
	if p.HasWaiter("perm-1") {
		t.Fatal("waiter must be cleared after Resolve")
	}
}

// TestPermissionState_DuplicateRegisterIsError covers AC-6-3 (b):
// the second Register for the same permission_id MUST surface an
// explicit error rather than silently overwriting (which would
// orphan the original waiter's channel).
func TestPermissionState_DuplicateRegisterIsError(t *testing.T) {
	p := NewPermissionState()
	if _, err := p.RegisterWaiter("perm-x"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, err := p.RegisterWaiter("perm-x")
	if !errors.Is(err, ErrWaiterAlreadyRegistered) {
		t.Fatalf("got err=%v, want ErrWaiterAlreadyRegistered", err)
	}
}

// TestPermissionState_ResolveBeforeRegister covers AC-6-3 (a): when
// the resolver path runs before the registration (race window), the
// Resolve must return false WITHOUT panicking, and a subsequent
// Register must succeed (the prior Resolve cannot leak state into
// the next request).
func TestPermissionState_ResolveBeforeRegister(t *testing.T) {
	p := NewPermissionState()
	if p.ResolveWaiter("perm-late", canUseToolResponse{Behavior: "deny"}) {
		t.Fatal("ResolveWaiter must return false when no waiter exists")
	}
	// Register afterward — must succeed (no leftover state).
	if _, err := p.RegisterWaiter("perm-late"); err != nil {
		t.Fatalf("Register after stale Resolve: %v", err)
	}
}

// TestPermissionState_DropWaiter covers AC-6-3 (c): a waiter that
// is dropped (e.g. agent shutting down) must be removed from the
// map so a subsequent Resolve returns false without delivering.
func TestPermissionState_DropWaiter(t *testing.T) {
	p := NewPermissionState()
	ch, _ := p.RegisterWaiter("perm-drop")
	p.DropWaiter("perm-drop")
	if p.HasWaiter("perm-drop") {
		t.Fatal("waiter still registered after Drop")
	}
	if p.ResolveWaiter("perm-drop", canUseToolResponse{Behavior: "allow"}) {
		t.Fatal("Resolve after Drop must return false")
	}
	// Channel never received — still empty.
	select {
	case got := <-ch:
		t.Fatalf("unexpected recv after Drop: %+v", got)
	default:
		// OK
	}
}

// TestPermissionState_CloseDropsAllWaiters covers AC-6-3 (c) at
// scale: agent teardown wipes every registered waiter without
// delivering. WaiterCount must be 0 afterward.
func TestPermissionState_CloseDropsAllWaiters(t *testing.T) {
	p := NewPermissionState()
	for i := 0; i < 50; i++ {
		_, err := p.RegisterWaiter(string(rune('a'+i%26)) + string(rune('A'+i)))
		if err != nil {
			// Collisions on `a-A`, `b-B`, ... are fine after the first
			// 26 — we just want a meaningful count.
			continue
		}
	}
	if p.WaiterCount() == 0 {
		t.Fatal("expected at least one waiter")
	}
	p.CloseAndDropAllWaiters()
	if p.WaiterCount() != 0 {
		t.Fatalf("WaiterCount=%d after Close, want 0", p.WaiterCount())
	}
}

// TestPermissionState_PendingRoundTrip exercises pendingByID
// lifecycle: SetPending stores the CLI request_id, TakePending
// retrieves + removes it, a second TakePending returns false.
func TestPermissionState_PendingRoundTrip(t *testing.T) {
	p := NewPermissionState()
	p.SetPending("perm-1", "cli-req-1")
	got, ok := p.TakePending("perm-1")
	if !ok || got != "cli-req-1" {
		t.Fatalf("TakePending got=(%q,%v), want (cli-req-1,true)", got, ok)
	}
	if _, ok := p.TakePending("perm-1"); ok {
		t.Fatal("second TakePending must return false")
	}
}

// TestPermissionState_AllowList covers session-scoped allow-list:
// AddAllow + IsAllowed round-trip; Reset wipes the entry.
func TestPermissionState_AllowList(t *testing.T) {
	p := NewPermissionState()
	if p.IsAllowed("Bash") {
		t.Fatal("Bash must not be allowed by default")
	}
	p.AddAllow("Bash")
	if !p.IsAllowed("Bash") {
		t.Fatal("Bash must be allowed after AddAllow")
	}
	p.Reset()
	if p.IsAllowed("Bash") {
		t.Fatal("AllowList entries must be cleared by Reset")
	}
}

// TestPermissionState_AskAndPlan covers the per-block tracking maps
// — Ask and Plan both store a permission_id → tool_use_id mapping.
func TestPermissionState_AskAndPlan(t *testing.T) {
	p := NewPermissionState()
	p.RegisterAsk("ask-perm", "tu-1")
	if !p.HasAsk("ask-perm") {
		t.Fatal("HasAsk false after Register")
	}
	got, ok := p.TakeAsk("ask-perm")
	if !ok || got != "tu-1" {
		t.Fatalf("TakeAsk got=(%q,%v)", got, ok)
	}
	if p.HasAsk("ask-perm") {
		t.Fatal("HasAsk must be false after Take")
	}

	p.RegisterPlan("plan-perm", "tu-2")
	got2, ok := p.TakePlan("plan-perm")
	if !ok || got2 != "tu-2" {
		t.Fatalf("TakePlan got=(%q,%v)", got2, ok)
	}
}

// TestPermissionState_HookBlocks covers the hook_id → block ref
// registry used by HookBlock state continuity.
func TestPermissionState_HookBlocks(t *testing.T) {
	p := NewPermissionState()
	ref := hookBlockRef{TurnID: "t1", BlockID: "b1"}
	p.RegisterHook("hook-1", ref)
	got, ok := p.LookupHook("hook-1")
	if !ok || got != ref {
		t.Fatalf("LookupHook got=(%+v,%v)", got, ok)
	}
	p.DropHook("hook-1")
	if _, ok := p.LookupHook("hook-1"); ok {
		t.Fatal("LookupHook must return false after Drop")
	}
}

// TestPermissionState_ConcurrentAccess covers AC-6-3 (e): 1000
// concurrent Register / Resolve / pending / allow-list ops with no
// panics under -race.
func TestPermissionState_ConcurrentAccess(t *testing.T) {
	p := NewPermissionState()
	const N = 1000
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			permID := "register-" + string(rune('A'+(i%26)))
			// Best-effort — DropWaiter first to clear any prior reg
			// (the test deliberately reuses 26 ids for collisions).
			p.DropWaiter(permID)
			if _, err := p.RegisterWaiter(permID); err != nil && !errors.Is(err, ErrWaiterAlreadyRegistered) {
				t.Errorf("unexpected register err: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			permID := "register-" + string(rune('A'+(i%26)))
			p.ResolveWaiter(permID, canUseToolResponse{Behavior: "allow"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			permID := "pending-" + string(rune('A'+(i%26)))
			p.SetPending(permID, "cli-req")
			p.TakePending(permID)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			tool := "tool-" + string(rune('A'+(i%26)))
			p.AddAllow(tool)
			_ = p.IsAllowed(tool)
		}
	}()
	wg.Wait()
}

// TestPermissionState_ResolveMultipleDistinctPerms covers the
// happy-path concurrent shape that production runs hit: many
// distinct permission_ids in flight simultaneously, each with its
// own waiter. Every Resolve must reach exactly the right channel.
func TestPermissionState_ResolveMultipleDistinctPerms(t *testing.T) {
	p := NewPermissionState()
	const N = 100
	chans := make(map[string]chan canUseToolResponse, N)
	for i := 0; i < N; i++ {
		permID := "perm-multi-" + string(rune('a'+(i%26))) + string(rune('a'+((i/26)%26)))
		ch, err := p.RegisterWaiter(permID)
		if err != nil {
			t.Fatalf("Register %q: %v", permID, err)
		}
		chans[permID] = ch
	}
	// Resolve each in parallel.
	var wg sync.WaitGroup
	for permID := range chans {
		permID := permID
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !p.ResolveWaiter(permID, canUseToolResponse{Behavior: "allow", Message: permID}) {
				t.Errorf("Resolve %q: false", permID)
			}
		}()
	}
	wg.Wait()
	// Each channel must have received its own permID's response.
	for permID, ch := range chans {
		select {
		case got := <-ch:
			if got.Message != permID {
				t.Errorf("permID=%s got Message=%q (cross-talk)", permID, got.Message)
			}
		case <-time.After(time.Second):
			t.Errorf("permID=%s: timeout waiting for resp", permID)
		}
	}
}
