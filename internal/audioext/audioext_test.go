// file: internal/audioext/audioext_test.go
// version: 1.0.0
// guid: 2c5f1959-a0d9-45e7-8b2d-0420aff37b6e
// last-edited: 2026-09-01

package audioext

import (
	"slices"
	"testing"
)

// canonicalExtensions is the list every downstream predicate is now expected
// to agree on. Spelling it out here rather than reusing Default() is the
// point: if someone narrows defaultExtensions, this test fails instead of
// silently agreeing with the narrowing.
var canonicalExtensions = []string{
	".aac", ".aax", ".aaxc", ".aif", ".aiff", ".flac", ".m4a", ".m4b",
	".mka", ".mp3", ".oga", ".ogg", ".opus", ".wav", ".wma",
}

func TestDefaultIsTheCanonicalList(t *testing.T) {
	got := Default()
	slices.Sort(got)
	if !slices.Equal(got, canonicalExtensions) {
		t.Fatalf("Default() = %v\nwant %v", got, canonicalExtensions)
	}
}

// The library ships .aax and .aaxc. Every hardcoded list this package replaced
// omitted them, which is why a test whose fixture is only ".mp3" cannot see
// any of this.
func TestDefaultCarriesTheDRMAndLongTailExtensions(t *testing.T) {
	set := DefaultSet()
	for _, ext := range []string{".aax", ".aaxc", ".aiff", ".aif", ".mka", ".oga", ".wav", ".wma"} {
		if !set.Has(ext) {
			t.Errorf("DefaultSet() is missing %q", ext)
		}
	}
}

// .mp4 is deliberately excluded — see the package doc. This asserts the
// decision so that adding it becomes a conscious edit to a failing test
// rather than a one-word change nobody notices.
func TestDefaultExcludesMP4(t *testing.T) {
	if DefaultSet().Has(".mp4") {
		t.Fatal("Default() must not contain .mp4: it feeds the ingest scanner, " +
			"and .mp4 is overwhelmingly a video container")
	}
}

func TestDefaultReturnsACopy(t *testing.T) {
	first := Default()
	first[0] = ".corrupted"
	if second := Default(); second[0] == ".corrupted" {
		t.Fatal("Default() handed out the package's own backing array")
	}
}

// 🔴 The fail-open case. An empty or nil configured list must yield the full
// default set, never an empty one — an empty set makes every predicate answer
// "not audio" for every file, and the watcher/relink/provenance paths then do
// zero work while reporting success.
func TestResolveFailsOpenOnEmptyInput(t *testing.T) {
	cases := map[string][]string{
		"nil":               nil,
		"empty slice":       {},
		"only blanks":       {"", "   "},
		"blanks and commas": {"", "\t"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := Resolve(in)
			if len(got) != len(canonicalExtensions) {
				t.Fatalf("Resolve(%v) has %d extensions, want the %d-entry default set",
					in, len(got), len(canonicalExtensions))
			}
			if !got.Has(".aax") || !got.Has(".mp3") {
				t.Fatalf("Resolve(%v) = %v, want the compiled-in default", in, got.Sorted())
			}
		})
	}
}

func TestResolveUsesConfiguredListWhenNonEmpty(t *testing.T) {
	got := Resolve([]string{".mp3", ".m4b"})
	if len(got) != 2 {
		t.Fatalf("Resolve() = %v, want exactly the 2 configured extensions", got.Sorted())
	}
	if got.Has(".aax") {
		t.Fatal("Resolve() fell back to the default even though a list was configured")
	}
}

func TestNormalizeAddsDotAndLowercases(t *testing.T) {
	cases := map[string]string{
		"mp3":    ".mp3",
		".MP3":   ".mp3",
		" M4B ":  ".m4b",
		".aAxC":  ".aaxc",
		"":       "",
		"   ":    "",
		".flac":  ".flac",
		"AIFF":   ".aiff",
		"\t.oga": ".oga",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetMatchPathIsCaseInsensitive(t *testing.T) {
	s := DefaultSet()
	match := []string{
		"/library/Author/Book.aax",
		"/library/Author/Book.AAX",
		"/library/Author/Chapter 01.MP3",
		"relative/Book.aiff",
		"Book.mka",
	}
	for _, p := range match {
		if !s.MatchPath(p) {
			t.Errorf("MatchPath(%q) = false, want true", p)
		}
	}
	noMatch := []string{
		"",
		"/library/Author/Book",        // a directory
		"/library/Author/cover.jpg",   // not audio
		"/library/Author/trailer.mp4", // video: deliberately excluded
		"/library/Author.aax/Book",    // extension is on a parent, not the file
	}
	for _, p := range noMatch {
		if s.MatchPath(p) {
			t.Errorf("MatchPath(%q) = true, want false", p)
		}
	}
}

// A configured list written the way a human writes YAML — no dots, mixed case
// — must behave identically to the canonical spelling. Without normalization
// a user typing `supported_extensions: [mp3]` would get a set that matches
// nothing, which is the empty-set failure by another route.
func TestResolveNormalizesUserSpelling(t *testing.T) {
	got := Resolve([]string{"MP3", "m4b", ".AAX"})
	for _, p := range []string{"a.mp3", "b.M4B", "c.aax"} {
		if !got.MatchPath(p) {
			t.Errorf("MatchPath(%q) = false for a user-spelled config list", p)
		}
	}
}

func TestSortedIsStableAndComplete(t *testing.T) {
	got := DefaultSet().Sorted()
	if !slices.Equal(got, canonicalExtensions) {
		t.Fatalf("Sorted() = %v\nwant %v", got, canonicalExtensions)
	}
}

func TestNewSetDoesNotFailOpen(t *testing.T) {
	// NewSet is the raw constructor; only Resolve carries the fallback. Keeping
	// them distinct is what lets a caller ask "did the user configure nothing?"
	if len(NewSet(nil)) != 0 {
		t.Fatal("NewSet(nil) must be empty; the fallback belongs to Resolve alone")
	}
}
