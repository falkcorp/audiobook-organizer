// file: internal/ai/dedup_review_aijobs_test.go
// version: 1.0.1
// guid: 7f3c4a8d-9b2e-4f6a-8c1d-2e5f9a1b3c7d
// last-edited: 2026-09-02

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDedup Applier records verdicts for inspection in tests.
type fakeDedupApplier struct {
	verdicts   []DedupPairVerdict
	byIndex    map[int]database.DedupCandidate
	candidates map[int64]database.DedupCandidate
}

func newFakeDedupApplier() *fakeDedupApplier {
	return &fakeDedupApplier{
		candidates: map[int64]database.DedupCandidate{},
	}
}

func (f *fakeDedupApplier) ApplyVerdicts(verdicts []DedupPairVerdict, byIndex map[int]database.DedupCandidate) int {
	f.byIndex = byIndex
	applied := 0
	for _, v := range verdicts {
		if _, ok := byIndex[v.Index]; ok {
			f.verdicts = append(f.verdicts, v)
			applied++
		}
	}
	return applied
}

func (f *fakeDedupApplier) LookupCandidate(id int64) (database.DedupCandidate, bool) {
	c, ok := f.candidates[id]
	return c, ok
}

func (f *fakeDedupApplier) SetCandidate(id int64, c database.DedupCandidate) {
	f.candidates[id] = c
}

// fakeStoreForDedup implements AIJobsStore for dedup tests.
type fakeStoreForDedup struct {
	jobs     map[string]database.AIJob
	payloads map[string][]byte
}

func newFakeStoreForDedup() *fakeStoreForDedup {
	return &fakeStoreForDedup{
		jobs:     map[string]database.AIJob{},
		payloads: map[string][]byte{},
	}
}

func (f *fakeStoreForDedup) CreateAIJob(j database.AIJob, p []byte) error {
	f.jobs[j.ID] = j
	f.payloads[j.ID] = p
	return nil
}
func (f *fakeStoreForDedup) GetAIJob(id string) (database.AIJob, error) { return f.jobs[id], nil }
func (f *fakeStoreForDedup) GetAIJobByBatchID(b string) (database.AIJob, error) {
	for _, j := range f.jobs {
		if j.BatchID == b {
			return j, nil
		}
	}
	return database.AIJob{}, errors.New("not found")
}
func (f *fakeStoreForDedup) GetAIJobPayload(id string) ([]byte, error) { return f.payloads[id], nil }
func (f *fakeStoreForDedup) MarkAIJobSubmitted(id, b string) error {
	j := f.jobs[id]
	j.Status = "submitted"
	j.BatchID = b
	f.jobs[id] = j
	return nil
}
func (f *fakeStoreForDedup) MarkAIJobCompleted(id, status string, s, e int, re []database.AIJobRowError) error {
	j := f.jobs[id]
	j.Status = status
	j.SuccessCount = s
	j.ErrorCount = e
	if len(re) > 0 {
		b, _ := json.Marshal(re)
		j.RowErrors = string(b)
	}
	f.jobs[id] = j
	return nil
}
func (f *fakeStoreForDedup) MarkAIJobFailed(id, msg string) error {
	j := f.jobs[id]
	j.Status = "failed"
	j.ErrorMsg = msg
	f.jobs[id] = j
	return nil
}
func (f *fakeStoreForDedup) ListAIJobs(t, s string, l, o int) ([]database.AIJob, error) {
	var out []database.AIJob
	for _, j := range f.jobs {
		if t != "" && j.Type != t {
			continue
		}
		if s != "" && j.Status != s {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

// fakeBatchClientForDedup records batch submissions.
type fakeBatchClientForDedup struct {
	uploadCalls int
	createCalls int
	lastJSONL   []byte
}

func (f *fakeBatchClientForDedup) UploadBatchFile(ctx context.Context, data []byte) (string, error) {
	f.uploadCalls++
	f.lastJSONL = append([]byte(nil), data...)
	return "file_dedup_test", nil
}
func (f *fakeBatchClientForDedup) CreateBatchWithMetadata(ctx context.Context, fileID, batchType string) (string, error) {
	f.createCalls++
	return "batch_dedup_test", nil
}

// TestDedupReviewCallback_HappyPath tests the callback with one successful row.
func TestDedupReviewCallback_HappyPath(t *testing.T) {
	applier := newFakeDedupApplier()
	applier.SetCandidate(101, database.DedupCandidate{
		ID: 101, EntityType: "book", EntityAID: "book_a", EntityBID: "book_b",
	})

	payload := dedupReviewPayload{
		Inputs: []DedupPairInput{
			{Index: 0, EntityType: "book", A: DedupEntity{ID: "book_a", Title: "Book A"}, B: DedupEntity{ID: "book_b", Title: "Book B"}},
		},
		ByIndex: map[int]int64{0: 101},
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	verdictJSON, err := json.Marshal(struct {
		Verdicts []DedupPairVerdict `json:"verdicts"`
	}{
		Verdicts: []DedupPairVerdict{
			{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "same ISBN"},
		},
	})
	require.NoError(t, err)

	results := []aijobs.RowResult{
		{CustomID: "job1-0", Content: string(verdictJSON)},
	}

	// Set the applier before calling the callback.
	oldApplier := dedupVerdictApplier
	dedupVerdictApplier = applier
	defer func() { dedupVerdictApplier = oldApplier }()

	success, fail, rowErrors, fatalErr := dedupReviewCallback(context.Background(), payloadJSON, results)

	require.NoError(t, fatalErr)
	assert.Equal(t, 1, success, "expected 1 success")
	assert.Equal(t, 0, fail, "expected 0 failures")
	assert.Len(t, rowErrors, 0, "expected no row errors")
	assert.Len(t, applier.verdicts, 1, "expected 1 verdict applied")
	assert.Equal(t, 0, applier.verdicts[0].Index)
	assert.True(t, applier.verdicts[0].IsDuplicate)
}

// TestDedupReviewCallback_PerRowErrorsIsolated tests that errors in one row don't affect others.
func TestDedupReviewCallback_PerRowErrorsIsolated(t *testing.T) {
	applier := newFakeDedupApplier()
	applier.SetCandidate(101, database.DedupCandidate{
		ID: 101, EntityType: "book", EntityAID: "book_a", EntityBID: "book_b",
	})

	payload := dedupReviewPayload{
		Inputs: []DedupPairInput{
			{Index: 0, EntityType: "book"},
			{Index: 1, EntityType: "book"},
			{Index: 2, EntityType: "book"},
		},
		ByIndex: map[int]int64{0: 101, 1: 102, 2: 103},
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	goodVerdictJSON, err := json.Marshal(struct {
		Verdicts []DedupPairVerdict `json:"verdicts"`
	}{
		Verdicts: []DedupPairVerdict{
			{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "match"},
		},
	})
	require.NoError(t, err)

	results := []aijobs.RowResult{
		{CustomID: "job1-0", Content: string(goodVerdictJSON)}, // Success
		{CustomID: "job1-1", Err: "api error"},                 // Error from OpenAI
		{CustomID: "job1-2", Content: "not valid json"},        // Parse error
	}

	oldApplier := dedupVerdictApplier
	dedupVerdictApplier = applier
	defer func() { dedupVerdictApplier = oldApplier }()

	success, fail, rowErrors, fatalErr := dedupReviewCallback(context.Background(), payloadJSON, results)

	require.NoError(t, fatalErr)
	assert.Equal(t, 1, success, "expected 1 success")
	assert.Equal(t, 2, fail, "expected 2 failures")
	assert.Len(t, rowErrors, 2, "expected 2 row errors")
	assert.Len(t, applier.verdicts, 1, "expected only 1 verdict despite 3 rows")
}

// TestSubmitDedupReviewJob_SplitsIntoSubBatches tests that 51 inputs split into 3 JSONL rows (25+25+1).
func TestSubmitDedupReviewJob_SplitsIntoSubBatches(t *testing.T) {
	store := newFakeStoreForDedup()
	client := &fakeBatchClientForDedup{}

	deps := aijobs.Deps{
		Store:  store,
		Client: client,
	}

	// Build 51 inputs.
	inputs := make([]DedupPairInput, 51)
	byIndex := make(map[int]int64, 51)
	for i := range 51 {
		inputs[i] = DedupPairInput{
			Index:      i,
			EntityType: "book",
			A:          DedupEntity{ID: "a" + string(rune('0'+i%10))},
			B:          DedupEntity{ID: "b" + string(rune('0'+i%10))},
		}
		byIndex[i] = int64(1000 + i)
	}

	jobID, err := SubmitDedupReviewJob(context.Background(), deps, "gpt-5-mini", inputs, byIndex)
	require.NoError(t, err)
	require.NotEmpty(t, jobID)

	// Verify JSONL has 3 lines (sub-batches).
	lines := 0
	jsonlBytes := client.lastJSONL
	for _, b := range jsonlBytes {
		if b == '\n' {
			lines++
		}
	}
	assert.Equal(t, 3, lines, "expected 3 JSONL lines (sub-batches)")

	// Verify the job was created.
	job, err := store.GetAIJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, "dedup_review", job.Type)
	assert.Equal(t, 3, job.ItemCount, "expected ItemCount=3 sub-batches")
}

// TestDedupReviewCallback_MissingCandidates tests graceful handling of deleted candidates.
func TestDedupReviewCallback_MissingCandidates(t *testing.T) {
	applier := newFakeDedupApplier()
	// Only candidate 101 exists; 102 and 103 are missing (deleted/purged).
	applier.SetCandidate(101, database.DedupCandidate{
		ID: 101, EntityType: "book", EntityAID: "book_a", EntityBID: "book_b",
	})

	payload := dedupReviewPayload{
		Inputs: []DedupPairInput{
			{Index: 0, EntityType: "book"},
			{Index: 1, EntityType: "book"},
			{Index: 2, EntityType: "book"},
		},
		ByIndex: map[int]int64{0: 101, 1: 102, 2: 103},
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	verdictJSON, err := json.Marshal(struct {
		Verdicts []DedupPairVerdict `json:"verdicts"`
	}{
		Verdicts: []DedupPairVerdict{
			{Index: 0, IsDuplicate: true, Confidence: "high"},
			{Index: 1, IsDuplicate: false, Confidence: "medium"},
			{Index: 2, IsDuplicate: true, Confidence: "low"},
		},
	})
	require.NoError(t, err)

	results := []aijobs.RowResult{
		{CustomID: "job1-0", Content: string(verdictJSON)},
	}

	oldApplier := dedupVerdictApplier
	dedupVerdictApplier = applier
	defer func() { dedupVerdictApplier = oldApplier }()

	success, fail, _, fatalErr := dedupReviewCallback(context.Background(), payloadJSON, results)

	require.NoError(t, fatalErr)
	assert.Equal(t, 1, success, "expected 1 success row")
	assert.Equal(t, 0, fail, "expected 0 failures")
	// Only verdict for index 0 should be applied (indices 1 and 2's candidates are missing).
	assert.Len(t, applier.verdicts, 1, "expected only 1 verdict (for the existing candidate)")
	assert.Equal(t, 0, applier.verdicts[0].Index)
}
