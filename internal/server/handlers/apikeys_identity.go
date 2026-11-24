// file: internal/server/handlers/apikeys_identity.go
// version: 0.1.0
// guid: 5e1b7c93-a2d4-4f68-8b30-9d6e2f41c7a5
// last-edited: 2026-11-22

package handlers

import (
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// HEAD's /api/v1/auth/api-keys under core identity (F4-08 B10z). M3 moves
// books' apikey: rows into core and retires the claim, so under the flip the
// HEAD store holds no key and refuses every key write. Each route keeps its
// HEAD path, its HEAD checks (in HEAD's order) and its HEAD response shape, and
// reads and writes core's keys through Identity.Admin, which acts as the
// principal stage 12 bound and applies core's own rules on top (the actor's
// effective set now, dominance over another owner, a key actor clamped to its
// own expiry). A new key's scopes come from Identity.Keys, the one rule M3
// applies to every key it imports, so a key created after the flip holds what
// an imported key with the same HEAD row holds.

// apiKeyLog is the key routes' log under core identity. No key value, raw or
// hashed, is ever logged.
var apiKeyLog = logger.New("handlers.apikeys")

// keyForAnotherUserMessage is the 403 of a key-authenticated caller asking to
// create a key for another user.
const keyForAnotherUserMessage = "API keys cannot create keys for other users; sign in to do that"

// errKeysNotWired is the 503 of a server whose Identity carries no key rule.
const errKeysNotWired = "api keys are not available on this server: the identity wiring has no key rule"

// corePrincipal is the principal stage 12 bound, answering 401 when there is
// none with a user (the auth-off principal never reaches here: CurrentUser
// refused it first, as HEAD did).
func corePrincipal(c *gin.Context, id *legacyauth.Identity) (*coreauth.Principal, bool) {
	p, ok := coreauth.PrincipalFrom(c.Request.Context())
	if !ok || p == nil || p.User == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return nil, false
	}
	if id.Admin == nil || id.Keys == nil {
		httputil.RespondWithServiceUnavailable(c, errKeysNotWired)
		return nil, false
	}
	return p, true
}

// respondCoreKeyError answers a refusal from core's key methods in HEAD's
// vocabulary. No core error text that could carry a credential reaches the
// body: the 403s use fixed messages, and a 400 carries ErrBadInput's text,
// which names fields and limits only.
func respondCoreKeyError(c *gin.Context, err error, what string) {
	switch {
	case errors.Is(err, coreauth.ErrScopeEscalation):
		httputil.RespondWithForbidden(c, "cannot grant scope you don't have")
	case errors.Is(err, coreauth.ErrInactiveUser):
		httputil.RespondWithForbidden(c, "the key's owner is not active")
	case errors.Is(err, coreauth.ErrDenied):
		httputil.RespondWithForbidden(c, "access denied")
	case errors.Is(err, coreauth.ErrKeyRevoked):
		httputil.RespondWithConflict(c, "api key is revoked")
	case errors.Is(err, coreauth.ErrBadInput):
		httputil.RespondWithBadRequest(c, err.Error())
	default:
		httputil.InternalError(c, what, err)
	}
}

// createUnderIdentity is Create's tail under core identity, after the checks
// both modes share (the caller, the body, the expiry asked for, the admin
// check for another user's key, every atom known and held by the caller).
func (h *APIKeyHandler) createUnderIdentity(c *gin.Context, id *legacyauth.Identity, req CreateAPIKeyRequest, target string, asked time.Time) {
	p, ok := corePrincipal(c, id)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	// HEAD's clampToCallingKey, in its place. A method that may change
	// credentials (a signed-in person) is not clamped. A key is clamped by
	// core (CreateAPIKey shortens a key actor's new key to its own expiry, and
	// a core key always has one). Every other method had no calling key to
	// clamp to on HEAD and was refused: it still is.
	method := auth.MethodFromContext(ctx)
	// A key never mints for another user (F4-08 B10rn, STOP 2 ruling, option 2).
	// Since B10g the conditional credRouteWhen guard passes through under core
	// identity, so this handler refusal is the live gate. It does not rest on
	// core's refusal or on the clamp below.
	if method == auth.MethodAPIKey && target != string(p.User.ID) {
		httputil.RespondWithForbidden(c, keyForAnotherUserMessage)
		return
	}
	if !servermiddleware.CredentialChangeAllowed(c) && method != auth.MethodAPIKey {
		httputil.RespondWithForbidden(c, noClampMessage)
		return
	}
	// The empty-list refusal keeps its HEAD place, after the clamp.
	if len(req.Scopes) == 0 && method == auth.MethodAPIKey {
		httputil.RespondWithForbidden(c, "an API key must name the scopes of the key it creates")
		return
	}
	owner, err := id.Admin.KeyOwnerScopes(ctx, p, coreauth.UserID(target))
	if errors.Is(err, coreauth.ErrNotFound) {
		httputil.RespondWithNotFound(c, "user", target)
		return
	}
	if err != nil {
		respondCoreKeyError(c, err, "failed to create api key")
		return
	}
	scopes, unknown, err := id.Keys.Scopes(req.Scopes, owner)
	if err != nil {
		httputil.InternalError(c, "failed to map the key's scopes", err)
		return
	}
	if len(unknown) > 0 {
		// auth.IsKnown passed it, but the atom table core's scopes come from
		// has no row for it: minting would drop it silently.
		httputil.RespondWithBadRequest(c, "scope has no equivalent on this server: "+unknown[0])
		return
	}
	raw, k, err := id.Admin.AdminCreateAPIKey(ctx, p, coreauth.KeySpec{
		Owner: coreauth.UserID(target), Name: req.Name, Description: req.Description, Scopes: scopes, ExpiresAt: asked,
	})
	if err != nil {
		respondCoreKeyError(c, err, "failed to create api key")
		return
	}
	// Core converts ExpiresAt to a lifetime on its own clock and back, so an
	// unclamped key expires exactly when asked; earlier means it clamped.
	clamped := k.ExpiresAt.Before(asked)
	apiKeyLog.Info("apikey created: id=%s user=%s name=%s scopes=%s expires=%s clamped_to_calling_key=%t", k.ID,
		logger.SanitizeLogValue(target), logger.SanitizeLogValue(k.Name), logger.SanitizeLogValue(fmt.Sprint(req.Scopes)),
		k.ExpiresAt.Format(time.RFC3339), clamped)
	atoms := req.Scopes
	if len(atoms) == 0 {
		atoms = []string{}
	}
	exp := k.ExpiresAt
	resp := CreateAPIKeyResponse{ID: k.ID, Name: k.Name, Token: raw, Scopes: atoms, ExpiresAt: &exp, CreatedAt: k.CreatedAt}
	if clamped {
		resp.Note = clampedNote
	}
	httputil.RespondWithCreated(c, resp)
}

// listUnderIdentity is List under core identity: the caller's keys, or with
// ?all=true and users.manage every key core lets the caller read.
func (h *APIKeyHandler) listUnderIdentity(c *gin.Context, id *legacyauth.Identity, callerID string) {
	p, ok := corePrincipal(c, id)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	showAll := c.Query("all") == "true" && isAdminUser(c)
	results := make([]APIKeyResponse, 0)
	unreadable := 0
	names := map[coreauth.UserID]string{}
	for k, err := range id.Admin.AdminAPIKeys(ctx, p, showAll) {
		if err != nil {
			// One unreadable key record must not turn the whole listing
			// into a 500, as on HEAD.
			if errors.Is(err, coreauth.ErrUndecodableRecord) {
				unreadable++
				continue
			}
			respondCoreKeyError(c, err, "failed to list api keys")
			return
		}
		username := ""
		if showAll {
			name, cached := names[k.Owner]
			if !cached && id.Users != nil {
				if u, uerr := id.Users.User(ctx, k.Owner); uerr == nil && u != nil {
					name = u.Username
				}
				names[k.Owner] = name
			}
			username = name
		}
		results = append(results, coreAPIKeyResponse(k, username, id.Keys))
	}
	if unreadable > 0 {
		apiKeyLog.Warn("apikey list: some keys could not be read: caller=%s all=%t unreadable=%d", logger.SanitizeLogValue(callerID), showAll, unreadable)
	}
	httputil.RespondWithOK(c, gin.H{"api_keys": results, "count": len(results), "unreadable": unreadable})
}

// coreKey reads one key for the caller, answering HEAD's 404, 403 and 500.
func coreKey(c *gin.Context, id *legacyauth.Identity, p *coreauth.Principal, keyID string) (*coreauth.APIKey, bool) {
	k, err := id.Admin.AdminAPIKey(c.Request.Context(), p, keyID)
	switch {
	case errors.Is(err, coreauth.ErrNotFound):
		httputil.RespondWithNotFound(c, "api key", keyID)
		return nil, false
	case errors.Is(err, coreauth.ErrDenied):
		httputil.RespondWithForbidden(c, "access denied")
		return nil, false
	case err != nil:
		httputil.InternalError(c, "failed to get api key", err)
		return nil, false
	}
	return k, true
}

// getUnderIdentity is Get under core identity.
func (h *APIKeyHandler) getUnderIdentity(c *gin.Context, id *legacyauth.Identity) {
	p, ok := corePrincipal(c, id)
	if !ok {
		return
	}
	k, ok := coreKey(c, id, p, c.Param("id"))
	if !ok {
		return
	}
	username := ""
	if isAdminUser(c) && id.Users != nil {
		if u, err := id.Users.User(c.Request.Context(), k.Owner); err == nil && u != nil {
			username = u.Username
		}
	}
	httputil.RespondWithOK(c, coreAPIKeyResponse(k, username, id.Keys))
}

// updateStatusUnderIdentity is UpdateStatus under core identity.
func (h *APIKeyHandler) updateStatusUnderIdentity(c *gin.Context, id *legacyauth.Identity, callerID string) {
	p, ok := corePrincipal(c, id)
	if !ok {
		return
	}
	keyID := c.Param("id")
	k, ok := coreKey(c, id, p, keyID)
	if !ok {
		return
	}
	if k.Status == coreauth.KeyRevoked || !k.RevokedAt.IsZero() {
		httputil.RespondWithConflict(c, "api key is revoked")
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	var st coreauth.KeyStatus
	switch req.Status {
	case "revoked":
		httputil.RespondWithBadRequest(c, "use DELETE to revoke a key")
		return
	case "active":
		st = coreauth.KeyActive
	case "inactive":
		st = coreauth.KeyDisabled
	default:
		httputil.RespondWithBadRequest(c, "status must be 'active' or 'inactive'")
		return
	}
	if err := id.Admin.AdminSetAPIKeyStatus(c.Request.Context(), p, keyID, st); err != nil {
		if errors.Is(err, coreauth.ErrKeyRevoked) && k.Status == coreauth.KeyNeedsConfirmation {
			httputil.RespondWithConflict(c, "api key needs confirmation: set it active or revoke it")
			return
		}
		respondCoreKeyError(c, err, "failed to update api key status")
		return
	}
	apiKeyLog.Info("apikey status changed: id=%s caller=%s status=%s", logger.SanitizeLogValue(keyID), logger.SanitizeLogValue(callerID), logger.SanitizeLogValue(req.Status))
	updated, err := id.Admin.AdminAPIKey(c.Request.Context(), p, keyID)
	if err != nil {
		httputil.RespondWithOK(c, gin.H{"status": req.Status})
		return
	}
	httputil.RespondWithOK(c, coreAPIKeyResponse(updated, "", id.Keys))
}

// revokeUnderIdentity is Revoke under core identity.
func (h *APIKeyHandler) revokeUnderIdentity(c *gin.Context, id *legacyauth.Identity, callerID string) {
	p, ok := corePrincipal(c, id)
	if !ok {
		return
	}
	keyID := c.Param("id")
	k, ok := coreKey(c, id, p, keyID)
	if !ok {
		return
	}
	if err := id.Admin.AdminRevokeAPIKey(c.Request.Context(), p, keyID); err != nil {
		respondCoreKeyError(c, err, "failed to revoke api key")
		return
	}
	apiKeyLog.Info("apikey revoked: id=%s caller=%s name=%s", logger.SanitizeLogValue(keyID), logger.SanitizeLogValue(callerID), logger.SanitizeLogValue(k.Name))
	httputil.RespondWithNoContent(c)
}

// coreAPIKeyResponse is HEAD's APIKeyResponse for a core key. Scopes are the
// HEAD atoms the key's core scopes reach (Keys.Atoms); core keeps no
// deactivation time, and a key core holds for confirmation after a restore
// shows its own status.
func coreAPIKeyResponse(k *coreauth.APIKey, username string, keys legacyauth.KeyScoper) APIKeyResponse {
	status := string(k.Status)
	if k.Status == coreauth.KeyDisabled {
		status = "inactive"
	}
	prefix := string(coreauth.KindKey)
	if k.Imported {
		prefix = "abk_"
	}
	ident := ""
	if len(k.HashPrefix) >= 8 {
		ident = prefix + k.HashPrefix[:8]
	}
	at := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}
	var daysSince *int
	if !k.LastUsedAt.IsZero() {
		d := int(time.Since(k.LastUsedAt).Hours() / 24)
		daysSince = &d
	}
	ip := ""
	if k.LastUsedIP.IsValid() {
		ip = k.LastUsedIP.String()
	}
	return APIKeyResponse{
		ID: k.ID, UserID: string(k.Owner), Name: k.Name, Description: k.Description,
		Scopes: keys.Atoms(k.Scopes), Status: status, CreatedAt: k.CreatedAt,
		LastUsedAt: at(k.LastUsedAt), LastUsedIP: ip, UseCount: k.UseCount,
		ExpiresAt: at(k.ExpiresAt), RevokedAt: at(k.RevokedAt),
		Identifier: ident, DaysSinceLastUse: daysSince, NeverUsed: k.LastUsedAt.IsZero(),
		Username: username,
	}
}
