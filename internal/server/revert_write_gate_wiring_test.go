// file: internal/server/revert_write_gate_wiring_test.go
// version: 1.0.0
// guid: 2a7c5e91-8d36-4b04-9f1a-c60e3b8d7254
// last-edited: 2026-11-18

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestWireHandlers_RevertWriteGateIsInstalled: POST /operations/:id/revert
// through the real router is refused with 409 while another writer holds the
// write-set the reverted operation's definition declares, and runs once it is
// released. Without the server giving the operations handler the registry as the
// gate, the revert would run beside a series prune or merge; the handler-level
// tests use a fake gate and cannot see a missing wiring.
func TestWireHandlers_RevertWriteGateIsInstalled(t *testing.T) {
	srv, _, admin := repairsPermServer(t, true, true)
	require.NotNil(t, srv.opRegistry)

	const defID = "maintenance.series-denumber"
	declared := srv.opRegistry.WriteSetOfDef(defID)
	require.Contains(t, declared, opsregistry.ResSeries, "the denumber op declares a series write")

	now := time.Now().UTC()
	require.NoError(t, srv.storeForWiring().InsertOperationV2(database.OperationV2Row{
		ID: "op-denumber-run", DefID: defID, Plugin: "maintenance", Status: "completed",
		Params: `{}`, QueuedAt: now, CompletedAt: &now,
	}))
	revert := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/op-denumber-run/revert", nil)
		req.Header.Set("Authorization", "Bearer "+admin)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w
	}

	release, err := srv.opRegistry.HoldWriteSet("test:series-writer", "a series merge", []opsregistry.Resource{opsregistry.ResSeries})
	require.NoError(t, err)
	w := revert()
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "a series merge")

	release()
	w = revert()
	assert.NotEqual(t, http.StatusConflict, w.Code, "released: the revert is no longer refused by the gate: %s", w.Body.String())

	// And the revert itself does not leave its claim behind: a second writer can take it.
	again, err := srv.opRegistry.HoldWriteSet("test:series-writer-2", "another series merge", []opsregistry.Resource{opsregistry.ResSeries})
	require.NoError(t, err, "the revert released its claim when it ended")
	again()
}
