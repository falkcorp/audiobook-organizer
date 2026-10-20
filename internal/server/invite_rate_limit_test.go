// file: internal/server/invite_rate_limit_test.go
// version: 1.0.0
// guid: a4acf753-a843-41d6-8769-4d28540be7e1
// last-edited: 2026-10-19

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// inviteLimitHarness drives a limiter built exactly as wireAuthRoutes builds it,
// in front of a handler that answers 200 and echoes what it read of the body.
type inviteLimitHarness struct {
	t      *testing.T
	engine *gin.Engine
	limits *inviteRateLimits
}

// newInviteLimitHarness builds one harness per case so no bucket is shared.
// A request carrying the X-Test-Verified header stands in for one whose Access
// assertion CloudflareAccessAuth verified.
func newInviteLimitHarness(t *testing.T, localPeer bool) *inviteLimitHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	limits := newInviteRateLimits(ctx)
	limits.isLocalPeer = func(string) bool { return localPeer }

	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-Verified") != "" {
			servermiddleware.MarkCFAccessVerified(c)
		}
	})
	r.GET("/invite/:token", limits.middleware(inviteTokenFromParam), func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.POST("/accept", limits.middleware(inviteTokenFromBody), func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, "%d", len(b))
	})
	return &inviteLimitHarness{t: t, engine: r, limits: limits}
}

// peerIP returns the i-th distinct TEST-NET-1 address (1..254).
func peerIP(i int) string { return fmt.Sprintf("192.0.2.%d", i%254+1) }

// get sends GET /invite/<token> from peer with the given extra headers.
func (h *inviteLimitHarness) get(token, peer string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/invite/"+token, nil)
	req.RemoteAddr = peer + ":4000"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	return w
}

func (h *inviteLimitHarness) post(body, peer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/accept", strings.NewReader(body))
	req.RemoteAddr = peer + ":4000"
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	return w
}

func inviteTestToken(i int) string { return fmt.Sprintf("%064d", i) }

// countAllowedUntilLimited calls send until it is answered 429 or max calls
// were made, and returns how many calls succeeded before the first 429
// (limited reports whether a 429 was seen). Rate limiters refill with real
// time, so the tests assert "no more than burst + a small slack", never an
// exact count.
func countAllowedUntilLimited(t *testing.T, max int, send func(i int) *httptest.ResponseRecorder) (allowed int, limited bool, last *httptest.ResponseRecorder) {
	t.Helper()
	for i := 0; i < max; i++ {
		w := send(i)
		switch w.Code {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			return allowed, true, w
		default:
			t.Fatalf("request %d: unexpected status %d: %s", i, w.Code, w.Body.String())
		}
	}
	return allowed, false, nil
}

func TestInviteRateLimit_PerToken(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	token := inviteTestToken(1)
	// Every request comes from a different client, so only the token layer can trip.
	allowed, limited, _ := countAllowedUntilLimited(t, inviteTokenRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(token, peerIP(i), nil)
	})
	if !limited {
		t.Fatalf("token never limited after %d requests", allowed)
	}
	if allowed < inviteTokenRateBurst || allowed > inviteTokenRateBurst+2 {
		t.Fatalf("token allowed %d requests before limiting, want about the burst of %d", allowed, inviteTokenRateBurst)
	}
	// The same token stays refused from a client that never used it.
	if w := h.get(token, "198.51.100.77", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("limited token from a fresh client: %d, want 429", w.Code)
	}
	// A different token still works.
	if w := h.get(inviteTestToken(2), "198.51.100.78", nil); w.Code != http.StatusOK {
		t.Fatalf("a different token while the first is limited: %d, want 200", w.Code)
	}
}

func TestInviteRateLimit_PerToken_PostBody(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	body := func(tok string) string { return `{"token":"` + tok + `","password":"correct-horse-1"}` }
	allowed, limited, _ := countAllowedUntilLimited(t, inviteTokenRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.post(body(inviteTestToken(1)), peerIP(i))
	})
	if !limited || allowed < inviteTokenRateBurst || allowed > inviteTokenRateBurst+2 {
		t.Fatalf("POST token allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteTokenRateBurst)
	}
	if w := h.post(body(inviteTestToken(2)), "198.51.100.90"); w.Code != http.StatusOK {
		t.Fatalf("a different POST token: %d, want 200", w.Code)
	}
	// A GET for the limited token shares the bucket with the POSTs.
	if w := h.get(inviteTestToken(1), "198.51.100.91", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("GET of a token its POSTs exhausted: %d, want 429", w.Code)
	}
}

// TestInviteRateLimit_PostBodyReachesHandlerIntact proves peeking the token
// does not consume or truncate the body the real handler parses.
func TestInviteRateLimit_PostBodyReachesHandlerIntact(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"normal", `{"token":"` + inviteTestToken(3) + `","password":"correct-horse-1"}`},
		{"larger than the peek window", `{"token":"` + inviteTestToken(3) + `","password":"` + strings.Repeat("p", inviteBodyPeekLimit*2) + `"}`},
		{"malformed", `{"token":`},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInviteLimitHarness(t, false)
			w := h.post(tt.body, peerIP(1))
			if w.Code != http.StatusOK {
				t.Fatalf("status %d", w.Code)
			}
			if got, want := w.Body.String(), fmt.Sprint(len(tt.body)); got != want {
				t.Fatalf("handler read %s bytes, want %s", got, want)
			}
		})
	}
}

func TestInviteRateLimit_Global(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	// A new token AND a new client every time: only the global ceiling can trip.
	allowed, limited, _ := countAllowedUntilLimited(t, inviteGlobalRateBurst+30, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(1000+i), fmt.Sprintf("198.51.100.%d", i%254+1), map[string]string{"CF-Connecting-IP": fmt.Sprintf("203.0.113.%d", i%254+1)})
	})
	if !limited {
		t.Fatalf("global ceiling never tripped after %d requests", allowed)
	}
	if allowed < inviteGlobalRateBurst || allowed > inviteGlobalRateBurst+10 {
		t.Fatalf("global allowed %d requests, want about the burst of %d", allowed, inviteGlobalRateBurst)
	}
	// Even a never-seen token from a never-seen client is refused.
	if w := h.get(inviteTestToken(9999), "203.0.113.250", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("fresh token and client past the global ceiling: %d, want 429", w.Code)
	}
}

// TestInviteRateLimit_ClientKey covers which address buckets a client.
func TestInviteRateLimit_ClientKey(t *testing.T) {
	tests := []struct {
		name      string
		localPeer bool
		verified  bool
		// spoofed means each request carries a different CF-Connecting-IP.
		// wantShared is true when the requests must still land in ONE client
		// bucket (the tunnel address), false when each CF-Connecting-IP gets
		// its own bucket.
		wantShared bool
	}{
		{"no verified Access: CF-Connecting-IP ignored", true, false, true},
		{"verified Access but peer is not this host: ignored", false, true, true},
		{"neither verified nor local: ignored", false, false, true},
		{"verified Access through the local tunnel: honored", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInviteLimitHarness(t, tt.localPeer)
			hdr := func(i int) map[string]string {
				m := map[string]string{"CF-Connecting-IP": fmt.Sprintf("203.0.113.%d", i%254+1)}
				if tt.verified {
					m["X-Test-Verified"] = "1"
				}
				return m
			}
			// One tunnel peer, a new token and a new CF-Connecting-IP each time,
			// so only the client layer can trip.
			allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
				return h.get(inviteTestToken(i), "192.0.2.1", hdr(i))
			})
			if tt.wantShared {
				if !limited || allowed < inviteAuthRateBurst || allowed > inviteAuthRateBurst+2 {
					t.Fatalf("spoofed CF-Connecting-IP got fresh buckets: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteAuthRateBurst)
				}
				return
			}
			if limited {
				t.Fatalf("distinct verified CF-Connecting-IP values shared a bucket: limited after %d", allowed)
			}
			// Exhaust ONE verified client; another verified client is unaffected.
			one := map[string]string{"CF-Connecting-IP": "203.0.113.200", "X-Test-Verified": "1"}
			_, limited, _ = countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
				return h.get(inviteTestToken(500+i), "192.0.2.1", one)
			})
			if !limited {
				t.Fatal("a single verified client was never limited")
			}
			other := map[string]string{"CF-Connecting-IP": "203.0.113.201", "X-Test-Verified": "1"}
			if w := h.get(inviteTestToken(900), "192.0.2.1", other); w.Code != http.StatusOK {
				t.Fatalf("a second verified client while the first is limited: %d, want 200", w.Code)
			}
		})
	}
}

// TestInviteRateLimit_ClientKey_InvalidHeader: a verified request whose
// CF-Connecting-IP is not an address falls back to the peer address rather
// than keying on attacker-chosen text.
func TestInviteRateLimit_ClientKey_InvalidHeader(t *testing.T) {
	h := newInviteLimitHarness(t, true)
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), "192.0.2.1", map[string]string{"X-Test-Verified": "1", "CF-Connecting-IP": fmt.Sprintf("not-an-ip-%d", i)})
	})
	if !limited || allowed > inviteAuthRateBurst+2 {
		t.Fatalf("invalid CF-Connecting-IP got fresh buckets: allowed %d (limited=%v)", allowed, limited)
	}
}

// TestInviteRateLimit_IdenticalRefusals proves a caller cannot tell which
// layer refused it: status, headers that matter and body are byte-identical.
func TestInviteRateLimit_IdenticalRefusals(t *testing.T) {
	trip := map[string]func(h *inviteLimitHarness) *httptest.ResponseRecorder{
		"per token": func(h *inviteLimitHarness) *httptest.ResponseRecorder {
			_, limited, w := countAllowedUntilLimited(t, inviteTokenRateBurst+10, func(i int) *httptest.ResponseRecorder {
				return h.get(inviteTestToken(1), peerIP(i), nil)
			})
			if !limited {
				t.Fatal("token layer did not trip")
			}
			return w
		},
		"per client": func(h *inviteLimitHarness) *httptest.ResponseRecorder {
			_, limited, w := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
				return h.get(inviteTestToken(i), "192.0.2.1", nil)
			})
			if !limited {
				t.Fatal("client layer did not trip")
			}
			return w
		},
		"global": func(h *inviteLimitHarness) *httptest.ResponseRecorder {
			_, limited, w := countAllowedUntilLimited(t, inviteGlobalRateBurst+30, func(i int) *httptest.ResponseRecorder {
				return h.get(inviteTestToken(2000+i), fmt.Sprintf("198.51.100.%d", i%254+1), nil)
			})
			if !limited {
				t.Fatal("global layer did not trip")
			}
			return w
		},
	}
	type refusal struct {
		code        int
		body, ctype string
	}
	got := map[string]refusal{}
	for name, f := range trip {
		w := f(newInviteLimitHarness(t, false))
		got[name] = refusal{w.Code, w.Body.String(), w.Header().Get("Content-Type")}
	}
	ref := got["per client"]
	if ref.code != http.StatusTooManyRequests || !strings.Contains(ref.body, "TOO_MANY_REQUESTS") {
		t.Fatalf("refusal = %+v, want a 429 TOO_MANY_REQUESTS body", ref)
	}
	for name, r := range got {
		if r != ref {
			t.Fatalf("%s refusal %+v differs from %+v", name, r, ref)
		}
	}
}

func TestInviteTokenKey(t *testing.T) {
	tok := inviteTestToken(7)
	k := inviteTokenKey(tok)
	if k == "" || strings.Contains(k, tok) || strings.Contains(k, tok[:20]) {
		t.Fatalf("key %q must be a hash of the token, not contain it", k)
	}
	if k != inviteTokenKey(tok) || k == inviteTokenKey(inviteTestToken(8)) {
		t.Fatal("key must be stable per token and distinct across tokens")
	}
	if inviteTokenKey("") != "" {
		t.Fatal("an empty token has no per-token bucket")
	}
}

func TestRemoteAddrIsLocal(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"[::1]:5000", true},
		{"127.0.0.1", true},
		{"192.0.2.55:5000", false},
		{"[2001:db8::1]:5000", false},
		{"not-an-address", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := remoteAddrIsLocal(tt.addr); got != tt.want {
			t.Errorf("remoteAddrIsLocal(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

// TestPublicInviteRoute_TokenLimitDoesNotBlockOthers drives the real router: one
// token's lookups being limited must not turn away a different token's lookup
// from the same client (the failure of the single shared bucket).
func TestPublicInviteRoute_TokenLimitDoesNotBlockOthers(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)
	get := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/invite/"+token, nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w.Code
	}
	hot := strings.Repeat("a", 64)
	limited := false
	for range inviteTokenRateBurst + 5 {
		if get(hot) == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("one token was never limited")
	}
	if code := get(strings.Repeat("b", 64)); code != http.StatusNotFound {
		t.Fatalf("a different unknown token: %d, want 404 (not limited)", code)
	}
}
