// file: internal/dedup/rescore_writeback_test.go
// version: 1.2.0
// guid: 2d7f4a16-08b5-4c93-91ae-5f6c3d20b784
// last-edited: 2026-09-02

package dedup

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/models"
)

// recordingRescoreWriter is the injection seam (PR #3052 follow-up, D2): it
// stands in for *database.EmbeddingStore as Rescore's write target so a test
// can make a re-band FAIL — the WriteErrors > 0 path that ReloadScoreConfig's
// error message reports on had no test at all, because with the concrete store
// there was no way to make an individual row's write fail.
type recordingRescoreWriter struct {
	batches   [][]database.CandidateScoreUpdate
	syncCalls int
	// failIDs are reported back as rows that could not be written.
	failIDs map[int64]bool
	// batchErr, when set, fails whole batches.
	batchErr error
	syncErr  error
}

func (w *recordingRescoreWriter) UpdateCandidateScores(updates []database.CandidateScoreUpdate) (int, []int64, error) {
	cp := append([]database.CandidateScoreUpdate(nil), updates...)
	w.batches = append(w.batches, cp)
	if w.batchErr != nil {
		return 0, nil, w.batchErr
	}
	var failed []int64
	applied := 0
	for _, u := range updates {
		if w.failIDs[u.ID] {
			failed = append(failed, u.ID)
			continue
		}
		applied++
	}
	return applied, failed, nil
}

func (w *recordingRescoreWriter) SyncCandidateWrites() error {
	w.syncCalls++
	return w.syncErr
}

// seedRescorableCandidates writes n pending candidates whose stored breakdown
// scores 95.0 — CERTAIN under a 90 ladder, HIGH under the default 97 — so a
// ladder swap moves every one of them.
func seedRescorableCandidates(t *testing.T, es *database.EmbeddingStore, n int) {
	t.Helper()
	for i := range n {
		sim := 0.95
		bd := &models.UnifiedDedupScore{
			Score:   95.0,
			Band:    unified.BandHigh,
			Formula: "unified_v1",
			Pair:    [2]string{"a", "b"},
			Signals: []unified.Signal{{
				Kind:       unified.SigEmbedHigh,
				Confidence: 0.95,
				Raw:        0.95,
			}},
		}
		if _, _, err := es.UpsertCandidateNew(database.DedupCandidate{
			EntityType:     "book",
			EntityAID:      "book-a-" + strconv.Itoa(i),
			EntityBID:      "book-b-" + strconv.Itoa(i),
			Layer:          "embedding",
			Similarity:     &sim,
			Status:         "pending",
			ScoreBreakdown: bd,
			Band:           unified.BandHigh,
			FormulaVersion: "unified_v1",
		}); err != nil {
			t.Fatalf("UpsertCandidateNew %d: %v", i, err)
		}
	}
}

// TestRescore_BatchesWritesAndSyncsOnce is D4's write-path statement: a
// whole-backlog re-band must not issue one fsync per row (the pattern
// candidateWriteOpts documents as the 2026-07-06 nine-hour production stall).
// Rescore now hands the rows to the store in batches and syncs ONCE at the end.
//
// Mutation check: put the per-row UpdateCandidateScore call back and this
// fails — no batch is ever recorded and SyncCandidateWrites is never called.
func TestRescore_BatchesWritesAndSyncsOnce(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := database.NewEmbeddingStore(store.DB())
	const rows = 12
	seedRescorableCandidates(t, es, rows)

	w := &recordingRescoreWriter{}
	eng.SetRescoreWriter(w)
	// A ladder that pulls the seeded 95.0 rows up from HIGH into CERTAIN.
	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	if err := eng.SetScoreConfig(cfg); err != nil {
		t.Fatalf("SetScoreConfig: %v", err)
	}

	res, err := eng.Rescore(context.Background(), true)
	if err != nil {
		t.Fatalf("Rescore: %v", err)
	}
	if res.Changed != rows {
		t.Fatalf("Changed = %d, want %d (the seeded rows should all move HIGH→CERTAIN)", res.Changed, rows)
	}
	if res.WriteErrors != 0 {
		t.Fatalf("WriteErrors = %d, want 0", res.WriteErrors)
	}
	if len(w.batches) != 1 {
		t.Fatalf("expected 1 batch for %d rows (batch size %d), got %d batches", rows, rescoreWriteBatchSize, len(w.batches))
	}
	if got := len(w.batches[0]); got != rows {
		t.Errorf("batch carried %d rows, want %d", got, rows)
	}
	if w.syncCalls != 1 {
		t.Errorf("SyncCandidateWrites called %d time(s), want exactly 1 for the whole pass", w.syncCalls)
	}
}

// TestRescore_ReportsPerRowWriteFailures: rows the store could not write are
// counted in WriteErrors rather than silently reported as re-banded.
func TestRescore_ReportsPerRowWriteFailures(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := database.NewEmbeddingStore(store.DB())
	seedRescorableCandidates(t, es, 5)

	w := &recordingRescoreWriter{failIDs: map[int64]bool{1: true, 3: true}}
	eng.SetRescoreWriter(w)
	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	if err := eng.SetScoreConfig(cfg); err != nil {
		t.Fatalf("SetScoreConfig: %v", err)
	}

	res, err := eng.Rescore(context.Background(), true)
	if err != nil {
		t.Fatalf("Rescore: %v", err)
	}
	if res.WriteErrors != 2 {
		t.Fatalf("WriteErrors = %d, want 2 (candidate ids 1 and 3 failed)", res.WriteErrors)
	}
}

// TestReloadScoreConfig_PartialRebandIsHonest is D2: when the re-band cannot
// finish, the ladder STAYS live (it is valid — the config layer ran the same
// Validate), the error does not claim the engine rejected anything, and it
// names the endpoint that finishes the job.
//
// Mutation check: revert ReloadScoreConfig to the "dedup engine rejected the
// new score ladder" wording, or make it roll the ladder back on a rescore
// failure, and this test fails.
func TestReloadScoreConfig_PartialRebandIsHonest(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := database.NewEmbeddingStore(store.DB())
	seedRescorableCandidates(t, es, 4)
	eng.SetRescoreWriter(&recordingRescoreWriter{failIDs: map[int64]bool{2: true}})

	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	res, err := eng.ReloadScoreConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("ReloadScoreConfig returned nil error despite an un-written row")
	}
	if res.WriteErrors != 1 {
		t.Errorf("WriteErrors = %d, want 1", res.WriteErrors)
	}
	msg := err.Error()
	if strings.Contains(msg, "rejected") {
		t.Errorf("error must not claim the engine rejected the ladder: %q", msg)
	}
	if !strings.Contains(msg, "POST /api/v1/dedup/rescore") {
		t.Errorf("error must name the real remedy endpoint (there is no `dedup.rescore` command to run): %q", msg)
	}
	if got := eng.ScoreConfig().BandCertainMin; got != 93 {
		t.Errorf("the validated ladder must stay live after a partial re-band; band_certain_min = %v, want 93", got)
	}
}

// TestRescore_SyncFailureIsAnError: if the final fsync fails the rows are not
// durable, so the call must not report success.
func TestRescore_SyncFailureIsAnError(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := database.NewEmbeddingStore(store.DB())
	seedRescorableCandidates(t, es, 2)
	eng.SetRescoreWriter(&recordingRescoreWriter{syncErr: errors.New("disk gone")})
	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	if err := eng.SetScoreConfig(cfg); err != nil {
		t.Fatalf("SetScoreConfig: %v", err)
	}
	if _, err := eng.Rescore(context.Background(), true); err == nil {
		t.Fatal("Rescore reported success although the final sync failed")
	}
}

// cancelAtNthCheck is a context that reports Canceled only from its Nth Err()
// call onward. Rescore checks ctx.Err() once per candidate, so this cancels the
// pass at a KNOWN row — with the batch size (500) far above the seeded row
// count, that row lands mid-buffer, which is the state the test needs. Cancelling
// a normal context before the call instead cancels at row 1, when the buffer is
// still empty, and the assertion below then holds trivially (0+0==0) no matter
// what the production code does — that blind version of this test SURVIVED the
// mutation it was written to catch.
type cancelAtNthCheck struct {
	context.Context
	calls *int
	n     int
}

func (c cancelAtNthCheck) Err() error {
	*c.calls++
	if *c.calls >= c.n {
		return context.Canceled
	}
	return nil
}

// TestRescore_CancelledMidPassDoesNotClaimBufferedRows is the honest-counter
// statement. Rescore buffers changed rows and writes them in batches, so a
// cancellation between two flushes abandons whatever is buffered. Those rows
// were already counted in Changed; if the caller infers "written" as
// Changed-WriteErrors it credits rows the store never saw. Written is counted
// from what the store CONFIRMED, and the abandoned buffer is counted as write
// errors.
//
// Mutation check: drop the `result.WriteErrors += len(pending)` from the
// ctx.Err() branch and this fails — 4 buffered rows go unaccounted for.
func TestRescore_CancelledMidPassDoesNotClaimBufferedRows(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := database.NewEmbeddingStore(store.DB())
	const rows = 8
	seedRescorableCandidates(t, es, rows)

	w := &recordingRescoreWriter{}
	eng.SetRescoreWriter(w)
	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	if err := eng.SetScoreConfig(cfg); err != nil {
		t.Fatalf("SetScoreConfig: %v", err)
	}

	// Cancel at the 5th row: rows 1-4 changed and are sitting in the buffer
	// (batch size 500, so no flush has happened) and are abandoned.
	calls := 0
	ctx := cancelAtNthCheck{Context: context.Background(), calls: &calls, n: 5}
	res, err := eng.Rescore(ctx, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rescore err = %v, want context.Canceled", err)
	}
	if res.Changed != 4 {
		t.Fatalf("Changed = %d, want 4 (the pass must reach 4 rows before the cancel so the buffer is NON-empty; an empty buffer cannot observe this bug)", res.Changed)
	}
	if res.Written != 0 {
		t.Errorf("Written = %d, want 0 — nothing was flushed, so nothing was written", res.Written)
	}
	if len(w.batches) != 0 {
		t.Errorf("store saw %d batches, want 0", len(w.batches))
	}
	if res.Written+res.WriteErrors != res.Changed {
		t.Errorf("Written(%d) + WriteErrors(%d) != Changed(%d): a cancelled run must account for every changed row it counted, including the ones abandoned in the buffer",
			res.Written, res.WriteErrors, res.Changed)
	}
}

// TestRescore_WrittenCountsWhatTheStoreConfirmed: on a partial per-row failure
// Written reports only the rows the store applied.
func TestRescore_WrittenCountsWhatTheStoreConfirmed(t *testing.T) {
	eng, store := newRescoreTestEngine(t)
	es := database.NewEmbeddingStore(store.DB())
	const rows = 6
	seedRescorableCandidates(t, es, rows)

	w := &recordingRescoreWriter{failIDs: map[int64]bool{2: true, 4: true}}
	eng.SetRescoreWriter(w)
	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93
	if err := eng.SetScoreConfig(cfg); err != nil {
		t.Fatalf("SetScoreConfig: %v", err)
	}

	res, err := eng.Rescore(context.Background(), true)
	if err != nil {
		t.Fatalf("Rescore: %v", err)
	}
	if res.WriteErrors != 2 {
		t.Fatalf("WriteErrors = %d, want 2", res.WriteErrors)
	}
	if res.Written != res.Changed-2 {
		t.Errorf("Written = %d, want %d (Changed %d minus the 2 rows the store refused)", res.Written, res.Changed-2, res.Changed)
	}
}
