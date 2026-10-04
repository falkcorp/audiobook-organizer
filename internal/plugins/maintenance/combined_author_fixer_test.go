// file: internal/plugins/maintenance/combined_author_fixer_test.go
// version: 1.0.0
// guid: d8097de1-71d8-4949-8195-fec97df8ae52
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const combinedTestOpID = "op-combined-apply"

// combinedLib is a real Pebble library for the combined-credit fixer.
type combinedLib struct {
	t     *testing.T
	store *database.PebbleStore
	fixer *combinedAuthorFixer
	ids   map[string]string
}

func newCombinedLib(t *testing.T) *combinedLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return &combinedLib{t: t, store: st, ids: map[string]string{},
		fixer: newCombinedAuthorFixer(&Plugin{deps: fakeDeps{store: st}, standDownWait: noWait})}
}

// author returns the id of the author named name, creating it.
func (l *combinedLib) author(name string) int {
	l.t.Helper()
	a, err := l.store.CreateAuthor(name)
	require.NoError(l.t, err)
	return a.ID
}

// credit is one book_authors row: author name, role, position.
type credit struct {
	name, role string
	pos        int
}

// book creates a book whose primary author is primary ("" none), with the
// given credits and one file at path.
func (l *combinedLib) book(key, title, primary, path string, credits ...credit) string {
	l.t.Helper()
	b := &database.Book{Title: title, Format: "m4b", FilePath: path}
	if primary != "" {
		id := l.author(primary)
		b.AuthorID = &id
	}
	created, err := l.store.CreateBook(b)
	require.NoError(l.t, err)
	require.NoError(l.t, l.store.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: path, Format: "m4b"}))
	var cs []database.BookAuthor
	for _, c := range credits {
		cs = append(cs, database.BookAuthor{BookID: created.ID, AuthorID: l.author(c.name), Role: c.role, Position: c.pos})
	}
	if len(cs) > 0 {
		require.NoError(l.t, l.store.SetBookAuthors(created.ID, cs))
	}
	l.ids[key] = created.ID
	return created.ID
}

func (l *combinedLib) plan() (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, nil,
		repairs.PlanDeps{Guard: l.store, Series: repairs.SeriesNamesFrom(series)}, &fakeReporter{})
	require.NoError(l.t, err)
	raw, err := json.Marshal(res)
	require.NoError(l.t, err)
	var stored repairs.PlanResult
	require.NoError(l.t, json.Unmarshal(raw, &stored))
	byKey := map[string]repairs.Row{}
	for key, id := range l.ids {
		for _, r := range stored.Rows {
			if r.RowID == id {
				byKey[key] = r
			}
		}
	}
	return &stored, byKey
}

func (l *combinedLib) apply(plan *repairs.PlanResult, rowIDs []string) *repairs.ApplyResult {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	w := repairs.NewWriter(l.store, l.store, l.fixer.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, combinedTestOpID).WithCredits(l.store).WithFieldStates(l.store)
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, "op-combined-plan", rowIDs, false,
		repairs.ApplyDeps{Guard: l.store, Series: repairs.SeriesNamesFrom(series), Writer: w, OpID: combinedTestOpID}, &fakeReporter{})
	require.NoError(l.t, err)
	return res
}

// credits renders a book's junction as "name@pos/role".
func (l *combinedLib) credits(key string) []string {
	l.t.Helper()
	cs, err := l.store.GetBookAuthors(l.ids[key])
	require.NoError(l.t, err)
	var out []string
	for _, c := range cs {
		a, err := l.store.GetAuthorByID(c.AuthorID)
		require.NoError(l.t, err)
		name := "?"
		if a != nil {
			name = a.Name
		}
		out = append(out, name+"@"+strconv.Itoa(c.Position)+"/"+c.Role)
	}
	return out
}

func (l *combinedLib) primary(key string) string {
	l.t.Helper()
	b, err := l.store.GetBookByID(l.ids[key])
	require.NoError(l.t, err)
	if b.AuthorID == nil {
		return ""
	}
	a, err := l.store.GetAuthorByID(*b.AuthorID)
	require.NoError(l.t, err)
	if a == nil {
		return "?"
	}
	return a.Name
}

const combinedLibRoot = "/lib/Shelf/"

// populate builds one book per decision.
func (l *combinedLib) populate() {
	t := l.t
	for _, n := range []string{"J. N. Chaney", "Jonathan P. Brazee", "Turner Tellborn", "Marcus Sloss", "Amy DuBoff",
		"Michael Anderle", "Ann Leckie", "Shirtaloon", "Travis Deverell", "A Dark", "Drowning Tide", "Kumo Kagyu",
		"Kevin Steinbach - translator", "A. G. Riddle", "Mary-Ann Fox", "Maryann Fox", "Bob Stone",
		"Jonathan Strange", "Mr Norrell", "Greg Bear", "Ben Bova", "David Brin", "Larry Niven"} {
		l.author(n)
	}
	// ---- applicable ----
	// Mission Creep: both parts credited at one position, the combined
	// record after them and as the primary.
	l.book("duplicate", "Mission Creep", "J.N. Chaney, Jonathan P. Brazee", combinedLibRoot+"Mission Creep/a.m4b",
		credit{"J. N. Chaney", "author", 0}, credit{"Jonathan P. Brazee", "author", 0}, credit{"J.N. Chaney, Jonathan P. Brazee", "author", 1})
	l.book("only", "Wraith Knight", "Turner Tellborn, Marcus Sloss", combinedLibRoot+"Wraith Knight/a.m4b",
		credit{"Turner Tellborn, Marcus Sloss", "author", 0}, credit{"Some Narrator", "narrator", 1})
	l.book("partial", "Partial Book", "Amy DuBoff, Michael Anderle", combinedLibRoot+"Partial/a.m4b",
		credit{"Amy DuBoff, Michael Anderle", "author", 0}, credit{"Amy DuBoff", "author", 1})
	l.book("primary-only", "Primary Only", "Turner Tellborn, Marcus Sloss", combinedLibRoot+"Primary Only/a.m4b")
	l.book("new-author", "New Author Book", "Ann Leckie, Zed Newperson", combinedLibRoot+"New/a.m4b",
		credit{"Ann Leckie, Zed Newperson", "author", 0})
	// A primary that is a real author, the combined record a later credit.
	l.book("keep-primary", "Keep Primary", "J. N. Chaney", combinedLibRoot+"Keep/a.m4b",
		credit{"J. N. Chaney", "author", 0}, credit{"J.N. Chaney, Jonathan P. Brazee", "author", 1})
	// The combined record is also credited as narrator: that row stays.
	l.book("narrator-too", "Narrated", "J.N. Chaney, Jonathan P. Brazee", combinedLibRoot+"Narrated/a.m4b",
		credit{"J.N. Chaney, Jonathan P. Brazee", "author", 0}, credit{"J.N. Chaney, Jonathan P. Brazee", "narrator", 1})
	// ---- held ----
	l.book("refused", "He Who Fights", "Shirtaloon, Travis Deverell", combinedLibRoot+"HWFWM/a.m4b",
		credit{"Shirtaloon, Travis Deverell", "author", 0})
	l.book("title-part", "Some Tide", "A Dark and Drowning Tide", combinedLibRoot+"Tide/a.m4b",
		credit{"A Dark and Drowning Tide", "author", 0})
	l.book("title", "Jonathan Strange and Mr Norrell", "Jonathan Strange and Mr Norrell", combinedLibRoot+"JSMN/a.m4b",
		credit{"Jonathan Strange and Mr Norrell", "author", 0})
	l.book("role", "Goblin Slayer", "Kumo Kagyu, Kevin Steinbach - translator", combinedLibRoot+"Goblin/a.m4b",
		credit{"Kumo Kagyu, Kevin Steinbach - translator", "author", 0})
	l.book("doubled", "Genome", "A. G. Riddle, A. G. Riddle", combinedLibRoot+"Genome/a.m4b",
		credit{"A. G. Riddle, A. G. Riddle", "author", 0})
	l.book("anthology", "Far Futures", "Greg Bear, Ben Bova, David Brin, Larry Niven", combinedLibRoot+"Far Futures/a.m4b",
		credit{"Greg Bear, Ben Bova, David Brin, Larry Niven", "author", 0})
	l.book("ambiguous", "Fox Tales", "Mary Ann Fox, Bob Stone", combinedLibRoot+"Fox/a.m4b",
		credit{"Mary Ann Fox, Bob Stone", "author", 0})
	l.book("locked", "Locked Book", "Turner Tellborn, Marcus Sloss", combinedLibRoot+"Locked/a.m4b",
		credit{"Turner Tellborn, Marcus Sloss", "author", 0})
	ov := `"hand typed"`
	require.NoError(t, l.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.ids["locked"],
		Field: database.FieldKeyAuthorName, OverrideLocked: true, OverrideValue: &ov}))
	l.book("itunes", "iTunes Book", "Turner Tellborn, Marcus Sloss", "/mnt/data/books/itunes/T/iTunes Book/01.m4b",
		credit{"Turner Tellborn, Marcus Sloss", "author", 0})
	l.book("dw", "Doctor Who: The Daleks", "Turner Tellborn, Marcus Sloss", combinedLibRoot+"Daleks/a.m4b",
		credit{"Turner Tellborn, Marcus Sloss", "author", 0})
	// A plain book with no combined credit is not listed at all.
	l.book("plain", "Plain", "Ann Leckie", combinedLibRoot+"Plain/a.m4b", credit{"Ann Leckie", "author", 0})
}

func TestCombinedAuthorFixer_PlanDecisions(t *testing.T) {
	l := newCombinedLib(t)
	l.populate()
	plan, rows := l.plan()

	want := map[string]string{
		"duplicate": combinedClassDuplicate, "only": combinedClassOnly, "partial": combinedClassPartial,
		"primary-only": combinedClassOnly, "new-author": combinedClassNewAuthors, "keep-primary": combinedClassPartial,
		"narrator-too": combinedClassOnly,
	}
	for key, class := range want {
		r, ok := rows[key]
		require.True(t, ok, "%s is listed", key)
		require.True(t, r.Applicable(), "%s: %s %s", key, r.Skipped, r.SkipReason)
		require.Equal(t, class, r.Class, key)
	}
	held := map[string]string{
		"refused": combinedSkipSplitRefused, "title-part": combinedSkipImplausiblePart, "title": combinedSkipTitle,
		"role": combinedSkipRole, "doubled": combinedSkipDoubled, "anthology": combinedSkipAnthology,
		"ambiguous": swapSkipAmbiguousAuthor, "locked": junkSkipUserLocked, "itunes": repairs.SkipITunes,
		"dw": repairs.SkipOwnerManual,
	}
	for key, kind := range held {
		r, ok := rows[key]
		require.True(t, ok, "%s is listed", key)
		require.Equal(t, kind, r.Skipped, "%s: %s", key, r.SkipReason)
	}
	_, listed := rows["plain"]
	require.False(t, listed, "a book with no combined credit is not a row")
	require.Equal(t, len(want), plan.Applicable)
	require.Equal(t, "J. N. Chaney @0, Jonathan P. Brazee @1", rows["duplicate"].Proposed["credits"])
	require.Equal(t, "J. N. Chaney", rows["duplicate"].Proposed["primary"])
	require.Contains(t, rows["new-author"].Proposed["credits"], "Zed Newperson (new author)")
}

func TestCombinedAuthorFixer_ApplyAndRevert(t *testing.T) {
	l := newCombinedLib(t)
	l.populate()
	plan, rows := l.plan()
	keys := []string{"duplicate", "only", "partial", "primary-only", "new-author", "keep-primary", "narrator-too", "refused", "itunes"}
	before := map[string][]string{}
	primaryBefore := map[string]string{}
	var sel []string
	for _, k := range keys {
		before[k] = l.credits(k)
		primaryBefore[k] = l.primary(k)
		sel = append(sel, rows[k].RowID)
	}
	out := l.apply(plan, sel)
	require.Equal(t, 7, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, 2, out.ByOutcome[repairs.OutcomeNotApplicable], "held rows are never written")

	require.Equal(t, []string{"J. N. Chaney@0/author", "Jonathan P. Brazee@1/author"}, l.credits("duplicate"))
	require.Equal(t, "J. N. Chaney", l.primary("duplicate"))
	require.Equal(t, []string{"Turner Tellborn@0/author", "Marcus Sloss@1/author", "Some Narrator@2/narrator"}, l.credits("only"))
	require.Equal(t, "Turner Tellborn", l.primary("only"))
	require.Equal(t, []string{"Amy DuBoff@0/author", "Michael Anderle@1/author"}, l.credits("partial"),
		"the combined credit's slot takes every part credited after it, in the combined name's order")
	require.Equal(t, "Amy DuBoff", l.primary("partial"), "the primary is the first author credit")
	require.Equal(t, []string{"Turner Tellborn@0/author", "Marcus Sloss@1/author"}, l.credits("primary-only"))
	require.Equal(t, "Turner Tellborn", l.primary("primary-only"))
	require.Equal(t, []string{"Ann Leckie@0/author", "Zed Newperson@1/author"}, l.credits("new-author"))
	require.Equal(t, []string{"J. N. Chaney@0/author", "Jonathan P. Brazee@1/author"}, l.credits("keep-primary"))
	require.Equal(t, "J. N. Chaney", l.primary("keep-primary"))
	require.Equal(t, []string{"J. N. Chaney@0/author", "Jonathan P. Brazee@1/author", "J.N. Chaney, Jonathan P. Brazee@2/narrator"},
		l.credits("narrator-too"), "a narrator credit is left alone")
	for _, k := range []string{"refused", "itunes"} {
		require.Equal(t, before[k], l.credits(k), "%s untouched", k)
	}
	// No combined record is deleted by the fixer.
	a, err := l.store.GetAuthorByName("Turner Tellborn, Marcus Sloss")
	require.NoError(t, err)
	require.NotNil(t, a)

	// A second apply of the same plan writes nothing twice.
	again := l.apply(plan, sel[:7])
	require.Zero(t, again.Applied)
	require.Equal(t, 7, again.ChangedSincePlan, "outcomes %v", again.ByOutcome)

	// The op revert restores every credit list and primary, and removes the
	// author the apply created.
	rev, err := audiobooks.NewRevertService(l.store).RevertOperation(combinedTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	for _, k := range keys {
		require.Equal(t, before[k], l.credits(k), "credits of %s after revert", k)
		require.Equal(t, primaryBefore[k], l.primary(k), "primary of %s after revert", k)
	}
	z, err := l.store.GetAuthorByName("Zed Newperson")
	require.NoError(t, err)
	require.Nil(t, z, "the created author is removed by the revert")
}

func TestCombinedAuthorFixer_ChangeAfterPlanRefuses(t *testing.T) {
	l := newCombinedLib(t)
	l.populate()
	plan, rows := l.plan()
	// A credit added after the plan: the re-plan's fingerprint moves.
	cur, err := l.store.GetBookAuthors(l.ids["only"])
	require.NoError(t, err)
	cur = append(cur, database.BookAuthor{BookID: l.ids["only"], AuthorID: l.author("Late Addition"), Role: "author", Position: 5})
	require.NoError(t, l.store.SetBookAuthors(l.ids["only"], cur))
	// The author locked after the plan.
	ov := `"typed"`
	require.NoError(t, l.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.ids["duplicate"],
		Field: database.FieldKeyAuthorName, OverrideLocked: true, OverrideValue: &ov}))
	out := l.apply(plan, []string{rows["only"].RowID, rows["duplicate"].RowID})
	require.Zero(t, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, 2, out.ChangedSincePlan, "outcomes %v", out.ByOutcome)
}

func TestCombinedAuthorFixer_StaleEmbeddedSeriesIsHeld(t *testing.T) {
	l := newCombinedLib(t)
	l.populate()
	stale, err := l.store.CreateSeries("Stale Saga", nil)
	require.NoError(t, err)
	_, err = l.store.SeedLegacyBookRowForTest(l.ids["only"], func(b *database.Book) error {
		b.SeriesID = nil
		b.Series = &database.Series{ID: stale.ID, Name: stale.Name}
		return nil
	})
	require.NoError(t, err)
	_, rows := l.plan()
	require.Equal(t, swapSkipRelinkSeriesFirst, rows["only"].Skipped, rows["only"].SkipReason)
	// keep-primary moves no primary, so it is not held.
	require.True(t, rows["keep-primary"].Applicable())
}

func TestCombinedNextCredits(t *testing.T) {
	ba := func(id int, role string, pos int) database.BookAuthor {
		return database.BookAuthor{BookID: "b", AuthorID: id, Role: role, Position: pos}
	}
	ids := func(cs []database.BookAuthor) [][3]any {
		var out [][3]any
		for _, c := range cs {
			out = append(out, [3]any{c.AuthorID, c.Role, c.Position})
		}
		return out
	}
	// Mission Creep, parts stored in the other order at one position: the
	// combined string's order breaks the tie.
	got := combinedNextCredits([]database.BookAuthor{ba(2, "author", 0), ba(1, "author", 0), ba(9, "author", 1)}, "b", []int{9}, [][]int{{1, 2}})
	require.Equal(t, [][3]any{{1, "author", 0}, {2, "author", 1}}, ids(got))
	// Distinct positions are the real order and win over the combined string.
	got = combinedNextCredits([]database.BookAuthor{ba(2, "author", 0), ba(1, "author", 1), ba(9, "author", 2)}, "b", []int{9}, [][]int{{1, 2}})
	require.Equal(t, [][3]any{{2, "author", 0}, {1, "author", 1}}, ids(got))
	// A part credited after the combined credit moves up into its slot.
	got = combinedNextCredits([]database.BookAuthor{ba(9, "author", 0), ba(2, "co-author", 1)}, "b", []int{9}, [][]int{{1, 2}})
	require.Equal(t, [][3]any{{1, "author", 0}, {2, "co-author", 1}}, ids(got))
	// Combined-only, with an unrelated co-author after it and a narrator.
	got = combinedNextCredits([]database.BookAuthor{ba(9, "", 0), ba(5, "co-author", 1), ba(7, "narrator", 2)}, "b", []int{9}, [][]int{{1, 2}})
	require.Equal(t, [][3]any{{1, "author", 0}, {2, "author", 1}, {5, "co-author", 2}, {7, "narrator", 3}}, ids(got))
	// Primary-only (no junction row for the combined record): parts first.
	got = combinedNextCredits([]database.BookAuthor{ba(5, "author", 0)}, "b", []int{9}, [][]int{{1, 2}})
	require.Equal(t, [][3]any{{1, "author", 0}, {2, "author", 1}, {5, "author", 2}}, ids(got))
	// Two combined records sharing a part: it is credited once.
	got = combinedNextCredits([]database.BookAuthor{ba(9, "author", 0), ba(8, "author", 1)}, "b", []int{9, 8}, [][]int{{1, 2}, {2, 3}})
	require.Equal(t, [][3]any{{1, "author", 0}, {2, "author", 1}, {3, "author", 2}}, ids(got))
}

func TestCombinedClassify(t *testing.T) {
	cases := map[string]string{
		"J.N. Chaney, Jonathan P. Brazee":                     "",
		"Adam Lance, Leon West, Adam Lance, Leon West":        "",
		"A. G. Riddle, A. G. Riddle":                          combinedSkipDoubled,
		"Shirtaloon, Travis Deverell":                         combinedSkipSplitRefused,
		"A Dark and Drowning Tide":                            combinedSkipImplausiblePart,
		"Reuben Woolley - translator, Alex Toxic":             combinedSkipRole,
		"Greg Bear, Ben Bova, David Brin, Larry Niven":        combinedSkipAnthology,
		"SPEC -- Drew, Hayes – Villains', Code 02":            combinedSkipSplitRefused,
		"Le Guin, Ursula K.":                                  combinedSkipSplitRefused,
		"Martin Luther King, Jr.":                             combinedSkipSplitRefused,
		"Dante King (Dragon Born)":                            combinedSkipSplitRefused,
		"Annabelle Hawthorne, Virgil Knightley(Master Class)": combinedSkipSplitRefused,
		"Terry Pratchett, Full Cast":                          combinedSkipImplausiblePart,
		"Cassius Lange, LitForge Press, Damien Hanson":        combinedSkipImplausiblePart,
	}
	for name, want := range cases {
		_, skip, why := combinedClassify(name)
		require.Equal(t, want, skip, "%s: %s", name, why)
	}
	names, _, _ := combinedClassify("Adam Lance, Leon West, Adam Lance, Leon West")
	require.Equal(t, []string{"Adam Lance", "Leon West"}, names)
}

// Build 3: once the fixer has moved every credit off a combined record, the
// existing maintenance.purge-empty-authors removes it -- unless a series row
// still names it as its author, which the purge now holds rather than leave
// the series pointing at a deleted id.
func TestCombinedAuthorFixer_PurgeRemovesTheEmptiedRecord(t *testing.T) {
	l := newCombinedLib(t)
	for _, n := range []string{"J. N. Chaney", "Jonathan P. Brazee", "Turner Tellborn", "Marcus Sloss"} {
		l.author(n)
	}
	l.book("duplicate", "Mission Creep", "J.N. Chaney, Jonathan P. Brazee", combinedLibRoot+"Mission Creep/a.m4b",
		credit{"J. N. Chaney", "author", 0}, credit{"Jonathan P. Brazee", "author", 0}, credit{"J.N. Chaney, Jonathan P. Brazee", "author", 1})
	l.book("only", "Wraith Knight", "Turner Tellborn, Marcus Sloss", combinedLibRoot+"Wraith Knight/a.m4b",
		credit{"Turner Tellborn, Marcus Sloss", "author", 0})
	combinedA := l.author("J.N. Chaney, Jonathan P. Brazee")
	combinedB := l.author("Turner Tellborn, Marcus Sloss")
	// The scanner creates a series under the book's primary author.
	_, err := l.store.CreateSeries("Wraith Knight", &combinedB)
	require.NoError(t, err)

	plan, rows := l.plan()
	out := l.apply(plan, []string{rows["duplicate"].RowID, rows["only"].RowID})
	require.Equal(t, 2, out.Applied, "outcomes %v", out.ByOutcome)

	p := &Plugin{deps: fakeDeps{store: l.store}, standDownWait: noWait}
	require.NoError(t, p.runPurgeEmptyAuthors(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}))
	a, err := l.store.GetAuthorByID(combinedA)
	require.NoError(t, err)
	require.Nil(t, a, "the emptied combined record is purged")
	b, err := l.store.GetAuthorByID(combinedB)
	require.NoError(t, err)
	require.NotNil(t, b, "a combined record that still owns a series is held")
	for _, n := range []string{"J. N. Chaney", "Jonathan P. Brazee", "Turner Tellborn", "Marcus Sloss"} {
		got, err := l.store.GetAuthorByName(n)
		require.NoError(t, err)
		require.NotNil(t, got, "%s stays", n)
	}
}

// Two rows creating the same missing author: the first apply creates it, and
// the second row (whose re-plan now finds the author) still applies rather
// than being refused as changed_since_plan, and credits the same row.
func TestCombinedAuthorFixer_SiblingCreateDoesNotRefuseTheNextRow(t *testing.T) {
	l := newCombinedLib(t)
	l.author("Ann Leckie")
	l.book("one", "Book One", "Ann Leckie, Zed Newperson", combinedLibRoot+"One/a.m4b", credit{"Ann Leckie, Zed Newperson", "author", 0})
	l.book("two", "Book Two", "Ann Leckie, Zed Newperson", combinedLibRoot+"Two/a.m4b", credit{"Ann Leckie, Zed Newperson", "author", 0})
	plan, rows := l.plan()
	require.Equal(t, combinedClassNewAuthors, rows["one"].Class)
	out := l.apply(plan, []string{rows["one"].RowID, rows["two"].RowID})
	require.Equal(t, 2, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, l.credits("one"), l.credits("two"))
	all, err := l.store.GetAllAuthors()
	require.NoError(t, err)
	zeds := 0
	for _, a := range all {
		if a.Name == "Zed Newperson" {
			zeds++
		}
	}
	require.Equal(t, 1, zeds, "one author created, not one per row")
}

// The primary moves to the first author credit of the result, so it agrees
// with the organizer, which files a book under its lowest-position author.
// Where the existing credits put the combined name's second part first, that
// author is the primary and the reason says why.
func TestCombinedAuthorFixer_PrimaryIsTheFirstCredit(t *testing.T) {
	l := newCombinedLib(t)
	l.author("J. N. Chaney")
	l.author("Jonathan P. Brazee")
	l.book("order", "Order", "J.N. Chaney, Jonathan P. Brazee", combinedLibRoot+"Order/a.m4b",
		credit{"Jonathan P. Brazee", "author", 0}, credit{"J. N. Chaney", "author", 1}, credit{"J.N. Chaney, Jonathan P. Brazee", "author", 2})
	plan, rows := l.plan()
	r := rows["order"]
	require.True(t, r.Applicable(), r.SkipReason)
	require.Equal(t, "Jonathan P. Brazee", r.Proposed["primary"])
	require.Contains(t, r.Reason, "not \"J. N. Chaney\"")
	out := l.apply(plan, []string{r.RowID})
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, []string{"Jonathan P. Brazee@0/author", "J. N. Chaney@1/author"}, l.credits("order"))
	require.Equal(t, "Jonathan P. Brazee", l.primary("order"))
}

// A series named like the credit's first author does not hold the credit as
// a title.
func TestCombinedAuthorFixer_AuthorNamedSeriesIsNotATitle(t *testing.T) {
	l := newCombinedLib(t)
	l.author("Michael Anderle")
	l.author("Craig Martelle")
	_, err := l.store.CreateSeries("Michael Anderle", nil)
	require.NoError(t, err)
	l.book("series", "Some Book", "Michael Anderle, Craig Martelle", combinedLibRoot+"Some/a.m4b",
		credit{"Michael Anderle, Craig Martelle", "author", 0})
	_, rows := l.plan()
	require.True(t, rows["series"].Applicable(), "%s %s", rows["series"].Skipped, rows["series"].SkipReason)
}
