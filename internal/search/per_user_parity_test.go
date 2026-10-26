// file: internal/search/per_user_parity_test.go
// version: 1.0.0
// guid: 9e4c1a7b-0d35-4f82-a6b9-5c3e8f1d7a20
// last-edited: 2026-10-26

package search_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// Two packages each keep a list of the per-user fields: the translator routes
// them to the Go-side pass (search), and the library list filter splits its field
// filters on them (audiobooks.PerUserFieldNames). A field in one list only is a
// query that is evaluated as one thing in one place and another elsewhere, and for
// a collection it is the difference between a per-viewer answer and one user's
// answer served to everyone. The copies cannot share a variable (audiobooks imports
// search, not the reverse), so a test holds them equal. It lives here, in the
// external test package, because only an external package can import both.
func TestPerUserFieldNamesAgree(t *testing.T) {
	want := search.PerUserFieldNamesForTest()
	got := slices.Sorted(maps.Keys(audiobooks.PerUserFieldNames))
	if !slices.Equal(want, got) {
		t.Fatalf("search routes %v as per-user but audiobooks.PerUserFieldNames is %v; keep the two lists equal", want, got)
	}
	for _, name := range want {
		if !search.IsPerUserField(name) || !audiobooks.IsPerUserField(name) {
			t.Errorf("%q is in a list but not recognised by both IsPerUserField helpers", name)
		}
	}
}
