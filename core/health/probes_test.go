// file: core/health/probes_test.go
// version: 1.0.0
// guid: 8f1b3d5a-2c74-4e96-a0b8-6d9e2f4c7a15
// last-edited: 2026-11-13

package health

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func probeMux(r *Registry) *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, h := range ProbeHandlers(r) {
		mux.Handle(pattern, h)
	}
	return mux
}

func get(mux http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestReadyz_CriticalDownIs503_LivezStays200: readiness and liveness are
// different questions. A Critical component that is Down fails /readyz and
// never /livez, which follows only the heartbeat.
func TestReadyz_CriticalDownIs503_LivezStays200(t *testing.T) {
	r := NewRegistry()
	storage := mustRegister(t, r, Check{ID: "core.storage", Critical: true, Readiness: true})
	mux := probeMux(r)

	storage.Report(Result{Status: StatusUp})
	if rec := get(mux, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz with storage Up = %d, want 200", rec.Code)
	}
	storage.Report(Result{Status: StatusDown, Reason: "check_timeout"})
	rec := get(mux, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with a Critical component Down = %d, want 503", rec.Code)
	}
	var body StatusPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ready || !slices.Equal(body.NotReady, []string{"core.storage:check_timeout"}) {
		t.Fatalf("/readyz body = %+v", body)
	}
	if rec := get(mux, "/livez"); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"live":true}` {
		t.Fatalf("/livez with a Critical component Down = %d %q, want 200 {\"live\":true}", rec.Code, rec.Body.String())
	}

	// A Critical component that is only Degraded, and a non-Critical,
	// non-Readiness component that is Down, do not fail readiness.
	storage.Report(Result{Status: StatusDegraded, Reason: "storage_checkpoint_mode"})
	other := mustRegister(t, r, Check{ID: "core.updater"})
	other.Report(Result{Status: StatusDown, Reason: "check_panic"})
	if rec := get(mux, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz with a Degraded Critical and a Down advisory component = %d, want 200", rec.Code)
	}

	// A wedged heartbeat fails /livez and still leaves readiness to the components.
	r.beating.Store(true)
	r.lastBeat.Store(time.Now().Add(-time.Hour).UnixNano())
	rec = get(mux, "/livez")
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"live":false}` {
		t.Fatalf("/livez with a stale heartbeat = %d %q, want 503 {\"live\":false}", rec.Code, rec.Body.String())
	}
	if rec := get(mux, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz with a stale heartbeat = %d, want 200: readiness does not follow the heartbeat", rec.Code)
	}
}

// TestProbes_VariantsAreNotSPA: the probe patterns are exact; a variant is
// never answered by a handler (here the mux's 404; the real router's NonSPA
// rule is asserted in internal/healthhttp).
func TestProbes_VariantsAreNotSPA(t *testing.T) {
	mux := probeMux(NewRegistry())
	for _, path := range []string{"/livez/", "/READYZ", "/Livez", "/readyz/x", "/health/", "/metrics/"} {
		rec := get(mux, path)
		if rec.Code == http.StatusOK || strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s = %d %q, want a non-200 non-HTML answer", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// POST is not a probe either.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/readyz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /readyz = %d, want 405", rec.Code)
	}
}

// TestProbes_BodyHasOnlyTokens: an unauthenticated probe body carries booleans
// and fixed <component>:<reason> tokens, never Detail text, durations, errors
// or a posture token (SEC-7, I1).
func TestProbes_BodyHasOnlyTokens(t *testing.T) {
	r := NewRegistry()
	const secretDetail = "password=hunter2 at /var/lib/gotham/db: connection refused to 192.0.2.7"
	mustRegister(t, r, Check{ID: "core.storage", Critical: true, Readiness: true}).
		Report(Result{Status: StatusDown, Reason: "storage_quarantine", Detail: secretDetail})
	mustRegister(t, r, Check{ID: "core.secrets", Critical: true, Readiness: true}).
		Report(Result{Status: StatusDown, Reason: "secrets_keyring_not_exported", Detail: secretDetail})
	mustRegister(t, r, Check{ID: "core.backup", Readiness: true}).
		Report(Result{Status: StatusDegraded, Reason: "restore_last_admin_kept", Detail: secretDetail}) // passes readiness: not listed
	mustRegister(t, r, Check{ID: "books.memdb", Readiness: true}).
		Report(Result{Status: StatusUp, Reason: "memdb_warming"})
	mustRegister(t, r, Check{ID: "books.search", Readiness: true}).
		Report(Result{Status: StatusDown, Reason: "warming", Detail: secretDetail})
	mustRegister(t, r, Check{ID: "core.config", Readiness: true}).
		Report(Result{Status: StatusDown, Reason: "Bad Reason With Spaces", Detail: secretDetail})
	mustRegister(t, r, Check{ID: "core.tasks", Readiness: true}) // Unknown, no reason

	rec := get(probeMux(r), "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.Bytes()
	for _, leak := range []string{"hunter2", "/var/lib", "192.0.2", "refused", "quarantine", "keyring", "restore_", "Detail", "duration"} {
		if bytes.Contains(body, []byte(leak)) {
			t.Errorf("body leaks %q: %s", leak, body)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields() // exactly {ready, not_ready}
	var p StatusPayload
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	want := []string{
		"core.config:invalid_reason",
		"core.secrets:down",
		"core.storage:down",
		"core.tasks:unknown",
		"books.search:warming",
	}
	slices.Sort(want)
	got := slices.Clone(p.NotReady)
	slices.Sort(got)
	if p.Ready || !slices.Equal(got, want) {
		t.Fatalf("not_ready = %v, want the golden set %v", got, want)
	}
	grammar := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)+:[a-z0-9_]{1,40}$`)
	for _, tok := range p.NotReady {
		if !grammar.MatchString(tok) {
			t.Errorf("token %q is not <component>:<reason>", tok)
		}
	}
}

// TestPublicTokens covers the filter directly: the posture families are
// generalised, malformed tokens dropped, ordinary reasons kept, and the result
// is never nil (it is a JSON array).
func TestPublicTokens(t *testing.T) {
	in := []string{
		"core.storage:storage_checkpoint_open", "core.secrets:secrets_undecryptable", "core.backup:revocation_journal_gaps",
		"core.backup:restore_inflight", "core.storage:factory_reset_failed", "core.config:config_fail_closed_override",
		"core.auth:auth_disabled_remote", "core.kernel:starting", "core.kernel:stopping", "books.memdb:warming",
		"no-dot:down", "core.x:UPPER", "core.x:", ":down", "core.updater:unknown",
	}
	want := []string{
		"core.storage:down", "core.secrets:down", "core.backup:down", "core.backup:down", "core.storage:down",
		"core.config:down", "core.auth:down", "core.kernel:starting", "core.kernel:stopping", "books.memdb:warming",
		"core.updater:unknown",
	}
	if got := PublicTokens(in); !slices.Equal(got, want) {
		t.Errorf("PublicTokens = %v\nwant           %v", got, want)
	}
	if got := PublicTokens(nil); got == nil || len(got) != 0 {
		t.Errorf("PublicTokens(nil) = %#v, want an empty non-nil slice", got)
	}
}

// TestReadyz_FlipsBeforeHTTPDrain: shutdown flips readiness to 503 and tells
// systemd STOPPING=1 before the HTTP drain phase starts, so load balancers see
// 503 before connections drop (05-health §5H.4).
func TestReadyz_FlipsBeforeHTTPDrain(t *testing.T) {
	r := NewRegistry()
	n := &fakeNotifier{active: true}
	sup := NewSupervisor(nil, n)
	su, err := NewStartup(r, nil, CoreStartupSteps()...)
	if err != nil {
		t.Fatal(err)
	}
	su.Complete()
	mux := probeMux(r)
	var published []StatusPayload
	r.SetStatusPublisher(func(p StatusPayload) { published = append(published, p) })
	if rec := get(mux, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz after startup = %d, want 200", rec.Code)
	}

	b := NewStopBudget(25*time.Second, StopLimits{}, nil)
	// The kernel's shutdown path, in this order.
	r.BeginShutdown()
	sup.Stopping()
	var duringDrain int
	var sentDuringDrain []string
	if err := b.Run(context.Background(), PhaseHTTPDrain, func(context.Context) error {
		duringDrain = get(mux, "/readyz").Code
		sentDuringDrain = n.sent()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if duringDrain != http.StatusServiceUnavailable {
		t.Errorf("/readyz during the HTTP drain = %d, want 503", duringDrain)
	}
	if !slices.Contains(sentDuringDrain, "STOPPING") {
		t.Errorf("STOPPING=1 not sent before the drain: %v", sentDuringDrain)
	}
	if rec := get(mux, "/readyz"); !strings.Contains(rec.Body.String(), "core.kernel:stopping") {
		t.Errorf("/readyz body at shutdown = %s, want the stopping token", rec.Body.String())
	}
	// The status feed: the initial ready state, then exactly one ready:false.
	if len(published) != 2 || !published[0].Ready || published[1].Ready || !slices.Equal(published[1].NotReady, []string{"core.kernel:stopping"}) {
		t.Errorf("status feed = %+v, want [ready, not-ready(stopping)]", published)
	}
	r.BeginShutdown() // idempotent
	if len(published) != 2 {
		t.Errorf("a second BeginShutdown published again: %+v", published)
	}
}

// TestStatusFeed_PublishesOnlyOnChange: the feed carries Ready() and the token
// set, in the order they changed, and nothing for a Report that changes
// neither.
func TestStatusFeed_PublishesOnlyOnChange(t *testing.T) {
	r := NewRegistry()
	rep := mustRegister(t, r, Check{ID: "books.memdb", Readiness: true})
	var got []StatusPayload
	r.SetStatusPublisher(func(p StatusPayload) { got = append(got, p) })
	rep.Report(Result{Status: StatusDown, Reason: "warming"})
	rep.Report(Result{Status: StatusDown, Reason: "warming", Detail: "still"}) // same tokens: no event
	rep.Report(Result{Status: StatusUp})
	rep.Report(Result{Status: StatusDegraded, Reason: "slow"}) // still ready, no tokens: no event
	want := []StatusPayload{
		{Ready: false, NotReady: []string{"books.memdb:unknown"}}, // the initial state: Unknown is not Up
		{Ready: false, NotReady: []string{"books.memdb:warming"}},
		{Ready: true, NotReady: []string{}},
	}
	if len(got) != len(want) {
		t.Fatalf("feed = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Ready != want[i].Ready || !slices.Equal(got[i].NotReady, want[i].NotReady) {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if b, _ := json.Marshal(got[2]); string(b) != `{"ready":true,"not_ready":[]}` {
		t.Errorf("payload JSON = %s: not_ready must be an array", b)
	}
}
