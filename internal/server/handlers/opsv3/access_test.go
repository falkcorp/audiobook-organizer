// file: internal/server/handlers/opsv3/access_test.go
// version: 1.0.0
// guid: 91c4e7a2-5d38-4b06-a2f1-6e0b8d3c7a95
// last-edited: 2026-10-24

package opsv3

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// The Repairs lane's runs through the v3 handler's own routes (the server-level
// tests in internal/server drive the same checks through the wired router): a
// repairs.plan / repairs.apply run belongs to a fixer, and a caller who does not
// hold the fixer's permissions can neither read it nor cancel or approve it,
// whatever the generic permissions on those routes say.

const refusesRepairsRun = "Repairs fixer you may not use"

type usersManageFixer struct{ repairs.Fixer }

func (usersManageFixer) RequiredPermissions() []auth.Permission {
	return []auth.Permission{auth.PermUsersManage}
}

type fixerSet map[string]repairs.Fixer

func (m fixerSet) Get(id string) (repairs.Fixer, bool) { f, ok := m[id]; return f, ok }

// holdsHeader holds exactly the permissions the test caller's header lists.
func holdsHeader(c *gin.Context, p auth.Permission) bool { return auth.Can(c.Request.Context(), p) }

func repairsHarness(withAccess bool) (*fakeEnq, *fakeApprover, *memRuns, http.Handler) {
	defs := map[string]DefInfo{
		repairs.PlanOpID:  {ID: repairs.PlanOpID, Title: "Repairs plan", Kind: "adapted", Approver: auth.PermSettingsManage, Permissions: []auth.Permission{auth.PermLibraryEditMetadata}},
		repairs.ApplyOpID: {ID: repairs.ApplyOpID, Title: "Repairs apply", Kind: "adapted", Approver: auth.PermSettingsManage, Permissions: []auth.Permission{auth.PermLibraryEditMetadata}},
		"library.scan":    {ID: "library.scan", Title: "Scan", Kind: "adapted"},
	}
	enq, appr, mem, _ := startHarness(defs, nil)
	d := Deps{Runs: mem, Defs: &fakeDefs{defs: defs}, Enqueuer: enq, Approver: appr}
	if withAccess {
		d.RepairsAccess = opaccess.NewRepairs(fixerSet{"needs.users": usersManageFixer{}}, holdsHeader)
	}
	return enq, appr, mem, newRouter(New(d))
}

func repairsRun(id, fixer string, st state.V3, starter string) database.RunRecord {
	return database.RunRecord{
		ID: id, DefID: repairs.ApplyOpID, State: st,
		Params: []byte(`{"fixer_id":"` + fixer + `"}`),
		Actor:  database.Actor{UserID: starter, AuthMethod: "session"},
	}
}

func TestRepairsRun_ReadsNeedTheFixersPermissions(t *testing.T) {
	_, _, mem, r := repairsHarness(true)
	mem.runs["run1"] = repairsRun("run1", "needs.users", state.V3Succeeded, "someone-else")
	mem.runs["plain"] = database.RunRecord{ID: "plain", DefID: "library.scan", State: state.V3Succeeded}
	admin := caller{perms: auth.PermLibraryView + "," + auth.PermUsersManage, user: "u-adm", method: string(auth.MethodSession)}

	for _, path := range []string{"/api/v3/ops/runs/run1", "/api/v3/ops/runs/run1/rows"} {
		if w := do(t, r, viewer, http.MethodGet, path, nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), refusesRepairsRun) {
			t.Errorf("viewer %s = %d %s, want the Repairs 403", path, w.Code, w.Body.String())
		}
		if w := do(t, r, admin, http.MethodGet, path, nil); w.Code != http.StatusOK {
			t.Errorf("fixer-permitted caller %s = %d %s, want 200", path, w.Code, w.Body.String())
		}
	}
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/plain", nil); w.Code != http.StatusOK {
		t.Errorf("viewer reading a plain run = %d, want 200", w.Code)
	}
}

// A handler built without the check refuses every repairs run: a route the
// server forgot to wire cannot leak.
func TestRepairsRun_ReadsFailClosedWithoutTheCheck(t *testing.T) {
	_, _, mem, r := repairsHarness(false)
	mem.runs["run1"] = repairsRun("run1", "needs.users", state.V3Succeeded, "x")
	mem.runs["plain"] = database.RunRecord{ID: "plain", DefID: "library.scan", State: state.V3Succeeded}
	admin := caller{perms: auth.PermLibraryView + "," + auth.PermUsersManage, user: "u-adm", method: string(auth.MethodSession)}
	if w := do(t, r, admin, http.MethodGet, "/api/v3/ops/runs/run1", nil); w.Code != http.StatusForbidden {
		t.Fatalf("repairs run with no check wired = %d %s, want 403", w.Code, w.Body.String())
	}
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/plain", nil); w.Code != http.StatusOK {
		t.Fatalf("plain run with no check wired = %d, want 200", w.Code)
	}
}

func TestRepairsRun_CancelNeedsToReadTheRunUnlessStartedByTheCaller(t *testing.T) {
	enq, _, mem, r := repairsHarness(true)
	mem.runs["run1"] = repairsRun("run1", "needs.users", state.V3Running, "u-starter")
	// Holds everything mayCancel asks for (scan.trigger and the definition's
	// permission), not the fixer's.
	scanner := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger + "," + auth.PermLibraryEditMetadata, user: "u-scan", method: string(auth.MethodSession)}
	if w := do(t, r, scanner, http.MethodPost, "/api/v3/ops/runs/run1/cancel", nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), refusesRepairsRun) {
		t.Fatalf("cancel by a caller who may not read the run = %d %s, want the Repairs 403", w.Code, w.Body.String())
	}
	if len(enq.cancelled) != 0 {
		t.Fatalf("the run was cancelled: %v", enq.cancelled)
	}
	// The user who started it may stop it, as before.
	starter := caller{perms: auth.PermLibraryView, user: "u-starter", method: string(auth.MethodSession)}
	if w := do(t, r, starter, http.MethodPost, "/api/v3/ops/runs/run1/cancel", nil); w.Code != http.StatusAccepted {
		t.Fatalf("cancel by the starter = %d %s, want 202", w.Code, w.Body.String())
	}
	// A caller who may read it and has scan.trigger may too.
	mem.runs["run2"] = repairsRun("run2", "needs.users", state.V3Running, "u-starter")
	mgr := caller{perms: auth.PermLibraryView + "," + auth.PermScanTrigger + "," + auth.PermLibraryEditMetadata + "," + auth.PermUsersManage, user: "u-mgr", method: string(auth.MethodSession)}
	if w := do(t, r, mgr, http.MethodPost, "/api/v3/ops/runs/run2/cancel", nil); w.Code != http.StatusAccepted {
		t.Fatalf("cancel by a caller who may read it = %d %s, want 202", w.Code, w.Body.String())
	}
}

func TestRepairsRun_ApproveNeedsToReadTheRun(t *testing.T) {
	_, appr, mem, r := repairsHarness(true)
	mem.runs["run1"] = repairsRun("run1", "needs.users", state.V3AwaitingDecision, "x")
	// Holds the definition's approver permission, not the fixer's.
	approver := caller{perms: auth.PermLibraryView + "," + auth.PermSettingsManage, user: "u-appr", method: string(auth.MethodSession)}
	if w := do(t, r, approver, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), refusesRepairsRun) {
		t.Fatalf("approve by a caller who may not read the run = %d %s, want the Repairs 403", w.Code, w.Body.String())
	}
	if len(appr.calls) != 0 {
		t.Fatalf("an approval reached the approver: %+v", appr.calls)
	}
	both := caller{perms: approver.perms + "," + auth.PermUsersManage, user: "u-both", method: string(auth.MethodSession)}
	if w := do(t, r, both, http.MethodPost, "/api/v3/ops/runs/run1/approve", `{}`); w.Code != http.StatusAccepted {
		t.Fatalf("approve by a caller who may read it = %d %s, want 202", w.Code, w.Body.String())
	}
}

func TestRepairsRun_TimelineLeavesOutUnreadableRuns(t *testing.T) {
	_, _, mem, r := repairsHarness(true)
	mem.done = []database.RunRecord{
		repairsRun("run1", "needs.users", state.V3Succeeded, "x"),
		{ID: "plain", DefID: "library.scan", State: state.V3Succeeded},
	}
	mem.doneCmplete = true
	w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/timeline", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "run1") || !strings.Contains(w.Body.String(), "plain") {
		t.Fatalf("viewer's timeline = %d %s, want the plain run only", w.Code, w.Body.String())
	}
	admin := caller{perms: auth.PermLibraryView + "," + auth.PermUsersManage, user: "u-adm", method: string(auth.MethodSession)}
	if w := do(t, r, admin, http.MethodGet, "/api/v3/ops/timeline", nil); !strings.Contains(w.Body.String(), "run1") {
		t.Fatalf("permitted caller's timeline = %d %s, want run1 too", w.Code, w.Body.String())
	}
}
