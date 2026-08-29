// file: internal/maintenance/jobs/revert_metadata_fetch_test.go
// version: 1.0.0
// guid: 6c2f8a91-4db7-4e35-9a80-2f5c1d7b3e64
// last-edited: 2026-08-29

package jobs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revert-metadata-fetch was the worst case of the dead-params defect: fetch_op_ids
// is REQUIRED, and the only route it had for reading it —
// store.GetOperationParams(opID) — lost its writer when the v1 op minter was
// retired (#2784). So every invocation reached exactly one outcome, the
// "fetch_op_ids required" error, no matter what the operator sent. The job was
// 100% non-functional and had no test file at all.
//
// These tests pin both halves: that the parameter now arrives, and that its
// absence is still a clear error rather than a silent no-op.

func TestRevertMetadataFetchJob_Registered(t *testing.T) {
	assertJobRegistered(t, "revert-metadata-fetch")
}

// The headline assertion: with fetch_op_ids on the live channel the job gets PAST
// the required-parameter error and actually does its work. Before this change
// this test could not be made to pass by any request an operator could send.
func TestRevertMetadataFetchJob_FetchOpIDsArriveOnRawParams(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	prev, _ := json.Marshal("Original Title")
	prevStr := string(prev)

	var updated *database.Book
	store := &database.MockStore{
		GetOperationByIDFunc: func(id string) (*database.Operation, error) {
			return &database.Operation{
				ID: id, Type: "bulk_metadata_fetch",
				CreatedAt: started, StartedAt: &started,
			}, nil
		},
		GetOperationResultsFunc: func(opID string) ([]database.OperationResult, error) {
			return []database.OperationResult{{OperationID: opID, BookID: "book-1", Status: "updated"}}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Fetched Title"}, nil
		},
		GetBookChangeHistoryFunc: func(bookID string, limit int) ([]database.MetadataChangeRecord, error) {
			return []database.MetadataChangeRecord{{
				BookID: bookID, Field: "title", ChangeType: "fetched",
				ChangedAt: time.Now(), PreviousValue: &prevStr,
			}}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updated = b
			return b, nil
		},
	}

	j, err := maintenance.Get("revert-metadata-fetch")
	require.NoError(t, err)

	ctx := maintenance.WithOperationID(context.Background(), "op-revert-1")
	ctx = maintenance.WithRawParams(ctx, json.RawMessage(`{"fetch_op_ids":["fetch-op-a"]}`))

	require.NoError(t, j.Run(ctx, store, &noopReporter{}, false),
		"fetch_op_ids was supplied, so the job must not report it missing")

	require.NotNil(t, updated, "the job never wrote a book; fetch_op_ids did not reach it")
	assert.Equal(t, "Original Title", updated.Title,
		"the title must be reverted to its pre-fetch value")
}

// The discriminating negative — without it, a job that hardcoded an operation ID
// or ignored the parameter entirely would pass the test above.
func TestRevertMetadataFetchJob_MissingFetchOpIDsIsAnError(t *testing.T) {
	store := &database.MockStore{}
	j, err := maintenance.Get("revert-metadata-fetch")
	require.NoError(t, err)

	err = j.Run(context.Background(), store, &noopReporter{}, false)
	require.Error(t, err, "no fetch_op_ids must be a clear error, not a silent no-op")
	assert.Contains(t, err.Error(), "fetch_op_ids required")
}

// The dead path must stay dead. A populated params side table must NOT revive the
// job: if it did, the fix would be cosmetic and the real channel untested.
func TestRevertMetadataFetchJob_IgnoresOperationParamsSideTable(t *testing.T) {
	sideTableReads := 0
	store := &database.MockStore{
		GetOperationParamsFunc: func(opID string) ([]byte, error) {
			sideTableReads++
			return []byte(`{"fetch_op_ids":["fetch-op-a"]}`), nil
		},
	}

	j, err := maintenance.Get("revert-metadata-fetch")
	require.NoError(t, err)

	ctx := maintenance.WithOperationID(context.Background(), "op-revert-2")
	err = j.Run(ctx, store, &noopReporter{}, false)

	assert.Zero(t, sideTableReads, "the job must not read the params side table")
	require.Error(t, err, "a side-table fetch_op_ids must not satisfy the requirement")
	assert.Contains(t, err.Error(), "fetch_op_ids required")
}
