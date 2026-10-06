// file: internal/metabatch/search_query_names_test.go
// version: 1.0.0
// guid: 4d230bdc-f5c8-4daf-a1a4-8ae60cd023e0
// last-edited: 2026-10-05

package metabatch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// knownPeople returns an authority keyspace in which each name is a known
// author (one tier-A observation: authority.AuthorEvidenceRule).
func knownPeople(t *testing.T, names ...string) map[string][]byte {
	t.Helper()
	raw := map[string][]byte{}
	for _, n := range names {
		p := authority.Person{Fold: authority.Fold(n), Display: n, Tier: authority.TierA,
			Roles: map[authority.Role]authority.RoleStat{authority.RoleAuthor: {Count: 1, Tier: authority.TierA, ByTier: map[authority.Tier]int{authority.TierA: 1}}}}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		raw[authority.PersonKey(n)] = b
	}
	return raw
}

// A title that is a known person's name -- the organizer filed the author in
// the title field -- is no title: the folder stands in. Made-up names.
func TestResolveCandidateSearchQuery_PersonNameTitleFallsBack(t *testing.T) {
	path := "/library/Mara Quill/The Paper Garden/The Paper Garden.m4b"
	files := fakeBookFiles{files: []database.BookFile{{FilePath: path, Duration: 9 * 3600}}, raw: knownPeople(t, "Mara Quill")}
	for _, title := range []string{"Mara Quill", "mara_quill", "Mara-Quill"} {
		book := database.Book{ID: "b1", Title: title, FilePath: path, Duration: ip(9 * 3600)}
		q := ResolveCandidateSearchQuery(files, &book)
		require.True(t, q.Usable, title)
		assert.Equal(t, "The Paper Garden", q.Title, title)
		assert.Equal(t, SearchQuerySourceFolderTitle, q.Source, title)
	}
}

// Shape alone is not evidence: "Hammer Fall Rising" reads like a name and
// is a title. Without an authority entry it is searched as written, even when
// the book's author field holds the same text (that author is the junk side;
// the search drops it).
func TestResolveCandidateSearchQuery_PersonShapedTitleWithoutEvidenceIsSearched(t *testing.T) {
	path := "/library/Hammer Fall Rising/Hammer Fall Rising - Unknown Author/book.m4b"
	files := fakeBookFiles{files: []database.BookFile{{FilePath: path}}, raw: knownPeople(t, "Mara Quill")}
	book := database.Book{ID: "b1", Title: "Hammer Fall Rising", FilePath: path, Author: &database.Author{Name: "Hammer Fall Rising"}}
	q := ResolveCandidateSearchQuery(files, &book)
	require.True(t, q.Usable)
	assert.Equal(t, "Hammer Fall Rising", q.Title)
	assert.Equal(t, SearchQuerySourceTitle, q.Source)
}

// A filename-shaped title is still the book's own title: it is searched (the
// search side reads its shapes, metafetch.parseSearchTitleWith), not replaced
// by a stand-in.
func TestResolveCandidateSearchQuery_FilenameShapedTitleIsSearched(t *testing.T) {
	for _, title := range []string{"2018 - Glasswake", "Driftworld 24 - The Seventh Lantern - 01", "J.K. Marlow - Freighter for Hire 02 [Fixed]"} {
		path := "/library/Someone/" + title + "/" + title + ".m4b"
		files := fakeBookFiles{files: []database.BookFile{{FilePath: path, Duration: 9 * 3600}}}
		book := database.Book{ID: "b1", Title: title, FilePath: path, Duration: ip(9 * 3600)}
		q := ResolveCandidateSearchQuery(files, &book)
		require.True(t, q.Usable, title)
		assert.Equal(t, title, q.Title)
		assert.Equal(t, SearchQuerySourceTitle, q.Source)
	}
}

// The track-suffix strip never reaches a part row: "X - 01", "X - 02", "X -
// 03" filed as separate rows in one folder are one book's files, and each is
// still refused before any title is read (the 2026-09-30 incident shape).
func TestResolveCandidateSearchQuery_TrackSuffixPartRowsStillSkipped(t *testing.T) {
	dir := "/library/Dorian Vex/Driftworld 24 - The Seventh Lantern"
	self := "Driftworld 24 - The Seventh Lantern - 02.mp3"
	path := dir + "/" + self
	book := database.Book{ID: "self", Title: "Driftworld 24 - The Seventh Lantern - 02", FilePath: path, Duration: ip(20 * 60)}
	files := fakeBookFiles{
		files: []database.BookFile{{FilePath: path, Duration: 20 * 60, FileSize: 20 * 60 * 8000}},
		dir:   siblingRows(dir, self, "Driftworld 24 - The Seventh Lantern - 01.mp3", "Driftworld 24 - The Seventh Lantern - 03.mp3"),
	}
	q := ResolveCandidateSearchQuery(files, &book)
	assert.False(t, q.Usable, "%+v", q)
	assert.Equal(t, SkipKindSiblingPart, q.SkipKind)
}

// The title names the book's own author -> refused. A known person's name as
// the title of a book by someone else is a biography -> searched.
func TestResolveCandidateSearchQuery_PersonNameNeedsTheBooksOwnPerson(t *testing.T) {
	path := "/library/Mara Quill/The Paper Garden/The Paper Garden.m4b"
	files := fakeBookFiles{files: []database.BookFile{{FilePath: path, Duration: 9 * 3600}}, raw: knownPeople(t, "Mara Quill", "Ada Penn")}

	own := database.Book{ID: "b1", Title: "Mara Quill", FilePath: path, Author: &database.Author{Name: "Mara Quill"}}
	q := ResolveCandidateSearchQuery(files, &own)
	require.True(t, q.Usable)
	assert.Equal(t, SearchQuerySourceFolderTitle, q.Source, "the author's own name is no title")

	bio := database.Book{ID: "b2", Title: "Ada Penn", FilePath: "/library/Gene Holt/Ada Penn/Ada Penn.m4b", Author: &database.Author{Name: "Gene Holt"}}
	q = ResolveCandidateSearchQuery(fakeBookFiles{files: []database.BookFile{{FilePath: bio.FilePath}}, raw: files.raw}, &bio)
	require.True(t, q.Usable)
	assert.Equal(t, "Ada Penn", q.Title)
	assert.Equal(t, SearchQuerySourceTitle, q.Source, "a biography titled by its subject is searched")
}
