// file: internal/server/middleware/absauth.go
// version: 1.1.0
// guid: e7051b93-6c28-4a0f-9d34-b8f2a61c05de
// last-edited: 2026-08-01

package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
)

// ABS auth mode names as reported by ABSAuthMode.
const (
	// ABSModeCF means the request was identified by a VERIFIED
	// Cf-Access-Jwt-Assertion (design spec Modes C and A).
	ABSModeCF = "cf"
	// ABSModeJWT means the request was identified by our own bearer access token
	// (design spec Mode B).
	ABSModeJWT = "jwt"
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
}

// NewABSIdentityResolver wires the resolver. A nil cfg, or a cfg whose ABS API is
// disabled, makes every request 401 — a wiring mistake must never open a door.
func NewABSIdentityResolver(cfg *absauth.Config, verifier CFAssertionVerifier, oauthCfg *oauth.Config, store ABSIdentityStore) *ABSIdentityResolver {
	return &ABSIdentityResolver{cfg: cfg, verifier: verifier, oauthCfg: oauthCfg, store: store}
}

// ABSIdentity is a successfully resolved ABS identity.
type ABSIdentity struct {
	User *database.User
	// Mode is ABSModeCF or ABSModeJWT.
	Mode string
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

func (e *ABSAuthError) Error() string { return e.Reason }

func absErr(status int, reason, message, mode string) *ABSAuthError {
	return &ABSAuthError{Status: status, Reason: reason, Message: message, Mode: mode}
}

// ABSCredentialPresent reports whether the request carries anything the ABS resolver
// could act on. Used by POST /login, which must work with no credential at all but
// must skip the password check when a verified assertion is present.
func (r *ABSIdentityResolver) ABSCredentialPresent(c *gin.Context) bool {
	if r == nil {
		return false
	}
	return absAssertionFromRequest(c) != "" || absBearerFromRequest(c) != ""
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
	claims, err := r.cfg.ParseAccessToken(raw)
	if err != nil {
		return nil, absErr(http.StatusUnauthorized, "token-invalid", "invalid access token", ABSModeJWT)
	}

	session, err := r.store.GetABSSession(claims.SessionID)
	if err != nil {
		// Transient: 500, not 401 (§1.7.3 item 3).
		return nil, absErr(http.StatusInternalServerError, "session-lookup-error", "could not verify session", ABSModeJWT)
	}
	// A cryptographically valid JWT is not enough: revocation and expiry live on the
	// session record, which is what makes logout and /api/me/sessions meaningful.
	if session == nil || !session.Live(time.Now()) || session.UserID != claims.UserID {
		return nil, absErr(http.StatusUnauthorized, "session-not-live", "session not found", ABSModeJWT)
	}

	user, err := r.store.GetUserByID(claims.UserID)
	if err != nil {
		return nil, absErr(http.StatusInternalServerError, "user-lookup-error", "could not load user", ABSModeJWT)
	}
	if user == nil {
		return nil, absErr(http.StatusUnauthorized, "user-missing", "session user no longer exists", ABSModeJWT)
	}
	if e := absCheckUserActive(user, ABSModeJWT); e != nil {
		return nil, e
	}
	return &ABSIdentity{User: user, Mode: ABSModeJWT, SessionID: session.ID, AccessToken: raw}, nil
}

// Resolve applies the full §3.0.1 order and is what ABSRequireAuth uses.
func (r *ABSIdentityResolver) Resolve(c *gin.Context) (*ABSIdentity, *ABSAuthError) {
	if !r.Enabled() {
		return nil, absErr(http.StatusUnauthorized, "abs-auth-unavailable", "authentication required", "")
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
	}
	ctx := auth.WithUser(c.Request.Context(), id.User)
	ctx = auth.WithPermissions(ctx, perms)
	c.Request = c.Request.WithContext(ctx)
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
		if tok := strings.TrimSpace(c.Query("token")); tok != "" {
			return tok
		}
	}
	return ""
}

// absEffectivePermissions resolves the union of a user's role permissions.
//
// It mirrors effectivePermissionsFor (auth.go) but takes only the single method it
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
