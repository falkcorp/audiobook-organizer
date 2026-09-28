// file: internal/dedup/drain_stale_test.go
// version: 1.3.0
// guid: 6b8c9a6c-b168-4fb9-ba23-99937427b562
// last-edited: 2026-09-28

// Tests for Engine.DrainStaleCandidates (DEDUP-1 / CONS-16 / CONS-17).
//
// The engine method re-runs pending exact candidates through the current guard
// chain and buckets would-purge rows by rejecting reason. These tests plant
// candidates in a real in-memory EmbeddingStore and wire book lookups via the
// MockStore, then assert counts, sample buckets, and the dry-run/apply contract.

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// drainBook is a compact spec for a book fixture in these tests.
type drainBook struct {
	id               string
	title            string
	duration         *int
	isbn13           *string
	files            []database.BookFile
	isPrimaryVersion *bool // nil = unknown/conservative (never non-primary)
	filePath         string
}

// setupDrainTest wires an Engine whose GetBookByID / GetBookFiles resolve from
// the given fixtures. A book ID not present in the map resolves to (nil, nil),
// modelling a since-deleted book.
func setupDrainTest(t *testing.T, books []drainBook) (*Engine, *database.EmbeddingStore) {
	t.Helper()
	engine, mock, es := setupTestEngine(t)

	byID := make(map[string]drainBook, len(books))
	for _, b := range books {
		byID[b.id] = b
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		b, ok := byID[id]
		if !ok {
			return nil, nil // since-deleted book
		}
		return &database.Book{
			ID:               b.id,
			Title:            b.title,
			Duration:         b.duration,
			ISBN13:           b.isbn13,
			IsPrimaryVersion: b.isPrimaryVersion,
			FilePath:         b.filePath,
		}, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return byID[bookID].files, nil
	}
	return engine, es
}

// seedDrainCandidate plants a pending exact candidate for the pair.
func seedDrainCandidate(t *testing.T, es *database.EmbeddingStore, aID, bID string) {
	t.Helper()
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book",
		EntityAID:  aID,
		EntityBID:  bID,
		Layer:      "exact",
		Status:     "pending",
	}); err != nil {
		t.Fatalf("UpsertCandidate(%s,%s): %v", aID, bID, err)
	}
}

func TestDrainStale_BoilerplateTitle(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "Opening Credits", duration: new(3600)},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 1 || res.WouldPurge != 1 || res.Kept != 0 {
		t.Fatalf("counts = inspected %d, wouldPurge %d, kept %d; want 1/1/0", res.Inspected, res.WouldPurge, res.Kept)
	}
	if res.ReasonCounts[drainReasonBoilerplateTitle] != 1 {
		t.Fatalf("boilerplate_title count = %d; want 1 (all: %v)", res.ReasonCounts[drainReasonBoilerplateTitle], res.ReasonCounts)
	}
	if len(res.Samples[drainReasonBoilerplateTitle]) != 1 {
		t.Fatalf("expected 1 boilerplate sample, got %d", len(res.Samples[drainReasonBoilerplateTitle]))
	}
}

func TestDrainStale_ShortDuration(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(30)}, // < minFingerprintMatchSeconds (60)
		{id: "BOOK_B", title: "Another Real Book", duration: new(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonShortDuration] != 1 {
		t.Fatalf("short_duration bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

func TestDrainStale_IdentifierConflict(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(3600), isbn13: new("9781111111111")},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600), isbn13: new("9782222222222")},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonIdentifierConflict] != 1 {
		t.Fatalf("identifier_conflict bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

func TestDrainStale_PartVsWhole(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(3600), files: []database.BookFile{
			{ID: "FA1", BookID: "BOOK_A", Duration: 100},
		}},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600), files: []database.BookFile{
			{ID: "FB1", BookID: "BOOK_B", Duration: 500},
			{ID: "FB2", BookID: "BOOK_B", Duration: 500},
		}},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonPartVsWhole] != 1 {
		t.Fatalf("part_vs_whole bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

// TestDrainStale_NonPrimaryVersion is the INIT-2 T3 drain-gate-parity
// regression: the chokepoint's FIRST gate (isNonPrimaryVersion) must have a
// drain twin bucketed as non_primary_version, matching upsertExactCandidate's
// behavior of never emitting a candidate involving a non-primary
// version-group member.
func TestDrainStale_NonPrimaryVersion(t *testing.T) {
	primary := true
	nonPrimary := false
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(3600), isPrimaryVersion: &primary},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600), isPrimaryVersion: &nonPrimary},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonNonPrimaryVersion] != 1 {
		t.Fatalf("non_primary_version bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

// TestDrainStale_NonPrimaryVersion_ConservativeNilKept is the
// anti-over-suppression proof for the new gate: a pair whose IsPrimaryVersion
// is unknown (nil) on both sides — the common case for older rows and for
// stores that never set the flag — must NOT be treated as non-primary.
// isNonPrimaryVersion(nil-flag book) returns false, matching
// upsertExactCandidate's own conservative default, so this pair must be KEPT
// by the drain exactly like it would still be emitted by the chokepoint.
func TestDrainStale_NonPrimaryVersion_ConservativeNilKept(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(3600)}, // isPrimaryVersion left nil
		{id: "BOOK_B", title: "A Real Book", duration: new(3600)}, // isPrimaryVersion left nil
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 1 || res.WouldPurge != 0 || res.Kept != 1 {
		t.Fatalf("nil-primary-flag pair over-suppressed: inspected %d, wouldPurge %d, kept %d (want 1/0/1)", res.Inspected, res.WouldPurge, res.Kept)
	}
}

func TestDrainStale_MissingBook(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(3600)},
		// BOOK_GONE is intentionally absent → GetBookByID returns (nil, nil).
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_GONE")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonMissingBook] != 1 {
		t.Fatalf("missing_book bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

func TestDrainStale_KeptWhenStillValid(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: new(3600)},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 1 || res.WouldPurge != 0 || res.Kept != 1 {
		t.Fatalf("counts = inspected %d, wouldPurge %d, kept %d; want 1/0/1", res.Inspected, res.WouldPurge, res.Kept)
	}
}

// TestDrainStale_DryRunWritesNothing asserts apply=false leaves every candidate
// status untouched.
func TestDrainStale_DryRunWritesNothing(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "Opening Credits", duration: new(3600)},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	if _, err := engine.DrainStaleCandidates(context.Background(), "", false, nil); err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(cands) != 1 || cands[0].Status != "pending" {
		t.Fatalf("dry-run mutated store: %+v", cands)
	}
}

// TestDrainStale_ApplyReclassifiesOnlyWouldPurge asserts apply=true sets
// stale-drain on would-purge rows but leaves kept rows pending.
func TestDrainStale_ApplyReclassifiesOnlyWouldPurge(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "Opening Credits", duration: new(3600)}, // boilerplate → purge
		{id: "BOOK_B", title: "A Real Book", duration: new(3600)},
		{id: "BOOK_C", title: "A Real Book", duration: new(3600)}, // C+D still valid → kept
		{id: "BOOK_D", title: "A Real Book", duration: new(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_C", "BOOK_D")

	res, err := engine.DrainStaleCandidates(context.Background(), "", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates apply: %v", err)
	}
	if res.WouldPurge != 1 || res.Kept != 1 {
		t.Fatalf("apply counts wrong: wouldPurge %d, kept %d", res.WouldPurge, res.Kept)
	}

	var pending, staleDrain int
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	for _, c := range cands {
		switch c.Status {
		case "pending":
			pending++
		case staleDrainStatus:
			staleDrain++
		}
	}
	if staleDrain != 1 || pending != 1 {
		t.Fatalf("after apply want 1 stale-drain + 1 pending; got stale-drain %d, pending %d (all: %+v)", staleDrain, pending, cands)
	}
}

// TestDrainStale_PagingAcrossBatches proves the page loop neither skips nor
// double-counts rows when the backlog exceeds one batch. It lowers the batch
// size to force multiple pages.
func TestDrainStale_PagingAcrossBatches(t *testing.T) {
	old := drainStaleBatchSize
	drainStaleBatchSize = 2
	t.Cleanup(func() { drainStaleBatchSize = old })

	const pairs = 7 // > 3 full batches of 2
	books := make([]drainBook, 0, pairs*2)
	for i := range pairs {
		aID := "PA" + string(rune('a'+i))
		bID := "PB" + string(rune('a'+i))
		// Every pair has a boilerplate side → all would-purge.
		books = append(books,
			drainBook{id: aID, title: "Opening Credits", duration: new(3600)},
			drainBook{id: bID, title: "A Real Book", duration: new(3600)},
		)
	}
	engine, es := setupDrainTest(t, books)
	for i := range pairs {
		seedDrainCandidate(t, es, "PA"+string(rune('a'+i)), "PB"+string(rune('a'+i)))
	}

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != pairs {
		t.Fatalf("paging inspected %d; want %d (skip/double-count across pages)", res.Inspected, pairs)
	}
	if res.WouldPurge != pairs {
		t.Fatalf("paging wouldPurge %d; want %d", res.WouldPurge, pairs)
	}
}

// TestDrainStale_CheckpointResumeAndClear verifies the APPLY path saves a
// checkpoint during the scan, clears it on clean completion, and resumes from a
// saved offset. Checkpoint/resume is intentionally apply-only (dry runs always
// full-scan for a complete report), so this test runs apply=true.
//
// The candidates are all still-valid pairs (kept, not would-purge) so apply
// marks nothing — this isolates the offset-resume behaviour from the marking
// pass: a resume that skips inspected rows is due to the offset, not because the
// rows were already reclassified.
func TestDrainStale_CheckpointResumeAndClear(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	byID := map[string]*database.Book{
		"BOOK_A": {ID: "BOOK_A", Title: "A Real Book", Duration: new(3600)},
		"BOOK_B": {ID: "BOOK_B", Title: "A Real Book", Duration: new(3600)},
		"BOOK_C": {ID: "BOOK_C", Title: "A Real Book", Duration: new(3600)},
		"BOOK_D": {ID: "BOOK_D", Title: "A Real Book", Duration: new(3600)},
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }
	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_C", "BOOK_D")

	// Capture checkpoint traffic.
	var saved []byte
	var cleared bool
	mock.SaveOperationStateFunc = func(opID string, state []byte) error { saved = state; return nil }
	mock.GetOperationStateFunc = func(opID string) ([]byte, error) { return nil, nil } // start fresh
	mock.DeleteOperationStateFunc = func(opID string) error { cleared = true; return nil }

	res, err := engine.DrainStaleCandidates(context.Background(), "op-123", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 2 || res.Kept != 2 {
		t.Fatalf("inspected %d kept %d; want 2/2", res.Inspected, res.Kept)
	}
	if saved == nil {
		t.Fatalf("expected a checkpoint to be saved during the apply scan")
	}
	if !cleared {
		t.Fatalf("expected the checkpoint to be cleared on clean completion")
	}

	var cp operations.OperationState
	if err := json.Unmarshal(saved, &cp); err != nil {
		t.Fatalf("decode saved checkpoint: %v", err)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	var maxID int64
	for _, c := range cands {
		maxID = max(maxID, c.ID)
	}
	if cp.Phase != drainStaleCheckpointPhase || int64(cp.PhaseIndex) != maxID || cp.PhaseTotal != 2 {
		t.Fatalf("checkpoint = phase %q index %d total %d; want %q / last candidate ID %d / 2",
			cp.Phase, cp.PhaseIndex, cp.PhaseTotal, drainStaleCheckpointPhase, maxID)
	}

	drainStaleBatchSizeOld := drainStaleBatchSize
	drainStaleBatchSize = 1
	t.Cleanup(func() { drainStaleBatchSize = drainStaleBatchSizeOld })

	// Resume after a cursor at the FIRST candidate → only the second is inspected.
	var firstID int64 = maxID
	for _, c := range cands {
		firstID = min(firstID, c.ID)
	}
	mock.GetOperationStateFunc = func(opID string) ([]byte, error) {
		return fmt.Appendf(nil, `{"operation_id":"op-123","type":"dedup:drain-stale","phase":%q,"phase_index":%d,"phase_total":2,"status":"interrupted"}`,
			drainStaleCheckpointPhase, firstID), nil
	}
	res2, err := engine.DrainStaleCandidates(context.Background(), "op-123", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates resume: %v", err)
	}
	if res2.Inspected != 1 {
		t.Fatalf("resume after the first candidate ID should inspect 1 row, inspected %d", res2.Inspected)
	}

	// Resume after the last candidate ID → nothing inspected.
	mock.GetOperationStateFunc = func(opID string) ([]byte, error) {
		return fmt.Appendf(nil, `{"operation_id":"op-123","phase":%q,"phase_index":%d,"phase_total":2}`,
			drainStaleCheckpointPhase, maxID), nil
	}
	res3, err := engine.DrainStaleCandidates(context.Background(), "op-123", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates resume: %v", err)
	}
	if res3.Inspected != 0 {
		t.Fatalf("resume after the last candidate ID should inspect 0 rows, inspected %d", res3.Inspected)
	}
}

// A checkpoint from the old offset-paged scan (phase "scanning", PhaseIndex =
// a row offset) must not be read as a candidate-ID cursor: the apply restarts
// from the beginning, which is safe because phase 1 writes nothing.
func TestDrainStale_LegacyOffsetCheckpointRestartsFromBeginning(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	byID := map[string]*database.Book{
		"BOOK_A": {ID: "BOOK_A", Title: "A Real Book", Duration: new(3600)},
		"BOOK_B": {ID: "BOOK_B", Title: "A Real Book", Duration: new(3600)},
		"BOOK_C": {ID: "BOOK_C", Title: "A Real Book", Duration: new(3600)},
		"BOOK_D": {ID: "BOOK_D", Title: "A Real Book", Duration: new(3600)},
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }
	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_C", "BOOK_D")
	mock.SaveOperationStateFunc = func(string, []byte) error { return nil }
	mock.DeleteOperationStateFunc = func(string) error { return nil }
	mock.GetOperationStateFunc = func(string) ([]byte, error) {
		return []byte(`{"operation_id":"op-old","phase":"scanning","phase_index":1,"phase_total":2}`), nil
	}

	res, err := engine.DrainStaleCandidates(context.Background(), "op-old", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 2 {
		t.Fatalf("legacy offset checkpoint was honoured as a cursor: inspected %d; want 2", res.Inspected)
	}
}

// TestDrainStale_DryRunIgnoresCheckpoint verifies a dry run never resumes from a
// checkpoint: even with a saved offset present, it full-scans so its report is
// complete.
func TestDrainStale_DryRunIgnoresCheckpoint(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	byID := map[string]*database.Book{
		"BOOK_A": {ID: "BOOK_A", Title: "A Real Book", Duration: new(3600)},
		"BOOK_B": {ID: "BOOK_B", Title: "A Real Book", Duration: new(3600)},
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }
	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	// A stale checkpoint that, if honoured, would skip the only row.
	var saveCalled bool
	mock.SaveOperationStateFunc = func(string, []byte) error { saveCalled = true; return nil }
	mock.GetOperationStateFunc = func(string) ([]byte, error) {
		return []byte(`{"operation_id":"op-x","phase":"scanning","phase_index":99,"phase_total":99}`), nil
	}

	res, err := engine.DrainStaleCandidates(context.Background(), "op-x", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 1 {
		t.Fatalf("dry run honoured a checkpoint offset (inspected %d; want 1) — report would be partial", res.Inspected)
	}
	if saveCalled {
		t.Fatalf("dry run must not write checkpoints")
	}
}

// A human pins a would-purge pair while phase 1 is still scanning (here: on
// the first book lookup, after the page snapshot was read). Phase 2 must not
// move it to stale-drain, and its manual mark must survive.
func TestDrainStale_PinDuringScanIsNotReclassified(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "Opening Credits", duration: new(3600)}, // boilerplate → purge
		{id: "BOOK_B", title: "A Real Book", duration: new(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	mock := engine.bookStore.(*database.MockStore)
	inner := mock.GetBookByIDFunc
	// Book reads run on the prefetch worker pool, so the one-shot pin is a
	// sync.Once and failures use t.Errorf (t.Fatalf is illegal off the test
	// goroutine).
	var pinOnce sync.Once
	var pinned atomic.Bool
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		pinOnce.Do(func() {
			if _, err := es.EnqueueManualCandidate("book", "BOOK_A", "BOOK_B", ""); err != nil {
				t.Errorf("pin: %v", err)
			}
			pinned.Store(true)
		})
		return inner(id)
	}

	res, err := engine.DrainStaleCandidates(context.Background(), "", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates apply: %v", err)
	}
	if !pinned.Load() {
		t.Fatal("fixture never looked a book up")
	}
	if res.WouldPurge != 1 || res.SkippedAtWrite != 1 {
		t.Fatalf("want wouldPurge 1 (scan snapshot) and skippedAtWrite 1; got %d / %d", res.WouldPurge, res.SkippedAtWrite)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil || len(cands) != 1 {
		t.Fatalf("ListCandidates: %v (%d rows)", err, len(cands))
	}
	if cands[0].Status != "pending" || !database.IsManualCandidate(cands[0]) {
		t.Fatalf("pinned row was reclassified or lost its mark: %+v", cands[0])
	}
}

// Two book rows at one cleaned path are review-queue-only: drain-stale keeps
// them even when a guard would otherwise drain the pair. The second pair is
// the positive control: same guard, different paths, drained.
func TestDrainStale_SamePathPairKept(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "Opening Credits", duration: new(3600), filePath: "/lib/x/book.m4b"},
		{id: "BOOK_B", title: "A Real Book", duration: new(3600), filePath: "/lib/x/book.m4b"},
		{id: "BOOK_C", title: "Opening Credits", duration: new(3600), filePath: "/lib/c/c.m4b"},
		{id: "BOOK_D", title: "A Real Book", duration: new(3600), filePath: "/lib/d/d.m4b"},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_C", "BOOK_D")

	res, err := engine.DrainStaleCandidates(context.Background(), "", true, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates apply: %v", err)
	}
	if res.WouldPurge != 1 || res.Kept != 1 {
		t.Fatalf("want wouldPurge 1 / kept 1; got %d / %d", res.WouldPurge, res.Kept)
	}
	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	for _, c := range cands {
		samePath := c.EntityAID == "BOOK_A" || c.EntityBID == "BOOK_A"
		if samePath && c.Status != "pending" {
			t.Fatalf("same-path pair was drained: %+v", c)
		}
		if !samePath && c.Status != staleDrainStatus {
			t.Fatalf("control pair not drained: %+v", c)
		}
	}
}

type drainProgressCall struct {
	done, total int
}

// The progress callback fires once before the first page and once after every
// page, with done = rows inspected so far and total = the pending-exact count
// taken at the start; apply adds a phase-2 start and finish report.
func TestDrainStale_ProgressReportedPerPage(t *testing.T) {
	old := drainStaleBatchSize
	drainStaleBatchSize = 2
	t.Cleanup(func() { drainStaleBatchSize = old })

	books := []drainBook{{id: "BOOK_B", title: "A Real Book", duration: new(3600)}}
	for i := range 5 {
		books = append(books, drainBook{id: fmt.Sprintf("P%d", i), title: "Opening Credits", duration: new(3600)})
	}
	engine, es := setupDrainTest(t, books)
	for i := range 5 {
		seedDrainCandidate(t, es, fmt.Sprintf("P%d", i), "BOOK_B")
	}

	var calls []drainProgressCall
	var msgs []string
	res, err := engine.DrainStaleCandidates(context.Background(), "", false, func(done, total int, msg string) {
		calls = append(calls, drainProgressCall{done, total})
		msgs = append(msgs, msg)
	})
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	want := []drainProgressCall{{0, 5}, {2, 5}, {4, 5}, {5, 5}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("dry-run progress calls = %v; want %v", calls, want)
	}
	if res.Inspected != 5 || res.WouldPurge != 5 {
		t.Fatalf("inspected %d wouldPurge %d; want 5/5", res.Inspected, res.WouldPurge)
	}
	if !strings.Contains(msgs[len(msgs)-1], "Scanned 5 of 5 pending exact candidates") {
		t.Fatalf("last scan message = %q", msgs[len(msgs)-1])
	}

	calls = nil
	if _, err := engine.DrainStaleCandidates(context.Background(), "", true, func(done, total int, _ string) {
		calls = append(calls, drainProgressCall{done, total})
	}); err != nil {
		t.Fatalf("DrainStaleCandidates apply: %v", err)
	}
	// Scan: 0,2,4,5 of 5; then marking: 0 of 5, 5 of 5.
	want = []drainProgressCall{{0, 5}, {2, 5}, {4, 5}, {5, 5}, {0, 5}, {5, 5}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("apply progress calls = %v; want %v", calls, want)
	}
}

// seedDrainParallelFixture plants many candidates across several pages whose
// books repeat across pairs (so the per-run cache and cross-page reuse are
// exercised) and whose outcomes span every reason bucket plus kept rows.
func seedDrainParallelFixture(t *testing.T, n int) *Engine {
	t.Helper()
	var books []drainBook
	for i := range 40 {
		id := fmt.Sprintf("BK%02d", i)
		b := drainBook{id: id, title: "A Real Book", duration: new(3600)}
		switch i % 8 {
		case 1:
			b.title = "Opening Credits" // boilerplate_title
		case 2:
			b.duration = new(30) // short_duration
		case 3:
			b.isbn13 = new(fmt.Sprintf("97811111111%02d", i)) // identifier_conflict vs case 4
		case 4:
			b.isbn13 = new(fmt.Sprintf("97822222222%02d", i))
		case 5:
			continue // missing_book: never registered
		}
		books = append(books, b)
	}
	engine, es := setupDrainTest(t, books)
	// n DISTINCT pairs (the store dedups by pair), interleaved so books recur
	// on many pages rather than clustering on one.
	seeded := 0
	for gap := 1; gap < 40 && seeded < n; gap++ {
		for i := 0; i+gap < 40 && seeded < n; i++ {
			seedDrainCandidate(t, es, fmt.Sprintf("BK%02d", i), fmt.Sprintf("BK%02d", i+gap))
			seeded++
		}
	}
	if seeded != n {
		t.Fatalf("seeded %d pairs; want %d", seeded, n)
	}
	return engine
}

// The pooled prefetch must produce exactly the serial result: same counts,
// same reason buckets, same samples in the same (candidate) order. Run under
// -race this also proves the pool shares no unsynchronised state.
func TestDrainStale_ParallelPrefetchMatchesSerial(t *testing.T) {
	oldBatch, oldWorkers := drainStaleBatchSize, drainStaleWorkers
	t.Cleanup(func() { drainStaleBatchSize, drainStaleWorkers = oldBatch, oldWorkers })
	drainStaleBatchSize = 7

	run := func(workers int, statusIndex bool) *DrainStaleResult {
		drainStaleWorkers = workers
		engine := seedDrainParallelFixture(t, 120)
		if statusIndex {
			// Prod reads through the dedup:s: status index; a fresh test store
			// has the flag unset and takes the full dedup:r: scan.
			if err := engine.embedStore.SetCandidateStatusIndexBuilt(); err != nil {
				t.Fatalf("SetCandidateStatusIndexBuilt: %v", err)
			}
		}
		res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
		if err != nil {
			t.Fatalf("DrainStaleCandidates(workers=%d): %v", workers, err)
		}
		return res
	}

	serial := run(1, false)
	if serial.Inspected < 100 || serial.WouldPurge == 0 || serial.Kept == 0 {
		t.Fatalf("fixture too thin: %+v", serial)
	}
	for _, reason := range []string{drainReasonMissingBook, drainReasonBoilerplateTitle, drainReasonShortDuration, drainReasonIdentifierConflict} {
		if serial.ReasonCounts[reason] == 0 {
			t.Fatalf("fixture never hits %s: %v", reason, serial.ReasonCounts)
		}
	}
	for reason, samples := range serial.Samples {
		for i := 1; i < len(samples); i++ {
			if samples[i].CandidateID <= samples[i-1].CandidateID {
				t.Fatalf("%s samples out of candidate order: %v", reason, samples)
			}
		}
	}

	for _, statusIndex := range []bool{false, true} {
		for _, workers := range []int{1, 4, 16} {
			for range 3 {
				if got := run(workers, statusIndex); !reflect.DeepEqual(got, serial) {
					t.Fatalf("workers=%d statusIndex=%v result differs from serial:\n got  %+v\n want %+v",
						workers, statusIndex, got, serial)
				}
			}
		}
	}
}

// A book or book-file READ ERROR (not a missing book) keeps the pair and is
// counted, instead of landing it in missing_book or stripping its content
// evidence.
func TestDrainStale_ReadErrorKeepsPair(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_B", title: "Opening Credits", duration: new(3600)}, // boilerplate → would purge
		{id: "BOOK_FERR", title: "A Real Book", duration: new(3600)},
		{id: "BOOK_C", title: "Opening Credits", duration: new(3600)}, // control → purge
		{id: "BOOK_D", title: "A Real Book", duration: new(3600)},
	})
	mock := engine.bookStore.(*database.MockStore)
	innerBook, innerFiles := mock.GetBookByIDFunc, mock.GetBookFilesFunc
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_ERR" {
			return nil, errors.New("pebble: transient read failure")
		}
		return innerBook(id)
	}
	mock.GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
		if id == "BOOK_FERR" {
			return nil, errors.New("pebble: transient read failure")
		}
		return innerFiles(id)
	}
	seedDrainCandidate(t, es, "BOOK_ERR", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_FERR", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_C", "BOOK_D")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false, nil)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 3 || res.LookupErrors != 2 || res.Kept != 2 || res.WouldPurge != 1 {
		t.Fatalf("inspected %d lookupErrors %d kept %d wouldPurge %d; want 3/2/2/1",
			res.Inspected, res.LookupErrors, res.Kept, res.WouldPurge)
	}
	if res.ReasonCounts[drainReasonMissingBook] != 0 {
		t.Fatalf("a read error was classified missing_book: %v", res.ReasonCounts)
	}
}
