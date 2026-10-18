// file: internal/server/unreadable_books_count_test.go
// version: 1.0.1
// guid: 7c3f1a92-5e04-4d8b-b6a1-2e9d0f47c815
// last-edited: 2026-10-18
//
// The bulk-apply surfaces report how many books the sibling-part index could
// not read: batch-apply-candidates answers it as unreadable_books, and the
// preview op saves it in a reserved index row.

package server

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// An operation row the index cannot decode, for a book with nothing else
// known, is counted and blocks nothing: the requested book still applies.
func TestBatchApplyCandidates_ReportsUnreadableBooks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withBadRow bool
		want       string
	}{
		{"one undecodable sibling row", true, `"unreadable_books":1`},
		{"control: every row readable", false, `"unreadable_books":0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &database.MockStore{}
			f := newPreflightFixture(t, store, "{title} - {track:02d}")
			f.countWrites()
			rows, err := store.GetOperationResultsFunc("op")
			require.NoError(t, err)
			if tc.withBadRow {
				rows = append(rows, database.OperationResult{OperationID: "op", BookID: "b2", ResultJSON: "{", Status: "matched"})
			}
			store.GetOperationResultsFunc = func(string) ([]database.OperationResult, error) { return rows, nil }
			b1 := store.GetBookByIDFunc
			store.GetBookByIDFunc = func(id string) (*database.Book, error) {
				if id != "b1" {
					return nil, nil // nothing known about b2
				}
				return b1(id)
			}

			body := runBatchApplyCandidates(t, store, nil).Body.String()
			require.Contains(t, body, tc.want, body)
			require.Contains(t, body, `"applied":1`, body)
			require.Contains(t, body, `"blocked_count":0`, body)
		})
	}
}

// memResults is an in-memory previewResultStore.
type memResults struct {
	mu   sync.Mutex
	rows []database.OperationResult
}

func (m *memResults) CreateOperationResult(r *database.OperationResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, *r)
	return nil
}

func (m *memResults) GetOperationResults(string) ([]database.OperationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]database.OperationResult(nil), m.rows...), nil
}

// The preview op saves the count in a reserved index row under its own op id,
// keyed so it can never be mistaken for a book's report row (no verdict).
func TestBulkApplyPreview_IndexRowSavesUnreadableBooks(t *testing.T) {
	results := &memResults{}
	require.NoError(t, writePreviewIndexRow(results, "pv", 2))

	rows, err := results.GetOperationResults("pv")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, previewIndexRowID, rows[0].BookID)
	require.Equal(t, "index", rows[0].Status)
	require.JSONEq(t, `{"unreadable_books":2}`, rows[0].ResultJSON)

	var asReportRow bulkApplyPreviewRow
	require.NoError(t, json.Unmarshal([]byte(rows[0].ResultJSON), &asReportRow))
	require.Empty(t, asReportRow.Verdict, "the index row must carry no verdict")
}
