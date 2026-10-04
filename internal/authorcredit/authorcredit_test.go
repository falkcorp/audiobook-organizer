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

func TestResolve_SplitsAndCreatesMissingParts(t *testing.T) {
	st := newStore(t, "J. N. Chaney")
	got, err := Resolve(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, names(got))
	combined, err := st.GetAuthorByName("J.N. Chaney, Jonathan P. Brazee")
	require.NoError(t, err)
	require.Nil(t, combined, "no combined row is created")
}

func TestResolve_ExistingCombinedRowIsNotReusedWhenItSplits(t *testing.T) {
	st := newStore(t, "J.N. Chaney, Jonathan P. Brazee")
	got, err := Resolve(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
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
	// One piece missing still names two people: never created as one row.
	_, err = Resolve(st, "Shirtaloon, Somebody New", PrepareGate)
	require.True(t, errors.Is(err, ErrCombinedCredit), "err %v", err)
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

// The review of #3717 cases, through the real Pebble store (which has the
// title check). None may credit a series, a publisher or a cast credit as an
// author, and none may create a combined record.
func TestResolve_ReviewCases(t *testing.T) {
	st := newStore(t, "Dante King", "Annabelle Hawthorne")
	for _, s := range []string{"Dragon Born", "Star Wars", "Master Class", "Kurtherian Gambit"} {
		_, err := st.CreateSeries(s, nil)
		require.NoError(t, err)
	}
	_, err := st.CreateBook(&database.Book{Title: "Full Dark, No Stars", FilePath: "/l/fdns.m4b", Format: "m4b"})
	require.NoError(t, err)
	ResetTitleCache()
	created := func(n string) bool {
		a, err := st.GetAuthorByName(n)
		require.NoError(t, err)
		return a != nil
	}

	// A bracket is stripped, never split: the series is not an author.
	var got []database.Author
	got, err = Resolve(st, "Dante King (Dragon Born)", PrepareGate)
	require.NoError(t, err)
	require.Len(t, got, 1, "one credit, as before")
	require.False(t, created("Dragon Born"))
	got, err = Resolve(st, "Alphabet Squadron (Star Wars)", PrepareGate)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.False(t, created("Star Wars"))

	// Two people, one with a series tag: the two people, no combined row.
	got, err = Resolve(st, "Annabelle Hawthorne, Virgil Knightley(Master Class)", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Annabelle Hawthorne", "Virgil Knightley"}, names(got))
	require.False(t, created("Master Class"))
	require.False(t, created("Annabelle Hawthorne, Virgil Knightley"))
	require.False(t, created("Annabelle Hawthorne, Virgil Knightley(Master Class)"))

	// A publisher, a cast credit, a series name, or a long list refuses the
	// split, and the whole string is not created either.
	for _, n := range []string{
		"Cassius Lange, LitForge Press, Damien Hanson",
		"Terry Pratchett, Full Cast",
		"Michael Anderle, Kurtherian Gambit",
		"Ray Porter, Kate Reading, Michael Kramer, Tim Gerard Reynolds",
		"Full Dark, No Stars",
		"Arthur Stone, Mikhail Yagupov (translator)",
	} {
		got, err := Resolve(st, n, PrepareGate)
		require.True(t, errors.Is(err, ErrCombinedCredit), "%s: err %v", n, err)
		require.Empty(t, got, n)
		require.False(t, created(n), n)
	}
	for _, n := range []string{"LitForge Press", "Full Cast", "Kurtherian Gambit", "Cassius Lange", "Ray Porter"} {
		require.False(t, created(n), "%s created", n)
	}
}

func TestStripBracketsAndCollective(t *testing.T) {
	require.Equal(t, "Annabelle Hawthorne, Virgil Knightley", StripBrackets("Annabelle Hawthorne, Virgil Knightley(Master Class)"))
	require.Equal(t, "Dante King", StripBrackets("Dante King (Dragon Born)"))
	require.True(t, IsCollectiveCredit("Full Cast"))
	require.True(t, IsCollectiveCredit("various authors"))
	require.False(t, IsCollectiveCredit("Terry Pratchett"))
	require.Nil(t, SplitNames("Dante King (Dragon Born)", PrepareGate), "never the bracket branch")
}
