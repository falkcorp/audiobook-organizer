// file: internal/server/handlers/apikeys.go
// version: 2.5.1
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-11-09

package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Every API key expires (2026-10-07). See
// docs/plans/2026-10-07-apikey-expiry-and-privilege.md, D5–D8.
const (
	// DefaultAPIKeyTTLDays is the lifetime of a key created without
	// expires_in_days and of the one-time stamp given to keys that never
	// expired.
	DefaultAPIKeyTTLDays = 30
	// DefaultAPIKeyTTL is DefaultAPIKeyTTLDays as a duration.
	DefaultAPIKeyTTL = DefaultAPIKeyTTLDays * 24 * time.Hour
	// MaxAPIKeyTTLDays is the longest lifetime a key may be given: three
	// years of 365 days (owner decision, 2026-11-20). A set-and-forget
	// integration renews once every three years, and a leaked, forgotten key
	// still dies on its own. It bounds expires_in_days and expires_at alike,
	// counted from the moment of the request; over it is a 400, not a clamp.
	// Three years is 1095 days, not three calendar years: a span that
	// crosses a leap day is 1096 days long, and one number for both request
	// fields, the UI's date picker and core's max_ttl keeps a picked date
	// from being refused by one of them.
	MaxAPIKeyTTLDays = 1095
	// MaxAPIKeyTTL is MaxAPIKeyTTLDays as a duration.
	MaxAPIKeyTTL = MaxAPIKeyTTLDays * 24 * time.Hour
)

// clampedNote explains a clamped expiry in the create response.
const clampedNote = "expiry shortened to the expiry of the API key that made this request: a key cannot mint a key that outlives it (sign in to create a longer-lived one)"

// clampToCallingKey limits exp to the expiry of the API key that
// authenticated the request: a key may not mint a key that outlives it (an 8h
// bootstrap key must not turn into a 365-day one). A signed-in person is not
// clamped. Reports whether it clamped, and ok=false when the caller is not a
// person and no calling key with a real expiry is on the request — the clamp
// then has nothing to clamp to, and the caller must refuse rather than mint
// an unclamped key (it used to return exp unchanged there: a key with no
// expiry minted keys of any length, the 2026-10-07 review's fail-open).
func clampToCallingKey(c *gin.Context, exp time.Time) (clampedExp time.Time, clamped, ok bool) {
	if servermiddleware.CredentialChangeAllowed(c) {
		return exp, false, true
	}
	k, found := servermiddleware.CurrentAPIKey(c)
	if !found || k == nil || k.ExpiresAt == nil || k.ExpiresAt.IsZero() {
		return time.Time{}, false, false
	}
	if exp.After(*k.ExpiresAt) {
		return *k.ExpiresAt, true, true
	}
	return exp, false, true
}

// noClampMessage is the 403 when clampToCallingKey has nothing to clamp to.
const noClampMessage = "this credential has no expiry to limit a new key to; sign in to create keys"

// ---- Request / response types -----------------------------------------------

// CreateAPIKeyRequest is the JSON body for POST /api/v1/auth/api-keys. The
// expiry is expires_in_days or expires_at, never both; with neither the key
// gets DefaultAPIKeyTTLDays.
type CreateAPIKeyRequest struct {
	Name          string   `json:"name" binding:"required"`
	Description   string   `json:"description"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays int      `json:"expires_in_days"`
	// ExpiresAt is an RFC 3339 instant, after the request and no later than
	// MaxAPIKeyTTL from it. A string, not a time.Time, so a malformed value
	// gets requestedExpiry's message rather than encoding/json's.
	ExpiresAt string `json:"expires_at"`
	UserID    string `json:"user_id"`
}

// requestedExpiry is the expiry req asks for at now, before any clamp to the
// calling key. Its error is the 400 message.
func requestedExpiry(req CreateAPIKeyRequest, now time.Time) (time.Time, error) {
	if req.ExpiresAt != "" {
		if req.ExpiresInDays != 0 {
			return time.Time{}, errors.New("send expires_in_days or expires_at, not both")
		}
		at, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			return time.Time{}, fmt.Errorf("expires_at must be an RFC 3339 date-time such as 2027-01-31T12:00:00Z, got %q", req.ExpiresAt)
		}
		if !at.After(now) {
			return time.Time{}, errors.New("expires_at must be in the future")
		}
		if at.After(now.Add(MaxAPIKeyTTL)) {
			return time.Time{}, fmt.Errorf("expires_at must be no more than %d days (3 years) from now", MaxAPIKeyTTLDays)
		}
		return at.UTC(), nil
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > MaxAPIKeyTTLDays {
		return time.Time{}, fmt.Errorf("expires_in_days must be between 1 and %d (3 years); omit it or send 0 for the default of %d", MaxAPIKeyTTLDays, DefaultAPIKeyTTLDays)
	}
	days := req.ExpiresInDays
	if days == 0 {
		days = DefaultAPIKeyTTLDays
	}
	return now.Add(time.Duration(days) * 24 * time.Hour), nil
}

// CreateAPIKeyResponse is returned after successfully creating an API key.
type CreateAPIKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	// Note explains an expiry shorter than the one asked for.
	Note string `json:"note,omitempty"`
}

// APIKeyResponse is the JSON shape for a single API key (list and detail endpoints).
type APIKeyResponse struct {
	ID               string     `json:"id"`
	UserID           string     `json:"user_id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Scopes           []string   `json:"scopes"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP       string     `json:"last_used_ip,omitempty"`
	UseCount         int64      `json:"use_count"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	DeactivatedAt    *time.Time `json:"deactivated_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	Identifier       string     `json:"identifier"`
	DaysSinceLastUse *int       `json:"days_since_last_use"`
	NeverUsed        bool       `json:"never_used"`
	Username         string     `json:"username,omitempty"`
}

// ---- Narrow interface --------------------------------------------------------

// APIKeyHandlerStore is the narrow database interface APIKeyHandler requires.
// Named distinctly from database.APIKeyStore to avoid import collisions.
type APIKeyHandlerStore interface {
	CreateAPIKey(key *database.APIKey) (*database.APIKey, error)
	GetAPIKey(id string) (*database.APIKey, error)
	GetUserByID(id string) (*database.User, error)
	ListAllAPIKeys() ([]database.APIKey, error)
	ListAPIKeysForUser(userID string) ([]database.APIKey, error)
	RevokeAPIKey(id string) error
	SetAPIKeyStatus(id, status string, at time.Time) error
}

// ---- Handler -----------------------------------------------------------------

// APIKeyHandler handles all /auth/api-keys routes.
type APIKeyHandler struct {
	store APIKeyHandlerStore
	// now is the clock a new key's expiry is counted from: time.Now outside
	// tests, which pin it to test the cap to the second.
	now func() time.Time
}

// NewAPIKeyHandler constructs an APIKeyHandler.
func NewAPIKeyHandler(store APIKeyHandlerStore) *APIKeyHandler {
	return &APIKeyHandler{store: store, now: time.Now}
}

func buildAPIKeyResponse(key database.APIKey, username string) APIKeyResponse {
	var daysSince *int
	neverUsed := key.LastUsedAt == nil
	if key.LastUsedAt != nil {
		d := int(time.Since(*key.LastUsedAt).Hours() / 24)
		daysSince = &d
	}
	ident := ""
	if len(key.TokenHash) >= 8 {
		ident = "abk_" + key.TokenHash[:8]
	}
	return APIKeyResponse{
		ID:               key.ID,
		UserID:           key.UserID,
		Name:             key.Name,
		Description:      key.Description,
		Scopes:           key.Scopes,
		Status:           key.Status,
		CreatedAt:        key.CreatedAt,
		LastUsedAt:       key.LastUsedAt,
		LastUsedIP:       key.LastUsedIP,
		UseCount:         key.UseCount,
		ExpiresAt:        key.ExpiresAt,
		DeactivatedAt:    key.DeactivatedAt,
		RevokedAt:        key.RevokedAt,
		Identifier:       ident,
		DaysSinceLastUse: daysSince,
		NeverUsed:        neverUsed,
		Username:         username,
	}
}

// isAdminUser reports whether the current request has the users.manage permission.
func isAdminUser(c *gin.Context) bool {
	return auth.Can(c.Request.Context(), auth.PermUsersManage)
}

// Create handles POST /auth/api-keys.
func (h *APIKeyHandler) Create(c *gin.Context) {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return
	}
	var req CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	asked, err := requestedExpiry(req, h.now())
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	targetUserID := caller.ID
	if req.UserID != "" && req.UserID != caller.ID {
		// Creating a key for another user is minting a credential for them:
		// an API key is refused at the route (server.credRouteWhen).
		if !isAdminUser(c) {
			httputil.RespondWithForbidden(c, "only admins can create keys for other users")
			return
		}
		targetUserID = req.UserID
	}
	// Every requested scope must be in the caller's effective set. A nil set
	// is the empty set for every caller (05-authn §11.1 item 2): a lookup in a
	// nil map finds nothing, so a caller with no permissions grants no scope.
	// This used to skip the check when the set was nil, so a key whose
	// effective set was empty could hand out any scope.
	callerPermSet := auth.PermissionsFromContext(c.Request.Context())
	for _, scope := range req.Scopes {
		if !auth.IsKnown(scope) {
			httputil.RespondWithBadRequest(c, "unknown scope: "+scope)
			return
		}
		if _, has := callerPermSet[auth.Permission(scope)]; !has {
			httputil.RespondWithForbidden(c, "cannot grant scope you don't have: "+scope)
			return
		}
	}
	rawToken, hash, err := database.GenerateAPIKeyToken()
	if err != nil {
		httputil.InternalError(c, "failed to generate token", err)
		return
	}
	key := &database.APIKey{
		UserID:      targetUserID,
		Name:        req.Name,
		Description: req.Description,
		TokenHash:   hash,
		Scopes:      req.Scopes,
		Status:      "active",
	}
	if len(key.Scopes) == 0 {
		key.Scopes = []string{}
	}
	exp, clamped, ok := clampToCallingKey(c, asked)
	if !ok {
		httputil.RespondWithForbidden(c, noClampMessage)
		return
	}
	// An empty scope list means "all of the owner's role permissions"
	// (middleware intersectPermissions), which a key may never hand out: a
	// key caller must name the scopes of the key it creates, and the loop
	// above has held each one to the key's own effective set (05-authn §11.1
	// item 2). A session caller keeps the empty list. Checked after the clamp
	// so a key with nothing to clamp to is still refused for that reason.
	if len(req.Scopes) == 0 && auth.MethodFromContext(c.Request.Context()) == auth.MethodAPIKey {
		httputil.RespondWithForbidden(c, "an API key must name the scopes of the key it creates")
		return
	}
	key.ExpiresAt = &exp
	created, err := h.store.CreateAPIKey(key)
	if err != nil {
		httputil.InternalError(c, "failed to create api key", err)
		return
	}
	slog.Info("apikey created", "id", created.ID, "user", logger.SanitizeLogValue(targetUserID), "name", logger.SanitizeLogValue(created.Name), "scopes", logger.SanitizeLogValue(fmt.Sprint(created.Scopes)), "expires", created.ExpiresAt, "clamped_to_calling_key", clamped)
	resp := CreateAPIKeyResponse{
		ID:        created.ID,
		Name:      created.Name,
		Token:     rawToken,
		Scopes:    created.Scopes,
		ExpiresAt: created.ExpiresAt,
		CreatedAt: created.CreatedAt,
	}
	if clamped {
		resp.Note = clampedNote
	}
	httputil.RespondWithCreated(c, resp)
}

// List handles GET /auth/api-keys.
func (h *APIKeyHandler) List(c *gin.Context) {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return
	}
	showAll := c.Query("all") == "true" && isAdminUser(c)
	var keys []database.APIKey
	var err error
	if showAll {
		keys, err = h.store.ListAllAPIKeys()
	} else {
		keys, err = h.store.ListAPIKeysForUser(caller.ID)
	}
	// One unreadable key record must not turn the whole listing into a 500:
	// serve the keys that read and report how many did not. Any other error
	// (a failed index scan) is a hard failure.
	unreadable, partial := database.UnreadableMemberCount(err)
	if err != nil && !partial {
		httputil.InternalError(c, "failed to list api keys", err)
		return
	}
	if partial {
		slog.Warn("apikey list: some keys could not be read", "caller", caller.ID, "all", showAll, "unreadable", unreadable, "err", err)
	}
	if keys == nil {
		keys = []database.APIKey{}
	}
	userCache := map[string]string{}
	results := make([]APIKeyResponse, 0, len(keys))
	for _, k := range keys {
		username := ""
		if showAll {
			if u, cached := userCache[k.UserID]; cached {
				username = u
			} else {
				if user, uerr := h.store.GetUserByID(k.UserID); uerr == nil && user != nil {
					username = user.Username
				}
				userCache[k.UserID] = username
			}
		}
		results = append(results, buildAPIKeyResponse(k, username))
	}
	httputil.RespondWithOK(c, gin.H{"api_keys": results, "count": len(results), "unreadable": unreadable})
}

// Get handles GET /auth/api-keys/:id.
func (h *APIKeyHandler) Get(c *gin.Context) {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return
	}
	id := c.Param("id")
	key, err := h.store.GetAPIKey(id)
	if err != nil {
		httputil.InternalError(c, "failed to get api key", err)
		return
	}
	if key == nil {
		httputil.RespondWithNotFound(c, "api key", id)
		return
	}
	if key.UserID != caller.ID && !isAdminUser(c) {
		httputil.RespondWithForbidden(c, "access denied")
		return
	}
	username := ""
	if isAdminUser(c) {
		if user, uerr := h.store.GetUserByID(key.UserID); uerr == nil && user != nil {
			username = user.Username
		}
	}
	httputil.RespondWithOK(c, buildAPIKeyResponse(*key, username))
}

// UpdateStatus handles PATCH /auth/api-keys/:id.
func (h *APIKeyHandler) UpdateStatus(c *gin.Context) {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return
	}
	id := c.Param("id")
	key, err := h.store.GetAPIKey(id)
	if err != nil {
		httputil.InternalError(c, "failed to get api key", err)
		return
	}
	if key == nil {
		httputil.RespondWithNotFound(c, "api key", id)
		return
	}
	if key.UserID != caller.ID && !isAdminUser(c) {
		httputil.RespondWithForbidden(c, "access denied")
		return
	}
	// Revocation is terminal (05-authn §11.1 item 3): no status change on a
	// revoked key, whichever field records the revocation.
	if key.Status == "revoked" || key.RevokedAt != nil {
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
	if req.Status == "revoked" {
		httputil.RespondWithBadRequest(c, "use DELETE to revoke a key")
		return
	}
	if req.Status != "active" && req.Status != "inactive" {
		httputil.RespondWithBadRequest(c, "status must be 'active' or 'inactive'")
		return
	}
	if err := h.store.SetAPIKeyStatus(id, req.Status, time.Now()); err != nil {
		// A revoke that landed after the read above: the store refuses the
		// re-activation, and the answer is the same 409.
		if errors.Is(err, database.ErrAPIKeyRevoked) {
			httputil.RespondWithConflict(c, "api key is revoked")
			return
		}
		httputil.InternalError(c, "failed to update api key status", err)
		return
	}
	slog.Info("apikey status changed", "id", logger.SanitizeLogValue(id), "caller", caller.ID, "status", logger.SanitizeLogValue(req.Status))
	updated, err := h.store.GetAPIKey(id)
	if err != nil || updated == nil {
		httputil.RespondWithOK(c, gin.H{"status": req.Status})
		return
	}
	httputil.RespondWithOK(c, buildAPIKeyResponse(*updated, ""))
}

// Revoke handles DELETE /auth/api-keys/:id.
func (h *APIKeyHandler) Revoke(c *gin.Context) {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return
	}
	id := c.Param("id")
	key, err := h.store.GetAPIKey(id)
	if err != nil {
		httputil.InternalError(c, "failed to get api key", err)
		return
	}
	if key == nil {
		httputil.RespondWithNotFound(c, "api key", id)
		return
	}
	if key.UserID != caller.ID && !isAdminUser(c) {
		httputil.RespondWithForbidden(c, "access denied")
		return
	}
	if err := h.store.RevokeAPIKey(id); err != nil {
		httputil.InternalError(c, "failed to revoke api key", err)
		return
	}
	slog.Info("apikey revoked", "id", logger.SanitizeLogValue(id), "caller", caller.ID, "name", key.Name)
	httputil.RespondWithNoContent(c)
}
