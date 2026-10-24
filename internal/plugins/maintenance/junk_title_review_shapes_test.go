// file: internal/plugins/maintenance/junk_title_review_shapes_test.go
// version: 1.0.0
// guid: 5d2e8a1c-47b3-4c90-9e6f-3a1b7c0d8e24
// last-edited: 2026-10-24

package maintenance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Every title in this file is invented. The shapes are the ones the second
// review of the junk-title fixer's plan found slipping or wrongly cut, reduced
// to a pattern and given made-up words.

func TestStripRecordedSeries(t *testing.T) {
	const ser = "Ember Wars"
	cases := []struct{ in, series, want string }{
		// tail: "<title> [,.] <series> Book N"
		{"Harbor Lights Ember Wars Book 3", ser, "Harbor Lights"},
		{"Harbor Lights, Ember Wars, Book 3", ser, "Harbor Lights"},
		{"Harbor Lights, the Ember Wars, Book Three", ser, "Harbor Lights"},
		{"Harbor Lights. Ember Wars Book IV", ser, "Harbor Lights"},
		{"harbor lights ember wars book 3", ser, "harbor lights"},
		// prefix: "<series> [,] Book N [,] <title>"
		{"Ember Wars, Book 3, Harbor Lights", ser, "Harbor Lights"},
		{"Ember Wars Book 3 Harbor Lights", ser, "Harbor Lights"},
		{"The Ember Wars - Book 3 - Harbor Lights", ser, "Harbor Lights"},
		// the series' own leading "The" and an apostrophe a transcription lacks
		{"Harbor Lights, Ember Wars Book 2", "The Ember Wars", "Harbor Lights"},
		{"Harbor Lights Captains Tide Book 2", "Captain's Tide", "Harbor Lights"},
		// look-alikes of titles a reviewer accepted stay as they are
		{"Harbor Lights, Book 9", ser, "Harbor Lights, Book 9"},
		{"Lantern Mage 8", "Lantern Mage", "Lantern Mage 8"},
		{"The Lantern Keeper, book 10", ser, "The Lantern Keeper, book 10"},
		{"Wrath of the Marsh, Book V", ser, "Wrath of the Marsh, Book V"},
		// nothing after the number is the book's whole title, not a tail
		{"Ember Wars, Book 3", ser, "Ember Wars, Book 3"},
		{"Ember Wars Book 3", ser, "Ember Wars Book 3"},
		// a remainder that is not a title of its own
		{"Wrath of the Ember Wars Book 3", ser, "Wrath of the Ember Wars Book 3"},
		{"Ember Wars Book 3 7 Harbor", ser, "Ember Wars Book 3 7 Harbor"},
		{"Ember Wars, Book 3, Ember Wars", ser, "Ember Wars, Book 3, Ember Wars"},
		{"Ember Wars, Book 3, Book 4", ser, "Ember Wars, Book 3, Book 4"},
		// another series is not this book's
		{"Harbor Lights Salt Road Book 3", ser, "Harbor Lights Salt Road Book 3"},
		// no recorded series
		{"Harbor Lights Ember Wars Book 3", "", "Harbor Lights Ember Wars Book 3"},
		{"  Harbor Lights  ", ser, "Harbor Lights"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, stripRecordedSeries(c.in, c.series), "%q / %q", c.in, c.series)
	}
}

func TestStartsLowercase(t *testing.T) {
	for in, want := range map[string]bool{
		"iron salt white lantern book five": true,
		"the wall be":                       true,
		"  and so on":                       true,
		"(lowercase after a bracket":        true,
		"Salt Postal":                       false,
		"I, Lantern":                        false,
		"3 Tides":                           false,
		"3 tides":                           false,
		"":                                  false,
		"...":                               false,
	} {
		require.Equal(t, want, startsLowercase(in), in)
	}
}

// Shapes the second review found proposed as titles: a bare "Book N", a
// lowercase run of words, roman and parenthesised positions, a bare section
// name. The accepted look-alikes stay.
func TestJunkProposalCheck_SecondReviewShapes(t *testing.T) {
	pc := junkProposalCheck{stored: "read by narrator", people: []string{"Zed Writer"}}

	transcribed := map[string]string{
		"Book 4":                             "announcer",
		"Book Four":                          "announcer",
		"book 12.":                           "announcer",
		"iron salt white lantern book five":  "prose",
		"and the wall came down in the tide": "prose",
		"Prologue":                           "section",
		"Chapter XIII":                       "junk itself",
		"CHAPTER XIII The Wall":              "chapter",
		"Part III Salt and Stone":            "chapter",
	}
	for c, want := range transcribed {
		got := pc.refusal(c, junkSrcTranscribed)
		require.NotEmpty(t, got, "%q must be refused", c)
		require.Contains(t, got, want, c)
	}
	for _, c := range []string{
		"Booked Solid", "Book of Tides", "The Book Thief of Salt", "Tide", "Civil Tide", "Chapter House", "Part Time Gods",
	} {
		require.Empty(t, pc.refusal(c, junkSrcTranscribed), c)
	}

	markers := []string{
		"Harbor Lights Part (16)", "Harbor Lights-Part (16)", "Harbor Lights (1 of 5)", "Harbor Lights [2 of 12]",
		"Harbor Lights [XYZ]", "Harbor Lights (2020)", "Harbor Lights - Track IV", "Harbor Lights Chapter XIII",
		"Harbor Lights - CHAPTER XIV",
	}
	for _, c := range markers {
		for _, src := range []string{junkSrcFolder, junkSrcFilename} {
			require.Contains(t, pc.refusal(c, src), "marker", "%q (%s)", c, src)
		}
	}
	for _, c := range []string{
		"Harbor Lights", "Harbor Lights Part 2", "Harbor Lights 2020", "Harbor Lights - The Civil Tide", "Harbor Lights - The Track Mix",
		"The Last Part of the Tide",
	} {
		for _, src := range []string{junkSrcFolder, junkSrcFilename} {
			require.Empty(t, pc.refusal(c, src), "%q (%s)", c, src)
		}
	}

	// A bare section name is refused from every source that goes through it.
	for _, src := range []string{junkSrcFolder, junkSrcFilename, junkSrcTranscribed} {
		require.Contains(t, pc.refusal("Prologue", src), "section", src)
	}
}

// The library recogniser: a library author refuses a folder or filename
// proposal that is, or starts or ends with, that name in either order; a name
// that is also a live book title is not a library author.
func TestJunkProposalCheck_LibraryAuthorHeadAndWhole(t *testing.T) {
	lib := map[string]bool{"zed writer": true, "writer, zed": true, "q. v. example": true, "example, q. v.": true}
	pc := junkProposalCheck{stored: "read by narrator",
		libraryAuthor: func(s string) bool { return lib[strings.ToLower(s)] }}
	refused := map[string]string{
		"Zed Writer - Salt Road":      "starts with a person",
		"Writer, Zed - Salt Road":     "starts with a person",
		"Q. V. Example - Salt Road":   "starts with a person",
		"Zed Writer":                  "names an author",
		"Salt Road - Zed Writer":      "author credit",
		"Salt Road 3 - Q. V. Example": "author credit",
	}
	for c, want := range refused {
		for _, src := range []string{junkSrcFolder, junkSrcFilename} {
			got := pc.refusal(c, src)
			require.NotEmpty(t, got, "%q (%s) must be refused", c, src)
			require.Contains(t, got, want, "%q (%s)", c, src)
		}
	}
	// Only a folder or a filename is judged this way: a provider's or the
	// transcription's title can be a title that shares a name with an author.
	for _, src := range []string{junkSrcCandidate, junkSrcProvider, junkSrcStripped} {
		require.Empty(t, pc.refusal("Zed Writer", src), src)
	}
}

func TestBookRuntimeSec(t *testing.T) {
	d := func(n int) *int { return &n }
	f := func(secs ...int) []database.BookFile {
		var out []database.BookFile
		for _, s := range secs {
			out = append(out, database.BookFile{Duration: s})
		}
		return out
	}
	cases := []struct {
		name  string
		book  database.BookCore
		files []database.BookFile
		want  int
		known bool
	}{
		{"the book's own duration", database.BookCore{Duration: d(900)}, f(100), 900, true},
		{"sum of files", database.BookCore{}, f(600, 700, 800), 2100, true},
		{"zero duration falls to the files", database.BookCore{Duration: d(0)}, f(600, 600), 1200, true},
		// A partial sum would call a whole book short.
		{"one file unknown", database.BookCore{}, f(600, 0, 800), 0, false},
		{"no files", database.BookCore{}, nil, 0, false},
		{"all files unknown", database.BookCore{}, f(0, 0), 0, false},
	}
	for _, c := range cases {
		got, known := bookRuntimeSec(c.book, c.files)
		require.Equal(t, c.known, known, c.name)
		require.Equal(t, c.want, got, c.name)
	}
}

func TestJunkTitleFixer_FileDurationsReachTheGate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		durations []int
		want      string // "" = held as a possible fragment
	}{
		{"short pieces", []int{300, 300}, ""},
		{"whole book in pieces", []int{900, 900, 900}, "Lantern Keepers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newEvLib(t)
			a := l.author("Zed Writer")
			var files []string
			for i := range tc.durations {
				files = append(files, "/lib/Zed Writer/zz"+string(rune('a'+i))+".mp3")
			}
			l.add(evBook{name: "b", title: "read by narrator", path: files[0], author: a, files: files,
				fileDurations: tc.durations, transcribed: "Lantern Keepers"})
			r := l.rows(l.fixer())["b"]
			if tc.want == "" {
				require.Equal(t, junkSkipPossibleFragment, r.Skipped, "%v: %s", r.Proposed, r.SkipReason)
				return
			}
			require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
			require.Equal(t, tc.want, r.Proposed["title"])
		})
	}
}

// A junk stored author is not stripped from a file name, and the marker in
// the stem it would have swallowed still refuses the title; a library author
// is stripped behind a credit and only once. (Review findings S1, S2, S3.)
func TestJunkTitleFixer_FilenameAuthorStripping(t *testing.T) {
	l := newEvLib(t)
	zed := l.author("Zed Writer")
	l.authorWithBook("Q. V. Example")
	chapterAuthor, err := l.store.CreateAuthor("Prologue + Chapter 1")
	require.NoError(t, err)
	// A title-shaped author row: a book titled "Salt Harbor" exists too.
	l.author("Salt Harbor")
	l.add(evBook{name: "salt-harbor", title: "Salt Harbor", author: zed, path: "/lib/Zed Writer/Salt Harbor/book.m4b",
		files: []string{"/lib/Zed Writer/Salt Harbor/book.m4b"}})

	// S2: the chapter label sits in the author field; it is title text.
	l.add(evBook{name: "junk-author-is-a-chapter", title: "read by narrator", author: &chapterAuthor.ID,
		path:  "/lib/Books/Tide Log 1 0 - Prologue + Chapter 1.mp3",
		files: []string{"/lib/Books/Tide Log 1 0 - Prologue + Chapter 1.mp3"}})
	// S3: the head before " - " is a library author, either order.
	l.add(evBook{name: "author-first", title: "read by narrator", author: nil,
		path: "/lib/Books/Zed Writer - Salt Road.mp3", files: []string{"/lib/Books/Zed Writer - Salt Road.mp3"}})
	l.add(evBook{name: "last-first", title: "read by narrator", author: nil,
		path: "/lib/Books/Writer, Zed - Salt Road.mp3", files: []string{"/lib/Books/Writer, Zed - Salt Road.mp3"}})
	// S1: a title-shaped author row before the real author. Only the credited
	// segment goes; the other is title text, and a segment shaped like a name
	// is refused rather than guessed.
	l.add(evBook{name: "title-shaped-segment", title: "read by narrator", author: nil,
		path:  "/lib/Books/Ember Wars 13 - Salt Harbor - Zed Writer - read by narrator.mp3",
		files: []string{"/lib/Books/Ember Wars 13 - Salt Harbor - Zed Writer - read by narrator.mp3"}})
	// An author row nobody is credited to is not an author of the library: the
	// tail is not stripped (and a name-shaped tail is refused, not guessed).
	_, err = l.store.CreateAuthor("Orphan Writer")
	require.NoError(t, err)
	l.add(evBook{name: "orphan-author-row", title: "read by narrator", author: nil,
		path:  "/lib/Books/Salt Road 4 - Orphan Writer - read by narrator.mp3",
		files: []string{"/lib/Books/Salt Road 4 - Orphan Writer - read by narrator.mp3"}})
	// The head of "<series> - Volume II" is the series even when the author
	// table holds a row of that name that has a book: it is not name-shaped.
	l.authorWithBook("The Wheel Keepers")
	l.add(evBook{name: "series-shaped-head", title: "read by narrator", author: nil,
		path:  "/lib/Books/The Wheel Keepers - Volume II.mp3",
		files: []string{"/lib/Books/The Wheel Keepers - Volume II.mp3"}})
	// A credited library author is the author, whatever the title.
	l.add(evBook{name: "credited-library-author", title: "read by narrator", author: nil,
		path:  "/lib/Books/Salt Road 3 - Zed Writer - read by narrator.mp3",
		files: []string{"/lib/Books/Salt Road 3 - Zed Writer - read by narrator.mp3"}})
	rows := l.rows(l.fixer())

	for _, name := range []string{"junk-author-is-a-chapter", "author-first", "last-first"} {
		r := rows[name]
		require.False(t, r.Applicable(), "%s proposed %q", name, r.Proposed["title"])
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%s: %s", name, r.SkipReason)
	}
	require.Contains(t, rows["junk-author-is-a-chapter"].SkipReason, "marker")
	require.Contains(t, rows["author-first"].SkipReason, "person")
	require.Contains(t, rows["last-first"].SkipReason, "person")

	// The title is never cut to "Ember Wars 13": whatever the row does, it
	// does not propose that.
	if r := rows["title-shaped-segment"]; r.Applicable() {
		require.Equal(t, "Ember Wars 13 - Salt Harbor", r.Proposed["title"])
	}

	r := rows["orphan-author-row"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, "%v %s", r.Proposed, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	r = rows["series-shaped-head"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "The Wheel Keepers - Volume II", r.Proposed["title"])

	r = rows["credited-library-author"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Salt Road 3", r.Proposed["title"])
}

// S4 and the spelling tie-break: with a junk stored author the file name's
// title corroborates a provider's, and when the provider's title only differs
// from the path's by punctuation the path's spelling is kept.
func TestJunkTitleFixer_ProviderCorroboratedByFilenameAndSpelling(t *testing.T) {
	l := newEvLib(t)
	junk, err := l.store.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	// S4: the file name says the provider's title; the folder is generic.
	l.add(evBook{name: "filename-corroborates", title: "read by narrator", author: &junk.ID,
		path: "/lib/Books/Lantern Keepers.mp3", files: []string{"/lib/Books/Lantern Keepers.mp3"},
		providerTitle: "Lantern Keepers", providerAuthor: "Zed Writer"})
	// The provider's title is the file name's with a colon for the dash.
	l.add(evBook{name: "spelling", title: "read by narrator", author: &junk.ID,
		path: "/lib/Books/Marble Gate/Marble Gate - Volume II.mp3", files: []string{"/lib/Books/Marble Gate/Marble Gate - Volume II.mp3"},
		providerTitle: "Marble Gate: Volume II", providerAuthor: "Zed Writer"})
	rows := l.rows(l.fixer())

	r := rows["filename-corroborates"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Lantern Keepers", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from filename")
	require.Contains(t, r.Reason, `a metadata provider recorded the title "Lantern Keepers"`)

	r = rows["spelling"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Marble Gate - Volume II", r.Proposed["title"])
	require.Contains(t, r.Reason, "path's spelling was kept")
}

// S5: a catalog title that is the transcription's title plus a tagline
// subtitle is not proposed; with nothing echoing it the catalog title stands.
func TestJunkTitleFixer_CatalogTaglineIsNotProposed(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	weak := func(name string) []string { return []string{"/lib/Zed Writer/" + name + "/zz.mp3"} }
	// The transcription and the folder-less path agree on the short title.
	l.add(evBook{name: "tagline-over-heard", title: "read by narrator", path: weak("t1")[0], author: a, files: weak("t1"),
		providerTitle: "Orchard Keep 2: A Windswept Harbor Romance", providerAuthor: "Zed Writer", transcribed: "Orchard Keep 2"})
	// Nothing echoes the tagline title: it is the only evidence and the
	// title text itself, so it stays.
	l.add(evBook{name: "tagline-alone", title: "read by narrator", path: "/lib/Zed Writer/Lone Keep/zz.mp3", author: a,
		files:         []string{"/lib/Zed Writer/Lone Keep/zz.mp3"},
		providerTitle: "Lone Keep: A Windswept Harbor Romance", providerAuthor: "Zed Writer"})
	rows := l.rows(l.fixer())

	r := rows["tagline-over-heard"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Orchard Keep 2", r.Proposed["title"])
	require.Contains(t, r.Reason, "plus a tagline subtitle")

	r = rows["tagline-alone"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Lone Keep: A Windswept Harbor Romance", r.Proposed["title"])
}

// S6: the same proposal for a real-author book and a junk-author one is held
// for both when the owners can be the same person, and left alone when they
// are two different people.
func TestJunkTitleFixer_DuplicateGuardAcrossRealAndJunkOwners(t *testing.T) {
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = "/lib"
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	two := func(dir string) []string { return []string{dir + "/01.mp3", dir + "/02.mp3"} }
	build := func(t *testing.T, junkBook evBook) map[string]string {
		l := newEvLib(t)
		zed := l.author("Zed Writer")
		unknown, err := l.store.CreateAuthor("Unknown Author")
		require.NoError(t, err)
		l.add(evBook{name: "real", title: "read by narrator", author: zed,
			path: "/lib/Zed Writer/Marble Gate/01.mp3", files: two("/lib/Zed Writer/Marble Gate")})
		junkBook.name, junkBook.title, junkBook.author = "junk", "read by narrator", &unknown.ID
		l.add(junkBook)
		rows := l.rows(l.fixer())
		return map[string]string{"real": rows["real"].Skipped, "junk": rows["junk"].Skipped}
	}
	t.Run("same provider author", func(t *testing.T) {
		got := build(t, evBook{path: "/mnt/x/Marble Gate/01.mp3", files: two("/mnt/x/Marble Gate"),
			providerTitle: "Marble Gate", providerAuthor: "Zed Writer"})
		require.Equal(t, junkSkipPossibleFragment, got["real"])
		require.Equal(t, junkSkipPossibleFragment, got["junk"])
	})
	t.Run("unknown owner is anybody", func(t *testing.T) {
		got := build(t, evBook{path: "/mnt/y/Marble Gate/01.mp3", files: two("/mnt/y/Marble Gate")})
		require.Equal(t, junkSkipPossibleFragment, got["real"])
		require.Equal(t, junkSkipPossibleFragment, got["junk"])
	})
	t.Run("different owners", func(t *testing.T) {
		got := build(t, evBook{path: "/mnt/z/Marble Gate/01.mp3", files: two("/mnt/z/Marble Gate"),
			providerTitle: "Marble Gate", providerAuthor: "Other Person"})
		require.Empty(t, got["real"])
		require.Empty(t, got["junk"])
	})
}

// S7: a comma after a transcription's title is a subtitle for the
// transcription only: it agrees with the stripped title, and another book of
// the series still conflicts.
func TestJunkTitleFixer_TranscriptionCommaSubtitleAgreesWithStripped(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	l.add(evBook{name: "comma-subtitle", title: "05 - Harbor Lights", path: "/lib/Zed Writer/Harbor Lights/Harbor Lights.mp3", author: a,
		files: []string{"/lib/Zed Writer/Harbor Lights/Harbor Lights.mp3"}, transcribed: "Harbor Lights, The Second Tide"})
	l.add(evBook{name: "other-title", title: "06 - Salt Garden", path: "/lib/Zed Writer/Salt Garden/Salt Garden.mp3", author: a,
		files: []string{"/lib/Zed Writer/Salt Garden/Salt Garden.mp3"}, transcribed: "Salt Orchard, The Second Tide"})
	rows := l.rows(l.fixer())

	r := rows["comma-subtitle"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Harbor Lights", r.Proposed["title"])
	require.Contains(t, r.Reason, "proposed from title_prefix_stripped")

	r = rows["other-title"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "conflicting evidence")
}

// The shapes of the provider_value rows a reviewer accepted: a real stored
// author, the provider's title recorded under that author, nothing else to
// compare it with. The tagline and spelling rules leave them as they are.
func TestJunkTitleFixer_AcceptedProviderShapesStay(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	l.add(evBook{name: "volume-subtitle", title: "read by narrator", author: a, path: "/lib/Books/zz1.mp3",
		files: []string{"/lib/Books/zz1.mp3"}, providerTitle: "Tide Reborn as a Lantern: Volume 1", providerAuthor: "Zed Writer"})
	l.add(evBook{name: "multi-colon", title: "Intro", author: a, path: "/lib/Books/zz2.mp3",
		files: []string{"/lib/Books/zz2.mp3"}, providerTitle: "Salt Saga: The New Tide: Edge of Harbor II: Rebirth", providerAuthor: "Zed Writer"})
	rows := l.rows(l.fixer())
	for name, want := range map[string]string{
		"volume-subtitle": "Tide Reborn as a Lantern: Volume 1",
		"multi-colon":     "Salt Saga: The New Tide: Edge of Harbor II: Rebirth",
	} {
		r := rows[name]
		require.True(t, r.Applicable(), "%s: %s: %s", name, r.Skipped, r.SkipReason)
		require.Equal(t, want, r.Proposed["title"], name)
		require.Contains(t, r.Reason, "proposed from provider_value", name)
	}
}
