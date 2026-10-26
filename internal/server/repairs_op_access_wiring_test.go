// file: internal/server/repairs_op_access_wiring_test.go
// version: 1.5.0
// guid: 0c6b1f84-93d7-4e52-a8b0-d2f5e7a14c39
// last-edited: 2026-10-26

// The generic operations routes as the server wires them, driven through the
// real router: a Repairs operation's data (repairs.plan, repairs.apply) is
// readable only by a caller who holds the op's fixer's permissions, however it
// is reached (result, change journal, undo report, single-op read, log reads,
// timeline, event stream, and a book's change journal and changelog). A
// fixer's journal rows stay scoped after the op row is discarded. Journal rows
// that carry other users' own data (listening state, playlists) are readable
// only by a caller who may manage users, whoever may run the fixer. Other
// operations are unchanged for a viewer.

package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

const (
	opScopedPlan    = "op-repairs-plan-scoped"     // fixer needing settings.manage
	opDefaultApply  = "op-repairs-apply-default"   // fixer needing library.edit_metadata
	opNoFixerApply  = "op-repairs-apply-nofixer"   // a repairs op that states no fixer
	opUnknownFixer  = "op-repairs-apply-unknown"   // a fixer this build does not register
	opPlainScan     = "op-plain-scan"              // not a repairs op
	opDiscarded     = "op-repairs-apply-discarded" // a repairs apply whose op row was discarded
	opV3Plain       = "op-v3-plain-history"        // a v3 op whose journal rows carry its def id as Source
	opDiscardedOld  = "op-repairs-apply-prestamp"  // a discarded repairs apply journaled before Source stamping
	opDiscardedGone = "op-repairs-apply-removed"   // a discarded repairs apply of a fixer this build removed
	journalBookID   = "book-invented-journal"      // the book the journal rows below are on
	scopedFixerID   = "reconcile.missing-files"    // settings.manage (repairs_permission_wiring_test.go)
	secondaryUserID = "user-whose-state-was-read"  // appears in the repairs rows and logs

	// Each user-data row below carries its own marker, so an assertion that a
	// caller does not see one cannot pass because a different row is missing.
	listenerUnstampedFollow = "invented-listener-unstamped-follow"   // user_state_follow, Source ""
	listenerRemovedFixer    = "invented-listener-removed-fixer"      // user_state_follow, Source a removed fixer
	listenerDiscardedOld    = "invented-listener-prestamp-discard"   // user_state_follow, Source "", op row gone
	listenerDiscardedGone   = "invented-listener-removed-discard"    // user_state_follow, Source a removed fixer, op row gone
	playlistOfAnotherUser   = "invented-playlist-of-another-user"    // user_content_follow, playlist kind
	fixerFileMove           = "/invented/library/moved-by-fixer.m4b" // book_file_move stamped by the default fixer
	scopedUnstampedTitle    = "Invented Scoped Prestamp Title"       // metadata_update, Source "", scoped repairs op
)

// repairsOpAccessFixture seeds the five operations above with a result, a log
// line and (for the repairs ones) a body naming secondaryUserID, and returns
// the server with a session for each role.
type repairsOpAccessFixture struct {
	srv                        *Server
	viewer, editor, admin      string
	defaultFixerID             string
	scopedPlan, defaultApply   string
	noFixerApply, unknownFixer string
	plainScan                  string
	discarded                  string
	bookID                     string
	allRepairsOps              []string
}

func newRepairsOpAccessFixture(t *testing.T) *repairsOpAccessFixture {
	t.Helper()
	srv, editor, admin := repairsPermServer(t, true, true)
	store, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "the test server runs on a PebbleStore")
	vu, err := store.CreateUser("viewer", "viewer@x.test", "bcrypt", "hash", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	vs, err := store.CreateSession(vu.ID, "192.0.2.10", "test", time.Hour)
	require.NoError(t, err)

	// A fixer whose only requirement is the lane's default permission.
	require.NotNil(t, srv.repairFixers)
	defaultFixer := ""
	for _, f := range srv.repairFixers.List() {
		if p := repairs.RequiredPermissions(f); len(p) == 1 && p[0] == repairs.DefaultPermission {
			defaultFixer = f.ID()
			break
		}
	}
	require.NotEmpty(t, defaultFixer, "no registered fixer uses the default permission")
	scoped, ok := srv.repairFixers.Get(scopedFixerID)
	require.True(t, ok)
	require.Contains(t, repairs.RequiredPermissions(scoped), auth.PermSettingsManage)

	now := time.Now().UTC()
	body := func(fixer string) string {
		return `{"fixer_id":"` + fixer + `","results":[{"row_id":"r1","owner_user_id":"` + secondaryUserID + `"}]}`
	}
	insert := func(id, def, params, result string) {
		res := result
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "completed", Params: params,
			ResultData: &res, QueuedAt: now, CompletedAt: &now,
		}))
		require.NoError(t, store.AppendOpLogsV2([]database.OpLogV2Row{{
			OperationID: id, Level: "info", Message: "row ran for " + secondaryUserID, CreatedAt: now,
		}}))
	}
	insert(opScopedPlan, repairs.PlanOpID, `{"fixer_id":"`+scopedFixerID+`"}`, body(scopedFixerID))
	insert(opDefaultApply, repairs.ApplyOpID, `{"fixer_id":"`+defaultFixer+`"}`, body(defaultFixer))
	insert(opNoFixerApply, repairs.ApplyOpID, `{}`, `{"results":[{"owner_user_id":"`+secondaryUserID+`"}]}`)
	insert(opUnknownFixer, repairs.ApplyOpID, `{"fixer_id":"fixer.removed-since"}`, body("fixer.removed-since"))
	insert(opPlainScan, "library.scan", `{}`, `{"scanned":3}`)

	// The change journal on one book. A user-state row is what the Audible
	// read-status fixer writes (repairs.Writer.SetUserState): one user's state
	// and positions before and after, stamped with the fixer's id. The plain
	// and v3 rows are ordinary history every library.view caller reads.
	state := func(label string) string {
		return `{"state":{"user_id":"` + secondaryUserID + `","book_id":"` + journalBookID + `","status":"` + label + `"}}`
	}
	journal := func(id, opID, changeType, field, oldV, newV, source string) {
		require.NoError(t, store.CreateOperationChange(&database.OperationChange{
			ID: id, OperationID: opID, BookID: journalBookID, ChangeType: changeType, FieldName: field,
			OldValue: oldV, NewValue: newV, Source: source, CreatedAt: now,
		}))
	}
	journal("chg-user-state", opDefaultApply, undo.ChangeTypeUserBookStateSet, undo.UserStateField(secondaryUserID),
		state("unread"), state("finished"), defaultFixer)
	journal("chg-plain-title", opPlainScan, "metadata_update", "title", "Invented Old Title", "Invented New Title", "")
	journal("chg-v3-narrator", opV3Plain, "metadata_update", "narrator", "", "Invented Narrator", "library.scan")

	// A completed repairs apply that was discarded (registry.Discard runs
	// DeleteOperationV2): the op row goes, its journal stays.
	insert(opDiscarded, repairs.ApplyOpID, `{"fixer_id":"`+defaultFixer+`"}`, body(defaultFixer))
	journal("chg-discarded-user-state", opDiscarded, undo.ChangeTypeUserBookStateSet, undo.UserStateField(secondaryUserID),
		state("in_progress"), state("finished"), defaultFixer)
	_, deleted, err := store.DeleteOperationV2(opDiscarded, []string{"completed"})
	require.NoError(t, err)
	require.True(t, deleted)

	// Rows journaled before Source stamping (2026-10-03), as the fragment
	// consolidation apply of 2026-10-01 left thousands of: user_state_follow
	// with no Source under the default fixer's apply, a follow under the
	// removed fixer's op naming it, a playlist carry, a file move the default
	// fixer stamped, and an unstamped metadata row of the scoped fixer's op,
	// which only its op row scopes.
	follow := func(listener string) string {
		return `{"sync_redirected":false,"progress":[{"user_id":"` + listener + `","absorbed_state":{"user_id":"` + listener + `","status":"finished"}}]}`
	}
	journal("chg-follow-unstamped", opDefaultApply, undo.ChangeTypeUserStateFollow, undo.SurvivorField("book-invented-survivor"),
		"", follow(listenerUnstampedFollow), "")
	journal("chg-follow-removed-fixer", opUnknownFixer, undo.ChangeTypeUserStateFollow, undo.SurvivorField("book-invented-survivor"),
		"", follow(listenerRemovedFixer), "fixer.removed-since")
	journal("chg-playlist-carry", opDefaultApply, undo.ChangeTypeUserContentFollow,
		undo.UserContentField(undo.UserContentPlaylist, playlistOfAnotherUser),
		`{"index":2,"anchored":true,"prev":"book-invented-prev","next":"book-invented-next"}`, "book-invented-survivor", defaultFixer)
	journal("chg-fixer-file-move", opDefaultApply, undo.ChangeTypeBookFileMove, "book_file:file-invented-1",
		"/invented/library/before.m4b", fixerFileMove, defaultFixer)
	journal("chg-scoped-unstamped-title", opScopedPlan, "metadata_update", "title", "", scopedUnstampedTitle, "")

	// A discarded apply from before Source stamping: its only row is an
	// unstamped follow and nothing names its fixer any more.
	insert(opDiscardedOld, repairs.ApplyOpID, `{"fixer_id":"`+defaultFixer+`"}`, body(defaultFixer))
	journal("chg-follow-prestamp-discarded", opDiscardedOld, undo.ChangeTypeUserStateFollow, undo.SurvivorField("book-invented-survivor"),
		"", follow(listenerDiscardedOld), "")
	_, deleted, err = store.DeleteOperationV2(opDiscardedOld, []string{"completed"})
	require.NoError(t, err)
	require.True(t, deleted)

	// A discarded apply of a fixer this build no longer registers: its only
	// row is a follow stamped with that fixer's id, and no op row is left to
	// refuse on first.
	insert(opDiscardedGone, repairs.ApplyOpID, `{"fixer_id":"fixer.removed-since"}`, body("fixer.removed-since"))
	journal("chg-follow-removed-discarded", opDiscardedGone, undo.ChangeTypeUserStateFollow, undo.SurvivorField("book-invented-survivor"),
		"", follow(listenerDiscardedGone), "fixer.removed-since")
	_, deleted, err = store.DeleteOperationV2(opDiscardedGone, []string{"completed"})
	require.NoError(t, err)
	require.True(t, deleted)
	gone, err := store.GetOperationV2(opDiscarded)
	require.NoError(t, err)
	require.Nil(t, gone, "the discarded op has no row")
	kept, err := store.GetOperationChanges(opDiscarded)
	require.NoError(t, err)
	require.Len(t, kept, 1, "the discard keeps the op's journal")

	return &repairsOpAccessFixture{
		srv: srv, viewer: vs.ID, editor: editor, admin: admin, defaultFixerID: defaultFixer,
		scopedPlan: opScopedPlan, defaultApply: opDefaultApply, noFixerApply: opNoFixerApply,
		unknownFixer: opUnknownFixer, plainScan: opPlainScan, discarded: opDiscarded, bookID: journalBookID,
		allRepairsOps: []string{opScopedPlan, opDefaultApply, opNoFixerApply, opUnknownFixer},
	}
}

func getAs(srv *Server, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// The read routes that take an op id, for one op. Each is a 200 for a caller
// who may read the op and a 403 for one who may not.
func opReadPaths(id string) map[string]string {
	return map[string]string{
		"result":         "/api/v1/operations/" + id + "/result",
		"changes":        "/api/v1/operations/" + id + "/changes",
		"undo preflight": "/api/v1/operations/" + id + "/undo/preflight",
		"v2 op":          "/api/v1/operations/v2/" + id,
		"log download":   "/api/v1/operations/v2/" + id + "/logs/download",
	}
}

// TestRepairsOpData_ReadRoutesNeedTheFixersPermissions: by id, a viewer is
// refused every repairs op on every read route and nothing of the op leaks in
// the refusal; an editor reads the default-permission fixer's op but not the
// settings.manage fixer's; an admin reads every op whose fixer is known and
// stated; an op that states no fixer is refused to everyone (fail closed); and
// a plain op is a 200 for a viewer on every route.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_ReadRoutesNeedTheFixersPermissions(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, tc := range []struct {
		who, token string
		op         string
		allowed    bool
	}{
		{"viewer", f.viewer, f.scopedPlan, false},
		{"viewer", f.viewer, f.defaultApply, false},
		{"viewer", f.viewer, f.noFixerApply, false},
		{"viewer", f.viewer, f.unknownFixer, false},
		{"viewer", f.viewer, f.plainScan, true},
		{"editor", f.editor, f.defaultApply, true},
		{"editor", f.editor, f.scopedPlan, false},
		{"editor", f.editor, f.unknownFixer, false},
		{"admin", f.admin, f.scopedPlan, true},
		{"admin", f.admin, f.defaultApply, true},
		{"admin", f.admin, f.unknownFixer, true},
		{"admin", f.admin, f.noFixerApply, false},
		{"admin", f.admin, f.plainScan, true},
	} {
		for route, path := range opReadPaths(tc.op) {
			w := getAs(f.srv, tc.token, path)
			if tc.who == "editor" && tc.op == f.defaultApply && route == "undo preflight" {
				// The editor may run this fixer, but its journal holds user-state
				// rows, and undoing those needs users.manage (MayUndoChange).
				require.Equal(t, http.StatusForbidden, w.Code, "%s %s %s: %s", tc.who, tc.op, route, w.Body.String())
				require.NotContains(t, w.Body.String(), secondaryUserID)
				continue
			}
			if tc.allowed {
				requireReadShape(t, route, tc.op, w, "%s %s %s", tc.who, tc.op, route)
				continue
			}
			require.Equal(t, http.StatusForbidden, w.Code, "%s %s %s: %s", tc.who, tc.op, route, w.Body.String())
			require.NotContains(t, w.Body.String(), secondaryUserID, "%s %s %s", tc.who, tc.op, route)
		}
		if tc.allowed && tc.op != f.plainScan {
			w := getAs(f.srv, tc.token, "/api/v1/operations/"+tc.op+"/result")
			require.Equal(t, http.StatusOK, w.Code, "%s %s result: %s", tc.who, tc.op, w.Body.String())
			require.Contains(t, w.Body.String(), secondaryUserID, "the permitted caller reads the result")
		}
	}
	w := getAs(f.srv, f.viewer, "/api/v1/operations/"+f.plainScan+"/result")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"scanned":3`)
}

// requireReadShape asserts a permitted read of op on route answered 200 with
// that route's body, not merely "not 403": a 500 or an empty body on a route a
// caller may read is a regression too.
func requireReadShape(t *testing.T, route, op string, w *httptest.ResponseRecorder, msgAndArgs ...any) {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, append(msgAndArgs, w.Body.String())...)
	switch route {
	case "result":
		require.Contains(t, w.Body.String(), `"result_data"`, msgAndArgs...)
	case "changes":
		require.Contains(t, w.Body.String(), `"changes":[`, msgAndArgs...)
	case "undo preflight":
		require.Contains(t, w.Body.String(), `"total_changes":`, msgAndArgs...)
	case "v2 op":
		require.Contains(t, w.Body.String(), `"`+op+`"`, msgAndArgs...)
	case "log download":
		require.Equal(t, "application/gzip", w.Header().Get("Content-Type"), msgAndArgs...)
		zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
		require.NoError(t, err, msgAndArgs...)
		text, err := io.ReadAll(zr)
		require.NoError(t, err, msgAndArgs...)
		require.Contains(t, string(text), "row ran for", msgAndArgs...)
	default:
		t.Fatalf("no body shape for route %q", route)
	}
}

// TestRepairsOpData_BookJournalLeavesOutWhatTheCallerMayNotRead: a book's
// change journal (GET /audiobooks/:id/changes) and changelog
// (/audiobooks/:id/changelog) keep the book's ordinary history, including a v3
// op's rows stamped with its def id, for everyone. A Repairs fixer's library
// rows (a stamped file move, an unstamped row whose op row names a fixer) need
// the fixer's permissions. Rows that carry other users' own data -- the
// read-status fixer's user state, user_state_follow stamped, unstamped or
// naming a removed fixer, a playlist carry -- need users.manage as well, so
// the editor, who may run the default fixer, reads its file move and not its
// user data; only the admin reads those, whether or not the row's op still has
// its row.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_BookJournalLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	userData := []string{secondaryUserID, listenerUnstampedFollow, listenerRemovedFixer, playlistOfAnotherUser}
	for _, route := range []string{"changes", "changelog"} {
		path := "/api/v1/audiobooks/" + f.bookID + "/" + route
		for _, tc := range []struct {
			who, token     string
			seesFixerRows  bool // the default fixer's file move
			seesScopedRows bool // the settings.manage fixer's unstamped row
			seesUserData   bool
		}{
			{"viewer", f.viewer, false, false, false},
			{"editor", f.editor, true, false, false},
			{"admin", f.admin, true, true, true},
		} {
			w := getAs(f.srv, tc.token, path)
			require.Equal(t, http.StatusOK, w.Code, "%s %s: %s", tc.who, route, w.Body.String())
			body := w.Body.String()
			key := `"changes":[`
			if route == "changelog" {
				key = `"entries":[`
			}
			require.Contains(t, body, key, "%s %s", tc.who, route)
			require.Contains(t, body, "Invented New Title", "%s %s: the book's other history stays", tc.who, route)
			require.Contains(t, body, "Invented Narrator", "%s %s: a v3 op's history stays", tc.who, route)
			requireSeen := func(want bool, marker, what string) {
				if want {
					require.Contains(t, body, marker, "%s %s: %s", tc.who, route, what)
				} else {
					require.NotContains(t, body, marker, "%s %s: %s", tc.who, route, what)
				}
			}
			requireSeen(tc.seesFixerRows, fixerFileMove, "the default fixer's file move")
			requireSeen(tc.seesScopedRows, scopedUnstampedTitle, "the scoped fixer's unstamped row (its op row scopes it)")
			for _, marker := range userData {
				requireSeen(tc.seesUserData, marker, "a user-data row")
			}
			requireSeen(tc.seesUserData, f.discarded, "the discarded op's user-state row")
			requireSeen(tc.seesFixerRows, f.defaultApply, "the default fixer's op id")
		}
	}
}

// TestRepairsOpData_DiscardedOpJournalStaysScoped: once a repairs apply's op
// row is discarded, its journal is still kept from a caller without the
// fixer's permission: GET /operations/:id/changes answers 200 without the
// rows, and /undo/preflight, a report built from every row, is a 403. Its
// user-state row is admin data: the editor (the default fixer's permission)
// gets the changes without it, and still gets the undo report, which names
// rows and never their values. The admin reads both.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_DiscardedOpJournalStaysScoped(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	changes := "/api/v1/operations/" + f.discarded + "/changes"
	preflight := "/api/v1/operations/" + f.discarded + "/undo/preflight"

	w := getAs(f.srv, f.viewer, changes)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"changes":[]`)
	require.NotContains(t, w.Body.String(), secondaryUserID)

	w = getAs(f.srv, f.viewer, preflight)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), secondaryUserID)

	w = getAs(f.srv, f.editor, changes)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"changes":[]`, "the editor may run the fixer, not read other users' state")
	require.NotContains(t, w.Body.String(), secondaryUserID)

	w = getAs(f.srv, f.editor, preflight)
	require.Equal(t, http.StatusForbidden, w.Code, "the editor may run the fixer but not undo a user's state: %s", w.Body.String())
	require.NotContains(t, w.Body.String(), secondaryUserID)

	w = getAs(f.srv, f.admin, changes)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "chg-discarded-user-state")
	require.Contains(t, w.Body.String(), secondaryUserID)

	w = getAs(f.srv, f.admin, preflight)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"total_changes":1`)
}

// TestRepairsOpData_UserDataRowsNeedUsersManage: on the two operation journal
// routes, a row carrying other users' own data is withheld from every caller
// without users.manage, stamped or not.
//
//   - The default fixer's apply (op row present): a viewer is refused the op;
//     the editor reads its file move but none of its user state, unstamped
//     follow or playlist carry, and is refused the undo report (it would
//     offer an undo of those rows); the admin reads every row.
//   - The removed fixer's op: its follow row names a fixer this build does not
//     register; only the admin reads the op and the row.
//   - A discarded apply from before Source stamping: its unstamped follow row
//     has no op row to name its fixer, so it needs every permission: the
//     viewer and the editor get its changes empty and its undo report
//     refused; the admin gets both.
//   - A discarded apply of a removed fixer: its follow row names a fixer this
//     build does not register, so the undo report needs every permission; the
//     same answers.
//
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_UserDataRowsNeedUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	changes := func(op string) string { return "/api/v1/operations/" + op + "/changes" }
	preflight := func(op string) string { return "/api/v1/operations/" + op + "/undo/preflight" }
	defaultUserData := []string{secondaryUserID, listenerUnstampedFollow, playlistOfAnotherUser}

	// The default fixer's apply.
	for _, path := range []string{changes(f.defaultApply), preflight(f.defaultApply)} {
		w := getAs(f.srv, f.viewer, path)
		require.Equal(t, http.StatusForbidden, w.Code, "viewer %s: %s", path, w.Body.String())
	}
	w := getAs(f.srv, f.editor, changes(f.defaultApply))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), fixerFileMove, "the editor reads the fixer's library rows")
	for _, marker := range defaultUserData {
		require.NotContains(t, w.Body.String(), marker, "the editor reads no user data")
	}
	w = getAs(f.srv, f.editor, preflight(f.defaultApply))
	require.Equal(t, http.StatusForbidden, w.Code, "the journal holds user rows the editor may not undo: %s", w.Body.String())
	for _, marker := range defaultUserData {
		require.NotContains(t, w.Body.String(), marker, "the refusal carries no user data")
	}
	w = getAs(f.srv, f.admin, changes(f.defaultApply))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	for _, marker := range append([]string{fixerFileMove}, defaultUserData...) {
		require.Contains(t, w.Body.String(), marker, "the admin reads every row")
	}

	// The removed fixer's op.
	for who, token := range map[string]string{"viewer": f.viewer, "editor": f.editor} {
		w := getAs(f.srv, token, changes(f.unknownFixer))
		require.Equal(t, http.StatusForbidden, w.Code, "%s: %s", who, w.Body.String())
		require.NotContains(t, w.Body.String(), listenerRemovedFixer, who)
	}
	w = getAs(f.srv, f.admin, changes(f.unknownFixer))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), listenerRemovedFixer)
	w = getAs(f.srv, f.admin, preflight(f.unknownFixer))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// The discarded pre-stamp apply and the discarded removed-fixer apply.
	for op, listener := range map[string]string{opDiscardedOld: listenerDiscardedOld, opDiscardedGone: listenerDiscardedGone} {
		for who, token := range map[string]string{"viewer": f.viewer, "editor": f.editor} {
			w := getAs(f.srv, token, changes(op))
			require.Equal(t, http.StatusOK, w.Code, "%s %s: %s", who, op, w.Body.String())
			require.Contains(t, w.Body.String(), `"changes":[]`, "%s %s", who, op)
			require.NotContains(t, w.Body.String(), listener, "%s %s", who, op)
			w = getAs(f.srv, token, preflight(op))
			require.Equal(t, http.StatusForbidden, w.Code, "%s %s: %s", who, op, w.Body.String())
			require.NotContains(t, w.Body.String(), listener, "%s %s", who, op)
		}
		w = getAs(f.srv, f.admin, changes(op))
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", op, w.Body.String())
		require.Contains(t, w.Body.String(), listener, op)
		w = getAs(f.srv, f.admin, preflight(op))
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", op, w.Body.String())
		require.Contains(t, w.Body.String(), `"total_changes":1`, op)
	}
}

// TestRepairsOpData_TimelineLeavesOutWhatTheCallerMayNotRead: the timeline
// lists the repairs ops the caller may read and no others, and counts only
// those; a viewer sees the plain op alone, an admin sees every op whose fixer
// is stated.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_TimelineLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	ids := func(token string) []string {
		w := getAs(f.srv, token, "/api/v1/operations/timeline?since=24h&limit=100")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var out []string
		for _, id := range []string{f.scopedPlan, f.defaultApply, f.noFixerApply, f.unknownFixer, f.plainScan} {
			if strings.Contains(w.Body.String(), `"`+id+`"`) {
				out = append(out, id)
			}
		}
		return out
	}
	require.ElementsMatch(t, []string{f.plainScan}, ids(f.viewer))
	require.ElementsMatch(t, []string{f.defaultApply, f.plainScan}, ids(f.editor))
	require.ElementsMatch(t, []string{f.scopedPlan, f.defaultApply, f.unknownFixer, f.plainScan}, ids(f.admin))

	w := getAs(f.srv, f.viewer, "/api/v1/operations/timeline?since=24h&limit=100")
	require.Contains(t, w.Body.String(), `"matched":1`, "the viewer's count covers only what the viewer may see")
}

// syncRecorder is a ResponseWriter a test can read while the handler writes.
type syncRecorder struct {
	mu   sync.Mutex
	hdr  http.Header
	buf  bytes.Buffer
	code int
}

func (r *syncRecorder) Header() http.Header { return r.hdr }
func (r *syncRecorder) WriteHeader(c int)   { r.mu.Lock(); r.code = c; r.mu.Unlock() }
func (r *syncRecorder) Flush()              {}
func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}
func (r *syncRecorder) body() string { r.mu.Lock(); defer r.mu.Unlock(); return r.buf.String() }

// eventsSeenBy opens /operations/events as token, publishes op.log, op.created
// and op.terminal for every seeded op, and returns the stream once the
// plain op's last event has arrived (it is published last).
func eventsSeenBy(t *testing.T, f *repairsOpAccessFixture, token string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := &syncRecorder{hdr: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); f.srv.router.ServeHTTP(rec, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), ": heartbeat") },
		5*time.Second, 5*time.Millisecond, "the stream never opened: %s", rec.body())

	ops := append(append([]string{}, f.allRepairsOps...), f.plainScan)
	for _, id := range ops {
		def, msg := repairs.ApplyOpID, "row ran for "+secondaryUserID
		if id == f.plainScan {
			def, msg = "library.scan", "scanning"
		}
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.created", map[string]any{"op_id": id, "def_id": def}))
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.log", map[string]any{"op_id": id, "message": msg}))
		require.NoError(t, f.srv.opHub.Publish(ctx, "op.updated", map[string]any{"op_id": id, "progress_current": 1}))
	}
	// A marker event naming no op passes for everyone: it proves the stream
	// had drained everything published before it.
	require.NoError(t, f.srv.opHub.Publish(ctx, "op.marker", map[string]any{"note": "end"}))
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), "op.marker") },
		5*time.Second, 5*time.Millisecond, "the marker never arrived: %s", rec.body())
	cancel()
	<-done
	return rec.body()
}

// TestRepairsOpData_EventStreamLeavesOutWhatTheCallerMayNotRead: a viewer's
// stream carries the plain op's events and none of any repairs op's (not the
// op id, not the log line naming a user); an admin's carries every op whose
// fixer is stated.
// Not parallel: it swaps the process-wide config.
func TestRepairsOpData_EventStreamLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	viewer := eventsSeenBy(t, f, f.viewer)
	require.Contains(t, viewer, f.plainScan)
	for _, id := range f.allRepairsOps {
		require.NotContains(t, viewer, id, "the viewer's stream names a repairs op")
	}
	require.NotContains(t, viewer, secondaryUserID)

	admin := eventsSeenBy(t, f, f.admin)
	for _, id := range []string{f.scopedPlan, f.defaultApply, f.unknownFixer, f.plainScan} {
		require.Contains(t, admin, id)
	}
	require.NotContains(t, admin, f.noFixerApply, "an op with no fixer is withheld from everyone")
}

// TestWireHandlers_RepairsOpAccessIsInstalled: the generic operations
// handlers and the audiobooks handler were given the check. Without it they refuse every repairs op (fail closed), so
// an admin reading a stated-fixer op is the oracle that the wiring is there.
// Not parallel: it swaps the process-wide config.
func TestWireHandlers_RepairsOpAccessIsInstalled(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, path := range []string{
		"/api/v1/operations/" + f.scopedPlan + "/result", // operations.Handler
		"/api/v1/operations/v2/" + f.scopedPlan,          // OperationsV2Handler
		"/api/v1/audiobooks/" + f.bookID + "/changes",    // audiobookshandler.Handler
	} {
		w := getAs(f.srv, f.admin, path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
		if strings.Contains(path, "/audiobooks/") {
			require.Contains(t, w.Body.String(), secondaryUserID, "%s: the admin reads the fixer's journal row", path)
		}
	}
}
