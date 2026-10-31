// file: internal/server/handlers/user.go
// version: 1.15.2
// guid: b2c3d4e5-f6a7-8901-bcde-ef0123456789
// last-edited: 2026-10-30

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
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/gin-gonic/gin"
)

// UserInviteStore is the invite half of UserStore: creating, reading, listing,
// deleting and consuming invitations.
type UserInviteStore interface {
	CreateInvite(invite *database.Invite) (*database.Invite, error)
	GetInvite(token string) (*database.Invite, error)
	ListActiveInvites() ([]database.Invite, error)
	DeleteInvite(token string) error
	ConsumeInvite(token, algo, hash string) (*database.User, error)
}

// UserStore is the narrow persistence interface required by UserHandler. Split
// on 2026-10-30 (the interface-width ratchet, when the web-session cleanup
// added two methods); the name is the composition of the pieces, so no consumer
// moves and the method set is the old one plus WebSessionRevoker's two.
type UserStore interface {
	UserInviteStore
	// WebSessionRevoker tidies a user's web sessions after a deactivation or
	// reactivation (RevokeStaleWebSessions).
	WebSessionRevoker
	ListUsers() ([]database.User, error)
	CreateSession(userID, ip, ua string, ttl time.Duration, credentialEpoch int64) (*database.Session, error)
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
	// delivery supplies the mail sender, link base and policy-sync switch; nil or
	// empty means invitation email is off (SetInviteDelivery).
	delivery *InviteDelivery
	// sendLimiter bounds invitation emails per invite and per admin.
	sendLimiter *inviteSendLimiter
	// revokeGuestAccess ends invite-granted SSO / Cloudflare access on
	// deactivation; nil = nothing to revoke (SetGuestAccessRevoker).
	revokeGuestAccess GuestAccessRevoker
	// revokeGuestToken revokes the user's per-guest Cloudflare service token on
	// deactivation (not on an email change).
	revokeGuestToken GuestTokenRevoker
	// emailSetter sets another user's email as verified (SetUserEmail). nil
	// when the store lacks the capability; SetUserEmail then refuses.
	emailSetter database.AdminEmailSetter
	// acceptChecker runs accept's own checks for the public lookup, so it
	// answers 404 whenever accept would refuse. nil when the store lacks the
	// capability; the lookup then checks only missing, used and expired.
	acceptChecker database.InviteAcceptChecker
	// inviteByID resolves the admin routes' invite ID; nil when the store
	// lacks the capability, and the pending list is searched instead.
	inviteByID database.InviteIDLookup
	// authEnabled mirrors config enable_auth (SetAuthEnabled). With auth off
	// there is no caller to tell apart and the credential-route guard is off
	// too, so invite tokens are listed as before. Defaults to true, so a
	// handler nobody configured hides them.
	authEnabled bool
	// merge, mergeEnqueuer and mergeGrants serve the account-merge routes
	// (SetUserMerge). nil answers 503.
	merge         UserMergeService
	mergeEnqueuer UserMergeEnqueuer
	mergeGrants   *usermerge.Grants
}

// NewUserHandler constructs a UserHandler backed by the given UserStore.
func NewUserHandler(store UserStore) *UserHandler {
	h := &UserHandler{store: store, absSessions: ResolveABSSessionRevoker(store), creds: ResolveUserCredentialWriter(store), sendLimiter: newInviteSendLimiter(), authEnabled: true}
	if ds, ok := database.AsCapability[database.InviteDeliveryStore](store); ok {
		h.deliveryStore = ds
	}
	if es, ok := database.AsCapability[database.AdminEmailSetter](store); ok {
		h.emailSetter = es
	}
	if ac, ok := database.AsCapability[database.InviteAcceptChecker](store); ok {
		h.acceptChecker = ac
	}
	if il, ok := database.AsCapability[database.InviteIDLookup](store); ok {
		h.inviteByID = il
	}
	return h
}

// SetAuthEnabled records config enable_auth (wire time), which decides
// whether invite tokens are shown to every caller (auth off) or only as
// inviteTokenVisible allows.
func (h *UserHandler) SetAuthEnabled(enabled bool) { h.authEnabled = enabled }

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
			// Set once an admin merged the account into another: the Users page
			// then offers no reactivate and shows where the account went.
			"merged_into_user_id": u.MergedIntoUserID,
			"created_at":          u.CreatedAt, "updated_at": u.UpdatedAt,
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
	// The creator sees the token: the route is a credential route (no API
	// key reaches it), and an emailed invite from a creator who is not an
	// admin binds no address and grants nothing (InviteCreatorMayGrant), so
	// accepting it themselves gains them nothing.
	view := newPendingInviteView(created, true)
	resp := gin.H{
		"invite": view, "token": token,
		"invite_url": InviteURL(h.delivery.baseURL(), token),
		"email_sent": false,
	}
	if req.SendEmail {
		if blocked := h.delivery.sendBlocked(); blocked != "" {
			resp["email_error"] = blocked
		} else if why := h.sendLimiter.allow(view.ID, currentUserID(c)); why != "" {
			resp["email_error"] = why
		} else if sentAt, errMsg := h.sendInviteEmail(c.Request.Context(), created); errMsg != "" {
			resp["email_error"] = errMsg
		} else {
			resp["email_sent"] = true
			view.EmailSentAt = sentAt
			view.EmailSentTo = created.Email
		}
	}
	httputil.RespondWithCreated(c, resp)
}

// pendingInviteView is the admin's view of a pending invite. ID is the handle
// the revoke and resend routes take. Token, the bearer secret that accepts
// the invite, is present only when inviteTokenVisible allows it.
type pendingInviteView struct {
	ID              string     `json:"id"`
	Token           string     `json:"token,omitempty"`
	Username        string     `json:"username"`
	Email           string     `json:"email,omitempty"`
	RoleID          string     `json:"role_id"`
	CreatedByUserID string     `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	EmailSentAt     *time.Time `json:"email_sent_at,omitempty"`
	EmailSentTo     string     `json:"email_sent_to,omitempty"`
}

func newPendingInviteView(inv *database.Invite, withToken bool) *pendingInviteView {
	v := &pendingInviteView{
		ID: database.InviteID(inv.Token), Username: inv.Username, Email: inv.Email,
		RoleID: inv.RoleID, CreatedByUserID: inv.CreatedByUserID,
		CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt,
		EmailSentAt: inv.EmailSentAt, EmailSentTo: inv.EmailSentTo,
	}
	if withToken {
		v.Token = inv.Token
	}
	return v
}

// inviteTokenVisible reports whether the caller of the invite list may see
// inv's token. Holding the token of an invite an admin emailed lets anyone
// accept it and receive the address verified, the SSO grant and the
// Cloudflare guest entry, and later capture the real invitee's SSO sign-in,
// so:
//   - an API key, an ABS token or any other method that may not change
//     credentials never sees a token (the same allowlist as the credential
//     routes, so a method nobody classified is refused);
//   - an invite with an email address shows its token only to an active
//     admin: users.manage alone lets a non-admin list invites, and a resend
//     can deliver an invite that was not emailed yet, so "has an email" is
//     the test, not "was sent".
//
// With auth disabled there is no caller to tell apart and the credential
// guard is off as well, so every token is shown, as before.
func (h *UserHandler) inviteTokenVisible(c *gin.Context, inv *database.Invite) bool {
	if !h.authEnabled {
		return true
	}
	if !servermiddleware.CredentialChangeAllowed(c) {
		return false
	}
	if inv.Email == "" {
		return true
	}
	actor, ok := servermiddleware.CurrentUser(c)
	return ok && actor != nil && actor.Status == "active" && slices.Contains(actor.Roles, adminRole)
}

// findInvite resolves an admin route's invite ID to the invite, or nil.
func (h *UserHandler) findInvite(id string) (*database.Invite, error) {
	if h.inviteByID != nil {
		return h.inviteByID.GetInviteByID(id)
	}
	invites, err := h.store.ListActiveInvites()
	if err != nil {
		return nil, err
	}
	for i := range invites {
		if database.InviteID(invites[i].Token) == id {
			return &invites[i], nil
		}
	}
	return nil, nil
}

// ListInvites handles GET /api/v1/users/invites — returns active invites,
// each with its ID and, only where inviteTokenVisible allows, its token.
func (h *UserHandler) ListInvites(c *gin.Context) {
	invites, err := h.store.ListActiveInvites()
	if err != nil {
		httputil.InternalError(c, "failed to list invites", err)
		return
	}
	views := make([]*pendingInviteView, 0, len(invites))
	for i := range invites {
		views = append(views, newPendingInviteView(&invites[i], h.inviteTokenVisible(c, &invites[i])))
	}
	httputil.RespondWithOK(c, gin.H{
		"invites": views, "count": len(views),
		// Accepted invites from the last 30 days with their Cloudflare
		// guest-policy outcome, so a failed sync can be done by hand.
		"accepted": h.acceptedInvites(),
		// mail_configured: an invitation email can go out (mail set up and
		// an absolute link base); mail_unavailable_reason says why not.
		"mail_configured":              h.delivery.sendBlocked() == "",
		"mail_unavailable_reason":      h.delivery.sendBlocked(),
		"guest_policy_sync_configured": h.delivery.policySync(),
	})
}

// DeleteInvite handles DELETE /api/v1/users/invites/:id — revokes an invite
// by its ID (database.InviteID), never its token.
func (h *UserHandler) DeleteInvite(c *gin.Context) {
	id := c.Param("id")
	inv, err := h.findInvite(id)
	if err != nil {
		httputil.InternalError(c, "failed to load invite", err)
		return
	}
	if inv == nil {
		httputil.RespondWithNotFound(c, "invite", id)
		return
	}
	if err := h.store.DeleteInvite(inv.Token); err != nil {
		httputil.InternalError(c, "failed to delete invite", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"deleted": id})
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

	sess, err := h.store.CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent(), 30*24*time.Hour, user.CredentialEpoch)
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
	RevokeStaleWebSessions(h.store, user.ID, user.CredentialEpoch, "user-deactivated")
	if h.revokeGuestAccess != nil {
		h.revokeGuestAccess(user.ID, user.Email, "user deactivated")
	}
	if h.revokeGuestToken != nil {
		h.revokeGuestToken(user.ID, "user deactivated")
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
		httputil.RespondWithConflict(c, "another account already has that email address verified; clear it there or merge the two accounts first")
		return
	case err != nil:
		httputil.InternalError(c, "set user email", err)
		return
	}
	if h.revokeGuestAccess != nil && previous != "" && !util.SameEmail(previous, email) {
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
	// A merged-away account stays locked: reactivating it would put a second
	// live account back on sign-ins and data that now belong to the survivor.
	// Undoing the merge is the way back.
	if user.MergedIntoUserID != "" {
		httputil.RespondWithConflict(c, "this account was merged into another account; undo the merge instead of reactivating it")
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
	RevokeStaleWebSessions(h.store, user.ID, user.CredentialEpoch, "user-reactivated")
	// Safe shape only — never echo the raw *database.User (leaks password hash, CRIT-2).
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(user)})
}
