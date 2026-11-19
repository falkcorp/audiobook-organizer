// file: core/internal/logjournal/tail_test.go
// version: 1.0.1
// guid: 0d9e5b73-2c64-4a18-b7f1-93a60e4d2c85
// last-edited: 2026-11-19

package logjournal_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// The live tail is tested through the real parts it will run on: a bus, the
// hub from F6-02 and the real compiled checkers of authz over core's own
// scope catalog, so core:system.manage really is AuthRequired and
// core:users.view really is not. No kernel exists yet (F6-03), so the test
// plays the kernel: declare, subscribe the hub, freeze, open. Fixtures are
// Gotham's: the Batcave logs, the cast are Batman characters.

const (
	permManage = logjournal.PermSystemManage
	permUsers  = logjournal.PermUsersView
	permView   = rbac.Permission("core:system.view")
)

// checkerOf is the compiled checker of a session principal holding exactly perms.
func checkerOf(t testing.TB, perms ...rbac.Permission) rbac.Checker {
	t.Helper()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	return authz.Compile(rbac.CompileInput{
		Interactive: true, RolePatterns: perms,
	}, cat)
}

// person is a signed-in principal holding exactly perms.
func person(t testing.TB, id string, perms ...rbac.Permission) *auth.Principal {
	t.Helper()
	return &auth.Principal{
		User: &auth.User{ID: auth.UserID(id), Username: strings.TrimPrefix(id, "u-")}, Method: auth.MethodSession,
		Perms: checkerOf(t, perms...), ClientAddr: netip.MustParseAddr("192.0.2.10"), CredentialID: "sess-" + id,
	}
}

// authOff is the principal of an install with authentication disabled.
func authOff(t testing.TB) *auth.Principal {
	t.Helper()
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	return &auth.Principal{Method: auth.MethodAuthDisabled, Perms: authz.AuthDisabled(cat), ClientAddr: netip.MustParseAddr("192.0.2.10")}
}

type okAuth struct{}

func (okAuth) Revalidate(context.Context, *auth.Principal) error { return nil }

// namesSelina is a books-style predicate (06 LogOtherUserData): a record naming
// selina is her data unless she is the viewer.
func namesSelina(rec log.Record, viewer string) bool {
	return strings.Contains(strings.ToLower(rec.Message), "selina") && viewer != "u-selina"
}

// rig is the tail wired the way the kernel will wire it at F6-03.
type rig struct {
	t    *testing.T
	l    *log.Logging
	bus  *events.Bus
	tail *logjournal.Tail
	srv  *httptest.Server

	mu     sync.Mutex
	roster map[string]*auth.Principal
}

func newRig(t *testing.T, pred log.OtherUserPredicate) *rig {
	t.Helper()
	r := &rig{t: t, l: newLogging(t), roster: map[string]*auth.Principal{}}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", logjournal.TailSpec(pred)); err != nil {
		t.Fatal(err)
	}
	// AllowNoControlTopics: the control topics are F4-08's, not under test.
	hub, err := realtime.NewHub(realtime.Options{Heartbeat: time.Minute, AllowNoControlTopics: true}, realtime.Deps{
		Auth: okAuth{}, Catalog: bus.Catalog(),
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
	r.bus = bus
	if r.tail, err = logjournal.StartTail(r.l, bus, enabled); err != nil {
		t.Fatal(err)
	}
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
		r.srv.Close()
		r.tail.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Close(ctx)
		_ = bus.Close(ctx)
		_ = r.l.Close(ctx)
	})
	return r
}

func (r *rig) sign(name string, p *auth.Principal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roster[name] = p
}

// frame is one data frame of a stream.
type frame struct {
	ID    string
	Topic string
	View  logjournal.RecordView
}

// stream is an open event stream read on its own goroutine.
type stream struct {
	t      *testing.T
	resp   *http.Response
	frames chan frame
	opened []string
	done   chan struct{}
}

// open GETs path as name. A non-200 answer is returned as its status and the
// problem code, with no stream.
func (r *rig) open(name, path string, hdr ...string) (*stream, int, string) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+path, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("X-Test-Principal", name)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		r.t.Fatalf("GET %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &p)
		return nil, resp.StatusCode, p.Code
	}
	s := &stream{t: r.t, resp: resp, frames: make(chan frame, 1024), done: make(chan struct{})}
	opened := make(chan []string, 1)
	go s.pump(opened)
	r.t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
		<-s.done
	})
	select {
	case s.opened = <-opened:
	case <-time.After(5 * time.Second):
		r.t.Fatal("the stream never sent its opened frame")
	}
	return s, http.StatusOK, ""
}

// mustOpen is open that fails the test unless the stream opened.
func (r *rig) mustOpen(name, path string, hdr ...string) *stream {
	r.t.Helper()
	s, status, code := r.open(name, path, hdr...)
	if s == nil {
		r.t.Fatalf("open %s as %s: %d %s", path, name, status, code)
	}
	return s
}

func (s *stream) pump(opened chan<- []string) {
	defer close(s.done)
	defer close(s.frames)
	sc := bufio.NewScanner(s.resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var id string
	sentOpened := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			var en struct {
				Topic string          `json:"topic"`
				Data  json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &en) != nil {
				continue
			}
			if en.Topic == "core.stream.opened.v1" && !sentOpened {
				var d struct {
					Topics []string `json:"topics"`
				}
				_ = json.Unmarshal(en.Data, &d)
				opened <- d.Topics
				sentOpened = true
				continue
			}
			f := frame{ID: id, Topic: en.Topic}
			if en.Topic == logjournal.TailTopicName {
				_ = json.Unmarshal(en.Data, &f.View)
			}
			id = ""
			s.frames <- f
		}
	}
}

// next is the next frame of the tail topic.
func (s *stream) next() frame {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			s.t.Fatal("the stream ended while waiting for a record")
		}
		return f
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for a record")
	}
	return frame{}
}

// messages reads n records and returns their messages.
func (s *stream) messages(n int) []string {
	s.t.Helper()
	out := make([]string, n)
	for i := range out {
		out[i] = s.next().View.Message
	}
	return out
}

// quiet fails when a tail record arrives within d.
func (s *stream) quiet(d time.Duration) {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		if ok {
			s.t.Fatalf("unexpected record %+v", f)
		}
	case <-time.After(d):
	}
}

// logCast logs the fixture's lines through the module logger: one flagged
// line, two with user_id (one top level, one in a group) and one without.
func logCast(l *log.Logging) {
	lg := l.Module("core").With(slog.String(log.KeySubsystem, "scanner"))
	lg.Info("selina's password was reset", slog.String("user_id", "u-selina"))
	lg.Info("scan finished", slog.String("user_id", "u-bruce"), slog.String("request_id", "req-1"))
	lg.Info("grouped attribute", slog.Group("ctx", slog.String("user_id", "u-alfred")))
	lg.Info("no user at all", slog.Int("status", 204))
}

// TestJournal_RequiresSystemManage's event half lives in loghttp's
// permissions_test.go (F5-19 owns the test); this is the same cells at the
// package that owns the topic.
func TestJournalTail_TopicNeedsSystemManage(t *testing.T) {
	r := newRig(t, nil)
	r.sign("bruce", person(t, "u-bruce", permManage))
	r.sign("dick", person(t, "u-dick", permView))

	if _, status, code := r.open("dick", "/?topic="+logjournal.TailTopicName); status != http.StatusForbidden || code != "topic_not_permitted" {
		t.Errorf("system.view-only on the topic: %d %q, want 403 topic_not_permitted", status, code)
	}
	bruce := r.mustOpen("bruce", "/?topic=*")
	dick := r.mustOpen("dick", "/?topic=*")
	if !slices.Equal(bruce.opened, []string{logjournal.TailTopicName}) || len(dick.opened) != 0 {
		t.Fatalf("opened topics: bruce %v, dick %v", bruce.opened, dick.opened)
	}
	r.l.Module("core").Info("the signal is lit")
	if got := bruce.messages(1); !slices.Equal(got, []string{"the signal is lit"}) {
		t.Errorf("system.manage subscriber got %v", got)
	}
	dick.quiet(300 * time.Millisecond)
}

// TestJournalTail_OtherUsersLinesNeedUsersView is the live-tail twin of
// TestJournal_OtherUsersLinesNeedUsersView: without core:users.view a record
// the predicate flags is not sent and user_id is blank on the rest; with it
// every record arrives intact; a replay after Last-Event-ID is filtered the
// same way.
func TestJournalTail_OtherUsersLinesNeedUsersView(t *testing.T) {
	r := newRig(t, namesSelina)
	r.sign("bruce", person(t, "u-bruce", permManage))
	r.sign("lucius", person(t, "u-lucius", permManage, permUsers))
	r.sign("selina", person(t, "u-selina", permManage))
	const topic = "/?topic=" + logjournal.TailTopicName

	bruce := r.mustOpen("bruce", topic)
	lucius := r.mustOpen("lucius", topic)
	selina := r.mustOpen("selina", topic)
	r.l.Module("core").Info("the opening line")
	logCast(r.l)

	byMsg := func(fs []frame) map[string]logjournal.RecordView {
		m := map[string]logjournal.RecordView{}
		for _, f := range fs {
			m[f.View.Message] = f.View
		}
		return m
	}
	read := func(s *stream, n int) []frame {
		t.Helper()
		fs := make([]frame, n)
		for i := range fs {
			fs[i] = s.next()
		}
		return fs
	}
	// blanked asserts the records fs are what a subscriber without users.view
	// is sent: the flagged line absent unless it is the viewer's own, and
	// user_id present and blank wherever the record has one.
	blanked := func(who string, fs []frame, wantMsgs ...string) {
		t.Helper()
		m := byMsg(fs)
		if len(fs) != len(wantMsgs) {
			t.Fatalf("%s: %d records, want %d %v", who, len(fs), len(wantMsgs), wantMsgs)
		}
		for _, msg := range wantMsgs {
			if _, ok := m[msg]; !ok {
				t.Errorf("%s: %q was not sent", who, msg)
			}
		}
		for _, f := range fs {
			for k, v := range f.View.Attrs {
				if (k == "user_id" || strings.HasSuffix(k, ".user_id")) && v != "" {
					t.Errorf("%s: %q: %s = %q not blanked", who, f.View.Message, k, v)
				}
			}
		}
		for msg, key := range map[string]string{"scan finished": "user_id", "grouped attribute": "ctx.user_id", "selina's password was reset": "user_id"} {
			if v, sent := m[msg]; sent {
				if got, ok := v.Attrs[key]; !ok || got != "" {
					t.Errorf("%s: %q: %s = %q (present %v), want present and blank", who, msg, key, got, ok)
				}
			}
		}
		if v, sent := m["scan finished"]; sent && v.Attrs["request_id"] != "req-1" {
			t.Errorf("%s: request_id was touched: %v", who, v.Attrs)
		}
		if v, sent := m["no user at all"]; sent {
			if _, ok := v.Attrs["user_id"]; ok || v.Attrs["status"] != "204" {
				t.Errorf("%s: a record with no user_id changed: %v", who, v.Attrs)
			}
		}
	}

	// With users.view: all five records, user_id intact.
	all := read(lucius, 5)
	got := byMsg(all)
	if got["selina's password was reset"].Attrs["user_id"] != "u-selina" || got["scan finished"].Attrs["user_id"] != "u-bruce" ||
		got["grouped attribute"].Attrs["ctx.user_id"] != "u-alfred" {
		t.Errorf("with users.view: user_id changed: %+v", got)
	}

	// Without: the flagged line never comes.
	live := read(bruce, 4)
	if _, ok := byMsg(live)["selina's password was reset"]; ok {
		t.Error("live: the flagged line was sent")
	}
	blanked("live", live, "the opening line", "scan finished", "grouped attribute", "no user at all")
	bruce.quiet(100 * time.Millisecond)

	t.Run("the_subject_herself_is_not_another_user", func(t *testing.T) {
		// The predicate sees the viewer's id (read from the request principal
		// in ctx): her own line is sent, still with user_id blank.
		blanked("selina", read(selina, 5), "the opening line", "selina's password was reset", "scan finished", "grouped attribute", "no user at all")
	})

	t.Run("replay_is_filtered_like_live", func(t *testing.T) {
		// Resume after the opening line: the four cast lines replay.
		resumed := r.mustOpen("bruce", topic, "Last-Event-ID", all[0].ID)
		replay := read(resumed, 3)
		blanked("replay", replay, "scan finished", "grouped attribute", "no user at all")
		resumed.quiet(100 * time.Millisecond)

		full := r.mustOpen("lucius", topic, "Last-Event-ID", all[0].ID)
		if got := byMsg(read(full, 4)); got["selina's password was reset"].Attrs["user_id"] != "u-selina" {
			t.Errorf("replay to users.view: %+v", got)
		}
	})
}

// TestJournalTail_AuthOffDeliversNothing: core:system.manage is AuthRequired,
// so with authentication off the topic is refused by name and a wildcard
// stream leaves it out and carries nothing (05-logging §5, "Auth-off reach").
func TestJournalTail_AuthOffDeliversNothing(t *testing.T) {
	r := newRig(t, nil)
	r.sign("off", authOff(t))
	if _, status, code := r.open("off", "/?topic="+logjournal.TailTopicName); status != http.StatusForbidden || code != "topic_not_permitted" {
		t.Errorf("auth-off on the topic: %d %q, want 403 topic_not_permitted", status, code)
	}
	s := r.mustOpen("off", "/?topic=*")
	if len(s.opened) != 0 {
		t.Errorf("auth-off wildcard stream opened with %v", s.opened)
	}
	r.l.Module("core").Error("the Batcave alarm rang")
	s.quiet(300 * time.Millisecond)
}

// TestJournalTail_WireMembers pins the payload's member names to the query
// route's (loghttp.recordView), which the SPA's log view reads from both.
func TestJournalTail_WireMembers(t *testing.T) {
	b, err := json.Marshal(logjournal.RecordView{Attrs: map[string]string{}, TraceID: "t", SpanID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var names []string
	for k := range m {
		names = append(names, k)
	}
	slices.Sort(names)
	want := []string{"attrs", "level", "message", "module", "span_id", "subsystem", "time", "trace_id"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("members %v, want %v", names, want)
	}
}

// TestJournalTail_InviteTokenAbsent is the tail cell of TestRedact_CoreInvitePath
// (05-logging §10): an invite token logged under path never reaches the bus,
// so no subscriber's record can carry it.
func TestJournalTail_InviteTokenAbsent(t *testing.T) {
	const token = "robin-grapple-7c1f9a"
	l, err := log.New(log.Options{Output: io.Discard, Format: log.FormatText, Level: slog.LevelDebug, Redactor: log.NewRedactor(nil)})
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", logjournal.TailSpec(nil)); err != nil {
		t.Fatal(err)
	}
	got := make(chan json.RawMessage, 4)
	if err := bus.Subscribe("core", events.OnRaw(logjournal.TailTopicName, "core.test.tail", func(_ context.Context, e events.RawEnvelope) {
		got <- e.Payload
	})); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	tl, err := logjournal.StartTail(l, bus, enabled)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		tl.Stop()
		_ = l.Close(context.Background())
		_ = bus.Close(context.Background())
	}()
	l.Module("core").Info("a request",
		slog.String("path", "/api/core/v1/auth/invites/"+token),
		slog.String("url.path", "/invite/"+token),
		slog.String("note", "GET /invite/"+token))
	select {
	case p := <-got:
		if strings.Contains(string(p), token) {
			t.Errorf("the tail record carries the invite token: %s", p)
		}
		if !strings.Contains(string(p), "[REDACTED]") {
			t.Errorf("the tail record shows no redaction marker: %s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tail record arrived")
	}
}

// TestJournalTail_TapNeverBlocks: a subscriber that never reads and a full
// bus queue leave 10k log calls unblocked. The log calls run on the test's
// goroutine, so a blocked publish would be a deadlock the bubble reports; the
// bus's drops are counted (reason queue_full) and add up with what it queued.
func TestJournalTail_TapNeverBlocks(t *testing.T) {
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "batcave", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("events", "topic", "subscriber", "reason")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		l := newLogging(t)
		bus, err := events.NewBus(events.BusOptions{Metrics: sc})
		if err != nil {
			t.Fatal(err)
		}
		if err := bus.Declare("core", logjournal.TailSpec(nil)); err != nil {
			t.Fatal(err)
		}
		release, entered := make(chan struct{}), make(chan struct{}, 1)
		var held atomic.Int64
		if err := bus.Subscribe("core", events.OnRaw(logjournal.TailTopicName, "core.test.never-reads", func(context.Context, events.RawEnvelope) {
			held.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}, events.QueueSize(1))); err != nil {
			t.Fatal(err)
		}
		bus.Freeze()
		if err := bus.Open(); err != nil {
			t.Fatal(err)
		}
		tl, err := logjournal.StartTail(l, bus, enabled)
		if err != nil {
			t.Fatal(err)
		}
		lg := l.Module("core")
		lg.Info("the first signal")
		<-entered // the subscriber now holds one event and never returns
		for range 10_000 {
			lg.Info("signal")
		}
		synctest.Wait()
		published := tl.Counters().Published
		if published < 3 {
			t.Fatalf("only %d records reached the bus", published)
		}
		if lost := l.Counters().Dropped[logjournal.TailTapName]; published+lost < 10_001 {
			t.Errorf("published %d + dropped at the tap %d < 10001 records logged", published, lost)
		}
		// The subscriber holds one event and its queue one: everything else was dropped and counted.
		if dropped := queueFullDrops(t, p); dropped != float64(published)-2 {
			t.Errorf("bus drops = %v, want published %d - 2 (one held, one queued)", dropped, published)
		}
		close(release)
		synctest.Wait()
		tl.Stop()
		if err := l.Close(context.Background()); err != nil {
			t.Errorf("logger close: %v", err)
		}
		if err := bus.Close(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("bus close: %v", err)
		}
	})
}

// queueFullDrops is mediacore_events_dropped_total{reason="queue_full"} for the tail topic.
func queueFullDrops(t testing.TB, p *metrics.Provider) float64 {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != "mediacore_events_dropped_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["topic"] == logjournal.TailTopicName && labels["reason"] == "queue_full" {
				sum += m.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// TestJournalTail_DisabledIsInertAndNilPublisherRefused: the tail follows the
// journal's own Enabled and level, and refuses to start without a publisher.
func TestJournalTail_DisabledIsInertAndNilPublisherRefused(t *testing.T) {
	l := newLogging(t)
	if _, err := logjournal.StartTail(l, nil, enabled); !errors.Is(err, logjournal.ErrNoPublisher) {
		t.Errorf("nil publisher: err = %v, want ErrNoPublisher", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logjournal.StartTail(l, bus, logjournal.Settings{Enabled: true, MinLevel: "loud"}); err == nil {
		t.Error("a bad min_level was accepted")
	}
	off := enabled
	off.Enabled = false
	tl, err := logjournal.StartTail(l, bus, off)
	if err != nil {
		t.Fatal(err)
	}
	l.Module("core").Info("nobody is listening")
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := tl.Counters(); c != (logjournal.TailCounters{}) {
		t.Errorf("a disabled tail counted %+v", c)
	}
	tl.Stop()
}

// TestJournalTail_FollowsJournalLevel: the tail carries what the journal
// keeps, so a record below core.log.journal.min_level is not published.
func TestJournalTail_FollowsJournalLevel(t *testing.T) {
	l := newLogging(t)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", logjournal.TailSpec(nil)); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 8)
	if err := bus.Subscribe("core", events.OnRaw(logjournal.TailTopicName, "core.test.levels", func(_ context.Context, e events.RawEnvelope) {
		var v logjournal.RecordView
		_ = json.Unmarshal(e.Payload, &v)
		got <- v.Level + ":" + v.Message
	})); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	tl, err := logjournal.StartTail(l, bus, enabled) // min_level info
	if err != nil {
		t.Fatal(err)
	}
	defer tl.Stop()
	lg := l.Module("core")
	lg.Debug("below the journal's level")
	lg.Info("at the level")
	lg.Error("above it")
	for _, want := range []string{"info:at the level", "error:above it"} {
		select {
		case g := <-got:
			if g != want {
				t.Fatalf("got %q, want %q", g, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no record %q", want)
		}
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case g := <-got:
		t.Errorf("an extra record %q", g)
	default:
	}
	_ = bus.Close(context.Background())
}

// TestJournalTail_FiltersFailClosed: a payload the filters cannot read is
// withheld rather than sent, whatever the subscriber holds; a missing checker
// holds nothing.
func TestJournalTail_FiltersFailClosed(t *testing.T) {
	spec := logjournal.TailSpec(namesSelina)
	manage := checkerOf(t, permManage)
	ctx := auth.WithPrincipal(context.Background(), person(t, "u-bruce", permManage))
	good, err := json.Marshal(logjournal.RecordView{Time: time.Unix(1, 0).UTC(), Level: "info", Message: "scan finished", Attrs: map[string]string{"user_id": "u-bruce"}})
	if err != nil {
		t.Fatal(err)
	}
	env := func(p []byte) events.RawEnvelope {
		return events.RawEnvelope{Topic: logjournal.TailTopicName, Payload: p}
	}

	if !spec.Visible(ctx, manage, env(good)) {
		t.Error("an unflagged record was withheld")
	}
	for name, p := range map[string][]byte{
		"not_json":      []byte(`{"time":`),
		"unknown_level": []byte(`{"level":"loud","message":"scan finished"}`),
	} {
		if spec.Visible(ctx, manage, env(p)) {
			t.Errorf("%s: an unreadable payload was sent", name)
		}
	}
	for name, p := range map[string]string{
		"not_json":        `{"time":`,
		"wrong_shape":     `{"attrs":5,"user_id":"u-bruce"}`,
		"not_an_object":   `[1,2]`,
		"attrs_not_texts": `{"attrs":{"user_id":7}}`,
	} {
		if out := spec.Redact(manage, []byte(p)); json.Valid(out) {
			t.Errorf("Redact of %s = %q, want something realtime withholds", name, out)
		}
	}
	for who, c := range map[string]rbac.Checker{"manage_only": manage, "nil_checker": nil} {
		var v logjournal.RecordView
		if err := json.Unmarshal(spec.Redact(c, good), &v); err != nil || v.Attrs["user_id"] != "" {
			t.Errorf("%s: user_id = %q (err %v), want blank", who, v.Attrs["user_id"], err)
		}
	}
	// Holding the scope is not enough on its own: rbac.Can's rule also needs
	// the requirement satisfied, as the query route applies it.
	if out := spec.Redact(holdsButUnsatisfied{manage}, good); string(out) == string(good) {
		t.Error("a checker that has users.view but does not satisfy it was treated as reading user data")
	}
	if out := spec.Redact(checkerOf(t, permManage, permUsers), good); string(out) != string(good) {
		t.Errorf("Redact for users.view changed the payload: %s", out)
	}
	if spec.Realtime == nil || spec.Durable != nil || spec.Permission.String() != rbac.Need(permManage).String() {
		t.Errorf("TailSpec = realtime %v durable %v permission %s", spec.Realtime, spec.Durable, spec.Permission)
	}
}

// holdsButUnsatisfied reports every scope as held and no requirement as met.
type holdsButUnsatisfied struct{ rbac.Checker }

func (holdsButUnsatisfied) Has(rbac.Permission) bool        { return true }
func (holdsButUnsatisfied) Satisfies(rbac.Requirement) bool { return false }

// TestJournalTail_UnstorableTimeIsSkipped: a record the journal's own encoding
// refuses (a time past year 9999) is not published, so the stream never
// carries a record the query route could not return; it is counted.
func TestJournalTail_UnstorableTimeIsSkipped(t *testing.T) {
	l, err := log.New(log.Options{
		Output: io.Discard, Format: log.FormatText, Level: slog.LevelDebug, Redactor: log.NewRedactor(nil),
		Now: func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", logjournal.TailSpec(nil)); err != nil {
		t.Fatal(err)
	}
	var seen atomic.Int64
	if err := bus.Subscribe("core", events.OnRaw(logjournal.TailTopicName, "core.test.count", func(context.Context, events.RawEnvelope) { seen.Add(1) })); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	tl, err := logjournal.StartTail(l, bus, enabled)
	if err != nil {
		t.Fatal(err)
	}
	l.Module("core").Info("a record from the year ten thousand")
	if err := l.Close(context.Background()); err != nil { // drains the tap
		t.Fatal(err)
	}
	tl.Stop()
	if err := bus.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := tl.Counters(); c.Skipped != 1 || c.Published != 0 || seen.Load() != 0 {
		t.Errorf("counters %+v, subscriber saw %d; want 1 skipped, 0 published, 0 seen", c, seen.Load())
	}
}
