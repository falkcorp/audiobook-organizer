// file: core/internal/taskhttp/counts_reach_test.go
// version: 1.0.0
// guid: 0b7e3c58-d1a6-4e92-8f4b-6a2c9d5e1f73
// last-edited: 2026-11-28

package taskhttp

import (
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 reach R2 over the HTTP surface and a real executor.

func totalOf(t *testing.T, g *rig, as ...rbac.Permission) int64 {
	t.Helper()
	rec := g.do(req{method: "GET", path: "/counts", as: session("robin", as...)})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /counts = %d\n%s", rec.Code, rec.Body.String())
	}
	var v struct {
		Total int64 `json:"total"`
	}
	decode(t, rec, &v)
	return v.Total
}

// TestCounts_LeavesOutRunsTheCallerCannotView (R2): a run of kindScan
// (View batcave:see) is in an admin's census and not in a plain viewer's,
// and the viewer gets 404 for the same run. Killed by Deps.Counts answering
// the store's raw census.
func TestCounts_LeavesOutRunsTheCallerCannotView(t *testing.T) {
	g := realRigSeeded(t, seedRetired(nil, kindScan, kindSweep))
	if got := totalOf(t, g, scopeView); got != 1 {
		t.Fatalf("viewer's total = %d, want 1 (the sweep run only)", got)
	}
	if got := totalOf(t, g, scopeView, scopeSee); got != 2 {
		t.Fatalf("viewer with batcave:see total = %d, want 2", got)
	}
	wantProblem(t, g.do(req{method: "GET", path: "/runs/run-" + string(kindScan), as: session("robin", scopeView)}), 404, "tasks.run_not_found")
}

// TestCounts_RetiredKindFollowsItsSnapshot (R2 over R3): the census counts a
// run of a kind with no live definition exactly when GET /runs/{id} shows it.
func TestCounts_RetiredKindFollowsItsSnapshot(t *testing.T) {
	for _, c := range []struct {
		name string
		snap tasks.Definition
		want int64
	}{
		{"snapshot records a View", retiredDef(rbac.Need(scopeSee)), 0},
		{"snapshot records no View", retiredDef(rbac.Requirement{}), 1},
		{"no snapshot", nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := realRigSeeded(t, seedRetired(c.snap, kindRetired))
			if got := totalOf(t, g, scopeView); got != c.want {
				t.Fatalf("viewer's census = %d, want %d", got, c.want)
			}
			wantAdmin := c.want
			if c.snap != nil {
				wantAdmin = 1
			}
			if got := totalOf(t, g, scopeView, scopeSee, scopeManage); got != wantAdmin {
				t.Fatalf("admin's census = %d, want %d", got, wantAdmin)
			}
		})
	}
}
