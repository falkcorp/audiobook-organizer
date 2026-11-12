// file: core/internal/taskhttp/fakes_test.go
// version: 1.0.1
// guid: ba1ee649-0c1e-4172-b3e6-7a16c5561b5e
// last-edited: 2026-11-11

package taskhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Fixtures are Batman-themed: kinds, users and run text name the Batcave.
const (
	kindSweep    = tasks.Kind("batcave.sweep")   // writer; Start batcave:run; schedulable
	kindScan     = tasks.Kind("batcave.scan")    // read-only; View batcave:view
	kindRelocate = tasks.Kind("arkham.relocate") // Approve arkham:approve
	kindNightly  = tasks.Kind("core.window.nightly")

	scopeRun     = rbac.Permission("batcave:run")
	scopeSee     = rbac.Permission("batcave:view")
	scopeApprove = rbac.Permission("arkham:approve")
	scopeUsers   = rbac.Permission("core:users.view")

	sweepKey = "core.batcave.schedules.sweep"
)

// grants is a fake checker: exactly the scopes it names. Any scope at all
// satisfies AnyAuthenticated, as a principal holding a non-confining scope
// does.
type grants map[rbac.Permission]bool

func (g grants) Has(p rbac.Permission) bool { return g[p] }

func (g grants) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindAuthenticated:
		return len(g) > 0
	case rbac.KindAllOf:
		for _, p := range ps {
			if !g[p] {
				return false
			}
		}
		return len(ps) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, g.Has)
	}
	return false
}

func (grants) Confined() bool { return false }

func (g grants) Concrete() []rbac.Permission {
	var out []rbac.Permission
	for p := range g {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// who builds a principal: a signed-in user holding perms, over method.
func who(user string, method auth.Method, perms ...rbac.Permission) *auth.Principal {
	g := grants{}
	for _, p := range perms {
		g[p] = true
	}
	return &auth.Principal{User: &auth.User{ID: auth.UserID(user), Username: user}, Method: method, CredentialID: "cred-" + user, Perms: g}
}

// session is a user signed in interactively.
func session(user string, perms ...rbac.Permission) *auth.Principal {
	return who(user, auth.MethodSession, perms...)
}

// asPrincipal is the fake principal resolver: the middleware core's auth
// stages will be, binding p into the request context.
func asPrincipal(p *auth.Principal, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// fakeClient is a tasks.Client over an in-memory run table that models the
// executor's idempotency: one run per kind and key.
type fakeClient struct {
	mu     sync.Mutex
	runs   map[tasks.RunID]tasks.RunView
	hidden map[tasks.RunID]bool // runs Get reports as not found (View refused)
	idem   map[string]tasks.RunID
	seq    int
	errs   map[string]error // method name -> error to return

	starts   []startCall
	inserted int
	cancels  []tasks.RunID
	decides  []decideCall
	discards []tasks.RunID
	pauses   []string
	unpauses int
	pause    *tasks.Pause

	timelineQ tasks.TimelineQuery
	runsQ     tasks.RunQuery
	logQ      tasks.PageQuery
	logLines  []tasks.LogLine
	timeline  []tasks.RunView
}

type startCall struct {
	Kind tasks.Kind
	Req  tasks.StartRequest
}

type decideCall struct {
	ID tasks.RunID
	D  tasks.Decision
}

func newFakeClient() *fakeClient {
	return &fakeClient{runs: map[tasks.RunID]tasks.RunView{}, hidden: map[tasks.RunID]bool{}, idem: map[string]tasks.RunID{}, errs: map[string]error{}}
}

// add stores a run and returns its ID.
func (f *fakeClient) add(v tasks.RunView) tasks.RunID {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[v.ID] = v
	return v.ID
}

func (f *fakeClient) failWith(method string, err error) {
	f.mu.Lock()
	f.errs[method] = err
	f.mu.Unlock()
}

func (f *fakeClient) err(method string) error { return f.errs[method] }

func (f *fakeClient) Start(_ context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, startCall{kind, req})
	if err := f.err("Start"); err != nil {
		return tasks.RunRef{}, err
	}
	key := string(kind) + "/" + req.IdempotencyKey
	if req.IdempotencyKey != "" {
		if id, ok := f.idem[key]; ok {
			return tasks.RunRef{ID: id, Existing: true}, nil
		}
	}
	f.seq++
	f.inserted++
	id := tasks.RunID(fmt.Sprintf("run-%03d", f.seq))
	f.runs[id] = tasks.RunView{ID: id, Kind: kind, Mode: tasks.ModePreview, State: tasks.StateQueued, Actor: tasks.Actor{UserID: "bruce", AuthMethod: "session"},
		Trigger: req.Trigger, Created: time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC), Current: "Selina Kyle's file"}
	if req.IdempotencyKey != "" {
		f.idem[key] = id
	}
	return tasks.RunRef{ID: id}, nil
}

func (f *fakeClient) Get(_ context.Context, id tasks.RunID) (tasks.RunView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("Get"); err != nil {
		return tasks.RunView{}, err
	}
	v, ok := f.runs[id]
	if !ok || f.hidden[id] {
		return tasks.RunView{}, fmt.Errorf("%w: %s", tasks.ErrRunNotFound, id)
	}
	return v, nil
}

func (f *fakeClient) Runs(_ context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunView, error] {
	f.mu.Lock()
	f.runsQ = q
	var out []tasks.RunView
	for _, v := range f.runs {
		if !f.hidden[v.ID] {
			out = append(out, v)
		}
	}
	err := f.err("Runs")
	f.mu.Unlock()
	slices.SortFunc(out, func(a, b tasks.RunView) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return func(yield func(tasks.RunView, error) bool) {
		if err != nil {
			yield(tasks.RunView{}, err)
			return
		}
		for _, v := range out {
			if !yield(v, nil) {
				return
			}
		}
	}
}

func (f *fakeClient) Cancel(_ context.Context, id tasks.RunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
	return f.err("Cancel")
}

func (f *fakeClient) Decide(_ context.Context, id tasks.RunID, d tasks.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decides = append(f.decides, decideCall{id, d})
	return f.err("Decide")
}

func (f *fakeClient) Pause(_ context.Context, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses = append(f.pauses, reason)
	if err := f.err("Pause"); err != nil {
		return err
	}
	f.pause = &tasks.Pause{Reason: reason, By: tasks.Actor{UserID: "bruce", AuthMethod: "session"}, At: time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC)}
	return nil
}

func (f *fakeClient) Unpause(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpauses++
	if err := f.err("Unpause"); err != nil {
		return err
	}
	f.pause = nil
	return nil
}

func (f *fakeClient) PauseState(context.Context) (tasks.Pause, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("PauseState"); err != nil {
		return tasks.Pause{}, false, err
	}
	if f.pause == nil {
		return tasks.Pause{}, false, nil
	}
	return *f.pause, true, nil
}

func (f *fakeClient) Timeline(_ context.Context, q tasks.TimelineQuery) ([]tasks.RunView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timelineQ = q
	return slices.Clone(f.timeline), f.err("Timeline")
}

func (f *fakeClient) Log(_ context.Context, id tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.LogLine, error] {
	f.mu.Lock()
	f.logQ = q
	_, ok := f.runs[id]
	hidden := f.hidden[id]
	lines := slices.Clone(f.logLines)
	err := f.err("Log")
	f.mu.Unlock()
	return func(yield func(tasks.LogLine, error) bool) {
		if !ok || hidden {
			yield(tasks.LogLine{}, fmt.Errorf("%w: %s", tasks.ErrRunNotFound, id))
			return
		}
		if err != nil {
			yield(tasks.LogLine{}, err)
			return
		}
		for _, l := range lines {
			if !yield(l, nil) {
				return
			}
		}
	}
}

func (f *fakeClient) Discard(_ context.Context, id tasks.RunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discards = append(f.discards, id)
	if err := f.err("Discard"); err != nil {
		return err
	}
	if _, ok := f.runs[id]; !ok {
		return fmt.Errorf("%w: %s", tasks.ErrRunNotFound, id)
	}
	delete(f.runs, id)
	return nil
}

// fakeApprover records Approve calls.
type fakeApprover struct {
	mu    sync.Mutex
	calls []approveCall
	err   error
}

type approveCall struct {
	ID  tasks.RunID
	Req tasks.ApproveRequest
}

func (a *fakeApprover) Approve(_ context.Context, id tasks.RunID, r tasks.ApproveRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, approveCall{id, r})
	return a.err
}

// fakeRuns is the RunReader: counts, plan rows and item outcomes.
type fakeRuns struct {
	counts map[tasks.RunState]int64
	rows   []tasks.Row
	items  []tasks.PageEntry
	pageQ  tasks.PageQuery
	err    error
}

func (f *fakeRuns) Counts(context.Context) (map[tasks.RunState]int64, error) { return f.counts, f.err }

func (f *fakeRuns) Rows(context.Context, tasks.RunID) iter.Seq2[tasks.Row, error] {
	return func(yield func(tasks.Row, error) bool) {
		for _, r := range f.rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func (f *fakeRuns) Pages(_ context.Context, _ tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.PageEntry, error] {
	f.pageQ = q
	return func(yield func(tasks.PageEntry, error) bool) {
		n := 0
		for _, e := range f.items {
			if q.After != "" && e.Key <= q.After {
				continue
			}
			if q.Outcome != "" && !strings.HasPrefix(e.Item.Outcome, q.Outcome) {
				continue
			}
			if n == q.Limit {
				return
			}
			n++
			if !yield(e, nil) {
				return
			}
		}
	}
}

// fakeConfig is the ConfigPort over a map of keys.
type fakeConfig struct {
	mu       sync.Mutex
	keys     map[config.Path]config.KeyState
	applied  []config.Patch
	actors   []config.Actor
	applyErr error
}

func (f *fakeConfig) Lookup(p config.Path) (config.KeyState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[p]
	return k, ok
}

func (f *fakeConfig) Apply(_ context.Context, p config.Patch, a config.Actor) (config.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, p)
	f.actors = append(f.actors, a)
	if f.applyErr != nil {
		return config.Result{}, f.applyErr
	}
	for path, v := range p.Set {
		ks := f.keys[path]
		ks.Effective = v
		f.keys[path] = ks
	}
	return config.Result{Generation: 1, Applied: slices.Collect(func(yield func(config.Path) bool) {
		for path := range p.Set {
			if !yield(path) {
				return
			}
		}
	})}, nil
}

// fakeAudit records what the routes record and emit.
type fakeAudit struct {
	mu        sync.Mutex
	recorded  []audit.Event
	emitted   []audit.Event
	recordErr error
}

func (a *fakeAudit) Record(_ context.Context, e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.recordErr != nil {
		return a.recordErr
	}
	a.recorded = append(a.recorded, e)
	return nil
}

func (a *fakeAudit) RecordIn(context.Context, *storage.Batch, audit.Event) error {
	return fmt.Errorf("fakeAudit: RecordIn is not used by taskhttp")
}

func (a *fakeAudit) Emit(_ context.Context, e audit.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitted = append(a.emitted, e)
}

// rig is one Server over the fakes.
type rig struct {
	t      *testing.T
	srv    *Server
	client *fakeClient
	appr   *fakeApprover
	runs   *fakeRuns
	cfg    *fakeConfig
	aud    *fakeAudit
	h      http.Handler
}

// defs are the fixture definitions.
func defs() []tasks.Definition {
	noop := func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil }
	return []tasks.Definition{
		tasks.Task(kindSweep, tasks.TaskSpec[struct{}, struct{}]{
			Title: "Sweep the Batcave", Help: "Dusts every trophy case.", Effects: tasks.Writes(tasks.Res("batcave", "cases")),
			Perms: tasks.Permissions{Start: rbac.Need(scopeRun)}, Resume: tasks.ResumeContinue,
			Schedule: tasks.ScheduleFromConfig(sweepKey).Live(), Run: noop}),
		tasks.Task(kindScan, tasks.TaskSpec[struct{}, struct{}]{
			Title: "Scan Gotham", Effects: tasks.ReadOnly(tasks.Res("batcave", "signal")),
			Perms:  tasks.Permissions{Start: rbac.Need(scopeRun), View: rbac.Need(scopeSee)},
			Resume: tasks.ResumeContinue, Run: noop}),
		tasks.Task(kindRelocate, tasks.TaskSpec[struct{}, struct{}]{
			Title: "Relocate inmates", Effects: tasks.Writes(tasks.Res("arkham", "cells")),
			Perms:    tasks.Permissions{Start: rbac.Need(scopeRun), Approve: rbac.Need(scopeApprove)},
			Approval: &tasks.Approval{PlanRequired: true}, Resume: tasks.ResumeAsk, Run: noop}),
	}
}

func newRig(t *testing.T, edit ...func(*Deps)) *rig {
	t.Helper()
	g := &rig{t: t, client: newFakeClient(), appr: &fakeApprover{}, runs: &fakeRuns{}, aud: &fakeAudit{}}
	g.cfg = &fakeConfig{keys: map[config.Path]config.KeyState{
		sweepKey: {Path: sweepKey, Effective: json.RawMessage(`{"enabled":true,"every":"6h"}`)},
	}}
	d := Deps{Client: g.client, Approver: g.appr, Runs: g.runs, Definitions: defs(), Config: g.cfg, Audit: g.aud}
	for _, e := range edit {
		e(&d)
	}
	srv, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	g.srv, g.h = srv, srv.Handler()
	return g
}

// req is one request: the route's path is relative to BasePath.
type req struct {
	method, path string
	body         string
	headers      map[string]string
	as           *auth.Principal
}

// do sends r through the route table with the fake principal resolver.
func (g *rig) do(r req) *httptest.ResponseRecorder {
	g.t.Helper()
	var body *bytes.Reader
	if r.body != "" {
		body = bytes.NewReader([]byte(r.body))
	} else {
		body = bytes.NewReader(nil)
	}
	hr := httptest.NewRequest(r.method, BasePath+r.path, body)
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	asPrincipal(r.as, g.h).ServeHTTP(rec, hr)
	return rec
}

// decode reads a JSON response body into v.
func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("response %d is not JSON: %v\n%s", rec.Code, err, rec.Body.String())
	}
}

// code is a problem response's machine code.
func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	decode(t, rec, &p)
	return p.Code
}

// wantProblem fails unless rec is a problem+json with status and code.
func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, wantCode string) {
	t.Helper()
	if rec.Code != status || code(t, rec) != wantCode {
		t.Fatalf("response = %d %s, want %d %s\n%s", rec.Code, code(t, rec), status, wantCode, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
}

// view is a run view over the Batcave.
func view(id string, kind tasks.Kind, state tasks.RunState, actor string) tasks.RunView {
	return tasks.RunView{ID: tasks.RunID(id), Kind: kind, Mode: tasks.ModeLive, State: state, Actor: tasks.Actor{UserID: actor, AuthMethod: "session"},
		Trigger: tasks.TriggerManual, Created: time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC), Current: "Harvey Dent's case file"}
}

// doMulti is do with a header sent several times.
func (g *rig) doMulti(r req, name string, values ...string) *httptest.ResponseRecorder {
	g.t.Helper()
	hr := httptest.NewRequest(r.method, BasePath+r.path, strings.NewReader(r.body))
	for _, v := range values {
		hr.Header.Add(name, v)
	}
	rec := httptest.NewRecorder()
	asPrincipal(r.as, g.h).ServeHTTP(rec, hr)
	return rec
}

// decodeAsync is decode for goroutines other than the test's: it reports with
// Errorf, since FailNow must run on the test goroutine.
func decodeAsync(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Errorf("response %d is not JSON: %v\n%s", rec.Code, err, rec.Body.String())
	}
}
