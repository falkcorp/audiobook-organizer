// file: internal/database/book_field_render_tracked_test.go
// version: 1.0.0
// guid: 5c0e9a37-8d42-4b16-a7f3-2e6b1d9c4a85
// last-edited: 2026-09-27

package database

import "testing"

// TrackedBookFields is the list repairs.Writer diffs to record history, so
// every name must round-trip through RenderBookField and the store-owned
// fields must stay out.
func TestTrackedBookFields_RenderableAndExcludesStoreOwned(t *testing.T) {
	b := &Book{ID: "x", Title: "T"}
	seen := map[string]bool{}
	for _, name := range TrackedBookFields() {
		if seen[name] {
			t.Fatalf("duplicate field %q", name)
		}
		seen[name] = true
		if _, err := RenderBookField(b, name); err != nil {
			t.Fatalf("RenderBookField(%q): %v", name, err)
		}
	}
	for _, want := range []string{"title", "is_primary_version", "merged_into_book_id", "narrator"} {
		if !seen[want] {
			t.Errorf("%q missing from TrackedBookFields", want)
		}
	}
	for _, never := range []string{"id", "created_at", "updated_at", "-"} {
		if seen[never] {
			t.Errorf("%q must not be tracked", never)
		}
	}
}
