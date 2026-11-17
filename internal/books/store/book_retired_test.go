// file: internal/books/store/book_retired_test.go
// version: 1.0.1
// guid: 7d3b9e41-8a52-4c06-b1e7-0f4a6c2d9e38
// last-edited: 2026-11-17

package store

import "testing"

// Book.IsRetired is the one definition of "the library has put this book
// away": in the trash, or absorbed into another book. One field per row, so a
// check that reads only the other field fails.
func TestBookIsRetired(t *testing.T) {
	yes, no := true, false
	into, empty := "survivor-book-id", ""
	cases := []struct {
		name string
		book *Book
		want bool
	}{
		{"nil book", nil, false},
		{"live", &Book{ID: "a"}, false},
		{"soft deleted", &Book{ID: "a", MarkedForDeletion: &yes}, true},
		{"deletion flag false", &Book{ID: "a", MarkedForDeletion: &no}, false},
		{"merged into another", &Book{ID: "a", MergedIntoBookID: &into}, true},
		{"merge cleared to empty (an un-merge)", &Book{ID: "a", MergedIntoBookID: &empty}, false},
		{"both", &Book{ID: "a", MarkedForDeletion: &yes, MergedIntoBookID: &into}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.book.IsRetired(); got != tc.want {
				t.Fatalf("IsRetired() = %v, want %v", got, tc.want)
			}
			// The BookCore twin answers the same for the same two fields.
			var core *BookCore
			if tc.book != nil {
				c := tc.book.Core()
				core = &c
			}
			if got := core.IsRetired(); got != tc.want {
				t.Fatalf("BookCore.IsRetired() = %v, want %v", got, tc.want)
			}
		})
	}
}
