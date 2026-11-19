// file: core/internal/outboxhttp/routes_test.go
// version: 1.1.3
// guid: b91b2c32-bee4-4aac-a400-6fb0e871d709
// last-edited: 2026-11-19

package outboxhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// ---- fakes ------------------------------------------------------------------

// journal is the order in which the handler touched its collaborators.
type journal struct {
	mu    sync.Mutex
	steps []string
}

func (j *journal) add(s string) {
	j.mu.Lock()
	j.steps = append(j.steps, s)
	j.mu.Unlock()
}

func (j *journal) all() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.steps)
}

// ctxInfo is what a collaborator saw of the context it was handed: whether it
// was still live, and how long until its deadline (zero when it had none).
type ctxInfo struct {
	alive     bool
	remaining time.Duration
}

func ctxOf(ctx context.Context) ctxInfo {
	c := ctxInfo{alive: ctx.Err() == nil}
	if d, ok := ctx.Deadline(); ok {
		c.remaining = time.Until(d)
	}
	return c
}

// fakeOp is an Operator that answers what the test set and logs every call.
type fakeOp struct {
	j *journal

	status    outbox.Status
	statusErr error

	// topic is what DeadTopic answers (noticeTopic when empty) and taken what
	// RetryDead and DiscardDead answer (the topic read when empty).
	topic, taken string
	topicErr     error

	retryErr, discardErr error
	purgeSkipped         int
	purgeErr             error

	letters  []outbox.DeadLetter
	listNext string
	listErr  error
	pages    [][]outbox.DeadLetter // when set: the n-th ListDead call answers pages[n], with a next token but on the last

	mu        sync.Mutex
	gotAfter  string
	gotLimit  int
	listCalls int
	opCtx     []ctxInfo // the context of each RetryDead, DiscardDead and PurgeBacklog
}

func (f *fakeOp) ListDead(_ context.Context, after string, limit int) ([]outbox.DeadLetter, string, error) {
	f.j.add("op:list")
	f.mu.Lock()
	f.gotAfter, f.gotLimit = after, limit
	n := f.listCalls
	f.listCalls++
	f.mu.Unlock()
	if f.pages != nil {
		if n >= len(f.pages) {
			return nil, "", nil
		}
		next := ""
		if n < len(f.pages)-1 {
			next = fmt.Sprintf("page-%d", n+1)
		}
		return f.pages[n], next, f.listErr
	}
	return f.letters, f.listNext, f.listErr
}

func (f *fakeOp) readTopic() string {
	if f.topic != "" {
		return f.topic
	}
	return noticeTopic
}

func (f *fakeOp) takenTopic() string {
	if f.taken != "" {
		return f.taken
	}
	return f.readTopic()
}

func (f *fakeOp) sawCtx(ctx context.Context) {
	f.mu.Lock()
	f.opCtx = append(f.opCtx, ctxOf(ctx))
	f.mu.Unlock()
}

func (f *fakeOp) DeadTopic(_ context.Context, consumer string, seq uint64) (string, error) {
	f.j.add(fmt.Sprintf("op:topic %s/%d", consumer, seq))
	if f.topicErr != nil {
		return "", f.topicErr
	}
	return f.readTopic(), nil
}

func (f *fakeOp) RetryDead(ctx context.Context, consumer string, seq uint64) (string, error) {
	f.j.add(fmt.Sprintf("op:retry %s/%d", consumer, seq))
	f.sawCtx(ctx)
	if f.retryErr != nil {
		return "", f.retryErr
	}
	return f.takenTopic(), nil
}

func (f *fakeOp) DiscardDead(ctx context.Context, consumer string, seq uint64) (string, error) {
	f.j.add(fmt.Sprintf("op:discard %s/%d", consumer, seq))
	f.sawCtx(ctx)
	if f.discardErr != nil {
		return "", f.discardErr
	}
	return f.takenTopic(), nil
}

func (f *fakeOp) PurgeBacklog(ctx context.Context, consumer string) (int, error) {
	f.j.add("op:purge " + consumer)
	f.sawCtx(ctx)
	return f.purgeSkipped, f.purgeErr
}

func (f *fakeOp) Status(context.Context) (outbox.Status, error) {
	f.j.add("op:status")
	return f.status, f.statusErr
}

// fakeRec is an audit.Logger that keeps what it is given and can fail.
type fakeRec struct {
	j   *journal
	err error // fails every Record

	// honorCtx makes Record fail with the context's error once it is done, as a
	// real store does, after keeping the event.
	honorCtx bool

	// errOn fails the Records it returns an error for, after keeping them.
	errOn func(audit.Event) error
	// onRecord runs after the n-th Record (1-based) was kept.
	onRecord func(n int)

	mu     sync.Mutex
	events []audit.Event
	ctxs   []ctxInfo // the context of each Record
}

func (f *fakeRec) Record(ctx context.Context, e audit.Event) error {
	step := "record:" + e.Action + ":" + string(e.Outcome)
	if e.Reason != "" {
		step += "/" + e.Reason
	}
	f.j.add(step)
	f.mu.Lock()
	f.events = append(f.events, e)
	f.ctxs = append(f.ctxs, ctxOf(ctx))
	n := len(f.events)
	f.mu.Unlock()
	if f.onRecord != nil {
		f.onRecord(n)
	}
	if f.err != nil {
		return f.err
	}
	if f.honorCtx {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if f.errOn != nil {
		return f.errOn(e)
	}
	return nil
}

func (f *fakeRec) RecordIn(context.Context, *storage.Batch, audit.Event) error {
	panic("outboxhttp records synchronously, never into a batch")
}

func (f *fakeRec) Emit(context.Context, audit.Event) { panic("outboxhttp never emits asynchronously") }

func (f *fakeRec) recorded() []audit.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

func (f *fakeRec) seen() []ctxInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ctxs)
}

// rig is the fakes over one api.
type rig struct {
	j    *journal
	op   *fakeOp
	rec  *fakeRec
	api  *api
	logs *lockedBuffer
}

func newRig() *rig {
	j := &journal{}
	// The consumer the rig's purges name is known to Status with its topic, as a
	// registered one is; a purge of a consumer Status does not list is a 404.
	op, rec, logs := &fakeOp{j: j, status: outbox.Status{Consumers: []outbox.ConsumerStatus{{Name: "core.notify.deliver", Topic: noticeTopic}}}},
		&fakeRec{j: j}, &lockedBuffer{}
	return &rig{j: j, op: op, rec: rec, logs: logs,
		api: newAPI("/api/core/v1", op, nil, rec, WithLogger(slog.New(slog.NewTextHandler(logs, nil))))}
}

// ---- helpers ----------------------------------------------------------------

func deadIn(p *auth.Principal, consumer string, seq uint64) *httpapi.In[deadReq] {
	return &httpapi.In[deadReq]{Req: deadReq{Consumer: consumer, Seq: seq}, Principal: p, RequestID: "req-batmobile"}
}

func purgeIn(p *auth.Principal, consumer string) *httpapi.In[purgeReq] {
	return &httpapi.In[purgeReq]{Req: purgeReq{Consumer: consumer}, Principal: p, RequestID: "req-batmobile"}
}

func listIn(limit int, cursor string) *httpapi.In[listDeadReq] {
	return &httpapi.In[listDeadReq]{
		Req:       listDeadReq{CursorParams: httpapi.CursorParams{Limit: limit, Cursor: cursor}},
		Principal: bruce(), RequestID: "req-batmobile",
	}
}

func pageIn(limit, offset int) *httpapi.In[pageReq] {
	return &httpapi.In[pageReq]{Req: pageReq{PageParams: httpapi.PageParams{Limit: limit, Offset: offset}}, Principal: bruce()}
}

func wantProblem(t testing.TB, err error, status int, code string) *httpapi.Error {
	t.Helper()
	var he *httpapi.Error
	if !errors.As(err, &he) {
		t.Fatalf("error = %v (%T), want a %d %s problem", err, err, status, code)
	}
	if he.Status != status || he.Code != code {
		t.Fatalf("problem = %d %s (%q), want %d %s", he.Status, he.Code, he.Detail, status, code)
	}
	return he
}

func toJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- the route table --------------------------------------------------------

var wantRoutes = []struct {
	method, path, name string
	perm               string
	interactive        bool
}{
	{http.MethodGet, "/api/core/v1/outbox/dead", "listDeadLetters", "core:system.manage", false},
	{http.MethodPost, "/api/core/v1/outbox/dead/{consumer}/{seq}/retry", "retryDeadLetter", "core:system.manage", false},
	{http.MethodDelete, "/api/core/v1/outbox/dead/{consumer}/{seq}", "discardDeadLetter", "core:system.manage", false},
	{http.MethodDelete, "/api/core/v1/outbox/consumers/{consumer}/backlog", "purgeConsumerBacklog", "core:system.manage", true},
	{http.MethodGet, "/api/core/v1/outbox/consumers", "listOutboxConsumers", "core:system.view", false},
	{http.MethodGet, "/api/core/v1/events/topics", "listEventTopics", "core:system.view", false},
}

func registered(t testing.TB) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discardLogger()})
	Register(r.CoreGroup(), nil, nil, nil)
	return r
}

// TestOutboxHTTP_RouteTable pins 05-events §5E.11's table: the six routes, the
// scope of each, and the interactive guard on the purge alone.
func TestOutboxHTTP_RouteTable(t *testing.T) {
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
		if ri.Owner != "core" || ri.Kind != httpapi.RouteKindNative || ri.Name != w.name {
			t.Errorf("%s = owner %s kind %d name %s, want core native %s", w.path, ri.Owner, ri.Kind, ri.Name, w.name)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != w.perm {
			t.Errorf("%s requires %q, want %s", w.name, ri.Perm, w.perm)
		}
		if want := (httpapi.Guards{Interactive: w.interactive}); ri.Guards != want {
			t.Errorf("%s has guards %+v, want %+v", w.name, ri.Guards, want)
		}
	}
}

// TestOutboxHTTP_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses every
// non-Public route with ErrNoAuthService; a bad binding, a schema httpapi cannot
// describe, a non-snake_case name or a duplicate would each be a further error.
func TestOutboxHTTP_BuildFailsOnlyForWantOfAuth(t *testing.T) {
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
		t.Errorf("Build reported %d problems, want %d (one ErrNoAuthService per route): %v", len(problems), len(wantRoutes), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// openAPIOps is the document's operations by method and path.
func openAPIOps(t testing.TB) map[string]map[string]any {
	t.Helper()
	doc, err := registered(t).OpenAPI("core")
	if err != nil {
		t.Fatalf("OpenAPI: %v", err)
	}
	raw, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	ops := map[string]map[string]any{}
	for path, byMethod := range d.Paths {
		for method, op := range byMethod {
			ops[strings.ToUpper(method)+" "+path] = op
		}
	}
	return ops
}

// paramsOf is op's parameters as "in:name" -> schema type.
func paramsOf(t testing.TB, op map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	list, _ := op["parameters"].([]any)
	for _, p := range list {
		m := p.(map[string]any)
		typ := ""
		if sc, ok := m["schema"].(map[string]any); ok {
			typ, _ = sc["type"].(string)
		}
		out[fmt.Sprint(m["in"])+":"+fmt.Sprint(m["name"])] = typ
	}
	return out
}

// TestOutboxHTTP_OpenAPIBindings: the request types bind what the document says:
// the cursor and limit of the dead letter list and the limit and offset of the
// two offset lists are query parameters, {consumer} and {seq} are path
// parameters, and {seq} is an integer. The router builds the document from the
// registrations, so this is the binding the handlers receive (Router.Build
// refuses these non-Public routes until F4-08).
func TestOutboxHTTP_OpenAPIBindings(t *testing.T) {
	ops := openAPIOps(t)
	for _, c := range []struct {
		key  string
		want map[string]string
	}{
		{"GET /outbox/dead", map[string]string{"query:cursor": "string", "query:limit": "integer"}},
		{"POST /outbox/dead/{consumer}/{seq}/retry", map[string]string{"path:consumer": "string", "path:seq": "integer"}},
		{"DELETE /outbox/dead/{consumer}/{seq}", map[string]string{"path:consumer": "string", "path:seq": "integer"}},
		{"DELETE /outbox/consumers/{consumer}/backlog", map[string]string{"path:consumer": "string"}},
		{"GET /outbox/consumers", map[string]string{"query:limit": "integer", "query:offset": "integer"}},
		{"GET /events/topics", map[string]string{"query:limit": "integer", "query:offset": "integer"}},
	} {
		op := ops[strings.Replace(c.key, " ", " /api/core/v1", 1)]
		if op == nil {
			t.Errorf("the document has no operation %s (have %v)", c.key, keys(ops))
			continue
		}
		got := paramsOf(t, op)
		for p, typ := range c.want {
			if got[p] != typ {
				t.Errorf("%s: parameter %s = %q, want %q (have %v)", c.key, p, got[p], typ, got)
			}
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: parameters %v, want exactly %v", c.key, got, c.want)
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

type probeAnswer struct {
	Consumer string `json:"consumer"`
	Seq      uint64 `json:"seq"`
}

// seqProbe registers a Public route over deadReq, so the router's own binder
// runs on the request type the audited routes use.
func seqProbe(t testing.TB) http.Handler {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discardLogger()})
	httpapi.Handle(r.CoreGroup(), httpapi.Route{
		Method: http.MethodGet, Path: "/probe/{consumer}/{seq}", Name: "probeDeadLetter", Perm: rbac.Public,
	}, func(_ context.Context, in *httpapi.In[deadReq]) (probeAnswer, error) {
		return probeAnswer{Consumer: in.Req.Consumer, Seq: in.Req.Seq}, nil
	})
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

// TestOutboxHTTP_SeqBinding: {seq} is a decimal uint64, and the binder refuses
// anything else with 422 validation_failed on /path/seq (05-http-api §6; the
// brief's "400" is the spec's 422) before a handler runs: a bad value never
// reaches the outbox.
func TestOutboxHTTP_SeqBinding(t *testing.T) {
	h := seqProbe(t)
	get := func(seq string) (int, string) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/core/v1/probe/core.notify.deliver/"+seq, nil))
		return rr.Code, rr.Body.String()
	}
	for _, ok := range []string{"0", "1", "18446744073709551615"} {
		code, body := get(ok)
		if code != http.StatusOK || !strings.Contains(body, `"seq":`+ok) {
			t.Errorf("seq %s = %d %s, want 200 echoing it", ok, code, body)
		}
	}
	for _, bad := range []string{"abc", "-1", "1.5", "18446744073709551616", "0x10", "%20"} {
		code, body := get(bad)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"code":"validation_failed"`) || !strings.Contains(body, `"field":"/path/seq"`) {
			t.Errorf("seq %q = %d %s, want 422 validation_failed on /path/seq", bad, code, body)
		}
	}
}

// ---- the permission, guard and 401 tables (stage 13: F4-08) -----------------

// stage13Caller is who a stage-13 cell calls as. The zero value is no
// principal at all.
type stage13Caller struct {
	name      string
	anonymous bool
	method    auth.Method
	scopes    []rbac.Permission
}

// held is the rbac.Checker of a caller that holds exactly the scopes given: what
// Service.Compile builds from roles at F4-08, as far as these routes can tell.
type held []rbac.Permission

func (h held) Has(s rbac.Permission) bool { return slices.Contains(h, s) }

func (h held) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindAuthenticated:
		return true
	case rbac.KindAllOf:
		return !slices.ContainsFunc(r.Permissions(), func(p rbac.Permission) bool { return !h.Has(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(r.Permissions(), h.Has)
	}
	return false
}

func (h held) Confined() bool              { return false }
func (h held) Concrete() []rbac.Permission { return slices.Clone(h) }

func (c stage13Caller) principal() *auth.Principal {
	p := bruce()
	p.Method, p.Perms = c.method, held(c.scopes)
	return p
}

var (
	noCaller   = stage13Caller{name: "no principal", anonymous: true}
	viewer     = stage13Caller{name: "view only", method: auth.MethodSession, scopes: []rbac.Permission{permView}}
	manager    = stage13Caller{name: "manage", method: auth.MethodSession, scopes: []rbac.Permission{permManage}}
	managerKey = stage13Caller{name: "manage by api key", method: auth.MethodAPIKey, scopes: []rbac.Permission{permManage}}
	managerABS = stage13Caller{name: "manage by delegated session", method: auth.MethodSessionDelegated, scopes: []rbac.Permission{permManage}}
	managerOff = stage13Caller{name: "manage with auth off", method: auth.MethodAuthDisabled, scopes: []rbac.Permission{permManage}}
)

// letThrough is the want of a cell the route lets through: neither 401 nor 403,
// whatever the handler then answers (these cells name consumers that need not exist).
const letThrough = 0

// stage13Do serves one request to the real routes, built into a router over a
// live outbox, as caller. It is the body of every stage-13 cell: Build refuses
// non-Public routes until F4-08 brings the auth service, so no cell reaches it
// before then, and a cell that is un-skipped before then fails here, on Build.
func stage13Do(t *testing.T, method, path string, c stage13Caller) *httptest.ResponseRecorder {
	t.Helper()
	e := newEnv(t)
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discardLogger()})
	Register(r.CoreGroup(), e.ob, e.cat, e.aud, WithLogger(discardLogger()))
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest(method, "/api/core/v1"+path, nil)
	if !c.anonymous {
		req = req.WithContext(auth.WithPrincipal(req.Context(), c.principal()))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func wantStage13(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	switch {
	case want == letThrough && (rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden):
		t.Errorf("status = %d (%s), want the route to let the caller through", rec.Code, rec.Body.String())
	case want != letThrough && rec.Code != want:
		t.Errorf("status = %d (%s), want %d", rec.Code, rec.Body.String(), want)
	}
}

// TestOutboxHTTP_DeadLetterRoutesNeedSystemManage: the dead-letter list and both
// dead-letter writes answer 401 with no principal, 403 to a holder of
// core:system.view alone, and let a holder of core:system.manage through; the
// purge's own table (it also has the interactive guard) is
// TestOutboxHTTP_PurgeNeedsInteractive. Route permission (stage 13) arrives at
// F4-08, so each cell skips; F6-03, which mounts the package, un-skips them with
// no other change (the bodies are written).
func TestOutboxHTTP_DeadLetterRoutesNeedSystemManage(t *testing.T) {
	for _, c := range []struct {
		name, method, path string
	}{
		{"list", http.MethodGet, "/outbox/dead"},
		{"retry", http.MethodPost, "/outbox/dead/core.notify.deliver/1/retry"},
		{"discard", http.MethodDelete, "/outbox/dead/core.notify.deliver/1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
			for _, cell := range []struct {
				caller stage13Caller
				want   int
			}{
				{noCaller, http.StatusUnauthorized},
				{viewer, http.StatusForbidden},
				{manager, letThrough},
			} {
				t.Run(cell.caller.name, func(t *testing.T) {
					wantStage13(t, stage13Do(t, c.method, c.path, cell.caller), cell.want)
				})
			}
		})
	}
}

// TestOutboxHTTP_PurgeNeedsInteractive: the purge answers 401 with no principal,
// 403 to a holder of core:system.view alone, 403 to a holder of
// core:system.manage whose credential is not an interactive one (an API key, a
// delegated session, the auth-off principal: Guards.Interactive), and lets an
// interactive session holding core:system.manage through. Skipped until stage 13
// (F4-08).
func TestOutboxHTTP_PurgeNeedsInteractive(t *testing.T) {
	const path = "/outbox/consumers/core.notify.deliver/backlog"
	for _, cell := range []struct {
		caller stage13Caller
		want   int
	}{
		{noCaller, http.StatusUnauthorized},
		{viewer, http.StatusForbidden},
		{managerKey, http.StatusForbidden},
		{managerABS, http.StatusForbidden},
		{managerOff, http.StatusForbidden},
		{manager, letThrough},
	} {
		t.Run(cell.caller.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
			wantStage13(t, stage13Do(t, http.MethodDelete, path, cell.caller), cell.want)
		})
	}
}

// TestOutboxHTTP_ConsumersAndTopicsNeedSystemView: both read routes answer 401
// with no principal, 403 to a caller holding neither scope, and let a holder of
// core:system.view through. Skipped until stage 13 (F4-08).
func TestOutboxHTTP_ConsumersAndTopicsNeedSystemView(t *testing.T) {
	nothing := stage13Caller{name: "no scope", method: auth.MethodSession}
	for _, c := range []struct{ name, path string }{
		{"consumers", "/outbox/consumers"},
		{"topics", "/events/topics"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
			for _, cell := range []struct {
				caller stage13Caller
				want   int
			}{
				{noCaller, http.StatusUnauthorized},
				{nothing, http.StatusForbidden},
				{viewer, letThrough},
			} {
				t.Run(cell.caller.name, func(t *testing.T) {
					wantStage13(t, stage13Do(t, http.MethodGet, c.path, cell.caller), cell.want)
				})
			}
		})
	}
}

// ---- the dead letter list ---------------------------------------------------

// TestOutboxHTTP_ListDeadLetters: the dead letters of two consumers in
// (consumer, topic, seq) order, each with its consumer, topic, seq, partition,
// attempts, error token and idempotency key and nothing of the message itself,
// paged by a cursor: a walk with a page of one meets every letter once.
func TestOutboxHTTP_ListDeadLetters(t *testing.T) {
	e := newEnv(t)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 2, false)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, false)
	e.deadLetter(consumerMirror, noticeTopic, noticeParts, 5, false)
	ctx := context.Background()

	whole, err := e.api.listDead(ctx, listIn(50, ""))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, d := range whole.Items {
		order = append(order, fmt.Sprintf("%s/%s/%d", d.Consumer, d.Topic, d.Seq))
	}
	want := []string{
		consumerDeliver + "/" + noticeTopic + "/1",
		consumerDeliver + "/" + noticeTopic + "/2",
		consumerMirror + "/" + noticeTopic + "/5",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("letters = %v, want %v", order, want)
	}
	if whole.NextCursor != "" {
		t.Errorf("a page that held everything carries cursor %q", whole.NextCursor)
	}
	d := whole.Items[0]
	if d.Partition != outboxstore.Partition("wayne-manor", noticeParts) || d.Attempts != 3 || d.Error != "permanent" ||
		d.IdempotencyKey != "01J0000000000000000BATMN01" || d.Producer != "core" || d.StagedAt.IsZero() || d.DeadAt.Sub(d.StagedAt) < 59*time.Minute || d.DeadAt.Sub(d.StagedAt) > 61*time.Minute {
		t.Errorf("letter = %+v", d)
	}
	raw := toJSON(t, whole)
	for _, leak := range []string{"payload", "Joker", "wayne-manor", `"key"`} {
		if strings.Contains(raw, leak) {
			t.Errorf("the response carries %q: %s", leak, raw)
		}
	}
	var fields map[string]any
	var one struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &one); err != nil {
		t.Fatal(err)
	}
	fields = one.Items[0]
	for _, k := range []string{"consumer", "topic", "seq", "partition", "attempts", "error", "idempotency_key", "producer", "staged_at", "dead_at"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("the letter has no %q field: %v", k, fields)
		}
	}

	var walked []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("the walk does not end")
		}
		page, err := e.api.listDead(ctx, listIn(1, cursor))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page.Items {
			walked = append(walked, fmt.Sprintf("%s/%s/%d", d.Consumer, d.Topic, d.Seq))
		}
		if page.NextCursor == "" {
			if len(page.Items) > 1 {
				t.Errorf("a page of %d with limit 1", len(page.Items))
			}
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(walked, want) {
		t.Errorf("a walk with a page of one met %v, want %v exactly once each", walked, want)
	}

	// A full page ends at the last letter and still carries a cursor; the page
	// after it is empty and says so.
	full, err := e.api.listDead(ctx, listIn(3, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Items) != 3 || full.NextCursor == "" {
		t.Fatalf("full page = %d items, cursor %q", len(full.Items), full.NextCursor)
	}
	rest, err := e.api.listDead(ctx, listIn(3, full.NextCursor))
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Items) != 0 || rest.NextCursor != "" {
		t.Errorf("page after the last letter = %d items, cursor %q", len(rest.Items), rest.NextCursor)
	}
	if toJSON(t, rest) != `{"items":[],"next_cursor":null}` && !strings.Contains(toJSON(t, rest), `"items":[]`) {
		t.Errorf("an empty page encodes as %s, want items as an empty array", toJSON(t, rest))
	}
}

// TestOutboxHTTP_ListDeadBoundsLimit: the route asks the outbox for the page the
// client asked for, and never for more than 500 (the outbox's own bound, below
// httpapi's limit of 1000).
func TestOutboxHTTP_ListDeadBoundsLimit(t *testing.T) {
	for _, c := range []struct{ asked, want int }{{1, 1}, {7, 7}, {500, 500}, {501, 500}, {1000, 500}} {
		rg := newRig()
		if _, err := rg.api.listDead(context.Background(), listIn(c.asked, "")); err != nil {
			t.Fatal(err)
		}
		if rg.op.gotLimit != c.want {
			t.Errorf("limit %d asked the outbox for %d, want %d", c.asked, rg.op.gotLimit, c.want)
		}
	}
}

// TestOutboxHTTP_ListDeadCursorIsExclusive: the cursor the route hands out is
// the position the store said to go on after, and the next request gives the
// store back exactly that token, so no letter is read twice or skipped.
func TestOutboxHTTP_ListDeadCursorIsExclusive(t *testing.T) {
	key := outboxstore.DeadKey(consumerDeliver, noticeTopic, 3)
	token := base64.RawURLEncoding.EncodeToString(key)
	rg := newRig()
	rg.op.letters = []outbox.DeadLetter{{Consumer: consumerDeliver, Topic: noticeTopic, Seq: 3}}
	rg.op.listNext = token
	page, err := rg.api.listDead(context.Background(), listIn(1, ""))
	if err != nil {
		t.Fatal(err)
	}
	if rg.op.gotAfter != "" {
		t.Errorf("the first page asked to go on after %q", rg.op.gotAfter)
	}
	pos, err := httpapi.DecodeCursor(page.NextCursor, deadCursorHash())
	if err != nil || string(pos) != string(key) {
		t.Fatalf("cursor decodes to %q, %v; want the store's key %q", pos, err, key)
	}
	if _, err := rg.api.listDead(context.Background(), listIn(1, page.NextCursor)); err != nil {
		t.Fatal(err)
	}
	if rg.op.gotAfter != token {
		t.Errorf("the next page asked to go on after %q, want the store's token %q", rg.op.gotAfter, token)
	}
	// The last page has no cursor.
	rg.op.listNext = ""
	last, err := rg.api.listDead(context.Background(), listIn(1, ""))
	if err != nil || last.NextCursor != "" {
		t.Errorf("last page = cursor %q, %v", last.NextCursor, err)
	}
}

// TestOutboxHTTP_ListDeadBadCursor: a malformed cursor, one made for another
// route, and one whose position is not a dead letter's key (a forged position
// would start the store's scan in another key family) are each 400
// invalid_cursor, and the store is never asked.
func TestOutboxHTTP_ListDeadBadCursor(t *testing.T) {
	foreign := httpapi.EncodeCursor(outboxstore.DeadKey(consumerDeliver, noticeTopic, 1), deadCursorHash()+1)
	forged := []string{
		"not base64 !",
		"",
		// a real key of every other family: the position a scan would start at
		httpapi.EncodeCursor(outboxstore.InKey(noticeTopic, "01J0000000000000000BATMN01"), deadCursorHash()),
		httpapi.EncodeCursor(outboxstore.MsgKey(noticeTopic, 0, 1), deadCursorHash()),
		httpapi.EncodeCursor(outboxstore.CurKey(consumerDeliver, noticeTopic, 0), deadCursorHash()),
		httpapi.EncodeCursor(outboxstore.DeadCountKey(consumerDeliver, noticeTopic, 0), deadCursorHash()),
		httpapi.EncodeCursor(outboxstore.SeqKey(noticeTopic), deadCursorHash()),
		httpapi.EncodeCursor(nil, deadCursorHash()),
		httpapi.EncodeCursor(outboxstore.DeadRoot(), deadCursorHash()),
		httpapi.EncodeCursor(outboxstore.DeadPrefix(consumerDeliver), deadCursorHash()),
		httpapi.EncodeCursor(append(outboxstore.DeadKey(consumerDeliver, noticeTopic, 1), 0xff), deadCursorHash()),
		foreign,
	}
	for i, cur := range forged {
		if cur == "" {
			continue // the first page
		}
		rg := newRig()
		_, err := rg.api.listDead(context.Background(), listIn(10, cur))
		he := wantProblem(t, err, http.StatusBadRequest, "invalid_cursor")
		if len(he.Fields) == 0 || he.Fields[0].Field != "/query/cursor" {
			t.Errorf("cursor %d: fields = %+v, want /query/cursor", i, he.Fields)
		}
		if rg.op.listCalls != 0 {
			t.Errorf("cursor %d reached the store", i)
		}
	}
}

// TestOutboxHTTP_ListDeadFailures: no operator is a 503, a store failure is
// returned for httpapi to answer 500 and withhold.
func TestOutboxHTTP_ListDeadFailures(t *testing.T) {
	bare := newAPI("/api/core/v1", nil, nil, nil)
	_, err := bare.listDead(context.Background(), listIn(10, ""))
	wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")

	rg := newRig()
	boom := errors.New("pebble: the Batcave flooded")
	rg.op.listErr = boom
	_, err = rg.api.listDead(context.Background(), listIn(10, ""))
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the store's", err)
	}
	if he, ok := errors.AsType[*httpapi.Error](err); ok {
		t.Errorf("a store failure became the problem %d %s", he.Status, he.Code)
	}
}

// ---- retry and discard ------------------------------------------------------

func recordOf(t testing.TB, e *env, action string) audit.Record {
	t.Helper()
	var hits []audit.Record
	for _, r := range e.records() {
		if r.Action == action {
			hits = append(hits, r)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%d records of %s, want 1 (all: %+v)", len(hits), action, e.records())
	}
	return hits[0]
}

// detailInt is the number under key in a stored record's details (JSON decoded).
func detailInt(t testing.TB, r audit.Record, key string) int {
	t.Helper()
	switch v := r.Details[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			t.Fatal(err)
		}
		return int(n)
	}
	t.Fatalf("details[%q] = %#v in %+v", key, r.Details[key], r.Details)
	return 0
}

// TestOutboxHTTP_RetryRedeliversAndAudits: with the relay running, a retried dead
// letter is handed to its consumer again, under its old idempotency key and with
// its own payload, the dead row is gone (the other letter stays), and one audit
// record names the retry and the bound principal.
func TestOutboxHTTP_RetryRedeliversAndAudits(t *testing.T) {
	e := newRelayEnv(t)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 2, true)
	e.startRelay()
	if n := len(e.got.all()); n != 0 {
		t.Fatalf("%d deliveries before the retry", n)
	}

	if _, err := e.api.retry(context.Background(), deadIn(bruce(), consumerDeliver, 1)); err != nil {
		t.Fatal(err)
	}
	// The retry staged exactly one message, so once the relay has drained it (the
	// intake is empty and the consumer has nothing pending) a second delivery has
	// nothing left to come from: the check needs no wait for one that never arrives.
	eventually(t, "the retried message to be delivered and the stream drained", func() bool {
		return len(e.got.all()) >= 1 && len(e.intake(noticeTopic)) == 0 && e.consumerStatus(consumerDeliver).Pending == 0
	})
	got := e.got.all()
	if len(got) != 1 {
		t.Fatalf("%d deliveries after one retry, want 1: %+v", len(got), got)
	}
	m := got[0]
	if m.IdempotencyKey != "01J0000000000000000BATMN01" || m.Topic != noticeTopic || m.Payload.Villain != "Joker" || m.Payload.Block != 7 || m.Key != "wayne-manor" {
		t.Errorf("delivery = %+v, want the dead letter's message under its old key", m)
	}
	rows := e.deadRows()
	if len(rows) != 1 || rows[0].Seq != 2 {
		t.Errorf("dead letters after the retry = %+v, want only seq 2", rows)
	}
	r := recordOf(t, e, "core.outbox.dead_retry")
	if r.Actor.Username != "bruce" || r.Outcome != audit.Success || r.Target.ID != consumerDeliver {
		t.Errorf("record = %+v", r)
	}
	if n := len(e.records()); n != 1 {
		t.Errorf("%d audit records, want 1", n)
	}
}

// TestOutboxHTTP_DeadOperationsNameTheTopicWithoutCursors: a consumer whose
// cursors expired while its letters stayed (or that never registered here) is
// unknown to the status read, and the audit record of a retry or discard still
// names the topic, taken from the letter itself.
func TestOutboxHTTP_DeadOperationsNameTheTopicWithoutCursors(t *testing.T) {
	for _, c := range []struct {
		consumer, topic string
		parts           int
	}{
		{consumerMirror, noticeTopic, noticeParts},
		{consumerRespond, signalTopic, signalParts},
	} {
		for _, kind := range []string{"retry", "discard"} {
			t.Run(c.consumer+"/"+kind, func(t *testing.T) {
				e := newEnv(t)
				// A letter of another consumer sorts before this one's, and one
				// sorts after it: neither may name the topic.
				e.deadLetter("core.notify.alpha", "core.notify.delivery.requested.v1", noticeParts, 1, false)
				e.deadLetter(c.consumer, c.topic, c.parts, 5, false)
				e.deadLetter("wayne.vault.zeta", signalTopic, signalParts, 1, false)
				if st := e.consumerStatus(c.consumer); st.Topic != "" {
					t.Fatalf("the status read names topic %q: the fixture is not the cursorless shape", st.Topic)
				}
				var err error
				if kind == "retry" {
					_, err = e.api.retry(context.Background(), deadIn(bruce(), c.consumer, 5))
				} else {
					_, err = e.api.discard(context.Background(), deadIn(bruce(), c.consumer, 5))
				}
				if err != nil {
					t.Fatal(err)
				}
				recs := e.records()
				if len(recs) != 1 || recs[0].Details["topic"] != c.topic || detailInt(t, recs[0], "seq") != 5 {
					t.Errorf("records = %+v, want one naming topic %s and seq 5", recs, c.topic)
				}
			})
		}
	}
}

// TestOutboxHTTP_RetryStagesForItsConsumerAlone: with no relay running, a retried
// dead letter is gone from the dead rows and staged again for its consumer
// alone, under the same idempotency key, and one audit record names the action,
// the bound principal, the consumer, the seq and the topic, and never the
// message.
func TestOutboxHTTP_RetryStagesForItsConsumerAlone(t *testing.T) {
	e := newEnv(t)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 2, true)

	out, err := e.api.retry(context.Background(), deadIn(bruce(), consumerDeliver, 1))
	if err != nil {
		t.Fatal(err)
	}
	if out != (httpapi.NoContent{}) {
		t.Errorf("retry answered %+v, want 204", out)
	}
	rows := e.deadRows()
	if len(rows) != 1 || rows[0].Seq != 2 {
		t.Fatalf("dead letters after the retry = %+v, want only seq 2", rows)
	}
	intake := e.intake(noticeTopic)
	if len(intake) != 1 || intake[0].Intake.Msg.To != consumerDeliver || intake[0].Intake.Msg.ID != "01J0000000000000000BATMN01" {
		t.Fatalf("intake after the retry = %+v, want the one message, addressed to %s, under its old key", intake, consumerDeliver)
	}

	r := recordOf(t, e, "core.outbox.dead_retry")
	if r.Outcome != audit.Success || r.Permission != "core:system.manage" || r.RequestID != "req-batmobile" {
		t.Errorf("record = %+v", r.Event)
	}
	if r.Route != "POST /api/core/v1/outbox/dead/{consumer}/{seq}/retry" {
		t.Errorf("route = %q", r.Route)
	}
	if r.Actor.UserID != "u-bruce" || r.Actor.Username != "bruce" || r.Actor.Method != "session" ||
		r.Actor.CredentialID != "sess-batcave" || r.Actor.ClientAddr != "192.0.2.10" {
		t.Errorf("actor = %+v, want the principal's", r.Actor)
	}
	if r.Target.Module != "core" || r.Target.Kind != "outbox_consumer" || r.Target.ID != consumerDeliver {
		t.Errorf("target = %+v", r.Target)
	}
	if detailInt(t, r, "seq") != 1 || r.Details["topic"] != noticeTopic || len(r.Details) != 2 {
		t.Errorf("details = %+v, want seq and topic only", r.Details)
	}
	if raw := toJSON(t, r); strings.Contains(raw, "Joker") || strings.Contains(raw, "wayne-manor") || strings.Contains(raw, "payload") {
		t.Errorf("the audit record carries the message: %s", raw)
	}
	if n := len(e.records()); n != 1 {
		t.Errorf("%d audit records, want 1", n)
	}
}

// TestOutboxHTTP_RetryActorFromContext: when the router bound an audit actor to
// the request's context the record names that one, and a request with no
// principal at all is then still attributable.
func TestOutboxHTTP_RetryActorFromContext(t *testing.T) {
	e := newEnv(t)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
	bound := audit.Actor{UserID: "u-barbara", Username: "barbara", Method: "session", ClientAddr: "192.0.2.20"}
	ctx := audit.WithActor(context.Background(), bound)
	if _, err := e.api.retry(ctx, deadIn(nil, consumerDeliver, 1)); err != nil {
		t.Fatal(err)
	}
	if r := recordOf(t, e, "core.outbox.dead_retry"); r.Actor != bound {
		t.Errorf("actor = %+v, want the bound %+v", r.Actor, bound)
	}

	// With a principal too, the actor the router bound still wins: it is the one
	// every other audit record of the request names.
	e2 := newEnv(t)
	e2.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
	if _, err := e2.api.retry(ctx, deadIn(bruce(), consumerDeliver, 1)); err != nil {
		t.Fatal(err)
	}
	if r := recordOf(t, e2, "core.outbox.dead_retry"); r.Actor != bound {
		t.Errorf("actor with a principal = %+v, want the bound %+v", r.Actor, bound)
	}
}

// TestOutboxHTTP_DiscardDeletesAndAudits: a discarded letter is gone, nothing is
// staged for delivery, one audit record names it, and the core.outbox health
// component, Degraded for its dead letters, is Up again when it was the last.
func TestOutboxHTTP_DiscardDeletesAndAudits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
	e.ob.RefreshStatus(ctx)
	if got := e.health(); got.Status != health.StatusDegraded || got.Reason != "dead_letters" {
		t.Fatalf("health with a dead letter = %+v, want Degraded dead_letters", got)
	}

	out, err := e.api.discard(ctx, deadIn(bruce(), consumerDeliver, 1))
	if err != nil {
		t.Fatal(err)
	}
	if out != (httpapi.NoContent{}) {
		t.Errorf("discard answered %+v, want 204", out)
	}
	if rows := e.deadRows(); len(rows) != 0 {
		t.Errorf("dead letters after the discard = %+v", rows)
	}
	if rows := e.intake(noticeTopic); len(rows) != 0 {
		t.Errorf("a discard staged %+v for delivery", rows)
	}
	if got := e.health(); got.Status != health.StatusUp {
		t.Errorf("health after the last dead letter was discarded = %+v, want Up", got)
	}

	r := recordOf(t, e, "core.outbox.dead_discard")
	if r.Route != "DELETE /api/core/v1/outbox/dead/{consumer}/{seq}" || r.Actor.Username != "bruce" || r.Outcome != audit.Success {
		t.Errorf("record = %+v", r)
	}
	if r.Target.ID != consumerDeliver || detailInt(t, r, "seq") != 1 || r.Details["topic"] != noticeTopic {
		t.Errorf("record target/details = %+v / %+v", r.Target, r.Details)
	}
	if n := len(e.records()); n != 1 {
		t.Errorf("%d audit records, want 1", n)
	}
}

func (e *env) health() health.Result {
	e.t.Helper()
	for _, c := range e.reg.Snapshot().Components {
		if c.ID == outbox.ComponentID {
			return c.Result
		}
	}
	e.t.Fatal("core.outbox is not registered")
	return health.Result{}
}

// ---- purge ------------------------------------------------------------------

// TestOutboxHTTP_PurgeAdvancesCursorsAndAudits: a disabled module's consumer
// with a retained backlog is purged: 200 with the number skipped, its backlog
// gone, its dead letters kept, and one audit record with the count.
func TestOutboxHTTP_PurgeAdvancesCursorsAndAudits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cursors(consumerRespond, signalTopic, signalParts)
	e.sequence(signalTopic, signalParts, 5)
	e.deadLetter(consumerRespond, signalTopic, signalParts, 99, false)
	if c := e.consumerStatus(consumerRespond); c.Enabled || c.RetainedBacklog != 5 || c.Pending != 5 {
		t.Fatalf("before the purge: enabled=%v retained=%d pending=%d, want a disabled consumer holding 5", c.Enabled, c.RetainedBacklog, c.Pending)
	}

	out, err := e.api.purge(ctx, purgeIn(bruce(), consumerRespond))
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 5 {
		t.Errorf("skipped = %d, want 5", out.Skipped)
	}
	if got := toJSON(t, out); got != `{"skipped":5}` {
		t.Errorf("purge answered %s", got)
	}
	c := e.consumerStatus(consumerRespond)
	if c.RetainedBacklog != 0 || c.Pending != 0 || c.Dead != 1 {
		t.Errorf("after the purge: retained=%d pending=%d dead=%d, want 0 0 1 (dead letters stay)", c.RetainedBacklog, c.Pending, c.Dead)
	}
	if rows := e.deadRows(); len(rows) != 1 || rows[0].Seq != 99 {
		t.Errorf("dead letters after the purge = %+v", rows)
	}
	again, err := e.api.purge(ctx, purgeIn(bruce(), consumerRespond))
	if err != nil || again.Skipped != 0 {
		t.Errorf("a second purge = %+v, %v; want 0 skipped", again, err)
	}

	// Each purge is recorded twice: as requested before it ran (its count is not
	// known yet) and as completed with the count after.
	var requested, completed []audit.Record
	for _, r := range e.records() {
		switch r.Reason {
		case reasonRequested:
			requested = append(requested, r)
		case reasonCompleted:
			completed = append(completed, r)
		default:
			t.Errorf("record with reason %q: %+v", r.Reason, r.Event)
		}
	}
	if len(requested) != 2 || len(completed) != 2 {
		t.Fatalf("%d requested and %d completed records after two purges, want 2 and 2", len(requested), len(completed))
	}
	for _, r := range requested {
		if r.Action != "core.outbox.backlog_purge" || r.Outcome != audit.Success || r.Actor.Username != "bruce" ||
			r.Target.ID != consumerRespond || r.Details["topic"] != signalTopic || len(r.Details) != 1 {
			t.Errorf("request record = %+v, want the action, the actor, the consumer and the topic with no count", r.Event)
		}
	}
	slices.SortFunc(completed, func(x, y audit.Record) int { return detailInt(t, y, "skipped") - detailInt(t, x, "skipped") })
	first, second := completed[0], completed[1]
	if first.Action != "core.outbox.backlog_purge" || first.Outcome != audit.Success || first.Actor.Username != "bruce" {
		t.Errorf("record = %+v", first.Event)
	}
	if first.Route != "DELETE /api/core/v1/outbox/consumers/{consumer}/backlog" || first.Target.ID != consumerRespond {
		t.Errorf("route/target = %q / %+v", first.Route, first.Target)
	}
	if detailInt(t, first, "skipped") != 5 || first.Details["topic"] != signalTopic || len(first.Details) != 2 {
		t.Errorf("details = %+v, want skipped and topic only", first.Details)
	}
	if detailInt(t, second, "skipped") != 0 {
		t.Errorf("the second purge's details = %+v", second.Details)
	}
}

// ---- audit failure ----------------------------------------------------------

// TestOutboxHTTP_AuditFailureMeansNotApplied: with the audit store's writes
// failing, each state change answers 503 and the operation is NOT applied (05-rbac
// §3.5 and §8: a sync audit write that fails fails the operation, closed). Over
// the real outbox, the letter is still there, nothing was staged and the
// cursors have not moved; the failure is counted on the sync path, and the log
// line names who tried what.
func TestOutboxHTTP_AuditFailureMeansNotApplied(t *testing.T) {
	type cell struct {
		name      string
		seed      func(e *env)
		run       func(e *env) error
		untouched func(t *testing.T, e *env)
	}
	cells := []cell{
		{"retry",
			func(e *env) { e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true) },
			func(e *env) error {
				_, err := e.api.retry(context.Background(), deadIn(bruce(), consumerDeliver, 1))
				return err
			},
			func(t *testing.T, e *env) {
				if rows := e.deadRows(); len(rows) != 1 || rows[0].Seq != 1 {
					t.Errorf("dead letters = %+v, want the letter still there", rows)
				}
				if rows := e.intake(noticeTopic); len(rows) != 0 {
					t.Errorf("intake = %+v, want nothing re-staged", rows)
				}
			}},
		{"discard",
			func(e *env) { e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true) },
			func(e *env) error {
				_, err := e.api.discard(context.Background(), deadIn(bruce(), consumerDeliver, 1))
				return err
			},
			func(t *testing.T, e *env) {
				if rows := e.deadRows(); len(rows) != 1 || rows[0].Seq != 1 {
					t.Errorf("dead letters = %+v, want the letter still there", rows)
				}
			}},
		{"purge",
			func(e *env) {
				e.cursors(consumerRespond, signalTopic, signalParts)
				e.sequence(signalTopic, signalParts, 3)
			},
			func(e *env) error {
				_, err := e.api.purge(context.Background(), purgeIn(bruce(), consumerRespond))
				return err
			},
			func(t *testing.T, e *env) {
				if c := e.consumerStatus(consumerRespond); c.Pending != 3 {
					t.Errorf("pending = %d, want the cursors where they were (3)", c.Pending)
				}
			}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.seed(e)
			e.breakAudit()
			before := e.auditWriteErrors()

			err := c.run(e)
			he := wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")
			if !strings.Contains(he.Detail, "nothing was changed") {
				t.Errorf("detail = %q, want it to say nothing was changed", he.Detail)
			}
			if he.Err == nil {
				t.Error("the cause of the failed audit write was dropped")
			}
			if got := e.auditWriteErrors(); got != before+1 {
				t.Errorf("audit_write_errors_total{sync} = %v, want %v (one more)", got, before+1)
			}
			c.untouched(t, e)
			log := e.logBuf.String()
			for _, want := range []string{"was not run", "actor_username=bruce", "actor_user_id=u-bruce", "request_id=req-batmobile", "client_addr=192.0.2.10"} {
				if !strings.Contains(log, want) {
					t.Errorf("the failure log lacks %q: %q", want, log)
				}
			}
			if c.name != "purge" && !strings.Contains(log, "consumer="+consumerDeliver) {
				t.Errorf("the failure log does not name the consumer: %q", log)
			}
		})
	}
}

// TestOutboxHTTP_AuditFailureStopsBeforeTheOperation: over fakes, a first record
// that cannot be written stops every route before it calls the operation.
func TestOutboxHTTP_AuditFailureStopsBeforeTheOperation(t *testing.T) {
	for _, kind := range []string{"retry", "discard", "purge"} {
		t.Run(kind, func(t *testing.T) {
			rg := newRig()
			rg.rec.err = errors.New("audit: the disk is full")
			var err error
			switch kind {
			case "retry":
				_, err = rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 4))
			case "discard":
				_, err = rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 4))
			case "purge":
				_, err = rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
			}
			wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")
			for _, step := range rg.j.all() {
				if strings.HasPrefix(step, "op:retry") || strings.HasPrefix(step, "op:discard") || strings.HasPrefix(step, "op:purge") {
					t.Errorf("the operation ran after its record failed: %v", rg.j.all())
				}
			}
			if recs := rg.rec.recorded(); len(recs) != 1 {
				t.Errorf("%d records written, want the one that failed", len(recs))
			}
		})
	}
}

// ---- order, contexts and mapping, over fakes --------------------------------

// TestOutboxHTTP_RecordsBeforeTheOperation: the audit record is written before
// the operation runs (and so before anything is applied), with outcome success;
// a purge adds a record of what it skipped after it ran, which is not known
// before.
func TestOutboxHTTP_RecordsBeforeTheOperation(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(rg *rig) error
		want []string
	}{
		{"retry", func(rg *rig) error {
			_, err := rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 4))
			return err
		},
			[]string{"op:topic core.notify.deliver/4", "record:core.outbox.dead_retry:success", "op:retry core.notify.deliver/4"}},
		{"discard", func(rg *rig) error {
			_, err := rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 4))
			return err
		},
			[]string{"op:topic core.notify.deliver/4", "record:core.outbox.dead_discard:success", "op:discard core.notify.deliver/4"}},
		{"purge", func(rg *rig) error {
			rg.op.purgeSkipped = 2
			_, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
			return err
		},
			[]string{"op:status", "record:core.outbox.backlog_purge:success/requested", "op:purge core.notify.deliver",
				"record:core.outbox.backlog_purge:success/completed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rg := newRig()
			if err := c.run(rg); err != nil {
				t.Fatal(err)
			}
			if got := rg.j.all(); !slices.Equal(got, c.want) {
				t.Errorf("steps = %v, want %v", got, c.want)
			}
		})
	}
}

// TestOutboxHTTP_OperationAndRecordsOutliveAHangUp: a client that hangs up once
// its request is recorded cannot cancel the operation (a purge split between its
// partitions is half done) or the records after it, and each of those runs on a
// context with a bound of its own, because a detached context has no deadline.
// The first record keeps the request's context (a client that left before it has
// nothing to be recorded for) with a bound.
func TestOutboxHTTP_OperationAndRecordsOutliveAHangUp(t *testing.T) {
	for _, kind := range []string{"retry", "discard", "purge"} {
		t.Run(kind, func(t *testing.T) {
			rg := newRig()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rg.op.purgeSkipped = 2
			rg.op.taken = "gotham.signal.raised.v1" // the follow-up record of a retry or discard
			rg.rec.onRecord = func(n int) {
				if n == 1 {
					cancel() // the client hangs up once the request is recorded
				}
			}
			var err error
			switch kind {
			case "retry":
				_, err = rg.api.retry(ctx, deadIn(bruce(), "core.notify.deliver", 4))
			case "discard":
				_, err = rg.api.discard(ctx, deadIn(bruce(), "core.notify.deliver", 4))
			case "purge":
				_, err = rg.api.purge(ctx, purgeIn(bruce(), "core.notify.deliver"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(rg.op.opCtx) != 1 {
				t.Fatalf("%d operation calls, want 1", len(rg.op.opCtx))
			}
			if o := rg.op.opCtx[0]; !o.alive || o.remaining <= 0 || o.remaining > 2*time.Minute {
				t.Errorf("the operation's context: %+v, want alive with a deadline within 2m", o)
			}
			recs := rg.rec.seen()
			if len(recs) != 2 {
				t.Fatalf("%d records, want the request's and one follow-up", len(recs))
			}
			if first := recs[0]; !first.alive || first.remaining <= 0 || first.remaining > 10*time.Second {
				t.Errorf("the first record's context: %+v, want the request's, live, with a deadline within 10s", first)
			}
			if after := recs[1]; !after.alive || after.remaining <= 0 || after.remaining > 10*time.Second {
				t.Errorf("the follow-up record's context: %+v, want a live one with a deadline within 10s after the hang-up", after)
			}
		})
	}
}

// TestOutboxHTTP_FailedOperationIsRecordedAsFailed: an operation that is
// refused or fails after its request was recorded gets a second record (outcome
// failure for a refusal, error for anything else) and its refusal maps by
// sentinel to the status a client acts on.
func TestOutboxHTTP_FailedOperationIsRecordedAsFailed(t *testing.T) {
	store := errors.New("pebble: the Batcave flooded")
	run := func(rg *rig, kind string) error {
		var err error
		switch kind {
		case "retry":
			_, err = rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
		case "discard":
			_, err = rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
		case "purge":
			_, err = rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
		}
		return err
	}
	inject := func(rg *rig, kind string, err error) {
		switch kind {
		case "retry":
			rg.op.retryErr = err
		case "discard":
			rg.op.discardErr = err
		case "purge":
			rg.op.purgeErr = err
		}
	}
	wantRecords := func(t *testing.T, rg *rig, kind string, outcome audit.Outcome, reason string) {
		t.Helper()
		recs := rg.rec.recorded()
		if len(recs) != 2 {
			t.Fatalf("%d records, want the request's and the failure's: %+v", len(recs), recs)
		}
		first, second := recs[0], recs[1]
		if first.Outcome != audit.Success || second.Action != first.Action || second.Outcome != outcome || second.Reason != reason {
			t.Errorf("records = %+v and %+v, want the request (success) and a %s/%s one", first, second, outcome, reason)
		}
		if second.Actor.Username != "bruce" || second.Target.ID != "core.notify.deliver" || second.RequestID != "req-batmobile" {
			t.Errorf("the failure record does not carry the request's attribution: %+v", second)
		}
		if kind != "purge" && detailInt(t, audit.Record{Event: second}, "seq") != 1 {
			t.Errorf("the failure record's details = %+v, want the seq", second.Details)
		}
	}
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"dead letter gone", fmt.Errorf("outbox: %w", outbox.ErrDeadNotFound), http.StatusNotFound, "not_found"},
		{"unknown consumer", fmt.Errorf("%w: gotham.dispatch.nobody", outbox.ErrUnknownConsumer), http.StatusNotFound, "not_found"},
		{"changed under the operation", fmt.Errorf("%w: x at seq 1", outbox.ErrDeadConflict), http.StatusConflict, "conflict"},
		{"topic not declared", fmt.Errorf("cannot retry: %w", outbox.ErrUnknownTopic), http.StatusConflict, "conflict"},
		{"topic not durable", fmt.Errorf("cannot retry: %w", outbox.ErrNotDurable), http.StatusConflict, "conflict"},
		{"bad partitions", fmt.Errorf("%w: x declares 0", outbox.ErrBadPartitions), http.StatusConflict, "conflict"},
		{"partitions changed", fmt.Errorf("cannot purge: %w", outboxstore.ErrPartitionsChanged), http.StatusConflict, "conflict"},
	} {
		for _, kind := range []string{"retry", "discard", "purge"} {
			t.Run(c.name+"/"+kind, func(t *testing.T) {
				rg := newRig()
				inject(rg, kind, c.err)
				wantProblem(t, run(rg, kind), c.status, c.code)
				wantRecords(t, rg, kind, audit.Failure, reasonRefused)
			})
		}
	}
	for _, kind := range []string{"retry", "discard", "purge"} {
		t.Run("store failure/"+kind, func(t *testing.T) {
			rg := newRig()
			inject(rg, kind, store)
			err := run(rg, kind)
			if !errors.Is(err, store) {
				t.Errorf("error = %v, want the store's, unchanged", err)
			}
			if he, ok := errors.AsType[*httpapi.Error](err); ok {
				t.Errorf("a store failure became the problem %d %s", he.Status, he.Code)
			}
			wantRecords(t, rg, kind, audit.Error, reasonFailed)
		})
	}
}

// TestOutboxHTTP_ReadFailureStopsTheOperation: the read that names the topic runs
// before the record and the operation (DeadTopic for a retry or discard, the
// status for a purge), and when it fails nothing is applied or recorded. A letter
// that is not there is a 404 with nothing to record.
func TestOutboxHTTP_ReadFailureStopsTheOperation(t *testing.T) {
	boom := errors.New("pebble: the Batcave flooded")
	rg := newRig()
	rg.op.topicErr = boom
	if _, err := rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 1)); !errors.Is(err, boom) {
		t.Errorf("retry = %v", err)
	}
	if _, err := rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1)); !errors.Is(err, boom) {
		t.Errorf("discard = %v", err)
	}
	if got := rg.j.all(); !slices.Equal(got, []string{"op:topic core.notify.deliver/1", "op:topic core.notify.deliver/1"}) {
		t.Errorf("steps = %v, want only the two topic reads", got)
	}

	for _, gone := range []error{outbox.ErrDeadNotFound, outbox.ErrUnknownConsumer} {
		rg = newRig()
		rg.op.topicErr = fmt.Errorf("outbox: %w", gone)
		_, err := rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
		wantProblem(t, err, http.StatusNotFound, "not_found")
		if recs := rg.rec.recorded(); len(recs) != 0 {
			t.Errorf("a request for a letter that is not there was recorded: %+v", recs)
		}
		if got := rg.j.all(); len(got) != 1 {
			t.Errorf("steps = %v, want only the topic read", got)
		}
	}

	rg = newRig()
	rg.op.statusErr = boom
	if _, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver")); !errors.Is(err, boom) {
		t.Errorf("purge = %v", err)
	}
	if got := rg.j.all(); !slices.Equal(got, []string{"op:status"}) {
		t.Errorf("steps = %v, want only the status read", got)
	}
}

// TestOutboxHTTP_PartialPurgeIsRecorded: a purge that fails after some
// partitions committed has discarded messages, so its failure record carries what
// it skipped (the outbox counts committed partitions only) as the error
// "partial"; one that skipped nothing records the plain failure. The error is
// answered even when the failure record cannot be written.
func TestOutboxHTTP_PartialPurgeIsRecorded(t *testing.T) {
	boom := errors.New("pebble: the Batcave flooded")
	rg := newRig()
	rg.op.purgeSkipped, rg.op.purgeErr = 3, boom
	rg.op.status = outbox.Status{Consumers: []outbox.ConsumerStatus{{Name: "core.notify.deliver", Topic: noticeTopic}}}
	_, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the store's", err)
	}
	recs := rg.rec.recorded()
	if len(recs) != 2 {
		t.Fatalf("%d records, want the request's and the failure's", len(recs))
	}
	if recs[0].Reason != reasonRequested || recs[0].Outcome != audit.Success || recs[0].Details["skipped"] != nil {
		t.Errorf("first record = %+v, want the request, with no count", recs[0])
	}
	r := recs[1]
	if r.Action != "core.outbox.backlog_purge" || r.Outcome != audit.Error || r.Reason != reasonPartial ||
		r.Details["skipped"] != 3 || r.Details["topic"] != noticeTopic || r.Actor.Username != "bruce" {
		t.Errorf("failure record = %+v", r)
	}

	// A purge that skipped nothing is the plain failure, whatever the count's type.
	rg = newRig()
	rg.op.purgeErr = boom
	if _, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver")); !errors.Is(err, boom) {
		t.Errorf("error = %v", err)
	}
	if recs := rg.rec.recorded(); len(recs) != 2 || recs[1].Reason != reasonFailed || recs[1].Outcome != audit.Error {
		t.Errorf("records = %+v, want the request and a failed/error one", recs)
	}
}

// TestOutboxHTTP_WhatCannotBeRecordedAfterwardsIsLogged: the records after the
// first are written once the operation ran or failed, so the answer is the
// operation's either way and a write that fails is logged, with who did what to
// which consumer, seq, topic and count, in which request.
func TestOutboxHTTP_WhatCannotBeRecordedAfterwardsIsLogged(t *testing.T) {
	boom := errors.New("pebble: the Batcave flooded")
	disk := errors.New("audit: the disk is full")
	notFirst := func(e audit.Event) error {
		if e.Outcome == audit.Success && e.Reason != reasonCompleted && e.Reason != reasonTopicDiffer {
			return nil // the request's record
		}
		return disk
	}
	for _, c := range []struct {
		name    string
		prepare func(rg *rig)
		run     func(rg *rig) error
		want    []string
		wantErr error
	}{
		{"failed retry", func(rg *rig) { rg.op.retryErr = boom },
			func(rg *rig) error {
				_, err := rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 6))
				return err
			},
			[]string{"core.outbox.dead_retry", "consumer=core.notify.deliver", "seq=6", "topic=" + noticeTopic, "outcome=error", "reason=failed", "Batcave flooded"}, boom},
		{"partial purge", func(rg *rig) { rg.op.purgeSkipped, rg.op.purgeErr = 4, boom },
			func(rg *rig) error {
				_, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
				return err
			},
			[]string{"core.outbox.backlog_purge", "consumer=core.notify.deliver", "skipped=4", "reason=partial", "Batcave flooded"}, boom},
		{"completed purge", func(rg *rig) { rg.op.purgeSkipped = 7 },
			func(rg *rig) error {
				_, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
				return err
			},
			[]string{"core.outbox.backlog_purge", "skipped=7", "reason=completed"}, nil},
		{"topic taken differs", func(rg *rig) { rg.op.taken = "gotham.signal.raised.v1" },
			func(rg *rig) error {
				_, err := rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 6))
				return err
			},
			[]string{"core.outbox.dead_discard", "seq=6", "topic=gotham.signal.raised.v1", "reason=topic_differs"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			rg := newRig()
			rg.rec.errOn = notFirst
			c.prepare(rg)
			err := c.run(rg)
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want the operation's, not the audit's", err)
			}
			if c.wantErr == nil && err != nil {
				t.Errorf("error = %v, want the operation's success", err)
			}
			log := rg.logs.String()
			for _, want := range append([]string{"actor_username=bruce", "actor_user_id=u-bruce", "actor_method=session",
				"actor_credential_id=sess-batcave", "client_addr=192.0.2.10", "request_id=req-batmobile", "the disk is full"}, c.want...) {
				if !strings.Contains(log, want) {
					t.Errorf("the log lacks %q: %q", want, log)
				}
			}
		})
	}
}

// TestOutboxHTTP_TopicTakenThatDiffersIsRecorded: the operation answers the topic
// of the letter its batch took. When that is not the topic the first record
// named (the letters of the consumer changed between the read and the batch) a
// second record names the letter actually taken, with the same seq; when it is,
// there is no second record.
func TestOutboxHTTP_TopicTakenThatDiffersIsRecorded(t *testing.T) {
	for _, kind := range []string{"retry", "discard"} {
		t.Run(kind, func(t *testing.T) {
			rg := newRig()
			rg.op.topic, rg.op.taken = noticeTopic, signalTopic
			var err error
			if kind == "retry" {
				_, err = rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 8))
			} else {
				_, err = rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 8))
			}
			if err != nil {
				t.Fatal(err)
			}
			recs := rg.rec.recorded()
			if len(recs) != 2 {
				t.Fatalf("%d records, want the request's and the correction", len(recs))
			}
			if recs[0].Details["topic"] != noticeTopic || recs[0].Reason != "" {
				t.Errorf("first record = %+v, want the topic read first", recs[0])
			}
			c := recs[1]
			if c.Action != recs[0].Action || c.Outcome != audit.Success || c.Reason != reasonTopicDiffer ||
				c.Details["topic"] != signalTopic || c.Details["seq"] != uint64(8) || c.Actor.Username != "bruce" || c.RequestID != "req-batmobile" {
				t.Errorf("correction = %+v, want the letter actually taken", c)
			}
			if recs[0].Details["topic"] != noticeTopic {
				t.Error("the correction rewrote the first record's details")
			}

			rg = newRig()
			rg.op.topic, rg.op.taken = noticeTopic, noticeTopic
			if kind == "retry" {
				_, err = rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 8))
			} else {
				_, err = rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 8))
			}
			if err != nil || len(rg.rec.recorded()) != 1 {
				t.Errorf("a letter of the topic named: err = %v, %d records, want one", err, len(rg.rec.recorded()))
			}
		})
	}
}

// hangupOp cancels the request's context as its operation starts, the shape of a
// client that disconnects once its request was recorded, over the real outbox.
type hangupOp struct {
	*outbox.Outbox
	cancel context.CancelFunc
}

func (h *hangupOp) RetryDead(ctx context.Context, consumer string, seq uint64) (string, error) {
	h.cancel()
	return h.Outbox.RetryDead(ctx, consumer, seq)
}

func (h *hangupOp) DiscardDead(ctx context.Context, consumer string, seq uint64) (string, error) {
	h.cancel()
	return h.Outbox.DiscardDead(ctx, consumer, seq)
}

func (h *hangupOp) PurgeBacklog(ctx context.Context, consumer string) (int, error) {
	h.cancel()
	return h.Outbox.PurgeBacklog(ctx, consumer)
}

// TestOutboxHTTP_HangUpDoesNotSplitAPurge: over the real outbox and the real
// audit store, a client that hangs up once its purge is recorded does not leave a
// consumer half purged (partitions 0..k advanced, the rest not): every partition
// advances, and the record of what it skipped is written after the hang-up.
func TestOutboxHTTP_HangUpDoesNotSplitAPurge(t *testing.T) {
	e := newEnv(t)
	e.cursors(consumerRespond, signalTopic, signalParts)
	e.sequence(signalTopic, signalParts, 12)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.api.op = &hangupOp{Outbox: e.ob, cancel: cancel}

	out, err := e.api.purge(ctx, purgeIn(bruce(), consumerRespond))
	if err != nil {
		t.Fatalf("a purge whose client hung up once it was recorded: %v", err)
	}
	if out.Skipped != 12 {
		t.Errorf("skipped = %d, want all 12", out.Skipped)
	}
	if c := e.consumerStatus(consumerRespond); c.Pending != 0 {
		t.Errorf("pending = %d after the purge, want every partition advanced", c.Pending)
	}
	var requested, completed int
	for _, r := range e.records() {
		switch r.Reason {
		case reasonRequested:
			requested++
		case reasonCompleted:
			completed++
			if detailInt(t, r, "skipped") != 12 {
				t.Errorf("the completion record's details = %+v", r.Details)
			}
		}
	}
	if requested != 1 || completed != 1 {
		t.Errorf("%d request and %d completion records, want one of each: %+v", requested, completed, e.records())
	}
}

// TestOutboxHTTP_HangUpDoesNotCancelARetryOrDiscard: the same for a retry and a
// discard over the real outbox: the operation completes and is recorded once.
func TestOutboxHTTP_HangUpDoesNotCancelARetryOrDiscard(t *testing.T) {
	for _, kind := range []string{"retry", "discard"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.api.op = &hangupOp{Outbox: e.ob, cancel: cancel}
			var err error
			if kind == "retry" {
				_, err = e.api.retry(ctx, deadIn(bruce(), consumerDeliver, 1))
			} else {
				_, err = e.api.discard(ctx, deadIn(bruce(), consumerDeliver, 1))
			}
			if err != nil {
				t.Fatalf("%s after the client hung up: %v", kind, err)
			}
			if rows := e.deadRows(); len(rows) != 0 {
				t.Errorf("dead letters = %+v, want the operation applied", rows)
			}
			recordOf(t, e, "core.outbox.dead_"+kind)
		})
	}
}

// TestOutboxHTTP_RefusesWhatItCannotAudit: a route mounted without its operator
// or without its audit logger answers 503 before anything is applied, and a
// request that names no caller is a 401.
func TestOutboxHTTP_RefusesWhatItCannotAudit(t *testing.T) {
	calls := map[string]func(a *api) error{
		"retry": func(a *api) error {
			_, err := a.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
			return err
		},
		"discard": func(a *api) error {
			_, err := a.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
			return err
		},
		"purge": func(a *api) error {
			_, err := a.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
			return err
		},
	}
	for name, call := range calls {
		t.Run(name+"/no logger", func(t *testing.T) {
			rg := newRig()
			a := newAPI("/api/core/v1", rg.op, nil, nil)
			wantProblem(t, call(a), http.StatusServiceUnavailable, "unavailable")
			if steps := rg.j.all(); len(steps) != 0 {
				t.Errorf("an unauditable request touched the outbox: %v", steps)
			}
		})
		t.Run(name+"/no operator", func(t *testing.T) {
			rg := newRig()
			a := newAPI("/api/core/v1", nil, nil, rg.rec)
			wantProblem(t, call(a), http.StatusServiceUnavailable, "unavailable")
			if recs := rg.rec.recorded(); len(recs) != 0 {
				t.Errorf("recorded %+v", recs)
			}
		})
	}
	noPrincipal := map[string]func(rg *rig) error{
		"retry": func(rg *rig) error {
			_, err := rg.api.retry(context.Background(), deadIn(nil, "core.notify.deliver", 1))
			return err
		},
		"discard": func(rg *rig) error {
			_, err := rg.api.discard(context.Background(), deadIn(nil, "core.notify.deliver", 1))
			return err
		},
		"purge": func(rg *rig) error {
			_, err := rg.api.purge(context.Background(), purgeIn(nil, "core.notify.deliver"))
			return err
		},
	}
	for name, call := range noPrincipal {
		t.Run(name+"/no principal", func(t *testing.T) {
			rg := newRig()
			wantProblem(t, call(rg), http.StatusUnauthorized, "authentication_required")
			if steps := rg.j.all(); len(steps) != 0 {
				t.Errorf("an unattributable request touched the outbox: %v", steps)
			}
		})
	}
	// A principal with no user (auth disabled) is attributed by method alone, as
	// the router binds it; the handlers only refuse a request naming nobody.
	rg := newRig()
	p := &auth.Principal{Method: auth.MethodAuthDisabled}
	if _, err := rg.api.retry(context.Background(), deadIn(p, "core.notify.deliver", 1)); err != nil {
		t.Fatal(err)
	}
	if recs := rg.rec.recorded(); len(recs) != 1 || recs[0].Actor.Method != string(auth.MethodAuthDisabled) || recs[0].Actor.UserID != "" {
		t.Errorf("records = %+v", recs)
	}
}

// TestOutboxHTTP_UnknownConsumerOrSeq404: an unknown consumer, or a known
// consumer with no dead letter at that seq, is a 404 on all three writes, over
// the real outbox, and changes nothing. The two dead-letter writes name their
// letter before they record anything, so a letter that is not there records
// nothing; a purge cannot know a consumer has no backlog until the outbox has
// looked, so its request is recorded and then closed as refused. A consumer that
// is declared but never ran has no backlog, and the purge says so.
func TestOutboxHTTP_UnknownConsumerOrSeq404(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)

	_, err := e.api.retry(ctx, deadIn(bruce(), "gotham.dispatch.nobody", 1))
	wantProblem(t, err, http.StatusNotFound, "not_found")
	_, err = e.api.discard(ctx, deadIn(bruce(), "gotham.dispatch.nobody", 1))
	wantProblem(t, err, http.StatusNotFound, "not_found")
	_, err = e.api.purge(ctx, purgeIn(bruce(), "gotham.dispatch.nobody"))
	he := wantProblem(t, err, http.StatusNotFound, "not_found")
	if strings.Contains(he.Detail, "never ran") {
		t.Errorf("an unknown consumer was described as one that never ran: %q", he.Detail)
	}

	_, err = e.api.retry(ctx, deadIn(bruce(), consumerDeliver, 7))
	wantProblem(t, err, http.StatusNotFound, "not_found")
	_, err = e.api.discard(ctx, deadIn(bruce(), consumerDeliver, 7))
	wantProblem(t, err, http.StatusNotFound, "not_found")
	_, err = e.api.discard(ctx, deadIn(bruce(), consumerMirror, 1)) // known, but its letter is the other consumer's
	wantProblem(t, err, http.StatusNotFound, "not_found")

	// consumerMirror is on the core list and never registered or ran: no cursors.
	_, err = e.api.purge(ctx, purgeIn(bruce(), consumerMirror))
	he = wantProblem(t, err, http.StatusNotFound, "not_found")
	if !strings.Contains(he.Detail, "never ran") {
		t.Errorf("detail = %q, want it to say the consumer has no backlog", he.Detail)
	}

	if rows := e.deadRows(); len(rows) != 1 {
		t.Errorf("dead letters = %+v, want the one untouched", rows)
	}
	// A 404 from the status read or the dead-letter read records nothing, for a
	// purge as for a retry or discard: no audit row names a consumer or letter
	// that was never there.
	if recs := e.records(); len(recs) != 0 {
		t.Fatalf("%d records for requests that were refused before anything was recorded: %+v", len(recs), recs)
	}
}

// ---- consumers and topics ---------------------------------------------------

// TestOutboxHTTP_ConsumersAndTopicsList: the consumers view lists every
// declared consumer, a disabled module's with the backlog the store keeps for
// it, with lag, pending, dead count and intake backlog as the outbox reports
// them; the topics view lists the catalog's topics.
func TestOutboxHTTP_ConsumersAndTopicsList(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cursors(consumerRespond, signalTopic, signalParts)
	e.sequence(signalTopic, signalParts, 4)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
	e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 2, true)
	b := e.st.OpenBatch()
	mustDo(t, outbox.Stage(e.ob.Writer("core"), b, notice(), "wayne-manor", Dispatch{Villain: "Riddler", Block: 2}))
	mustDo(t, b.Commit(ctx, storage.Sync))
	b.Close()

	page, err := e.api.consumers(ctx, pageIn(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Count != 3 || page.Limit != 50 || page.Offset != 0 {
		t.Fatalf("page = %+v, want 3 consumers", page)
	}
	by := map[string]OutboxConsumer{}
	for _, c := range page.Items {
		by[c.Name] = c
	}
	gotham := by[consumerRespond]
	if gotham.Enabled || !gotham.Declared || gotham.Module != "gotham" || gotham.Topic != signalTopic ||
		gotham.RetainedBacklog != 4 || gotham.Pending != 4 || gotham.PendingCapped || gotham.Retrying != 0 {
		t.Errorf("disabled consumer = %+v, want a declared, disabled one holding 4", gotham)
	}
	deliver := by[consumerDeliver]
	if !deliver.Enabled || deliver.Dead != 2 || deliver.Topic != noticeTopic || deliver.IntakeBacklog != 1 || deliver.IntakeCapped || deliver.RetainedBacklog != 0 {
		t.Errorf("enabled consumer = %+v, want 2 dead letters and 1 intake row", deliver)
	}
	mirror := by[consumerMirror]
	if mirror.Enabled || !mirror.Declared || mirror.Topic != "" || mirror.Pending != 0 {
		t.Errorf("a consumer that never ran = %+v", mirror)
	}
	if !strings.Contains(toJSON(t, page), `"stopped":[]`) {
		t.Errorf("a page with nothing stopped encodes stopped as %s, want an empty array", toJSON(t, page))
	}
	var one map[string]any
	if err := json.Unmarshal([]byte(toJSON(t, deliver)), &one); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "module", "topic", "enabled", "declared", "pending", "pending_capped", "retrying", "lag_ms",
		"dead", "intake_backlog", "intake_capped", "retained_backlog"} {
		if _, ok := one[k]; !ok {
			t.Errorf("the consumer has no %q field: %v", k, one)
		}
	}

	paged, err := e.api.consumers(ctx, pageIn(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(paged.Items) != 1 || paged.Count != 1 || paged.Total != 3 || paged.Offset != 1 || paged.Items[0].Name != page.Items[1].Name {
		t.Errorf("page 2 of 1 = %+v, want the second consumer of three", paged)
	}
	past, err := e.api.consumers(ctx, pageIn(10, 99))
	if err != nil || len(past.Items) != 0 || past.Total != 3 || toJSON(t, past.Items) != "[]" {
		t.Errorf("a page past the end = %+v, %v", past, err)
	}

	topics, err := e.api.topics(ctx, pageIn(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tp := range topics.Items {
		names = append(names, tp.Name)
	}
	if want := []string{noticeTopic, signalTopic, busTopic}; !slices.Equal(names, want) || topics.Total != 3 {
		t.Fatalf("topics = %v (total %d), want %v", names, topics.Total, want)
	}
	notify, signalT, bus := topics.Items[0], topics.Items[1], topics.Items[2]
	if notify.Permission != "core:system.view" || notify.Durable == nil || notify.Durable.Partitions != noticeParts || notify.Notify != nil || notify.Realtime != nil {
		t.Errorf("notice topic = %+v", notify)
	}
	if signalT.Durable == nil || signalT.Durable.Partitions != signalParts || signalT.Permission != "gotham:view" || len(signalT.Schema) == 0 || !json.Valid(signalT.Schema) {
		t.Errorf("signal topic = %+v", signalT)
	}
	if bus.Durable != nil || bus.Notify == nil || bus.Notify.Title != "A villain escaped" || bus.Notify.DefaultSeverity != "warning" ||
		bus.Realtime == nil || bus.Realtime.CoalesceMS != 250 {
		t.Errorf("bus-only topic = %+v", bus)
	}
	raw := toJSON(t, bus)
	if strings.Contains(raw, `"durable"`) {
		t.Errorf("a bus-only topic encodes durable: %s", raw)
	}
	pt, err := e.api.topics(ctx, pageIn(1, 2))
	if err != nil || len(pt.Items) != 1 || pt.Items[0].Name != busTopic || pt.Count != 1 || pt.Total != 3 {
		t.Errorf("topics page 3 of 1 = %+v, %v", pt, err)
	}
}

// TestOutboxHTTP_ConsumersStoppedList: everything the outbox says the relay will
// not work on is listed, whole, on every page, whether or not a consumer is
// involved: a pair with its partition, the counter of a consumer's undecodable
// letters with none, a topic, and the key families the status read names. A
// partition is only ever a pair's.
func TestOutboxHTTP_ConsumersStoppedList(t *testing.T) {
	rg := newRig()
	rg.op.status = outbox.Status{
		Consumers: []outbox.ConsumerStatus{
			{Name: "core.notify.deliver", Topic: noticeTopic, Lag: 1500 * time.Millisecond},
			{Name: "core.notify.mirror", Topic: noticeTopic},
		},
		Stopped: []outbox.StoppedStatus{
			{Consumer: "core.notify.deliver", Topic: noticeTopic, Partition: 1, Cause: "corrupt_row"},
			{Consumer: "core.notify.deliver", Topic: noticeTopic, Partition: -1, Cause: "corrupt_row"},
			{Consumer: "gotham.dispatch.respond", Topic: signalTopic, Partition: 0, Cause: "unknown_version"},
			{Topic: noticeTopic, Cause: "partitions_changed"},
			{Topic: "dead/", Cause: "corrupt_row"},
			{Topic: "deadn/", Cause: "corrupt_row"},
		},
	}
	want := `[{"cause":"corrupt_row","topic":"` + noticeTopic + `","consumer":"core.notify.deliver","partition":1},` +
		`{"cause":"corrupt_row","topic":"` + noticeTopic + `","consumer":"core.notify.deliver"},` +
		`{"cause":"unknown_version","topic":"` + signalTopic + `","consumer":"gotham.dispatch.respond","partition":0},` +
		`{"cause":"partitions_changed","topic":"` + noticeTopic + `"},` +
		`{"cause":"corrupt_row","topic":"dead/"},{"cause":"corrupt_row","topic":"deadn/"}]`
	for _, pg := range []struct{ limit, offset int }{{50, 0}, {1, 1}, {10, 99}} {
		page, err := rg.api.consumers(context.Background(), pageIn(pg.limit, pg.offset))
		if err != nil {
			t.Fatal(err)
		}
		if got := toJSON(t, page.Stopped); got != want {
			t.Errorf("page %+v stopped = %s\nwant %s", pg, got, want)
		}
	}
	page, _ := rg.api.consumers(context.Background(), pageIn(50, 0))
	if page.Items[0].LagMS != 1500 {
		t.Errorf("lag_ms = %d, want 1500", page.Items[0].LagMS)
	}

	// A real stop: the topic was sequenced over another partition count.
	e := newEnv(t)
	e.sequence(noticeTopic, noticeParts+1, 1)
	real, err := e.api.consumers(context.Background(), pageIn(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := toJSON(t, real.Stopped); got != `[{"cause":"partitions_changed","topic":"`+noticeTopic+`"}]` {
		t.Errorf("stopped over a changed partition count = %s", got)
	}
}

// TestOutboxHTTP_ReadsAreIndependent: the topics route needs no operator and the
// consumers route no catalog; each is 503 only for its own missing piece, and
// neither writes an audit record.
func TestOutboxHTTP_ReadsAreIndependent(t *testing.T) {
	e := newEnv(t)
	onlyCat := newAPI("/api/core/v1", nil, e.cat, nil)
	if _, err := onlyCat.topics(context.Background(), pageIn(50, 0)); err != nil {
		t.Errorf("topics without an operator: %v", err)
	}
	_, err := onlyCat.consumers(context.Background(), pageIn(50, 0))
	wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")

	rg := newRig()
	if _, err := rg.api.consumers(context.Background(), pageIn(50, 0)); err != nil {
		t.Errorf("consumers without a catalog: %v", err)
	}
	_, err = rg.api.topics(context.Background(), pageIn(50, 0))
	wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")

	boom := errors.New("pebble: the Batcave flooded")
	rg.op.statusErr = boom
	if _, err := rg.api.consumers(context.Background(), pageIn(50, 0)); !errors.Is(err, boom) {
		t.Errorf("consumers = %v, want the store's", err)
	}
	if recs := rg.rec.recorded(); len(recs) != 0 {
		t.Errorf("a read wrote %+v", recs)
	}
}

// TestOutboxHTTP_RegisterTakesNilCollaborators: a router built before the outbox
// exists registers all six routes, so the document is complete without it.
func TestOutboxHTTP_RegisterTakesNilCollaborators(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discardLogger()})
	Register(r.CoreGroup(), nil, nil, nil, WithLogger(nil))
	n := 0
	for range r.Routes() {
		n++
	}
	if n != len(wantRoutes) {
		t.Errorf("%d routes", n)
	}
	if _, err := r.OpenAPI("core"); err != nil {
		t.Errorf("OpenAPI: %v", err)
	}
}

// TestOutboxHTTP_TopicDetailOnlyWhenKnown: the first audit record of a retry or
// discard names the topic DeadTopic read from the consumer's letter and never
// the status read's (the status read is not consulted at all), and a purge's
// records take the topic from the status read. A purge of a consumer the status
// does not list, or lists with no topic, is a 404 with nothing recorded.
func TestOutboxHTTP_TopicDetailOnlyWhenKnown(t *testing.T) {
	const consumer = "core.notify.deliver"
	status := func(topic string) []outbox.ConsumerStatus {
		return []outbox.ConsumerStatus{{Name: consumer, Topic: topic}}
	}
	for _, c := range []struct {
		name      string
		consumers []outbox.ConsumerStatus
		wantPurge string // "" is a 404 with no record
	}{
		{"status names a topic", status(signalTopic), signalTopic},
		{"consumer unknown to the status", nil, ""},
		{"consumer without a topic", status(""), ""},
	} {
		for _, kind := range []string{"retry", "discard", "purge"} {
			t.Run(c.name+"/"+kind, func(t *testing.T) {
				rg := newRig()
				rg.op.topic = noticeTopic
				rg.op.status = outbox.Status{Consumers: c.consumers}
				var err error
				want, wantRecs := noticeTopic, 1
				switch kind {
				case "retry":
					_, err = rg.api.retry(context.Background(), deadIn(bruce(), consumer, 1))
				case "discard":
					_, err = rg.api.discard(context.Background(), deadIn(bruce(), consumer, 1))
				case "purge":
					_, err = rg.api.purge(context.Background(), purgeIn(bruce(), consumer))
					want, wantRecs = c.wantPurge, 2 // the request and the count
					if c.wantPurge == "" {
						wantProblem(t, err, http.StatusNotFound, "not_found")
						if recs := rg.rec.recorded(); len(recs) != 0 {
							t.Fatalf("%d records for a purge refused before it was recorded: %+v", len(recs), recs)
						}
						if slices.Contains(rg.j.all(), "op:purge") {
							t.Fatalf("the purge ran: %v", rg.j.all())
						}
						return
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				recs := rg.rec.recorded()
				if len(recs) != wantRecs {
					t.Fatalf("%d records, want %d", len(recs), wantRecs)
				}
				for _, r := range recs {
					got, has := r.Details["topic"]
					switch {
					case want == "" && has:
						t.Errorf("details name topic %q, want no topic key: %v", got, r.Details)
					case want != "" && got != want:
						t.Errorf("details topic = %v, want %q", got, want)
					}
				}
				if kind != "purge" && slices.Contains(rg.j.all(), "op:status") {
					t.Errorf("a dead-letter operation read the status: %v", rg.j.all())
				}
			})
		}
	}
}

// TestOutboxHTTP_UnavailableAsksToRetry: a route mounted before the outbox is
// there answers 503 unavailable with Retry-After, through the router's own writer.
func TestOutboxHTTP_UnavailableAsksToRetry(t *testing.T) {
	a := newAPI("/api/core/v1", nil, nil, nil)
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discardLogger()})
	httpapi.Handle(r.CoreGroup(), httpapi.Route{
		Method: http.MethodGet, Path: "/probe/{consumer}/{seq}", Name: "probeUnavailable", Perm: rbac.Public,
	}, func(ctx context.Context, in *httpapi.In[deadReq]) (httpapi.NoContent, error) { return a.retry(ctx, in) })
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/core/v1/probe/core.notify.deliver/1", nil))
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"code":"unavailable"`) {
		t.Errorf("answer = %d %s, want 503 unavailable", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want 5", got)
	}
}

// listedCatalog is a Catalog that lists its topics in the order it was given,
// as the interface does not promise a sorted one.
type listedCatalog []events.TopicSpec

func (c listedCatalog) Lookup(name string) (events.TopicSpec, bool) {
	for _, s := range c {
		if s.Name == name {
			return s, true
		}
	}
	return events.TopicSpec{}, false
}

func (c listedCatalog) All() iter.Seq[events.TopicSpec] { return slices.Values(c) }

// TestOutboxHTTP_TopicsAreSortedByName: the route orders the catalog itself, so
// a page boundary never moves with the catalog's own listing order.
func TestOutboxHTTP_TopicsAreSortedByName(t *testing.T) {
	cat := listedCatalog{{Name: signalTopic}, {Name: busTopic}, {Name: noticeTopic}}
	a := newAPI("/api/core/v1", nil, cat, nil)
	page, err := a.topics(context.Background(), pageIn(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tp := range page.Items {
		names = append(names, tp.Name)
	}
	if want := []string{noticeTopic, signalTopic, busTopic}; !slices.Equal(names, want) {
		t.Errorf("topics = %v, want %v", names, want)
	}
}

// TestOutboxHTTP_DeadOperationsFindTheLetterBySeq: an operator names the consumer
// and the seq, and the audit record names the topic of the letter the operation
// took, not of the consumer's first letter: with letters on two topics (a
// consumer that changed topics between builds) and the same seq on both, the
// outbox takes the first in key order and so does the record.
func TestOutboxHTTP_DeadOperationsFindTheLetterBySeq(t *testing.T) {
	e := newEnv(t)
	e.deadLetter(consumerMirror, noticeTopic, noticeParts, 3, false)
	e.deadLetter(consumerMirror, signalTopic, signalParts, 7, false)
	e.deadLetter(consumerMirror, noticeTopic, noticeParts, 5, false)
	e.deadLetter(consumerMirror, signalTopic, signalParts, 5, false)
	for _, c := range []struct {
		seq   uint64
		topic string
	}{{7, signalTopic}, {3, noticeTopic}, {5, noticeTopic}} {
		if _, err := e.api.discard(context.Background(), deadIn(bruce(), consumerMirror, c.seq)); err != nil {
			t.Fatal(err)
		}
		var hit []audit.Record
		for _, r := range e.records() {
			if detailInt(t, r, "seq") == int(c.seq) {
				hit = append(hit, r)
			}
		}
		if len(hit) != 1 || hit[0].Details["topic"] != c.topic {
			t.Errorf("discard of seq %d recorded %+v, want one naming topic %s", c.seq, hit, c.topic)
		}
	}
	// The seq both topics held went from the first topic: the other is what is left.
	rows := e.deadRows()
	if len(rows) != 1 || rows[0].Topic != signalTopic || rows[0].Seq != 5 {
		t.Errorf("dead letters left = %+v, want only %s seq 5", rows, signalTopic)
	}
}

// TestOutboxHTTP_CursorFilterHashIsPinned: a cursor is handed to a client and
// comes back on a later request, possibly to the next build, so what makes a
// cursor this route's (the FNV-1a hash of the route's name) is part of the wire
// contract. A cursor minted here, from the documented name and independently of
// the route's own helper, is accepted.
func TestOutboxHTTP_CursorFilterHashIsPinned(t *testing.T) {
	h := fnv.New64a()
	_, _ = h.Write([]byte("core.outbox.dead"))
	key := outboxstore.DeadKey(consumerDeliver, noticeTopic, 3)
	rg := newRig()
	if _, err := rg.api.listDead(context.Background(), listIn(10, httpapi.EncodeCursor(key, h.Sum64()))); err != nil {
		t.Fatalf("a cursor of the route's documented hash was refused: %v", err)
	}
	if want := base64.RawURLEncoding.EncodeToString(key); rg.op.gotAfter != want {
		t.Errorf("went on after %q, want %q", rg.op.gotAfter, want)
	}
}

// ---- the topic of the letter, and the neighbours the operation does not read --

// TestOutboxHTTP_DeadOperationsIgnoreUnreadableNeighbours: the outbox finds the
// letter by its key and decodes only that one, so a row beside it that does not
// decode (another letter of the same consumer, a later consumer's letter or a
// stray key among them) must not fail a retry or a discard the outbox itself
// carries out. An operator acts at the moment these rows exist.
func TestOutboxHTTP_DeadOperationsIgnoreUnreadableNeighbours(t *testing.T) {
	const later = "wayne.vault.zeta"
	stray := outboxstore.DeadKey(later, noticeTopic, 1)
	stray = stray[:len(stray)-3]
	if _, _, _, err := outboxstore.ParseDeadKey(stray); err == nil {
		t.Fatal("the fixture's stray key parses as a dead letter key")
	}
	for _, c := range []struct {
		name string
		key  []byte
	}{
		{"an undecodable letter at another seq of the same consumer", outboxstore.DeadKey(consumerDeliver, noticeTopic, 2)},
		{"an undecodable letter of a later consumer", outboxstore.DeadKey(later, signalTopic, 1)},
		{"a stray key among a later consumer's letters", stray},
	} {
		for _, kind := range []string{"retry", "discard"} {
			t.Run(c.name+"/"+kind, func(t *testing.T) {
				e := newEnv(t)
				e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true)
				e.putRaw(c.key, []byte(`j{"v":1,"id":`))
				var err error
				if kind == "retry" {
					_, err = e.api.retry(context.Background(), deadIn(bruce(), consumerDeliver, 1))
				} else {
					_, err = e.api.discard(context.Background(), deadIn(bruce(), consumerDeliver, 1))
				}
				if err != nil {
					t.Fatalf("%s failed over a row the outbox does not read: %v", kind, err)
				}
				r := recordOf(t, e, "core.outbox.dead_"+kind)
				if r.Details["topic"] != noticeTopic || detailInt(t, r, "seq") != 1 {
					t.Errorf("record details = %+v, want the letter's topic and seq", r.Details)
				}
				if rows := e.intake(noticeTopic); (kind == "retry") != (len(rows) == 1) {
					t.Errorf("intake = %+v after a %s", rows, kind)
				}
			})
		}
	}
}

// TestOutboxHTTP_RequestRecordKeepsTheRequestsContext: the record of the request
// is written on the request's own context (bounded), so a client that is already
// gone gets no operation: the write fails and nothing is changed. Only what
// follows an applied operation outlives the request.
func TestOutboxHTTP_RequestRecordKeepsTheRequestsContext(t *testing.T) {
	for _, kind := range []string{"retry", "discard", "purge"} {
		t.Run(kind, func(t *testing.T) {
			rg := newRig()
			rg.rec.honorCtx = true
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			switch kind {
			case "retry":
				_, err = rg.api.retry(ctx, deadIn(bruce(), "core.notify.deliver", 4))
			case "discard":
				_, err = rg.api.discard(ctx, deadIn(bruce(), "core.notify.deliver", 4))
			case "purge":
				_, err = rg.api.purge(ctx, purgeIn(bruce(), "core.notify.deliver"))
			}
			wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")
			for _, step := range rg.j.all() {
				if strings.HasPrefix(step, "op:retry") || strings.HasPrefix(step, "op:discard") || strings.HasPrefix(step, "op:purge") {
					t.Errorf("the operation ran for a client that had hung up: %v", rg.j.all())
				}
			}
		})
	}
}

// A client that hung up before the first record neither reaches the audit store
// (which counts a canceled commit as a write error) nor logs at ERROR: the
// request ends 503 with the operation not run and the audit log untouched.
func TestOutboxHTTP_HangUpBeforeTheFirstRecordIsNotAnAuditFailure(t *testing.T) {
	for _, kind := range []string{"retry", "discard", "purge"} {
		t.Run(kind, func(t *testing.T) {
			rg := newRig()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			switch kind {
			case "retry":
				_, err = rg.api.retry(ctx, deadIn(bruce(), "core.notify.deliver", 4))
			case "discard":
				_, err = rg.api.discard(ctx, deadIn(bruce(), "core.notify.deliver", 4))
			case "purge":
				_, err = rg.api.purge(ctx, purgeIn(bruce(), "core.notify.deliver"))
			}
			wantProblem(t, err, http.StatusServiceUnavailable, "unavailable")
			if got := rg.rec.recorded(); len(got) != 0 {
				t.Errorf("the audit store was written for a client that had hung up: %+v", got)
			}
			if logs := rg.logs.String(); strings.Contains(logs, "level=ERROR") {
				t.Errorf("a hang-up before the first record logged at ERROR: %s", logs)
			}
		})
	}
}

// TestOutboxHTTP_UnrecordedRequestAsksToRetry: the 503 of a request whose record
// could not be written reaches the client as a 503 with Retry-After and says
// that nothing was changed, so the client knows a retry is safe.
func TestOutboxHTTP_UnrecordedRequestAsksToRetry(t *testing.T) {
	rg := newRig()
	rg.rec.err = errors.New("audit: the disk is full")
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discardLogger()})
	httpapi.Handle(r.CoreGroup(), httpapi.Route{
		Method: http.MethodGet, Path: "/probe/{consumer}/{seq}", Name: "probeUnrecorded", Perm: rbac.Public,
	}, func(ctx context.Context, in *httpapi.In[deadReq]) (httpapi.NoContent, error) {
		in.Principal = bruce()
		return rg.api.retry(ctx, in)
	})
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/core/v1/probe/core.notify.deliver/1", nil))
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "nothing was changed") {
		t.Errorf("answer = %d %s, want 503 saying nothing was changed", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want 5", got)
	}
}

// TestOutboxHTTP_IsRefusalBounds: a refusal is a 4xx problem and nothing else.
func TestOutboxHTTP_IsRefusalBounds(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&httpapi.Error{Status: 399}, false},
		{&httpapi.Error{Status: 400}, true},
		{&httpapi.Error{Status: 404}, true},
		{&httpapi.Error{Status: 499}, true},
		{&httpapi.Error{Status: 500}, false},
		{&httpapi.Error{Status: 503}, false},
		{fmt.Errorf("wrapped: %w", &httpapi.Error{Status: 409}), true},
		{errors.New("pebble: the Batcave flooded"), false},
		{nil, false},
	} {
		if got := isRefusal(c.err); got != c.want {
			t.Errorf("isRefusal(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestOutboxHTTP_CloneWithCopies: the copy has the base's entries and the extra
// ones (extra wins), whether or not there is a base, and writing to it never
// reaches the base, which every copy of an event shares.
func TestOutboxHTTP_CloneWithCopies(t *testing.T) {
	base := map[string]any{"seq": 4, "topic": "gotham.signal.raised.v1"}
	got := cloneWith(base, map[string]any{"skipped": 2, "topic": "gotham.notice.v1"})
	if len(got) != 3 || got["seq"] != 4 || got["skipped"] != 2 || got["topic"] != "gotham.notice.v1" {
		t.Errorf("cloneWith = %v", got)
	}
	got["extra"] = true
	if len(base) != 2 || base["topic"] != "gotham.signal.raised.v1" {
		t.Errorf("the base changed: %v", base)
	}
	if got := cloneWith(nil, map[string]any{"skipped": 2}); len(got) != 1 || got["skipped"] != 2 {
		t.Errorf("cloneWith over no base = %v", got)
	}
	if got := cloneWith(nil, nil); got == nil {
		t.Error("cloneWith over nothing = nil, want a map that can be written to")
	}
}
