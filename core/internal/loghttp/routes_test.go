// file: core/internal/loghttp/routes_test.go
// version: 1.1.0
// guid: 4f2d8893-327e-408f-a295-2ed2d43c6397
// last-edited: 2026-11-18

package loghttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
)

// wantInvalid asserts err is httpapi's 422 validation_failed naming field.
func wantInvalid(t testing.TB, err error, field string) {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok || he.Status != http.StatusUnprocessableEntity || he.Code != "validation_failed" {
		t.Errorf("%s: err = %v (%T), want a 422 validation_failed *httpapi.Error", field, err, err)
		return
	}
	if !slices.ContainsFunc(he.Fields, func(f httpapi.FieldError) bool { return f.Field == field }) {
		t.Errorf("%s: the 422 does not name the field: %+v", field, he.Fields)
	}
}

// TestLogRoute_ParamsMapToJournalQuery: each query parameter lands in the
// matching JournalQuery field, limit defaults to 50 and is clamped to 1000, and
// a malformed parameter is a 422 with no query run.
func TestLogRoute_ParamsMapToJournalQuery(t *testing.T) {
	since := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	p := principal("u-bruce", permManage, permUsers)

	t.Run("every_parameter_lands", func(t *testing.T) {
		fj := &fakeJournal{}
		a := newAPI(fj, nil)
		mustCall(t, a, p, queryRequest{
			Level: "warn", Module: "books", Since: since, Until: until, Q: "joker", CursorParams: paged(7, "j1.abc"),
		})
		qs := fj.queries()
		if len(qs) != 1 {
			t.Fatalf("%d queries, want 1", len(qs))
		}
		q := qs[0]
		if q.MinLevel != slog.LevelWarn || q.Module != "books" || q.Text != "joker" || q.Cursor != "j1.abc" || q.Limit != 7 {
			t.Errorf("query = %+v", q)
		}
		if !q.Since.Equal(since) || !q.Until.Equal(until) {
			t.Errorf("since/until = %v / %v, want %v / %v", q.Since, q.Until, since, until)
		}
	})

	t.Run("absent_parameters_are_the_zero_query", func(t *testing.T) {
		fj := &fakeJournal{}
		mustCall(t, newAPI(fj, nil), p, queryRequest{})
		q := fj.queries()[0]
		if q.MinLevel != log.LevelDebug || q.Module != "" || !q.Since.IsZero() || !q.Until.IsZero() || q.Text != "" || q.Cursor != "" {
			t.Errorf("query = %+v, want debug and nothing else", q)
		}
	})

	t.Run("levels", func(t *testing.T) {
		for _, c := range []struct {
			in   string
			want slog.Level
		}{{"", log.LevelDebug}, {"debug", log.LevelDebug}, {"info", log.LevelInfo}, {"warn", log.LevelWarn}, {"error", log.LevelError}} {
			fj := &fakeJournal{}
			mustCall(t, newAPI(fj, nil), p, queryRequest{Level: c.in})
			if got := fj.queries()[0].MinLevel; got != c.want {
				t.Errorf("level %q -> %v, want %v", c.in, got, c.want)
			}
		}
	})

	t.Run("limit_default_and_clamp", func(t *testing.T) {
		for _, c := range []struct{ in, want int }{{0, 50}, {-3, 50}, {1, 1}, {50, 50}, {1000, 1000}, {1001, 1000}, {5000, 1000}} {
			fj := &fakeJournal{}
			mustCall(t, newAPI(fj, nil), p, queryRequest{CursorParams: paged(c.in, "")})
			if got := fj.queries()[0].Limit; got != c.want {
				t.Errorf("limit %d -> %d, want %d", c.in, got, c.want)
			}
		}
	})

	t.Run("malformed_is_422_and_runs_no_query", func(t *testing.T) {
		for _, c := range []struct {
			field string
			req   queryRequest
		}{
			{"/query/level", queryRequest{Level: "loud"}},
			{"/query/level", queryRequest{Level: "trace"}},
			{"/query/since", queryRequest{Since: until, Until: since}},
			// a good value beside a bad one is still no partial query
			{"/query/level", queryRequest{Level: "shout", Since: since, Until: until}},
		} {
			fj := &fakeJournal{}
			_, err := call(t, newAPI(fj, nil), p, c.req)
			wantInvalid(t, err, c.field)
			if n := len(fj.queries()); n != 0 {
				t.Errorf("%s %+v: %d queries ran, want none", c.field, c.req, n)
			}
		}
	})

	t.Run("since_equal_until_is_not_a_range_error", func(t *testing.T) {
		fj := &fakeJournal{}
		mustCall(t, newAPI(fj, nil), p, queryRequest{Since: since, Until: since})
		if len(fj.queries()) != 1 {
			t.Error("since == until was refused")
		}
	})

	t.Run("hide_is_set_only_for_a_caller_without_users_view_and_a_predicate", func(t *testing.T) {
		for _, c := range []struct {
			name string
			p    *auth.Principal
			pred log.OtherUserPredicate
			want bool
		}{
			{"manage_only_with_predicate", principal("u-bruce", permManage), namesSelina, true},
			{"manage_only_no_predicate", principal("u-bruce", permManage), nil, false},
			{"users_view_with_predicate", principal("u-bruce", permManage, permUsers), namesSelina, false},
		} {
			fj := &fakeJournal{}
			mustCall(t, newAPI(fj, c.pred), c.p, queryRequest{})
			if got := fj.queries()[0].Hide != nil; got != c.want {
				t.Errorf("%s: Hide set = %v, want %v", c.name, got, c.want)
			}
		}
	})

	t.Run("hide_asks_the_predicate_with_the_viewer", func(t *testing.T) {
		var seen []string
		pred := func(_ log.Record, viewer string) bool { seen = append(seen, viewer); return false }
		fj := &fakeJournal{}
		mustCall(t, newAPI(fj, pred), principal("u-dick", permManage), queryRequest{})
		fj.queries()[0].Hide(log.Record{})
		if !slices.Equal(seen, []string{"u-dick"}) {
			t.Errorf("predicate saw viewers %v, want [u-dick]", seen)
		}
	})

	t.Run("store_error_is_a_plain_error_never_echoed", func(t *testing.T) {
		fj := &fakeJournal{err: errors.New("pebble: corrupt block at /var/lib/gotham/journal")}
		_, err := call(t, newAPI(fj, nil), p, queryRequest{})
		if err == nil {
			t.Fatal("store error swallowed")
		}
		// A plain error is what httpapi logs and answers 500 for without echoing it.
		if _, isProblem := errors.AsType[*httpapi.Error](err); isProblem {
			t.Errorf("err = %v is already a problem; the router would echo it", err)
		}
	})

	t.Run("next_cursor_passes_through", func(t *testing.T) {
		fj := &fakeJournal{page: log.JournalPage{Next: "j1.next"}}
		v := mustCall(t, newAPI(fj, nil), p, queryRequest{})
		if v.NextCursor != "j1.next" {
			t.Errorf("next_cursor = %q", v.NextCursor)
		}
		if v.Items == nil {
			t.Error("items is nil: it marshals as null, not []")
		}
	})
}

// TestLogRoute_BadCursorIs400: a cursor the native journal did not issue is
// 400 invalid_cursor (05-http-api §6), not a 500.
func TestLogRoute_BadCursorIs400(t *testing.T) {
	a := newAPI(openJournal(t), nil)
	for _, c := range []string{"garbage", "j1.", "j1.not-a-ulid", "j2.01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		_, err := call(t, a, principal("u-bruce", permManage, permUsers), queryRequest{CursorParams: paged(0, c)})
		he, ok := errors.AsType[*httpapi.Error](err)
		if !ok || he.Status != http.StatusBadRequest || he.Code != "invalid_cursor" ||
			!slices.ContainsFunc(he.Fields, func(f httpapi.FieldError) bool { return f.Field == "/query/cursor" }) {
			t.Errorf("cursor %q: err = %v, want 400 invalid_cursor naming /query/cursor", c, err)
		}
	}
}

// TestLogRoute_QueryStringBindsToTheRequest: the URL-to-struct half that the
// handler tests skip. Router.Build is unavailable until F4-08, but OpenAPI
// reads each route's compiled binding, so the operation's query parameters are
// exactly the names 05-logging §5 fixes, with the types the binder parses.
func TestLogRoute_QueryStringBindsToTheRequest(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	if err := Register(r.CoreGroup(), Deps{Journal: &fakeJournal{}}); err != nil {
		t.Fatal(err)
	}
	doc, err := r.OpenAPI("core")
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	op := doc.Paths["/api/core/v1/system/logs"]["get"]
	if op == nil {
		t.Fatalf("no GET /api/core/v1/system/logs in the document: %v", doc.Paths)
	}
	if op.OperationID != "core_listSystemLogs" || op.Permission != "core:system.manage" {
		t.Errorf("operation = %s needing %s", op.OperationID, op.Permission)
	}
	got := map[string]string{}
	for _, p := range op.Parameters {
		if p.In != "query" {
			t.Errorf("parameter %s is in %s, want query", p.Name, p.In)
		}
		got[p.Name] = p.Schema.Type
	}
	want := map[string]string{"level": "string", "module": "string", "since": "string", "until": "string", "q": "string", "cursor": "string", "limit": "integer"}
	if !maps.Equal(got, want) {
		t.Errorf("query parameters = %v, want %v", got, want)
	}
}

// TestLogRoute_WireShape: the JSON names are 05-http-api §4.4's CursorPage and
// the record's own, an empty page encodes items as [] and no next_cursor, and a
// record's attrs are an object even when empty.
func TestLogRoute_WireShape(t *testing.T) {
	empty, err := json.Marshal(mustCall(t, newAPI(&fakeJournal{}, nil), principal("u-bruce", permManage), queryRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{"items":[]}` {
		t.Errorf("empty page = %s, want {\"items\":[]}", empty)
	}
	fj := &fakeJournal{page: log.JournalPage{Next: "j1.x", Records: []log.Record{{
		Time: at(0), Level: log.LevelError, Module: "core", Subsystem: "bat", Message: "alarm", TraceID: "t1", SpanID: "s1",
	}}}}
	full, err := json.Marshal(mustCall(t, newAPI(fj, nil), principal("u-bruce", permManage), queryRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"items":[{"time":"2026-11-18T03:00:00Z","level":"error","module":"core","subsystem":"bat","message":"alarm","attrs":{},"trace_id":"t1","span_id":"s1"}],"next_cursor":"j1.x"}`
	if string(full) != want {
		t.Errorf("page = %s\nwant   %s", full, want)
	}
}

// TestRegister_RefusesNoJournal: Deps without a journal is an error, and no
// route is registered for it.
func TestRegister_RefusesNoJournal(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	if err := Register(r.CoreGroup(), Deps{}); !errors.Is(err, ErrNoJournal) {
		t.Fatalf("Register = %v, want ErrNoJournal", err)
	}
	if n := len(slices.Collect(r.Routes())); n != 0 {
		t.Errorf("%d routes registered for a refused Deps", n)
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses every
// non-Public route with ErrNoAuthService; a bad binding, a schema httpapi
// cannot describe, a non-snake_case name or a duplicate would each be a further
// error.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	if err := Register(r.CoreGroup(), Deps{Journal: &fakeJournal{}}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != 1 || !errors.Is(problems[0], httpapi.ErrNoAuthService) {
		t.Errorf("Build reported %v, want exactly one ErrNoAuthService", err)
	}
}

// TestLogRoute_CursorPagesNewestFirst: over the native store, three pages
// chained by next return all 120 records newest first with no duplicate, and
// the last page's next is empty.
func TestLogRoute_CursorPagesNewestFirst(t *testing.T) {
	st := openJournal(t)
	var in []log.Record
	for i := range 120 {
		in = append(in, log.Record{Time: at(time.Duration(i) * time.Second), Level: log.LevelInfo, Module: "core",
			Subsystem: "bat", Message: fmt.Sprintf("r%03d", i)})
	}
	if err := st.Append(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	a := newAPI(st, nil)
	p := principal("u-bruce", permManage, permUsers)
	var got []string
	cursor := ""
	var sizes []int
	for range 10 {
		v := mustCall(t, a, p, queryRequest{CursorParams: paged(50, cursor)})
		sizes = append(sizes, len(v.Items))
		got = append(got, messages(v)...)
		if v.NextCursor == "" {
			break
		}
		cursor = v.NextCursor
	}
	if !slices.Equal(sizes, []int{50, 50, 20}) {
		t.Fatalf("page sizes = %v, want [50 50 20]", sizes)
	}
	if len(got) != 120 {
		t.Fatalf("%d records, want 120", len(got))
	}
	for i, m := range got {
		if want := fmt.Sprintf("r%03d", 119-i); m != want {
			t.Fatalf("position %d = %s, want %s (newest first, no duplicate)", i, m, want)
		}
	}
}

// TestLogRoute_AuthOffAnswers401: runs now: the route needs
// core:system.manage and the catalog marks that scope AuthRequired, which is
// what makes the router's chain refuse the auth-disabled principal with 401
// before the handler runs (05-rbac §4.11). The booted cell needs stage 13.
func TestLogRoute_AuthOffAnswers401(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	if err := Register(r.CoreGroup(), Deps{Journal: &fakeJournal{}}); err != nil {
		t.Fatal(err)
	}
	var ri httpapi.RouteInfo
	for x := range r.Routes() {
		ri = x
	}
	if ri.Perm.String() != rbac.Need(permManage).String() {
		t.Fatalf("route needs %s, want %s", ri.Perm, permManage)
	}
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := cat.Lookup(permManage)
	if !ok || !spec.AuthRequired {
		t.Fatalf("catalog entry for %s = %+v (found %v), want AuthRequired", permManage, spec, ok)
	}

	t.Run("booted_auth_disabled_answers_401", func(t *testing.T) {
		t.Skip("needs stage 13: F4-08")
	})
}
