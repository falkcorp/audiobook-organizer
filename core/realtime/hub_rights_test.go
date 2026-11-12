// file: core/realtime/hub_rights_test.go
// version: 1.0.0
// guid: 3b9e5c70-6d21-4f48-8a0c-91e7f2b4d615
// last-edited: 2026-11-11

package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// refresher is the Deps.Refresh a test steers: the function in fn is called,
// and calls counts the calls.
type refresher struct {
	fn    atomic.Pointer[func(ctx context.Context, p *auth.Principal) (*auth.Principal, error)]
	calls atomic.Int32
}

func (r *refresher) hook(ctx context.Context, p *auth.Principal) (*auth.Principal, error) {
	r.calls.Add(1)
	if f := r.fn.Load(); f != nil {
		return (*f)(ctx, p)
	}
	return p, nil
}

func (r *refresher) set(f func(ctx context.Context, p *auth.Principal) (*auth.Principal, error)) {
	r.fn.Store(&f)
}

// narrowed is p rebuilt over the scopes only: what a Refresh returns after a
// role lost its other grants.
func (c *cast) narrowed(t testing.TB, u *auth.User, old *auth.Principal, scopes ...rbac.Permission) *auth.Principal {
	t.Helper()
	p, err := c.svc.PrincipalFor(context.Background(), u, auth.MethodSession, scopes, u.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	p.CredentialID = old.CredentialID
	return p
}

func (c *cast) rbacChanged(gen uint64) {
	events.Publish(c.cor, context.Background(), topicRBAC, "", RBACChanged{Generation: gen})
}

// TestHub_PermissionRevokedDropsTopic: an rbac generation bump makes every
// stream re-resolve its principal (the kernel's Refresh). A topic the new
// rights no longer cover is dropped from a wildcard stream without a word and
// ends a stream that named it exactly with closed{permission_revoked}
// (05-realtime §4.6).
func TestHub_PermissionRevokedDropsTopic(t *testing.T) {
	ref := &refresher{}
	c := newCast(t, hubOptions{deps: func(d *realtime.Deps) { d.Refresh = ref.hook }})
	lucius := c.ro.get("lucius")
	ref.set(func(_ context.Context, p *auth.Principal) (*auth.Principal, error) {
		return c.narrowed(t, c.lucius, p, scopeView), nil // lost batcave:run
	})

	wild := c.open(t, "lucius", "/events?topic=batcave.*")
	if got := wild.expectOpened(); !slices.Contains(got, tCell) {
		t.Fatalf("opened = %v, lucius should start with the cell topic", got)
	}
	named := c.open(t, "lucius", "/events?topic="+tCell+"&topic="+tSignal)
	named.expectOpened()
	c.rbacChanged(2)
	if reason := named.drainUntilClosed(); reason != "permission_revoked" {
		t.Errorf("named stream closed with %q, want permission_revoked", reason)
	}
	waitFor(t, func() bool { return ref.calls.Load() >= 2 })
	c.signal(topicCell, "", "cell-after", "private")
	c.signal(topicSignal, "", "signal-after", "public")
	en, _ := wild.nextData()
	if en.Topic != tSignal {
		t.Errorf("wildcard stream saw %s after the narrowing, want only %s", en.Topic, tSignal)
	}
	wild.quiet(100 * time.Millisecond)
	if c.hub.Subscribers() != 1 {
		t.Errorf("%d streams open, want the wildcard one", c.hub.Subscribers())
	}
	_ = lucius
}

// TestHub_RightsChangedRefusals: what Refresh may answer, and what the stream
// does with it.
func TestHub_RightsChangedRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(c *cast, p *auth.Principal) (*auth.Principal, error)
		want   string // closed reason; "" = the stream stays
	}{
		{"credential gone", func(*cast, *auth.Principal) (*auth.Principal, error) { return nil, auth.ErrRevoked }, "credential_revoked"},
		{"another user's principal", func(c *cast, p *auth.Principal) (*auth.Principal, error) {
			return c.narrowed(t, c.dick, p, scopeView), nil
		}, "permission_revoked"},
		{"no longer signed in", func(c *cast, p *auth.Principal) (*auth.Principal, error) {
			q := *p
			q.Perms = nil
			return &q, nil
		}, "permission_revoked"},
		{"nil principal", func(*cast, *auth.Principal) (*auth.Principal, error) { return nil, nil }, "permission_revoked"},
		{"unchanged rights", func(_ *cast, p *auth.Principal) (*auth.Principal, error) { return p, nil }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &refresher{}
			c := newCast(t, hubOptions{deps: func(d *realtime.Deps) { d.Refresh = ref.hook }})
			ref.set(func(_ context.Context, p *auth.Principal) (*auth.Principal, error) { return tc.answer(c, p) })
			rd := c.open(t, "lucius", "/events")
			rd.expectOpened()
			c.rbacChanged(2)
			if tc.want == "" {
				waitFor(t, func() bool { return ref.calls.Load() >= 1 })
				c.signal(topicSignal, "", "still", "public")
				if en, _ := rd.nextData(); en.Topic != tSignal {
					t.Errorf("saw %+v", en)
				}
				return
			}
			if reason := rd.drainUntilClosed(); reason != tc.want {
				t.Errorf("closed with %q, want %q", reason, tc.want)
			}
		})
	}
}

// TestHub_RightsChangedWithoutRefreshEndsStreams: with no Refresh hook the hub
// cannot know what a stream may still see, so a generation bump ends every
// stream with permission_revoked and the client reconnects (fail closed).
func TestHub_RightsChangedWithoutRefreshEndsStreams(t *testing.T) {
	c := newCast(t, hubOptions{})
	a := c.open(t, "lucius", "/events")
	a.expectOpened()
	b := c.open(t, "dick", "/events?topic="+tSignal)
	b.expectOpened()
	c.rbacChanged(2)
	for _, rd := range []*reader{a, b} {
		if reason := rd.drainUntilClosed(); reason != "permission_revoked" {
			t.Errorf("closed with %q", reason)
		}
	}
	// The reconnect works and sees the rights as they stand now.
	c.open(t, "lucius", "/events").expectOpened()
}

// TestHub_ResetOnRestartNonce: an id from another process (a restart) is not
// resumed; the stream says reset{restart} first, replays nothing and carries on
// live. A reset is a normal frame, not an error status.
func TestHub_ResetOnRestartNonce(t *testing.T) {
	c := newCast(t, hubOptions{})
	w := c.witness(t)
	c.publishLamps(1, 3)
	lamps(t, w, 3)
	rd := c.open(t, "dick", "/events?topic="+tSignal, "Last-Event-ID", "0123456789abcdef-1")
	rd.expectOpened()
	expectReset(t, rd, "restart")
	c.publishLamps(4, 4)
	names, _ := lamps(t, rd, 1)
	if names[0] != "lamp-4" {
		t.Errorf("after the reset the stream saw %v, want live lamp-4 only", names)
	}
}

// expectReset reads the next frame and asserts it is core.stream.reset.v1 with
// reason and no id.
func expectReset(t testing.TB, rd *reader, reason string) {
	t.Helper()
	en, id := rd.nextData()
	var d struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(en.Data, &d)
	if en.Topic != "core.stream.reset.v1" || d.Reason != reason || id != "" {
		t.Fatalf("frame = %s %s id %q, want core.stream.reset.v1 {%s} with no id", en.Topic, en.Data, id, reason)
	}
}

// TestHub_ResetOnGap: an id the ring cannot resume from (malformed, older than
// the oldest entry, or past the newest) is a reset{gap}; the edge of the window
// resumes cleanly.
func TestHub_ResetOnGap(t *testing.T) {
	c := newCast(t, hubOptions{opts: sizes(4, 128)})
	nonce := c.nonceViaProbe(t) // seq 1
	w := c.witness(t)
	for i := 2; i <= 10; i++ { // seqs 2..10; the ring of 4 holds 7..10
		c.signal(topicSignal, "", fmt.Sprintf("lamp-%d", i), "public")
	}
	for range 9 {
		w.nextData()
	}
	id := func(n string) string { return nonce + "-" + n }
	for _, tc := range []struct {
		name, last string
		reset      string
		replay     []string
	}{
		{"older than the ring", id("2"), "gap", nil},
		{"one before the oldest minus one", id("5"), "gap", nil},
		{"the edge of the window", id("6"), "", []string{"lamp-7", "lamp-8", "lamp-9", "lamp-10"}},
		{"in the window", id("8"), "", []string{"lamp-9", "lamp-10"}},
		{"at the newest", id("10"), "", nil},
		{"past the newest", id("11"), "gap", nil},
		{"far past the newest", id("99999999"), "gap", nil},
		{"zero is older than the ring", id("0"), "gap", nil},
		{"no seq", nonce + "-", "gap", nil},
		{"no dash", "garbage", "gap", nil},
		{"not a number", id("12abc"), "gap", nil},
		{"negative", id("-5"), "gap", nil},
		{"an overflowing number", id("99999999999999999999999"), "gap", nil},
		{"longer than any id", nonce + "-" + strings.Repeat("1", 30), "gap", nil},
		{"another process", "ffffffffffffffff-8", "restart", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rd := c.open(t, "dick", "/events?topic="+tSignal, "Last-Event-ID", tc.last)
			rd.expectOpened()
			if tc.reset != "" {
				expectReset(t, rd, tc.reset)
			}
			if len(tc.replay) > 0 {
				got, _ := lamps(t, rd, len(tc.replay))
				if !slices.Equal(got, tc.replay) {
					t.Errorf("replay = %v, want %v", got, tc.replay)
				}
			}
			rd.quiet(60 * time.Millisecond) // no data frame follows (a reset, if owed, was read above)
		})
	}
}

// TestHub_ErrorsBeforeSubscriptions: a handler asked before the kernel has
// registered the hub's subscriptions, or after Close, answers 503 and opens no
// stream; NewHub refuses a hub that could not revalidate or read the catalog.
func TestHub_ErrorsBeforeSubscriptions(t *testing.T) {
	e := newAuth(t)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := realtime.NewHub(realtime.Options{}, realtime.Deps{Auth: e.svc, Catalog: bus.Catalog()})
	if err != nil {
		t.Fatal(err)
	}
	srv, ro := e.serve(t, hub.Handler())
	ro.set("lucius", e.session(e.user("lucius", "batcave.keeper")))
	_, resp := open(t, srv, "lucius", "/events")
	if p := readProblem(t, resp); resp.StatusCode != 503 || p.Code != "unavailable" || resp.Header.Get("Retry-After") != "5" {
		t.Errorf("before Subscriptions: %d %+v Retry-After %q", resp.StatusCode, p, resp.Header.Get("Retry-After"))
	}
	if _, err := hub.Subscriptions(); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Subscriptions(); err == nil {
		t.Error("Subscriptions may be called once, and a second call was accepted")
	}
	ctx := context.Background()
	if err := hub.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := hub.Close(ctx); err != nil {
		t.Errorf("a second Close: %v", err)
	}
	_, resp = open(t, srv, "lucius", "/events")
	if p := readProblem(t, resp); resp.StatusCode != 503 || p.Code != "unavailable" {
		t.Errorf("after Close: %d %+v", resp.StatusCode, p)
	}

	for name, tc := range map[string]struct {
		o realtime.Options
		d realtime.Deps
	}{
		"no Auth":           {d: realtime.Deps{Catalog: bus.Catalog()}},
		"no Catalog":        {d: realtime.Deps{Auth: e.svc}},
		"negative ring":     {o: realtime.Options{RingSize: -1}, d: realtime.Deps{Auth: e.svc, Catalog: bus.Catalog()}},
		"oversized buffer":  {o: realtime.Options{ClientBuffer: 1 << 20}, d: realtime.Deps{Auth: e.svc, Catalog: bus.Catalog()}},
		"negative duration": {o: realtime.Options{Heartbeat: -time.Second}, d: realtime.Deps{Auth: e.svc, Catalog: bus.Catalog()}},
		"huge payload cap":  {o: realtime.Options{MaxPayload: 1 << 30}, d: realtime.Deps{Auth: e.svc, Catalog: bus.Catalog()}},
	} {
		if _, err := realtime.NewHub(tc.o, tc.d); err == nil {
			t.Errorf("NewHub(%s) = nil error", name)
		}
	}
	_ = errors.New
}
