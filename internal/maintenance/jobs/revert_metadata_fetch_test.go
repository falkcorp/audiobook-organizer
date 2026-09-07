// file: internal/maintenance/jobs/revert_metadata_fetch_test.go
// version: 1.1.0
// guid: 6c2f8a91-4db7-4e35-9a80-2f5c1d7b3e64
// last-edited: 2026-09-07

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

// revertStore builds the MockStore the v2 cases share: a fetch that updated one
// book, whose pre-fetch title the revert should restore. Only the operation
// LOOKUP varies between them, which is the thing under test.
func revertStore(t *testing.T, v2 *database.OperationV2Row, v1 *database.Operation) (*database.MockStore, **database.Book) {
	t.Helper()
	prev, _ := json.Marshal("Original Title")
	prevStr := string(prev)
	updated := new(*database.Book)
	return &database.MockStore{
		GetOperationV2Func:   func(string) (*database.OperationV2Row, error) { return v2, nil },
		GetOperationByIDFunc: func(string) (*database.Operation, error) { return v1, nil },
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
			*updated = b
			return b, nil
		},
	}, updated
}

func runRevertWithIDs(t *testing.T, store *database.MockStore, ids string) error {
	t.Helper()
	j, err := maintenance.Get("revert-metadata-fetch")
	require.NoError(t, err)
	ctx := maintenance.WithOperationID(context.Background(), "op-revert-v2")
	ctx = maintenance.WithRawParams(ctx, json.RawMessage(`{"fetch_op_ids":["`+ids+`"]}`))
	return j.Run(ctx, store, &noopReporter{}, false)
}

// THE case this job was broken on. Every id a user can paste today is a v2 id —
// the v1 minter was retired 2026-08-23 — and until 2026-09-07 the job resolved
// operations through GetOperationByID alone, which answers (nil, nil) for a v2
// id. The loop's `continue` swallowed it, bookIDSet stayed empty, and the job
// reported success having reverted nothing.
//
// Note what the fixture withholds: GetOperationByIDFunc returns nil. The
// pre-existing TestRevertMetadataFetchJob_FetchOpIDsArriveOnRawParams returns a
// v1 operation for ANY id, so it passed throughout the outage — a green test
// only reaches what its fixture contains.
func TestRevertMetadataFetchJob_ResolvesV2FetchOperations(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	for _, defID := range []string{"library.bulk-metadata-fetch", "maintenance.bulk-fetch-metadata"} {
		t.Run(defID, func(t *testing.T) {
			store, updated := revertStore(t, &database.OperationV2Row{
				ID: "fetch-op-v2", DefID: defID,
				QueuedAt: started, StartedAt: &started,
			}, nil)

			require.NoError(t, runRevertWithIDs(t, store, "fetch-op-v2"))
			require.NotNil(t, *updated,
				"the job never wrote a book — a v2-keyed fetch op did not resolve")
			assert.Equal(t, "Original Title", (*updated).Title)
		})
	}
}

// The guard that stops a revert being aimed at an unrelated operation. Its v1
// form (op.Type != "bulk_metadata_fetch") is load-bearing, so the v2 arm must
// carry an equivalent or the fix would have widened what a revert can touch.
func TestRevertMetadataFetchJob_RejectsNonFetchV2Operation(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	store, updated := revertStore(t, &database.OperationV2Row{
		ID: "scan-op", DefID: "library.scan", QueuedAt: started, StartedAt: &started,
	}, nil)

	err := runRevertWithIDs(t, store, "scan-op")
	require.Error(t, err, "a library.scan id must not be revertible as a metadata fetch")
	assert.Contains(t, err.Error(), "not a bulk metadata fetch")
	assert.Nil(t, *updated, "nothing may be written when the guard rejects the op")
}

// An id present in NEITHER keyspace is now a hard error. It used to `continue`,
// which meant a typo'd or aged-out id silently shrank the revert's scope while
// the job still reported success. A revert is destructive and its scope is
// decided here, so there is no safe default for "I could not find one of the
// operations you named".
func TestRevertMetadataFetchJob_UnknownOperationIsAnError(t *testing.T) {
	store, updated := revertStore(t, nil, nil)

	err := runRevertWithIDs(t, store, "does-not-exist")
	require.Error(t, err, "an unresolvable id must not be silently skipped")
	assert.Contains(t, err.Error(), "not found in either operations keyspace")
	assert.Nil(t, *updated)
}
