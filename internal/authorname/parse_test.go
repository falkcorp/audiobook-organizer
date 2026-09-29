// file: internal/authorname/parse_test.go
// version: 1.5.0
// guid: 3b8e5f27-14a9-4c03-9d6b-8e21f70a4c95
// last-edited: 2026-09-28

package authorname

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// TestExtractAuthorFromDirectoryCorpus is the differential corpus that measured
// the collapse of internal/scanner's and internal/metadata's copies, preserved
// as a table so the measurement stays runnable instead of living in a PR body.
//
// Every row was executed against BOTH original copies before the move. They
// disagreed on exactly one: "Unknown Author", where metadata returned the
// placeholder as a real author and scanner returned "". The unified function
// takes scanner's answer, and the metadata consumer cleared that value anyway --
// pinned separately by
// internal/metadata.TestExtractMetadataNeverReturnsThePlaceholderAsArtist.
func TestExtractAuthorFromDirectoryCorpus(t *testing.T) {
	cases := []struct {
		dir  string
		want string
		why  string
	}{
		// Container directories. All single words, so the shape gate would
		// refuse them even with no skipDirs map at all -- see
		// TestSkipDirsIsRedundantExceptForThePlaceholder.
		{"import", "", "container dir"},
		{"imports", "", "container dir"},
		{"organized", "", "container dir"},
		{"Import", "", "container dir, case-insensitive"},
		{"ORGANIZED", "", "container dir, case-insensitive"},
		{"books", "", "container dir"},
		{"audiobooks", "", "container dir"},
		{"bt", "", "container dir"},

		// The placeholder: the only skipDirs entry that changes THIS FUNCTION'S
		// return value. It does not follow that it changes a consumer's outcome --
		// it does not; see parse.go. These rows are the only instrument that sees it.
		{Placeholder, "", "the organizer's own placeholder is not an author"},
		{"unknown author", "", "placeholder, case-insensitive"},
		{Placeholder + " (Unabridged)", "", "decorated placeholder; refused by the trailing-paren rule"},

		// Real authors.
		{"Terry Pratchett", "Terry Pratchett", "bare person name"},
		{"J. R. R. Tolkien", "J. R. R. Tolkien", "four words with initials"},
		{"Terry Pratchett - Mort", "Terry Pratchett", "Author - Title"},

		// Credit patterns; the first branch tried, and shape-gated.
		{"Terry Pratchett - translator - Mort", "Terry Pratchett", "translator credit, real name"},
		{"Stephen Fry - narrated by - Mort", "Stephen Fry", "narrator credit, real name"},
		{"Discworld - translator - Mort", "", "series name in the credit slot is refused"},
		{"Unabridged - narrated by - Stephen Fry", "", "edition word in the credit slot is refused"},

		// Work-named directories are person-SHAPED, so the shape gate alone
		// took them as authors. A series or title folder names no author.
		{"The Stormlight Archive", "", "series folder, article-led"},
		{"The Hobbit", "", "title folder, article-led"},
		{"Dune Chronicles", "", "series folder, series word"},
		{"The Stormlight Archive - The Way of Kings", "", "Series - Title folder"},
		{"A J Finn", "A J Finn", "initials, not an article"},

		// Junk that reaches the "Author - Title" branch. These are the strings
		// that make the shape gate on that branch necessary.
		{"Discworld - Mort", "", "series - title, not author - title"},

		// Refused by shape.
		{"Tolkien", "", "single word; the documented cost of the 2-4 word rule"},
		{"Pratchett 036", "", "second word starts with a digit"},
		{"Do Androids Dream?", "", "sentence punctuation belongs to titles"},
		{"van Gogh Vincent", "", "leading lowercase particle is not a name start"},
		{"import - Mort", "", "container word in the author slot"},
		{"organized - Volume One", "", "container word in the author slot"},
	}

	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			path := filepath.Join("/lib", tc.dir, "01.mp3")
			if got := ExtractAuthorFromDirectory(path); got != tc.want {
				t.Errorf("ExtractAuthorFromDirectory(%q) = %q, want %q (%s)",
					path, got, tc.want, tc.why)
			}
		})
	}
}

// TestExtractAuthorFromDirectoryPathEdges covers the inputs that discriminated
// the two copies' differing path idioms.
//
// scanner split on os.PathSeparator and indexed the last element; metadata used
// filepath.Base(filepath.Dir(...)), which is what survived. The two agreed on
// every one of these, which is why the swap was safe -- but "they agreed" is
// only worth anything if the cases that COULD have disagreed were actually run.
func TestExtractAuthorFromDirectoryPathEdges(t *testing.T) {
	for _, path := range []string{
		"01.mp3",       // no directory at all; Dir -> "."
		"/01.mp3",      // root; Dir -> "/", where the split idiom yields ""
		"./01.mp3",     // Dir -> "."
		"/lib//01.mp3", // doubled separator, cleaned by Dir
	} {
		if got := ExtractAuthorFromDirectory(path); got != "" {
			t.Errorf("ExtractAuthorFromDirectory(%q) = %q, want \"\"", path, got)
		}
	}
}

// TestSkipDirsIsRedundantExceptForThePlaceholder pins the finding that made this
// collapse a one-row change rather than a four-entry behaviour fix.
//
// The map LOOKS load-bearing. It is not: LooksLikePersonName requires 2-4 words,
// so every single-word entry is refused by the shape gate whether or not the map
// catches it first. "Unknown Author" is the only multi-word entry, hence the only
// one that can change what this function RETURNS.
//
// Even that entry changes no consumer's outcome -- both internal/metadata and
// internal/scanner clear the placeholder again downstream, so deleting it leaves
// both suites green and only this file catches it. That is why this test exists
// here and not there.
//
// This is written as an assertion about the ENTRIES, so it fails if someone adds
// a multi-word entry without noticing they have just made the map load-bearing in
// a second place -- and it fails if the placeholder entry is deleted as "dead
// code like the rest", which is the specific mistake this whole test exists to
// prevent.
func TestSkipDirsIsRedundantExceptForThePlaceholder(t *testing.T) {
	// The map's KEYS are lowercased, because the lookup lowercases the directory
	// name. Asking LooksLikePersonName about a key directly therefore answers
	// the wrong question -- it refuses "unknown author" for starting lowercase,
	// not for its shape, and every entry then looks redundant.
	//
	// The real question is whether ANY capitalisation of the key can reach the
	// shape gate, which is a word-count question. So title-case first.
	titleCase := func(s string) string {
		fields := strings.Fields(s)
		for i, f := range fields {
			fields[i] = strings.ToUpper(f[:1]) + f[1:]
		}
		return strings.Join(fields, " ")
	}

	for dir := range skipDirs {
		shapePasses := personname.LooksLikePersonName(titleCase(dir))
		isPlaceholder := IsPlaceholder(dir)

		switch {
		case isPlaceholder && !shapePasses:
			t.Errorf("skipDirs[%q]: the placeholder no longer passes the shape gate, so this "+
				"entry has become redundant and the guard it provides has silently moved elsewhere", dir)
		case !isPlaceholder && shapePasses:
			t.Errorf("skipDirs[%q]: a NEW load-bearing entry. Capitalised it passes "+
				"LooksLikePersonName, so unlike the other container words it is the map -- not the "+
				"shape gate -- refusing it. That is fine, but it is no longer true that the "+
				"placeholder is the only live entry; update the comments in parse.go that say so", dir)
		}
	}

	// The known-good control: without it, the loop above passes just as happily
	// over an empty map.
	if !skipDirs[strings.ToLower(Placeholder)] {
		t.Fatalf("skipDirs lost its placeholder entry; %q would be returned as a real author", Placeholder)
	}
}

func TestParseDashFilenameAuthor(t *testing.T) {
	cases := []struct {
		filename   string
		wantTitle  string
		wantAuthor string
	}{
		{"The Stand - Stephen King", "The Stand", "Stephen King"},
		{"No Author Here", "", ""},
		{"", "", ""},

		// THE THREE-PART GUARD. `len(parts) != 2` was untested in all three
		// packages: mutating it to `< 2` left every suite green.
		//
		// "a - b - c" cannot observe it -- neither side is a person name, so
		// ChooseAuthorSide refuses and the mutant returns ("","") too. The row
		// has to have a real name and a real title in the first two segments,
		// or it pins nothing. Under the mutant this one yields
		// author="Norse Mythology": a TITLE filed as the author, which is the
		// wrong-author-beats-absent-author failure this file calls structural.
		{"Neil Gaiman - Norse Mythology - 01", "", ""},
		{"Stephen King - The Stand - Part 1", "", ""},
		{"a - b - c", "", ""},

		// THE TIE. personname calls the tie policy "the ONE place the four call
		// sites legitimately differ", and this function is what passes it --
		// yet flipping PreferRightOnTie was caught only by pre-existing tests in
		// internal/metadata, reached through the shim alias. A load-bearing
		// argument with no coverage where the argument lives.
		//
		// Both sides are person-shaped, so the policy alone decides: right wins.
		{"Stephen King - John Doe", "Stephen King", "John Doe"},

		// A WORK-NAMED side is never the author (personname.LooksLikeWorkTitle).
		// ("", "") is the signal both callers read as "series X, title Y";
		// that end of it is pinned in internal/metadata and internal/scanner.
		//
		// The reported bug: the left side is person-SHAPED (three capitalised
		// words), the right is not ("of"), so the series was filed as the author.
		{"The Stormlight Archive - The Way of Kings", "", ""},
		// Both sides article-led and both person-shaped: the old dash tie
		// resolved "prefer right" and filed "The Gunslinger" as the author.
		{"The Dark Tower - The Gunslinger", "", ""},
		{"A Song of Ice and Fire - A Game of Thrones", "", ""},
		{"Wheel of Time 01 - The Eye of the World", "", ""},
		{"Discworld 01 - The Colour of Magic", "", ""},
		// Series word without an article, against a person-shaped title.
		{"Dune Chronicles - Children of Dune", "", ""},

		// Real authors are unaffected, in either order and with initials.
		{"Brandon Sanderson - The Way of Kings", "The Way of Kings", "Brandon Sanderson"},
		{"The Way of Kings - Brandon Sanderson", "The Way of Kings", "Brandon Sanderson"},
		{"Sanderson, Brandon - Mistborn", "Mistborn", "Sanderson, Brandon"},
		{"J.R.R. Tolkien - The Hobbit", "The Hobbit", "J.R.R. Tolkien"},
		// "A" before a single letter is initials, not an article.
		{"A J Finn - The Woman in the Window", "The Woman in the Window", "A J Finn"},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			// Rows with no author assert only that none was found; the
			// title/series shape of those is pinned by TestParseDashFilename.
			p := ParseDashFilename(tc.filename, "")
			title, author := p.Title, p.Author
			if author == "" {
				title = ""
			}
			if title != tc.wantTitle || author != tc.wantAuthor {
				t.Errorf("ParseDashFilename(%q) = (%q, %q), want (%q, %q)",
					tc.filename, title, author, tc.wantTitle, tc.wantAuthor)
			}
		})
	}
}

// TestParseDashFilename pins the three answer shapes and, above all, that a
// side refused as an author but still credit-shaped is filed NOWHERE -- not as
// the series and not as the title.
func TestParseDashFilename(t *testing.T) {
	cases := []struct {
		in                    string
		author, title, series string
	}{
		{"The Stormlight Archive - The Way of Kings", "", "The Way of Kings", "The Stormlight Archive"},
		{"A Song of Ice and Fire - A Game of Thrones", "", "A Game of Thrones", "A Song of Ice and Fire"},
		{"Wheel of Time 01 - The Eye of the World", "", "The Eye of the World", "Wheel of Time 01"},
		{"Discworld 01 - The Colour of Magic", "", "The Colour of Magic", "Discworld 01"},
		{"The Expanse 01 - Leviathan Wakes", "", "Leviathan Wakes", "The Expanse 01"},
		{"Brandon Sanderson - The Way of Kings", "Brandon Sanderson", "The Way of Kings", ""},
		{"Mistborn Book 1 - Brandon Sanderson", "Brandon Sanderson", "Mistborn Book 1", ""},
		{"An Na - A Step from Heaven", "", "A Step from Heaven", ""},
		{"A Step from Heaven - An Na", "", "A Step from Heaven", ""},
		{"The Arbinger Institute - Leadership and Self-Deception", "", "Leadership and Self-Deception", ""},
		{"The Dark Tower - The Gunslinger", "", "The Dark Tower - The Gunslinger", ""},
		{"Memories of Silk and Straw - Junichi Saga", "Junichi Saga", "Memories of Silk and Straw", ""},
		{"Junichi Saga - Memories of Silk and Straw", "Junichi Saga", "Memories of Silk and Straw", ""},
		// KNOWN LIMIT: the same shape as "The Stand - Stephen King".
		{"The Hunger Games - Catching Fire", "Catching Fire", "The Hunger Games", ""},
		// Chapter positions are the caller's check (SeriesFromTitlePrefix).
		{"Eldest - 02", "", "02", "Eldest"},
		// A series-marked left side against a non-credit right: series+title.
		{"Mistborn Book 1 - The Final Empire", "", "The Final Empire", "Mistborn Book 1"},
		// A position marker on the right is not the title.
		{"The Hobbit - Chapter 01", "", "The Hobbit", ""},
		{"The Hobbit - Part 1", "", "The Hobbit", ""},
		{"The Hobbit - 01", "", "The Hobbit", ""},
		// Titles carrying a series word are not series.
		{"The Book Thief - Markus Zusak", "Markus Zusak", "The Book Thief", ""},
		// A strong name on the right survives a padded number on the left...
		{"The Expanse 01 - James S. A. Corey", "James S. A. Corey", "The Expanse 01", ""},
		{"The Dark Tower 01 - King, Stephen", "King, Stephen", "The Dark Tower 01", ""},
		// ...a plain two-word one does not (documented cost).
		{"The Dark Tower 01 - Stephen King", "", "Stephen King", "The Dark Tower 01"},
		// Round-4 review rows: word count and a bare "." are not strong
		// evidence; a function word inside vetoes; an edition suffix,
		// initials and a list of names are.
		{"Stormlight 02 - Words Of Radiance", "", "Words Of Radiance", "Stormlight 02"},
		{"Dune 03 - Children Of Dune", "", "Children Of Dune", "Dune 03"},
		{"Bill Hodges 01 - Mr. Mercedes", "", "Mr. Mercedes", "Bill Hodges 01"},
		{"Stormlight 01 - Brandon Sanderson Jr", "", "Brandon Sanderson Jr", "Stormlight 01"},
		{"Mistborn 01 - Brandon Sanderson (Unabridged)", "Brandon Sanderson (Unabridged)", "Mistborn 01", ""},
		{"Discworld 01 - J. R. R. Tolkien", "J. R. R. Tolkien", "Discworld 01", ""},
		{"Good Omens 01 - Neil Gaiman & Terry Pratchett", "Neil Gaiman & Terry Pratchett", "Good Omens 01", ""},
	}
	for _, tc := range cases {
		got := ParseDashFilename(tc.in, "")
		if !got.Parsed || got.Author != tc.author || got.Title != tc.title || got.Series != tc.series {
			t.Errorf("ParseDashFilename(%q) = %+v, want author %q title %q series %q",
				tc.in, got, tc.author, tc.title, tc.series)
		}
	}
	// The organizer's own "<author>/<title>/<title> - <author>" layout: the
	// folders vouch for the right side despite the padded number.
	for _, path := range []string{
		"/lib/Terry Pratchett/Discworld 01/Discworld 01 - Terry Pratchett.mp3",
		"/mnt/bigdata/books/audiobook-organizer/Terry Pratchett/Pratchett 036/Pratchett 036 - Terry Pratchett.mp3",
	} {
		base := strings.TrimSuffix(filepath.Base(path), ".mp3")
		left := strings.SplitN(base, " - ", 2)[0]
		org := ParseDashFilename(base, path)
		if org.Author != "Terry Pratchett" || org.Title != left || org.Series != "" {
			t.Errorf("organizer layout %s: %+v, want author Terry Pratchett, title %q", path, org, left)
		}
	}
	// "<author>/<series>/<title>/<series NN> - <title>" is NOT that layout,
	// even though the right side names a folder above: the immediate parent
	// is the title, not the left side.
	for _, path := range []string{
		"/lib/James S. A. Corey/The Expanse/Leviathan Wakes/The Expanse 01 - Leviathan Wakes.mp3",
		"/lib/Leviathan Wakes/The Expanse 01 - Leviathan Wakes.mp3",
	} {
		got := ParseDashFilename("The Expanse 01 - Leviathan Wakes", path)
		if got.Author != "" || got.Series != "The Expanse 01" || got.Title != "Leviathan Wakes" {
			t.Errorf("series/title layout %s: %+v, want series The Expanse 01, title Leviathan Wakes", path, got)
		}
	}
	if got := ParseDashFilename("Neil Gaiman - Norse Mythology - 01", ""); got.Parsed {
		t.Errorf("three-part name parsed: %+v", got)
	}
}

func TestSameCredit(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"Andy Weir", "andy weir", true},
		{"Andy Weir (Unabridged)", "Andy Weir", true},
		{"King, Stephen", "Stephen King", true},
		{"J.R.R. Tolkien", "J R R Tolkien", true},
		{"Stephen King", "Suzanne Collins", false},
		{"", "", false},
		// Round-4: lists compare as name sets; the comma swap is only for
		// "Surname, Given".
		{"Neil Gaiman, Terry Pratchett", "Neil Gaiman & Terry Pratchett", true},
		{"Terry Pratchett and Neil Gaiman", "Neil Gaiman & Terry Pratchett", true},
		{"Neil Gaiman, Terry Pratchett", "Terry Pratchett Neil Gaiman", false},
		{"Tolkien, J.R.R.", "J.R.R. Tolkien", true},
		{"Le Guin, Ursula K.", "Ursula K. Le Guin", true},
		{"Douglas Preston & Lincoln Child", "Lincoln Child", false},
	} {
		if got := SameCredit(tc.a, tc.b); got != tc.want {
			t.Errorf("SameCredit(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLooksLikeStrongName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"J. R. R. Tolkien", true},
		{"James S. A. Corey", true},
		{"King, Stephen", true},
		{"Tolkien, J.R.R.", true},
		{"Neil Gaiman & Terry Pratchett", true},
		{"Neil Gaiman, Terry Pratchett", true},
		{"Brandon Sanderson (Unabridged)", true},
		{"Stephen King", false},
		{"Brandon Sanderson Jr", false},
		{"Words Of Radiance", false},
		{"Children Of Dune", false},
		{"Mr. Mercedes", false},
		{"Oathbringer Part One", false},
		{"Preston & Child", false},
	} {
		if got := looksLikeStrongName(tc.in); got != tc.want {
			t.Errorf("looksLikeStrongName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestExtractAuthorAboveTitle(t *testing.T) {
	for _, tc := range []struct {
		probe, title, want string
	}{
		// The probe is the title folder; its parent is the folder read.
		{"/lib/Neil Gaiman/Good Omens", "Good Omens", "Neil Gaiman"},
		{"/lib/Harry Potter/Harry Potter and the Goblet of Fire", "Harry Potter and the Goblet of Fire", ""},
		{"/lib/Science Fiction/Good Omens", "Good Omens", ""},
		{"/lib/Audiobooks/Good Omens", "Good Omens", ""},
		// Prefix means a whole-word prefix, not a shared first letters.
		{"/lib/Stephen King/Stephen Kingdom", "Stephen Kingdom", "Stephen King"},
		// LIMIT: not prefixing its titles, a series folder is person-shaped.
		{"/lib/Jack Reacher/Killing Floor", "Killing Floor", "Jack Reacher"},
		// title "" is the ordinary fallback.
		{"/lib/Harry Potter/x.mp3", "", "Harry Potter"},
	} {
		if got := ExtractAuthorAboveTitle(tc.probe, tc.title); got != tc.want {
			t.Errorf("ExtractAuthorAboveTitle(%q, %q) = %q, want %q", tc.probe, tc.title, got, tc.want)
		}
	}
}
