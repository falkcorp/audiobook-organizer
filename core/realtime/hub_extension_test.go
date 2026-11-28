// file: core/realtime/hub_extension_test.go
// version: 1.0.0
// guid: 7d3f5b1a-2c94-4e86-a0b7-5e19c8d4f362
// last-edited: 2026-11-28

package realtime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// cfAccessHeader marks a request as carrying a Cloudflare Access token.
const cfAccessHeader = "Cf-Access-Jwt-Assertion"

// cfAccessLike is an identity extension shaped as the app's ext:cfaccess
// (internal/cfaccess): the same method spec, and an assertion that carries the
// verified token's own end (ExpiresAt) and no session or key. The user is set
// after the service is built, because the store is empty until then.
type cfAccessLike struct {
	mu      sync.Mutex
	user    auth.UserID
	expires time.Time
	issued  time.Time
}

func (c *cfAccessLike) Name() string { return "cfaccess" }

// Authenticate answers only a request that carries the Access header, as the
// app's extension answers only one carrying a token: every other request (the
// sessions the test mints) goes on to the next method.
func (c *cfAccessLike) Authenticate(r *http.Request) (*auth.Assertion, error) {
	if r.Header.Get(cfAccessHeader) == "" {
		return nil, auth.ErrNoCredential
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return &auth.Assertion{User: c.user, CredentialID: "cfaccess:oracle", AuthTime: c.issued, ExpiresAt: c.expires}, nil
}

func (c *cfAccessLike) set(u auth.UserID, issued, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.user, c.issued, c.expires = u, issued, expires
}

func cfAccessExtension(c *cfAccessLike) auth.Extension {
	return auth.Extension{
		Authenticator: c,
		Order:         150, // OrderCFAccess: after the key authenticator, before the session
		Spec: auth.MethodSpec{
			Method: "ext:cfaccess", Interactive: true, MayChangeCredentials: true, OwnerVerifier: true,
		},
		OwnerSet: func() bool { return false },
	}
}

// closesWithin is drainUntilClosed with a deadline: a stream that is wrongly
// kept open fails the test instead of hanging it.
func closesWithin(t *testing.T, rd *reader, d time.Duration) string {
	t.Helper()
	got := make(chan string, 1)
	go func() { got <- rd.drainUntilClosed() }()
	select {
	case reason := <-got:
		return reason
	case <-time.After(d):
		t.Fatalf("the stream was still open %v after its credential ended", d)
	}
	return ""
}

// TestHub_CFAccessPrincipalSurvivesTheHeartbeat: a Cloudflare Access sign-in
// has no session and no key, so the hub's heartbeat Revalidate (and the Refresh
// beside it) has no credential record to find. Over the real auth.Service and
// the real stage 12 binding, such a stream stands on every heartbeat while its
// token does and still receives what its rights allow; it ends when the
// token's own end passes, and when its user is disabled.
func TestHub_CFAccessPrincipalSurvivesTheHeartbeat(t *testing.T) {
	ext := &cfAccessLike{}
	e := newEnv(t, hubOptions{opts: hubHeartbeat(60 * time.Millisecond), skipCtl: true}, cfAccessExtension(ext))
	srv, ro := e.serve(t, e.hub.Handler())
	bruce := e.user("bruce", rbac.RoleAdmin)
	ro.set("bruce", e.session(bruce))

	bind := func(who *auth.User, ttl time.Duration) *auth.Principal {
		t.Helper()
		ext.set(who.ID, e.clock(), e.clock().Add(ttl))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(cfAccessHeader, "verified-by-cloudflare")
		p := e.bound(r)
		if p.Method != "ext:cfaccess" || p.User == nil || p.User.ID != who.ID {
			t.Fatalf("stage 12 bound %+v, want an ext:cfaccess principal for %s", p, who.ID)
		}
		return p
	}

	t.Run("stands while the token does", func(t *testing.T) {
		dick := e.user("dick", "batcave.watcher")
		ro.set("dick-cf", bind(dick, time.Hour))
		rd, resp := open(t, srv, "dick-cf", "/events")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("open: status %d", resp.StatusCode)
		}
		rd.expectOpened()
		time.Sleep(300 * time.Millisecond) // five heartbeats
		select {
		case _, ok := <-rd.frames:
			if !ok {
				t.Fatal("the stream ended on a heartbeat while its Access token stood")
			}
		default:
		}
		e.signal(topicSignal, "", "still-here", "public")
		if en, _ := rd.nextData(); en.Topic != tSignal {
			t.Errorf("the surviving stream saw %+v", en)
		}
	})

	t.Run("ends at the token's own end", func(t *testing.T) {
		barbara := e.user("barbara", "batcave.watcher")
		ro.set("barbara-cf", bind(barbara, time.Minute))
		rd, resp := open(t, srv, "barbara-cf", "/events")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("open: status %d", resp.StatusCode)
		}
		rd.expectOpened()
		e.advance(2 * time.Minute)
		if reason := closesWithin(t, rd, 5*time.Second); reason != "credential_revoked" {
			t.Errorf("the stream closed with %q after its token ended, want credential_revoked", reason)
		}
	})

	t.Run("ends when its user is disabled", func(t *testing.T) {
		lucius := e.user("lucius", "batcave.keeper")
		ro.set("lucius-cf", bind(lucius, time.Hour))
		rd, resp := open(t, srv, "lucius-cf", "/events")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("open: status %d", resp.StatusCode)
		}
		rd.expectOpened()
		if err := e.svc.SetStatus(context.Background(), ro.get("bruce"), lucius.ID, auth.StatusDisabled); err != nil {
			t.Fatal(err)
		}
		if reason := closesWithin(t, rd, 5*time.Second); reason != "credential_revoked" {
			t.Errorf("the stream closed with %q after its user was disabled, want credential_revoked", reason)
		}
	})
}
