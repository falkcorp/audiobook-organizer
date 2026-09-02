// file: internal/plugins/dedup/bookfile_seg_sweep_test.go
// version: 1.0.1
// guid: 5c7e9f1a-b3d5-4f7e-9a1c-b3d5f7e9a1c3
// last-edited: 2026-09-02

// Tests for dedup.bookfile-seg-drop op (T020).
//
// Test coverage:
//  1. Non-BookfileSegDropStore store returns a descriptive error.
//  2. Dry-run: counts returned, flag not set.
//  3. Apply: counts returned, flag set.
//  4. Second apply after flag set: fast-path "already done" is taken.
//  5. Dry-run never sets flag even when rows need rewriting.

package dedup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mockSegDropStore implements BookfileSegDropStore for testing.
type mockSegDropStore struct {
	database.Store // embedded nil to satisfy database.Store interface (not called)

	// Configured results for SweepBookFileSegDrop.
	sweepResult database.SweepBookFileSegDropResult
	sweepErr    error
	sweepCalls  int

	// Settings key-value store (simple in-memory).
	settings map[string]string
}

func newMockSegDropStore(r database.SweepBookFileSegDropResult) *mockSegDropStore {
	return &mockSegDropStore{
		sweepResult: r,
		settings:    make(map[string]string),
	}
}

func (m *mockSegDropStore) SweepBookFileSegDrop(
	_ context.Context, dryRun bool, _ int, _ func(int, int),
) (database.SweepBookFileSegDropResult, error) {
	m.sweepCalls++
	return m.sweepResult, m.sweepErr
}

func (m *mockSegDropStore) GetSetting(key string) (*database.Setting, error) {
	v, ok := m.settings[key]
	if !ok {
		return nil, nil
	}
	return &database.Setting{Key: key, Value: v}, nil
}

func (m *mockSegDropStore) SetSetting(key, value, _ string, _ bool) error {
	m.settings[key] = value
	return nil
}

// segDropStoreAdapter wraps mockSegDropStore so it satisfies database.Store
// (for Plugin.store) AND BookfileSegDropStore (for the type-assertion).
type segDropStoreAdapter struct {
	database.Store
	inner *mockSegDropStore
}

func (a *segDropStoreAdapter) SweepBookFileSegDrop(
	ctx context.Context, dryRun bool, batchSize int, progress func(int, int),
) (database.SweepBookFileSegDropResult, error) {
	return a.inner.SweepBookFileSegDrop(ctx, dryRun, batchSize, progress)
}

func (a *segDropStoreAdapter) GetSetting(key string) (*database.Setting, error) {
	return a.inner.GetSetting(key)
}

func (a *segDropStoreAdapter) SetSetting(k, v, dt string, internal bool) error {
	return a.inner.SetSetting(k, v, dt, internal)
}

// newSegDropPlugin creates a Plugin with the mock store adapter.
func newSegDropPlugin(ms *mockSegDropStore) *Plugin {
	return &Plugin{store: &segDropStoreAdapter{inner: ms}}
}

// ── 1. Non-BookfileSegDropStore returns error ─────────────────────────────────

func TestBookfileSegDrop_NonBookfileSegDropStore_ReturnsError(t *testing.T) {
	p := &Plugin{store: nil}
	err := p.runBookfileSegDrop(context.Background(), json.RawMessage("{}"), &fakeReporter{})
	if err == nil {
		t.Fatal("expected error when store doesn't implement BookfileSegDropStore, got nil")
	}
}

// ── 2. Dry-run: counts returned, flag not set ─────────────────────────────────

func TestBookfileSegDrop_DryRun(t *testing.T) {
	ms := newMockSegDropStore(database.SweepBookFileSegDropResult{
		Total: 100, Rewrite: 30, Skipped: 70,
	})
	p := newSegDropPlugin(ms)

	// Dry-run (apply=false).
	err := p.runBookfileSegDrop(context.Background(), json.RawMessage(`{"apply":false}`), &fakeReporter{})
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}

	if ms.sweepCalls != 1 {
		t.Errorf("expected 1 SweepBookFileSegDrop call, got %d", ms.sweepCalls)
	}

	// Flag must NOT be set.
	if v := ms.settings[bookfileSegDropDoneFlag]; v == "true" {
		t.Error("completion flag must not be set after dry-run")
	}
}

// ── 3. Apply: counts returned, flag set ──────────────────────────────────────

func TestBookfileSegDrop_Apply(t *testing.T) {
	ms := newMockSegDropStore(database.SweepBookFileSegDropResult{
		Total: 200, Rewrite: 80, Skipped: 120,
	})
	p := newSegDropPlugin(ms)

	err := p.runBookfileSegDrop(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
	if err != nil {
		t.Fatalf("apply run failed: %v", err)
	}

	if ms.sweepCalls != 1 {
		t.Errorf("expected 1 sweep call, got %d", ms.sweepCalls)
	}
	if ms.settings[bookfileSegDropDoneFlag] != "true" {
		t.Error("completion flag must be set after apply=true")
	}
}

// ── 4. Second apply takes fast-path when flag is set ─────────────────────────

func TestBookfileSegDrop_AlreadyDone_FastPath(t *testing.T) {
	ms := newMockSegDropStore(database.SweepBookFileSegDropResult{Total: 10, Rewrite: 10})
	// Pre-set the flag as if a previous run completed.
	ms.settings[bookfileSegDropDoneFlag] = "true"
	p := newSegDropPlugin(ms)

	err := p.runBookfileSegDrop(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
	if err != nil {
		t.Fatalf("fast-path run failed: %v", err)
	}

	// The sweep must NOT have been called — the op should exit early.
	if ms.sweepCalls != 0 {
		t.Errorf("expected 0 sweep calls when flag already set, got %d", ms.sweepCalls)
	}
}

// ── 5. Dry-run never sets flag ────────────────────────────────────────────────

func TestBookfileSegDrop_DryRunNeverSetsFlag(t *testing.T) {
	ms := newMockSegDropStore(database.SweepBookFileSegDropResult{
		Total: 50, Rewrite: 50,
	})
	p := newSegDropPlugin(ms)

	// Run dry-run three times — flag should never appear.
	for i := range 3 {
		err := p.runBookfileSegDrop(context.Background(), json.RawMessage(`{}`), &fakeReporter{})
		if err != nil {
			t.Fatalf("dry-run #%d failed: %v", i+1, err)
		}
		if ms.settings[bookfileSegDropDoneFlag] == "true" {
			t.Errorf("dry-run #%d: flag must not be set", i+1)
		}
	}
}
