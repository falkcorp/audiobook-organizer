// file: core/kernel/identity_client_test.go
// version: 0.1.0
// guid: 5d1c7a92-3e84-4f06-a8b3-9c2e0f6d1b47
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only. F4-08 B4 follow-up: auth.Deps.Client is httpapi's
// resolver, proved over HTTP through a booted kernel behind a trusted
// HTTPS-terminating proxy.

const (
	// proxyPeer is the one trusted proxy the fixtures name in
	// core.http.trusted_proxies; strangerPeer is any other peer.
	proxyPeer    = "192.0.2.1"
	strangerPeer = "192.0.2.10"
)

// fixturePassword is the fixture user's password.
var fixturePassword = strings.Repeat("batmobile", 2)

// proxiedFx is a switch-on kernel that has been Run, so the perimeter (stage 0,
// which sets the trusted-proxy state the resolver reads) is installed: a boot
// alone never sets it, because core.http reaches the router at start step 7
// (supervisor.go), not at New. relay is the module route the fixtures read.
type proxiedFx struct {
	*mountFx
	plain string // the plain session cookie name, read off a direct login
}

// newProxiedFx boots the mount fixtures with core.http.trusted_proxies naming
// proxyPeer, runs the kernel against a recording listener, waits until it is
// listening (the perimeter is in force from then on) and stops it with the test.
// It creates one user, "lucius", holding relay-worker.
func newProxiedFx(t *testing.T) *proxiedFx {
	t.Helper()
	h, c, hits := mountConfig(t, true)
	rec := &recorder{}
	listening := make(chan http.Handler, 1)
	c.Notifier = notifier{rec}
	c.Exit = func(code int) { rec.add("exit:%d", code) }
	c.Listen = func(_ httpapi.ListenOptions, handler http.Handler, _ *slog.Logger) (Server, error) {
		listening <- handler
		return &fakeServer{rec: rec, stopped: make(chan struct{})}, nil
	}
	withCore(&c, "core.http.listen", "localhost:0", "core.http.trusted_proxies", `["`+proxyPeer+`"]`)
	k := h.mustBoot(c)
	ctx, cancel := context.WithCancel(t.Context())
	done := run(ctx, k)
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	var handler http.Handler
	select {
	case handler = <-listening:
	case err := <-done:
		t.Fatalf("Run ended before it listened: %v", err)
	}
	fx := &proxiedFx{mountFx: &mountFx{k: k, h: handler, hits: hits}}
	for ri := range k.Routes() {
		if ri.Method+" "+ri.Pattern == "GET /api/alfred/v1/relay" {
			fx.relay = ri.Pattern
		}
	}
	if fx.relay == "" {
		t.Fatalf("the module's route is not mounted: %v", routeInfos(k))
	}
	actor := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: &auth.User{ID: "u-alfred", Username: "alfred"}}
	if _, err := k.ident.svc.Auth.CreateUser(t.Context(), actor, auth.NewUser{
		Username: "lucius", Password: fixturePassword, Roles: []rbac.RoleName{"relay-worker"},
	}); err != nil {
		t.Fatal(err)
	}
	return fx
}

// from is the shape of a request: the TCP peer it comes from and whether that
// peer claims the client connection was HTTPS (X-Forwarded-Proto).
type from struct {
	peer     string
	forwards bool
}

func (f from) mutate(r *http.Request) {
	r.RemoteAddr = f.peer + ":4000"
	if f.forwards {
		r.Header.Set("X-Forwarded-Proto", "https")
	}
}

// login signs lucius in as a request of shape f and returns the one cookie it
// sets.
func (fx *proxiedFx) login(t *testing.T, f from) *http.Cookie {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": "lucius", "password": fixturePassword})
	if err != nil {
		t.Fatal(err)
	}
	rec := fx.do(http.MethodPost, "/api/core/v1/auth/login", func(r *http.Request) {
		f.mutate(r)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login from %+v = %d %s", f, rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login from %+v set %d cookies, want 1", f, len(cookies))
	}
	return cookies[0]
}

// relayAs reads the module's route as a request of shape f carrying c, and
// reports the status and whether the handler ran.
func (fx *proxiedFx) relayAs(f from, c *http.Cookie) (code int, reached bool) {
	before := fx.hits.Load()
	rec := fx.do(http.MethodGet, fx.relay, func(r *http.Request) {
		f.mutate(r)
		r.AddCookie(c)
	})
	return rec.Code, fx.hits.Load() != before
}

// TestIdentityClient_TrustedProxyKeepsAHostSession: auth.Deps.Client is
// httpapi's resolver. A request from a trusted proxy that says X-Forwarded-Proto
// https is HTTPS to stage 12 and to the login that set the cookie, so the
// session cookie is the Secure __Host- one and a replay of it the same way is
// served; any other shape of the same cookie is not admitted, and an untrusted
// peer's claim of HTTPS is never believed. With Client nil auth falls back to
// DirectClient, which believes no forwarding header, so stage 12 reads the plain
// name and refuses the __Host- cookie the login set (the Client: nil mutant).
func TestIdentityClient_TrustedProxyKeepsAHostSession(t *testing.T) {
	fx := newProxiedFx(t)
	proxied := from{peer: proxyPeer, forwards: true}
	stranger := from{peer: strangerPeer, forwards: true}

	// The plain name: a direct, unproxied login, whose cookie is neither Secure
	// nor __Host-.
	direct := fx.login(t, from{peer: strangerPeer})
	if strings.HasPrefix(direct.Name, "__Host-") || direct.Secure {
		t.Fatalf("a direct HTTP login set %q (Secure=%v), want the plain, non-Secure cookie", direct.Name, direct.Secure)
	}
	fx.plain = direct.Name

	t.Run("trusted proxy, X-Forwarded-Proto https: __Host- cookie, served", func(t *testing.T) {
		ck := fx.login(t, proxied)
		if ck.Name != "__Host-"+fx.plain || !ck.Secure || ck.Path != "/" || ck.Domain != "" {
			t.Fatalf("login through the proxy set %+v, want the Secure __Host-%s cookie on Path=/ with no Domain", ck, fx.plain)
		}
		if code, reached := fx.relayAs(proxied, ck); code != http.StatusOK || !reached {
			t.Fatalf("replay through the proxy = %d (handler reached: %v), want 200", code, reached)
		}
		// The same cookie is not admitted any other way.
		for name, f := range map[string]from{
			"from a peer that is not the proxy, claiming https": stranger,
			"through the proxy with no https claim":             {peer: proxyPeer},
			"from a peer that is not the proxy, no claim":       {peer: strangerPeer},
		} {
			if code, reached := fx.relayAs(f, ck); code != http.StatusUnauthorized || reached {
				t.Errorf("a __Host- cookie %s = %d (handler reached: %v), want 401", name, code, reached)
			}
		}
	})

	t.Run("untrusted peer claiming https: plain cookie, never __Host-", func(t *testing.T) {
		ck := fx.login(t, stranger)
		if ck.Name != fx.plain || ck.Secure {
			t.Fatalf("login from an untrusted peer claiming https set %+v, want the plain non-Secure cookie %q", ck, fx.plain)
		}
		// The plain session is served as the plain connection it is, and the same
		// token under the __Host- name is not read at all: the untrusted peer's
		// claim of HTTPS is not believed on the way back either.
		if code, reached := fx.relayAs(stranger, ck); code != http.StatusOK || !reached {
			t.Fatalf("the plain cookie replayed from the same untrusted peer = %d (handler reached: %v), want 200", code, reached)
		}
		forged := &http.Cookie{Name: "__Host-" + fx.plain, Value: ck.Value}
		if code, reached := fx.relayAs(stranger, forged); code != http.StatusUnauthorized || reached {
			t.Errorf("a valid session token under the __Host- name from an untrusted peer = %d (handler reached: %v), want 401", code, reached)
		}
	})
}
