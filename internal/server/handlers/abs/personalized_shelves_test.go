// file: internal/server/handlers/abs/personalized_shelves_test.go
// version: 1.0.0
// guid: bfebef16-8d78-4765-909a-90ec835b61f4
// last-edited: 2026-10-10

package abs_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// The home shelves used to be cut from ONE window: the first 20 rows of the
// unfiltered summary list in storage order, sorted afterwards. A book added
// today sits at the END of storage order, so it never reached Recently Added,
// and Continue Listening only saw progress on the library's twenty oldest rows.
//
// Every fixture here puts the books that matter PAST row 20, so a shelf built
// from a leading window of the library fails these tests.

const shelfFixtureVisible = 32

type shelfFixture struct {
	h    *harness
	lib  *fakeLibrary
	tok  string
	sync map[string]string // book id -> sync id
	at   map[string]time.Time
}

func shelfBookID(i int) string { return fmt.Sprintf("shelf-bk-%02d", i) }

// newShelfFixture seeds shelfFixtureVisible visible books whose date added
// rises with storage order (so the newest are the LAST rows), followed by three
// books that are newer still but must never appear: a non-primary version, an
// unorganized import, and a quarantined file.
func newShelfFixture(t *testing.T) *shelfFixture {
	t.Helper()
	lib := newFakeLibrary()
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	intp := func(i int) *int { return &i }
	base := time.UnixMilli(1785000000000)

	f := &shelfFixture{lib: lib, sync: map[string]string{}, at: map[string]time.Time{}}
	add := func(id string, at time.Time, mutate func(*database.Book)) {
		created := at
		b := &database.Book{
			ID: id, Title: "Synthetic " + id,
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
			CreatedAt: &created, UpdatedAt: &created, Duration: intp(600),
		}
		if mutate != nil {
			mutate(b)
		}
		lib.addBook(b, []database.BookFile{{
			ID: id + "-f", BookID: id, FilePath: "/nonexistent/" + id + ".m4b",
			FileSize: 1000, Duration: 600, Format: "m4b",
		}}, nil)
		f.at[id] = created
	}
	for i := range shelfFixtureVisible {
		add(shelfBookID(i), base.Add(time.Duration(i)*time.Hour), nil)
	}
	newest := base.Add(time.Duration(shelfFixtureVisible+10) * time.Hour)
	add("shelf-alt-version", newest, func(b *database.Book) { b.IsPrimaryVersion = boolp(false) })
	add("shelf-unorganized", newest.Add(time.Minute), func(b *database.Book) { b.LibraryState = strp("imported") })
	add("shelf-quarantined", newest.Add(2*time.Minute), func(b *database.Book) {
		q := newest
		b.QuarantinedAt = &q
	})

	provider, err := abshandler.NewUserData(abshandler.UserDataOptions{
		Progress: lib, Bookmarks: newFakeBookmarks(), Identity: lib, Library: lib, AliasUses: lib,
		AliasSeedCutoff: testAliasSeedCutoff,
	})
	if err != nil {
		t.Fatalf("NewUserData: %v", err)
	}
	seed := &oracleSeed{lib: lib, root: t.TempDir()}
	f.h = newHarness(t, "jwt", nil, withLibrary(seed), withUserData(provider))
	f.h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	f.tok = str(t, userObj(t, f.h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	for id := range f.at {
		s, err := lib.MintOrGetSyncID(id)
		if err != nil {
			t.Fatal(err)
		}
		f.sync[id] = s
	}
	return f
}

// listen stores a started position for the user and stamps the activity time
// the row's lastUpdate is rendered from, so ordering does not depend on two
// writes landing in different milliseconds.
func (f *shelfFixture) listen(t *testing.T, bookID string, at time.Time, mutate func(*database.UserBookState)) {
	t.Helper()
	if err := f.lib.SetUserPosition("u1", bookID, "abs", 100); err != nil {
		t.Fatal(err)
	}
	st := &database.UserBookState{
		UserID: "u1", BookID: bookID, Status: database.UserBookStatusInProgress, LastActivityAt: at,
	}
	if mutate != nil {
		mutate(st)
	}
	if err := f.lib.SetUserBookState(st); err != nil {
		t.Fatal(err)
	}
}

// shelves fetches /personalized and returns each book shelf's entities by shelf id.
func (f *shelfFixture) shelves(t *testing.T) map[string][]map[string]any {
	t.Helper()
	code, body := f.h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + f.h.libraryID() + "/personalized",
		headers: bearer(f.tok),
	})
	if code != http.StatusOK {
		t.Fatalf("personalized = %d", code)
	}
	arr, ok := body.([]any)
	if !ok {
		t.Fatalf("personalized must be a bare array, got %T", body)
	}
	out := map[string][]map[string]any{}
	for _, raw := range arr {
		shelf, _ := raw.(map[string]any)
		if shelf == nil || shelf["type"] != "book" {
			continue
		}
		id, _ := shelf["id"].(string)
		entities, _ := shelf["entities"].([]any)
		for _, e := range entities {
			if item, _ := e.(map[string]any); item != nil {
				out[id] = append(out[id], item)
			}
		}
	}
	return out
}

func entityIDs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		id, _ := it["id"].(string)
		out = append(out, id)
	}
	return out
}

func (f *shelfFixture) syncIDs(bookIDs ...string) []string {
	out := make([]string, 0, len(bookIDs))
	for _, id := range bookIDs {
		out = append(out, f.sync[id])
	}
	return out
}

// TestPersonalized_RecentlyAddedIsTheNewestVisibleBooks is the owner's report:
// a freshly organized book must head Recently Added, however far down storage
// order it sits, and the alternate version / unorganized / quarantined rows that
// are newer still must not displace it.
func TestPersonalized_RecentlyAddedIsTheNewestVisibleBooks(t *testing.T) {
	f := newShelfFixture(t)
	got := f.shelves(t)["recently-added"]

	var want []string
	for i := shelfFixtureVisible - 1; i >= shelfFixtureVisible-10; i-- {
		want = append(want, f.sync[shelfBookID(i)])
	}
	if ids := entityIDs(got); !slices.Equal(ids, want) {
		t.Fatalf("recently-added = %v\nwant the ten newest visible books, newest first: %v", ids, want)
	}

	// The emitted addedAt is the value the shelf is ordered by.
	for i, item := range got {
		bookID := shelfBookID(shelfFixtureVisible - 1 - i)
		addedAt, _ := item["addedAt"].(float64)
		if int64(addedAt) != f.at[bookID].UnixMilli() {
			t.Fatalf("recently-added[%d].addedAt = %v, want %d (the book's CreatedAt)", i, addedAt, f.at[bookID].UnixMilli())
		}
	}
}

// TestPersonalized_HiddenRowsNeverReachAnyShelf: a non-primary version, an
// unorganized import and a quarantined file are not in the ABS library, so no
// shelf may show them — even when they are the newest rows, and even when the
// user has progress on one.
func TestPersonalized_HiddenRowsNeverReachAnyShelf(t *testing.T) {
	f := newShelfFixture(t)
	f.listen(t, "shelf-unorganized", time.UnixMilli(1786000000000), nil)
	hidden := f.syncIDs("shelf-alt-version", "shelf-unorganized", "shelf-quarantined")
	for shelf, items := range f.shelves(t) {
		for _, id := range entityIDs(items) {
			if slices.Contains(hidden, id) {
				t.Fatalf("shelf %q lists %s, which the ABS library does not show", shelf, id)
			}
		}
	}
}

// TestPersonalized_ContinueListeningComesFromStoredProgress: books the user is
// partway through, wherever they sit in the library, most recently listened
// first; finished and user-hidden books stay off; Discover never repeats them.
func TestPersonalized_ContinueListeningComesFromStoredProgress(t *testing.T) {
	f := newShelfFixture(t)
	older, newer := time.UnixMilli(1786000000000), time.UnixMilli(1786000500000)
	f.listen(t, shelfBookID(27), older, nil)
	f.listen(t, shelfBookID(25), newer, nil)
	f.listen(t, shelfBookID(29), newer.Add(time.Minute), func(s *database.UserBookState) {
		s.Status = database.UserBookStatusFinished
	})
	f.listen(t, shelfBookID(30), newer.Add(2*time.Minute), func(s *database.UserBookState) {
		s.HideFromContinueListening = true
	})
	// One inside the old 20-row window too, so the shelf is not merely "the far end".
	f.listen(t, shelfBookID(3), older.Add(-time.Minute), nil)

	shelves := f.shelves(t)
	want := f.syncIDs(shelfBookID(25), shelfBookID(27), shelfBookID(3))
	if ids := entityIDs(shelves["continue-listening"]); !slices.Equal(ids, want) {
		t.Fatalf("continue-listening = %v\nwant in-progress books, most recent first: %v", ids, want)
	}

	discover := entityIDs(shelves["discover"])
	if len(discover) == 0 {
		t.Fatal("discover shelf is missing")
	}
	for _, id := range want {
		if slices.Contains(discover, id) {
			t.Fatalf("discover repeats Continue Listening book %s: %v", id, discover)
		}
	}
}

// TestPersonalized_ContinueListeningFollowsAMergedBook: progress stored under a
// book that was later merged away is shelved as the surviving book — unless the
// survivor has a progress row of its own, which then decides.
func TestPersonalized_ContinueListeningFollowsAMergedBook(t *testing.T) {
	f := newShelfFixture(t)
	f.listen(t, "shelf-alt-version", time.UnixMilli(1786000000000), nil)
	f.lib.mergeLoser(t, "shelf-alt-version", shelfBookID(28))

	if ids := entityIDs(f.shelves(t)["continue-listening"]); !slices.Equal(ids, f.syncIDs(shelfBookID(28))) {
		t.Fatalf("continue-listening = %v, want the merge survivor %s", ids, f.sync[shelfBookID(28)])
	}

	// The survivor's own row says finished: the stale in-progress row must not
	// put it back on the shelf.
	f.listen(t, shelfBookID(28), time.UnixMilli(1786000100000), func(s *database.UserBookState) {
		s.Status = database.UserBookStatusFinished
	})
	if ids := entityIDs(f.shelves(t)["continue-listening"]); len(ids) != 0 {
		t.Fatalf("continue-listening = %v; a stale merge-loser row overrode the survivor's finished row", ids)
	}
}
