// file: internal/plugins/maintenance/combined_author_fixer_test.go
// version: 1.2.0
// guid: d8097de1-71d8-4949-8195-fec97df8ae52
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
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
	// A single-word pen name that is an author already: a review row.
	l.book("single-word", "He Who Fights", "Shirtaloon, Travis Deverell", combinedLibRoot+"HWFWM/a.m4b",
		credit{"Shirtaloon, Travis Deverell", "author", 0})
	// A single-word name no author carries, but a provider credited it to
	// the book on its own: a review row that creates it.
	l.book("single-provider", "The Wandering Inn", "Pirateaba, Travis Deverell", combinedLibRoot+"TWI/a.m4b",
		credit{"Pirateaba, Travis Deverell", "author", 0})
	pv := `"Pirateaba"`
	require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: l.ids["single-provider"],
		Field: database.HistoryFieldAuthor, NewValue: &pv, ChangeType: "fetched", Source: "Audible"}))
	// A byline record: replaced by the name after it, a review row.
	l.book("by-prefix", "Ancillary Justice", "By: Ann Leckie", combinedLibRoot+"AJ/a.m4b",
		credit{"By: Ann Leckie", "author", 0})
	// An unknown single word with no provider credit stays held, even when
	// a provider credited the joined string (that is where the record came
	// from, so it proves nothing).
	l.book("refused", "Zork Book", "Zorkington, Travis Deverell", combinedLibRoot+"Zork/a.m4b",
		credit{"Zorkington, Travis Deverell", "author", 0})
	jv := `"Zorkington, Travis Deverell"`
	require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: l.ids["refused"],
		Field: database.HistoryFieldAuthor, NewValue: &jv, ChangeType: "fetched", Source: "Audible"}))
	// A one-word name a person typed is not a provider credit.
	l.book("refused-manual", "Manual Book", "Quxworth, Travis Deverell", combinedLibRoot+"Qux/a.m4b",
		credit{"Quxworth, Travis Deverell", "author", 0})
	mv := `"Quxworth"`
	require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: l.ids["refused-manual"],
		Field: database.HistoryFieldAuthor, NewValue: &mv, ChangeType: "override", Source: "manual"}))
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
		"narrator-too": combinedClassOnly, "single-word": combinedClassSingleWord,
		"single-provider": combinedClassSingleWord, "by-prefix": combinedClassByPrefix,
	}
	for key, class := range want {
		r, ok := rows[key]
		require.True(t, ok, "%s is listed", key)
		require.True(t, r.Applicable(), "%s: %s %s", key, r.Skipped, r.SkipReason)
		require.Equal(t, class, r.Class, key)
	}
	held := map[string]string{
		"refused": combinedSkipSplitRefused, "refused-manual": combinedSkipSplitRefused, "title-part": combinedSkipImplausiblePart, "title": combinedSkipTitle,
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
	// Owner decision 2026-10-04: byline and single-word rows are review
	// rows, never low risk.
	for _, key := range []string{"single-word", "single-provider", "by-prefix"} {
		require.Equal(t, repairs.RiskReview, rows[key].Risk, key)
	}
	require.Equal(t, "Shirtaloon @0, Travis Deverell @1", rows["single-word"].Proposed["credits"])
	require.Equal(t, "Pirateaba (new author) @0, Travis Deverell @1", rows["single-provider"].Proposed["credits"])
	require.Equal(t, "Ann Leckie @0", rows["by-prefix"].Proposed["credits"])
	require.Equal(t, "Ann Leckie", rows["by-prefix"].Proposed["primary"])
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
		"Shirtaloon, Travis Deverell":                         "",
		"Deverell, Travis":                                    combinedSkipSplitRefused,
		"By: Brandon Sanderson":                               "",
		"By: Zork":                                            "",
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
	c, _, _ := combinedClassify("Adam Lance, Leon West, Adam Lance, Leon West")
	require.Equal(t, []string{"Adam Lance", "Leon West"}, c.names)
	c, _, _ = combinedClassify("Shirtaloon, Travis Deverell")
	require.Equal(t, []string{"Shirtaloon", "Travis Deverell"}, c.names)
	require.Equal(t, []string{"Shirtaloon"}, c.singleWord)
	c, _, _ = combinedClassify("By: Brandon Sanderson")
	require.True(t, c.byLed)
	require.Equal(t, []string{"Brandon Sanderson"}, c.names)
	require.Empty(t, c.singleWord)
	c, _, _ = combinedClassify("By: Zork")
	require.Equal(t, []string{"Zork"}, c.singleWord, "a one-word byline needs evidence")
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

// Owner decision 2026-10-04: the byline and single-word review rows apply
// like any other row once the owner selects them; the held ones never do.
func TestCombinedAuthorFixer_ReviewRowsApply(t *testing.T) {
	l := newCombinedLib(t)
	l.populate()
	plan, rows := l.plan()
	var sel []string
	for _, k := range []string{"single-word", "single-provider", "by-prefix", "refused", "refused-manual"} {
		sel = append(sel, rows[k].RowID)
	}
	out := l.apply(plan, sel)
	require.Equal(t, 3, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, []string{"Shirtaloon@0/author", "Travis Deverell@1/author"}, l.credits("single-word"))
	require.Equal(t, "Shirtaloon", l.primary("single-word"))
	require.Equal(t, []string{"Pirateaba@0/author", "Travis Deverell@1/author"}, l.credits("single-provider"))
	require.Equal(t, []string{"Ann Leckie@0/author"}, l.credits("by-prefix"))
	require.Equal(t, "Ann Leckie", l.primary("by-prefix"))
	require.Equal(t, []string{"Zorkington, Travis Deverell@0/author"}, l.credits("refused"))
	for _, n := range []string{"Zorkington", "Quxworth"} {
		a, err := l.store.GetAuthorByName(n)
		require.NoError(t, err)
		require.Nil(t, a, "%s is never created", n)
	}
}

// Prod dry run 2026-10-04 (op 01M44K51SZRXRHPF8FW03SZKR2): the five proposal
// bugs, one book each.
func TestCombinedAuthorFixer_DryRunProposalBugs(t *testing.T) {
	l := newCombinedLib(t)
	// Created in this order so author ids never happen to sort the answer.
	for _, n := range []string{"Michael Anderle", "P. T. Hylton", "A. F. Harrold", "Allie Piper", "Matt Hicks",
		"Alvin Atwater", "Neil Hellegers", "Shane Purdy", "Brandon Q Morris", "Brandon Q. Morris", "Ashton McLee",
		"Arthur C. Clarke", "Gentry Lee", "Travis Baldree", "Mark Gatiss"} {
		l.author(n)
	}
	// 1. Two records of one book name the same new author: proposed once.
	l.book("dup-new", "The Worlds We Leave Behind", "Levi Pinfold, A.F. Harrold", combinedLibRoot+"Worlds/a.m4b",
		credit{"A.F. Harrold, Levi Pinfold", "author", 0})
	// 1. Two author rows spelled "Brandon Q Morris" / "Brandon Q. Morris":
	// one person, credited once.
	l.book("dup-spelling", "The Clouds of Venus", "", combinedLibRoot+"Venus/a.m4b",
		credit{"Brandon Q Morris", "author", 0}, credit{"Ashton McLee", "author", 0},
		credit{"Brandon Q. Morris", "author", 2}, credit{"Brandon Q. Morris, Ashton McLee", "author", 3})
	// 2. A part that still joins two names is never one new author.
	l.book("slash", "The Brightwood", "Travis Baldree, Sarah Lin/Travis Baldree", combinedLibRoot+"Bright/a.m4b",
		credit{"Travis Baldree, Sarah Lin/Travis Baldree", "author", 0})
	// 3. The credited first name keeps its place.
	l.book("order-primary", "The Lord Ruler", "Alvin Atwater, Matt Hicks, Allie Piper", combinedLibRoot+"Ruler/a.m4b",
		credit{"Alvin Atwater", "author", 0})
	l.book("order-primary2", "Threat from the Deep", "Shane Purdy, Neil Hellegers", combinedLibRoot+"Threat/a.m4b",
		credit{"Shane Purdy", "author", 0})
	// 3. A tie at one position is broken by the primary record's order.
	// Two records (the real Storm Warrior shape): the junction one lists
	// Michael Anderle first, the primary lists P. T. Hylton first.
	l.book("order-tie", "Storm Warrior", "P. T. Hylton, Michael Anderle", combinedLibRoot+"Storm/a.m4b",
		credit{"Michael Anderle", "author", 0}, credit{"P. T. Hylton", "author", 0},
		credit{"Michael Anderle, P. T. Hylton", "author", 1})
	// 5. A one-letter misspelling of a credited author is held.
	l.book("misspelt", "Rama II", "Artur C. Clarke, Gentry Lee", combinedLibRoot+"Rama/a.m4b",
		credit{"Arthur C. Clarke", "author", 0}, credit{"Artur C. Clarke, Gentry Lee", "author", 1})
	// 4. A credit of an author id that no longer exists is dropped.
	dangling := l.book("dangling", "Sherlock", "", combinedLibRoot+"Sherlock/a.m4b",
		credit{"Mark Gatiss", "author", 0}, credit{"Mark Gatiss, Steven Moffat", "author", 1})
	cs, err := l.store.GetBookAuthors(dangling)
	require.NoError(t, err)
	cs = append(cs, database.BookAuthor{BookID: dangling, AuthorID: 987654, Role: "author", Position: 2})
	require.NoError(t, l.store.SetBookAuthors(dangling, cs))

	plan, rows := l.plan()
	require.Equal(t, "Levi Pinfold (new author) @0, A. F. Harrold @1", rows["dup-new"].Proposed["credits"])
	require.Equal(t, "Brandon Q Morris @0, Ashton McLee @1", rows["dup-spelling"].Proposed["credits"])
	require.Contains(t, rows["dup-spelling"].Reason, `the second credit of "Brandon Q. Morris"`)
	require.Equal(t, "Travis Baldree @0, Sarah Lin (new author) @1", rows["slash"].Proposed["credits"])
	require.Equal(t, "Alvin Atwater @0, Matt Hicks @1, Allie Piper @2", rows["order-primary"].Proposed["credits"])
	require.Equal(t, "Alvin Atwater", rows["order-primary"].Proposed["primary"])
	require.Equal(t, "Shane Purdy @0, Neil Hellegers @1", rows["order-primary2"].Proposed["credits"])
	require.Equal(t, "Shane Purdy", rows["order-primary2"].Proposed["primary"])
	require.Equal(t, "P. T. Hylton @0, Michael Anderle @1", rows["order-tie"].Proposed["credits"])
	require.Equal(t, swapSkipAmbiguousAuthor, rows["misspelt"].Skipped, rows["misspelt"].SkipReason)
	require.Contains(t, rows["misspelt"].SkipReason, `"Arthur C. Clarke"`)
	require.Equal(t, "Mark Gatiss @0, Steven Moffat (new author) @1", rows["dangling"].Proposed["credits"])
	require.Contains(t, rows["dangling"].Reason, "author id 987654 (no such author)")

	// Apply writes what the plan shows.
	var sel []string
	for _, k := range []string{"dup-new", "dup-spelling", "order-primary", "order-tie", "dangling"} {
		sel = append(sel, rows[k].RowID)
	}
	out := l.apply(plan, sel)
	require.Equal(t, 5, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, []string{"Levi Pinfold@0/author", "A. F. Harrold@1/author"}, l.credits("dup-new"))
	require.Equal(t, []string{"Brandon Q Morris@0/author", "Ashton McLee@1/author"}, l.credits("dup-spelling"))
	require.Equal(t, []string{"Alvin Atwater@0/author", "Matt Hicks@1/author", "Allie Piper@2/author"}, l.credits("order-primary"))
	require.Equal(t, "Alvin Atwater", l.primary("order-primary"))
	require.Equal(t, []string{"P. T. Hylton@0/author", "Michael Anderle@1/author"}, l.credits("order-tie"))
	require.Equal(t, []string{"Mark Gatiss@0/author", "Steven Moffat@1/author"}, l.credits("dangling"))
}

func TestCombinedProposal_DedupesByPersonKey(t *testing.T) {
	ba := func(id int, pos int) database.BookAuthor {
		return database.BookAuthor{BookID: "b", AuthorID: id, Role: "author", Position: pos}
	}
	keys := map[int]string{1: "jkrowling", 2: "jkrowling", 3: "", 9: "x"} // 2 is an alias of 1; 3 is dangling
	out, dropped := combinedProposal([]database.BookAuthor{ba(1, 0), ba(2, 1), ba(3, 2), ba(4, 3)}, "b", nil, nil, 0,
		func(id int) string {
			if k, ok := keys[id]; ok {
				return k
			}
			return "k" + strconv.Itoa(id)
		})
	require.Equal(t, []int{1, 4}, combinedIDs(out))
	require.Equal(t, []int{0, 1}, combinedPositions(out))
	require.Equal(t, []int{2, 3}, combinedIDs(dropped))
}

func TestEditDistanceAtMostOne(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"arthurcclarke", "arturcclarke", true}, {"abc", "abd", true}, {"abc", "abc", true},
		{"abc", "ab", true}, {"abcd", "abdc", false}, {"jonsmith", "janesmith", false}} {
		require.Equal(t, c.want, editDistanceAtMostOne(c.a, c.b), "%s %s", c.a, c.b)
	}
}

func combinedIDs(cs []database.BookAuthor) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.AuthorID
	}
	return out
}

func combinedPositions(cs []database.BookAuthor) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.Position
	}
	return out
}

// #3729 review B2: a real primary tied at @0 with another part stays first,
// and a proposal that would not keep the primary at position 0 is refused.
func TestCombinedAuthorFixer_RealPrimaryTiedAtZeroStaysFirst(t *testing.T) {
	l := newCombinedLib(t)
	for _, n := range []string{"Jia Shen", "J. N. Chaney", "Other Person", "Amy Writer", "Bob Writer"} {
		l.author(n)
	}
	l.book("tie", "Digital Chimera", "J. N. Chaney", combinedLibRoot+"Chimera/a.m4b",
		credit{"J. N. Chaney", "author", 0}, credit{"Jia Shen", "author", 0}, credit{"Jia Shen, J. N. Chaney", "author", 1})
	// Stored order the other way round: the primary is still sorted first.
	l.book("tie-rev", "Digital Chimera 2", "J. N. Chaney", combinedLibRoot+"Chimera2/a.m4b",
		credit{"Jia Shen", "author", 0}, credit{"J. N. Chaney", "author", 0}, credit{"Jia Shen, J. N. Chaney", "author", 1})
	// The primary is an author the credits do not list first at all.
	l.book("uncredited-primary", "Elsewhere", "Other Person", combinedLibRoot+"Else/a.m4b",
		credit{"Amy Writer, Bob Writer", "author", 0})
	plan, rows := l.plan()
	require.Equal(t, "J. N. Chaney @0, Jia Shen @1", rows["tie"].Proposed["credits"])
	require.Equal(t, "J. N. Chaney @0, Jia Shen @1", rows["tie-rev"].Proposed["credits"])
	require.Equal(t, combinedSkipPrimaryOrder, rows["uncredited-primary"].Skipped, rows["uncredited-primary"].SkipReason)
	out := l.apply(plan, []string{rows["tie"].RowID, rows["tie-rev"].RowID})
	require.Equal(t, 2, out.Applied, "outcomes %v", out.ByOutcome)
	for _, k := range []string{"tie", "tie-rev"} {
		require.Equal(t, []string{"J. N. Chaney@0/author", "Jia Shen@1/author"}, l.credits(k), k)
		require.Equal(t, "J. N. Chaney", l.primary(k), k)
	}
}

// combinedAuthorityStore is a Pebble store offering the authority lists
// (authorcredit.AuthoritySource) with a lookup the test swaps.
type combinedAuthorityStore struct {
	*database.PebbleStore
	mu     sync.Mutex
	lookup authority.Lookup
}

func (s *combinedAuthorityStore) AuthorityLookup() authority.Lookup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookup
}

// The authority lines are display only: a part apply would create says how
// the lists grade it (or that they hold no entry), and a re-plan that no
// longer has the lists (flag turned off, reload failed) omits the lines
// without changing the fingerprint, so the row still applies.
func TestCombinedAuthorFixer_AuthorityLinesAreDisplayOnly(t *testing.T) {
	l := newCombinedLib(t)
	l.author("Ann Leckie")
	l.book("one", "Book One", "Ann Leckie, Zed Newperson", combinedLibRoot+"One/a.m4b", credit{"Ann Leckie, Zed Newperson", "author", 0})
	l.book("two", "Book Two", "Ann Leckie, Yan Nobody", combinedLibRoot+"Two/a.m4b", credit{"Ann Leckie, Yan Nobody", "author", 0})
	require.NoError(t, authority.PutPersonOverride(l.store, authority.PersonOverride{Name: "Zed Newperson",
		Roles: map[authority.Role]bool{authority.RoleAuthor: true}, SetAt: time.Now()}))
	snap, err := authority.LoadSnapshot(context.Background(), l.store)
	require.NoError(t, err)
	as := &combinedAuthorityStore{PebbleStore: l.store, lookup: snap}
	l.fixer = newCombinedAuthorFixer(&Plugin{deps: fakeDeps{store: as}, standDownWait: noWait})

	_, rows := l.plan()
	require.Equal(t, combinedClassNewAuthors, rows["one"].Class)
	require.Contains(t, rows["one"].Evidence,
		`the authority lists hold "Zed Newperson" as an author from the owner's library (tier O) (strong evidence)`)
	require.Contains(t, rows["two"].Evidence, `the authority lists hold no author entry for "Yan Nobody"`)

	as.mu.Lock()
	as.lookup = authority.Empty()
	as.mu.Unlock()
	l.fixer.idxMu.Lock()
	l.fixer.idx = nil // force the re-plan to rebuild the index without the lists
	l.fixer.idxMu.Unlock()
	fresh, err := l.fixer.Replan(context.Background(), nil, rows["one"], &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, rows["one"].Fingerprint, fresh.Fingerprint, "authority lines never change the fingerprint")
	for _, e := range fresh.Evidence {
		require.NotContains(t, e, "authority lists", "no lists in hand: no authority line")
	}
}
