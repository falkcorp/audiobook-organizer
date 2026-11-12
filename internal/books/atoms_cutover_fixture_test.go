// file: internal/books/atoms_cutover_fixture_test.go
// version: 1.0.0
// guid: c9dfca45-cfb8-4671-b580-1ad0fdcf717b
// last-edited: 2026-11-12

package books

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// TestAtomTable_MatchesIdentityCutoverFixture: the identity cutover's tests run
// M3 over a fixture atom table that internal/books/cutover/testdata holds as a
// golden (that package cannot import this one). A row added to, dropped from
// or reordered in AtomTable fails here until the golden, and through it the
// cutover's fixture, follows.
func TestAtomTable_MatchesIdentityCutoverFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("cutover", "testdata", "identity_atoms.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Atoms []struct {
			Name   string            `json:"name"`
			Scopes []rbac.Permission `json:"scopes"`
		} `json:"atoms"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	table := AtomTable()
	if len(table) != len(golden.Atoms) {
		t.Fatalf("AtomTable has %d rows, the cutover fixture's golden %d", len(table), len(golden.Atoms))
	}
	for i, a := range table {
		if a.Name != golden.Atoms[i].Name || !slices.Equal(a.Scopes, golden.Atoms[i].Scopes) {
			t.Errorf("row %d: AtomTable %s %v, golden %s %v", i, a.Name, a.Scopes, golden.Atoms[i].Name, golden.Atoms[i].Scopes)
		}
	}
}
