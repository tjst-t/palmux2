package claudeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// This file is the PERSISTENCE half of S862203-2 (ADR-0004): where palmux2
// remembers, per (repoId, branchId, tabId), the last pipe-mode replay
// offset it has fully processed — the sessions.json-equivalent the ADR
// calls for. ptyhost itself NEVER persists (ADR-0002/ADR-0004 PD-4); this
// store is what makes replay survive not just a clean palmux2 restart but a
// kill -9 (nothing else palmux2-side needs to have flushed for a correct
// resume — only this file, written atomically on every acked line).

// OffsetRecord is one tab's persisted pipe-mode replay bookmark.
type OffsetRecord struct {
	// LastAckOffset is the absolute ptyhost offset of the first byte NOT
	// yet processed — i.e. exactly the value to pass to
	// ptyhost.EncodeAttach / [PipeClient.Run] to resume replay with no gap
	// and no duplicate.
	LastAckOffset int64 `json:"lastAckOffset"`
	// RingGeneration is an opaque marker (e.g. the ptyhost argv hash + pid,
	// or any caller-chosen string) a caller MAY use to detect "this isn't
	// even the same ptyhost/child instance anymore" independent of the
	// offset-based overflow check. Optional — S862203-2's overflow
	// detection (AC-3) does not require it (the ATTACH clamped-start
	// comparison in ptyclient.go is sufficient on its own); it is exposed
	// for a Story-3 consumer that wants an extra belt-and-braces signal.
	RingGeneration string `json:"ringGeneration,omitempty"`
	// ResolvedControlRequests maps the request_id of each control_request
	// whose control_response has already been written back to the CLI, to
	// the absolute ptyhost offset of the FIRST byte of that request's line
	// (its lineStart). Persisted atomically with LastAckOffset (same file,
	// same write) so the two are always consistent.
	//
	// Why this is needed on TOP of the single LastAckOffset frontier
	// (S862203-3 review HIGH): the frontier is held back at the EARLIEST
	// still-unresolved control_request so a permission pending across a
	// restart is replayed. But claude issues PARALLEL tool calls, so a
	// LATER permission (B) can be answered while an EARLIER one (A) is
	// still pending — the frontier then sits at A, and a reconnect replays
	// B's control_request line too. Without this set, B would re-surface in
	// the UI as a spurious duplicate prompt for an already-answered
	// request. On reconnect the consumer skips re-dispatching any
	// control_request whose id is in here (see client.go's replaySuppress).
	// Entries whose lineStart is strictly before the persisted
	// LastAckOffset are pruned by the writer (they can never be replayed),
	// keeping the map bounded to the currently-replayable window.
	ResolvedControlRequests map[string]int64 `json:"resolvedControlRequests,omitempty"`
	UpdatedAt               time.Time        `json:"updatedAt"`
}

// offsetPersistedShape is the on-disk JSON layout for agent_offsets.json.
type offsetPersistedShape struct {
	Offsets map[string]OffsetRecord `json:"offsets"`
}

// OffsetStore persists per-tab pipe-mode replay offsets with the same
// atomic write-then-rename discipline as [Store] (sessions.json) — see
// save(). Deliberately a SEPARATE file (agent_offsets.json) rather than a
// new field bolted onto sessions.json's PersistedShape: it is written far
// more often (once per processed line, potentially) than session
// bookkeeping, and keeping it separate means a torn/corrupt write can never
// take sessions.json's resume pointers down with it.
type OffsetStore struct {
	path string

	mu   sync.RWMutex
	data offsetPersistedShape
}

// NewOffsetStore loads (or initialises) agent_offsets.json under dir.
func NewOffsetStore(dir string) (*OffsetStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("claudeagent: mkdir %s: %w", dir, err)
	}
	s := &OffsetStore{path: filepath.Join(dir, "agent_offsets.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *OffsetStore) load() error {
	s.data.Offsets = map[string]OffsetRecord{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("claudeagent: read %s: %w", s.path, err)
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return fmt.Errorf("claudeagent: parse %s: %w", s.path, err)
	}
	if s.data.Offsets == nil {
		s.data.Offsets = map[string]OffsetRecord{}
	}
	return nil
}

// save writes the store atomically (write-then-rename) so a palmux2 crash
// (including kill -9) mid-write never leaves agent_offsets.json torn/
// corrupt — the reader always sees either the old or the new complete
// contents, never a partial one.
func (s *OffsetStore) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("claudeagent: marshal offset store: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("claudeagent: write offset store tmp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("claudeagent: rename offset store file: %w", err)
	}
	return nil
}

// Get returns the persisted offset record for (repoID, branchID, tabID), or
// zero-value + false if the tab has never acked anything yet (a fresh
// session should ATTACH with offset -1 — "from oldest retained" — rather
// than 0, since the ring may not start at byte 0 of the *conversation* even
// though it starts at byte 0 of the ring itself; callers that DO know the
// ring is guaranteed fresh may use 0 interchangeably).
func (s *OffsetStore) Get(repoID, branchID, tabID string) (OffsetRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.data.Offsets[offsetKey(repoID, branchID, tabID)]
	return rec, ok
}

// Save persists lastAckOffset (+ optional ringGeneration marker +
// resolvedControlRequests set) for (repoID, branchID, tabID) in a single
// atomic write, so the offset and the resolved-request set are never
// observed inconsistent with each other. Intended to be called from inside
// a [LineHandler] callback after that line has been fully processed
// (transcript/permstate updated etc. — Story 3), so a torn callback never
// advances the persisted offset past what was actually applied.
//
// resolved may be nil (no resolved control_requests to remember) and is
// COPIED defensively so the caller may keep mutating its own map after the
// call returns.
func (s *OffsetStore) Save(repoID, branchID, tabID string, lastAckOffset int64, ringGeneration string, resolved map[string]int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var resolvedCopy map[string]int64
	if len(resolved) > 0 {
		resolvedCopy = make(map[string]int64, len(resolved))
		for k, v := range resolved {
			resolvedCopy[k] = v
		}
	}
	s.data.Offsets[offsetKey(repoID, branchID, tabID)] = OffsetRecord{
		LastAckOffset:           lastAckOffset,
		RingGeneration:          ringGeneration,
		ResolvedControlRequests: resolvedCopy,
		UpdatedAt:               time.Now().UTC(),
	}
	return s.save()
}

// Clear forgets the persisted offset for (repoID, branchID, tabID) — used
// when AC-S862203-2-3's overflow detection declares "lossless restore
// impossible" and the caller resets to a brand new session (there is no
// longer a meaningful resume point for the OLD ptyhost/ring generation).
func (s *OffsetStore) Clear(repoID, branchID, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := offsetKey(repoID, branchID, tabID)
	if _, ok := s.data.Offsets[key]; !ok {
		return nil
	}
	delete(s.data.Offsets, key)
	return s.save()
}

// offsetKey mirrors tabKey (store.go) — same {repoId}/{branchId}/{tabId}
// shape, same CanonicaliseTabID legacy-id folding, so the two stores agree
// on tab identity without duplicating that logic.
func offsetKey(repoID, branchID, tabID string) string {
	return tabKey(repoID, branchID, tabID)
}
