// file: internal/server/handlers/opsv3/access_test.go
// version: 1.1.0
// guid: 91c4e7a2-5d38-4b06-a2f1-6e0b8d3c7a95
// last-edited: 2026-10-25

package opsv3

import (
	"errors"
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

// noOpRows is the op lookup of a store with no operations_v2 rows: these tests
// read runs, not change-journal rows, so the lookup is never asked.
type noOpRows struct{}

func (noOpRows) GetOperationV2(string) (*database.OperationV2Row, error) { return nil, nil }

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
		d.RepairsAccess = opaccess.NewRepairs(fixerSet{"needs.users": usersManageFixer{}}, noOpRows{}, holdsHeader)
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

// keepRuns is a RunReader whose listing applies RunQuery.SkipID, Keep and
// OnUndecodable to a fixed list the way the store does (a skipped id is never
// read, Total counts what Keep accepts, the page holds the first Limit of them, a
// corrupt id fails the listing unless the query has a handler for it) and
// remembers each query it was asked.
type keepRuns struct {
	*memRuns
	recs    []database.RunRecord
	corrupt map[string]bool
	calls   []database.RunQuery
	read    []string // ids whose record the fake "loaded", in order
}

func (k *keepRuns) ListRunsV3(q database.RunQuery) (database.RunPage, error) {
	k.calls = append(k.calls, q)
	var page database.RunPage
	for i := range k.recs {
		r := k.recs[i]
		if q.DefID != "" && r.DefID != q.DefID {
			continue
		}
		if q.SkipID != nil && q.SkipID(r.ID) {
			continue
		}
		k.read = append(k.read, r.ID)
		if k.corrupt[r.ID] {
			if q.OnUndecodable == nil {
				return database.RunPage{}, errors.New("opv3: run record " + r.ID + " does not decode")
			}
			q.OnUndecodable(r.ID, errors.New("does not decode"))
			continue
		}
		if q.Keep != nil && !q.Keep(&r) {
			continue
		}
		page.Total++
		if len(page.Runs) < q.Limit {
			page.Runs = append(page.Runs, r)
		}
	}
	return page, nil
}

// A run listing censuses the Repairs definitions' indexes for the runs the caller
// may not read, then asks the store for the page with those ids in SkipID (their
// keys are skipped before a record is read) and a Keep that judges every record
// it does read. The page and total the store returns are the caller's own (no
// refill), a viewer gets the plain run and not a hidden one even though the hidden
// ones come first, and a def filter that names no Repairs definition sets neither
// so the store can count keys alone.
func TestRepairsRun_ListSkipsUnreadableRunsByIDAndKeepsJudgingRecords(t *testing.T) {
	kr := &keepRuns{
		memRuns: &memRuns{runs: map[string]database.RunRecord{}},
		recs: []database.RunRecord{
			repairsRun("hidden-1", "needs.users", state.V3Running, "x"),
			repairsRun("hidden-2", "needs.users", state.V3Running, "x"),
			{ID: "plain", DefID: "library.scan", State: state.V3Succeeded},
		},
	}
	r := newRouter(New(Deps{
		Runs:          kr,
		RepairsAccess: opaccess.NewRepairs(fixerSet{"needs.users": usersManageFixer{}}, noOpRows{}, holdsHeader),
	}))

	w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs?limit=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "hidden-") || !strings.Contains(body, `"plain"`) || !strings.Contains(body, `"total":1`) {
		t.Fatalf("viewer's list = %s, want the plain run alone and total 1", body)
	}
	// One census pass per Repairs definition, then the list itself.
	if len(kr.calls) != len(repairsDefs)+1 {
		t.Fatalf("a viewer's list made %d store calls, want %d (a census per Repairs definition and the list)", len(kr.calls), len(repairsDefs)+1)
	}
	list := kr.calls[len(kr.calls)-1]
	if list.Keep == nil || list.SkipID == nil || list.OnUndecodable == nil {
		t.Fatalf("the list query lacks Keep/SkipID/OnUndecodable: %+v", list)
	}
	if !list.SkipID("hidden-1") || !list.SkipID("hidden-2") || list.SkipID("plain") {
		t.Errorf("SkipID must name exactly the runs the viewer may not read")
	}

	kr.calls = nil
	admin := caller{perms: auth.PermLibraryView + "," + auth.PermUsersManage, user: "u-adm", method: string(auth.MethodSession)}
	w = do(t, r, admin, http.MethodGet, "/api/v3/ops/runs?limit=1", nil)
	if !strings.Contains(w.Body.String(), `"total":3`) || kr.calls[len(kr.calls)-1].SkipID("hidden-1") {
		t.Fatalf("permitted caller's list = %s, want total 3 and nothing skipped", w.Body.String())
	}

	kr.calls = nil
	w = do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs?def_id=library.scan", nil)
	if w.Code != http.StatusOK || len(kr.calls) != 1 || kr.calls[0].Keep != nil || kr.calls[0].SkipID != nil {
		t.Fatalf("a def filter naming no repairs definition = %d after %d store calls, want one with no Keep or SkipID", w.Code, len(kr.calls))
	}
	kr.calls = nil
	w = do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs?def_id="+repairs.ApplyOpID, nil)
	if w.Code != http.StatusOK || len(kr.calls) != 2 || kr.calls[1].Keep == nil || strings.Contains(w.Body.String(), "hidden-") {
		t.Fatalf("a def filter naming a repairs definition = %d %s after %d store calls, want a census and a list", w.Code, w.Body.String(), len(kr.calls))
	}
}

// A corrupt record of a run the caller may not read must not fail the list the
// caller is allowed to see: the census hides it (its fixer is unknown), the list
// skips its key and never reads the record. A corrupt record of a run that was
// not in the census is skipped and logged instead of a 500. Without the
// RunQuery.SkipID/OnUndecodable wiring both were a 500 for every caller.
func TestRepairsRun_ListSurvivesACorruptRecord(t *testing.T) {
	kr := &keepRuns{
		memRuns: &memRuns{runs: map[string]database.RunRecord{}},
		recs: []database.RunRecord{
			repairsRun("hidden-corrupt", "needs.users", state.V3Running, "x"),
			{ID: "plain-1", DefID: "library.scan", State: state.V3Succeeded},
			{ID: "plain-corrupt", DefID: "library.scan", State: state.V3Succeeded},
			{ID: "plain-2", DefID: "library.scan", State: state.V3Succeeded},
		},
		corrupt: map[string]bool{"hidden-corrupt": true, "plain-corrupt": true},
	}
	r := newRouter(New(Deps{
		Runs:          kr,
		RepairsAccess: opaccess.NewRepairs(fixerSet{"needs.users": usersManageFixer{}}, noOpRows{}, holdsHeader),
	}))
	w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"plain-1"`) || !strings.Contains(body, `"plain-2"`) ||
		strings.Contains(body, "corrupt") || !strings.Contains(body, `"total":2`) {
		t.Fatalf("list with corrupt records = %d %s, want the two readable runs and total 2", w.Code, body)
	}
	list := kr.calls[len(kr.calls)-1]
	if !list.SkipID("hidden-corrupt") {
		t.Errorf("the corrupt repairs record is not in the hidden set")
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
