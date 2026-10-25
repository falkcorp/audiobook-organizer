// file: internal/plugins/maintenance/junk_title_evidence_test.go
// version: 1.0.5
// guid: 0b7c1d4e-93a6-4f21-8d5a-2e6f7a9c3b18
// last-edited: 2026-10-25

package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Every title in this file is invented. The shapes are the ones an Opus review
// of the 2026-10-23 prod plan found wrong (513 of 624 rows proposed from a
// transcription, filename, folder or series and number), reduced to a pattern
// and given made-up words: no row of a real library is a fixture here.

func TestStripSeriesTail(t *testing.T) {
	cases := []struct{ in, want string }{
		// "Book N of/in …" tails, digit and spelled
		{"Gorselby Lamps, Book 2 of the Brackle series", "Gorselby Lamps"},
		{"Gorselby Lamps Book 5 of The Brackle Wars", "Gorselby Lamps"},
		{"Gorselby Lamps, Book Nine in Brackle Wars, a Tinker Spindle saga", "Gorselby Lamps"},
		{"Gorselby Lamps, a Lamplight LitRPG", "Gorselby Lamps"},
		{"Gorselby Lamps, Book 6 in the Brackle Series", "Gorselby Lamps"},
		{"Gorselby Lamps, Book IV of the Brackle Cycle", "Gorselby Lamps"},
		{"Gorselby Lamps (Book 3 of the Brackle Wars)", "Gorselby Lamps"},
		// genre tails
		{"Gorselby Lamps, a drowsy Quillfeather tale", "Gorselby Lamps"},
		{"Gorselby Lamps, a novel", "Gorselby Lamps"},
		{"Gorselby Lamps, an Open Tide Adventure", "Gorselby Lamps"},
		{"Gorselby Lamps, a Space Fleet Saga of Many Ships", "Gorselby Lamps"},
		// series-name tails
		{"Gorselby Lamps, The Brackle Cycle", "Gorselby Lamps"},
		{"Gorselby Lamps, The Brackle Wars Series", "Gorselby Lamps"},
		// a bare book number is NOT a tail: reviewers accepted these
		{"Gorselby Lamps, Book 9", "Gorselby Lamps, Book 9"},
		{"The Lantern Keeper, book 10", "The Lantern Keeper, book 10"},
		{"Wrath of the Marsh, Book V", "Wrath of the Marsh, Book V"},
		// nothing to strip
		{"Gorselby Lamps", "Gorselby Lamps"},
		{"Gorselby Lamps: A Tale of Salt", "Gorselby Lamps: A Tale of Salt"},
		{"Plank, Wren Hatch, Tobin", "Plank, Wren Hatch, Tobin"},
		{"  Gorselby Lamps  ", "Gorselby Lamps"},
		// only the tail
		{"Book One of Brackle Wars", ""},
		{"", ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, stripSeriesTail(c.in), c.in)
	}
}

func TestBookNumberOf(t *testing.T) {
	cases := map[string]int{
		"Gorselby Lamps, Book 2 of the Brackle series": 2,
		"Gorselby Lamps Book Two":                      2,
		"Gorselby Lamps, Book IV":                      4,
		"Gorselby Lamps book 12":                       12,
		"Gorselby Lamps":                               0,
		"Bookshelf 3":                                  0,
		"Book of Tides":                                0,
	}
	for in, want := range cases {
		require.Equal(t, want, bookNumberOf(in), in)
	}
}

// The comma no longer opens a subtitle, the tails are dropped from both sides,
// and two named book numbers that differ are two books.
func TestJunkEvidenceAgrees(t *testing.T) {
	cases := []struct {
		name     string
		cand     string
		evidence []string
		want     bool
	}{
		{"same title", "Gorselby Lamps", []string{"Gorselby Lamps"}, true},
		{"colon subtitle", "Gorselby Lamps: A Tale of Salt", []string{"Gorselby Lamps"}, true},
		{"parenthesis subtitle", "Gorselby Lamps (Unabridged Tide)", []string{"Gorselby Lamps"}, true},
		{"dash subtitle", "Gorselby Lamps - Salt Edition", []string{"Gorselby Lamps"}, true},
		{"apostrophes ignored", "Captain's Tide", []string{"Captains Tide"}, true},
		{"book-of tail agrees with the folder", "Gorselby Lamps, Book 2 of the Brackle series", []string{"Gorselby Lamps"}, true},
		{"genre tail agrees", "Gorselby Lamps, a drowsy Quillfeather tale", []string{"Gorselby Lamps"}, true},
		{"tail on the evidence side", "Gorselby Lamps", []string{"Gorselby Lamps, Book Two of Brackle Wars"}, true},
		{"comma is not a subtitle", "Gorselby Lamps, The Second Tide", []string{"Gorselby Lamps"}, false},
		{"comma is not a subtitle (reverse)", "Gorselby Lamps", []string{"Gorselby Lamps, The Second Tide"}, false},
		{"different book of the series", "Brackle Court, Book 2 of the Brackle series", []string{"Brackle Court, Book 3"}, false},
		{"another book of the series vetoes a number-less folder", "Brackle Court, Book 2 of the Brackle series", []string{"Brackle Court, Book 3", "Brackle Court"}, false},
		{"same book number", "Brackle Court, Book 2 of the Brackle series", []string{"Brackle Court, Book 2"}, true},
		{"other title", "Lantern Keepers", []string{"Gorselby Lamps"}, false},
		{"longer title is another work", "Gorselby Lamps Son", []string{"Gorselby Lamps"}, false},
		{"series-name tail leaves a different title", "Lantern Keepers, The Brackle Cycle", []string{"Gorselby Lamps"}, false},
		{"no evidence", "Gorselby Lamps", nil, false},
		{"empty candidate", "", []string{"Gorselby Lamps"}, false},
		{"only a tail", "Book One of Brackle Wars", []string{"Gorselby Lamps"}, false},
	}
	for _, c := range cases {
		require.Equal(t, c.want, junkEvidenceAgrees(c.cand, c.evidence), c.name)
	}
	// titleAgreesWithAny, which the swapped title/author fixer reads, keeps
	// its comma.
	require.True(t, titleAgreesWithAny("Gorselby Lamps, The Second Tide", []string{"Gorselby Lamps"}))
}

// Transcription filters: the announcer, the format word, the sentence.
func TestJunkProposalCheck_TranscriptionAnnouncersAndSentences(t *testing.T) {
	pc := junkProposalCheck{stored: "read by narrator", people: []string{"Zed Writer"}}
	refused := map[string]string{
		// announcer phrases and idents
		"an unabridged recording of Gorselby Lamps":             "announcer",
		"the unabridged recording of Gorselby Lamps":            "announcer",
		"Gate Studios presents, Gorselby Lamps":                 "announcer",
		"Simon Example presents Gorselby Lamps":                 "announcer",
		"Marble Gate Studios is pleased to present Tides":       "announcer",
		"You're listening to Gorselby Lamps, Volume 5":          "announcer",
		"You’re listening to Gorselby Lamps":                    "announcer",
		"you are listening to Gorselby Lamps":                   "announcer",
		"And now, part five, of twelve":                         "announcer",
		"And now Part 2 of Gorselby Lamps":                      "announcer",
		"This concludes Gorselby Lamps":                         "announcer",
		"This is the end of Gorselby Lamps":                     "announcer",
		"This has been Gorselby Lamps":                          "announcer",
		"Welcome to Gorselby Lamps":                             "announcer",
		"Side four of the Marble Tides":                         "announcer",
		"Side 9, Salt and Gorsel":                               "announcer",
		"Book One of Scathing Tides":                            "announcer",
		"Episode 1 Gorselby Lamps a Chronicle of Quiet Ferries": "announcer",
		// a format, edition or disc marker
		"Gorselby Lamps (Unabridged)": "format",
		"Gorselby Lamps MP3":          "format",
		"Gorselby Lamps Tape 3":       "format",
		"Gorselby Lamps CD 2":         "format",
		// sentences of the text
		"He turned toward the door":          "prose",
		"She walked into the harbor light":   "prose",
		"They were gone by morning":          "prose",
		"Marta grabbed Oliver":               "prose",
		"Marta looked at the tide":           "prose",
		"The tide came in.":                  "prose",
		"A cold wind blew across the water,": "prose",
		"The captain said \"hold fast\"":     "prose",
		"“Hold fast” said the captain":       "prose",
	}
	for c, want := range refused {
		got := pc.refusal(c, junkSrcTranscribed)
		require.NotEmpty(t, got, "%q must be refused", c)
		require.Contains(t, got, want, c)
	}
	// Titles, the shapes a reviewer accepted: short, name-led, pronoun alone,
	// ending in ? or !, a trailing abbreviation, a subtitle.
	for _, c := range []string{
		"Brindle Quoxx", "Ox", "Tide", "Kreel", "Dorvins", "The Wenvarrow", "Bexil Ploomed Varn",
		"She Quibbled at Tarnwick", "They Plonked Nine Gorsels", "I Counted Barrows at Plimmerside", "Whither Plonk Garrow?", "Zonday at the Prill!", "Dr Quillby Plonks!",
		"Gorselby Lamps: A Tale of Salt", "Gorselby Lamps, Book 9", "Gifts and Presents",
		"Sidewalk Tides", "Welcomed Guests", "Episodes of Salt",
	} {
		require.Empty(t, pc.refusal(c, junkSrcTranscribed), c)
	}
	// The format list used to apply to folder and filename evidence only.
	require.Contains(t, pc.refusal("Gorselby Lamps Tape 3", junkSrcTranscribed), "format")
	// None of this reaches a candidate or provider title.
	require.Empty(t, pc.refusal("Gate Studios presents, Gorselby Lamps", junkSrcProvider))
	require.Empty(t, pc.refusal("He turned toward the door", junkSrcCandidate))
}

// Folder and filename proposals: chapter, track and credit tokens count
// anywhere in the title, not only at the start.
func TestJunkProposalCheck_FolderAndFilenameTokens(t *testing.T) {
	pc := junkProposalCheck{stored: "read by narrator", people: []string{"Zed Writer"}, narrators: nil,
		libraryAuthor: func(s string) bool { return strings.EqualFold(s, "Known Person") }}
	refused := map[string]string{
		"41.Chapter 9 - 12) Tides - read by narrator": "marker",
		"Tides - Chapter 4":                           "marker",
		"Tide Log 1 0 - Prologue + Chapter 1":         "marker",
		"Gorselby Lamps Track 07":                     "marker",
		"Foreword - 1987 Gorselby Lamps":              "marker",
		"Intro - Gorselby Lamps 2033":                 "marker",
		"Recap - The Brackle Wars 9":                  "marker",
		"Unknown Album (3 12 2014 2 -48 -17 PM)":      "marker",
		"Gorselby Lamps - Unknown Author":             "marker",
		"Gorselby Lamps (read by Some Narrator)":      "marker",
		"Gorselby Lamps narrated by Some Narrator":    "marker",
		"Gorselby Lamps - Known Person":               "author credit",
		"Gorselby Lamps 3 - Q. V. Example":            "author credit",
		"Gorselby Lamps, Book 2 - Cassius Example":    "author credit",
		"Gorselby Lamps - Zed Writer":                 "author",
		"Zed Writer - Gorselby Lamps":                 "person",
		"Ripped Gorselby Lamps (Unabridged)":          "format",
		"Gorselby Lamps [B0ZZ99QQ11]":                 "ASIN",
	}
	for c, want := range refused {
		for _, src := range []string{junkSrcFolder, junkSrcFilename} {
			got := pc.refusal(c, src)
			require.NotEmpty(t, got, "%q (%s) must be refused", c, src)
			require.Contains(t, got, want, "%q (%s)", c, src)
		}
	}
	for _, c := range []string{
		"Gorselby Lamps", "Gorselby Lamps - Salt Winds 2", "Dim Wharf - The Salt Wind Part 2", "The Chapter Mill",
		"Salt and Gorsel", "Charity Tides", "Trackless Fen", "Gorselby Lamps - The Second Tide",
		"Dim Wharf - Bram Wickle", // looks like a name; refused below only when the segment is two capitalised words
	} {
		got := pc.refusal(c, junkSrcFolder)
		if c == "Dim Wharf - Bram Wickle" {
			require.Contains(t, got, "author credit", "a segment shaped like a name is refused, not guessed: "+c)
			continue
		}
		require.Empty(t, got, c)
	}
	// The same tokens do not reach a stripped title: chapterHeadingRe and the
	// shared refusals still decide those.
	require.Empty(t, pc.refusal("Gorselby Lamps - Unknown Author", junkSrcStripped))
}

func TestStripReadByCredit(t *testing.T) {
	cases := map[string]string{
		"Gorselby Lamps (read by Some Narrator)":            "Gorselby Lamps",
		"Gorselby Lamps [read by Some Narrator]":            "Gorselby Lamps",
		"Foreword - 1987 Gorselby Lamps - read by narrator": "Foreword - 1987 Gorselby Lamps",
		"41.Chapter 9 - 12) Tides - read by narrator":       "41.Chapter 9 - 12) Tides",
		"Gorselby Lamps":     "Gorselby Lamps",
		"Read by Quillgrass": "Read by Quillgrass",
		"Tides (Unabridged)": "Tides (Unabridged)",
	}
	for in, want := range cases {
		require.Equal(t, want, stripReadByCredit(in), in)
	}
}

func TestJunkStoredAuthor(t *testing.T) {
	for _, junk := range []string{"", "  ", "Unknown Author", "unknown", "read by narrator", "Unknown Title"} {
		require.True(t, junkStoredAuthor(junk), "%q is junk", junk)
	}
	for _, real := range []string{"Zed Writer", "Q. V. Example", "Marta Quill"} {
		require.False(t, junkStoredAuthor(real), "%q is a person", real)
	}
}

func TestSeriesNumberRefusal(t *testing.T) {
	seq := func(n int) *int { return &n }
	cases := []struct {
		name      string
		series    string
		title     string
		seq       *int
		agreeWith []string
		wantWhy   string
	}{
		{"a real series, the recorded position", "Brackle Wars", "03", seq(3), nil, ""},
		{"no recorded position", "Brackle Wars", "03", nil, nil, "no recorded series position"},
		{"disc title", "Brackle Wars", "Disc 03", seq(3), nil, "position inside a book"},
		{"CD title", "Brackle Wars", "CD5", seq(5), nil, "position inside a book"},
		{"N of M title", "Brackle Wars", "02 of 14", seq(1), nil, "position inside a book"},
		{"part title", "Brackle Wars", "Part 02", seq(2), nil, "position inside a book"},
		{"series is a disc", "Disc 04", "04", seq(4), nil, "not a real series name"},
		{"series is a junk title", "read by narrator", "04", seq(4), nil, "not a real series name"},
		{"series is a placeholder album", "Unknown Album", "04", seq(4), nil, "not a real series name"},
		{"series is the stored title", "03", "03", seq(3), nil, "not a real series name"},
		// "of" in a series name is not a position ("2 of 19" is).
		{"series name with of", "The Orb of Quallow", "03", seq(3), nil, ""},
		{"series name with of, book of", "Book of Tides", "03", seq(3), nil, ""},
		// A series that merely contains a position word is a series; one that
		// is a position word with its number is not.
		{"series name with side", "Side Effects of Tides", "03", seq(3), nil, ""},
		{"series name with track", "Track Marks", "03", seq(3), nil, ""},
		{"series name with part", "Part Barrow Quoxx", "03", seq(3), nil, ""},
		{"series name with chapter barn", "Chapter Barn Quoxx", "03", seq(3), nil, ""},
		{"series name with chi", "Chi Alpha Tides", "03", seq(3), nil, ""},
		// A disc, side or volume marker is refused whatever separates the word
		// from its number: a hyphen, a dot or an underscore too.
		{"series ends in a hyphenated CD", "Brackle Wars CD-2", "02", seq(2), nil, "not a real series name"},
		{"series ends in a hyphenated disc", "Brackle Wars Disc-02", "02", seq(2), nil, "not a real series name"},
		{"series is a side letter", "Side A", "02", seq(2), nil, "not a real series name"},
		{"series is a hyphenated side letter", "Side-B", "02", seq(2), nil, "not a real series name"},
		{"series is a dotted roman volume", "Vol.III", "02", seq(2), nil, "not a real series name"},
		{"series ends in a dotted volume number", "Brackle Wars Vol.3", "03", seq(3), nil, "not a real series name"},
		{"series is a tape and a side", "Tape 1 Side B", "02", seq(2), nil, "not a real series name"},
		{"series is a spaced disc", "Disc 04", "04", seq(4), nil, "not a real series name"},
		{"series is a joined CD", "CD1", "01", seq(1), nil, "not a real series name"},
		// …and a series that merely holds one of those words, with a hyphen or a
		// dot beside it, is still a series (the false refusal that made the
		// separator-less form the original).
		{"series with a hyphenated part word", "Part-Time Tides", "03", seq(3), nil, ""},
		{"series with disc and a word", "Disc-Cover Tides", "03", seq(3), nil, ""},
		{"series with side dish", "Side Dish Tides", "03", seq(3), nil, ""},
		{"series with side-eye", "Side-Eye Tides", "03", seq(3), nil, ""},
		{"series with sidecar", "Sidecar Tides", "03", seq(3), nil, ""},
		{"series with a hyphenated chi", "Chi-Alpha Tides", "03", seq(3), nil, ""},
		{"series with a dotted vol word", "Vol.Cano Tides", "03", seq(3), nil, ""},
		{"series is a side and number", "Side 3", "03", seq(3), nil, "not a real series name"},
		{"series is a part and roman number", "Part IV", "03", seq(3), nil, "not a real series name"},
		{"series is a spaced N of M", "3 of 12", "03", seq(3), nil, "not a real series name"},
		{"disc with an underscore", "Brackle Wars", "CD_01", seq(1), nil, "position inside a book"},
		{"track of a total, spaced", "Brackle Wars", "3 of 19", seq(3), nil, "position inside a book"},
		{"series is the book's folder", "Quimby Gutters", "03", seq(3), []string{"Quimby Gutters"}, "own folder or title"},
	}
	for _, c := range cases {
		b := database.BookCore{Title: c.title, SeriesSequence: c.seq}
		got := seriesNumberRefusal(c.series, b, nil, c.agreeWith)
		if c.wantWhy == "" {
			require.Empty(t, got, c.name)
			continue
		}
		require.Contains(t, got, c.wantWhy, c.name)
	}
}

// ---- plan-level decisions ----

// evLib is a small real Pebble library for the plan-level decisions of the
// evidence ranking. Each case adds its own books.
type evLib struct {
	*junkLib
	t *testing.T
}

func newEvLib(t *testing.T) *evLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return &evLib{junkLib: &junkLib{store: st, ids: map[string]string{}}, t: t}
}

type evBook struct {
	name, title, path string
	author            *int
	series            *int
	seq               *int
	duration          *int
	state             string
	transcribed       string
	files             []string
	// narrator is the book's narrator credit. coAuthors are author rows linked
	// to the book through the book_authors junction only (role "co-author"),
	// the one trace a co-author has.
	narrator  string
	coAuthors []*int
	// unknownRuntime leaves the book with no duration at all. A book with
	// neither a duration nor fileDurations otherwise gets junkFixtureRuntimeSec:
	// the fragment gate fails closed on an unknown runtime.
	unknownRuntime bool
	// fileDurations, when set, is each file's duration in seconds (same order
	// as files); the book's own aggregate is then whatever the store derives.
	fileDurations []int
	// providerTitle and providerAuthor are what a provider recorded for the
	// title and author (JSON-encoded when stored).
	providerTitle, providerAuthor string
}

func (l *evLib) add(b evBook) string {
	l.t.Helper()
	st := l.store
	if b.duration == nil && len(b.fileDurations) == 0 && !b.unknownRuntime {
		b.duration = &junkFixtureRuntimeSec
	}
	book := &database.Book{Title: b.title, FilePath: b.path, AuthorID: b.author, SeriesID: b.series,
		SeriesSequence: b.seq, Duration: b.duration, Format: "mp3"}
	if b.state != "" {
		s := b.state
		book.LibraryState = &s
	}
	if b.narrator != "" {
		n := b.narrator
		book.Narrator = &n
	}
	created, err := st.CreateBook(book)
	require.NoError(l.t, err)
	if len(b.coAuthors) > 0 {
		var rows []database.BookAuthor
		if b.author != nil {
			rows = append(rows, database.BookAuthor{BookID: created.ID, AuthorID: *b.author, Role: "author", Position: 0})
		}
		for _, c := range b.coAuthors {
			rows = append(rows, database.BookAuthor{BookID: created.ID, AuthorID: *c, Role: "co-author", Position: len(rows)})
		}
		require.NoError(l.t, st.SetBookAuthors(created.ID, rows))
	}
	for i, f := range b.files {
		bf := &database.BookFile{BookID: created.ID, FilePath: f, Format: "mp3"}
		if i < len(b.fileDurations) {
			bf.Duration = b.fileDurations[i]
		}
		require.NoError(l.t, st.CreateBookFile(bf))
	}
	if b.transcribed != "" {
		_, err := st.ModifyBook(created.ID, func(bk *database.Book) error { bk.TranscribedTitle = &b.transcribed; return nil })
		require.NoError(l.t, err)
	}
	if b.providerTitle != "" {
		v := `"` + b.providerTitle + `"`
		require.NoError(l.t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: created.ID, Field: "title",
			FetchedValue: &v, UpdatedAt: time.Now()}))
	}
	if b.providerAuthor != "" {
		v := `"` + b.providerAuthor + `"`
		require.NoError(l.t, st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: created.ID, Field: "author_name",
			FetchedValue: &v, UpdatedAt: time.Now()}))
	}
	l.ids[b.name] = created.ID
	return created.ID
}

func (l *evLib) author(name string) *int {
	l.t.Helper()
	a, err := l.store.CreateAuthor(name)
	require.NoError(l.t, err)
	return &a.ID
}

// authorWithBook adds an author and one real-titled book credited to it: an
// author row nobody is credited to is not an author of the library.
func (l *evLib) authorWithBook(name string) *int {
	l.t.Helper()
	id := l.author(name)
	l.add(evBook{name: "owned by " + name, title: "Tides Told by " + name, author: id,
		path: "/lib/" + name + "/book.m4b", files: []string{"/lib/" + name + "/book.m4b"}})
	return id
}

func intp(n int) *int { return &n }

func (l *evLib) rows(f *junkTitleFixer) map[string]repairs.Row {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), f, nil,
		repairs.PlanDeps{Guard: l.store, Series: repairs.SeriesNamesFrom(series)}, &fakeReporter{})
	require.NoError(l.t, err)
	out := map[string]repairs.Row{}
	for name, id := range l.ids {
		for _, r := range res.Rows {
			if r.RowID == id {
				out[name] = r
			}
		}
	}
	return out
}

func (l *evLib) fixer() *junkTitleFixer {
	return newJunkTitleFixer(&Plugin{deps: fakeDeps{store: l.store}, standDownWait: noWait})
}

// Fix 1: a provider's title and the title without its prefix outrank the
// transcription; a series tail of the transcription is dropped; a transcription
// that names another book of the series is a conflict.
func TestJunkTitleFixer_EvidenceRanking(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	weak := func(name string) []string { return []string{"/lib/Zed Writer/" + name + "/zz.mp3"} }
	// The provider recorded the title under the book's author; the
	// transcription misheard it. The weak filename stem "zz" is no evidence.
	l.add(evBook{name: "provider-over-heard", title: "read by narrator", path: weak("p1")[0], author: a,
		files: weak("p1"), providerTitle: "Lantern Keepers", providerAuthor: "Zed Writer", transcribed: "Lantern Keeprs"})
	// The transcription is the same title plus the publisher's series line.
	l.add(evBook{name: "tail-dropped", title: "read by narrator", path: weak("p2")[0], author: a,
		files: weak("p2"), transcribed: "Orchard Keep, Book Nine in the Brackle Wars, a Tinker Spindle saga"})
	// The stripped title and the transcription agree (a subtitle); the
	// stripped one is proposed, at low risk, not the longer transcription.
	l.add(evBook{name: "stripped-over-heard", title: "05 - Gorselby Lamps", path: "/lib/Zed Writer/Gorselby Lamps/Gorselby Lamps.mp3", author: a,
		files: []string{"/lib/Zed Writer/Gorselby Lamps/Gorselby Lamps.mp3"}, transcribed: "Gorselby Lamps: A Tale of Salt"})
	// "X, Book N of …" no longer counts as the folder's subtitle when it is
	// another book of the series than the title's own.
	l.add(evBook{name: "other-book", title: "03 - Brackle Court, Book 3", path: "/lib/Zed Writer/Brackle Court/Brackle Court.mp3", author: a,
		files: []string{"/lib/Zed Writer/Brackle Court/Brackle Court.mp3"}, transcribed: "Brackle Court, Book 2 of the Brackle series"})
	// A transcription of a different title still conflicts with the path.
	l.add(evBook{name: "plain-conflict", title: "06 - Salt Garden", path: "/lib/Zed Writer/Salt Garden/Salt Garden.mp3", author: a,
		files: []string{"/lib/Zed Writer/Salt Garden/Salt Garden.mp3"}, transcribed: "Salt Orchard, Book 6 of the Brackle Wars"})
	rows := l.rows(l.fixer())

	r := rows["provider-over-heard"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Lantern Keepers", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from provider_value")
	require.Contains(t, r.Reason, `transcription: "Lantern Keeprs"`)

	r = rows["tail-dropped"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Orchard Keep", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from transcription")
	require.Contains(t, r.Reason, "tail of the transcription")
	require.Equal(t, repairs.RiskReview, r.Risk)

	r = rows["stripped-over-heard"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Gorselby Lamps", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from title_prefix_stripped")
	require.Equal(t, repairs.RiskLow, r.Risk)

	r = rows["other-book"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "conflicting evidence")

	r = rows["plain-conflict"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "conflicting evidence")
}

// Fix 2 at plan level: an announcer transcription is refused and nothing is
// proposed from it.
func TestJunkTitleFixer_AnnouncerTranscriptionIsNotProposed(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	for i, heard := range []string{
		"an unabridged recording of Gorselby Lamps", "Gate Studios presents, Gorselby Lamps",
		"You're listening to Gorselby Lamps, Volume 5", "And now, part five, of twelve", "Marta grabbed Oliver",
	} {
		name := string(rune('a' + i))
		p := "/lib/Zed Writer/" + name + "/zz.mp3"
		l.add(evBook{name: name, title: "read by narrator", path: p, author: a, files: []string{p}, transcribed: heard})
	}
	for name, r := range l.rows(l.fixer()) {
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%s: %v", name, r.Proposed)
		require.Contains(t, r.SkipReason, "only a weak filename stem", name)
	}
}

// Fix 3 and 4 at plan level: a filename that carries only a placeholder author
// after the title, and a folder with a chapter token in the middle.
func TestJunkTitleFixer_FilenameAndFolderShapes(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	l.authorWithBook("Q. V. Example") // a library author: a credited filename's " - Q. V. Example" is the author
	l.add(evBook{name: "placeholder-author", title: "read by narrator", author: nil,
		path:  "/lib/Books/Marble Gate - Unknown Author - read by narrator.mp3",
		files: []string{"/lib/Books/Marble Gate - Unknown Author - read by narrator.mp3"}})
	l.add(evBook{name: "library-author-suffix", title: "read by narrator", author: nil,
		path:  "/lib/Books/Salt Road 3 - Q. V. Example.mp3",
		files: []string{"/lib/Books/Salt Road 3 - Q. V. Example.mp3"}})
	l.add(evBook{name: "library-author-credited", title: "read by narrator", author: nil,
		path:  "/lib/Books/Salt Road 3 - Q. V. Example - read by narrator.mp3",
		files: []string{"/lib/Books/Salt Road 3 - Q. V. Example - read by narrator.mp3"}})
	l.add(evBook{name: "unknown-name-suffix", title: "read by narrator", author: nil,
		path:  "/lib/Books/Iron Tide - Cassius Example.mp3",
		files: []string{"/lib/Books/Iron Tide - Cassius Example.mp3"}})
	l.add(evBook{name: "credit-only", title: "read by narrator", author: a,
		path: "/lib/Books/read by Some Narrator.mp3", files: []string{"/lib/Books/read by Some Narrator.mp3"}})
	l.add(evBook{name: "chapter-folder", title: "read by narrator", author: a,
		path:  "/lib/Zed Writer/41.Chapter 9 - 12) Tides - read by narrator/01.mp3",
		files: []string{"/lib/Zed Writer/41.Chapter 9 - 12) Tides - read by narrator/01.mp3", "/lib/Zed Writer/41.Chapter 9 - 12) Tides - read by narrator/02.mp3"}})
	l.add(evBook{name: "credit-in-folder", title: "read by narrator", author: a,
		path:  "/lib/Zed Writer/Glass Orchard (read by Some Narrator)/01.mp3",
		files: []string{"/lib/Zed Writer/Glass Orchard (read by Some Narrator)/01.mp3", "/lib/Zed Writer/Glass Orchard (read by Some Narrator)/02.mp3"}})
	rows := l.rows(l.fixer())

	r := rows["placeholder-author"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Marble Gate", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from filename")

	// Nothing says the tail is the author when the stem has no credit, so the
	// tail stays in the candidate and the candidate is refused as an author
	// credit; with a credit behind it the library author is the author.
	r = rows["library-author-suffix"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, "%v %s", r.Proposed, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	r = rows["library-author-credited"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Salt Road 3", r.Proposed["title"])

	r = rows["unknown-name-suffix"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, "an author-shaped tail is refused, not guessed")
	require.Contains(t, r.SkipReason, "author credit")

	for _, name := range []string{"credit-only", "chapter-folder"} {
		require.Equal(t, junkSkipNeedsManual, rows[name].Skipped, "%s: %v %s", name, rows[name].Proposed, rows[name].SkipReason)
	}
	require.Contains(t, rows["chapter-folder"].SkipReason, "chapter, track, credit or placeholder marker")

	// A credit in brackets is dropped and the title kept.
	r = rows["credit-in-folder"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Glass Orchard", r.Proposed["title"])
}

// Fix 5 at plan level: "<series>, Book <N>" is proposed only for a real series
// and the book's own series position. Every case is its own library: the
// generic folder "Books" gives the chapter-titled book no folder proposal, and
// a second book there would make the pair a possible fragment.
func TestJunkTitleFixer_SeriesAndNumber(t *testing.T) {
	cases := []struct {
		name, title, series string
		seq                 *int
		wantTitle           string // "" = not proposed
	}{
		{"real series, own position", "03", "Brackle Wars", intp(3), "Brackle Wars, Book 3"},
		{"number is not the position", "03", "Brackle Wars", intp(4), ""},
		{"no recorded position", "03", "Brackle Wars", nil, ""},
		{"disc title", "Disc 03", "Brackle Wars", intp(3), ""},
		{"CD title", "CD5", "Brackle Wars", intp(5), ""},
		{"track of a total", "02 of 14", "Brackle Wars", intp(1), ""},
		{"part title", "Part 02", "Brackle Wars", intp(2), ""},
		{"series is a disc", "04", "Disc 04", intp(4), ""},
		{"series is a placeholder album", "02", "Unknown Album", intp(2), ""},
		{"series is a junk title", "02", "read by narrator", intp(2), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newEvLib(t)
			ser, err := l.store.CreateSeries(c.series, nil)
			require.NoError(t, err)
			l.add(evBook{name: "b", title: c.title, path: "/lib/Books/01.mp3", series: &ser.ID, seq: c.seq,
				files: []string{"/lib/Books/01.mp3", "/lib/Books/02.mp3"}})
			r := l.rows(l.fixer())["b"]
			if c.wantTitle == "" {
				require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
				require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
				return
			}
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			require.Equal(t, c.wantTitle, r.Proposed["title"])
			require.Contains(t, r.Reason, "proposed from series_and_number")
		})
	}
}

// Fix 6: a book under the minimum duration, or flagged suspicious, is not
// retitled from a transcription, folder or filename; the prefix-stripped title
// is the file's own and holds either way; an unknown duration fails closed
// (needs_manual), because nothing says the book is a whole one.
func TestJunkTitleFixer_DurationAndSuspiciousGate(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		duration *int
		state    string
		min      *int // nil = the default
		want     string
		skipWhy  string
	}{
		{"9 s intro fragment", "read by narrator", intp(9), "", nil, "", "runs 9 s"},
		{"five minute track", "read by narrator", intp(300), "", nil, "", "under the 1800 s"},
		{"just under", "read by narrator", intp(1799), "", nil, "", "runs 1799 s"},
		{"exactly the minimum", "read by narrator", intp(1800), "", nil, "Lantern Keepers", ""},
		{"a whole book", "read by narrator", intp(36000), "", nil, "Lantern Keepers", ""},
		{"unknown duration", "read by narrator", nil, "", nil, "", "runtime unknown"},
		{"zero duration is unknown", "read by narrator", intp(0), "", nil, "", "runtime unknown"},
		{"unknown duration, gate off", "read by narrator", nil, "", intp(0), "Lantern Keepers", ""},
		{"unknown duration, stripped prefix", "05 - Lantern Keepers", nil, "", nil, "Lantern Keepers", ""},
		{"suspicious", "read by narrator", intp(36000), "suspicious", nil, "", "marked the book suspicious"},
		{"suspicious, unknown duration", "read by narrator", nil, "Suspicious", nil, "", "marked the book suspicious"},
		{"another library state", "read by narrator", intp(36000), "imported", nil, "Lantern Keepers", ""},
		{"gate off", "read by narrator", intp(9), "", intp(0), "Lantern Keepers", ""},
		{"stripped prefix is not gated", "05 - Lantern Keepers", intp(9), "", nil, "Lantern Keepers", ""},
		{"stripped prefix, suspicious", "05 - Lantern Keepers", intp(9), "suspicious", nil, "Lantern Keepers", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newEvLib(t)
			a := l.author("Zed Writer")
			l.add(evBook{name: "b", title: c.title, path: "/lib/Zed Writer/Lantern Keepers.mp3", author: a,
				files: []string{"/lib/Zed Writer/zz.mp3"}, duration: c.duration, unknownRuntime: c.duration == nil,
				state: c.state, transcribed: "Lantern Keepers"})
			f := l.fixer()
			if c.min != nil {
				f.minDurationSec = *c.min
			}
			r := l.rows(f)["b"]
			if c.want == "" {
				wantSkip := junkSkipPossibleFragment
				if c.skipWhy == "runtime unknown" {
					wantSkip = junkSkipNeedsManual // fails closed, held for a person
				}
				require.Equal(t, wantSkip, r.Skipped, "proposed %q: %s", r.Proposed["title"], r.SkipReason)
				require.Contains(t, r.SkipReason, c.skipWhy)
				return
			}
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			require.Equal(t, c.want, r.Proposed["title"])
		})
	}
}

// Fix 8: a junk stored author cannot vouch for a provider's title by author,
// so the title must agree with the path or the transcription; when it does not,
// the row is skipped and the folder is not used instead.
func TestJunkTitleFixer_ProviderTitleWithJunkAuthor(t *testing.T) {
	l := newEvLib(t)
	real := l.author("Zed Writer")
	junk, err := l.store.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	folder := func(name string) []string {
		return []string{"/lib/Books/" + name + "/01.mp3", "/lib/Books/" + name + "/02.mp3"}
	}
	// the provider title and the transcription agree: accepted although the
	// recorded author cannot be compared with the stored one
	l.add(evBook{name: "agrees-with-transcription", title: "read by narrator", author: &junk.ID, path: "/lib/Books/zz.mp3",
		files: []string{"/lib/Books/zz.mp3"}, providerTitle: "Wumbo Spindle Eleven", providerAuthor: "Zed Writer", transcribed: "Wumbo Spindle Eleven"})
	// the provider title extends the folder with a subtitle: accepted
	l.add(evBook{name: "agrees-with-folder", title: "read by narrator", author: nil, path: folder("Real Folder")[0],
		files: folder("Real Folder"), providerTitle: "Real Folder: The Sequel", providerAuthor: "Zed Writer"})
	// the provider title names something else than the folder: skipped, and
	// the folder is not the fallback
	l.add(evBook{name: "contradicts-folder", title: "read by narrator", author: &junk.ID, path: folder("Other Folder")[0],
		files: folder("Other Folder"), providerTitle: "Totally Different Book", providerAuthor: "Zed Writer"})
	// no evidence at all but the provider's title: skipped, never guessed
	l.add(evBook{name: "provider-alone", title: "read by narrator", author: nil, path: "/lib/Books/yy.mp3",
		files: []string{"/lib/Books/yy.mp3"}, providerTitle: "Lone Provider Title", providerAuthor: "Zed Writer"})
	// a REAL stored author keeps the author gate: recorded under someone
	// else, the provider's title is not used and the folder stands
	l.add(evBook{name: "real-author-other-recorded", title: "read by narrator", author: real, path: folder("Third Folder")[0],
		files: folder("Third Folder"), providerTitle: "Totally Different Book", providerAuthor: "Somebody Else"})
	rows := l.rows(l.fixer())

	r := rows["agrees-with-transcription"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Wumbo Spindle Eleven", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from provider_value")

	r = rows["agrees-with-folder"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Real Folder: The Sequel", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from provider_value")

	for _, name := range []string{"contradicts-folder", "provider-alone"} {
		r = rows[name]
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%s proposed %q", name, r.Proposed["title"])
		require.Contains(t, r.SkipReason, "stored author is junk", name)
	}

	r = rows["real-author-other-recorded"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Third Folder", r.Proposed["title"])
	require.Contains(t, r.Reason, "not recorded under this book's author")
}

// Fix 7: the duplicate guard is keyed on the proposal's owner, the provider's
// or the folder's author when the stored one is junk, and a proposal two junk
// rows share is a possible fragment for both.
func TestJunkTitleFixer_DuplicateGuardWithJunkAuthor(t *testing.T) {
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = "/lib"
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	l := newEvLib(t)
	zed := l.author("Zed Writer")
	unknown, err := l.store.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	unknownTitle, err := l.store.CreateAuthor("Unknown Title")
	require.NoError(t, err)
	two := func(dir string) []string { return []string{dir + "/01.mp3", dir + "/02.mp3"} }

	// the library already has a real book of Zed Writer's
	l.add(evBook{name: "existing", title: "Marble Gate", author: zed, path: "/lib/Zed Writer/Marble Gate/book.m4b",
		files: []string{"/lib/Zed Writer/Marble Gate/book.m4b"}})
	// junk author, folder author is Zed Writer: the same book
	l.add(evBook{name: "same-folder-author", title: "read by narrator", author: &unknown.ID,
		path: "/lib/Zed Writer/Marble Gate/01.mp3", files: two("/lib/Zed Writer/Marble Gate")})
	// junk author, provider names Zed Writer: the same book
	l.add(evBook{name: "same-provider-author", title: "read by narrator", author: &unknownTitle.ID,
		path: "/mnt/x/Marble Gate/01.mp3", files: two("/mnt/x/Marble Gate"), providerTitle: "Marble Gate", providerAuthor: "Zed Writer"})
	// junk author, folder author is someone else: not the same book
	l.add(evBook{name: "other-folder-author", title: "read by narrator", author: &unknown.ID,
		path: "/lib/Other Person/Marble Gate/01.mp3", files: two("/lib/Other Person/Marble Gate")})
	// junk author, no author known at all: the title alone decides
	l.add(evBook{name: "no-identity", title: "read by narrator", author: &unknown.ID,
		path: "/mnt/y/Marble Gate/01.mp3", files: two("/mnt/y/Marble Gate")})
	// two junk rows, two different junk authors, no identity, one proposal
	l.add(evBook{name: "twin-a", title: "read by narrator", author: &unknown.ID,
		path: "/mnt/a/Lone Tower/01.mp3", files: two("/mnt/a/Lone Tower")})
	l.add(evBook{name: "twin-b", title: "read by narrator", author: &unknownTitle.ID,
		path: "/mnt/b/Lone Tower/01.mp3", files: two("/mnt/b/Lone Tower")})
	// two junk rows, one proposal, two different folder authors: two books
	l.add(evBook{name: "apart-a", title: "read by narrator", author: &unknown.ID,
		path: "/lib/Alpha Person/Cold Spire/01.mp3", files: two("/lib/Alpha Person/Cold Spire")})
	l.add(evBook{name: "apart-b", title: "read by narrator", author: &unknownTitle.ID,
		path: "/lib/Beta Person/Cold Spire/01.mp3", files: two("/lib/Beta Person/Cold Spire")})
	rows := l.rows(l.fixer())

	for _, name := range []string{"same-folder-author", "same-provider-author", "no-identity"} {
		r := rows[name]
		require.Equal(t, junkSkipPossibleFragment, r.Skipped, "%s proposed %q (%s)", name, r.Proposed["title"], r.SkipReason)
		require.Contains(t, r.SkipReason, `a book titled "Marble Gate"`, name)
	}
	require.Contains(t, rows["same-folder-author"].SkipReason, "a book by Zed Writer already has")
	require.Contains(t, rows["no-identity"].SkipReason, "any author")

	r := rows["other-folder-author"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Marble Gate", r.Proposed["title"])

	for _, name := range []string{"twin-a", "twin-b"} {
		r := rows[name]
		require.Equal(t, junkSkipPossibleFragment, r.Skipped, name)
		require.Contains(t, r.SkipReason, `would all be retitled "Lone Tower"`, name)
	}
	for _, name := range []string{"apart-a", "apart-b"} {
		r := rows[name]
		require.True(t, r.Applicable(), "%s: %s: %s", name, r.Skipped, r.SkipReason)
		require.Equal(t, "Cold Spire", r.Proposed["title"])
	}
}

// A position in a work ("Volume II") is title text even when a scan stored it
// as the book's author: a reviewer accepted "Gorselby Lamps - Volume II".
func TestTitleFromFilename_PositionSegmentIsNotAnAuthor(t *testing.T) {
	require.Equal(t, "Gorselby Lamps - Volume II", titleFromFilename("Gorselby Lamps - Volume II.mp3", "Volume II", authorRecognisers{}))
	require.Equal(t, "Gorselby Lamps - Part 3", titleFromFilename("Gorselby Lamps - Part 3.mp3", "", authorRecognisers{}))
	// The stored author is stripped behind a credit; with none it may be this
	// name's own last segment read back, so the stem stays whole.
	require.Equal(t, "Gorselby Lamps", titleFromFilename("Gorselby Lamps - Zed Writer - read by narrator.mp3", "Zed Writer", authorRecognisers{}))
	require.Equal(t, "Gorselby Lamps - Zed Writer", titleFromFilename("Gorselby Lamps - Zed Writer.mp3", "Zed Writer", authorRecognisers{}))
}
