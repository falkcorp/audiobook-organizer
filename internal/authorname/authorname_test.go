// file: internal/authorname/authorname_test.go
// version: 1.0.0
// guid: b8f31d0a-6c47-4e29-95a1-7d3e0b62c4f8
// last-edited: 2026-08-25

package authorname

import "testing"

func TestIsPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"exact", "Unknown Author", true},
		// The value has usually round-tripped through a file path by the time
		// it is tested, so neither case nor padding is guaranteed.
		{"lowercased by a path round trip", "unknown author", true},
		{"padded", "  Unknown Author  ", true},
		{"a real author", "Terry Pratchett", false},
		{"empty", "", false},
		// Must not swallow a real author who merely contains the word.
		{"substring is not a match", "Unknown Authority Press", false},
		{"unknown alone", "Unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPlaceholder(tc.in); got != tc.want {
				t.Errorf("IsPlaceholder(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The whole point of the package is that three other packages stop carrying
// their own copy of this literal, so the literal itself is worth pinning: a
// change here silently changes which on-disk directory the organizer writes and
// which prefix the purge job sweeps.
func TestPlaceholderLiteralIsPinned(t *testing.T) {
	if Placeholder != "Unknown Author" {
		t.Fatalf("Placeholder = %q: this names a real production directory "+
			"(.../audiobook-organizer/Unknown Author/) and a maintenance job's "+
			"sweep prefix; changing it strands both", Placeholder)
	}
}
