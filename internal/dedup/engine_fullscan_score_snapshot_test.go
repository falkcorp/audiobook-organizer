// file: internal/dedup/engine_fullscan_score_snapshot_test.go
// version: 1.0.0
// guid: 9b4e7d2a-1c3f-4e5b-8a6d-2f7c9e0b1d3a
// last-edited: 2026-09-02

package dedup

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// snapshotFixture seeds N books whose titles share nothing (so no Layer-1
// exact emitter fires — an "exact" row is protected in UpsertCandidateNew and
// never receives a band, see the finding in PR #3052's review round) plus a
// pre-existing "embedding"-layer pending candidate for every pair, so the
// unified pass finds a candidate set for each book and composes an
// embedding-only score strictly below 100 — one that a shifted ladder can
// move into a different band.
func snapshotFixture(t *testing.T, mock *database.MockStore, es *database.EmbeddingStore, n int, cosine float64) []database.Book {
	t.Helper()
	dur := 3600
	books := make([]database.Book, n)
	for i := range n {
		// Six copies of one letter then six of another: any two titles differ
		// in at least six positions, far above checkExactTitle's Levenshtein
		// tolerance, so the pair never becomes a protected "exact" row.
		title := strings.Repeat(string(rune('A'+i%26)), 6) + " " + strings.Repeat(string(rune('A'+i/26)), 6)
		books[i] = database.Book{ID: fmt.Sprintf("SN%03d", i), Title: title, Duration: &dur}
	}
	wireFullScanScoreMock(mock, books)
	sim := cosine
	for i := range n {
		for j := i + 1; j < n; j++ {
			if err := es.UpsertCandidate(database.DedupCandidate{
				EntityType: "book", EntityAID: books[i].ID, EntityBID: books[j].ID,
				Layer: "embedding", Similarity: &sim, Status: "pending",
			}); err != nil {
				t.Fatalf("seed candidate %s/%s: %v", books[i].ID, books[j].ID, err)
			}
		}
	}
	return books
}

func bandedCandidates(t *testing.T, es *database.EmbeddingStore) []database.DedupCandidate {
	t.Helper()
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 1_000_000})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	return cands
}

// TestFullScan_ScoreConfigSnapshottedOncePerScan is review finding M3 on
// PR #3052: FullScan's scoring pass runs runUnifiedScoringForBook across
// NumCPU workers, and each worker used to read de.ScoreConfig() for itself.
// A SetScoreConfig landing mid-scan (PUT /api/v1/config, calibrate apply)
// therefore banded the books scored before the swap under one ladder and the
// rest under another, in the same scan, with nothing on the rows saying
// which. The config must be read ONCE per scan.
//
// Method: run the scan under defaults to learn the fixture's composed scores
// and confirm none is CERTAIN; then run a second scan on a fresh store,
// swapping in a ladder that makes EVERY pair CERTAIN from inside the first
// "score" progress callback (i.e. after the first book has been scored, while
// the remaining 39 are still queued). Every persisted row must carry the band
// the scan-start ladder gives its signals. Mutation check: restore the
// per-book `cfg := de.ScoreConfig()` read and the post-swap books band
// CERTAIN under the shifted ladder, failing this.
func TestFullScan_ScoreConfigSnapshottedOncePerScan(t *testing.T) {
	const numBooks = 40
	const cosine = 0.90 // medium tier under the 0.95/0.85 defaults: a mid-ladder score

	// --- Pass 1: learn S and the default band. ---
	engineA, mockA, esA := setupTestEngine(t)
	snapshotFixture(t, mockA, esA, numBooks, cosine)
	if err := engineA.FullScan(context.Background(), nil); err != nil {
		t.Fatalf("reference FullScan: %v", err)
	}
	ref := bandedCandidates(t, esA)
	if len(ref) != numBooks*(numBooks-1)/2 {
		t.Fatalf("fixture: want %d pairs, got %d", numBooks*(numBooks-1)/2, len(ref))
	}
	// Scores vary pair to pair (the fuzzy-title signal fires for pairs whose
	// letter-blocks overlap), so the fixture yields a SPREAD of default bands.
	// The shifted ladder therefore has to move EVERY score, whatever it is:
	// a floor of 5/4/3/2 bands anything ≥ 5 as CERTAIN, and the fixture must
	// contain no CERTAIN under the defaults for that move to be observable.
	defaults := unified.DefaultScoreConfig()
	for _, c := range ref {
		if c.ScoreBreakdown == nil || c.Band == "" {
			t.Fatalf("fixture is vacuous: pair %s/%s has no unified band after FullScan (band=%q breakdown=%v)", c.EntityAID, c.EntityBID, c.Band, c.ScoreBreakdown != nil)
		}
		if c.Band == unified.BandCertain || c.ScoreBreakdown.Score >= defaults.BandCertainMin {
			t.Fatalf("fixture: pair %s/%s is already CERTAIN (%.2f) under defaults; the shifted ladder could not move it", c.EntityAID, c.EntityBID, c.ScoreBreakdown.Score)
		}
		if c.ScoreBreakdown.Score < 5 {
			t.Fatalf("fixture: pair %s/%s scored %.2f, below the shifted ladder's CERTAIN floor", c.EntityAID, c.EntityBID, c.ScoreBreakdown.Score)
		}
	}
	shifted := defaults.Clone()
	shifted.BandCertainMin, shifted.BandHighMin, shifted.BandMediumMin, shifted.BandReviewMin = 5, 4, 3, 2
	if err := shifted.Validate(); err != nil {
		t.Fatalf("shifted ladder invalid: %v", err)
	}
	for _, c := range ref {
		if got := unified.ComposeScore(c.ScoreBreakdown.Signals, nil, shifted, c.ScoreBreakdown.Pair).Band; got != unified.BandCertain {
			t.Fatalf("shifted ladder does not move S=%.2f to CERTAIN (got %s)", c.ScoreBreakdown.Score, got)
		}
	}

	// --- Pass 2: swap the ladder mid-scan. ---
	engineB, mockB, esB := setupTestEngine(t)
	snapshotFixture(t, mockB, esB, numBooks, cosine)
	var (
		swapOnce  sync.Once
		swappedAt int
	)
	err := engineB.FullScan(context.Background(), func(phase string, done, total int) {
		if phase != "score" {
			return
		}
		swapOnce.Do(func() {
			swappedAt = done
			if err := engineB.SetScoreConfig(shifted); err != nil {
				t.Errorf("SetScoreConfig mid-scan: %v", err)
			}
		})
	})
	if err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	if swappedAt == 0 || swappedAt >= numBooks {
		t.Fatalf("swap must land mid-scan; fired at done=%d of %d", swappedAt, numBooks)
	}
	// The swap took: the engine now holds the shifted ladder, so any post-swap
	// per-book re-read WOULD have seen it.
	if live := engineB.ScoreConfig(); live.BandHighMin != shifted.BandHighMin {
		t.Fatalf("SetScoreConfig did not take: live high=%.2f want %.2f", live.BandHighMin, shifted.BandHighMin)
	}

	got := bandedCandidates(t, esB)
	if len(got) != len(ref) {
		t.Fatalf("pair count drifted between passes: %d vs %d", len(got), len(ref))
	}
	mixed := 0
	for _, c := range got {
		if c.ScoreBreakdown == nil {
			t.Fatalf("pair %s/%s lost its breakdown in pass 2", c.EntityAID, c.EntityBID)
		}
		// Every row must carry the band the scan-start ladder (defaults) gives
		// its own stored signals; a CERTAIN here came from the shifted ladder.
		want := unified.ComposeScore(c.ScoreBreakdown.Signals, nil, defaults, c.ScoreBreakdown.Pair).Band
		if c.Band != want {
			mixed++
		}
	}
	if mixed > 0 {
		t.Fatalf("mixed ladders within one scan: %d/%d pairs banded under the ladder swapped in after book %d instead of the scan-start ladder — FullScan must snapshot the score config once per scan",
			mixed, len(got), swappedAt)
	}
}
