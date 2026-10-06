// file: internal/metafetch/search_query_shapes_test.go
// version: 1.1.0
// guid: f79f3c26-3085-46f4-9b30-058a6200bd75
// last-edited: 2026-10-06

package metafetch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The shapes below are those of the 2026-10-05 no-match census (732 books a
// forced candidate re-fetch left unmatched). Titles and people are made up,
// except public authors named to prove a real credit is kept.

// knowPeople records names with the given authority roles on store.
func knowPeople(t *testing.T, store interface{ SetRaw(string, []byte) error }, roles map[authority.Role]bool, names ...string) {
	t.Helper()
	for _, n := range names {
		require.NoError(t, authority.PutPersonOverride(store, authority.PersonOverride{Name: n, Roles: roles, SetAt: time.Unix(0, 0)}))
	}
}

var asAuthor = map[authority.Role]bool{authority.RoleAuthor: true}

// shapeFixture is a Pebble-backed service whose books and author rows the
// author-vs-title judgement reads.
type shapeFixture struct {
	t     *testing.T
	store *database.PebbleStore
	svc   *Service
	n     int
}

func newShapeFixture(t *testing.T) *shapeFixture {
	t.Helper()
	f := newVerdictFixture(t)
	return &shapeFixture{t: t, store: f.store, svc: f.mfs}
}

// book creates a book credited to author (an author row is created when the
// store accepts the name) at path, plus otherTitles as more books of that
// author row.
func (f *shapeFixture) book(title, author, path string, otherTitles ...string) *database.Book {
	f.t.Helper()
	var authorID *int
	if author != "" {
		if a, err := f.store.CreateAuthor(author); err == nil && a != nil {
			authorID = &a.ID
		}
	}
	mk := func(t, p string) *database.Book {
		f.n++
		if p == "" {
			p = fmt.Sprintf("/srv/example-organizer/b%d/b%d.m4b", f.n, f.n)
		}
		b, err := f.store.CreateBook(&database.Book{Title: t, FilePath: p, Format: "m4b", AuthorID: authorID})
		require.NoError(f.t, err)
		return b
	}
	for _, o := range otherTitles {
		mk(o, "")
	}
	return mk(title, path)
}

// Author text no catalog credits is cleaned where the hint is sent AND
// hashed (SearchAuthorHint).
func TestSearchAuthorHint_CensusShapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"[XYZ]", ""}, // a release group's tag
		{"GraphicAudio [Jane Example]", "Jane Example"},
		{"zzJane Example", "Jane Example"}, // a sort prefix
		{"Some Long Book_10-02", "Some Long Book"},
		{"Some Title_copy1", "Some Title"},
		{"Book 2 (Unabridged) & Some & Example Saga", "Book 2 & Some & Example Saga"},
		{"Fish &amp; Chips", "Fish & Chips"},
		{"Example Magic_", "Example Magic"},
		// Clean names pass through unchanged.
		{"Jane Example", "Jane Example"},
		{"J. Q. Sample", "J. Q. Sample"},
		{"Fish & Chips", "Fish & Chips"},
		{"Unknown Author", ""},
	} {
		assert.Equal(t, tc.want, SearchAuthorHint(tc.in), tc.in)
	}
}

// A credit proven junk is dropped and the title asked alone: a shape no
// person has (a composite with a book number, a genre tagline), or an author
// row whose every other book restates it (a series filed as an author).
func TestResolveSearchInputs_JunkCreditRestatingTheTitleIsDropped(t *testing.T) {
	f := newShapeFixture(t)
	for _, tc := range []struct {
		name, title, query, author, wantTitle string
		others                                []string
	}{
		{name: "composite of the folder title", title: "read by narrator",
			query: "Some Example Saga, Book 2 (Unabridged)", author: "Book 2 (Unabridged) & Some & Example Saga"},
		{name: "series filed as author", title: "Void Example Series 4", author: "Void Example Series",
			others: []string{"Void Example Series 5"}},
		{name: "genre tagline as author", title: "Rise of the Example Paladin, Book One_ A LitRPG Apocalypse",
			author: "A LitRPG Apocalypse", wantTitle: "Rise of the Example Paladin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := f.book(tc.title, tc.author, "", tc.others...)
			q := tc.query
			if q == "" {
				q = tc.title
			}
			title, author := f.svc.SearchQuestion(b, q, SearchAuthorHint(tc.author))
			assert.Empty(t, author, "the title is asked alone")
			if tc.wantTitle != "" {
				assert.Equal(t, tc.wantTitle, title)
			}
		})
	}
}

// A real author whose name the title carries keeps the credit. Measured
// regressions of the first cut of this rule, each a public book: the
// authority lists need not know the author, and the library may hold only
// this one book by them. A suspect credit is still sent; the title is
// asked alone as well (SuspectAuthor), so a junk one cannot hide the book.
func TestResolveSearchInputs_RealAuthorInTheTitleIsKept(t *testing.T) {
	f := newShapeFixture(t)
	for _, tc := range []struct {
		title, author, wantTitle string
		others                   []string
		suspect                  bool
	}{
		{title: "Brandon Sanderson - Mistborn", author: "Brandon Sanderson", wantTitle: "Mistborn"},
		{title: "The Stephen King Collection", author: "Stephen King", suspect: true},
		{title: "Moby: Then It Fell Apart", author: "Moby", suspect: true},
		{title: "The Essential Rumi", author: "Rumi", suspect: true},
		{title: "Sun Tzu: The Art of War", author: "Sun Tzu", suspect: true},
		{title: "Dave Barry Turns 50", author: "Dave Barry", suspect: true},
		{title: "Becoming Michelle Obama", author: "Michelle Obama", suspect: true},
		{title: "Homer", author: "Homer", others: []string{"The Odyssey"}, suspect: true},
		{title: "Tom Clancy's Op-Center", author: "Tom Clancy"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			b := f.book(tc.title, tc.author, "", tc.others...)
			in := f.svc.resolveSearchInputs(b, tc.title, tc.author, "")
			assert.Equal(t, tc.author, in.author, "the author is kept")
			if tc.wantTitle != "" {
				assert.Equal(t, tc.wantTitle, in.title)
			}
			assert.Equal(t, tc.suspect, in.parsed.SuspectAuthor, "suspect")
			if tc.suspect {
				vs := buildQueryVariants(in.parsed, in.literal, in.rawQuery, in.author, "")
				require.GreaterOrEqual(t, len(vs), 2)
				assert.Equal(t, in.author, vs[0].Author)
				assert.Equal(t, "", vs[1].Author, "the title is also asked alone, second")
				assert.False(t, vs[1].personRequired)
			}
		})
	}
}

// A person-shaped credit whose author row's books all restate it is an
// author who names their books, or a series filed as an author: nothing
// tells them apart, so the credit is SUSPECT -- still sent, with the title
// also asked alone.
func TestResolveSearchInputs_PersonShapedRowThatRestatesIsSuspect(t *testing.T) {
	f := newShapeFixture(t)
	for _, tc := range []struct {
		title, author string
		others        []string
	}{
		{"Dave Barry Turns 50", "Dave Barry", []string{"Dave Barry Slept Here", "Dave Barry Does Japan"}},
		{"Rick Steves Italy 2024", "Rick Steves", []string{"Rick Steves France 2024"}},
		{"Void Example 4", "Void Example", []string{"Void Example 5"}},
		{"Example Worlds - 4 - The First Empire", "Example Worlds", []string{"Example Worlds - 5 - The Second Empire"}},
	} {
		t.Run(tc.title, func(t *testing.T) {
			b := f.book(tc.title, tc.author, "", tc.others...)
			in := f.svc.resolveSearchInputs(b, tc.title, tc.author, "")
			assert.Equal(t, tc.author, in.author, "the author is still sent")
			assert.True(t, in.parsed.SuspectAuthor, "the title is also asked alone")
		})
	}
	// A possessive "s'" names the owner too.
	b := f.book("Rick Steves' Italy", "Rick Steves", "")
	in := f.svc.resolveSearchInputs(b, b.Title, "Rick Steves", "")
	assert.Equal(t, "Rick Steves", in.author)
	assert.False(t, in.parsed.SuspectAuthor)
}

// An authority read error never drops an author: it counts as known.
func TestResolveSearchInputs_AuthorityFaultKeepsTheAuthor(t *testing.T) {
	book := &database.Book{ID: "b1", Title: "The Essential Rumi"}
	svc := fanoutHarness(t, book)
	mock, ok := svc.db.(*database.MockStore)
	require.True(t, ok)
	mock.GetRawFunc = func(string) ([]byte, error) { return nil, errors.New("pebble: closed") }
	_, author := svc.SearchQuestion(book, book.Title, "Rumi")
	assert.Equal(t, "Rumi", author)
}

// A dropped credit is replaced only by an ancestor folder the authority
// lists know as an author and not as a narrator. Otherwise: title-only.
func TestResolveSearchInputs_DroppedAuthorFallsBackToAKnownAuthorFolder(t *testing.T) {
	f := newShapeFixture(t)
	knowPeople(t, f.store, asAuthor, "John Sample")
	path := "/srv/example-organizer/John Sample/Example Worlds/Example Worlds - 4 - The First Empire/Example Worlds - 4 - The First Empire.m4b"
	b := f.book("Example Worlds - 4 - The First Empire", "", path)
	title, author := f.svc.SearchQuestion(b, b.Title, "[XYZ]")
	assert.Equal(t, "The First Empire", title)
	assert.Equal(t, "John Sample", author)

	// "[XYZ]" under a narrator's folder: the narrator also has author
	// credits, and the folder is still no author.
	knowPeople(t, f.store, map[authority.Role]bool{authority.RoleAuthor: true, authority.RoleNarrator: true}, "Pat Reader")
	path = "/srv/example-organizer/Pat Reader/Some Summoner/Some Summoner, Vol. 03 - The Army of Things/x.m4b"
	b = f.book("Some Summoner, Vol. 03: The Army of Things", "", path)
	_, author = f.svc.SearchQuestion(b, b.Title, "[XYZ]")
	assert.Empty(t, author, "a narrator's folder is no author")
}

// Title and author swapped: the title is a known author, the author is not,
// AND the author side carries positive junk evidence (a cleaned-away file
// suffix, or a shape no person has). A biography -- a person's name as the
// title, by another person -- is never swapped.
func TestResolveSearchInputs_SwapNeedsJunkEvidence(t *testing.T) {
	f := newShapeFixture(t)
	knowPeople(t, f.store, asAuthor, "Jane Example", "Steve Jobs", "Stephen King", "Agatha Christie", "Mark Twain")

	b := f.book("Jane Example", "Some Long Book_10-02", "/srv/example-organizer/Some Long Book_10-02/Jane Example/Jane Example.mp3")
	title, author := f.svc.SearchQuestion(b, b.Title, SearchAuthorHint("Some Long Book_10-02"))
	assert.Equal(t, "Some Long Book", title)
	assert.Equal(t, "Jane Example", author)

	for _, tc := range []struct{ title, author string }{
		{"Steve Jobs", "Walter Isaacson"},
		{"Stephen King", "George Beahm"},
		{"Agatha Christie", "Laura Thompson"},
		{"Mark Twain", "Ron Chernow"},
	} {
		b := f.book(tc.title, tc.author, "")
		title, author := f.svc.SearchQuestion(b, b.Title, tc.author)
		assert.Equal(t, tc.title, title, "a biography is asked as stored")
		assert.Equal(t, tc.author, author)
	}

	// A credit the hint cleans is no swap evidence by itself, whichever way
	// the caller passes it: raw (the per-book dialog, the bulk fetch) or
	// cleaned (the batch fetch). A copy suffix on a person-shaped name is
	// none either.
	for _, stored := range []string{"zzWalter Isaacson", "Walter Isaacson [XYZ]", "Walter Isaacson_copy1"} {
		b := f.book("Steve Jobs", stored, "")
		for _, passed := range []string{stored, SearchAuthorHint(stored)} {
			title, author := f.svc.SearchQuestion(b, b.Title, passed)
			assert.Equal(t, "Steve Jobs", title, "%q passed as %q", stored, passed)
			assert.Equal(t, "Walter Isaacson", author, "%q passed as %q", stored, passed)
		}
	}
}

// End to end against a catalog shaped like Audible's: books of each census
// shape are found once the question is fixed. Everything here is made up.
func TestSearchFanout_CensusShapesAreFound(t *testing.T) {
	product := func(title, series, pos, author, asin string) metadata.BookMetadata {
		sub := ""
		if series != "" {
			sub = series + ", Book " + pos
		}
		return metadata.BookMetadata{Title: title, Subtitle: sub, Author: author, Series: series, SeriesPosition: pos,
			ASIN: asin, DurationSec: 9 * 3600, CoverURL: "c"}
	}
	catalog := []metadata.BookMetadata{
		product("The First Empire", "Example Worlds", "4", "John Sample", "B0TEST0501"),
		product("Onward", "", "", "Jane Example", "B0TEST0502"),
		product("The Army of Things", "Some Summoner", "3", "Kim Writer", "B0TEST0503"),
		product("Many of Us", "Some Example Saga", "2", "Lee Author", "B0TEST0504"),
		product("Meeting Point", "Example World", "3", "Jane Example", "B0TEST0505"),
		product("Some Long Book", "", "", "Jane Example", "B0TEST0506"),
	}
	for _, tc := range []struct {
		title, query, author, wantASIN string
		known                          []string
		// raw: the caller sends the stored credit as is (the per-book
		// dialog), not the batch's cleaned hint.
		raw bool
	}{
		{title: "Example Worlds - 4 - The First Empire", author: "Example Worlds", wantASIN: "B0TEST0501"},
		{title: "Onward", author: "zzJane Example", wantASIN: "B0TEST0502"},
		{title: "Some Summoner, Vol. 03: The Army of Things", author: "[XYZ]", wantASIN: "B0TEST0503"},
		{title: "read by narrator", query: "Some Example Saga, Book 2 (Unabridged)",
			author: "Book 2 (Unabridged) & Some & Example Saga", wantASIN: "B0TEST0504"},
		{title: "Meeting Point (2017) [Example World 3]", author: "Jane Example", wantASIN: "B0TEST0505"},
		{title: "Jane Example", author: "Some Long Book_10-02", wantASIN: "B0TEST0506", known: []string{"Jane Example"}, raw: true},
	} {
		t.Run(tc.title, func(t *testing.T) {
			query := tc.query
			if query == "" {
				query = tc.title
			}
			audible := audibleCatalog(catalog...)
			book := &database.Book{ID: "b1", Title: tc.title}
			svc := fanoutHarness(t, book, audible)
			knowPeople(t, svc.db, asAuthor, tc.known...)
			hint := SearchAuthorHint(tc.author)
			if tc.raw {
				hint = tc.author
			}
			resp, err := svc.SearchMetadataForBookWithOptions("b1", query, hint, "", "", SearchOptions{})
			require.NoError(t, err)
			require.NotEmpty(t, resp.Results, "the book must be found")
			assert.Equal(t, tc.wantASIN, resp.Results[0].ASIN)
		})
	}
}

// A suspect author changes the questions asked, so it changes the search
// fingerprint: a cached "nothing found" asked without the title-only
// question is re-asked.
func TestResolveSearchInputs_SuspectAuthorChangesFingerprint(t *testing.T) {
	f := newShapeFixture(t)
	b := f.book("The Stephen King Collection", "Stephen King", "")
	in := f.svc.resolveSearchInputs(b, b.Title, "Stephen King", "")
	require.True(t, in.parsed.SuspectAuthor)
	plain := in
	plain.parsed.SuspectAuthor = false
	assert.NotEqual(t, plain.fingerprint(b.Title), in.fingerprint(b.Title))
	assert.True(t, strings.HasPrefix(in.fingerprint(b.Title), FingerprintPrefix))
}
