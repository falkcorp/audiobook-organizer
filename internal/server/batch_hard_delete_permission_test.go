// file: internal/server/batch_hard_delete_permission_test.go
// version: 1.4.1
// guid: e0a974ef-0780-48c3-921a-e32b2e124b6c
// last-edited: 2026-10-30

package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const batchOpsPath = "/api/v1/audiobooks/batch-operations"

// TestBatchOperations_HardDeleteNeedsLibraryDelete: POST
// /audiobooks/batch-operations is guarded by library.edit_metadata, and a hard
// delete in the list also needs library.delete, as DELETE /audiobooks/:id
// does. The check is the handler's, made on the request it just bound and is
// about to run, so a body the handler reads as a hard delete is refused
// however the bytes are shaped. A role holding edit_metadata but not delete
// ("metadata-only") gets 403 naming library.delete and the book
// survives for: a plain hard delete, a hard delete after other operations,
// trailing bytes after the JSON value (the handler's decoder reads one value
// and ignores the rest), and a duplicate hard_delete key (the last wins). The
// same role may still update. Editor and admin (both hold
// library.delete) really hard delete a file-less book through the router, which
// also proves the wired store resolves the owned-files check. Soft delete and
// restore are covered by TestBatchRoutes_TrashAndRestoreNeedTheSingleBookPermissions.
// Not parallel: it swaps the process-wide config.
func TestBatchOperations_HardDeleteNeedsLibraryDelete(t *testing.T) {
	srv, _, adminToken := repairsPermServer(t, true, true)
	ps, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "test store is a PebbleStore")

	_, err := ps.CreateRole(&database.Role{
		ID: "metadata-only", Name: "metadata-only",
		Permissions: []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata},
	})
	require.NoError(t, err)
	session := func(name, role string) string {
		u, err := ps.CreateUser(name, name+"@example.test", "bcrypt", "hash", []string{role}, "active")
		require.NoError(t, err)
		s, err := ps.CreateSession(u.ID, "127.0.0.1", "test", time.Hour, 0)
		require.NoError(t, err)
		return s.ID
	}
	meta := session("meta", "metadata-only")
	editor := session("ed2", auth.SeedRoleEditor)

	newBook := func(title string) string {
		b, err := ps.CreateBook(&database.Book{Title: title, FilePath: "/lib/Gotham/" + title + ".m4b"})
		require.NoError(t, err)
		return b.ID
	}
	exists := func(id string) bool {
		b, err := ps.GetBookByID(id)
		return err == nil && b != nil
	}

	// Each shape runs on its own file-less book so a mutation shows which
	// shapes slip through.
	for _, tc := range []struct{ name, body string }{
		{"plain", `{"operations":[{"id":"%s","action":"delete","hard_delete":true}]}`},
		{"after update", `{"operations":[{"id":"%s","action":"update","updates":{"title":"t"}},{"id":"%s","action":"delete","hard_delete":true}]}`},
		{"trailing bytes", `{"operations":[{"id":"%s","action":"delete","hard_delete":true}]}x`},
		{"trailing value", `{"operations":[{"id":"%s","action":"delete","hard_delete":true}]}{}`},
		{"duplicate key", `{"operations":[{"id":"%s","action":"delete","hard_delete":false,"hard_delete":true}]}`},
	} {
		t.Run("metadata-only role refused: "+tc.name, func(t *testing.T) {
			id := newBook("Batman: Refused " + tc.name)
			body := strings.ReplaceAll(tc.body, "%s", id)
			w := postRepairs(srv, meta, batchOpsPath, body)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "permission denied: library.delete")
			require.True(t, exists(id), "the book must survive")
		})
	}
	t.Run("metadata-only role: a plain update still reaches the handler", func(t *testing.T) {
		id := newBook("Batman: Update")
		w := postRepairs(srv, meta, batchOpsPath, `{"operations":[{"id":"`+id+`","action":"update","updates":{"title":"t2"}}]}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.True(t, exists(id))
	})
	t.Run("editor and admin: a file-less book is really hard deleted", func(t *testing.T) {
		for _, tok := range []string{editor, adminToken} {
			id := newBook("Batman: Deleted " + tok[:4])
			w := postRepairs(srv, tok, batchOpsPath, `{"operations":[{"id":"`+id+`","action":"delete","hard_delete":true}]}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.False(t, exists(id), "the book is gone: %s", w.Body.String())
		}
	})
}

// TestBatchOperations_HardDeleteThroughTheWiredPermissionCheck: the handler's
// permission check is the one wireHandlers installs (SetPermissionCheck with
// the server's permCheck), which has the semantics of the route middleware:
// with auth disabled, and on a first run before any user exists, a batch hard
// delete needs no credentials at all. An unwired handler is strict (401 with no
// user), so dropping the SetPermissionCheck line makes both subtests fail.
// Not parallel: it swaps the process-wide config.
func TestBatchOperations_HardDeleteThroughTheWiredPermissionCheck(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enableAuth, users bool
	}{
		{"auth disabled", false, false},
		{"first run, no users yet", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := repairsPermServer(t, tc.enableAuth, tc.users)
			ps, ok := srv.storeForWiring().(*database.PebbleStore)
			require.True(t, ok, "test store is a PebbleStore")
			b, err := ps.CreateBook(&database.Book{Title: "Batman: Wired", FilePath: "/lib/Gotham/wired.m4b"})
			require.NoError(t, err)

			w := postRepairs(srv, "", batchOpsPath, `{"operations":[{"id":"`+b.ID+`","action":"delete","hard_delete":true}]}`)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			got, err := ps.GetBookByID(b.ID)
			require.True(t, err != nil || got == nil, "the book is gone: %s", w.Body.String())
		})
	}
}

const batchPath = "/api/v1/audiobooks/batch"

// TestBatchRoutes_TrashAndRestoreNeedTheSingleBookPermissions: both bulk routes
// are mounted on library.edit_metadata, but moving books to the trash is the
// single-book DELETE's act (library.delete) and taking them out is the
// single-book restore's (library.organize), so the handler asks for those too,
// on the request it bound. Three roles: "metadata-only" (view and edit_metadata,
// refused both), "no-organize" (adds library.delete, refused restore only) and
// the seeded editor (holds both). Every shape is tried as sent and with
// trailing bytes or a duplicate key, where the handler's decoder reads one value
// and the last duplicate wins, so a body the handler reads as a trash or restore
// is refused however the bytes are shaped. Not parallel: it swaps the
// process-wide config.
func TestBatchRoutes_TrashAndRestoreNeedTheSingleBookPermissions(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	ps, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "test store is a PebbleStore")

	for id, perms := range map[string][]auth.Permission{
		"metadata-only": {auth.PermLibraryView, auth.PermLibraryEditMetadata},
		"no-organize":   {auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermLibraryDelete},
	} {
		_, err := ps.CreateRole(&database.Role{ID: id, Name: id, Permissions: perms})
		require.NoError(t, err)
	}
	session := func(name, role string) string {
		u, err := ps.CreateUser(name, name+"@example.test", "bcrypt", "hash", []string{role}, "active")
		require.NoError(t, err)
		s, err := ps.CreateSession(u.ID, "127.0.0.1", "test", time.Hour, 0)
		require.NoError(t, err)
		return s.ID
	}
	meta := session("meta-trash", "metadata-only")
	noOrganize := session("no-org", "no-organize")
	editor := session("ed-trash", auth.SeedRoleEditor)

	newBook := func(title string, trashed bool) string {
		b := &database.Book{Title: title, FilePath: "/lib/Gotham/" + title + ".m4b"}
		if trashed {
			yes := true
			b.MarkedForDeletion = &yes
		}
		b, err := ps.CreateBook(b)
		require.NoError(t, err)
		return b.ID
	}
	trashed := func(id string) bool {
		b, err := ps.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		return b.IsSoftDeleted()
	}

	// Each shape: the route, a body template (%s is the book id), and the
	// permission the refusal must name.
	type shape struct{ name, path, body string }
	trashShapes := []shape{
		{"ops delete", batchOpsPath, `{"operations":[{"id":"%s","action":"delete"}]}`},
		{"ops delete hard_delete false", batchOpsPath, `{"operations":[{"id":"%s","action":"delete","hard_delete":false}]}`},
		{"ops update marked_for_deletion", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"marked_for_deletion":true}}]}`},
		{"ops update library_state", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"library_state":"deleted"}}]}`},
		{"ops after metadata update", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"title":"t"}},{"id":"%s","action":"delete"}]}`},
		{"ops trailing bytes", batchOpsPath, `{"operations":[{"id":"%s","action":"delete"}]}x`},
		{"ops trailing value", batchOpsPath, `{"operations":[{"id":"%s","action":"delete"}]}{}`},
		{"ops duplicate key", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"marked_for_deletion":false,"marked_for_deletion":true}}]}`},
		{"batch marked_for_deletion", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":true}}`},
		{"batch library_state", batchPath, `{"ids":["%s"],"updates":{"library_state":"Deleted"}}`},
		{"batch trailing bytes", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":true}}x`},
		{"batch trailing value", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":true}}{}`},
		{"batch duplicate key", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":false,"marked_for_deletion":true}}`},
	}
	restoreShapes := []shape{
		{"ops restore", batchOpsPath, `{"operations":[{"id":"%s","action":"restore"}]}`},
		{"ops update marked_for_deletion false", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"marked_for_deletion":false}}]}`},
		{"ops trailing bytes", batchOpsPath, `{"operations":[{"id":"%s","action":"restore"}]}x`},
		{"ops duplicate key", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"marked_for_deletion":true,"marked_for_deletion":false}}]}`},
		{"batch marked_for_deletion false", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":false}}`},
		{"batch trailing bytes", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":false}}x`},
		{"batch trailing value", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":false}}{}`},
		{"batch duplicate key", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":true,"marked_for_deletion":false}}`},
	}

	for _, tc := range trashShapes {
		t.Run("trash refused without library.delete: "+tc.name, func(t *testing.T) {
			id := newBook("Batman: Trash "+tc.name, false)
			w := postRepairs(srv, meta, tc.path, strings.ReplaceAll(tc.body, "%s", id))
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "permission denied: library.delete")
			require.False(t, trashed(id), "the book must not be trashed")
		})
	}
	for _, tc := range restoreShapes {
		for who, tok := range map[string]string{"metadata-only": meta, "no-organize": noOrganize} {
			t.Run("restore refused without library.organize ("+who+"): "+tc.name, func(t *testing.T) {
				id := newBook("Batman: Restore "+who+" "+tc.name, true)
				require.True(t, trashed(id), "the fixture book starts in the trash")
				w := postRepairs(srv, tok, tc.path, strings.ReplaceAll(tc.body, "%s", id))
				require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "permission denied: library.organize")
				require.True(t, trashed(id), "the book must stay in the trash")
			})
		}
	}

	t.Run("no-organize role may still trash, and plain metadata edits pass on both routes", func(t *testing.T) {
		id := newBook("Batman: Allowed Trash", false)
		w := postRepairs(srv, noOrganize, batchOpsPath, `{"operations":[{"id":"`+id+`","action":"delete"}]}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.True(t, trashed(id), "library.delete is enough to trash")

		for _, tok := range []string{meta, noOrganize} {
			id := newBook("Batman: Meta Edit", false)
			for path, body := range map[string]string{
				batchOpsPath: `{"operations":[{"id":"` + id + `","action":"update","updates":{"title":"Batman: Edited"}}]}`,
				batchPath:    `{"ids":["` + id + `"],"updates":{"genre":"Fantasy"}}`,
			} {
				w := postRepairs(srv, tok, path, body)
				require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
			}
			require.False(t, trashed(id))
		}
	})
	t.Run("editor: trash and restore both work on both routes", func(t *testing.T) {
		for _, tc := range []struct{ name, path, trash, restore string }{
			{"ops", batchOpsPath, `{"operations":[{"id":"%s","action":"delete"}]}`, `{"operations":[{"id":"%s","action":"restore"}]}`},
			{"batch", batchPath, `{"ids":["%s"],"updates":{"marked_for_deletion":true}}`, `{"ids":["%s"],"updates":{"marked_for_deletion":false}}`},
		} {
			id := newBook("Batman: Editor "+tc.name, false)
			w := postRepairs(srv, editor, tc.path, strings.ReplaceAll(tc.trash, "%s", id))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.True(t, trashed(id), "%s: trashed", tc.name)
			w = postRepairs(srv, editor, tc.path, strings.ReplaceAll(tc.restore, "%s", id))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.False(t, trashed(id), "%s: restored", tc.name)
		}
	})
}

// TestBatchRoutes_LibraryStateEditOnATrashedBookIsARestore: a bulk edit that
// sets library_state to a live value on a book in the trash takes it out of the
// trash without looking like a restore (no marked_for_deletion, no restore
// action), so the request's shape cannot decide the permission. The service
// judges each row after loading it against what the caller holds: without
// library.organize the row fails ("not changed", 200 with failed=1, nothing
// written); with it the edit goes through; a book that is not in the trash is a
// plain metadata edit for either role. Both trash representations are tried
// (labelled "deleted" only, and flagged with a label kept), on both routes, and
// one body with trailing bytes. Not parallel: it swaps the process-wide config.
func TestBatchRoutes_LibraryStateEditOnATrashedBookIsARestore(t *testing.T) {
	srv, _, _ := repairsPermServer(t, true, true)
	ps, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "test store is a PebbleStore")

	for id, perms := range map[string][]auth.Permission{
		"state-metadata-only": {auth.PermLibraryView, auth.PermLibraryEditMetadata},
		"state-organize":      {auth.PermLibraryView, auth.PermLibraryEditMetadata, auth.PermLibraryOrganize},
	} {
		_, err := ps.CreateRole(&database.Role{ID: id, Name: id, Permissions: perms})
		require.NoError(t, err)
	}
	session := func(name, role string) string {
		u, err := ps.CreateUser(name, name+"@example.test", "bcrypt", "hash", []string{role}, "active")
		require.NoError(t, err)
		s, err := ps.CreateSession(u.ID, "127.0.0.1", "test", time.Hour, 0)
		require.NoError(t, err)
		return s.ID
	}
	meta := session("state-meta", "state-metadata-only")
	organizer := session("state-org", "state-organize")

	deleted := "deleted"
	newBook := func(title string, label, flagged bool) string {
		b := &database.Book{Title: title, FilePath: "/lib/Gotham/" + title + ".m4b"}
		if label {
			b.LibraryState = &deleted
		}
		if flagged {
			yes := true
			b.MarkedForDeletion = &yes
		}
		b, err := ps.CreateBook(b)
		require.NoError(t, err)
		return b.ID
	}
	state := func(id string) (inTrash bool, label string) {
		b, err := ps.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		if b.LibraryState != nil {
			label = *b.LibraryState
		}
		return database.IsInTrash(b), label
	}

	routes := []struct{ name, path, body string }{
		{"batch-operations", batchOpsPath, `{"operations":[{"id":"%s","action":"update","updates":{"library_state":"organized"}}]}`},
		{"batch", batchPath, `{"ids":["%s"],"updates":{"library_state":"organized"}}`},
		{"batch trailing bytes", batchPath, `{"ids":["%s"],"updates":{"library_state":"organized"}}x`},
	}
	for _, rt := range routes {
		for _, kind := range []struct {
			name           string
			label, flagged bool
		}{{"labelled only", true, false}, {"flagged and labelled", true, true}} {
			t.Run("refused row without library.organize: "+rt.name+", "+kind.name, func(t *testing.T) {
				id := newBook("Batman: State "+rt.name+kind.name, kind.label, kind.flagged)
				w := postRepairs(srv, meta, rt.path, strings.ReplaceAll(rt.body, "%s", id))
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "library.organize")
				require.Contains(t, w.Body.String(), `"failed":1`)
				inTrash, label := state(id)
				require.True(t, inTrash, "the book must stay in the trash")
				require.Equal(t, "deleted", label)
			})
			t.Run("allowed with library.organize: "+rt.name+", "+kind.name, func(t *testing.T) {
				id := newBook("Batman: Organizer "+rt.name+kind.name, kind.label, kind.flagged)
				w := postRepairs(srv, organizer, rt.path, strings.ReplaceAll(rt.body, "%s", id))
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), `"success":1`)
				_, label := state(id)
				require.Equal(t, "organized", label)
			})
		}
		t.Run("a book that is not in the trash is a plain edit: "+rt.name, func(t *testing.T) {
			id := newBook("Batman: Live "+rt.name, false, false)
			w := postRepairs(srv, meta, rt.path, strings.ReplaceAll(rt.body, "%s", id))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"success":1`)
			_, label := state(id)
			require.Equal(t, "organized", label)
		})
	}
}

// TestBatchRoutes_TrashAndRestoreThroughTheWiredHolds: the grants the service
// judges each row by come from the permission probe wireHandlers installs
// (SetPermissionHolds with the server's permHolds), which holds every
// permission with auth disabled and on a first run. An unwired handler is
// strict and grants nothing without a user, so dropping the SetPermissionHolds
// line makes both subtests fail on the row-level refusal. Not parallel: it
// swaps the process-wide config.
func TestBatchRoutes_TrashAndRestoreThroughTheWiredHolds(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enableAuth, users bool
	}{
		{"auth disabled", false, false},
		{"first run, no users yet", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := repairsPermServer(t, tc.enableAuth, tc.users)
			ps, ok := srv.storeForWiring().(*database.PebbleStore)
			require.True(t, ok, "test store is a PebbleStore")
			deleted := "deleted"
			b, err := ps.CreateBook(&database.Book{Title: "Batman: Wired Holds", FilePath: "/lib/Gotham/wired-holds.m4b", LibraryState: &deleted})
			require.NoError(t, err)

			w := postRepairs(srv, "", batchPath, `{"ids":["`+b.ID+`"],"updates":{"library_state":"organized"}}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"success":1`)
			got, err := ps.GetBookByID(b.ID)
			require.NoError(t, err)
			require.False(t, database.IsInTrash(got), "taken out of the trash")

			w = postRepairs(srv, "", batchOpsPath, `{"operations":[{"id":"`+b.ID+`","action":"update","updates":{"library_state":"deleted"}}]}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), `"success":1`)
			got, err = ps.GetBookByID(b.ID)
			require.NoError(t, err)
			require.True(t, database.IsInTrash(got), "moved to the trash")
		})
	}
}
