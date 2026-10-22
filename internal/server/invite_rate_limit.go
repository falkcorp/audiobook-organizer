// file: internal/server/invite_rate_limit.go
// version: 1.22.0
// guid: 660fd56e-7850-420a-8302-5290684460cf
// last-edited: 2026-10-21

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Public invite endpoints (GET /auth/invite/:token and POST
// /auth/accept-invite) are bounded by three layers and a request must pass each
// one that applies to it. The token space is 256 bits, so this is about load and about
// protecting one guest's invitation from another caller, not about guessing.
//
// Production sits behind a Cloudflare tunnel whose connectors (cloudflared)
// run on other LAN hosts and connect to the service from their own private
// addresses, and the router trusts no proxy headers (SetTrustedProxies(nil),
// see NewServer), so the peer address is a connector's address for every tunnel
// request: a limiter keyed on it alone is one bucket per connector, shared by
// every visitor, and a single caller could exhaust it for every guest. The
// invite routes also bypass Cloudflare Access (a guest accepts before being
// allowed in), so there is no Access assertion to lean on. The layers
// therefore do not rely on the peer address alone:
//
//  1. per token: a token's lookups and accept attempts, from anywhere, share
//     one small bucket (keyed by a hash of the token, never the raw token).
//  2. per client: keyed by trustedClientIP, i.e. CF-Connecting-IP when it can
//     be believed (the peer is a configured tunnel connector, or the request
//     carries a verified Access assertion and the peer is this host),
//     otherwise the peer's own address; public IPv6 clients are grouped by
//     /64, LAN IPv6 peers keep their full address (clientPrefixKey). A LAN
//     peer keyed on its own address also gets LAN aggregates, reserved with
//     the per-client bucket (classifyLANPeer, inviteClientKey). A direct
//     peer (directLANPeer) gets its /64's aggregate when it is IPv6, at
//     four times the per-client budget, and the direct LAN total, half the
//     global layer. A LAN host sending Cloudflare markers but not a listed
//     connector ("unlisted") gets its /64 (IPv6) or /24 (IPv4) aggregate,
//     at twice the per-client budget, and the unlisted LAN total, a quarter
//     of the global layer, so one LAN host rotating
//     through many addresses, /64s or /24s can never drain the global layer
//     for everyone else. Direct and unlisted traffic never share a bucket,
//     so tunnel-looking traffic cannot spend what a direct LAN caller
//     depends on. Sending Cf-* headers does not change a non-connector
//     peer's key. A request whose client cannot be established (a configured
//     connector or a local peer with no trusted header) SKIPS this layer: a
//     key taken from an unproven header would let a caller pick its own
//     bucket, and one shared bucket would let one abuser lock out every
//     guest. Layers 1 and 3 still apply to it. TUNNEL_CONNECTOR_ADDRS must
//     list every connector: an unlisted connector is keyed on its own
//     address, one bucket shared by everyone it relays (logged once at
//     start-up when the setting is empty).
//  3. global: a generous ceiling across every token and client, so
//     enumeration spread over many tokens or many clients is still bounded.
//     EVERY request is under it: a request whose client is unattributed, a
//     tunnel visitor, an unlisted LAN host and a direct LAN peer alike. A
//     direct LAN peer was exempt from it until 2026-10-21 (it had the direct
//     LAN total instead). It is not any more, because "direct" only means
//     the TCP peer is on a private address and sent no Cloudflare header, which
//     anything that reaches the port off-tunnel can satisfy, so the exemption
//     was request budget for any such caller. The direct LAN total below is
//     still a separate, tighter layer for it. The price is that a flood
//     (from the tunnel or from a LAN device) can spend the layer and refuse
//     the owner until the tokens refill.
//
// Real use is one page load and one accept per guest.
//
// Tuning constraint: the number of new keys a limiter can be made to track
// within its idle TTL (15 minutes) must stay below its tracked-entry cap
// (10,000), or a flood of distinct keys could evict a victim's bucket and hand
// it a fresh burst (eviction only ever refreshes a bucket; see
// IPRateLimiter.insertLocked). A request the middleware refuses inserts no
// key anywhere (an unseen key's entry is created only when the request is
// admitted, ReserveKey/Commit), so every new per-client, per-token or LAN
// aggregate entry belongs to a request that the global layer admitted (every
// request takes a global slot): each of those limiters gains at most
// inviteGlobalRateBurst + 15 * inviteGlobalRatePerMinute = 4,600 keys per idle
// TTL, and the global and LAN total limiters hold one key each. That is why
// the global ceiling is not larger. (Until 2026-10-21 a direct LAN peer
// skipped the global layer and a second bound, the direct LAN total, added up
// to 2,300 more keys; with the exemption gone that second bound is not needed
// for this constraint.)
const (
	// Per client (layer 2). Name kept from the single shared limiter this
	// replaced.
	inviteAuthRatePerMinute = 20
	inviteAuthRateBurst     = 10

	// Per token (layer 1): one guest opens the link a few times and accepts
	// once; a handful of password retries is the realistic worst case.
	inviteTokenRatePerMinute = 6
	inviteTokenRateBurst     = 8

	// LAN aggregates (part of layer 2). A LAN peer keyed on its own address
	// shares a prefix aggregate with every address of its prefix, so one
	// LAN host rotating through its prefix is capped there while a few
	// distinct LAN devices each keep their own per-client bucket, and a
	// total with all LAN traffic of its kind. Direct and unlisted traffic
	// have separate limiters, so neither can spend the other's. The layers
	// nest, each larger than the one before, so each can be the one that
	// refuses (bursts; per-minute rates likewise):
	//   - direct: per client 10 < /64 40 < direct total 50 < global 100;
	//   - unlisted: per client 10 < /64 or /24 20 < unlisted total 25 <
	//     global 100.

	// Direct LAN IPv6 /64: four times the per-client budget, room for the
	// devices of a household sharing one ULA /64.
	inviteDirectLANPrefixRatePerMinute = 4 * inviteAuthRatePerMinute
	inviteDirectLANPrefixRateBurst     = 4 * inviteAuthRateBurst

	// Direct LAN total: half the global layer, so direct LAN traffic as a
	// whole can never spend more than half of it, and an IPv4 LAN host (which
	// has no prefix aggregate) cannot rotate aliases without limit.
	inviteDirectLANTotalRatePerMinute = inviteGlobalRatePerMinute / 2
	inviteDirectLANTotalRateBurst     = inviteGlobalRateBurst / 2

	// Unlisted LAN /64 (IPv6) or /24 (IPv4): twice the per-client budget,
	// below the unlisted total so it can bind.
	inviteUnlistedLANPrefixRatePerMinute = 2 * inviteAuthRatePerMinute
	inviteUnlistedLANPrefixRateBurst     = 2 * inviteAuthRateBurst

	// Unlisted LAN total: a quarter of the global layer, so a LAN host
	// sending Cloudflare markers cannot drain the global layer by rotating
	// across prefixes.
	inviteUnlistedLANTotalRatePerMinute = inviteGlobalRatePerMinute / 4
	inviteUnlistedLANTotalRateBurst     = inviteGlobalRateBurst / 4

	// Global (layer 3), across all tokens and clients.
	inviteGlobalRatePerMinute = 300
	inviteGlobalRateBurst     = 100
)

// inviteAcceptBodyLimit caps an accept-invite body. The real body is
// {"token": 64 hex chars, "password": ...}, so 4 KiB leaves room for any
// sane password; a larger body is refused with 413 before the handler runs,
// so the limiter always reads the whole body the handler will use.
const inviteAcceptBodyLimit = 4 << 10

// inviteAcceptBodyReadTimeout bounds how long readAcceptInviteBody waits for
// the accept body. The body is read before any limiter runs (the limiter
// needs the token), and the server sets no body read timeout (uploads), so
// without this a caller could hold a connection and a goroutine open by
// trickling the 4 KiB body a byte at a time. A real body is one small JSON
// object sent at once. A variable so tests can shorten it.
var inviteAcceptBodyReadTimeout = 5 * time.Second

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
	timedOut bool // not received within inviteAcceptBodyReadTimeout
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
		restore := setBodyReadDeadline(c, inviteAcceptBodyReadTimeout)
		buf, err := io.ReadAll(io.LimitReader(c.Request.Body, inviteAcceptBodyLimit+1))
		if err == nil && len(buf) <= inviteAcceptBodyLimit {
			// The body reached EOF: clear the deadline so a keep-alive
			// connection's next request is unaffected.
			restore()
		} else {
			// The body did NOT reach EOF (too large, timed out, or a read
			// error). The read deadline stays in force either way.
			//
			// HTTP/1: whatever response follows, the connection closes after
			// it. Without Connection: close, net/http drains up to 256 KiB of
			// the unread body BEFORE writing the response headers, and a
			// caller that stops sending holds the request with no answer. The
			// deadline also bounds the drain net/http still does after the
			// response (finishRequest closes the body).
			//
			// HTTP/2 and HTTP/3: no header. The Go HTTP/2 server turns
			// Connection: close into a GOAWAY, which would end every stream
			// the browser has open on that connection, and neither protocol
			// needs it: one stream's unread body never delays its response,
			// and the server resets just that stream when the handler
			// returns.
			if c.Request.ProtoMajor == 1 {
				c.Header("Connection", "close")
			}
		}
		switch {
		case len(buf) > inviteAcceptBodyLimit:
			p.tooLarge = true
		case errors.Is(err, os.ErrDeadlineExceeded):
			p.timedOut = true
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

// rawResponseControllerKey is the gin context key under which
// captureRawResponseController stores the request's *http.ResponseController.
const rawResponseControllerKey = "server.rawResponseController"

// captureRawResponseController stores an http.ResponseController for the
// writer as it is when this middleware runs. It must be installed BEFORE any
// middleware that replaces c.Writer with a wrapper lacking Unwrap: the gzip
// middleware (compressionMiddleware) does, and a ResponseController built on
// its writer cannot set connection deadlines (http.ErrNotSupported), which
// silently disabled the accept-invite body read deadline for every client
// sending Accept-Encoding: gzip (every browser).
func captureRawResponseController() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(rawResponseControllerKey, http.NewResponseController(c.Writer))
		c.Next()
	}
}

// bodyDeadlineUnsupported warns once that no body read deadline could be set.
var bodyDeadlineUnsupported sync.Once

// setBodyReadDeadline sets a read deadline d from now on the request's
// connection and returns the function that clears it again. It uses the
// controller captureRawResponseController stored before any writer wrapper,
// falling back to one built on c.Writer. The server has no ReadTimeout, so
// the connection's read deadline is zero while a handler runs; clearing
// restores exactly that, and the next request on a keep-alive connection is
// unaffected. When the deadline still cannot be set (http.ErrNotSupported:
// an HTTP/2 stream without support, a test recorder, or the capture
// middleware missing), the read is unbounded in time, as before (the 4 KiB
// cap still applies), and that is logged once at Warn.
func setBodyReadDeadline(c *gin.Context, d time.Duration) (restore func()) {
	rc, _ := c.Value(rawResponseControllerKey).(*http.ResponseController)
	if rc == nil {
		rc = http.NewResponseController(c.Writer)
	}
	if err := rc.SetReadDeadline(time.Now().Add(d)); err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			bodyDeadlineUnsupported.Do(func() {
				inviteLimitLog.Warn("accept-invite: the connection does not support read deadlines, so the body read is bounded only in size; check that captureRawResponseController runs before any response-writer wrapper")
			})
		} else {
			inviteLimitLog.Debug("accept-invite: could not set a body read deadline: %v", err)
		}
		return func() {}
	}
	return func() { _ = rc.SetReadDeadline(time.Time{}) }
}

// inviteGlobalKey is the single bucket of the global layer.
const inviteGlobalKey = "invite:global"

// The single buckets of the LAN total limiters.
const (
	inviteDirectLANTotalKey   = "invite-lan-total:direct"
	inviteUnlistedLANTotalKey = "invite-lan-total:unlisted"
)

// inviteRateLimits holds the limiters of the public invite endpoints.
type inviteRateLimits struct {
	perToken  *servermiddleware.IPRateLimiter
	perClient *servermiddleware.IPRateLimiter
	// The LAN aggregates (classifyLANPeer), reserved together with
	// perClient: per prefix and total, for direct and unlisted peers.
	directLANPrefix   *servermiddleware.IPRateLimiter
	directLANTotal    *servermiddleware.IPRateLimiter
	unlistedLANPrefix *servermiddleware.IPRateLimiter
	unlistedLANTotal  *servermiddleware.IPRateLimiter
	global            *servermiddleware.IPRateLimiter
	// isLocalPeer reports whether an address is this host. Injectable for tests.
	isLocalPeer func(remoteAddr string) bool
	// connectors returns the configured tunnel connector addresses (see
	// trustedClientIP), read on every request so an entry that becomes
	// valid later (tunnelConnectorPeers) applies without a restart.
	// Injectable for tests.
	connectors func() connectorPeers
}

var inviteLimitLog = logger.New("server.invite-rate-limit")

// newInviteRateLimits builds the limiters and starts their idle sweepers on ctx.
func newInviteRateLimits(ctx context.Context) *inviteRateLimits {
	l := &inviteRateLimits{
		perToken:          servermiddleware.NewIPRateLimiter(inviteTokenRatePerMinute, inviteTokenRateBurst),
		perClient:         servermiddleware.NewIPRateLimiter(inviteAuthRatePerMinute, inviteAuthRateBurst),
		directLANPrefix:   servermiddleware.NewIPRateLimiter(inviteDirectLANPrefixRatePerMinute, inviteDirectLANPrefixRateBurst),
		directLANTotal:    servermiddleware.NewIPRateLimiter(inviteDirectLANTotalRatePerMinute, inviteDirectLANTotalRateBurst),
		unlistedLANPrefix: servermiddleware.NewIPRateLimiter(inviteUnlistedLANPrefixRatePerMinute, inviteUnlistedLANPrefixRateBurst),
		unlistedLANTotal:  servermiddleware.NewIPRateLimiter(inviteUnlistedLANTotalRatePerMinute, inviteUnlistedLANTotalRateBurst),
		global:            servermiddleware.NewIPRateLimiter(inviteGlobalRatePerMinute, inviteGlobalRateBurst),
		isLocalPeer:       remoteAddrIsLocal,
		connectors:        tunnelConnectorPeers,
	}
	if len(l.connectors()) == 0 {
		inviteLimitLog.Info("TUNNEL_CONNECTOR_ADDRS is not set: no tunnel visitor address can be established, so each tunnel connector gets one invite per-client bucket shared by every visitor it relays, and all tunnel traffic shares the unlisted LAN total (a quarter of the global limit); the per-token and global limits still apply")
	}
	l.perToken.Start(ctx)
	l.perClient.Start(ctx)
	l.directLANPrefix.Start(ctx)
	l.directLANTotal.Start(ctx)
	l.unlistedLANPrefix.Start(ctx)
	l.unlistedLANTotal.Start(ctx)
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

// inviteClient is how the client layer keys one request (inviteClientKey).
type inviteClient struct {
	// key is the per-client bucket.
	key string
	// prefixKey is the LAN prefix aggregate, "" for none.
	prefixKey string
	// totalKey is the LAN total, "" for none.
	totalKey string
	// direct: a direct LAN peer (direct limiters; the global layer still
	// applies to it);
	// unlisted: an unlisted Cf-marked LAN peer (unlisted limiters). Both
	// false for any other request.
	direct, unlisted bool
}

// inviteClientKey returns how the client layer keys the request, and false
// when no trusted client address can be established (see trustedClientIP),
// in which case the per-client layer is skipped for the request and the
// global layer applies. The key is never derived from an unproven header.
// Public IPv6 clients share one bucket per /64 (clientPrefixKey), as the
// bootstrap limiter keys them, so one host cannot rotate through its /64 for
// fresh buckets; a LAN IPv6 peer (isLANAddr: ULA, link-local, on-link global)
// keeps its full address, so
// LAN devices sharing a /64 do not share a bucket. A LAN peer keyed on its
// own address also gets its LAN aggregates (classifyLANPeer):
//   - direct: "invite-direct-lan64:<its /64>" for IPv6 (none for IPv4: a /24
//     bucket would be shared by every device on the home network), the
//     direct LAN total;
//   - unlisted (Cloudflare markers, not a listed connector):
//     "invite-unlisted-lan:<its /64 or /24>" and the unlisted LAN total.
//
// A visitor attributed through a connector gets none of them (the global
// layer covers it).
func (l *inviteRateLimits) inviteClientKey(c *gin.Context) (inviteClient, bool) {
	connectors := l.connectors()
	ip, ok := trustedClientIP(c, clientTrust{isLocalPeer: l.isLocalPeer, connectors: connectors})
	if !ok {
		return inviteClient{}, false
	}
	k, ok := clientPrefixKey(ip)
	if !ok {
		return inviteClient{}, false
	}
	ic := inviteClient{key: "invite-client:" + k}
	switch lp := classifyLANPeer(c, ip, connectors); {
	case lp.direct:
		if lp.prefix != "" {
			ic.prefixKey = "invite-direct-lan64:" + lp.prefix
		}
		ic.totalKey = inviteDirectLANTotalKey
		ic.direct = true
	case lp.unlisted:
		if lp.prefix != "" {
			ic.prefixKey = "invite-unlisted-lan:" + lp.prefix
		}
		ic.totalKey = inviteUnlistedLANTotalKey
		ic.unlisted = true
	}
	return ic, true
}

// middleware returns the Gin middleware enforcing the layers.
// tokenOf extracts the invite token from the request; "" (missing or
// unparseable) uses the shared fallback bucket. ok=false means tokenOf has
// already answered the request (413) and the chain stops.
//
// Order, settled once:
//  1. tokenOf runs FIRST, before any bucket is touched. Reading the body is
//     bounded in size (inviteAcceptBodyLimit, 4 KiB) and in time
//     (inviteAcceptBodyReadTimeout), and doing it first means a request
//     tokenOf rejects never reaches a limiter at all.
//  2. The client (with its LAN aggregates), token and global layers are
//     then reserved together
//     (ReserveKeys). Each reservation is atomic (IPRateLimiter.ReserveKey),
//     and when any layer refuses, every reservation already taken is
//     cancelled: a refused request spends nothing anywhere.
//  3. Only once every layer admits is the set committed. A per-client or
//     per-token key the limiter has never seen is NOT inserted until then;
//     an unseen key is always allowed at full burst, so ReserveKey records a
//     pending admission instead of an entry. A refused request therefore
//     inserts nothing, and each admitted request can create at most one new
//     key per layer while taking one global slot.
//
// Consequences: a client can never spend more global slots than it holds
// client tokens, even with many concurrent requests, because each must hold
// one of the client's tokens (pending admissions count against the burst)
// before the global layer is asked, so one host cannot drain the shared
// ceiling; a caller that is already limited never spends a token or global
// slot; and a flood of refused requests cannot grow or churn the per-key
// tables, so it cannot evict anyone's bucket.
//
// A request with no trusted client address skips the client layer (see
// inviteClientKey); every request, a direct LAN peer included, is also under
// the global layer. Every layer refuses with the same status and body, so a
// caller cannot tell which layer tripped.
func (l *inviteRateLimits) middleware(tokenOf func(*gin.Context) (token string, ok bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, proceed := tokenOf(c)
		if !proceed {
			c.Abort()
			return
		}
		tokenKey := inviteTokenKey(token)
		if tokenKey == "" {
			tokenKey = inviteFallbackTokenKey
		}
		steps := make([]servermiddleware.KeyStep, 0, 5)
		ic, attributed := l.inviteClientKey(c)
		if attributed {
			steps = append(steps, servermiddleware.KeyStep{Limiter: l.perClient, Key: ic.key})
			prefix, total := l.unlistedLANPrefix, l.unlistedLANTotal
			if ic.direct {
				prefix, total = l.directLANPrefix, l.directLANTotal
			}
			if ic.prefixKey != "" {
				steps = append(steps, servermiddleware.KeyStep{Limiter: prefix, Key: ic.prefixKey})
			}
			if ic.totalKey != "" {
				steps = append(steps, servermiddleware.KeyStep{Limiter: total, Key: ic.totalKey})
			}
		}
		steps = append(steps, servermiddleware.KeyStep{Limiter: l.perToken, Key: tokenKey})
		steps = append(steps, servermiddleware.KeyStep{Limiter: l.global, Key: inviteGlobalKey})
		held, ok := servermiddleware.ReserveKeys(steps...)
		if !ok {
			httputil.RespondWithError(c, http.StatusTooManyRequests, "rate limit exceeded", "TOO_MANY_REQUESTS")
			c.Abort()
			return
		}
		servermiddleware.CommitReservations(held)
		c.Next()
	}
}

// inviteTokenFromParam reads the token of GET /auth/invite/:token.
func inviteTokenFromParam(c *gin.Context) (string, bool) { return c.Param("token"), true }

// inviteTokenFromBody reads the token of POST /auth/accept-invite through
// readAcceptInviteBody, which stashes the parsed body for handleAcceptInvite.
// A body over inviteAcceptBodyLimit is answered 413 here, and one not received
// within inviteAcceptBodyReadTimeout 408. A missing or malformed body yields
// "" (the fallback bucket; the handler answers it).
func inviteTokenFromBody(c *gin.Context) (string, bool) {
	p := readAcceptInviteBody(c)
	if p.tooLarge {
		httputil.RespondWithError(c, http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
		return "", false
	}
	if p.timedOut {
		httputil.RespondWithError(c, http.StatusRequestTimeout, "request body not received in time", "REQUEST_TIMEOUT")
		return "", false
	}
	if !p.parsed {
		return "", true
	}
	return p.req.Token, true
}
