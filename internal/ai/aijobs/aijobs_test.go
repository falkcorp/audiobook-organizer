// file: internal/ai/aijobs/aijobs_test.go
// version: 1.1.0
// guid: 92b8a4e2-1647-48c3-acc3-ae3e101623d7

package aijobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is a minimal in-memory AIJobsStore for tests.
type fakeStore struct {
	jobs     map[string]database.AIJob
	payloads map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{jobs: map[string]database.AIJob{}, payloads: map[string][]byte{}}
}

func (f *fakeStore) CreateAIJob(j database.AIJob, p []byte) error {
	f.jobs[j.ID] = j
	f.payloads[j.ID] = p
	return nil
}
func (f *fakeStore) GetAIJob(id string) (database.AIJob, error) { return f.jobs[id], nil }
func (f *fakeStore) GetAIJobByBatchID(b string) (database.AIJob, error) {
	for _, j := range f.jobs {
		if j.BatchID == b {
			return j, nil
		}
	}
	return database.AIJob{}, errors.New("not found")
}
func (f *fakeStore) GetAIJobPayload(id string) ([]byte, error) { return f.payloads[id], nil }
func (f *fakeStore) MarkAIJobSubmitted(id, b string) error {
	j := f.jobs[id]
	j.Status = "submitted"
	j.BatchID = b
	f.jobs[id] = j
	return nil
}
func (f *fakeStore) MarkAIJobCompleted(id, status string, s, e int, re []database.AIJobRowError) error {
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
func (f *fakeStore) MarkAIJobFailed(id, msg string) error {
	j := f.jobs[id]
	j.Status = "failed"
	j.ErrorMsg = msg
	f.jobs[id] = j
	return nil
}
func (f *fakeStore) ListAIJobs(t, s string, l, o int) ([]database.AIJob, error) {
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

// fakeBatchClient satisfies BatchClient for tests.
type fakeBatchClient struct {
	uploadCalls   int
	createCalls   int
	lastJSONL     []byte
	lastType      string
	returnBatchID string
	returnErr     error
}

func (f *fakeBatchClient) UploadBatchFile(ctx context.Context, data []byte) (string, error) {
	f.uploadCalls++
	f.lastJSONL = append([]byte(nil), data...)
	return "file_123", f.returnErr
}
func (f *fakeBatchClient) CreateBatchWithMetadata(ctx context.Context, fileID, batchType string) (string, error) {
	f.createCalls++
	f.lastType = batchType
	if f.returnErr != nil {
		return "", f.returnErr
	}
	return f.returnBatchID, nil
}

func TestSubmit_HappyPath(t *testing.T) {
	store := newFakeStore()
	client := &fakeBatchClient{returnBatchID: "batch_xyz"}
	deps := Deps{Store: store, Client: client}

	items := []string{"alpha", "beta", "gamma"}
	jobID, err := Submit(context.Background(), deps, SubmitRequest{
		Type:        "test_feature",
		ItemCount:   len(items),
		PayloadJSON: mustMarshal(items),
		Build: func(i int) (BatchRequest, error) {
			return BatchRequest{Body: map[string]any{"item": items[i]}, MaxTokens: 100}, nil
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, jobID)

	j, _ := store.GetAIJob(jobID)
	assert.Equal(t, "submitted", j.Status)
	assert.Equal(t, "batch_xyz", j.BatchID)
	assert.Equal(t, 3, j.ItemCount)
	assert.Equal(t, "aijobs", client.lastType) // BatchPoller routes all aijobs under one metadata type
	assert.Equal(t, 1, client.uploadCalls)
	assert.Equal(t, 1, client.createCalls)

	// JSONL must have 3 lines, each with a custom_id and the chat-completions URL
	lines := bytesSplitLines(client.lastJSONL)
	assert.Len(t, lines, 3)
}

func TestSubmit_UploadFailure_MarksRowFailed(t *testing.T) {
	store := newFakeStore()
	client := &fakeBatchClient{returnErr: errors.New("insufficient_quota")}
	deps := Deps{Store: store, Client: client}

	_, err := Submit(context.Background(), deps, SubmitRequest{
		Type: "test_feature", ItemCount: 1, PayloadJSON: []byte("[]"),
		Build: func(i int) (BatchRequest, error) { return BatchRequest{Body: map[string]any{}}, nil },
	})
	require.Error(t, err)

	// A row was created and then marked failed
	jobs, _ := store.ListAIJobs("test_feature", "failed", 10, 0)
	assert.Len(t, jobs, 1)
	assert.Contains(t, jobs[0].ErrorMsg, "insufficient_quota")
}

func TestDispatch_PerRowErrorsIsolated(t *testing.T) {
	store := newFakeStore()
	Register("test_feature", func(ctx context.Context, itemsJSON []byte, results []RowResult) (int, int, []database.AIJobRowError, error) {
		success, fail := 0, 0
		var errs []database.AIJobRowError
		for _, r := range results {
			if r.CustomID == "bad-1" {
				fail++
				errs = append(errs, database.AIJobRowError{CustomID: r.CustomID, Error: "bad row"})
				continue
			}
			success++
		}
		return success, fail, errs, nil
	})
	// Seed a job with payload
	jobID := "01DISP"
	_ = store.CreateAIJob(database.AIJob{ID: jobID, Type: "test_feature", CustomIDPrefix: "01DISP", Status: "submitted", ItemCount: 2, BatchID: "batch_d"}, []byte(`[{"x":1},{"x":2}]`))
	_ = store.MarkAIJobSubmitted(jobID, "batch_d")

	results := []RowResult{
		{CustomID: "good-1", Content: `{"ok":true}`},
		{CustomID: "bad-1", Content: `{"ok":false}`},
	}
	err := Dispatch(context.Background(), store, "batch_d", results)
	require.NoError(t, err)

	j, _ := store.GetAIJob(jobID)
	assert.Equal(t, "completed_with_errors", j.Status)
	assert.Equal(t, 1, j.SuccessCount)
	assert.Equal(t, 1, j.ErrorCount)
	assert.Contains(t, j.RowErrors, "bad-1")
}

func TestDispatch_AllSuccess_MarksCompleted(t *testing.T) {
	store := newFakeStore()
	Register("only_success", func(ctx context.Context, itemsJSON []byte, results []RowResult) (int, int, []database.AIJobRowError, error) {
		return len(results), 0, nil, nil
	})
	_ = store.CreateAIJob(database.AIJob{ID: "01OK", Type: "only_success", CustomIDPrefix: "01OK", Status: "submitted", ItemCount: 1, BatchID: "batch_ok"}, []byte("[]"))
	_ = store.MarkAIJobSubmitted("01OK", "batch_ok")

	err := Dispatch(context.Background(), store, "batch_ok", []RowResult{{CustomID: "x", Content: "{}"}})
	require.NoError(t, err)
	j, _ := store.GetAIJob("01OK")
	assert.Equal(t, "completed", j.Status)
}

func TestDispatch_PanicInCallbackRecovered(t *testing.T) {
	store := newFakeStore()
	Register("panicker", func(ctx context.Context, itemsJSON []byte, results []RowResult) (int, int, []database.AIJobRowError, error) {
		panic("boom")
	})
	_ = store.CreateAIJob(database.AIJob{ID: "01P", Type: "panicker", CustomIDPrefix: "01P", Status: "submitted", ItemCount: 1, BatchID: "batch_p"}, []byte("[]"))
	_ = store.MarkAIJobSubmitted("01P", "batch_p")

	err := Dispatch(context.Background(), store, "batch_p", []RowResult{{CustomID: "x"}})
	require.Error(t, err)
	j, _ := store.GetAIJob("01P")
	assert.Equal(t, "failed", j.Status)
	assert.Contains(t, j.ErrorMsg, "panic")
}

// helpers
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func bytesSplitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := range b {
		if b[i] == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
