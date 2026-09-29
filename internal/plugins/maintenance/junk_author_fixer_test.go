// file: internal/plugins/maintenance/junk_author_fixer_test.go
// version: 1.3.0
// guid: 3d080ff3-6e67-4d08-a849-8bc0996a8315
// last-edited: 2026-09-29

package maintenance

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// These tests run the real PebbleStore (junction, author lock, field locks,
// op journal and the op revert all behave as in production) and do NOT skip
// under -short.

const junkTestOpID = "op-junk-apply"

type junkFixture struct {
	t     *testing.T
	s     *database.PebbleStore
	fixer *junkAuthorFixer

	authors map[string]int // name -> id
	series  map[string]int
}

func newJunkFixture(t *testing.T) *junkFixture {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	p := &Plugin{deps: &fakeDeps{store: s}}
	return &junkFixture{t: t, s: s, fixer: newJunkAuthorFixer(p), authors: map[string]int{}, series: map[string]int{}}
}

func (f *junkFixture) author(name string) int {
	f.t.Helper()
	if id, ok := f.authors[name]; ok {
		return id
	}
	a, err := f.s.CreateAuthor(name)
	var gate *database.ImplausibleAuthorNameError
	if errors.As(err, &gate) {
		// Prod holds rows the creation gate now refuses ("read by narrator",
		// "Book 1 (Unabridged)"): make a stand-in and rename it, which the
		// gate does not cover, so those classes run through plan and apply.
		a, err = f.s.CreateAuthor("Stand In " + strconv.Itoa(len(f.authors)+1))
		require.NoError(f.t, err)
		require.NoError(f.t, f.s.UpdateAuthorName(a.ID, name))
		got, gerr := f.s.GetAuthorByID(a.ID)
		require.NoError(f.t, gerr)
		require.Equal(f.t, name, got.Name, "renamed stand-in")
	}
	require.NoError(f.t, err, name)
	f.authors[name] = a.ID
	return a.ID
}

func (f *junkFixture) mkSeries(name, owner string) int {
	f.t.Helper()
	id := f.author(owner)
	s, err := f.s.CreateSeries(name, &id)
	require.NoError(f.t, err)
	f.series[name] = s.ID
	return s.ID
}

type junkBookSpec struct {
	title, path, author string
	// coAuthor is a real co-author credit (role "author", position 1).
	coAuthor string
	// narratorCredit is a narrator-role credit in the author junction (prod
	// stores these), after any co-author.
	narratorCredit string
	// narrator is the book's Narrator string; bookNarrators are rows in the
	// book_narrators table.
	narrator      string
	bookNarrators []string
	series        string
	tags          map[string]string
}

// book creates a book whose junction and primary both name spec.author (plus
// an optional co-author and narrator credit) with one file carrying spec.tags.
func (f *junkFixture) book(spec junkBookSpec) string {
	f.t.Helper()
	aid := f.author(spec.author)
	b := &database.Book{Title: spec.title, FilePath: spec.path, AuthorID: &aid, IsPrimaryVersion: new(true)}
	if spec.narrator != "" {
		b.Narrator = &spec.narrator
	}
	if spec.series != "" {
		sid := f.series[spec.series]
		require.NotZero(f.t, sid, "series %q not made", spec.series)
		b.SeriesID = &sid
	}
	created, err := f.s.CreateBook(b)
	require.NoError(f.t, err)
	credits := []database.BookAuthor{{BookID: created.ID, AuthorID: aid, Role: "author", Position: 0}}
	if spec.coAuthor != "" {
		credits = append(credits, database.BookAuthor{BookID: created.ID, AuthorID: f.author(spec.coAuthor), Role: "author", Position: len(credits)})
	}
	if spec.narratorCredit != "" {
		credits = append(credits, database.BookAuthor{BookID: created.ID, AuthorID: f.author(spec.narratorCredit), Role: "narrator", Position: len(credits)})
	}
	require.NoError(f.t, f.s.SetBookAuthors(created.ID, credits))
	if len(spec.bookNarrators) > 0 {
		var ns []database.BookNarrator
		for i, name := range spec.bookNarrators {
			n, err := f.s.CreateNarrator(name)
			require.NoError(f.t, err)
			ns = append(ns, database.BookNarrator{BookID: created.ID, NarratorID: n.ID, Role: "narrator", Position: i})
		}
		require.NoError(f.t, f.s.SetBookNarrators(created.ID, ns))
	}
	require.NoError(f.t, f.s.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: spec.path + "/01.m4b", RawTags: spec.tags}))
	return created.ID
}

func (f *junkFixture) plan() *repairs.PlanResult {
	f.t.Helper()
	all, err := f.s.GetAllSeries()
	require.NoError(f.t, err)
	res, err := repairs.RunPlan(context.Background(), f.fixer, nil,
		repairs.PlanDeps{Guard: f.s, Series: repairs.SeriesNamesFrom(all)}, &fakeReporter{})
	require.NoError(f.t, err)
	return res
}

// apply applies rowIDs (nil: every applicable row of the plan).
func (f *junkFixture) apply(plan *repairs.PlanResult, rowIDs []string) *repairs.ApplyResult {
	f.t.Helper()
	if rowIDs == nil {
		for _, r := range plan.Rows {
			if r.Skipped == "" {
				rowIDs = append(rowIDs, r.RowID)
			}
		}
	}
	all, err := f.s.GetAllSeries()
	require.NoError(f.t, err)
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	res, err := repairs.RunApply(context.Background(), f.fixer, plan, "op-junk-plan", rowIDs, false,
		repairs.ApplyDeps{Guard: f.s, Series: repairs.SeriesNamesFrom(all), Writer: w, OpID: junkTestOpID}, &fakeReporter{})
	require.NoError(f.t, err)
	return res
}

func (f *junkFixture) credits(bookID string) []int {
	f.t.Helper()
	cs, err := f.s.GetBookAuthors(bookID)
	require.NoError(f.t, err)
	out := make([]int, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.AuthorID)
	}
	return out
}

func (f *junkFixture) primary(bookID string) *int {
	f.t.Helper()
	b, err := f.s.GetBookByID(bookID)
	require.NoError(f.t, err)
	require.NotNil(f.t, b)
	return b.AuthorID
}

func rowByID(plan *repairs.PlanResult, id string) *repairs.Row {
	for i := range plan.Rows {
		if plan.Rows[i].RowID == id {
			return &plan.Rows[i]
		}
	}
	return nil
}

// seedJunkLibrary builds one book per behaviour. Returned: name -> book id.
func seedJunkLibrary(f *junkFixture) map[string]string {
	const (
		wayOfShadows = "The Way of Shadows" // work_title, strong
		demonCycle   = "Demon Cycle Series" // series_name, strong
		graphicAudio = "GraphicAudio"       // publisher_or_studio, strong
	)
	f.author("Brent Weeks")
	f.author("Peter V. Brett")
	f.author("J. K. Rowling")
	f.mkSeries("Demon Cycle", "Peter V. Brett")
	f.mkSeries("Harry Potter", "J. K. Rowling")
	f.mkSeries("Brandon Sanderson", "Brent Weeks") // a junk SERIES named for a real author

	b := map[string]string{}
	b["tags"] = f.book(junkBookSpec{title: "Night Angel One", path: "/lib/bk-tags", author: wayOfShadows,
		tags: map[string]string{"artist": "Brent Weeks"}})
	b["prov"] = f.book(junkBookSpec{title: "Shadow Book", path: "/lib/bk-prov", author: wayOfShadows, coAuthor: "Peter V. Brett"})
	b["sib"] = f.book(junkBookSpec{title: "The Desert Spear", path: "/lib/bk-sib", author: "Peter V. Brett", series: "Demon Cycle"})
	b["series"] = f.book(junkBookSpec{title: "The Warded Man", path: "/lib/bk-series", author: demonCycle, series: "Demon Cycle"})
	b["noser"] = f.book(junkBookSpec{title: "The Skull Throne", path: "/lib/bk-noser", author: demonCycle,
		tags: map[string]string{"album_artist": "Peter V. Brett"}})
	b["folder"] = f.book(junkBookSpec{title: "Brayan's Gold", path: "/lib/Peter V. Brett/bk-folder", author: graphicAudio})
	b["none"] = f.book(junkBookSpec{title: "Nothing Known", path: "/lib/bk-none", author: graphicAudio})
	b["amb"] = f.book(junkBookSpec{title: "Two Artists", path: "/lib/bk-amb", author: graphicAudio,
		tags: map[string]string{"artist": "Brent Weeks", "album_artist": "Peter V. Brett"}})
	b["create"] = f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: graphicAudio,
		tags: map[string]string{"artist": "Robin Hobb"}})
	b["locked"] = f.book(junkBookSpec{title: "Locked One", path: "/lib/bk-locked", author: wayOfShadows,
		tags: map[string]string{"artist": "Brent Weeks"}})
	b["itunes"] = f.book(junkBookSpec{title: "Tunes One", path: "/mnt/data/books/itunes/bk-itunes", author: wayOfShadows,
		tags: map[string]string{"artist": "Brent Weeks"}})
	b["dw"] = f.book(junkBookSpec{title: "Spare Parts", path: "/lib/Big Finish/Doctor Who/bk-dw", author: graphicAudio,
		tags: map[string]string{"artist": "Brent Weeks"}})
	// Weak, confirmed: a character credited as author, the file says who wrote it.
	b["rowling"] = f.book(junkBookSpec{title: "Philosopher's Stone", path: "/lib/bk-rowling", author: "J. K. Rowling", series: "Harry Potter"})
	b["hp"] = f.book(junkBookSpec{title: "Chamber of Secrets", path: "/lib/bk-hp", author: "Harry Potter",
		tags: map[string]string{"artist": "J. K. Rowling"}})
	// Weak, NOT confirmed: a real author whose name is also a (junk) series.
	b["bs"] = f.book(junkBookSpec{title: "Elantris", path: "/lib/bk-bs", author: "Brandon Sanderson",
		tags: map[string]string{"artist": "Brandon Sanderson"}})
	b["bs-series"] = f.book(junkBookSpec{title: "Swapped Fields", path: "/lib/bk-bs-series", author: "Brent Weeks", series: "Brandon Sanderson"})
	// Real authors the name classifier must never flag.
	b["saga"] = f.book(junkBookSpec{title: "Silk and Straw", path: "/lib/bk-saga", author: "Junichi Saga"})
	b["anna"] = f.book(junkBookSpec{title: "A Step from Heaven", path: "/lib/bk-anna", author: "An Na"})

	// Provider: a cached candidate whose title agrees names the author.
	for field, v := range map[string]string{"title": "Shadow Book", "author_name": "Brent Weeks"} {
		enc, err := metastate.Encode(v)
		require.NoError(f.t, err)
		require.NoError(f.t, f.s.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: b["prov"], Field: field, FetchedValue: enc}))
	}
	require.NoError(f.t, database.RecordUserOverrides(f.s, b["locked"], map[string]any{database.FieldKeyAuthorName: wayOfShadows}))
	return b
}

func TestJunkAuthorFixer_PlanDecisions(t *testing.T) {
	f := newJunkFixture(t)
	b := seedJunkLibrary(f)
	plan := f.plan()

	way, demon, ga := f.authors["The Way of Shadows"], f.authors["Demon Cycle Series"], f.authors["GraphicAudio"]
	row := func(authorID int, key string) *repairs.Row {
		t.Helper()
		r := rowByID(plan, junkAuthorRowID(authorID, b[key]))
		require.NotNil(t, r, "no row for %s", key)
		return r
	}
	cases := []struct {
		key, skip, decision, source, target string
		author                              int
	}{
		{key: "tags", author: way, decision: junkAuthorDecRelink, source: junkAuthorSrcTags, target: "Brent Weeks"},
		{key: "prov", author: way, decision: junkAuthorDecRelink, source: junkAuthorSrcProvider, target: "Brent Weeks"},
		{key: "series", author: demon, decision: junkAuthorDecRelink, source: junkAuthorSrcSeries, target: "Peter V. Brett"},
		{key: "noser", author: demon, decision: junkAuthorDecRelink, source: junkAuthorSrcTags, target: "Peter V. Brett"},
		{key: "folder", author: ga, decision: junkAuthorDecRelink, source: junkAuthorSrcPath, target: "Peter V. Brett"},
		{key: "none", author: ga, decision: junkAuthorDecUnlink},
		{key: "create", author: ga, decision: junkAuthorDecCreate, source: junkAuthorSrcTags, target: "Robin Hobb"},
		{key: "amb", author: ga, skip: junkAuthorSkipAmbiguous},
		{key: "locked", author: way, skip: junkAuthorSkipUserLocked},
		{key: "itunes", author: way, skip: repairs.SkipITunes},
		{key: "dw", author: ga, skip: repairs.SkipOwnerManual},
		{key: "hp", author: f.authors["Harry Potter"], decision: junkAuthorDecRelink, source: junkAuthorSrcTags, target: "J. K. Rowling"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			r := row(c.author, c.key)
			require.Equal(t, c.skip, r.Skipped, "skip reason: %s", r.SkipReason)
			require.Equal(t, []string{b[c.key]}, r.BookIDs, "every row lists its book")
			if c.skip != "" {
				return
			}
			require.Equal(t, c.decision, r.Proposed["decision"], r.Reason)
			if c.decision == junkAuthorDecUnlink {
				require.Equal(t, repairs.RiskReview, r.Risk, "needs_manual rows are always review")
				return
			}
			require.Equal(t, c.source, r.Proposed["source"], r.Reason)
			require.Equal(t, c.target, r.Proposed["author"])
		})
	}
	// Series proposal: only for the series-name author's book with no series.
	require.Equal(t, "Demon Cycle", row(demon, "noser").Proposed["series"])
	require.Empty(t, row(demon, "series").Proposed["series"], "a book with a series keeps it")
	require.Equal(t, "character", row(f.authors["Harry Potter"], "hp").Current["class"])
	require.Equal(t, "weak", row(f.authors["Harry Potter"], "hp").Current["strength"])

	// Never flagged: real authors, including one whose name is a junk series
	// and whose own file names him (weak, unconfirmed).
	for _, name := range []string{"Brandon Sanderson", "Junichi Saga", "An Na", "Brent Weeks", "Peter V. Brett", "J. K. Rowling"} {
		id := f.authors[name]
		for _, r := range plan.Rows {
			require.NotEqual(t, id, mustAuthorOfRow(t, r.RowID), "%s must not be flagged: %s", name, r.Reason)
		}
	}
}

func mustAuthorOfRow(t *testing.T, rowID string) int {
	t.Helper()
	a, _, err := parseJunkAuthorRowID(rowID)
	require.NoError(t, err)
	return a
}

func TestJunkAuthorFixer_ReplanFingerprintIsStable(t *testing.T) {
	f := newJunkFixture(t)
	seedJunkLibrary(f)
	plan := f.plan()
	n := 0
	for _, r := range plan.Rows {
		if r.Skipped != "" {
			continue
		}
		fresh, err := f.fixer.Replan(context.Background(), nil, r, &fakeReporter{})
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, fresh.Fingerprint, "row %s moved with nothing changed", r.RowID)
		n++
	}
	require.Equal(t, 8, n, "applicable rows")
}

func TestJunkAuthorFixer_ApplyThenRevert(t *testing.T) {
	f := newJunkFixture(t)
	b := seedJunkLibrary(f)
	plan := f.plan()
	way, demon, ga := f.authors["The Way of Shadows"], f.authors["Demon Cycle Series"], f.authors["GraphicAudio"]
	brent, brett, rowling := f.authors["Brent Weeks"], f.authors["Peter V. Brett"], f.authors["J. K. Rowling"]

	before := map[string][]int{}
	for k, id := range b {
		before[k] = f.credits(id)
	}

	res := f.apply(plan, nil)
	require.Equal(t, 8, res.Applied, "by outcome: %v", res.ByOutcome)
	require.Zero(t, res.Failed)
	require.Zero(t, res.Partial)

	require.Equal(t, []int{brent}, f.credits(b["tags"]))
	require.Equal(t, brent, *f.primary(b["tags"]))
	require.Equal(t, []int{brent, brett}, f.credits(b["prov"]), "co-credit kept in place")
	require.Equal(t, []int{brett}, f.credits(b["series"]))
	require.Equal(t, []int{brett}, f.credits(b["folder"]))
	require.Equal(t, []int{rowling}, f.credits(b["hp"]))
	require.Empty(t, f.credits(b["none"]), "no evidence: junk credit removed")
	require.Nil(t, f.primary(b["none"]))
	hobb, err := f.s.GetAuthorByName("Robin Hobb")
	require.NoError(t, err)
	require.NotNil(t, hobb, "missing target author created")
	require.Equal(t, []int{hobb.ID}, f.credits(b["create"]))
	noser, err := f.s.GetBookByID(b["noser"])
	require.NoError(t, err)
	require.NotNil(t, noser.SeriesID)
	require.Equal(t, f.series["Demon Cycle"], *noser.SeriesID, "series proposal applied")

	// Guarded and held rows untouched.
	for _, k := range []string{"amb", "locked", "itunes", "dw"} {
		require.Equal(t, before[k], f.credits(b[k]), k)
	}
	// The junk author rows are NOT deleted (left for purge-empty-authors).
	for _, id := range []int{way, demon, ga} {
		a, err := f.s.GetAuthorByID(id)
		require.NoError(t, err)
		require.NotNil(t, a)
	}

	// No double apply: the same plan again changes nothing.
	again := f.apply(plan, nil)
	require.Zero(t, again.Applied)
	require.Equal(t, 8, again.ChangedSincePlan, "by outcome: %v", again.ByOutcome)

	// The op revert puts every credit, primary and series back.
	rev, err := audiobooks.NewRevertService(f.s).RevertOperation(junkTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	for k, id := range b {
		require.Equal(t, before[k], f.credits(id), "credits of %s after revert", k)
	}
	require.Equal(t, ga, *f.primary(b["none"]))
	require.Equal(t, way, *f.primary(b["tags"]))
	noser, err = f.s.GetBookByID(b["noser"])
	require.NoError(t, err)
	require.Nil(t, noser.SeriesID, "series link reverted")
	hobb, err = f.s.GetAuthorByName("Robin Hobb")
	require.NoError(t, err)
	require.Nil(t, hobb, "created author removed by the revert")
}

func TestJunkAuthorFixer_ChangedSincePlanAndLockAfterPlan(t *testing.T) {
	f := newJunkFixture(t)
	b := seedJunkLibrary(f)
	plan := f.plan()
	way := f.authors["The Way of Shadows"]
	tagsRow := junkAuthorRowID(way, b["tags"])
	provRow := junkAuthorRowID(way, b["prov"])

	// Someone adds a co-author after the plan.
	cur, err := f.s.GetBookAuthors(b["tags"])
	require.NoError(t, err)
	cur = append(cur, database.BookAuthor{BookID: b["tags"], AuthorID: f.authors["Peter V. Brett"], Role: "author", Position: 1})
	require.NoError(t, f.s.SetBookAuthors(b["tags"], cur))
	// The user locks the author after the plan.
	require.NoError(t, database.RecordUserOverrides(f.s, b["prov"], map[string]any{database.FieldKeyAuthorName: "Mine"}))

	res := f.apply(plan, []string{tagsRow, provRow})
	require.Zero(t, res.Applied, "by outcome: %v", res.ByOutcome)
	require.Equal(t, 2, res.ChangedSincePlan, "by outcome: %v", res.ByOutcome)
	require.Contains(t, f.credits(b["tags"]), way, "not written")
	require.Contains(t, f.credits(b["prov"]), way, "not written")
}

func TestJunkAuthorFixer_ParamsScopeAndWeakOff(t *testing.T) {
	f := newJunkFixture(t)
	seedJunkLibrary(f)
	all, err := f.s.GetAllSeries()
	require.NoError(t, err)
	res, err := repairs.RunPlan(context.Background(), f.fixer, []byte(`{"include_weak":false}`),
		repairs.PlanDeps{Guard: f.s, Series: repairs.SeriesNamesFrom(all)}, &fakeReporter{})
	require.NoError(t, err)
	for _, r := range res.Rows {
		require.Equal(t, "strong", r.Current["strength"], r.RowID)
	}
	ga := f.authors["GraphicAudio"]
	res, err = repairs.RunPlan(context.Background(), f.fixer, []byte(`{"author_ids":[`+strconv.Itoa(ga)+`]}`),
		repairs.PlanDeps{Guard: f.s, Series: repairs.SeriesNamesFrom(all)}, &fakeReporter{})
	require.NoError(t, err)
	require.NotEmpty(t, res.Rows)
	for _, r := range res.Rows {
		require.Equal(t, ga, mustAuthorOfRow(t, r.RowID))
	}
}

func TestJunkAuthorRowID_RoundTrip(t *testing.T) {
	a, b, err := parseJunkAuthorRowID(junkAuthorRowID(42, "01ABC"))
	require.NoError(t, err)
	require.Equal(t, 42, a)
	require.Equal(t, "01ABC", b)
	for _, bad := range []string{"", "x", "a0:b1", "aX:b1", "a1:b", "a1"} {
		_, _, err := parseJunkAuthorRowID(bad)
		require.Error(t, err, bad)
	}
}

// The owner-manual rule reads the author name too, not only paths and series:
// a Big Finish studio credit on a neutral path is never relinked.
func TestJunkAuthorFixer_OwnerManualByAuthorName(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Brent Weeks")
	bf := f.book(junkBookSpec{title: "The Holy Terror", path: "/lib/bk-neutral", author: "Big Finish Productions",
		tags: map[string]string{"artist": "Brent Weeks"}})
	plan := f.plan()
	r := rowByID(plan, junkAuthorRowID(f.authors["Big Finish Productions"], bf))
	require.NotNil(t, r)
	require.Equal(t, repairs.SkipOwnerManual, r.Skipped, r.SkipReason)
}

// "Unknown Author" is relinked away from when there is evidence, but a book is
// never left with no author instead of it.
func TestJunkAuthorFixer_UnknownAuthorIsNeverUnlinked(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Brent Weeks")
	withTags := f.book(junkBookSpec{title: "Has Tags", path: "/lib/bk-unk-tags", author: database.UnknownAuthorName,
		tags: map[string]string{"artist": "Brent Weeks"}})
	bare := f.book(junkBookSpec{title: "No Evidence", path: "/lib/bk-unk-bare", author: database.UnknownAuthorName})
	plan := f.plan()
	unk := f.authors[database.UnknownAuthorName]
	r := rowByID(plan, junkAuthorRowID(unk, withTags))
	require.NotNil(t, r)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Brent Weeks", r.Proposed["author"])
	r = rowByID(plan, junkAuthorRowID(unk, bare))
	require.NotNil(t, r)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "no unlink away from the fallback credit")
}

// A weak row the plan confirms as junk is not a relink target for another
// junk row's book: the folder sibling credited to it is not evidence.
func TestJunkAuthorFixer_ConfirmedWeakRowIsNotATarget(t *testing.T) {
	f := newJunkFixture(t)
	f.author("Brent Weeks")
	// "Night Angel" is weak junk: another author's book is titled that.
	f.book(junkBookSpec{title: "Night Angel", path: "/lib/bk-na", author: "Brent Weeks"})
	// Its own book says Brent Weeks wrote it: confirmed.
	w := f.book(junkBookSpec{title: "Beyond the Shadows", path: "/lib/shared/w.m4b", author: "Night Angel",
		tags: map[string]string{"artist": "Brent Weeks"}})
	// A strong-junk book in the same folder, no evidence of its own.
	j := f.book(junkBookSpec{title: "Perfect Shadow", path: "/lib/shared/j.m4b", author: "GraphicAudio"})
	plan := f.plan()

	rw := rowByID(plan, junkAuthorRowID(f.authors["Night Angel"], w))
	require.NotNil(t, rw, "the weak row is confirmed")
	require.Equal(t, "Brent Weeks", rw.Proposed["author"])
	rj := rowByID(plan, junkAuthorRowID(f.authors["GraphicAudio"], j))
	require.NotNil(t, rj)
	require.NotEqual(t, "Night Angel", rj.Proposed["author"], "relinked junk to junk: %s", rj.Reason)
	require.Equal(t, junkAuthorDecUnlink, rj.Proposed["decision"], rj.Reason)

	// Replan agrees with the plan (the weak exclusion is the same both times).
	for _, r := range []*repairs.Row{rw, rj} {
		fresh, err := f.fixer.Replan(context.Background(), nil, *r, &fakeReporter{})
		require.NoError(t, err)
		require.Equal(t, r.Fingerprint, fresh.Fingerprint, r.RowID)
	}
}

// A create decision whose author appeared after the plan uses that row and
// journals no create (whose revert would delete a row this op never made).
func TestJunkAuthorFixer_CreateUsesARowMadeSincePlan(t *testing.T) {
	f := newJunkFixture(t)
	bk := f.book(junkBookSpec{title: "Assassin's Apprentice", path: "/lib/bk-create", author: "GraphicAudio",
		tags: map[string]string{"artist": "Robin Hobb"}})
	plan := f.plan()
	id := junkAuthorRowID(f.authors["GraphicAudio"], bk)
	require.Equal(t, junkAuthorDecCreate, rowByID(plan, id).Proposed["decision"])

	hobb, err := f.s.CreateAuthor("Robin Hobb")
	require.NoError(t, err)
	f.fixer.idxMu.Lock()
	f.fixer.idx = nil // the apply-time index is rebuilt; the plan row still says create
	f.fixer.idxMu.Unlock()
	res := f.apply(plan, []string{id})
	// The rebuilt index resolves Robin Hobb to the existing row, so the
	// decision (and the fingerprint) moved: refused, nothing written.
	require.Equal(t, 1, res.ChangedSincePlan, "by outcome: %v", res.ByOutcome)
	require.Equal(t, []int{f.authors["GraphicAudio"]}, f.credits(bk))

	// With the plan-time index still cached, Apply itself finds the row.
	plan = f.plan()
	_, err = f.s.CreateAuthor("Robin Hobb")
	require.NoError(t, err)
	f.fixer.idxMu.Lock()
	f.fixer.idx.byName = map[string][]database.Author{} // stale: no Robin Hobb
	f.fixer.idxMu.Unlock()
	fresh, err := f.fixer.Replan(context.Background(), nil, *rowByID(plan, id), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, junkAuthorDecCreate, fresh.Proposed["decision"])
	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	require.NoError(t, f.fixer.Apply(context.Background(), w, fresh))
	require.Equal(t, []int{hobb.ID}, f.credits(bk))
	changes, err := f.s.GetOperationChanges(junkTestOpID)
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeJunkAuthorCreate, c.ChangeType, "no create journaled for an existing row")
		require.NotEqual(t, undo.ChangeTypeTitleRelinkAuthorCreate, c.ChangeType, "no create journaled for an existing row")
	}
}
