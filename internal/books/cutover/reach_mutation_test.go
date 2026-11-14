// file: internal/books/cutover/reach_mutation_test.go
// version: 1.1.0
// guid: d3f60a82-7b19-4e54-8c2d-0a5e91b4f736
// last-edited: 2026-11-14

package cutover

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/httpapi"
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
	rows := append(reachExpected(), reachRecorderLimits()...)
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
			// A fresh cell is not a failure: a row of expected differences could
			// absorb it. The gate itself must fail on the changed table.
			if len(reachGateProblems(res, rows)) == 0 {
				t.Errorf("changing the %q row (%s) moved %d cell(s) and the gate passed: an expected-difference row absorbed them", row.Name, side, fresh)
			}
		}
	}
}

// ---- changes to the manifest itself ----

// reachJudgeRoutes is reachJudgeErr over a changed copy of the manifest.
func reachJudgeRoutes(vd *reachR0Doc, atoms []m3.AtomRow, whos []reachWho, routes []httpapi.LegacyRoute) (reachDiffResult, *reachR2, error) {
	saved := reachManifestSource
	reachManifestSource = func() []httpapi.LegacyRoute { return slices.Clone(routes) }
	defer func() { reachManifestSource = saved }()
	return reachJudgeErr(vd, atoms, whos)
}

// reachChecks is everything the oracle fails on for one run: the gate (an
// unexpected cell, an unused or miscounted row), the routes only one side has,
// the delegated set, the uniform-404 routes and the oracle's size.
func reachChecks(vd *reachR0Doc, res reachDiffResult, rows []reachExpect) []string {
	out := reachGateProblems(res, rows)
	out = append(out, reachOneSideProblems(res)...)
	out = append(out, reachDelegatedProblems(vd, res)...)
	out = append(out, reachUniformNotFoundProblems(vd)...)
	out = append(out, reachCountProblems(vd, res)...)
	return out
}

// reachRouteMutant is one change to one manifest route.
type reachRouteMutant struct {
	kind  string
	route string
	idx   int
	apply func(*httpapi.LegacyRoute)
}

// reachRouteMutants lists the changes the gate must not let through: every
// non-delegated route widened to AnyAuthenticated and narrowed to core:admin (a
// Public route closed, an AnyAuthenticated one narrowed), every AllOf of several
// scopes turned into AnyOf, and every Interactive and Owner guard dropped.
func reachRouteMutants(routes []httpapi.LegacyRoute) []reachRouteMutant {
	var out []reachRouteMutant
	add := func(kind string, i int, apply func(*httpapi.LegacyRoute)) {
		out = append(out, reachRouteMutant{kind: kind, route: routes[i].Method + " " + routes[i].GinPath, idx: i, apply: apply})
	}
	for i, lr := range routes {
		perm := lr.Perm
		switch perm.Kind() {
		case rbac.KindDelegated:
			continue
		case rbac.KindPublic:
			add("public route closed to AnyAuthenticated", i, func(r *httpapi.LegacyRoute) { r.Perm = rbac.AnyAuthenticated })
		case rbac.KindAuthenticated:
			add("AnyAuthenticated route narrowed to core:admin", i, func(r *httpapi.LegacyRoute) { r.Perm = rbac.Need(rbac.CoreAdmin) })
		default:
			add("route widened to AnyAuthenticated", i, func(r *httpapi.LegacyRoute) { r.Perm = rbac.AnyAuthenticated })
			add("route narrowed to core:admin", i, func(r *httpapi.LegacyRoute) { r.Perm = rbac.Need(rbac.CoreAdmin) })
		}
		if perm.Kind() == rbac.KindAllOf && len(perm.Permissions()) > 1 {
			scopes := perm.Permissions()
			add("AllOf turned into AnyOf", i, func(r *httpapi.LegacyRoute) { r.Perm = rbac.AnyOf(scopes...) })
		}
		if lr.Guards.Interactive {
			add("Interactive guard dropped", i, func(r *httpapi.LegacyRoute) { r.Guards.Interactive = false })
		}
		if lr.Guards.Owner {
			add("Owner guard dropped", i, func(r *httpapi.LegacyRoute) { r.Guards.Owner = false })
		}
	}
	return out
}

// reachEquivalentKind says why a mutant that changed no route's R2 verdict for
// any of the 37 principals could not have been seen by this oracle: a guard or the
// handler pin already dominates the requirement the mutant changed, or the
// principal table does not hold a principal that tells the two requirements apart
// (every holder of the old requirement holds the new one, and the reverse).
func reachEquivalentKind(m reachRouteMutant, routes []httpapi.LegacyRoute) string {
	lr := routes[m.idx]
	if _, pinned := reachHandlerAdmin[m.route]; pinned || lr.Guards.Owner {
		return "dominated by the Owner guard's core:admin fallback or the handler-admin pin"
	}
	return "no principal of the table tells the requirement from its replacement"
}

// reachEquivalentMutants is how many mutants the generator produces that move no
// principal's reach. It is pinned so a change in the oracle's resolving power (a
// principal added or dropped, a requirement changed) is a visible decision.
const reachEquivalentMutants = 37

func reachVerdictsEqual(a, b *reachR2) bool { return maps.Equal(a.verdict, b.verdict) }

// Any single change to a manifest route that moves R2's reach must fail the
// oracle (the gate, the pinned one-sided routes, the delegated set or the size).
// The gate used to be shown only atom-table changes: here each route is changed
// in the manifest, where the cutover's risk lies, and the changed run is held to
// the same checks the unchanged one passes.
func TestReachOracle_AnyChangedManifestRouteFailsTheOracle(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	snap, seed := reachMigrate(t, pd, atoms)
	whos := reachWhos(t, snap, seed, pd.Principals, reachCatalog(t))
	rows := append(reachExpected(), reachRecorderLimits()...)

	routes := reachManifest()
	baseRes, baseR2, err := reachJudgeRoutes(vd, atoms, whos, routes)
	if err != nil {
		t.Fatal(err)
	}
	if p := reachChecks(vd, baseRes, rows); len(p) > 0 {
		t.Fatalf("the unchanged manifest already fails the oracle:\n%s", strings.Join(p, "\n"))
	}

	mutants := reachRouteMutants(routes)
	byKind := map[string][3]int{} // total, moved reach and failed, equivalent
	var escaped []string
	equivalentWhy := map[string]int{}
	for _, m := range mutants {
		changed := slices.Clone(routes)
		m.apply(&changed[m.idx])
		res, r2, err := reachJudgeRoutes(vd, atoms, whos, changed)
		c := byKind[m.kind]
		c[0]++
		switch {
		case err != nil:
			c[1]++ // a route that no longer resolves fails the oracle by itself
		case reachVerdictsEqual(baseR2, r2):
			equivalentWhy[reachEquivalentKind(m, routes)]++
			c[2]++
		default:
			if len(reachChecks(vd, res, rows)) == 0 {
				escaped = append(escaped, fmt.Sprintf("%s: %s moved R2's reach and the oracle passed", m.route, m.kind))
			}
			c[1]++
		}
		byKind[m.kind] = c
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	total, caught, equivalent := 0, 0, 0
	for _, k := range kinds {
		c := byKind[k]
		total, caught, equivalent = total+c[0], caught+c[1], equivalent+c[2]
		t.Logf("manifest mutants %-48s %4d: reach moved and oracle failed %4d, equivalent %3d", k, c[0], c[1], c[2])
	}
	t.Logf("manifest mutants in all: %d; reach moved and the oracle failed on %d; equivalent (reach unchanged) %d; escaped %d", total, caught, equivalent, len(escaped))
	for why, n := range equivalentWhy {
		t.Logf("equivalent mutants (reach unchanged for all principals), %d: %s", n, why)
	}
	for _, e := range escaped {
		t.Error(e)
	}
	if equivalent != reachEquivalentMutants {
		t.Errorf("%d mutants changed no principal's reach, the pinned count is %d: the oracle's resolving power changed (a principal, a requirement or the generator)", equivalent, reachEquivalentMutants)
	}
	// The test must have bite: each kind produced mutants that moved reach.
	for _, k := range []string{"route widened to AnyAuthenticated", "route narrowed to core:admin", "public route closed to AnyAuthenticated", "AnyAuthenticated route narrowed to core:admin", "AllOf turned into AnyOf", "Interactive guard dropped", "Owner guard dropped"} {
		if byKind[k][1] == 0 {
			t.Errorf("no %q mutant moved reach: the generator or the oracle lost its bite", k)
		}
	}
}

// A route added, dropped or delegated without a decision fails the oracle, each
// by a named check: a route with no requirement cannot be judged at all, a new
// route is not on R0's side (pinned one-sided lists), a dropped one leaves R0's
// side, and a delegated one leaves the comparison (the delegated-set pin).
func TestReachOracle_ARouteAddedDroppedOrDelegatedWithoutADecisionFails(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	snap, seed := reachMigrate(t, pd, atoms)
	whos := reachWhos(t, snap, seed, pd.Principals, reachCatalog(t))
	rows := append(reachExpected(), reachRecorderLimits()...)
	routes := reachManifest()

	t.Run("a new route with no requirement is refused outright", func(t *testing.T) {
		added := append(slices.Clone(routes), httpapi.LegacyRoute{Method: "GET", GinPath: "/api/v1/batcave/gadgets"})
		_, _, err := reachJudgeRoutes(vd, atoms, whos, added)
		if err == nil || !strings.Contains(err.Error(), "no requirement") {
			t.Fatalf("a route with a zero requirement was judged: %v", err)
		}
	})
	t.Run("a new route with a decision is on no side R0 has", func(t *testing.T) {
		added := append(slices.Clone(routes), httpapi.LegacyRoute{Method: "GET", GinPath: "/api/v1/batcave/gadgets", Perm: rbac.AnyAuthenticated})
		res, _, err := reachJudgeRoutes(vd, atoms, whos, added)
		if err != nil {
			t.Fatal(err)
		}
		if p := reachOneSideProblems(res); len(p) == 0 {
			t.Fatal("a route R0 does not have was added and the pinned one-sided lists passed")
		}
		if p := reachCountProblems(vd, res); len(p) == 0 {
			t.Fatal("a route was added and the size pin passed")
		}
	})
	t.Run("a dropped route leaves R0's side", func(t *testing.T) {
		for i, lr := range routes {
			if lr.Method+" "+lr.GinPath != "GET /api/v1/users/merges" {
				continue
			}
			dropped := slices.Delete(slices.Clone(routes), i, i+1)
			res, _, err := reachJudgeRoutes(vd, atoms, whos, dropped)
			if err != nil {
				t.Fatal(err)
			}
			if p := reachOneSideProblems(res); len(p) == 0 {
				t.Fatal("a route was dropped and the pinned one-sided lists passed")
			}
			return
		}
		t.Fatal("the manifest has no GET /api/v1/users/merges")
	})
	t.Run("a route delegated to the ABS handler leaves the comparison and fails the pin", func(t *testing.T) {
		for i, lr := range routes {
			if lr.Method+" "+lr.GinPath != "GET /api/v1/users/merges" {
				continue
			}
			changed := slices.Clone(routes)
			changed[i].Perm = rbac.Delegated("books.abs")
			changed[i].OwnAuth = true
			res, _, err := reachJudgeRoutes(vd, atoms, whos, changed)
			if err != nil {
				t.Fatal(err)
			}
			saved := reachManifestSource
			reachManifestSource = func() []httpapi.LegacyRoute { return changed }
			p := reachDelegatedProblems(vd, res)
			reachManifestSource = saved
			if len(p) == 0 {
				t.Fatal("a route was delegated and the delegated-set pin passed")
			}
			if cp := reachChecks(vd, res, rows); len(cp) == 0 {
				t.Fatal("a route was delegated and the oracle passed")
			}
			return
		}
		t.Fatal("the manifest has no GET /api/v1/users/merges")
	})
}

// reachMaskedRoutes are the routes whose own requirement the oracle cannot
// see: an Owner guard (its fallback is Interactive plus core:admin, 05-authn 4.8)
// or the handler-admin pin refuses everyone but an admin session first, so
// widening or narrowing the requirement moves no cell (33 of the manifest
// mutants above). Each requirement is pinned here, from the spec, and the set
// is held to the manifest's Owner-guarded routes plus the pinned ones.
var reachMaskedRoutes = map[string]string{
	"POST /api/v1/admin/guest-service-tokens/:userId/revoke":  "core:admin",
	"POST /api/v1/admin/guest-service-tokens/:userId/reissue": "core:admin",
	"GET /api/v1/admin/guest-service-tokens/plan":             "core:admin",
	"POST /api/v1/backup/restore":                             "legacy(settings.manage)",
	"PUT /api/v1/users/:id/email":                             "legacy(users.manage)",
	"GET /api/v1/users/:id/footprint":                         "legacy(users.manage)",
	"POST /api/v1/users/:id/merge-into/:target/apply":         "legacy(users.manage)",
	"POST /api/v1/users/:id/merge-into/:target/preview":       "legacy(users.manage)",
	"GET /api/v1/users/merges":                                "legacy(users.manage)",
	"POST /api/v1/users/merges/:jid/resume":                   "legacy(users.manage)",
	"POST /api/v1/users/merges/:jid/undo":                     "legacy(users.manage)",
}

func TestReachOracle_RoutesAGuardDominatesKeepTheirRequirement(t *testing.T) {
	got := map[string]string{}
	for _, lr := range reachManifest() {
		k := lr.Method + " " + lr.GinPath
		if _, pinned := reachHandlerAdmin[k]; pinned || lr.Guards.Owner {
			got[k] = lr.Perm.String()
		}
	}
	for k, want := range reachMaskedRoutes {
		if got[k] != want {
			t.Errorf("%s: requirement %q, pinned %q (the oracle cannot see this change: an Owner guard or the handler pin dominates it)", k, got[k], want)
		}
	}
	for k := range got {
		if _, ok := reachMaskedRoutes[k]; !ok {
			t.Errorf("%s is Owner-guarded or pinned and its requirement is not pinned in reachMaskedRoutes", k)
		}
	}
}
