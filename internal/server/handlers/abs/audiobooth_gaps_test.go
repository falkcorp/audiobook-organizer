// file: internal/server/handlers/abs/audiobooth_gaps_test.go
// version: 1.0.0
// guid: 7c9e5b89-8301-444c-bb5d-386530b87351
// last-edited: 2026-09-19

package abs_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Regression tests for the item-6 AudioBooth decode matrix (2026-09-19): app
// actions that had NO ABS route and therefore 301'd into /api/v1 (a POST/PATCH/
// DELETE the client then re-issued as a GET) or 404'd. Each test drives the real
// handler through the harness router, so a route that is not registered fails
// here with a 404 exactly as it did on production.

func obj(t *testing.T, body any) map[string]any {
	t.Helper()
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want a JSON object", body)
	}
	return m
}

func itemIDs(t *testing.T, m map[string]any, key string) []string {
	t.Helper()
	raw, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%q is %T, want an array: %v", key, m[key], m)
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		row := obj(t, r)
		if id, ok := row["libraryItemId"].(string); ok {
			out = append(out, id)
		} else if id, ok := row["id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// ── F2: playlist mutations ──────────────────────────────────────────────────

func TestPlaylists_MutationsPersistThroughTheStore(t *testing.T) {
	store := &absplFakeStore{}
	h, seed, tok := absplHarness(t, store)
	single := absplSyncIDFor(t, seed, seed.singleID)
	multi := absplSyncIDFor(t, seed, seed.multiID)

	// create
	code, body := h.doAny(t, request{method: http.MethodPost, path: "/api/playlists", headers: bearer(tok),
		body: map[string]any{"libraryId": h.libraryID(), "name": "Road trip",
			"items": []map[string]any{{"libraryItemId": single}}}})
	if code != http.StatusOK {
		t.Fatalf("POST /api/playlists = %d %v", code, body)
	}
	created := obj(t, body)
	plID, _ := created["id"].(string)
	if plID == "" || created["name"] != "Road trip" {
		t.Fatalf("create answered %v", created)
	}
	if got := itemIDs(t, created, "items"); len(got) != 1 || got[0] != single {
		t.Fatalf("created items = %v, want [%s]", got, single)
	}
	if len(store.lists) != 1 {
		t.Fatalf("store holds %d playlists, want 1", len(store.lists))
	}
	stored := store.lists[0]
	if stored.CreatedByUserID != "u1" || !stored.Dirty || stored.Type != database.UserPlaylistTypeStatic {
		t.Fatalf("stored playlist = %+v; want owner u1, Dirty, static", stored)
	}
	// Sync ids MUST be translated: storing the client's id would make a playlist
	// whose members match no book.
	if len(stored.BookIDs) != 1 || stored.BookIDs[0] != seed.singleID {
		t.Fatalf("stored BookIDs = %v, want the book ULID %s", stored.BookIDs, seed.singleID)
	}

	// batch add: a duplicate is skipped, the new book appended
	code, body = h.doAny(t, request{method: http.MethodPost, path: "/api/playlists/" + plID + "/batch/add", headers: bearer(tok),
		body: map[string]any{"items": []map[string]any{{"libraryItemId": single}, {"libraryItemId": multi}}}})
	if code != http.StatusOK {
		t.Fatalf("batch/add = %d %v", code, body)
	}
	if got := itemIDs(t, obj(t, body), "items"); len(got) != 2 || got[0] != single || got[1] != multi {
		t.Fatalf("after batch/add items = %v, want [%s %s]", got, single, multi)
	}

	// patch: rename and reorder via items
	code, body = h.doAny(t, request{method: http.MethodPatch, path: "/api/playlists/" + plID, headers: bearer(tok),
		body: map[string]any{"name": "Commute", "items": []map[string]any{{"libraryItemId": multi}, {"libraryItemId": single}}}})
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d %v", code, body)
	}
	patched := obj(t, body)
	if patched["name"] != "Commute" {
		t.Fatalf("rename not applied: %v", patched["name"])
	}
	if got := itemIDs(t, patched, "items"); len(got) != 2 || got[0] != multi {
		t.Fatalf("reorder not applied: %v", got)
	}
	if store.lists[0].Name != "Commute" || store.lists[0].BookIDs[0] != seed.multiID {
		t.Fatalf("PATCH did not persist: %+v", store.lists[0])
	}

	// batch remove
	code, body = h.doAny(t, request{method: http.MethodPost, path: "/api/playlists/" + plID + "/batch/remove", headers: bearer(tok),
		body: map[string]any{"items": []map[string]any{{"libraryItemId": multi}}}})
	if code != http.StatusOK {
		t.Fatalf("batch/remove = %d %v", code, body)
	}
	if got := itemIDs(t, obj(t, body), "items"); len(got) != 1 || got[0] != single {
		t.Fatalf("after batch/remove items = %v", got)
	}

	// item delete — the playlist is kept even when emptied
	code, body = h.doAny(t, request{method: http.MethodDelete, path: "/api/playlists/" + plID + "/item/" + single, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("DELETE item = %d %v", code, body)
	}
	if got := itemIDs(t, obj(t, body), "items"); len(got) != 0 {
		t.Fatalf("after item delete items = %v, want []", got)
	}
	if len(store.lists) != 1 || len(store.lists[0].BookIDs) != 0 {
		t.Fatalf("item delete did not persist or deleted the playlist: %+v", store.lists)
	}

	// delete
	code, body = h.doAny(t, request{method: http.MethodDelete, path: "/api/playlists/" + plID, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("DELETE = %d %v", code, body)
	}
	if len(store.lists) != 0 {
		t.Fatalf("DELETE did not remove the playlist: %+v", store.lists)
	}
	if code, _ := absplDetail(t, h, tok, plID); code != http.StatusNotFound {
		t.Fatalf("detail after delete = %d, want 404", code)
	}
}

// Playlists belong to a person. Every mutation of another user's playlist must
// answer 404 (not 403: that would confirm the id exists) and write NOTHING.
func TestPlaylists_MutationsOfAnotherUsersPlaylistAreNotFound(t *testing.T) {
	store := &absplFakeStore{lists: []database.UserPlaylist{{
		ID: "01OTHERSPLAYLIST0000000000", Name: "Theirs", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "someone-else",
	}}}
	h, seed, tok := absplHarness(t, store)
	single := absplSyncIDFor(t, seed, seed.singleID)
	id := store.lists[0].ID
	item := []map[string]any{{"libraryItemId": single}}

	for _, r := range []request{
		{method: http.MethodPatch, path: "/api/playlists/" + id, body: map[string]any{"name": "Mine now"}},
		{method: http.MethodDelete, path: "/api/playlists/" + id},
		{method: http.MethodPost, path: "/api/playlists/" + id + "/batch/add", body: map[string]any{"items": item}},
		{method: http.MethodPost, path: "/api/playlists/" + id + "/batch/remove", body: map[string]any{"items": item}},
		{method: http.MethodDelete, path: "/api/playlists/" + id + "/item/" + single},
	} {
		r.headers = bearer(tok)
		if code, body := h.doAny(t, r); code != http.StatusNotFound {
			t.Errorf("%s %s on another user's playlist = %d %v, want 404", r.method, r.path, code, body)
		}
	}
	if len(store.lists) != 1 || store.lists[0].Name != "Theirs" || len(store.lists[0].BookIDs) != 0 || store.lists[0].Dirty {
		t.Fatalf("another user's playlist was modified: %+v", store.lists)
	}
}

func TestPlaylists_SmartPlaylistMembershipIsNotEditable(t *testing.T) {
	store := &absplFakeStore{lists: []database.UserPlaylist{{
		ID: "01SMARTPLAYLIST00000000000", Name: "Smart", Type: database.UserPlaylistTypeSmart,
		Query: "author:x", CreatedByUserID: "u1",
	}}}
	h, seed, tok := absplHarness(t, store)
	single := absplSyncIDFor(t, seed, seed.singleID)
	code, _ := h.doAny(t, request{method: http.MethodPost, path: "/api/playlists/" + store.lists[0].ID + "/batch/add",
		headers: bearer(tok), body: map[string]any{"items": []map[string]any{{"libraryItemId": single}}}})
	if code != http.StatusConflict {
		t.Fatalf("batch/add on a smart playlist = %d, want 409", code)
	}
	if len(store.lists[0].BookIDs) != 0 {
		t.Fatalf("smart playlist membership was written: %v", store.lists[0].BookIDs)
	}
}

func TestPlaylists_CreateDuplicateNameIsConflict(t *testing.T) {
	store := &absplFakeStore{}
	h, _, tok := absplHarness(t, store)
	for i, want := range []int{http.StatusOK, http.StatusConflict} {
		code, body := h.doAny(t, request{method: http.MethodPost, path: "/api/playlists", headers: bearer(tok),
			body: map[string]any{"name": "Same"}})
		if code != want {
			t.Fatalf("create #%d = %d %v, want %d", i+1, code, body, want)
		}
	}
}

// ── F1: collection batch add/remove ─────────────────────────────────────────

func TestCollections_BatchAddAndRemove(t *testing.T) {
	store := &abscolFakeStore{}
	h, seed, tok := abscolHarness(t, store)
	single := absplSyncIDFor(t, seed, seed.singleID)
	multi := absplSyncIDFor(t, seed, seed.multiID)

	code, body := h.doAny(t, request{method: http.MethodPost, path: "/api/collections", headers: bearer(tok),
		body: map[string]any{"libraryId": h.libraryID(), "name": "Shelf"}})
	if code != http.StatusOK {
		t.Fatalf("create = %d %v", code, body)
	}
	colID, _ := obj(t, body)["id"].(string)

	code, body = h.doAny(t, request{method: http.MethodPost, path: "/api/collections/" + colID + "/batch/add",
		headers: bearer(tok), body: map[string]any{"books": []string{single, multi, single}}})
	if code != http.StatusOK {
		t.Fatalf("batch/add = %d %v", code, body)
	}
	added := obj(t, body)
	if n, _ := added["numBooks"].(float64); n != 2 {
		t.Fatalf("after batch/add numBooks = %v, want 2 (duplicates collapse)", added["numBooks"])
	}
	col, _ := store.GetCollection(colID)
	if col == nil || len(col.BookIDs) != 2 || col.BookIDs[0] != seed.singleID || col.BookIDs[1] != seed.multiID {
		t.Fatalf("batch/add did not persist book ULIDs in order: %+v", col)
	}

	code, body = h.doAny(t, request{method: http.MethodPost, path: "/api/collections/" + colID + "/batch/remove",
		headers: bearer(tok), body: map[string]any{"books": []string{single}}})
	if code != http.StatusOK {
		t.Fatalf("batch/remove = %d %v", code, body)
	}
	if got := itemIDs(t, obj(t, body), "books"); len(got) != 1 || got[0] != multi {
		t.Fatalf("after batch/remove books = %v, want [%s]", got, multi)
	}
	col, _ = store.GetCollection(colID)
	if len(col.BookIDs) != 1 || col.BookIDs[0] != seed.multiID {
		t.Fatalf("batch/remove did not persist: %v", col.BookIDs)
	}
}

// Writes are gated on PermCollectionsManage exactly like the single-book routes.
func TestCollections_BatchRequiresPermission(t *testing.T) {
	store := &abscolFakeStore{}
	h, seed, tok := abscolHarness(t, store)
	code, body := h.doAny(t, request{method: http.MethodPost, path: "/api/collections", headers: bearer(tok),
		body: map[string]any{"name": "Shelf"}})
	if code != http.StatusOK {
		t.Fatalf("create = %d %v", code, body)
	}
	colID, _ := obj(t, body)["id"].(string)

	h.seedUser(t, "u2", "viewer", "", "pw-pw-pw-pw")
	viewer := str(t, userObj(t, h.login(t, "viewer", "pw-pw-pw-pw")), "accessToken")
	code, _ = h.doAny(t, request{method: http.MethodPost, path: "/api/collections/" + colID + "/batch/add",
		headers: bearer(viewer), body: map[string]any{"books": []string{absplSyncIDFor(t, seed, seed.singleID)}}})
	if code != http.StatusForbidden {
		t.Fatalf("batch/add without PermCollectionsManage = %d, want 403", code)
	}
	if col, _ := store.GetCollection(colID); len(col.BookIDs) != 0 {
		t.Fatalf("an unauthorised batch/add wrote: %v", col.BookIDs)
	}
}

// ── F3: offline sessions bulk upload ────────────────────────────────────────

func localAll(t *testing.T, w *writeHarness, sessions ...map[string]any) []map[string]any {
	t.Helper()
	code, body, raw := w.req(t, http.MethodPost, "/api/session/local-all", map[string]any{"sessions": sessions})
	if code != http.StatusOK {
		t.Fatalf("POST /api/session/local-all = %d %s", code, raw)
	}
	rows, ok := body["results"].([]any)
	if !ok {
		t.Fatalf("results missing: %s", raw)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, obj(t, r))
	}
	return out
}

func storedPosition(t *testing.T, w *writeHarness) float64 {
	t.Helper()
	pos, err := w.seed.lib.GetUserPosition(w.userID, w.bookID)
	if err != nil {
		t.Fatalf("GetUserPosition: %v", err)
	}
	if pos == nil {
		return 0
	}
	return pos.PositionSeconds
}

func TestSessionLocalAll_AppliesOfflineProgress(t *testing.T) {
	w := newWriteHarness(t)
	rows := localAll(t, w, map[string]any{
		"id": "offline-1", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 321.5, "timeListening": 300,
	})
	if len(rows) != 1 || rows[0]["success"] != true || rows[0]["progressSynced"] != true {
		t.Fatalf("results = %v, want one successful synced row", rows)
	}
	if got := storedPosition(t, w); got != 321.5 {
		t.Fatalf("stored position = %v, want 321.5", got)
	}
	state, _ := w.seed.lib.GetUserBookState(w.userID, w.bookID)
	if state == nil || state.Status != database.UserBookStatusInProgress {
		t.Fatalf("book state = %+v, want in_progress", state)
	}
}

// 🔴 The rule this endpoint exists to honour: an offline session that is BEHIND
// the server must never rewind the listener, even though offline clients stamp
// replayed sessions with updatedAt=now (which a timestamp comparison would let
// win).
func TestSessionLocalAll_NeverRegressesNewerProgress(t *testing.T) {
	w := newWriteHarness(t)
	if err := w.seed.lib.SetUserPosition(w.userID, w.bookID, "abs", 500); err != nil {
		t.Fatal(err)
	}
	rows := localAll(t, w, map[string]any{
		"id": "stale-1", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 100.0,
		"updatedAt": int64(9999999999999), // a far-future stamp must not buy a rewind
	})
	if len(rows) != 1 || rows[0]["success"] != true || rows[0]["progressSynced"] != false {
		t.Fatalf("results = %v, want success without a progress change", rows)
	}
	if got := storedPosition(t, w); got != 500 {
		t.Fatalf("stale offline session rewound the listener: position %v, want 500", got)
	}
}

func TestSessionLocalAll_IdempotentOnSessionID(t *testing.T) {
	w := newWriteHarness(t)
	s := map[string]any{"id": "dup-1", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 200.0}
	rows := localAll(t, w, s, s)
	if len(rows) != 1 {
		t.Fatalf("a session id sent twice in one upload produced %d result rows, want 1: %v", len(rows), rows)
	}
	first := storedPosition(t, w)
	again := localAll(t, w, s)
	if len(again) != 1 || again[0]["success"] != true || again[0]["progressSynced"] != false {
		t.Fatalf("replay results = %v, want success with no change", again)
	}
	if got := storedPosition(t, w); got != first || got != 200 {
		t.Fatalf("replay changed the position: %v -> %v", first, got)
	}
}

func TestSessionLocalAll_RejectsForeignAndUnknownSessionsWithoutFailingTheBatch(t *testing.T) {
	w := newWriteHarness(t)
	rows := localAll(t, w,
		map[string]any{"id": "theirs", "userId": "someone-else", "libraryItemId": w.syncID, "currentTime": 999.0},
		map[string]any{"id": "ghost", "userId": w.userID, "libraryItemId": "00000000-0000-4000-8000-00000000dead", "currentTime": 50.0},
		map[string]any{"id": "mine", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 42.0},
	)
	if len(rows) != 3 {
		t.Fatalf("results = %v, want 3 rows", rows)
	}
	if rows[0]["success"] != false || rows[1]["success"] != false || rows[2]["success"] != true {
		t.Fatalf("results = %v; want foreign+unknown rejected, own accepted", rows)
	}
	if got := storedPosition(t, w); got != 42 {
		t.Fatalf("stored position = %v, want 42 (the foreign 999 must not land)", got)
	}
}

// A malformed body is still a 2xx: a 4xx wedges the client's replay queue.
func TestSessionLocalAll_MalformedBodyIsStill200(t *testing.T) {
	w := newWriteHarness(t)
	code, _, raw := w.req(t, http.MethodPost, "/api/session/local-all", "not an object")
	if code != http.StatusOK || !strings.Contains(raw, "results") {
		t.Fatalf("malformed body = %d %s, want 200 with results", code, raw)
	}
}

// ── F5 / F6 ─────────────────────────────────────────────────────────────────

func TestNarratorImage_IsAPlain404(t *testing.T) {
	h, _, tok := absplHarness(t, &absplFakeStore{})
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec, _ := h.do(t, request{method: m, path: "/api/narrators/SmFuZSBEb2U=/image", headers: bearer(tok)})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s narrator image = %d, want 404", m, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Fatalf("%s narrator image redirected to %q", m, loc)
		}
	}
}

func TestSendEbookToDevice_IsAClearError(t *testing.T) {
	h, _, tok := absplHarness(t, &absplFakeStore{})
	rec, _ := h.do(t, request{method: http.MethodPost, path: "/api/emails/send-ebook-to-device", headers: bearer(tok),
		body: map[string]any{"libraryItemId": "x", "deviceName": "kindle"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("send-ebook-to-device = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Ereader device not found") {
		t.Fatalf("body %q does not say why", rec.Body.String())
	}
}

// Review B2: an ABS batch add racing a native-API rename on the same playlist
// must keep BOTH. The fake commits the rename between the ABS read and write
// (exactly what an unlocked native PUT does); the store's version check turns
// that into a conflict, and the ABS path re-reads and re-applies its add.
func TestPlaylists_ConcurrentNativeRenameAndABSAddBothLand(t *testing.T) {
	store := &absplFakeStore{lists: []database.UserPlaylist{{
		ID: "01RACEPLAYLIST000000000000", Name: "Before", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "u1", Version: 1,
	}}}
	h, seed, tok := absplHarness(t, store)
	single := absplSyncIDFor(t, seed, seed.singleID)
	store.beforeUpdate = func(f *absplFakeStore) {
		f.lists[0].Name = "Renamed natively"
		f.lists[0].Version++
	}
	code, body := h.doAny(t, request{method: http.MethodPost, path: "/api/playlists/01RACEPLAYLIST000000000000/batch/add",
		headers: bearer(tok), body: map[string]any{"items": []map[string]any{{"libraryItemId": single}}}})
	if code != http.StatusOK {
		t.Fatalf("batch/add = %d %v", code, body)
	}
	got := store.lists[0]
	if got.Name != "Renamed natively" || !slices.Equal(got.BookIDs, []string{seed.singleID}) {
		t.Fatalf("stored playlist = name %q books %v; want the native rename AND the ABS add", got.Name, got.BookIDs)
	}
}

// Review W1: renaming onto a taken name (any user's — the index is global) is a
// 409 that does not reveal whose playlist holds it.
func TestPlaylists_RenameOntoTakenNameIsGenericConflict(t *testing.T) {
	store := &absplFakeStore{lists: []database.UserPlaylist{
		{ID: "01THEIRS000000000000000000", Name: "Bedtime", Type: database.UserPlaylistTypeStatic, CreatedByUserID: "other-user"},
		{ID: "01MINE0000000000000000000", Name: "Commute", Type: database.UserPlaylistTypeStatic, CreatedByUserID: "u1"},
	}}
	h, _, tok := absplHarness(t, store)
	rec, _ := h.do(t, request{method: http.MethodPatch, path: "/api/playlists/01MINE0000000000000000000",
		headers: bearer(tok), body: map[string]any{"name": "bedtime"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("rename onto a taken name = %d, want 409", rec.Code)
	}
	if b := rec.Body.String(); strings.Contains(b, "other-user") || strings.Contains(b, "01THEIRS") {
		t.Fatalf("conflict body leaks the other playlist: %s", b)
	}
	if store.lists[1].Name != "Commute" {
		t.Fatalf("rename was written: %q", store.lists[1].Name)
	}
}

// ── Review B1: a hand-pinned status survives automatic progress writes ─────

func pinStatus(t *testing.T, w *writeHarness, status string) {
	t.Helper()
	if err := w.seed.lib.SetUserBookState(&database.UserBookState{UserID: w.userID, BookID: w.bookID,
		Status: status, StatusManual: true}); err != nil {
		t.Fatal(err)
	}
}

func assertPinned(t *testing.T, w *writeHarness, status string) {
	t.Helper()
	st, _ := w.seed.lib.GetUserBookState(w.userID, w.bookID)
	if st == nil || st.Status != status || !st.StatusManual {
		t.Fatalf("book state = %+v; the hand-pinned %q status was overwritten by a derived one", st, status)
	}
}

func TestSessionSync_LeavesAManualStatusAlone(t *testing.T) {
	w := newWriteHarness(t)
	pinStatus(t, w, database.UserBookStatusAbandoned)
	sess := startSession(t, w.harness, w.syncID, w.token)
	id, _ := sess["id"].(string)
	if code, _, raw := w.req(t, http.MethodPost, "/api/session/"+id+"/sync",
		map[string]any{"currentTime": 100.0, "timeListened": 10.0}); code != http.StatusOK {
		t.Fatalf("sync = %d %s", code, raw)
	}
	if got := storedPosition(t, w); got != 100 {
		t.Fatalf("position = %v, want 100 (the position still moves)", got)
	}
	assertPinned(t, w, database.UserBookStatusAbandoned)
}

func TestSessionLocalAll_LeavesAManualStatusAlone(t *testing.T) {
	w := newWriteHarness(t)
	pinStatus(t, w, database.UserBookStatusAbandoned)
	localAll(t, w, map[string]any{"id": "m1", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 80.0})
	if got := storedPosition(t, w); got != 80 {
		t.Fatalf("position = %v, want 80", got)
	}
	assertPinned(t, w, database.UserBookStatusAbandoned)
}

// ── Review W3: offline replay rules ─────────────────────────────────────────

// (a) A session that BEGAN after the server's newest position may rewind it
// (the user re-listened offline); one that began earlier stays forward-only.
func TestSessionLocalAll_RewindOnlyWhenSessionStartedAfterStoredProgress(t *testing.T) {
	w := newWriteHarness(t)
	if err := w.seed.lib.SetUserPosition(w.userID, w.bookID, "abs", 500); err != nil {
		t.Fatal(err)
	}
	// The stored position was written two hours ago.
	w.seed.lib.positions[w.userID+"|"+w.bookID].UpdatedAt = time.Now().Add(-2 * time.Hour)
	older := time.Now().Add(-3 * time.Hour).UnixMilli()
	localAll(t, w, map[string]any{"id": "old", "userId": w.userID, "libraryItemId": w.syncID,
		"currentTime": 100.0, "startedAt": older})
	if got := storedPosition(t, w); got != 500 {
		t.Fatalf("a session that started BEFORE the stored progress rewound it to %v", got)
	}
	newer := time.Now().Add(-10 * time.Minute).UnixMilli()
	rows := localAll(t, w, map[string]any{"id": "new", "userId": w.userID, "libraryItemId": w.syncID,
		"currentTime": 100.0, "startedAt": newer})
	if rows[0]["progressSynced"] != true {
		t.Fatalf("results = %v", rows)
	}
	if got := storedPosition(t, w); got != 100 {
		t.Fatalf("a session that started AFTER the stored progress did not apply: position %v, want 100", got)
	}
}

// (b) Reset progress leaves a tombstone; a replayed session from before the
// reset must not resurrect the discarded position.
func TestSessionLocalAll_RefusesSessionsFromBeforeAProgressReset(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 300.0})
	before := time.Now().Add(-10 * time.Minute).UnixMilli()
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
		t.Fatalf("reset = %d %s", code, raw)
	}
	rows := localAll(t, w, map[string]any{"id": "pre-reset", "userId": w.userID, "libraryItemId": w.syncID,
		"currentTime": 300.0, "startedAt": before, "updatedAt": before})
	if rows[0]["success"] != false {
		t.Fatalf("a pre-reset session was accepted: %v", rows)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("the reset position was resurrected: %v", got)
	}
	// Age the tombstone so a session can provably start after reset+tolerance.
	w.seed.lib.mu.Lock()
	old := time.Now().Add(-10 * time.Minute)
	w.seed.lib.states[w.userID+"|"+w.bookID].ProgressResetAt = &old
	w.seed.lib.mu.Unlock()
	after := time.Now().Add(-time.Minute).UnixMilli()
	localAll(t, w, map[string]any{"id": "post-reset", "userId": w.userID, "libraryItemId": w.syncID,
		"currentTime": 30.0, "startedAt": after, "updatedAt": after})
	if got := storedPosition(t, w); got != 30 {
		t.Fatalf("a post-reset session did not apply: %v", got)
	}
}

// (c) A position far past the end is rejected and never marks the book done.
func TestSessionLocalAll_RejectsAPositionBeyondTheDuration(t *testing.T) {
	w := newWriteHarness(t)
	code, item, raw := w.req(t, http.MethodGet, "/api/items/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("item = %d %s", code, raw)
	}
	dur, _ := obj(t, item["media"])["duration"].(float64)
	if dur <= 0 {
		t.Fatalf("fixture book has no duration: %s", raw)
	}
	rows := localAll(t, w, map[string]any{"id": "absurd", "userId": w.userID, "libraryItemId": w.syncID,
		"currentTime": dur * 50})
	if rows[0]["success"] != false {
		t.Fatalf("an out-of-range position was accepted: %v", rows)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("position = %v after an out-of-range session, want untouched", got)
	}
	if st, _ := w.seed.lib.GetUserBookState(w.userID, w.bookID); st != nil && st.Status == database.UserBookStatusFinished {
		t.Fatalf("an out-of-range position auto-finished the book")
	}
}

// ── Review W4: /api/session/local applies its session ──────────────────────

func TestSessionLocal_AppliesTheSession(t *testing.T) {
	w := newWriteHarness(t)
	code, _, raw := w.req(t, http.MethodPost, "/api/session/local", map[string]any{
		"id": "single-1", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 77.0})
	if code != http.StatusOK || raw != "OK" {
		t.Fatalf("session/local = %d %q, want 200 OK", code, raw)
	}
	if got := storedPosition(t, w); got != 77 {
		t.Fatalf("position = %v, want 77: /api/session/local still discards its session", got)
	}
}

// Re-review MEDIUM #3: fail-safe offline replay around resets and clock skew.

// While a reset tombstone exists, a session's POSITION must provably
// post-date the reset (its updatedAt). Inside the tolerance window, with no
// updatedAt, or with a clock too far ahead to trust, it is refused — the
// stored position is 0 after a reset, so forward-only would bring the
// discarded position back. With no tombstone, the fallback is forward-only.
func TestSessionLocalAll_ResetRefusesSessionsNotProvablyAfterIt(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
		t.Fatalf("reset = %d %s", code, raw)
	}
	for name, sess := range map[string]map[string]any{
		"inside tolerance": {"updatedAt": time.Now().Add(-90 * time.Second).UnixMilli()},
		"no updatedAt":     {"startedAt": time.Now().Add(time.Hour).UnixMilli()},
		"clock far ahead":  {"updatedAt": time.Now().Add(time.Hour).UnixMilli()},
	} {
		sess["id"], sess["userId"], sess["libraryItemId"], sess["currentTime"] = "s-"+name, w.userID, w.syncID, 3600.0
		if rows := localAll(t, w, sess); rows[0]["success"] != false {
			t.Errorf("%s: accepted after a reset: %v", name, rows)
		}
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("the reset position was resurrected: %v", got)
	}

	fresh := newWriteHarness(t) // no reset, no tombstone
	localAll(t, fresh, map[string]any{"id": "nostart", "userId": fresh.userID, "libraryItemId": fresh.syncID, "currentTime": 90.0})
	if got := storedPosition(t, fresh); got != 90 {
		t.Fatalf("without a tombstone a session without timestamps must apply forward-only; position %v", got)
	}
}

// Round-5 MEDIUM: AudioBooth REUSES its local session across a reset, so real
// listening after a 10:00 reset arrives with startedAt 09:00. The position's
// updatedAt is after the reset, so it must be accepted on both endpoints.
func TestSessionLocal_ReusedSessionListeningAfterResetIsAccepted(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
		t.Fatalf("reset = %d %s", code, raw)
	}
	// Age the tombstone to "10:00" so "10:30" is after reset + tolerance.
	w.seed.lib.mu.Lock()
	resetAt := time.Now().Add(-30 * time.Minute)
	w.seed.lib.states[w.userID+"|"+w.bookID].ProgressResetAt = &resetAt
	w.seed.lib.mu.Unlock()

	code, _, raw := w.req(t, http.MethodPost, "/api/session/local", map[string]any{
		"id": "reused", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 1800.0,
		"startedAt": resetAt.Add(-time.Hour).UnixMilli(), "updatedAt": time.Now().UnixMilli()})
	if code != http.StatusOK {
		t.Fatalf("session/local = %d %s, want 200", code, raw)
	}
	if got := storedPosition(t, w); got != 1800 {
		t.Fatalf("real post-reset listening on a reused session was dropped: position %v, want 1800", got)
	}
}

// Round-6: a session discarded because it would undo a reset can NEVER
// become valid. AudioBooth re-sends a 4xx'd session on every activation
// forever, so /api/session/local answers 200 and applies nothing (logged).
// Malformed/impossible data keeps its 409.
func TestSessionLocal_DiscardedResetSessionIs200AndAppliesNothing(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 300.0})
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
		t.Fatalf("reset = %d %s", code, raw)
	}
	code, _, raw := w.req(t, http.MethodPost, "/api/session/local", map[string]any{
		"id": "pre", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 300.0,
		"startedAt": time.Now().Add(-time.Hour).UnixMilli(), "updatedAt": time.Now().Add(-time.Hour).UnixMilli()})
	if code != http.StatusOK {
		t.Fatalf("discarded session answered %d %s, want 200 (a 4xx is re-sent forever)", code, raw)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("a discarded session was applied: position %v", got)
	}
	code, _, raw = w.req(t, http.MethodPost, "/api/session/local", map[string]any{
		"id": "foreign", "userId": "someone-else", "libraryItemId": w.syncID, "currentTime": 10.0})
	if code != http.StatusConflict {
		t.Fatalf("an impossible session answered %d %s, want 409", code, raw)
	}
}

// Round-6 MEDIUM: clients that RE-STAMP updatedAt=now on replay pass the
// updatedAt test, but the playhead restarts at 0 on reset and cannot move
// faster than elapsed × max rate. A backlog position beyond that is discarded;
// real listening at 1x and 3x since the reset is accepted.
func TestSessionLocalAll_ReStampedBacklogCannotUndoAReset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		ct      float64
		accept  bool
	}{
		{"re-stamped backlog", 10 * time.Minute, 3600, false},
		{"5 min at 1x", 5 * time.Minute, 300, true},
		{"5 min at 3x", 5 * time.Minute, 900, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWriteHarness(t)
			w.patch(t, map[string]any{"currentTime": 3600.0})
			if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
				t.Fatalf("reset = %d %s", code, raw)
			}
			w.seed.lib.mu.Lock()
			resetAt := time.Now().Add(-tc.elapsed)
			w.seed.lib.states[w.userID+"|"+w.bookID].ProgressResetAt = &resetAt
			w.seed.lib.mu.Unlock()
			localAll(t, w, map[string]any{"id": "s", "userId": w.userID, "libraryItemId": w.syncID,
				"currentTime": tc.ct, "startedAt": time.Now().Add(-2 * time.Hour).UnixMilli(), "updatedAt": time.Now().UnixMilli()})
			got := storedPosition(t, w)
			if tc.accept && got != tc.ct {
				t.Fatalf("real post-reset listening refused: position %v, want %v", got, tc.ct)
			}
			if !tc.accept && got != 0 {
				t.Fatalf("a re-stamped backlog undid the reset: position %v", got)
			}
		})
	}
}

// A device clock running AHEAD (within tolerance) must not let an older listen
// rewind a position written after it; an implausibly-future startedAt is not
// trusted at all.
func TestSessionLocalAll_ClockAheadNeverRewindsANewerPosition(t *testing.T) {
	w := newWriteHarness(t)
	if err := w.seed.lib.SetUserPosition(w.userID, w.bookID, "abs", 500); err != nil {
		t.Fatal(err)
	}
	for _, ahead := range []time.Duration{time.Minute, 24 * time.Hour} {
		localAll(t, w, map[string]any{"id": "ahead-" + ahead.String(), "userId": w.userID, "libraryItemId": w.syncID,
			"currentTime": 100.0, "startedAt": time.Now().Add(ahead).UnixMilli()})
		if got := storedPosition(t, w); got != 500 {
			t.Fatalf("startedAt %v ahead rewound a newer position to %v", ahead, got)
		}
	}
}
