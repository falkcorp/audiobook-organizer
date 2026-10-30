// file: internal/server/handlers/dedup/search_parity_test.go
// version: 1.0.0
// guid: ef2c6990-c652-43b3-b12e-5a7ebc5309cb
// last-edited: 2026-10-30

// Parity and cost tests for resolveBookIDsMatching's move from
// GetAllBooksCore(0, 0) + GetAllAuthors to the store-side ListBookSearchRows
// projection. The parity test holds the new resolver to the OLD one over a real
// store; the benchmark puts numbers on the allocation difference.

package deduphandler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// legacyBookIDsMatching is resolveBookIDsMatching exactly as it was before the
// projection (GetAllAuthors, then GetAllBooksCore(0, 0), matching on the
// lower-cased title, path and author name). It is kept here as the oracle the
// parity test and the benchmark compare against, and for nothing else.
func legacyBookIDsMatching(store interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllAuthors() ([]database.Author, error)
}, needle string) (map[string]struct{}, error) {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return nil, nil
	}
	authorNames := map[int]string{}
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, err
	}
	for _, a := range authors {
		authorNames[a.ID] = strings.ToLower(a.Name)
	}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, err
	}
	matched := make(map[string]struct{})
	for _, b := range books {
		if strings.Contains(strings.ToLower(b.Title), needle) ||
			strings.Contains(strings.ToLower(b.FilePath), needle) {
			matched[b.ID] = struct{}{}
			continue
		}
		if b.AuthorID != nil {
			if name, ok := authorNames[*b.AuthorID]; ok && strings.Contains(name, needle) {
				matched[b.ID] = struct{}{}
			}
		}
	}
	return matched, nil
}

var (
	parityStorylines = []string{
		"Year One", "The Long Halloween", "Dark Victory", "Hush", "The Killing Joke",
		"Knightfall", "No Man's Land", "A Death in the Family", "The Dark Knight Returns",
		"Court of Owls", "Zero Year", "Death of the Family", "Endgame", "Gotham by Gaslight",
		"Batman: Año Uno",
	}
	parityAuthors = []string{
		"Bruce WAYNE", "Dick Grayson", "Barbara Gordon", "Alfred Pennyworth", "Selina Kyle",
		"Harvey Dent", "Jim Gordon", "Lucius Fox", "Oswald Cobblepot", "Edward Nygma",
		"Pamela Isley", "Harleen Quinzel", "Jonathan Crane", "Victor Zsasz", "Talia al Ghul",
		"Ra's al Ghul", "Kate Kane", "Jason Todd", "Tim Drake", "Damian Wayne",
	}
)

// newBatmanSearchStore builds a real in-memory PebbleStore holding 'books'
// books spread over the storylines and authors. Every shape the projection must
// agree on is present: a book with no author, a book whose AuthorID points at an
// author row that does not exist, and soft-deleted books. It returns the store
// and the number of live books.
func newBatmanSearchStore(tb testing.TB, books int) (*database.PebbleStore, int) {
	tb.Helper()
	store, err := database.NewPebbleStoreInMemory(tb.TempDir())
	if err != nil {
		tb.Fatalf("open store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()

	authorIDs := make([]int, len(parityAuthors))
	for i, name := range parityAuthors {
		a, err := store.CreateAuthor(name)
		if err != nil {
			tb.Fatalf("CreateAuthor(%q): %v", name, err)
		}
		authorIDs[i] = a.ID
	}

	live := 0
	for i := 0; i < books; i++ {
		title := fmt.Sprintf("%s Vol. %d", parityStorylines[i%len(parityStorylines)], i/len(parityStorylines)+1)
		b := &database.Book{
			Title:    title,
			FilePath: fmt.Sprintf("/library/%s/%s.m4b", parityAuthors[i%len(parityAuthors)], strings.ReplaceAll(title, " ", "_")),
		}
		switch {
		case i%17 == 5:
			// no author at all
		case i%19 == 7:
			dangling := 90000 + i // no such author row
			b.AuthorID = &dangling
		default:
			id := authorIDs[(i*7)%len(authorIDs)]
			b.AuthorID = &id
		}
		created, err := store.CreateBook(b)
		if err != nil {
			tb.Fatalf("CreateBook: %v", err)
		}
		if i%23 == 11 {
			gone := true
			created.MarkedForDeletion = &gone
			if _, err := store.UpdateBook(created.ID, created); err != nil {
				tb.Fatalf("soft delete: %v", err)
			}
			continue
		}
		live++
	}
	return store, live
}

// TestResolveBookIDsMatchingParityWithLegacyReader holds the projection-backed
// resolver to the old GetAllBooksCore-backed one: identical ID sets for every
// needle, on the memdb path AND the Pebble-scan fallback.
func TestResolveBookIDsMatchingParityWithLegacyReader(t *testing.T) {
	const books = 300
	store, live := newBatmanSearchStore(t, books)

	needles := []string{
		"year one",             // title
		"LONG HALLOWEEN",       // title, upper-case needle
		"vol. 7",               // title suffix
		"no man's",             // title with apostrophe
		"año",                  // non-ASCII title, folded
		"AÑO",                  // same, upper-case needle
		"/library/",            // every path
		"selina_kyle",          // path only (underscored)
		"wayne",                // author, two authors share it
		"BRUCE",                // author, upper-case needle
		"gordon",               // author, two authors
		"al ghul",              // author with spaces
		"ra's",                 // author with apostrophe
		"  zsasz  ",            // needle with surrounding whitespace
		"nygma",                // author reached only via the join
		"isley",                // author, mixed-case stored name
		"m4b",                  // extension, matches every path
		"joker",                // title word, one storyline
		"zzz-no-such-needle",   // nothing
		"quinzel dark victory", // spans fields: must match nothing
	}
	if len(needles) != 20 {
		t.Fatalf("fixture drifted: want 20 needles, have %d", len(needles))
	}

	for _, useMemDB := range []bool{true, false} {
		label := "pebble-scan"
		if useMemDB {
			label = "memdb"
		}
		store.UseMemDB = useMemDB

		// Non-vacuity: the fixture must really hold 'live' books on this path.
		rows, err := store.ListBookSearchRows()
		if err != nil {
			t.Fatalf("%s: ListBookSearchRows: %v", label, err)
		}
		if len(rows) != live {
			t.Fatalf("%s: projection returned %d rows, want %d live books (soft-deleted must be excluded)", label, len(rows), live)
		}

		nonEmpty := 0
		for _, needle := range needles {
			want, err := legacyBookIDsMatching(store, needle)
			if err != nil {
				t.Fatalf("%s %q: legacy: %v", label, needle, err)
			}
			got, err := resolveBookIDsMatching(store, needle)
			if err != nil {
				t.Fatalf("%s %q: new: %v", label, needle, err)
			}
			if len(got) != len(want) {
				t.Errorf("%s %q: got %d ids, legacy %d", label, needle, len(got), len(want))
			}
			for id := range want {
				if _, ok := got[id]; !ok {
					t.Errorf("%s %q: legacy matched %s, projection did not", label, needle, id)
				}
			}
			for id := range got {
				if _, ok := want[id]; !ok {
					t.Errorf("%s %q: projection matched %s, legacy did not", label, needle, id)
				}
			}
			if len(want) > 0 {
				nonEmpty++
			}
		}
		// A parity test where nothing ever matches proves nothing.
		if nonEmpty < 15 {
			t.Fatalf("%s: only %d of 20 needles matched anything; the fixture is not exercising the matcher", label, nonEmpty)
		}
	}
}

// BenchmarkResolveBookIDsMatching puts the allocation difference on record:
// legacy is GetAllBooksCore(0, 0) + GetAllAuthors, projection is
// ListBookSearchRows, both on a 5,000-book memdb-backed store with a needle that
// matches a small fraction (the common case: a user typing a title).
func BenchmarkResolveBookIDsMatching(b *testing.B) {
	store, _ := newBatmanSearchStore(b, 5000)
	const needle = "court of owls vol. 12"

	b.Run("legacy_GetAllBooksCore", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := legacyBookIDsMatching(store, needle); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("projection_ListBookSearchRows", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := resolveBookIDsMatching(store, needle); err != nil {
				b.Fatal(err)
			}
		}
	})
}
