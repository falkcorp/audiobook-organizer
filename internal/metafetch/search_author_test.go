// file: internal/metafetch/search_author_test.go
// version: 1.2.0
// guid: bf7207f0-35f9-406d-8c06-9010e2079b36
// last-edited: 2026-09-28

package metafetch

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchAuthorHint(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Unknown Author", ""},
		{"  unknown author ", ""},
		{"read by narrator", ""},
		{"Unknown Title", ""},
		{"Narrator", ""},
		{"Unknown", ""},
		{"various authors", ""},
		{"", ""},
		{"Greg Pak", "Greg Pak"},
		{"  Andrzej Sapkowski ", "Andrzej Sapkowski"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, SearchAuthorHint(tc.in), "SearchAuthorHint(%q)", tc.in)
	}
}

// The fetch-cache stamp keys on the author actually sent: a book whose author
// is a placeholder no longer matches a row stamped with that placeholder (so
// the "by Unknown Author" junk is refetched, not replayed), and a real
// author's stamp is exactly what it was.
func TestFetchCacheIdentity(t *testing.T) {
	asin := "B000000001"
	cases := []struct {
		name, author string
		wantSameAsRaw bool
	}{
		{"placeholder author is keyed as no author", "Unknown Author", false},
		{"narrator placeholder is keyed as no author", "read by narrator", false},
		{"real author is unchanged", "Greg Pak", true},
		{"no author is unchanged", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FetchCacheIdentity("Planet Hulk", tc.author, &asin, nil, nil)
			raw := database.MetadataSearchIdentity("Planet Hulk", tc.author, &asin, nil, nil)
			assert.Equal(t, tc.wantSameAsRaw, got == raw)
			if !tc.wantSameAsRaw {
				assert.Equal(t, database.MetadataSearchIdentity("Planet Hulk", "", &asin, nil, nil), got)
			}
		})
	}
}

// authorRecordingSource records every author a provider was asked with.
type authorRecordingSource struct {
	mu      sync.Mutex
	authors []string
	titles  []string
}

func (r *authorRecordingSource) Name() string { return "Recorder" }
func (r *authorRecordingSource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.titles = append(r.titles, title)
	return nil, nil
}
func (r *authorRecordingSource) SearchByTitleAndAuthor(_ context.Context, title, author string) ([]metadata.BookMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.titles = append(r.titles, title)
	r.authors = append(r.authors, author)
	return nil, nil
}

// The provider ladder must never be asked with a placeholder author, whether
// it arrives as the caller's hint or is resolved from the book's AuthorID
// (the refill that would undo a caller-side strip). A real author is still
// sent. The search always asks by title.
func TestSearchMetadataForBook_NeverSendsPlaceholderAuthor(t *testing.T) {
	cases := []struct {
		name       string
		authorRow  string // "" = no AuthorID
		hint       string
		narrator   string // book.Narrator
		wantAuthor string // "" = no title+author call at all
		wantSent   string // author a title+author call carried when wantAuthor is "" (narrator-as-author rung)
	}{
		{name: "placeholder from AuthorID", authorRow: "Unknown Author"},
		{name: "narrator placeholder hint", hint: "read by narrator"},
		{name: "placeholder hint", hint: "Unknown Author"},
		{name: "narrator placeholder hint over placeholder row", authorRow: "Unknown Author", hint: "read by narrator"},
		{name: "real author from AuthorID", authorRow: "Greg Pak", wantAuthor: "Greg Pak"},
		{name: "real hint", hint: "Greg Pak", wantAuthor: "Greg Pak"},
		{name: "placeholder narrator is not sent as an author", authorRow: "Unknown Author", narrator: "read by narrator"},
		{name: "unknown narrator is not sent as an author", narrator: "Unknown Narrator"},
		{name: "real narrator is still tried as an author", authorRow: "Unknown Author", narrator: "Kim Mai Guest", wantSent: "Kim Mai Guest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			b := &database.Book{Title: "Planet Hulk", FilePath: "/library/x/Planet Hulk.m4b", Format: "m4b"}
			if tc.authorRow != "" {
				a, err := store.CreateAuthor(tc.authorRow)
				require.NoError(t, err)
				b.AuthorID = &a.ID
			}
			if tc.narrator != "" {
				b.Narrator = &tc.narrator
			}
			book, err := store.CreateBook(b)
			require.NoError(t, err)

			rec := &authorRecordingSource{}
			svc := NewService(store)
			svc.overrideSources = []metadata.MetadataSource{rec}
			_, err = svc.SearchMetadataForBookWithOptions(book.ID, "Planet Hulk", tc.hint, "", "", SearchOptions{})
			require.NoError(t, err)

			rec.mu.Lock()
			defer rec.mu.Unlock()
			assert.Contains(t, rec.titles, "Planet Hulk", "the title is always searched")
			if tc.wantSent != "" {
				assert.Equal(t, []string{tc.wantSent}, rec.authors, "only the real narrator may be sent as an author")
			} else if tc.wantAuthor == "" {
				assert.Empty(t, rec.authors, "no title+author call may carry a placeholder")
			} else {
				assert.Contains(t, rec.authors, tc.wantAuthor)
			}
			assert.Equal(t, tc.wantAuthor, svc.SearchAuthorFor(book, "Planet Hulk", tc.hint),
				"SearchAuthorFor must report the author the ladder used")
		})
	}
}

// With no usable author the ladder looks the book's own ASIN up directly, and
// the fingerprint names that question; a book with a real author is searched
// (and fingerprinted) exactly as before.
func TestResolveSearchInputs_ASINOnlyWithoutUsableAuthor(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	placeholder, err := store.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	realAuthor, err := store.CreateAuthor("Greg Pak")
	require.NoError(t, err)
	asin := "B00TESTASN"
	svc := NewService(store)

	cases := []struct {
		name     string
		authorID *int
		asin     *string
		wantASIN string
	}{
		{name: "placeholder author with ASIN", authorID: &placeholder.ID, asin: &asin, wantASIN: asin},
		{name: "no author with ASIN", asin: &asin, wantASIN: asin},
		{name: "real author with ASIN", authorID: &realAuthor.ID, asin: &asin},
		{name: "placeholder author without ASIN", authorID: &placeholder.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{ID: "b1", Title: "Planet Hulk", AuthorID: tc.authorID, ASIN: tc.asin}
			in := svc.resolveSearchInputs(book, "Planet Hulk", "", "")
			assert.Equal(t, tc.wantASIN, in.asin)
			noASIN := in
			noASIN.asin = ""
			if tc.wantASIN == "" {
				assert.Equal(t, noASIN.fingerprint(book.Title), in.fingerprint(book.Title))
			} else {
				assert.NotEqual(t, noASIN.fingerprint(book.Title), in.fingerprint(book.Title),
					"a new ASIN question must change the fingerprint")
			}
		})
	}
}

// A batch row hashed with no author hint (what the fetch now writes for a
// placeholder-author book) must still pass the apply gate's identity check
// for that book, and must NOT pass it for a book with a real author.
// CachedQueryMatchesIdentity follows the same rule for a stand-in query.
func TestValidateCachedIdentityForBook_PlaceholderAuthorEmptyHint(t *testing.T) {
	svc := &Service{}
	placeholder := &database.Author{Name: "Unknown Author"}
	realAuthor := &database.Author{Name: "Greg Pak"}
	cases := []struct {
		name      string
		author    *database.Author
		live      []string
		hashTitle string
		hashAuth  string
		query     string // "" = ValidateCachedIdentityForBook; else CachedQueryMatchesIdentity
		wantOK    bool
	}{
		{name: "placeholder snapshot, empty-hint row", author: placeholder, hashTitle: "Unknown Title", wantOK: true},
		{name: "placeholder live author, empty-hint row", author: realAuthor, live: []string{"read by narrator"}, hashTitle: "Unknown Title", wantOK: true},
		{name: "real author, empty-hint row", author: realAuthor, live: []string{"Greg Pak"}, hashTitle: "Unknown Title", wantOK: false},
		{name: "placeholder author, row for another title", author: placeholder, hashTitle: "Something Else", wantOK: false},
		{name: "legacy placeholder-hint row still passes", author: placeholder, hashTitle: "Unknown Title", hashAuth: "Unknown Author", wantOK: true},
		{name: "stand-in query, placeholder author, empty hint", author: placeholder, hashTitle: "Planet Hulk", query: "Planet Hulk", wantOK: true},
		{name: "stand-in query, real author, empty hint", author: realAuthor, live: []string{"Greg Pak"}, hashTitle: "Planet Hulk", query: "Planet Hulk", wantOK: false},
		{name: "stand-in query, row for a different query", author: placeholder, hashTitle: "Planet Hulk", query: "World War Hulk", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{ID: "b1", Title: "Unknown Title", Author: tc.author}
			entry := &MetadataCandidateCache{BookID: "b1", SourceHash: hashSearchInputs("b1", tc.hashTitle, tc.hashAuth, "", "")}
			if tc.query == "" {
				err := svc.ValidateCachedIdentityForBook(entry, book, tc.live)
				assert.Equal(t, tc.wantOK, err == nil, "err=%v", err)
				return
			}
			assert.Equal(t, tc.wantOK, svc.CachedQueryMatchesIdentity(entry, book, tc.live, tc.query))
		})
	}
}
