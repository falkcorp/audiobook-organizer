// file: internal/plugins/acoustid/reset_all_test.go
// version: 1.0.0
// guid: 8b2f4a6c-1d3e-4f5a-9c7b-0e1a2b3c4d5f
// last-edited: 2026-07-01

package acoustid

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- test reporter --------------------------------------------------------
//
// Mirrors lshTestReporter in lsh_backfill_test.go — kept separate to avoid
// coupling the two test files together.

type resetAllTestReporter struct {
	mu     sync.Mutex
	frames []lshFrame
}

func (r *resetAllTestReporter) UpdateProgress(current, total int, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, lshFrame{current, total, message})
	return nil
}
func (r *resetAllTestReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *resetAllTestReporter) Logger() *slog.Logger                      { return slog.Default() }
func (r *resetAllTestReporter) Checkpoint(any) error                      { return nil }
func (r *resetAllTestReporter) IsCanceled() bool                          { return false }
func (r *resetAllTestReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, r)
}
func (r *resetAllTestReporter) Trigger(context.Context, string, any) error { return nil }
func (r *resetAllTestReporter) SetCurrentItem(string)                     {}

// newTestResetAllEmbeddingStore opens a real, temp-dir-backed EmbeddingStore.
// EmbeddingStore is a concrete PebbleDB-backed struct (not an interface), so
// unlike p.store (database.Store) it can't be swapped for a hand-rolled mock
// — the sibling database package tests use the same real-pebble approach via
// their own (unexported) newTestEmbeddingStore helper.
func newTestResetAllEmbeddingStore(t *testing.T) *database.EmbeddingStore {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open test pebble db: %v", err)
	}
	store := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })
	return store
}

// TestResetAll_SlowPathClearsFingerprints verifies the per-row fallback loop
// (used when p.store is not a *database.PebbleStore) clears AcoustIDSeg0..6
// on every file that has at least one non-empty segment, and leaves
// already-clear files untouched while still counting only real clears.
func TestResetAll_SlowPathClearsFingerprints(t *testing.T) {
	files := []database.BookFile{
		{ID: "f1", AcoustIDSeg0: "AQADtAcSRY"},
		{ID: "f2"}, // already clear
		{ID: "f3", AcoustIDSeg3: "AQADtAcSRZ", AcoustIDSeg6: "AQADtAcSRA"},
	}

	var mu sync.Mutex
	var updates []string
	store := &database.MockStore{
		GetAllBookFilesFunc: func() ([]database.BookFile, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(id string, updated *database.BookFile) error {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, id)
			if updated.AcoustIDSeg0 != "" || updated.AcoustIDSeg1 != "" || updated.AcoustIDSeg2 != "" ||
				updated.AcoustIDSeg3 != "" || updated.AcoustIDSeg4 != "" || updated.AcoustIDSeg5 != "" ||
				updated.AcoustIDSeg6 != "" {
				t.Errorf("UpdateBookFile called with non-empty segment for %q: %+v", id, updated)
			}
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &resetAllTestReporter{}

	if err := p.runResetAll(context.Background(), nil, r); err != nil {
		t.Fatalf("runResetAll returned error: %v", err)
	}

	if got, want := len(updates), 2; got != want {
		t.Fatalf("UpdateBookFile calls = %d (%v), want %d", got, updates, want)
	}
	wantIDs := map[string]bool{"f1": true, "f3": true}
	for _, id := range updates {
		if !wantIDs[id] {
			t.Errorf("unexpected update for id %q", id)
		}
	}
}

// TestResetAll_DeletesCandidatesAcrossPages seeds a real EmbeddingStore with
// enough "acoustid"-layer candidates to force multiple ListCandidates pages
// and verifies the drain-then-RunItems approach deletes every one of them,
// while leaving a candidate on a different layer untouched.
func TestResetAll_DeletesCandidatesAcrossPages(t *testing.T) {
	emb := newTestResetAllEmbeddingStore(t)

	const n = 7 // > 1 page when the loop's internal pageSize is small enough to matter
	for i := 0; i < n; i++ {
		c := database.DedupCandidate{
			EntityType: "book",
			EntityAID:  string(rune('a' + i)),
			EntityBID:  string(rune('A' + i)),
			Layer:      "acoustid",
			Status:     "pending",
		}
		if err := emb.UpsertCandidate(c); err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	// A non-"acoustid" candidate that must survive the reset.
	if err := emb.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  "keep-a",
		EntityBID:  "keep-b",
		Layer:      "embedding",
		Status:     "pending",
	}); err != nil {
		t.Fatalf("seed keeper candidate: %v", err)
	}

	store := &database.MockStore{
		GetAllBookFilesFunc: func() ([]database.BookFile, error) { return nil, nil },
	}
	p := &Plugin{store: store, embeddingStore: emb}
	r := &resetAllTestReporter{}

	if err := p.runResetAll(context.Background(), nil, r); err != nil {
		t.Fatalf("runResetAll returned error: %v", err)
	}

	remaining, total, err := emb.ListCandidates(database.CandidateFilter{Layer: "acoustid", Limit: 100})
	if err != nil {
		t.Fatalf("list remaining acoustid candidates: %v", err)
	}
	if len(remaining) != 0 || total != 0 {
		t.Fatalf("expected 0 remaining acoustid candidates, got %d (total=%d)", len(remaining), total)
	}

	kept, _, err := emb.ListCandidates(database.CandidateFilter{Layer: "embedding", Limit: 100})
	if err != nil {
		t.Fatalf("list embedding candidates: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("expected the non-acoustid candidate to survive, got %d", len(kept))
	}
}

// TestResetAll_CancelledContextReturnsError verifies that a pre-cancelled
// context causes runResetAll to return a non-nil error promptly, relying on
// RunItems's built-in ctx.Done() polling in the slow-path loop.
func TestResetAll_CancelledContextReturnsError(t *testing.T) {
	files := []database.BookFile{
		{ID: "f1", AcoustIDSeg0: "AQADtAcSRY"},
		{ID: "f2", AcoustIDSeg0: "AQADtAcSRZ"},
	}
	store := &database.MockStore{
		GetAllBookFilesFunc: func() ([]database.BookFile, error) { return files, nil },
		UpdateBookFileFunc: func(string, *database.BookFile) error {
			t.Error("UpdateBookFile should not be called with a pre-cancelled context")
			return nil
		},
	}
	p := &Plugin{store: store}
	r := &resetAllTestReporter{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.runResetAll(ctx, nil, r); err == nil {
		t.Fatal("expected non-nil error from runResetAll with pre-cancelled context")
	}
}
