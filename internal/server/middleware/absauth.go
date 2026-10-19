// file: internal/server/middleware/absauth.go
// version: 1.10.0
// guid: e7051b93-6c28-4a0f-9d34-b8f2a61c05de
// last-edited: 2026-10-19

package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
)

// Gin context keys set by the ABS identity middleware. They are additive: the ABS
// group never touches the browser-session or API-key keys.
const (
	contextABSSessionKey = "abs_session_id"
	contextABSModeKey    = "abs_auth_mode"
	// contextABSServiceTokenKey holds the Cloudflare service token's common_name.
	// A CREDENTIAL label, never an identity — nothing may resolve a user from it.
	contextABSServiceTokenKey = "abs_service_token"
)

// ABS auth mode names as reported by ABSAuthMode.
const (
	// ABSModeCF means the request was identified by a VERIFIED
	// Cf-Access-Jwt-Assertion (design spec Modes C and A).
	ABSModeCF = "cf"
	// ABSModeJWT means the request was identified by our own bearer access token
	// (design spec Mode B).
	ABSModeJWT = "jwt"
	// ABSModeAPIKey means the request was identified by an `abk_` application API
	// key. This exists so ONE credential can exercise the whole app — /api/v1 and
	// the ABS surface — during testing and diagnostics. It is deliberately last in
	// the resolution order and carries the key's SCOPED permissions, so it widens
	// reach without widening privilege.
	ABSModeAPIKey = "apikey"
)

// CFAssertionVerifier is the slice of *oauth.CFAccessVerifier this middleware needs.
// Declared as an interface so tests can inject a verifier without standing up
// Cloudflare's JWKS endpoint.
type CFAssertionVerifier interface {
	Verify(ctx context.Context, rawJWT string) (*oauth.IdentityClaims, error)
}

// ABSIdentityStore is the narrow store slice the ABS identity resolver needs.
// database.Store satisfies it (PebbleStore implements GetABSSession).
type ABSIdentityStore interface {
	oauth.UserStore
	GetRoleByID(id string) (*database.Role, error)
	GetABSSession(id string) (*database.ABSSession, error)
	// ExtendABSSession is how authenticated use keeps a session alive (sliding
	// expiry). It is part of the required interface, not an optional assertion:
	// a store that silently lacked it would quietly bring back the fixed 30-day
	// logout. See PebbleStore.ExtendABSSession for the concurrency contract.
	ExtendABSSession(id string, now, expiresAt time.Time, minAdvance time.Duration) (bool, error)
}

// ABSIdentityResolver implements the unified identity resolution of design spec
// §3.0.1 for the Audiobookshelf-compatible surface.
//
// Resolution order on EVERY ABS request, strictly:
//
//  1. A VERIFIED Cf-Access-Jwt-Assertion — signature checked against Cloudflare's
//     public keys AND the application AUD tag — identifies the user by its `email`
//     claim (Mode C, and also Mode A). The user is JIT-provisioned if the email is on
//     the allowlist and no local user exists.
//  2. Else our own bearer access JWT, from `Authorization: Bearer` or `?token=` on a
//     GET (Mode B).
//  3. Else 401.
//
// WHY THIS IS NOT CloudflareAccessAuth: that middleware is FAIL-OPEN on /api/v1 — an
// unverifiable assertion falls through to RequireAuth, which then enforces a session
// or API key. On the ABS surface there is no such second gate, so falling through
// would be an authentication bypass. Here every failure mode is terminal:
//
//   - assertion present but unverifiable  → 401 (never a pass-through, never a
//     fall-back to the bearer path, so the CF path cannot be probed for free)
//   - assertion verified, email not on the allowlist → 403, and NO user is created
//   - bearer invalid / session revoked, expired or unknown → 401
//   - user exists but is not active → 403
//   - the store could not answer → 500, NEVER 401 (§1.7.3 item 3: a 401 on a
//     transient error force-logs-out the client and can wedge it)
//
// The unsigned Cf-Access-Authenticated-User-Email header is never consulted: it is
// trivially forged on a direct-to-origin request. The signed JWT is the only anchor.
type ABSIdentityResolver struct {
	cfg      *absauth.Config
	verifier CFAssertionVerifier
	oauthCfg *oauth.Config
	store    ABSIdentityStore
	// now is the clock for token expiry, session liveness and session extension.
	// time.Now until UseClock binds it (abs.New binds it to the ABS handler's
	// clock, so refresh and bearer verification share one timeline).
	now func() time.Time
}

// NewABSIdentityResolver wires the resolver. A nil cfg, or a cfg whose ABS API is
// disabled, makes every request 401 — a wiring mistake must never open a door.
func NewABSIdentityResolver(cfg *absauth.Config, verifier CFAssertionVerifier, oauthCfg *oauth.Config, store ABSIdentityStore) *ABSIdentityResolver {
	return &ABSIdentityResolver{cfg: cfg, verifier: verifier, oauthCfg: oauthCfg, store: store, now: time.Now}
}

// UseClock makes fn the resolver's clock; a nil fn is ignored. abs.New calls it
// with the ABS handler's clock, so the decisions refresh makes (session expiry,
// sliding) and the ones every bearer request makes (token exp, liveness,
// extension) are judged on the same timeline. That is time.Now in production;
// a test that moves the handler's clock moves both.
func (r *ABSIdentityResolver) UseClock(fn func() time.Time) {
	if r != nil && fn != nil {
		r.now = fn
	}
}

func (r *ABSIdentityResolver) clock() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}
	return r.now()
}

// ABSIdentity is a successfully resolved ABS identity.
type ABSIdentity struct {
	User *database.User
	// Mode is ABSModeCF, ABSModeJWT or ABSModeAPIKey.
	Mode string
	// APIKeyScopes are the scopes of the `abk_` key in ABSModeAPIKey. Bind narrows
	// the owner's role permissions to these, so a scoped key cannot reach more on
	// the ABS surface than it can on /api/v1. nil in every other mode.
	APIKeyScopes []string
	// SessionID is the abs_sess record backing a Mode B request. Empty in Mode C,
	// where identity arrives with every request and no server-side session is needed.
	SessionID string
	// Email is the verified assertion email in Mode C.
	Email string
	// AccessToken is the raw bearer presented in Mode B, so handlers can echo it as
	// the ABS `user.token` field without minting a second one.
	AccessToken string
}

// ABSAuthError is a terminal resolution failure with the HTTP status to return and a
// fixed-vocabulary reason for the audit log.
type ABSAuthError struct {
	Status  int
	Reason  string
	Message string
	Mode    string
	Email   string
}

func absErr(status int, reason, message, mode string) *ABSAuthError {
	return &ABSAuthError{Status: status, Reason: reason, Message: message, Mode: mode}
}

// Enabled reports whether the resolver can authenticate anything at all.
func (r *ABSIdentityResolver) Enabled() bool {
	return r != nil && r.cfg != nil && r.cfg.Enabled && r.store != nil
}

// Config exposes the resolved ABS config to handlers wired alongside the resolver.
func (r *ABSIdentityResolver) Config() *absauth.Config {
	if r == nil {
		return nil
	}
	return r.cfg
}

// CFAssertionsEnabled reports whether ResolveCFAssertion can ever resolve a person:
// the resolver is enabled, ABS_AUTH_MODES includes cf, and a verifier and
// allowlist config are wired. Single sign-on (/auth/openid) uses it to refuse up
// front instead of sending a browser through a sign-in that cannot complete.
func (r *ABSIdentityResolver) CFAssertionsEnabled() bool {
	return r.Enabled() && r.cfg.Modes.CF && r.verifier != nil && r.oauthCfg != nil
}

// CFCallerKey verifies the request's Cloudflare Access assertion WITHOUT resolving
// or provisioning a user, and returns a stable key naming the verified credential:
// "svc:<common_name>" for a service token, "email:<lowercased email>" for a person.
// ok is false when CF mode is off, there is no assertion, it does not verify, or
// it names neither a person nor a service token (an empty common_name would put
// every such caller in one anonymous bucket).
//
// It exists for per-caller quotas on unauthenticated endpoints (SSO hop 1). The
// origin trusts no proxy headers (server.go SetTrustedProxies(nil)), so behind the
// shared reverse proxies every caller has the same c.ClientIP(); a quota keyed on
// it would let one caller lock out everyone. The verified credential is the only
// per-caller attribute an attacker cannot forge or rotate freely.
//
// 🔴 A KEY, NOT IDENTITY. A service token is shared by a GROUP of people and
// devices, so a quota keyed on it alone is shared by that whole group; callers
// must add a per-device discriminator before applying a small cap. Never resolve
// a user from this value.
func (r *ABSIdentityResolver) CFCallerKey(c *gin.Context) (string, bool) {
	if !r.CFAssertionsEnabled() {
		return "", false
	}
	raw := absAssertionFromRequest(c)
	if raw == "" {
		return "", false
	}
	claims, err := r.verifier.Verify(c.Request.Context(), raw)
	if err != nil {
		if errors.Is(err, oauth.ErrNonIdentityAssertion) {
			cn := strings.TrimSpace(oauth.CFAssertionCommonName(err))
			if cn == "" {
				return "", false
			}
			c.Set(contextABSServiceTokenKey, cn)
			return "svc:" + cn, true
		}
		return "", false
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return "", false
	}
	return "email:" + email, true
}

// ResolveCFAssertion runs ONLY step 1. It returns (nil, nil) when there is no
// assertion to consider — either because none was sent or because Mode CF is
// disabled — and a terminal error when an assertion was sent but did not fully
// verify and authorize.
//
// POST /login and POST /auth/refresh call this directly: a verified assertion means
// the edge already authenticated a real person against the IdP, so login skips the
// password check and refresh always succeeds (§3.0.1).
func (r *ABSIdentityResolver) ResolveCFAssertion(c *gin.Context) (*ABSIdentity, *ABSAuthError) {
	if !r.Enabled() || !r.cfg.Modes.CF || r.verifier == nil || r.oauthCfg == nil {
		return nil, nil
	}
	raw := absAssertionFromRequest(c)
	if raw == "" {
		return nil, nil
	}

	claims, err := r.verifier.Verify(c.Request.Context(), raw)
	if err != nil {
		// A cryptographically VALID assertion that simply names no person — the shape
		// Cloudflare mints for a service token — is NOT a bad credential. Report "no
		// CF identity here" and let the caller try the bearer token, exactly as if no
		// assertion had been sent at all. This is the Mode B topology: the service
		// token proves the device may reach the origin, and our own ABS JWT proves
		// who the user is. Treating it as fatal (the original bug) 401'd every Mode B
		// request even when it carried a perfectly valid bearer alongside.
		//
		// Note this does NOT widen access: falling through lands on the bearer path,
		// which is itself fail-closed. A request with a service token and no bearer
		// still gets 401 — it has simply not proven who it is.
		if errors.Is(err, oauth.ErrNonIdentityAssertion) {
			// Record WHICH service token this was before falling through. It is the
			// only attribution a machine credential ever carries, and on the
			// fall-through path the request may still go on to authenticate as a
			// real user via its bearer — at which point the token and the person
			// appear together on one audit line, which is the pairing that makes an
			// anomaly visible (see absauth.AuditEvent.ServiceToken).
			if cn := oauth.CFAssertionCommonName(err); cn != "" && c != nil {
				c.Set(contextABSServiceTokenKey, cn)
			}
			return nil, nil
		}
		// Everything else FAILS CLOSED — forged signature, wrong issuer, wrong aud,
		// expired. This is the single most important difference from
		// CloudflareAccessAuth: no c.Next(), no fall-through to the bearer path. A
		// bad credential must not be rescued by presenting a second one.
		return nil, absErr(http.StatusUnauthorized, "assertion-invalid",
			"invalid Cloudflare Access assertion", ABSModeCF)
	}

	user, err := r.oauthCfg.ResolveUser(r.store, *claims)
	if err != nil {
		// A verified identity that is not on the allowlist is 403 and provisions
		// nothing. Anything else is a store failure, which is a 500 — reporting it as
		// 401/403 would tell the client its credential is dead when it is not.
		if errors.Is(err, oauth.ErrEmailNotAllowed) || errors.Is(err, oauth.ErrEmailNotVerified) {
			return nil, &ABSAuthError{
				Status: http.StatusForbidden, Reason: "email-not-allowed",
				Message: "identity is not authorized for this server",
				Mode:    ABSModeCF, Email: claims.Email,
			}
		}
		return nil, &ABSAuthError{
			Status: http.StatusInternalServerError, Reason: "identity-resolve-error",
			Message: "could not resolve identity", Mode: ABSModeCF, Email: claims.Email,
		}
	}
	if e := absCheckUserActive(user, ABSModeCF); e != nil {
		e.Email = claims.Email
		return nil, e
	}
	return &ABSIdentity{User: user, Mode: ABSModeCF, Email: claims.Email}, nil
}

// ResolveBearer runs ONLY step 2 — our own access JWT plus its abs_sess record.
func (r *ABSIdentityResolver) ResolveBearer(c *gin.Context) (*ABSIdentity, *ABSAuthError) {
	if !r.Enabled() || !r.cfg.Modes.JWT {
		return nil, nil
	}
	raw := absBearerFromRequest(c)
	if raw == "" {
		return nil, nil
	}
	now := r.clock()
	claims, err := r.cfg.ParseAccessToken(raw, now)
	if err != nil {
		// Both are a 401 — the client's cue to call /auth/refresh — but the audit
		// reason must say which. Reporting a routine expiry as token-invalid made
		// the fixed 30-day session death look like an ABS_JWT_SECRET change.
		if absauth.IsAccessTokenExpired(err) {
			return nil, absErr(http.StatusUnauthorized, "token-expired", "access token expired", ABSModeJWT)
		}
		return nil, absErr(http.StatusUnauthorized, "token-invalid", "invalid access token", ABSModeJWT)
	}

	session, err := r.store.GetABSSession(claims.SessionID)
	if err != nil {
		// Transient: 500, not 401 (§1.7.3 item 3).
		return nil, absErr(http.StatusInternalServerError, "session-lookup-error", "could not verify session", ABSModeJWT)
	}
	// A cryptographically valid JWT is not enough: revocation and expiry live on the
	// session record, which is what makes logout and /api/me/sessions meaningful.
	if session == nil || !session.Live(now) || session.UserID != claims.UserID {
		return nil, absErr(http.StatusUnauthorized, "session-not-live", "session not found", ABSModeJWT)
	}
	if r.cfg.SessionPastMaxLifetime(session.CreatedAt, now) {
		return nil, absErr(http.StatusUnauthorized, "session-max-lifetime", "session not found", ABSModeJWT)
	}

	user, err := r.store.GetUserByID(claims.UserID)
	if err != nil {
		return nil, absErr(http.StatusInternalServerError, "user-lookup-error", "could not load user", ABSModeJWT)
	}
	if user == nil {
		return nil, absErr(http.StatusUnauthorized, "user-missing", "session user no longer exists", ABSModeJWT)
	}
	// A password change, reset or deactivation bumps User.CredentialEpoch in the
	// same write as the change. A session stamped with an older epoch (including
	// one minted with the old password while the change was in flight) is dead,
	// whether or not the follow-up revoke-all reached it. Checked before the
	// active check so a pre-deactivation session reads as dead, not suspended.
	if !session.CredentialsCurrent(user) {
		return nil, absErr(http.StatusUnauthorized, "credentials-changed", "session not found", ABSModeJWT)
	}
	if e := absCheckUserActive(user, ABSModeJWT); e != nil {
		return nil, e
	}
	r.extendSession(c, session, now)
	return &ABSIdentity{User: user, Mode: ABSModeJWT, SessionID: session.ID, AccessToken: raw}, nil
}

// extendSession is sliding expiry on use: an authenticated request pushes the
// session's ExpiresAt out to SessionExpiry(now), so a device that keeps using the
// app never reaches the end of its session. It runs only after every check in
// ResolveBearer has passed.
//
// Throttled to about one write per session per day (SessionExtendWindow) so it is
// not a Pebble write per request. The pre-check here is a cheap filter on the copy
// already in hand; the authoritative decision is re-made by the store under its
// write lock against a fresh read, which is what keeps a parallel refresh or logout
// from being overwritten.
//
// A failure is audited and swallowed: the request is already authenticated, and
// failing it over a bookkeeping write would turn a transient store error into a
// visible error on a request that was entitled to succeed.
func (r *ABSIdentityResolver) extendSession(c *gin.Context, session *database.ABSSession, now time.Time) {
	target := r.cfg.SessionExpiry(session.CreatedAt, now)
	window := r.cfg.SessionExtendWindow()
	if session.ExpiresAt.IsZero() || target.Sub(session.ExpiresAt) < window {
		return
	}
	if _, err := r.store.ExtendABSSession(session.ID, now, target, window); err != nil {
		ev := absauth.AuditEvent{
			Action: "session-extend", Outcome: absauth.OutcomeError, Mode: ABSModeJWT,
			UserID: session.UserID, SessionID: session.ID, Reason: "session-extend-failed",
		}
		if c != nil && c.Request != nil {
			ev.SourceIP = c.ClientIP()
			ev.Path = c.Request.URL.Path
		}
		absauth.Audit(ev)
	}
}

// absAPIKeyStore is the OPTIONAL store slice needed to resolve an `abk_` key. It is
// asserted at runtime rather than folded into ABSIdentityStore so that the many
// existing test fakes implementing ABSIdentityStore keep compiling — a fake that
// does not implement it simply never resolves an API key, which is the safe default.
type absAPIKeyStore interface {
	GetAPIKeyByHash(hash string) (*database.APIKey, error)
}

// ResolveAPIKey runs step 3 — an `abk_` application API key presented against the
// ABS surface.
//
// 🔑 WHY THIS EXISTS. Before this, `absBearerFromRequest` discarded every `abk_`
// token, so the key minted at startup could reach /api/v1 but got a flat 401 from
// every ABS route. That made the ABS surface untestable without first performing a
// password login, and it is why the author-count and cold-cache measurements after
// PR #2122 could not be taken.
//
// This is NOT a bypass, and the distinction matters:
//
//   - Every existing key check still runs — hash lookup, revoked, inactive,
//     expiry, and owner-active. "Short-lived" is the key's OWN ExpiresAt, already
//     enforced here; no second TTL mechanism was invented.
//   - Permissions are the key's SCOPES intersected with the owner's role
//     permissions, exactly as /api/v1 computes them, so every ABS route keeps its
//     normal authz. A read-only key stays read-only.
//   - Resolve tries it FIRST whenever a key is presented, and then nothing
//     else (2026-10-07, owner decision: the key wins). A request carrying a
//     key and an Access assertion is the key's request, with every key
//     restriction, not the assertion's.
//
// There is no abs_sess record behind an API key, so SessionID stays empty; routes
// that genuinely need a session (logout, /api/me/sessions) will behave as they do
// for any sessionless identity rather than being handed a fake one.
func (r *ABSIdentityResolver) ResolveAPIKey(c *gin.Context) (*ABSIdentity, *ABSAuthError) {
	if !r.Enabled() || !r.cfg.Modes.JWT {
		return nil, nil
	}
	raw := absAPIKeyFromRequest(c)
	if raw == "" {
		return nil, nil
	}
	ks, ok := r.store.(absAPIKeyStore)
	if !ok {
		return nil, nil
	}

	key, err := ks.GetAPIKeyByHash(database.HashAPIKeyToken(raw))
	if err != nil {
		// Transient, so 500 rather than 401 — same reasoning as ResolveBearer.
		return nil, absErr(http.StatusInternalServerError, "apikey-lookup-error", "could not verify API key", ABSModeAPIKey)
	}
	if key == nil {
		return nil, absErr(http.StatusUnauthorized, "apikey-invalid", "invalid API key", ABSModeAPIKey)
	}
	switch key.Status {
	case "revoked":
		return nil, absErr(http.StatusUnauthorized, "apikey-revoked", "API key has been revoked", ABSModeAPIKey)
	case "inactive":
		return nil, absErr(http.StatusUnauthorized, "apikey-inactive", "API key is inactive", ABSModeAPIKey)
	}
	if code, msg, refused := APIKeyExpiryRefusal(key, time.Now()); refused {
		return nil, absErr(http.StatusUnauthorized, code, msg, ABSModeAPIKey)
	}

	user, err := r.store.GetUserByID(key.UserID)
	if err != nil {
		return nil, absErr(http.StatusInternalServerError, "user-lookup-error", "could not load user", ABSModeAPIKey)
	}
	if user == nil {
		return nil, absErr(http.StatusUnauthorized, "apikey-owner-missing", "API key owner no longer exists", ABSModeAPIKey)
	}
	if e := absCheckUserActive(user, ABSModeAPIKey); e != nil {
		return nil, e
	}

	return &ABSIdentity{User: user, Mode: ABSModeAPIKey, APIKeyScopes: key.Scopes}, nil
}

// Resolve applies the full §3.0.1 order and is what ABSRequireAuth uses.
func (r *ABSIdentityResolver) Resolve(c *gin.Context) (*ABSIdentity, *ABSAuthError) {
	if !r.Enabled() {
		return nil, absErr(http.StatusUnauthorized, "abs-auth-unavailable", "authentication required", "")
	}
	// A presented API key wins: the request is authenticated by the key
	// alone, whatever Access assertion or ABS token rides along with it.
	if PresentedAPIKey(c) != "" {
		if id, e := r.ResolveAPIKey(c); e != nil || id != nil {
			return id, e
		}
		// Keys are not accepted in the configured modes; refuse rather than
		// fall back to a weaker-recorded identity.
		return nil, absErr(http.StatusUnauthorized, "apikey-not-accepted", "API keys are not accepted here", ABSModeAPIKey)
	}
	if id, e := r.ResolveCFAssertion(c); e != nil {
		return nil, e
	} else if id != nil {
		return id, nil
	}
	if id, e := r.ResolveBearer(c); e != nil {
		return nil, e
	} else if id != nil {
		return id, nil
	}
	if id, e := r.ResolveAPIKey(c); e != nil {
		return nil, e
	} else if id != nil {
		return id, nil
	}
	return nil, absErr(http.StatusUnauthorized, "no-credential", "authentication required", "")
}

// Bind attaches a resolved identity to the gin and request contexts so handlers can
// use CurrentUser / auth.Can exactly as they do on /api/v1.
//
// Setting contextUserKey is load-bearing (see auth.go): only a stage that has FULLY
// verified an identity may do it. Every path reaching here has.
func (r *ABSIdentityResolver) Bind(c *gin.Context, id *ABSIdentity) {
	if c == nil || id == nil || id.User == nil {
		return
	}
	c.Set(contextUserKey, id.User)
	c.Set(contextABSModeKey, id.Mode)
	if id.SessionID != "" {
		c.Set(contextABSSessionKey, id.SessionID)
	}
	var perms []auth.Permission
	if r != nil && r.store != nil {
		perms = absEffectivePermissions(r.store, id.User)
		// An API key must not reach MORE through the ABS surface than it reaches
		// through /api/v1, so narrow role permissions by the key's scopes exactly
		// as handleAPIKeyAuth does. Without this line a read-only key would become
		// a full-privilege key the moment it was pointed at an ABS route.
		if id.Mode == ABSModeAPIKey {
			perms = intersectPermissions(perms, id.APIKeyScopes)
		}
	}
	ctx := auth.WithUser(c.Request.Context(), id.User)
	ctx = auth.WithPermissions(ctx, perms)
	// Every ABS identity (its JWT sessions, a CF assertion read through this
	// surface) is recorded as ABS, and an API key as an API key: none of them
	// counts as the owner's interactive login for owner-only actions.
	// An API key is recorded as one wherever it is presented, so it meets
	// every key restriction on this surface too.
	method := auth.MethodABS
	if id.Mode == ABSModeAPIKey {
		method = auth.MethodAPIKey
	}
	ctx = auth.WithMethod(ctx, method)
	ctx = auth.WithAccessEmail(ctx, "")
	c.Request = c.Request.WithContext(ctx)
}

// ABSServiceToken returns the Cloudflare service token's common_name for this
// request, or "" when it carried none.
//
// 🔴 FOR LOGGING ONLY. It names a credential shared by a GROUP of people (the owner
// mints separate friends/family/other/testing tokens so a revocation is contained),
// so it can never answer "who is this". Identity comes from SSO.
func ABSServiceToken(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, ok := c.Get(contextABSServiceTokenKey)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// seenServiceTokenPairings remembers which (service token -> user) pairings this
// process has already reported.
var seenServiceTokenPairings sync.Map // common_name -> userID

// noteServiceTokenPairing logs the token↔person pairing the FIRST time it is seen and
// again whenever it CHANGES for a given token.
//
// Why not log it on every request: the ABS surface is polled every 15-20 s per
// device, so an unconditional line would be pure journal noise — the same reason
// ABSAuthProbe is opt-in. Why log it at all: token↔person is normally stable, so the
// `family` token suddenly carrying a friend's SSO identity is a tripwire for either a
// compromised Google account or a leaked token, and it is invisible unless both
// values are recorded together.
//
// Steady state is one line per (token, person) per process lifetime. A token
// legitimately shared by several people therefore logs once per person and then goes
// quiet, which is the intended signal rather than a false positive.
func noteServiceTokenPairing(commonName string, id *ABSIdentity) {
	if commonName == "" || id == nil || id.User == nil {
		return
	}
	prev, loaded := seenServiceTokenPairings.Swap(commonName, id.User.ID)
	if loaded && prev == id.User.ID {
		return // already reported for this person
	}
	absauth.Audit(absauth.AuditEvent{
		Action:       "service-token-pairing",
		Outcome:      absauth.OutcomeSuccess,
		Mode:         id.Mode,
		UserID:       id.User.ID,
		Username:     id.User.Username,
		ServiceToken: commonName,
		Reason:       serviceTokenPairingReason(loaded),
	})
}

func serviceTokenPairingReason(hadPrevious bool) string {
	if hadPrevious {
		// The interesting one: this token was carrying somebody else a moment ago.
		return "service-token-now-used-by-a-different-user"
	}
	return "first-seen"
}

// ABSRequireAuth is the ABS group's authentication middleware. It never falls
// through: either an identity is bound or the request is aborted with a JSON body.
func ABSRequireAuth(r *ABSIdentityResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, authErr := r.Resolve(c)
		if authErr != nil {
			AbortABSAuth(c, "resolve", authErr)
			return
		}
		r.Bind(c, id)
		// A worker-only credential never reaches the ABS surface either.
		if abortWorkerOnlyOutsideWorkerAPI(c) {
			return
		}
		noteServiceTokenPairing(ABSServiceToken(c), id)
		c.Next()
	}
}

// AbortABSAuth writes the terminal auth failure as JSON and records the audit event.
//
// The body is always JSON and never empty: §1.8.6 / §1.7.3 item 11 — a 200 carrying
// HTML is fatal to both target clients' decoders, and an empty body is fatal to any
// typed endpoint, so the ABS surface never serves the SPA index.html.
func AbortABSAuth(c *gin.Context, action string, e *ABSAuthError) {
	if e == nil {
		return
	}
	absauth.Audit(absauth.AuditEvent{
		Action:    action,
		Outcome:   absOutcomeForStatus(e.Status),
		Mode:      e.Mode,
		SourceIP:  c.ClientIP(),
		Email:     e.Email,
		Reason:    e.Reason,
		Path:      c.Request.URL.Path,
		UserAgent: c.Request.UserAgent(),
		// On a FAILED attempt there is no user_id and never will be, so this is the
		// only attribution the record can carry.
		ServiceToken: ABSServiceToken(c),
	})
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	c.AbortWithStatusJSON(e.Status, gin.H{"error": msg})
}

func absOutcomeForStatus(status int) absauth.Outcome {
	switch {
	case status >= 500:
		return absauth.OutcomeError
	case status == http.StatusForbidden:
		return absauth.OutcomeDenied
	case status == http.StatusTooManyRequests:
		return absauth.OutcomeThrottled
	default:
		return absauth.OutcomeFailure
	}
}

// ABSSessionID returns the abs_sess id bound to this request, if any.
func ABSSessionID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, _ := c.Get(contextABSSessionKey)
	s, _ := v.(string)
	return s
}

// ABSAuthMode returns ABSModeCF or ABSModeJWT for this request, or "".
func ABSAuthMode(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, _ := c.Get(contextABSModeKey)
	s, _ := v.(string)
	return s
}

// absAssertionFromRequest reads the signed Cloudflare Access assertion.
//
// Header only, deliberately: the CF_Authorization cookie fallback that
// CloudflareAccessAuth keeps for browser flows is irrelevant to native ABS clients
// (iOS sandboxes the browser cookie jar away from an app's URLSession), and
// Cloudflare's own guidance for API clients is to validate this header. Fewer inputs,
// smaller surface.
func absAssertionFromRequest(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader))
}

// absBearerFromRequest reads our own access token from Authorization: Bearer, or from
// ?token= on a GET/HEAD.
//
// The ?token= form is MANDATORY, not a convenience: §1.7.2 verified that Absorb
// appends ?token= to cover, author-image and file URLs, and CarPlay does the same.
// It is accepted only on safe methods so a token cannot leak into a referer or a
// server log for a state-changing request.
func absBearerFromRequest(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		if tok := strings.TrimSpace(header[7:]); tok != "" {
			// An `abk_` token is an app API key, not an ABS access token. Ignore it
			// here so the two schemes stay strictly separate.
			if !strings.HasPrefix(tok, "abk_") {
				return tok
			}
			return ""
		}
	}
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
		// Same `abk_` exclusion as the header branch above. Without it an API key
		// sent as ?token= (AudioBooth's ebook-reader and Watch download URLs, for
		// a user signed in with an API key) was handed to ResolveBearer as a JWT,
		// failed token-invalid, and Resolve returned 401 before ResolveAPIKey —
		// which accepts the query form — ever ran. Measured on prod 2026-09-19.
		if tok := strings.TrimSpace(c.Query("token")); tok != "" && !strings.HasPrefix(tok, "abk_") {
			return tok
		}
	}
	return ""
}

// absAPIKeyFromRequest reads an `abk_` application API key from the request. It is
// the exact mirror of absBearerFromRequest: that one returns everything EXCEPT an
// `abk_` token, this one returns ONLY an `abk_` token, so the two credential schemes
// stay strictly separate and a token can only ever take one path.
//
// `?token=` is accepted on safe methods only, matching absBearerFromRequest, so a key
// cannot leak into a referer or an access log on a state-changing request.
// PresentedAPIKey returns the "abk_" key the request carries in the places
// the ABS surface accepts one, or "".
func PresentedAPIKey(c *gin.Context) string { return absAPIKeyFromRequest(c) }

func absAPIKeyFromRequest(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		if tok := strings.TrimSpace(header[7:]); strings.HasPrefix(tok, "abk_") {
			return tok
		}
	}
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
		if tok := strings.TrimSpace(c.Query("token")); strings.HasPrefix(tok, "abk_") {
			return tok
		}
	}
	return ""
}

// absEffectivePermissions resolves the union of a user's role permissions.
//
// It mirrors EffectivePermissionsFor (auth.go) but takes only the single method it
// needs, so ABSIdentityStore does not have to embed the whole six-method
// database.RoleStore and the existing helper's signature stays untouched. A user with
// no resolvable roles gets nil, which makes every auth.Can() check return false —
// the safe default.
func absEffectivePermissions(store interface {
	GetRoleByID(id string) (*database.Role, error)
}, user *database.User) []auth.Permission {
	if store == nil || user == nil || len(user.Roles) == 0 {
		return nil
	}
	seen := make(map[auth.Permission]struct{})
	for _, roleID := range user.Roles {
		role, err := store.GetRoleByID(roleID)
		if err != nil || role == nil {
			continue
		}
		for _, p := range role.Permissions {
			seen[p] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]auth.Permission, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

// absCheckUserActive rejects a user whose account is not active. A disabled account
// with a still-valid credential is 403, not 401: the credential is fine, the
// authorization is not.
func absCheckUserActive(user *database.User, mode string) *ABSAuthError {
	if user == nil {
		return absErr(http.StatusUnauthorized, "user-missing", "user not found", mode)
	}
	status := strings.ToLower(strings.TrimSpace(user.Status))
	if status != "" && status != "active" {
		return &ABSAuthError{
			Status: http.StatusForbidden, Reason: "user-inactive",
			Message: "account is not active", Mode: mode,
		}
	}
	return nil
}
