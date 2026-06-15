// file: internal/plugins/dedup/reembed_embeddings_test.go
// version: 1.0.0
// guid: 3a2b1c0d-9e8f-7a6b-5c4d-3e2f1a0b9c8d
// last-edited: 2026-06-14

// Unit tests for the reembed-embeddings op's scan-partition helper.

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestEmbeddableForReembed(t *testing.T) {
	primaryTrue := true
	primaryFalse := false

	cases := []struct {
		name string
		book database.Book
		want bool
	}{
		{"primary nil + good title", database.Book{Title: "A Real Book"}, true},
		{"primary true + good title", database.Book{Title: "Another Book", IsPrimaryVersion: &primaryTrue}, true},
		{"non-primary excluded", database.Book{Title: "A Real Book", IsPrimaryVersion: &primaryFalse}, false},
		{"empty title excluded", database.Book{Title: ""}, false},
		{"whitespace title excluded", database.Book{Title: "   "}, false},
		{"2-rune title excluded (matches hasUsableTitle >2)", database.Book{Title: "ab"}, false},
		{"3-rune title included", database.Book{Title: "abc"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.book
			if got := embeddableForReembed(&b); got != tc.want {
				t.Errorf("embeddableForReembed(%+v) = %v, want %v", tc.book, got, tc.want)
			}
		})
	}
}
