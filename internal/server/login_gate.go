// file: internal/server/login_gate.go
// version: 5.0.1
// guid: bbbd655c-e45d-48ca-badb-d7b48a4d69e0
// last-edited: 2026-11-11

package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/gin-gonic/gin"
)

// The gin gate in front of the two password logins (the web POST
// /api/v1/auth/login and the Audiobookshelf-compatible POST /login). The
// aggregate failure ceilings and the saturated admission it applies are core
// auth's (coreauth.LoginCeiling, 05-authn §4.9, moved from this package's
// login_ceiling.go at F4-02); what stays here is the HTTP half: the visitor,
// saturated and connector keys taken from the request, the Cloudflare Access
// exemption, the body read, and the refusals written in each door's error
// shape.
//
// Before anything is counted the middleware reads the login body itself,
// under loginBodyLimit and a read deadline (loginBodyReadTimeout), and hands
// the handler a buffered copy: a caller that opens a login and then stalls its
// body holds a connection for a few seconds, not a ceiling slot, and never
// reaches the handler's JSON decoding.
//
// A request carrying a VERIFIED Cloudflare Access identity (a JWT verified
// against the team's JWKS and AUD, for an allow-listed verified email, the
// same checks CloudflareAccessAuth applies) is exempt: Cloudflare has
// authenticated that person, so an anonymous flood cannot starve them. It is
// still subject to the per-visitor and per-account limits in the handlers, and
// its failures do not count. A saturated login is marked on the request
// (absauth.MarkLoginSaturated) and the handlers delay it before password work
// (absauth.RefuseSaturatedLogin, coreauth.SaturationPolicy). The middleware's
// own refusals (413, 408, 400, 429) are written in the error shape of the door
// it guards (see loginResponder). POST /auth/refresh is deliberately outside
// the ceilings: it does no password work, and a server restart that
// invalidates every refresh token makes every client fail once, which must not
// read as an attack. HTTP Basic auth on the API is a separate path and is not
// throttled here.

// loginBodyLimit caps a login body (the real one is a few hundred bytes).
const loginBodyLimit = 4 << 10

// loginBodyReadTimeout bounds how long the middleware waits for a login body. A
// variable so tests can shorten it.
var loginBodyReadTimeout = 5 * time.Second

// startupLoginSaturated is the startup half of the saturated-admission bucket:
// it refuses to start on an unusable LOGIN_SATURATED_RATE / LOGIN_SATURATED_BURST
// (a typo must neither remove nor tighten the bound unnoticed) and logs the
// effective limits.
func startupLoginSaturated() error {
	r, b, err := config.LoginSaturatedLimits()
	if err != nil {
		return fmt.Errorf("login saturation limits: %w", err)
	}
	lifecycleLog.Info("saturated password logins (while the login failure ceilings are spent) are admitted at %v per second, burst %d", r, b)
	return nil
}

// loginThrottle returns the one failed-login throttle both password logins share,
// so a guess at an account through either door counts toward the same
// per-account counter and the same per-source budget.
func (s *Server) loginThrottle() *coreauth.Throttle {
	s.loginThrottleOnce.Do(func() { s.loginThrottleState = coreauth.NewThrottle(coreauth.ThrottleLimits{}, nil, nil) })
	return s.loginThrottleState
}

// loginCeiling returns the one ceiling both password logins share, so the global
// bucket counts web and ABS failures together.
func (s *Server) loginCeiling() *coreauth.LoginCeiling {
	s.loginCeilingOnce.Do(func() {
		s.loginCeilingState = newLoginCeiling()
		// An unusable setting stops the service starting (startupLoginSaturated);
		// the defaults here only cover a ceiling built without that check.
		if r, b, err := config.LoginSaturatedLimits(); err == nil {
			s.loginCeilingState.SetSaturatedBound(r, b)
		}
	})
	return s.loginCeilingState
}

// loginCeilingMiddleware is the server's gate for the requests match accepts,
// over its one ceiling. The Access exemption is looked up per request, not
// copied here: buildOAuthWiring sets loginCeilingExempt, and this may run
// first.
func (s *Server) loginCeilingMiddleware(match func(*gin.Context) bool, respond loginResponder) gin.HandlerFunc {
	return loginCeilingMiddleware(s.loginCeiling(), func(c *gin.Context) bool {
		fn := s.loginCeilingExempt
		return fn != nil && fn(c)
	}, match, respond)
}

// newLoginCeiling is an empty ceiling with the default saturated bound
// (coreauth.DefaultSaturatedRate and DefaultSaturatedBurst, HEAD's
// LOGIN_SATURATED_* defaults).
func newLoginCeiling() *coreauth.LoginCeiling { return coreauth.NewLoginCeiling(nil) }

// loginCeilingConnectorKey is the bucket key for a request that arrived through
// a configured tunnel connector, "" for any other peer (direct callers are
// covered by the per-visitor throttle and the global ceiling).
func loginCeilingConnectorKey(c *gin.Context) string {
	if !tunnelConnectorPeers().contains(c.Request.RemoteAddr) {
		return ""
	}
	if a, ok := peerKeyAddr(c); ok {
		return a.String()
	}
	return ""
}

// loginResponder writes the middleware's own refusals (413, 408, 400, 429) in
// the error shape of the door it guards: the web API's envelope, or the
// Audiobookshelf-compatible {"error": msg} that ABS clients parse. code is the
// web envelope's machine code and is ignored by shapes without one.
type loginResponder func(c *gin.Context, status int, msg, code string)

// respondWebLoginError is the web API's error envelope.
func respondWebLoginError(c *gin.Context, status int, msg, code string) {
	httputil.RespondWithError(c, status, msg, code)
}

// respondABSLoginError is the Audiobookshelf error shape, the one the ABS
// handlers themselves answer with.
func respondABSLoginError(c *gin.Context, status int, msg, _ string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// readLoginBody reads the request body under loginBodyLimit and
// loginBodyReadTimeout and puts a buffered copy back, so what the handler decodes
// is bytes already in memory. It answers (through respond) and aborts itself when
// the body is too large (413), not delivered in time (408) or unreadable (400),
// and reports whether the request may go on.
func readLoginBody(c *gin.Context, respond loginResponder) bool {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return true
	}
	fail := func(status int, msg, code string) bool {
		// The body did not reach EOF, so the read deadline stays in force (it also
		// bounds the drain net/http does before answering). HTTP/1 closes the
		// connection after the answer; HTTP/2 and HTTP/3 reset just the stream.
		if c.Request.ProtoMajor == 1 {
			c.Header("Connection", "close")
		}
		respond(c, status, msg, code)
		c.Abort()
		return false
	}
	if c.Request.ContentLength > loginBodyLimit {
		return fail(http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
	}
	restore := setBodyReadDeadline(c, loginBodyReadTimeout)
	buf, err := io.ReadAll(io.LimitReader(c.Request.Body, loginBodyLimit+1))
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return fail(http.StatusRequestTimeout, "timed out reading the request body", "REQUEST_TIMEOUT")
	case err != nil:
		return fail(http.StatusBadRequest, "could not read the request body", "BAD_REQUEST")
	case len(buf) > loginBodyLimit:
		return fail(http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
	}
	restore() // EOF reached: a keep-alive connection's next request is unaffected
	c.Request.Body = io.NopCloser(bytes.NewReader(buf))
	return true
}

// loginCeilingMiddleware applies l to the requests match accepts (all of them
// when match is nil). exempt (nil: nothing is exempt) is read on every request.
// respond writes the middleware's own refusals; nil means the web API's
// envelope.
func loginCeilingMiddleware(l *coreauth.LoginCeiling, exempt func(*gin.Context) bool, match func(*gin.Context) bool, respond loginResponder) gin.HandlerFunc {
	if respond == nil {
		respond = respondWebLoginError
	}
	return func(c *gin.Context) {
		if match != nil && !match(c) {
			c.Next()
			return
		}
		// Body first, before anything is reserved or counted: a stalled or oversized
		// body must not hold a ceiling slot or reach the handler.
		if !readLoginBody(c, respond) {
			return
		}
		if exempt != nil && exempt(c) {
			c.Next()
			return
		}
		// Saturated admission is keyed coarser than the per-visitor throttle (a
		// public IPv6 visitor per /48), asked only when a ceiling is spent, and
		// decided HERE, before any handler looks up the submitted user, so every
		// name gets the same answer.
		adm, d := l.Admit(loginThrottleKey(c), func() string { return loginSaturatedKey(c) }, loginCeilingConnectorKey(c))
		if d.Warn != "" {
			clientTrustLog.Warn("login failure ceiling reached (%s): further password logins are delayed, and refused for a username past %d failures of its own, until its window ends", d.Warn, coreauth.DefaultSoftThreshold)
		}
		if adm == nil {
			c.Header("Retry-After", strconv.Itoa(d.RetryAfter))
			switch d.Refusal {
			case coreauth.CeilingSaturatedFailed:
				respond(c, http.StatusTooManyRequests, "too many failed login attempts — try again later", "TOO_MANY_REQUESTS")
			case coreauth.CeilingSaturatedCapacity:
				respond(c, http.StatusTooManyRequests, "too many login attempts right now — try again shortly", "TOO_MANY_REQUESTS")
			default: // CeilingBusy, CeilingSaturatedBusy
				respond(c, http.StatusTooManyRequests, "too many simultaneous login attempts — try again shortly", "TOO_MANY_REQUESTS")
			}
			c.Abort()
			return
		}
		if adm.Saturated() {
			absauth.MarkLoginSaturated(c)
		}
		outcome := coreauth.LoginOther
		defer func() { adm.Done(outcome, absauth.LoginAccount(c)) }()
		c.Next()
		switch code := c.Writer.Status(); {
		case code == http.StatusUnauthorized:
			outcome = coreauth.LoginFailed
		case adm.Saturated() && absauth.LoginSaturationRefused(c):
			// The handler's account refusal is a failure while saturated, like a 401.
			outcome = coreauth.LoginSaturationRefused
		case code >= 200 && code < 300:
			outcome = coreauth.LoginSucceeded
		}
	}
}

// newAccessExemption is the ceilings' exemption test: the request carries a
// Cloudflare Access assertion (header, or the CF_Authorization cookie browsers
// deliver) that verifier accepts (signature, issuer, expiry, AUD) for a verified
// email cfg allows. A service-token assertion names no person and is not exempt.
// The answer is kept on the request.
func newAccessExemption(verifier servermiddleware.CFAssertionVerifier, cfg *legacyoauth.Config) func(*gin.Context) bool {
	const ctxKey = "login_ceiling_access_exempt"
	if verifier == nil || cfg == nil {
		return nil
	}
	return func(c *gin.Context) bool {
		if v, ok := c.Get(ctxKey); ok {
			b, _ := v.(bool)
			return b
		}
		raw := c.GetHeader(cfaccess.CFAccessHeader)
		if raw == "" {
			if ck, err := c.Cookie("CF_Authorization"); err == nil {
				raw = ck
			}
		}
		exempt := false
		if raw != "" {
			if claims, err := verifier.Verify(c.Request.Context(), raw); err == nil && claims != nil {
				exempt = claims.EmailVerified && cfg.IsEmailAllowed(claims.Email)
			}
		}
		c.Set(ctxKey, exempt)
		return exempt
	}
}
