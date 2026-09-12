// file: internal/util/normalize_test.go
// version: 1.1.0
// guid: b4e8f3a2-0c5d-4f9b-a7e1-3d6c8b2e4f0a
// last-edited: 2026-09-12

package util_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/foo/Bar/BAZ.mp3", "/foo/bar/baz.mp3"},
		{"./Audio/Book/../file.MP3", "audio/file.mp3"},
		{"/Audiobooks/Author/Title/", "/audiobooks/author/title"},
		{"UPPER/lower/Mixed.go", "upper/lower/mixed.go"},
	}
	for _, c := range cases {
		if got := util.NormalizePath(c.in); got != c.want {
			t.Errorf("NormalizePath(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  The Hobbit  ", "the hobbit"},
		{"DUNE", "dune"},
		{"Foundation and Empire", "foundation and empire"},
		{"\tWhitespace\n", "whitespace"},
	}
	for _, c := range cases {
		if got := util.NormalizeTitle(c.in); got != c.want {
			t.Errorf("NormalizeTitle(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAuthor(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  J.R.R. Tolkien  ", "j.r.r. tolkien"},
		{"TOLKIEN", "tolkien"},
		{"Isaac Asimov", "isaac asimov"},
		{"\tFrank Herbert\n", "frank herbert"},
		// Internal whitespace runs collapse to one ASCII space (TASK-086).
		{"Raymond  L.  Weil", "raymond l. weil"},
		{"J.R.R.\tTolkien", "j.r.r. tolkien"},
		{"  Karen   Joy \t Fowler \n", "karen joy fowler"},
		{"Ursula\u00a0K.\u00a0Le Guin", "ursula k. le guin"}, // NBSP
		{"Ursula\u2003K. Le\u3000Guin", "ursula k. le guin"}, // em space, ideographic space
		// Case-only difference is unaffected.
		{"RAYMOND L. WEIL", "raymond l. weil"},
		// Blank input stays blank.
		{"", ""},
		{" \t\n ", ""},
	}
	for _, c := range cases {
		if got := util.NormalizeAuthor(c.in); got != c.want {
			t.Errorf("NormalizeAuthor(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeAuthorLegacy pins the pre-2026-09-12 key shape the store's
// read-compat fallback depends on: trim and lowercase, internal whitespace kept
// byte for byte. If this changes, index entries written under the old key
// become unreachable.
func TestNormalizeAuthorLegacy(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  J.R.R. Tolkien  ", "j.r.r. tolkien"},
		{"Raymond  L.  Weil", "raymond  l.  weil"},
		{"J.R.R.\tTolkien", "j.r.r.\ttolkien"},
		{"", ""},
	}
	for _, c := range cases {
		if got := util.NormalizeAuthorLegacy(c.in); got != c.want {
			t.Errorf("NormalizeAuthorLegacy(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeAuthorIsIdempotent: a key normalized twice is the same key, so a
// stored key re-normalized by any caller never drifts.
func TestNormalizeAuthorIsIdempotent(t *testing.T) {
	for _, in := range []string{"Raymond  L.  Weil", "\tA\u00a0 B ", "", "ÉMILE  ZOLA"} {
		once := util.NormalizeAuthor(in)
		if twice := util.NormalizeAuthor(once); twice != once {
			t.Errorf("NormalizeAuthor not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestNormalizeString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  Hello World  ", "hello world"},
		{"SCIENCE-FICTION", "science-fiction"},
		{"Mystery", "mystery"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := util.NormalizeString(c.in); got != c.want {
			t.Errorf("NormalizeString(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestCollapseSpaces(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  hello   world  ", "hello world"},
		{"no  extra\t\tspaces", "no extra spaces"},
		{"single", "single"},
		{"   ", ""},
		{"a\nb\tc", "a b c"},
	}
	for _, c := range cases {
		if got := util.CollapseSpaces(c.in); got != c.want {
			t.Errorf("CollapseSpaces(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
