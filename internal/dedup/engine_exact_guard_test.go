// file: internal/dedup/engine_exact_guard_test.go
// version: 1.1.0
// guid: 4a1e6d3f-9c72-4a0b-8e35-1f6b2c7d9e40
// last-edited: 2026-07-11

// Regression guard for DEDUP-INTRO-1 (residual): upsertExactCandidate is the
// shared chokepoint for every exact-family emitter. It must reject pairs
// where either book has a boilerplate publisher-intro/outro title, and pairs
// where either book has a known, positive Duration under
// minFingerprintMatchSeconds — while still allowing genuine duplicates
// (normal titles, durations well above the threshold) through.
//
// INIT-2 T3 (2026-07-11) added the gate-parity + anti-over-suppression tests
// below: TestUpsertExactCandidateGateParityWithDrain proves
// DrainStaleCandidates' guard chain mirrors upsertExactCandidate's
// gate-for-gate (including the non_primary_version twin added by this task),
// TestExactEmitHappyPathSurvives proves a known-good duplicate still emits
// and is kept by both paths, TestUpsertExactCandidate_PairDedupeConfirmed
// confirms (without rebuilding) UpsertCandidateNew's existing pair-level
// dedupe, and TestUpsertExactCandidate_NilDurationConservativeBothGates
// locks in nil/unknown-duration conservatism across both duration-sensitive
// gates.
package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestUpsertExactCandidate_BoilerplateTitleGuard(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "This is Audible")
	b := primaryBook("B", "This is Audible")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for boilerplate-title pair, got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_MinDurationGuard(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	shortDur := 30
	a.Duration = &shortDur
	b := primaryBook("B", "Real Book Title")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for short-duration pair, got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_UnknownDurationNotSuppressed(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	a.Duration = nil
	b := primaryBook("B", "Real Book Title")
	b.Duration = nil

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Errorf("expected 1 candidate for unknown-duration pair (no over-suppression), got %d: %+v", len(cands), cands)
	}
}

func TestUpsertExactCandidate_GenuineDuplicateStillPersisted(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Genuine Duplicate Book")
	b := primaryBook("B", "Genuine Duplicate Book")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Fatalf("expected exactly 1 candidate for genuine duplicate pair, got %d: %+v", len(cands), cands)
	}
	if cands[0].EntityAID != "A" || cands[0].EntityBID != "B" {
		t.Errorf("unexpected candidate pair: %+v", cands[0])
	}
}

// TestUpsertExactCandidate_ZeroDurationNotSuppressed locks in the "<=0 means
// unknown" convention: a zero Duration must not be mistaken for a genuinely
// short clip.
func TestUpsertExactCandidate_ZeroDurationNotSuppressed(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	zero := 0
	a.Duration = &zero
	b := primaryBook("B", "Real Book Title")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Errorf("expected 1 candidate for zero-duration pair (treated as unknown), got %d: %+v", len(cands), cands)
	}
}

// TestUpsertExactCandidateGateParityWithDrain is the INIT-2 T3 acceptance
// test: for every gate in upsertExactCandidate's chain (non-primary-version
// skip, identifiersConflict, isBoilerplateTitle, hasKnownShortDuration,
// isPartVsWholeMismatch), a pair rejected by the chokepoint must be
// classified would-purge by DrainStaleCandidates with the MATCHING reason
// bucket, and a pair that passes every gate must be emitted by the
// chokepoint AND kept by the drain. This proves DrainStaleCandidates' gate
// chain mirrors upsertExactCandidate's gate-for-gate, in the same order.
func TestUpsertExactCandidateGateParityWithDrain(t *testing.T) {
	short := 30

	type pairCase struct {
		name       string
		a, b       *database.Book
		files      map[string][]database.BookFile // bookID -> files, for part-vs-whole only
		wantReason string                          // "" = kept by both chokepoint and drain
	}

	isbnA, isbnB := "9781111111111", "9782222222222"

	cases := []pairCase{
		{
			name:       "non_primary_version",
			a:          primaryBook("A", "Real Title"),
			b:          nonPrimaryBook("B", "Real Title"),
			wantReason: drainReasonNonPrimaryVersion,
		},
		{
			name: "identifier_conflict",
			a: func() *database.Book {
				b := primaryBook("A", "Real Title")
				b.ISBN13 = &isbnA
				return b
			}(),
			b: func() *database.Book {
				b := primaryBook("B", "Real Title")
				b.ISBN13 = &isbnB
				return b
			}(),
			wantReason: drainReasonIdentifierConflict,
		},
		{
			name:       "boilerplate_title",
			a:          primaryBook("A", "This is Audible"),
			b:          primaryBook("B", "This is Audible"),
			wantReason: drainReasonBoilerplateTitle,
		},
		{
			name: "short_duration",
			a: func() *database.Book {
				b := primaryBook("A", "Real Title")
				b.Duration = &short
				return b
			}(),
			b:          primaryBook("B", "Real Title"),
			wantReason: drainReasonShortDuration,
		},
		{
			name: "part_vs_whole",
			a:    primaryBook("A", "Real Title"),
			b:    primaryBook("B", "Real Title"),
			files: map[string][]database.BookFile{
				"A": {{ID: "FA1", BookID: "A", Duration: 100}},
				"B": {
					{ID: "FB1", BookID: "B", Duration: 500},
					{ID: "FB2", BookID: "B", Duration: 500},
				},
			},
			wantReason: drainReasonPartVsWhole,
		},
		{
			name:       "kept_genuine_duplicate",
			a:          primaryBook("A", "Genuine Duplicate Book"),
			b:          primaryBook("B", "Genuine Duplicate Book"),
			wantReason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// --- Chokepoint side: call upsertExactCandidate directly. ---
			engine, mock, es := setupTestEngine(t)
			if tc.files != nil {
				mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
					return tc.files[bookID], nil
				}
			}
			if err := engine.upsertExactCandidate(tc.a, tc.b, "exact", 1.0); err != nil {
				t.Fatalf("upsertExactCandidate: %v", err)
			}
			chokepointCands := pendingCandidates(t, es)
			if tc.wantReason == "" {
				if len(chokepointCands) != 1 {
					t.Fatalf("chokepoint: expected 1 candidate (kept case), got %d: %+v", len(chokepointCands), chokepointCands)
				}
			} else if len(chokepointCands) != 0 {
				t.Fatalf("chokepoint: expected 0 candidates (rejected by %s), got %d: %+v", tc.name, len(chokepointCands), chokepointCands)
			}

			// --- Drain side: fresh engine/store; seed the pending candidate
			// directly since the chokepoint may have refused to create it in
			// the reject cases. ---
			dEngine, dMock, dEs := setupTestEngine(t)
			books := map[string]*database.Book{tc.a.ID: tc.a, tc.b.ID: tc.b}
			dMock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
			if tc.files != nil {
				dMock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
					return tc.files[bookID], nil
				}
			}
			if err := dEs.UpsertCandidate(database.DedupCandidate{
				EntityType: "book",
				EntityAID:  tc.a.ID,
				EntityBID:  tc.b.ID,
				Layer:      "exact",
				Status:     "pending",
			}); err != nil {
				t.Fatalf("seed drain candidate: %v", err)
			}

			res, err := dEngine.DrainStaleCandidates(context.Background(), "", false)
			if err != nil {
				t.Fatalf("DrainStaleCandidates: %v", err)
			}
			if tc.wantReason == "" {
				if res.WouldPurge != 0 || res.Kept != 1 {
					t.Fatalf("drain: expected kept (0 would-purge), got wouldPurge=%d kept=%d reasons=%v", res.WouldPurge, res.Kept, res.ReasonCounts)
				}
			} else if res.WouldPurge != 1 || res.ReasonCounts[tc.wantReason] != 1 {
				t.Fatalf("drain: expected would-purge with reason %s, got wouldPurge=%d reasons=%v", tc.wantReason, res.WouldPurge, res.ReasonCounts)
			}
		})
	}
}

// TestExactEmitHappyPathSurvives is the anti-over-suppression proof required
// by INIT-2 T3: a known-good duplicate pair (plausible audio on both sides —
// real duration, distinct folders, no identifier conflict, no boilerplate
// title, no part-vs-whole mismatch) must still emit a candidate through the
// full exact-title emission path (CheckBook -> checkExactTitle ->
// upsertExactCandidate, not just the chokepoint called in isolation), and
// that candidate must be KEPT (not would-purge) by DrainStaleCandidates. If
// the gate-parity fix in this task over-suppressed, this test would catch
// it.
func TestExactEmitHappyPathSurvives(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	dur := 3600
	authorID := 1
	primary := true
	a := &database.Book{
		ID: "HAPPY_A", Title: "The Great Adventure", AuthorID: &authorID,
		Duration: &dur, IsPrimaryVersion: &primary, FilePath: "/library/author/book-a/file.m4b",
	}
	b := &database.Book{
		ID: "HAPPY_B", Title: "The Great Adventure", AuthorID: &authorID,
		Duration: &dur, IsPrimaryVersion: &primary, FilePath: "/library/author/book-b/file.m4b",
	}
	byAuthor := []database.Book{*a, *b}
	wireExactTitleOnly(mock, map[string]*database.Book{"HAPPY_A": a, "HAPPY_B": b}, byAuthor)

	for _, id := range []string{"HAPPY_A", "HAPPY_B"} {
		if _, err := engine.CheckBook(context.Background(), id); err != nil {
			t.Fatalf("CheckBook(%s): %v", id, err)
		}
	}

	cands := pendingCandidates(t, es)
	found := false
	for _, c := range cands {
		if (c.EntityAID == "HAPPY_A" && c.EntityBID == "HAPPY_B") || (c.EntityAID == "HAPPY_B" && c.EntityBID == "HAPPY_A") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a known-good duplicate pair to still emit a candidate, got: %+v", cands)
	}

	// Feed the same pair through the drain and confirm it is KEPT, not
	// would-purge — proves the gate-parity fix does not over-suppress a
	// legitimate duplicate.
	dEngine, dMock, dEs := setupTestEngine(t)
	books := map[string]*database.Book{"HAPPY_A": a, "HAPPY_B": b}
	dMock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	dMock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }
	if err := dEs.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "HAPPY_A", EntityBID: "HAPPY_B", Layer: "exact", Status: "pending",
	}); err != nil {
		t.Fatalf("seed drain candidate: %v", err)
	}
	res, err := dEngine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 0 || res.Kept != 1 {
		t.Fatalf("drain over-suppressed a known-good duplicate: wouldPurge=%d kept=%d reasons=%v", res.WouldPurge, res.Kept, res.ReasonCounts)
	}
}

// TestUpsertExactCandidate_PairDedupeConfirmed confirms (does not rebuild)
// the pair-level dedupe already implemented in
// EmbeddingStore.UpsertCandidateNew: two upsertExactCandidate calls for the
// SAME pair — including with A/B swapped — must produce exactly one stored
// candidate row, not two, because UpsertCandidateNew canonicalizes A/B order
// and point-reads dedupPairKey before insert.
func TestUpsertExactCandidate_PairDedupeConfirmed(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	a := primaryBook("A", "Genuine Duplicate Book")
	b := primaryBook("B", "Genuine Duplicate Book")

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate(a,b): %v", err)
	}
	// Reversed argument order — UpsertCandidateNew canonicalizes A/B so this
	// must still land on the SAME pair key, not a second row.
	if err := engine.upsertExactCandidate(b, a, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate(b,a): %v", err)
	}

	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Fatalf("expected exactly 1 stored candidate for the same pair called twice, got %d: %+v", len(cands), cands)
	}
}

// TestUpsertExactCandidate_NilDurationConservativeBothGates locks in nil/
// unknown-duration conservatism across BOTH duration-sensitive gates
// (hasKnownShortDuration and isPartVsWholeMismatch): a pair with unknown
// Book.Duration on both sides, and a single BookFile each (so neither side
// has the >=2-file "whole" shape isPartVsWholeMismatch looks for), must
// still emit through the chokepoint AND be kept by the drain — proving
// neither gate's nil-conservative default was flipped by this task's
// changes.
func TestUpsertExactCandidate_NilDurationConservativeBothGates(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	a := primaryBook("A", "Real Book Title")
	a.Duration = nil
	b := primaryBook("B", "Real Book Title")
	b.Duration = nil

	files := map[string][]database.BookFile{
		"A": {{ID: "FA1", BookID: "A", Duration: 100}},
		"B": {{ID: "FB1", BookID: "B", Duration: 100}},
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return files[bookID], nil }

	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("upsertExactCandidate: %v", err)
	}
	cands := pendingCandidates(t, es)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate for nil-duration pair (no over-suppression), got %d: %+v", len(cands), cands)
	}

	// Drain-side parity: the same pair, re-seeded, must be KEPT.
	dEngine, dMock, dEs := setupTestEngine(t)
	books := map[string]*database.Book{"A": a, "B": b}
	dMock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	dMock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return files[bookID], nil }
	if err := dEs.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "A", EntityBID: "B", Layer: "exact", Status: "pending",
	}); err != nil {
		t.Fatalf("seed drain candidate: %v", err)
	}
	res, err := dEngine.DrainStaleCandidates(context.Background(), "", false)
	if err != nil {
		t.Fatalf("DrainStaleCandidates: %v", err)
	}
	if res.WouldPurge != 0 || res.Kept != 1 {
		t.Fatalf("drain over-suppressed nil-duration pair: wouldPurge=%d kept=%d reasons=%v", res.WouldPurge, res.Kept, res.ReasonCounts)
	}
}
