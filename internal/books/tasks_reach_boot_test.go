// file: internal/books/tasks_reach_boot_test.go
// version: 0.1.0
// guid: e9d7f1df-3bcd-4046-be74-ed41c8fce038
// last-edited: 2026-11-28

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// The task routes' reach through the composed app's root handler, tasks flip
// off and on (F5-18). Until flip commit 5 the kernel builds core's task server
// (core/kernel/tasks.go constructTasks) but mounts none of its routes, so with
// the predicate on a caller reaches exactly what HEAD's routes give it: core's
// /api/core/v1/tasks/* answer 404 to everyone, and every HEAD route answers the
// viewer as it does with the flip off. Both tests assert the same table, so
// off and on cannot drift apart.
//
// AT COMMIT 5 THIS CELL CHANGES: the mount makes core's rows live, and the
// commit that mounts them must replace the 404 rows with the full per-route
// reach table (route reach audit 2026-11-28, R1-R4: /schedules needs
// core:tasks.manage, /counts and every run read honour the kind's View
// including retired kinds, a caller's own run is not theirs to discard).
// Deleting a row here to make the mount pass is the regression this guards.
//
// Every request comes from its own address (192.0.2.0/24, TEST-NET-1): the
// /api limiter buckets per client and class (core/internal/httpmw/ratelimit.go
// rateLimit), and a shared address turned the mutating rows into 429s that
// measured the limiter, not the route. The product limiter is not touched.

// tasksReachViewerScopes are library.view's scopes as a key carries them:
// books:view plus the core:tasks.view library.view grants.
var tasksReachViewerScopes = []string{"books:view", "core:tasks.view"}

type tasksReachCell struct {
	method, path, body string
	viewer             int
	// admin is the session admin's status; 0 leaves it unasserted (a HEAD
	// mutation on a run that does not exist answers whatever its handler's
	// lookup does, which says nothing about reach).
	admin int
}

// tasksReachTable is the reach both flip states must give. Rows cite HEAD's
// rule; the core rows are the unmounted table.
var tasksReachTable = []tasksReachCell{
	// core's task routes (core/internal/taskhttp/routes.go): unmounted.
	{"GET", "/api/core/v1/tasks/schedules", "", http.StatusNotFound, http.StatusNotFound},
	{"GET", "/api/core/v1/tasks/counts", "", http.StatusNotFound, http.StatusNotFound},
	{"GET", "/api/core/v1/tasks/timeline", "", http.StatusNotFound, http.StatusNotFound},
	{"GET", "/api/core/v1/tasks/definitions", "", http.StatusNotFound, http.StatusNotFound},
	{"GET", "/api/core/v1/tasks/runs", "", http.StatusNotFound, http.StatusNotFound},
	{"GET", "/api/core/v1/tasks/pause", "", http.StatusNotFound, http.StatusNotFound},
	{"POST", "/api/core/v1/tasks/pause", `{"reason":"Joker"}`, http.StatusNotFound, http.StatusNotFound},
	{"POST", "/api/core/v1/tasks/unpause", `{}`, http.StatusNotFound, http.StatusNotFound},
	{"PATCH", "/api/core/v1/tasks/schedules/core.tasks.prune", `{"enabled":false}`, http.StatusNotFound, http.StatusNotFound},
	{"POST", "/api/core/v1/tasks/definitions/core.tasks.prune/runs", `{}`, http.StatusNotFound, http.StatusNotFound},
	{"POST", "/api/core/v1/tasks/runs/run-gotham/cancel", `{}`, http.StatusNotFound, http.StatusNotFound},
	{"DELETE", "/api/core/v1/tasks/runs/run-gotham", "", http.StatusNotFound, http.StatusNotFound},

	// HEAD's settings.manage routes (legacy_manifest.go :858, :782, :795,
	// :797, :798, :793; wire_operations_routes.go :123).
	{"GET", "/api/v1/tasks", "", http.StatusForbidden, http.StatusOK},
	{"GET", "/api/v1/maintenance-window/status", "", http.StatusForbidden, http.StatusOK},
	{"POST", "/api/v1/operations/pause", `{}`, http.StatusForbidden, 0},
	{"POST", "/api/v1/operations/v2/run-gotham/finish", `{}`, http.StatusForbidden, 0},
	{"POST", "/api/v1/operations/v2/run-gotham/retry", `{}`, http.StatusForbidden, 0},
	{"DELETE", "/api/v1/operations/v2/run-gotham/record", "", http.StatusForbidden, 0},
	{"DELETE", "/api/v1/operations/v2/run-gotham", "", http.StatusForbidden, 0},

	// HEAD's library.view reads (legacy_manifest.go :781, :891-902).
	{"GET", "/api/v3/ops/defs", "", http.StatusOK, http.StatusOK},
	{"GET", "/api/v3/ops/counts", "", http.StatusOK, http.StatusOK},
	{"GET", "/api/v3/ops/timeline", "", http.StatusOK, http.StatusOK},
	{"GET", "/api/v3/ops/runs", "", http.StatusOK, http.StatusOK},
	{"GET", "/api/v1/operations/pause", "", http.StatusOK, http.StatusOK},
	{"GET", "/api/v1/operations/timeline", "", http.StatusOK, http.StatusOK},

	// HEAD's v3 controls behind the library.view floor; the handler decides
	// (opsv3/runs.go startedBy and mayCancel; approve needs the approver;
	// starting needs scan.trigger plus the definition's perms, :894). The
	// cancel and approve rows name a run that does not exist, so their 404
	// pins that the floor admits the viewer and the lookup answers first, the
	// same both ways; the per-run decision on a real run is the route reach
	// audit's (2026-11-28) and becomes asserted rows at commit 5.
	{"POST", "/api/v3/ops/runs/run-gotham/cancel", `{}`, http.StatusNotFound, 0},
	{"POST", "/api/v3/ops/runs/run-gotham/approve", `{}`, http.StatusNotFound, 0},
	{"POST", "/api/v3/ops/defs/db_optimize/runs", `{}`, http.StatusForbidden, 0},
}

// tasksReachRun boots the composed app with auth on and asserts the table.
// It returns the v3 run list's total for the UI-parity cell.
func tasksReachRun(t *testing.T, label string) (v3Total int) {
	t.Helper()
	r := runComposedWith(t, func(c *config.Config) { c.EnableAuth = true }, nil, nil, nil)
	admin := newCoreAdmin(t, r)
	viewer, _ := admin.mintKey("oracle-clocktower-viewer", tasksReachViewerScopes)

	var mounted []string
	for ri := range r.app.Routes() {
		if strings.HasPrefix(ri.Pattern, "/api/core/v1/tasks") {
			mounted = append(mounted, ri.Method+" "+ri.Pattern)
		}
	}
	sort.Strings(mounted)
	if len(mounted) != 0 {
		t.Errorf("%s: core's task routes are mounted before flip commit 5: %v", label, mounted)
	}

	addr := 0
	do := func(method, path, body, bearer string, withCookie bool) *httptest.ResponseRecorder {
		addr++
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.RemoteAddr = "192.0.2." + strconv.Itoa(10+addr) + ":41000"
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if withCookie {
			req.AddCookie(admin.cookie)
		}
		rec := httptest.NewRecorder()
		r.app.Handler().ServeHTTP(rec, req)
		return rec
	}
	for _, c := range tasksReachTable {
		v := do(c.method, c.path, c.body, viewer, false)
		a := do(c.method, c.path, c.body, "", true)
		t.Logf("%s cell %-6s %-55s viewer=%d admin=%d", label, c.method, c.path, v.Code, a.Code)
		if v.Code == http.StatusTooManyRequests || a.Code == http.StatusTooManyRequests {
			t.Errorf("%s %s %s: 429, the cell measured the limiter, not the route", label, c.method, c.path)
			continue
		}
		if v.Code != c.viewer {
			t.Errorf("%s %s %s: viewer %d %s; want %d", label, c.method, c.path, v.Code, credExcerpt(v), c.viewer)
		}
		if c.admin != 0 && a.Code != c.admin {
			t.Errorf("%s %s %s: admin %d %s; want %d", label, c.method, c.path, a.Code, credExcerpt(a), c.admin)
		}
		if c.path == "/api/v3/ops/runs" && v.Code == http.StatusOK {
			var out struct {
				Total int `json:"total"`
			}
			if err := json.Unmarshal(v.Body.Bytes(), &out); err != nil {
				t.Errorf("%s: v3 runs body: %v %s", label, err, credExcerpt(v))
			}
			v3Total = out.Total
		}
	}
	return v3Total
}

// TestTasksReachBoot_FlipOff: HEAD's routes, with core's task routes absent.
func TestTasksReachBoot_FlipOff(t *testing.T) {
	if parent, out := inFreshProcess(t); parent {
		logCells(t, out)
		return
	}
	if total := tasksReachRun(t, "off"); total == 0 {
		// The UI-parity cell below relies on the flip-off boot producing
		// startup runs that HEAD's v3 list shows.
		t.Errorf("off: /api/v3/ops/runs listed no runs; the UI-parity cell has no baseline")
	}
}

// TestTasksReachBoot_FlipOnMountsNothing: the predicate on, the same table.
//
// It also pins UI-PARITY ITEM UP-1 (F5-18 lead plan, "UI parity list"): with
// the flip on, HEAD's /api/v3/ops lists read HEAD's op store, which no longer
// receives runs, and core's runs are not reachable over HTTP until commit 5
// mounts the task routes, so the operations pages list nothing. The
// assertion records the gap as it stands; the change that closes UP-1 (the UI
// reading core's routes, or v3 reading core's store) must flip it.
func TestTasksReachBoot_FlipOnMountsNothing(t *testing.T) {
	if parent, out := inFreshProcess(t); parent {
		logCells(t, out)
		return
	}
	t.Cleanup(SetTasksFlipForTest(true))
	if total := tasksReachRun(t, "on"); total != 0 {
		t.Errorf("on: /api/v3/ops/runs lists %d runs; UI-parity item UP-1 says it lists none until the UI reads core's runs. If UP-1 was closed, update this assertion and the plan's UI parity list together", total)
	}
}

// logCells relays the child's cell lines to the parent's log.
func logCells(t *testing.T, out string) {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, " cell ") || strings.Contains(l, "mounted") || strings.Contains(l, "want ") {
			t.Log(strings.TrimSpace(l))
		}
	}
}
