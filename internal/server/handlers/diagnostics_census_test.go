// file: internal/server/handlers/diagnostics_census_test.go
// version: 1.0.0
// guid: 90aa916c-0432-4200-8e35-00b08069563b
// last-edited: 2026-10-03

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newCensusHandlerStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	p, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()
	return p
}

// callDBCensus runs GetDBCensus and returns the unwrapped payload.
func callDBCensus(t *testing.T, store diagnosticsStore, rawQuery string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/diagnostics/db-census?"+rawQuery, nil)

	NewDiagnosticsHandler(store, nil, nil, nil, nil).GetDBCensus(c)

	require.Equal(t, http.StatusOK, w.Code, "body %s", w.Body.String())
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	if data, ok := envelope["data"].(map[string]any); ok {
		return data
	}
	return envelope
}

func TestGetDBCensus_ReturnsFamilies(t *testing.T) {
	p := newCensusHandlerStore(t)
	payload := callDBCensus(t, p, "")

	fams, ok := payload["families"].([]any)
	require.True(t, ok, "families must be an array: %v", payload)
	prefixes := map[string]bool{}
	for _, f := range fams {
		prefixes[f.(map[string]any)["prefix"].(string)] = true
	}
	require.True(t, prefixes["(unregistered)"])
	require.True(t, prefixes["book_ver:"])
	require.Nil(t, payload["history"], "history must be absent or null without deep")
	require.NotEmpty(t, payload["family_figures_basis"])
	require.NotEmpty(t, payload["notes"])
	require.NotNil(t, payload["retired"], "warm memdb must report retired counts")
	require.NotNil(t, payload["signals"])
}

func TestGetDBCensus_DeepAddsHistory(t *testing.T) {
	p := newCensusHandlerStore(t)
	for i := 0; i < 4; i++ {
		require.NoError(t, p.SetRaw(fmt.Sprintf("book_ver:BOOKX:%020d", i), []byte("{}")))
	}
	payload := callDBCensus(t, p, "deep=true&fresh=true")
	h, ok := payload["history"].(map[string]any)
	require.True(t, ok, "deep=true must add history: %v", payload)
	require.EqualValues(t, 4, h["entries"])
	require.EqualValues(t, 1, h["books_with_history"])
}

// censusStoreStub has no DBCensus capability: the handler must answer 500, not
// a 200 with an empty census.
type censusStoreStub struct{ diagnosticsStore }

func TestGetDBCensus_NonPebbleStoreIsAnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/diagnostics/db-census", nil)
	NewDiagnosticsHandler(censusStoreStub{}, nil, nil, nil, nil).GetDBCensus(c)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
