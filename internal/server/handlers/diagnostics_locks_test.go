// file: internal/server/handlers/diagnostics_locks_test.go
// version: 1.0.0
// guid: 7d2e5b1a-4c8f-4e93-a6b0-2f9d3c7e8a15
// last-edited: 2026-09-02

package handlers_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// An AI fix_metadata / reassign_series suggestion is a hard overwrite of Title
// or SeriesID. The user's lock on that field beats the suggestion: the book is
// not written and the suggestion is reported as skipped, not applied and not
// failed. A lock on a DIFFERENT field does not block the write.

func diagLockedRow(bookID, key string) []database.MetadataFieldState {
	return []database.MetadataFieldState{{BookID: bookID, Field: key, OverrideLocked: true}}
}

func diagSuggestionOp(suggestions string) *database.OperationV2Row {
	rd := `{"suggestions":[` + suggestions + `]}`
	return &database.OperationV2Row{ID: "op-1", Status: "completed", ResultData: &rd}
}

func TestDiagnosticsHandler_ApplySuggestions_LockedTitleIsSkippedNotOverwritten(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	seriesID := 4
	store.EXPECT().GetOperationV2("op-1").Return(diagSuggestionOp(
		`{"id":"s1","action":"fix_metadata","book_ids":["b1"],"fix":"{\"title\":\"AI Title\"}"},`+
			`{"id":"s2","action":"reassign_series","book_ids":["b1"],"fix":"{\"series_id\":9}"}`), nil)
	// The same book twice: once per suggestion.
	store.EXPECT().GetBookByID("b1").RunAndReturn(func(string) (*database.Book, error) {
		return &database.Book{ID: "b1", Title: "User Title", SeriesID: &seriesID}, nil
	}).Times(2)
	store.EXPECT().GetMetadataFieldStates("b1").Return(diagLockedRow("b1", database.FieldKeyTitle), nil).Times(2)
	// Only the series reassignment (unlocked) reaches the store, with the
	// user's title intact.
	store.EXPECT().UpdateBook("b1", mock.MatchedBy(func(b *database.Book) bool {
		return b.Title == "User Title" && b.SeriesID != nil && *b.SeriesID == 9
	})).Return(nil, nil).Once()

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s1","s2"]}`, nil)
	h.ApplySuggestions(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"applied":1`)
	assert.Contains(t, w.Body.String(), `"skipped":1`)
	assert.Contains(t, w.Body.String(), `"failed":0`)
	assert.Contains(t, w.Body.String(), `suggestion s1: b1 (title)`)
}

func TestDiagnosticsHandler_ApplySuggestions_LockedSeriesBlocksReassign(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	seriesID := 4
	store.EXPECT().GetOperationV2("op-1").Return(diagSuggestionOp(
		`{"id":"s2","action":"reassign_series","book_ids":["b1"],"fix":"{\"series_id\":9}"}`), nil)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "User Title", SeriesID: &seriesID}, nil)
	store.EXPECT().GetMetadataFieldStates("b1").Return(diagLockedRow("b1", database.FieldKeySeriesName), nil)
	// No UpdateBook expectation: any write fails the test.

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s2"]}`, nil)
	h.ApplySuggestions(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"applied":0`)
	assert.Contains(t, w.Body.String(), `"skipped":1`)
	assert.Contains(t, w.Body.String(), `suggestion s2: b1 (series_name)`)
}

func TestDiagnosticsHandler_ApplySuggestions_LockReadErrorFailsTheSuggestion(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-1").Return(diagSuggestionOp(
		`{"id":"s1","action":"fix_metadata","book_ids":["b1"],"fix":"{\"title\":\"AI Title\"}"}`), nil)
	store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "User Title"}, nil)
	store.EXPECT().GetMetadataFieldStates("b1").Return(nil, errors.New("pebble: closed"))
	// No UpdateBook expectation: fail closed.

	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"applied":0`)
	assert.Contains(t, w.Body.String(), `"failed":1`)
	assert.Contains(t, w.Body.String(), database.ErrFieldLocksUnavailable.Error())
}
