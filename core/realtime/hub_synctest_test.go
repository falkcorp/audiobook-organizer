// file: core/realtime/hub_synctest_test.go
// version: 1.0.0
// guid: b3e81d54-2f67-4c09-a1d8-6e5f0c7a9b32
// last-edited: 2026-11-11

package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// These cells run in a synctest bubble: the clock is fake, so heartbeats, write
// deadlines, coalescing windows and the shutdown budget are exact, and a
// goroutine the hub leaves behind makes the bubble fail when it ends. They use
// principals built from a plain in-memory checker (no auth store, no pebble:
// nothing durable-blocks outside the bubble) and a hand-written Revalidator.

// perms is a fixed-set rbac.Checker.
type perms struct {
	has      map[rbac.Permission]bool
	confined bool
}

func (p perms) Has(s rbac.Permission) bool { return p.has[s] }
func (p perms) Confined() bool             { return p.confined }
func (p perms) Concrete() []rbac.Permission {
	var out []rbac.Permission
	for k := range p.has {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (p perms) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindAuthenticated:
		return !p.confined && len(p.has) > 0
	case rbac.KindAllOf:
		for _, s := range r.Permissions() {
			if !p.has[s] {
				return false
			}
		}
		return len(r.Permissions()) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(r.Permissions(), func(s rbac.Permission) bool { return p.has[s] })
	}
	return false
}

func principal(name string, ps ...rbac.Permission) *auth.Principal {
	m := map[rbac.Permission]bool{}
	for _, s := range ps {
		m[s] = true
	}
	return &auth.Principal{
		User:         &auth.User{ID: auth.UserID("u-" + name), Username: name},
		Method:       auth.MethodSession,
		CredentialID: "s-" + name,
		Perms:        perms{has: m},
	}
}

type revalFunc func(ctx context.Context, p *auth.Principal) error

func (f revalFunc) Revalidate(ctx context.Context, p *auth.Principal) error { return f(ctx, p) }

// script answers each call with the next error of errs, then with the last
// one for ever, and counts the calls.
type script struct {
	mu    sync.Mutex
	errs  []error
	calls int
	seen  []*auth.Principal
}

func (s *script) next(p *auth.Principal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, p)
	i := s.calls
	s.calls++
	if i >= len(s.errs) {
		i = len(s.errs) - 1
	}
	return s.errs[i]
}

func (s *script) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

// pipe is the ResponseWriter of a bubble stream. After gateFrom writes it
// blocks every write until open is closed or the write deadline passes, which
// is what a client that stopped reading looks like to the server.
type pipe struct {
	mu       sync.Mutex
	hdr      http.Header
	body     strings.Builder
	writes   int
	gateFrom int
	open     chan struct{}
	deadline time.Time
}

func newPipe(gateFrom int) *pipe {
	return &pipe{hdr: http.Header{}, gateFrom: gateFrom, open: make(chan struct{})}
}

func (p *pipe) Header() http.Header { return p.hdr }
func (p *pipe) WriteHeader(int)     {}
func (p *pipe) FlushError() error   { return nil }
func (p *pipe) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	p.deadline = t
	p.mu.Unlock()
	return nil
}

func (p *pipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.writes++
	gated := p.gateFrom > 0 && p.writes >= p.gateFrom
	dl := p.deadline
	p.mu.Unlock()
	if gated {
		select {
		case <-p.open:
		case <-time.After(time.Until(dl)):
			return 0, os.ErrDeadlineExceeded
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.body.Write(b)
}

func (p *pipe) text() string { p.mu.Lock(); defer p.mu.Unlock(); return p.body.String() }

type wire struct {
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

// wires parses every data frame of text.
func wires(t testing.TB, text string) []wire {
	t.Helper()
	var out []wire
	for l := range strings.SplitSeq(text, "\n") {
		if d, ok := strings.CutPrefix(l, "data: "); ok {
			var w wire
			if err := json.Unmarshal([]byte(d), &w); err != nil {
				t.Fatalf("data line %q: %v", d, err)
			}
			out = append(out, w)
		}
	}
	return out
}

// lampsIn is the lamp (or beacon light:n) of every frame of topic.
func lampsIn(t testing.TB, ws []wire, topic string) []string {
	t.Helper()
	var out []string
	for _, w := range ws {
		if w.Topic != topic {
			continue
		}
		var s struct {
			Lamp  string `json:"lamp"`
			Light string `json:"light"`
			N     int    `json:"n"`
		}
		if err := json.Unmarshal(w.Data, &s); err != nil {
			t.Fatalf("payload %s: %v", w.Data, err)
		}
		if s.Light != "" {
			out = append(out, fmt.Sprintf("%s:%d", s.Light, s.N))
		} else {
			out = append(out, s.Lamp)
		}
	}
	return out
}

func closedReason(t testing.TB, text string) string {
	t.Helper()
	for _, w := range wires(t, text) {
		if w.Topic == "core.stream.closed.v1" {
			var d struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(w.Data, &d)
			return d.Reason
		}
	}
	return ""
}

// bubble is a bus and a hub built inside the bubble, with streams served on
// goroutines of the bubble.
type bubble struct {
	t   *testing.T
	bus *events.Bus
	hub *realtime.Hub
	pub events.Scope
	cor events.Scope
}

func newBubble(t *testing.T, o realtime.Options, d realtime.Deps, mod func(core, batcave, arkham *[]events.TopicSpec)) *bubble {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	core, batcave, arkham := topicSpecs()
	if mod != nil {
		mod(&core, &batcave, &arkham)
	}
	for _, c := range []struct {
		owner string
		specs []events.TopicSpec
	}{{"core", core}, {"batcave", batcave}, {"arkham", arkham}} {
		if err := bus.Declare(c.owner, c.specs...); err != nil {
			t.Fatal(err)
		}
	}
	d.Catalog = bus.Catalog()
	if d.Auth == nil {
		d.Auth = revalFunc(func(context.Context, *auth.Principal) error { return nil })
	}
	hub, err := realtime.NewHub(o, d)
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
	return &bubble{t: t, bus: bus, hub: hub, pub: bus.Scope("batcave"), cor: bus.Scope("core")}
}

// stop closes the hub and the bus inside the bubble, so the bubble can end.
func (b *bubble) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = b.hub.Close(ctx)
	_ = b.bus.Close(ctx)
}

// serve runs one stream as p over w until ctx ends; the channel closes when the
// handler returns.
func (b *bubble) serve(ctx context.Context, p *auth.Principal, w http.ResponseWriter, target string) <-chan struct{} {
	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(auth.WithPrincipal(ctx, p))
	go func() {
		defer close(done)
		b.hub.Handler().ServeHTTP(w, req)
	}()
	return done
}

func (b *bubble) signal(topic events.Topic[Signal], key, lamp string) {
	events.Publish(b.pub, context.Background(), topic, key, Signal{Lamp: lamp, Wing: "public"})
}

func hasFinished(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// TestHub_SlowSubscriberDoesNotBlockPublisher: a stream whose client stopped
// reading holds one write; the publisher is never held up by it, a fast stream
// beside it gets every event in order, and the slow one is told to refetch
// (reset overflow) and continues with the newest events when its client wakes.
func TestHub_SlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBubble(t, realtime.Options{RingSize: 128, ClientBuffer: 4}, realtime.Deps{}, nil)
		defer b.stop()
		bruce := principal("bruce", scopeView)
		slow, fast := newPipe(2), newPipe(0)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		slowDone := b.serve(ctx, bruce, slow, "/events?topic="+tSignal)
		fastDone := b.serve(ctx, bruce, fast, "/events?topic="+tSignal)
		synctest.Wait()
		if n := b.hub.Subscribers(); n != 2 {
			t.Fatalf("subscribers = %d", n)
		}
		start := time.Now()
		const total = 40
		var want []string
		for i := range total {
			l := fmt.Sprintf("lamp-%02d", i)
			want = append(want, l)
			b.signal(topicSignal, "", l)
			synctest.Wait() // events.Publish is async: let the bus hand each to the hub
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("publishing took %v of fake time: the publisher waited on a stalled stream", d)
		}
		if got := lampsIn(t, wires(t, fast.text()), tSignal); !slices.Equal(got, want) {
			t.Errorf("the fast stream got %v, want all %d in order", got, total)
		}
		close(slow.open)
		synctest.Wait()
		sw := wires(t, slow.text())
		got := lampsIn(t, sw, tSignal)
		if len(got) == 0 || len(got) >= total || got[len(got)-1] != want[total-1] {
			t.Errorf("the slow stream got %d frames, last %v; want a short tail ending at %s", len(got), got, want[total-1])
		}
		if !strings.Contains(slow.text(), `"reason":"overflow"`) {
			t.Errorf("the slow stream was never told to refetch:\n%s", slow.text())
		}
		if b.hub.Subscribers() != 2 {
			t.Error("a slow subscriber was disconnected instead of reset")
		}
		cancel()
		synctest.Wait()
		if !hasFinished(slowDone) || !hasFinished(fastDone) {
			t.Error("a handler outlived its context")
		}
	})
}

// TestHub_WriteDeadlineDisconnects: a client that never reads is cut after
// WriteTimeout (a write past its deadline), its place is freed, and the stream
// beside it is untouched.
func TestHub_WriteDeadlineDisconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBubble(t, realtime.Options{WriteTimeout: 10 * time.Second, Heartbeat: time.Hour}, realtime.Deps{}, nil)
		defer b.stop()
		bruce := principal("bruce", scopeView)
		stuck, fine := newPipe(2), newPipe(0)
		stuckDone := b.serve(t.Context(), bruce, stuck, "/events?topic="+tSignal)
		b.serve(t.Context(), bruce, fine, "/events?topic="+tSignal)
		synctest.Wait()
		b.signal(topicSignal, "", "first")
		synctest.Wait() // the stuck stream is now blocked in a write
		if hasFinished(stuckDone) {
			t.Fatal("ended before its deadline")
		}
		time.Sleep(9 * time.Second)
		synctest.Wait()
		if hasFinished(stuckDone) || b.hub.Subscribers() != 2 {
			t.Fatal("cut before WriteTimeout")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !hasFinished(stuckDone) {
			t.Fatal("a write past its deadline did not end the stream")
		}
		if n := b.hub.Subscribers(); n != 1 {
			t.Errorf("subscribers = %d, want the stuck place freed", n)
		}
		b.signal(topicSignal, "", "second")
		synctest.Wait()
		if got := lampsIn(t, wires(t, fine.text()), tSignal); !slices.Equal(got, []string{"first", "second"}) {
			t.Errorf("the other stream got %v", got)
		}
	})
}

// TestHub_CoalesceLatestPerKey: a Coalesce topic delivers the latest event of
// each key when the window ends, one frame per key, and an event after the
// window opens a new one. Events without a key are not held back.
func TestHub_CoalesceLatestPerKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBubble(t, realtime.Options{Heartbeat: time.Hour}, realtime.Deps{}, nil)
		defer b.stop()
		w := newPipe(0)
		b.serve(t.Context(), principal("bruce", scopeView), w, "/events?topic="+topicBeacon.Name())
		synctest.Wait()
		beacon := func(key string, n int) {
			events.Publish(b.pub, context.Background(), topicBeacon, key, Beacon{Light: key, N: n})
		}
		for n := 1; n <= 5; n++ {
			beacon("east", n)
		}
		for n := 1; n <= 3; n++ {
			beacon("west", n)
		}
		synctest.Wait()
		if got := lampsIn(t, wires(t, w.text()), topicBeacon.Name()); len(got) != 0 {
			t.Fatalf("delivered %v before the window ended", got)
		}
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		got := lampsIn(t, wires(t, w.text()), topicBeacon.Name())
		slices.Sort(got)
		if want := []string{"east:5", "west:3"}; !slices.Equal(got, want) {
			t.Fatalf("after the window: %v, want %v", got, want)
		}
		beacon("east", 6)
		events.Publish(b.pub, context.Background(), topicBeacon, "", Beacon{Light: "plain", N: 7})
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		got = lampsIn(t, wires(t, w.text()), topicBeacon.Name())
		slices.Sort(got)
		if want := []string{"east:5", "east:6", "plain:7", "west:3"}; !slices.Equal(got, want) {
			t.Errorf("after the second window: %v, want %v", got, want)
		}
	})
}

// TestHub_RevalidationStrikes: Revalidate runs on every heartbeat with the
// stream's own principal. A definite refusal ends the stream at once; a store
// failure ends it only on the third consecutive one, and any success resets
// the count.
func TestHub_RevalidationStrikes(t *testing.T) {
	down := fmt.Errorf("store: %w", auth.ErrStoreUnavailable)
	t.Run("transient failures need three in a row", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sc := &script{errs: []error{down, down, nil, down, down, down}}
			b := newBubble(t, realtime.Options{Heartbeat: 15 * time.Second}, realtime.Deps{
				Auth: revalFunc(func(_ context.Context, p *auth.Principal) error { return sc.next(p) }),
			}, nil)
			defer b.stop()
			p := principal("bruce", scopeView)
			w := newPipe(0)
			done := b.serve(t.Context(), p, w, "/events")
			synctest.Wait()
			for beat := 1; beat <= 5; beat++ {
				time.Sleep(15 * time.Second)
				synctest.Wait()
				if hasFinished(done) {
					t.Fatalf("ended on beat %d of the script %v", beat, sc.errs)
				}
			}
			time.Sleep(15 * time.Second)
			synctest.Wait()
			if !hasFinished(done) {
				t.Fatal("three store failures in a row did not end the stream")
			}
			if r := closedReason(t, w.text()); r != "credential_revoked" {
				t.Errorf("closed with %q", r)
			}
			if sc.count() != 6 {
				t.Errorf("Revalidate ran %d times, want once per heartbeat", sc.count())
			}
			for _, s := range sc.seen {
				if s != p {
					t.Fatal("Revalidate was not given the stream's principal")
				}
			}
		})
	})
	t.Run("a definite refusal ends at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			sc := &script{errs: []error{nil, errors.New("session revoked")}}
			b := newBubble(t, realtime.Options{Heartbeat: 15 * time.Second}, realtime.Deps{
				Auth: revalFunc(func(_ context.Context, p *auth.Principal) error { return sc.next(p) }),
			}, nil)
			defer b.stop()
			w := newPipe(0)
			done := b.serve(t.Context(), principal("bruce", scopeView), w, "/events")
			time.Sleep(15 * time.Second)
			synctest.Wait()
			if hasFinished(done) {
				t.Fatal("ended while the credential stood")
			}
			time.Sleep(15 * time.Second)
			synctest.Wait()
			if !hasFinished(done) || closedReason(t, w.text()) != "credential_revoked" {
				t.Errorf("a revoked credential kept streaming:\n%s", w.text())
			}
		})
	})
	t.Run("a cancelled context is not a strike", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := newBubble(t, realtime.Options{Heartbeat: 15 * time.Second}, realtime.Deps{
				Auth: revalFunc(func(ctx context.Context, _ *auth.Principal) error { return ctx.Err() }),
			}, nil)
			defer b.stop()
			ctx, cancel := context.WithCancel(context.Background())
			done := b.serve(ctx, principal("bruce", scopeView), newPipe(0), "/events")
			synctest.Wait()
			cancel()
			synctest.Wait()
			if !hasFinished(done) {
				t.Fatal("did not return")
			}
		})
	})
}

// TestHub_RefreshStrikes: after core.rbac.changed.v1 the hub asks Refresh for
// the principal as it stands now, once per wake and again on each heartbeat
// while it fails; three store failures in a row end the stream
// permission_revoked, a success keeps it and is not asked again.
func TestHub_RefreshStrikes(t *testing.T) {
	down := fmt.Errorf("store: %w", auth.ErrStoreUnavailable)
	run := func(t *testing.T, errs []error) (calls func() int, ended func() bool, w *pipe, step func(time.Duration)) {
		sc := &script{errs: errs}
		p := principal("bruce", scopeView)
		b := newBubble(t, realtime.Options{Heartbeat: 15 * time.Second}, realtime.Deps{
			Refresh: func(_ context.Context, old *auth.Principal) (*auth.Principal, error) {
				if err := sc.next(old); err != nil {
					return nil, err
				}
				return old, nil
			},
		}, nil)
		t.Cleanup(b.stop)
		w = newPipe(0)
		done := b.serve(t.Context(), p, w, "/events")
		synctest.Wait()
		events.Publish(b.cor, context.Background(), topicRBAC, "", RBACChanged{Generation: 1})
		synctest.Wait()
		return sc.count, func() bool { return hasFinished(done) }, w, func(d time.Duration) { time.Sleep(d); synctest.Wait() }
	}
	t.Run("three failures end it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			calls, ended, w, step := run(t, []error{down})
			if calls() != 1 || ended() {
				t.Fatalf("after the change: %d calls, ended %v", calls(), ended())
			}
			step(15 * time.Second)
			if calls() != 2 || ended() {
				t.Fatalf("after one beat: %d calls, ended %v", calls(), ended())
			}
			step(15 * time.Second)
			if !ended() || closedReason(t, w.text()) != "permission_revoked" {
				t.Errorf("not ended permission_revoked after three failures:\n%s", w.text())
			}
		})
	})
	t.Run("a success ends the retrying", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			calls, ended, _, step := run(t, []error{down, nil})
			step(15 * time.Second)
			if calls() != 2 || ended() {
				t.Fatalf("%d calls, ended %v", calls(), ended())
			}
			step(60 * time.Second)
			if calls() != 2 {
				t.Errorf("Refresh was asked again (%d calls) with no new generation", calls())
			}
		})
	})
	t.Run("a definite error is a revoked credential", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			_, ended, w, _ := run(t, []error{errors.New("user deleted")})
			if !ended() || closedReason(t, w.text()) != "credential_revoked" {
				t.Errorf("a deleted user kept streaming:\n%s", w.text())
			}
		})
	})
}

// TestHub_ShutdownDrainsWithinBudget: Close sends closed{shutdown} to a stream
// of every permission level and returns when they are gone; with a client that
// has stopped reading it returns at its own deadline instead of waiting, and
// Run, which has a 5 s budget, returns an error at exactly that budget. Nothing
// is left running: the bubble ends only when every goroutine has.
func TestHub_ShutdownDrainsWithinBudget(t *testing.T) {
	t.Run("every permission level gets the frame", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := newBubble(t, realtime.Options{}, realtime.Deps{}, nil)
			defer b.stop()
			levels := []*auth.Principal{
				principal("bruce", scopeView, scopeRun, scopeArk, "core:admin"),
				principal("dick", scopeView),
				principal("oswald", "core:self"),
			}
			var pipes []*pipe
			var dones []<-chan struct{}
			for _, p := range levels {
				w := newPipe(0)
				pipes = append(pipes, w)
				dones = append(dones, b.serve(t.Context(), p, w, "/events"))
			}
			synctest.Wait()
			if b.hub.Subscribers() != 3 {
				t.Fatalf("subscribers = %d", b.hub.Subscribers())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			if err := b.hub.Close(ctx); err != nil {
				t.Fatalf("Close = %v", err)
			}
			if d := time.Since(start); d != 0 {
				t.Errorf("Close took %v", d)
			}
			for i, w := range pipes {
				if !hasFinished(dones[i]) || closedReason(t, w.text()) != "shutdown" {
					t.Errorf("stream %d:\n%s", i, w.text())
				}
			}
			if err := b.hub.Close(ctx); err != nil {
				t.Errorf("second Close = %v", err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(auth.WithPrincipal(t.Context(), levels[0]))
			b.hub.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
				t.Errorf("a stream opened after Close: %d %v", rec.Code, rec.Header())
			}
		})
	})
	t.Run("a stuck client does not hold Close past its context", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := newBubble(t, realtime.Options{WriteTimeout: 20 * time.Second}, realtime.Deps{}, nil)
			defer b.stop()
			w := newPipe(2) // the closed frame is its second write, and blocks
			done := b.serve(t.Context(), principal("bruce", scopeView), w, "/events")
			synctest.Wait()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			err := b.hub.Close(ctx)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Second {
				t.Errorf("Close = %v after %v, want the deadline error at 1s", err, time.Since(start))
			}
			time.Sleep(20 * time.Second)
			synctest.Wait()
			if !hasFinished(done) {
				t.Error("the stuck stream never ended")
			}
		})
	})
	t.Run("Run is bounded by its budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := newBubble(t, realtime.Options{WriteTimeout: time.Minute}, realtime.Deps{}, nil)
			defer b.stop()
			b.serve(t.Context(), principal("bruce", scopeView), newPipe(2), "/events")
			synctest.Wait()
			ctx, cancel := context.WithCancel(context.Background())
			var runErr atomic.Value
			ran := make(chan struct{})
			go func() { runErr.Store(errOrNil(b.hub.Run(ctx))); close(ran) }()
			synctest.Wait()
			cancel()
			start := time.Now()
			<-ran
			if d := time.Since(start); d != 5*time.Second {
				t.Errorf("Run returned after %v, want its 5s budget", d)
			}
			if err, _ := runErr.Load().(error); err == nil {
				t.Error("Run reported a clean drain with a stream still open")
			}
			time.Sleep(time.Minute) // the stuck write expires and the stream ends
			synctest.Wait()
		})
	})
}

func errOrNil(err error) any {
	if err == nil {
		return struct{ error }{}
	}
	return err
}

// TestHub_FanOutStartsNoGoroutinePerEvent: the publisher starts no goroutine per
// subscriber. A hundred subscribers over one publish add none beyond their own
// streams.
func TestHub_FanOutStartsNoGoroutinePerEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBubble(t, realtime.Options{MaxClients: 100, MaxPerBucket: 100}, realtime.Deps{}, nil)
		defer b.stop()
		for i := range 100 {
			b.serve(t.Context(), principal(fmt.Sprintf("u%d", i), scopeView), newPipe(0), "/events?topic="+tSignal)
		}
		synctest.Wait()
		if b.hub.Subscribers() != 100 {
			t.Fatalf("subscribers = %d", b.hub.Subscribers())
		}
		before := goroutines()
		for range 50 {
			b.signal(topicSignal, "", "x")
		}
		synctest.Wait()
		if after := goroutines(); after > before {
			t.Errorf("goroutines %d -> %d over 50 publishes to 100 streams", before, after)
		}
	})
}

func goroutines() int { return runtime.NumGoroutine() }
