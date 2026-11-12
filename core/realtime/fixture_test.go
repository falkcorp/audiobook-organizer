// file: core/realtime/fixture_test.go
// version: 1.0.0
// guid: ea2c4934-b4a3-4e4c-ac47-4a7c90f8f521
// last-edited: 2026-11-11

package realtime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Fixtures are Batman-themed: the Batcave publishes signals and alarms, Arkham
// holds its own scope, and the cast are Batman characters.

// Signal is a Batcave signal's payload.
type Signal struct {
	Lamp string `json:"lamp"`
	Wing string `json:"wing"`
}

// Beacon is a coalesced topic's payload.
type Beacon struct {
	Light string `json:"light"`
	N     int    `json:"n"`
}

// Retired is a core topic's payload that every signed-in caller may see.
type Retired struct {
	Ward string `json:"ward"`
}

// Invalidated and RBACChanged are the control topics' payloads, as F4-08
// declares them (05-authn §4.12, 05-rbac §5).
type Invalidated struct {
	UserID       string `json:"user_id"`
	CredentialID string `json:"credential_id,omitempty"`
	Reason       string `json:"reason"`
}

type RBACChanged struct {
	Generation uint64 `json:"generation"`
}

const (
	scopeView  = rbac.Permission("batcave:view")
	scopeRun   = rbac.Permission("batcave:run")
	scopeRelay = rbac.Permission("batcave:relay") // Confining: a worker key's scope
	scopeArk   = rbac.Permission("arkham:view")
)

var (
	topicSignal  = events.NewTopic[Signal]("batcave.signal.lit.v1")      // batcave:view
	topicCell    = events.NewTopic[Signal]("batcave.cell.opened.v1")     // batcave:run
	topicAlarm   = events.NewTopic[Signal]("batcave.alarm.rang.v1")      // batcave:view, Visible: public wing or batcave:run
	topicBeacon  = events.NewTopic[Beacon]("batcave.beacon.updated.v1")  // batcave:view, Coalesce 250ms
	topicArk     = events.NewTopic[Signal]("arkham.ward.alerted.v1")     // arkham:view
	topicRetired = events.NewTopic[Retired]("core.ward.retired.v1")      // AnyAuthenticated
	topicRun     = events.NewTopic[tasks.RunView]("core.task.run-changed.v1")
	topicLog     = events.NewTopic[Retired]("core.task.log-appended.v1") // AnyAuthenticated, keyed by run
	topicInval   = events.NewTopic[Invalidated]("core.principal.invalidated.v1")
	topicRBAC    = events.NewTopic[RBACChanged]("core.rbac.changed.v1")
)

// wingVisible is the alarm topic's per-envelope filter: a public-wing alarm is
// for every viewer, any other needs batcave:run (the shape of a task
// definition's View).
func wingVisible(_ context.Context, who rbac.Checker, e events.RawEnvelope) bool {
	var s Signal
	if json.Unmarshal(e.Payload, &s) != nil {
		return false
	}
	return s.Wing == "public" || who.Has(scopeRun)
}

type sink struct{}

func (sink) Record(context.Context, audit.Event) error { return errors.New("audited with RecordIn") }
func (sink) RecordIn(_ context.Context, b *storage.Batch, _ audit.Event) error {
	if b == nil {
		return errors.New("RecordIn without a batch")
	}
	return nil
}
func (sink) Emit(context.Context, audit.Event) {}

type revJournal struct{}

func (revJournal) Append(_ context.Context, b *storage.Batch, _ journal.Revocation) error {
	if b == nil {
		return errors.New("Append without a batch")
	}
	return nil
}
func (revJournal) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without a batch")
	}
	return nil
}

type noHolders struct{}

func (noHolders) HoldersOf(context.Context, *storage.Batch, []rbac.RoleName) ([]rbac.Holder, error) {
	return nil, nil
}

// env is one booted auth.Service over an in-memory store, plus a bus and a
// hub wired the way the kernel will wire them (F6-03): declare, subscribe the
// hub's subscriptions, freeze, open.
type env struct {
	t   testing.TB
	st  *authstore.Store
	rb  *rbac.Service
	svc *auth.Service
	now time.Time

	bus *events.Bus
	hub *realtime.Hub
	pub events.Scope
	cor events.Scope
}

// newAuth boots auth and rbac with the Batcave scopes; it opens no bus.
func newAuth(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
			{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		},
		Build:           "realtime+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &env{t: t, now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	aks, _ := db.Keyspace(authstore.Owner)
	rks, _ := db.Keyspace(rbacstore.Owner)
	if e.st, err = authstore.New(aks, authstore.Options{Now: func() time.Time { return e.now }}); err != nil {
		t.Fatal(err)
	}
	rst, err := rbacstore.New(rks, rbacstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if e.rb, err = rbac.New(ctx, rbac.Options{Source: "realtime-test"}, rbac.Deps{
		Store: rst, Compile: authz.Compile, Audit: sink{}, Revocations: revJournal{},
		NewBatch: rst.NewBatch, Holders: noHolders{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct {
		ns    string
		perms []rbac.PermissionSpec
		roles []rbac.RoleSeed
	}{
		{"batcave", []rbac.PermissionSpec{
			{Name: scopeRun, Title: "Run the Batcave"},
			{Name: scopeView, Title: "See the Batcave", Class: rbac.ClassView},
			{Name: scopeRelay, Title: "Relay worker", Confining: true},
		}, []rbac.RoleSeed{
			{Name: "batcave.keeper", Title: "Keeper", Permissions: []rbac.Permission{scopeRun, scopeView}},
			{Name: "batcave.watcher", Title: "Watcher", Permissions: []rbac.Permission{scopeView}},
			{Name: "batcave.relay", Title: "Relay", Permissions: []rbac.Permission{scopeRelay}},
		}},
		{"arkham", []rbac.PermissionSpec{{Name: scopeArk, Title: "See Arkham", Class: rbac.ClassView}},
			[]rbac.RoleSeed{{Name: "arkham.warden", Title: "Warden", Permissions: []rbac.Permission{scopeArk}}}},
	} {
		dc := e.rb.Declarer(d.ns)
		if err := dc.Permissions(d.perms...); err != nil {
			t.Fatal(err)
		}
		if err := dc.Roles(d.roles...); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.rb.Freeze(ctx); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if err := e.rb.CommitSeeds(ctx); err != nil {
		t.Fatalf("CommitSeeds: %v", err)
	}
	if e.svc, err = auth.New(ctx, auth.Options{}, auth.Deps{
		Store: e.st, NewBatch: e.st.NewBatch, RBAC: e.rb, Audit: sink{}, Revocations: revJournal{},
		Clock:  func() time.Time { return e.now },
		Hasher: password.New(password.Options{Time: 1, MemoryKiB: 64}),
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

// user stores an active user holding roles.
func (e *env) user(name string, roles ...rbac.RoleName) *auth.User {
	e.t.Helper()
	u := &auth.User{
		ID: auth.UserID("u-" + name), Username: name, Roles: roles, Status: auth.StatusActive,
		CreatedAt: e.now, UpdatedAt: e.now, Version: 1,
	}
	b := e.st.NewBatch()
	defer func() { _ = b.Close() }()
	if err := e.st.PutUser(b, u); err != nil {
		e.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		e.t.Fatal(err)
	}
	got, err := e.st.GetUser(context.Background(), u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// session is u's interactive principal over a real session record, so
// Revalidate sees it and RevokeSession ends it.
func (e *env) session(u *auth.User) *auth.Principal {
	e.t.Helper()
	is, err := e.svc.IssueSession(context.Background(), u, auth.OriginPassword, false, auth.ClientInfo{})
	if err != nil {
		e.t.Fatalf("IssueSession %s: %v", u.ID, err)
	}
	p, err := e.svc.PrincipalFor(context.Background(), u, auth.MethodSession, nil, u.CredentialEpoch)
	if err != nil {
		e.t.Fatalf("PrincipalFor %s: %v", u.ID, err)
	}
	p.CredentialID = is.PublicID
	return p
}

// key mints an API key for u naming scopes and returns u's principal for it.
func (e *env) key(u *auth.User, scopes ...rbac.Permission) (*auth.Principal, *auth.APIKey) {
	e.t.Helper()
	_, k, err := e.svc.CreateAPIKey(context.Background(), e.session(u), auth.KeySpec{Name: "batphone", Scopes: scopes})
	if err != nil {
		e.t.Fatalf("CreateAPIKey %s %v: %v", u.ID, scopes, err)
	}
	p, err := e.svc.PrincipalFor(context.Background(), u, auth.MethodAPIKey, k.Scopes, u.CredentialEpoch)
	if err != nil {
		e.t.Fatalf("PrincipalFor key %s: %v", u.ID, err)
	}
	p.CredentialID = k.ID
	return p, k
}

// topicSpecs is the catalog the hub streams: the Batcave's and Arkham's
// topics, three core topics, and the two control topics (which are not
// Realtime and so never streamed).
func topicSpecs() (core, batcave, arkham []events.TopicSpec) {
	rt := &events.RealtimeMeta{}
	batcave = []events.TopicSpec{
		topicSignal.Spec(events.TopicSpec{Permission: rbac.Need(scopeView), Realtime: rt}),
		topicCell.Spec(events.TopicSpec{Permission: rbac.Need(scopeRun), Realtime: rt}),
		topicAlarm.Spec(events.TopicSpec{Permission: rbac.Need(scopeView), Realtime: rt, Visible: wingVisible}),
		topicBeacon.Spec(events.TopicSpec{Permission: rbac.Need(scopeView), Realtime: &events.RealtimeMeta{Coalesce: 250 * time.Millisecond}}),
	}
	arkham = []events.TopicSpec{topicArk.Spec(events.TopicSpec{Permission: rbac.Need(scopeArk), Realtime: rt})}
	core = []events.TopicSpec{
		topicRetired.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated, Realtime: rt}),
		topicRun.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated, Realtime: rt, Redact: tasks.RunViewRedactor()}),
		topicLog.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated, Realtime: rt}),
		topicInval.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated}),
		topicRBAC.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated}),
	}
	return core, batcave, arkham
}

// hubOptions are tests' hub settings: small bounds so overflow, caps and the
// ring are easy to reach.
type hubOptions struct {
	opts    realtime.Options
	deps    func(*realtime.Deps)
	specs   func(core, batcave, arkham *[]events.TopicSpec)
	skipCtl bool // leave out the two control topics
}

// newEnv boots auth, a bus and a hub wired as the kernel will wire them.
func newEnv(t testing.TB, ho hubOptions) *env {
	t.Helper()
	e := newAuth(t)
	e.wire(ho)
	return e
}

// wire builds the bus and the hub over e's auth service.
func (e *env) wire(ho hubOptions) {
	e.t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	core, batcave, arkham := topicSpecs()
	if ho.skipCtl {
		core = core[:3]
	}
	if ho.specs != nil {
		ho.specs(&core, &batcave, &arkham)
	}
	for _, d := range []struct {
		owner string
		specs []events.TopicSpec
	}{{"core", core}, {"batcave", batcave}, {"arkham", arkham}} {
		if err := bus.Declare(d.owner, d.specs...); err != nil {
			e.t.Fatalf("Declare %s: %v", d.owner, err)
		}
	}
	deps := realtime.Deps{Auth: e.svc, Catalog: bus.Catalog()}
	if ho.deps != nil {
		ho.deps(&deps)
	}
	hub, err := realtime.NewHub(ho.opts, deps)
	if err != nil {
		e.t.Fatal(err)
	}
	subs, err := hub.Subscriptions()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := bus.Subscribe("core", subs...); err != nil {
		e.t.Fatalf("Subscribe: %v", err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		e.t.Fatal(err)
	}
	e.bus, e.hub, e.pub, e.cor = bus, hub, bus.Scope("batcave"), bus.Scope("core")
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Close(ctx)
		_ = bus.Close(ctx)
	})
}

// signal publishes a Batcave signal.
func (e *env) signal(topic events.Topic[Signal], key, lamp, wing string) {
	events.Publish(e.pub, context.Background(), topic, key, Signal{Lamp: lamp, Wing: wing})
}

// ---- transport ---------------------------------------------------------------

// serve mounts the hub's handler behind a middleware that binds the principal
// named by the X-Test-Principal header, the way auth's middleware binds one.
type roster struct {
	mu sync.Mutex
	m  map[string]*auth.Principal
}

func (r *roster) set(name string, p *auth.Principal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*auth.Principal{}
	}
	r.m[name] = p
}

func (r *roster) get(name string) *auth.Principal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[name]
}

func (e *env) serve(t testing.TB, h http.Handler) (*httptest.Server, *roster) {
	t.Helper()
	ro := &roster{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := ro.get(r.Header.Get("X-Test-Principal")); p != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		h.ServeHTTP(w, r)
	}))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, ro
}

// frame is one parsed SSE frame.
type frame struct {
	ID      string
	Data    string
	Comment string
}

// env decodes a data frame.
func (f frame) env(t testing.TB) realtime.Envelope {
	t.Helper()
	var en realtime.Envelope
	if err := json.Unmarshal([]byte(f.Data), &en); err != nil {
		t.Fatalf("frame data %q: %v", f.Data, err)
	}
	return en
}

// reader reads SSE frames from a body on one goroutine and offers them on a
// channel, so a test can wait with a deadline and can also assert silence.
type reader struct {
	t      testing.TB
	resp   *http.Response
	frames chan frame
	lines  chan string // every raw line, for the "no event: line" assertion
	cancel context.CancelFunc
	done   chan struct{}
}

// open GETs path as principal name and returns the response; the body is read
// by the returned reader only when the status is 200.
func open(t testing.TB, srv *httptest.Server, name, path string, hdr ...string) (*reader, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Principal", name)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", path, err)
	}
	rd := &reader{t: t, resp: resp, cancel: cancel, frames: make(chan frame, 256), lines: make(chan string, 1024), done: make(chan struct{})}
	t.Cleanup(rd.close)
	if resp.StatusCode != http.StatusOK {
		cancel()
		close(rd.done)
		return rd, resp
	}
	go rd.pump()
	return rd, resp
}

func (r *reader) close() {
	r.cancel()
	_ = r.resp.Body.Close()
	<-r.done
}

func (r *reader) pump() {
	defer close(r.done)
	defer close(r.frames)
	sc := bufio.NewScanner(r.resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var f frame
	has := false
	for sc.Scan() {
		line := sc.Text()
		select {
		case r.lines <- line:
		default:
		}
		switch {
		case line == "":
			if has {
				r.frames <- f
			}
			f, has = frame{}, false
		case strings.HasPrefix(line, "id: "):
			f.ID, has = strings.TrimPrefix(line, "id: "), true
		case strings.HasPrefix(line, "data: "):
			f.Data, has = strings.TrimPrefix(line, "data: "), true
		case strings.HasPrefix(line, ":"):
			f.Comment, has = line, true
		default: // retry: and anything else
			has = true
		}
	}
}

// next waits for the next frame.
func (r *reader) next() frame {
	r.t.Helper()
	select {
	case f, ok := <-r.frames:
		if !ok {
			r.t.Fatal("the stream ended while waiting for a frame")
		}
		return f
	case <-time.After(5 * time.Second):
		r.t.Fatal("timed out waiting for a frame")
	}
	return frame{}
}

// nextData skips comments and returns the next data frame's envelope and id.
func (r *reader) nextData() (realtime.Envelope, string) {
	r.t.Helper()
	for {
		f := r.next()
		if f.Data != "" {
			return f.env(r.t), f.ID
		}
	}
}

// expectOpened reads the opened control frame and returns its topics.
func (r *reader) expectOpened() []string {
	r.t.Helper()
	en, id := r.nextData()
	if en.Topic != "core.stream.opened.v1" || id != "" {
		r.t.Fatalf("first frame = %+v id %q, want core.stream.opened.v1 with no id", en, id)
	}
	var d struct {
		Topics []string `json:"topics"`
	}
	if err := json.Unmarshal(en.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	return d.Topics
}

// quiet asserts that nothing but comments arrives within d.
func (r *reader) quiet(d time.Duration) {
	r.t.Helper()
	select {
	case f, ok := <-r.frames:
		if ok && f.Data != "" {
			r.t.Fatalf("unexpected frame %+v", f)
		}
	case <-time.After(d):
	}
}

// ended reports whether the stream's body ended within the time.
func (r *reader) ended() bool {
	select {
	case _, ok := <-r.frames:
		return !ok
	case <-time.After(5 * time.Second):
		return false
	}
}

// drainUntilClosed reads frames until the stream ends and returns the closed
// frame's reason ("" when no closed frame came).
func (r *reader) drainUntilClosed() string {
	r.t.Helper()
	reason := ""
	for {
		select {
		case f, ok := <-r.frames:
			if !ok {
				return reason
			}
			if f.Data == "" {
				continue
			}
			en := f.env(r.t)
			if en.Topic == "core.stream.closed.v1" {
				var d struct {
					Reason string `json:"reason"`
				}
				_ = json.Unmarshal(en.Data, &d)
				reason = d.Reason
			}
		case <-time.After(5 * time.Second):
			r.t.Fatal("the stream did not end")
		}
	}
}

// problem decodes a refusal's body.
type problem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
}

func readProblem(t testing.TB, resp *http.Response) problem {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var p problem
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("problem body %q: %v", b, err)
	}
	return p
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// logs is a goroutine-safe log sink.
type logs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *logs) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ---- the cast -----------------------------------------------------------------

// cast is the people of the fixture, each with the principal their sign-in
// builds, registered on the roster under their first name.
type cast struct {
	*env
	srv                                           *httptest.Server
	ro                                            *roster
	bruce, lucius, dick, barbara, oswald, robin *auth.User
}

// newCast boots the full environment and serves the hub's handler.
func newCast(t testing.TB, ho hubOptions) *cast {
	t.Helper()
	e := newEnv(t, ho)
	c := &cast{env: e}
	c.srv, c.ro = e.serve(t, e.hub.Handler())
	c.bruce = e.user("bruce", rbac.RoleAdmin)
	c.lucius = e.user("lucius", "batcave.keeper")
	c.dick = e.user("dick", "batcave.watcher")
	c.barbara = e.user("barbara", "arkham.warden", "batcave.watcher")
	c.oswald = e.user("oswald")
	c.robin = e.user("robin", "batcave.relay")
	for name, u := range map[string]*auth.User{"bruce": c.bruce, "lucius": c.lucius, "dick": c.dick, "barbara": c.barbara, "oswald": c.oswald, "robin": c.robin} {
		c.ro.set(name, e.session(u))
	}
	return c
}

// open opens a stream as who and fails the test unless it is a 200.
func (c *cast) open(t testing.TB, who, path string, hdr ...string) *reader {
	t.Helper()
	rd, resp := open(t, c.srv, who, path, hdr...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open %s as %s: status %d %+v", path, who, resp.StatusCode, readProblem(t, resp))
	}
	return rd
}

// hubHeartbeat is options with only the heartbeat set.
func hubHeartbeat(d time.Duration) realtime.Options { return realtime.Options{Heartbeat: d} }
