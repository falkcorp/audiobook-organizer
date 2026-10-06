// file: internal/plugins/maintenance/junk_title_fixer_test.go
// version: 1.14.0
// guid: 3a8d6f52-1e9c-4b07-92d4-6c5b0e8a7f13
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
	// The transcription disagrees with the folder: neither is proposed.
	tr := add("transcribed", "Opening", "/lib/Author A/Some Folder", a, nil,
		"/lib/Author A/Some Folder/01.mp3", "/lib/Author A/Some Folder/02.mp3")
	_, err = st.ModifyBook(tr, func(b *database.Book) error {
		s := "The Spoken Name"
		b.TranscribedTitle = &s
		return nil
	})
	require.NoError(t, err)
	// The filename stem "zz" is too weak to be title evidence: the candidate
	// stands on its own.
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
	// A series-position prefix a refetch echoed back as the provider's value
	// ("02 - No Quarter"): 350 such books on prod, 2026-10-03.
	fetchedPos := `"02 - No Quarter"`
	pos := add("provider-prefix", "02 - No Quarter", "/lib/Author P/02 - No Quarter.m4b", nil, nil,
		"/lib/Author P/02 - No Quarter.m4b")
	require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: pos, Field: "title",
		FetchedValue: &fetchedPos, UpdatedAt: time.Now()}))
	// The provider's recorded title differs from the stored junk one: it is
	// evidence. 1,216 of 1,233 provider-flagged rows on prod, 2026-10-03.
	// recAuthor is the author the provider recorded beside the title ("" none).
	withFetched := func(name, title, fetchedJSON, recAuthor string, authorID *int, paths ...string) {
		id := add(name, title, paths[0], authorID, nil, paths...)
		val := fetchedJSON
		require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: id, Field: "title",
			FetchedValue: &val, UpdatedAt: time.Now()}))
		if recAuthor != "" {
			enc, err := json.Marshal(recAuthor)
			require.NoError(t, err)
			av := string(enc)
			require.NoError(t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: id, Field: "author_name",
				FetchedValue: &av, UpdatedAt: time.Now()}))
		}
	}
	// agrees with the stripped title
	withFetched("provider-agrees", "85 - Echo Book", `"Echo Book: A LitRPG Adventure"`, "", nil, "/lib/Author Q/85 - Echo Book.m4b")
	// the fetch searched on the junk title and hit another book
	withFetched("provider-wrong-hit", "07 - Mutineer Song", `"Shadowfall"`, "", nil, "/lib/Author R/07 - Mutineer Song.m4b")
	// a narrator credit with nothing in the title: recorded under the book's
	// author, and the folder agrees
	withFetched("provider-credit", "read by narrator", `"Divine Creation"`, "Author A", a,
		"/lib/Author A/Divine Creation/01.mp3", "/lib/Author A/Divine Creation/02.mp3")
	// …and the folder disagrees
	withFetched("provider-conflict", "read by narrator", `"Divine Wrath"`, "Author A", a,
		"/lib/Author A/Quite Another Folder/01.mp3", "/lib/Author A/Quite Another Folder/02.mp3")
	// recorded under nobody the book knows: the value is not used at all
	withFetched("provider-no-author", "read by narrator", `"Marvel Comics"`, "Somebody Else", nil,
		"/lib/Author U/Third Folder/01.mp3", "/lib/Author U/Third Folder/02.mp3")
	// the same title by its letters and digits is "the provider recorded this title"
	withFetched("provider-near-equal", "3-10 to Yuma", `"3:10 to Yuma"`, "", nil, "/lib/Author V/3-10 to Yuma.m4b")
	// a value on record that is no string
	withFetched("provider-unreadable", "04 - Null Book", `null`, "", nil, "/lib/Author W/04 - Null Book.m4b")
	// a catalog title's bracketed edition marker is dropped, the title kept
	withFetched("provider-format", "read by narrator", `"Probe Title (Unabridged)"`, "Author A", a,
		"/lib/Author A/Probe Title/01.mp3", "/lib/Author A/Probe Title/02.mp3")
	// The path and the provider agree exactly; the transcription misheard the
	// name: the transcription is overruled.
	withFetched("provider-outvotes", "20 - Hilldiggers", `"Hilldiggers"`, "", nil, "/lib/Author X/20 - Hilldiggers.m4b")
	// …also for a credit title whose folder and author-gated provider agree
	withFetched("provider-outvotes-folder", "read by narrator", `"Line of Polity"`, "Author A", a,
		"/lib/Author A/Line of Polity/01.mp3", "/lib/Author A/Line of Polity/02.mp3")
	// the same disagreement with no provider on record stays a conflict
	add("no-provider-conflict", "21 - Line War", "/lib/Author Y/21 - Line War.m4b", nil, nil, "/lib/Author Y/21 - Line War.m4b")
	// the provider names the series' first book, the path only the series:
	// the transcription is the one source that has the book right
	withFetched("provider-series-hit", "03 - Mistborn", `"Mistborn: The Final Empire"`, "", nil, "/lib/Author S/03 - Mistborn.m4b")
	withFetched("provider-series-hit-folder", "read by narrator", `"Mistborn: The Final Empire"`, "Author A", a,
		"/lib/Author A/Mistborn/01.mp3", "/lib/Author A/Mistborn/02.mp3")
	// a refused candidate sides with the transcription: two against the path
	withFetched("provider-vs-two", "03 - Gridlinked", `"Gridlinked"`, "", a, "/lib/Shelf G/03 - Gridlinked.m4b")
	cacheRaw := `{"title":"The Line of Polity","author":"Author A","score":0.95}`
	require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: lib.ids["provider-vs-two"], FetchedAt: time.Now(),
		Candidates: []json.RawMessage{json.RawMessage(cacheRaw)}}))
	// the transcription names a book this author already has
	add("owned-title", "Brass Man", "/lib/Shelf O/Brass Man.m4b", a, nil, "/lib/Shelf O/Brass Man.m4b")
	withFetched("provider-vs-owned", "05 - Polity Agent", `"Polity Agent"`, "", a, "/lib/Shelf P/05 - Polity Agent.m4b")
	// an owner-manual transcription is never overruled into a proposal
	withFetched("provider-vs-owner", "06 - Plain Title", `"Plain Title"`, "", nil, "/lib/Author T/06 - Plain Title.m4b")
	for name, heard := range map[string]string{
		"provider-outvotes": "Hildiggers", "provider-outvotes-folder": "Lime of Polly Tea", "no-provider-conflict": "Lime Wart",
		"provider-series-hit": "The Hero of Ages", "provider-series-hit-folder": "The Well of Ascension",
		"provider-vs-two": "The Line of Polity", "provider-vs-owned": "Brass Man", "provider-vs-owner": "Doctor Who: The Daleks",
	} {
		_, err := st.ModifyBook(lib.ids[name], func(b *database.Book) error { b.TranscribedTitle = &heard; return nil })
		require.NoError(t, err)
	}
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
		"folder":    "Good Book",
		"prefix":    "Lonely Book",
		"disc":      "Whole Work",
		"candidate": "Candidate Title",
		// highest score among the trustworthy candidates, not the first
		"eldest-prefix": "Eldest",
		"named-credit":  "Named Credit Book",
		// a provider-recorded title that fails the junk classifier is
		// repaired like a file-derived one (owner decision 2026-10-03)
		"provider":        "Provider Folder",
		"provider-prefix": "No Quarter",
		// the provider's recorded title is evidence
		"provider-agrees":     "Echo Book",
		"provider-wrong-hit":  "Mutineer Song",
		"provider-credit":     "Divine Creation",
		"provider-no-author":  "Third Folder",
		"provider-near-equal": "to Yuma",
		"provider-unreadable": "Null Book",
		"provider-format":     "Probe Title",
		// path + provider outvote a transcription that disagrees
		"provider-outvotes":        "Hilldiggers",
		"provider-outvotes-folder": "Line of Polity",
	}
	for name, want := range applicable {
		r, ok := rows[name]
		require.True(t, ok, "%s: no row", name)
		require.True(t, r.Applicable(), "%s: skipped %s (%s)", name, r.Skipped, r.SkipReason)
		require.Equal(t, want, r.Proposed["title"], name)
		require.Equal(t, []string{lib.ids[name]}, r.BookIDs, "%s: one book per row", name)
	}
	// The recorded title IS the stored one (exactly, or by its letters and
	// digits): repaired, never at low risk.
	for _, name := range []string{"provider", "provider-prefix", "provider-near-equal"} {
		require.Contains(t, rows[name].Reason, "recorded for this book is this same title", name)
		require.Equal(t, repairs.RiskReview, rows[name].Risk, name)
	}
	require.NotContains(t, rows["prefix"].Reason, "provider")
	// A value on record that cannot be read says so, and is review risk.
	require.Contains(t, rows["provider-unreadable"].Reason, "is not a readable string")
	require.Equal(t, repairs.RiskReview, rows["provider-unreadable"].Risk)
	// A recorded title that agrees with the stripped one corroborates it.
	require.Equal(t, repairs.RiskLow, rows["provider-agrees"].Risk)
	require.Contains(t, rows["provider-agrees"].Reason, `a metadata provider recorded the title "Echo Book: A LitRPG Adventure"`)
	// One that disagrees with the title's own evidence is never proposed,
	// is named in the reason, and takes the row out of low risk.
	require.Contains(t, rows["provider-wrong-hit"].Reason, "title_prefix_stripped")
	require.Contains(t, rows["provider-wrong-hit"].Reason, `not used: provider value "Shadowfall" disagrees`)
	require.Equal(t, repairs.RiskReview, rows["provider-wrong-hit"].Risk)
	// With nothing in the title to vouch for it, it must be recorded under
	// the book's own author.
	require.Equal(t, repairs.RiskReview, rows["provider-credit"].Risk)
	require.Contains(t, rows["provider-no-author"].Reason, "proposed from folder")
	require.Contains(t, rows["provider-no-author"].Reason, `not used: provider value "Marvel Comics" was not recorded under this book's author`)
	require.Contains(t, rows["provider-format"].Reason, "proposed from provider_value")
	// Overruling a transcription is said in the reason and is never low risk.
	for name, heard := range map[string]string{"provider-outvotes": "Hildiggers", "provider-outvotes-folder": "Lime of Polly Tea"} {
		require.Equal(t, repairs.RiskReview, rows[name].Risk, name)
		require.Contains(t, rows[name].Reason, fmt.Sprintf("the transcription %q disagrees and was overruled", heard), name)
	}
	// Every limit of the rule is still a conflict.
	for _, name := range []string{"no-provider-conflict", "provider-series-hit", "provider-series-hit-folder", "provider-vs-two", "provider-vs-owned"} {
		require.Contains(t, rows[name].SkipReason, "conflicting evidence", name)
	}
	require.Contains(t, rows["provider-vs-two"].SkipReason, `which the candidate "The Line of Polity" agrees with`)
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
		"owned":                      junkSkipFragment,
		"owner-proposal":             repairs.SkipOwnerManual,
		"provider-conflict":          junkSkipNeedsManual,
		"no-provider-conflict":       junkSkipNeedsManual,
		"provider-series-hit":        junkSkipNeedsManual,
		"provider-series-hit-folder": junkSkipNeedsManual,
		"provider-vs-two":            junkSkipNeedsManual,
		"provider-vs-owned":          junkSkipNeedsManual,
		"provider-vs-owner":          repairs.SkipOwnerManual,
		"bare13":                     junkSkipNeedsManual,
		"unabridged":                 junkSkipNeedsManual,
		"transcribed":                junkSkipNeedsManual,
		"generic":                    junkSkipNeedsManual,
		"locked":                     junkSkipUserLocked,
		"manual":                     junkSkipNeedsManual,
		"person":                     junkSkipNeedsManual,
		"bf-title":                   repairs.SkipOwnerManual,
		"itunes":                     repairs.SkipITunes,
		"dw-series":                  repairs.SkipOwnerManual,
	}
	for name, want := range skipped {
		r, ok := rows[name]
		require.True(t, ok, "%s: no row", name)
		require.Equal(t, want, r.Skipped, "%s: %s", name, r.SkipReason)
	}
	require.Contains(t, rows["provider-conflict"].SkipReason, `provider_value "Divine Wrath"`)
	require.Contains(t, rows["frag1"].SkipReason, "possible fragment")
	require.Contains(t, rows["owned"].SkipReason, "fragment — use the consolidation fixer")
	require.Contains(t, rows["eldest98"].SkipReason, `its author "Eldest" is the name of its folder`)
	require.Contains(t, rows["owner-proposal"].SkipReason, "Torchwood: The Dead Line")
	require.Contains(t, rows["unabridged"].SkipReason, "format, edition or disc marker")
	require.Contains(t, rows["transcribed"].SkipReason, `conflicting evidence: folder "Some Folder" vs transcription "The Spoken Name"`)
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
	const opID = "op-junk-apply"
	deps := repairs.ApplyDeps{Guard: lib.store, Series: repairs.SeriesNamesFrom(series), OpID: opID,
		Writer: repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-").
			WithJournal(lib.store, lib.store, opID).WithFieldStates(lib.store)}
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

	// The title is locked, so a forced rescan cannot write the file tag's
	// junk back over it.
	locks, err := database.LoadFieldLocks(lib.store, lib.ids["folder"])
	require.NoError(t, err)
	require.True(t, locks.Locked(database.FieldKeyTitle), "the written title is locked")

	// Undo-last-apply refuses the batch: the lock lives in the op journal,
	// and putting the title back alone would leave the junk title locked.
	_, uerr := metafetch.NewService(lib.store).UndoLastApply(lib.ids["folder"])
	require.Error(t, uerr)
	still, err := lib.store.GetBookByID(lib.ids["folder"])
	require.NoError(t, err)
	require.Equal(t, "Good Book", still.Title)

	// The op revert puts the junk title back and lifts the lock.
	rev, err := audiobooks.NewRevertService(lib.store).RevertOperation(opID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	require.Equal(t, 2, rev.RestoredTypes[undo.ChangeTypeFieldLock], "both locks lifted: %+v", rev.RestoredTypes)
	restored, err := lib.store.GetBookByID(lib.ids["folder"])
	require.NoError(t, err)
	require.Equal(t, "read by narrator", restored.Title)
	locks, err = database.LoadFieldLocks(lib.store, lib.ids["folder"])
	require.NoError(t, err)
	require.False(t, locks.Locked(database.FieldKeyTitle), "the revert lifts the lock")

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
	// A new apply is a new operation with its own journal.
	deps.OpID = "op-junk-apply-2"
	deps.Writer = repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-").
		WithJournal(lib.store, lib.store, deps.OpID).WithFieldStates(lib.store)
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
	w := repairs.NewWriter(lib.store, lib.store, junkTitlesFixerID, "bulk_update", "repairs-").
		WithJournal(lib.store, lib.store, "op-junk-refused").WithFieldStates(lib.store)
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
	// A transcription that is the intro's first sentence, a publisher ident
	// or a chapter heading is not a title (every one of these was proposed on
	// prod 2026-10-03).
	for c, want := range map[string]string{
		"Chapter 26 The apartment was in Asimov, a city at":                        "chapter heading",
		"Chapter 77. Testing. Primordials were strange, en":                        "chapter heading",
		"Together, they fell toward the light. The wall be":                        "prose",
		"his brain loose. The simulated G-forces, as he ro":                        "prose",
		"It's easy to idealize the past. Take sailing ships, for instance. Not th": "prose",
		"Tantor Audio, a division of recorded books presents. Class A threat":      "publisher",
		"Tantor audio presents, The Legend of Coronair":                            "publisher",
		"from Simon and Schuster Audio, Star Trek, Voyager, Pathways":              "publisher",
		"Full cast audio resents. Wild magic":                                      "publisher",
		"This is Audible.":                                                         "publisher",
		"Star Force, Endless Crusade. Written":                                     "prose",
	} {
		got := pc.refusal(c, junkSrcTranscribed)
		require.NotEmpty(t, got, "%q must be refused", c)
		require.Contains(t, got, want, c)
	}
	for _, c := range []string{"The Shadow of Saginami", "Quantico", "Stormborn, The Seaborn Cycle", "Star Trek, Spectre",
		"So Long and Thanks for All the Fish", "1984", "Life, the universe, and everything",
		"Star Trek Deep Space Nine Millennium The Fall of Terok Nor", "Reunion, a Star Trek The Next Generation novel"} {
		require.Empty(t, pc.refusal(c, junkSrcTranscribed), c)
	}
	// A chapter heading is refused from every source: "01 - Prologue" strips
	// to "Prologue", "03 Chapter Two - The Hunter" to "Chapter Two - The Hunter".
	require.Contains(t, pc.refusal("Prologue Bobbie Draper", junkSrcStripped), "chapter heading")
	require.Contains(t, pc.refusal("Chapter Two - The Hunter", junkSrcStripped), "chapter heading")
	require.Empty(t, pc.refusal("Prologue to Murder", junkSrcStripped), "a title can start with the word")
	require.NotEmpty(t, pc.refusal("lib", junkSrcCandidate))
	require.Empty(t, pc.refusal("Dune", junkSrcFolder))
	// The author folder directly below an author-first library root is a path.
	pc2 := junkProposalCheck{stored: "x", roots: []string{"/mnt/data/audiobooks"}, authorRoot: "/mnt/data/audiobooks",
		paths: []string{"/mnt/data/audiobooks/Some Author/Real Work/01.mp3"}}
	require.Contains(t, pc2.refusal("Some Author", junkSrcFolder), "root")
	require.Empty(t, pc2.refusal("Real Work", junkSrcFolder))
	// An import path is not author-first: a flat import folder is the title.
	pc3 := junkProposalCheck{stored: "x", roots: []string{"/mnt/data/audiobooks", "/srv/incoming-rips"},
		authorRoot: "/mnt/data/audiobooks", paths: []string{"/srv/incoming-rips/Real Work/01.mp3"}}
	require.Empty(t, pc3.refusal("Real Work", junkSrcFolder))
	require.Contains(t, pc3.refusal("incoming-rips", junkSrcFolder), "root")
}

func TestTitleAgreesWithAny(t *testing.T) {
	require.True(t, titleAgreesWithAny("Eldest", []string{"Eldest"}))
	require.True(t, titleAgreesWithAny("Assassin's Apprentice", []string{"Assassins Apprentice"}))
	require.True(t, titleAgreesWithAny("Assassin’s Apprentice", []string{"Assassins Apprentice"}))
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

	const opID = "op-letter-l"
	w := repairs.NewWriter(st, st, f.ID(), "bulk_update", "repairs-").WithJournal(st, st, opID).WithFieldStates(st)
	out, err := repairs.RunApply(context.Background(), f, res, "plan-1",
		[]string{ids["l Corinthians"], ids["ll Kings 4"], ids["l Timothy"]}, false,
		repairs.ApplyDeps{Guard: st, Writer: w, OpID: opID}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 2, out.Applied, "%v", out.ByOutcome)
	b, err := st.GetBookByID(ids["l Corinthians"])
	require.NoError(t, err)
	require.Equal(t, "1 Corinthians", b.Title)
	b, err = st.GetBookByID(ids["l Timothy"])
	require.NoError(t, err)
	require.Equal(t, "l Timothy", b.Title, "a user-locked title is never rewritten")
	locks, err := database.LoadFieldLocks(st, ids["l Corinthians"])
	require.NoError(t, err)
	require.True(t, locks.Locked(database.FieldKeyTitle), "the written title is locked")

	// The op revert puts the title back and lifts the lock.
	rev, err := audiobooks.NewRevertService(st).RevertOperation(opID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "revert: %+v", rev)
	b, err = st.GetBookByID(ids["l Corinthians"])
	require.NoError(t, err)
	require.Equal(t, "l Corinthians", b.Title)
	locks, err = database.LoadFieldLocks(st, ids["l Corinthians"])
	require.NoError(t, err)
	require.False(t, locks.Locked(database.FieldKeyTitle))
}

// planRows plans the fixer over st and returns the rows by book id.
func planRows(t *testing.T, st *database.PebbleStore) map[string]repairs.Row {
	t.Helper()
	f := newJunkTitleFixer(&Plugin{deps: fakeDeps{store: st}})
	rows, err := f.Plan(context.Background(), nil, &fakeReporter{})
	require.NoError(t, err)
	out := map[string]repairs.Row{}
	for _, r := range rows {
		out[r.RowID] = r
	}
	return out
}

func addJunkBook(t *testing.T, st *database.PebbleStore, title string, author *int, files ...string) string {
	t.Helper()
	b, err := st.CreateBook(&database.Book{Title: title, FilePath: files[0], AuthorID: author, Format: "mp3"})
	require.NoError(t, err)
	for _, f := range files {
		require.NoError(t, st.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: f, Format: "mp3"}))
	}
	return b.ID
}

// When the book's own path and a transcription or candidate disagree,
// neither is proposed; agreement still proposes; a weak filename stem is no
// evidence; a series folder is no evidence.
func TestJunkTitleFixer_ConflictingEvidenceProposesNeither(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	author := func(name string) *int {
		a, err := st.CreateAuthor(name)
		require.NoError(t, err)
		return &a.ID
	}
	cache := func(id, title, by string, score float64) {
		raw := fmt.Sprintf(`{"title":%q,"author":%q,"score":%v}`, title, by, score)
		require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: id, FetchedAt: time.Now(),
			Candidates: []json.RawMessage{json.RawMessage(raw)}}))
	}
	transcribe := func(id, s string) {
		_, err := st.ModifyBook(id, func(b *database.Book) error { b.TranscribedTitle = &s; return nil })
		require.NoError(t, err)
	}
	herbert, banks, rowling := author("Frank Herbert"), author("Iain M. Banks"), author("J.K. Rowling")
	sanderson, weir, hobb := author("Brandon Sanderson"), author("Andy Weir"), author("Robin Hobb")
	two := func(dir string) []string { return []string{dir + "/01.mp3", dir + "/02.mp3"} }

	conflicts := map[string][2]string{} // id -> the two values the reason must name
	proposed := map[string]string{}     // id -> the title the row must propose
	id := addJunkBook(t, st, "Unknown Title", herbert, "/lib/Frank Herbert/Dune Messiah/Dune Messiah.m4b")
	cache(id, "Dune", "Frank Herbert", 0.6)
	conflicts[id] = [2]string{"Dune Messiah", "Dune"}
	id = addJunkBook(t, st, "Opening", herbert, two("/lib/Frank Herbert/Children of Dune")...)
	cache(id, "Dune", "Frank Herbert", 0.9)
	conflicts[id] = [2]string{"Children of Dune", "Dune"}
	// A publisher ident is refused as a transcription (not evidence, not a
	// conflict): the folder alone names the book.
	id = addJunkBook(t, st, "read by narrator", banks, two("/lib/Iain M. Banks/Excession")...)
	transcribe(id, "Audible Studios presents")
	proposed[id] = "Excession"
	id = addJunkBook(t, st, "Opening", sanderson, two("/lib/Brandon Sanderson/Libation")...)
	transcribe(id, "Mistborn: The Final Empire")
	conflicts[id] = [2]string{"Libation", "Mistborn: The Final Empire"}

	stormlight, err := st.CreateSeries("The Stormlight Archive", sanderson)
	require.NoError(t, err)
	id = addJunkBook(t, st, "Opening", sanderson, two("/lib/Brandon Sanderson/The Stormlight Archive")...)
	_, err = st.ModifyBook(id, func(b *database.Book) error { b.SeriesID = &stormlight.ID; return nil })
	require.NoError(t, err)
	cache(id, "Words of Radiance", "Brandon Sanderson", 0.8)
	proposed[id] = "Words of Radiance"
	// A generic folder ("New Folder (2)", "Complete Collection") is no
	// evidence and no proposal.
	id = addJunkBook(t, st, "Opening", rowling, two("/lib/J.K. Rowling/New Folder (2)")...)
	transcribe(id, "Harry Potter and the Chamber of Secrets")
	proposed[id] = "Harry Potter and the Chamber of Secrets"
	id = addJunkBook(t, st, "Unknown Title", weir, two("/lib/Andy Weir/Complete Collection")...)
	cache(id, "The Martian", "Andy Weir", 0.8)
	proposed[id] = "The Martian"
	for stem, want := range map[string]string{
		"hp1":                   "Harry Potter and the Philosopher's Stone",
		"dune_messiah_64kbps":   "Dune Messiah",
		"final":                 "Chapterhouse: Dune",
		"audible_download_2019": "God Emperor of Dune",
	} {
		by, byName := herbert, "Frank Herbert"
		if stem == "hp1" {
			by, byName = rowling, "J.K. Rowling"
		}
		id = addJunkBook(t, st, "Unknown Title", by, "/lib/"+byName+"/Rips/"+stem+".m4b")
		cache(id, want, byName, 0.8)
		proposed[id] = want
	}
	// Agreement still proposes, apostrophes aside.
	id = addJunkBook(t, st, "Opening", hobb, two("/lib/Robin Hobb/Assassins Apprentice")...)
	transcribe(id, "Assassin's Apprentice")
	proposed[id] = "Assassin's Apprentice"

	rows := planRows(t, st)
	for id, want := range conflicts {
		r := rows[id]
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%v: %s", want, r.SkipReason)
		require.Contains(t, r.SkipReason, "conflicting evidence")
		require.Contains(t, r.SkipReason, fmt.Sprintf("%q", want[0]))
		require.Contains(t, r.SkipReason, fmt.Sprintf("%q", want[1]))
	}
	for id, want := range proposed {
		r := rows[id]
		require.True(t, r.Applicable(), "%s: %s (%s)", want, r.Skipped, r.SkipReason)
		require.Equal(t, want, r.Proposed["title"])
	}
}

func TestIsWeakFileStem(t *testing.T) {
	for _, s := range []string{"zz", "hp1", "dune_messiah_64kbps", "final", "Merged", "output", "audible_download_2019", "Dune 64kbps"} {
		require.True(t, isWeakFileStem(s), s)
	}
	for _, s := range []string{"Dune Messiah", "The Martian", "1984 Revisited", "Words of Radiance"} {
		require.False(t, isWeakFileStem(s), s)
	}
}

// A folder reached by climbing past a chapter folder, directly under an
// import root, may be the author: without corroboration it needs a person.
func TestJunkTitleFixer_ClimbToAFolderUnderARootNeedsCorroboration(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.CreateImportPath("/imports/dl", "dl")
	require.NoError(t, err)
	unknown, err := st.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	id := addJunkBook(t, st, "Chapter 1", &unknown.ID, "/imports/dl/Robin Hobb/01/a.mp3", "/imports/dl/Robin Hobb/01/b.mp3")
	r := planRows(t, st)[id]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "may be the author")
}

// A bare number filed in a folder named after itself is that book, not a
// chapter of its author: needs a person. Unpadded numbers side by side in one
// folder are chapters of one book: a possible fragment.
func TestJunkTitleFixer_BareNumbers(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	armstrong, err := st.CreateAuthor("Kelley Armstrong")
	require.NoError(t, err)
	thirteen := addJunkBook(t, st, "13", &armstrong.ID, "/lib/Kelley Armstrong/13/13.m4b")
	addJunkBook(t, st, "Bitten", &armstrong.ID, "/lib/Kelley Armstrong/Bitten.m4b")
	other, err := st.CreateAuthor("Some Author")
	require.NoError(t, err)
	var sibs []string
	for _, n := range []string{"1", "2", "3"} {
		sibs = append(sibs, addJunkBook(t, st, n, &other.ID, "/lib/Some Author/Book/"+n+".mp3"))
	}
	rows := planRows(t, st)
	require.Equal(t, junkSkipNeedsManual, rows[thirteen].Skipped, rows[thirteen].SkipReason)
	for _, id := range sibs {
		require.Equal(t, junkSkipPossibleFragment, rows[id].Skipped, rows[id].SkipReason)
	}
}

// A weak filename stem with no other evidence is never proposed.
func TestJunkTitleFixer_WeakStemAloneNeedsManual(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	a, err := st.CreateAuthor("Some Author")
	require.NoError(t, err)
	ids := map[string]string{}
	for _, stem := range []string{"hp1", "final", "audible_download_2019"} {
		ids[stem] = addJunkBook(t, st, "Unknown Title", &a.ID, "/lib/Some Author/Rips/"+stem+".m4b")
	}
	rows := planRows(t, st)
	for stem, id := range ids {
		r := rows[id]
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%s: %s", stem, r.SkipReason)
		require.Equal(t, "only a weak filename stem: "+stem, r.SkipReason)
	}
}

func TestJunkTitleFixer_WeakStemStillVetoesDisagreeingEvidence(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	a, err := st.CreateAuthor("Some Author")
	require.NoError(t, err)
	spoken := func(id, s string) {
		_, err := st.ModifyBook(id, func(b *database.Book) error { b.TranscribedTitle = &s; return nil })
		require.NoError(t, err)
	}
	q84 := addJunkBook(t, st, "Unknown Title", &a.ID, "/lib/Some Author/A/1Q84.m4b")
	spoken(q84, "Audible Studios presents")
	it := addJunkBook(t, st, "Unknown Title", &a.ID, "/lib/Some Author/B/It.m4b")
	spoken(it, "This is Audible")
	fe := addJunkBook(t, st, "Unknown Title", &a.ID, "/lib/Some Author/C/The_Final_Empire.m4b")
	require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: fe, FetchedAt: time.Now(),
		Candidates: []json.RawMessage{json.RawMessage(`{"title":"Elantris","author":"Some Author","score":0.9}`)}}))
	rows := planRows(t, st)
	for name, id := range map[string]string{"1Q84": q84, "It": it, "The_Final_Empire": fe} {
		r := rows[id]
		require.False(t, r.Applicable(), "%s: proposed %q over its own filename", name, r.Proposed["title"])
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%s: %s", name, r.SkipReason)
	}
}

func TestWeakStemVeto(t *testing.T) {
	vetoes := map[string]string{"The_Final_Empire": "The Final Empire", "It": "It", "1Q84": "1Q84",
		"dune_messiah_64kbps": "dune messiah"}
	for stem, want := range vetoes {
		got, ok := weakStemVeto(stem)
		require.True(t, ok, stem)
		require.Equal(t, want, got, stem)
	}
	for _, stem := range []string{"hp1", "zz", "final", "merged", "audible_download_2019"} {
		_, ok := weakStemVeto(stem)
		require.False(t, ok, stem)
	}
}

func TestJunkTitleFixer_DottedWeakStemVetoesDisagreeingCandidate(t *testing.T) {
	for _, stem := range []string{"The_Final_Empire", "The.Final.Empire", "the-final-empire-64kbps", "The.Final.Empire.64kbps"} {
		t.Run(stem, func(t *testing.T) {
			st, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })
			a, err := st.CreateAuthor("Some Author")
			require.NoError(t, err)
			id := addJunkBook(t, st, "Unknown Title", &a.ID, "/lib/Some Author/Rips/"+stem+".m4b")
			require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: id, FetchedAt: time.Now(),
				Candidates: []json.RawMessage{json.RawMessage(`{"title":"Elantris","author":"Some Author","score":0.9}`)}}))
			r := planRows(t, st)[id]
			require.False(t, r.Applicable(), "%s: proposed %q over its own filename (%s)", stem, r.Proposed["title"], r.Reason)
		})
	}
}

// filteredCacheDeps serves a CachedMetadataCandidates row that differs from
// the raw store row, as metafetch's version filter makes it differ.
type filteredCacheDeps struct {
	fakeDeps
	filtered map[string]*database.MetadataCandidateCache
}

func (d filteredCacheDeps) CachedMetadataCandidates(bookID string) (*database.MetadataCandidateCache, error) {
	return d.filtered[bookID], nil
}

// The fixer reads candidates the way the apply paths do, through
// CachedMetadataCandidates: a candidate an earlier search version cached and
// the current position rules filter out (a sibling the old ladder pooled) is
// never proposed as the book's title, though the raw row still holds it.
func TestJunkTitleFixer_CandidateReadIsTheFilteredRow(t *testing.T) {
	lib := newJunkLib(t)
	id := lib.ids["candidate"]
	raw := newJunkTitleFixer(&Plugin{deps: fakeDeps{store: lib.store}, standDownWait: noWait})
	title, _, ok := raw.candidateTitle(id, "Author A", nil)
	require.True(t, ok)
	require.Equal(t, "Candidate Title", title)

	filtered := newJunkTitleFixer(&Plugin{deps: filteredCacheDeps{fakeDeps: fakeDeps{store: lib.store},
		filtered: map[string]*database.MetadataCandidateCache{id: {BookID: id}}}, standDownWait: noWait})
	_, _, ok = filtered.candidateTitle(id, "Author A", nil)
	require.False(t, ok, "the filtered row has no candidate; the raw row must not be read")
}

// TestJunkTitleFixer_ProviderRecordedRowApplies: a row whose title state
// carries a provider value plans, re-plans to the same fingerprint, applies,
// and leaves the recorded provider value exactly as it was.
func TestJunkTitleFixer_ProviderRecordedRowApplies(t *testing.T) {
	lib := newJunkLib(t)
	f, res, rows := lib.plan(t)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var plan repairs.PlanResult
	require.NoError(t, json.Unmarshal(raw, &plan))
	id := lib.ids["provider-agrees"]
	fetchedBefore := titleFetched(t, lib.store, id)
	require.NotNil(t, fetchedBefore)

	fresh, err := f.Replan(context.Background(), nil, rows["provider-agrees"], &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, rows["provider-agrees"].Fingerprint, fresh.Fingerprint, "plan and re-plan agree")

	series, err := lib.store.GetAllSeries()
	require.NoError(t, err)
	deps := repairs.ApplyDeps{Guard: lib.store, Series: repairs.SeriesNamesFrom(series), OpID: "op-junk-provider",
		Writer: repairs.NewWriter(lib.store, lib.store, f.ID(), "bulk_update", "repairs-").
			WithJournal(lib.store, lib.store, "op-junk-provider").WithFieldStates(lib.store)}
	out, err := repairs.RunApply(context.Background(), f, &plan, "plan-1", []string{rows["provider-agrees"].RowID}, false, deps, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, out.Applied, "outcomes %v", out.ByOutcome)
	b, err := lib.store.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "Echo Book", b.Title)
	fetchedAfter := titleFetched(t, lib.store, id)
	require.NotNil(t, fetchedAfter)
	require.Equal(t, *fetchedBefore, *fetchedAfter, "the recorded provider value is not rewritten")
}

// TestJunkTitleFixer_UserLockBeatsProviderValue: a title with both a user
// override and a provider value stays user-locked.
func TestJunkTitleFixer_UserLockBeatsProviderValue(t *testing.T) {
	lib := newJunkLib(t)
	fetched := `"Some Provider Title"`
	override := `"Unknown Title"`
	require.NoError(t, lib.store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: lib.ids["locked"], Field: "title",
		FetchedValue: &fetched, OverrideValue: &override, OverrideLocked: true, UpdatedAt: time.Now()}))
	_, _, rows := lib.plan(t)
	require.Equal(t, junkSkipUserLocked, rows["locked"].Skipped, rows["locked"].SkipReason)
}

func titleFetched(t *testing.T, st database.Store, bookID string) *string {
	t.Helper()
	states, err := st.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	for i := range states {
		if states[i].Field == "title" {
			return states[i].FetchedValue
		}
	}
	return nil
}

// The book's ASIN decides among trustworthy candidates: one carrying it wins
// over a higher score, and one naming another ASIN is never proposed.
func TestJunkTitleFixer_CandidateTitlePrefersTheBooksASIN(t *testing.T) {
	cand := func(title, asin string, score float64) json.RawMessage {
		b, err := json.Marshal(map[string]any{"title": title, "author": "Author A", "asin": asin, "score": score})
		require.NoError(t, err)
		return b
	}
	row := &database.MetadataCandidateCache{BookID: "b1", Candidates: []json.RawMessage{
		cand("Other Record", "B00OTHERAS", 0.99),
		cand("No ASIN", "", 0.97),
		cand("The Book Itself", "b00bookasi", 0.92),
	}}
	f := newJunkTitleFixer(&Plugin{deps: filteredCacheDeps{filtered: map[string]*database.MetadataCandidateCache{"b1": row}}, standDownWait: noWait})
	asin := "B00BOOKASI"
	title, score, ok := f.candidateTitle("b1", "Author A", &asin)
	require.True(t, ok)
	require.Equal(t, "The Book Itself", title)
	require.InDelta(t, 0.92, score, 1e-9)

	other := "B00NOMATCH"
	title, _, ok = f.candidateTitle("b1", "Author A", &other)
	require.True(t, ok)
	require.Equal(t, "No ASIN", title, "candidates naming another ASIN are skipped")

	title, _, ok = f.candidateTitle("b1", "Author A", nil)
	require.True(t, ok)
	require.Equal(t, "Other Record", title, "a book with no ASIN keeps the highest score")
}
