// file: core/internal/outboxhttp/routes_test.go
// version: 1.0.1
// guid: b91b2c32-bee4-4aac-a400-6fb0e871d709
// last-edited: 2026-11-19

package outboxhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
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

// fakeOp is an Operator that answers what the test set and logs every call.
type fakeOp struct {
	j *journal

	status    outbox.Status
	statusErr error

	retryErr, discardErr error
	purgeSkipped         int
	purgeErr             error

	letters  []outbox.DeadLetter
	listNext string
	listErr  error

	mu        sync.Mutex
	gotAfter  string
	gotLimit  int
	listCalls int
}

func (f *fakeOp) ListDead(_ context.Context, after string, limit int) ([]outbox.DeadLetter, string, error) {
	f.j.add("op:list")
	f.mu.Lock()
	f.gotAfter, f.gotLimit = after, limit
	f.listCalls++
	f.mu.Unlock()
	return f.letters, f.listNext, f.listErr
}

func (f *fakeOp) RetryDead(_ context.Context, consumer string, seq uint64) error {
	f.j.add(fmt.Sprintf("op:retry %s/%d", consumer, seq))
	return f.retryErr
}

func (f *fakeOp) DiscardDead(_ context.Context, consumer string, seq uint64) error {
	f.j.add(fmt.Sprintf("op:discard %s/%d", consumer, seq))
	return f.discardErr
}

func (f *fakeOp) PurgeBacklog(_ context.Context, consumer string) (int, error) {
	f.j.add("op:purge " + consumer)
	return f.purgeSkipped, f.purgeErr
}

func (f *fakeOp) Status(context.Context) (outbox.Status, error) {
	f.j.add("op:status")
	return f.status, f.statusErr
}

// fakeRec is an audit.Logger that keeps what it is given and can fail.
type fakeRec struct {
	j   *journal
	err error

	mu       sync.Mutex
	events   []audit.Event
	ctxAlive []bool // whether the context of each Record was still live
}

func (f *fakeRec) Record(ctx context.Context, e audit.Event) error {
	f.j.add("record:" + e.Action + ":" + string(e.Outcome))
	f.mu.Lock()
	f.events = append(f.events, e)
	f.ctxAlive = append(f.ctxAlive, ctx.Err() == nil)
	f.mu.Unlock()
	return f.err
}

func (f *fakeRec) RecordIn(context.Context, *storage.Batch, audit.Event) error {
	panic("outboxhttp records synchronously after the operation, never into a batch")
}

func (f *fakeRec) Emit(context.Context, audit.Event) { panic("outboxhttp never emits asynchronously") }

func (f *fakeRec) recorded() []audit.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// rig is the fakes over one api.
type rig struct {
	j   *journal
	op  *fakeOp
	rec *fakeRec
	api *api
}

func newRig() *rig {
	j := &journal{}
	op, rec := &fakeOp{j: j}, &fakeRec{j: j}
	return &rig{j: j, op: op, rec: rec, api: newAPI("/api/core/v1", op, nil, rec, WithLogger(discardLogger()))}
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

// TestOutboxHTTP_ListDeadNeedsSystemManage: core:system.view alone is refused
// (403) on the dead-letter list and on both dead-letter writes; core:system.manage
// is let through. Route permission (stage 13) arrives at F4-08, so each cell
// skips; F6-03, which mounts the package, un-skips them.
func TestOutboxHTTP_ListDeadNeedsSystemManage(t *testing.T) {
	for _, c := range []struct{ name, method, path string }{
		{"list", http.MethodGet, "/outbox/dead"},
		{"retry", http.MethodPost, "/outbox/dead/core.notify.deliver/1/retry"},
		{"discard", http.MethodDelete, "/outbox/dead/core.notify.deliver/1"},
		{"purge", http.MethodDelete, "/outbox/consumers/core.notify.deliver/backlog"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
		})
	}
}

// TestOutboxHTTP_PurgeNeedsInteractive: an API-key principal holding
// core:system.manage is refused by the interactive guard; an interactive
// session is let through. Skipped until stage 13 (F4-08).
func TestOutboxHTTP_PurgeNeedsInteractive(t *testing.T) {
	for _, c := range []struct {
		name   string
		method auth.Method
		want   int
	}{
		{"api key", auth.MethodAPIKey, http.StatusForbidden},
		{"session", auth.MethodSession, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
		})
	}
}

// TestOutboxHTTP_ConsumersAndTopicsNeedSystemView: both read routes answer 401
// with no principal and 200 to a holder of core:system.view. Skipped until
// stage 13 (F4-08).
func TestOutboxHTTP_ConsumersAndTopicsNeedSystemView(t *testing.T) {
	for _, c := range []struct{ name, path string }{
		{"consumers", "/outbox/consumers"},
		{"topics", "/events/topics"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Skip("needs stage 13: F4-08")
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
		d.IdempotencyKey != "01J0000000000000000BATMN01" || d.Producer != "core" || d.StagedAt.IsZero() || d.DeadAt.IsZero() {
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
	eventually(t, "the retried message to be delivered", func() bool { return len(e.got.all()) >= 1 })
	time.Sleep(50 * time.Millisecond) // a second delivery would arrive by now
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

	recs := e.records()
	if len(recs) != 2 {
		t.Fatalf("%d audit records after two purges, want 2", len(recs))
	}
	first := recs[1] // newest first
	if first.Action != "core.outbox.backlog_purge" || first.Outcome != audit.Success || first.Actor.Username != "bruce" {
		t.Errorf("record = %+v", first)
	}
	if first.Route != "DELETE /api/core/v1/outbox/consumers/{consumer}/backlog" || first.Target.ID != consumerRespond {
		t.Errorf("route/target = %q / %+v", first.Route, first.Target)
	}
	if detailInt(t, first, "skipped") != 5 || first.Details["topic"] != signalTopic || len(first.Details) != 2 {
		t.Errorf("details = %+v, want skipped and topic only", first.Details)
	}
	if detailInt(t, recs[0], "skipped") != 0 {
		t.Errorf("the second purge's details = %+v", recs[0].Details)
	}
}

// ---- audit failure ----------------------------------------------------------

// TestOutboxHTTP_AuditFailureFailsRequest: with the audit store's writes failing,
// each state change answers 500 audit_write_failed saying the operation was
// applied and not audited, the failure is counted on the sync path, and the
// operation did commit (no record was written before it).
func TestOutboxHTTP_AuditFailureFailsRequest(t *testing.T) {
	type cell struct {
		name    string
		seed    func(e *env)
		run     func(e *env) error
		applied func(t *testing.T, e *env)
	}
	cells := []cell{
		{"retry",
			func(e *env) { e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true) },
			func(e *env) error {
				_, err := e.api.retry(context.Background(), deadIn(bruce(), consumerDeliver, 1))
				return err
			},
			func(t *testing.T, e *env) {
				if rows := e.deadRows(); len(rows) != 0 {
					t.Errorf("dead letters = %+v, want the retry applied", rows)
				}
				if rows := e.intake(noticeTopic); len(rows) != 1 {
					t.Errorf("intake = %+v, want the retried message", rows)
				}
			}},
		{"discard",
			func(e *env) { e.deadLetter(consumerDeliver, noticeTopic, noticeParts, 1, true) },
			func(e *env) error {
				_, err := e.api.discard(context.Background(), deadIn(bruce(), consumerDeliver, 1))
				return err
			},
			func(t *testing.T, e *env) {
				if rows := e.deadRows(); len(rows) != 0 {
					t.Errorf("dead letters = %+v, want the discard applied", rows)
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
				if c := e.consumerStatus(consumerRespond); c.Pending != 0 {
					t.Errorf("pending = %d, want the purge applied", c.Pending)
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
			he := wantProblem(t, err, http.StatusInternalServerError, "audit_write_failed")
			if !strings.Contains(he.Detail, "applied") || !strings.Contains(he.Detail, "audit") {
				t.Errorf("detail = %q, want it to say the operation was applied and not audited", he.Detail)
			}
			if he.Err == nil {
				t.Error("the cause of the failed audit write was dropped")
			}
			if got := e.auditWriteErrors(); got != before+1 {
				t.Errorf("audit_write_errors_total{sync} = %v, want %v (one more)", got, before+1)
			}
			c.applied(t, e)
			if log := e.logBuf.String(); !strings.Contains(log, "could not be written") {
				t.Errorf("the failure was not logged: %q", log)
			}
		})
	}
}

// ---- order and mapping, over fakes ------------------------------------------

// TestOutboxHTTP_RecordsAfterTheOperation: the audit record is written only
// after the operation ran and committed, with Outcome success, and from a
// context that survives the request's cancellation.
func TestOutboxHTTP_RecordsAfterTheOperation(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(rg *rig) error
		want []string
	}{
		{"retry", func(rg *rig) error {
			_, err := rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 4))
			return err
		},
			[]string{"op:list", "op:retry core.notify.deliver/4", "record:core.outbox.dead_retry:success"}},
		{"discard", func(rg *rig) error {
			_, err := rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 4))
			return err
		},
			[]string{"op:list", "op:discard core.notify.deliver/4", "record:core.outbox.dead_discard:success"}},
		{"purge", func(rg *rig) error {
			_, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
			return err
		},
			[]string{"op:status", "op:purge core.notify.deliver", "record:core.outbox.backlog_purge:success"}},
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

	// A client that hangs up after the commit still gets its record.
	rg := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	rg.op.purgeSkipped = 2
	rg.api.op = &cancelingOp{fakeOp: rg.op, cancel: cancel}
	if _, err := rg.api.purge(ctx, purgeIn(bruce(), "core.notify.deliver")); err != nil {
		t.Fatal(err)
	}
	if rec := rg.rec.recorded(); len(rec) != 1 || !rg.rec.ctxAlive[0] {
		t.Errorf("records = %d, live context = %v; the audit write must outlive the request", len(rec), rg.rec.ctxAlive)
	}
}

// cancelingOp cancels the request's context as its purge returns.
type cancelingOp struct {
	*fakeOp
	cancel context.CancelFunc
}

func (c *cancelingOp) PurgeBacklog(ctx context.Context, consumer string) (int, error) {
	n, err := c.fakeOp.PurgeBacklog(ctx, consumer)
	c.cancel()
	return n, err
}

// TestOutboxHTTP_FailedOperationWritesNoRecord: an operation that is refused or
// fails changes nothing and so records nothing, and its refusal maps by
// sentinel to the status a client acts on.
func TestOutboxHTTP_FailedOperationWritesNoRecord(t *testing.T) {
	store := errors.New("pebble: the Batcave flooded")
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
		{"topic not durable", fmt.Errorf("%w: %w", outbox.ErrUnknownTopic, outbox.ErrNotDurable), http.StatusConflict, "conflict"},
		{"bad partitions", fmt.Errorf("%w: x declares 0", outbox.ErrBadPartitions), http.StatusConflict, "conflict"},
		{"partitions changed", fmt.Errorf("cannot purge: %w", outboxstore.ErrPartitionsChanged), http.StatusConflict, "conflict"},
	} {
		for _, kind := range []string{"retry", "discard", "purge"} {
			t.Run(c.name+"/"+kind, func(t *testing.T) {
				rg := newRig()
				var err error
				switch kind {
				case "retry":
					rg.op.retryErr = c.err
					_, err = rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
				case "discard":
					rg.op.discardErr = c.err
					_, err = rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
				case "purge":
					rg.op.purgeErr = c.err
					_, err = rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
				}
				wantProblem(t, err, c.status, c.code)
				if rec := rg.rec.recorded(); len(rec) != 0 {
					t.Errorf("a refused operation was recorded: %+v", rec)
				}
			})
		}
	}
	for _, kind := range []string{"retry", "discard", "purge"} {
		t.Run("store failure/"+kind, func(t *testing.T) {
			rg := newRig()
			var err error
			switch kind {
			case "retry":
				rg.op.retryErr = store
				_, err = rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
			case "discard":
				rg.op.discardErr = store
				_, err = rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1))
			case "purge":
				rg.op.purgeErr = store
				_, err = rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver"))
			}
			if !errors.Is(err, store) {
				t.Errorf("error = %v, want the store's, unchanged", err)
			}
			if he, ok := errors.AsType[*httpapi.Error](err); ok {
				t.Errorf("a store failure became the problem %d %s", he.Status, he.Code)
			}
			if rec := rg.rec.recorded(); len(rec) != 0 {
				t.Errorf("a failed operation was recorded: %+v", rec)
			}
		})
	}
}

// TestOutboxHTTP_ReadFailureStopsTheOperation: the read that names the topic runs
// before the operation (the dead letter list for a retry or discard, the status
// for a purge), and when it fails nothing is applied or recorded.
func TestOutboxHTTP_ReadFailureStopsTheOperation(t *testing.T) {
	boom := errors.New("pebble: the Batcave flooded")
	rg := newRig()
	rg.op.listErr = boom
	if _, err := rg.api.retry(context.Background(), deadIn(bruce(), "core.notify.deliver", 1)); !errors.Is(err, boom) {
		t.Errorf("retry = %v", err)
	}
	if _, err := rg.api.discard(context.Background(), deadIn(bruce(), "core.notify.deliver", 1)); !errors.Is(err, boom) {
		t.Errorf("discard = %v", err)
	}
	if got := rg.j.all(); !slices.Equal(got, []string{"op:list", "op:list"}) {
		t.Errorf("steps = %v, want only the two list reads", got)
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

// TestOutboxHTTP_PartialPurgeIsRecorded: a purge that fails after one partition
// advanced has discarded messages, so it records what it skipped as the error it
// ended in and still answers its own error; one that skipped nothing records
// nothing.
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
	if len(recs) != 1 {
		t.Fatalf("%d records, want 1", len(recs))
	}
	r := recs[0]
	if r.Action != "core.outbox.backlog_purge" || r.Outcome != audit.Error || r.Reason != "partial" ||
		r.Details["skipped"] != 3 || r.Details["topic"] != noticeTopic || r.Actor.Username != "bruce" {
		t.Errorf("record = %+v", r)
	}

	// The error is answered even when the partial record cannot be written.
	rg = newRig()
	rg.op.purgeSkipped, rg.op.purgeErr = 3, boom
	rg.rec.err = errors.New("audit: the disk is full")
	if _, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver")); !errors.Is(err, boom) {
		t.Errorf("error = %v, want the purge's, not the audit's", err)
	}

	rg = newRig()
	rg.op.purgeErr = boom
	if _, err := rg.api.purge(context.Background(), purgeIn(bruce(), "core.notify.deliver")); !errors.Is(err, boom) {
		t.Errorf("error = %v", err)
	}
	if recs := rg.rec.recorded(); len(recs) != 0 {
		t.Errorf("a purge that skipped nothing recorded %+v", recs)
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
// the real outbox, and changes nothing and records nothing. A consumer that is
// declared but never ran has no backlog, and the purge says so.
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
	if recs := e.records(); len(recs) != 0 {
		t.Errorf("refused requests were recorded: %+v", recs)
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
