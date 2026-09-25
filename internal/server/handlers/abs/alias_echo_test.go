// file: internal/server/handlers/abs/alias_echo_test.go
// version: 1.0.0
// guid: de050482-2489-42a2-ad9d-bc151b672b20
// last-edited: 2026-09-25

package abs_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Owner report 2026-09-25: "marked finished, left the page, came back, not
// marked". The book was opened through a merge LOSER's libraryItemId (an alias
// that redirects to the surviving book). AudioBooth keys its item page and its
// local MediaProgress row by the id it opened, and its syncFromAPI deletes every
// local row whose id is missing from user.mediaProgress. So every body the
// client files under the id it asked with must echo that id, and the
// client-facing list must carry a row under each alias. Storage stays keyed by
// the canonical book: these tests also pin that no second record appears.

const aliasLoserBookID = "01LOSER000000000000000000L"

// newAliasHarness is the write harness with a merge loser redirecting to the
// single-file book (w.syncID stays the canonical id).
func newAliasHarness(t *testing.T) (w *writeHarness, alias string) {
	t.Helper()
	w = newWriteHarness(t)
	alias, winner := w.seed.lib.mergeLoser(t, aliasLoserBookID, w.bookID)
	if winner != w.syncID {
		t.Fatalf("mergeLoser winner sync id = %s, want %s", winner, w.syncID)
	}
	if alias == w.syncID {
		t.Fatal("alias and canonical ids must differ")
	}
	return w, alias
}

func markFinishedVia(t *testing.T, w *writeHarness, id string) {
	t.Helper()
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+id,
		map[string]any{"currentTime": 9975.0, "duration": 9975.0, "isFinished": true})
	if code != http.StatusOK {
		t.Fatalf("PATCH /api/me/progress/%s = %d %s", id, code, raw)
	}
}

func getProgress(t *testing.T, w *writeHarness, id string) map[string]any {
	t.Helper()
	code, body, raw := w.req(t, http.MethodGet, "/api/me/progress/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/me/progress/%s = %d %s", id, code, raw)
	}
	return body
}

// assertOneStoredRecord pins "no duplicate progress records": the position and
// the book state exist under the canonical book only, never under the loser.
func assertOneStoredRecord(t *testing.T, w *writeHarness) {
	t.Helper()
	lib := w.seed.lib
	lib.mu.Lock()
	defer lib.mu.Unlock()
	var canonical, loser int
	for key := range lib.positions {
		switch key {
		case w.userID + "|" + w.bookID:
			canonical++
		case w.userID + "|" + aliasLoserBookID:
			loser++
		}
	}
	if canonical != 1 || loser != 0 {
		t.Fatalf("stored positions: canonical=%d loser=%d, want 1 and 0 — a write through the alias "+
			"must land on the canonical book's record", canonical, loser)
	}
	if lib.states[w.userID+"|"+aliasLoserBookID] != nil {
		t.Fatal("a book state was stored under the merge loser's book id")
	}
	if lib.states[w.userID+"|"+w.bookID] == nil {
		t.Fatal("no book state stored under the canonical book id")
	}
}

func TestAliasProgress_PatchThenGetViaAliasEchoesAlias(t *testing.T) {
	w, alias := newAliasHarness(t)
	markFinishedVia(t, w, alias)

	row := getProgress(t, w, alias)
	if got := str(t, row, "libraryItemId"); got != alias {
		t.Fatalf("GET via alias: libraryItemId = %s, want the requested alias %s — AudioBooth files the "+
			"row under this id and the alias page never reads a row filed under the canonical id", got, alias)
	}
	if got := str(t, row, "id"); got != w.userID+"-"+alias {
		t.Fatalf("GET via alias: id = %s, want %s", got, w.userID+"-"+alias)
	}
	if got := str(t, row, "mediaItemId"); got != alias {
		t.Fatalf("GET via alias: mediaItemId = %s, want %s", got, alias)
	}
	if row["isFinished"] != true {
		t.Fatalf("GET via alias: isFinished = %v, want true", row["isFinished"])
	}

	// The row-id form of the alias echoes the alias too.
	if got := str(t, getProgress(t, w, w.userID+"-"+alias), "libraryItemId"); got != alias {
		t.Fatalf("GET via alias row id: libraryItemId = %s, want %s", got, alias)
	}

	canon := getProgress(t, w, w.syncID)
	if got := str(t, canon, "libraryItemId"); got != w.syncID {
		t.Fatalf("GET via canonical: libraryItemId = %s, want %s", got, w.syncID)
	}
	if canon["isFinished"] != true {
		t.Fatalf("GET via canonical: isFinished = %v, want true — alias and canonical must be one record", canon["isFinished"])
	}
	for _, k := range []string{"currentTime", "duration", "lastUpdate", "progress"} {
		if num(t, canon, k) != num(t, row, k) {
			t.Fatalf("%s differs: canonical %v, alias %v — they must be the same record", k, canon[k], row[k])
		}
	}
	assertOneStoredRecord(t, w)
}

// TestAliasProgress_ListCarriesAnAliasRow: GET /api/me/progress and GET /api/me
// carry the canonical row AND one row under the alias with the same values, so
// syncFromAPI keeps (and refreshes) the client's alias-keyed row.
func TestAliasProgress_ListCarriesAnAliasRow(t *testing.T) {
	w, alias := newAliasHarness(t)
	markFinishedVia(t, w, alias)

	for _, path := range []string{"/api/me/progress", "/api/me"} {
		code, body, raw := w.req(t, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, raw)
		}
		rows, ok := body["mediaProgress"].([]any)
		if !ok {
			t.Fatalf("%s: mediaProgress must be an array, got %#v", path, body["mediaProgress"])
		}
		byID := map[string]map[string]any{}
		for _, r := range rows {
			m := r.(map[string]any)
			byID[str(t, m, "libraryItemId")] = m
		}
		if len(rows) != 2 || byID[w.syncID] == nil || byID[alias] == nil {
			t.Fatalf("%s: rows = %d %v, want exactly the canonical row and the alias row", path, len(rows), keys(byID))
		}
		if byID[alias]["isFinished"] != true || byID[w.syncID]["isFinished"] != true {
			t.Fatalf("%s: both rows must read finished: alias=%v canonical=%v", path,
				byID[alias]["isFinished"], byID[w.syncID]["isFinished"])
		}
		if got := str(t, byID[alias], "id"); got != w.userID+"-"+alias {
			t.Fatalf("%s: alias row id = %s, want %s", path, got, w.userID+"-"+alias)
		}
	}
	assertOneStoredRecord(t, w)
}

// TestAliasItem_EchoesRequestedID: GET /api/items/:id and POST
// /api/items/:id/play render under the id the client opened.
func TestAliasItem_EchoesRequestedID(t *testing.T) {
	w, alias := newAliasHarness(t)
	markFinishedVia(t, w, alias)

	code, item, raw := w.req(t, http.MethodGet, "/api/items/"+alias, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/items/alias = %d %s", code, raw)
	}
	if got := str(t, item, "id"); got != alias {
		t.Fatalf("item id = %s, want the requested alias %s", got, alias)
	}
	ump, ok := item["userMediaProgress"].(map[string]any)
	if !ok {
		t.Fatalf("userMediaProgress missing: %#v", item["userMediaProgress"])
	}
	if got := str(t, ump, "libraryItemId"); got != alias || ump["isFinished"] != true {
		t.Fatalf("userMediaProgress libraryItemId=%s isFinished=%v, want %s/true", got, ump["isFinished"], alias)
	}

	code, item, raw = w.req(t, http.MethodGet, "/api/items/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/items/canonical = %d %s", code, raw)
	}
	if got := str(t, item, "id"); got != w.syncID {
		t.Fatalf("item id via canonical = %s, want %s", got, w.syncID)
	}

	code, play, raw := w.req(t, http.MethodPost, "/api/items/"+alias+"/play", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("play via alias = %d %s", code, raw)
	}
	if got := str(t, play, "libraryItemId"); got != alias {
		t.Fatalf("play session libraryItemId = %s, want %s", got, alias)
	}
	li, ok := play["libraryItem"].(map[string]any)
	if !ok || str(t, li, "id") != alias {
		t.Fatalf("play session libraryItem.id = %#v, want %s", play["libraryItem"], alias)
	}

	// A sync on that session writes the canonical record, not a second one.
	sid := str(t, play, "id")
	code, _, raw = w.req(t, http.MethodPost, "/api/session/"+sid+"/sync",
		map[string]any{"currentTime": 9975.0, "timeListened": 1.0})
	if code != http.StatusOK {
		t.Fatalf("session sync = %d %s", code, raw)
	}
	assertOneStoredRecord(t, w)
}

// ── provider-level: the client list's alias rows ────────────────────────────

// TestClientMediaProgress_AliasErrorFailsTheList: an alias read failure is an
// error, never a list without the alias rows (the client deletes what is missing).
func TestClientMediaProgress_AliasErrorFailsTheList(t *testing.T) {
	f := newUDFake()
	f.addBook("bk1", nil, 1800)
	f.presetSyncID("bk1", udSyncID(1))
	f.addPosition("u1", "bk1", "abs", 60, time.Now())
	f.aliasErr = errors.New("disk on fire")

	rows, err := udProvider(t, f).ClientMediaProgress("u1")
	if err == nil || rows != nil {
		t.Fatalf("ClientMediaProgress = %d rows, err %v; want nil rows and an error", len(rows), err)
	}
}

// TestClientMediaProgress_AliasRowShadowsAStaleLoserRow: a position left under
// the merge loser's book renders under the loser's syncID; when that id is an
// alias of a live item with progress, the list carries the live item's values
// for it (what GET /api/me/progress/<id> serves), once.
func TestClientMediaProgress_AliasRowShadowsAStaleLoserRow(t *testing.T) {
	f := newUDFake()
	f.addBook("winner", nil, 1800)
	f.addBook("loser", nil, 1800)
	f.presetSyncID("winner", udSyncID(1))
	f.presetSyncID("loser", udSyncID(2))
	now := time.Now()
	f.addPosition("u1", "winner", "abs", 900, now)
	f.addPosition("u1", "loser", "abs", 10, now.Add(-time.Hour))
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(2)}}

	rows, err := udProvider(t, f).ClientMediaProgress("u1")
	if err != nil {
		t.Fatalf("ClientMediaProgress: %v", err)
	}
	decoded := udRows(t, rows)
	if len(decoded) != 2 {
		t.Fatalf("got %d rows, want 2 (canonical + one alias row)", len(decoded))
	}
	for i := range decoded {
		if udStr(t, decoded[i], "libraryItemId") == udSyncID(2) {
			if got := decoded[i]["currentTime"].String(); got != "900" {
				t.Fatalf("alias row currentTime = %s, want 900 (the live item's record, not the stale loser row)", got)
			}
		}
	}

	// MediaProgress (browse filters, sorts) is untouched: one row per stored book.
	plain, err := udProvider(t, f).MediaProgress("u1")
	if err != nil || len(plain) != 2 {
		t.Fatalf("MediaProgress = %d rows, err %v; want the 2 stored books", len(plain), err)
	}
}

func keys(m map[string]map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ",")
}
