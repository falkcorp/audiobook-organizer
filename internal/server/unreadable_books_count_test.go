// file: internal/server/unreadable_books_count_test.go
// version: 1.0.0
// guid: 7c3f1a92-5e04-4d8b-b6a1-2e9d0f47c815
// last-edited: 2026-09-13
//
// Both bulk-apply surfaces report how many books the sibling-part index could
// not read, under one field name: unreadable_books.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
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

// The preview op saves the count in its index row; the report's summary reads
// it back as unreadable_books and does not count that row as a report row.
func TestBulkApplyPreview_SummaryReportsUnreadableBooks(t *testing.T) {
	results := &memResults{}
	require.NoError(t, writePreviewIndexRow(results, "pv", 2))
	raw, err := json.Marshal(bulkApplyPreviewRow{BookID: "b1", Verdict: previewVerdictApply})
	require.NoError(t, err)
	require.NoError(t, results.CreateOperationResult(&database.OperationResult{OperationID: "pv", BookID: "b1", ResultJSON: string(raw), Status: previewVerdictApply}))

	store := &database.MockStore{GetOperationResultsFunc: results.GetOperationResults}
	gin.SetMode(gin.TestMode)
	s := &Server{store: store}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/metadata/bulk-apply-preview/pv", nil)
	c.Params = gin.Params{{Key: "id", Value: "pv"}}
	s.handleGetBulkApplyPreview(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	require.Contains(t, body, `"unreadable_books":2`, body)
	require.Contains(t, body, `"total":1`, body, "the index row must not count as a report row")
	require.NotContains(t, body, previewIndexRowID, "the index row must not be listed as a row")
}
