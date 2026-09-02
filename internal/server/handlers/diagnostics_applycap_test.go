// file: internal/server/handlers/diagnostics_applycap_test.go
// version: 1.1.0
// guid: 5f2c8d17-9b3e-4a60-b7d4-1c6e0a2f8b95
// last-edited: 2026-09-02

package handlers_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bulk apply cap (internal/applycap) on /diagnostics/apply-suggestions.
// The unit the cap counts is a BOOK WRITE, not an approved suggestion id: a
// merge_versions suggestion writes every book in its BookIDs. So the fixtures
// below are operations whose stored suggestions carry several books each, and
// the bogus/known-good pair is "two suggestions, six books" (refused at cap 5
// even though 2 < 5) against "five books" (admitted, reaches the apply loop).
// An id that matches no stored suggestion must not count at all.

func withDiagBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

// diagOpWithSuggestions is an OperationV2 whose result data holds n
// suggestions, each with booksEach book ids and the given action.
func diagOpWithSuggestions(t *testing.T, n, booksEach int, action string) *database.OperationV2Row {
	t.Helper()
	suggestions := make([]map[string]any, 0, n)
	for i := range n {
		ids := make([]string, 0, booksEach)
		for j := range booksEach {
			ids = append(ids, "book-"+strconv.Itoa(i)+"-"+strconv.Itoa(j))
		}
		suggestions = append(suggestions, map[string]any{"id": "s" + strconv.Itoa(i), "action": action, "book_ids": ids})
	}
	raw, err := json.Marshal(map[string]any{"suggestions": suggestions})
	require.NoError(t, err)
	rd := string(raw)
	return &database.OperationV2Row{ID: "op1", ResultData: &rd}
}

func applySuggestionsBody(t *testing.T, approved ...string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"operation_id": "op1", "approved_suggestion_ids": approved})
	require.NoError(t, err)
	return string(b)
}

func TestDiagnosticsHandler_ApplySuggestions_CountsBookWritesNotSuggestionIDs(t *testing.T) {
	withDiagBulkApplyCap(t, 5)
	// Two approved suggestions, three books each: 2 ids would pass a
	// suggestion-count gate, 6 book writes must not pass a cap of 5. The
	// mock has no merge/update expectations, so any write fails the test.
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op1").Return(diagOpWithSuggestions(t, 2, 3, "merge_versions"), nil).Once()
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, "s0", "s1"), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "BULK_APPLY_CAP_EXCEEDED")
	assert.Contains(t, w.Body.String(), "6 items")
	assert.Contains(t, w.Body.String(), "cap is 5")
}

func TestDiagnosticsHandler_ApplySuggestions_ExactlyTheCapReachesTheApplyLoop(t *testing.T) {
	withDiagBulkApplyCap(t, 5)
	store := databasemocks.NewMockStore(t)
	// Five books across five suggestions with an action the switch does not
	// know: the loop runs (proving the gate admitted it) and every item lands
	// in "failed" without touching the store.
	store.EXPECT().GetOperationV2("op1").Return(diagOpWithSuggestions(t, 5, 1, "not-an-action"), nil).Once()
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, "s0", "s1", "s2", "s3", "s4"), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"failed":5`)
}

func TestDiagnosticsHandler_ApplySuggestions_UnmatchedIDsDoNotCount(t *testing.T) {
	withDiagBulkApplyCap(t, 5)
	store := databasemocks.NewMockStore(t)
	// One stored suggestion with one book; the request approves it plus 100
	// ids that match nothing. Only the one real book write counts.
	store.EXPECT().GetOperationV2("op1").Return(diagOpWithSuggestions(t, 1, 1, "not-an-action"), nil).Once()
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	approved := []string{"s0"}
	for i := range 100 {
		approved = append(approved, "ghost-"+strconv.Itoa(i))
	}
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, approved...), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestDiagnosticsHandler_ApplySuggestions_ZeroConfigIsTheDefaultCap(t *testing.T) {
	withDiagBulkApplyCap(t, 0)
	store := databasemocks.NewMockStore(t)
	// One suggestion carrying Default+1 books: refused under the default cap.
	store.EXPECT().GetOperationV2("op1").Return(diagOpWithSuggestions(t, 1, applycap.Default+1, "merge_versions"), nil).Once()
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, "s0"), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}
