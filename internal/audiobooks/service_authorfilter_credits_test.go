// file: internal/audiobooks/service_authorfilter_credits_test.go
// version: 1.0.0
// guid: 8725df66-7845-486e-a6be-da257c9b07e1
// last-edited: 2026-10-12

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// The author: filter matches every author-role credit of a book (book_authors
// rows), not only the primary author. These tests run on a real PebbleStore so
// the memdb pushdown path and the credit index are exercised as in
// production. Every name and title is synthetic (public repo).

// creditFixture is a small library built for the credit cases:
//
//	soloBravo:   primary Bravo, credited Bravo
//	coAuthored:  primary Alpha, credits Alpha (author), Bravo (co-author)
//	contributed: primary Alpha, credits Alpha (author), Charlie (translator),
//	             Delta (narrator)
//	alsoNarrates: primary Alpha, credits Alpha (author), Echo (co-author) and
//	             Echo again as narrator
//	noPrimary:   no AuthorID, credits Foxtrot (author), Golf (co-author)
type creditFixture struct {
	ps                                                            *database.PebbleStore
	alpha, bravo, charlie, delta, echo, foxtrot, golf             int
	soloBravo, coAuthored, contributed, alsoNarrates, noPrimaryID string
}

func seedCreditFixture(t *testing.T) creditFixture {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	person := func(name string) int {
		a, err := ps.CreateAuthor(name)
		require.NoError(t, err)
		return a.ID
	}
	fx := creditFixture{ps: ps}
	fx.alpha = person("Synthauthor Alpha")
	fx.bravo = person("Synthauthor Bravo")
	fx.charlie = person("Synthauthor Charlie")
	fx.delta = person("Synthauthor Delta")
	fx.echo = person("Synthauthor Echo")
	fx.foxtrot = person("Synthauthor Foxtrot")
	fx.golf = person("Synthauthor Golf")

	book := func(title string, primary *int, rows ...database.BookAuthor) string {
		b, err := ps.CreateBook(&database.Book{Title: title, FilePath: "/synthetic/" + title + ".m4b", AuthorID: primary})
		require.NoError(t, err)
		if len(rows) > 0 {
			require.NoError(t, ps.SetBookAuthors(b.ID, rows))
		}
		return b.ID
	}
	credit := func(id int, role string, pos int) database.BookAuthor {
		return database.BookAuthor{AuthorID: id, Role: role, Position: pos}
	}
	fx.soloBravo = book("Synthetic Solo", &fx.bravo, credit(fx.bravo, "author", 0))
	fx.coAuthored = book("Synthetic Pair", &fx.alpha,
		credit(fx.alpha, "author", 0), credit(fx.bravo, "co-author", 1))
	fx.contributed = book("Synthetic Contributors", &fx.alpha,
		credit(fx.alpha, "author", 0), credit(fx.charlie, "translator", 1), credit(fx.delta, "narrator", 2))
	fx.alsoNarrates = book("Synthetic Self Read", &fx.alpha,
		credit(fx.alpha, "author", 0), credit(fx.echo, "co-author", 1), credit(fx.echo, "narrator", 2))
	fx.noPrimaryID = book("Synthetic Unattributed", nil,
		credit(fx.foxtrot, "author", 0), credit(fx.golf, "co-author", 1))
	return fx
}

func (fx creditFixture) ids(t *testing.T, ff FieldFilter) []string {
	t.Helper()
	got, err := NewAudiobookService(fx.ps).GetAudiobooks(context.Background(), 1000, 0, "", nil, nil, ListFilters{FieldFilters: []FieldFilter{ff}})
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	return ids
}

// author:<second credit> matches the co-authored book, and still matches the
// book that credits that person as its only author. The count agrees.
func TestAuthorFilter_MatchesSecondCredit(t *testing.T) {
	fx := seedCreditFixture(t)
	ff := FieldFilter{Field: "author", Value: "Synthauthor Bravo"}
	require.ElementsMatch(t, []string{fx.soloBravo, fx.coAuthored}, fx.ids(t, ff))
	count, err := NewAudiobookService(fx.ps).CountAudiobooksFiltered(context.Background(), ListFilters{FieldFilters: []FieldFilter{ff}})
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestAuthorFilter_PrimaryStillMatches(t *testing.T) {
	fx := seedCreditFixture(t)
	require.ElementsMatch(t, []string{fx.coAuthored, fx.contributed, fx.alsoNarrates},
		fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Alpha"}))
}

// -author:<second credit> excludes the co-authored book: a negated filter
// holds only when NO credit matches.
func TestAuthorFilter_NegationRequiresNoCreditMatch(t *testing.T) {
	fx := seedCreditFixture(t)
	ids := fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Bravo", Negated: true})
	require.ElementsMatch(t, []string{fx.contributed, fx.alsoNarrates, fx.noPrimaryID}, ids)
}

// A book with no junction rows behaves exactly as before: its primary author
// (resolved from AuthorID) is the only value.
func TestAuthorFilter_NoCreditsFallsBackToPrimary(t *testing.T) {
	fx := seedCreditFixture(t)
	bare, err := fx.ps.CreateBook(&database.Book{Title: "Synthetic Bare", FilePath: "/synthetic/Synthetic Bare.m4b", AuthorID: &fx.bravo})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{fx.soloBravo, fx.coAuthored, bare.ID},
		fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Bravo"}))
	require.NotContains(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Alpha"}), bare.ID)
}

// author: means author-role credits only. A translator or narrator credited
// on the book is not its author: author:<them> does not match it, and
// -author:<them> keeps it.
func TestAuthorFilter_NonAuthorRolesDoNotMatch(t *testing.T) {
	fx := seedCreditFixture(t)
	require.Empty(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Charlie"}), "translator-only credit")
	require.Empty(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Delta"}), "narrator-only credit")
	require.Contains(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Charlie", Negated: true}), fx.contributed)
}

// A person credited as co-author AND narrator still matches author:. memdb
// keeps one junction row per person and book, and here keeps the narrator
// row (see database TestBookAuthorRoleCredits_AuthorWhoAlsoNarrates), so this
// passes only if that book is answered from Pebble.
func TestAuthorFilter_AuthorWhoAlsoNarratesMatches(t *testing.T) {
	fx := seedCreditFixture(t)
	require.Equal(t, []string{fx.alsoNarrates}, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Echo"}))
}

// A book with no primary AuthorID but two credit rows matches either credit,
// and a negation excludes it when either credit matches.
func TestAuthorFilter_NilPrimaryWithTwoCredits(t *testing.T) {
	fx := seedCreditFixture(t)
	require.Equal(t, []string{fx.noPrimaryID}, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Foxtrot"}))
	require.Equal(t, []string{fx.noPrimaryID}, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Golf"}))
	require.NotContains(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Golf", Negated: true}), fx.noPrimaryID)
	require.NotContains(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Foxtrot", Negated: true}), fx.noPrimaryID)
	require.Contains(t, fx.ids(t, FieldFilter{Field: "author", Value: "Synthauthor Alpha", Negated: true}), fx.noPrimaryID)
}

// A free-text search plus author:<second credit> runs through the search
// post-filter (queryAudiobooksBudgeted), not the memdb pushdown, and must see
// the credits there too.
func TestAuthorFilter_FreeTextSearchMatchesSecondCredit(t *testing.T) {
	fx := seedCreditFixture(t)
	svc := NewAudiobookService(fx.ps)
	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "Synthetic", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "author", Value: "Synthauthor Bravo"}},
	})
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	require.ElementsMatch(t, []string{fx.soloBravo, fx.coAuthored}, ids)
}

// A regex is tried against each name on its own: author:^A B$ must not match
// a book credited to A and B. Names come in credit order with the primary
// first.
func TestAuthorFilter_RegexDoesNotSpanNames(t *testing.T) {
	primary, second := 1, 2
	credits := &creditLookup{
		rows: map[string][]database.BookAuthor{"co": {
			{BookID: "co", AuthorID: second, Role: "co-author", Position: 1},
			{BookID: "co", AuthorID: primary, Role: "author", Position: 0},
		}},
		authorNames: map[int]string{primary: "Synthetic A", second: "Synthetic B"},
	}
	book := database.Book{ID: "co", AuthorID: &primary, Author: &database.Author{Name: "Synthetic A"}}
	match := func(value string) bool {
		cf, err := compileFieldFilter(FieldFilter{Field: "author", Value: value})
		require.NoError(t, err)
		return fieldMatchesCompiled(&book, &cf, nil, credits)
	}
	require.False(t, match("/^Synthetic A Synthetic B$/"), "a regex must not see the names joined")
	require.False(t, match("/^Synthetic A, Synthetic B$/"))
	require.True(t, match("/^Synthetic B$/"), "an anchored regex matches the second credit alone")
	require.True(t, match("/^Synthetic A$/"))
	require.Equal(t, []string{"Synthetic A", "Synthetic B"}, credits.names(&book), "credit order, primary first")
}

// A book whose only credit is its primary author needs no name resolution:
// names returns nil so the matcher uses the primary it already has.
func TestCreditLookup_OnlyPrimaryCreditSkipped(t *testing.T) {
	id := 7
	credits := &creditLookup{
		rows: map[string][]database.BookAuthor{
			"solo": {{BookID: "solo", AuthorID: id, Role: "author"}},
			"dup":  {{BookID: "dup", AuthorID: id, Role: "author"}, {BookID: "dup", AuthorID: id, Role: "co-author", Position: 1}},
		},
		authorNames: map[int]string{id: "Synthetic Seven"},
	}
	require.Nil(t, credits.names(&database.Book{ID: "solo", AuthorID: &id}))
	require.Nil(t, credits.names(&database.Book{ID: "dup", AuthorID: &id}))
	// Without a primary the lone credit is the only author the book has.
	require.Equal(t, []string{"Synthetic Seven"}, credits.names(&database.Book{ID: "solo"}))
	var none *creditLookup
	require.Nil(t, none.names(&database.Book{ID: "solo"}))
}
