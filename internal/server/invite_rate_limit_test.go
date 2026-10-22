// file: internal/server/invite_rate_limit_test.go
// version: 1.27.0
// guid: a4acf753-a843-41d6-8769-4d28540be7e1
// last-edited: 2026-10-22

package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// The production writer chain: the raw controller captured, then gzip
	// (which wraps c.Writer for clients sending Accept-Encoding: gzip).
	r.Use(captureRawResponseController(), compressionMiddleware())
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
	h.limits.connectors = fixedConnectors(peers)
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
	if len(peers) != 3 || len(bad) != 1 || bad[0] != (connectorReject{"not-an-ip", rejectNotIP}) {
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
		code                    int
		body, ctype, retryAfter string
	}
	got := map[string]refusal{}
	for name, f := range trip {
		w := f(newInviteLimitHarness(t, false))
		got[name] = refusal{w.Code, w.Body.String(), w.Header().Get("Content-Type"), w.Header().Get("Retry-After")}
	}
	ref := got["per client"]
	if ref.code != http.StatusTooManyRequests || !strings.Contains(ref.body, "TOO_MANY_REQUESTS") {
		t.Fatalf("refusal = %+v, want a 429 TOO_MANY_REQUESTS body", ref)
	}
	if ref.retryAfter != "60" {
		t.Fatalf("refusal Retry-After = %q, want the fixed \"60\" (the same for every layer)", ref.retryAfter)
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
	if ic, ok := h.limits.inviteClientKey(c); !ok || ic.key != "invite-client:"+piConnectors[2] {
		t.Fatalf("inviteClientKey = %q, %v; want the unlisted Pi's own address", ic.key, ok)
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
	h.limits.connectors = fixedConnectors(peers)

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
	if ic, ok := h.limits.inviteClientKey(c); !ok || ic.key != "invite-client:198.51.100.7" {
		t.Fatalf("inviteClientKey = %q, %v; want the visitor's address", ic.key, ok)
	}
}

// SF1 (b): IPv6 visitors share one per-client bucket per /64, so rotating
// addresses inside one /64 buys no fresh bucket.
func TestInviteRateLimit_IPv6VisitorsShareTheirSlash64(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	peers, _ := parseConnectorPeers(piConnectors[0])
	h.limits.connectors = fixedConnectors(peers)
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

// len reports how many values the set holds. Test-only.
func (o *onceSet) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

// NIT3: a private peer that is not a listed connector but sends Cloudflare
// headers is warned about once per address, so a missing
// TUNNEL_CONNECTOR_ADDRS entry is visible; public peers and listed
// connectors are not remembered.
func TestTrustedClientIP_WarnsOncePerUnlistedPrivateConnector(t *testing.T) {
	saved := unlistedConnectors
	unlistedConnectors = newOnceSet(unlistedConnectorWarnCap)
	t.Cleanup(func() { unlistedConnectors = saved })
	listed, _ := parseConnectorPeers(piConnectors[0])
	trust := clientTrust{isLocalPeer: func(string) bool { return false }, connectors: listed}
	call := func(peer string, hdr map[string]string) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = peer + ":4000"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		trustedClientIP(c, trust)
	}
	for range 3 {
		call(piConnectors[1], cfHeaders("203.0.113.7")) // unlisted Pi
	}
	if n := unlistedConnectors.len(); n != 1 {
		t.Fatalf("an unlisted Pi sending Cf headers three times: %d remembered, want 1", n)
	}
	call(piConnectors[0], cfHeaders("203.0.113.7")) // listed connector
	call("198.51.100.20", cfHeaders("203.0.113.7")) // public peer forging headers
	call(piConnectors[2], nil)                      // private peer, no markers
	if n := unlistedConnectors.len(); n != 1 {
		t.Fatalf("listed, public and marker-less peers were remembered: %d, want 1", n)
	}
}

// The remembered set is bounded, and says once that it stopped.
func TestOnceSet_Bounded(t *testing.T) {
	o := newOnceSet(2)
	got := []onceSetResult{o.add("a"), o.add("a"), o.add("b"), o.add("c"), o.add("d"), o.add("a")}
	want := []onceSetResult{onceSetAdded, onceSetSeen, onceSetAdded, onceSetFull, onceSetSeen, onceSetSeen}
	if !slices.Equal(got, want) {
		t.Fatalf("add results %v, want %v", got, want)
	}
	if o.len() != 2 {
		t.Fatalf("set holds %d, want 2", o.len())
	}
}

// LAN /64 aggregate (review MEDIUM): one LAN host rotating through 50
// addresses of its /64 gets a fresh per-address bucket each time, but every
// address also shares the /64's aggregate, which caps it well before the
// global ceiling; other clients are still admitted.
func TestInviteRateLimit_LANSlash64RotationIsCappedByTheAggregate(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	admitted := 0
	for i := range 50 {
		peer := fmt.Sprintf("[fd12:3456:789a:1::%x]", i+1)
		for j := range 2 {
			if w := h.get(inviteTestToken(i*2+j), peer, nil); w.Code == http.StatusOK {
				admitted++
			}
		}
	}
	if admitted > inviteDirectLANPrefixRateBurst+1 {
		t.Fatalf("one LAN /64 rotating 50 addresses got %d requests through, want at most the aggregate burst of %d", admitted, inviteDirectLANPrefixRateBurst)
	}
	if admitted < inviteDirectLANPrefixRateBurst {
		t.Fatalf("the /64 got only %d through, want the aggregate burst of %d", admitted, inviteDirectLANPrefixRateBurst)
	}
	if w := h.get(inviteTestToken(900), "198.51.100.30", nil); w.Code != http.StatusOK {
		t.Fatalf("another client after the rotation: %d, want 200 (global not drained)", w.Code)
	}
	if w := h.get(inviteTestToken(901), "[fd12:3456:789a:2::1]", nil); w.Code != http.StatusOK {
		t.Fatalf("a device in another LAN /64: %d, want 200 (its own aggregate)", w.Code)
	}
}

// Two distinct LAN devices in one /64 each get their own per-address burst,
// within the shared aggregate.
func TestInviteRateLimit_LANDevicesInOneSlash64EachGetTheirBurst(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	for d, peer := range []string{"[fd12:3456:789a:1::a]", "[fd12:3456:789a:1::b]"} {
		allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
			return h.get(inviteTestToken(d*100+i), peer, nil)
		})
		if !limited || allowed < inviteAuthRateBurst || allowed > inviteAuthRateBurst+1 {
			t.Fatalf("device %d: allowed %d (limited=%v), want its own burst of %d", d, allowed, limited, inviteAuthRateBurst)
		}
	}
}

// NIT5 / S1: a body trickled slower than inviteAcceptBodyReadTimeout is
// answered 408 instead of holding the connection, also for a client sending
// Accept-Encoding: gzip through the gzip middleware (which hides the
// connection unless captureRawResponseController ran first), and the deadline is cleared after
// the read, so a later request on the same keep-alive connection, sent after
// the timeout has passed, is unaffected.
func TestInviteRateLimit_SlowBodyTimesOut(t *testing.T) {
	saved := inviteAcceptBodyReadTimeout
	inviteAcceptBodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { inviteAcceptBodyReadTimeout = saved })
	h := newInviteLimitHarness(t, false)
	srv := httptest.NewServer(h.engine)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	readStatus := func(conn net.Conn, br *bufio.Reader) int {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("reading the response: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}

	// Slow: headers promise 100 bytes, a few arrive, then nothing.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Accept-Encoding: gzip, as every browser sends: gzip wraps the writer.
	fmt.Fprintf(conn, "POST /accept HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"tok")
	start := time.Now()
	if code := readStatus(conn, bufio.NewReader(conn)); code != http.StatusRequestTimeout {
		t.Fatalf("trickled body: %d, want 408", code)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("the 408 took %v; the read deadline did not apply", el)
	}

	// Keep-alive: a full body, then a pause longer than the timeout, then
	// another full body on the same connection.
	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	br := bufio.NewReader(conn2)
	body := `{"token":"` + inviteTestToken(1) + `","password":"x"}`
	send := func() {
		fmt.Fprintf(conn2, "POST /accept HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}
	send()
	if code := readStatus(conn2, br); code != http.StatusOK {
		t.Fatalf("first request: %d, want 200", code)
	}
	time.Sleep(2 * inviteAcceptBodyReadTimeout)
	send()
	if code := readStatus(conn2, br); code != http.StatusOK {
		t.Fatalf("second request after the timeout elapsed: %d, want 200 (deadline was left set)", code)
	}
}

// Review MEDIUM: the invite LAN /64 aggregate is for direct peers only. A
// flood from unlisted connectors on LAN addresses rotating through the
// owner's /64 (Cloudflare headers, so not direct) never spends it, so a
// direct device in that /64 still gets its burst.
func TestInviteRateLimit_TunnelFloodFromLANConnectorsLeavesTheDirectAggregate(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	h.limits.global = servermiddleware.NewIPRateLimiter(1, 1000)
	for i := range 100 {
		h.get(inviteTestToken(i), fmt.Sprintf("[fd12:3456:789a:1::%x]", 0x100+i), cfHeaders("203.0.113.9"))
	}
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(500+i), "[fd12:3456:789a:1::1]", nil)
	})
	if !limited || allowed < inviteAuthRateBurst {
		t.Fatalf("a direct device after the tunnel flood: %d admitted (limited=%v), want its burst of %d", allowed, limited, inviteAuthRateBurst)
	}
}

// N1: a link-local client with a zoned RemoteAddr ("[fe80::1%en0]:4000") is
// attributed through the real request path: gin's ClientIP returns "" for
// it, which skipped the per-client layer. It is now keyed on its address
// (zone dropped), limited at its burst, and a second link-local device has
// its own bucket.
func TestInviteRateLimit_ZonedLinkLocalPeerIsAttributed(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(i), "[fe80::1%en0]", nil)
	})
	if !limited || allowed != inviteAuthRateBurst {
		t.Fatalf("zoned link-local peer: allowed %d (limited=%v), want its burst of %d", allowed, limited, inviteAuthRateBurst)
	}
	if w := h.get(inviteTestToken(500), "[fe80::2%en0]", nil); w.Code != http.StatusOK {
		t.Fatalf("another link-local device: %d, want 200", w.Code)
	}
}

// S2: a LAN host rotating ULA addresses while sending a Cf-* header is not
// direct, so it used to get full-address keys and no /64 aggregate (100/100
// admitted, draining the global burst). It now shares the
// invite-unlisted-lan aggregate, the owner's direct peer in the same /64 is
// still admitted afterwards, and the rotation is remembered as one /64 by the
// unlisted-connector warning set.
func TestInviteRateLimit_CfMarkedULARotationIsCapped(t *testing.T) {
	saved := unlistedConnectors
	unlistedConnectors = newOnceSet(unlistedConnectorWarnCap)
	t.Cleanup(func() { unlistedConnectors = saved })
	h := newInviteLimitHarness(t, false)
	admitted := 0
	for i := range 100 {
		if w := h.get(inviteTestToken(i), fmt.Sprintf("[fd12:3456:789a:1::%x]", 0x100+i), map[string]string{"Cf-Ray": "x"}); w.Code == http.StatusOK {
			admitted++
		}
	}
	if admitted > inviteUnlistedLANPrefixRateBurst+1 {
		t.Fatalf("Cf-marked ULA rotation: %d of 100 admitted, want at most the aggregate burst of %d", admitted, inviteUnlistedLANPrefixRateBurst)
	}
	allowed, limited, _ := countAllowedUntilLimited(t, inviteAuthRateBurst+10, func(i int) *httptest.ResponseRecorder {
		return h.get(inviteTestToken(500+i), "[fd12:3456:789a:1::1]", nil)
	})
	if !limited || allowed < inviteAuthRateBurst {
		t.Fatalf("the owner's direct peer after the flood: %d admitted (limited=%v), want its burst of %d", allowed, limited, inviteAuthRateBurst)
	}
	if n := unlistedConnectors.len(); n != 1 {
		t.Fatalf("the rotation filled %d warning slots, want 1 (its /64)", n)
	}
}

// S1: the accept-invite route is excluded from gzip, so its body read
// deadline never depends on the gzip writer; an ordinary route is still
// compressed.
func TestCompressionMiddleware_ExcludesAcceptInvite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(compressionMiddleware())
	big := strings.Repeat("x", 4096)
	r.POST("/api/v1/auth/accept-invite", func(c *gin.Context) { c.String(http.StatusOK, big) })
	r.GET("/api/v1/other", func(c *gin.Context) { c.String(http.StatusOK, big) })
	for path, wantGzip := range map[string]bool{"/api/v1/auth/accept-invite": false, "/api/v1/other": true} {
		method := http.MethodGet
		if path == "/api/v1/auth/accept-invite" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if got := w.Header().Get("Content-Encoding") == "gzip"; got != wantGzip {
			t.Errorf("%s: gzipped=%v, want %v", path, got, wantGzip)
		}
	}
}

// S-A: a body larger than the cap whose sender then stalls (4097 bytes of a
// declared 5000) is answered 413 at once, with Connection: close, and the
// connection is closed within the read deadline. Before the fix net/http
// tried to drain the unread body before writing the response headers, with
// the deadline already cleared, so the caller got no answer at all.
func TestInviteRateLimit_OversizeThenStallIs413Promptly(t *testing.T) {
	saved := inviteAcceptBodyReadTimeout
	inviteAcceptBodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { inviteAcceptBodyReadTimeout = saved })
	h := newInviteLimitHarness(t, false)
	srv := httptest.NewServer(h.engine)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /accept HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\nContent-Type: application/json\r\nContent-Length: 5000\r\n\r\n%s", strings.Repeat("a", inviteAcceptBodyLimit+1))
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no response to an oversize body whose sender stalled: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
	if !resp.Close {
		t.Fatal("the 413 does not carry Connection: close")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("the 413 took %v", el)
	}
	// The server closes the connection once the bounded drain gives up.
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the connection stayed open after the 413")
	}
	if el := time.Since(start); el > 2500*time.Millisecond {
		t.Fatalf("the connection closed only after %v; the post-response drain was not bounded", el)
	}
}

// N-b (security review): a link-local address is only unique per interface,
// so a connector entry must carry its zone and is matched with it. A listed
// fe80::5%en0 connecting from "[fe80::5%en0]" is trusted (its visitors are
// keyed on CF-Connecting-IP); the same address arriving on en1 is a
// different host, NOT trusted: it is keyed on its own address and, sending
// Cloudflare headers, counts as an unlisted LAN peer.
func TestInviteRateLimit_ZonedLinkLocalConnectorIsRecognised(t *testing.T) {
	withFreshUnlistedWarnings(t)
	withFakeHost(t)
	peers, rejected := parseConnectorPeers("fe80::5%en0")
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	h := newInviteLimitHarness(t, false)
	h.limits.connectors = fixedConnectors(peers)
	keyFrom := func(remote string) (inviteClient, bool) {
		req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
		req.RemoteAddr = remote
		for k, v := range cfHeaders("203.0.113.44") {
			req.Header.Set(k, v)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		return h.limits.inviteClientKey(c)
	}
	if ic, ok := keyFrom("[fe80::5%en0]:4000"); !ok || ic != (inviteClient{key: "invite-client:203.0.113.44"}) {
		t.Fatalf("listed connector on its own interface: inviteClientKey = %+v, %v; want the visitor's address", ic, ok)
	}
	want := inviteClient{key: "invite-client:fe80::5", prefixKey: "invite-unlisted-lan:fe80::/64", totalKey: inviteUnlistedLANTotalKey, unlisted: true}
	if ic, ok := keyFrom("[fe80::5%en1]:4000"); !ok || ic != want {
		t.Fatalf("the same address on another interface: inviteClientKey = %+v, %v; want %+v (not trusted)", ic, ok, want)
	}
}

// A zoneless IPv6 link-local connector entry would trust that address on
// every interface, so it is rejected (and not trusted); a zoned one matches
// only with its zone; global, ULA and IPv4 entries stay zone-free, and a
// zone on them is ignored.
func TestParseConnectorPeers_LinkLocalNeedsItsZone(t *testing.T) {
	withFakeHost(t)
	peers, rejected := parseConnectorPeers("fe80::5%en0, fe80::6, 10.0.0.9, fd12:3456:789a:1::11%en0, 2001:db8::5")
	if len(rejected) != 1 || rejected[0] != (connectorReject{"fe80::6", rejectNoZone}) {
		t.Fatalf("rejected=%v, want only fe80::6, for its missing zone", rejected)
	}
	if len(peers) != 4 {
		t.Fatalf("peers=%v, want 4", peers)
	}
	for addr, want := range map[string]bool{
		"[fe80::5%en0]:4000":              true,
		"fe80::5%en0":                     true,
		"[fe80::5%en1]:4000":              false,
		"[fe80::5]:4000":                  false,
		"fe80::5":                         false,
		"[fe80::6%en0]:4000":              false, // the rejected zoneless entry trusts nothing
		"[fe80::6]:4000":                  false,
		"10.0.0.9:4000":                   true,
		"[::ffff:10.0.0.9]:4000":          true,
		"[fd12:3456:789a:1::11]:4000":     true,
		"[fd12:3456:789a:1::11%en3]:4000": true,
		"[2001:db8::5%eth0]:4000":         true,
	} {
		if got := peers.contains(addr); got != want {
			t.Errorf("contains(%q) = %v, want %v", addr, got, want)
		}
	}
}

// parseTrustAddr keeps the zone only on an IPv6 link-local address.
func TestParseTrustAddr(t *testing.T) {
	for in, want := range map[string]string{
		"[fe80::5%en0]:4000":      "fe80::5%en0",
		"fe80::5%en0":             "fe80::5%en0",
		"[fe80::5]:4000":          "fe80::5",
		"[fd12::1%en0]:4000":      "fd12::1",
		"[2001:db8::1%eth0]:4000": "2001:db8::1",
		"[::ffff:10.0.0.1]:4000":  "10.0.0.1",
		"10.0.0.1":                "10.0.0.1",
	} {
		a, ok := parseTrustAddr(in)
		if !ok || a.String() != want {
			t.Errorf("parseTrustAddr(%q) = %v, %v; want %s", in, a, ok, want)
		}
	}
	if _, ok := parseTrustAddr("not-an-address"); ok {
		t.Error("parseTrustAddr accepted garbage")
	}
}

// fakeHost counts the interface lookups a test makes. configZones is what
// resolveZone (config parsing) knows, zone -> interface name; a test may add
// to it to bring an interface up.
type fakeHost struct {
	zoneLookups, hostLookups, resolves atomic.Int32
	configZones                        map[string]string
}

// fixedConnectors is an inviteRateLimits.connectors that always returns peers.
func fixedConnectors(peers connectorPeers) func() connectorPeers {
	return func() connectorPeers { return peers }
}

// ipNet is cidr ("2001:db8:1::2/64") as an interface address: the address
// with its prefix length.
func ipNet(t *testing.T, cidr string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

// withOnLinkIfaces describes this host's interfaces for the on-link prefix
// list and empties its cache, before and after the test.
func withOnLinkIfaces(t *testing.T, ifaces ...onLinkIface) {
	t.Helper()
	resetOnLink := func() {
		onLinkCache.mu.Lock()
		onLinkCache.at, onLinkCache.prefixes = time.Time{}, nil
		onLinkCache.mu.Unlock()
	}
	saved := hostOnLinkIfaces
	hostOnLinkIfaces = func() ([]onLinkIface, error) { return ifaces, nil }
	resetOnLink()
	t.Cleanup(func() {
		hostOnLinkIfaces = saved
		resetOnLink()
	})
}

// withFakeHost describes a host for the trust code: en0 (index 2) owns
// fe80::5 and 10.0.0.2, en1 (index 3) owns 192.0.2.1, and an up broadcast
// interface carries 2001:db8:1::2/64. Config parsing also accepts EN0 for
// en0 (case folding); the trust path does not. It counts interface lookups.
func withFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	fh := &fakeHost{configZones: map[string]string{"en0": "en0", "2": "en0", "EN0": "en0", "en1": "en1", "3": "en1"}}
	byZone := map[string][]netip.Addr{
		"en0": {netip.MustParseAddr("fe80::5"), netip.MustParseAddr("10.0.0.2")},
		"en1": {netip.MustParseAddr("192.0.2.1")},
	}
	trustNames := map[string]string{"en0": "en0", "2": "en0", "en1": "en1", "3": "en1"}
	savedResolve, savedZone, savedHost := resolveZone, zoneInterfaceAddrs, hostInterfaceAddrs
	resolveZone = func(zone string) (string, zoneMatch) {
		fh.resolves.Add(1)
		if n, ok := fh.configZones[zone]; ok {
			return n, zoneFound
		}
		return "", zoneUnknown
	}
	zoneInterfaceAddrs = func(zone string) ([]netip.Addr, error) {
		fh.zoneLookups.Add(1)
		n, ok := trustNames[zone]
		if !ok {
			return nil, fmt.Errorf("no interface %q", zone)
		}
		return byZone[n], nil
	}
	hostInterfaceAddrs = func() ([]netip.Addr, error) {
		fh.hostLookups.Add(1)
		return slices.Concat(byZone["en0"], byZone["en1"]), nil
	}
	withOnLinkIfaces(t, onLinkIface{flags: net.FlagUp | net.FlagBroadcast, addrs: []net.Addr{ipNet(t, "2001:db8:1::2/64")}})
	t.Cleanup(func() {
		resolveZone, zoneInterfaceAddrs, hostInterfaceAddrs = savedResolve, savedZone, savedHost
	})
	return fh
}

// A link-local peer is this host only when the interface its zone names
// (by name or numeric index) owns the address; the same address with another
// zone, or no zone, is not local. Other addresses match on any interface.
func TestRemoteAddrIsLocal_MismatchedZoneIsNotLocal(t *testing.T) {
	withFakeHost(t)
	for addr, want := range map[string]bool{
		"[fe80::5%en0]:4000": true,
		"[fe80::5%2]:4000":   true,
		"[fe80::5%en1]:4000": false,
		"[fe80::5%3]:4000":   false,
		"[fe80::5%en9]:4000": false,
		"[fe80::5]:4000":     false,
		"[fe80::6%en0]:4000": false,
		"10.0.0.2:4000":      true,
		"192.0.2.1:4000":     true,
		"10.0.0.3:4000":      false,
		"127.0.0.1:4000":     true,
	} {
		if got := remoteAddrIsLocal(addr); got != want {
			t.Errorf("remoteAddrIsLocal(%q) = %v, want %v", addr, got, want)
		}
	}
}

// S1: the local-peer check asks the kernel once per request, and not at all
// for a configured connector: a connector request makes no interface lookup,
// a link-local peer looks up only its own interface, and any other peer
// makes one whole-host lookup.
func TestTrustedClientIP_InterfaceLookups(t *testing.T) {
	fh := withFakeHost(t)
	connectors, rejected := parseConnectorPeers("10.0.0.9, fe80::9%en0")
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	trust := clientTrust{isLocalPeer: remoteAddrIsLocal, connectors: connectors}
	call := func(remote string) {
		req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
		req.RemoteAddr = remote
		req.Header.Set("CF-Connecting-IP", "203.0.113.5")
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		trustedClientIP(c, trust)
	}
	for _, tt := range []struct {
		remote     string
		zone, host int32
	}{
		{"10.0.0.9:4000", 0, 0},      // connector
		{"[fe80::9%en0]:4000", 0, 0}, // link-local connector
		{"10.0.0.50:4000", 0, 1},
		{"[fe80::7%en0]:4000", 1, 0},
	} {
		fh.zoneLookups.Store(0)
		fh.hostLookups.Store(0)
		call(tt.remote)
		if z, h := fh.zoneLookups.Load(), fh.hostLookups.Load(); z != tt.zone || h != tt.host {
			t.Errorf("%s: %d zone and %d host lookups, want %d and %d", tt.remote, z, h, tt.zone, tt.host)
		}
	}
}

// S2: the peer address the server sees always carries the interface NAME,
// so a numeric or wrong-case zone in TUNNEL_CONNECTOR_ADDRS is normalised to
// the name at parse time (it used to be accepted and never match), and a
// zone that names no interface is refused.
func TestParseConnectorPeers_NormalisesTheZone(t *testing.T) {
	withFakeHost(t)
	peers, rejected := parseConnectorPeers("fe80::5%2, fe80::6%EN0, fe80::7%9")
	if len(rejected) != 1 || rejected[0] != (connectorReject{"fe80::7%9", rejectUnknownZone}) {
		t.Fatalf("rejected=%v, want only fe80::7%%9 (no interface 9)", rejected)
	}
	for addr, want := range map[string]bool{
		"[fe80::5%en0]:4000": true,
		"[fe80::5%en1]:4000": false,
		"[fe80::6%en0]:4000": true,
		"[fe80::7%9]:4000":   false,
	} {
		if got := peers.contains(addr); got != want {
			t.Errorf("contains(%q) = %v, want %v", addr, got, want)
		}
	}
}

// The real zone lookup, on the loopback interface every host has: by name,
// by index, and by a wrong-case name all find it; a made-up name does not.
func TestLookupZoneInterface_Loopback(t *testing.T) {
	ifs, err := net.Interfaces()
	if err != nil {
		t.Skip(err)
	}
	var lo *net.Interface
	for i := range ifs {
		if ifs[i].Flags&net.FlagLoopback != 0 {
			lo = &ifs[i]
			break
		}
	}
	if lo == nil {
		t.Skip("no loopback interface")
	}
	for _, zone := range []string{lo.Name, strconv.Itoa(lo.Index), strings.ToUpper(lo.Name)} {
		if strings.ToUpper(lo.Name) == zone && zone != lo.Name {
			if _, err := net.InterfaceByName(zone); err == nil {
				continue // an interface really has that name
			}
		}
		if name, m := resolveZone(zone); m != zoneFound || name != lo.Name {
			t.Errorf("resolveZone(%q) = %q, %v; want %q", zone, name, m, lo.Name)
		}
	}
	if _, m := resolveZone("nosuchif0"); m != zoneUnknown {
		t.Error("resolveZone found an interface that does not exist")
	}
	// The trust path never folds case: a peer's zone is the exact name.
	if up := strings.ToUpper(lo.Name); up != lo.Name {
		if _, err := net.InterfaceByName(up); err != nil {
			if _, err := lookupZoneInterface(up); err == nil {
				t.Errorf("lookupZoneInterface(%q) matched ignoring case", up)
			}
		}
	}
	if ifi, err := lookupZoneInterface(strconv.Itoa(lo.Index)); err != nil || ifi.Name != lo.Name {
		t.Errorf("lookupZoneInterface(index %d) = %v, %v; want %s", lo.Index, ifi, err, lo.Name)
	}
	addrs, err := zoneInterfaceAddrs(lo.Name)
	if err != nil || len(addrs) == 0 {
		t.Fatalf("zoneInterfaceAddrs(%q) = %v, %v; want the loopback addresses", lo.Name, addrs, err)
	}
	for _, a := range addrs {
		if !a.IsLoopback() && !a.IsLinkLocalUnicast() {
			t.Errorf("loopback interface address %s is neither loopback nor link-local", a)
		}
	}
}

// NIT1: a home IPv6 LAN usually runs on the ISP's global /64, not ULA. A
// peer inside an on-link prefix of this host is a LAN peer: keyed on its
// full address, with its /64 aggregate, direct when it sends no Cloudflare
// markers. A global address outside every on-link prefix stays public.
func TestInviteClientKey_OnLinkGlobalIPv6IsLAN(t *testing.T) {
	withFakeHost(t)
	withFreshUnlistedWarnings(t)
	h := newInviteLimitHarness(t, false)
	for _, tt := range []struct {
		peer string
		want inviteClient
	}{
		{"[2001:db8:1::50]", inviteClient{key: "invite-client:2001:db8:1::50", prefixKey: "invite-direct-lan64:2001:db8:1::/64", totalKey: inviteDirectLANTotalKey, direct: true}},
		{"[2001:db8:2::50]", inviteClient{key: "invite-client:2001:db8:2::/64"}},
	} {
		req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
		req.RemoteAddr = tt.peer + ":4000"
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		if got, ok := h.limits.inviteClientKey(c); !ok || got != tt.want {
			t.Errorf("%s: inviteClientKey = %+v, %v; want %+v", tt.peer, got, ok, tt.want)
		}
	}
}

// NIT4: an unlisted connector on a link-local address is warned about once
// per /64 PER INTERFACE: fe80::/64 on en0 and on en1 are different links, so
// a connector on en1 is not silenced by one already seen on en0.
func TestWarnUnlistedConnector_LinkLocalPerInterface(t *testing.T) {
	withFreshUnlistedWarnings(t)
	warnUnlistedConnector("[fe80::5%en0]:4000")
	warnUnlistedConnector("[fe80::6%en0]:4000") // same /64, same link
	warnUnlistedConnector("[fe80::5%en1]:4000")
	if n := unlistedConnectors.len(); n != 2 {
		t.Fatalf("%d warnings remembered, want 2 (one per interface)", n)
	}
	a, _ := parseTrustAddr("fe80::5%en1")
	if msg := unlistedConnectorMessage(a, "fe80::/64 on interface en1"); !strings.Contains(msg, "peer fe80::5%en1") || !strings.Contains(msg, "all addresses in fe80::/64 on interface en1") {
		t.Fatalf("message: %s", msg)
	}
}

// N-c: when the unlisted-connector warning is keyed by a /64 it says so.
func TestUnlistedConnectorMessage_NamesThePrefix(t *testing.T) {
	a := netip.MustParseAddr("fd12:3456:789a:1::11")
	if msg := unlistedConnectorMessage(a, "fd12:3456:789a:1::/64"); !strings.Contains(msg, "all addresses in fd12:3456:789a:1::/64") || !strings.Contains(msg, "peer fd12:3456:789a:1::11") {
		t.Fatalf("prefix-keyed message: %s", msg)
	}
	if msg := unlistedConnectorMessage(netip.MustParseAddr("10.0.0.12"), ""); strings.Contains(msg, "addresses in") {
		t.Fatalf("an address-keyed message mentions a prefix: %s", msg)
	}
}

// N-d: the accept handler records the client address through
// requestClientIP, which, unlike c.ClientIP(), is not "" for a zoned
// link-local peer.
func TestRequestClientIP_ZonedPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.RemoteAddr = "[fe80::1%en0]:4000"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	if got := requestClientIP(c); got != "fe80::1" {
		t.Fatalf("requestClientIP = %q, want fe80::1", got)
	}
}

// withFreshUnlistedWarnings gives the test its own unlisted-connector warning
// set, so the rotations below neither fill nor depend on the package one.
func withFreshUnlistedWarnings(t *testing.T) {
	saved := unlistedConnectors
	unlistedConnectors = newOnceSet(unlistedConnectorWarnCap)
	t.Cleanup(func() { unlistedConnectors = saved })
}

// N-a: how inviteClientKey keys each kind of peer: a direct LAN peer gets
// the direct total (and its /64 aggregate when it is IPv6), an unlisted Cf-marked LAN peer its /64 or /24 aggregate and the
// unlisted total, and a public client none of them.
func TestInviteClientKey_LANAggregates(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	withFreshUnlistedWarnings(t)
	cf := map[string]string{"Cf-Ray": "x"}
	tests := []struct {
		name, peer string
		hdr        map[string]string
		want       inviteClient
	}{
		{"direct IPv4", "10.0.0.50", nil, inviteClient{key: "invite-client:10.0.0.50", totalKey: inviteDirectLANTotalKey, direct: true}},
		{"direct ULA", "[fd12:3456:789a:1::5]", nil, inviteClient{key: "invite-client:fd12:3456:789a:1::5", prefixKey: "invite-direct-lan64:fd12:3456:789a:1::/64", totalKey: inviteDirectLANTotalKey, direct: true}},
		{"unlisted IPv4", "10.0.7.9", cf, inviteClient{key: "invite-client:10.0.7.9", prefixKey: "invite-unlisted-lan:10.0.7.0/24", totalKey: inviteUnlistedLANTotalKey, unlisted: true}},
		{"unlisted ULA", "[fd12:3456:789a:2::9]", cf, inviteClient{key: "invite-client:fd12:3456:789a:2::9", prefixKey: "invite-unlisted-lan:fd12:3456:789a:2::/64", totalKey: inviteUnlistedLANTotalKey, unlisted: true}},
		{"public IPv4", "198.51.100.7", nil, inviteClient{key: "invite-client:198.51.100.7"}},
		{"public IPv4 with Cf headers", "198.51.100.7", cf, inviteClient{key: "invite-client:198.51.100.7"}},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
		req.RemoteAddr = tt.peer + ":4000"
		for k, v := range tt.hdr {
			req.Header.Set(k, v)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		if got, ok := h.limits.inviteClientKey(c); !ok || got != tt.want {
			t.Errorf("%s: inviteClientKey = %+v, %v; want %+v", tt.name, got, ok, tt.want)
		}
	}
}

// A direct LAN peer is NOT exempt from the global layer (it was until
// 2026-10-21). Once a public client has spent the whole global budget, a
// direct LAN caller (IPv4 and ULA, no Cloudflare markers) is refused like an
// unlisted Cf-marked LAN host, even with its own per-client burst untouched.
func TestInviteRateLimit_DirectLANPeerIsNotExemptFromTheGlobalLayer(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	withFreshUnlistedWarnings(t)
	h.limits.global = servermiddleware.NewIPRateLimiter(1, 1)
	if w := h.get(inviteTestToken(1), "198.51.100.30", nil); w.Code != http.StatusOK {
		t.Fatalf("the public client spending the global budget: %d, want 200", w.Code)
	}
	if w := h.get(inviteTestToken(2), "198.51.100.31", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a second public client: %d, want 429 (global spent)", w.Code)
	}
	for d, peer := range []string{"10.0.0.50", "[fd12:3456:789a:1::50]", "127.0.0.1"} {
		if w := h.get(inviteTestToken(100*(d+1)), peer, nil); w.Code != http.StatusTooManyRequests {
			t.Fatalf("direct LAN peer %s with the global spent: %d, want 429", peer, w.Code)
		}
	}
	if w := h.get(inviteTestToken(900), "10.0.0.60", map[string]string{"Cf-Ray": "x"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("an unlisted Cf-marked LAN host with the global spent: %d, want 429", w.Code)
	}
}

// The other direction: direct LAN traffic spends the global layer. With a
// global budget of three, three direct peers are admitted and the fourth
// caller, a public client, is refused.
func TestInviteRateLimit_DirectLANTrafficSpendsTheGlobalLayer(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	h.limits.global = servermiddleware.NewIPRateLimiter(1, 3)
	for i, peer := range []string{"10.0.0.50", "10.0.0.51", "[fd12:3456:789a:1::52]"} {
		if w := h.get(inviteTestToken(i), peer, nil); w.Code != http.StatusOK {
			t.Fatalf("direct LAN peer %s under the global budget: %d, want 200", peer, w.Code)
		}
	}
	if w := h.get(inviteTestToken(10), "198.51.100.30", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a public client after three direct requests: %d, want 429 (direct traffic spends the global layer)", w.Code)
	}
}

// One direct LAN host rotating through 100 IPv4 aliases (no IPv4 prefix
// aggregate: a /24 would be shared by the whole home network) gets a fresh
// per-client bucket each time, but all direct traffic shares the direct
// total, so it is capped there. That total is half the global layer, so the
// other half is still there for public clients.
func TestInviteRateLimit_DirectIPv4AliasRotationIsCappedByTheDirectTotal(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	admitted := 0
	for i := range 100 {
		peer := fmt.Sprintf("10.0.%d.%d", i/200, i%200+1)
		for j := range 2 {
			if w := h.get(inviteTestToken(i*2+j), peer, nil); w.Code == http.StatusOK {
				admitted++
			}
		}
	}
	if admitted < inviteDirectLANTotalRateBurst || admitted > inviteDirectLANTotalRateBurst+1 {
		t.Fatalf("direct IPv4 alias rotation: %d of 200 admitted, want the direct total's burst of %d", admitted, inviteDirectLANTotalRateBurst)
	}
	for i := range inviteGlobalRateBurst - inviteDirectLANTotalRateBurst - 5 {
		if w := h.get(inviteTestToken(1000+i), fmt.Sprintf("198.51.100.%d", i+1), nil); w.Code != http.StatusOK {
			t.Fatalf("public client %d after the direct rotation: %d, want 200 (direct traffic can spend at most half the global layer)", i, w.Code)
		}
	}
}

// N-a: a Cf-marked LAN host rotating IPv4 aliases inside one /24 is capped
// by that /24's unlisted aggregate, and spread across many /24s by the
// unlisted total; the direct peer in the same /24 is still admitted.
func TestInviteRateLimit_CfMarkedIPv4AliasRotationIsCapped(t *testing.T) {
	withFreshUnlistedWarnings(t)
	cf := map[string]string{"Cf-Ray": "x"}

	h := newInviteLimitHarness(t, false)
	admitted := 0
	for i := range 100 {
		if w := h.get(inviteTestToken(i), fmt.Sprintf("10.0.0.%d", i+100), cf); w.Code == http.StatusOK {
			admitted++
		}
	}
	if admitted < inviteUnlistedLANPrefixRateBurst || admitted > inviteUnlistedLANPrefixRateBurst+1 {
		t.Fatalf("Cf-marked rotation within one /24: %d of 100 admitted, want the /24 aggregate's burst of %d", admitted, inviteUnlistedLANPrefixRateBurst)
	}
	if w := h.get(inviteTestToken(500), "10.0.0.50", nil); w.Code != http.StatusOK {
		t.Fatalf("the direct peer in that /24 after the rotation: %d, want 200 (separate buckets)", w.Code)
	}

	h = newInviteLimitHarness(t, false)
	admitted = 0
	for i := range 100 {
		if w := h.get(inviteTestToken(i), fmt.Sprintf("10.0.%d.9", i), cf); w.Code == http.StatusOK {
			admitted++
		}
	}
	if admitted < inviteUnlistedLANTotalRateBurst || admitted > inviteUnlistedLANTotalRateBurst+1 {
		t.Fatalf("Cf-marked rotation across 100 /24s: %d admitted, want the unlisted total's burst of %d", admitted, inviteUnlistedLANTotalRateBurst)
	}
}

// N-a: a Cf-marked LAN host rotating across many ULA /64s used to get a
// fresh /64 aggregate each time, so 60 /64s drained the global layer for
// every guest. The unlisted total now caps it, well below the global burst,
// so public clients and the owner's direct peer are still admitted.
func TestInviteRateLimit_CfMarkedRotationAcrossSlash64sCannotDrainTheGlobal(t *testing.T) {
	withFreshUnlistedWarnings(t)
	h := newInviteLimitHarness(t, false)
	admitted := 0
	for i := range 60 {
		for j := range 3 {
			peer := fmt.Sprintf("[fd12:3456:%x::%x]", 0x100+i, j+1)
			if w := h.get(inviteTestToken(i*3+j), peer, map[string]string{"Cf-Ray": "x"}); w.Code == http.StatusOK {
				admitted++
			}
		}
	}
	if admitted < inviteUnlistedLANTotalRateBurst || admitted > inviteUnlistedLANTotalRateBurst+1 {
		t.Fatalf("Cf-marked rotation across 60 /64s: %d of 180 admitted, want the unlisted total's burst of %d", admitted, inviteUnlistedLANTotalRateBurst)
	}
	// What is left of the global burst is still there for everyone else.
	for i := range inviteGlobalRateBurst - inviteUnlistedLANTotalRateBurst - 5 {
		if w := h.get(inviteTestToken(1000+i), fmt.Sprintf("198.51.100.%d", i+1), nil); w.Code != http.StatusOK {
			t.Fatalf("public client %d after the rotation: %d, want 200 (global not drained)", i, w.Code)
		}
	}
	if w := h.get(inviteTestToken(2000), "[fd12:3456:789a:1::1]", nil); w.Code != http.StatusOK {
		t.Fatalf("the owner's direct peer after the rotation: %d, want 200", w.Code)
	}
}

// NIT2: on HTTP/2 an oversize accept body is still answered 413, but the
// connection is NOT closed: Connection: close there would become a GOAWAY
// for every stream the browser has open, and HTTP/2 has no drain to
// short-circuit (the unread stream is reset when the handler returns). The
// next request reuses the connection.
func TestInviteRateLimit_OversizeOnHTTP2KeepsTheConnection(t *testing.T) {
	h := newInviteLimitHarness(t, false)
	srv := httptest.NewUnstartedServer(h.engine)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := srv.Client()

	post := func(body string) (*http.Response, bool) {
		t.Helper()
		reused := false
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodPost, srv.URL+"/accept", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp, reused
	}
	resp, _ := post(strings.Repeat("x", inviteAcceptBodyLimit+500))
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: %s %d, want HTTP/2 413", resp.Proto, resp.StatusCode)
	}
	resp, reused := post(`{"token":"` + inviteTestToken(1) + `","password":"x"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("next request: %d, want 200", resp.StatusCode)
	}
	if !reused {
		t.Fatal("the next request opened a new connection: the 413 closed the HTTP/2 connection (GOAWAY)")
	}
}

// Round 8 S1: only a /64 or narrower prefix of a global IPv6 address on an
// up, broadcast-capable, non-point-to-point interface counts as on-link.
func TestOnLinkPrefixesOf(t *testing.T) {
	bcast := net.FlagUp | net.FlagBroadcast
	got := onLinkPrefixesOf([]onLinkIface{
		{flags: bcast, addrs: []net.Addr{ipNet(t, "2001:db8:1::2/64"), ipNet(t, "2001:db8:1:9::2/80")}},
		{flags: bcast, addrs: []net.Addr{ipNet(t, "2001:db8:2::2/48")}},                             // too wide
		{flags: net.FlagUp | net.FlagPointToPoint, addrs: []net.Addr{ipNet(t, "2001:db8:3::2/64")}}, // a tunnel
		{flags: net.FlagUp | net.FlagBroadcast | net.FlagPointToPoint, addrs: []net.Addr{ipNet(t, "2001:db8:4::2/64")}},
		{flags: net.FlagUp, addrs: []net.Addr{ipNet(t, "2001:db8:5::2/64")}},        // not broadcast
		{flags: net.FlagBroadcast, addrs: []net.Addr{ipNet(t, "2001:db8:6::2/64")}}, // down
		{flags: bcast, addrs: []net.Addr{ipNet(t, "fd12::2/64"), ipNet(t, "2001:db8:7::2/128"), ipNet(t, "198.51.100.2/24")}},
	})
	want := []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64"), netip.MustParsePrefix("2001:db8:1:9::/80")}
	if !slices.Equal(got, want) {
		t.Fatalf("onLinkPrefixesOf = %v, want %v", got, want)
	}
}

// Round 8 S1, end to end through isLANAddr: a broadcast /64 is LAN; a /48
// on-link prefix and a point-to-point interface's /64 are not.
func TestIsLANAddr_OnLinkPrefixRules(t *testing.T) {
	withOnLinkIfaces(t,
		onLinkIface{flags: net.FlagUp | net.FlagBroadcast, addrs: []net.Addr{ipNet(t, "2001:db8:1::2/64"), ipNet(t, "2001:db8:2::2/48")}},
		onLinkIface{flags: net.FlagUp | net.FlagPointToPoint, addrs: []net.Addr{ipNet(t, "2001:db8:3::2/64")}},
	)
	for in, want := range map[string]bool{
		"2001:db8:1::50":   true,
		"2001:db8:2:5::50": false,
		"2001:db8:2::50":   false,
		"2001:db8:3::50":   false,
		"2001:db8:9::50":   false,
	} {
		if got := isLANAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("isLANAddr(%s) = %v, want %v", in, got, want)
		}
	}
}

// Round 8 NIT3: a tunnel visitor's address inside a wide on-link prefix
// (here a /48) is not LAN, so visitors there are grouped by /64 as public
// IPv6 is, not given a bucket per address. Inside a counted /64 a visitor
// is keyed on its full address, which only a device on that segment has.
func TestInviteClientKey_TunnelVisitorInAWidePrefixIsGroupedBy64(t *testing.T) {
	withOnLinkIfaces(t, onLinkIface{flags: net.FlagUp | net.FlagBroadcast, addrs: []net.Addr{ipNet(t, "2001:db8:1::2/48"), ipNet(t, "2001:db8:9::2/64")}})
	h := newInviteLimitHarness(t, false)
	peers, rejected := parseConnectorPeers("10.0.0.9")
	if len(rejected) != 0 {
		t.Fatal(rejected)
	}
	h.limits.connectors = fixedConnectors(peers)
	for visitor, want := range map[string]string{
		"2001:db8:1:5::7": "invite-client:2001:db8:1:5::/64",
		"2001:db8:1:6::8": "invite-client:2001:db8:1:6::/64",
		"2001:db8:9::7":   "invite-client:2001:db8:9::7",
	} {
		req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
		req.RemoteAddr = "10.0.0.9:4000"
		for k, v := range cfHeaders(visitor) {
			req.Header.Set(k, v)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		if ic, ok := h.limits.inviteClientKey(c); !ok || ic != (inviteClient{key: want}) {
			t.Errorf("visitor %s: inviteClientKey = %+v, %v; want key %s only", visitor, ic, ok, want)
		}
	}
}

// Round 8 NIT1: config zones match the exact name first, then a numeric
// index, then (config only) a unique case-insensitive name; two names that
// fold to the zone with neither exact is ambiguous. The trust path never
// folds.
func TestMatchZone(t *testing.T) {
	ifs := []net.Interface{{Name: "eth0", Index: 2}, {Name: "Eth0", Index: 3}, {Name: "wlan0", Index: 4}, {Name: "4", Index: 9}}
	for _, tt := range []struct {
		zone string
		fold bool
		name string
		want zoneMatch
	}{
		{"eth0", true, "eth0", zoneFound},
		{"Eth0", true, "Eth0", zoneFound}, // exact beats folding
		{"ETH0", true, "", zoneAmbiguous},
		{"WLAN0", true, "wlan0", zoneFound},
		{"WLAN0", false, "", zoneUnknown},
		{"2", false, "eth0", zoneFound},
		{"4", false, "4", zoneFound}, // an interface named "4" beats index 4
		{"99", true, "", zoneUnknown},
		{"nosuch", true, "", zoneUnknown},
	} {
		if name, m := matchZone(tt.zone, ifs, tt.fold); name != tt.name || m != tt.want {
			t.Errorf("matchZone(%q, fold=%v) = %q, %v; want %q, %v", tt.zone, tt.fold, name, m, tt.name, tt.want)
		}
	}
}

// Round 8 NIT1: an ambiguous config zone is refused, not guessed.
func TestParseConnectorPeers_AmbiguousZoneIsRefused(t *testing.T) {
	saved := resolveZone
	resolveZone = func(zone string) (string, zoneMatch) {
		return matchZone(zone, []net.Interface{{Name: "eth0", Index: 2}, {Name: "Eth0", Index: 3}}, true)
	}
	t.Cleanup(func() { resolveZone = saved })
	peers, rejected := parseConnectorPeers("fe80::5%ETH0, fe80::6%Eth0")
	if len(rejected) != 1 || rejected[0] != (connectorReject{"fe80::5%ETH0", rejectAmbiguousZone}) {
		t.Fatalf("rejected=%v, want fe80::5%%ETH0 as ambiguous", rejected)
	}
	if !peers.contains("[fe80::6%Eth0]:4000") || peers.contains("[fe80::6%eth0]:4000") {
		t.Fatalf("peers=%v, want fe80::6 on Eth0 only", peers)
	}
}

// Round 8 NIT2: an entry refused because its interface does not exist yet
// becomes trusted once the interface appears, without a restart: the parse
// is retried after connectorRetryAfter. A parse with no unknown-zone refusal
// is never retried. The invite limiter reads the connectors on every
// request, so the new entry applies at once.
func TestConnectorCache_RetriesAnUnknownZone(t *testing.T) {
	fh := withFakeHost(t)
	var cc connectorCache
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if peers := cc.get("fe80::5%en5", t0); len(peers) != 0 {
		t.Fatalf("before en5 exists: %v, want none", peers)
	}
	fh.configZones["en5"] = "en5" // the interface comes up
	if peers := cc.get("fe80::5%en5", t0.Add(connectorRetryAfter/2)); len(peers) != 0 {
		t.Fatalf("within the retry window: %v, want the cached refusal", peers)
	}
	peers := cc.get("fe80::5%en5", t0.Add(connectorRetryAfter))
	if !peers.contains("[fe80::5%en5]:4000") {
		t.Fatalf("after the retry window: %v, want fe80::5%%en5 trusted", peers)
	}

	var ok connectorCache
	ok.get("fe80::5%en0", t0)
	before := fh.resolves.Load()
	ok.get("fe80::5%en0", t0.Add(10*connectorRetryAfter))
	if fh.resolves.Load() != before {
		t.Fatal("a parse with nothing refused for an unknown zone was retried")
	}

	h := newInviteLimitHarness(t, false)
	var current connectorPeers
	h.limits.connectors = func() connectorPeers { return current }
	keyOf := func() string {
		req := httptest.NewRequest(http.MethodGet, "/invite/x", nil)
		req.RemoteAddr = "[fe80::5%en5]:4000"
		for k, v := range cfHeaders("203.0.113.44") {
			req.Header.Set(k, v)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		ic, _ := h.limits.inviteClientKey(c)
		return ic.key
	}
	withFreshUnlistedWarnings(t)
	if k := keyOf(); k != "invite-client:fe80::5" {
		t.Fatalf("before the entry is trusted: key %q, want the peer's own", k)
	}
	current = peers
	if k := keyOf(); k != "invite-client:203.0.113.44" {
		t.Fatalf("after: key %q, want the visitor's (connectors read per request)", k)
	}
}
