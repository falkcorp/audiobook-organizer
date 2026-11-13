// file: internal/books/cutover/reach_gate_test.go
// version: 1.0.0
// guid: 7d3a8f21-6c4e-4b95-a0d7-1e9b2c5f8a36
// last-edited: 2026-11-13

package cutover

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The reach oracle's expected differences (F4-07), and the classifier that holds
// the oracle to them.
//
// An expected difference is a claim about a set of (route, principal) cells:
// every cell it matches has the R0 letter and the R2 letter it names, and it
// matches at least one cell. A cell that differs and no row claims fails the
// oracle with STOP 2's wording: the difference is reported to the owner of the
// spec, never added here to make the gate pass.

// reachExpect is one expected-difference row.
type reachExpect struct {
	name   string                      // the spec clause or decision it comes from
	why    string                      // what changed, in a sentence
	route  func(key string) bool       // which routes ("METHOD /gin/path")
	who    func(principal string) bool // which principals
	r0, r2 byte                        // the letters it asserts on each side
}

// routeIs matches exact routes.
func routeIs(keys ...string) func(string) bool {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return func(k string) bool { return set[k] }
}

// routeHas matches routes whose key contains any of the fragments.
func routeHas(frags ...string) func(string) bool {
	return func(k string) bool {
		for _, f := range frags {
			if strings.Contains(k, f) {
				return true
			}
		}
		return false
	}
}

// anyRoute matches every route.
func anyRoute(string) bool { return true }

// whoIs matches exact principal names.
func whoIs(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(p string) bool { return set[p] }
}

// whoHas matches principals whose name contains any of the fragments.
func whoHas(frags ...string) func(string) bool {
	return func(p string) bool {
		for _, f := range frags {
			if strings.Contains(p, f) {
				return true
			}
		}
		return false
	}
}

// anyWho matches every principal.
func anyWho(string) bool { return true }

func (e reachExpect) matches(c reachCell) bool {
	return e.route(c.route) && e.who(c.principal) && c.r0 == e.r0 && c.r2 == e.r2
}

// reachClassified is the diff sorted against the rows.
type reachClassified struct {
	unexpected []reachCell    // differ, and no row claims them
	unused     []string       // rows that matched no cell
	hits       map[string]int // cells per row
}

// reachClassify assigns every differing cell to the rows that match it. A row
// asserts both letters, so a cell that moved the other way is unexpected even
// when its route and principal are on the row.
func reachClassify(cells []reachCell, rows []reachExpect) reachClassified {
	out := reachClassified{hits: map[string]int{}}
	for _, c := range cells {
		claimed := false
		for _, r := range rows {
			if r.matches(c) {
				out.hits[r.name]++
				claimed = true
			}
		}
		if !claimed {
			out.unexpected = append(out.unexpected, c)
		}
	}
	for _, r := range rows {
		if out.hits[r.name] == 0 {
			out.unused = append(out.unused, r.name)
		}
	}
	return out
}

// reportUnexpected is the STOP-2 message: it lists the cells grouped by route so
// a reader sees the shape of the difference.
func reportUnexpected(cells []reachCell) string {
	byRoute := map[string][]string{}
	for _, c := range cells {
		byRoute[c.route] = append(byRoute[c.route], fmt.Sprintf("%s %c>%c", c.principal, c.r0, c.r2))
	}
	routes := make([]string, 0, len(byRoute))
	for r := range byRoute {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	var b strings.Builder
	fmt.Fprintf(&b, "%d cell(s) on %d route(s) differ from R0 and are on no expected-difference row.\n", len(cells), len(routes))
	b.WriteString("STOP 2: report these to the spec's owner; do not add them to reachExpected.\n")
	for _, r := range routes {
		fmt.Fprintf(&b, "  %s\n    %s\n", r, strings.Join(byRoute[r], ", "))
	}
	return b.String()
}

// reachExpected is the list of expected differences between R0's recorded reach
// and R2's after M3. Each row is a clause of 05-rbac or a recorded decision; a
// difference that is on none of them is STOP 2.
var reachExpected = []reachExpect{}

// reachGateProblems is what the gate fails on, as text: an unexpected cell, a
// row that matched nothing, or a route one side has and the other lacks that
// no row names.
func reachGateProblems(res reachDiffResult, rows []reachExpect) []string {
	var out []string
	c := reachClassify(res.cells, rows)
	if len(c.unexpected) > 0 {
		out = append(out, reportUnexpected(c.unexpected))
	}
	for _, name := range c.unused {
		out = append(out, "expected difference matches no cell (it is stale, or the oracle stopped seeing it): "+name)
	}
	return out
}

func TestIdentityCutover_ReachOracle(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _ := reachCompare(t, pd, vd, atoms, atoms)
	t.Logf("R0 routes recorded: %d; manifest routes compared: %d routes' cells %d; delegated (ABS handler's own auth): %d; R0 only: %d; R2 only: %d; differing cells: %d",
		vd.RouteCount, vd.RouteCount-len(res.r0Only)-len(res.delegated), res.compared, len(res.delegated), len(res.r0Only), len(res.r2Only), len(res.cells))
	t.Logf("R0 only: %v", res.r0Only)
	t.Logf("R2 only: %v", res.r2Only)
	for _, c := range res.cells {
		t.Logf("DIFF %s", c)
	}
	for _, p := range reachGateProblems(res, reachExpected) {
		t.Error(p)
	}
}
