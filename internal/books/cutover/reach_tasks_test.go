// file: internal/books/cutover/reach_tasks_test.go
// version: 1.0.0
// guid: 0c7e4a19-5d38-4b62-9f81-a3d6b2e8c570
// last-edited: 2026-11-13

package cutover

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Task starts and approvals (F4-07).
//
// R0 starts a run behind two checks: the route's floor (scan.trigger, on
// POST /api/v3/ops/defs/:id/runs) and then the definition's own permissions,
// checked inside the handler. It approves behind the route's library.view, a
// verified method, and the definition's approver permission, also inside the
// handler. The recorder asks every definition's start and every awaiting run's
// approval of every principal; here both are rebuilt from the same atom table
// the routes resolve through, so a changed atom row moves them too.
//
// The two pseudo-routes enter the diff as "TASK-START <def>" and
// "TASK-APPROVE <def>", so an expected-difference row names them like a route.

const (
	reachTaskStartRoute   = "POST /api/v3/ops/defs/:id/runs"
	reachTaskApproveRoute = "POST /api/v3/ops/runs/:id/approve"
	reachTaskStartMethod  = "TASK-START"
	reachTaskApproveMeth  = "TASK-APPROVE"
)

// reachAtomGate resolves one legacy atom the way a route naming it would.
func reachAtomGate(atoms []m3.AtomRow, atom string) (reachGate, error) {
	return reachResolve(atoms, httpapi.LegacyRoute{Method: "TASK", GinPath: atom, Perm: rbac.LegacyAtom(atom)})
}

// reachRouteGate is the gate of a manifest route, by its "METHOD /gin/path" key.
func reachRouteGate(atoms []m3.AtomRow, routes []httpapi.LegacyRoute, key string) (reachGate, error) {
	for _, lr := range routes {
		if lr.Method+" "+lr.GinPath == key {
			return reachResolve(atoms, lr)
		}
	}
	return reachGate{}, fmt.Errorf("the manifest has no route %s", key)
}

// reachTaskVerdicts returns the R2 letter strings of the recorded task starts
// and approvals, keyed like the pseudo-routes, with one letter for each of the
// first n principals (the recorder does not start tasks as the auth-disabled
// principal). An approval whose R0 letter is N reached no approver check and
// carries no reach information: it is left out.
func reachTaskVerdicts(atoms []m3.AtomRow, routes []httpapi.LegacyRoute, vd *reachR0Doc, whos []reachWho) (map[string]string, error) {
	out := map[string]string{}
	floor, err := reachRouteGate(atoms, routes, reachTaskStartRoute)
	if err != nil {
		return nil, err
	}
	for _, t := range vd.Tasks {
		gates := []reachGate{floor}
		for _, p := range t.Permissions {
			g, err := reachAtomGate(atoms, p)
			if err != nil {
				return nil, fmt.Errorf("task %s: %w", t.ID, err)
			}
			gates = append(gates, g)
		}
		var b strings.Builder
		for _, w := range whos[:len(t.Verdict)] {
			b.WriteByte(reachAllOf(gates, w))
		}
		out[reachTaskStartMethod+" "+t.ID] = b.String()
	}
	view, err := reachRouteGate(atoms, routes, reachTaskApproveRoute)
	if err != nil {
		return nil, err
	}
	approver := map[string]string{}
	for _, t := range vd.Tasks {
		approver[t.ID] = t.Approver
	}
	for _, a := range vd.Approvals {
		def, _ := a.Run["def_id"].(string)
		name := approver[def]
		if name == "" {
			name = "settings.manage" // R0's default approver permission
		}
		g, err := reachAtomGate(atoms, name)
		if err != nil {
			return nil, fmt.Errorf("approval of a run of %s: %w", def, err)
		}
		var b strings.Builder
		for _, w := range whos[:len(a.Verdict)] {
			// An approval needs a verified method: R0 refuses the
			// auth-disabled principal for want of a user to record.
			if w.chk == nil || w.chk.authOff {
				b.WriteByte('D')
				continue
			}
			b.WriteByte(reachAllOf([]reachGate{view, g}, w))
		}
		out[reachTaskApproveMeth+" "+def] = b.String()
	}
	return out, nil
}

// reachAllOf is 'A' when every gate allows w, else 'D'.
func reachAllOf(gates []reachGate, w reachWho) byte {
	for _, g := range gates {
		if !g.allows(w) {
			return 'D'
		}
	}
	return 'A'
}

// reachTaskRoutes are the recorded starts and approvals as pseudo-routes of an
// R0 document: an approval letter N (no approver check reached) becomes the R2
// letter, so the cell never differs and is never counted as a finding.
func reachTaskRoutes(vd *reachR0Doc, r2 map[string]string) []reachR0Route {
	var out []reachR0Route
	for _, t := range vd.Tasks {
		out = append(out, reachR0Route{Method: reachTaskStartMethod, Path: t.ID, Verdict: t.Verdict})
	}
	for _, a := range vd.Approvals {
		def, _ := a.Run["def_id"].(string)
		v2 := r2[reachTaskApproveMeth+" "+def]
		v := []byte(a.Verdict)
		for i := range v {
			if v[i] == 'N' {
				v[i] = v2[i]
			}
		}
		out = append(out, reachR0Route{Method: reachTaskApproveMeth, Path: def, Verdict: string(v)})
	}
	return out
}
