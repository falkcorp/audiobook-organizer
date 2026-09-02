// file: internal/server/handlers/diagnostics_applycap_test.go
// version: 1.0.0
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
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bulk apply cap (internal/applycap) on /diagnostics/apply-suggestions:
// approved suggestions become merges and deletes. Bogus/known-good pair —
// cap+1 ids are refused before the store is touched; exactly cap ids reach
// the operation lookup.

func withDiagBulkApplyCap(t *testing.T, n int) {
	t.Helper()
	prev := config.AppConfig.BulkApplyMaxItems
	config.AppConfig.BulkApplyMaxItems = n
	t.Cleanup(func() { config.AppConfig.BulkApplyMaxItems = prev })
}

func applySuggestionsBody(t *testing.T, n int) string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, "s"+strconv.Itoa(i))
	}
	b, err := json.Marshal(map[string]any{"operation_id": "op1", "approved_suggestion_ids": ids})
	require.NoError(t, err)
	return string(b)
}

func TestDiagnosticsHandler_ApplySuggestions_RefusesOverTheBulkApplyCap(t *testing.T) {
	withDiagBulkApplyCap(t, 3)
	// No expectations on the store: mockery fails the test on any call, which
	// is the "nothing was touched" half of the assertion.
	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, 4), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "BULK_APPLY_CAP_EXCEEDED")
	assert.Contains(t, w.Body.String(), "cap is 3")
}

func TestDiagnosticsHandler_ApplySuggestions_ExactlyTheCapReachesTheStore(t *testing.T) {
	withDiagBulkApplyCap(t, 3)
	store := databasemocks.NewMockStore(t)
	// Reaching the operation lookup proves the gate let the request through;
	// a missing op then 404s, which keeps the test independent of the merge
	// machinery downstream.
	store.EXPECT().GetOperationV2("op1").Return(nil, nil).Once()
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, 3), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestDiagnosticsHandler_ApplySuggestions_ZeroConfigIsTheDefaultCap(t *testing.T) {
	withDiagBulkApplyCap(t, 0)
	store := databasemocks.NewMockStore(t)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions", applySuggestionsBody(t, applycap.Default+1), nil)
	h.ApplySuggestions(c)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}
