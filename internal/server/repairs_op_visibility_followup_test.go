// file: internal/server/repairs_op_visibility_followup_test.go
// version: 1.0.0
// guid: 3e8a51c7-b240-4d96-9f1a-7c05d2e8b463
// last-edited: 2026-10-24

// The operations v3 routes (/api/v3/ops) and the activity log as the server
// wires them, driven through the real router: a Repairs run (repairs.plan,
// repairs.apply) and everything the activity log recorded about it is readable
// only by a caller who holds the permissions of the run's fixer. Other runs and
// their activity are unchanged for a viewer. Same fixture as
// repairs_op_access_wiring_test.go.

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// v3Page is the part of GET /api/v3/ops/runs a test reads.
type v3Page struct {
	Items []struct {
		ID    string `json:"id"`
		DefID string `json:"def_id"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
	Total      int    `json:"total"`
}

func (p v3Page) ids() []string {
	out := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

func decodeAs[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var v T
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v), w.Body.String())
	return v
}

// whoMayRead is what each seeded op's fixer allows, per role. The op that
// states no fixer is readable by no one, and the unknown-fixer op needs the
// fallback set (admins only).
func (f *repairsOpAccessFixture) readable(who string) []string {
	switch who {
	case "viewer":
		return []string{f.plainScan}
	case "editor":
		return []string{f.defaultApply, f.plainScan}
	default: // admin
		return []string{f.scopedPlan, f.defaultApply, f.unknownFixer, f.plainScan}
	}
}

func (f *repairsOpAccessFixture) token(who string) string {
	switch who {
	case "viewer":
		return f.viewer
	case "editor":
		return f.editor
	}
	return f.admin
}

var allRoles = []string{"viewer", "editor", "admin"}

// TestOpsV3_RunListLeavesOutWhatTheCallerMayNotRead: GET /api/v3/ops/runs lists
// the repairs runs the caller may read and no others, its total counts only
// those, and a page boundary never lands on (or leaks the id of) a hidden run.
// Not parallel: it swaps the process-wide config.
func TestOpsV3_RunListLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, who := range allRoles {
		want := f.readable(who)
		page := decodeAs[v3Page](t, getAs(f.srv, f.token(who), "/api/v3/ops/runs?limit=100"))
		require.ElementsMatch(t, want, page.ids(), who)
		require.Equal(t, len(want), page.Total, "%s: total counts hidden runs", who)

		// One run per page: every page holds one readable run (a hidden run
		// never makes a short or empty page), and the walk meets each once.
		var walked []string
		cursor := ""
		for range 10 {
			path := "/api/v3/ops/runs?limit=1"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			w := getAs(f.srv, f.token(who), path)
			p := decodeAs[v3Page](t, w)
			require.Equal(t, len(want), p.Total, "%s: total moved while paging", who)
			require.LessOrEqual(t, len(p.Items), 1, who)
			walked = append(walked, p.ids()...)
			if p.NextCursor == "" {
				break
			}
			require.Len(t, p.Items, 1, "%s: a non-final page is empty or short: %s", who, w.Body.String())
			cursor = p.NextCursor
		}
		require.ElementsMatch(t, want, walked, "%s: paged walk", who)
	}

	// A filter on the repairs definitions finds nothing for a viewer, and not
	// a total that admits the runs exist.
	p := decodeAs[v3Page](t, getAs(f.srv, f.viewer, "/api/v3/ops/runs?def_id="+repairs.ApplyOpID))
	require.Empty(t, p.Items)
	require.Zero(t, p.Total)
}

// TestOpsV3_CountsCoverOnlyWhatTheCallerMayRead: the census a caller gets
// matches the list it links to, for every grouping: no group reveals a hidden
// run by its count, a group that only hidden runs fill is not there, and the
// grand total equals the runs total.
// Not parallel: it swaps the process-wide config.
func TestOpsV3_CountsCoverOnlyWhatTheCallerMayRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	type counts struct {
		Groups []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
			Href  string `json:"href"`
		} `json:"groups"`
		Total int `json:"total"`
	}
	for _, who := range allRoles {
		want := f.readable(who)
		for _, by := range []string{"state", "def", "outcome"} {
			c := decodeAs[counts](t, getAs(f.srv, f.token(who), "/api/v3/ops/counts?group_by="+by))
			sum := 0
			for _, g := range c.Groups {
				sum += g.Count
				list := decodeAs[v3Page](t, getAs(f.srv, f.token(who), g.Href))
				require.Equal(t, g.Count, list.Total, "%s %s %s: count differs from its list", who, by, g.Key)
				require.Len(t, list.Items, g.Count, "%s %s %s", who, by, g.Key)
			}
			require.Equal(t, len(want), sum, "%s %s: groups sum", who, by)
			if by == "state" {
				require.Equal(t, len(want), c.Total, "%s: total", who)
			}
			if who == "viewer" && by == "def" {
				require.Len(t, c.Groups, 1, "the viewer's def census names only the plain def")
				require.Equal(t, "library.scan", c.Groups[0].Key)
			}
		}
	}
}

// TestOpsV3_RunReadsNeedTheFixersPermissions: a run and its rows are a 403 for
// a caller who may not read the run (nothing of it in the refusal), a 200 for
// one who may, and a plain run is a 200 for a viewer.
// Not parallel: it swaps the process-wide config.
func TestOpsV3_RunReadsNeedTheFixersPermissions(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, who := range allRoles {
		may := f.readable(who)
		for _, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
			for _, path := range []string{"/api/v3/ops/runs/" + id, "/api/v3/ops/runs/" + id + "/rows"} {
				w := getAs(f.srv, f.token(who), path)
				if slices.Contains(may, id) {
					require.Equal(t, http.StatusOK, w.Code, "%s %s: %s", who, path, w.Body.String())
					continue
				}
				require.Equal(t, http.StatusForbidden, w.Code, "%s %s: %s", who, path, w.Body.String())
				require.NotContains(t, w.Body.String(), secondaryUserID, "%s %s", who, path)
			}
		}
	}
}

// TestOpsV3_TimelineAndDefsLeaveOutWhatTheCallerMayNotRead: the timeline lists
// only readable runs, and a definition's last_run is null for a caller who may
// not read the run it would name (repairs.plan and repairs.apply each have one
// def for every fixer).
// Not parallel: it swaps the process-wide config.
func TestOpsV3_TimelineAndDefsLeaveOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, who := range allRoles {
		tl := decodeAs[v3Page](t, getAs(f.srv, f.token(who), "/api/v3/ops/timeline?limit=100"))
		require.ElementsMatch(t, f.readable(who), tl.ids(), "%s: timeline", who)
	}

	type defs struct {
		Items []struct {
			ID      string          `json:"id"`
			LastRun json.RawMessage `json:"last_run"`
		} `json:"items"`
	}
	lastRunOf := func(who, def string) string {
		d := decodeAs[defs](t, getAs(f.srv, f.token(who), "/api/v3/ops/defs"))
		for _, it := range d.Items {
			if it.ID == def {
				return string(it.LastRun)
			}
		}
		t.Fatalf("def %s not listed", def)
		return ""
	}
	// repairs.plan's only run is the scoped one: only an admin may see it as the
	// last run. repairs.apply's newest readable run depends on the role, but for a
	// viewer there is none.
	require.Equal(t, "null", lastRunOf("viewer", repairs.PlanOpID))
	require.Equal(t, "null", lastRunOf("editor", repairs.PlanOpID))
	require.NotEqual(t, "null", lastRunOf("admin", repairs.PlanOpID))
	require.Equal(t, "null", lastRunOf("viewer", repairs.ApplyOpID))
	// The def names one run, its newest; a caller who may not read that run gets
	// null, not an older run they may read.
}

// TestOpsV3_CancelNeedsToReadTheRun: a caller who may not read a Repairs run is
// refused cancel with the Repairs refusal before anything about the run's state
// is said; a caller who may reads past that check (the run is finished, so they
// meet a different answer). Approve's check is in opsv3/access_test.go: the
// router test sessions are not a verified approval method.
// Not parallel: it swaps the process-wide config.
func TestOpsV3_CancelNeedsToReadTheRun(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	post := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.srv.router.ServeHTTP(w, req)
		return w
	}
	const refusal = "Repairs fixer you may not use"
	for _, action := range []string{"cancel"} {
		for _, who := range []string{"viewer", "editor"} {
			w := post(f.token(who), "/api/v3/ops/runs/"+f.scopedPlan+"/"+action)
			require.Equal(t, http.StatusForbidden, w.Code, "%s %s: %s", who, action, w.Body.String())
			require.Contains(t, w.Body.String(), refusal, "%s %s", who, action)
		}
		w := post(f.admin, "/api/v3/ops/runs/"+f.scopedPlan+"/"+action)
		require.NotContains(t, w.Body.String(), refusal, "admin %s: %s", action, w.Body.String())
	}
}

// TestWireHandlers_OpsV3RepairsAccessIsInstalled: the v3 handler was given the
// check. Without it every repairs run is refused (fail closed), so an admin
// reading a stated-fixer run through each v3 surface is the oracle that the
// wiring passes the shared instance in.
// Not parallel: it swaps the process-wide config.
func TestWireHandlers_OpsV3RepairsAccessIsInstalled(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, path := range []string{
		"/api/v3/ops/runs/" + f.scopedPlan,
		"/api/v3/ops/runs/" + f.scopedPlan + "/rows",
	} {
		w := getAs(f.srv, f.admin, path)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
	}
	for _, path := range []string{"/api/v3/ops/runs?limit=100", "/api/v3/ops/timeline?limit=100"} {
		p := decodeAs[v3Page](t, getAs(f.srv, f.admin, path))
		require.Contains(t, p.ids(), f.scopedPlan, path)
	}
	// The same instance the Repairs routes and the generic handlers use.
	require.Same(t, f.srv.repairsOpAccess(), f.srv.repairsOpAccess())
}

// v3EventsSeenBy opens /api/v3/ops/events as token, announces every seeded run,
// and returns the stream once it has provably carried everything the caller may
// see. The broker re-reads the dirty runs in one flush and publishes them in
// that flush, in order; announcing the plain run a second time afterwards puts a
// second copy of it behind every message of the first flush on the same
// client queue, so seeing the second copy means the first flush was written.
func v3EventsSeenBy(t *testing.T, f *repairsOpAccessFixture, token string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v3/ops/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := &syncRecorder{hdr: http.Header{}}
	done := make(chan struct{})
	go func() { defer close(done); f.srv.router.ServeHTTP(rec, req) }()
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), ": heartbeat") },
		5*time.Second, 5*time.Millisecond, "the stream never opened: %s", rec.body())

	announce := func(ids ...string) {
		for _, id := range ids {
			require.NoError(t, f.srv.opHub.Publish(ctx, "op.updated", map[string]any{"op_id": id}))
		}
	}
	announce(append(slices.Clone(f.allRepairsOps), f.plainScan)...)
	plain := `"id":"` + f.plainScan + `"`
	require.Eventually(t, func() bool { return strings.Contains(rec.body(), plain) },
		5*time.Second, 5*time.Millisecond, "the first flush never arrived: %s", rec.body())
	announce(f.plainScan)
	require.Eventually(t, func() bool { return strings.Count(rec.body(), plain) >= 2 },
		5*time.Second, 5*time.Millisecond, "the second flush never arrived: %s", rec.body())
	cancel()
	<-done
	return rec.body()
}

// TestOpsV3_EventStreamLeavesOutWhatTheCallerMayNotRead: a viewer's v3 event
// stream carries the plain run's events and none of a repairs run's, an editor's
// carries its default-fixer run's but not the settings.manage one's, an admin's
// carries every run whose fixer is stated.
// Not parallel: it swaps the process-wide config.
func TestOpsV3_EventStreamLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	for _, who := range allRoles {
		body := v3EventsSeenBy(t, f, f.token(who))
		for _, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
			if slices.Contains(f.readable(who), id) {
				require.Contains(t, body, id, "%s: stream misses a readable run", who)
			} else {
				require.NotContains(t, body, id, "%s: stream names a run the caller may not read", who)
			}
		}
		require.NotContains(t, body, secondaryUserID, who)
	}
}

// ---- activity log ----

// recordActivityFor writes what an operation leaves in the activity log: the
// reporter's row (own OperationID plus def: tag) and the slog "system" row (the
// engine's own log line: OperationID, no def: tag), both naming secondaryUserID
// for a repairs op. Returns the summaries written.
func recordActivityFor(t *testing.T, f *repairsOpAccessFixture, base time.Time, id, def string) (reporter, system string) {
	t.Helper()
	svc := f.srv.activityService
	require.NotNil(t, svc)
	reporter, system = "reporter:"+id, "system:"+id
	if id != f.plainScan {
		reporter += " " + secondaryUserID
		system += " " + secondaryUserID
	}
	require.NoError(t, svc.Record(database.ActivityEntry{
		Timestamp: base, Tier: "change", Type: def, Level: "info", Source: "operation",
		OperationID: id, Summary: reporter, Tags: []string{"def:" + def},
	}))
	require.NoError(t, svc.Record(database.ActivityEntry{
		Timestamp: base.Add(time.Second), Tier: "debug", Type: "system", Level: "info", Source: "slog",
		OperationID: id, Summary: system,
	}))
	return reporter, system
}

type activityFixture struct {
	*repairsOpAccessFixture
	summaries map[string][]string // op id -> the summaries recorded for it
	noOp      string
	digest    string
}

func newActivityFixture(t *testing.T) *activityFixture {
	t.Helper()
	f := &activityFixture{repairsOpAccessFixture: newRepairsOpAccessFixture(t), summaries: map[string][]string{}}
	base := time.Now().UTC().Add(-time.Hour)
	for i, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
		def := repairs.ApplyOpID
		switch id {
		case f.scopedPlan:
			def = repairs.PlanOpID
		case f.plainScan:
			def = "library.scan"
		}
		r, s := recordActivityFor(t, f.repairsOpAccessFixture, base.Add(time.Duration(i)*time.Minute), id, def)
		f.summaries[id] = []string{r, s}
	}
	f.noOp = "no operation: housekeeping"
	require.NoError(t, f.srv.activityService.Record(database.ActivityEntry{
		Timestamp: base.Add(30 * time.Minute), Tier: "change", Type: "system", Level: "info", Source: "x", Summary: f.noOp,
	}))
	// A digest names several operations by tag; one the caller may not read
	// takes the whole row.
	f.digest = "digest of many runs"
	require.NoError(t, f.srv.activityService.Record(database.ActivityEntry{
		Timestamp: base.Add(31 * time.Minute), Tier: "change", Type: "digest", Level: "info", Source: "summarize",
		Summary: f.digest, Tags: []string{"op:" + f.plainScan, "op:" + f.scopedPlan},
	}))
	return f
}

// visibleSummaries is what the caller may see in the log: the rows of every
// readable op, the op-less row, and the digest only for who may read all it names.
func (f *activityFixture) visibleSummaries(who string) []string {
	out := []string{f.noOp}
	for _, id := range f.readable(who) {
		out = append(out, f.summaries[id]...)
	}
	if slices.Contains(f.readable(who), f.scopedPlan) {
		out = append(out, f.digest)
	}
	return out
}

// requireNamesNoHiddenOp fails if body names the id of a repairs op the caller
// may not read (every row of an op carries its id, so this also covers the
// summary and the tags).
func (f *activityFixture) requireNamesNoHiddenOp(t *testing.T, who, body string) {
	t.Helper()
	for _, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
		if !slices.Contains(f.readable(who), id) {
			require.NotContains(t, body, id, "%s: names an op the caller may not read", who)
		}
	}
}

type activityPage struct {
	Entries []struct {
		Summary string `json:"summary"`
	} `json:"entries"`
	Total int `json:"total"`
}

func (p activityPage) summaries() []string {
	out := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		out = append(out, e.Summary)
	}
	return out
}

// TestActivity_ListLeavesOutWhatTheCallerMayNotRead: GET /activity carries no
// row of a repairs op the caller may not read, whichever shape the row has
// (reporter row with a def tag, engine log line with none, digest naming it),
// its total counts only what is shown, and paging walks exactly those rows.
// Not parallel: it swaps the process-wide config.
func TestActivity_ListLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newActivityFixture(t)
	for _, who := range allRoles {
		want := f.visibleSummaries(who)
		w := getAs(f.srv, f.token(who), "/api/v1/activity?limit=200")
		page := decodeAs[struct {
			Data activityPage `json:"data"`
		}](t, w).Data
		require.ElementsMatch(t, want, page.summaries(), who)
		require.Equal(t, len(want), page.Total, "%s: total counts hidden rows", who)
		f.requireNamesNoHiddenOp(t, who, w.Body.String())

		var walked []string
		for off := 0; off < len(want); off += 2 {
			w := getAs(f.srv, f.token(who), "/api/v1/activity?limit=2&offset="+strconv.Itoa(off))
			p := decodeAs[struct {
				Data activityPage `json:"data"`
			}](t, w).Data
			walked = append(walked, p.summaries()...)
		}
		require.ElementsMatch(t, want, walked, "%s: paged walk", who)
	}
}

// TestActivity_OperationReadsNeedTheFixersPermissions: GET
// /operations/:id/activity is a 403 for a repairs op the caller may not read
// (nothing of its rows in the refusal), the rows for one they may, and a plain
// op's rows for a viewer. The merged read leaves unreadable members out, names
// only the readable ones in operation_ids, and is a 403 when none is readable.
// Not parallel: it swaps the process-wide config.
func TestActivity_OperationReadsNeedTheFixersPermissions(t *testing.T) {
	f := newActivityFixture(t)
	for _, who := range allRoles {
		may := f.readable(who)
		for _, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
			w := getAs(f.srv, f.token(who), "/api/v1/operations/"+id+"/activity")
			if slices.Contains(may, id) {
				require.Equal(t, http.StatusOK, w.Code, "%s %s: %s", who, id, w.Body.String())
				for _, s := range f.summaries[id] {
					require.Contains(t, w.Body.String(), s, "%s %s", who, id)
				}
				continue
			}
			require.Equal(t, http.StatusForbidden, w.Code, "%s %s: %s", who, id, w.Body.String())
			require.NotContains(t, w.Body.String(), secondaryUserID, "%s %s", who, id)
		}
	}

	merged := func(who string, ids ...string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"ids": ids})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/activity/merged", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+f.token(who))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.srv.router.ServeHTTP(w, req)
		return w
	}
	all := append(slices.Clone(f.allRepairsOps), f.plainScan)
	for _, who := range allRoles {
		w := merged(who, all...)
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", who, w.Body.String())
		for _, id := range all {
			if slices.Contains(f.readable(who), id) {
				require.Contains(t, w.Body.String(), id, "%s: merged read drops a readable member", who)
				continue
			}
			require.NotContains(t, w.Body.String(), id, "%s: merged read names a member the caller may not read", who)
		}
		f.requireNamesNoHiddenOp(t, who, w.Body.String())
	}
	w := merged("viewer", f.scopedPlan, f.defaultApply)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), secondaryUserID)
}

// TestWireHandlers_ActivityRepairsAccessIsInstalled: the activity handler was
// given the check and the store to resolve operations with. Without them the
// activity of a repairs op is open to a viewer, so a viewer being refused a
// repairs op's activity (and an admin reading it) is the oracle that the
// server's wiring passes both in.
// Not parallel: it swaps the process-wide config.
func TestWireHandlers_ActivityRepairsAccessIsInstalled(t *testing.T) {
	f := newActivityFixture(t)
	w := getAs(f.srv, f.viewer, "/api/v1/operations/"+f.scopedPlan+"/activity")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	w = getAs(f.srv, f.admin, "/api/v1/operations/"+f.scopedPlan+"/activity")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), f.summaries[f.scopedPlan][0])
	w = getAs(f.srv, f.viewer, "/api/v1/activity?limit=200")
	require.NotContains(t, w.Body.String(), f.summaries[f.scopedPlan][1])
}
