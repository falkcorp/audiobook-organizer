// file: internal/authorcredit/authorcredit_test.go
// version: 1.1.0
// guid: 4a5f7bee-3d1d-427b-bc5f-baaa4b6fb584
// last-edited: 2026-10-04

package authorcredit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
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
	// Four names is past MaxSplitParts, so the credit is not split; every
	// piece exists, so it must not be created either. (Until 2026-10-04 the
	// example was "Shirtaloon, Travis Deverell"; a single-word piece that is
	// an author now splits, TestResolve_SingleWordNameThatExists.)
	st := newStore(t, "Amy Adams", "Ben Brown", "Cat Cole", "Dan Dorn")
	got, err := Resolve(st, "Amy Adams, Ben Brown, Cat Cole, Dan Dorn", PrepareGate)
	require.True(t, errors.Is(err, ErrCombinedCredit), "err %v", err)
	require.Empty(t, got)
	a, err := st.GetAuthorByName("Amy Adams, Ben Brown, Cat Cole, Dan Dorn")
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

// SF1: a name written surname first is one person, never split, even when its
// fragments exist as author rows; an existing whole row is found first.
func TestResolve_SurnameFirstIsOnePerson(t *testing.T) {
	st := newStore(t, "Le Guin, Ursula K.", "Le Guin", "Ursula K.", "Van Vogt", "A. E.", "Van Der Berg", "Jan Willem",
		"Rob J. Hayes", "M.A.")
	got, err := Resolve(st, "Le Guin, Ursula K.", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Le Guin, Ursula K."}, names(got))
	for _, n := range []string{"Van Vogt, A. E.", "Van Der Berg, Jan Willem", "Rob J. Hayes, M.A."} {
		got, err := Resolve(st, n, PrepareGate)
		require.NoError(t, err, n)
		require.Equal(t, []string{n}, names(got), "%s is one person, created whole", n)
	}
	for _, n := range []string{"Le Guin, Ursula K.", "Van Vogt, A. E.", "Van Der Berg, Jan Willem", "Rob J. Hayes, M.A.",
		"Tolkien, J. R. R.", "Martin, George R. R.", "Martin Luther King, Jr.", "King, Stephen"} {
		require.True(t, OnePersonShape(n), n)
	}
	for _, n := range []string{"Travis Deverell, Shirtaloon", "J.N. Chaney, Jonathan P. Brazee", "A, B and C"} {
		require.False(t, OnePersonShape(n), n)
	}
}

// SF2: every piece the same author (a doubled name, a pen name and its alias)
// is that author, not a refusal.
func TestResolve_SameAuthorPiecesResolveToIt(t *testing.T) {
	st := newStore(t, "A. G. Riddle", "J. K. Rowling")
	got, err := Resolve(st, "A. G. Riddle, A. G. Riddle", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"A. G. Riddle"}, names(got))
	jk, err := st.GetAuthorByName("J. K. Rowling")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(jk.ID, "Robert Galbraith", "pen_name")
	require.NoError(t, err)
	got, err = Resolve(st, "Robert Galbraith, J. K. Rowling", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J. K. Rowling"}, names(got))
}

// SF3: suffix and degree pieces are not people, and a non-person piece means
// the string is not a list of authors: none of these is refused.
func TestResolve_SuffixesAndShapesAreNotCombined(t *testing.T) {
	st := newStore(t, "Martin Luther King", "Jr.", "Tolkien", "J. R. R.", "Martin", "George R. R.")
	for _, n := range []string{"Martin Luther King, Jr.", "Tolkien, J. R. R.", "Martin, George R. R."} {
		got, err := Resolve(st, n, PrepareGate)
		require.NoError(t, err, n)
		require.Equal(t, []string{n}, names(got), n)
	}
}

// Lookup never creates.
func TestLookup_NeverCreates(t *testing.T) {
	st := newStore(t, "J. N. Chaney", "Jonathan P. Brazee")
	got, err := Lookup(st, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J. N. Chaney", "Jonathan P. Brazee"}, names(got))
	got, err = Lookup(st, "Somebody Unknown", PrepareGate)
	require.NoError(t, err)
	require.Empty(t, got)
	a, err := st.GetAuthorByName("Somebody Unknown")
	require.NoError(t, err)
	require.Nil(t, a)
}

// NIT2b: an alias lookup failure fails open to the whole-string path.
type aliasFailStore struct{ *database.PebbleStore }

func (aliasFailStore) FindAuthorByAlias(string) (*database.Author, error) {
	return nil, errors.New("alias index down")
}

func TestResolve_AliasFailureFailsOpen(t *testing.T) {
	st := newStore(t, "J. N. Chaney")
	got, err := Resolve(aliasFailStore{st}, "J.N. Chaney, Jonathan P. Brazee", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"J.N. Chaney, Jonathan P. Brazee"}, names(got))
}

// countingStore counts CreateAuthor calls.
type countingStore struct {
	*database.PebbleStore
	creates []string
}

func (c *countingStore) CreateAuthor(name string) (*database.Author, error) {
	c.creates = append(c.creates, name)
	return c.PebbleStore.CreateAuthor(name)
}

func TestResolve_ByPrefixIsStripped(t *testing.T) {
	st := &countingStore{PebbleStore: newStore(t, "Brandon Sanderson", "Shirtaloon")}
	for _, raw := range []string{"By: Brandon Sanderson", "by Brandon Sanderson", "BY:Brandon Sanderson", "By :  Brandon Sanderson"} {
		got, err := Resolve(st, raw, PrepareGate)
		require.NoError(t, err, raw)
		require.Equal(t, []string{"Brandon Sanderson"}, names(got), raw)
	}
	// A byline before an existing single-word author links it.
	got, err := Resolve(st, "By: Shirtaloon", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Shirtaloon"}, names(got))
	// A byline before an unknown single word is never created.
	got, err = Resolve(st, "By: Zork", PrepareGate)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Empty(t, st.creates)
	// A byline before an unknown full name is created WITHOUT the byline.
	got, err = Resolve(st, "By: Newly Seen", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Newly Seen"}, names(got))
	require.Equal(t, []string{"Newly Seen"}, st.creates)
	// "Byron" is a name, not a byline.
	require.Equal(t, "Byron Smith", personname.StripByPrefix("Byron Smith"))
}

func TestGates_StripByPrefix(t *testing.T) {
	for _, g := range []struct {
		name string
		gate Gate
	}{{"prepare", PrepareGate}, {"clean", CleanGate}} {
		got, ok := g.gate("By: Brandon Sanderson")
		require.True(t, ok, g.name)
		require.Equal(t, "Brandon Sanderson", got, g.name)
		_, ok = g.gate("By: Zork")
		require.False(t, ok, g.name+": a byline leaving one bare word is refused")
		_, ok = g.gate("By:")
		require.False(t, ok, g.name)
	}
}

func TestResolve_SingleWordNameThatExists(t *testing.T) {
	st := &countingStore{PebbleStore: newStore(t, "Shirtaloon", "Travis Deverell")}
	got, err := Resolve(st, "Shirtaloon", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Shirtaloon"}, names(got))
	for _, raw := range []string{"Shirtaloon, Travis Deverell", "Travis Deverell & Shirtaloon", "Shirtaloon; Travis Deverell"} {
		got, err = Resolve(st, raw, PrepareGate)
		require.NoError(t, err, raw)
		require.ElementsMatch(t, []string{"Shirtaloon", "Travis Deverell"}, names(got), raw)
	}
	require.Empty(t, st.creates)
}

func TestResolve_SingleWordNameByAlias(t *testing.T) {
	st := newStore(t, "Travis Deverell", "Shirtaloon Writer")
	a, err := st.GetAuthorByName("Shirtaloon Writer")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(a.ID, "Shirtaloon", "pen_name")
	require.NoError(t, err)
	got, err := Resolve(st, "Shirtaloon, Travis Deverell", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Shirtaloon Writer", "Travis Deverell"}, names(got))
}

func TestResolve_UnknownSingleWordStaysRefused(t *testing.T) {
	st := &countingStore{PebbleStore: newStore(t, "Travis Deverell")}
	// Split refused: "Zorkington" is no author, so the credit is not split
	// into a new author; the whole string follows today's path.
	got, err := Resolve(st, "Zorkington, Travis Deverell", PrepareGate)
	require.NoError(t, err)
	require.Equal(t, []string{"Zorkington, Travis Deverell"}, names(got))
	require.Equal(t, []string{"Zorkington, Travis Deverell"}, st.creates)
	got, err = Lookup(st, "Zorkington & Travis Deverell", PrepareGate)
	require.NoError(t, err)
	require.Empty(t, got)
	// "Deverell, Travis" is one person surname first, never two.
	require.Nil(t, SingleWordParts("Deverell, Travis", PrepareGate))
	require.Equal(t, []string{"Shirtaloon", "Travis Deverell"}, SingleWordParts("Shirtaloon, Travis Deverell", PrepareGate))
	require.Nil(t, SingleWordParts("The Wandering Inn, Pirateaba", PrepareGate))
}
