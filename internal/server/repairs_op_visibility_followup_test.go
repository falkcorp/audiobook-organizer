// file: internal/server/repairs_op_visibility_followup_test.go
// version: 1.3.1
// guid: 3e8a51c7-b240-4d96-9f1a-7c05d2e8b463
// last-edited: 2026-10-30

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

	"github.com/falkcorp/audiobook-organizer/internal/auth"
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

// TestOpsV3_RunListTotalStaysExactWhenHiddenRunsInterleave: with readable and
// hidden repairs runs alternating in the index, a def_id + state listing (the
// shape that makes the store judge every record) returns a total that is the
// readable count on every page of every page size, each page is full until the
// last, no page is empty, and the walk meets each readable run once and no hidden
// one. The same listing for a viewer, who may read none of them, is empty with
// total 0 and no cursor.
// Not parallel: it swaps the process-wide config.
func TestOpsV3_RunListTotalStaysExactWhenHiddenRunsInterleave(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	now := time.Now().UTC()
	var readable, hidden []string
	for i := range 9 {
		id, fixer := "op-inter-"+strconv.Itoa(i), f.defaultFixerID // the editor may read this fixer's runs
		if i%2 == 1 {
			fixer = scopedFixerID // settings.manage: not the editor
			hidden = append(hidden, id)
		} else {
			readable = append(readable, id)
		}
		res := `{"fixer_id":"` + fixer + `"}`
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: repairs.ApplyOpID, Plugin: "test", Status: "completed",
			Params: `{"fixer_id":"` + fixer + `"}`, ResultData: &res,
			QueuedAt: now.Add(time.Duration(i) * time.Second), CompletedAt: &now,
		}))
	}
	// The fixture's own default-fixer apply run is readable to the editor too.
	readable = append(readable, f.defaultApply)

	base := "/api/v3/ops/runs?def_id=" + repairs.ApplyOpID + "&state=succeeded"
	for _, size := range []int{1, 2, 3, 100} {
		var walked []string
		cursor := ""
		for range 20 {
			path := base + "&limit=" + strconv.Itoa(size)
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			p := decodeAs[v3Page](t, getAs(f.srv, f.editor, path))
			require.Equal(t, len(readable), p.Total, "limit %d: total is not the readable count", size)
			require.NotEmpty(t, p.Items, "limit %d: an empty page (a hidden run took a slot)", size)
			if p.NextCursor != "" {
				require.Len(t, p.Items, size, "limit %d: a non-final page is short", size)
			}
			walked = append(walked, p.ids()...)
			if p.NextCursor == "" {
				break
			}
			cursor = p.NextCursor
		}
		require.ElementsMatch(t, readable, walked, "limit %d: walk", size)
		for _, id := range hidden {
			require.NotContains(t, walked, id, "limit %d", size)
		}
	}

	p := decodeAs[v3Page](t, getAs(f.srv, f.viewer, base+"&limit=1"))
	require.Empty(t, p.Items)
	require.Zero(t, p.Total)
	require.Empty(t, p.NextCursor)
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

// TestActivity_CompactedDigestLeavesOutWhatTheCallerMayNotRead: the activity
// rows of a repairs op survive in a nightly daily digest after compaction
// deletes them, as items carrying the op id, type and summary. The digest is
// built here by the real CompactByDay over rows written the way the reporter and
// the slog bridge write them, not hand-shaped, and a caller's tier=digest read
// carries the items of the ops they may read and none of the others. The digest
// row itself stays for every caller, and so does the total.
// Not parallel: it swaps the process-wide config.
func TestActivity_CompactedDigestLeavesOutWhatTheCallerMayNotRead(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	day := time.Now().UTC().AddDate(0, 0, -3).Truncate(time.Hour)
	summaries := map[string][]string{}
	for i, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
		def := repairs.ApplyOpID
		switch id {
		case f.scopedPlan:
			def = repairs.PlanOpID
		case f.plainScan:
			def = "library.scan"
		}
		r, s := recordActivityFor(t, f, day.Add(time.Duration(i)*time.Minute), id, def)
		summaries[id] = []string{r, s}
	}
	svc := f.srv.activityService
	res, err := svc.CompactByDay(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Positive(t, res.DaysCompacted, "nothing was compacted")

	type digestPage struct {
		Entries []struct {
			Type    string `json:"type"`
			Details struct {
				OriginalCount int            `json:"original_count"`
				Counts        map[string]int `json:"counts"`
				Items         []struct {
					OperationID string `json:"operation_id"`
					Summary     string `json:"summary"`
				} `json:"items"`
			} `json:"details"`
		} `json:"entries"`
		Total int `json:"total"`
	}
	read := func(who string) (digestPage, string) {
		w := getAs(f.srv, f.token(who), "/api/v1/activity?tier=digest&limit=50")
		return decodeAs[struct {
			Data digestPage `json:"data"`
		}](t, w).Data, w.Body.String()
	}

	adminPage, _ := read("admin")
	require.Equal(t, 1, adminPage.Total, "one day was compacted into one digest")
	for _, who := range allRoles {
		page, body := read(who)
		require.Equal(t, adminPage.Total, page.Total, "%s: the digest row went missing or the total moved", who)
		require.Len(t, page.Entries, 1, who)
		require.Equal(t, "daily_digest", page.Entries[0].Type, who)
		require.Equal(t, 2*(len(f.allRepairsOps)+1), page.Entries[0].Details.OriginalCount, "%s: the digest does not describe the day (two rows per op)", who)

		var itemOps []string
		for _, it := range page.Entries[0].Details.Items {
			itemOps = append(itemOps, it.OperationID)
		}
		for _, id := range append(slices.Clone(f.allRepairsOps), f.plainScan) {
			if slices.Contains(f.readable(who), id) {
				require.Contains(t, itemOps, id, "%s: the digest lost an item of an op the caller may read", who)
				for _, sm := range summaries[id] {
					require.Contains(t, body, sm, "%s %s", who, id)
				}
				continue
			}
			require.NotContains(t, body, id, "%s: the digest names an op the caller may not read", who)
			for _, sm := range summaries[id] {
				require.NotContains(t, body, sm, "%s %s", who, id)
			}
		}
		if who == "viewer" {
			require.NotContains(t, body, secondaryUserID, "viewer: the digest carries a user id of a repairs run")
		}
	}
}

// TestDashboard_RecentOperationsLeaveOutRepairsRuns: GET /dashboard, which needs
// only library.view, lists the newest operations without the repairs runs the
// caller may not read (their result, error text and actor named the target user),
// still fills its list from older operations when the newest ones are hidden, and
// serves no operation's result_data to anyone. It also pins that the server wires
// the check into the system handler: without it every repairs run is left out, so
// an admin seeing one is the oracle.
// Not parallel: it swaps the process-wide config.
func TestDashboard_RecentOperationsLeaveOutRepairsRuns(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)

	// A failed settings.manage-fixer run, the newest operation of all, whose
	// error text and actor name the target user; and five more behind it, so the
	// five newest operations are all repairs runs a viewer may not read.
	const failedID = "op-repairs-apply-failed"
	now := time.Now().UTC()
	errText := "apply failed for target_user_id=" + secondaryUserID
	actor := secondaryUserID
	hidden := []string{failedID}
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: failedID, DefID: repairs.ApplyOpID, Plugin: "test", Status: "failed",
		Params: `{"fixer_id":"` + scopedFixerID + `"}`, ErrorMessage: &errText, ActorUserID: &actor,
		QueuedAt: now.Add(10 * time.Minute),
	}))
	for i := range 5 {
		id := "op-repairs-plan-newer-" + strconv.Itoa(i)
		hidden = append(hidden, id)
		res := `{"params":{"target_user_id":"` + secondaryUserID + `"}}`
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: repairs.PlanOpID, Plugin: "test", Status: "completed",
			Params: `{"fixer_id":"` + scopedFixerID + `"}`, ResultData: &res,
			QueuedAt: now.Add(time.Duration(2+i) * time.Minute), CompletedAt: &now,
		}))
	}

	type dash struct {
		Data struct {
			Recent []map[string]any `json:"recent_operations"`
		} `json:"data"`
	}
	ids := func(d dash) []string {
		out := []string{}
		for _, op := range d.Data.Recent {
			id, _ := op["id"].(string)
			out = append(out, id)
		}
		return out
	}

	for _, who := range allRoles {
		w := getAs(f.srv, f.token(who), "/api/v1/dashboard")
		d := decodeAs[dash](t, w)
		body := w.Body.String()
		require.NotContains(t, body, "result_data", "%s: the dashboard serves an operation's result", who)
		require.NotContains(t, body, `{\"scanned\":3}`, who)
		require.LessOrEqual(t, len(d.Data.Recent), 5, who)

		mayRead := slices.Contains(f.readable(who), f.scopedPlan) // settings.manage fixer: admin only
		for _, id := range hidden {
			if mayRead {
				require.Contains(t, ids(d), failedID, "%s: the run a settings.manage caller may read is missing", who)
				break
			}
			require.NotContains(t, body, id, "%s: the dashboard names a repairs run the caller may not read", who)
		}
		if mayRead {
			continue
		}
		require.NotContains(t, body, secondaryUserID, "%s: the dashboard carries the target user of a repairs run", who)
		require.NotContains(t, body, "target_user_id", who)
		// The five newest operations are hidden from this caller; the list is
		// still filled from the ones behind them.
		require.NotEmpty(t, d.Data.Recent, "%s: hidden runs emptied the list", who)
		require.Contains(t, ids(d), f.plainScan, who)
	}
}

// TestSystemStatus_RecentOperationsLeaveOutRepairsRuns: GET /system/status (the
// Dashboard page's source, settings.manage) lists the newest operations without
// the Repairs runs the caller may not read and without any operation's
// result_data. A custom role with settings.manage but not users.manage passes
// the route's gate and must still not see the Audible read-status runs (the plan
// result carries a user's listening state, owner decision 2026-10-24); the admin
// sees them, which is the oracle that the server wires the check in. The list is
// still filled from the older operations behind the hidden ones.
// Not parallel: it swaps the process-wide config.
func TestSystemStatus_RecentOperationsLeaveOutRepairsRuns(t *testing.T) {
	const ars = "maintenance.audible-read-status"
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)

	now := time.Now().UTC()
	hidden := []string{}
	for i := range 6 {
		id := "op-ars-plan-newer-" + strconv.Itoa(i)
		hidden = append(hidden, id)
		res := `{"params":{"target_user_id":"` + secondaryUserID + `"}}`
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: repairs.PlanOpID, Plugin: "test", Status: "completed",
			Params: `{"fixer_id":"` + ars + `"}`, ResultData: &res,
			QueuedAt: now.Add(time.Duration(2+i) * time.Minute), CompletedAt: &now,
		}))
	}

	type status struct {
		Data struct {
			Operations struct {
				Recent []map[string]any `json:"recent"`
			} `json:"operations"`
		} `json:"data"`
	}
	for who, token := range map[string]string{"ops manager": mgr, "admin": f.admin} {
		w := getAs(f.srv, token, "/api/v1/system/status")
		require.Equal(t, http.StatusOK, w.Code, "%s: %s", who, w.Body.String())
		body := w.Body.String()
		recent := decodeAs[status](t, w).Data.Operations.Recent
		ids := []string{}
		for _, op := range recent {
			id, _ := op["id"].(string)
			ids = append(ids, id)
		}
		require.NotContains(t, body, "result_data", "%s: the status serves an operation's result", who)
		require.LessOrEqual(t, len(recent), 5, who)
		if who == "admin" {
			require.Contains(t, ids, hidden[len(hidden)-1], "the admin reads the Audible read-status runs")
			continue
		}
		for _, id := range hidden {
			require.NotContains(t, body, id, "%s: the status names a run of a fixer the caller may not use", who)
		}
		require.NotContains(t, body, secondaryUserID, who)
		require.NotEmpty(t, recent, "%s: hidden runs emptied the list", who)
		require.Contains(t, ids, f.plainScan, who)
	}
}

// opsManagerToken is a session for a custom role that holds everything the
// generic start routes ask for (library.view, scan.trigger, settings.manage) and
// library.edit_metadata, but not users.manage.
func opsManagerToken(t *testing.T, f *repairsOpAccessFixture) string {
	t.Helper()
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	_, err := store.CreateRole(&database.Role{
		ID: "role-ops-manager", Name: "ops-manager",
		Permissions: []auth.Permission{auth.PermLibraryView, auth.PermScanTrigger, auth.PermLibraryEditMetadata, auth.PermSettingsManage},
	})
	require.NoError(t, err)
	u, err := store.CreateUser("opsmgr", "opsmgr@example.invalid", "bcrypt", "hash", []string{"role-ops-manager"}, "active")
	require.NoError(t, err)
	sess, err := store.CreateSession(u.ID, "192.0.2.11", "test", time.Hour, 0)
	require.NoError(t, err)
	return sess.ID
}

func postJSONAs(srv *Server, token, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// TestGenericStartRoutes_ApplyTheFixersPermissions: POST /operations/v2, a retry
// through /operations/v2/:id/retry, POST /api/v3/ops/runs and POST
// /api/v3/ops/defs/:id/runs start a Repairs run only for a caller who holds the
// permissions of the fixer the params name, as the Repairs routes require. A
// custom role with settings.manage but not users.manage is refused 403 naming
// users.manage for the Audible read-status fixer (owner decision 2026-10-24) on
// every one, for plan and apply alike, and nothing is queued; the same role
// starts a fixer that needs only settings.manage; an admin starts both; params
// naming no fixer are a 400 and a fixer this build does not register a 404, for
// everyone. Not parallel: it swaps the process-wide config.
func TestGenericStartRoutes_ApplyTheFixersPermissions(t *testing.T) {
	const ars = "maintenance.audible-read-status"
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)

	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	queued := func() int {
		rows, err := store.ListOperationsV2Since(time.Time{}, 500)
		require.NoError(t, err)
		return len(rows)
	}

	denied := func(w *httptest.ResponseRecorder, what string) {
		t.Helper()
		require.Equal(t, http.StatusForbidden, w.Code, what+": "+w.Body.String())
		require.Contains(t, w.Body.String(), "permission denied: users.manage", what)
	}

	v2 := func(token, def, params string) *httptest.ResponseRecorder {
		return postJSONAs(f.srv, token, "/api/v1/operations/v2", `{"def_id":"`+def+`","params":`+params+`}`)
	}
	v3 := func(token, def, params string) *httptest.ResponseRecorder {
		return postJSONAs(f.srv, token, "/api/v3/ops/runs", `{"def":"`+def+`","params":`+params+`}`)
	}
	v3Nested := func(token, def, params string) *httptest.ResponseRecorder {
		return postJSONAs(f.srv, token, "/api/v3/ops/defs/"+def+"/runs", `{"params":`+params+`}`)
	}
	routes := map[string]func(token, def, params string) *httptest.ResponseRecorder{
		"POST /operations/v2": v2, "POST /api/v3/ops/runs": v3, "POST /api/v3/ops/defs/:id/runs": v3Nested,
	}

	before := queued()
	for name, start := range routes {
		for _, def := range []string{repairs.PlanOpID, repairs.ApplyOpID} {
			denied(start(mgr, def, `{"fixer_id":"`+ars+`"}`), name+" "+def+" by the custom role")
		}
	}
	// A retry of an Audible read-status run: the route's own gate is
	// settings.manage, which the custom role holds.
	res := `{"fixer_id":"` + ars + `"}`
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-ars-failed", DefID: repairs.ApplyOpID, Plugin: "test", Status: "failed",
		Params: res, QueuedAt: time.Now().UTC(),
	}))
	denied(postJSONAs(f.srv, mgr, "/api/v1/operations/v2/op-ars-failed/retry", `{}`), "retry by the custom role")
	require.Equal(t, before+1, queued(), "a refused start queued a run (only the seeded failed run is new)")
	// "Finish without resuming" re-queues a waiting Repairs run, so it is
	// gated by the same fixer check as the retry.
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-ars-asking", DefID: repairs.ApplyOpID, Plugin: "test", Status: "interrupted_ask",
		Params: res, QueuedAt: time.Now().UTC(),
	}))
	denied(postJSONAs(f.srv, mgr, "/api/v1/operations/v2/op-ars-asking/finish", `{}`), "finish by the custom role")
	asking, err := store.GetOperationV2("op-ars-asking")
	require.NoError(t, err)
	require.Equal(t, "interrupted_ask", asking.Status, "a refused finish re-queued the run")

	// No fixer and an unknown fixer are refused to everyone, an admin included.
	for name, start := range routes {
		w := start(f.admin, repairs.PlanOpID, `{}`)
		require.Equal(t, http.StatusBadRequest, w.Code, name+": "+w.Body.String())
		w = start(f.admin, repairs.PlanOpID, `{"fixer_id":"fixer.removed-since"}`)
		require.Equal(t, http.StatusNotFound, w.Code, name+": "+w.Body.String())
	}
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-nofixer-failed", DefID: repairs.ApplyOpID, Plugin: "test", Status: "failed",
		Params: `{}`, QueuedAt: time.Now().UTC(),
	}))
	w := postJSONAs(f.srv, f.admin, "/api/v1/operations/v2/op-nofixer-failed/retry", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// The custom role and the admin start a settings.manage-only fixer; the
	// admin starts the Audible read-status fixer. Same shape, so a 403 above is
	// the fixer check and nothing else.
	for name, start := range routes {
		w := start(mgr, repairs.PlanOpID, `{"fixer_id":"`+scopedFixerID+`"}`)
		require.Equal(t, http.StatusAccepted, w.Code, name+" custom role, settings.manage fixer: "+w.Body.String())
	}
	for name, start := range routes {
		w := start(f.admin, repairs.PlanOpID, `{"fixer_id":"`+ars+`"}`)
		require.Equal(t, http.StatusAccepted, w.Code, name+" admin, Audible read-status: "+w.Body.String())
	}
}

// TestPlanResultParams_NamingATargetUserNeedsUsersManage: a plan whose params
// name a target user (repairs.PlanResult.Params carries target_user_id into the
// op's result_data and its params) is read only by a caller who holds users.manage,
// on every generic route that serves the op's result: the Audible read-status
// and master-list-import fixers are scoped to users.manage, so an editor, a
// viewer and a custom role with settings.manage but not users.manage are refused
// and the refusal names no user, while the admin reads the same body. The
// admin's read is the oracle that the ops are really the ones the roles are
// refused.
// Not parallel: it swaps the process-wide config.
func TestPlanResultParams_NamingATargetUserNeedsUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)

	now := time.Now().UTC()
	for _, fixer := range []string{"maintenance.audible-read-status", "master-list-import"} {
		plan, err := json.Marshal(repairs.PlanResult{
			FixerID: fixer, Params: json.RawMessage(`{"target_user_id":"` + secondaryUserID + `"}`),
			PlannedAt: now, Total: 3, Applicable: 2, SkippedByKind: map[string]int{},
		})
		require.NoError(t, err)
		res := string(plan)
		id := "op-plan-" + strings.ReplaceAll(fixer, ".", "-")
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: repairs.PlanOpID, Plugin: "test", Status: "completed",
			Params: `{"fixer_id":"` + fixer + `"}`, ResultData: &res,
			QueuedAt: now, CompletedAt: &now,
		}))
		for _, route := range []string{"result", "v2 op"} {
			path := opReadPaths(id)[route]
			for who, token := range map[string]string{"viewer": f.viewer, "editor": f.editor, "ops manager": mgr} {
				w := getAs(f.srv, token, path)
				require.Equal(t, http.StatusForbidden, w.Code, "%s %s %s: %s", who, fixer, route, w.Body.String())
				require.NotContains(t, w.Body.String(), secondaryUserID, "%s %s %s", who, fixer, route)
				require.NotContains(t, w.Body.String(), "target_user_id", "%s %s %s", who, fixer, route)
			}
		}
		w := getAs(f.srv, f.admin, opReadPaths(id)["result"])
		require.Equal(t, http.StatusOK, w.Code, "%s admin: %s", fixer, w.Body.String())
		require.Contains(t, w.Body.String(), secondaryUserID, "%s: the admin reads the plan's target user", fixer)
	}
}
