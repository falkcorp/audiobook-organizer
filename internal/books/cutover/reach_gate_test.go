// file: internal/books/cutover/reach_gate_test.go
// version: 1.3.0
// guid: 7d3a8f21-6c4e-4b95-a0d7-1e9b2c5f8a36
// last-edited: 2026-11-13

package cutover

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
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
	want   int                         // how many cells it claims, exactly: fewer or more is a failure
}

// routeIs matches exact routes.
func routeIs(keys ...string) func(string) bool {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	return func(k string) bool { return set[k] }
}

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
	short      []string       // rows whose cell count is not the one they assert
}

// reachClassify assigns every differing cell to the rows that match it. A row
// asserts both letters, so a cell that moved the other way is unexpected even
// when its route and principal are on the row, and it asserts how many cells it
// claims, so a partial reversal of a ruled change (a route that stops differing
// for some of the row's principals) fails instead of leaving the row "used".
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
		switch n := out.hits[r.name]; {
		case n == 0:
			out.unused = append(out.unused, r.name)
		case n != r.want:
			out.short = append(out.short, fmt.Sprintf("%s: matches %d cell(s), asserts %d", r.name, n, r.want))
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

// reachInteractiveRoutes matches the manifest's Guards.Interactive routes.
func reachInteractiveRoutes() func(string) bool {
	set := map[string]bool{}
	for _, lr := range reachManifest() {
		if lr.Guards.Interactive {
			set[lr.Method+" "+lr.GinPath] = true
		}
	}
	return func(k string) bool { return set[k] }
}

// reachAuthenticatedRoutes matches the manifest's routes that need authentication
// and nothing else (HEAD's permission-less routes, pAuth).
func reachAuthenticatedRoutes() func(string) bool {
	set := map[string]bool{}
	for _, lr := range reachManifest() {
		if lr.Perm.Kind() == rbac.KindAuthenticated {
			set[lr.Method+" "+lr.GinPath] = true
		}
	}
	return func(k string) bool { return set[k] }
}

// reachIdentityRoutes are the credential-metadata routes whose successors are
// core's native ones (05-authn 4.11, 4.13): the manifest keeps them as
// Authenticated until F4-08 deletes them, and core's replacement answers the
// auth-disabled principal 401 itself, which is the core-route half's assertion.
var reachIdentityRoutes = []string{
	"GET /api/v1/auth/me",
	"GET /api/v1/auth/sessions",
	"DELETE /api/v1/auth/sessions/:id",
	"GET /api/v1/auth/api-keys",
	"GET /api/v1/auth/api-keys/:id",
	"POST /api/v1/auth/api-keys",
	"DELETE /api/v1/auth/api-keys/:id",
}

// reachExpected is the list of expected differences between R0's recorded reach
// and R2's after M3. Each row is a clause of 05-rbac or a recorded decision; a
// difference that is on none of them is STOP 2. A row names both letters.
func reachExpected() []reachExpect {
	return []reachExpect{
		{
			name:  "05-rbac 4.11(a): auth-off tightening on Guards.Interactive routes",
			why:   "HEAD's credential guard is a pass-through with auth off; the auth-disabled principal is non-interactive",
			route: reachInteractiveRoutes(), who: whoIs("authoff"), r0: 'A', r2: 'D', want: 11,
		},
		{
			name:  "05-rbac 4.11(d): credential-metadata routes answer the auth-disabled principal 401",
			why:   "HEAD's handler needs a user and has none with auth off; the successor is core's native route, whose answer is core's half",
			route: routeIs(reachIdentityRoutes...), who: whoIs("authoff"), r0: 'D', r2: 'A', want: 7,
		},
		{
			name:  "05-authn 4.8: restore's owner guard falls back to Interactive plus core:admin",
			why:   "HEAD's s.perm let a role holding every atom restore; with no OwnerVerifier the guard's fallback needs core:admin, which a custom role never holds (core:backup.restore, 05-rbac 6.2, is the core half's check)",
			route: routeIs("POST /api/v1/backup/restore"), who: whoIs("session:custom-all"), r0: 'A', r2: 'D', want: 1,
		},
		{
			name:  "05-rbac 6.3: a key whose atoms lie outside an fp-worker owner's roles reaches no route",
			why:   "HEAD's workerOnly reads an empty effective set as not worker-only and let the key through every authenticated-only route; the key's checker is empty after the cutover, because the fp-worker role holds no ambient scope",
			route: reachAuthenticatedRoutes(), who: whoIs("key:fp-worker:view", "key:fp-worker:settings", "key:fp-worker:users"), r0: 'A', r2: 'D', want: 96,
		},
		{
			name:  "05-rbac 6.2 / 4.4 rule 1: debug edits are interactive-only",
			why:   "books:admin.debug is Sensitive, so a non-interactive principal is refused even with core:admin; HEAD's RequireAdmin let an admin's every-atom or empty-list key through",
			route: routeIs("PATCH /api/v1/admin/debug/books/:id", "PATCH /api/v1/admin/debug/book-files/:id", "POST /api/v1/admin/debug/edits/:edit_id/undo"),
			who:   whoIs("key:admin:every", "key:admin:empty"), r0: 'A', r2: 'D', want: 6,
		},
	}
}

// reachRecorderLimits are differences that are not a change of policy: the
// probe cannot reach the authorization decision R0's own code makes, so R0's
// letter is the handler's refusal of the probe, not the router's reach. Each is
// asserted on both letters like an expected row; none is a clause of 05-rbac.
func reachRecorderLimits() []reachExpect {
	return []reachExpect{
		{
			name:  "recorder limit: a Public route whose handler checks credentials",
			why:   "POST /login and /auth/refresh answer 401 to the probe's dummy credentials for every principal; the router lets everyone in",
			route: routeIs("POST /login", "POST /auth/refresh"), who: anyWho, r0: 'D', r2: 'A', want: 74,
		},
		{
			name:  "recorder limit: the conditional credential guard fails closed on the probe's unknown key id",
			why:   "POST /auth/api-keys/:id/rotate (retired, answers 410) refuses a key whose target it cannot show is its own user's; the probe's id is unknown",
			route: routeIs("POST /api/v1/auth/api-keys/:id/rotate"), who: whoHas("key:"), r0: 'D', r2: 'A', want: 21,
		},
		{
			name:  "recorder limit: approving needs a user and a verified method inside the handler",
			why:   "R0's ApproveRun refuses the auth-disabled principal for want of a user to record; R2's route-level atom is books:view",
			route: routeIs("POST /api/v3/ops/runs/:id/approve"), who: whoIs("authoff"), r0: 'D', r2: 'A', want: 1,
		},
	}
}

// reachGateProblems is what the gate fails on, as text: an unexpected cell or a
// row that matched nothing.
func reachGateProblems(res reachDiffResult, rows []reachExpect) []string {
	var out []string
	c := reachClassify(res.cells, rows)
	if len(c.unexpected) > 0 {
		out = append(out, reportUnexpected(c.unexpected))
	}
	for _, name := range c.unused {
		out = append(out, "expected difference matches no cell (it is stale, or the oracle stopped seeing it): "+name)
	}
	for _, msg := range c.short {
		out = append(out, "expected difference claims a different number of cells than it asserts (a ruled change was partly reversed, or the oracle sees a different set): "+msg)
	}
	total := 0
	for _, r := range rows {
		if r.want <= 0 {
			out = append(out, "expected difference asserts no cell count: "+r.name)
		}
		total += r.want
	}
	if len(c.unexpected) == 0 && len(res.cells) != total {
		out = append(out, fmt.Sprintf("the rows assert %d differing cell(s) in all, the oracle found %d", total, len(res.cells)))
	}
	return out
}

func TestIdentityCutover_ReachOracle(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _ := reachCompare(t, pd, vd, atoms, atoms)
	t.Logf("R0 routes recorded: %d; manifest routes compared: %d (%d cells, task starts and approvals included); delegated to the ABS handler: %d; R0 only: %d; R2 only: %d; differing cells: %d",
		vd.RouteCount, vd.RouteCount-len(res.r0Only)-len(res.delegated), res.compared, len(res.delegated), len(res.r0Only), len(res.r2Only), len(res.cells))
	rows := append(reachExpected(), reachRecorderLimits()...)
	c := reachClassify(res.cells, rows)
	for _, r := range rows {
		t.Logf("expected difference %-3d cells: %s", c.hits[r.name], r.name)
	}
	for _, p := range reachGateProblems(res, rows) {
		t.Error(p)
	}
}

// The routes only one side has are pinned exactly, with the reason for each.
// A route that appears or disappears fails here, never silently.
func TestReachOracle_RoutesOnOneSideAreExactlyThesePinned(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _ := reachCompare(t, pd, vd, atoms, atoms)
	wantR0Only := []string{
		"GET /",                             // the SPA handler, outside the manifest (served by the embedded frontend)
		"POST /api/v1/system/factory-reset", // HEAD's reset routes: deleted at F4, the native core route succeeds them (05-rbac 6.2)
		"POST /api/v1/system/reset",
	}
	wantR2Only := []string{
		"POST /api/v1/admin/guest-service-tokens/:userId/reissue", // newer than the R0 tag: no baseline to compare with
	}
	if !slices.Equal(res.r0Only, wantR0Only) {
		t.Errorf("routes only R0 has:\n got %q\nwant %q", res.r0Only, wantR0Only)
	}
	if !slices.Equal(res.r2Only, wantR2Only) {
		t.Errorf("routes only R2 has:\n got %q\nwant %q", res.r2Only, wantR2Only)
	}
}
