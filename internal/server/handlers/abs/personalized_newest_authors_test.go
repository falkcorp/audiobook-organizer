// file: internal/server/handlers/abs/personalized_newest_authors_test.go
// version: 1.0.1
// guid: beae48ac-f2a3-4e1d-8f13-e9b94078227b
// last-edited: 2026-10-11

package abs_test

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
)

// Until 2026-10-11 the Newest Authors shelf was the first ten authors BY NAME,
// every one stamped "added now". Names led by punctuation or a digit sort first,
// so the shelf on prod was junk like "&#<code>", "(c) <year> …", "<digits> <size>] …" and
// "<n> short <plural noun>", all "added today".
//
// This fixture gives every kind of author that must NOT lead the shelf a name
// that sorts BEFORE the real new authors ("Zed …"), so a name-ordered shelf
// fails it, and puts the new authors' books past row 20 like the book-shelf
// fixtures do.

// authorShelf fetches /personalized and returns the newest-authors shelf's
// entities, or nil when the shelf is absent.
func (f *shelfFixture) authorShelf(t *testing.T) []map[string]any {
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
	for _, raw := range arr {
		shelf, _ := raw.(map[string]any)
		if shelf == nil || shelf["id"] != "newest-authors" {
			continue
		}
		if shelf["type"] != "authors" {
			t.Fatalf("newest-authors type = %v, want authors", shelf["type"])
		}
		var out []map[string]any
		entities, _ := shelf["entities"].([]any)
		for _, e := range entities {
			if a, _ := e.(map[string]any); a != nil {
				out = append(out, a)
			}
		}
		if total, _ := shelf["total"].(float64); int(total) != len(out) {
			t.Fatalf("newest-authors total = %v, entities = %d", shelf["total"], len(out))
		}
		return out
	}
	return nil
}

// TestPersonalized_NewestAuthorsAreTheNewestVisibleAuthors: the shelf is the
// authors who most recently reached the VISIBLE library, newest first, with
// addedAt equal to the date it is ordered by.
func TestPersonalized_NewestAuthorsAreTheNewestVisibleAuthors(t *testing.T) {
	f := newShelfFixtureUndated(t, 1)
	lib := f.lib

	// Junk names on OLD visible books. A name-ordered shelf leads with these.
	lib.addAuthor(1, "&#174", shelfBookID(0))
	lib.addAuthor(2, "(c) 1999 Synthetic Press", shelfBookID(1))
	lib.addAuthor(3, "10 555MB] #SyntheticTag", shelfBookID(2))
	lib.addAuthor(4, "12 short tales", shelfBookID(3))
	// Junk whose only books are hidden (alternate version, unorganized,
	// quarantined) — and those books are the NEWEST rows in the store.
	lib.addAuthor(5, "&#182", "shelf-alt-version")
	lib.addAuthor(6, "(c) 1999 Hidden Writer", "shelf-unorganized")
	lib.addAuthor(7, "#hidden", "shelf-quarantined")
	// Visible, but no visible book is dated: its date is unknown, not new.
	lib.addAuthor(8, "0 Undated Writer", "shelf-undated-000")
	// An old visible book plus a hidden book newer than every visible one: the
	// hidden book's date must not count.
	lib.addAuthor(9, "Aaron Hiddennew", shelfBookID(4), "shelf-quarantined")
	// Established author (an old book) who gained the NEWEST visible book today.
	// They are not a new author: keyed on the newest book they would lead.
	lib.addAuthor(10, "Abel Established", shelfBookID(5), shelfBookID(shelfFixtureVisible-1))
	// Real new authors, one per book on rows 20..31 (newest last).
	for k := 20; k < shelfFixtureVisible; k++ {
		lib.addAuthor(100+k, "Zed Writer "+strconv.Itoa(k), shelfBookID(k))
	}
	// A co-author on the newest book: same date as author 131, and the later-
	// created row (higher id) goes first.
	lib.addAuthor(200, "Zz Coauthor", shelfBookID(shelfFixtureVisible-1))

	got := f.authorShelf(t)

	type want struct {
		id   int
		book int
	}
	wants := []want{{200, 31}, {131, 31}, {130, 30}, {129, 29}, {128, 28}, {127, 27}, {126, 26}, {125, 25}, {124, 24}, {123, 23}}
	var wantIDs, gotIDs []string
	for _, w := range wants {
		wantIDs = append(wantIDs, strconv.Itoa(w.id))
	}
	for _, a := range got {
		id, _ := a["id"].(string)
		gotIDs = append(gotIDs, id)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		names := make([]any, 0, len(got))
		for _, a := range got {
			names = append(names, a["name"])
		}
		t.Fatalf("newest-authors = %v %v\nwant the ten authors newest to the visible library: %v", gotIDs, names, wantIDs)
	}

	// The emitted addedAt is the value the shelf is ordered by: the author's
	// earliest visible book's date added.
	for i, w := range wants {
		addedAt, _ := got[i]["addedAt"].(float64)
		if wantAt := f.at[shelfBookID(w.book)].UnixMilli(); int64(addedAt) != wantAt {
			t.Fatalf("newest-authors[%d] (id %d).addedAt = %v, want %d (book %d's CreatedAt)", i, w.id, addedAt, wantAt, w.book)
		}
	}
}

// TestLibraryAuthors_AddedAtIsTheFirstVisibleBook: /authors emits the same
// addedAt the shelf orders by — the earliest DATED visible book — and 0 for an
// author with no dated visible book, never the time of the request.
func TestLibraryAuthors_AddedAtIsTheFirstVisibleBook(t *testing.T) {
	f := newShelfFixtureUndated(t, 1)
	f.lib.addAuthor(10, "Abel Established", shelfBookID(5), shelfBookID(shelfFixtureVisible-1), "shelf-quarantined")
	f.lib.addAuthor(8, "Undated Writer", "shelf-undated-000")

	code, body := f.h.doAny(t, request{
		method: http.MethodGet, path: "/api/libraries/" + f.h.libraryID() + "/authors",
		headers: bearer(f.tok),
	})
	if code != http.StatusOK {
		t.Fatalf("authors = %d", code)
	}
	m, _ := body.(map[string]any)
	list, _ := m["authors"].([]any)
	want := map[string]int64{"10": f.at[shelfBookID(5)].UnixMilli(), "8": 0}
	seen := 0
	for _, raw := range list {
		a, _ := raw.(map[string]any)
		id, _ := a["id"].(string)
		w, ok := want[id]
		if !ok {
			continue
		}
		seen++
		if addedAt, _ := a["addedAt"].(float64); int64(addedAt) != w {
			t.Fatalf("author %s addedAt = %v, want %d", id, addedAt, w)
		}
	}
	if seen != len(want) {
		t.Fatalf("authors list has %d of the %d seeded authors: %v", seen, len(want), list)
	}
}

// TestPersonalized_NewestAuthorsLeavesOffUndatedAndHiddenOnly: with fewer
// authors than the shelf holds, nothing pads it — an author with no dated
// visible book and an author whose only book is hidden stay off, rather than
// filling the empty slots.
func TestPersonalized_NewestAuthorsLeavesOffUndatedAndHiddenOnly(t *testing.T) {
	f := newShelfFixtureUndated(t, 1)
	f.lib.addAuthor(8, "Undated Writer", "shelf-undated-000")
	f.lib.addAuthor(5, "Hidden Writer", "shelf-unorganized")
	f.lib.addAuthor(1, "Dated Writer", shelfBookID(3))

	got := f.authorShelf(t)
	if len(got) != 1 || got[0]["id"] != "1" {
		t.Fatalf("newest-authors = %v, want only the dated visible author 1", got)
	}
}
