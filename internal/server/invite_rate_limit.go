// file: internal/server/invite_rate_limit.go
// version: 1.1.0
// guid: 660fd56e-7850-420a-8302-5290684460cf
// last-edited: 2026-10-19

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Public invite endpoints (GET /auth/invite/:token and POST
// /auth/accept-invite) are bounded by three layers and a request must pass all
// three. The token space is 256 bits, so this is about load and about
// protecting one guest's invitation from another caller, not about guessing.
//
// Production sits behind a Cloudflare tunnel and the router trusts no proxy
// headers (SetTrustedProxies(nil), see NewServer), so c.ClientIP() is the
// tunnel's local address for every request: a limiter keyed on it alone is ONE
// bucket shared by everyone, and a single caller could exhaust it for every
// guest. The layers therefore do not rely on the client address:
//
//  1. per token: a token's lookups and accept attempts, from anywhere, share
//     one small bucket (keyed by a hash of the token, never the raw token).
//  2. per client: keyed by CF-Connecting-IP when, and only when, the request
//     provably came through Cloudflare (see inviteClientKey); otherwise by
//     c.ClientIP().
//  3. global: a generous ceiling across every token and client, so
//     enumeration spread over many tokens or many clients is still bounded.
//
// Real use is one page load and one accept per guest.
//
// Tuning constraint: the idle TTL of a limiter (15 minutes) times its request
// rate must stay below the limiter's tracked-entry cap (10,000), or a flood of
// distinct keys could evict a victim's bucket and hand it a fresh burst. The
// global ceiling below admits at most inviteGlobalRatePerMinute new keys a
// minute (4,500 per idle TTL), which is why it is not larger.
const (
	// Per client (layer 2). Name kept from the single shared limiter this
	// replaced.
	inviteAuthRatePerMinute = 20
	inviteAuthRateBurst     = 10

	// Per token (layer 1): one guest opens the link a few times and accepts
	// once; a handful of password retries is the realistic worst case.
	inviteTokenRatePerMinute = 6
	inviteTokenRateBurst     = 8

	// Global (layer 3), across all tokens and clients.
	inviteGlobalRatePerMinute = 300
	inviteGlobalRateBurst     = 100
)

// inviteAcceptBodyLimit caps an accept-invite body. The real body is
// {"token": 64 hex chars, "password": ...}, so 4 KiB leaves room for any
// sane password; a larger body is refused with 413 before the handler runs,
// so the limiter always reads the whole body the handler will use.
const inviteAcceptBodyLimit = 4 << 10

// inviteFallbackTokenKey is the one per-token bucket shared by every request
// whose token is empty or whose body does not parse, so a malformed body
// cannot skip the per-token layer.
const inviteFallbackTokenKey = "invite-token:unparsed"

// acceptInviteRequestKey is the gin context key under which the accept-invite
// limiter stashes the request it parsed; handleAcceptInvite uses exactly that
// request instead of parsing the body again.
const acceptInviteRequestKey = "invite.acceptRequest"

// acceptInviteRequest is the parsed accept-invite body.
type acceptInviteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// parsedAcceptInvite is what readAcceptInviteBody found.
type parsedAcceptInvite struct {
	req      acceptInviteRequest
	parsed   bool // the body was one valid JSON object
	empty    bool // there was no body
	tooLarge bool // over inviteAcceptBodyLimit
}

// readAcceptInviteBody reads and parses the accept-invite body once per
// request and stashes the result on c; later calls return the stash. Both the
// limiter and handleAcceptInvite call it, so they act on the same bytes.
func readAcceptInviteBody(c *gin.Context) parsedAcceptInvite {
	if v, ok := c.Get(acceptInviteRequestKey); ok {
		if p, ok := v.(parsedAcceptInvite); ok {
			return p
		}
	}
	var p parsedAcceptInvite
	if c.Request.Body == nil {
		p.empty = true
	} else {
		buf, err := io.ReadAll(io.LimitReader(c.Request.Body, inviteAcceptBodyLimit+1))
		switch {
		case len(buf) > inviteAcceptBodyLimit:
			p.tooLarge = true
		case err == nil && len(bytes.TrimSpace(buf)) == 0:
			p.empty = true
		case err == nil:
			p.parsed = json.Unmarshal(buf, &p.req) == nil
		}
		// The handler reads the stash, never the body; leave it empty.
		c.Request.Body = io.NopCloser(bytes.NewReader(nil))
	}
	c.Set(acceptInviteRequestKey, p)
	return p
}

// inviteGlobalKey is the single bucket of the global layer.
const inviteGlobalKey = "invite:global"

// inviteRateLimits holds the three limiters of the public invite endpoints.
type inviteRateLimits struct {
	perToken  *servermiddleware.IPRateLimiter
	perClient *servermiddleware.IPRateLimiter
	global    *servermiddleware.IPRateLimiter
	// isLocalPeer reports whether the request's peer address is this host
	// (where the tunnel connector runs). Injectable for tests.
	isLocalPeer func(remoteAddr string) bool
}

// newInviteRateLimits builds the limiters and starts their idle sweepers on ctx.
func newInviteRateLimits(ctx context.Context) *inviteRateLimits {
	l := &inviteRateLimits{
		perToken:    servermiddleware.NewIPRateLimiter(inviteTokenRatePerMinute, inviteTokenRateBurst),
		perClient:   servermiddleware.NewIPRateLimiter(inviteAuthRatePerMinute, inviteAuthRateBurst),
		global:      servermiddleware.NewIPRateLimiter(inviteGlobalRatePerMinute, inviteGlobalRateBurst),
		isLocalPeer: remoteAddrIsLocal,
	}
	l.perToken.Start(ctx)
	l.perClient.Start(ctx)
	l.global.Start(ctx)
	return l
}

// inviteTokenKey is the per-token bucket key: a hash prefix, so raw tokens
// never sit in limiter maps. An empty token has no key (the layer is skipped;
// the client and global layers still apply).
func inviteTokenKey(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return "invite-token:" + hex.EncodeToString(sum[:16])
}

// inviteClientKey returns the per-client bucket key for the request.
//
// CF-Connecting-IP is set by Cloudflare's edge and is trustworthy only on a
// request that Cloudflare handled, so it is believed only when BOTH hold:
//
//   - the peer is this host (RemoteAddr is loopback or one of this host's own
//     addresses), i.e. the request came in through the tunnel connector and
//     not straight from a remote caller; and
//   - the request carried a Cf-Access-Jwt-Assertion that CloudflareAccessAuth
//     verified (human login or service token), proving it passed Access.
//
// Anything else, including a spoofed CF-Connecting-IP, falls back to
// c.ClientIP() (the tunnel address, so effectively one shared bucket). The
// public invite routes may sit under a Cloudflare Access bypass application
// that attaches no assertion; for those requests this layer degrades to
// c.ClientIP() by design, and the per-token and global layers carry the
// protection.
func (l *inviteRateLimits) inviteClientKey(c *gin.Context) string {
	if servermiddleware.CFAccessVerified(c) && l.isLocalPeer(c.Request.RemoteAddr) {
		if ip := net.ParseIP(strings.TrimSpace(c.GetHeader("CF-Connecting-IP"))); ip != nil {
			return "invite-client:" + ip.String()
		}
	}
	return "invite-client:" + c.ClientIP()
}

// remoteAddrIsLocal reports whether addr ("host:port") is a loopback address or
// an address assigned to one of this host's interfaces.
func remoteAddrIsLocal(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// middleware returns the Gin middleware enforcing all three layers.
// tokenOf extracts the invite token from the request; "" (missing or
// unparseable) uses the shared fallback bucket. ok=false means tokenOf has
// already answered the request (413) and the chain stops.
//
// Order: client, then token, then global. A caller that is already limited
// never spends a token or global slot, so one noisy client or one hammered
// token cannot drain the shared ceiling. Every layer refuses with the same
// status and body, so a caller cannot tell which layer tripped.
func (l *inviteRateLimits) middleware(tokenOf func(*gin.Context) (token string, ok bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok := l.perClient.AllowKey(l.inviteClientKey(c))
		if ok {
			token, proceed := tokenOf(c)
			if !proceed {
				c.Abort()
				return
			}
			key := inviteTokenKey(token)
			if key == "" {
				key = inviteFallbackTokenKey
			}
			ok = l.perToken.AllowKey(key)
		}
		if ok {
			ok = l.global.AllowKey(inviteGlobalKey)
		}
		if !ok {
			httputil.RespondWithError(c, http.StatusTooManyRequests, "rate limit exceeded", "TOO_MANY_REQUESTS")
			c.Abort()
			return
		}
		c.Next()
	}
}

// inviteTokenFromParam reads the token of GET /auth/invite/:token.
func inviteTokenFromParam(c *gin.Context) (string, bool) { return c.Param("token"), true }

// inviteTokenFromBody reads the token of POST /auth/accept-invite through
// readAcceptInviteBody, which stashes the parsed body for handleAcceptInvite.
// A body over inviteAcceptBodyLimit is answered 413 here. A missing or
// malformed body yields "" (the fallback bucket; the handler answers it).
func inviteTokenFromBody(c *gin.Context) (string, bool) {
	p := readAcceptInviteBody(c)
	if p.tooLarge {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
		return "", false
	}
	if !p.parsed {
		return "", true
	}
	return p.req.Token, true
}
