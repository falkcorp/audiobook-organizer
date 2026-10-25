// file: internal/plugins/maintenance/junk_title_review_shapes_test.go
// version: 1.0.2
// guid: 5d2e8a1c-47b3-4c90-9e6f-3a1b7c0d8e24
// last-edited: 2026-10-25

package maintenance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Every title in this file is invented. The shapes are the ones the second
// review of the junk-title fixer's plan found slipping or wrongly cut, reduced
// to a pattern and given made-up words.

func TestStripRecordedSeries(t *testing.T) {
	const ser = "Ember Wars"
	cases := []struct{ in, series, want string }{
		// tail: "<title> [,.;:-] <series> Book N", the separator explicit
		{"Harbor Lights, Ember Wars Book 3", ser, "Harbor Lights"},
		{"Harbor Lights, Ember Wars, Book 3", ser, "Harbor Lights"},
		{"Harbor Lights, the Ember Wars, Book Three", ser, "Harbor Lights"},
		{"Harbor Lights. Ember Wars Book IV", ser, "Harbor Lights"},
		{"Harbor Lights - Ember Wars - Book 3", ser, "Harbor Lights"},
		{"harbor lights; ember wars book 3", ser, "harbor lights"},
		// a series that follows the title after a plain space is left in the
		// text: it may end the title ("Second Quillmoor Book 3" in series
		// "Quillmoor" is the title "Second Quillmoor"), and nothing tells the
		// two readings apart
		{"Harbor Lights Ember Wars Book 3", ser, "Harbor Lights Ember Wars Book 3"},
		{"harbor lights ember wars book 3", ser, "harbor lights ember wars book 3"},
		{"Second Quillmoor Book 3", "Quillmoor", "Second Quillmoor Book 3"},
		{"Second Quillmoor, Book 3", "Quillmoor", "Second Quillmoor, Book 3"},
		{"Tarn Hollow Quillmoor Book 6", "Quillmoor", "Tarn Hollow Quillmoor Book 6"},
		// prefix: "<series> [,] Book N [,] <title>"
		{"Ember Wars, Book 3, Harbor Lights", ser, "Harbor Lights"},
		{"Ember Wars Book 3 Harbor Lights", ser, "Harbor Lights"},
		{"The Ember Wars - Book 3 - Harbor Lights", ser, "Harbor Lights"},
		// the series' own leading "The" and an apostrophe a transcription lacks
		{"Harbor Lights, Ember Wars Book 2", "The Ember Wars", "Harbor Lights"},
		{"Harbor Lights, Captains Tide Book 2", "Captain's Tide", "Harbor Lights"},
		// look-alikes of titles a reviewer accepted stay as they are
		{"Harbor Lights, Book 9", ser, "Harbor Lights, Book 9"},
		{"Orchard Wizzle 8", "Orchard Wizzle", "Orchard Wizzle 8"},
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
		{"Harbor Lights, Ember Wars Book 3", "", "Harbor Lights, Ember Wars Book 3"},
		{"  Harbor Lights  ", ser, "Harbor Lights"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, stripRecordedSeries(c.in, c.series, nil), "%q / %q", c.in, c.series)
	}
	// The series can end a real title that has a separator before it too
	// ("Dark, Ember Wars Book 3"): with no title in sight the text before
	// "Book N" is a title and a series tail, with one it is a title.
	live := func(s string) bool { return strings.EqualFold(s, "Dark Ember Wars") }
	require.Equal(t, "Dark", stripRecordedSeries("Dark, Ember Wars Book 3", ser, nil))
	require.Equal(t, "Dark, Ember Wars Book 3", stripRecordedSeries("Dark, Ember Wars Book 3", ser, live))
	require.Equal(t, "Dark - Ember Wars, Book 3", stripRecordedSeries("Dark - Ember Wars, Book 3", ser, live))
	require.Equal(t, "Harbor Lights", stripRecordedSeries("Harbor Lights, Ember Wars Book 3", ser, live),
		"a live title elsewhere does not stop the strip")
	// The regexps are cached by series name: repeated calls, a name with
	// regexp metacharacters included, answer the same.
	for range 2 {
		require.Equal(t, "Harbor Lights", stripRecordedSeries("Harbor Lights, Ember (Wars Book 3", "Ember (Wars", nil))
		require.Equal(t, "Harbor Lights Book 3", stripRecordedSeries("Harbor Lights Book 3", "Ember (Wars", nil))
	}
}

func TestStartsLowercase(t *testing.T) {
	for in, want := range map[string]bool{
		"iron salt white lantern book five": true,
		"the wall be":                       true,
		"  and so on":                       true,
		"(lowercase after a bracket":        true,
		"Brindle Quoxx":                     false,
		"I Left My Hat in Wexmoor":          false,
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
		"Bookeddle Prill", "Book of Tides", "Brindle's Book Plinth", "Tide", "Civil Tide", "Chapter Hollow", "Part Quillow Gods",
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
		want      string // "" = held, as heldAs
		heldAs    string
	}{
		{"short pieces", []int{300, 300}, "", junkSkipPossibleFragment},
		{"whole book in pieces", []int{900, 900, 900}, "Lantern Keepers", ""},
		// No file has a duration (the store keeps no aggregate either): the
		// runtime is unknown, and unknown fails closed. bookRuntimeSec's unit
		// test covers a partial set.
		{"no file has a duration", []int{0, 0}, "", junkSkipNeedsManual},
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
				require.Equal(t, tc.heldAs, r.Skipped, "%v: %s", r.Proposed, r.SkipReason)
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
	l.authorWithBook("The Quallow Wardens")
	l.add(evBook{name: "series-shaped-head", title: "read by narrator", author: nil,
		path:  "/lib/Books/The Quallow Wardens - Volume II.mp3",
		files: []string{"/lib/Books/The Quallow Wardens - Volume II.mp3"}})
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

	// The title is never cut to "Ember Wars 13": the credited segment goes,
	// "Salt Harbor" is name-shaped and no author of the library (the live book
	// of that title holds the name; its row has no book), so the tail is
	// refused rather than guessed. The live-title guard itself is decided in
	// TestJunkTitleFixer_LibraryAuthorGuards, where the row has other books.
	r := rows["title-shaped-segment"]
	require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	r = rows["orphan-author-row"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, "%v %s", r.Proposed, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	r = rows["series-shaped-head"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "The Quallow Wardens - Volume II", r.Proposed["title"])

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

// The provider_value shape the fixer must leave alone: a real stored author,
// the provider's title recorded under that author, nothing else to compare it
// with (invented titles). The tagline and spelling rules leave them as they are.
func TestJunkTitleFixer_AcceptedProviderShapesStay(t *testing.T) {
	l := newEvLib(t)
	a := l.author("Zed Writer")
	l.add(evBook{name: "volume-subtitle", title: "read by narrator", author: a, path: "/lib/Books/zz1.mp3",
		files: []string{"/lib/Books/zz1.mp3"}, providerTitle: "Quillow Tales: Volume 1", providerAuthor: "Zed Writer"})
	l.add(evBook{name: "multi-colon", title: "Intro", author: a, path: "/lib/Books/zz2.mp3",
		files: []string{"/lib/Books/zz2.mp3"}, providerTitle: "Harbor Lights: Salt: Tide", providerAuthor: "Zed Writer"})
	rows := l.rows(l.fixer())
	for name, want := range map[string]string{
		"volume-subtitle": "Quillow Tales: Volume 1",
		"multi-colon":     "Harbor Lights: Salt: Tide",
	} {
		r := rows[name]
		require.True(t, r.Applicable(), "%s: %s: %s", name, r.Skipped, r.SkipReason)
		require.Equal(t, want, r.Proposed["title"], name)
		require.Contains(t, r.Reason, "proposed from provider_value", name)
	}
}

// S1, decided by the two conditions of isLibraryAuthor and by nothing else.
// Every author row here is credited to a book other than the one judged, or
// would be but for the condition under test, so the "no other book" rule
// cannot be what holds the title.
//
//   - Live title: the author row has other books and its name is also a live
//     book's title ("Marlow Finch" is a person and a title). It is not an
//     author of the library, so the segment before the credit is title text
//     and stays. Without the guard it is stripped.
//   - Self-exclusion: the stored author's only book is the judged one, and the
//     file name spells the name the other way round ("Last, First"), so no
//     stored-author or own-person match strips it first. The row is the very
//     author the stem was read back from, so it is no evidence of an author:
//     the segment stays and the name-shaped tail is refused. Without the
//     exclusion it counts the judged book and the segment is stripped.
func TestJunkTitleFixer_LibraryAuthorGuards(t *testing.T) {
	l := newEvLib(t)
	zed := l.author("Zed Writer")

	// Live title.
	l.authorWithBook("Marlow Finch")
	l.add(evBook{name: "holds-the-title", title: "Marlow Finch", author: zed, path: "/lib/Zed Writer/Marlow Finch/book.m4b",
		files: []string{"/lib/Zed Writer/Marlow Finch/book.m4b"}})
	l.authorWithBook("The Last Lantern")
	l.add(evBook{name: "holds-the-other-title", title: "The Last Lantern", author: zed, path: "/lib/Zed Writer/Last Lantern/book.m4b",
		files: []string{"/lib/Zed Writer/Last Lantern/book.m4b"}})
	l.add(evBook{name: "live-title-not-name-shaped", title: "read by narrator", author: nil,
		path:  "/lib/u00/Harbor Dusk - The Last Lantern - read by narrator.mp3",
		files: []string{"/lib/u00/Harbor Dusk - The Last Lantern - read by narrator.mp3"}})
	l.add(evBook{name: "live-title-segment", title: "read by narrator", author: nil,
		path:  "/lib/u01/Harbor Dusk - Marlow Finch - read by narrator.mp3",
		files: []string{"/lib/u01/Harbor Dusk - Marlow Finch - read by narrator.mp3"}})
	// The same shape with an author row no live book is titled after: the
	// segment is the author and goes.
	l.authorWithBook("Wren Marsh")
	l.add(evBook{name: "not-a-title-segment", title: "read by narrator", author: nil,
		path:  "/lib/u02/Harbor Dusk - Wren Marsh - read by narrator.mp3",
		files: []string{"/lib/u02/Harbor Dusk - Wren Marsh - read by narrator.mp3"}})

	// Self-exclusion.
	alone := l.author("Alma Reed")
	l.add(evBook{name: "self-only-book", title: "read by narrator", author: alone,
		path:  "/lib/u03/Dawn Orchard - Reed, Alma - read by narrator.mp3",
		files: []string{"/lib/u03/Dawn Orchard - Reed, Alma - read by narrator.mp3"}})
	// The same shape where the author has another book: the segment is the
	// author, whichever way round the name is spelled.
	other := l.authorWithBook("Mina Quill")
	l.add(evBook{name: "has-other-book", title: "read by narrator", author: other,
		path:  "/lib/u04/Dawn Orchard - Quill, Mina - read by narrator.mp3",
		files: []string{"/lib/u04/Dawn Orchard - Quill, Mina - read by narrator.mp3"}})
	rows := l.rows(l.fixer())

	// A name-shaped segment that survives the guard is still a refusal (a
	// "Title - Name" tail is never guessed), so the guard shows as a held row
	// where the strip would have proposed "Harbor Dusk".
	r := rows["live-title-segment"]
	require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	r = rows["live-title-not-name-shaped"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Harbor Dusk - The Last Lantern", r.Proposed["title"])

	r = rows["not-a-title-segment"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Harbor Dusk", r.Proposed["title"])

	r = rows["self-only-book"]
	require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	r = rows["has-other-book"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Dawn Orchard", r.Proposed["title"])
}

// A co-author has no trace but the book_authors junction. Such a row is an
// author of the library when another live book credits it there, and the
// judged book is left out of its own co-authors' counts. With no " - read by "
// credit in the stem the segment goes only for a name with evidence beyond the
// stem, so the junction is what decides.
func TestJunkTitleFixer_CoAuthorsAreCountedWithoutTheJudgedBook(t *testing.T) {
	l := newEvLib(t)
	zed := l.author("Zed Writer")
	// "Mina Quill" is linked to one other book only through the junction.
	mina := l.author("Mina Quill")
	l.add(evBook{name: "minas-other-book", title: "Tides Told Twice", author: zed, coAuthors: []*int{mina},
		path: "/lib/Zed Writer/Tides Told Twice/book.m4b", files: []string{"/lib/Zed Writer/Tides Told Twice/book.m4b"}})
	l.add(evBook{name: "co-author-elsewhere", title: "read by narrator", author: zed, coAuthors: []*int{mina},
		path: "/lib/u05/Dawn Orchard - Mina Quill.mp3", files: []string{"/lib/u05/Dawn Orchard - Mina Quill.mp3"}})
	// "Nell Harbor" is linked to the judged book and to no other.
	nell := l.author("Nell Harbor")
	l.add(evBook{name: "co-author-only-here", title: "read by narrator", author: zed, coAuthors: []*int{nell},
		path: "/lib/u06/Dawn Orchard - Nell Harbor.mp3", files: []string{"/lib/u06/Dawn Orchard - Nell Harbor.mp3"}})
	rows := l.rows(l.fixer())

	r := rows["co-author-elsewhere"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Dawn Orchard", r.Proposed["title"])

	r = rows["co-author-only-here"]
	require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")
}

// zedOwner is a throwaway author for a book that only has to exist.
func zedOwner(l *evLib) *int { return l.author("Elsewhere Writer") }

// S2/SF4: with no " - read by " credit the stem is a bare "<Title> - <Segment>",
// which the scanner reads as a title and an author. A stored author with no
// other book is that segment read back, so stripping it is circular and the
// subtitle it cuts off goes with it. A stored author with other books, or the
// book's narrator who narrates another book, is evidence of its own, and a
// credit vouches for any of the book's people.
func TestJunkTitleFixer_NoCreditStripNeedsIndependentEvidence(t *testing.T) {
	l := newEvLib(t)
	// The stored author's only book is the judged one: the segment is kept
	// (and, name-shaped, refused rather than guessed).
	circular := l.author("Moss Keeper")
	l.add(evBook{name: "circular", title: "read by narrator", author: circular,
		path: "/lib/u07/Quiet Orchard - Moss Keeper.mp3", files: []string{"/lib/u07/Quiet Orchard - Moss Keeper.mp3"}})
	// The same stem with a credit: the author goes.
	credited := l.author("Wren Keeper")
	l.add(evBook{name: "credited", title: "read by narrator", author: credited,
		path:  "/lib/u08/Quiet Orchard - Wren Keeper - read by narrator.mp3",
		files: []string{"/lib/u08/Quiet Orchard - Wren Keeper - read by narrator.mp3"}})
	// The stored author has another book: independent, so the author goes.
	established := l.authorWithBook("Ivy Keeper")
	l.add(evBook{name: "established", title: "read by narrator", author: established,
		path: "/lib/u09/Quiet Orchard - Ivy Keeper.mp3", files: []string{"/lib/u09/Quiet Orchard - Ivy Keeper.mp3"}})
	// The segment is the narrator, who is on the book's narrator field and
	// narrates another book of the library under a real title.
	l.add(evBook{name: "narrated elsewhere", title: "Tides Told Thrice", narrator: "Oliver Narrate",
		author: zedOwner(l), path: "/lib/Elsewhere/book.m4b", files: []string{"/lib/Elsewhere/book.m4b"}})
	soloAuthor := l.author("Fern Writer")
	l.add(evBook{name: "narrator", title: "read by narrator", author: soloAuthor, narrator: "Oliver Narrate",
		path: "/lib/u10/Quiet Orchard - Oliver Narrate.mp3", files: []string{"/lib/u10/Quiet Orchard - Oliver Narrate.mp3"}})
	rows := l.rows(l.fixer())

	r := rows["circular"]
	require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "author credit")

	for name, want := range map[string]string{"credited": "Quiet Orchard", "established": "Quiet Orchard", "narrator": "Quiet Orchard"} {
		r := rows[name]
		require.True(t, r.Applicable(), "%s: %s: %s", name, r.Skipped, r.SkipReason)
		require.Equal(t, want, r.Proposed["title"], name)
	}
}

// SF3: a real-author proposal is held by the same rule Plan applies between
// two proposals (ownersMayBeSame), not by author id alone: a live book of a
// duplicate row of the same person, or of no author, holds the title too.
func TestJunkTitleFixer_RealOwnerDuplicateGuardSharesTheOwnerRule(t *testing.T) {
	two := func(dir string) []string { return []string{dir + "/01.mp3", dir + "/02.mp3"} }
	build := func(t *testing.T, holder func(l *evLib, zed *int) evBook) repairs.Row {
		l := newEvLib(t)
		zed := l.author("J.M. Quill")
		l.add(evBook{name: "proposal", title: "read by narrator", author: zed, transcribed: "Marble Gate",
			path: "/lib/Books/Marble Gate/01.mp3", files: two("/lib/Books/Marble Gate")})
		h := holder(l, zed)
		h.name, h.title = "holder", "Marble Gate"
		l.add(h)
		return l.rows(l.fixer())["proposal"]
	}
	const held = `already has a book titled "Marble Gate"`
	requireHeld := func(t *testing.T, r repairs.Row) {
		t.Helper()
		require.Equal(t, junkSkipPossibleFragment, r.Skipped, "proposed %q: %s", r.Proposed["title"], r.SkipReason)
		require.Contains(t, r.SkipReason, held)
	}
	hold := func(path string, author func(l *evLib) *int) func(*evLib, *int) evBook {
		return func(l *evLib, _ *int) evBook {
			return evBook{author: author(l), path: path, files: []string{path}}
		}
	}
	t.Run("same author id", func(t *testing.T) {
		requireHeld(t, build(t, func(l *evLib, zed *int) evBook {
			return evBook{author: zed, path: "/lib/h/a.m4b", files: []string{"/lib/h/a.m4b"}}
		}))
	})
	t.Run("duplicate row of the same person", func(t *testing.T) {
		requireHeld(t, build(t, hold("/lib/h/b.m4b", func(l *evLib) *int { return l.author("J. M. Quill") })))
	})
	t.Run("no author", func(t *testing.T) {
		requireHeld(t, build(t, hold("/lib/h/c.m4b", func(*evLib) *int { return nil })))
	})
	t.Run("junk author", func(t *testing.T) {
		requireHeld(t, build(t, hold("/lib/h/d.m4b", func(l *evLib) *int {
			a, err := l.store.CreateAuthor("Unknown Author")
			require.NoError(t, err)
			return &a.ID
		})))
	})
	t.Run("another person", func(t *testing.T) {
		r := build(t, hold("/lib/h/e.m4b", func(l *evLib) *int { return l.author("Zed Writer") }))
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, "Marble Gate", r.Proposed["title"])
	})
}

// NIT-4: the author a provider recorded names the proposal's owner only when
// the provider's title was not refused. Here the provider's title disagrees
// with the title's own evidence, so it is another book's and so is its author:
// the owner is unknown, and a live book of any author holds the title.
func TestJunkTitleFixer_RefusedProviderAuthorIsNotTheOwner(t *testing.T) {
	l := newEvLib(t)
	zed := l.author("Zed Writer")
	l.add(evBook{name: "holder", title: "Marble Gate", author: zed, path: "/lib/h/a.m4b", files: []string{"/lib/h/a.m4b"}})
	unknown, err := l.store.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	l.add(evBook{name: "refused-provider", title: "05 - Marble Gate", author: &unknown.ID,
		path: "/lib/u11/Marble Gate.mp3", files: []string{"/lib/u11/Marble Gate.mp3"},
		providerTitle: "Salt Orchard", providerAuthor: "Other Person"})
	// The provider's title agrees with the path (the catalog tagline is not
	// proposed): its author is the owner, a different person from the
	// holder's, so nothing is held.
	l.add(evBook{name: "accepted-provider", title: "05 - Cold Spire", author: &unknown.ID,
		path: "/lib/u12/Cold Spire.mp3", files: []string{"/lib/u12/Cold Spire.mp3"},
		providerTitle: "Cold Spire: The Tower", providerAuthor: "Other Person"})
	l.add(evBook{name: "holder-2", title: "Cold Spire", author: zed, path: "/lib/h/b.m4b", files: []string{"/lib/h/b.m4b"}})
	rows := l.rows(l.fixer())

	r := rows["refused-provider"]
	require.Equal(t, junkSkipPossibleFragment, r.Skipped, "proposed %q: %s", r.Proposed["title"], r.SkipReason)
	require.Contains(t, r.SkipReason, `already has a book titled "Marble Gate"`)

	r = rows["accepted-provider"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Cold Spire", r.Proposed["title"])
}

// NIT-3: the conflict check reads the transcription without the book's own
// recorded series, as the proposal does: "<series>, Book 3, <title>" agrees
// with a path that says <title>.
func TestJunkTitleFixer_RecordedSeriesInTranscriptionIsNoConflict(t *testing.T) {
	l := newEvLib(t)
	zed := l.author("Zed Writer")
	ser, err := l.store.CreateSeries("Ember Wars", nil)
	require.NoError(t, err)
	l.add(evBook{name: "series-prefixed", title: "05 - Harbor Lights", author: zed, series: &ser.ID, seq: intp(3),
		path:  "/lib/Zed Writer/Harbor Lights/Harbor Lights.mp3",
		files: []string{"/lib/Zed Writer/Harbor Lights/Harbor Lights.mp3"}, transcribed: "Ember Wars, Book 3, Harbor Lights"})
	l.add(evBook{name: "other-title", title: "06 - Salt Garden", author: zed, series: &ser.ID, seq: intp(4),
		path:  "/lib/Zed Writer/Salt Garden/Salt Garden.mp3",
		files: []string{"/lib/Zed Writer/Salt Garden/Salt Garden.mp3"}, transcribed: "Ember Wars, Book 4, Salt Orchard"})
	rows := l.rows(l.fixer())

	r := rows["series-prefixed"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Harbor Lights", r.Proposed["title"])

	r = rows["other-title"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "conflicting evidence")
}

// NIT-10: the author classification pass runs on a pool; the index it builds
// must not depend on the scheduling, and a junk name must stay out of it.
func TestJunkTitleFixer_IndexAuthorPassIsOrderIndependent(t *testing.T) {
	l := newEvLib(t)
	const n = 1300 // more than two chunks of the pool
	for i := range n {
		l.author(fmt.Sprintf("Quill Person%04d", i))
	}
	unknown, err := l.store.CreateAuthor("Unknown Author")
	require.NoError(t, err)
	chapter, err := l.store.CreateAuthor("Prologue + Chapter 1")
	require.NoError(t, err)
	idx, _, err := l.fixer().buildJunkIndex()
	require.NoError(t, err)
	require.Len(t, idx.authorNames, n)
	for i := range n {
		require.Len(t, idx.authorNames[util.NormalizeAuthor(fmt.Sprintf("Quill Person%04d", i))], 1, i)
	}
	for _, id := range []int{unknown.ID, chapter.ID} {
		for _, ids := range idx.authorNames {
			require.NotContains(t, ids, id)
		}
	}
}

// seriesEndsText is the shape stripRecordedSeries cannot cut: the series after a
// plain space and a book number, with something before it.
func TestSeriesEndsText(t *testing.T) {
	const ser = "Ember Wars"
	for _, c := range []string{
		"Harbor Lights Ember Wars Book 3", "Second Quillmoor Book 3", "harbor lights ember wars, book IV",
		"Harbor Lights Ember Wars Book Three.",
	} {
		series := ser
		if strings.Contains(c, "Quillmoor") {
			series = "Quillmoor"
		}
		require.True(t, seriesEndsText(c, series), c)
	}
	for _, c := range []string{
		"Ember Wars Book 3",                // the book's whole title
		"Ember Wars, Book 3",               // …
		"Harbor Lights, Ember Wars Book 3", // an explicit separator: stripRecordedSeries cuts it
		"Dark - Ember Wars, Book 3",
		"Harbor Lights Ember Wars Book 3 Salt Road", // not at the end
		"Harbor Lights",
		"Harbor Lights Salt Road Book 3", // another series
	} {
		require.False(t, seriesEndsText(c, ser), c)
	}
	require.False(t, seriesEndsText("Harbor Lights Ember Wars Book 3", ""), "no recorded series")
}

// SF-2: a series that ends a real title. The book being judged has a junk
// title, so no live book can hold its real title: "Second Quillmoor" in series
// "Quillmoor" is held by nobody, and a strip that trusted the live titles alone
// proposed "Second". A series that follows the title after a plain space is no
// longer cut, and the text is refused rather than proposed with the series in
// it; a series after an explicit separator is still cut unless the book's own
// other evidence (here the transcription) spells the whole of it.
func TestJunkTitleFixer_SeriesEndingTheTitleIsNotCutOff(t *testing.T) {
	l := newEvLib(t)
	zed := l.author("Zed Writer")
	quill, err := l.store.CreateSeries("Quillmoor", nil)
	require.NoError(t, err)
	two := func(dir string) []string { return []string{dir + "/01.mp3", dir + "/02.mp3"} }
	// The real title is "Second Quillmoor", the third book of the series
	// "Quillmoor": the folder says it with no separator.
	dir := "/lib/Zed Writer/Second Quillmoor Book 3"
	l.add(evBook{name: "bare", title: "read by narrator", author: zed, series: &quill.ID, seq: intp(3),
		path: dir, files: two(dir)})
	// The same, with a comma after the title's first word, so the strip is
	// reached: the transcription spells the whole title, which keeps the
	// series in it.
	dir2 := "/lib/Zed Writer/Second, Quillmoor Book 4"
	l.add(evBook{name: "separated-own-file", title: "read by narrator", author: zed, series: &quill.ID, seq: intp(4),
		path: dir2, files: two(dir2), transcribed: "Second Quillmoor"})
	// A genuine "<title>, <series>, Book N": cut, as before.
	dir3 := "/lib/Zed Writer/Tarn Hollow, Quillmoor, Book 5"
	l.add(evBook{name: "genuine-tail", title: "read by narrator", author: zed, series: &quill.ID, seq: intp(5),
		path: dir3, files: two(dir3)})
	rows := l.rows(l.fixer())

	r := rows["bare"]
	require.False(t, r.Applicable(), "proposed %q", r.Proposed["title"])
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, "ends with the book's own series")

	// The transcription spells "Second Quillmoor", so the text before "Book 4"
	// is a title and not a title plus the series: the folder is named whole in
	// the conflict, never cut down to "Second" (which would be the evidence
	// the transcription is then compared with).
	r = rows["separated-own-file"]
	require.Equal(t, junkSkipNeedsManual, r.Skipped, r.SkipReason)
	require.Contains(t, r.SkipReason, `folder "Second, Quillmoor Book 4"`)
	require.NotContains(t, r.SkipReason, `folder "Second"`)

	r = rows["genuine-tail"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Tarn Hollow", r.Proposed["title"])
}

// SF-4: a narrator is independent evidence only when the library knows the name
// apart from this book: it narrates another live book that has a real title, and
// it is not a live title itself. The narrator field is filled from the same file
// name and tags the stem came from (the AI filename parse, the artist tag), so a
// subtitle or a character's name in "Harbor Lights - Marlow Finch" can land in
// it, and the strip would then cut the subtitle off the title.
func TestJunkTitleFixer_NarratorIsIndependentOnlyWhenItNarratesElsewhere(t *testing.T) {
	l := newEvLib(t)
	narrated := func(name, narrator, title string) {
		l.add(evBook{name: name, title: title, narrator: narrator, author: l.author("Writer of " + name),
			path: "/lib/narrated/" + name + ".m4b", files: []string{"/lib/narrated/" + name + ".m4b"}})
	}
	junk := func(name, narrator, stem string) {
		l.add(evBook{name: name, title: "read by narrator", narrator: narrator, author: l.author("Solo Writer " + name),
			path: "/lib/" + name + "/" + stem + ".mp3", files: []string{"/lib/" + name + "/" + stem + ".mp3"}})
	}
	// 1: the narrator is a subtitle the AI parse labelled: no other book.
	junk("subtitle-narrator", "Marlow Finch", "Harbor Lights - Marlow Finch")
	// 2: the same name on another book with a real title: a narrator.
	narrated("elsewhere", "Wren Ostrander", "Tides Told Four Times")
	junk("real-narrator", "Wren Ostrander", "Harbor Lights - Wren Ostrander")
	// 3: narrates elsewhere, but the name is also a live book's title.
	narrated("titled-narrator-book", "Pell Garrow", "Tides Told Five Times")
	narrated("titled-narrator-title", "Someone Else", "Pell Garrow")
	junk("name-is-a-title", "Pell Garrow", "Cold Quay - Pell Garrow")
	// 4: the only other books under the name are junk-titled siblings of the
	// same work, whose narrator field came from the same file name.
	for _, n := range []string{"sibling-one", "sibling-two"} {
		junk(n, "Corin Vale", "Just a Moment - Corin Vale")
	}
	// 5: a junk narrator value never vouches, even when it is on another book.
	narrated("junk-narrator-book", "Unknown Author", "Tides Told Six Times")
	junk("junk-narrator", "Unknown Author", "Ash Meadow - Unknown Author")
	rows := l.rows(l.fixer())

	r := rows["real-narrator"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Harbor Lights", r.Proposed["title"])

	for _, name := range []string{"subtitle-narrator", "name-is-a-title", "sibling-one", "sibling-two"} {
		r := rows[name]
		require.False(t, r.Applicable(), "%s: proposed %q: the subtitle was cut off", name, r.Proposed["title"])
		require.Equal(t, junkSkipNeedsManual, r.Skipped, "%s: %s", name, r.SkipReason)
		require.Contains(t, r.SkipReason, "author credit", name)
	}
	// An "Unknown Author" segment is a placeholder: stripped as one, never as a
	// narrator, so the title is what is left either way.
	r = rows["junk-narrator"]
	require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
	require.Equal(t, "Ash Meadow", r.Proposed["title"])
}
