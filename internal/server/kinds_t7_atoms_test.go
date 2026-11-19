// file: internal/server/kinds_t7_atoms_test.go
// version: 1.0.0
// guid: 00005d1a-c664-43f7-80ab-012e2890c554
// last-edited: 2026-11-18

package server_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// internal/books imports this package, so the native kinds cannot read
// books.AtomTable and keep a copy of the rows they need (t7AtomScopes). This test
// is the drift guard for that copy: every atom it holds must be a row of the
// table, with the same scopes in the same order.
func TestKindAtomScopes_MatchBooksAtomTable(t *testing.T) {
	table := map[string]books.Atom{}
	for _, a := range books.AtomTable() {
		table[a.Name] = a
	}
	require.NotEmpty(t, server.T7AtomScopes)
	for name, scopes := range server.T7AtomScopes {
		row, ok := table[name]
		require.True(t, ok, "atom %q is not a row of books.AtomTable", name)
		require.Equal(t, row.Scopes, scopes, "atom %q scopes drifted from books.AtomTable", name)
	}
}
