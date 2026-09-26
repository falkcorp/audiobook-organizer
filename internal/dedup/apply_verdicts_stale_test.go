// file: internal/dedup/apply_verdicts_stale_test.go
// version: 1.4.0
// guid: f1e7f635-5dfb-46da-b1e4-fc3f63f6cacc
// last-edited: 2026-09-26

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoMergeFixture seeds two mergeable books and one pending candidate for
// them, with LLM auto-merge on. updates counts UpdateBook calls — every merge
// makes some, so an unchanged count proves no merge ran.
func autoMergeFixture(t *testing.T) (*Engine, *database.EmbeddingStore, database.DedupCandidate, *atomic.Int32) {
	t.Helper()
	engine, mock, es := setupTestEngine(t)
	prev := config.AppConfig.Dedup.LLMAutoMergeHighConfidence
	config.AppConfig.Dedup.LLMAutoMergeHighConfidence = true
	t.Cleanup(func() { config.AppConfig.Dedup.LLMAutoMergeHighConfidence = prev })

	authorID := 1
	books := map[string]*database.Book{
		"BOOK_A": {ID: "BOOK_A", Title: "Foundation", AuthorID: &authorID, Format: "mp3"},
		"BOOK_B": {ID: "BOOK_B", Title: "Foundation", AuthorID: &authorID, Format: "m4b"},
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	updates := &atomic.Int32{}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updates.Add(1)
		books[id] = b
		return b, nil
	}
	sim := 0.88
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "BOOK_A", EntityBID: "BOOK_B",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	}))
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	return engine, es, cands[0], updates
}

var highDup = []ai.DedupPairVerdict{{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "identical"}}

// An owner-dismissed pair must not be merged or have its verdict rewritten by
// an LLM verdict that arrives later — even one computed while it was pending.
func TestApplyVerdicts_DismissedCandidateNotMergedOrOverwritten(t *testing.T) {
	engine, es, cand, updates := autoMergeFixture(t)
	snapshot := cand // what the callback captured while the pair was pending
	require.NoError(t, es.UpdateCandidateStatus(cand.ID, "dismissed"))

	res := engine.ApplyVerdicts(highDup, map[int]database.DedupCandidate{0: snapshot})

	assert.Equal(t, 0, res.Applied)
	assert.Equal(t, 1, res.SkippedStale)
	assert.Equal(t, int32(0), updates.Load(), "a dismissed pair was merged")
	got, err := es.GetCandidateByID(cand.ID)
	require.NoError(t, err)
	assert.Equal(t, "dismissed", got.Status)
	assert.Empty(t, got.LLMVerdict, "verdict written over an owner decision")
}

// Replaying the same verdicts (a crash before the job recorded the apply) is a
// no-op: the first run left the candidate merged.
func TestApplyVerdicts_ReplayIsNoOp(t *testing.T) {
	engine, es, cand, updates := autoMergeFixture(t)
	byIndex := map[int]database.DedupCandidate{0: cand}

	first := engine.ApplyVerdicts(highDup, byIndex)
	require.Equal(t, 1, first.Applied)
	mergedUpdates := updates.Load()
	require.Positive(t, mergedUpdates)
	got, _ := es.GetCandidateByID(cand.ID)
	require.Equal(t, "merged", got.Status)
	verdict, reason := got.LLMVerdict, got.LLMReason

	second := engine.ApplyVerdicts(highDup, byIndex)
	assert.Equal(t, 0, second.Applied)
	assert.Equal(t, 1, second.SkippedStale)
	assert.Equal(t, mergedUpdates, updates.Load(), "replay merged again")
	got, _ = es.GetCandidateByID(cand.ID)
	assert.Equal(t, "merged", got.Status)
	assert.Equal(t, verdict, got.LLMVerdict)
	assert.Equal(t, reason, got.LLMReason)
}

// Two book rows at the same cleaned path (CHAPTER-SUBFOLDER-NN-ROWS,
// 2026-09-25) are review-queue-only by owner decision: even a high-confidence
// LLM "duplicate" verdict with auto-merge enabled must never merge this pair.
// The verdict itself is still recorded (a human reviewing the pair should see
// what the LLM thought), just never acted on.
func TestApplyVerdicts_SamePathPair_VerdictRecordedButNeverAutoMerged(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	prev := config.AppConfig.Dedup.LLMAutoMergeHighConfidence
	config.AppConfig.Dedup.LLMAutoMergeHighConfidence = true
	t.Cleanup(func() { config.AppConfig.Dedup.LLMAutoMergeHighConfidence = prev })

	authorID := 1
	path := "/lib/Author/Book/Book - NN/32.m4b"
	books := map[string]*database.Book{
		"ROW1": {ID: "ROW1", Title: "Same Path Book", AuthorID: &authorID, FilePath: path},
		"ROW2": {ID: "ROW2", Title: "Same Path Book", AuthorID: &authorID, FilePath: path},
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	updates := &atomic.Int32{}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updates.Add(1)
		books[id] = b
		return b, nil
	}
	sim := 1.0
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "ROW1", EntityBID: "ROW2",
		Layer: "exact", Similarity: &sim, Status: "pending",
	}))
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	res := engine.ApplyVerdicts(highDup, map[int]database.DedupCandidate{0: cands[0]})

	assert.Equal(t, 1, res.Applied, "the verdict should still be recorded")
	assert.Equal(t, int32(0), updates.Load(), "a same-path pair must never be auto-merged")
	got, err := es.GetCandidateByID(cands[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", got.Status, "an un-mergeable pair stays pending for a human")
	assert.Equal(t, "duplicate", got.LLMVerdict)
}

// A human pins the pair while the OpenAI batch is still running. The verdict
// that arrives afterwards must neither merge the pair nor rewrite the pinned
// row (its Layer would become "llm", its status, band or score change). It is
// kept on the row as advice for the reviewer (owner decision 2026-09-26).
func TestApplyVerdicts_PinnedAfterSubmit_RecordedAsAdviceOnly(t *testing.T) {
	engine, es, cand, updates := autoMergeFixture(t)
	snapshot := cand // captured at submit time, before the pin
	pin, err := es.EnqueueManualCandidate("book", "BOOK_A", "BOOK_B", "")
	require.NoError(t, err)
	require.True(t, pin.Pinned)
	before, err := es.GetCandidateByID(cand.ID)
	require.NoError(t, err)

	res := engine.ApplyVerdicts(highDup, map[int]database.DedupCandidate{0: snapshot})

	assert.Equal(t, 0, res.Applied)
	assert.Equal(t, 0, res.SkippedStale)
	assert.Equal(t, 1, res.Advised)
	assert.Equal(t, int32(0), updates.Load(), "a pinned pair was merged")
	got, err := es.GetCandidateByID(cand.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", got.Status)
	assert.Equal(t, "embedding", got.Layer, "the pinned row's layer was rewritten")
	assert.Equal(t, before.Band, got.Band)
	assert.Equal(t, before.ScoreBreakdown, got.ScoreBreakdown)
	assert.Empty(t, got.LLMVerdict)
	assert.True(t, database.IsManualCandidate(*got))
	assert.Equal(t, "duplicate", got.AIAdviceVerdict)
	assert.Equal(t, "[high] identical", got.AIAdviceReason)
	assert.NotNil(t, got.AIAdviceAt)
}

// A pinned row a human then dismisses gets no advice from a late verdict.
func TestApplyVerdicts_PinnedThenDismissed_NoAdvice(t *testing.T) {
	engine, es, cand, updates := autoMergeFixture(t)
	_, err := es.EnqueueManualCandidate("book", "BOOK_A", "BOOK_B", "")
	require.NoError(t, err)
	require.NoError(t, es.UpdateCandidateStatus(cand.ID, "dismissed"))

	res := engine.ApplyVerdicts(highDup, map[int]database.DedupCandidate{0: cand})

	assert.Equal(t, 0, res.Advised)
	assert.Equal(t, 1, res.SkippedStale)
	assert.Equal(t, int32(0), updates.Load())
	got, err := es.GetCandidateByID(cand.ID)
	require.NoError(t, err)
	assert.Equal(t, "dismissed", got.Status)
	assert.Empty(t, got.AIAdviceVerdict)
}

// The pin lands after the verdict write but before the merge (here: while
// the auto-merge path loads the books). The merge-time re-check must catch it.
func TestApplyVerdicts_PinnedBeforeMerge_NotMerged(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	prev := config.AppConfig.Dedup.LLMAutoMergeHighConfidence
	config.AppConfig.Dedup.LLMAutoMergeHighConfidence = true
	t.Cleanup(func() { config.AppConfig.Dedup.LLMAutoMergeHighConfidence = prev })

	authorID := 1
	books := map[string]*database.Book{
		"BOOK_A": {ID: "BOOK_A", Title: "Foundation", AuthorID: &authorID, Format: "mp3"},
		"BOOK_B": {ID: "BOOK_B", Title: "Foundation", AuthorID: &authorID, Format: "m4b"},
	}
	var pinned atomic.Bool
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if pinned.CompareAndSwap(false, true) {
			_, err := es.EnqueueManualCandidate("book", "BOOK_A", "BOOK_B", "")
			require.NoError(t, err)
		}
		return books[id], nil
	}
	updates := &atomic.Int32{}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		updates.Add(1)
		books[id] = b
		return b, nil
	}
	sim := 0.88
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "BOOK_A", EntityBID: "BOOK_B",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	}))
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	res := engine.ApplyVerdicts(highDup, map[int]database.DedupCandidate{0: cands[0]})

	require.True(t, pinned.Load(), "fixture never reached the merge path")
	assert.Equal(t, 1, res.Applied, "the verdict was written before the pin")
	assert.Equal(t, int32(0), updates.Load(), "a pair pinned before the merge was merged")
	got, err := es.GetCandidateByID(cands[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", got.Status)
}

// lostAppliedMarkStore is a real PebbleStore (as the AIJobsStore) whose next
// MarkAIJobApplied is lost, modelling a kill after the callback applied its
// verdicts but before the job recorded that.
type lostAppliedMarkStore struct {
	*database.PebbleStore
	loseNext bool
}

func (s *lostAppliedMarkStore) MarkAIJobApplied(id string, sc, ec int, re []database.AIJobRowError) error {
	if s.loseNext {
		s.loseNext = false
		return errors.New("killed before the applied mark")
	}
	return s.PebbleStore.MarkAIJobApplied(id, sc, ec, re)
}

// The REAL dedup_review callback, replayed through aijobs.Dispatch after the
// crash window, with auto-merge on: the pair is merged once, its verdict is
// not rewritten, and the job completes.
func TestDedupReviewCallback_ReplayAfterCrashIsNoOp(t *testing.T) {
	engine, es, cand, updates := autoMergeFixture(t)
	ai.SetDedupVerdictApplier(engine)
	t.Cleanup(func() { ai.SetDedupVerdictApplier(nil) })

	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	jobs := &lostAppliedMarkStore{PebbleStore: ps, loseNext: true}

	payload, err := json.Marshal(map[string]any{"inputs": []any{}, "by_index": map[string]int64{"0": cand.ID}})
	require.NoError(t, err)
	require.NoError(t, jobs.CreateAIJob(database.AIJob{ID: "JDR", Type: "dedup_review", Status: "pending", CreatedAt: time.Now()}, payload))
	require.NoError(t, jobs.MarkAIJobSubmitted("JDR", "batch_dr"))
	results := []aijobs.RowResult{{CustomID: "JDR-0", Content: `{"verdicts":[{"index":0,"is_duplicate":true,"confidence":"high","reason":"identical"}]}`}}

	require.Error(t, aijobs.Dispatch(context.Background(), jobs, "batch_dr", results))
	merged := updates.Load()
	require.Positive(t, merged, "first apply should have merged")
	first, _ := es.GetCandidateByID(cand.ID)
	require.Equal(t, "merged", first.Status)

	// Restart after the backoff: the job was never marked applied, so Dispatch
	// replays the real callback once.
	job, err := jobs.GetAIJob("JDR")
	require.NoError(t, err)
	require.False(t, job.Applied)
	job.LastApplyAt = time.Now().Add(-2 * time.Hour)
	raw, _ := json.Marshal(job)
	require.NoError(t, ps.SetRaw("aijob:JDR", raw))

	require.NoError(t, aijobs.Dispatch(context.Background(), jobs, "batch_dr", results))
	assert.Equal(t, merged, updates.Load(), "replay merged again")
	again, _ := es.GetCandidateByID(cand.ID)
	assert.Equal(t, "merged", again.Status)
	assert.Equal(t, first.LLMVerdict, again.LLMVerdict)
	assert.Equal(t, first.LLMReason, again.LLMReason)
	job, _ = jobs.GetAIJob("JDR")
	assert.Equal(t, "completed", job.Status)
}
