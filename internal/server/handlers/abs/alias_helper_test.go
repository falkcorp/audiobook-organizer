// file: internal/server/handlers/abs/alias_helper_test.go
// version: 1.0.0
// guid: 4e2d8b61-7a93-4c05-b1f8-2d6e9c47a3b5
// last-edited: 2026-09-25

package abs_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

// ABS-ALIAS-HELPER (2026-09-25): every item-addressed route resolves through
// one helper (item_ref.go) that keys storage by the canonical item, echoes the
// requested id, and records alias uses per user. /api/me carries alias rows
// only for recorded aliases, so a finished merged book is not "finished" once
// per id in AudioBooth's stats.

// seededAliasHarness is newAliasHarness with the one-time alias seed already
// run, so every alias row in a list comes from a recorded use.
func seededAliasHarness(t *testing.T) (*writeHarness, string) {
	t.Helper()
	w, alias := newAliasHarness(t)
	if err := w.seed.lib.SeedSyncAliasUses(w.userID, nil); err != nil {
		t.Fatal(err)
	}
	return w, alias
}

func meProgressIDs(t *testing.T, w *writeHarness) []string {
	t.Helper()
	code, body, raw := w.req(t, http.MethodGet, "/api/me", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/me = %d %s", code, raw)
	}
	rows, ok := body["mediaProgress"].([]any)
	if !ok {
		t.Fatalf("mediaProgress must be an array, got %#v", body["mediaProgress"])
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, str(t, r.(map[string]any), "libraryItemId"))
	}
	slices.Sort(ids)
	return ids
}

// TestAliasUse_UnusedAliasGetsNoRow is the stats fix: a book finished through
// its canonical id, whose merge left an alias the client never addressed, is
// ONE row in /api/me, not one per id.
func TestAliasUse_UnusedAliasGetsNoRow(t *testing.T) {
	w, alias := seededAliasHarness(t)
	markFinishedVia(t, w, w.syncID)

	if got := meProgressIDs(t, w); !slices.Equal(got, []string{w.syncID}) {
		t.Fatalf("/api/me rows = %v, want only the canonical %s — alias %s was never used", got, w.syncID, alias)
	}
	if got := w.seed.lib.usedAliases(w.userID); got != nil {
		t.Fatalf("recorded aliases = %v, want none: the client only used the canonical id", got)
	}
}

// TestAliasUse_EveryItemRouteRecordsTheAlias: each authenticated item route
// addressed by an alias records it, after which /api/me carries its row.
func TestAliasUse_EveryItemRouteRecordsTheAlias(t *testing.T) {
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/items/%s", nil},
		{http.MethodPost, "/api/items/%s/play", map[string]any{}},
		{http.MethodGet, "/api/me/progress/%s", nil},
		{http.MethodPatch, "/api/me/progress/%s", map[string]any{"currentTime": 30.0}},
		{http.MethodGet, "/api/me/bookmarks/%s", nil},
		{http.MethodPost, "/api/me/item/%s/bookmark", map[string]any{"time": 5, "title": "t"}},
		{http.MethodPost, "/api/me/item/%s/remove-from-continue-listening", nil},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w, alias := seededAliasHarness(t)
			markFinishedVia(t, w, w.syncID)
			path := fmt.Sprintf(rt.path, alias)
			code, _, raw := w.req(t, rt.method, path, rt.body)
			if code != http.StatusOK && code != http.StatusNotFound {
				t.Fatalf("%s %s = %d %s", rt.method, path, code, raw)
			}
			if got := w.seed.lib.usedAliases(w.userID); !slices.Equal(got, []string{alias}) {
				t.Fatalf("recorded aliases = %v, want [%s]", got, alias)
			}
			want := []string{alias, w.syncID}
			slices.Sort(want)
			if got := meProgressIDs(t, w); !slices.Equal(got, want) {
				t.Fatalf("/api/me rows = %v, want %v once the alias was used", got, want)
			}
		})
	}
}

// TestAliasUse_BatchUpdateRecordsTheAlias: an id in a batch BODY is a use too.
func TestAliasUse_BatchUpdateRecordsTheAlias(t *testing.T) {
	w, alias := seededAliasHarness(t)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/batch/update",
		[]map[string]any{{"libraryItemId": alias, "currentTime": 42.0}})
	if code != http.StatusOK {
		t.Fatalf("batch update = %d %s", code, raw)
	}
	if got := w.seed.lib.usedAliases(w.userID); !slices.Equal(got, []string{alias}) {
		t.Fatalf("recorded aliases = %v, want [%s]", got, alias)
	}
	assertOneStoredRecord(t, w)
}

// TestAliasUse_CanonicalAndCoverRecordNothing: the canonical id is not an
// alias, and the cover route has no user to record against.
func TestAliasUse_CanonicalAndCoverRecordNothing(t *testing.T) {
	w, alias := seededAliasHarness(t)
	if code, _, raw := w.req(t, http.MethodGet, "/api/items/"+w.syncID, nil); code != http.StatusOK {
		t.Fatalf("GET canonical item = %d %s", code, raw)
	}
	rec, _ := w.do(t, request{method: http.MethodGet, path: "/api/items/" + alias + "/cover"})
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusOK {
		t.Fatalf("cover via alias = %d", rec.Code)
	}
	if got := w.seed.lib.usedAliases(w.userID); got != nil {
		t.Fatalf("recorded aliases = %v, want none", got)
	}
}

// TestAliasUse_RecordFailureStillServes: recording fails open.
func TestAliasUse_RecordFailureStillServes(t *testing.T) {
	w, alias := seededAliasHarness(t)
	w.seed.lib.mu.Lock()
	w.seed.lib.aliasUseErr = errString("disk on fire")
	w.seed.lib.mu.Unlock()
	code, item, raw := w.req(t, http.MethodGet, "/api/items/"+alias, nil)
	if code != http.StatusOK || str(t, item, "id") != alias {
		t.Fatalf("GET via alias with a failing recorder = %d %s; want 200 echoing the alias", code, raw)
	}
}

// ── bookmarks through an alias ──────────────────────────────────────────────

func bookmarkList(t *testing.T, w *writeHarness, id string) []map[string]any {
	t.Helper()
	code, body, raw := w.req(t, http.MethodGet, "/api/me/bookmarks/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/me/bookmarks/%s = %d %s", id, code, raw)
	}
	var out []map[string]any
	for _, b := range body["bookmarks"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

// TestAliasBookmarks_ItemSetAndEcho: a bookmark made before the merge (stored
// under the loser's id) and one made after (canonical) are both listed through
// either id, each under the id the client asked with; a same-instant pair is
// listed once, with the canonical row's title.
func TestAliasBookmarks_ItemSetAndEcho(t *testing.T) {
	w, alias := seededAliasHarness(t)
	for _, b := range []progress.Bookmark{
		{UserID: w.userID, ItemID: alias, TimeSec: 10, Title: "pre-merge"},
		{UserID: w.userID, ItemID: alias, TimeSec: 30, Title: "same spot, old"},
		{UserID: w.userID, ItemID: w.syncID, TimeSec: 30, Title: "same spot, new"},
	} {
		if err := w.bookmarks.CreateBookmark(b); err != nil {
			t.Fatal(err)
		}
	}
	code, created, raw := w.req(t, http.MethodPost, "/api/me/item/"+alias+"/bookmark",
		map[string]any{"time": 20, "title": "via alias"})
	if code != http.StatusOK || str(t, created, "libraryItemId") != alias {
		t.Fatalf("create via alias = %d %s; want 200 echoing %s", code, raw, alias)
	}
	if got, _ := w.bookmarks.ListBookmarks(w.userID, w.syncID); len(got) != 2 {
		t.Fatalf("canonical keyspace holds %d bookmarks, want 2 (the new one is stored canonically)", len(got))
	}

	for _, id := range []string{alias, w.syncID} {
		list := bookmarkList(t, w, id)
		var titles []string
		for _, b := range list {
			if got := str(t, b, "libraryItemId"); got != id {
				t.Fatalf("list via %s: libraryItemId = %s, want %s", id, got, id)
			}
			titles = append(titles, str(t, b, "title"))
		}
		want := []string{"pre-merge", "via alias", "same spot, new"}
		if !slices.Equal(titles, want) {
			t.Fatalf("list via %s: titles = %v, want %v", id, titles, want)
		}
	}
}

// TestAliasBookmarks_RenameAndDeleteReachPreMergeRows: a bookmark stored under
// the loser's id is renamed and deleted through the canonical id, and a delete
// at a shared instant removes every copy so it does not reappear.
func TestAliasBookmarks_RenameAndDeleteReachPreMergeRows(t *testing.T) {
	w, alias := seededAliasHarness(t)
	for _, b := range []progress.Bookmark{
		{UserID: w.userID, ItemID: alias, TimeSec: 10, Title: "pre-merge"},
		{UserID: w.userID, ItemID: alias, TimeSec: 30, Title: "old"},
		{UserID: w.userID, ItemID: w.syncID, TimeSec: 30, Title: "new"},
	} {
		if err := w.bookmarks.CreateBookmark(b); err != nil {
			t.Fatal(err)
		}
	}
	code, renamed, raw := w.req(t, http.MethodPatch, "/api/me/item/"+w.syncID+"/bookmark",
		map[string]any{"time": 10.0, "title": "renamed"})
	if code != http.StatusOK || str(t, renamed, "title") != "renamed" {
		t.Fatalf("rename pre-merge bookmark via canonical = %d %s", code, raw)
	}
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/item/"+w.syncID+"/bookmark/30", nil); code != http.StatusOK {
		t.Fatalf("delete shared instant = %d %s", code, raw)
	}
	list := bookmarkList(t, w, alias)
	if len(list) != 1 || str(t, list[0], "title") != "renamed" {
		t.Fatalf("after rename+delete: %v, want only the renamed pre-merge bookmark", list)
	}
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/item/"+alias+"/bookmark/10", nil); code != http.StatusOK {
		t.Fatalf("delete pre-merge bookmark via alias = %d %s", code, raw)
	}
	if list := bookmarkList(t, w, w.syncID); len(list) != 0 {
		t.Fatalf("after deleting everything: %v, want none", list)
	}
}
