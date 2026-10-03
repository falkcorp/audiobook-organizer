// file: internal/plugins/maintenance/swapped_title_author_fixer_test.go
// version: 1.4.0
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
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
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
	l.add(swapBook{name: "real-title", title: "A Real Title", storedAuthor: "Space Outlaws",
		provTitle: "Space Outlaws", provAuthor: "J.N. Chaney", files: []string{lib + "Real/book.m4b"}})
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

// A title of one or two words is weak identity: another book may share it.
// Such a row applies only when something besides the title ties the provider
// record to the book; a narrator or ASIN a metadata fetch filled in does not
// count, since it was copied from the record it would corroborate.
func TestSwappedTitleAuthorFixer_ShortTitleNeedsCorroboration(t *testing.T) {
	l := newSwapLib(t)
	lib := "/lib/Short/"
	l.add(swapBook{name: "bare", title: "read by narrator", storedAuthor: "Descent",
		provTitle: "Descent", provAuthor: "Tracy Gregory", files: []string{lib + "Descent/book.m4b"}})
	l.add(swapBook{name: "read-by", title: "read by Kim Reader", narrator: "Kim Reader", storedAuthor: "Fury",
		provTitle: "Fury", provAuthor: "Henry Kuttner", prov: map[string]any{"narrator": "Kim Reader"},
		files: []string{lib + "Fury/book.m4b"}})
	l.add(swapBook{name: "runtime", title: "read by narrator", storedAuthor: "Lust",
		provTitle: "Lust", provAuthor: "Ann Writer", duration: 10*3600 + 300, prov: map[string]any{"audible_runtime_min": 600},
		files: []string{lib + "Lust/book.m4b"}})
	l.add(swapBook{name: "runtime-off", title: "read by narrator", storedAuthor: "Titans",
		provTitle: "Titans", provAuthor: "Ben Writer", duration: 3 * 3600, prov: map[string]any{"audible_runtime_min": 600},
		files: []string{lib + "Titans/book.m4b"}})
	l.add(swapBook{name: "asin", title: "read by narrator", storedAuthor: "Majestic",
		provTitle: "Majestic", provAuthor: "Cal Writer", prov: map[string]any{"asin": "B00TEST123"},
		files: []string{lib + "Majestic/book.m4b"}})
	l.add(swapBook{name: "circular", title: "read by narrator", narrator: "Pat Voice", storedAuthor: "Vengeance",
		provTitle: "Vengeance", provAuthor: "Dee Writer", prov: map[string]any{"narrator": "Pat Voice"},
		files: []string{lib + "Vengeance/book.m4b"}})
	asin := "B00TEST123"
	_, err := l.store.ModifyBook(l.ids["asin"], func(b *database.Book) error { b.ASIN = &asin; return nil })
	require.NoError(t, err)
	// The circular book's narrator was filled in by a metadata fetch.
	prev, next := `""`, `"Pat Voice"`
	require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: l.ids["circular"],
		Field: database.HistoryFieldName("narrator"), PreviousValue: &prev, NewValue: &next, ChangeType: "fetched", Source: "Audible"}))
	// A long title needs nothing more.
	l.add(swapBook{name: "long", title: "read by narrator", storedAuthor: "The Long Road Home Again",
		provTitle: "The Long Road Home Again", provAuthor: "Eve Writer", files: []string{lib + "Long/book.m4b"}})

	_, rows := l.plan()
	// A runtime within 10% is not enough on its own: it is recorded as
	// evidence, and the row is still held.
	for _, name := range []string{"bare", "runtime", "runtime-off", "circular"} {
		require.Equal(t, junkSkipNeedsManual, rows[name].Skipped, "%s: %s", name, rows[name].SkipReason)
		require.Contains(t, rows[name].SkipReason, "nothing but the title ties the provider record", name)
	}
	require.Contains(t, rows["runtime"].SkipReason, "a runtime alone does not count")
	want := map[string]string{
		"read-by": `provider narrator "Kim Reader" matches the book's narrator "Kim Reader"`,
		"asin":    "provider ASIN B00TEST123 matches the book's ASIN",
	}
	for name, ev := range want {
		require.True(t, rows[name].Applicable(), "%s: %s %s", name, rows[name].Skipped, rows[name].SkipReason)
		require.Contains(t, rows[name].Evidence, ev, name)
	}
	require.True(t, rows["long"].Applicable(), "%s %s", rows["long"].Skipped, rows["long"].SkipReason)
}

// Fetched values merge field by field, so a title and an author recorded at
// different times may be two provider records' halves: the row is held.
func TestSwappedTitleAuthorFixer_TitleAndAuthorFromDifferentFetches(t *testing.T) {
	l := newSwapLib(t)
	l.add(swapBook{name: "split", title: "read by narrator", storedAuthor: "Ultimate Level 1_ Divine Creation",
		provTitle: "Ultimate Level 1: Divine Creation", provAuthor: "Shawn Wilson", authorAge: time.Hour,
		files: []string{"/lib/S/Ultimate Level 1/book.m4b"}})
	l.add(swapBook{name: "together", title: "read by narrator", storedAuthor: "Ultimate Level 2_ Ascension",
		provTitle: "Ultimate Level 2: Ascension", provAuthor: "Shawn Wilson", authorAge: 2 * time.Second,
		files: []string{"/lib/S/Ultimate Level 2/book.m4b"}})
	_, rows := l.plan()
	require.Equal(t, junkSkipNeedsManual, rows["split"].Skipped, rows["split"].SkipReason)
	require.Contains(t, rows["split"].SkipReason, "recorded 1h0m0s apart")
	require.True(t, rows["together"].Applicable(), "%s %s", rows["together"].Skipped, rows["together"].SkipReason)
}

// A provider value carrying HTML entities is decoded before it is compared,
// split or written.
func TestSwappedTitleAuthorFixer_HTMLEntitiesDecoded(t *testing.T) {
	l := newSwapLib(t)
	l.add(swapBook{name: "amp", title: "read by narrator", storedAuthor: "Magic Tides & Magic Claims",
		provTitle: "Magic Tides &amp; Magic Claims", provAuthor: "Jo O&#39;Neil",
		files: []string{"/lib/M/Magic Tides/book.m4b"}})
	plan, rows := l.plan()
	r := rows["amp"]
	require.True(t, r.Applicable(), "%s %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Magic Tides & Magic Claims", r.Proposed["title"])
	out := l.apply(plan, []string{r.RowID})
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, "Magic Tides & Magic Claims", l.book("amp").Title)
	a, err := l.store.GetAuthorByID(*l.book("amp").AuthorID)
	require.NoError(t, err)
	require.Equal(t, "Jo O'Neil", a.Name)
}

// The order of the two fixers: the junk-title fixer lists a swapped book for
// this fixer instead of retitling it. A book it retitled before that guard
// existed (title real, author still the title) is planned here author-only:
// the author moves, the title is not written, and the junk-title fixer's
// title lock does not hold the row. Each op's revert undoes its own part.
func TestSwappedTitleAuthorFixer_OrderWithJunkTitleFixer(t *testing.T) {
	l := newSwapLib(t)
	// The provider recorded the narrator too: an author-only row needs a tie
	// besides the title.
	l.add(swapBook{name: "swap", title: "read by Jack Voraces", narrator: "Jack Voraces",
		storedAuthor: "Ultimate Level 1_ Divine Creation", provTitle: "Ultimate Level 1: Divine Creation (Unabridged)",
		provAuthor: "Shawn Wilson", prov: map[string]any{"narrator": "Jack Voraces"},
		files: []string{"/lib/S/Ultimate Level 1_ Divine Creation/book.m4b"}})
	id := l.ids["swap"]

	junk := newJunkTitleFixer(&Plugin{deps: fakeDeps{store: l.store}, standDownWait: noWait})
	jrows, err := junk.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	var jr repairs.Row
	for _, r := range jrows {
		if r.RowID == id {
			jr = r
		}
	}
	require.Equal(t, junkSkipSwapped, jr.Skipped, jr.SkipReason)
	require.Contains(t, jr.SkipReason, swappedFixerID)

	// The title written the way the junk-title fixer wrote it before the
	// guard: journaled under its own op and locked.
	const junkOp = "op-junk-early"
	jw := repairs.NewWriter(l.store, l.store, junkTitlesFixerID, "bulk_update", "repairs-").
		WithJournal(l.store, l.store, junkOp).WithFieldStates(l.store)
	require.NoError(t, writeTitleOnly(jw, l.store, id, "read by Jack Voraces", "Ultimate Level 1: Divine Creation"))
	require.True(t, l.locked("swap", database.FieldKeyTitle))

	plan, rows := l.plan()
	r := rows["swap"]
	require.True(t, r.Applicable(), "author-only row: %s %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Ultimate Level 1: Divine Creation", r.Proposed["title"], "the title is shown unchanged")
	require.Contains(t, r.Reason, "the title is not changed")

	out := l.apply(plan, []string{r.RowID})
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	wilson := l.authorID("Shawn Wilson")
	require.Equal(t, wilson, *l.book("swap").AuthorID)
	require.Equal(t, []int{wilson}, l.credits("swap"))
	require.Equal(t, "Ultimate Level 1: Divine Creation", l.book("swap").Title)
	require.True(t, l.locked("swap", database.FieldKeyAuthorName))
	titleHist, err := l.store.GetMetadataChangeHistory(id, "title", 10)
	require.NoError(t, err)
	for _, h := range titleHist {
		require.NotEqual(t, swappedFixerID, h.Source, "an author-only apply writes no title")
	}

	// Reverting the swapped op puts the author back and lifts its lock; the
	// junk-title fixer's title and lock are its own op's.
	rev, err := audiobooks.NewRevertService(l.store).RevertOperation(swapTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	require.Equal(t, l.holder["swap"], *l.book("swap").AuthorID)
	require.False(t, l.locked("swap", database.FieldKeyAuthorName))
	require.True(t, l.locked("swap", database.FieldKeyTitle))
	require.Equal(t, "Ultimate Level 1: Divine Creation", l.book("swap").Title)

	rev, err = audiobooks.NewRevertService(l.store).RevertOperation(junkOp)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	require.Equal(t, "read by Jack Voraces", l.book("swap").Title)
	require.False(t, l.locked("swap", database.FieldKeyTitle))
}

// An author who narrates their own book keeps the narrator credit and gains
// the author credit; the book is never left with no author-role credit.
func TestSwappedTitleAuthorFixer_AuthorWhoNarratesKeepsBothCredits(t *testing.T) {
	l := newSwapLib(t)
	l.add(swapBook{name: "war", title: "read by Bob Woodward", narrator: "Bob Woodward", storedAuthor: "War",
		provTitle: "War", provAuthor: "Bob Woodward", prov: map[string]any{"narrator": "Bob Woodward"},
		files: []string{"/lib/W/War/book.m4b"}})
	bob, err := l.store.CreateAuthor("Bob Woodward")
	require.NoError(t, err)
	id := l.ids["war"]
	require.NoError(t, l.store.SetBookAuthors(id, []database.BookAuthor{
		{BookID: id, AuthorID: l.holder["war"], Role: "author", Position: 0},
		{BookID: id, AuthorID: bob.ID, Role: "narrator", Position: 1},
	}))
	plan, rows := l.plan()
	require.True(t, rows["war"].Applicable(), "%s %s", rows["war"].Skipped, rows["war"].SkipReason)
	out := l.apply(plan, []string{rows["war"].RowID})
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	cs, err := l.store.GetBookAuthors(id)
	require.NoError(t, err)
	roles := map[string]bool{}
	for _, c := range cs {
		require.Equal(t, bob.ID, c.AuthorID)
		roles[c.Role] = true
	}
	require.Equal(t, map[string]bool{"author": true, "narrator": true}, roles)
	require.Equal(t, bob.ID, *l.book("war").AuthorID)
}

// A real author is never a misplaced title. An author record that credits
// other real titles (Poe also wrote "The Raven") is held, and so is an
// author-only row tied to its provider record by a runtime alone.
func TestSwappedTitleAuthorFixer_RealAuthorIsNeverRewritten(t *testing.T) {
	l := newSwapLib(t)
	l.add(swapBook{name: "poe-other1", title: "The Raven and Other Poems", storedAuthor: "Edgar Allan Poe",
		files: []string{"/lib/Edgar Allan Poe/The Raven/book.m4b"}})
	l.add(swapBook{name: "poe-other2", title: "The Tell-Tale Heart", storedAuthor: "Edgar Allan Poe",
		files: []string{"/lib/Edgar Allan Poe/Tell-Tale/book.m4b"}})
	l.add(swapBook{name: "poe", title: "Edgar Allan Poe", narrator: "Basil Rathbone", storedAuthor: "Edgar Allan Poe",
		provTitle: "Edgar Allan Poe", provAuthor: "Jeffrey Meyers",
		files: []string{"/lib/Edgar Allan Poe/Edgar Allan Poe/book.m4b"}})
	l.add(swapBook{name: "fry", title: "Stephen Fry", storedAuthor: "Stephen Fry", duration: 9 * 3600,
		provTitle: "Stephen Fry", provAuthor: "Tim Biographer", prov: map[string]any{"audible_runtime_min": 580},
		files: []string{"/lib/Stephen Fry/Stephen Fry/book.m4b"}})
	plan, rows := l.plan()
	require.Equal(t, junkSkipNeedsManual, rows["poe"].Skipped, rows["poe"].SkipReason)
	require.Contains(t, rows["poe"].SkipReason, "also credits 2 other title(s)")
	require.Equal(t, junkSkipNeedsManual, rows["fry"].Skipped, rows["fry"].SkipReason)
	require.Contains(t, rows["fry"].SkipReason, "a runtime alone does not count")

	poeAuthor := *l.book("poe").AuthorID
	out := l.apply(plan, []string{rows["poe"].RowID, rows["fry"].RowID})
	require.Zero(t, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, poeAuthor, *l.book("poe").AuthorID)
	require.False(t, l.locked("poe", database.FieldKeyAuthorName))
	a, err := l.store.GetAuthorByName("Jeffrey Meyers")
	require.NoError(t, err)
	require.Nil(t, a)
}

// Full shape with a real author: the junk title names the stored author as
// its reader ("read by T. S. Eliot" by T. S. Eliot). Held whatever the
// spelling, and the word count does not depend on the initials' spacing.
func TestSwappedTitleAuthorFixer_SelfReadAuthorIsHeld(t *testing.T) {
	l := newSwapLib(t)
	l.add(swapBook{name: "eliot-other", title: "The Waste Land", storedAuthor: "T. S. Eliot",
		files: []string{"/lib/T. S. Eliot/The Waste Land/book.m4b"}})
	l.add(swapBook{name: "eliot", title: "read by T. S. Eliot", narrator: "T. S. Eliot", storedAuthor: "T. S. Eliot",
		provTitle: "T. S. Eliot", provAuthor: "Peter Ackroyd",
		files: []string{"/lib/T. S. Eliot/Collected Readings/book.m4b"}})
	l.add(swapBook{name: "eliot2", title: "read by T.S. Eliot", narrator: "T.S. Eliot", storedAuthor: "T.S. Eliot",
		provTitle: "T.S. Eliot", provAuthor: "Peter Ackroyd", files: []string{"/lib/TSE/Readings/book.m4b"}})
	_, rows := l.plan()
	require.Equal(t, junkSkipNeedsManual, rows["eliot"].Skipped, rows["eliot"].SkipReason)
	require.Equal(t, junkSkipNeedsManual, rows["eliot2"].Skipped, rows["eliot2"].SkipReason)
	require.Contains(t, rows["eliot2"].SkipReason, "read by its stored author")
	require.Equal(t, 3, titleWords("T.S. Eliot"))
	require.Equal(t, 3, titleWords("T. S. Eliot"))
	require.Equal(t, 5, titleWords("Ultimate Level 1: Divine Creation"))
}

// A title write refused because the title moved voids the row it journaled:
// reverting that operation later must not undo a later operation's write,
// nor leave the junk title locked.
func TestWriteTitleOnly_RefusedWriteLeavesNoUndoRow(t *testing.T) {
	l := newSwapLib(t)
	id := l.add(swapBook{name: "b", title: "read by Jack Voraces", storedAuthor: "Holder Writer", files: []string{"/lib/x/book.m4b"}})
	_, err := l.store.ModifyBook(id, func(b *database.Book) error { b.Title = "Person Typed"; return nil })
	require.NoError(t, err)
	w1 := repairs.NewWriter(l.store, l.store, junkTitlesFixerID, "bulk_update", "repairs-").
		WithJournal(l.store, l.store, "op1").WithFieldStates(l.store)
	require.ErrorIs(t, writeTitleOnly(w1, l.store, id, "read by Jack Voraces", "Real Title"), repairs.ErrChangedSincePlan)
	ch, err := l.store.GetOperationChanges("op1")
	require.NoError(t, err)
	for _, c := range ch {
		require.NotNil(t, c.RevertedAt, "the row of the refused write is voided: %+v", c)
	}

	_, err = l.store.ModifyBook(id, func(b *database.Book) error { b.Title = "read by Jack Voraces"; return nil })
	require.NoError(t, err)
	w2 := repairs.NewWriter(l.store, l.store, junkTitlesFixerID, "bulk_update", "repairs-").
		WithJournal(l.store, l.store, "op2").WithFieldStates(l.store)
	require.NoError(t, writeTitleOnly(w2, l.store, id, "read by Jack Voraces", "Real Title"))
	_, _ = audiobooks.NewRevertService(l.store).RevertOperation("op1")
	require.Equal(t, "Real Title", l.book("b").Title, "reverting op1 does not touch op2's write")
	require.True(t, l.locked("b", database.FieldKeyTitle))
}

// A book whose state is still in the pre-migration blob gets its blob
// migrated when the title is locked: the write is whole, not partial, and
// the blob's own lock stays a lock.
func TestWriteTitleOnly_MigratesLegacyBlob(t *testing.T) {
	l := newSwapLib(t)
	id := l.add(swapBook{name: "b", title: "read by Jack Voraces", storedAuthor: "Holder Writer", files: []string{"/lib/x/book.m4b"}})
	require.NoError(t, l.store.SetUserPreference(metastate.Key(id), `{"narrator":{"override_locked":true}}`))
	w := repairs.NewWriter(l.store, l.store, junkTitlesFixerID, "bulk_update", "repairs-").
		WithJournal(l.store, l.store, "op1").WithFieldStates(l.store)
	require.NoError(t, writeTitleOnly(w, l.store, id, "read by Jack Voraces", "Real Title"))
	require.Equal(t, "Real Title", l.book("b").Title)
	require.True(t, l.locked("b", database.FieldKeyTitle))
	require.True(t, l.locked("b", database.FieldKeyNarrator), "the blob's lock survives as a row")
	pref, err := l.store.GetUserPreference(metastate.Key(id))
	require.NoError(t, err)
	require.True(t, pref == nil || pref.Value == nil || *pref.Value == "", "the blob is retired")

	// A blob that does not parse is held at plan time, before any write.
	jl := newJunkLib(t)
	bad := jl.ids["folder"]
	require.NoError(t, jl.store.SetUserPreference(metastate.Key(bad), `{not json`))
	_, _, rows := jl.plan(t)
	require.Equal(t, junkSkipNeedsManual, rows["folder"].Skipped, rows["folder"].SkipReason)
	require.Contains(t, rows["folder"].SkipReason, "pre-migration metadata state cannot be read")
}

// The revert lifts only its own lock. A person who locked the field since
// (their lock carries no repair source) keeps it; the row is refused.
func TestSwappedTitleAuthorFixer_RevertKeepsAPersonsLock(t *testing.T) {
	l := newSwapLib(t)
	l.populate()
	plan, rows := l.plan()
	out := l.apply(plan, []string{rows["swap"].RowID})
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	// The person's lock toggle: UpdateAudiobook clears the source.
	st := fieldStateOf(mustStates(t, l, "swap"), database.FieldKeyAuthorName)
	require.NotNil(t, st)
	require.True(t, st.IsRepairLock())
	kind, why := lockHold(st, "author")
	require.Equal(t, junkSkipRepairLocked, kind)
	require.Contains(t, why, "locked by a repair")
	row := *st
	row.LockSource = ""
	require.NoError(t, l.store.UpsertMetadataFieldState(&row))

	rev, err := audiobooks.NewRevertService(l.store).RevertOperation(swapTestOpID)
	require.Error(t, err)
	require.Equal(t, 1, rev.Failed, "only the author lock row is refused: %+v", rev)
	require.True(t, l.locked("swap", database.FieldKeyAuthorName), "the person's lock stays")
	require.False(t, l.locked("swap", database.FieldKeyTitle), "the repair's own title lock is lifted")
}

func mustStates(t *testing.T, l *swapLib, name string) []database.MetadataFieldState {
	t.Helper()
	sts, err := l.store.GetMetadataFieldStates(l.ids[name])
	require.NoError(t, err)
	return sts
}

// An apply cut at any point leaves a book the next plan finishes, and the
// finished book matches an uninterrupted apply.
func TestSwappedTitleAuthorFixer_InterruptedApplyIsFinishedByTheNextPlan(t *testing.T) {
	setup := func(t *testing.T) (*swapLib, string) {
		l := newSwapLib(t)
		id := l.add(swapBook{name: "swap", title: "read by Jack Voraces", narrator: "Jack Voraces",
			storedAuthor: "Ultimate Level 1_ Divine Creation", provTitle: "Ultimate Level 1: Divine Creation",
			provAuthor: "Shawn Wilson", prov: map[string]any{"narrator": "Jack Voraces"},
			files: []string{"/lib/S/Ultimate Level 1_ Divine Creation/book.m4b"}})
		return l, id
	}
	cutWriter := func(l *swapLib) *repairs.Writer {
		return repairs.NewWriter(l.store, l.store, swappedFixerID, "bulk_update", "repairs-").
			WithJournal(l.store, l.store, "op-cut").WithCredits(l.store).WithFieldStates(l.store)
	}
	finish := func(t *testing.T, l *swapLib) {
		plan, rows := l.plan()
		r := rows["swap"]
		require.True(t, r.Applicable(), "the next plan lists the half-done book: %s %s", r.Skipped, r.SkipReason)
		out := l.apply(plan, []string{r.RowID})
		require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
		wilson := l.authorID("Shawn Wilson")
		require.Equal(t, "Ultimate Level 1: Divine Creation", l.book("swap").Title)
		require.Equal(t, wilson, *l.book("swap").AuthorID)
		require.Equal(t, []int{wilson}, l.credits("swap"))
		require.True(t, l.locked("swap", database.FieldKeyTitle))
		require.True(t, l.locked("swap", database.FieldKeyAuthorName))
	}
	t.Run("cut after the title write, before its lock", func(t *testing.T) {
		l, id := setup(t)
		_, err := l.store.ModifyBook(id, func(b *database.Book) error { b.Title = "Ultimate Level 1: Divine Creation"; return nil })
		require.NoError(t, err)
		finish(t, l)
	})
	t.Run("cut after the title and author locks", func(t *testing.T) {
		l, id := setup(t)
		w := cutWriter(l)
		require.NoError(t, writeTitleOnly(w, l.store, id, "read by Jack Voraces", "Ultimate Level 1: Divine Creation"))
		require.NoError(t, w.LockFields(id, database.FieldKeyAuthorName))
		finish(t, l)
	})
	t.Run("cut after the credit move, before the primary", func(t *testing.T) {
		l, id := setup(t)
		w := cutWriter(l)
		require.NoError(t, writeTitleOnly(w, l.store, id, "read by Jack Voraces", "Ultimate Level 1: Divine Creation"))
		require.NoError(t, w.LockFields(id, database.FieldKeyAuthorName))
		wilson, err := l.store.CreateAuthor("Shawn Wilson")
		require.NoError(t, err)
		require.NoError(t, l.store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: wilson.ID, Role: "author"}}))
		require.Equal(t, l.holder["swap"], *l.book("swap").AuthorID)
		finish(t, l)
		// The finishing op's revert puts the primary back.
		rev, err := audiobooks.NewRevertService(l.store).RevertOperation(swapTestOpID)
		require.NoError(t, err)
		require.Zero(t, rev.Failed, "%+v", rev)
		require.Equal(t, l.holder["swap"], *l.book("swap").AuthorID)
	})
}

// fetchedByProvider reads every history row: a fetch that filled the
// narrator long ago (behind more than a page of later rows) still makes the
// narrator circular.
func TestSwappedTitleAuthorFixer_FetchedNarratorFoundBehindAPage(t *testing.T) {
	l := newSwapLib(t)
	id := l.add(swapBook{name: "deep", title: "read by narrator", narrator: "Pat Voice", storedAuthor: "Vengeance",
		provTitle: "Vengeance", provAuthor: "Dee Writer", prov: map[string]any{"narrator": "Pat Voice"},
		files: []string{"/lib/D/Vengeance/book.m4b"}})
	field := database.HistoryFieldName("narrator")
	old := time.Now().Add(-time.Hour)
	v := `"Pat Voice"`
	require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id, Field: field,
		NewValue: &v, ChangeType: "fetched", ChangedAt: old}))
	for i := 0; i < 60; i++ {
		require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id, Field: field,
			NewValue: &v, ChangeType: "bulk_update", ChangedAt: old.Add(time.Duration(i+1) * time.Second)}))
	}
	_, rows := l.plan()
	require.Equal(t, junkSkipNeedsManual, rows["deep"].Skipped, rows["deep"].SkipReason)
}

// A person's title lock kept only in the pre-migration blob holds the row at
// plan time, and the write re-check refuses it too: nothing is written and
// no live undo row is left.
func TestJunkTitleFixer_BlobTitleLockHolds(t *testing.T) {
	jl := newJunkLib(t)
	id := jl.ids["folder"]
	require.NoError(t, jl.store.SetUserPreference(metastate.Key(id), `{"title":{"override_locked":true}}`))
	_, _, rows := jl.plan(t)
	require.Equal(t, junkSkipUserLocked, rows["folder"].Skipped, rows["folder"].SkipReason)

	before, err := jl.store.GetBookByID(id)
	require.NoError(t, err)
	w := repairs.NewWriter(jl.store, jl.store, junkTitlesFixerID, "bulk_update", "repairs-").
		WithJournal(jl.store, jl.store, "op-blob").WithFieldStates(jl.store)
	require.ErrorIs(t, writeTitleOnly(w, jl.store, id, before.Title, "Good Book"), repairs.ErrChangedSincePlan)
	after, err := jl.store.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, before.Title, after.Title)
	ch, err := jl.store.GetOperationChanges("op-blob")
	require.NoError(t, err)
	for _, c := range ch {
		require.NotNil(t, c.RevertedAt, "no live undo row for a refused write: %+v", c)
	}
}

// An apply cut at any point is finished by the next plan even when the row
// was only weakly tied (a long title, no narrator column, the narrator tie
// coming from the "read by X" title the title write replaces). The author
// lock goes first, and a book carrying it is a continuation: the next plan
// waives the tie requirement it already passed. The finishing op owns every
// lock it relies on.
func TestSwappedTitleAuthorFixer_ContinuationFinishesWeaklyTiedRows(t *testing.T) {
	type cut int
	const (
		afterAuthorLock cut = iota
		afterTitle
		afterCredits
	)
	cases := []struct {
		name, title, narrator string
		// filledNarrator: the narrator column was filled in by a metadata
		// fetch, so it does not count as a tie (circular).
		filledNarrator bool
		at             cut
	}{
		{name: "V1 long title, no tie, cut after the title", title: "read by narrator", at: afterTitle},
		{name: "V1a long title, no tie, cut after the author lock", title: "read by narrator", at: afterAuthorLock},
		{name: "V1b long title, no tie, cut after the credit move", title: "read by narrator", at: afterCredits},
		{name: "V2 narrator tie only from the read-by title", title: "read by Jack Voraces", narrator: "Jack Voraces",
			filledNarrator: true, at: afterTitle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newSwapLib(t)
			id := l.add(swapBook{name: "swap", title: c.title, narrator: c.narrator,
				storedAuthor: "Ultimate Level 1_ Divine Creation", provTitle: "Ultimate Level 1: Divine Creation",
				provAuthor: "Shawn Wilson", prov: map[string]any{"narrator": "Jack Voraces"}, files: []string{"/lib/S/X/book.m4b"}})
			if c.filledNarrator {
				v := `"Jack Voraces"`
				require.NoError(t, l.store.RecordMetadataChange(&database.MetadataChangeRecord{BookID: id,
					Field: database.HistoryFieldName("narrator"), NewValue: &v, ChangeType: "fetched"}))
			}
			_, rows := l.plan()
			first, listed := rows["swap"]
			require.True(t, listed, "the full row is planned")
			require.True(t, first.Applicable(), "%s %s", first.Skipped, first.SkipReason)

			cw := repairs.NewWriter(l.store, l.store, swappedFixerID, "bulk_update", "repairs-").
				WithJournal(l.store, l.store, "op-cut").WithCredits(l.store).WithFieldStates(l.store)
			require.NoError(t, cw.LockFields(id, database.FieldKeyAuthorName))
			if c.at >= afterTitle {
				require.NoError(t, writeTitleOnly(cw, l.store, id, c.title, "Ultimate Level 1: Divine Creation"))
			}
			if c.at >= afterCredits {
				wilson, err := l.store.CreateAuthor("Shawn Wilson")
				require.NoError(t, err)
				require.NoError(t, l.store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: wilson.ID, Role: "author"}}))
			}

			plan, rows := l.plan()
			r, listed := rows["swap"]
			require.True(t, listed, "the next plan lists the cut book")
			require.True(t, r.Applicable(), "the next plan finishes the cut apply: %s %s", r.Skipped, r.SkipReason)
			out := l.apply(plan, []string{r.RowID})
			require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
			wilson := l.authorID("Shawn Wilson")
			b := l.book("swap")
			require.Equal(t, "Ultimate Level 1: Divine Creation", b.Title)
			require.Equal(t, wilson, *b.AuthorID)
			require.Contains(t, l.credits("swap"), wilson, "the primary author is in the credit list")
			for _, f := range []string{database.FieldKeyTitle, database.FieldKeyAuthorName} {
				st := fieldStateOf(mustStates(t, l, "swap"), f)
				require.NotNil(t, st, f)
				require.Equal(t, database.RepairLockSource(swapTestOpID), st.LockSource, "%s is locked under the finishing op", f)
			}
		})
	}
}

// A person who picks a match by hand after a repair, changing the title,
// makes the title lock theirs: reverting the repair then leaves their title
// and their lock alone (the paired lock check), and undoes the rest.
func TestSwappedTitleAuthorFixer_RevertAfterHandPickedApplyKeepsThePersonsTitle(t *testing.T) {
	l := newSwapLib(t)
	l.populate()
	plan, rows := l.plan()
	out := l.apply(plan, []string{rows["swap"].RowID})
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	id := l.ids["swap"]

	_, err := metafetch.NewService(l.store).ApplyMetadataCandidate(id,
		metafetch.MetadataCandidate{Title: "Ultimate Level One", Source: "audible"}, []string{"title"})
	require.NoError(t, err)
	require.Equal(t, "Ultimate Level One", l.book("swap").Title)
	st := fieldStateOf(mustStates(t, l, "swap"), database.FieldKeyTitle)
	require.NotNil(t, st)
	require.True(t, st.OverrideLocked)
	require.Empty(t, st.LockSource, "the hand-picked apply claimed the lock")

	_, _ = audiobooks.NewRevertService(l.store).RevertOperation(swapTestOpID)
	require.Equal(t, "Ultimate Level One", l.book("swap").Title, "the person's title is not reverted to junk")
	require.True(t, l.locked("swap", database.FieldKeyTitle), "and their lock stays")
	require.Equal(t, l.holder["swap"], *l.book("swap").AuthorID, "the author move is undone")

	// The same when the person only locked the repaired title by hand.
	l2 := newSwapLib(t)
	l2.populate()
	plan2, rows2 := l2.plan()
	require.Equal(t, 1, l2.apply(plan2, []string{rows2["swap"].RowID}).Applied)
	row := *fieldStateOf(mustStates(t, l2, "swap"), database.FieldKeyTitle)
	row.LockSource = ""
	require.NoError(t, l2.store.UpsertMetadataFieldState(&row))
	_, _ = audiobooks.NewRevertService(l2.store).RevertOperation(swapTestOpID)
	require.Equal(t, "Ultimate Level 1: Divine Creation", l2.book("swap").Title)
	require.True(t, l2.locked("swap", database.FieldKeyTitle))
}
