// file: internal/server/entities_ops_credit_test.go
// version: 1.0.0
// guid: 388ccf1f-0f73-4767-bba0-23876b7a593b
// last-edited: 2026-10-04

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The resolved author takes the production company's slot; it used to be
// appended at position 0, colliding with whatever already held it.
func TestReplaceAuthorCredit(t *testing.T) {
	ba := func(id int, role string, pos int) database.BookAuthor {
		return database.BookAuthor{BookID: "b", AuthorID: id, Role: role, Position: pos}
	}
	require.Equal(t, []database.BookAuthor{ba(1, "author", 0), ba(9, "co-author", 1)},
		replaceAuthorCredit("b", []database.BookAuthor{ba(1, "author", 0), ba(5, "co-author", 1)}, 5, 9))
	require.Equal(t, []database.BookAuthor{ba(9, "author", 0), ba(1, "author", 1)},
		replaceAuthorCredit("b", []database.BookAuthor{ba(5, "", 0), ba(1, "author", 1)}, 5, 9))
	// Already credited: the company credit is dropped, nothing added.
	require.Equal(t, []database.BookAuthor{ba(9, "author", 1)},
		replaceAuthorCredit("b", []database.BookAuthor{ba(5, "author", 0), ba(9, "author", 1)}, 5, 9))
	// No company credit: appended after the last position.
	require.Equal(t, []database.BookAuthor{ba(1, "author", 0), ba(9, "author", 1)},
		replaceAuthorCredit("b", []database.BookAuthor{ba(1, "author", 0)}, 5, 9))
}
