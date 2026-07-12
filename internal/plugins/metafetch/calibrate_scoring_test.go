// file: internal/plugins/metafetch/calibrate_scoring_test.go
// version: 1.0.0
// guid: b3e6a1c8-9d24-4f57-8a06-1c2d3e4f5a61
// last-edited: 2026-07-11

package metafetch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	sdk "github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ---------------------------------------------------------------------------
// Pin: the harness core must reproduce the real scorer bit-for-bit at defaults
// ---------------------------------------------------------------------------

func fieldsFromBookMetadata(bm metadata.BookMetadata) candFields {
	return candFields{
		Title:          bm.Title,
		Author:         bm.Author,
		Narrator:       bm.Narrator,
		Description:    bm.Description,
		CoverURL:       bm.CoverURL,
		ISBN:           bm.ISBN,
		Series:         bm.Series,
		SeriesPosition: bm.SeriesPosition,
		DurationSec:    bm.DurationSec,
	}
}

// TestScoreCore_PinnedToProductionScorer locks the harness's f1-base + non-base
// core against the real exported metafetch.ScoreOneResult at default knobs. If
// the extracted formulas (compilation phrases, rich-bonus cap, length penalty,
// f1) ever drift, this fails. config.AppConfig is zero-value in this test binary
// so ScoreOneResult resolves the same default literals defaultSweepKnobs encodes.
func TestScoreCore_PinnedToProductionScorer(t *testing.T) {
	searchTitle := "The Way of Kings"
	words := metafetch.SignificantWords(searchTitle)
	k := defaultSweepKnobs()

	cases := []metadata.BookMetadata{
		// Exact title, rich metadata (bonus should cap at 0.15).
		{Title: "The Way of Kings", Author: "Brandon Sanderson", Narrator: "Michael Kramer", Description: "epic", CoverURL: "http://c", ISBN: "9780765326355"},
		// Compilation title exercises the compilation-penalty path.
		{Title: "The Stormlight Archive Box Set", Description: "collection"},
		// Partial overlap.
		{Title: "Way of Kings Prime"},
		// Zero overlap → base 0 → score 0 (early return).
		{Title: "Completely Unrelated Memoir"},
		// Numeric-compilation regex path ("3 books").
		{Title: "Mistborn 3 books omnibus"},
	}

	for i, bm := range cases {
		want := metafetch.ScoreOneResult(bm, words)
		got := scoreCore(fieldsFromBookMetadata(bm), words, k)
		if got != want {
			t.Fatalf("case %d %q: scoreCore=%.10f, want ScoreOneResult=%.10f", i, bm.Title, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// evaluateBook: known ranks + unmatchable skip counting
// ---------------------------------------------------------------------------

func intPtr(i int) *int         { return &i }
func strPtr(s string) *string   { return &s }
func sha256Hex(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func TestEvaluateBook_AppliedRanksFirst(t *testing.T) {
	applied := metafetch.MetadataCandidate{Title: "The Way of Kings", Author: "Sanderson", Source: "audible", ASIN: "B00INE24E4", DurationSec: 1000, CoverURL: "http://c", Description: "d"}
	distractor := metafetch.MetadataCandidate{Title: "Some Other Book", Source: "google", ISBN: "9999999999"}

	hash := candidateSourceHash(applied)
	if hash == "" {
		t.Fatal("applied candidate must have a canonical id / source hash")
	}
	book := &database.Book{ID: "b1", Title: "The Way of Kings", Duration: intPtr(1000), MetadataSourceHash: &hash}

	grid := buildSweepGrid(defaultSweepKnobs(), 3)
	ev, skip := evaluateBook(book, []metafetch.MetadataCandidate{applied, distractor}, "auto", defaultSweepKnobs(), grid)
	if skip != "" {
		t.Fatalf("unexpected skip %q", skip)
	}
	if ev.currentRank != 1 {
		t.Fatalf("applied currentRank = %d, want 1", ev.currentRank)
	}
	if len(ev.sweepRanks) != len(grid) {
		t.Fatalf("sweepRanks len = %d, want %d", len(ev.sweepRanks), len(grid))
	}
}

// TestEvaluateBook_DurationDemotesApplied gives a same-title distractor a better
// duration match than the applied candidate, so the applied candidate ranks #2
// under the current knobs — a known, duration-driven rank.
func TestEvaluateBook_DurationDemotesApplied(t *testing.T) {
	// Applied: exact title but a wildly wrong duration (ratio > 1 → ×0.50).
	applied := metafetch.MetadataCandidate{Title: "Elantris", Source: "audible", ASIN: "B00ELANTRIS", DurationSec: 100}
	// Distractor: exact title AND a perfect duration match (×1.30, +0.2).
	distractor := metafetch.MetadataCandidate{Title: "Elantris", Source: "google", ISBN: "1234567890", DurationSec: 2000}

	hash := candidateSourceHash(applied)
	book := &database.Book{ID: "b2", Title: "Elantris", Duration: intPtr(2000), MetadataSourceHash: &hash}

	grid := buildSweepGrid(defaultSweepKnobs(), 2)
	ev, skip := evaluateBook(book, []metafetch.MetadataCandidate{applied, distractor}, "manual", defaultSweepKnobs(), grid)
	if skip != "" {
		t.Fatalf("unexpected skip %q", skip)
	}
	if ev.currentRank != 2 {
		t.Fatalf("applied currentRank = %d, want 2 (distractor has the better duration match)", ev.currentRank)
	}
}

func TestEvaluateBook_Unmatchable(t *testing.T) {
	c1 := metafetch.MetadataCandidate{Title: "A", Source: "audible", ASIN: "B00AAA"}
	c2 := metafetch.MetadataCandidate{Title: "B", Source: "google", ISBN: "111"}
	// Hash that matches NO cached candidate.
	book := &database.Book{ID: "b3", Title: "A", MetadataSourceHash: strPtr("deadbeefdeadbeef")}

	grid := buildSweepGrid(defaultSweepKnobs(), 2)
	ev, skip := evaluateBook(book, []metafetch.MetadataCandidate{c1, c2}, "auto", defaultSweepKnobs(), grid)
	if skip != "unmatchable" {
		t.Fatalf("skip = %q, want unmatchable", skip)
	}
	if ev != nil {
		t.Fatalf("expected nil eval on unmatchable, got %+v", ev)
	}
}

// ---------------------------------------------------------------------------
// buildReport: shape, segmentation, verbatim caveat, circular-bias flag
// ---------------------------------------------------------------------------

func TestBuildReport_ShapeSegmentationCaveat(t *testing.T) {
	grid := buildSweepGrid(defaultSweepKnobs(), 3)
	nKnobs := len(knobSetters(defaultSweepKnobs()))

	mk := func(origin string, cur int) *bookEval {
		sr := make([]int, len(grid))
		for i := range sr {
			sr[i] = cur
		}
		return &bookEval{origin: origin, currentRank: cur, sweepRanks: sr}
	}
	evals := []*bookEval{
		mk("manual", 1),
		mk("manual", 2),
		mk("auto", 1),
		mk("auto", 1),
	}

	rep := buildReport(evals, grid, map[string]int{"unmatchable": 5}, 4, 3)

	if rep.CircularityCaveat != circularityCaveat || rep.CircularityCaveat == "" {
		t.Fatal("report must carry the verbatim circularity caveat")
	}
	if !rep.ManualSegmentDetermined {
		t.Fatal("manual segment present → ManualSegmentDetermined must be true")
	}
	if rep.CircularBiased {
		t.Fatal("with a manual segment the whole sweep must NOT be flagged circular-biased")
	}
	if rep.Evaluated != 4 {
		t.Fatalf("Evaluated = %d, want 4", rep.Evaluated)
	}
	man, ok := rep.Segments["manual"]
	if !ok || man.N != 2 || man.Top1Accuracy != 0.5 {
		t.Fatalf("manual segment = %+v, want N=2 top1=0.5", man)
	}
	auto, ok := rep.Segments["auto"]
	if !ok || auto.N != 2 || auto.Top1Accuracy != 1.0 {
		t.Fatalf("auto segment = %+v, want N=2 top1=1.0", auto)
	}
	if rep.CurrentKnobTop1Accuracy != 0.75 {
		t.Fatalf("current top1 = %.3f, want 0.75", rep.CurrentKnobTop1Accuracy)
	}
	if len(rep.Sweep) != nKnobs {
		t.Fatalf("sweep knob count = %d, want %d", len(rep.Sweep), nKnobs)
	}
	for _, ks := range rep.Sweep {
		if len(ks.Points) != 3 {
			t.Fatalf("knob %s has %d points, want 3", ks.Knob, len(ks.Points))
		}
	}
	if rep.SkipCounts["unmatchable"] != 5 {
		t.Fatalf("skip counts not carried: %+v", rep.SkipCounts)
	}
}

func TestBuildReport_NoManualSegmentIsCircularBiased(t *testing.T) {
	grid := buildSweepGrid(defaultSweepKnobs(), 2)
	sr := make([]int, len(grid))
	evals := []*bookEval{{origin: "auto", currentRank: 1, sweepRanks: sr}}

	rep := buildReport(evals, grid, map[string]int{}, 1, 2)
	if rep.ManualSegmentDetermined {
		t.Fatal("no manual eval → ManualSegmentDetermined must be false")
	}
	if !rep.CircularBiased {
		t.Fatal("no manual segment → whole sweep must be flagged circular-biased")
	}
}

func TestBuildReport_EmptyEvalSet(t *testing.T) {
	grid := buildSweepGrid(defaultSweepKnobs(), 2)
	rep := buildReport(nil, grid, map[string]int{"no_cache": 3}, 0, 2)
	if rep.Evaluated != 0 || !rep.CircularBiased || rep.ManualSegmentDetermined {
		t.Fatalf("empty eval set: got evaluated=%d circular=%t manual=%t", rep.Evaluated, rep.CircularBiased, rep.ManualSegmentDetermined)
	}
	if rep.CircularityCaveat == "" {
		t.Fatal("empty eval set still must carry the caveat")
	}
	if len(rep.Sweep) == 0 {
		t.Fatal("empty eval set should still enumerate the sweep knobs")
	}
}

// TestCandidateSourceHash_MatchesApplyFormula pins the ground-truth identity to
// service_apply.go's sha256("{source}:{canonical_id}") format and ASIN>ISBN
// precedence — a format near-miss would silently zero the match rate.
func TestCandidateSourceHash_MatchesApplyFormula(t *testing.T) {
	// ASIN wins over ISBN.
	got := candidateSourceHash(metafetch.MetadataCandidate{Source: "audible", ASIN: "B0123", ISBN: "9780765326355"})
	// Recompute independently the way service_apply.go does.
	want := sha256Hex("audible:B0123")
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
	// No canonical id → empty hash (could never have produced a MetadataSourceHash).
	if h := candidateSourceHash(metafetch.MetadataCandidate{Source: "audible", Title: "x"}); h != "" {
		t.Fatalf("expected empty hash with no id, got %q", h)
	}
}

// ---------------------------------------------------------------------------
// -race: the pooled replay loop
// ---------------------------------------------------------------------------

type stubReporter struct{}

func (stubReporter) UpdateProgress(int, int, string) error { return nil }
func (stubReporter) Log(slog.Level, string, ...slog.Attr) error {
	return nil
}
func (stubReporter) Logger() *slog.Logger { return slog.Default() }
func (stubReporter) Checkpoint(any) error { return nil }
func (stubReporter) IsCanceled() bool     { return false }
func (stubReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, stubReporter{})
}
func (stubReporter) Trigger(context.Context, string, any) error { return nil }
func (stubReporter) SetCurrentItem(string)                      {}

func mustCands(t *testing.T, cs ...metafetch.MetadataCandidate) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(cs))
	for _, c := range cs {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// TestRunCalibrateScoring_Race exercises the full read-only op through the
// bounded RunItems pool under the race detector with a mixed eval set (matched,
// empty cache, unmatchable, manual origin).
func TestRunCalibrateScoring_Race(t *testing.T) {
	applied := metafetch.MetadataCandidate{Title: "Warbreaker", Source: "audible", ASIN: "B00WARBRK", DurationSec: 3000}
	distract := metafetch.MetadataCandidate{Title: "Something Else", Source: "google", ISBN: "111"}
	appliedHash := candidateSourceHash(applied)

	books := []database.Book{
		{ID: "m1", Title: "Warbreaker", Duration: intPtr(3000), MetadataSourceHash: &appliedHash}, // matched, manual origin
		{ID: "m2", Title: "Warbreaker", Duration: intPtr(3000), MetadataSourceHash: &appliedHash}, // matched, auto origin
		{ID: "e3", Title: "Empty", MetadataSourceHash: strPtr("someotherhash")},                   // empty cache
		{ID: "u4", Title: "Unmatch", MetadataSourceHash: strPtr("nomatchhash")},                   // unmatchable
		{ID: "x5", Title: "NoHash"}, // not applied — skipped silently
	}

	caches := map[string]*database.MetadataCandidateCache{
		"m1": {BookID: "m1", Candidates: mustCands(t, applied, distract)},
		"m2": {BookID: "m2", Candidates: mustCands(t, applied, distract)},
		"e3": {BookID: "e3", Candidates: nil},
		"u4": {BookID: "u4", Candidates: mustCands(t, distract)},
	}

	store := &database.MockStore{}
	store.GetAllBooksFullFromFunc = func(afterID string, _ int) ([]database.Book, error) {
		if afterID == "" {
			return books, nil // single short page → loop terminates
		}
		return nil, nil
	}
	store.GetMetadataCacheFunc = func(bookID string) (*database.MetadataCandidateCache, error) {
		return caches[bookID], nil
	}
	store.GetMetadataFieldStatesFunc = func(bookID string) ([]database.MetadataFieldState, error) {
		if bookID == "m1" {
			return []database.MetadataFieldState{{Field: "title", OverrideValue: strPtr(`"x"`)}}, nil
		}
		return nil, nil
	}

	p := New(store, metafetch.NewService(store))
	if err := p.runCalibrateScoring(context.Background(), nil, stubReporter{}); err != nil {
		t.Fatalf("runCalibrateScoring: %v", err)
	}
}
