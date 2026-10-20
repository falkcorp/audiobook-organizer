// file: internal/server/invite_rate_limit.go
// version: 1.6.0
// guid: 660fd56e-7850-420a-8302-5290684460cf
// last-edited: 2026-10-20

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

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Public invite endpoints (GET /auth/invite/:token and POST
// /auth/accept-invite) are bounded by three layers and a request must pass all
// three. The token space is 256 bits, so this is about load and about
// protecting one guest's invitation from another caller, not about guessing.
//
// Production sits behind a Cloudflare tunnel whose connectors (cloudflared)
// run on other LAN hosts and connect to the service from their own private
// addresses, and the router trusts no proxy headers (SetTrustedProxies(nil),
// see NewServer), so c.ClientIP() is a connector's address for every tunnel
// request: a limiter keyed on it alone is one bucket per connector, shared by
// every visitor, and a single caller could exhaust it for every guest. The
// invite routes also bypass Cloudflare Access (a guest accepts before being
// allowed in), so there is no Access assertion to lean on. The layers
// therefore do not rely on c.ClientIP():
//
//  1. per token: a token's lookups and accept attempts, from anywhere, share
//     one small bucket (keyed by a hash of the token, never the raw token).
//  2. per client: keyed by trustedClientIP, i.e. CF-Connecting-IP when it can
//     be believed (the peer is a configured tunnel connector, or the request
//     carries a verified Access assertion and the peer is this host),
//     otherwise the peer's own address; IPv6 clients are grouped by /64
//     (clientPrefixKey). A request whose client cannot be established SKIPS
//     this layer: a connector or local peer with no trusted header, or a peer
//     that is not a configured connector but carries Cloudflare markers
//     (tunnel traffic from an unlisted connector). A key taken from an
//     unproven header would let a caller pick its own bucket, and one shared
//     bucket would let one abuser lock out every guest. Layers 1 and 3 still
//     apply to it. TUNNEL_CONNECTOR_ADDRS must therefore list every connector
//     for this layer to apply to tunnel traffic; with it unset the layer is
//     off for the tunnel (logged once at start-up).
//  3. global: a generous ceiling across every token and client, so
//     enumeration spread over many tokens or many clients is still bounded.
//
// Real use is one page load and one accept per guest.
//
// Tuning constraint: the number of new keys a limiter can be made to track
// within its idle TTL (15 minutes) must stay below its tracked-entry cap
// (10,000), or a flood of distinct keys could evict a victim's bucket and hand
// it a fresh burst. The middleware creates per-client and per-token entries
// only for requests the global layer has already admitted, so each of those
// limiters gains at most inviteGlobalRateBurst + 15 *
// inviteGlobalRatePerMinute = 4,600 keys per idle TTL, and the global limiter
// has one key. That is why the global ceiling is not larger.
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
	// isLocalPeer reports whether an address is this host. Injectable for tests.
	isLocalPeer func(remoteAddr string) bool
	// connectors are the configured tunnel connector addresses (see
	// trustedClientIP).
	connectors connectorPeers
}

var inviteLimitLog = logger.New("server.invite-rate-limit")

// newInviteRateLimits builds the limiters and starts their idle sweepers on ctx.
func newInviteRateLimits(ctx context.Context) *inviteRateLimits {
	l := &inviteRateLimits{
		perToken:    servermiddleware.NewIPRateLimiter(inviteTokenRatePerMinute, inviteTokenRateBurst),
		perClient:   servermiddleware.NewIPRateLimiter(inviteAuthRatePerMinute, inviteAuthRateBurst),
		global:      servermiddleware.NewIPRateLimiter(inviteGlobalRatePerMinute, inviteGlobalRateBurst),
		isLocalPeer: remoteAddrIsLocal,
		connectors:  tunnelConnectorPeers(),
	}
	if len(l.connectors) == 0 {
		inviteLimitLog.Info("invite per-client rate limit is off for tunnel traffic: TUNNEL_CONNECTOR_ADDRS does not list the tunnel connectors, so no visitor address can be established; the per-token and global limits still apply")
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

// inviteClientKey returns the per-client bucket key for the request, and false
// when no trusted client address can be established (see trustedClientIP), in
// which case the per-client layer is skipped for the request. The key is never
// derived from an unproven header. IPv6 clients share one bucket per /64
// (clientPrefixKey), as the bootstrap limiter keys them, so one host cannot
// rotate through its /64 for fresh buckets.
func (l *inviteRateLimits) inviteClientKey(c *gin.Context) (string, bool) {
	ip, ok := trustedClientIP(c, clientTrust{isLocalPeer: l.isLocalPeer, connectors: l.connectors})
	if !ok {
		return "", false
	}
	key, ok := clientPrefixKey(ip)
	if !ok {
		return "", false
	}
	return "invite-client:" + key, true
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
// Order:
//  1. the client and token layers are checked WITHOUT spending or tracking
//     anything (WouldAllowKey): a caller that is already limited is refused
//     here, so it never spends a global slot and one noisy client or one
//     hammered token cannot drain the shared ceiling;
//  2. the global layer spends its slot; a refusal here creates nothing;
//  3. only then do the client and token layers spend their tokens, creating
//     an entry for a key seen for the first time.
//
// So every new per-client or per-token entry belongs to a request the global
// layer admitted, which bounds key churn (see the tuning constraint above). A
// concurrent request can take a bucket's last token between steps 1 and 3;
// the request is then refused after spending a global slot, which is bounded
// by the global layer itself. A request with no trusted client address skips
// the client layer (see inviteClientKey). Every layer refuses with the same
// status and body, so a caller cannot tell which layer tripped.
func (l *inviteRateLimits) middleware(tokenOf func(*gin.Context) (token string, ok bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientKey, attributed := l.inviteClientKey(c)
		ok := !attributed || l.perClient.WouldAllowKey(clientKey)
		var tokenKey string
		if ok {
			token, proceed := tokenOf(c)
			if !proceed {
				c.Abort()
				return
			}
			tokenKey = inviteTokenKey(token)
			if tokenKey == "" {
				tokenKey = inviteFallbackTokenKey
			}
			ok = l.perToken.WouldAllowKey(tokenKey)
		}
		if ok {
			ok = l.global.AllowKey(inviteGlobalKey)
		}
		if ok && attributed {
			ok = l.perClient.AllowKey(clientKey)
		}
		if ok {
			ok = l.perToken.AllowKey(tokenKey)
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
