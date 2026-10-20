// file: internal/server/invite_rate_limit_test.go
// version: 1.10.0
// guid: a4acf753-a843-41d6-8769-4d28540be7e1
// last-edited: 2026-10-20

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
// localPeer makes every address count as this host (or none). A request
// carrying the X-Test-Verified header stands in for one whose Access assertion
// CloudflareAccessAuth verified. Tests that need a configured tunnel connector
// set h.limits.connectors.
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
		// What handleAcceptInvite would act on: the limiter's stashed parse.
		p := readAcceptInviteBody(c)
		c.String(http.StatusOK, "parsed=%v token=%s", p.parsed, p.req.Token)
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

// TestInviteRateLimit_HandlerSeesTheCountedToken proves the handler acts on
// the parse the limiter counted, including bodies built to make two parsers
// disagree.
func TestInviteRateLimit_HandlerSeesTheCountedToken(t *testing.T) {
	tok := inviteTestToken(3)
	tests := []struct {
		name, body, want string
	}{
		{"normal", `{"token":"` + tok + `","password":"correct-horse-1"}`, "parsed=true token=" + tok},
		{"duplicate token keys: the last one, for both", `{"token":"` + inviteTestToken(4) + `","token":"` + tok + `","password":"x"}`, "parsed=true token=" + tok},
		{"malformed", `{"token":`, "parsed=false token="},
		{"trailing data after the object", `{"token":"` + tok + `","password":"x"} {"token":"other"}`, "parsed=false token="},
		{"empty", "", "parsed=false token="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInviteLimitHarness(t, false)
			w := h.post(tt.body, peerIP(1))
			if w.Code != http.StatusOK || w.Body.String() != tt.want {
				t.Fatalf("got %d %q, want 200 %q", w.Code, w.Body.String(), tt.want)
			}
		})
	}
}

// An exhausted token stays exhausted however the body is padded: a duplicate
// key naming it last is counted against it.
func TestInviteRateLimit_DuplicateKeyCountsTheTokenUsed(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	victim := inviteTestToken(5)
	countAllowedUntilLimited(t, inviteTokenRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(victim, peerIP(i), nil)
	})
	body := `{"token":"` + inviteTestToken(6) + `","token":"` + victim + `","password":"x"}`
	if w := h.post(body, "198.51.100.60"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("padded body for an exhausted token: %d, want 429", w.Code)
	}
}

// A body over the cap is refused with 413 before the handler, including one
// padded with whitespace to push the token past a peek window.
func TestInviteRateLimit_OversizeBodyIs413(t *testing.T) {
	tok := inviteTestToken(7)
	for name, body := range map[string]string{
		"long password":      `{"token":"` + tok + `","password":"` + strings.Repeat("p", inviteAcceptBodyLimit) + `"}`,
		"whitespace padding": strings.Repeat(" ", inviteAcceptBodyLimit) + `{"token":"` + tok + `","password":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newInviteLimitHarness(t, false)
			w := h.post(body, peerIP(2))
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status %d (%s), want 413", w.Code, w.Body.String())
			}
		})
	}
	// Exactly at the cap is accepted.
	h := newInviteLimitHarness(t, false)
	base := `{"token":"` + tok + `","password":""}`
	body := `{"token":"` + tok + `","password":"` + strings.Repeat("p", inviteAcceptBodyLimit-len(base)) + `"}`
	if w := h.post(body, peerIP(3)); w.Code != http.StatusOK {
		t.Fatalf("a body of exactly %d bytes: %d", len(body), w.Code)
	}
}

// Unparseable bodies share one per-token bucket, so they cannot skip the
// token layer; valid tokens are unaffected.
func TestInviteRateLimit_UnparseableBodiesShareAFallbackBucket(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	allowed, limited, _ := countAllowedUntilLimited(t, inviteTokenRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.post(fmt.Sprintf(`{"token":%d`, i), peerIP(i)) // malformed, varied
	})
	if !limited || allowed > inviteTokenRateBurst+2 {
		t.Fatalf("malformed bodies allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteTokenRateBurst)
	}
	if w := h.post(`{"token":"`+inviteTestToken(8)+`","password":"x"}`, "198.51.100.61"); w.Code != http.StatusOK {
		t.Fatalf("a valid token while the fallback bucket is exhausted: %d", w.Code)
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

// connectorPeer is the address the tests configure as the tunnel connector.
const connectorPeer = "192.0.2.1"

// withConnector configures connectorPeer as the tunnel connector.
func (h *inviteLimitHarness) withConnector() *inviteLimitHarness {
	peers, bad := parseConnectorPeers(connectorPeer)
	if len(bad) != 0 {
		h.t.Fatalf("bad connector %v", bad)
	}
	h.limits.connectors = peers
	return h
}

// spoofed returns a header map whose CF-Connecting-IP differs on every call.
func spoofed(i int, extra ...string) map[string]string {
	m := map[string]string{"CF-Connecting-IP": fmt.Sprintf("203.0.113.%d", i%254+1)}
	for _, k := range extra {
		m[k] = "1"
	}
	return m
}

// TestInviteRateLimit_ClientKey_DirectPeerIsKeyedOnItsOwnAddress: a caller that
// is not a configured connector, not this host and carries no Cloudflare
// markers is a direct caller, keyed on its own address.
func TestInviteRateLimit_ClientKey_DirectPeerIsKeyedOnItsOwnAddress(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), "198.51.100.5", nil)
	})
	if !limited || allowed < inviteAuthRateBurst || allowed > inviteAuthRateBurst+2 {
		t.Fatalf("direct peer: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteAuthRateBurst)
	}
	// Another direct peer has its own bucket.
	if w := h.get(inviteTestToken(900), "198.51.100.6", nil); w.Code != http.StatusOK {
		t.Fatalf("a second direct peer while the first is limited: %d, want 200", w.Code)
	}
}

// TestInviteRateLimit_ClientKey_PublicPeerWithForgedCfHeadersIsLimitedPerAddress:
// a public peer is never a LAN connector, so sending Cloudflare headers (a
// rotating CF-Connecting-IP, Cf-Ray, the Access cookie, even a verified
// assertion) neither picks its bucket nor takes it out of the per-client
// layer: it is limited on its own address.
func TestInviteRateLimit_ClientKey_PublicPeerWithForgedCfHeadersIsLimitedPerAddress(t *testing.T) {
	for name, extra := range map[string][]string{"no assertion": {"Cf-Ray"}, "verified assertion": {"Cf-Ray", "X-Test-Verified"}} {
		t.Run(name, func(t *testing.T) {
			h := newInviteLimitHarness(t, false)
			allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
				return h.get(inviteTestToken(i), "198.51.100.5", spoofed(i, extra...))
			})
			if !limited || allowed < inviteAuthRateBurst || allowed > inviteAuthRateBurst+2 {
				t.Fatalf("forged Cloudflare headers: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteAuthRateBurst)
			}
			if w := h.get(inviteTestToken(900), "198.51.100.6", spoofed(900, extra...)); w.Code != http.StatusOK {
				t.Fatalf("a second public peer while the first is limited: %d, want 200", w.Code)
			}
		})
	}
}

// TestInviteRateLimit_ClientKey_UntrustedLocalSkipsClientLayer: with no
// connector configured, a header from a local peer without a
// verified Access assertion is not believed, and the request has no trusted
// client address. The per-client layer is SKIPPED for it, not shared: a flood
// from that population must not block a second token beyond the per-token and
// global limits.
func TestInviteRateLimit_ClientKey_UntrustedLocalSkipsClientLayer(t *testing.T) {
	h := newInviteLimitHarness(t, true)
	// Hammer ONE token past its own burst: only the per-token layer may refuse.
	allowed, limited, _ := countAllowedUntilLimited(t, inviteTokenRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(1), "192.0.2.1", spoofed(i))
	})
	if !limited || allowed < inviteTokenRateBurst || allowed > inviteTokenRateBurst+2 {
		t.Fatalf("token flood: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteTokenRateBurst)
	}
	// A second token from the same untrusted population is not blocked.
	if w := h.get(inviteTestToken(2), "192.0.2.1", spoofed(1)); w.Code != http.StatusOK {
		t.Fatalf("a second token after the flood: %d, want 200", w.Code)
	}
	// Nor is a run of distinct tokens well past the per-client burst (the
	// global ceiling is the only bound left, and it is far above this).
	for i := range inviteAuthRateBurst + 20 {
		hdr := spoofed(i)
		if i%3 == 0 {
			hdr = nil
		}
		if w := h.get(inviteTestToken(100+i), "192.0.2.1", hdr); w.Code != http.StatusOK {
			t.Fatalf("distinct token %d: %d, want 200 (client layer skipped)", i, w.Code)
		}
	}
	// A client that CAN be established is still limited on its own.
	one := map[string]string{"CF-Connecting-IP": "203.0.113.77", "X-Test-Verified": "1"}
	if _, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(500+i), "192.0.2.1", one)
	}); !limited {
		t.Fatal("a verified client was never limited")
	}
}

// TestInviteRateLimit_ClientKey_VerifiedAccessLocalPeerIsPerIP: with a verified
// Access assertion through a local peer the header is believed, so clients get
// independent buckets.
func TestInviteRateLimit_ClientKey_VerifiedAccessLocalPeerIsPerIP(t *testing.T) {
	h := newInviteLimitHarness(t, true)
	for i := range inviteAuthRateBurst + 10 {
		if w := h.get(inviteTestToken(i), "192.0.2.1", spoofed(i, "X-Test-Verified")); w.Code != http.StatusOK {
			t.Fatalf("distinct verified clients shared a bucket: request %d got %d", i, w.Code)
		}
	}
	one := map[string]string{"CF-Connecting-IP": "203.0.113.200", "X-Test-Verified": "1"}
	_, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(500+i), "192.0.2.1", one)
	})
	if !limited {
		t.Fatal("a single verified client was never limited")
	}
	other := map[string]string{"CF-Connecting-IP": "203.0.113.201", "X-Test-Verified": "1"}
	if w := h.get(inviteTestToken(900), "192.0.2.1", other); w.Code != http.StatusOK {
		t.Fatalf("a second verified client while the first is limited: %d, want 200", w.Code)
	}
}

// TestInviteRateLimit_ClientKey_ConfiguredConnectorClientsAreIndependent is the
// regression for the shared tunnel bucket on routes that bypass Access: through
// the configured connector with NO assertion, 11 garbage-token requests from
// one visitor must not lock out a real guest at a different address.
func TestInviteRateLimit_ClientKey_ConfiguredConnectorClientsAreIndependent(t *testing.T) {
	h := newInviteLimitHarness(t, true).withConnector()
	attacker := map[string]string{"CF-Connecting-IP": "203.0.113.50"}
	guest := map[string]string{"CF-Connecting-IP": "198.51.100.60"}

	for i := range inviteAuthRateBurst + 1 {
		w := h.get(inviteTestToken(100+i), connectorPeer, attacker)
		if i < inviteAuthRateBurst && w.Code != http.StatusOK {
			t.Fatalf("attacker request %d: %d, want 200 (still inside its burst)", i, w.Code)
		}
		if i == inviteAuthRateBurst && w.Code != http.StatusTooManyRequests {
			t.Fatalf("attacker request %d: %d, want 429 (its own bucket is spent)", i, w.Code)
		}
	}
	if w := h.get(inviteTestToken(1), connectorPeer, guest); w.Code != http.StatusOK {
		t.Fatalf("real guest after the attacker was limited: %d, want 200", w.Code)
	}
	// The same header spelled differently is the same client.
	attackerV6 := map[string]string{"CF-Connecting-IP": " ::ffff:203.0.113.50 "}
	if w := h.get(inviteTestToken(2), connectorPeer, attackerV6); w.Code != http.StatusTooManyRequests {
		t.Fatalf("attacker via an equivalent spelling: %d, want 429", w.Code)
	}
}

// TestInviteRateLimit_ClientKey_ConnectorWithoutHeaderSkipsClientLayer: the
// configured connector sending no usable header identifies no client, so the
// per-client layer is skipped for those requests (and attributed visitors are
// unaffected).
func TestInviteRateLimit_ClientKey_ConnectorWithoutHeaderSkipsClientLayer(t *testing.T) {
	h := newInviteLimitHarness(t, false).withConnector()
	for i := range inviteAuthRateBurst + 20 {
		hdr := map[string]string{"CF-Connecting-IP": fmt.Sprintf("not-an-ip-%d", i)}
		if i%2 == 0 {
			hdr = nil
		}
		if w := h.get(inviteTestToken(i), connectorPeer, hdr); w.Code != http.StatusOK {
			t.Fatalf("connector request %d without a usable header: %d, want 200 (client layer skipped)", i, w.Code)
		}
	}
	if w := h.get(inviteTestToken(801), connectorPeer, map[string]string{"CF-Connecting-IP": "198.51.100.61"}); w.Code != http.StatusOK {
		t.Fatalf("an attributed visitor: %d, want 200", w.Code)
	}
}

// TestTrustedClientIP pins the rule itself.
func TestTrustedClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	local := func(addr string) bool {
		return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "[::1]") || strings.HasPrefix(addr, "::1")
	}
	connectors, _ := parseConnectorPeers("198.51.100.20, 2001:db8::20")
	tests := []struct {
		name     string
		remote   string
		header   string
		verified bool
		want     string
		wantOK   bool
	}{
		{"connector, valid header", "198.51.100.20:5000", "203.0.113.9", false, "203.0.113.9", true},
		{"connector, header with spaces", "198.51.100.20:5000", " 203.0.113.9 ", false, "203.0.113.9", true},
		{"connector, ipv6 header normalised", "198.51.100.20:5000", "2001:DB8:0:0:0:0:0:1", false, "2001:db8::1", true},
		{"ipv6 connector, valid header", "[2001:db8::20]:5000", "203.0.113.9", false, "203.0.113.9", true},
		{"connector, no header", "198.51.100.20:5000", "", false, "198.51.100.20", false},
		{"connector, garbage header", "198.51.100.20:5000", "evil", false, "198.51.100.20", false},
		{"local peer, verified Access, valid header", "127.0.0.1:5000", "203.0.113.9", true, "203.0.113.9", true},
		{"local peer, verified Access, no header", "127.0.0.1:5000", "", true, "127.0.0.1", false},
		{"local peer, header but no Access", "127.0.0.1:5000", "203.0.113.9", false, "127.0.0.1", false},
		{"local peer, garbage header, no Access", "127.0.0.1:5000", "evil", false, "127.0.0.1", false},
		{"local ipv6 peer, header but no Access", "[::1]:5000", "198.51.100.4", false, "::1", false},
		{"public peer, forged header: keyed on the peer", "198.51.100.7:5000", "203.0.113.9", false, "198.51.100.7", true},
		{"public peer, verified Access, forged header: keyed on the peer", "198.51.100.7:5000", "203.0.113.9", true, "198.51.100.7", true},
		{"unlisted private connector with a header: keyed on the peer", "10.0.0.11:5000", "203.0.113.9", false, "10.0.0.11", true},
		{"direct peer, no header", "198.51.100.7:5000", "", false, "198.51.100.7", true},
		{"direct private peer, no header", "10.0.0.50:5000", "", false, "10.0.0.50", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			if err := r.SetTrustedProxies(nil); err != nil {
				t.Fatal(err)
			}
			r.Use(func(c *gin.Context) {
				if tt.verified {
					servermiddleware.MarkCFAccessVerified(c)
				}
			})
			var gotIP string
			var gotOK bool
			r.GET("/x", func(c *gin.Context) {
				gotIP, gotOK = trustedClientIP(c, clientTrust{isLocalPeer: local, connectors: connectors})
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.RemoteAddr = tt.remote
			if tt.header != "" {
				req.Header.Set("CF-Connecting-IP", tt.header)
			}
			r.ServeHTTP(httptest.NewRecorder(), req)
			if gotIP != tt.want || gotOK != tt.wantOK {
				t.Fatalf("trustedClientIP = (%q, %v), want (%q, %v)", gotIP, gotOK, tt.want, tt.wantOK)
			}
		})
	}
}

func TestParseConnectorPeers(t *testing.T) {
	peers, bad := parseConnectorPeers(" 192.0.2.5 ,, ::ffff:192.0.2.6, not-an-ip,2001:db8::5")
	if len(peers) != 3 || len(bad) != 1 || bad[0] != "not-an-ip" {
		t.Fatalf("peers=%v bad=%v", peers, bad)
	}
	for addr, want := range map[string]bool{
		"192.0.2.5:80": true, "192.0.2.6:80": true, "[2001:db8::5]:80": true, "192.0.2.5": true,
		"192.0.2.7:80": false, "not-an-address": false, "": false,
	} {
		if got := peers.contains(addr); got != want {
			t.Fatalf("contains(%q) = %v, want %v", addr, got, want)
		}
	}
	if (connectorPeers(nil)).contains("192.0.2.5:80") {
		t.Fatal("no configured connector must trust no peer")
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

// piConnectors are the production shape: three tunnel connectors on other LAN
// hosts, connecting from private addresses that are not this host.
var piConnectors = []string{"10.0.0.11", "10.0.0.12", "10.0.0.13"}

// cfHeaders is what a tunnel request from visitor carries.
func cfHeaders(visitor string) map[string]string {
	return map[string]string{"CF-Connecting-IP": visitor, "Cf-Ray": "8c0ffee-LHR"}
}

// SF1: with TUNNEL_CONNECTOR_ADDRS unset, a Pi connector is a private peer
// that is not listed: it is keyed on its own address, so each Pi is one
// bucket for the visitors it relays (production lists the Pis, so this is the
// misconfigured case), and the forwarded CF-Connecting-IP is not believed.
func TestInviteRateLimit_PiConnectorsUnset_EachPiIsOneBucket(t *testing.T) {
	h := newInviteLimitHarness(t, false) // nothing is this host
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), piConnectors[0], cfHeaders(fmt.Sprintf("203.0.113.%d", i+1)))
	})
	if !limited || allowed < inviteAuthRateBurst || allowed > inviteAuthRateBurst+2 {
		t.Fatalf("visitors through one unlisted Pi: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteAuthRateBurst)
	}
	if w := h.get(inviteTestToken(900), piConnectors[1], cfHeaders("203.0.113.200")); w.Code != http.StatusOK {
		t.Fatalf("another Pi while the first is limited: %d, want 200 (its own bucket)", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
	req.RemoteAddr = piConnectors[2] + ":4000"
	for k, v := range cfHeaders("203.0.113.99") {
		req.Header.Set(k, v)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	if key, ok := h.limits.inviteClientKey(c); !ok || key != "invite-client:"+piConnectors[2] {
		t.Fatalf("inviteClientKey = %q, %v; want the unlisted Pi's own address", key, ok)
	}
}

// SF1: with TUNNEL_CONNECTOR_ADDRS naming the three Pis, each visitor gets
// its own bucket whichever Pi carried the request: one abusive visitor is
// refused at its own burst, and after it and 40 other distinct visitors an
// innocent guest still gets 200.
func TestInviteRateLimit_PiConnectorsSet_PerVisitorBuckets(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	peers, bad := parseConnectorPeers(strings.Join(piConnectors, ","))
	if len(bad) != 0 {
		t.Fatal(bad)
	}
	h.limits.connectors = peers

	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(1000+i), piConnectors[i%3], cfHeaders("203.0.113.66"))
	})
	if !limited || allowed < inviteAuthRateBurst || allowed > inviteAuthRateBurst+2 {
		t.Fatalf("abusive visitor across the Pis: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteAuthRateBurst)
	}
	for i := range 40 {
		if w := h.get(inviteTestToken(i), piConnectors[i%3], cfHeaders(fmt.Sprintf("198.51.100.%d", i+1))); w.Code != http.StatusOK {
			t.Fatalf("distinct visitor %d: %d, want 200", i, w.Code)
		}
	}
	if w := h.get(inviteTestToken(5000), piConnectors[2], cfHeaders("192.0.2.200")); w.Code != http.StatusOK {
		t.Fatalf("innocent guest after 41 other visitors: %d, want 200", w.Code)
	}
	// The key is the visitor's, never the Pi's.
	req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
	req.RemoteAddr = piConnectors[0] + ":4000"
	req.Header.Set("CF-Connecting-IP", "198.51.100.7")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	if key, ok := h.limits.inviteClientKey(c); !ok || key != "invite-client:198.51.100.7" {
		t.Fatalf("inviteClientKey = %q, %v; want the visitor's address", key, ok)
	}
}

// SF1 (b): IPv6 visitors share one per-client bucket per /64, so rotating
// addresses inside one /64 buys no fresh bucket.
func TestInviteRateLimit_IPv6VisitorsShareTheirSlash64(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	peers, _ := parseConnectorPeers(piConnectors[0])
	h.limits.connectors = peers
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), piConnectors[0], cfHeaders(fmt.Sprintf("2001:db8:5:6::%x", i+1)))
	})
	if !limited || allowed > inviteAuthRateBurst+2 {
		t.Fatalf("one /64 rotating addresses: allowed %d (limited=%v), want about %d then 429", allowed, limited, inviteAuthRateBurst)
	}
	if w := h.get(inviteTestToken(900), piConnectors[0], cfHeaders("2001:db8:5:7::1")); w.Code != http.StatusOK {
		t.Fatalf("a different /64: %d, want 200", w.Code)
	}
}

func TestHasCloudflareMarkers(t *testing.T) {
	for name, tt := range map[string]struct {
		hdr    map[string]string
		cookie bool
		want   bool
	}{
		"none":             {nil, false, false},
		"unrelated":        {map[string]string{"X-Request-Id": "1"}, false, false},
		"CF-Connecting-IP": {map[string]string{"CF-Connecting-IP": "203.0.113.1"}, false, true},
		"Cf-Ray":           {map[string]string{"Cf-Ray": "x"}, false, true},
		"Cf-Access-Jwt":    {map[string]string{"Cf-Access-Jwt-Assertion": "x"}, false, true},
		"CF_Authorization": {nil, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			for k, v := range tt.hdr {
				req.Header.Set(k, v)
			}
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "x"})
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req
			if got := hasCloudflareMarkers(c); got != tt.want {
				t.Fatalf("hasCloudflareMarkers = %v, want %v", got, tt.want)
			}
		})
	}
}

// NIT2: a request the global layer refuses creates no per-client or per-token
// bucket, so key churn is bounded by the global ceiling; and a client that is
// already limited does not spend a global slot.
func TestInviteRateLimit_GlobalIsCheckedBeforeAnyBucketIsCreated(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	// A limited client: its refused requests leave the global budget alone.
	if _, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), "198.51.100.5", nil)
	}); !limited {
		t.Fatal("the client was never limited")
	}
	for i := range 200 {
		h.get(inviteTestToken(100+i), "198.51.100.5", nil)
	}
	if w := h.get(inviteTestToken(999), "198.51.100.6", nil); w.Code != http.StatusOK {
		t.Fatalf("a fresh client after 200 refused requests from a limited one: %d, want 200 (global untouched)", w.Code)
	}
	// Spend the global budget with distinct clients and tokens.
	if _, limited, _ := countAllowedUntilLimited(t, inviteGlobalRateBurst+30, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(2000+i), fmt.Sprintf("198.51.101.%d", i%254+1), nil)
	}); !limited {
		t.Fatal("global ceiling never tripped")
	}
	// Past the ceiling, a client's refused requests spend nothing of its own:
	// more requests than its burst still leave its bucket full.
	for i := range inviteAuthRateBurst + 5 {
		if w := h.get(inviteTestToken(5000+i), "198.51.102.1", nil); w.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d past the global ceiling: %d, want 429", i, w.Code)
		}
	}
	n := 0
	for range inviteAuthRateBurst + 5 {
		if h.limits.perClient.AllowKey("invite-client:198.51.102.1") {
			n++
		}
	}
	if n < inviteAuthRateBurst {
		t.Fatalf("the client had %d tokens left, want its full burst of %d: requests the global layer refused spent its bucket", n, inviteAuthRateBurst)
	}
}

// NIT2 (review): many concurrent requests from ONE client against a small
// global ceiling spend at most the client's own burst of global slots, so
// other clients are still admitted. A peek-then-spend order let every
// concurrent request pass the client peek and drain the global layer.
func TestInviteRateLimit_ConcurrentClientCannotDrainGlobal(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	h.limits.global = servermiddleware.NewIPRateLimiter(1, 3*inviteAuthRateBurst)
	// Refill once a minute, so a goroutine scheduled seconds late on a loaded
	// -race run cannot earn a refilled token and break the exact bound.
	h.limits.perClient = servermiddleware.NewIPRateLimiter(1, inviteAuthRateBurst)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := h.get(inviteTestToken(i), "198.51.100.5", nil); w.Code == http.StatusOK {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := admitted.Load(); n > inviteAuthRateBurst {
		t.Fatalf("one client got %d requests through, want at most its burst of %d", n, inviteAuthRateBurst)
	}
	for i := range 5 {
		if w := h.get(inviteTestToken(1000+i), fmt.Sprintf("198.51.100.%d", 50+i), nil); w.Code != http.StatusOK {
			t.Fatalf("another client after the concurrent flood: %d, want 200 (global not drained)", w.Code)
		}
	}
}

// Ordering (review): tokenOf runs before any layer, so a request it rejects
// (413) is answered 413 even from a client that is already limited (the
// previous order reserved the client first and answered 429), and it spends
// nothing of the global budget. (That a refused request inserts no per-key
// entry is proved on the limiter itself, TestReserveKeys_RefusalEvictsNobody;
// the table size is not visible from this package.)
func TestInviteRateLimit_RejectedBodyReachesNoLimiter(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	h.limits.perClient = servermiddleware.NewIPRateLimiter(1, 2)
	h.limits.global = servermiddleware.NewIPRateLimiter(1, 3)
	const peer = "198.51.100.9"
	for i := range 2 {
		if w := h.get(inviteTestToken(i), peer, nil); w.Code != http.StatusOK {
			t.Fatalf("admitted request %d: %d", i, w.Code)
		}
	}
	if w := h.get(inviteTestToken(2), peer, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("client should be limited now: %d", w.Code)
	}
	big := `{"token":"` + inviteTestToken(7) + `","password":"` + strings.Repeat("p", inviteAcceptBodyLimit) + `"}`
	for range 5 {
		if w := h.post(big, peer); w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversize body from a limited client: %d, want 413 (the body is read before any layer)", w.Code)
		}
	}
	// The one global slot the two admitted requests left is still there.
	if w := h.get(inviteTestToken(8), "198.51.100.10", nil); w.Code != http.StatusOK {
		t.Fatalf("after the rejected bodies: %d, want 200 (they spent no global slot)", w.Code)
	}
}

// NIT6: a LAN IPv6 peer is keyed on its full address, as bootstrap keys a
// direct LAN peer: every device on an IPv6 LAN shares one /64, so a /64 key
// would let one device spend every other device's budget. A public IPv6
// address still groups by /64.
func TestClientPrefixKey_LANIPv6KeepsItsFullAddress(t *testing.T) {
	for in, want := range map[string]string{
		"10.0.0.5":                "10.0.0.5",
		"::ffff:10.0.0.5":         "10.0.0.5",
		"fd12:3456:789a:1::5":     "fd12:3456:789a:1::5",
		"fe80::1%en0":             "fe80::1",
		"::1":                     "::1",
		"2001:db8:5:6::1":         "2001:db8:5:6::/64",
		"2001:db8:5:6:aaaa::9999": "2001:db8:5:6::/64",
	} {
		got, ok := clientPrefixKey(in)
		if !ok || got != want {
			t.Errorf("clientPrefixKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestInviteRateLimit_LANIPv6PeersInOneSlash64HaveTheirOwnBuckets(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	if _, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), "[fd12:3456:789a:1::5]", nil)
	}); !limited {
		t.Fatal("the first LAN device was never limited")
	}
	if w := h.get(inviteTestToken(500), "[fd12:3456:789a:1::6]", nil); w.Code != http.StatusOK {
		t.Fatalf("another device on the same LAN /64: %d, want 200 (its own bucket)", w.Code)
	}
}
