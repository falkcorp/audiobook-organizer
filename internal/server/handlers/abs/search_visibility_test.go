// file: internal/server/handlers/abs/search_visibility_test.go
// version: 1.0.0
// guid: 18fe3a64-0852-45a6-a04b-e847d0ab2d21
// last-edited: 2026-09-19

package abs_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Item-6 B6 (2026-09-19): the ABS search rendered books /items hides — merge
// losers among them — and a merge loser's sync id is a redirect, so
// GET /api/items/<hit id> answered a DIFFERENT id (96 of 652 prod hits).

type visFixture struct {
	h     *harness
	lib   *fakeLibrary
	tok   string
	root  string
	cols  *abscolFakeStore
	lists *absplFakeStore
}

func newVisFixture(t *testing.T) *visFixture {
	t.Helper()
	lib := newFakeLibrary()
	seed := &oracleSeed{lib: lib, root: t.TempDir()}
	cols, lists := &abscolFakeStore{}, &absplFakeStore{}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()),
		withCollections(cols), withPlaylists(lists))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	return &visFixture{h: h, lib: lib, tok: tok, root: seed.root, cols: cols, lists: lists}
}

func (v *visFixture) add(id, title string, primary bool, state string, files ...database.BookFile) {
	now := time.UnixMilli(1785370000000)
	dur := 600
	v.lib.addBook(&database.Book{ID: id, Title: title, IsPrimaryVersion: &primary, LibraryState: &state,
		CreatedAt: &now, UpdatedAt: &now, Duration: &dur}, files, nil)
}

func (v *visFixture) get(t *testing.T, path string) map[string]any {
	t.Helper()
	code, body := v.h.doAny(t, request{method: http.MethodGet, path: path, headers: bearer(v.tok)})
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %v", path, code, body)
	}
	return obj(t, body)
}

func (v *visFixture) searchIDs(t *testing.T, q string, limit int) []string {
	t.Helper()
	path := "/api/libraries/" + v.h.libraryID() + "/search?q=" + url.QueryEscape(q)
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	raw, _ := v.get(t, path)["book"].([]any)
	out := []string{}
	for _, r := range raw {
		id, _ := obj(t, obj(t, r)["libraryItem"])["id"].(string)
		out = append(out, id)
	}
	return out
}

func TestSearch_ExcludesHiddenAndMergeLoserBooks(t *testing.T) {
	v := newVisFixture(t)
	v.add("01WINNER00000000000000000V", "Needle Visible", true, "organized")
	v.add("01HIDDEN00000000000000000H", "Needle Hidden", false, "organized_source")
	// A loser that is STILL flagged primary+organized (the S1 contradictory-row
	// shape): only the redirect guard can keep it out of search.
	v.add("01LOSER000000000000000000L", "Needle Loser", true, "organized")
	loserSync, winnerSync := v.lib.mergeLoser(t, "01LOSER000000000000000000L", "01WINNER00000000000000000V")

	got := v.searchIDs(t, "needle", 0)
	if !slices.Equal(got, []string{winnerSync}) {
		t.Fatalf("search hits = %v, want only the visible book %s (loser id %s must not appear)", got, winnerSync, loserSync)
	}
	for _, id := range got {
		if back, _ := v.get(t, "/api/items/"+id)["id"].(string); back != id {
			t.Fatalf("search hit %s resolves to a different item %s", id, back)
		}
	}
}

func TestSearch_LimitCountsVisibleHitsOnly(t *testing.T) {
	v := newVisFixture(t)
	// Five hidden matches first in id order, then two visible ones.
	for i := range 5 {
		v.add("01A"+strconv.Itoa(i)+"0000000000000000000000", "Needle hidden "+strconv.Itoa(i), false, "organized_source")
	}
	v.add("01B00000000000000000000000", "Needle visible one", true, "organized")
	v.add("01B10000000000000000000000", "Needle visible two", true, "organized")
	want := []string{}
	for _, id := range []string{"01B00000000000000000000000", "01B10000000000000000000000"} {
		s, err := v.lib.MintOrGetSyncID(id)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, s)
	}
	if got := v.searchIDs(t, "needle", 2); !slices.Equal(got, want) {
		t.Fatalf("limit=2 search = %v, want both visible books %v", got, want)
	}
}

// The mapper invariant protects every list built from a source that is not
// visibility-filtered: /items (a contradictory primary+organized loser),
// collections and playlists (membership lists that can name a loser).
func TestItemView_NeverEmitsRedirectSyncID(t *testing.T) {
	v := newVisFixture(t)
	v.add("01WINNER00000000000000000V", "Winner", true, "organized")
	v.add("01LOSER000000000000000000L", "Loser", true, "organized")
	loserSync, winnerSync := v.lib.mergeLoser(t, "01LOSER000000000000000000L", "01WINNER00000000000000000V")
	members := []string{"01LOSER000000000000000000L", "01WINNER00000000000000000V"}

	ids := func(rows []any, key string) []string {
		out := []string{}
		for _, r := range rows {
			m := obj(t, r)
			if key != "" {
				m = obj(t, m[key])
			}
			id, _ := m["id"].(string)
			out = append(out, id)
		}
		return out
	}

	items, _ := v.get(t, "/api/libraries/"+v.h.libraryID()+"/items?limit=50")["results"].([]any)
	if got := ids(items, ""); slices.Contains(got, loserSync) || !slices.Contains(got, winnerSync) {
		t.Fatalf("/items ids = %v; must hold the winner %s and never the redirect id %s", got, winnerSync, loserSync)
	}

	col, err := v.cols.CreateCollection(&database.Collection{Name: "c", Type: database.CollectionTypeStatic, BookIDs: members})
	if err != nil {
		t.Fatal(err)
	}
	books, _ := v.get(t, "/api/collections/"+col.ID)["books"].([]any)
	if got := ids(books, ""); !slices.Equal(got, []string{winnerSync}) {
		t.Fatalf("collection books = %v, want only %s", got, winnerSync)
	}

	v.lists.lists = []database.UserPlaylist{{ID: "01PL", Name: "p", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "u1", BookIDs: members}}
	plItems, _ := v.get(t, "/api/playlists/01PL")["items"].([]any)
	if got := ids(plItems, "libraryItem"); !slices.Equal(got, []string{winnerSync}) {
		t.Fatalf("playlist items = %v, want only %s", got, winnerSync)
	}
}

// S4: the item path comes from a present track, not a missing=true row that
// happens to sort first.
func TestItemPath_ComesFromAPresentTrack(t *testing.T) {
	v := newVisFixture(t)
	present := filepath.Join(v.root, "Author", "Right Book", "02.mp3")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeAudio(t, present, 2048)
	stale := filepath.Join(v.root, "Other", "Wrong Book", "01.mp3") // never created
	v.add("01PATH0000000000000000000P", "Pathy", true, "organized",
		database.BookFile{ID: "f1", BookID: "01PATH0000000000000000000P", FilePath: stale, TrackNumber: 1,
			Missing: true, Duration: 300, FileSize: 2048, Format: "mp3"},
		database.BookFile{ID: "f2", BookID: "01PATH0000000000000000000P", FilePath: present, TrackNumber: 2,
			Duration: 300, FileSize: 2048, Format: "mp3"},
	)
	syncID, err := v.lib.MintOrGetSyncID("01PATH0000000000000000000P")
	if err != nil {
		t.Fatal(err)
	}
	item := v.get(t, "/api/items/"+syncID+"?expanded=1")
	path, _ := item["path"].(string)
	if !strings.Contains(path, "Right Book") || strings.Contains(path, "Wrong Book") {
		t.Fatalf("item path = %q, want the present track's directory (Right Book)", path)
	}
}

// Review W5: a membership list naming a merge loser shows the SURVIVOR in its
// place (not nothing), and removing that survivor id removes the stored loser.
func TestMembership_MergeLoserShowsAsSurvivorAndIsRemovable(t *testing.T) {
	v := newVisFixture(t)
	v.add("01WINNER00000000000000000V", "Winner", true, "organized")
	v.add("01LOSER000000000000000000L", "Loser", false, "organized")
	_, winnerSync := v.lib.mergeLoser(t, "01LOSER000000000000000000L", "01WINNER00000000000000000V")
	loserOnly := []string{"01LOSER000000000000000000L"}

	col, err := v.cols.CreateCollection(&database.Collection{Name: "c", Type: database.CollectionTypeStatic, BookIDs: loserOnly})
	if err != nil {
		t.Fatal(err)
	}
	books, _ := v.get(t, "/api/collections/"+col.ID)["books"].([]any)
	if len(books) != 1 || obj(t, books[0])["id"] != winnerSync {
		t.Fatalf("collection holding only a merge loser rendered %v, want the survivor %s", books, winnerSync)
	}

	v.lists.lists = []database.UserPlaylist{{ID: "01PL", Name: "p", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "u1", BookIDs: loserOnly}}
	items, _ := v.get(t, "/api/playlists/01PL")["items"].([]any)
	if len(items) != 1 || obj(t, items[0])["libraryItemId"] != winnerSync {
		t.Fatalf("playlist holding only a merge loser rendered %v, want the survivor %s", items, winnerSync)
	}

	// Removing by the id the client sees (the survivor's) removes the stored loser.
	if code, body := v.h.doAny(t, request{method: http.MethodPost, path: "/api/playlists/01PL/batch/remove",
		headers: bearer(v.tok), body: map[string]any{"items": []map[string]any{{"libraryItemId": winnerSync}}}}); code != http.StatusOK {
		t.Fatalf("playlist batch/remove = %d %v", code, body)
	}
	if got := v.lists.lists[0].BookIDs; len(got) != 0 {
		t.Fatalf("playlist still stores %v after removing the survivor id", got)
	}
	abscolGrantManage(t, v.h, "u1")
	if code, body := v.h.doAny(t, request{method: http.MethodDelete, path: "/api/collections/" + col.ID + "/book/" + winnerSync,
		headers: bearer(v.tok)}); code != http.StatusOK {
		t.Fatalf("collection remove = %d %v", code, body)
	}
	if got, _ := v.cols.GetCollection(col.ID); len(got.BookIDs) != 0 {
		t.Fatalf("collection still stores %v after removing the survivor id", got.BookIDs)
	}
}

// Review HIGH #1 + re-review LOW #2: a whole-list PATCH must never drop a
// member it did not know about, and must not silently ignore an omission
// either. The app read {A,B}; the web UI added C (version bump); the app's
// [B,A] no longer names every stored member, so it is refused with 409 and C
// survives. A pure reorder of the CURRENT set, and an addition, are applied.
func TestPlaylists_PatchItemsNeverDropsAConcurrentlyAddedMember(t *testing.T) {
	v := newVisFixture(t)
	ids := []string{"01AAAA0000000000000000000A", "01BBBB0000000000000000000B", "01CCCC0000000000000000000C", "01DDDD0000000000000000000D"}
	syncs := map[string]string{}
	for _, id := range ids {
		v.add(id, "Book "+id[2:6], true, "organized")
		syncs[id], _ = v.lib.MintOrGetSyncID(id)
	}
	A, B, C, D := ids[0], ids[1], ids[2], ids[3]
	items := func(books ...string) map[string]any {
		out := []map[string]any{}
		for _, b := range books {
			out = append(out, map[string]any{"libraryItemId": syncs[b]})
		}
		return map[string]any{"items": out}
	}
	patch := func(body map[string]any) int {
		code, _ := v.h.doAny(t, request{method: http.MethodPatch, path: "/api/playlists/01PL", headers: bearer(v.tok), body: body})
		return code
	}
	v.lists.lists = []database.UserPlaylist{{ID: "01PL", Name: "p", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "u1", Version: 1, BookIDs: []string{A, B}}}
	v.lists.beforeUpdate = func(f *absplFakeStore) {
		f.lists[0].BookIDs = append(f.lists[0].BookIDs, C)
		f.lists[0].Version++
	}
	if code := patch(items(B, A)); code != http.StatusConflict {
		t.Fatalf("stale [B,A] against {A,B,C} = %d, want 409", code)
	}
	if got := v.lists.lists[0].BookIDs; !slices.Equal(got, []string{A, B, C}) {
		t.Fatalf("stored %v, want [A B C]: the concurrently added member was lost", got)
	}
	if code := patch(items(C, B, A)); code != http.StatusOK {
		t.Fatalf("pure reorder = %d", code)
	}
	if got := v.lists.lists[0].BookIDs; !slices.Equal(got, []string{C, B, A}) {
		t.Fatalf("pure reorder stored %v", got)
	}
	if code := patch(items(D, C)); code != http.StatusConflict {
		t.Fatalf("a shorter list (would-be removal) = %d, want 409, not a silent no-op", code)
	}
	if code := patch(items(C, B, A, D)); code != http.StatusOK {
		t.Fatalf("addition = %d", code)
	}
	if got := v.lists.lists[0].BookIDs; !slices.Equal(got, []string{C, B, A, D}) {
		t.Fatalf("addition stored %v", got)
	}
}

// Re-review LOW #3: a sync id whose redirect chain is broken (dangling or
// cyclic) is a PERMANENT condition; it must be skipped like an unknown id, not
// 503 every request that carries it forever.
func TestPlaylists_BrokenRedirectChainIsSkippedNot503(t *testing.T) {
	v := newVisFixture(t)
	v.add("01AAAA0000000000000000000A", "A", true, "organized")
	v.add("01BBBB0000000000000000000B", "B", true, "organized")
	a, _ := v.lib.MintOrGetSyncID("01AAAA0000000000000000000A")
	b, _ := v.lib.MintOrGetSyncID("01BBBB0000000000000000000B")
	v.lists.lists = []database.UserPlaylist{{ID: "01PL", Name: "p", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "u1", Version: 1}}
	v.lib.resolveErr = map[string]error{a: fmt.Errorf("%w: starting at %s", database.ErrSyncRedirectChainBroken, a)}
	code, body := v.h.doAny(t, request{method: http.MethodPost, path: "/api/playlists/01PL/batch/add", headers: bearer(v.tok),
		body: map[string]any{"items": []map[string]any{{"libraryItemId": a}, {"libraryItemId": b}}}})
	if code != http.StatusOK {
		t.Fatalf("batch/add with one broken-chain id = %d %v, want 200 (skip it)", code, body)
	}
	if got := v.lists.lists[0].BookIDs; !slices.Equal(got, []string{"01BBBB0000000000000000000B"}) {
		t.Fatalf("stored %v, want only the resolvable book", got)
	}
}

// Review HIGH #1: a sync-id lookup ERROR must fail the request, never be
// treated as "no such book" and silently dropped from the written list.
func TestPlaylists_ResolveErrorFailsClosed(t *testing.T) {
	v := newVisFixture(t)
	v.add("01AAAA0000000000000000000A", "A", true, "organized")
	v.add("01BBBB0000000000000000000B", "B", true, "organized")
	a, _ := v.lib.MintOrGetSyncID("01AAAA0000000000000000000A")
	b, _ := v.lib.MintOrGetSyncID("01BBBB0000000000000000000B")
	stored := []string{"01AAAA0000000000000000000A", "01BBBB0000000000000000000B"}
	v.lists.lists = []database.UserPlaylist{{ID: "01PL", Name: "p", Type: database.UserPlaylistTypeStatic,
		CreatedByUserID: "u1", Version: 1, BookIDs: slices.Clone(stored)}}
	v.lib.resolveErr = map[string]error{a: errors.New("transient pebble read")}

	for _, r := range []request{
		{method: http.MethodPatch, path: "/api/playlists/01PL",
			body: map[string]any{"items": []map[string]any{{"libraryItemId": b}, {"libraryItemId": a}}}},
		{method: http.MethodPost, path: "/api/playlists/01PL/batch/add",
			body: map[string]any{"items": []map[string]any{{"libraryItemId": a}}}},
		{method: http.MethodPost, path: "/api/playlists/01PL/batch/remove",
			body: map[string]any{"items": []map[string]any{{"libraryItemId": a}}}},
	} {
		r.headers = bearer(v.tok)
		if code, _ := v.h.doAny(t, r); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with a failing lookup = %d, want 503", r.method, r.path, code)
		}
	}
	if got := v.lists.lists[0].BookIDs; !slices.Equal(got, stored) {
		t.Fatalf("a lookup error changed the stored list to %v", got)
	}
}
