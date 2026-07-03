// file: internal/server/handlers/apikeys.go
// version: 2.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-07-03

package handlers

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// apiKeyRotationGraceWindow is how long the OLD key keeps working after a
// rotation before the pre-existing middleware expiry check retires it. This
// gives in-flight clients/scripts holding the old key a chance to pick up
// the new one instead of failing instantly (SEC-1/PROC-6).
const apiKeyRotationGraceWindow = 1 * time.Hour

// ---- Request / response types -----------------------------------------------

// CreateAPIKeyRequest is the JSON body for POST /api/v1/auth/api-keys.
type CreateAPIKeyRequest struct {
	Name          string   `json:"name" binding:"required"`
	Description   string   `json:"description"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays int      `json:"expires_in_days"`
	UserID        string   `json:"user_id"`
}

// CreateAPIKeyResponse is returned after successfully creating an API key.
type CreateAPIKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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
	SetAPIKeyExpiry(id string, at time.Time) error
}

// ---- Handler -----------------------------------------------------------------

// APIKeyHandler handles all /auth/api-keys routes.
type APIKeyHandler struct {
	store APIKeyHandlerStore
}

// NewAPIKeyHandler constructs an APIKeyHandler.
func NewAPIKeyHandler(store APIKeyHandlerStore) *APIKeyHandler {
	return &APIKeyHandler{store: store}
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
	targetUserID := caller.ID
	if req.UserID != "" && req.UserID != caller.ID {
		if !isAdminUser(c) {
			httputil.RespondWithForbidden(c, "only admins can create keys for other users")
			return
		}
		targetUserID = req.UserID
	}
	callerPermSet := auth.PermissionsFromContext(c.Request.Context())
	for _, scope := range req.Scopes {
		if !auth.IsKnown(scope) {
			httputil.RespondWithBadRequest(c, "unknown scope: "+scope)
			return
		}
		if callerPermSet != nil {
			if _, has := callerPermSet[auth.Permission(scope)]; !has {
				httputil.RespondWithForbidden(c, "cannot grant scope you don't have: "+scope)
				return
			}
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
	if req.ExpiresInDays > 0 {
		exp := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		key.ExpiresAt = &exp
	}
	created, err := h.store.CreateAPIKey(key)
	if err != nil {
		httputil.InternalError(c, "failed to create api key", err)
		return
	}
	slog.Info("apikey created", "id", created.ID, "user", targetUserID, "name", created.Name, "scopes", created.Scopes, "expires", created.ExpiresAt)
	httputil.RespondWithCreated(c, CreateAPIKeyResponse{
		ID:        created.ID,
		Name:      created.Name,
		Token:     rawToken,
		Scopes:    created.Scopes,
		ExpiresAt: created.ExpiresAt,
		CreatedAt: created.CreatedAt,
	})
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
	if err != nil {
		httputil.InternalError(c, "failed to list api keys", err)
		return
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
	httputil.RespondWithOK(c, gin.H{"api_keys": results, "count": len(results)})
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
		httputil.InternalError(c, "failed to update api key status", err)
		return
	}
	slog.Info("apikey status changed", "id", id, "caller", caller.ID, "status", req.Status)
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
	slog.Info("apikey revoked", "id", id, "caller", caller.ID, "name", key.Name)
	httputil.RespondWithNoContent(c)
}

// Rotate handles POST /auth/api-keys/:id/rotate. It issues a fresh key that
// inherits the old key's scopes, and instead of revoking the old key
// immediately it puts it on a short grace-window expiry via
// SetAPIKeyExpiry — the pre-existing middleware expiry check
// (key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt)) retires it
// naturally once the grace window elapses. This avoids breaking in-flight
// clients that haven't picked up the new key yet (SEC-1/PROC-6).
func (h *APIKeyHandler) Rotate(c *gin.Context) {
	caller, ok := servermiddleware.CurrentUser(c)
	if !ok || caller == nil {
		httputil.RespondWithUnauthorized(c, "authentication required")
		return
	}
	id := c.Param("id")
	oldKey, err := h.store.GetAPIKey(id)
	if err != nil {
		httputil.InternalError(c, "failed to get api key", err)
		return
	}
	if oldKey == nil {
		httputil.RespondWithNotFound(c, "api key", id)
		return
	}
	if oldKey.UserID != caller.ID && !isAdminUser(c) {
		httputil.RespondWithForbidden(c, "access denied")
		return
	}

	rawToken, hash, err := database.GenerateAPIKeyToken()
	if err != nil {
		httputil.InternalError(c, "failed to generate token", err)
		return
	}

	ttlDays := config.AppConfig.BootstrapKeyTTLDays
	if ttlDays <= 0 {
		ttlDays = 30
	}
	newExpiresAt := time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour)

	newKey := &database.APIKey{
		UserID:      oldKey.UserID,
		Name:        oldKey.Name,
		Description: oldKey.Description,
		TokenHash:   hash,
		Scopes:      oldKey.Scopes,
		Status:      "active",
		ExpiresAt:   &newExpiresAt,
	}
	created, err := h.store.CreateAPIKey(newKey)
	if err != nil {
		httputil.InternalError(c, "failed to create api key", err)
		return
	}

	graceExpiresAt := time.Now().Add(apiKeyRotationGraceWindow)
	if err := h.store.SetAPIKeyExpiry(oldKey.ID, graceExpiresAt); err != nil {
		httputil.InternalError(c, "failed to grace-window old api key", err)
		return
	}

	slog.Info("apikey rotated", "id", created.ID, "user", oldKey.UserID, "old_key_id", oldKey.ID, "grace_window", apiKeyRotationGraceWindow.String())
	httputil.RespondWithCreated(c, CreateAPIKeyResponse{
		ID:        created.ID,
		Name:      created.Name,
		Token:     rawToken,
		Scopes:    created.Scopes,
		ExpiresAt: created.ExpiresAt,
		CreatedAt: created.CreatedAt,
	})
}
