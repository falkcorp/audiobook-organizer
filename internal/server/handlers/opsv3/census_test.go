// file: internal/server/handlers/opsv3/census_test.go
// version: 1.0.0
// guid: 4c8b6190-c9c8-436b-bf9f-2f269af7f052
// last-edited: 2026-10-17

package opsv3

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

type listResp struct {
	Items      []RunView `json:"items"`
	NextCursor string    `json:"next_cursor"`
	Total      int64     `json:"total"`
}

type countsResp struct {
	GroupBy string       `json:"group_by"`
	Groups  []CountGroup `json:"groups"`
	Total   int64        `json:"total"`
}

// seed25 inserts 25 runs over three definitions in a mix of states.
func seed25(t testing.TB, p *database.PebbleStore) {
	t.Helper()
	defs := []string{"test.alpha", "test.beta", "test.gamma"}
	for i := range 25 {
		id := insertRun(t, p, defs[i%3])
		switch {
		case i%5 == 1:
			finish(t, p, id, state.EvReturnNil)
		case i%5 == 2:
			finish(t, p, id, state.EvReturnErr)
		case i%5 == 3:
			step(t, p, id, state.EvDispatch, nil)
		}
	}
}

// allPages follows next_cursor from href until it ends, returning every item
// and the total each page reported.
func allPages(t testing.TB, r http.Handler, href string, limit string) (items []RunView, totals []int64) {
	t.Helper()
	cursor := ""
	for range 100 {
		u := href + "&limit=" + limit
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		w := do(t, r, viewer, http.MethodGet, u, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", u, w.Code, w.Body.String())
		}
		page := decode[listResp](t, w)
		items = append(items, page.Items...)
		totals = append(totals, page.Total)
		if page.NextCursor == "" {
			return items, totals
		}
		cursor = page.NextCursor
	}
	t.Fatalf("GET %s never ended", href)
	return nil, nil
}

func TestCountsExactAcrossPages(t *testing.T) {
	p := realStore(t)
	seed25(t, p)
	r := newRouter(New(Deps{Runs: p}))

	items, totals := allPages(t, r, "/api/v3/ops/runs?", "10")
	if len(items) != 25 {
		t.Fatalf("paging 25 runs by 10 returned %d rows", len(items))
	}
	if len(totals) != 3 {
		t.Fatalf("25 runs by 10 should take 3 pages, took %d", len(totals))
	}
	for i, tot := range totals {
		if tot != 25 {
			t.Errorf("page %d reported total %d, want 25 on every page", i, tot)
		}
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Errorf("run %s appeared on two pages", it.ID)
		}
		seen[it.ID] = true
	}

	for _, by := range []string{"state", "def", "outcome"} {
		w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/counts?group_by="+by, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("counts %s = %d: %s", by, w.Code, w.Body.String())
		}
		cr := decode[countsResp](t, w)
		var sum int64
		for _, g := range cr.Groups {
			sum += g.Count
		}
		if sum != cr.Total {
			t.Errorf("group_by=%s: groups sum to %d but total is %d", by, sum, cr.Total)
		}
		if by != "outcome" && cr.Total != 25 {
			t.Errorf("group_by=%s: total %d, want 25 (equal to the runs total)", by, cr.Total)
		}
	}
}

// TestEveryCountHrefReturnsExactlyCountRows is the "every count clicks
// through" rule: follow each group's href over every page and count the rows.
func TestEveryCountHrefReturnsExactlyCountRows(t *testing.T) {
	p := realStore(t)
	seed25(t, p)
	r := newRouter(New(Deps{Runs: p}))
	for _, by := range []string{"state", "def", "outcome"} {
		cr := decode[countsResp](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/counts?group_by="+by, nil))
		if len(cr.Groups) == 0 {
			t.Fatalf("group_by=%s: no groups", by)
		}
		for _, g := range cr.Groups {
			if g.Href == "" {
				t.Errorf("%s group %q has no href", by, g.Key)
				continue
			}
			items, totals := allPages(t, r, g.Href, "7")
			if int64(len(items)) != g.Count {
				t.Errorf("%s group %q: count %d but href %s returned %d rows", by, g.Key, g.Count, g.Href, len(items))
			}
			for _, tot := range totals {
				if tot != g.Count {
					t.Errorf("%s group %q: a page reported total %d, count is %d", by, g.Key, tot, g.Count)
				}
			}
		}
	}
}

func TestCountsRejectsUnknownGrouping(t *testing.T) {
	r := newRouter(New(Deps{Runs: realStore(t)}))
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/counts?group_by=colour", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("group_by=colour = %d, want 400", w.Code)
	}
}

func TestRunsRejectBadFilters(t *testing.T) {
	r := newRouter(New(Deps{Runs: realStore(t)}))
	for _, q := range []string{"state=melted", "limit=0", "limit=abc", "cursor=bogus"} {
		if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs?"+q, nil); w.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400: %s", q, w.Code, w.Body.String())
		}
	}
}

func TestTimelineCompleteFalseWhenTruncated(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	mk := func(id string, at time.Time) database.RunRecord {
		return database.RunRecord{ID: id, DefID: "test.alpha", State: state.V3Succeeded, CompletedAt: &at}
	}
	recs := []database.RunRecord{mk("a", now.Add(-3*time.Hour)), mk("b", now.Add(-2*time.Hour)), mk("c", now.Add(-time.Hour))}

	// The store stopped at its cap: the window is not complete.
	cut := &memRuns{done: recs, doneCmplete: false}
	r := newRouter(New(Deps{Runs: cut, Now: func() time.Time { return now }}))
	type tl struct {
		Items    []RunView `json:"items"`
		Complete bool      `json:"complete"`
		ScanCap  int       `json:"scan_cap"`
	}
	got := decode[tl](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/timeline", nil))
	if got.Complete {
		t.Fatal("a window the scan cap cut short must say complete=false")
	}
	if len(got.Items) != 3 || got.ScanCap != TimelineScanCap {
		t.Errorf("items=%d scan_cap=%d", len(got.Items), got.ScanCap)
	}

	// The store covered the whole window.
	whole := &memRuns{done: recs, doneCmplete: true}
	r = newRouter(New(Deps{Runs: whole, Now: func() time.Time { return now }}))
	if got := decode[tl](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/timeline", nil)); !got.Complete {
		t.Error("an uncut window must say complete=true")
	}

	// The cap was hit, but the scan already ran past `to`: nothing in
	// [from, to] is missing, and the runs after `to` are not listed.
	r = newRouter(New(Deps{Runs: cut, Now: func() time.Time { return now }}))
	to := now.Add(-90 * time.Minute).Format(time.RFC3339)
	got = decode[tl](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/timeline?to="+url.QueryEscape(to), nil))
	if !got.Complete || len(got.Items) != 2 {
		t.Errorf("window ending before the cut: complete=%v items=%d, want true and 2", got.Complete, len(got.Items))
	}

	for _, q := range []string{"from=yesterday", "to=nope", "from=2026-10-17T12:00:00Z&to=2026-10-16T12:00:00Z"} {
		if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/timeline?"+q, nil); w.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, w.Code)
		}
	}
}

func TestRowsAreExactAndSayWhenTruncated(t *testing.T) {
	p := realStore(t)
	id := insertRun(t, p, "test.alpha")
	finish(t, p, id, state.EvReturnNil)
	var rows []database.ItemOutcome
	for i := range 12 {
		out := "failed"
		if i%3 == 0 {
			out = "skipped:exists"
		}
		rows = append(rows, database.ItemOutcome{Key: string(rune('a'+i)) + "-item", Outcome: out, Chunk: i / 4})
	}
	if err := p.PutItemOutcomesV3(id, rows); err != nil {
		t.Fatal(err)
	}
	r := newRouter(New(Deps{Runs: p}))
	type rowsResp struct {
		Items      []RowView `json:"items"`
		NextCursor string    `json:"next_cursor"`
		Total      int64     `json:"total"`
		Truncated  bool      `json:"truncated"`
	}
	got := decode[rowsResp](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/"+id+"/rows?limit=5", nil))
	if got.Total != 12 || len(got.Items) != 5 || got.NextCursor == "" || got.Truncated {
		t.Fatalf("first page: total=%d items=%d next=%q truncated=%v", got.Total, len(got.Items), got.NextCursor, got.Truncated)
	}
	sk := decode[rowsResp](t, do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/"+id+"/rows?outcome=skipped", nil))
	if sk.Total != 4 || len(sk.Items) != 4 {
		t.Errorf("outcome=skipped: total=%d items=%d, want 4 and 4", sk.Total, len(sk.Items))
	}
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/"+id+"/rows?cursor=nope", nil); w.Code != http.StatusBadRequest {
		t.Errorf("foreign cursor = %d, want 400", w.Code)
	}
	if w := do(t, r, viewer, http.MethodGet, "/api/v3/ops/runs/missing/rows", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", w.Code)
	}

	// The per-run cap dropped outcomes: the answer says so.
	mem := &memRuns{runs: map[string]database.RunRecord{"r": {ID: "r"}}}
	capped := newRouter(New(Deps{Runs: &truncRows{memRuns: mem}}))
	if got := decode[rowsResp](t, do(t, capped, viewer, http.MethodGet, "/api/v3/ops/runs/r/rows", nil)); !got.Truncated {
		t.Error("a run whose outcomes were capped must report truncated=true")
	}
}

type truncRows struct{ *memRuns }

func (truncRows) ListItemOutcomesV3(string, database.ItemQuery) (database.ItemPage, error) {
	return database.ItemPage{Total: 10000, Truncated: true}, nil
}

func TestReadsNeedLibraryView(t *testing.T) {
	p := realStore(t)
	id := insertRun(t, p, "test.alpha")
	h := New(Deps{
		Runs: p, Defs: &fakeDefs{defs: map[string]DefInfo{"test.alpha": {ID: "test.alpha", Title: "Alpha", Kind: "task"}}},
		Broker: NewBroker(p, nil, nil, BrokerOptions{}),
	})
	r := newRouter(h)
	for _, path := range []string{
		"/api/v3/ops/defs", "/api/v3/ops/defs/test.alpha", "/api/v3/ops/runs", "/api/v3/ops/runs/" + id,
		"/api/v3/ops/runs/" + id + "/rows", "/api/v3/ops/counts", "/api/v3/ops/timeline",
	} {
		if w := do(t, r, nobody, http.MethodGet, path, nil); w.Code != http.StatusForbidden {
			t.Errorf("GET %s without library.view = %d, want 403", path, w.Code)
		}
		if w := do(t, r, viewer, http.MethodGet, path, nil); w.Code != http.StatusOK {
			t.Errorf("GET %s with library.view = %d, want 200: %s", path, w.Code, w.Body.String())
		}
	}
	// The event stream is library.view too (a streaming route, so only the
	// refusal is exercised here).
	if w := do(t, r, nobody, http.MethodGet, "/api/v3/ops/events", nil); w.Code != http.StatusForbidden {
		t.Errorf("GET events without library.view = %d, want 403", w.Code)
	}
	// Writes need library.view as the floor.
	for _, path := range []string{"/api/v3/ops/defs/test.alpha/runs", "/api/v3/ops/runs", "/api/v3/ops/runs/" + id + "/cancel", "/api/v3/ops/runs/" + id + "/approve"} {
		if w := do(t, r, nobody, http.MethodPost, path, "{}"); w.Code != http.StatusForbidden {
			t.Errorf("POST %s without library.view = %d, want 403", path, w.Code)
		}
	}
}
