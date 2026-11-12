// file: core/realtime/hub_auth_test.go
// version: 1.0.2
// guid: 7f3a1d52-0e84-4c36-b9a7-2d1c5e6f8a40
// last-edited: 2026-11-12

package realtime_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// refused asserts that opening path as who is refused with status and code,
// as problem+json and not as a stream, and returns the response.
func (c *cast) refused(t testing.TB, who, path string, status int, code string, hdr ...string) *http.Response {
	t.Helper()
	rd, resp := open(t, c.srv, who, path, hdr...)
	_ = rd
	if resp.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d", who, path, resp.StatusCode, status)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") || !strings.Contains(ct, "json") {
		t.Errorf("%s %s: Content-Type %q on a refusal", who, path, ct)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(body), "data:") || strings.Contains(string(body), "core.stream") {
		t.Errorf("%s %s: stream bytes in a refusal body: %s", who, path, body)
	}
	var p problem
	if err := jsonUnmarshal(body, &p); err != nil || p.Code != code {
		t.Errorf("%s %s: code %q (%v), want %q; body %s", who, path, p.Code, err, code, body)
	}
	return resp
}

// TestHub_RefusalsAreStatusesBeforeStream: every refusal is a problem+json
// status, with Retry-After where it applies, and no refusal writes a byte of
// text/event-stream (05-realtime §4.1, §6).
func TestHub_RefusalsAreStatusesBeforeStream(t *testing.T) {
	c := newCast(t, hubOptions{opts: realtime.Options{MaxClients: 100, RetryAfter: 7 * time.Second}})
	c.ro.set("lucius-key", func() *auth.Principal { p, _ := c.key(c.lucius, scopeView); return p }())
	for _, tc := range []struct {
		name, who, path string
		status          int
		code            string
	}{
		{"no principal", "nobody", "/events", 401, "authentication_required"},
		{"a signed-in user holding nothing", "oswald", "/events", 403, "permission_denied"},
		{"a confined key", "robin-key", "/events", 403, "permission_denied"},
		{"unknown topic", "dick", "/events?topic=batcave.nothing.here.v1", 400, "unknown_topic"},
		{"a topic that is not a realtime topic", "dick", "/events?topic=core.principal.invalidated.v1", 400, "unknown_topic"},
		{"a wildcard of a module with no topic", "dick", "/events?topic=nowhere.*", 400, "unknown_topic"},
		{"unknown exclude", "dick", "/events?exclude=batcave.nothing.here.v1", 400, "unknown_topic"},
		{"explicit topic not permitted", "dick", "/events?topic=" + tCell, 403, "topic_not_permitted"},
		{"explicit Arkham topic not permitted", "lucius", "/events?topic=" + tArk, 403, "topic_not_permitted"},
		{"too many topic patterns", "dick", "/events?" + strings.Repeat("topic=*&", 33), 400, "invalid_query"},
		{"a pattern too long", "dick", "/events?topic=" + strings.Repeat("a", 129), 400, "invalid_query"},
		{"an empty pattern", "dick", "/events?topic=", 400, "invalid_query"},
		{"key repeated", "dick", "/events?key=a&key=b", 400, "invalid_query"},
		{"key too long", "dick", "/events?key=" + strings.Repeat("k", 257), 400, "invalid_query"},
		{"last_event_id too long", "dick", "/events?last_event_id=" + strings.Repeat("9", 129), 400, "invalid_query"},
		{"query string too long", "dick", "/events?x=" + strings.Repeat("y", 9000), 400, "invalid_query"},
		{"malformed query", "dick", "/events?topic=%zz", 400, "invalid_query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.who == "robin-key" {
				p, _ := c.key(c.robin, scopeRelay)
				c.ro.set("robin-key", p)
			}
			c.refused(t, tc.who, tc.path, tc.status, tc.code)
		})
	}
	t.Run("a Last-Event-ID header too long", func(t *testing.T) {
		c.refused(t, "dick", "/events", 400, "invalid_query", "Last-Event-ID", strings.Repeat("1", 129))
	})
}

// TestHub_ConfinedAndEmptyPrincipalsNeverStream: a principal whose checker
// does not satisfy AnyAuthenticated (a Confined key holding only a worker
// scope, a signed-in user holding no scope, a nil checker) is refused 403 with
// no stream, however its request is phrased. "A principal exists" is not
// enough (05-rbac §4.10).
func TestHub_ConfinedAndEmptyPrincipalsNeverStream(t *testing.T) {
	c := newCast(t, hubOptions{})
	key, _ := c.key(c.robin, scopeRelay)
	if !key.Perms.Confined() {
		t.Fatal("fixture: the relay key is not confined")
	}
	nilPerms := *c.ro.get("dick")
	nilPerms.Perms = nil
	c.ro.set("robin-key", key)
	c.ro.set("nil-perms", &nilPerms)
	for _, who := range []string{"robin-key", "oswald", "nil-perms"} {
		// The last path is over the 8 KiB query bound: a caller who may not
		// stream is told so (403) before the query is looked at (it would be a
		// 400), so nothing about its shape is probed by a refused caller.
		for _, path := range []string{"/events", "/events?topic=" + tRetired, "/events?topic=*&exclude=" + tCell, "/events?key=" + strings.Repeat("k", 9000)} {
			c.refused(t, who, path, 403, "permission_denied")
		}
	}
	if n := c.hub.Subscribers(); n != 0 {
		t.Errorf("%d streams open after refusals", n)
	}
}

// TestHub_CapsGlobalAndBucket: MaxClients is a global 503 and MaxPerBucket a
// per-user 429, both with Retry-After; a user's sessions and API keys are
// separate buckets, one user's cap never touches another's, closing a stream
// frees its place, and auth-disabled principals take no bucket (FE-9).
func TestHub_CapsGlobalAndBucket(t *testing.T) {
	c := newCast(t, hubOptions{opts: realtime.Options{MaxClients: 5, MaxPerBucket: 2, RetryAfter: 7 * time.Second}})
	keyP, _ := c.key(c.lucius, scopeView)
	c.ro.set("lucius-key", keyP)
	opened := func(who string) *reader {
		rd := c.open(t, who, "/events")
		rd.expectOpened()
		return rd
	}
	l1, _ := opened("lucius"), opened("lucius")
	resp := c.refused(t, "lucius", "/events", 429, "throttled")
	if ra := resp.Header.Get("Retry-After"); ra != "7" {
		t.Errorf("429 Retry-After = %q, want 7", ra)
	}
	// The same user's API keys are another bucket; another user is another bucket.
	opened("lucius-key")
	opened("dick")
	opened("barbara")
	// Five streams are open: the global cap answers 503, whoever asks.
	resp = c.refused(t, "bruce", "/events", 503, "unavailable")
	if ra := resp.Header.Get("Retry-After"); ra != "7" {
		t.Errorf("503 Retry-After = %q, want 7", ra)
	}
	// A stream that ends frees its place at once.
	l1.close()
	waitFor(t, func() bool { return c.hub.Subscribers() == 4 })
	opened("bruce")
}

// TestHub_AuthDisabledSharesOneBucketBoundedByMaxClients: the auth-off
// principal has no user, so MaxPerBucket does not apply and only MaxClients
// bounds it.
func TestHub_AuthDisabledSharesOneBucketBoundedByMaxClients(t *testing.T) {
	c := newCast(t, hubOptions{opts: realtime.Options{MaxClients: 3, MaxPerBucket: 1}})
	c.ro.set("off", &auth.Principal{
		Method: auth.MethodAuthDisabled,
		Perms:  c.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true}),
	})
	for range 3 {
		c.open(t, "off", "/events").expectOpened()
	}
	c.refused(t, "off", "/events", 503, "unavailable")
}

// waitFor polls cond for up to five seconds.
func waitFor(t testing.TB, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// TestHub_StreamClosesOnCredentialRevoke: core.principal.invalidated.v1 ends
// the streams of the user it names (all of them, or the one credential),
// writes closed{credential_revoked} and no more, and leaves everyone else's
// streams open (05-realtime §4.6).
func TestHub_StreamClosesOnCredentialRevoke(t *testing.T) {
	c := newCast(t, hubOptions{})
	luciusSession2 := c.session(c.lucius)
	c.ro.set("lucius-2", luciusSession2)
	keyP, k := c.key(c.lucius, scopeView)
	c.ro.set("lucius-key", keyP)

	a := c.open(t, "lucius", "/events")
	a.expectOpened()
	b := c.open(t, "lucius-2", "/events")
	b.expectOpened()
	kd := c.open(t, "lucius-key", "/events")
	kd.expectOpened()
	d := c.open(t, "dick", "/events")
	d.expectOpened()
	invalidate := func(user, cred string) {
		events.Publish(c.cor, context.Background(), topicInval, "", Invalidated{UserID: user, CredentialID: cred, Reason: "test"})
	}

	// One credential: only the stream built on it ends.
	invalidate(string(c.lucius.ID), k.ID)
	if reason := kd.drainUntilClosed(); reason != "credential_revoked" {
		t.Errorf("key stream closed with %q", reason)
	}
	c.signal(topicSignal, "", "after-key", "public")
	for _, rd := range []*reader{a, b, d} {
		if en, _ := rd.nextData(); en.Topic != tSignal {
			t.Errorf("a stream that should have survived saw %+v", en)
		}
	}
	// The whole user: every remaining stream of lucius ends, dick's does not.
	invalidate(string(c.lucius.ID), "")
	for _, rd := range []*reader{a, b} {
		if reason := rd.drainUntilClosed(); reason != "credential_revoked" {
			t.Errorf("session stream closed with %q", reason)
		}
	}
	c.signal(topicSignal, "", "after-user", "public")
	if en, _ := d.nextData(); en.Topic != tSignal {
		t.Errorf("dick's stream saw %+v", en)
	}
	// An invalidation naming nobody, or unreadable, ends nothing.
	invalidate("", "")
	invalidate("u-nobody", "")
	c.signal(topicSignal, "", "still-here", "public")
	if en, _ := d.nextData(); !strings.Contains(string(en.Data), "still-here") {
		t.Errorf("dick's stream saw %+v", en)
	}
}

// TestHub_RevalidateOnHeartbeat: with no invalidation event at all, every
// heartbeat asks auth whether the credential still stands, so a revoked
// session, a revoked key and a disabled user each end their stream within a
// heartbeat, on a quiet stream too.
func TestHub_RevalidateOnHeartbeat(t *testing.T) {
	c := newCast(t, hubOptions{opts: hubHeartbeat(60 * time.Millisecond), skipCtl: true})
	ctx := context.Background()
	keyP, k := c.key(c.lucius, scopeView)
	c.ro.set("lucius-key", keyP)
	dickKey, _ := c.key(c.dick, scopeView)
	c.ro.set("dick-key", dickKey)
	c.ro.set("barbara-2", c.session(c.barbara))

	sess := c.open(t, "dick", "/events")
	sess.expectOpened()
	key := c.open(t, "lucius-key", "/events")
	key.expectOpened()
	disabled := c.open(t, "barbara-2", "/events")
	disabled.expectOpened()
	steady := c.open(t, "bruce", "/events")
	steady.expectOpened()
	time.Sleep(200 * time.Millisecond) // a few heartbeats: every credential stands
	for name, rd := range map[string]*reader{"session": sess, "key": key, "disabled": disabled, "steady": steady} {
		select {
		case _, ok := <-rd.frames:
			if !ok {
				t.Fatalf("%s stream ended while its credential stood", name)
			}
		default:
		}
	}

	if err := c.svc.RevokeSession(ctx, c.ro.get("dick"), c.ro.get("dick").CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := c.svc.RevokeAPIKey(ctx, c.ro.get("lucius"), k.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.svc.SetStatus(ctx, c.ro.get("bruce"), c.barbara.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	for name, rd := range map[string]*reader{"session": sess, "key": key, "disabled": disabled} {
		if reason := rd.drainUntilClosed(); reason != "credential_revoked" {
			t.Errorf("%s stream closed with %q, want credential_revoked", name, reason)
		}
	}
	// bruce's stream is untouched.
	c.signal(topicSignal, "", "still", "public")
	if en, _ := steady.nextData(); en.Topic != tSignal {
		t.Errorf("steady stream saw %+v", en)
	}
}

// TestHub_ClosedFrameReachesTheClientOverHTTP1AndHTTP2: an idle stream ended
// by Close (shutdown) or by an invalidation (credential_revoked) delivers its
// closed frame, over HTTP/1.1 and over HTTP/2, the production transport. A
// write deadline moved into the past resets an HTTP/2 stream before the frame
// is sent, so the cell fails if the end path ever interrupts an idle stream
// that way.
func TestHub_ClosedFrameReachesTheClientOverHTTP1AndHTTP2(t *testing.T) {
	for _, proto := range []string{"h1", "h2"} {
		for _, end := range []string{"shutdown", "credential_revoked"} {
			t.Run(proto+"/"+end, func(t *testing.T) {
				c := newCast(t, hubOptions{})
				srv, ro := c.serveProto(t, c.hub.Handler(), proto == "h2")
				ro.set("lucius", c.session(c.lucius))
				rd, resp := open(t, srv, "lucius", "/events")
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status %d", resp.StatusCode)
				}
				if want := map[string]int{"h1": 1, "h2": 2}[proto]; resp.ProtoMajor != want {
					t.Fatalf("negotiated HTTP/%d, want HTTP/%d", resp.ProtoMajor, want)
				}
				rd.expectOpened()
				if end == "shutdown" {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := c.hub.Close(ctx); err != nil {
						t.Fatalf("Close = %v", err)
					}
				} else {
					events.Publish(c.cor, context.Background(), topicInval, "", Invalidated{UserID: string(c.lucius.ID), Reason: "test"})
				}
				if got := rd.drainUntilClosed(); got != end {
					t.Errorf("closed frame reason %q, want %q", got, end)
				}
			})
		}
	}
}
