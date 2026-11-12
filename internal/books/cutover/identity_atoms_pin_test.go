// file: internal/books/cutover/identity_atoms_pin_test.go
// version: 1.0.0
// guid: 35f87a2f-d57f-4ae1-896d-0f89417e7e80
// last-edited: 2026-11-12

package cutover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// identityAtomsGolden is the atom table the identity tests run M3 over, as a
// file: internal/books pins books.AtomTable to the same file
// (TestAtomTable_MatchesIdentityCutoverFixture), because this package cannot
// import internal/books (it will import this one).
const identityAtomsGolden = "internal/books/cutover/testdata/identity_atoms.json"

// TestIdentityFixture_MatchesTheAtomGolden: the fixture the identity tests use
// (idAtoms) is the golden's rows in the golden's order. A fixture that drifted
// from the table would prove the mapping of a table nobody ships.
func TestIdentityFixture_MatchesTheAtomGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(identityAtomsGolden)))
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
	fix := idAtoms()
	if len(fix) != len(golden.Atoms) || len(fix) != 13 {
		t.Fatalf("the fixture has %d atoms, the golden %d (05-rbac 6.1 has 13)", len(fix), len(golden.Atoms))
	}
	for i, g := range golden.Atoms {
		if fix[i].Name != g.Name || !slices.Equal(fix[i].Scopes, g.Scopes) {
			t.Errorf("row %d: fixture %s %v, golden %s %v", i, fix[i].Name, fix[i].Scopes, g.Name, g.Scopes)
		}
	}
}
