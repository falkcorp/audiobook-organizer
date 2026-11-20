// file: internal/server/reconcile_apply_permission_test.go
// version: 1.1.0
// guid: 6b0f3d92-7a14-4c58-9e21-d5a83c7f1b04
// last-edited: 2026-11-19

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// TestReconcileApplyRoute_NeedsSettingsManage: reconcile.apply declares
// settings.manage and repoints books (and their file rows), but its own route
// POST /operations/reconcile was mounted on scan.trigger, which the seeded
// editor holds, so an editor could start it there while the generic
// POST /operations/v2 refused the same op. The route now enforces what the
// definition declares. Four callers through the real router:
//
//   - a custom role holding exactly view and scan.trigger (the old gate) is 403
//     naming settings.manage;
//   - the seeded editor is 403 the same way;
//   - a custom role holding exactly view and settings.manage reaches the
//     handler (400: an empty match list);
//   - the seeded admin reaches the handler too.
//
// The handler-direct tests of this package skip the route middleware, so this
// goes through srv.router: it fails with the route on scan.trigger.
// Not parallel: it swaps the process-wide config.
func TestReconcileApplyRoute_NeedsSettingsManage(t *testing.T) {
	const path = "/api/v1/operations/reconcile"
	srv, editor, admin := repairsPermServer(t, true, true)
	ps, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "test store is a PebbleStore")

	session := func(name, role string, perms []auth.Permission) string {
		_, err := ps.CreateRole(&database.Role{ID: role, Name: role, Permissions: perms})
		require.NoError(t, err)
		u, err := ps.CreateUser(name, name+"@example.test", "bcrypt", "hash", []string{role}, "active")
		require.NoError(t, err)
		s, err := ps.CreateSession(u.ID, "127.0.0.1", "test", time.Hour, 0)
		require.NoError(t, err)
		return s.ID
	}
	scanOnly := session("scanonly", "scan-trigger-only", []auth.Permission{auth.PermLibraryView, auth.PermScanTrigger})
	settingsOnly := session("settingsonly", "settings-manage-only", []auth.Permission{auth.PermLibraryView, auth.PermSettingsManage})

	body := `{"matches":[]}`
	for _, tc := range []struct {
		name, token string
	}{
		{"scan.trigger only", scanOnly},
		{"seeded editor", editor},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			w := postRepairs(srv, tc.token, path, body)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "permission denied: settings.manage")
		})
	}
	for _, tc := range []struct {
		name, token string
	}{
		{"settings.manage only", settingsOnly},
		{"seeded admin", admin},
	} {
		t.Run(tc.name+" reaches the handler", func(t *testing.T) {
			w := postRepairs(srv, tc.token, path, body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "no matches provided")
		})
	}
}

// An unknown match_type is a 400 at the route, before an operation is queued:
// the type decides whether the book keeps its hashes (n4 of the #4719 review).
func TestReconcileApplyRoute_RefusesAnUnknownMatchType(t *testing.T) {
	const path = "/api/v1/operations/reconcile"
	srv, _, admin := repairsPermServer(t, true, true)
	w := postRepairs(srv, admin, path, `{"matches":[{"book_id":"b1","new_path":"/x/a.m4b","match_type":"hash"},{"book_id":"b2","new_path":"/x/b.m4b","match_type":"Hash"}]}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "unknown match_type")
	require.Contains(t, w.Body.String(), "match 1 (book b2)")
}

// The store the apply runs against (storeForWiring, for both the HEAD shell and the
// native kind) resolves the capability the row repoint needs: if it did not, every
// match with a file row would be refused as "cannot look up or rewrite file rows".
func TestReconcileApply_TheWiredStoreResolvesTheAtomicRepoint(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	_, ok := database.AsCapability[interface {
		ModifyBookFileUnlessPathHeld(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	}](srv.storeForWiring())
	require.True(t, ok, "the wired store must expose ModifyBookFileUnlessPathHeld through its decorators")
}
