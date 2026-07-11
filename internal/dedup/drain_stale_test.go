// file: internal/dedup/drain_stale_test.go
// version: 1.1.0
// guid: 6b8c9a6c-b168-4fb9-ba23-99937427b562
// last-edited: 2026-07-11

// Tests for Engine.DrainStaleCandidates (DEDUP-1 / CONS-16 / CONS-17).
//
// The engine method re-runs pending exact candidates through the current guard
// chain and buckets would-purge rows by rejecting reason. These tests plant
// candidates in a real in-memory EmbeddingStore and wire book lookups via the
// MockStore, then assert counts, sample buckets, and the dry-run/apply contract.

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// drainBook is a compact spec for a book fixture in these tests.
type drainBook struct {
	id               string
	title            string
	duration         *int
	isbn13           *string
	files            []database.BookFile
	isPrimaryVersion *bool // nil = unknown/conservative (never non-primary)
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
		{id: "BOOK_A", title: "Opening Credits", duration: intPtr(3600)},
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
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
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(30)}, // < minFingerprintMatchSeconds (60)
		{id: "BOOK_B", title: "Another Real Book", duration: intPtr(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonShortDuration] != 1 {
		t.Fatalf("short_duration bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

func TestDrainStale_IdentifierConflict(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(3600), isbn13: strPtr("9781111111111")},
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600), isbn13: strPtr("9782222222222")},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonIdentifierConflict] != 1 {
		t.Fatalf("identifier_conflict bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

func TestDrainStale_PartVsWhole(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(3600), files: []database.BookFile{
			{ID: "FA1", BookID: "BOOK_A", Duration: 100},
		}},
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600), files: []database.BookFile{
			{ID: "FB1", BookID: "BOOK_B", Duration: 500},
			{ID: "FB2", BookID: "BOOK_B", Duration: 500},
		}},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
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
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(3600), isPrimaryVersion: &primary},
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600), isPrimaryVersion: &nonPrimary},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
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
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(3600)}, // isPrimaryVersion left nil
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600)}, // isPrimaryVersion left nil
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.Inspected != 1 || res.WouldPurge != 0 || res.Kept != 1 {
		t.Fatalf("nil-primary-flag pair over-suppressed: inspected %d, wouldPurge %d, kept %d (want 1/0/1)", res.Inspected, res.WouldPurge, res.Kept)
	}
}

func TestDrainStale_MissingBook(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(3600)},
		// BOOK_GONE is intentionally absent → GetBookByID returns (nil, nil).
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_GONE")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 1 || res.ReasonCounts[drainReasonMissingBook] != 1 {
		t.Fatalf("missing_book bucket wrong: wouldPurge %d, reasons %v", res.WouldPurge, res.ReasonCounts)
	}
}

func TestDrainStale_KeptWhenStillValid(t *testing.T) {
	engine, es := setupDrainTest(t, []drainBook{
		{id: "BOOK_A", title: "A Real Book", duration: intPtr(3600)},
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
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
		{id: "BOOK_A", title: "Opening Credits", duration: intPtr(3600)},
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")

	if _, err := engine.DrainStaleCandidates(context.Background(), "", false); err != nil {
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
		{id: "BOOK_A", title: "Opening Credits", duration: intPtr(3600)}, // boilerplate → purge
		{id: "BOOK_B", title: "A Real Book", duration: intPtr(3600)},
		{id: "BOOK_C", title: "A Real Book", duration: intPtr(3600)}, // C+D still valid → kept
		{id: "BOOK_D", title: "A Real Book", duration: intPtr(3600)},
	})
	seedDrainCandidate(t, es, "BOOK_A", "BOOK_B")
	seedDrainCandidate(t, es, "BOOK_C", "BOOK_D")

	res, err := engine.DrainStaleCandidates(context.Background(), "", true)
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
	for i := 0; i < pairs; i++ {
		aID := "PA" + string(rune('a'+i))
		bID := "PB" + string(rune('a'+i))
		// Every pair has a boilerplate side → all would-purge.
		books = append(books,
			drainBook{id: aID, title: "Opening Credits", duration: intPtr(3600)},
			drainBook{id: bID, title: "A Real Book", duration: intPtr(3600)},
		)
	}
	engine, es := setupDrainTest(t, books)
	for i := 0; i < pairs; i++ {
		seedDrainCandidate(t, es, "PA"+string(rune('a'+i)), "PB"+string(rune('a'+i)))
	}

	res, err := engine.DrainStaleCandidates(context.Background(), "", false)
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
		"BOOK_A": {ID: "BOOK_A", Title: "A Real Book", Duration: intPtr(3600)},
		"BOOK_B": {ID: "BOOK_B", Title: "A Real Book", Duration: intPtr(3600)},
		"BOOK_C": {ID: "BOOK_C", Title: "A Real Book", Duration: intPtr(3600)},
		"BOOK_D": {ID: "BOOK_D", Title: "A Real Book", Duration: intPtr(3600)},
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

	res, err := engine.DrainStaleCandidates(context.Background(), "op-123", true)
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

	// Now resume from an offset past all rows → nothing inspected.
	drainStaleBatchSizeOld := drainStaleBatchSize
	drainStaleBatchSize = 1
	t.Cleanup(func() { drainStaleBatchSize = drainStaleBatchSizeOld })
	mock.GetOperationStateFunc = func(opID string) ([]byte, error) {
		// Simulate an interrupted apply that already processed both rows.
		return []byte(`{"operation_id":"op-123","type":"scan","phase":"scanning","phase_index":2,"phase_total":2,"status":"interrupted"}`), nil
	}
	res2, err := engine.DrainStaleCandidates(context.Background(), "op-123", true)
	if err != nil {
		t.Fatalf("DrainStaleCandidates resume: %v", err)
	}
	if res2.Inspected != 0 {
		t.Fatalf("resume from offset 2 should inspect 0 rows, inspected %d", res2.Inspected)
	}
}

// TestDrainStale_DryRunIgnoresCheckpoint verifies a dry run never resumes from a
// checkpoint: even with a saved offset present, it full-scans so its report is
// complete.
func TestDrainStale_DryRunIgnoresCheckpoint(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	byID := map[string]*database.Book{
		"BOOK_A": {ID: "BOOK_A", Title: "A Real Book", Duration: intPtr(3600)},
		"BOOK_B": {ID: "BOOK_B", Title: "A Real Book", Duration: intPtr(3600)},
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

	res, err := engine.DrainStaleCandidates(context.Background(), "op-x", false)
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
