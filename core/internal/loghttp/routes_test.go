// file: core/internal/loghttp/routes_test.go
// version: 1.0.0
// guid: 4f2d8893-327e-408f-a295-2ed2d43c6397
// last-edited: 2026-11-18

package loghttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
)

func wantBadRequest(t testing.TB, err error, field string) {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok || he.Status != http.StatusBadRequest {
		t.Errorf("%s: err = %v (%T), want a 400 *httpapi.Error", field, err, err)
		return
	}
	if !strings.Contains(he.Detail+fmt.Sprint(he.Fields), field) {
		t.Errorf("%s: 400 does not name the parameter: %+v", field, he)
	}
}

// TestLogRoute_ParamsMapToJournalQuery: each query parameter lands in the
// matching JournalQuery field, limit defaults to 50 and is clamped to 1000, and
// a malformed parameter is a 400 with no query run.
func TestLogRoute_ParamsMapToJournalQuery(t *testing.T) {
	since := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	p := principal("u-bruce", permManage, permUsers)

	t.Run("every_parameter_lands", func(t *testing.T) {
		fj := &fakeJournal{}
		a := newAPI(fj, nil)
		mustCall(t, a, p, queryRequest{
			Level: "warn", Module: "books", Since: "2026-11-17T00:00:00Z", Until: "2026-11-18T00:00:00Z",
			Q: "joker", Cursor: "j1.abc", Limit: 7,
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
			mustCall(t, newAPI(fj, nil), p, queryRequest{Limit: c.in})
			if got := fj.queries()[0].Limit; got != c.want {
				t.Errorf("limit %d -> %d, want %d", c.in, got, c.want)
			}
		}
	})

	t.Run("malformed_is_400_and_runs_no_query", func(t *testing.T) {
		for _, c := range []struct {
			field string
			req   queryRequest
		}{
			{"since", queryRequest{Since: "yesterday"}},
			{"until", queryRequest{Until: "2026-11-18"}},
			{"level", queryRequest{Level: "loud"}},
			{"level", queryRequest{Level: "trace"}},
			// a good value beside a bad one is still no partial query
			{"since", queryRequest{Level: "warn", Since: "soon", Until: "2026-11-18T00:00:00Z"}},
		} {
			fj := &fakeJournal{}
			_, err := call(t, newAPI(fj, nil), p, c.req)
			wantBadRequest(t, err, c.field)
			if n := len(fj.queries()); n != 0 {
				t.Errorf("%s %+v: %d queries ran, want none", c.field, c.req, n)
			}
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

	t.Run("next_passes_through", func(t *testing.T) {
		fj := &fakeJournal{page: log.JournalPage{Next: "j1.next"}}
		v := mustCall(t, newAPI(fj, nil), p, queryRequest{})
		if v.Next != "j1.next" {
			t.Errorf("next = %q", v.Next)
		}
		if v.Records == nil {
			t.Error("records is nil: it marshals as null, not []")
		}
	})
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
		v := mustCall(t, a, p, queryRequest{Limit: 50, Cursor: cursor})
		sizes = append(sizes, len(v.Records))
		got = append(got, messages(v)...)
		if v.Next == "" {
			break
		}
		cursor = v.Next
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
