// file: internal/server/operation_v1_shape_user_text_test.go
// version: 1.0.0
// guid: 2f8b5d73-a691-4c0e-b384-7d1e6a9f5c20
// last-edited: 2026-10-26

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The reconcile scan read and the candidate-fetch results serve a stored
// operation in the v1 shape, message and error included. Both are read through
// the caller's text rule: a caller without users.manage gets
// activity.WithheldUserText for text that names a user, the admin gets it whole,
// and an ordinary message is served as it is to both.
// Not parallel: it swaps the process-wide config.
func TestV1ShapeOperations_TextNamingAUserNeedsUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	const marker = "01INVENTEDV1SHAPEUSER000A"
	userErr := "merge failed: user_id=" + marker + " could not be moved"
	userMsg := "moving state for user " + marker
	for id, def := range map[string]string{"op-reconcile-scan": "reconcile.scan", "op-candidate-fetch": "metadata.candidate-fetch"} {
		e := userErr
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "failed", Params: `{}`,
			ErrorMessage: &e, ProgressMessage: userMsg, QueuedAt: now, CompletedAt: &now,
		}))
	}

	for name, path := range map[string]string{
		"reconcile scan latest":   "/api/v1/operations/reconcile/scan/latest",
		"candidate-fetch results": "/api/v1/operations/op-candidate-fetch/results",
	} {
		w := getAs(f.srv, mgr, path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", name, w.Body.String())
		require.NotContains(t, w.Body.String(), marker, "%s: the message or error names an account", name)
		require.Contains(t, w.Body.String(), activity.WithheldUserText, "%s", name)

		w = getAs(f.srv, f.admin, path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", name, w.Body.String())
		require.Contains(t, w.Body.String(), marker, "%s: the admin reads it whole", name)
	}
}
