// file: internal/authorcredit/authorcredit_test.go
// version: 1.0.0
// guid: 4a5f7bee-3d1d-427b-bc5f-baaa4b6fb584
// last-edited: 2026-10-04

package authorcredit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newStore(t *testing.T, names ...string) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	for _, n := range names {
		_, err := st.CreateAuthor(n)
		require.NoError(t, err)
	}
	return st
}

func names(as []database.Author) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Name
	}
	return out
}

func TestLooseParts(t *testing.T) {
	require.Equal(t, []string{"A. P. Gore", "Patricia Jones", "A. P. Gore", "Patricia Jones"},
		LooseParts("A. P. Gore, Patricia Jones, A. P. Gore, Patricia Jones"))
	require.Equal(t, []string{"A Dark", "Drowning Tide"}, LooseParts("A Dark and Drowning Tide"))
	require.Equal(t, []string{"Stephen King"}, LooseParts("Stephen King"))
	require.Equal(t, []string{"A", "B", "C"}, LooseParts("A / B; C & "))
}

func TestSplitNames(t *testing.T) {
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, SplitNames("J.N. Chaney, Jonathan P. Brazee", PrepareGate))
	require.Equal(t, []string{"Adam Lance", "Leon West"}, SplitNames("Adam Lance, Leon West, Adam Lance, Leon West", PrepareGate),
		"a repeated list is its distinct names")
	require.Nil(t, SplitNames("A. G. Riddle, A. G. Riddle", PrepareGate), "one name twice is not a split")
	require.Nil(t, SplitNames("A Dark and Drowning Tide", PrepareGate), "a title is not split")
	require.Nil(t, SplitNames("King, Stephen", PrepareGate), "Last, First is one person")
	require.Nil(t, SplitNames("Stephen King", PrepareGate))
}

// Two existing authors: split and linked, no combined row.
func TestResolve_SplitsIntoExistingAuthors(t *testing.T) {
	st := newStore(t, "J. N. Chaney", "Jonathan P. Brazee")
	got, err := Resolve(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, names(got))
	combined, err := st.GetAuthorByName("J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Nil(t, combined, "no combined row is created")
}

// An existing combined row is not linked when its parts exist.
func TestResolve_ExistingCombinedRowIsNotReusedWhenItSplits(t *testing.T) {
	st := newStore(t, "J.N. Chaney, Jonathan P. Brazee", "J. N. Chaney", "Jonathan P. Brazee")
	got, err := Resolve(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, names(got))
}

// One unknown part: no split, no new part author; the whole string is looked
// up and created exactly as before this package existed.
func TestResolve_UnknownPartFallsBackToTheWholeString(t *testing.T) {
	st := newStore(t, "J. N. Chaney")
	got, err := Resolve(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J.N. Chaney, Jonathan P. Brazee"}, names(got))
	b, err := st.GetAuthorByName("Jonathan P. Brazee")
	require.NoError(t, err)
	require.Nil(t, b, "no author is created from a split at import")
	// And an existing whole row is found again.
	again, err := Resolve(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, got[0].ID, again[0].ID)
}

// A part found through an alias counts as existing.
func TestResolve_PartFoundByAlias(t *testing.T) {
	st := newStore(t, "J. N. Chaney", "Jonathan P. Brazee")
	b, err := st.GetAuthorByName("Jonathan P. Brazee")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(b.ID, "Jonathan Brazee", "pen_name")
	require.NoError(t, err)
	got, err := Resolve(st, "J. N. Chaney, Jonathan Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, names(got))
}

func TestResolve_RefusesAnUnsplittableCombinedOfExistingAuthors(t *testing.T) {
	// The shared splitter refuses a single-word pen name ("Shirtaloon"), so
	// the credit is not split; both pieces exist, so it must not be created.
	st := newStore(t, "Shirtaloon", "Travis Deverell")
	got, err := Resolve(st, "Shirtaloon, Travis Deverell", PrepareGate)
	require.True(t, errors.Is(err, ErrCombinedCredit), "err %v", err)
	require.Empty(t, got)
	a, err := st.GetAuthorByName("Shirtaloon, Travis Deverell")
	require.NoError(t, err)
	require.Nil(t, a)
}

func TestResolve_KeepsTodaysBehaviourWhenNotCombined(t *testing.T) {
	st := newStore(t, "King", "Stephen", "Shirtaloon, Travis Deverell")
	// "Surname, First" is one person even when both words are author rows.
	got, err := Resolve(st, "King, Stephen", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"King, Stephen"}, names(got))
	// An existing whole row the splitter cannot split is still found.
	got, err = Resolve(st, "Shirtaloon, Travis Deverell", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Shirtaloon, Travis Deverell"}, names(got))
	// A single author is looked up or created.
	got, err = Resolve(st, "Zogarth", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Zogarth"}, names(got))
	// One piece missing: created whole, as before.
	got, err = Resolve(st, "Shirtaloon, Somebody New", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Shirtaloon, Somebody New"}, names(got))
}

func TestCredits(t *testing.T) {
	cs := Credits("b1", []database.Author{{ID: 3}, {ID: 9}})
	require.Equal(t, []database.BookAuthor{{BookID: "b1", AuthorID: 3, Role: "author", Position: 0},
		{BookID: "b1", AuthorID: 9, Role: "author", Position: 1}}, cs)
}

func TestAddCredits(t *testing.T) {
	p := 7
	// Empty junction: the primary is kept first.
	next, changed := AddCredits(nil, "b1", &p, []database.Author{{ID: 3}, {ID: 9}})
	require.True(t, changed)
	require.Equal(t, []int{7, 3, 9}, ids(next))
	require.Equal(t, []int{0, 1, 2}, positions(next))
	// Existing rows kept as they are; new ones after the highest position.
	cur := []database.BookAuthor{{BookID: "b1", AuthorID: 3, Role: "author", Position: 0}, {BookID: "b1", AuthorID: 5, Role: "author", Position: 0}}
	next, changed = AddCredits(cur, "b1", &p, []database.Author{{ID: 3}, {ID: 9}})
	require.True(t, changed)
	require.Equal(t, []int{3, 5, 9}, ids(next))
	require.Equal(t, []int{0, 0, 1}, positions(next))
	// Nothing new: unchanged.
	_, changed = AddCredits(cur, "b1", &p, []database.Author{{ID: 5}})
	require.False(t, changed)
}

func TestLooksCombined(t *testing.T) {
	have := map[string]bool{"Shirtaloon": true, "Travis Deverell": true, "King": true, "Stephen": true}
	exists := func(p string) bool { return have[p] }
	require.True(t, LooksCombined("J.N. Chaney, Jonathan P. Brazee", exists), "the splitter splits it")
	require.True(t, LooksCombined("Shirtaloon, Travis Deverell", exists), "every piece is an author")
	require.False(t, LooksCombined("King, Stephen", exists), "Last, First")
	require.False(t, LooksCombined("Shirtaloon, Nobody", exists))
	require.True(t, LooksCombined("Travis Deverell, Shirtaloon", exists), "a two-word left side is not a bare surname")
	require.False(t, LooksCombined("Stephen King", exists))
}

func ids(cs []database.BookAuthor) []int {
	out := make([]int, len(cs))
	for i := range cs {
		out[i] = cs[i].AuthorID
	}
	return out
}

func positions(cs []database.BookAuthor) []int {
	out := make([]int, len(cs))
	for i := range cs {
		out[i] = cs[i].Position
	}
	return out
}

// The review of #3717 cases, through the real Pebble store. Every part that
// would be a NEW author (a series tag, a publisher, a cast credit, a narrator
// list, a missing co-author) refuses the split, so no author is created from
// a split; the credit falls back to the whole string exactly as before.
func TestResolve_ReviewCases(t *testing.T) {
	st := newStore(t, "Dante King", "Annabelle Hawthorne", "Cassius Lange", "Damien Hanson", "Terry Pratchett",
		"Ray Porter", "Kate Reading", "Michael Kramer", "Tim Gerard Reynolds", "Amie Kaufman", "Jay Kristoff")
	for _, s := range []string{"Dragon Born", "Star Wars", "Master Class"} {
		_, err := st.CreateSeries(s, nil)
		require.NoError(t, err)
	}
	// A junk book row titled with a real co-author credit.
	_, err := st.CreateBook(&database.Book{Title: "Amie Kaufman, Jay Kristoff", FilePath: "/l/ak.m4b", Format: "m4b"})
	require.NoError(t, err)
	ResetTitleCache()
	exists := func(n string) bool {
		a, err := st.GetAuthorByName(n)
		require.NoError(t, err)
		return a != nil
	}
	for _, n := range []string{
		"Dante King (Dragon Born)",
		"Alphabet Squadron (Star Wars)",
		"Annabelle Hawthorne, Virgil Knightley(Master Class)",
		"Cassius Lange, LitForge Press, Damien Hanson",
		"Terry Pratchett, Full Cast",
		"Arthur Stone, Mikhail Yagupov (translator)",
	} {
		got, err := Resolve(st, n, PrepareGate)
		require.NoError(t, err, n)
		require.Equal(t, []string{n}, names(got), "%s falls back to the whole string", n)
	}
	for _, n := range []string{"Dragon Born", "Star Wars", "Master Class", "Virgil Knightley", "LitForge Press",
		"Full Cast", "Mikhail Yagupov", "Arthur Stone", "Alphabet Squadron"} {
		require.False(t, exists(n), "%s must not be created from a split", n)
	}
	// Four existing narrators: over the cap, so not split, and as a list of
	// existing authors not created whole either (ErrCombinedCredit).
	_, err = Resolve(st, "Ray Porter, Kate Reading, Michael Kramer, Tim Gerard Reynolds", PrepareGate)
	require.True(t, errors.Is(err, ErrCombinedCredit), "err %v", err)
	// Both people exist: linked, even though a junk book carries the credit
	// as its title.
	got, err := Resolve(st, "Amie Kaufman, Jay Kristoff", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Amie Kaufman", "Jay Kristoff"}, names(got))
	// A series-name part refuses the split even when it is an author row.
	_, err = st.CreateAuthor("Dragon Born")
	require.NoError(t, err)
	got, err = Resolve(st, "Dante King, Dragon Born", PrepareGate)
	require.True(t, errors.Is(err, ErrCombinedCredit), "err %v, got %v", err, names(got))
}

func TestStripBracketsAndCollective(t *testing.T) {
	require.Equal(t, "Annabelle Hawthorne, Virgil Knightley", StripBrackets("Annabelle Hawthorne, Virgil Knightley(Master Class)"))
	require.Equal(t, "Dante King", StripBrackets("Dante King (Dragon Born)"))
	require.True(t, IsCollectiveCredit("Full Cast"))
	require.True(t, IsCollectiveCredit("various authors"))
	require.False(t, IsCollectiveCredit("Terry Pratchett"))
	require.Nil(t, SplitNames("Dante King (Dragon Born)", PrepareGate), "never the bracket branch")
}
