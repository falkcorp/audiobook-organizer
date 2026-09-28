// file: internal/authorname/placeholder_title_test.go
// version: 1.0.0
// guid: 182e1203-ee23-40f0-bf66-e47134aeb23a
// last-edited: 2026-09-27

package authorname

import "testing"

func TestIsPlaceholderTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"":                   true,
		"  ":                 true,
		"Unknown Title":      true,
		"unknown title  ":    true,
		"unknown author":     true,
		"Read by Narrator":   true,
		"narrator":           true,
		"Unknown Narrator":   true,
		"The Hobbit":         false,
		"Unknown Soldier":    false,
		"Read by Narrator 2": false,
	} {
		if got := IsPlaceholderTitle(title); got != want {
			t.Errorf("IsPlaceholderTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

func TestIsPlaceholderAuthor(t *testing.T) {
	for name, want := range map[string]bool{
		"":               true,
		"Unknown Author": true,
		"unknown":        true,
		// Real production author rows (2026-09-27): a placeholder title parsed
		// out of a path segment and stored as an author.
		"read by narrator":        true,
		"Unknown Title":           true,
		"Terry Pratchett":         false,
		"Unknown Authority Press": false,
	} {
		if got := IsPlaceholderAuthor(name); got != want {
			t.Errorf("IsPlaceholderAuthor(%q) = %v, want %v", name, got, want)
		}
	}
}

// The organizer writes this literal into real directory names; pin it for the
// same reason TestPlaceholderLiteralIsPinned pins Placeholder.
func TestPlaceholderTitleLiteralIsPinned(t *testing.T) {
	if PlaceholderTitle != "Unknown Title" {
		t.Fatalf("PlaceholderTitle = %q: this names real production directories", PlaceholderTitle)
	}
}
