// file: internal/authorname/parse_test.go
// version: 1.1.0
// guid: 3b8e5f27-14a9-4c03-9d6b-8e21f70a4c95
// last-edited: 2026-09-01

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

func TestParseFilenameForAuthor(t *testing.T) {
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
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			title, author := ParseFilenameForAuthor(tc.filename)
			if title != tc.wantTitle || author != tc.wantAuthor {
				t.Errorf("ParseFilenameForAuthor(%q) = (%q, %q), want (%q, %q)",
					tc.filename, title, author, tc.wantTitle, tc.wantAuthor)
			}
		})
	}
}
