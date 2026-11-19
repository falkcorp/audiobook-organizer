// file: internal/scheduler/kinds_t7_atoms_test.go
// version: 1.0.0
// guid: 315af39b-7fd4-4f0e-92c1-e5b756494c71
// last-edited: 2026-11-18

package scheduler_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
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
	require.NotEmpty(t, scheduler.T7AtomScopes)
	for name, scopes := range scheduler.T7AtomScopes {
		row, ok := table[name]
		require.True(t, ok, "atom %q is not a row of books.AtomTable", name)
		require.Equal(t, row.Scopes, scopes, "atom %q scopes drifted from books.AtomTable", name)
	}
}
