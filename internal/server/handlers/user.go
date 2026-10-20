// file: internal/server/handlers/user.go
// version: 1.9.0
// guid: b2c3d4e5-f6a7-8901-bcde-ef0123456789
// last-edited: 2026-10-19

// Package handlers contains extracted HTTP handler types for the audiobook
// organizer server. UserHandler covers user management and invite endpoints.

package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// UserStore is the narrow persistence interface required by UserHandler.
type UserStore interface {
	ListUsers() ([]database.User, error)
	CreateInvite(invite *database.Invite) (*database.Invite, error)
	GetInvite(token string) (*database.Invite, error)
	ListActiveInvites() ([]database.Invite, error)
	DeleteInvite(token string) error
	ConsumeInvite(token, algo, hash string) (*database.User, error)
	CreateSession(userID, ip, ua string, ttl time.Duration) (*database.Session, error)
	GetUserByID(id string) (*database.User, error)
}

// UserHandler handles user-management and invite HTTP endpoints.
type UserHandler struct {
	store UserStore
	// absSessions signs a user out of every ABS client on deactivation and
	// reactivation. nil when the store has no ABS keyspace.
	absSessions ABSSessionRevoker
	// creds changes Status (SetUserStatus bumps the credential epoch with it).
	// nil when the store lacks the capability; status changes then refuse.
	creds database.UserCredentialWriter
	// deliveryStore records invitation email sends and lists accepted
	// invites. nil when the store lacks the capability; sends still go out.
	deliveryStore database.InviteDeliveryStore
	// delivery supplies the mail sender, link base and guest headers; nil or
	// empty means invitation email is off (SetInviteDelivery).
	delivery *InviteDelivery
	// sendLimiter bounds invitation emails per invite and per admin.
	sendLimiter *inviteSendLimiter
	// revokeGuestAccess ends invite-granted SSO / Cloudflare access on
	// deactivation; nil = nothing to revoke (SetGuestAccessRevoker).
	revokeGuestAccess GuestAccessRevoker
	// emailSetter sets another user's email as verified (SetUserEmail). nil
	// when the store lacks the capability; SetUserEmail then refuses.
	emailSetter database.AdminEmailSetter
}

// NewUserHandler constructs a UserHandler backed by the given UserStore.
func NewUserHandler(store UserStore) *UserHandler {
	h := &UserHandler{store: store, absSessions: ResolveABSSessionRevoker(store), creds: ResolveUserCredentialWriter(store), sendLimiter: newInviteSendLimiter()}
	if ds, ok := database.AsCapability[database.InviteDeliveryStore](store); ok {
		h.deliveryStore = ds
	}
	if es, ok := database.AsCapability[database.AdminEmailSetter](store); ok {
		h.emailSetter = es
	}
	return h
}

// generateToken produces a cryptographically random 32-byte hex string.
func generateToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ListUsers handles GET /api/v1/users — returns all users (admin only).
func (h *UserHandler) ListUsers(c *gin.Context) {
	users, err := h.store.ListUsers()
	if err != nil {
		httputil.InternalError(c, "failed to list users", err)
		return
	}
	safe := make([]gin.H, 0, len(users))
	for _, u := range users {
		safe = append(safe, gin.H{
			"id": u.ID, "username": u.Username, "email": u.Email,
			"roles": u.Roles, "status": u.Status,
			"created_at": u.CreatedAt, "updated_at": u.UpdatedAt,
		})
	}
	httputil.RespondWithOK(c, gin.H{"users": safe, "count": len(safe)})
}

// CreateInvite handles POST /api/v1/users/invite — generates an invite token.
//
// With an email the invite is addressed to that person: on accept the email
// becomes theirs and is granted SSO sign-in. With send_email the invitation is
// emailed after the invite is stored. A failed or unconfigured send never fails
// the request: the response still carries the token and invite_url, so the
// admin can hand the link over by hand, plus email_sent=false and email_error.
func (h *UserHandler) CreateInvite(c *gin.Context) {
	var req struct {
		Username  string `json:"username" binding:"required"`
		RoleID    string `json:"role_id" binding:"required"`
		ExpiresIn int    `json:"expires_in"` // hours, default 72
		Email     string `json:"email"`
		SendEmail bool   `json:"send_email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	email, err := normalizeInviteEmail(req.Email)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.SendEmail && email == "" {
		httputil.RespondWithBadRequest(c, "send_email needs an email address")
		return
	}

	token := generateToken()
	expiresIn := 72 * time.Hour
	if req.ExpiresIn > 0 {
		expiresIn = time.Duration(req.ExpiresIn) * time.Hour
	}

	invite := &database.Invite{
		Token:     token,
		Username:  strings.TrimSpace(req.Username),
		Email:     email,
		RoleID:    req.RoleID,
		ExpiresAt: time.Now().Add(expiresIn),
	}
	// Record which admin created the invite for the audit trail (pen-test
	// finding MED-4). Empty only if the route somehow ran without an authed user.
	if creator, ok := servermiddleware.CurrentUser(c); ok && creator != nil {
		invite.CreatedByUserID = creator.ID
	}
	created, err := h.store.CreateInvite(invite)
	if err != nil {
		if errors.Is(err, database.ErrInviteEmailTaken) {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		httputil.InternalError(c, "failed to create invite", err)
		return
	}
	resp := gin.H{
		"invite": created, "token": token,
		"invite_url": InviteURL(h.delivery.baseURL(), token),
		"email_sent": false,
	}
	if req.SendEmail {
		if h.delivery.sender() == nil {
			resp["email_error"] = "email is not configured on this server (SMTP_HOST and SMTP_FROM); copy the link instead"
		} else if why := h.sendLimiter.allow(token, currentUserID(c)); why != "" {
			resp["email_error"] = why
		} else if sentAt, errMsg := h.sendInviteEmail(c.Request.Context(), created); errMsg != "" {
			resp["email_error"] = errMsg
		} else {
			resp["email_sent"] = true
			created.EmailSentAt = sentAt
		}
	}
	httputil.RespondWithCreated(c, resp)
}

// ListInvites handles GET /api/v1/users/invites — returns active invites.
func (h *UserHandler) ListInvites(c *gin.Context) {
	invites, err := h.store.ListActiveInvites()
	if err != nil {
		httputil.InternalError(c, "failed to list invites", err)
		return
	}
	if invites == nil {
		invites = []database.Invite{}
	}
	_, headersOK := h.delivery.headers()
	httputil.RespondWithOK(c, gin.H{
		"invites": invites, "count": len(invites),
		// Accepted invites from the last 30 days with their Cloudflare
		// guest-policy outcome, so a failed sync can be done by hand.
		"accepted":                     h.acceptedInvites(),
		"mail_configured":              h.delivery.sender() != nil,
		"guest_headers_configured":     headersOK,
		"guest_policy_sync_configured": h.delivery.policySync(),
	})
}

// DeleteInvite handles DELETE /api/v1/users/invites/:token — revokes an invite.
func (h *UserHandler) DeleteInvite(c *gin.Context) {
	token := c.Param("token")
	if err := h.store.DeleteInvite(token); err != nil {
		httputil.InternalError(c, "failed to delete invite", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"deleted": token})
}

// AcceptInvite handles POST /api/v1/auth/accept-invite — consumes an invite and creates a user session.
func (h *UserHandler) AcceptInvite(c *gin.Context) {
	var req struct {
		Token    string `json:"token" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.Password) < 8 {
		httputil.RespondWithBadRequest(c, "password must be at least 8 characters")
		return
	}

	algo, hash, err := absauth.HashPassword(req.Password)
	if err != nil {
		httputil.InternalError(c, "hash password", err)
		return
	}

	user, err := h.store.ConsumeInvite(req.Token, algo, hash)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	sess, err := h.store.CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent(), 30*24*time.Hour)
	if err != nil {
		httputil.InternalError(c, "create session", err)
		return
	}

	// SetSessionCookie is exported from handlers/auth.go; user.go is in the
	// same package so we can call it directly.
	SetSessionCookie(c, sess.ID, sess.ExpiresAt)
	// Session token is delivered via the HttpOnly cookie only — never in the
	// JSON body (would expose the bearer token to page JS, defeating HttpOnly).
	// Return the safe user shape, not the raw *database.User, which would leak
	// the password hash (pen-test finding CRIT-2).
	httputil.RespondWithCreated(c, gin.H{"user": buildAuthUserResponse(user), "expires_at": sess.ExpiresAt})
}

// DeactivateUser handles POST /api/v1/users/:id/deactivate — soft-deactivates a user.
func (h *UserHandler) DeactivateUser(c *gin.Context) {
	id := c.Param("id")
	user, err := h.store.GetUserByID(id)
	if err != nil {
		httputil.InternalError(c, "get user", err)
		return
	}
	if user == nil {
		httputil.RespondWithNotFound(c, "user", id)
		return
	}

	// Lock and bump CredentialEpoch in ONE write on the fresh row. The lock
	// refuses the account at once; the epoch bump means every ABS session issued
	// before it stays dead after a later reactivation, whether or not the cleanup
	// revoke below lands.
	if h.creds == nil {
		httputil.RespondWithInternalError(c, "status changes are not supported by this store")
		return
	}
	userID := user.ID
	user, err = h.creds.SetUserStatus(userID, "locked")
	if err != nil {
		if errors.Is(err, database.ErrUserNotFound) {
			// Defensive: nothing deletes users today, but the store reports a
			// missing row as ErrUserNotFound, and that is a 404, not a 500.
			httputil.RespondWithNotFound(c, "user", userID)
			return
		}
		httputil.InternalError(c, "deactivate user", err)
		return
	}
	RevokeAllABSSessions(h.absSessions, user.ID, "user-deactivated")
	if h.revokeGuestAccess != nil {
		h.revokeGuestAccess(user.ID, user.Email, "user deactivated")
	}
	// Safe shape only — never echo the raw *database.User (leaks password hash, CRIT-2).
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(user)})
}

// adminRole is the role whose holders may set another user's email as
// verified: the role middleware.RequireAdmin requires, and the one an
// invitation's creator needs for its accept to bind an address.
const adminRole = "admin"

// SetUserEmail handles PUT /api/v1/users/:id/email {"email": "..."}: an admin
// sets or ("") clears another user's email. The address counts as verified
// (EmailSource admin), so SSO sign-ins with it link to this account, which is
// why the route's users.manage is not enough: the acting user must also hold
// the admin role, who can already give any address access directly. An
// address another account has is refused with 409 (the admin can clear it
// there first). Any invite-granted SSO or Cloudflare access the old address
// had is revoked, as on a self-service change.
func (h *UserHandler) SetUserEmail(c *gin.Context) {
	actor, ok := servermiddleware.CurrentUser(c)
	if !ok || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) {
		httputil.RespondWithForbidden(c, "setting another user's email needs the admin role")
		return
	}
	if h.emailSetter == nil {
		httputil.RespondWithInternalError(c, "setting emails is not supported by this store")
		return
	}
	var req struct {
		Email *string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == nil {
		httputil.RespondWithBadRequest(c, `body must be {"email": "<address>"}, or "" to clear it`)
		return
	}
	email, err := normalizeInviteEmail(*req.Email)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	id := c.Param("id")
	previous, err := h.emailSetter.SetUserEmailAsAdmin(id, email)
	switch {
	case errors.Is(err, database.ErrUserNotFound):
		httputil.RespondWithNotFound(c, "user", id)
		return
	case errors.Is(err, database.ErrUserEmailTaken):
		httputil.RespondWithConflict(c, "another account already uses that email address; clear it there first")
		return
	case err != nil:
		httputil.InternalError(c, "set user email", err)
		return
	}
	if h.revokeGuestAccess != nil && previous != "" && !strings.EqualFold(strings.TrimSpace(previous), email) {
		h.revokeGuestAccess(id, previous, "email changed by an admin")
	}
	user, err := h.store.GetUserByID(id)
	if err != nil || user == nil {
		httputil.InternalError(c, "get user", err)
		return
	}
	// Safe shape only — never echo the raw *database.User (leaks password hash, CRIT-2).
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(user)})
}

// ReactivateUser handles POST /api/v1/users/:id/reactivate — reactivates a locked user.
func (h *UserHandler) ReactivateUser(c *gin.Context) {
	id := c.Param("id")
	user, err := h.store.GetUserByID(id)
	if err != nil {
		httputil.InternalError(c, "get user", err)
		return
	}
	if user == nil {
		httputil.RespondWithNotFound(c, "user", id)
		return
	}

	// Unlock and bump CredentialEpoch again in one write: every ABS session
	// issued before this instant (pre-deactivation ones, legacy unstamped ones,
	// any a failed cleanup left behind) is refused by construction. The revoke
	// after it only tidies the device list.
	if h.creds == nil {
		httputil.RespondWithInternalError(c, "status changes are not supported by this store")
		return
	}
	userID := user.ID
	user, err = h.creds.SetUserStatus(userID, "active")
	if err != nil {
		if errors.Is(err, database.ErrUserNotFound) {
			// Defensive: nothing deletes users today, but the store reports a
			// missing row as ErrUserNotFound, and that is a 404, not a 500.
			httputil.RespondWithNotFound(c, "user", userID)
			return
		}
		httputil.InternalError(c, "reactivate user", err)
		return
	}
	RevokeAllABSSessions(h.absSessions, user.ID, "user-reactivated")
	// Safe shape only — never echo the raw *database.User (leaks password hash, CRIT-2).
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(user)})
}
