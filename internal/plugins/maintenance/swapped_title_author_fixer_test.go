// file: internal/plugins/maintenance/swapped_title_author_fixer_test.go
// version: 1.1.0
// guid: 8223b479-ea79-40ca-a48a-1b7bc0f3bea2
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const swapTestOpID = "op-swap-apply"

// swapLib is a real Pebble library with one book per decision the swapped
// title/author fixer makes. Every book has the prod shape: the title is a
// narrator credit for its own narrator, and the author record holds the
// book's title.
type swapLib struct {
	t     *testing.T
	store *database.PebbleStore
	fixer *swappedTitleAuthorFixer
	ids   map[string]string
	// holder maps a fixture to the author record holding its title.
	holder map[string]int
}

// swapBook describes one fixture book.
type swapBook struct {
	name, title, narrator, storedAuthor string
	// provTitle / provAuthor are the provider values on record ("" none).
	provTitle, provAuthor string
	files                 []string
	series                *int
	lockTitle, lockAuthor bool
	// noCredit leaves the book's credit only in its primary author id.
	noCredit bool
	// duration is the book's duration in seconds (0 none).
	duration int
	// prov holds further provider values on record, by field-state key
	// ("narrator", "asin", "audible_runtime_min"), recorded with the title.
	prov map[string]any
	// authorAge records the provider author this long before the title.
	authorAge time.Duration
}

func newSwapLib(t *testing.T) *swapLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	l := &swapLib{t: t, store: st, ids: map[string]string{}, holder: map[string]int{}}
	l.fixer = newSwappedTitleAuthorFixer(&Plugin{deps: fakeDeps{store: st}, standDownWait: noWait})
	return l
}

func (l *swapLib) add(b swapBook) string {
	t := l.t
	t.Helper()
	st := l.store
	holder, err := st.GetAuthorByName(b.storedAuthor)
	require.NoError(t, err)
	if holder == nil {
		holder, err = st.CreateAuthor(b.storedAuthor)
		require.NoError(t, err)
	}
	n := b.narrator
	book := &database.Book{Title: b.title, AuthorID: &holder.ID, SeriesID: b.series, Format: "mp3"}
	if b.duration > 0 {
		d := b.duration
		book.Duration = &d
	}
	if n != "" {
		book.Narrator = &n
	}
	if len(b.files) > 0 {
		book.FilePath = b.files[0]
	}
	created, err := st.CreateBook(book)
	require.NoError(t, err)
	for _, f := range b.files {
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: f, Format: "mp3"}))
	}
	if !b.noCredit {
		require.NoError(t, st.SetBookAuthors(created.ID, []database.BookAuthor{{BookID: created.ID, AuthorID: holder.ID, Role: "author"}}))
	}
	now := time.Now()
	state := func(field, val string, locked bool) {
		at := now
		if field == "author_name" {
			at = now.Add(-b.authorAge)
		}
		s := &database.MetadataFieldState{BookID: created.ID, Field: field, UpdatedAt: at, OverrideLocked: locked}
		if val != "" {
			enc, err := json.Marshal(val)
			require.NoError(t, err)
			v := string(enc)
			s.FetchedValue = &v
		}
		if locked {
			ov := `"hand typed"`
			s.OverrideValue = &ov
		}
		if val != "" || locked {
			require.NoError(t, st.UpsertMetadataFieldState(s))
		}
	}
	state("title", b.provTitle, b.lockTitle)
	state("author_name", b.provAuthor, b.lockAuthor)
	for field, v := range b.prov {
		enc, err := json.Marshal(v)
		require.NoError(t, err)
		ev := string(enc)
		require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: created.ID, Field: field,
			FetchedValue: &ev, UpdatedAt: now}))
	}
	l.ids[b.name] = created.ID
	l.holder[b.name] = holder.ID
	return created.ID
}

func (l *swapLib) plan() (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, nil,
		repairs.PlanDeps{Guard: l.store, Series: repairs.SeriesNamesFrom(series)}, &fakeReporter{})
	require.NoError(l.t, err)
	// Apply reads the stored plan: round-trip it as the op result does.
	raw, err := json.Marshal(res)
	require.NoError(l.t, err)
	var stored repairs.PlanResult
	require.NoError(l.t, json.Unmarshal(raw, &stored))
	byName := map[string]repairs.Row{}
	for name, id := range l.ids {
		for _, r := range stored.Rows {
			if r.RowID == id {
				byName[name] = r
			}
		}
	}
	return &stored, byName
}

func (l *swapLib) apply(plan *repairs.PlanResult, rowIDs []string) *repairs.ApplyResult {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	w := repairs.NewWriter(l.store, l.store, l.fixer.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, swapTestOpID).WithCredits(l.store).WithFieldStates(l.store)
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, "op-swap-plan", rowIDs, false,
		repairs.ApplyDeps{Guard: l.store, Series: repairs.SeriesNamesFrom(series), Writer: w, OpID: swapTestOpID}, &fakeReporter{})
	require.NoError(l.t, err)
	return res
}

func (l *swapLib) credits(name string) []int {
	l.t.Helper()
	cs, err := l.store.GetBookAuthors(l.ids[name])
	require.NoError(l.t, err)
	out := make([]int, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.AuthorID)
	}
	return out
}

func (l *swapLib) book(name string) *database.Book {
	l.t.Helper()
	b, err := l.store.GetBookByID(l.ids[name])
	require.NoError(l.t, err)
	require.NotNil(l.t, b)
	return b
}

func (l *swapLib) locked(name, key string) bool {
	l.t.Helper()
	locks, err := database.LoadFieldLocks(l.store, l.ids[name])
	require.NoError(l.t, err)
	return locks.Locked(key)
}

func (l *swapLib) authorID(name string) int {
	l.t.Helper()
	a, err := l.store.GetAuthorByName(name)
	require.NoError(l.t, err)
	require.NotNil(l.t, a, "author %q", name)
	return a.ID
}

// populate builds the standard fixture set.
func (l *swapLib) populate() {
	t := l.t
	st := l.store
	_, err := st.CreateAuthor("J. N. Chaney")
	require.NoError(t, err)
	_, err = st.CreateAuthor("Mary-Ann Fox")
	require.NoError(t, err)
	_, err = st.CreateAuthor("Maryann Fox")
	require.NoError(t, err)
	dw, err := st.CreateSeries("Doctor Who: The Monthly Adventures", nil)
	require.NoError(t, err)
	lib := "/lib/Shelf/"
	// ---- applicable ----
	l.add(swapBook{name: "swap", title: "read by Jack Voraces", narrator: "Jack Voraces",
		storedAuthor: "Ultimate Level 1_ Divine Creation", provTitle: "Ultimate Level 1: Divine Creation (Unabridged)",
		provAuthor: "Shawn Wilson", files: []string{lib + "Ultimate Level 1_ Divine Creation/book.m4b"}})
	// A second book of the same new author: one create, both credited.
	l.add(swapBook{name: "swap2", title: "read by narrator", storedAuthor: "Ultimate Level 2_ Ascension",
		provTitle: "Ultimate Level 2: Ascension", provAuthor: "Shawn Wilson",
		files: []string{lib + "Ultimate Level 2/a.mp3", lib + "Ultimate Level 2/b.mp3"}})
	// A two-word title: filed under its author's folder, which ties the
	// provider record to the book.
	l.add(swapBook{name: "multi", title: "read by narrator", storedAuthor: "Galaxy Outlaws",
		provTitle: "Galaxy Outlaws", provAuthor: "J.N. Chaney, Terry Mixon",
		files: []string{"/lib/J.N. Chaney/Galaxy Outlaws/book.m4b"}})
	l.add(swapBook{name: "itunes", title: "read by narrator", storedAuthor: "Soul of the Warrior",
		provTitle: "Soul of the Warrior", provAuthor: "Kyfe",
		files: []string{"/mnt/data/books/itunes/Kyfe/Soul of the Warrior/01 Soul.m4b"}})
	l.add(swapBook{name: "nocredit", title: "read by narrator", storedAuthor: "Lone Credit Book",
		provTitle: "Lone Credit Book", provAuthor: "Pat Lone", noCredit: true,
		files: []string{lib + "Lone Credit Book/book.m4b"}})
	// ---- held ----
	l.add(swapBook{name: "no-provider-author", title: "read by narrator", storedAuthor: "Nobody Wrote This",
		provTitle: "Nobody Wrote This", files: []string{lib + "Nobody Wrote This/book.m4b"}})
	l.add(swapBook{name: "placeholder-author", title: "read by narrator", storedAuthor: "Placeholder Tale",
		provTitle: "Placeholder Tale", provAuthor: "Unknown Author", files: []string{lib + "Placeholder Tale/book.m4b"}})
	l.add(swapBook{name: "title-locked", title: "read by narrator", storedAuthor: "Locked Title Book",
		provTitle: "Locked Title Book", provAuthor: "Lock Smith", lockTitle: true, files: []string{lib + "Locked Title Book/book.m4b"}})
	l.add(swapBook{name: "author-locked", title: "read by narrator", storedAuthor: "Locked Author Book",
		provTitle: "Locked Author Book", provAuthor: "Lock Smith", lockAuthor: true, files: []string{lib + "Locked Author Book/book.m4b"}})
	l.add(swapBook{name: "dw-title", title: "read by narrator", storedAuthor: "Doctor Who_ The Daleks",
		provTitle: "Doctor Who: The Daleks", provAuthor: "Terry Nation", files: []string{lib + "Daleks/book.m4b"}})
	l.add(swapBook{name: "dw-series", title: "read by narrator", storedAuthor: "Plain Named Story",
		provTitle: "Plain Named Story", provAuthor: "Big Writer", series: &dw.ID, files: []string{lib + "Plain Named Story/book.m4b"}})
	l.add(swapBook{name: "role", title: "read by narrator", storedAuthor: "Goblin Slayer",
		provTitle: "Goblin Slayer", provAuthor: "Kumo Kagyu, Kevin Steinbach - translator", files: []string{lib + "Goblin Slayer/book.m4b"}})
	l.add(swapBook{name: "unsplittable", title: "read by narrator", storedAuthor: "Dungeon Book",
		provTitle: "Dungeon Book", provAuthor: "Leif Roder, Synonymoose", files: []string{lib + "Dungeon Book/book.m4b"}})
	l.add(swapBook{name: "ambiguous", title: "read by narrator", storedAuthor: "Fox Tales",
		provTitle: "Fox Tales", provAuthor: "Mary Ann Fox", files: []string{lib + "Fox Tales/book.m4b"}})
	// A fragment: another live book owns its file.
	parent, err := st.CreateAuthor("Parent Writer")
	require.NoError(t, err)
	pb, err := st.CreateBook(&database.Book{Title: "Big Parent", AuthorID: &parent.ID, FilePath: lib + "Big Parent/a.mp3", Format: "mp3"})
	require.NoError(t, err)
	require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: pb.ID, FilePath: lib + "Big Parent/a.mp3", Format: "mp3"}))
	l.add(swapBook{name: "fragment", title: "read by narrator", storedAuthor: "Big Parent",
		provTitle: "Big Parent", provAuthor: "Parent Writer", files: []string{lib + "Big Parent/a.mp3"}})
	// A near miss: the stored author is the provider's title without its
	// subtitle. Listed for a person, never written.
	l.add(swapBook{name: "near-miss", title: "read by narrator", storedAuthor: "Ultimate Level 3",
		provTitle: "Ultimate Level 3: Rebirth", provAuthor: "Shawn Wilson", files: []string{lib + "Ultimate Level 3/book.m4b"}})
	// ---- not this fixer's: never rows ----
	l.add(swapBook{name: "not-swapped", title: "read by narrator", storedAuthor: "Real Person",
		provTitle: "Some Other Title", provAuthor: "Real Person", files: []string{lib + "Other/book.m4b"}})
	l.add(swapBook{name: "real-title", title: "A Real Title", storedAuthor: "Galaxy Outlaws",
		provTitle: "Galaxy Outlaws", provAuthor: "J.N. Chaney", files: []string{lib + "Real/book.m4b"}})
}

func TestSwappedTitleAuthorFixer_PlanDecisions(t *testing.T) {
	l := newSwapLib(t)
	l.populate()
	res, rows := l.plan()

	applicable := map[string][2]string{
		"swap":     {"Ultimate Level 1: Divine Creation", "Shawn Wilson (new author)"},
		"swap2":    {"Ultimate Level 2: Ascension", "Shawn Wilson (new author)"},
		"multi":    {"Galaxy Outlaws", "J. N. Chaney, Terry Mixon (new author)"},
		"itunes":   {"Soul of the Warrior", "Kyfe (new author)"},
		"nocredit": {"Lone Credit Book", "Pat Lone (new author)"},
	}
	for name, want := range applicable {
		r, ok := rows[name]
		require.True(t, ok, "%s is a row", name)
		require.True(t, r.Applicable(), "%s: %s %s", name, r.Skipped, r.SkipReason)
		require.Equal(t, want[0], r.Proposed["title"], name)
		require.Equal(t, want[1], r.Proposed["author"], name)
		require.Equal(t, repairs.RiskReview, r.Risk, "%s: an author rewrite is always reviewed", name)
	}
	held := map[string]string{
		"no-provider-author": swapSkipNoProviderAuthor,
		"placeholder-author": swapSkipNoProviderAuthor,
		"title-locked":       junkSkipUserLocked,
		"author-locked":      junkSkipUserLocked,
		"dw-title":           repairs.SkipOwnerManual,
		"dw-series":          repairs.SkipOwnerManual,
		"role":               swapSkipMultiAuthor,
		"unsplittable":       swapSkipMultiAuthor,
		"ambiguous":          swapSkipAmbiguousAuthor,
		"fragment":           junkSkipFragment,
		"near-miss":          junkSkipNeedsManual,
	}
	for name, kind := range held {
		r, ok := rows[name]
		require.True(t, ok, "%s is a row", name)
		require.Equal(t, kind, r.Skipped, "%s: %s", name, r.SkipReason)
	}
	require.Equal(t, "no provider author on record", rows["no-provider-author"].SkipReason)
	for _, name := range []string{"not-swapped", "real-title"} {
		_, ok := rows[name]
		require.False(t, ok, "%s is not this fixer's row", name)
	}
	require.Equal(t, len(applicable)+len(held), res.Total)
	require.Equal(t, len(applicable), res.Applicable)

	// Plan and re-plan agree on every row.
	for name, r := range rows {
		fresh, err := l.fixer.Replan(context.Background(), nil, r, &fakeReporter{})
		require.NoError(t, err, name)
		require.Equal(t, r.Fingerprint, fresh.Fingerprint, name)
	}
}

func TestSwappedTitleAuthorFixer_ApplyAndRevert(t *testing.T) {
	l := newSwapLib(t)
	l.populate()
	plan, rows := l.plan()
	chaney := l.authorID("J. N. Chaney")
	beforeCredits := map[string][]int{}
	for name := range l.ids {
		beforeCredits[name] = l.credits(name)
	}
	itunesBefore := l.book("itunes")
	itunesFiles, err := l.store.GetBookFiles(l.ids["itunes"])
	require.NoError(t, err)

	sel := []string{rows["swap"].RowID, rows["swap2"].RowID, rows["multi"].RowID, rows["itunes"].RowID,
		rows["nocredit"].RowID, rows["no-provider-author"].RowID}
	out := l.apply(plan, sel)
	require.Equal(t, 5, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, 1, out.ByOutcome[repairs.OutcomeNotApplicable], "a held row is never written")

	wilson := l.authorID("Shawn Wilson")
	swap := l.book("swap")
	require.Equal(t, "Ultimate Level 1: Divine Creation", swap.Title)
	require.Equal(t, wilson, *swap.AuthorID)
	require.Equal(t, []int{wilson}, l.credits("swap"))
	swap2 := l.book("swap2")
	require.Equal(t, "Ultimate Level 2: Ascension", swap2.Title)
	require.Equal(t, wilson, *swap2.AuthorID, "both books credit the one created author")

	multi := l.book("multi")
	require.Equal(t, "Galaxy Outlaws", multi.Title)
	mixon := l.authorID("Terry Mixon")
	require.Equal(t, chaney, *multi.AuthorID, "the existing J. N. Chaney is resolved, not duplicated")
	require.Equal(t, []int{chaney, mixon}, l.credits("multi"))
	all, err := l.store.GetAllAuthors()
	require.NoError(t, err)
	chaneys := 0
	for _, a := range all {
		if junkLettersKey(a.Name) == "jnchaney" {
			chaneys++
		}
	}
	require.Equal(t, 1, chaneys)

	nocredit := l.book("nocredit")
	require.Equal(t, l.authorID("Pat Lone"), *nocredit.AuthorID)
	require.Equal(t, []int{l.authorID("Pat Lone")}, l.credits("nocredit"))

	// iTunes: database fields only. The path and every book_file row stay.
	it := l.book("itunes")
	require.Equal(t, "Soul of the Warrior", it.Title)
	require.Equal(t, l.authorID("Kyfe"), *it.AuthorID)
	require.Equal(t, itunesBefore.FilePath, it.FilePath)
	itFilesAfter, err := l.store.GetBookFiles(l.ids["itunes"])
	require.NoError(t, err)
	require.Len(t, itFilesAfter, len(itunesFiles))
	for i := range itunesFiles {
		require.Equal(t, itunesFiles[i].FilePath, itFilesAfter[i].FilePath)
	}

	// The author record that held the title is left in place.
	for _, name := range []string{"swap", "multi", "itunes"} {
		a, err := l.store.GetAuthorByID(l.holder[name])
		require.NoError(t, err)
		require.NotNil(t, a, "%s: the title-holding author record is not deleted", name)
	}

	// Title and author are locked so a forced rescan cannot write the file
	// tags' swapped values back.
	for _, name := range []string{"swap", "multi", "itunes", "nocredit"} {
		require.True(t, l.locked(name, database.FieldKeyTitle), "%s: title locked", name)
		require.True(t, l.locked(name, database.FieldKeyAuthorName), "%s: author locked", name)
	}
	require.False(t, l.locked("no-provider-author", database.FieldKeyTitle), "a held row is never locked")

	// History: the title change is recorded for the book's history view.
	hist, err := l.store.GetMetadataChangeHistory(l.ids["swap"], "title", 10)
	require.NoError(t, err)
	require.NotEmpty(t, hist, "a title history row is recorded")
	require.Equal(t, swappedFixerID, hist[0].Source)
	// Undo-last-apply refuses the batch: its credits live in the op journal.
	_, uerr := metafetch.NewService(l.store).UndoLastApply(l.ids["swap"])
	require.Error(t, uerr)
	require.Equal(t, "Ultimate Level 1: Divine Creation", l.book("swap").Title)

	// A second apply of the same plan writes nothing twice.
	again := l.apply(plan, sel[:5])
	require.Zero(t, again.Applied)
	require.Equal(t, 5, again.ChangedSincePlan, "outcomes %v", again.ByOutcome)

	// The op revert puts titles, credits and primaries back and removes the
	// authors the apply created (both books' credits first, then the create).
	rev, err := audiobooks.NewRevertService(l.store).RevertOperation(swapTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	for name := range l.ids {
		require.Equal(t, beforeCredits[name], l.credits(name), "credits of %s after revert", name)
	}
	require.Equal(t, "read by Jack Voraces", l.book("swap").Title)
	require.Equal(t, l.holder["swap"], *l.book("swap").AuthorID)
	require.Equal(t, "read by narrator", l.book("multi").Title)
	require.Equal(t, l.holder["multi"], *l.book("multi").AuthorID)
	for _, n := range []string{"Shawn Wilson", "Terry Mixon", "Kyfe", "Pat Lone"} {
		a, err := l.store.GetAuthorByName(n)
		require.NoError(t, err)
		require.Nil(t, a, "created author %q removed by the revert", n)
	}
	a, err := l.store.GetAuthorByID(chaney)
	require.NoError(t, err)
	require.NotNil(t, a, "an existing author the apply only resolved stays")
	for _, name := range []string{"swap", "multi", "itunes", "nocredit"} {
		require.False(t, l.locked(name, database.FieldKeyTitle), "%s: the revert lifts the title lock", name)
		require.False(t, l.locked(name, database.FieldKeyAuthorName), "%s: the revert lifts the author lock", name)
	}
}

// A provider value that changes between plan and apply is a different
// decision: the row is refused as changed_since_plan and nothing is written.
func TestSwappedTitleAuthorFixer_ReplanDetectsProviderChange(t *testing.T) {
	l := newSwapLib(t)
	l.populate()
	plan, rows := l.plan()
	v := `"Somebody Else"`
	require.NoError(t, l.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.ids["swap"],
		Field: "author_name", FetchedValue: &v, UpdatedAt: time.Now()}))
	fresh, err := l.fixer.Replan(context.Background(), nil, rows["swap"], &fakeReporter{})
	require.NoError(t, err)
	require.NotEqual(t, rows["swap"].Fingerprint, fresh.Fingerprint)

	out := l.apply(plan, []string{rows["swap"].RowID})
	require.Zero(t, out.Applied)
	require.Equal(t, 1, out.ChangedSincePlan, "outcomes %v", out.ByOutcome)
	require.Equal(t, "read by Jack Voraces", l.book("swap").Title)
	require.Equal(t, []int{l.holder["swap"]}, l.credits("swap"))
	a, err := l.store.GetAuthorByName("Shawn Wilson")
	require.NoError(t, err)
	require.Nil(t, a, "no author is created for a refused row")
}

// A user lock that lands after the plan refuses the row before anything is
// written.
func TestSwappedTitleAuthorFixer_LockAfterPlanRefuses(t *testing.T) {
	l := newSwapLib(t)
	l.populate()
	plan, rows := l.plan()
	ov := `"Hand Typed"`
	require.NoError(t, l.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.ids["swap"],
		Field: "author_name", OverrideValue: &ov, OverrideLocked: true, UpdatedAt: time.Now()}))
	out := l.apply(plan, []string{rows["swap"].RowID})
	require.Zero(t, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, "read by Jack Voraces", l.book("swap").Title)
	require.Equal(t, []int{l.holder["swap"]}, l.credits("swap"))
}

func TestSwapAuthorNames(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
		skip string
	}{
		{"Shawn Wilson", []string{"Shawn Wilson"}, ""},
		{"J.M. Clarke", []string{"J. M. Clarke"}, ""},
		{"J.N. Chaney, Terry Mixon", []string{"J. N. Chaney", "Terry Mixon"}, ""},
		// Three names or more is held: the light-novel shape lists the
		// illustrator with no role marker.
		{"J.N. Chaney, Aaron Bunce, Terry Maggert", nil, swapSkipMultiAuthor},
		{"Kumo Kagyu, Noboru Kannatuki, Shiro Nameless", nil, swapSkipMultiAuthor},
		{"Kumo Kagyu, Noboru Kannatuki, Kevin Steinbach - translator", nil, swapSkipMultiAuthor},
		{"Dmitry Dornichev, Nathan Klausner -translated by", nil, swapSkipMultiAuthor},
		{"Gardner Dozois - editor, George R. R. Martin", nil, swapSkipMultiAuthor},
		{"Leif Roder, Synonymoose", nil, swapSkipMultiAuthor},
		{"Chapter 12", nil, swapSkipImplausibleAuthor},
	}
	for _, c := range cases {
		got, skip, why := swapAuthorNames(c.raw)
		require.Equal(t, c.skip, skip, "%q: %s", c.raw, why)
		require.Equal(t, c.want, got, c.raw)
	}
}

func TestSwapCredits(t *testing.T) {
	a := func(id int) *database.Author { return &database.Author{ID: id} }
	cur := []database.BookAuthor{
		{AuthorID: 9, Role: "author", Position: 0},
		{AuthorID: 5, Role: "narrator", Position: 1},
	}
	got := swapCredits(cur, "b", 9, []*database.Author{a(1), a(5), a(2)})
	require.Equal(t, []database.BookAuthor{
		{BookID: "b", AuthorID: 1, Role: "author", Position: 0},
		{BookID: "b", AuthorID: 5, Role: "author", Position: 1},
		{BookID: "b", AuthorID: 2, Role: "author", Position: 2},
		{AuthorID: 5, Role: "narrator", Position: 3},
	}, got, "a target credited only as narrator (an author reading their own book) gets an author row and keeps the narrator row")
	cur = []database.BookAuthor{
		{AuthorID: 9, Role: "author", Position: 0},
		{AuthorID: 5, Role: "author", Position: 1},
	}
	require.Equal(t, []database.BookAuthor{
		{BookID: "b", AuthorID: 1, Role: "author", Position: 0},
		{AuthorID: 5, Role: "author", Position: 1},
	}, swapCredits(cur, "b", 9, []*database.Author{a(1), a(5)}),
		"an author already credited as an author is not added twice; other credits keep their order")
	require.Equal(t, []database.BookAuthor{{BookID: "b", AuthorID: 1, Role: "author", Position: 0}},
		swapCredits(nil, "b", 9, []*database.Author{a(1)}))
}

// Two rows of one apply bringing two spellings of one new name ("Jo-Ann
// Pike", "Jo Ann Pike") create ONE author: MintAuthor only serializes
// identical names, so the fixer remembers what it created.
func TestSwappedTitleAuthorFixer_TwoSpellingsMintOneAuthor(t *testing.T) {
	l := newSwapLib(t)
	l.add(swapBook{name: "a", title: "read by narrator", storedAuthor: "First Pike Book",
		provTitle: "First Pike Book", provAuthor: "Jo-Ann Pike", files: []string{"/lib/P/First Pike Book/book.m4b"}})
	l.add(swapBook{name: "b", title: "read by narrator", storedAuthor: "Second Pike Book",
		provTitle: "Second Pike Book", provAuthor: "Jo Ann Pike", files: []string{"/lib/P/Second Pike Book/book.m4b"}})
	plan, rows := l.plan()
	require.True(t, rows["a"].Applicable(), rows["a"].SkipReason)
	require.True(t, rows["b"].Applicable(), rows["b"].SkipReason)
	out := l.apply(plan, []string{rows["a"].RowID, rows["b"].RowID})
	require.Equal(t, 2, out.Applied, "outcomes %v", out.ByOutcome)
	all, err := l.store.GetAllAuthors()
	require.NoError(t, err)
	var pikes []database.Author
	for _, a := range all {
		if junkLettersKey(a.Name) == "joannpike" {
			pikes = append(pikes, a)
		}
	}
	require.Len(t, pikes, 1, "one author for both spellings: %v", pikes)
	require.Equal(t, pikes[0].ID, *l.book("a").AuthorID)
	require.Equal(t, pikes[0].ID, *l.book("b").AuthorID)
}
