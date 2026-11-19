// file: core/internal/loghttp/permissions_test.go
// version: 1.3.0
// guid: fcea5e18-897d-4f58-8688-b96d12f281ea
// last-edited: 2026-11-19

package loghttp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// TestJournal_RequiresSystemManage (the route half, 05-logging §10): the
// registered route needs core:system.manage and a principal holding it gets
// 200's body. The 403 and 401 cells are the router's stage 13, which arrives
// with F4-08; F5-18, which mounts the route after it, deletes the skips. The
// handler checks no permission itself (enforcement lives in one place).
func TestJournal_RequiresSystemManage(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	if err := Register(r.CoreGroup(), Deps{Journal: &fakeJournal{}}); err != nil {
		t.Fatal(err)
	}
	var routes []httpapi.RouteInfo
	for ri := range r.Routes() {
		routes = append(routes, ri)
	}
	if len(routes) != 1 {
		t.Fatalf("%d routes registered, want 1: %+v", len(routes), routes)
	}
	ri := routes[0]
	if ri.Method != http.MethodGet || ri.Pattern != "/api/core/v1/system/logs" || ri.Name != "listSystemLogs" ||
		ri.Owner != "core" || ri.Kind != httpapi.RouteKindNative || ri.RateClass != httpapi.RateDefault ||
		ri.Guards != (httpapi.Guards{}) {
		t.Errorf("route = %+v", ri)
	}
	if ri.Perm.String() != rbac.Need("core:system.manage").String() {
		t.Errorf("route needs %s, want core:system.manage", ri.Perm)
	}

	t.Run("system_manage_principal_gets_the_page", func(t *testing.T) {
		fj := &fakeJournal{page: log.JournalPage{Records: []log.Record{{Time: at(0), Level: log.LevelWarn, Module: "core", Message: "alarm"}}}}
		v := mustCall(t, newAPI(fj, nil), principal("u-bruce", "core:system.manage"), queryRequest{})
		if !slices.Equal(messages(v), []string{"alarm"}) {
			t.Errorf("records = %v", messages(v))
		}
		if r := v.Items[0]; r.Level != "warn" || r.Module != "core" || !r.Time.Equal(at(0)) || r.Attrs == nil {
			t.Errorf("record on the wire = %+v, want level warn (lower case), module core, its time, a non-nil attrs", r)
		}
	})

	// Stage-13 table: the router answers, the handler is never reached.
	for _, c := range []struct {
		name  string
		p     *auth.Principal
		want  int
		wantC string
	}{
		{"system_view_only_gets_403", principal("u-dick", "core:system.view"), http.StatusForbidden, "permission_denied"},
		{"no_principal_gets_401", nil, http.StatusUnauthorized, "authentication_required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
		})
	}

	// The event half (F6-05, 05-logging §10 "from F6, with realtime"): the same
	// permission on the journal's live tail. A hub built here plays the kernel's
	// part (F6-03); the principals carry real compiled checkers, so
	// core:system.manage is AuthRequired and core:system.view is not enough.
	t.Run("event_half", func(t *testing.T) {
		rig := newTailRig(t, nil)
		rig.sign("u-dick", "core:system.view")
		rig.sign("u-bruce", permManage)
		const topic = "/?topic=" + logjournal.TailTopicName

		if _, status, code := rig.open("u-dick", topic); status != http.StatusForbidden || code != "topic_not_permitted" {
			t.Errorf("system.view-only on topic=%s: %d %q, want 403 topic_not_permitted before the stream", logjournal.TailTopicName, status, code)
		}
		dick, _, _ := rig.open("u-dick", "/?topic=*")
		bruce, _, _ := rig.open("u-bruce", "/?topic=*")
		if dick == nil || bruce == nil {
			t.Fatal("a wildcard stream was refused")
		}
		if slices.Contains(dick.opened, logjournal.TailTopicName) || !slices.Contains(bruce.opened, logjournal.TailTopicName) {
			t.Fatalf("topic=* opened dick %v, bruce %v", dick.opened, bruce.opened)
		}
		rig.l.Module("core").Warn("the Batcave alarm rang")
		if got := bruce.next(t); got.Message != "the Batcave alarm rang" {
			t.Errorf("system.manage subscriber got %+v", got)
		}
		dick.quiet(t, 300*time.Millisecond)
	})
}

// A books-style predicate (06 LogOtherUserData): a record naming selina is her
// data unless she is the viewer. It reads the message, so it classifies an
// imported record, which carries no attributes, as it does a new one.
func namesSelina(rec log.Record, viewer string) bool {
	return strings.Contains(strings.ToLower(rec.Message), "selina") && viewer != "u-selina"
}

func seedUserLines(t testing.TB, st *logjournal.Store) {
	t.Helper()
	ctx := context.Background()
	imported := []log.ImportedRecord{
		{SourceID: "syslog:0001", Time: at(1e9), Level: log.LevelInfo, Module: "books", Subsystem: "users",
			Message: "user selina renamed"},
		{SourceID: "syslog:0002", Time: at(2e9), Level: log.LevelInfo, Module: "books", Subsystem: "scanner",
			Message: "imported scan done", Attrs: []slog.Attr{slog.String("user_id", "u-dick")}},
	}
	if err := logjournal.NewImporter(st).Import(ctx, imported); err != nil {
		t.Fatal(err)
	}
	fresh := []log.Record{
		{Time: at(3e9), Level: log.LevelInfo, Module: "core", Subsystem: "users", Message: "selina's password was reset",
			Attrs: []slog.Attr{slog.String("user_id", "u-selina")}},
		{Time: at(4e9), Level: log.LevelInfo, Module: "core", Subsystem: "scanner", Message: "scan finished",
			Attrs: []slog.Attr{slog.String("user_id", "u-bruce"), slog.String("request_id", "req-1")}},
		{Time: at(5e9), Level: log.LevelInfo, Module: "core", Subsystem: "http", Message: "grouped attribute",
			Attrs: []slog.Attr{slog.Group("ctx", slog.String("user_id", "u-alfred"))}},
		{Time: at(6e9), Level: log.LevelInfo, Module: "core", Subsystem: "http", Message: "no user at all",
			Attrs: []slog.Attr{slog.Int("status", 204)}},
	}
	if err := st.Append(ctx, fresh); err != nil {
		t.Fatal(err)
	}
}

func byMessage(v pageView) map[string]recordView {
	m := map[string]recordView{}
	for _, r := range v.Items {
		m[r.Message] = r
	}
	return m
}

// TestJournal_OtherUsersLinesNeedUsersView (05-logging §10): over a journal of
// imported and new records, a system.manage principal without users.view gets
// neither the imported nor the new record the predicate flags and sees user_id
// blank on the rest; with users.view it gets every record with user_id intact.
func TestJournal_OtherUsersLinesNeedUsersView(t *testing.T) {
	st := openJournal(t)
	seedUserLines(t, st)
	a := newAPI(st, namesSelina)

	t.Run("without_users_view", func(t *testing.T) {
		v := mustCall(t, a, principal("u-bruce", permManage), queryRequest{})
		got := byMessage(v)
		for _, flagged := range []string{"user selina renamed", "selina's password was reset"} {
			if _, ok := got[flagged]; ok {
				t.Errorf("%q (flagged) was served", flagged)
			}
		}
		if len(v.Items) != 4 {
			t.Fatalf("%d records %v, want 4", len(v.Items), messages(v))
		}
		for msg, key := range map[string]string{"imported scan done": "user_id", "scan finished": "user_id", "grouped attribute": "ctx.user_id"} {
			val, ok := got[msg].Attrs[key]
			if !ok || val != "" {
				t.Errorf("%q: %s = %q (present %v), want present and blank", msg, key, val, ok)
			}
		}
		if got["scan finished"].Attrs["request_id"] != "req-1" {
			t.Errorf("request_id was touched: %v", got["scan finished"].Attrs)
		}
		if _, ok := got["no user at all"].Attrs["user_id"]; ok || got["no user at all"].Attrs["status"] != "204" {
			t.Errorf("a record with no user_id changed: %v", got["no user at all"].Attrs)
		}
	})

	t.Run("with_users_view", func(t *testing.T) {
		v := mustCall(t, a, principal("u-bruce", permManage, permUsers), queryRequest{})
		if len(v.Items) != 6 {
			t.Fatalf("%d records %v, want all 6", len(v.Items), messages(v))
		}
		got := byMessage(v)
		if got["imported scan done"].Attrs["user_id"] != "u-dick" || got["scan finished"].Attrs["user_id"] != "u-bruce" ||
			got["grouped attribute"].Attrs["ctx.user_id"] != "u-alfred" || got["selina's password was reset"].Attrs["user_id"] != "u-selina" {
			t.Errorf("user_id changed for a viewer holding users.view: %+v", v.Items)
		}
	})

	t.Run("the_subject_herself_still_needs_users_view_for_flagged_to_pass", func(t *testing.T) {
		// The predicate sees the viewer's id: selina is not "another user" to herself.
		v := mustCall(t, a, principal("u-selina", permManage), queryRequest{})
		if _, ok := byMessage(v)["selina's password was reset"]; !ok {
			t.Errorf("the viewer's own flagged-for-others line was withheld: %v", messages(v))
		}
	})

	t.Run("no_predicate_registered_blanks_without_omitting", func(t *testing.T) {
		v := mustCall(t, newAPI(st, nil), principal("u-bruce", permManage), queryRequest{})
		if len(v.Items) != 6 {
			t.Fatalf("%d records, want 6 (nothing to flag)", len(v.Items))
		}
		for _, r := range v.Items {
			for k, val := range r.Attrs {
				if (k == "user_id" || strings.HasSuffix(k, ".user_id")) && val != "" {
					t.Errorf("%q: %s = %q not blanked", r.Message, k, val)
				}
			}
		}
	})

	t.Run("the_store_is_not_modified", func(t *testing.T) {
		mustCall(t, a, principal("u-bruce", permManage), queryRequest{})
		p, err := st.Query(context.Background(), log.JournalQuery{MinLevel: log.LevelTrace, Limit: 1000})
		if err != nil || len(p.Records) != 6 {
			t.Fatalf("store: %d records, %v", len(p.Records), err)
		}
		for _, r := range p.Records {
			if r.Message == "scan finished" && r.Attrs[0].Value.String() != "u-bruce" {
				t.Errorf("blanking wrote through to the store: %v", r.Attrs)
			}
		}
	})
}

// walk pages through the route with limit, following next_cursor, and returns
// each page's messages and cursor, so two walks can be compared page by page.
func walk(t testing.TB, a *api, p *auth.Principal, req queryRequest, limit int) (pages [][]string, cursors []string) {
	t.Helper()
	cursor := ""
	for range 400 {
		req.CursorParams = paged(limit, cursor)
		v := mustCall(t, a, p, req)
		pages = append(pages, messages(v))
		cursors = append(cursors, v.NextCursor)
		if v.NextCursor == "" {
			return pages, cursors
		}
		cursor = v.NextCursor
	}
	t.Fatal("a walk did not end in 400 pages")
	return nil, nil
}

// TestJournal_HiddenRecordsLeaveNoTrace (the other-user rule, 05-logging §5):
// the caller who may not read another user's lines cannot tell from a page, its
// length or its next_cursor that one exists. Each case is the reviewer's trace
// on the fixture of seedUserLines.
func TestJournal_HiddenRecordsLeaveNoTrace(t *testing.T) {
	st := openJournal(t)
	seedUserLines(t, st)
	a := newAPI(st, namesSelina)
	bruce := principal("u-bruce", permManage)

	t.Run("a_query_matching_only_hidden_lines_is_a_query_matching_nothing", func(t *testing.T) {
		hidden := mustCall(t, a, bruce, queryRequest{Q: "selina", CursorParams: paged(1, "")})
		none := mustCall(t, a, bruce, queryRequest{Q: "no such line", CursorParams: paged(1, "")})
		if len(hidden.Items) != 0 || hidden.NextCursor != "" {
			t.Errorf("q=selina&limit=1: %d items, next_cursor %q; want none and no cursor", len(hidden.Items), hidden.NextCursor)
		}
		if len(none.Items) != 0 || none.NextCursor != "" {
			t.Fatalf("a query matching nothing: %d items, next_cursor %q", len(none.Items), none.NextCursor)
		}
	})

	t.Run("a_trailing_hidden_match_does_not_set_next_cursor", func(t *testing.T) {
		// "user selina renamed" also matches q=user and is the oldest match.
		v := mustCall(t, a, bruce, queryRequest{Q: "user", CursorParams: paged(1, "")})
		if !slices.Equal(messages(v), []string{"no user at all"}) || v.NextCursor != "" {
			t.Errorf("q=user&limit=1: %v next_cursor %q; want [no user at all] and no cursor", messages(v), v.NextCursor)
		}
	})

	t.Run("a_walk_with_hidden_lines_equals_a_walk_without_them", func(t *testing.T) {
		clean := openJournal(t)
		seedUserLines(t, clean)
		// The same journal minus the two lines the predicate flags.
		visibleOnly := openJournal(t)
		all, err := clean.Query(context.Background(), log.JournalQuery{MinLevel: log.LevelTrace, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range slices.Backward(all.Records) {
			if !namesSelina(r, "u-bruce") {
				if err := visibleOnly.Append(context.Background(), []log.Record{r}); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, limit := range []int{1, 2, 3} {
			withP, withC := walk(t, a, bruce, queryRequest{}, limit)
			wo, woC := walk(t, newAPI(visibleOnly, namesSelina), bruce, queryRequest{}, limit)
			if !slices.EqualFunc(withP, wo, slices.Equal[[]string]) {
				t.Errorf("limit %d: pages differ with the hidden lines present:\n%v\n%v", limit, withP, wo)
			}
			if len(withC) != len(woC) || (len(withC) > 0 && (withC[len(withC)-1] != "" || woC[len(woC)-1] != "")) {
				t.Errorf("limit %d: cursors %v vs %v", limit, withC, woC)
			}
		}
	})

	t.Run("a_full_page_holds_served_lines_only", func(t *testing.T) {
		// Newest first: no user at all, grouped attribute, scan finished, (hidden),
		// imported scan done, (hidden). limit 3 is three served lines.
		v := mustCall(t, a, bruce, queryRequest{CursorParams: paged(3, "")})
		if !slices.Equal(messages(v), []string{"no user at all", "grouped attribute", "scan finished"}) {
			t.Fatalf("page = %v", messages(v))
		}
		next := mustCall(t, a, bruce, queryRequest{CursorParams: paged(3, v.NextCursor)})
		if !slices.Equal(messages(next), []string{"imported scan done"}) || next.NextCursor != "" {
			t.Errorf("second page = %v next_cursor %q, want [imported scan done] and no cursor", messages(next), next.NextCursor)
		}
	})

	t.Run("a_long_run_of_hidden_lines_is_skipped", func(t *testing.T) {
		long := openJournal(t)
		var in []log.Record
		in = append(in, log.Record{Time: at(0), Level: log.LevelInfo, Module: "core", Message: "the one served line"})
		for i := range 2500 {
			in = append(in, log.Record{Time: at(time.Duration(i+1) * time.Second), Level: log.LevelInfo, Module: "core",
				Message: "selina signed in"})
		}
		if err := long.Append(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		v := mustCall(t, newAPI(long, namesSelina), bruce, queryRequest{CursorParams: paged(1, "")})
		if !slices.Equal(messages(v), []string{"the one served line"}) || v.NextCursor != "" {
			t.Errorf("one served line behind 2500 hidden: %v next_cursor %q", messages(v), v.NextCursor)
		}
	})

	t.Run("users_view_sees_everything_and_the_cursor_names_real_lines", func(t *testing.T) {
		v := mustCall(t, a, principal("u-bruce", permManage, permUsers), queryRequest{Q: "selina", CursorParams: paged(1, "")})
		if len(v.Items) != 1 || v.NextCursor == "" {
			t.Errorf("with users.view: %d items next_cursor %q, want 1 and a cursor", len(v.Items), v.NextCursor)
		}
	})
}

// tailRig is a bus, a hub, the journal tap and the live tail over one logger,
// wired as the kernel will wire them at F6-03, with the query route's api over
// the same journal.
type tailRig struct {
	t   *testing.T
	l   *log.Logging
	tap *logjournal.Tap
	st  *logjournal.Store
	api *api
	srv *httptest.Server

	mu      sync.Mutex
	roster  map[string]*auth.Principal
	cancels []context.CancelFunc // the open streams' requests, ended before the server closes
}

// realPrincipal is a signed-in principal holding exactly perms, with the real
// compiled checker of authz over core's scope catalog.
func realPrincipal(t testing.TB, id string, perms ...rbac.Permission) *auth.Principal {
	t.Helper()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	return &auth.Principal{
		User: &auth.User{ID: auth.UserID(id), Username: strings.TrimPrefix(id, "u-")}, Method: auth.MethodSession,
		Perms:      authz.Compile(rbac.CompileInput{Interactive: true, RolePatterns: perms}, cat),
		ClientAddr: netip.MustParseAddr("192.0.2.10"), CredentialID: "sess-" + id,
	}
}

type liveAuth struct{}

func (liveAuth) Revalidate(context.Context, *auth.Principal) error { return nil }

func newTailRig(t *testing.T, pred log.OtherUserPredicate) *tailRig {
	t.Helper()
	r := &tailRig{t: t, roster: map[string]*auth.Principal{}, st: openJournal(t)}
	var err error
	if r.l, err = log.New(log.Options{Output: io.Discard, Format: log.FormatText, Level: slog.LevelDebug, Redactor: log.NewRedactor(nil)}); err != nil {
		t.Fatal(err)
	}
	set := logjournal.Settings{Enabled: true, MinLevel: "debug", RetentionDays: 90, QueueSize: 4096}
	if r.tap, err = logjournal.StartTap(r.l, r.st, set); err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", logjournal.TailSpec(pred)); err != nil {
		t.Fatal(err)
	}
	// AllowNoControlTopics: the control topics are F4-08's, not under test.
	hub, err := realtime.NewHub(realtime.Options{Heartbeat: time.Minute, AllowNoControlTopics: true}, realtime.Deps{
		Auth: liveAuth{}, Catalog: bus.Catalog(),
		Refresh: func(_ context.Context, p *auth.Principal) (*auth.Principal, error) { return p, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	subs, err := hub.Subscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Subscribe("core", subs...); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	tail, err := logjournal.StartTail(r.l, bus, set)
	if err != nil {
		t.Fatal(err)
	}
	r.api = newAPI(r.st, pred)
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		p := r.roster[req.Header.Get("X-Test-Principal")]
		r.mu.Unlock()
		if p != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), p))
		}
		hub.Handler().ServeHTTP(w, req)
	}))
	t.Cleanup(func() {
		r.mu.Lock()
		for _, c := range r.cancels {
			c()
		}
		r.mu.Unlock()
		r.srv.Close()
		tail.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Close(ctx)
		_ = bus.Close(ctx)
		_ = r.l.Close(ctx)
	})
	return r
}

func (r *tailRig) sign(id string, perms ...rbac.Permission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roster[id] = realPrincipal(r.t, id, perms...)
}

// tailStream is an open stream whose tail records are read on a goroutine.
type tailStream struct {
	opened []string
	recs   chan json.RawMessage // each tail record's data member, as sent
	done   chan struct{}
}

// open GETs path as id: a stream on 200, otherwise the status and problem code.
func (r *tailRig) open(id, path string) (*tailStream, int, string) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancels = append(r.cancels, cancel)
	r.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+path, nil)
	if err != nil {
		cancel()
		panic(err)
	}
	req.Header.Set("X-Test-Principal", id)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		cancel()
		panic(err)
	}
	if resp.StatusCode != http.StatusOK {
		defer cancel()
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &p)
		return nil, resp.StatusCode, p.Code
	}
	s := &tailStream{recs: make(chan json.RawMessage, 256), done: make(chan struct{})}
	opened := make(chan []string, 1)
	go func() {
		defer close(s.done)
		defer close(s.recs)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sent := false
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var en struct {
				Topic string          `json:"topic"`
				Data  json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(data), &en) != nil {
				continue
			}
			switch {
			case en.Topic == "core.stream.opened.v1" && !sent:
				var d struct {
					Topics []string `json:"topics"`
				}
				_ = json.Unmarshal(en.Data, &d)
				opened <- d.Topics
				sent = true
			case en.Topic == logjournal.TailTopicName:
				s.recs <- en.Data
			}
		}
	}()
	select {
	case s.opened = <-opened:
	case <-time.After(5 * time.Second):
		cancel()
		panic("the stream never sent its opened frame")
	}
	// The stream ends with the test's rig (the server closes), but a reader
	// blocked on the body is released here too.
	go func() { <-s.done; cancel() }()
	return s, http.StatusOK, ""
}

// raw is the next tail record exactly as sent.
func (s *tailStream) raw(t testing.TB) json.RawMessage {
	t.Helper()
	select {
	case d, ok := <-s.recs:
		if !ok {
			t.Fatal("the stream ended while waiting for a record")
		}
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a record")
	}
	return nil
}

func (s *tailStream) next(t testing.TB) logjournal.RecordView {
	t.Helper()
	var v logjournal.RecordView
	if err := json.Unmarshal(s.raw(t), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (s *tailStream) quiet(t testing.TB, d time.Duration) {
	t.Helper()
	select {
	case rec, ok := <-s.recs:
		if ok {
			t.Fatalf("unexpected record %s", rec)
		}
	case <-time.After(d):
	}
}

// TestJournalTail_SameViewAsQuery: a record appended to the journal arrives on
// the tail with the same fields the query route returns for it, byte for byte
// as JSON, for attributes of every kind the store flattens (a group, numbers,
// a boolean, a duration, a repeated key).
func TestJournalTail_SameViewAsQuery(t *testing.T) {
	rig := newTailRig(t, nil)
	rig.sign("u-bruce", permManage, permUsers)
	s, status, code := rig.open("u-bruce", "/?topic="+logjournal.TailTopicName)
	if s == nil {
		t.Fatalf("open: %d %s", status, code)
	}
	rig.l.Module("core").With(slog.String(log.KeySubsystem, "scanner")).Warn("a batarang was thrown",
		slog.Int("count", 3), slog.Float64("arc", 0.25), slog.Bool("hit", true), slog.Duration("flight", 1500*time.Millisecond),
		slog.Group("ctx", slog.String("user_id", "u-bruce"), slog.Group("deep", slog.Int64("n", -7))),
		slog.String("dup", "first"), slog.String("dup", "last"))
	tail := s.raw(t)

	var served pageView
	for range 100 { // the journal tap writes in its own time
		_ = rig.tap.Flush(context.Background())
		served = mustCall(t, rig.api, realPrincipal(t, "u-bruce", permManage, permUsers), queryRequest{})
		if len(served.Items) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(served.Items) != 1 {
		t.Fatalf("the query route returned %d records, want 1", len(served.Items))
	}
	want, err := json.Marshal(served.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var norm any
	if err := json.Unmarshal(tail, &norm); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(norm) // the stream's data member, key-sorted like the route's
	if err != nil {
		t.Fatal(err)
	}
	var wantNorm any
	if err := json.Unmarshal(want, &wantNorm); err != nil {
		t.Fatal(err)
	}
	wantSorted, _ := json.Marshal(wantNorm)
	if string(got) != string(wantSorted) {
		t.Errorf("tail and query disagree:\n tail  %s\n query %s", got, wantSorted)
	}
}

// attrSeq is the (key, kind, value) sequence of rec's attributes, as a
// predicate reading them would see them.
func attrSeq(rec log.Record) []string {
	out := make([]string, 0, len(rec.Attrs))
	for _, a := range rec.Attrs {
		out = append(out, a.Key+"|"+a.Value.Kind().String()+"|"+a.Value.String())
	}
	return out
}

// TestJournalTail_PredicateSeesSameRecordAsQuery (05-logging §5): the
// module's other-user predicate is handed the same record on the query route
// and on the live tail, for a record that carries typed attributes, a repeated
// key and an unsorted key order. The expected sequence is written out (text
// kinds, sorted keys, the last repeat wins), so a drift on either surface
// fails this test, not just a disagreement between them.
func TestJournalTail_PredicateSeesSameRecordAsQuery(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][][]string{}
	pred := func(rec log.Record, viewer string) bool {
		mu.Lock()
		defer mu.Unlock()
		seen[rec.Message] = append(seen[rec.Message], attrSeq(rec))
		return false
	}
	rig := newTailRig(t, pred)
	rig.sign("u-bruce", permManage) // no users.view: the predicate runs on both surfaces
	s, status, code := rig.open("u-bruce", "/?topic="+logjournal.TailTopicName)
	if s == nil {
		t.Fatalf("open: %d %s", status, code)
	}
	attrs := []any{
		slog.String("zeta", "z"), slog.Int("status", 204), slog.Bool("hit", true),
		slog.String("dup", "first"), slog.Float64("arc", 0.25), slog.String("dup", "last"),
	}
	want := []string{"arc|String|0.25", "dup|String|last", "hit|String|true", "status|String|204", "zeta|String|z"}
	// Enough keys that a map's iteration order (the tail decodes a JSON object)
	// cannot happen to come out sorted.
	for i := range 14 {
		k := fmt.Sprintf("k%02d", i)
		attrs = append(attrs, slog.Int(k, i))
		want = append(want, fmt.Sprintf("%s|String|%d", k, i))
	}
	slices.Sort(want) // keys are not prefixes of one another, so this is key order
	rig.l.Module("core").Info("a signal was lit", attrs...)
	s.next(t)
	var served pageView
	for range 100 { // the journal tap writes in its own time
		_ = rig.tap.Flush(context.Background())
		served = mustCall(t, rig.api, realPrincipal(t, "u-bruce", permManage), queryRequest{})
		if len(served.Items) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(served.Items) != 1 {
		t.Fatalf("the query route returned %d records, want 1", len(served.Items))
	}
	mu.Lock()
	defer mu.Unlock()
	got := seen["a signal was lit"]
	if len(got) != 2 {
		t.Fatalf("predicate called %d times for the record, want 2 (tail, query): %v", len(got), got)
	}
	for i, surface := range []string{"tail", "query"} {
		if !slices.Equal(got[i], want) {
			t.Errorf("%s: predicate saw %v, want %v", surface, got[i], want)
		}
	}
}
