// file: internal/itunes/service/hash_link_target_test.go
// version: 1.0.0
// guid: 8c4e1a72-5d93-4b06-a2f7-0e9b3d6c1f48
// last-edited: 2026-10-22

package itunesservice

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func hashHolder(id string, trashed bool) *database.Book {
	b := &database.Book{ID: id, Title: id}
	if trashed {
		b.MarkedForDeletion = new(true)
	}
	return b
}

// The hash link puts the new book into the target's version group as a
// non-primary member, so the target must be a live book other than itself.
func TestHashLinkTarget(t *testing.T) {
	cases := []struct {
		name    string
		holders []*database.Book
		self    string
		want    string
	}{
		{"no holders", nil, "new", ""},
		{"only itself", []*database.Book{hashHolder("new", false)}, "new", ""},
		// A re-run over a book that already holds the hash and is the oldest
		// holder: the single-answer lookup named it, hiding the other holder.
		{"oldest holder finds the newer one", []*database.Book{hashHolder("a-new", false), hashHolder("b-live", false)}, "a-new", "b-live"},
		{"first live holder wins", []*database.Book{hashHolder("a-live", false), hashHolder("b-live", false)}, "new", "a-live"},
		// A trashed holder's group may have been emptied by a merge, and a group
		// redirect is followed only by a restore: linking into it would leave the
		// new book a non-primary member of a group with no live primary.
		{"trashed holder is never a target", []*database.Book{hashHolder("a-trashed", true)}, "new", ""},
		{"trashed holder ahead of a live one is skipped", []*database.Book{hashHolder("a-trashed", true), hashHolder("b-live", false)}, "new", "b-live"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hashLinkTarget(tc.holders, tc.self)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("hashLinkTarget = %s, want none", got.ID)
			case tc.want != "" && (got == nil || got.ID != tc.want):
				t.Fatalf("hashLinkTarget = %v, want %s", got, tc.want)
			}
		})
	}
}
