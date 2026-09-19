// file: internal/ai/aijobs/integration_test.go
// version: 2.0.1
// guid: 69ad37a4-c90e-4dc4-92a5-155b51f85263
// last-edited: 2026-09-19

// NOTE(fable5 T022): Ported from NewSQLiteStore to NewPebbleStore.

package aijobs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_SubmitDispatchRoundTrip simulates the full aijobs flow:
// Submit → (mock batch completes) → Dispatch → callback applies results →
// ai_jobs row is marked completed.
func TestIntegration_SubmitDispatchRoundTrip(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, database.RunMigrations(store))

	ClearRegistryForTest()
	applied := 0
	Register("int_test", func(ctx context.Context, itemsJSON []byte, results []RowResult) (int, int, []database.AIJobRowError, error) {
		for range results {
			applied++
		}
		return len(results), 0, nil, nil
	})

	client := &fakeIntClient{returnBatchID: "batch_int"}
	deps := Deps{Store: store, Client: client}

	items := []map[string]any{{"n": 1}, {"n": 2}}
	payloadJSON, _ := json.Marshal(items)

	jobID, err := Submit(context.Background(), deps, SubmitRequest{
		Type:        "int_test",
		ItemCount:   len(items),
		PayloadJSON: payloadJSON,
		Build: func(i int) (BatchRequest, error) {
			return BatchRequest{Body: map[string]any{"i": i}}, nil
		},
	})
	require.NoError(t, err)

	// Simulate the BatchPoller calling Dispatch on completion.
	results := []RowResult{
		{CustomID: jobID + "-0", Content: `{"ok":true}`},
		{CustomID: jobID + "-1", Content: `{"ok":true}`},
	}
	require.NoError(t, Dispatch(context.Background(), store, "batch_int", results))

	assert.Equal(t, 2, applied)
	j, err := store.GetAIJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, "completed", j.Status)
	assert.Equal(t, 2, j.SuccessCount)
}

// TestIntegration_SubmitFailureMarksRowFailed verifies that an upload
// or create error during Submit leaves the ai_jobs row in "failed" state.
func TestIntegration_SubmitFailureMarksRowFailed(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, database.RunMigrations(store))

	ClearRegistryForTest()
	client := &fakeIntClient{uploadErr: assert.AnError}
	deps := Deps{Store: store, Client: client}

	jobID, err := Submit(context.Background(), deps, SubmitRequest{
		Type:        "int_test_fail",
		ItemCount:   1,
		PayloadJSON: []byte("[]"),
		Build: func(i int) (BatchRequest, error) {
			return BatchRequest{Body: map[string]any{}}, nil
		},
	})
	require.Error(t, err)
	require.NotEmpty(t, jobID)

	j, err := store.GetAIJob(jobID)
	require.NoError(t, err)
	assert.Equal(t, "failed", j.Status)
	assert.Contains(t, j.ErrorMsg, "upload")
}

type fakeIntClient struct {
	returnBatchID string
	uploadErr     error
	createErr     error
}

func (f *fakeIntClient) UploadBatchFile(context.Context, []byte) (string, error) {
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	return "file_int", nil
}
func (f *fakeIntClient) CreateBatchWithMetadata(context.Context, string, string, map[string]string) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.returnBatchID, nil
}
