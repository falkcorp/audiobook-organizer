// file: internal/server/handlers/abs/abs_decode_gaps_test.go
// version: 1.0.0
// guid: 64e1fdcd-d115-4551-b3b3-d5fac8064f13
// last-edited: 2026-09-19
//
// ── AudioBooth decode gaps and unordered sorts ──────────────────────────────
//
// Swift's Decodable is all-or-nothing: one missing required key fails the whole
// response, and the app shows an empty screen with nothing logged server-side.
// The mirror structs below use POINTER fields for every key the Swift model
// decodes non-optionally, so a missing key reads as nil and fails the test
// instead of silently decoding to a zero value.

package abs_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// decodeGapsRaw issues a GET and returns the raw body.
func decodeGapsRaw(t *testing.T, h *harness, tok, path string) []byte {
	t.Helper()
	w, _ := h.do(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func decodeGapsLogin(t *testing.T, seed *oracleSeed, ud *fakeUserData, opts ...harnessOpt) (*harness, string) {
	t.Helper()
	all := append([]harnessOpt{withLibrary(seed), withUserData(ud)}, opts...)
	h := newHarness(t, "jwt", nil, all...)
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	return h, str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
}

// ── #1: GET /api/libraries/:id?include=filterdata ───────────────────────────

// swiftIDName mirrors FilterData.Author / FilterData.Series: no custom decoder,
// so both keys are required.
type swiftIDName struct {
	ID   *string `json:"id"`
	Name *string `json:"name"`
}

// swiftFilterDataResponse mirrors FilterDataService.performFetch's Response:
// `filterdata` is decoded non-optionally.
type swiftFilterDataResponse struct {
	Filterdata *struct {
		Authors          []swiftIDName `json:"authors"`
		Genres           []string      `json:"genres"`
		Tags             []string      `json:"tags"`
		Series           []swiftIDName `json:"series"`
		Narrators        []string      `json:"narrators"`
		Languages        []string      `json:"languages"`
		Publishers       []string      `json:"publishers"`
		PublishedDecades []string      `json:"publishedDecades"`
	} `json:"filterdata"`
	Library *struct {
		ID *string `json:"id"`
	} `json:"library"`
}

func TestLibrary_IncludeFilterDataDecodesAsAudioBoothResponse(t *testing.T) {
	seed := absSeedTwoSeries(t)
	seed.lib.attachNarrators(seed.singleID, "Dan Stevens", "Claire Danes")
	h, tok := decodeGapsLogin(t, seed, fixtureUserData())

	raw := decodeGapsRaw(t, h, tok, "/api/libraries/"+h.libraryID()+"?include=filterdata")
	var got swiftFilterDataResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if got.Filterdata == nil {
		t.Fatalf("response has no `filterdata` key -- AudioBooth's FilterDataService decode fails: %s", raw)
	}
	fd := got.Filterdata
	if len(fd.Authors) < 2 {
		t.Fatalf("filterdata.authors = %d entries, want >= 2 (fixture seeds two authors)", len(fd.Authors))
	}
	for i, a := range fd.Authors {
		if a.ID == nil || a.Name == nil || *a.Name == "" {
			t.Fatalf("filterdata.authors[%d] missing id/name: %+v", i, a)
		}
	}
	if len(fd.Series) < 2 {
		t.Fatalf("filterdata.series = %d entries, want >= 2", len(fd.Series))
	}
	for i, s := range fd.Series {
		if s.ID == nil || s.Name == nil || *s.Name == "" {
			t.Fatalf("filterdata.series[%d] missing id/name: %+v", i, s)
		}
	}
	for _, want := range []string{"Dan Stevens", "Claire Danes"} {
		found := false
		for _, n := range fd.Narrators {
			found = found || n == want
		}
		if !found {
			t.Fatalf("filterdata.narrators = %v, missing %q", fd.Narrators, want)
		}
	}
	if len(fd.Genres) == 0 {
		t.Fatalf("filterdata.genres is empty; the fixture seeds genres")
	}
	if got.Library == nil || got.Library.ID == nil || *got.Library.ID != h.libraryID() {
		t.Fatalf("response `library` = %+v, want the library object alongside filterdata", got.Library)
	}

	// Control: without include the plain Library object is unchanged -- every
	// other caller decodes it directly.
	plain := decodeGapsRaw(t, h, tok, "/api/libraries/"+h.libraryID())
	var m map[string]any
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatalf("decode plain: %v", err)
	}
	if m["id"] != h.libraryID() {
		t.Fatalf("plain library id = %v, want %q", m["id"], h.libraryID())
	}
	if _, has := m["filterdata"]; has {
		t.Fatalf("plain library response carries filterdata without include")
	}
}

// ── #2: playlist items whose library item cannot be built ──────────────────

// swiftPlaylistPage mirrors Page<Playlist> with Playlist's and PlaylistItem's
// required keys. libraryItem is required whenever episodeId is null.
type swiftPlaylistPage struct {
	Results *[]struct {
		ID         *string `json:"id"`
		Name       *string `json:"name"`
		LibraryID  *string `json:"libraryId"`
		UserID     *string `json:"userId"`
		LastUpdate *int64  `json:"lastUpdate"`
		CreatedAt  *int64  `json:"createdAt"`
		Items      *[]struct {
			LibraryItemID *string          `json:"libraryItemId"`
			EpisodeID     *string          `json:"episodeId"`
			LibraryItem   *json.RawMessage `json:"libraryItem"`
		} `json:"items"`
	} `json:"results"`
	Total *int `json:"total"`
}

func TestLibraryPlaylists_ItemWithUnbuildableViewIsDropped(t *testing.T) {
	store := &absplFakeStore{}
	seed := absSeedTwoSeries(t)
	h, tok := decodeGapsLogin(t, seed, fixtureUserData(), withPlaylists(store))
	// s10-b1's item view cannot be built (its file list fails to load), but its
	// sync id still resolves -- the exact case that used to emit an item with no
	// libraryItem.
	seed.lib.setBookFilesErr("s10-b1", errors.New("injected: file list unreadable"))
	store.lists = []database.UserPlaylist{{
		ID:              "01PLAYLISTDECODEGAP0000000",
		Name:            "Mixed",
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         []string{seed.singleID, "s10-b1", "s10-b2", "s20-b1"},
		CreatedByUserID: "u1",
	}}

	raw := decodeGapsRaw(t, h, tok, "/api/libraries/"+h.libraryID()+"/playlists")
	var page swiftPlaylistPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Results == nil || len(*page.Results) != 1 || page.Total == nil {
		t.Fatalf("want one playlist with results+total, got %s", raw)
	}
	pl := (*page.Results)[0]
	if pl.ID == nil || pl.Name == nil || pl.LibraryID == nil || pl.UserID == nil ||
		pl.LastUpdate == nil || pl.CreatedAt == nil || pl.Items == nil {
		t.Fatalf("playlist is missing a required key: %s", raw)
	}
	items := *pl.Items
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 -- the unbuildable book must be dropped, the rest kept", len(items))
	}
	bad := absplSyncIDFor(t, seed, "s10-b1")
	for i, it := range items {
		if it.LibraryItemID == nil {
			t.Fatalf("items[%d] has no libraryItemId", i)
		}
		if *it.LibraryItemID == bad {
			t.Fatalf("items[%d] is the book whose view failed; it must be dropped", i)
		}
		if it.EpisodeID == nil && (it.LibraryItem == nil || string(*it.LibraryItem) == "null") {
			t.Fatalf("items[%d] has episodeId null and no libraryItem -- the whole Page<Playlist> fails to decode", i)
		}
	}
}

// ── #3: book list sorts ─────────────────────────────────────────────────────

// decodeGapsBookSeed builds five books whose insertion order (Alpha..Echo)
// differs from every ordering asserted below.
func decodeGapsBookSeed(t *testing.T) *oracleSeed {
	t.Helper()
	lib := newFakeLibrary()
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	base := time.UnixMilli(1785370000000)
	at := func(n int) *time.Time { ts := base.Add(time.Duration(n) * time.Minute); return &ts }
	add := func(id, title string, created, updated int) {
		lib.addBook(&database.Book{
			ID: id, Title: title,
			CreatedAt: at(created), UpdatedAt: at(updated),
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		}, nil, nil)
	}
	add("b1", "Alpha", 5, 1)
	add("b2", "Bravo", 1, 5)
	add("b3", "Charlie", 3, 2)
	add("b4", "Delta", 2, 4)
	add("b5", "Echo", 4, 3)
	lib.addAuthor(1, "Zadie Smith", "b1")
	lib.addAuthor(2, "Ann Leckie", "b2")
	lib.addAuthor(3, "Mary Beard", "b3")
	lib.addAuthor(4, "Iain M. Banks", "b4")
	lib.addAuthor(5, "Neil Gaiman", "b5")
	return &oracleSeed{lib: lib, root: t.TempDir()}
}

func decodeGapsItems(t *testing.T, h *harness, tok, sort string, desc bool, limit, page int) []string {
	t.Helper()
	path := "/api/libraries/" + h.libraryID() + "/items?limit=" + strconv.Itoa(limit) +
		"&page=" + strconv.Itoa(page) + "&sort=" + sort
	if desc {
		path += "&desc=1"
	}
	code, body := h.doAny(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, code)
	}
	m := body.(map[string]any)
	if total, _ := m["total"].(float64); int(total) != 5 {
		t.Fatalf("GET %s total = %v, want 5", path, m["total"])
	}
	return absItemTitles(m)
}

// progressRow renders a progress entry the way the real provider does, keyed by
// the book's sync id.
func decodeGapsProgress(t *testing.T, seed *oracleSeed, bookID string, lastUpdate, startedAt int64, finishedAt *int64) any {
	t.Helper()
	sid := absplSyncIDFor(t, seed, bookID)
	return map[string]any{
		"id": "u1-" + sid, "libraryItemId": sid, "mediaItemId": sid, "mediaItemType": "book",
		"userId": "u1", "duration": 100.0, "progress": 0.5, "currentTime": 50.0,
		"isFinished": finishedAt != nil, "hideFromContinueListening": false,
		"ebookLocation": nil, "ebookProgress": 0, "episodeId": nil,
		"lastUpdate": lastUpdate, "startedAt": startedAt, "finishedAt": finishedAt,
	}
}

func TestLibraryItems_ClientSortsAreOrdered(t *testing.T) {
	seed := decodeGapsBookSeed(t)
	i64 := func(v int64) *int64 { return &v }
	ud := &fakeUserData{}
	ud.progress = []any{
		decodeGapsProgress(t, seed, "b3", 300, 10, nil),
		decodeGapsProgress(t, seed, "b1", 100, 30, i64(500)),
		decodeGapsProgress(t, seed, "b5", 200, 20, i64(400)),
	}
	h, tok := decodeGapsLogin(t, seed, ud)

	cases := []struct {
		name string
		sort string
		desc bool
		want []string
	}{
		{"birthtime", "birthtimeMs", false, []string{"Bravo", "Delta", "Charlie", "Echo", "Alpha"}},
		{"mtime", "mtimeMs", false, []string{"Alpha", "Charlie", "Echo", "Delta", "Bravo"}},
		{"author last-first", "media.metadata.authorNameLF", false, []string{"Delta", "Charlie", "Echo", "Bravo", "Alpha"}},
		{"author last-first desc", "media.metadata.authorNameLF", true, []string{"Alpha", "Bravo", "Echo", "Charlie", "Delta"}},
		// Books without progress trail in title order whichever the direction.
		{"progress", "progress", false, []string{"Alpha", "Echo", "Charlie", "Bravo", "Delta"}},
		{"progress desc", "progress", true, []string{"Charlie", "Echo", "Alpha", "Bravo", "Delta"}},
		{"progress created", "progress.createdAt", false, []string{"Charlie", "Echo", "Alpha", "Bravo", "Delta"}},
		{"progress finished", "progress.finishedAt", false, []string{"Echo", "Alpha", "Bravo", "Charlie", "Delta"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeGapsItems(t, h, tok, tc.sort, tc.desc, 10, 0); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sort=%s desc=%v = %v, want %v", tc.sort, tc.desc, got, tc.want)
			}
			// The page boundary: page 1 of size 2 must be the 3rd and 4th of the
			// SET-WIDE order. Sorting only the fetched page returns other books.
			if got := decodeGapsItems(t, h, tok, tc.sort, tc.desc, 2, 1); !reflect.DeepEqual(got, tc.want[2:4]) {
				t.Fatalf("sort=%s desc=%v page 1 = %v, want %v", tc.sort, tc.desc, got, tc.want[2:4])
			}
		})
	}
}

func TestLibraryItems_RandomSortIsAPermutationThatVaries(t *testing.T) {
	seed := decodeGapsBookSeed(t)
	h, tok := decodeGapsLogin(t, seed, &fakeUserData{})
	seen := map[string]bool{}
	for range 25 {
		got := decodeGapsItems(t, h, tok, "random", false, 10, 0)
		if len(got) != 5 {
			t.Fatalf("random returned %d items, want 5", len(got))
		}
		set := map[string]bool{}
		for _, title := range got {
			set[title] = true
		}
		if len(set) != 5 {
			t.Fatalf("random returned duplicates: %v", got)
		}
		seen[strconv.Quote(got[0]+got[1]+got[2]+got[3]+got[4])] = true
	}
	if len(seen) < 2 {
		t.Fatalf("25 random-sorted requests all returned the same order")
	}
}

// ── #4: series sorts ────────────────────────────────────────────────────────

func decodeGapsSeriesSeed(t *testing.T) *oracleSeed {
	t.Helper()
	lib := newFakeLibrary()
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	intp := func(i int) *int { return &i }
	base := time.UnixMilli(1785370000000)
	at := func(n int) *time.Time { ts := base.Add(time.Duration(n) * time.Minute); return &ts }
	lib.series[1] = &database.Series{ID: 1, Name: "Aardvark Saga"}
	lib.series[2] = &database.Series{ID: 2, Name: "Badger Chronicles"}
	lib.series[3] = &database.Series{ID: 3, Name: "Cobra Cycle"}
	add := func(id string, series, seq, dur, created, updated int) {
		lib.addBook(&database.Book{
			ID: id, Title: id, SeriesID: intp(series), SeriesSequence: intp(seq),
			Duration: intp(dur), CreatedAt: at(created), UpdatedAt: at(updated),
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		}, nil, nil)
	}
	// numBooks: A1 C2 B3 · totalDuration: B150 A500 C1000
	// addedAt(first book): B10 C20 A30 · lastBookAdded: C25 A30 B50
	// lastBookUpdated: A60 C90 B95
	add("a1", 1, 1, 500, 30, 60)
	add("b1", 2, 1, 50, 10, 20)
	add("b2", 2, 2, 50, 40, 95)
	add("b3", 2, 3, 50, 50, 30)
	add("c1", 3, 1, 400, 20, 90)
	add("c2", 3, 2, 600, 25, 10)
	return &oracleSeed{lib: lib, root: t.TempDir()}
}

func decodeGapsSeries(t *testing.T, h *harness, tok, sort string, desc bool, limit, page int) []string {
	t.Helper()
	path := "/api/libraries/" + h.libraryID() + "/series?limit=" + strconv.Itoa(limit) +
		"&page=" + strconv.Itoa(page) + "&sort=" + sort
	if desc {
		path += "&desc=1"
	}
	code, body := h.doAny(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, code)
	}
	results, _ := body.(map[string]any)["results"].([]any)
	out := make([]string, 0, len(results))
	for _, r := range results {
		name, _ := r.(map[string]any)["name"].(string)
		out = append(out, name)
	}
	return out
}

func TestLibrarySeries_ClientSortsAreOrdered(t *testing.T) {
	seed := decodeGapsSeriesSeed(t)
	h, tok := decodeGapsLogin(t, seed, &fakeUserData{})
	const a, b, c = "Aardvark Saga", "Badger Chronicles", "Cobra Cycle"
	cases := []struct {
		sort string
		desc bool
		want []string
	}{
		{"name", false, []string{a, b, c}},
		{"numBooks", false, []string{a, c, b}},
		{"numBooks", true, []string{b, c, a}},
		{"totalDuration", false, []string{b, a, c}},
		{"addedAt", false, []string{b, c, a}},
		{"lastBookAdded", false, []string{c, a, b}},
		{"lastBookUpdated", false, []string{a, c, b}},
		{"lastBookUpdated", true, []string{b, c, a}},
	}
	for _, tc := range cases {
		t.Run(tc.sort+"/desc="+strconv.FormatBool(tc.desc), func(t *testing.T) {
			if got := decodeGapsSeries(t, h, tok, tc.sort, tc.desc, 10, 0); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("series sort=%s desc=%v = %v, want %v", tc.sort, tc.desc, got, tc.want)
			}
			if got := decodeGapsSeries(t, h, tok, tc.sort, tc.desc, 1, 1); !reflect.DeepEqual(got, tc.want[1:2]) {
				t.Fatalf("series sort=%s desc=%v page 1 = %v, want %v", tc.sort, tc.desc, got, tc.want[1:2])
			}
		})
	}

	seen := map[string]bool{}
	for range 25 {
		got := decodeGapsSeries(t, h, tok, "random", false, 10, 0)
		if len(got) != 3 {
			t.Fatalf("random series returned %v, want all 3", got)
		}
		seen[got[0]+"|"+got[1]+"|"+got[2]] = true
	}
	if len(seen) < 2 {
		t.Fatalf("25 random-sorted series requests all returned the same order")
	}
}

// The ?filter= drill-down is a separate path with its own sort handling. It
// must order handler-side keys too: authorNameLF left absSortFields, so a
// drill-down that only consulted absSortField would silently lose it.
func TestLibraryItems_FilteredViewHonoursHandlerSorts(t *testing.T) {
	seed := absSeedTwoSeries(t)
	// Group (reading) order is One, Two. First-name order is also One (Amy),
	// Two (Zed); only Last-First order puts Two (Adams) before One (Young).
	seed.lib.addAuthor(901, "Amy Young", "s10-b1")
	seed.lib.addAuthor(902, "Zed Adams", "s10-b2")
	h, tok := decodeGapsLogin(t, seed, fixtureUserData())
	series := absFilterToken("series", "10")

	got := absItemTitles(absItemsSorted(t, h, tok, series, "media.metadata.authorNameLF", false, 1))
	if !reflect.DeepEqual(got, []string{"Odyssey Book Two"}) {
		t.Fatalf("filtered authorNameLF page 0 = %v, want [Odyssey Book Two]", got)
	}
	got = absItemTitles(absItemsSorted(t, h, tok, series, "media.metadata.authorNameLF", true, 1))
	if !reflect.DeepEqual(got, []string{"Odyssey Book One"}) {
		t.Fatalf("filtered authorNameLF desc page 0 = %v, want [Odyssey Book One]", got)
	}
	// The cached group order must survive a handler sort: sorting the shared
	// id slice in place would reorder every later unsorted request.
	got = absItemTitles(absItemsSorted(t, h, tok, series, "", false, 50))
	if !reflect.DeepEqual(got, []string{"Odyssey Book One", "Odyssey Book Two"}) {
		t.Fatalf("unsorted series view after a sorted one = %v, want reading order", got)
	}
}
