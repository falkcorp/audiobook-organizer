// file: internal/books/cutover/registered_reach_b10y_test.go
// version: 0.1.0
// guid: 4e1c7a92-6d05-4b38-9f2a-c3b8e5d17a60
// last-edited: 2026-11-22

package cutover_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// TestRegisteredReach_B10yCellsMatchHEAD is HELD RED until B10y lands (F4-08 B10f).
//
// TestRegisteredReach_Oracle pins the cells below as differences from R0 so it stays
// green: the 38 admin-key cells of registeredB10yFamily, the 6 repairs.plan approval
// cells of registeredApprovalFixerPermitted and the 2 invite cells of
// registeredAuthOffNeedsUser, each with its core-key twins. None of them is a ruled
// narrowing. They are a fix B10y owns, so this test holds every one of them to the
// letter R0 recorded (HEAD's verdict) and fails, cell by cell, while the flip answers
// anything else.
//
// When B10y lands the cells stop differing. TestRegisteredReach_Oracle then fails
// with "listed difference no longer differs", which tells B10y to delete the tables,
// and this test goes green. A cell the sweep never probed also fails here, so a
// route that leaves the corpus cannot turn the test green.
func TestRegisteredReach_B10yCellsMatchHEAD(t *testing.T) {
	r0 := loadR0Recording(t)

	// check holds, for one boot's pass, every cell of the tables to R0's letter: the cell must
	// have been probed, R0 must have recorded a letter for it, and the flip must
	// answer that letter.
	check := func(t *testing.T, h *Registered, pass *oraclePass, tables []diffTable) int {
		t.Helper()
		if len(pass.noVerdict) > 0 || len(pass.canaryDrift) > 0 {
			t.Fatalf("the pass proves nothing: no verdict %v, canary drift %v", pass.noVerdict, pass.canaryDrift)
		}
		idx := map[string]int{}
		for i, n := range r0.Principals {
			idx[n] = i
		}
		verdict := map[string]string{}
		for _, r := range append(slices.Clone(r0.Routes), taskRoutes(r0)...) {
			verdict[r.key()] = r.Verdict
		}
		held := map[string]diffRow{}
		for _, tb := range tables {
			cells := tb.expand()
			twin := h.twins(cells)
			if len(cells) != tb.want || len(twin) != tb.wantTwin {
				t.Errorf("table %q expands to %d cells and %d core-key twin cells, claims %d and %d", tb.name, len(cells), len(twin), tb.want, tb.wantTwin)
			}
			maps.Copy(held, cells)
			maps.Copy(held, twin)
		}
		for _, k := range slices.Sorted(maps.Keys(held)) {
			route, principal := splitCell(k)
			c, probed := pass.cells[route][principal]
			v, recorded := verdict[route]
			i, known := idx[pass.recorded[principal]]
			if !probed || !recorded || !known || i >= len(v) {
				t.Errorf("held cell was not compared (not probed, or R0 has no letter for it): %s", k)
				continue
			}
			if want := v[i]; c.letter != want {
				t.Errorf("B10y cell: R0 %c, the flip answers %c (%d): %s", want, c.letter, c.status, k)
			}
		}
		return len(held)
	}

	t.Run("auth-on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{Seed: seedDelugeDiscovery})
		pass := h.runOracle(r0)
		h.runTasks(r0, pass)
		n := check(t, h, pass, []diffTable{registeredB10yFamily, registeredApprovalFixerPermitted})
		if want := 38 + 38 + 6 + 4; n != want {
			t.Errorf("held %d auth-on cells, want %d", n, want)
		}
	})

	t.Run("auth-off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true, Seed: seedDelugeDiscovery})
		pass := h.runOracle(r0)
		n := check(t, h, pass, []diffTable{registeredAuthOffNeedsUser})
		if n != 2 {
			t.Errorf("held %d auth-off cells, want 2", n)
		}
	})
}

// splitCell is the route and the principal of an oracleDiff.cell() key.
func splitCell(cell string) (route, principal string) {
	i := strings.LastIndex(cell, " / ") // the route "GET /" holds a " / " itself
	return cell[:i], cell[i+3:]
}
