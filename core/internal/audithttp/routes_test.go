// file: core/internal/audithttp/routes_test.go
// version: 1.0.0
// guid: 4c6e2210-19b1-49c9-b95e-ffcd88ee48e6
// last-edited: 2026-11-10

package audithttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// ---- helpers --------------------------------------------------------------

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

func wantStatus(t testing.TB, err error, status int, code string) {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok {
		t.Errorf("err = %v (%T), want an *httpapi.Error %d %s", err, err, status, code)
		return
	}
	if he.Status != status || he.Code != code {
		t.Errorf("err = %d %s (%v), want %d %s", he.Status, he.Code, err, status, code)
	}
}

// shown is what a client can see of err: the code, detail and fields.
func shown(t testing.TB, err error) string {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok {
		return ""
	}
	return he.Code + " " + he.Detail + " " + mustJSON(t, he.Fields)
}

func in(p *auth.Principal, req listReq) *httpapi.In[listReq] {
	return &httpapi.In[listReq]{Req: req, Principal: p}
}

func newAPI(r audit.Reader, rec Recorder) *api {
	return &api{reader: r, rec: rec, log: discard(), base: "/api/core/v1"}
}

// get lists with limit and the filters in req.
func get(t testing.TB, a *api, p *auth.Principal, req listReq) httpapi.CursorPage[AuditRecord] {
	t.Helper()
	page, err := a.list(context.Background(), in(p, req))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return page
}

func seqsOf(items []AuditRecord) []uint64 {
	out := make([]uint64, len(items))
	for i, r := range items {
		out[i] = r.Seq
	}
	return out
}

// exportOf runs the export handler as p over r with the given raw query.
func exportOf(a *api, p *auth.Principal, rawQuery string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/audit/export?"+rawQuery, nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rr := httptest.NewRecorder()
	a.export(rr, req)
	return rr
}

// lines is the NDJSON body's records.
func lines(t testing.TB, rr *httptest.ResponseRecorder) []AuditRecord {
	t.Helper()
	var out []AuditRecord
	sc := bufio.NewScanner(bytes.NewReader(rr.Body.Bytes()))
	for sc.Scan() {
		var rec AuditRecord
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, rec)
	}
	return out
}

// ---- the route table ------------------------------------------------------

var wantRoutes = []struct {
	method, path, name string
	kind               httpapi.RouteKind
	rate               httpapi.RateClass
}{
	{http.MethodGet, "/api/core/v1/audit", "listAudit", httpapi.RouteKindNative, httpapi.RateDefault},
	{http.MethodGet, "/api/core/v1/audit/export", "exportAudit", httpapi.RouteKindRaw, httpapi.RateExpensive},
}

func registered(t testing.TB) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	Register(r.CoreGroup(), Deps{})
	return r
}

// TestRegister_RouteTable pins 05-rbac §7.6's table: both routes need
// core:audit.view, are reads (no guard), and the export is RateExpensive.
func TestRegister_RouteTable(t *testing.T) {
	var got []httpapi.RouteInfo
	for ri := range registered(t).Routes() {
		got = append(got, ri)
	}
	if len(got) != len(wantRoutes) {
		t.Fatalf("%d routes registered, want %d: %+v", len(got), len(wantRoutes), got)
	}
	for _, w := range wantRoutes {
		i := slices.IndexFunc(got, func(ri httpapi.RouteInfo) bool { return ri.Method == w.method && ri.Pattern == w.path })
		if i < 0 {
			t.Errorf("no route %s %s", w.method, w.path)
			continue
		}
		ri := got[i]
		if ri.Owner != "core" || ri.Kind != w.kind || ri.Name != w.name {
			t.Errorf("%s = owner %s kind %d name %s, want core kind %d %s", w.path, ri.Owner, ri.Kind, ri.Name, w.kind, w.name)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != "core:audit.view" {
			t.Errorf("%s requires %s, want core:audit.view", w.name, ri.Perm)
		}
		if ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s has guards %+v, want none (a read)", w.name, ri.Guards)
		}
		if ri.RateClass != w.rate {
			t.Errorf("%s rate class = %d, want %d", w.name, ri.RateClass, w.rate)
		}
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses
// every non-Public route with ErrNoAuthService; a bad binding, a schema
// httpapi cannot describe, a non-snake_case name or a duplicate would each be
// a further error.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := registered(t).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != len(wantRoutes) {
		t.Errorf("Build reported %d problems, want %d: %v", len(problems), len(wantRoutes), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// ---- the guards -----------------------------------------------------------

// TestAuditRoutes_AuthDisabledIs401: the audit log holds user names, addresses
// and key ids, so the auth-disabled principal (User nil, method auth_disabled)
// is refused with 401 authentication_required (05-authn §4.11) even when its
// checker would hold the scope, and so is a request with no principal. The
// store is never read and no export is recorded.
func TestAuditRoutes_AuthDisabledIs401(t *testing.T) {
	everything := grants{held: []rbac.Permission{permView, permUsers}}
	for _, c := range []struct {
		name string
		p    *auth.Principal
	}{
		{"none", nil},
		{"user nil", &auth.Principal{Method: auth.MethodAuthDisabled, Perms: everything}},
		{"method only", &auth.Principal{User: &auth.User{ID: "u-ghost"}, Method: auth.MethodAuthDisabled, Perms: everything}},
		{"user nil on a session", &auth.Principal{Method: auth.MethodSession, Perms: everything}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rd, rec := &fakeReader{}, &recorder{}
			a := newAPI(rd, rec)
			_, err := a.list(context.Background(), in(c.p, listReq{}))
			wantStatus(t, err, http.StatusUnauthorized, "authentication_required")

			rr := exportOf(a, c.p, "")
			if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "authentication_required") {
				t.Errorf("export = %d %s, want 401 authentication_required", rr.Code, rr.Body)
			}
			if rd.callCount() != 0 || len(rec.recorded()) != 0 {
				t.Errorf("store read %d times and %d records written for a refused caller", rd.callCount(), len(rec.recorded()))
			}
		})
	}
}

// TestAuditRoutes_NeedAuditView: a principal whose checker lacks
// core:audit.view reads nothing (403), a route mounted without its reader is a
// 503, and an export without a Recorder does not run.
func TestAuditRoutes_NeedAuditView(t *testing.T) {
	e := newEnv(t)
	e.roster(t)
	rec := &recorder{}
	a := newAPI(e.store, rec)

	nobody := principal("u-alfred", "alfred") // holds nothing
	_, err := a.list(context.Background(), in(nobody, listReq{}))
	wantStatus(t, err, http.StatusForbidden, "permission_denied")
	if rr := exportOf(a, nobody, ""); rr.Code != http.StatusForbidden {
		t.Errorf("export without the scope = %d", rr.Code)
	}
	noChecker := &auth.Principal{User: &auth.User{ID: "u-alfred"}, Method: auth.MethodSession}
	_, err = a.list(context.Background(), in(noChecker, listReq{}))
	wantStatus(t, err, http.StatusForbidden, "permission_denied")

	// users.manage alone is not audit.view.
	_, err = a.list(context.Background(), in(principal("u-bruce", "bruce", permUsers), listReq{}))
	wantStatus(t, err, http.StatusForbidden, "permission_denied")
	if len(rec.recorded()) != 0 {
		t.Error("a refused export was recorded")
	}

	bare := newAPI(nil, rec)
	_, err = bare.list(context.Background(), in(commissioner(), listReq{}))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	if rr := exportOf(bare, commissioner(), ""); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("export without a reader = %d", rr.Code)
	}

	// An export that cannot be audited does not run.
	noRec := newAPI(e.store, nil)
	rr := exportOf(noRec, commissioner(), "")
	if rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), "core.auth.login") {
		t.Errorf("export without a recorder = %d %s", rr.Code, rr.Body)
	}
}

// ---- GET /audit -----------------------------------------------------------

// TestList_PagesNewestFirst walks a log in pages: newest first, each record
// once, the last page without a cursor, and a log of exactly limit records is
// one page with no next cursor while limit+1 is two.
func TestList_PagesNewestFirst(t *testing.T) {
	e := newEnv(t)
	var written []uint64
	for i := 0; i < 7; i++ {
		written = append(written, e.write(t, audit.Event{Action: "core.session.create", Outcome: audit.Success, Actor: actor("u-bruce", "bruce")}))
	}
	slices.Reverse(written)
	a := newAPI(e.store, nil)
	p := commissioner()

	var got []uint64
	req := listReq{CursorParams: httpapi.CursorParams{Limit: 3}}
	pages := 0
	for {
		page := get(t, a, p, req)
		pages++
		got = append(got, seqsOf(page.Items)...)
		if page.NextCursor == "" {
			if len(page.Items) == 0 || pages > 3 {
				t.Fatalf("last page = %+v after %d pages", page, pages)
			}
			break
		}
		if len(page.Items) != 3 {
			t.Fatalf("page %d has %d items before the end", pages, len(page.Items))
		}
		req.Cursor = page.NextCursor
	}
	if !slices.Equal(got, written) || pages != 3 {
		t.Errorf("walked %v in %d pages, want %v in 3", got, pages, written)
	}

	// The boundary: exactly limit records, then limit+1.
	small := newEnv(t)
	for i := 0; i < 3; i++ {
		small.write(t, audit.Event{Action: "core.session.create", Outcome: audit.Success})
	}
	sa := newAPI(small.store, nil)
	page := get(t, sa, p, listReq{CursorParams: httpapi.CursorParams{Limit: 3}})
	if len(page.Items) != 3 || page.NextCursor != "" {
		t.Errorf("exactly limit records = %d items, cursor %q; want 3 and none", len(page.Items), page.NextCursor)
	}
	small.write(t, audit.Event{Action: "core.session.create", Outcome: audit.Success})
	page = get(t, sa, p, listReq{CursorParams: httpapi.CursorParams{Limit: 3}})
	if len(page.Items) != 3 || page.NextCursor == "" {
		t.Fatalf("limit+1 records = %d items, cursor %q; want 3 and a cursor", len(page.Items), page.NextCursor)
	}
	rest := get(t, sa, p, listReq{CursorParams: httpapi.CursorParams{Limit: 3, Cursor: page.NextCursor}})
	if len(rest.Items) != 1 || rest.NextCursor != "" || rest.Items[0].Seq >= page.Items[2].Seq {
		t.Errorf("second page = %+v", rest)
	}

	// An empty log is an empty page, items [] not null.
	empty := get(t, newAPI(newEnv(t).store, nil), p, listReq{})
	if empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Errorf("empty log = %+v", empty)
	}
}

// TestList_BadCursorIs400: a cursor that is not one, was issued for other
// filters, or carries a position no cursor of this route carries is a 400
// invalid_cursor.
func TestList_BadCursorIs400(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 4; i++ {
		e.write(t, audit.Event{Action: "core.session.create", Outcome: audit.Success})
	}
	a := newAPI(e.store, nil)
	p := commissioner()
	page := get(t, a, p, listReq{CursorParams: httpapi.CursorParams{Limit: 2}})
	if page.NextCursor == "" {
		t.Fatal("no cursor to reuse")
	}
	hash := mustQuery(t, "", "", "", "").hash()
	for name, cursor := range map[string]string{
		"garbage":       "!!not-base64!!",
		"too short":     "AAAA",
		"short pos":     httpapi.EncodeCursor([]byte{1, 2, 3}, hash),
		"empty pos":     httpapi.EncodeCursor(nil, hash),
		"max seq":       httpapi.EncodeCursor(cursorPos(^uint64(0)), hash),
		"other filters": page.NextCursor,
	} {
		t.Run(name, func(t *testing.T) {
			req := listReq{CursorParams: httpapi.CursorParams{Limit: 2, Cursor: cursor}}
			if name == "other filters" {
				req.Outcome = "success"
			}
			_, err := a.list(context.Background(), in(p, req))
			wantStatus(t, err, http.StatusBadRequest, "invalid_cursor")
		})
	}
}

func mustQuery(t testing.TB, actor, action, outcome, target string, ts ...time.Time) query {
	t.Helper()
	var since, until time.Time
	if len(ts) > 0 {
		since = ts[0]
	}
	if len(ts) > 1 {
		until = ts[1]
	}
	q, err := parseQuery(since, until, actor, action, outcome, target)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// TestList_Filters: each filter narrows the store's answer, and filters
// combine.
func TestList_Filters(t *testing.T) {
	e := newEnv(t)
	s := e.roster(t)
	a := newAPI(e.store, nil)
	p := commissioner()
	// bruce disables selina at minute 3 of the roster (the clock starts at
	// epoch and moves a minute per record).
	at := func(min int) time.Time { return epoch.Add(time.Duration(min) * time.Minute) }

	for _, c := range []struct {
		name string
		req  listReq
		want []string // labels, newest first
	}{
		{"actor", listReq{Actor: "u-barbara"}, []string{"barbara-update", "patrol"}},
		{"action prefix", listReq{Action: "core.auth."}, []string{"dick-logout-noTarget", "anon-fail", "dick-login"}},
		{"outcome", listReq{Outcome: "denied"}, []string{"alfred-denied"}},
		{"outcome failure", listReq{Outcome: "failure"}, []string{"anon-fail"}},
		{"target module", listReq{Target: "books"}, []string{"patrol"}},
		{"target module and kind", listReq{Target: "core/user"}, []string{"barbara-update", "bruce-disables-selina", "dick-login"}},
		{"target full", listReq{Target: "core/user/u-selina"}, []string{"bruce-disables-selina"}},
		{"since", listReq{Since: at(6)}, []string{"dick-logout-noTarget", "alfred-denied"}},
		{"until", listReq{Until: at(2)}, []string{"patrol", "dick-login"}},
		{"since and until", listReq{Since: at(3), Until: at(5)}, []string{"barbara-update", "anon-fail", "bruce-disables-selina"}},
		{"combined", listReq{Action: "core.user.", Actor: "u-barbara"}, []string{"barbara-update"}},
		{"nothing matches", listReq{Actor: "u-nobody"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := seqsOf(get(t, a, p, c.req).Items)
			var want []uint64
			for _, l := range c.want {
				want = append(want, s[l])
			}
			if !slices.Equal(got, want) {
				t.Errorf("seqs = %v, want %v (%v)", got, want, c.want)
			}
		})
	}
}

// TestList_ValidatesFilters: every bad filter is a 422 at its own parameter,
// from Validate (bind time) and from the handler alike, and none reads the
// store.
func TestList_ValidatesFilters(t *testing.T) {
	long := strings.Repeat("x", 200)
	for _, c := range []struct {
		name string
		req  listReq
		want []string
	}{
		{"ok", listReq{Outcome: "denied", Action: "core.auth.", Target: "core/user/u-dick", Actor: "u-dick"}, nil},
		{"outcome", listReq{Outcome: "meh"}, []string{"/query/outcome"}},
		{"outcome case", listReq{Outcome: "Success"}, []string{"/query/outcome"}},
		{"action chars", listReq{Action: "Core.Auth"}, []string{"/query/action"}},
		{"action space", listReq{Action: "core auth"}, []string{"/query/action"}},
		{"action long", listReq{Action: strings.Repeat("a", maxActionLen+1)}, []string{"/query/action"}},
		{"actor long", listReq{Actor: long}, []string{"/query/actor"}},
		{"target empty module", listReq{Target: "/user/x"}, []string{"/query/target"}},
		{"target gap", listReq{Target: "core//x"}, []string{"/query/target"}},
		{"target trailing", listReq{Target: "core/user/"}, []string{"/query/target"}},
		{"target long", listReq{Target: "core/" + long}, []string{"/query/target"}},
		{"range", listReq{Since: epoch.Add(time.Hour), Until: epoch}, []string{"/query/since"}},
		{"several", listReq{Outcome: "meh", Action: "BAD", Actor: long}, []string{"/query/actor", "/query/action", "/query/outcome"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rd := &fakeReader{}
			a := newAPI(rd, nil)
			verr := c.req.Validate()
			_, herr := a.list(context.Background(), in(commissioner(), c.req))
			if c.want == nil {
				if verr != nil || herr != nil {
					t.Errorf("Validate = %v, handler = %v, want both nil", verr, herr)
				}
				return
			}
			for _, err := range []error{verr, herr} {
				wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
				he, _ := errors.AsType[*httpapi.Error](err)
				if he == nil {
					continue
				}
				var got []string
				for _, f := range he.Fields {
					got = append(got, f.Field)
				}
				slices.Sort(got)
				w := slices.Clone(c.want)
				slices.Sort(w)
				if !slices.Equal(got, w) {
					t.Errorf("fields = %v, want %v", got, w)
				}
				if strings.Contains(shown(t, err), long) {
					t.Error("the 422 echoes the value")
				}
			}
			if rd.callCount() != 0 {
				t.Error("a refused filter reached the store")
			}
		})
	}
}

// TestList_NoExtraFields: the wire record carries exactly the store's fields,
// snake_case, and empty details are omitted.
func TestList_NoExtraFields(t *testing.T) {
	e := newEnv(t)
	e.write(t, audit.Event{
		Action: "core.permission.deny", Outcome: audit.Denied, Actor: actor("u-alfred", "alfred"), Permission: "core:roles.manage",
		Route: "POST /api/core/v1/roles", RequestID: "req-1", TraceID: "trace-1", Reason: "scope_escalation",
		Target:  userRef("u-dick"),
		Details: map[string]any{"role": "custom.oracle"},
	})
	page := get(t, newAPI(e.store, nil), commissioner(), listReq{})
	var m map[string]any
	if err := json.Unmarshal([]byte(mustJSON(t, page.Items[0])), &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"action", "actor", "coalesced", "details", "id", "outcome", "permission", "reason", "request_id", "route", "seq", "target", "time", "trace_id"}
	if !slices.Equal(keys, want) {
		t.Errorf("record keys = %v, want %v", keys, want)
	}
	actorKeys := map[string]bool{}
	for k := range m["actor"].(map[string]any) {
		actorKeys[k] = true
	}
	for _, k := range []string{"user_id", "username", "method", "credential_id", "client_addr", "user_agent"} {
		if !actorKeys[k] {
			t.Errorf("actor lacks %s", k)
		}
	}
	rec := page.Items[0]
	if rec.Actor.UserID != "u-alfred" || rec.Target.ID != "u-dick" || rec.Reason != "scope_escalation" || !rec.Time.Equal(epoch.Add(time.Minute)) {
		t.Errorf("record = %+v", rec)
	}
	// No details: the key is absent.
	e.write(t, audit.Event{Action: "core.session.create", Outcome: audit.Success})
	page = get(t, newAPI(e.store, nil), commissioner(), listReq{CursorParams: Limit0(1)})
	if strings.Contains(mustJSON(t, page.Items[0]), `"details"`) {
		t.Errorf("empty details serialized: %s", mustJSON(t, page.Items[0]))
	}
}

// Limit0 is a CursorParams with the limit n, for a terse literal.
func Limit0(n int) httpapi.CursorParams { return httpapi.CursorParams{Limit: n} }

// TestList_RedactionHoldsEndToEnd: details are redacted when the store writes
// them (05-rbac §7.7); the routes pass what the store kept and add nothing
// back. A token under a secret-looking key and a credential in a value both
// read [redacted] in the list and in the export.
func TestList_RedactionHoldsEndToEnd(t *testing.T) {
	e := newEnv(t)
	const tok = "mck_0123456789abcdef0123456789abcdef"
	e.write(t, audit.Event{
		Action: "core.apikey.create", Outcome: audit.Success, Actor: actor("u-bruce", "bruce"),
		Details: map[string]any{"token": tok, "note": tok, "label": "batmobile", "nested": map[string]any{"password": "alfred-pennyworth-1939"}},
	})
	a := newAPI(e.store, &recorder{})
	page := get(t, a, commissioner(), listReq{})
	body := mustJSON(t, page)
	rr := exportOf(a, commissioner(), "")
	for what, blob := range map[string]string{"list": body, "export": rr.Body.String()} {
		if strings.Contains(blob, "0123456789abcdef") || strings.Contains(blob, "alfred-pennyworth") {
			t.Errorf("%s leaks a credential: %s", what, blob)
		}
		if strings.Count(blob, "[redacted]") != 3 || !strings.Contains(blob, "batmobile") {
			t.Errorf("%s details = %s, want 3 redactions and the label kept", what, blob)
		}
	}
}

// TestList_StoreFailureIs503: a store that fails is a 503 that shows the
// client nothing of the cause (it goes to the log), a store that fails after
// some records is still a 503, and a request whose context ended passes the
// context's error through.
func TestList_StoreFailureIs503(t *testing.T) {
	var logs bytes.Buffer
	rd := &fakeReader{err: errStore}
	a := &api{reader: rd, log: slog.New(slog.NewTextHandler(&logs, nil)), base: "/api/core/v1"}
	_, err := a.list(context.Background(), in(commissioner(), listReq{}))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	if blob := shown(t, err); strings.Contains(blob, "pebble") || strings.Contains(blob, "/var/lib") {
		t.Errorf("503 shows the cause: %s", blob)
	}
	if !strings.Contains(logs.String(), "corrupt block") {
		t.Errorf("the cause was not logged: %q", logs.String())
	}

	rd = &fakeReader{err: errStore, after: 2, recs: records(5)}
	a.reader = rd
	_, err = a.list(context.Background(), in(commissioner(), listReq{CursorParams: Limit0(10)}))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.reader = &fakeReader{recs: records(3)}
	_, err = a.list(ctx, in(commissioner(), listReq{}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled request = %v, want context.Canceled", err)
	}
	if _, ok := errors.AsType[*httpapi.Error](err); ok {
		t.Error("a cancelled request became an httpapi error")
	}
}

// records is n plain records, seq n down to 1.
func records(n int) []audit.Record {
	var out []audit.Record
	for i := n; i >= 1; i-- {
		out = append(out, audit.Record{Seq: uint64(i), ID: fmt.Sprint("r", i), Time: epoch, Event: audit.Event{Action: "core.session.create", Outcome: audit.Success}})
	}
	return out
}

// ---- visibility -----------------------------------------------------------

// TestVisibility_AuthAndUserRecords is 05-rbac §7.6's rule: a record whose
// action is core.auth.* or core.user.* is visible to a holder of
// core:users.manage, and to anyone else only when it is about themselves;
// everything else is visible to every audit viewer. A withheld record is
// absent whole, from the list and the export alike.
func TestVisibility_AuthAndUserRecords(t *testing.T) {
	e := newEnv(t)
	s := e.roster(t)
	a := newAPI(e.store, &recorder{})

	label := func(items []AuditRecord) []string {
		inv := map[uint64]string{}
		for l, q := range s {
			inv[q] = l
		}
		var out []string
		for _, it := range items {
			out = append(out, inv[it.Seq])
		}
		return out
	}
	for _, c := range []struct {
		name string
		p    *auth.Principal
		want []string
	}{
		{"commissioner sees all", commissioner(), []string{"dick-logout-noTarget", "alfred-denied", "barbara-update", "anon-fail", "bruce-disables-selina", "patrol", "dick-login"}},
		// dick: his own login (target him), his own logout (no user target,
		// actor him), the patrol and the denial (not auth or user actions).
		{"dick sees his own", auditor("u-dick", "dick"), []string{"dick-logout-noTarget", "alfred-denied", "patrol", "dick-login"}},
		// barbara: her own update (target her) and the non-auth records; not
		// dick's login or the anonymous failure.
		{"barbara sees her own", auditor("u-barbara", "barbara"), []string{"alfred-denied", "barbara-update", "patrol"}},
		// selina is the target of a disable done by someone else: about her.
		{"selina sees the disable about her", auditor("u-selina", "selina"), []string{"alfred-denied", "bruce-disables-selina", "patrol"}},
		// alfred has nothing about himself in the user or auth records.
		{"alfred sees none", auditor("u-alfred", "alfred"), []string{"alfred-denied", "patrol"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, limit := range []int{1, 2, 100} {
				var got []AuditRecord
				req := listReq{CursorParams: Limit0(limit)}
				for pages := 0; ; pages++ {
					page := get(t, a, c.p, req)
					got = append(got, page.Items...)
					if page.NextCursor == "" {
						break
					}
					if pages > 20 {
						t.Fatalf("limit %d never ended", limit)
					}
					req.Cursor = page.NextCursor
				}
				if !slices.Equal(label(got), c.want) {
					t.Errorf("limit %d: list = %v, want %v", limit, label(got), c.want)
				}
			}
			rr := exportOf(a, c.p, "")
			if rr.Code != http.StatusOK {
				t.Fatalf("export = %d %s", rr.Code, rr.Body)
			}
			if got := label(lines(t, rr)); !slices.Equal(got, c.want) {
				t.Errorf("export = %v, want %v", got, c.want)
			}
		})
	}
}

// TestVisibility_WithheldRecordsDoNotUseUpAPage: a page of N holds N visible
// records however many withheld ones lie between them, and the walk meets the
// visible ones beyond a long withheld run.
func TestVisibility_WithheldRecordsDoNotUseUpAPage(t *testing.T) {
	e := newEnv(t)
	first := e.write(t, audit.Event{Action: "books.library.scan", Outcome: audit.Success, Actor: actor("u-dick", "dick")})
	for i := 0; i < 12; i++ {
		e.write(t, audit.Event{Action: "core.auth.login", Outcome: audit.Success, Actor: actor("u-selina", "selina"), Target: userRef("u-selina")})
	}
	last := e.write(t, audit.Event{Action: "books.library.scan", Outcome: audit.Success, Actor: actor("u-dick", "dick")})
	a := newAPI(e.store, nil)
	p := auditor("u-dick", "dick")

	page := get(t, a, p, listReq{CursorParams: Limit0(1)})
	if got := seqsOf(page.Items); !slices.Equal(got, []uint64{last}) || page.NextCursor == "" {
		t.Fatalf("first page = %v cursor %q", got, page.NextCursor)
	}
	page = get(t, a, p, listReq{CursorParams: httpapi.CursorParams{Limit: 1, Cursor: page.NextCursor}})
	if got := seqsOf(page.Items); !slices.Equal(got, []uint64{first}) || page.NextCursor != "" {
		t.Errorf("second page = %v cursor %q, want the record past the withheld run and no cursor", got, page.NextCursor)
	}
}

// ---- GET /audit/export ----------------------------------------------------

// TestExport_StreamsNDJSONAndRecordsItself: the export is one JSON record per
// line, newest first, with the headers of a download; it records
// core.audit.export (the caller, the filters) and the record is made before
// the first byte is sent.
func TestExport_StreamsNDJSONAndRecordsItself(t *testing.T) {
	e := newEnv(t)
	s := e.roster(t)
	rr := httptest.NewRecorder()
	rec := &recorder{}
	sentWhenRecorded := -1
	rec.during = func() { sentWhenRecorded = rr.Body.Len() + len(rr.Header()) }
	a := newAPI(e.store, rec)

	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/audit/export?outcome=success&action=core.", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), commissioner()))
	a.export(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rr.Code, rr.Body)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != `attachment; filename="audit-export.ndjson"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	got := seqsOf(lines(t, rr))
	want := []uint64{s["dick-logout-noTarget"], s["barbara-update"], s["bruce-disables-selina"], s["dick-login"]}
	if !slices.Equal(got, want) {
		t.Errorf("exported seqs = %v, want %v", got, want)
	}
	if !strings.HasSuffix(rr.Body.String(), "\n") {
		t.Error("the last line has no newline")
	}
	if sentWhenRecorded != 0 {
		t.Errorf("%d bytes/headers were sent before the export was recorded", sentWhenRecorded)
	}

	evs := rec.recorded()
	if len(evs) != 1 {
		t.Fatalf("recorded %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Action != "core.audit.export" || ev.Outcome != audit.Success || ev.Permission != "core:audit.view" || ev.Route != "GET /api/core/v1/audit/export" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Actor.UserID != "u-bruce" || ev.Actor.Username != "bruce" || ev.Actor.Method != "session" || ev.Actor.ClientAddr != "192.0.2.10" || ev.Actor.CredentialID != "sess-batcave" {
		t.Errorf("actor = %+v", ev.Actor)
	}
	if ev.Details["outcome"] != "success" || ev.Details["action"] != "core." {
		t.Errorf("details = %v, want the filters used", ev.Details)
	}
	if _, ok := ev.Details["actor"]; ok {
		t.Errorf("details name a filter that was not set: %v", ev.Details)
	}

	// An actor the middleware bound is the one recorded.
	rec2 := &recorder{}
	bound := audit.Actor{UserID: "u-dick", Username: "dick", Method: "session"}
	req = httptest.NewRequest(http.MethodGet, "/api/core/v1/audit/export", nil)
	req = req.WithContext(audit.WithActor(auth.WithPrincipal(req.Context(), commissioner()), bound))
	newAPI(e.store, rec2).export(httptest.NewRecorder(), req)
	if evs := rec2.recorded(); len(evs) != 1 || evs[0].Actor != bound {
		t.Errorf("bound actor not used: %+v", evs)
	}
}

// TestExport_RecordIsInTheRealLog: with the real store as the Recorder the
// export shows up in the log it exports, with the filters, and not in its own
// stream.
func TestExport_RecordIsInTheRealLog(t *testing.T) {
	e := newEnv(t)
	e.roster(t)
	a := newAPI(e.store, e.store)
	p := commissioner()
	rr := exportOf(a, p, "actor=u-barbara")
	if got := lines(t, rr); len(got) != 2 {
		t.Fatalf("export = %d records, want 2", len(got))
	}
	page := get(t, a, p, listReq{Action: "core.audit."})
	if len(page.Items) != 1 {
		t.Fatalf("export records = %+v", page.Items)
	}
	rec := page.Items[0]
	if rec.Actor.UserID != "u-bruce" || rec.Details["actor"] != "u-barbara" || rec.Outcome != "success" {
		t.Errorf("export record = %+v", rec)
	}
	// A second export sees the first one's record.
	rr = exportOf(a, p, "action=core.audit.")
	if got := lines(t, rr); len(got) != 1 || got[0].Action != "core.audit.export" {
		t.Errorf("second export = %+v", got)
	}
}

// TestExport_FailsClosed: a record that cannot be written, a store that is
// down and a bad filter each leave the stream unsent, and only the first two
// are 503 (the third a 422); an unwritten record means no export.
func TestExport_FailsClosed(t *testing.T) {
	e := newEnv(t)
	e.roster(t)
	secret := e.store

	// Record fails: 503, no record content in the body, nothing audited.
	rec := &recorder{fail: errors.New("audit batch: disk on fire at /var/lib/gotham")}
	rr := exportOf(newAPI(secret, rec), commissioner(), "")
	if rr.Code != http.StatusServiceUnavailable || rr.Header().Get("Content-Type") == "application/x-ndjson" {
		t.Errorf("record failure = %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	for _, leak := range []string{"core.auth.login", "disk on fire", "/var/lib"} {
		if strings.Contains(rr.Body.String(), leak) {
			t.Errorf("record failure body shows %q: %s", leak, rr.Body)
		}
	}

	// The store is down at the first read: 503, and no export is recorded
	// for an export that never ran.
	rec = &recorder{}
	rr = exportOf(newAPI(&fakeReader{err: errStore}, rec), commissioner(), "")
	if rr.Code != http.StatusServiceUnavailable || len(rec.recorded()) != 0 {
		t.Errorf("store down = %d, %d records written", rr.Code, len(rec.recorded()))
	}
	if strings.Contains(rr.Body.String(), "pebble") || strings.Contains(rr.Body.String(), "/var/lib") {
		t.Errorf("store-down body shows the cause: %s", rr.Body)
	}

	// Bad filters are 422 and read nothing.
	rd := &fakeReader{}
	for _, q := range []string{"outcome=meh", "since=yesterday", "until=2026-13-45T00:00:00Z", "action=CORE", "target=core//x"} {
		rr = exportOf(newAPI(rd, rec), commissioner(), q)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("?%s = %d, want 422", q, rr.Code)
		}
	}
	if rd.callCount() != 0 || len(rec.recorded()) != 0 {
		t.Error("a refused export read the store or was recorded")
	}

	// An empty log is a 200 with no lines, still recorded.
	rec = &recorder{}
	rr = exportOf(newAPI(newEnv(t).store, rec), commissioner(), "")
	if rr.Code != http.StatusOK || rr.Body.Len() != 0 || len(rec.recorded()) != 1 {
		t.Errorf("empty export = %d %q, %d records", rr.Code, rr.Body, len(rec.recorded()))
	}
}

// TestExport_MidStreamFailureEndsWithAMarker: a read that fails after lines
// were sent ends the stream with {"error":"export_incomplete"} and nothing of
// the cause.
func TestExport_MidStreamFailureEndsWithAMarker(t *testing.T) {
	var logs bytes.Buffer
	rd := &fakeReader{err: errStore, after: 2, recs: records(5)}
	a := &api{reader: rd, rec: &recorder{}, log: slog.New(slog.NewTextHandler(&logs, nil)), base: "/api/core/v1"}
	rr := exportOf(a, commissioner(), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("export = %d", rr.Code)
	}
	ls := strings.Split(strings.TrimSuffix(rr.Body.String(), "\n"), "\n")
	if len(ls) != 3 || ls[2] != `{"error":"export_incomplete"}` {
		t.Fatalf("lines = %q", ls)
	}
	if strings.Contains(rr.Body.String(), "pebble") || strings.Contains(rr.Body.String(), "/var/lib") {
		t.Errorf("the stream shows the cause: %s", rr.Body)
	}
	if !strings.Contains(logs.String(), "corrupt block") {
		t.Errorf("the cause was not logged: %q", logs.String())
	}
}

// plainWriter is a ResponseWriter that cannot flush; failWriter's writes fail.
type plainWriter struct {
	h    http.Header
	body bytes.Buffer
	code int
}

func (w *plainWriter) Header() http.Header         { return w.h }
func (w *plainWriter) Write(b []byte) (int, error) { return w.body.Write(b) }
func (w *plainWriter) WriteHeader(c int)           { w.code = c }

type failWriter struct{ plainWriter }

func (w *failWriter) Write([]byte) (int, error) { return 0, errors.New("client went away") }

// TestExport_FlushesAndSurvivesWriters: a large export is flushed as it goes
// and complete; a writer that cannot flush still gets every line; a writer
// that fails stops the export without a panic or a loop.
func TestExport_FlushesAndSurvivesWriters(t *testing.T) {
	const n = 2*flushEvery + 50
	a := newAPI(&fakeReader{recs: records(n)}, &recorder{})
	rr := exportOf(a, commissioner(), "")
	if got := len(lines(t, rr)); got != n || !rr.Flushed {
		t.Errorf("export = %d lines (flushed %v), want %d flushed", got, rr.Flushed, n)
	}

	req := httptest.NewRequest(http.MethodGet, "/audit/export", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), commissioner()))
	pw := &plainWriter{h: http.Header{}}
	a.export(pw, req)
	if got := strings.Count(pw.body.String(), "\n"); got != n {
		t.Errorf("non-flushing writer got %d lines, want %d", got, n)
	}

	fw := &failWriter{plainWriter{h: http.Header{}}}
	a.export(fw, req) // must return
}

// TestRecords_PagesThroughBatches: the reader loop asks again from the last
// seq it saw until a batch comes back short, so a log longer than a batch is
// read whole, once, in order, and a withheld record does not stop it.
func TestRecords_PagesThroughBatches(t *testing.T) {
	rd := &fakeReader{recs: records(10)}
	a := newAPI(rd, nil)
	var got []uint64
	for rec, err := range a.records(context.Background(), audit.Filter{}, 3, func(r audit.Record) bool { return r.Seq%4 != 0 }) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rec.Seq)
	}
	if want := []uint64{10, 9, 7, 6, 5, 3, 2, 1}; !slices.Equal(got, want) {
		t.Errorf("records = %v, want %v", got, want)
	}
	var seen []uint64
	for _, f := range rd.calls {
		seen = append(seen, f.BeforeSeq)
	}
	if want := []uint64{0, 8, 5, 2}; !slices.Equal(seen, want) {
		t.Errorf("BeforeSeq per batch = %v, want %v", seen, want)
	}

	// Stopping early stops reading.
	rd = &fakeReader{recs: records(10)}
	a = newAPI(rd, nil)
	for range a.records(context.Background(), audit.Filter{}, 3, func(audit.Record) bool { return true }) {
		break
	}
	if rd.callCount() != 1 {
		t.Errorf("an abandoned read made %d queries", rd.callCount())
	}

	// A record with seq 0 (nothing real has one) cannot loop forever.
	rd = &fakeReader{recs: []audit.Record{{Seq: 0}, {Seq: 0}}}
	a = newAPI(rd, nil)
	n := 0
	for range a.records(context.Background(), audit.Filter{}, 2, func(audit.Record) bool { return true }) {
		n++
	}
	if n != 2 || rd.callCount() != 1 {
		t.Errorf("seq-0 batch: %d records in %d queries", n, rd.callCount())
	}
}
