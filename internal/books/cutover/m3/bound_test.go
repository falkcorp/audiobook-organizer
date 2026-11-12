// file: internal/books/cutover/m3/bound_test.go
// version: 1.0.0
// guid: 8b627007-079f-42c9-9a1c-1f9ae44985ae
// last-edited: 2026-11-12

package m3

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/auth"
)

// TestClip: a value is cut to at most n bytes at a rune boundary, never
// through a multi-byte rune.
func TestClip(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than n", "alfred", 32, "alfred"},
		{"exactly n", strings.Repeat("a", 32), 32, strings.Repeat("a", 32)},
		{"ASCII over n", strings.Repeat("a", 40), 32, strings.Repeat("a", 32)},
		// "a" then 20 two-byte runes: byte 32 is the middle of the 16th rune, so
		// the cut falls back to byte 31.
		{"a rune straddles n", "a" + strings.Repeat("é", 20), 32, "a" + strings.Repeat("é", 15)},
		{"a rune ends exactly at n", strings.Repeat("é", 20), 32, strings.Repeat("é", 16)},
		{"a four-byte rune straddles n", "ab" + strings.Repeat("\U0001F987", 10), 32, "ab" + strings.Repeat("\U0001F987", 7)},
		{"empty", "", 32, ""},
	} {
		got := clip(c.in, c.n)
		if got != c.want || !utf8.ValidString(got) || len(got) > c.n {
			t.Errorf("%s: clip = %q (%d bytes, valid %v), want %q", c.name, got, len(got), utf8.ValidString(got), c.want)
		}
	}
}

// TestBoundedAtoms: a log field lists at most maxLoggedAtoms names, each cut
// to maxAtomLen bytes, and counts what it left out.
func TestBoundedAtoms(t *testing.T) {
	var many []string
	for i := range 20 {
		many = append(many, fmt.Sprintf("atom-%02d", i))
	}
	got := boundedAtoms(many)
	if len(got) != maxLoggedAtoms+1 || got[0] != "atom-00" || got[maxLoggedAtoms-1] != "atom-07" || got[maxLoggedAtoms] != "(+12 more)" {
		t.Errorf("boundedAtoms(20 names) = %q", got)
	}
	if got := boundedAtoms(many[:maxLoggedAtoms]); len(got) != maxLoggedAtoms || got[len(got)-1] != "atom-07" {
		t.Errorf("exactly maxLoggedAtoms names must not be truncated: %q", got)
	}
	if got := boundedAtoms(nil); len(got) != 0 {
		t.Errorf("boundedAtoms(nil) = %q", got)
	}
	long := strings.Repeat("é", 200)
	got = boundedAtoms([]string{long})
	if len(got) != 1 || len(got[0]) > maxAtomLen || !utf8.ValidString(got[0]) {
		t.Errorf("a long atom name is %d bytes (valid %v), want at most %d", len(got[0]), utf8.ValidString(got[0]), maxAtomLen)
	}
}

// TestStatusIsCutAtARuneBoundary: the status a user or key row carries is
// named in the log, so a status of any length and any script is bounded and
// stays valid UTF-8.
func TestStatusIsCutAtARuneBoundary(t *testing.T) {
	raw := "a" + strings.Repeat("É", 30) // lower-cased to é: 61 bytes
	_, reported := userStatus(raw)
	if !utf8.ValidString(reported) || len(reported) > maxStatusLen || len(reported) < maxStatusLen-1 {
		t.Errorf("userStatus reported %q (%d bytes)", reported, len(reported))
	}
	_, odd := keyStatus(raw, nil)
	if !utf8.ValidString(odd) || len(odd) > maxStatusLen || len(odd) < maxStatusLen-1 {
		t.Errorf("keyStatus reported %q (%d bytes)", odd, len(odd))
	}
	if st, reported := userStatus("inactive"); st != auth.StatusDisabled || reported != "" {
		t.Errorf("userStatus(inactive) = %v %q", st, reported)
	}
}
