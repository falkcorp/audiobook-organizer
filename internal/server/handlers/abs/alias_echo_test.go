// file: internal/server/handlers/abs/alias_echo_test.go
// version: 1.3.0
// guid: de050482-2489-42a2-ad9d-bc151b672b20
// last-edited: 2026-09-25

package abs_test

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
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

// TestClientMediaProgress_AliasErrorKeepsCanonicalRows: an alias lookup failure
// omits only the alias rows. The canonical list is still returned, with no
// error, because failing it would take /api/me and login down over rows the
// client can live without (and leaving them out is the pre-alias behaviour).
func TestClientMediaProgress_AliasErrorKeepsCanonicalRows(t *testing.T) {
	f := newUDFake()
	for i, id := range []string{"bk1", "bk2"} {
		f.addBook(id, nil, 1800)
		f.presetSyncID(id, udSyncID(i+1))
		f.addPosition("u1", id, "abs", 60, time.Now())
	}
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(9)}}
	f.aliasErr = errors.New("disk on fire")

	rows, err := udProvider(t, f).ClientMediaProgress("u1")
	if err != nil {
		t.Fatalf("ClientMediaProgress err = %v, want nil — an alias failure must not fail the list", err)
	}
	decoded := udRows(t, rows)
	got := map[string]bool{}
	for i := range decoded {
		got[udStr(t, decoded[i], "libraryItemId")] = true
	}
	if len(decoded) != 2 || !got[udSyncID(1)] || !got[udSyncID(2)] {
		t.Fatalf("rows = %v, want exactly the 2 canonical rows and no alias row", got)
	}
}

// TestClientMediaProgress_StaleLoserRow: a position left under the merge
// loser's book renders under the loser's syncID, which now redirects to a live
// item with its own row. GET/PATCH /api/me/progress/<that id> serve the live
// item, so the stale row is never sent as stored: when the client has used
// the alias it is replaced by the live item's values, and when it has not it
// is dropped (it would be a second "finished" in the client's stats).
func TestClientMediaProgress_StaleLoserRow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		used    []string
		wantIDs []string
	}{
		{"alias used: live values under the alias", []string{udSyncID(2)}, []string{udSyncID(1), udSyncID(2)}},
		{"alias unused: stale row dropped", nil, []string{udSyncID(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUDFake()
			f.addBook("winner", nil, 1800)
			f.addBook("loser", nil, 1800)
			f.presetSyncID("winner", udSyncID(1))
			f.presetSyncID("loser", udSyncID(2))
			now := time.Now()
			f.addPosition("u1", "winner", "abs", 900, now)
			f.addPosition("u1", "loser", "abs", 10, now.Add(-time.Hour))
			f.aliases = map[string][]string{udSyncID(1): {udSyncID(2)}}
			f.markUsed("u1", tc.used...)

			rows, err := udProvider(t, f).ClientMediaProgress("u1")
			if err != nil {
				t.Fatalf("ClientMediaProgress: %v", err)
			}
			decoded := udRows(t, rows)
			var ids []string
			for i := range decoded {
				ids = append(ids, udStr(t, decoded[i], "libraryItemId"))
				if got := decoded[i]["currentTime"].String(); got != "900" {
					t.Fatalf("row %s currentTime = %s, want 900 (the live item's record, never the stale loser row)",
						ids[len(ids)-1], got)
				}
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.wantIDs) {
				t.Fatalf("rows = %v, want %v", ids, tc.wantIDs)
			}

			// MediaProgress (browse filters, sorts) is untouched: one row per stored book.
			plain, err := udProvider(t, f).MediaProgress("u1")
			if err != nil || len(plain) != 2 {
				t.Fatalf("MediaProgress = %d rows, err %v; want the 2 stored books", len(plain), err)
			}
		})
	}
}

// TestClientMediaProgress_SeedKeepsAliasesOfRecentlyTouchedItems: on a user's
// first list after the switch to tracked aliases, the aliases of items whose
// progress changed since #3558 shipped are recorded (the client may hold rows
// for them from #3558's list); older items' aliases are not. The seed runs once.
func TestClientMediaProgress_SeedKeepsAliasesOfRecentlyTouchedItems(t *testing.T) {
	f := newUDFake()
	f.addBook("recent", nil, 1800)
	f.addBook("old", nil, 1800)
	f.presetSyncID("recent", udSyncID(1))
	f.presetSyncID("old", udSyncID(2))
	f.addPosition("u1", "recent", "abs", 900, time.Now())
	f.addPosition("u1", "old", "abs", 900, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(8)}, udSyncID(2): {udSyncID(9)}}

	p := udProvider(t, f)
	for call := 1; call <= 2; call++ {
		rows, err := p.ClientMediaProgress("u1")
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		var ids []string
		for _, r := range udRows(t, rows) {
			ids = append(ids, udStr(t, r, "libraryItemId"))
		}
		slices.Sort(ids)
		if want := []string{udSyncID(1), udSyncID(2), udSyncID(8)}; !slices.Equal(ids, want) {
			t.Fatalf("call %d: rows = %v, want %v (the recent item's alias only)", call, ids, want)
		}
	}
	if f.seedWrites != 1 || !f.seeded["u1"] || !slices.Equal(f.used["u1"], []string{udSyncID(8)}) {
		t.Fatalf("seed writes=%d seeded=%v used=%v; want one seed recording only %s",
			f.seedWrites, f.seeded["u1"], f.used["u1"], udSyncID(8))
	}
}

// TestClientMediaProgress_SeedWriteFailureRetries: a failed seed write still
// sends the seeded rows and leaves the user unseeded, so the next list retries.
func TestClientMediaProgress_SeedWriteFailureRetries(t *testing.T) {
	f := newUDFake()
	f.addBook("bk", nil, 1800)
	f.presetSyncID("bk", udSyncID(1))
	f.addPosition("u1", "bk", "abs", 900, time.Now())
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(8)}}
	f.seedErr = errors.New("disk on fire")

	p := udProvider(t, f)
	for call := 1; call <= 2; call++ {
		rows, err := p.ClientMediaProgress("u1")
		if err != nil || len(rows) != 2 {
			t.Fatalf("call %d: %d rows, err %v; want canonical + seeded alias row", call, len(rows), err)
		}
	}
	if f.seedWrites != 2 || f.seeded["u1"] {
		t.Fatalf("seed writes=%d seeded=%v; want a retry per list and no seeded mark", f.seedWrites, f.seeded["u1"])
	}
}

// TestClientMediaProgress_SeedLookupFailureRetries: one item's alias lookup
// failing must not mark the user seeded. The aliases that were found are
// recorded and sent, and the next list retries the seed, so the failed item's
// aliases are recorded once the lookup works. Marking the user seeded here
// would drop that item's alias rows forever, and the client would delete the
// local row it holds under the alias (the 2026-09-25 bug).
func TestClientMediaProgress_SeedLookupFailureRetries(t *testing.T) {
	f := newUDFake()
	f.addBook("good", nil, 1800)
	f.addBook("flaky", nil, 1800)
	f.presetSyncID("good", udSyncID(1))
	f.presetSyncID("flaky", udSyncID(2))
	f.addPosition("u1", "good", "abs", 900, time.Now())
	f.addPosition("u1", "flaky", "abs", 900, time.Now())
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(8)}, udSyncID(2): {udSyncID(9)}}
	f.aliasErrFor = map[string]error{udSyncID(2): errors.New("disk hiccup")}

	p := udProvider(t, f)
	listIDs := func(call int) []string {
		t.Helper()
		rows, err := p.ClientMediaProgress("u1")
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		var ids []string
		for _, r := range udRows(t, rows) {
			ids = append(ids, udStr(t, r, "libraryItemId"))
		}
		slices.Sort(ids)
		return ids
	}

	if got, want := listIDs(1), []string{udSyncID(1), udSyncID(2), udSyncID(8)}; !slices.Equal(got, want) {
		t.Fatalf("call 1: rows = %v, want %v (the good item's alias; the failed item's is unknown yet)", got, want)
	}
	f.mu.Lock()
	seeded, used := f.seeded["u1"], slices.Clone(f.used["u1"])
	f.mu.Unlock()
	if seeded || !slices.Contains(used, udSyncID(8)) {
		t.Fatalf("after a failed lookup: seeded=%v used=%v; want %s recorded and the user NOT seeded", seeded, used, udSyncID(8))
	}

	f.mu.Lock()
	f.aliasErrFor = nil
	f.mu.Unlock()
	if got, want := listIDs(2), []string{udSyncID(1), udSyncID(2), udSyncID(8), udSyncID(9)}; !slices.Equal(got, want) {
		t.Fatalf("call 2: rows = %v, want %v (the retry finds the failed item's alias)", got, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.seeded["u1"] || !slices.Contains(f.used["u1"], udSyncID(9)) {
		t.Fatalf("after the retry: seeded=%v used=%v; want %s recorded and the user seeded", f.seeded["u1"], f.used["u1"], udSyncID(9))
	}
}

// TestClientMediaProgress_SeedAliasLimitStillSeeds: ErrSyncAliasLimit is
// deterministic, so it must not hold the seed open (a retry cannot succeed,
// and every list would redo the seed forever). The other items' aliases are
// recorded and the user is seeded.
func TestClientMediaProgress_SeedAliasLimitStillSeeds(t *testing.T) {
	f := newUDFake()
	f.addBook("good", nil, 1800)
	f.addBook("huge", nil, 1800)
	f.presetSyncID("good", udSyncID(1))
	f.presetSyncID("huge", udSyncID(2))
	f.addPosition("u1", "good", "abs", 900, time.Now())
	f.addPosition("u1", "huge", "abs", 900, time.Now())
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(8)}}
	f.aliasErrFor = map[string]error{udSyncID(2): fmt.Errorf("%w: starting at %s", database.ErrSyncAliasLimit, udSyncID(2))}

	p := udProvider(t, f)
	for call := 1; call <= 2; call++ {
		if _, err := p.ClientMediaProgress("u1"); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seedWrites != 1 || !f.seeded["u1"] || !slices.Equal(f.used["u1"], []string{udSyncID(8)}) {
		t.Fatalf("seed writes=%d seeded=%v used=%v; want one seed recording %s and the user seeded",
			f.seedWrites, f.seeded["u1"], f.used["u1"], udSyncID(8))
	}
}

// TestClientMediaProgress_SeedWindowBounds: the one-time seed records the
// aliases of items whose progress changed in [aliasSeedSince, cutoff), where
// the cutoff is the moment this server first ran with alias-use tracking.
// Before aliasSeedSince the client never received alias rows (#3558 had not
// shipped); from the cutoff on, every alias use is recorded as it happens,
// so seeding a row touched after it would re-create the stats double count
// for an alias the client never held. Edges are exact to the millisecond.
func TestClientMediaProgress_SeedWindowBounds(t *testing.T) {
	since := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		at     time.Time
		seeded bool
	}{
		{"1ms before since", since.Add(-time.Millisecond), false},
		{"at since", since, true},
		{"1ms before cutoff", cutoff.Add(-time.Millisecond), true},
		{"at cutoff", cutoff, false},
		{"weeks after cutoff", cutoff.Add(21 * 24 * time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUDFake()
			f.addBook("bk", nil, 1800)
			f.presetSyncID("bk", udSyncID(1))
			f.addPosition("u1", "bk", "abs", 900, tc.at)
			f.aliases = map[string][]string{udSyncID(1): {udSyncID(8)}}

			if _, err := udProviderWithCutoff(t, f, cutoff).ClientMediaProgress("u1"); err != nil {
				t.Fatalf("ClientMediaProgress: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if got := slices.Contains(f.used["u1"], udSyncID(8)); got != tc.seeded {
				t.Fatalf("lastUpdate %s: alias seeded = %v, want %v (window [%s, %s))",
					tc.at.Format(time.RFC3339Nano), got, tc.seeded, since.Format(time.RFC3339), cutoff.Format(time.RFC3339))
			}
			if !f.seeded["u1"] {
				t.Fatal("the user must be marked seeded either way")
			}
		})
	}
}

// TestClientMediaProgress_AliasUseReadFailureFailsOpen: an unreadable alias
// record sends the canonical list with no alias rows.
func TestClientMediaProgress_AliasUseReadFailureFailsOpen(t *testing.T) {
	f := newUDFake()
	f.addBook("bk", nil, 1800)
	f.presetSyncID("bk", udSyncID(1))
	f.addPosition("u1", "bk", "abs", 900, time.Now())
	f.aliases = map[string][]string{udSyncID(1): {udSyncID(8)}}
	f.usedErr = errors.New("disk on fire")

	rows, err := udProvider(t, f).ClientMediaProgress("u1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("got %d rows, err %v; want the canonical row alone", len(rows), err)
	}
}

// TestBookmarks_ListedUnderLiveAndUsedAliasIDs: /api/me bookmarks stored under
// a merge loser's id are listed under the live id and under each alias the
// client has used; a same-instant pair is listed once per id, canonical title.
func TestBookmarks_ListedUnderLiveAndUsedAliasIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		used []string
		want []string
	}{
		{"alias unused", nil, []string{
			udSyncID(1) + "@10=pre", udSyncID(1) + "@30=new",
		}},
		{"alias used", []string{udSyncID(2)}, []string{
			udSyncID(1) + "@10=pre", udSyncID(1) + "@30=new",
			udSyncID(2) + "@10=pre", udSyncID(2) + "@30=new",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUDFake()
			f.addBook("winner", nil, 1800)
			f.presetSyncID("winner", udSyncID(1))
			f.aliases = map[string][]string{udSyncID(1): {udSyncID(2)}}
			f.markUsed("u1", tc.used...)
			f.addBookmark(progress.Bookmark{UserID: "u1", ItemID: udSyncID(2), TimeSec: 10, Title: "pre"})
			f.addBookmark(progress.Bookmark{UserID: "u1", ItemID: udSyncID(2), TimeSec: 30, Title: "old"})
			f.addBookmark(progress.Bookmark{UserID: "u1", ItemID: udSyncID(1), TimeSec: 30, Title: "new"})

			rows, err := udProvider(t, f).Bookmarks("u1")
			if err != nil {
				t.Fatalf("Bookmarks: %v", err)
			}
			var got []string
			for _, r := range udRows(t, rows) {
				got = append(got, udStr(t, r, "libraryItemId")+"@"+r["time"].String()+"="+udStr(t, r, "title"))
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("bookmarks = %v, want %v", got, tc.want)
			}
		})
	}
}

func keys(m map[string]map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ",")
}
