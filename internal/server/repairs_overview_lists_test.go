// file: internal/server/repairs_overview_lists_test.go
// version: 1.0.0
// guid: 8b4e2d71-5c93-4a60-b7f2-e19d3a6c0845
// last-edited: 2026-10-30

// The operations overview lists as the server wires them, driven through the
// real router: every GET route under /operations and /op-defs that lists
// operation rows, or reports something derived from them (a definition id), must
// leave out a Repairs operation (repairs.plan, repairs.apply) the caller may not
// read. The walk over the wired routes is the guard: a list route added without a
// driver here fails the build, so it cannot ship without the check. Same fixture
// as repairs_op_access_wiring_test.go.

package server

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// overviewKind says how a GET route under /operations or /op-defs relates to
// operation rows.
type overviewKind int

const (
	// kindList lists operation rows, or reports what is derived from them. It
	// needs a driver in overviewDrivers.
	kindList overviewKind = iota
	// kindStream is the event stream, covered by
	// TestRepairsOpData_EventStreamLeavesOutWhatTheCallerMayNotRead.
	kindStream
	// kindByID reads one operation by id, covered by
	// TestRepairsOpData_ReadRoutesNeedTheFixersPermissions (403 for a hidden run).
	kindByID
	// kindNoRows serves no operation row and nothing derived from one: the
	// registered definitions, or a fixed-purpose report.
	kindNoRows
	// kindRetired answers 410.
	kindRetired
)

// overviewRoutes classifies every GET route under /operations and /op-defs.
// TestOperationsOverviewRoutes_EveryWiredGETIsClassified fails on a wired route
// missing here, so the author of a new one has to decide which it is.
var overviewRoutes = map[string]overviewKind{
	"/api/v1/operations/timeline":               kindList,
	"/api/v1/operations/pause":                  kindList,
	"/api/v1/operations/stale":                  kindList,
	"/api/v1/operations/events":                 kindStream,
	"/api/v1/operations/v2/:id":                 kindByID,
	"/api/v1/operations/v2/:id/logs/download":   kindByID,
	"/api/v1/operations/:id/result":             kindByID,
	"/api/v1/operations/:id/results":            kindByID, // metadata-fetch candidates, not a Repairs result
	"/api/v1/operations/:id/changes":            kindByID,
	"/api/v1/operations/:id/undo/preflight":     kindByID,
	"/api/v1/operations/:id/class-lists/:class": kindByID,
	"/api/v1/operations/:id/activity":           kindByID, // handlers/activity: its own visibility tests
	"/api/v1/op-defs":                           kindNoRows,
	"/api/v1/op-defs/:id":                       kindNoRows,
	"/api/v1/operations/audit-files":            kindNoRows,
	"/api/v1/operations/reconcile/preview":      kindNoRows,
	"/api/v1/operations/reconcile/scan/latest":  kindNoRows,
	"/api/v1/operations/active":                 kindRetired,
	"/api/v1/operations/recent":                 kindRetired,
}

// overviewFixture is the access fixture plus one operation of each shape the
// overview lists read: running v2 rows (the pause lists read these) and v1 rows
// old enough to be stale.
type overviewFixture struct {
	*repairsOpAccessFixture
	runningPlain  string // running library.scan
	runningDefApp string // running repairs.apply of the default-permission fixer
	runningScoped string // running repairs.plan of the settings.manage fixer
	runningNoFix  string // running repairs.apply that states no fixer
	staleRepairs  string // stale v1 repairs.apply row of the default-permission fixer
	staleScoped   string // stale v1 repairs.plan row of the settings.manage fixer
	staleNoFixer  string // stale v1 repairs.apply row that states no fixer
	stalePlain    string // stale v1 library.scan row
}

// staleStore serves a fixed v1 operations listing: a v1 row cannot be backdated
// through the store, and a stale row is one that started long ago.
type staleStore struct {
	database.Store
	ops []database.Operation
}

func (s staleStore) GetRecentOperations(int) ([]database.Operation, error) { return s.ops, nil }

func newOverviewFixture(t *testing.T) *overviewFixture {
	t.Helper()
	base := newRepairsOpAccessFixture(t)
	f := &overviewFixture{
		repairsOpAccessFixture: base,
		runningPlain:           "op-running-plain-scan",
		runningDefApp:          "op-running-repairs-apply-default",
		runningScoped:          "op-running-repairs-plan-scoped",
		runningNoFix:           "op-running-repairs-apply-nofixer",
		staleRepairs:           "op-stale-repairs-apply-default",
		staleScoped:            "op-stale-repairs-plan-scoped",
		staleNoFixer:           "op-stale-repairs-apply-nofixer",
		stalePlain:             "op-stale-plain-scan",
	}
	store, ok := base.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	now := time.Now().UTC()
	running := func(id, def, params string) {
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "running", Params: params, QueuedAt: now,
		}))
	}
	running(f.runningPlain, "library.scan", `{}`)
	running(f.runningDefApp, repairs.ApplyOpID, `{"fixer_id":"`+base.defaultFixerID+`"}`)
	running(f.runningScoped, repairs.PlanOpID, `{"fixer_id":"`+scopedFixerID+`"}`)
	running(f.runningNoFix, repairs.ApplyOpID, `{}`)

	long := now.Add(-3 * time.Hour)
	stale := func(id, typ, result string) database.Operation {
		op := database.Operation{ID: id, Type: typ, Status: "running", CreatedAt: long, StartedAt: &long}
		if result != "" {
			op.ResultData = &result
		}
		return op
	}
	orig := base.srv.store
	base.srv.store = staleStore{Store: orig, ops: []database.Operation{
		stale(f.staleRepairs, repairs.ApplyOpID, `{"fixer_id":"`+base.defaultFixerID+`"}`),
		stale(f.staleScoped, repairs.PlanOpID, `{"fixer_id":"`+scopedFixerID+`"}`),
		stale(f.staleNoFixer, repairs.ApplyOpID, ``),
		stale(f.stalePlain, "library.scan", ``),
	}}
	t.Cleanup(func() { base.srv.store = orig })
	return f
}

// overviewDriver drives one list route: path is what to GET, hidden are the
// markers a caller who may read no Repairs operation must not find in the body,
// and visible the markers it must still find (so a filter that empties the
// answer cannot pass).
type overviewDriver struct {
	path    string
	hidden  func(f *overviewFixture) []string
	visible func(f *overviewFixture) []string
	// editorSees and editorHides are the markers a caller holding the default
	// fixer's permission finds and does not; adminSees and adminHides those of one
	// holding every permission.
	editorSees  func(f *overviewFixture) []string
	editorHides func(f *overviewFixture) []string
	adminSees   func(f *overviewFixture) []string
	adminHides  func(f *overviewFixture) []string
}

var overviewDrivers = map[string]overviewDriver{
	"/api/v1/operations/timeline": {
		path: "/api/v1/operations/timeline?since=24h&limit=200",
		hidden: func(f *overviewFixture) []string {
			return append(slices.Clone(f.allRepairsOps), f.runningDefApp, f.runningScoped, f.runningNoFix)
		},
		visible:    func(f *overviewFixture) []string { return []string{f.plainScan, f.runningPlain} },
		editorSees: func(f *overviewFixture) []string { return []string{f.defaultApply, f.runningDefApp} },
		editorHides: func(f *overviewFixture) []string {
			return []string{f.scopedPlan, f.runningScoped, f.noFixerApply, f.runningNoFix}
		},
		adminSees: func(f *overviewFixture) []string {
			return []string{f.defaultApply, f.scopedPlan, f.runningDefApp, f.runningScoped}
		},
		adminHides: func(f *overviewFixture) []string { return []string{f.noFixerApply, f.runningNoFix} },
	},
	// The pause lists name definition ids taken from running rows: the id
	// repairs.apply alone says a fixer is running. The default-permission
	// fixer's apply is running, so an editor finds repairs.apply, and the
	// settings.manage fixer's plan is running, so only the admin finds
	// repairs.plan.
	"/api/v1/operations/pause": {
		path:        "/api/v1/operations/pause",
		hidden:      func(*overviewFixture) []string { return []string{repairs.PlanOpID, repairs.ApplyOpID} },
		visible:     func(*overviewFixture) []string { return []string{"library.scan"} },
		editorSees:  func(*overviewFixture) []string { return []string{repairs.ApplyOpID} },
		editorHides: func(*overviewFixture) []string { return []string{repairs.PlanOpID} },
		adminSees:   func(*overviewFixture) []string { return []string{repairs.ApplyOpID, repairs.PlanOpID} },
	},
	"/api/v1/operations/stale": {
		path: "/api/v1/operations/stale?timeout_minutes=30",
		hidden: func(f *overviewFixture) []string {
			return []string{f.staleRepairs, f.staleScoped, f.staleNoFixer}
		},
		visible:     func(f *overviewFixture) []string { return []string{f.stalePlain} },
		editorSees:  func(f *overviewFixture) []string { return []string{f.staleRepairs} },
		editorHides: func(f *overviewFixture) []string { return []string{f.staleScoped, f.staleNoFixer} },
		adminSees:   func(f *overviewFixture) []string { return []string{f.staleRepairs, f.staleScoped} },
		adminHides:  func(f *overviewFixture) []string { return []string{f.staleNoFixer} },
	},
}

// TestOperationsOverviewRoutes_EveryWiredGETIsClassified is the guard: it walks
// the routes the server wired. A GET route under /operations or /op-defs that
// overviewRoutes does not name fails (the new route's author must say whether it
// lists operation rows), and a list route with no driver fails, so one cannot
// ship without a test that seeds a hidden Repairs run and asserts it is absent.
func TestOperationsOverviewRoutes_EveryWiredGETIsClassified(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	seen := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		if r.Method != http.MethodGet {
			continue
		}
		if !strings.HasPrefix(r.Path, "/api/v1/operations") && !strings.HasPrefix(r.Path, "/api/v1/op-defs") {
			continue
		}
		seen[r.Path] = true
		kind, ok := overviewRoutes[r.Path]
		require.True(t, ok, "GET %s is wired under the operations overview but overviewRoutes does not classify it. "+
			"If it lists operation rows (or a count or definition id derived from them), add a kindList entry, a driver in "+
			"overviewDrivers, and filter through opaccess.Reader.ReadableOps; otherwise classify it.", r.Path)
		if kind == kindList {
			_, driven := overviewDrivers[r.Path]
			require.True(t, driven, "GET %s is a list route with no driver in overviewDrivers", r.Path)
		}
	}
	for path := range overviewRoutes {
		require.True(t, seen[path], "overviewRoutes names %s, which is not wired: remove it", path)
	}
	for path := range overviewDrivers {
		require.Equal(t, kindList, overviewRoutes[path], "%s has a driver but is not classified as a list route", path)
	}
}

// TestOperationsOverviewRoutes_LeaveOutRepairsRunsTheCallerMayNotRead drives each
// list route as a viewer, an editor and an admin. The viewer finds nothing of any
// Repairs operation (not its id, not its definition id), only the plain
// operations; the editor finds the default-permission fixer's and not the
// settings.manage fixer's; the admin finds every run whose fixer is stated; and
// a run that states no fixer is hidden from everyone.
// Not parallel: it swaps the process-wide config.
func TestOperationsOverviewRoutes_LeaveOutRepairsRunsTheCallerMayNotRead(t *testing.T) {
	f := newOverviewFixture(t)
	for route, d := range overviewDrivers {
		viewer := getAs(f.srv, f.viewer, d.path)
		require.Equal(t, http.StatusOK, viewer.Code, "%s: %s", route, viewer.Body.String())
		for _, m := range d.hidden(f) {
			require.NotContains(t, viewer.Body.String(), m, "%s: the viewer's answer names a Repairs operation", route)
		}
		require.NotContains(t, viewer.Body.String(), secondaryUserID, route)
		for _, m := range d.visible(f) {
			require.Contains(t, viewer.Body.String(), m, "%s: the viewer lost an operation that is not a Repairs one", route)
		}

		editor := getAs(f.srv, f.editor, d.path)
		require.Equal(t, http.StatusOK, editor.Code, "%s: %s", route, editor.Body.String())
		for _, m := range d.editorSees(f) {
			require.Contains(t, editor.Body.String(), m, "%s: the editor lost the default-permission fixer's operation", route)
		}
		for _, m := range d.editorHides(f) {
			require.NotContains(t, editor.Body.String(), m, "%s: the editor's answer names an operation of a fixer they may not use", route)
		}

		admin := getAs(f.srv, f.admin, d.path)
		require.Equal(t, http.StatusOK, admin.Code, "%s: %s", route, admin.Body.String())
		for _, m := range d.adminSees(f) {
			require.Contains(t, admin.Body.String(), m, "%s: the admin lost an operation they may read", route)
		}
		if d.adminHides != nil {
			for _, m := range d.adminHides(f) {
				require.NotContains(t, admin.Body.String(), m, "%s: an operation that states no fixer is hidden from everyone", route)
			}
		}
	}
}
