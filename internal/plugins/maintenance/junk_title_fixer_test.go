// file: internal/plugins/maintenance/junk_title_fixer_test.go
// version: 1.2.0
// guid: 3a8d6f52-1e9c-4b07-92d4-6c5b0e8a7f13
// last-edited: 2026-09-28

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// junkLib is a real Pebble library with one book per decision the fixer
// makes. ids maps a fixture name to the book id the store assigned.
type junkLib struct {
	store *database.PebbleStore
	ids   map[string]string
}

func newJunkLib(t *testing.T) *junkLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	lib := &junkLib{store: st, ids: map[string]string{}}

	authorA, err := st.CreateAuthor("Author A")
	require.NoError(t, err)
	phipps, err := st.CreateAuthor("C. T. Phipps")
	require.NoError(t, err)
	dw, err := st.CreateSeries("Doctor Who: The Monthly Adventures", nil)
	require.NoError(t, err)

	add := func(name, title, bookPath string, author *int, series *int, files ...string) string {
		t.Helper()
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: bookPath, AuthorID: author, SeriesID: series, Format: "mp3"})
		require.NoError(t, err)
		for _, f := range files {
			require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f, Format: "mp3"}))
		}
		lib.ids[name] = b.ID
		return b.ID
	}
	a := &authorA.ID
	// ---- applicable ----
	add("folder", "read by narrator", "/lib/Author A/Good Book", a, nil,
		"/lib/Author A/Good Book/01.mp3", "/lib/Author A/Good Book/02.mp3")
	add("prefix", "01 - Lonely Book", "/lib/Author A/Lonely Book/01 - Lonely Book.mp3", a, nil,
		"/lib/Author A/Lonely Book/01 - Lonely Book.mp3")
	add("disc", "Disc 2", "/lib/Author A/Whole Work/Disc 2", a, nil,
		"/lib/Author A/Whole Work/Disc 2/01.mp3", "/lib/Author A/Whole Work/Disc 2/02.mp3")
	tr := add("transcribed", "Opening", "/lib/Author A/Some Folder", a, nil,
		"/lib/Author A/Some Folder/01.mp3", "/lib/Author A/Some Folder/02.mp3")
	_, err = st.ModifyBook(tr, func(b *database.Book) error {
		s := "The Spoken Name"
		b.TranscribedTitle = &s
		return nil
	})
	require.NoError(t, err)
	cand := add("candidate", "Unknown Title", "/lib/Author A/Cand Dir/zz.mp3", a, nil, "/lib/Author A/Cand Dir/zz.mp3")
	require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: cand, FetchedAt: time.Now(),
		Candidates: []json.RawMessage{
			json.RawMessage(`{"title":"Wrong Author Book","author":"Someone Else","score":0.95}`),
			json.RawMessage(`{"title":"Low Score Book","author":"Author A","score":0.2}`),
			json.RawMessage(`{"title":"Second Best","author":"Author A","score":0.6}`),
			json.RawMessage(`{"title":"Candidate Title","author":"author a","score":0.8}`),
		}}))
	// The cached search ran on "01 - Eldest" and its top hit is the series'
	// first book: the stripped title wins and Eragon is refused.
	paolini, err := st.CreateAuthor("Christopher Paolini")
	require.NoError(t, err)
	el := add("eldest-prefix", "01 - Eldest", "/lib/Christopher Paolini/Eldest/01 - Eldest.mp3", &paolini.ID, nil,
		"/lib/Christopher Paolini/Eldest/01 - Eldest.mp3")
	require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: el, FetchedAt: time.Now(),
		Candidates: []json.RawMessage{
			json.RawMessage(`{"title":"Eragon","author":"Christopher Paolini","score":0.9}`),
		}}))
	kate := "Kate Reading"
	named, err := st.CreateBook(&database.Book{Title: "Read by Kate Reading", FilePath: "/lib/Author A/Named Credit Book",
		AuthorID: a, Narrator: &kate, Format: "mp3"})
	require.NoError(t, err)
	for _, fp := range []string{"/lib/Author A/Named Credit Book/01.mp3", "/lib/Author A/Named Credit Book/02.mp3"} {
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: named.ID, FilePath: fp, Format: "mp3"}))
	}
	lib.ids["named-credit"] = named.ID
	// ---- fragments ----
	add("frag1", "01", "/lib/Eldest/Eldest/01.mp3", nil, nil, "/lib/Eldest/Eldest/01.mp3")
	add("frag2", "02", "/lib/Eldest/Eldest/02.mp3", nil, nil, "/lib/Eldest/Eldest/02.mp3")
	add("chapter-alone", "Chapter 3", "/lib/X/Solo/03.mp3", nil, nil, "/lib/X/Solo/03.mp3")
	add("existing", "Existing Title", "/lib/Author A/Existing Title.m4b", a, nil, "/lib/Author A/Existing Title.m4b")
	add("dup", "Intro", "/lib/Author A/Existing Title", a, nil,
		"/lib/Author A/Existing Title/01.mp3", "/lib/Author A/Existing Title/02.mp3")
	// The parent owns a.mp3; the fragment is imported LAST, so the
	// single-slot path index names the fragment itself. Multi-file and not a
	// chapter title, so only the ownership check can catch it.
	add("parent", "Big Parent", "/lib/Parent/Big", a, nil, "/lib/Parent/Big/a.mp3", "/lib/Parent/Big/b.mp3")
	add("owned", "Opening", "/lib/Moved/Opening", nil, nil, "/lib/Parent/Big/a.mp3", "/lib/Moved/Opening/x.mp3")
	// The organizer-moved chapter: author "Eldest", title "98", alone.
	eldest, err := st.CreateAuthor("Eldest")
	require.NoError(t, err)
	add("eldest98", "98", "/lib/Eldest/02/98/98.mp3", &eldest.ID, nil, "/lib/Eldest/02/98/98.mp3")
	// ---- skipped for other reasons ----
	locked := add("locked", "Unknown Title", "/lib/Author A/Locked Folder", a, nil,
		"/lib/Author A/Locked Folder/01.mp3", "/lib/Author A/Locked Folder/02.mp3")
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: locked, Field: "title",
		OverrideLocked: true, UpdatedAt: time.Now()}))
	add("manual", "read by narrator", "", nil, nil)
	// The one-level climb above a junk-named folder lands on "Books": refused.
	add("generic", "Unknown", "/lib/Books/Unknown/Unknown.mp3", nil, nil, "/lib/Books/Unknown/Unknown.mp3")
	person := add("person", "read by narrator", "/lib/Someone/C. T. Phipps", nil, nil,
		"/lib/Someone/C. T. Phipps/01.mp3", "/lib/Someone/C. T. Phipps/02.mp3")
	require.NoError(t, st.SetBookAuthors(person, []database.BookAuthor{{BookID: person, AuthorID: phipps.ID, Role: "author"}}))
	add("bf-title", "Big Finish Ident", "/lib/Neutral/Real Title", nil, nil,
		"/lib/Neutral/Real Title/01.mp3", "/lib/Neutral/Real Title/02.mp3")
	add("itunes", "Intro", "/lib/Neutral/Other", nil, nil,
		"/mnt/data/books/itunes/Other/Title Here/01.mp3", "/mnt/data/books/itunes/Other/Title Here/02.mp3")
	add("dw-series", "read by narrator", "/lib/Plain/Series Title", nil, &dw.ID,
		"/lib/Plain/Series Title/01.mp3", "/lib/Plain/Series Title/02.mp3")
	// The PROPOSAL marks Torchwood content; path, title and series do not.
	tw := add("owner-proposal", "Opening", "/lib/Neutral/Plain Folder", nil, nil,
		"/lib/Neutral/Plain Folder/01.mp3", "/lib/Neutral/Plain Folder/02.mp3")
	_, err = st.ModifyBook(tw, func(b *database.Book) error {
		s := "Torchwood: The Dead Line"
		b.TranscribedTitle = &s
		return nil
	})
	require.NoError(t, err)
	fetched := `"read by narrator"`
	prov := add("provider", "read by narrator", "/lib/Author A/Provider Folder", a, nil,
		"/lib/Author A/Provider Folder/01.mp3", "/lib/Author A/Provider Folder/02.mp3")
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: prov, Field: "title",
		FetchedValue: &fetched, UpdatedAt: time.Now()}))
	// A bare unpadded number beside other books of its author may be the
	// real title: needs a person, never a (possible) fragment.
	add("bare13", "13", "/lib/Author A/13.m4b", a, nil, "/lib/Author A/13.m4b")
	add("unabridged", "read by narrator", "/lib/Author B/Dune (Unabridged)", nil, nil,
		"/lib/Author B/Dune (Unabridged)/01.mp3", "/lib/Author B/Dune (Unabridged)/02.mp3")
	// "Read by Moonlight" is a title, and nothing may climb to "lib" for it.
	add("moonlight", "Read by Moonlight", "/lib/A/Read by Moonlight.m4b", nil, nil, "/lib/A/Read by Moonlight.m4b")
	// ---- real titles: never rows ----
	add("robot", "I, Robot", "/lib/Asimov/I, Robot.m4b", nil, nil, "/lib/Asimov/I, Robot.m4b")
	add("etranger", "l'Étranger", "/lib/Camus/l'Étranger.m4b", nil, nil, "/lib/Camus/l'Étranger.m4b")
	add("1984", "1984", "/lib/Orwell/1984.m4b", nil, nil, "/lib/Orwell/1984.m4b")
	return lib
}

func (l *junkLib) plan(t *testing.T) (*junkTitleFixer, *repairs.PlanResult, map[string]repairs.Row) {
	t.Helper()
	p := &Plugin{deps: fakeDeps{store: l.store}, standDownWait: noWait}
	f := newJunkTitleFixer(p)
	series, err := l.store.GetAllSeries()
	require.NoError(t, err)
	res, err := repairs.RunPlan(context.Background(), f, nil,
		repairs.PlanDeps{Guard: l.store, Series: repairs.SeriesNamesFrom(series)}, &fakeReporter{})
	require.NoError(t, err)
	byName := map[string]repairs.Row{}
	for name, id := range l.ids {
		for _, r := range res.Rows {
			if r.RowID == id {
				byName[name] = r
			}
		}
	}
	return f, res, byName
}

func TestJunkTitleFixer_PlanDecisions(t *testing.T) {
	lib := newJunkLib(t)
	_, res, rows := lib.plan(t)

	applicable := map[string]string{
		"folder":      "Good Book",
		"prefix":      "Lonely Book",
		"disc":        "Whole Work",
		"transcribed": "The Spoken Name",
		"candidate":   "Candidate Title",
		// highest score among the trustworthy candidates, not the first
		"eldest-prefix": "Eldest",
		"named-credit":  "Named Credit Book",
	}
	for name, want := range applicable {
		r, ok := rows[name]
		require.True(t, ok, "%s: no row", name)
		require.True(t, r.Applicable(), "%s: skipped %s (%s)", name, r.Skipped, r.SkipReason)
		require.Equal(t, want, r.Proposed["title"], name)
		require.Equal(t, []string{lib.ids[name]}, r.BookIDs, "%s: one book per row", name)
	}
	require.Contains(t, rows["transcribed"].Reason, "transcription")
	require.Equal(t, repairs.RiskLow, rows["prefix"].Risk)
	require.Contains(t, rows["eldest-prefix"].Reason, "title_prefix_stripped")
	require.NotContains(t, rows["eldest-prefix"].Reason, "Eragon")

	skipped := map[string]string{
		// possible: only the shape says fragment
		"frag1":         junkSkipPossibleFragment,
		"frag2":         junkSkipPossibleFragment,
		"chapter-alone": junkSkipPossibleFragment,
		"dup":           junkSkipPossibleFragment,
		"eldest98":      junkSkipPossibleFragment,
		// proven: another book owns one of its files
		"owned":          junkSkipFragment,
		"provider":       junkSkipProviderTitle,
		"owner-proposal": repairs.SkipOwnerManual,
		"bare13":         junkSkipNeedsManual,
		"unabridged":     junkSkipNeedsManual,
		"generic":        junkSkipNeedsManual,
		"locked":         junkSkipUserLocked,
		"manual":         junkSkipNeedsManual,
		"person":         junkSkipNeedsManual,
		"bf-title":       repairs.SkipOwnerManual,
		"itunes":         repairs.SkipITunes,
		"dw-series":      repairs.SkipOwnerManual,
	}
	for name, want := range skipped {
		r, ok := rows[name]
		require.True(t, ok, "%s: no row", name)
		require.Equal(t, want, r.Skipped, "%s: %s", name, r.SkipReason)
	}
	require.Contains(t, rows["frag1"].SkipReason, "possible fragment")
	require.Contains(t, rows["owned"].SkipReason, "fragment — use the consolidation fixer")
	require.Contains(t, rows["eldest98"].SkipReason, `its author "Eldest" is the name of its folder`)
	require.Contains(t, rows["owner-proposal"].SkipReason, "Torchwood: The Dead Line")
	require.Contains(t, rows["unabridged"].SkipReason, "format, edition or disc marker")
	require.Contains(t, rows["person"].SkipReason, "names the author or narrator")
	require.Contains(t, rows["owned"].SkipReason, "is also a file of book "+lib.ids["parent"])

	for _, name := range []string{"robot", "etranger", "1984", "existing", "parent", "moonlight"} {
		_, ok := rows[name]
		require.False(t, ok, "%s has a real title and must not be a row", name)
	}
	require.Equal(t, len(applicable), res.Applicable)
	require.Equal(t, len(applicable)+len(skipped), res.Total)
}

func TestJunkTitleFixer_ApplyWritesTitleOnlyAndUndoRestoresIt(t *testing.T) {
	lib := newJunkLib(t)
	f, res, rows := lib.plan(t)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var plan repairs.PlanResult
	require.NoError(t, json.Unmarshal(raw, &plan)) // apply reads the stored plan

	before, err := lib.store.GetBookByID(lib.ids["folder"])
	require.NoError(t, err)

	series, err := lib.store.GetAllSeries()
	require.NoError(t, err)
	deps := repairs.ApplyDeps{Guard: lib.store, Series: repairs.SeriesNamesFrom(series),
		Writer: repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-")}
	sel := []string{rows["folder"].RowID, rows["prefix"].RowID, rows["frag1"].RowID}
	out, err := repairs.RunApply(context.Background(), f, &plan, "plan-1", sel, false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, out.Applied, "outcomes %v", out.ByOutcome)
	require.Equal(t, 1, out.ByOutcome[repairs.OutcomeNotApplicable], "the fragment is never written")
	require.Equal(t, 2, out.HistoryRows, "one history row per retitled book")

	after, err := lib.store.GetBookByID(lib.ids["folder"])
	require.NoError(t, err)
	require.Equal(t, "Good Book", after.Title)
	// Title only: nothing else on the row moved.
	after.Title = before.Title
	a, _ := database.SnapshotBook(after)
	b, _ := database.SnapshotBook(before)
	for _, field := range database.TrackedBookFields() {
		av, _ := database.RenderBookField(a, field)
		bv, _ := database.RenderBookField(b, field)
		require.Equal(t, bv, av, "field %s changed", field)
	}

	// Re-running the same apply (a resumed run) writes nothing twice.
	again, err := repairs.RunApply(context.Background(), f, &plan, "plan-1", sel[:2], false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 0, again.Applied)
	require.Equal(t, 2, again.ChangedSincePlan)

	// Undo puts the junk title back: the write is visible to undo-last-apply.
	undo, err := metafetch.NewService(lib.store).UndoLastApply(lib.ids["folder"])
	require.NoError(t, err)
	require.Equal(t, []string{"title"}, undo.Reverted)
	restored, err := lib.store.GetBookByID(lib.ids["folder"])
	require.NoError(t, err)
	require.Equal(t, "read by narrator", restored.Title)

	// The fixer keeps no memory of its writes: after the undo, a fresh plan
	// proposes the title again and the same fixer applies it again.
	res2, err := repairs.RunPlan(context.Background(), f, nil,
		repairs.PlanDeps{Guard: lib.store, Series: repairs.SeriesNamesFrom(series)}, &fakeReporter{})
	require.NoError(t, err)
	var row2 repairs.Row
	for _, r := range res2.Rows {
		if r.RowID == lib.ids["folder"] {
			row2 = r
		}
	}
	require.True(t, row2.Applicable(), "%s: %s", row2.Skipped, row2.SkipReason)
	re, err := repairs.RunApply(context.Background(), f, res2, "plan-2", []string{row2.RowID}, false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, re.Applied, "outcomes %v", re.ByOutcome)
	reapplied, err := lib.store.GetBookByID(lib.ids["folder"])
	require.NoError(t, err)
	require.Equal(t, "Good Book", reapplied.Title)
}

func TestJunkTitleFixer_TitleChangedAfterReplanIsRefused(t *testing.T) {
	lib := newJunkLib(t)
	_, _, rows := lib.plan(t)
	w := repairs.NewWriter(lib.store, lib.store, junkTitlesFixerID, "bulk_update", "repairs-")
	id := lib.ids["folder"]
	_, err := lib.store.ModifyBook(id, func(b *database.Book) error { b.Title = "Hand Edited"; return nil })
	require.NoError(t, err)
	err = writeTitleOnly(w, lib.store, id, rows["folder"].Current["title"], "Good Book")
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	got, err := lib.store.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "Hand Edited", got.Title)

	// A user lock that lands after the re-plan is re-checked inside the write.
	pid := lib.ids["prefix"]
	require.NoError(t, lib.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: pid, Field: "title",
		OverrideLocked: true, UpdatedAt: time.Now()}))
	err = writeTitleOnly(w, lib.store, pid, rows["prefix"].Current["title"], "Lonely Book")
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	got, err = lib.store.GetBookByID(pid)
	require.NoError(t, err)
	require.Equal(t, "01 - Lonely Book", got.Title)
}

func TestJunkProposalCheck_Refusals(t *testing.T) {
	pc := junkProposalCheck{stored: "read by narrator", people: []string{"Frank Herbert"},
		roots: []string{"/mnt/data/audiobooks"},
		paths: []string{"/mnt/data/audiobooks/Frank Herbert/Dune/01.mp3", "/lib/X/y.mp3"}}
	refused := map[string]string{
		"Dune MP3":                 "format",
		"Dune (Unabridged)":        "format",
		"Dune Abridged":            "format",
		"Dune Side A":              "format",
		"Dune Tape 3":              "format",
		"Dune CD01-02":             "format",
		"Dune [B002V1OF70]":        "ASIN",
		"B002V1OF70":               "ASIN",
		"Dune 9780441013593":       "ASIN",
		"merged":                   "generic",
		"Output":                   "generic",
		"Book":                     "generic",
		"audio":                    "generic",
		"Frank Herbert - Dune":     "person",
		"Herbert, Frank":           "author or narrator",
		"lib":                      "root",
		"mnt":                      "root",
		"data":                     "root",
		"Frank Herbert Collection": "",
	}
	for c, want := range refused {
		got := pc.refusal(c, junkSrcFolder)
		if want == "" {
			require.Empty(t, got, c)
			continue
		}
		require.NotEmpty(t, got, "%q must be refused", c)
		require.Contains(t, got, want, c)
	}
	// The format list applies to folder and filename evidence only; the
	// root, person and generic checks apply to every source.
	require.Empty(t, pc.refusal("Dune Tape 3", junkSrcTranscribed))
	require.NotEmpty(t, pc.refusal("lib", junkSrcCandidate))
	require.Empty(t, pc.refusal("Dune", junkSrcFolder))
	// The author folder directly below a configured root is a path.
	pc2 := junkProposalCheck{stored: "x", roots: []string{"/mnt/data/audiobooks"},
		paths: []string{"/mnt/data/audiobooks/Some Author/Real Work/01.mp3"}}
	require.Contains(t, pc2.refusal("Some Author", junkSrcFolder), "root")
	require.Empty(t, pc2.refusal("Real Work", junkSrcFolder))
}

func TestTitleAgreesWithAny(t *testing.T) {
	require.True(t, titleAgreesWithAny("Eldest", []string{"Eldest"}))
	require.True(t, titleAgreesWithAny("Eldest: Inheritance, Book 2", []string{"Eldest"}))
	require.False(t, titleAgreesWithAny("Eragon", []string{"Eldest"}))
	require.False(t, titleAgreesWithAny("Eldest Son", []string{"Eldest"}))
	require.False(t, titleAgreesWithAny("Eldest", nil))
}

func TestJunkTitleFixer_SameProposalTwiceIsAPossibleFragment(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	author, err := st.CreateAuthor("Some Author")
	require.NoError(t, err)
	for _, title := range []string{"01 - Eldest", "02 - Eldest"} {
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: "/lib/" + title + ".mp3", AuthorID: &author.ID})
		require.NoError(t, err)
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: "/lib/" + title + ".mp3"}))
	}
	f := newJunkTitleFixer(&Plugin{deps: fakeDeps{store: st}})
	rows, err := f.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Equal(t, junkSkipPossibleFragment, r.Skipped, r.SkipReason)
	}
}

func TestLetterLOrdinalFixer(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ids := map[string]string{}
	for _, title := range []string{"l Corinthians", "ll Kings 4", "I Corinthians", "l'Étranger", "l Timothy"} {
		b, err := st.CreateBook(&database.Book{Title: title, FilePath: "/lib/Bible/" + title + ".mp3"})
		require.NoError(t, err)
		ids[title] = b.ID
	}
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: ids["l Timothy"], Field: "title",
		OverrideLocked: true, UpdatedAt: time.Now()}))
	f := newLetterLOrdinalFixer(&Plugin{deps: fakeDeps{store: st}})
	res, err := repairs.RunPlan(context.Background(), f, nil, repairs.PlanDeps{Guard: st}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 3, res.Total, "only letter-l numbered books are rows")
	require.Equal(t, 2, res.Applicable)
	require.Equal(t, 1, res.SkippedByKind[junkSkipUserLocked])
	byID := map[string]repairs.Row{}
	for _, r := range res.Rows {
		byID[r.RowID] = r
	}
	require.Equal(t, "1 Corinthians", byID[ids["l Corinthians"]].Proposed["title"])
	require.Equal(t, "2 Kings 4", byID[ids["ll Kings 4"]].Proposed["title"])

	w := repairs.NewWriter(st, st, f.ID(), "bulk_update", "repairs-")
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1",
		[]string{ids["l Corinthians"], ids["ll Kings 4"], ids["l Timothy"]}, false,
		repairs.ApplyDeps{Guard: st, Writer: w}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, out.Applied, "%v", out.ByOutcome)
	b, err := st.GetBookByID(ids["l Corinthians"])
	require.NoError(t, err)
	require.Equal(t, "1 Corinthians", b.Title)
	b, err = st.GetBookByID(ids["l Timothy"])
	require.NoError(t, err)
	require.Equal(t, "l Timothy", b.Title, "a user-locked title is never rewritten")
}
