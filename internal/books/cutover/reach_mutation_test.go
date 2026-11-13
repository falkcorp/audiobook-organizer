// file: internal/books/cutover/reach_mutation_test.go
// version: 1.0.0
// guid: d3f60a82-7b19-4e54-8c2d-0a5e91b4f736
// last-edited: 2026-11-13

package cutover

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/rbac"
)

// The brief's mutation check: the oracle must fail if any single atom-table row
// is changed. Each row is changed in a test-only copy, to a scope held only by
// the fingerprint worker, and the oracle is run again; the changed run must show
// a differing cell the unchanged run did not. The row is changed twice over: in
// the table M3 migrates by, and in the table the routes resolve through.

// reachNoRoute are the rows no manifest route enforces: a mutation of one moves
// no route, so the oracle cannot see it. Each is pinned elsewhere, and the test
// fails if a row not named here also moves nothing.
var reachNoRoute = map[string]string{
	"playlists.create": "unenforced by design (books' table gives it a zero route requirement); TestAtomTable_Golden and TestIdentityFixture_MatchesTheAtomGolden pin the row",
	"requests.create":  "no manifest route and no recorded task definition names it; the golden tests pin the row",
	"requests.approve": "no manifest route names it (an approval's approver is the definition's, settings.manage on every recorded one); the golden tests pin the row",
}

func reachMutated(atoms []m3.AtomRow, name string) []m3.AtomRow {
	out := slices.Clone(atoms)
	for i := range out {
		if out[i].Name == name {
			out[i].Scopes = []rbac.Permission{"books:fingerprint.worker"}
			if name == "fingerprint.worker" {
				out[i].Scopes = []rbac.Permission{"books:view"}
			}
		}
	}
	return out
}

func reachCellSet(res reachDiffResult) map[string]bool {
	out := map[string]bool{}
	for _, c := range res.cells {
		out[c.String()] = true
	}
	return out
}

func TestReachOracle_AnyChangedAtomRowIsReported(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	base, _ := reachCompare(t, pd, vd, atoms, atoms)
	was := reachCellSet(base)
	for _, row := range atoms {
		for _, side := range []string{"route resolution", "M3 and route resolution"} {
			m3Atoms := atoms
			if side == "M3 and route resolution" {
				m3Atoms = reachMutated(atoms, row.Name)
			}
			res, _ := reachCompare(t, pd, vd, m3Atoms, reachMutated(atoms, row.Name))
			fresh := 0
			for k := range reachCellSet(res) {
				if !was[k] {
					fresh++
				}
			}
			if _, exempt := reachNoRoute[row.Name]; exempt {
				if side == "M3 and route resolution" {
					// M3 writes an every-atom key's scopes from every row, so a
					// route-less row still reaches that key's reach.
					continue
				}
				if fresh != 0 {
					t.Errorf("%s (%s): exempt as route-less, yet changed %d cells", row.Name, side, fresh)
				}
				continue
			}
			if fresh == 0 {
				t.Errorf("changing the %q row (%s) moved no cell: the oracle cannot see it", row.Name, side)
			}
		}
	}
}
