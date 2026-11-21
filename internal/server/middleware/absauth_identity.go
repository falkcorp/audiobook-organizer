// file: internal/server/middleware/absauth_identity.go
// version: 0.1.0
// guid: 5a3e8c17-d2b4-4f69-8e01-7c9b2d6f4a38
// last-edited: 2026-11-21

package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// The ABS surface under core identity (F4-08 B10b1). Under LegacyAuthGin
// ABSRequireAuth is HEAD's: the surface verifies its own token and reads the
// user, the epoch and the key from books' store. Once the identity cutover has
// run, the account record is core's: an epoch bump, a disable, a revoked key
// lands in core's store only, and a read of books' rows would miss it and
// admit what HEAD refused. So a server wired with legacyauth.WithIdentity
// keeps what is the surface's own (the ABS access token, its abs_sess record)
// and sends everything about the account to core's compat handle:
//
//   - a bearer token: the JWT and its session record are checked here
//     exactly as HEAD checks them; the user is then Compat.PrincipalFor with
//     the epoch the SESSION was stamped at (never a fresh read, which would
//     match by construction) and no scopes, so core answers standing, epoch,
//     sign-in restriction and its throttle;
//   - an abk_ key: the raw key goes to Compat.AuthenticateKey, which applies
//     every stage 12 key rule;
//   - both then through Compat.CheckCredentialFor with the route's manifest
//     Perm (rbac.Delegated of the compat method), which re-reads a key and
//     refuses a confined credential (an fp-worker key) outside its scopes;
//   - a verified Cloudflare Access assertion naming a person is refused
//     (ResolveCFAssertion): resolving one reads and writes books' store.
//
// Nothing here runs without an Identity bound by Identity.Bind.

// absCorePerm is the manifest Perm of every ABS row (legacy_manifest.go pABS).
var absCorePerm = rbac.Delegated(string(legacyauth.MethodBooksABS))

// absCoreSeen is what coreCredential learned about the request that the later
// stages need: which HEAD mode decided it and the session record behind a
// bearer token. One per request.
type absCoreSeen struct {
	mode    string
	reason  string
	email   string
	session *database.ABSSession
}

// requireThroughCore is ABSRequireAuth's body under core identity.
func (r *ABSIdentityResolver) requireThroughCore(c *gin.Context, id *legacyauth.Identity) {
	seen := &absCoreSeen{}
	legacyauth.ABSRequireAuthThen(id.Compat,
		func(c *gin.Context) (legacyauth.ABSCredential, error) { return r.coreCredential(c, id, seen) },
		absCorePerm,
		func(c *gin.Context, p *coreauth.Principal, _ legacyauth.ABSCredential) bool {
			return r.bindCore(c, id, p, seen)
		},
		func(c *gin.Context, status int, err error) { recordCoreRefusal(c, status, err, seen) },
	)(c)
}

// coreCredential is HEAD's Resolve with every read of the account removed: it
// verifies what the surface verifies (a presented key's presence, an assertion,
// the access token and its session record) and names the credential for core.
// The order is HEAD's (a key wins; then the assertion; then the bearer), and
// each refusal keeps HEAD's status.
func (r *ABSIdentityResolver) coreCredential(c *gin.Context, id *legacyauth.Identity, seen *absCoreSeen) (legacyauth.ABSCredential, error) {
	refuse := func(status int, reason, msg, mode string) (legacyauth.ABSCredential, error) {
		seen.reason, seen.mode = reason, mode
		return legacyauth.ABSCredential{}, &legacyauth.ABSRefusal{Status: status, Message: msg}
	}
	if !r.Enabled() {
		return refuse(http.StatusUnauthorized, "abs-auth-unavailable", "authentication required", "")
	}
	client := id.Client(c.Request)

	// A presented key wins, whatever assertion or token rides along.
	if key := PresentedAPIKey(c); key != "" {
		if !r.cfg.Modes.JWT {
			// Keys are not accepted in the configured modes; refuse rather
			// than fall back to a weaker-recorded identity.
			return refuse(http.StatusUnauthorized, "apikey-not-accepted", "API keys are not accepted here", ABSModeAPIKey)
		}
		seen.mode = ABSModeAPIKey
		return legacyauth.ABSCredential{Key: key, Client: client}, nil
	}

	if cf, e := r.ResolveCFAssertion(c); e != nil {
		seen.email = e.Email
		return refuse(e.Status, e.Reason, e.Message, e.Mode)
	} else if cf != nil {
		// ResolveCFAssertion refuses every person under core identity; a
		// resolved one here would be a user from books' store.
		return refuse(http.StatusForbidden, ABSReasonCFUnderIdentity, "single sign-on is not available on this surface", ABSModeCF)
	}

	if !r.cfg.Modes.JWT {
		return refuse(http.StatusUnauthorized, "no-credential", "authentication required", "")
	}
	raw := absBearerFromRequest(c)
	if raw == "" {
		return refuse(http.StatusUnauthorized, "no-credential", "authentication required", "")
	}
	now := r.clock()
	claims, err := r.cfg.ParseAccessToken(raw, now)
	if err != nil {
		if absauth.IsAccessTokenExpired(err) {
			return refuse(http.StatusUnauthorized, "token-expired", "access token expired", ABSModeJWT)
		}
		return refuse(http.StatusUnauthorized, "token-invalid", "invalid access token", ABSModeJWT)
	}
	session, err := r.store.GetABSSession(claims.SessionID)
	if err != nil {
		// Transient: 500, not 401 (§1.7.3 item 3).
		return refuse(http.StatusInternalServerError, "session-lookup-error", "could not verify session", ABSModeJWT)
	}
	if session == nil || !session.Live(now) || session.UserID != claims.UserID {
		return refuse(http.StatusUnauthorized, "session-not-live", "session not found", ABSModeJWT)
	}
	if r.cfg.SessionPastMaxLifetime(session.CreatedAt, now) {
		return refuse(http.StatusUnauthorized, "session-max-lifetime", "session not found", ABSModeJWT)
	}
	seen.mode, seen.session = ABSModeJWT, session
	return legacyauth.ABSCredential{
		User:   &coreauth.User{ID: coreauth.UserID(session.UserID)},
		Epoch:  session.CredentialEpoch,
		Client: client,
	}, nil
}

// bindCore fills what the handlers behind the surface read once core admitted
// the credential: HEAD's user, permission set and method (from the principal,
// Bridge.FromCorePrincipal), the gin user, the ABS mode and session. It then
// runs what HEAD ran after Bind: the worker-only confinement, the sliding
// extension (only now that core admitted the session, never for a refused
// one) and the service-token pairing note. false means it refused.
func (r *ABSIdentityResolver) bindCore(c *gin.Context, id *legacyauth.Identity, p *coreauth.Principal, seen *absCoreSeen) bool {
	ctx, err := id.Bridge.FromCorePrincipal(c.Request.Context())
	var user *database.User
	if err == nil {
		var ok bool
		if user, ok = auth.UserFromContext(ctx); !ok || user == nil {
			err = legacyauth.ErrUnauthenticated
		}
	}
	if err != nil {
		seen.reason = "principal-unmapped"
		recordCoreRefusal(c, http.StatusUnauthorized, err, seen)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return false
	}
	if seen.mode == ABSModeAPIKey {
		// An ABS-surface key is recorded as the key it is wherever it is
		// presented, so it meets every key restriction on this surface too.
		ctx = auth.WithMethod(ctx, auth.MethodAPIKey)
	}
	ctx = auth.WithAccessEmail(ctx, "")
	c.Request = c.Request.WithContext(ctx)
	BindCurrentUser(c, user)
	c.Set(contextABSModeKey, seen.mode)
	if seen.session != nil {
		c.Set(contextABSSessionKey, seen.session.ID)
	}
	if abortWorkerOnlyOutsideWorkerAPI(c) {
		return false
	}
	if seen.session != nil {
		r.extendSession(c, seen.session, r.clock())
	}
	noteServiceTokenPairing(ABSServiceToken(c), &ABSIdentity{User: user, Mode: seen.mode})
	return true
}

// recordCoreRefusal audits a refusal of the core path with HEAD's fixed
// vocabulary of reasons: the one coreCredential or bindCore named, else the
// one the compat refusal implies. No reason names a credential, a user or a key.
func recordCoreRefusal(c *gin.Context, status int, err error, seen *absCoreSeen) {
	reason := seen.reason
	if reason == "" {
		reason = coreRefusalReason(err)
	}
	if errors.Is(err, coreauth.ErrThrottled) {
		absauth.SetRetryAfter(c)
	}
	recordABSRefusal(c, "resolve", &ABSAuthError{Status: status, Reason: reason, Mode: seen.mode, Email: seen.email})
}

func coreRefusalReason(err error) string {
	switch {
	case err == nil:
		return "abs-auth-unconfigured"
	case errors.Is(err, coreauth.ErrStaleEpoch):
		return "credentials-changed"
	case errors.Is(err, coreauth.ErrInactiveUser):
		return "user-inactive"
	case errors.Is(err, coreauth.ErrResetRequired):
		return "reset-required"
	case errors.Is(err, coreauth.ErrConfined):
		return "confined-credential"
	case errors.Is(err, coreauth.ErrThrottled):
		return "core-throttled"
	case errors.Is(err, coreauth.ErrStoreUnavailable):
		return "core-store-unavailable"
	case errors.Is(err, coreauth.ErrRevoked):
		return "credential-revoked"
	case errors.Is(err, coreauth.ErrExpired):
		return "credential-expired"
	default:
		return "credential-refused"
	}
}
