// file: internal/ai/author_dedup_job_test.go
// version: 1.0.0
// guid: dee19992-4b40-4193-9395-eb90bb24dcc7
// last-edited: 2026-09-19

package ai

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/stretchr/testify/require"
)

// TestAuthorDedupJobCallbackAppliesThroughSink: the aijobs callback decodes
// every successful row and hands the suggestions, keyed by the submitting
// run's source id, to the registered sink. A replay (the interrupted-apply
// case aijobs documents) hands over the same source id again, which is what
// lets the sink be idempotent.
func TestAuthorDedupJobCallbackAppliesThroughSink(t *testing.T) {
	type call struct {
		source string
		n      int
	}
	var calls []call
	SetAuthorDedupResultSink(func(_ context.Context, sourceID string, s []AuthorDiscoverySuggestion) error {
		calls = append(calls, call{sourceID, len(s)})
		return nil
	})
	t.Cleanup(func() { SetAuthorDedupResultSink(nil) })

	payload, err := json.Marshal(authorDedupJobPayload{SourceID: "run-1", Inputs: []AuthorDiscoveryInput{{ID: 1, Name: "A"}}})
	require.NoError(t, err)
	content := `{"suggestions":[{"author_ids":[1,2],"action":"merge","canonical_name":"A","reason":"r","confidence":"high"}]}`
	rows := []aijobs.RowResult{{CustomID: "j-0", Content: content}, {CustomID: "j-1", Err: "rate limited"}}

	ok, bad, rowErrs, fatal := authorDedupJobCallback(context.Background(), payload, rows)
	require.NoError(t, fatal)
	require.Equal(t, 1, ok)
	require.Equal(t, 1, bad)
	require.Len(t, rowErrs, 1)
	_, _, _, fatal = authorDedupJobCallback(context.Background(), payload, rows)
	require.NoError(t, fatal)
	require.Equal(t, []call{{"run-1", 1}, {"run-1", 1}}, calls)
}

// Without a sink the apply must FAIL (aijobs retries it), never report success
// with the results dropped.
func TestAuthorDedupJobCallbackWithoutSinkFails(t *testing.T) {
	SetAuthorDedupResultSink(nil)
	payload, _ := json.Marshal(authorDedupJobPayload{SourceID: "run-1"})
	_, _, _, fatal := authorDedupJobCallback(context.Background(), payload, []aijobs.RowResult{{CustomID: "j-0", Content: `{"suggestions":[]}`}})
	require.Error(t, fatal)
}
