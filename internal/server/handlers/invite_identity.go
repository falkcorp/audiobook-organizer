// file: internal/server/handlers/invite_identity.go
// version: 0.1.0
// guid: 8c4e1a97-2b63-4d50-9f18-e7a3c5d0b629
// last-edited: 2026-11-22

// The invite and admin set-email routes under core identity (F4-08 B10t).
//
// Under core identity the account record, including every invite and every
// address, is core's, so the books routes that wrote books' store become
// wrappers over core's own calls (Identity.Admin, core's auth.AccountAdmin), made with the
// REQUEST's principal so that every core rule runs for the caller. What stays
// in books is what core does not do: the e-mail itself (HEAD's delivery code,
// mailInvite) and a delivery record (database.InviteMailRecord) that holds only
// what core does not, the sent-at and the recipient, for the Users page and for
// resend. That record is never a gate: whether accepting an invite binds its
// address is core's BindEmailOnAccept, set by MarkInviteEmailDelivered only
// after the mailer reported success.
//
// With no Admin on the Identity every route answers 503 and none falls through
// to HEAD's store path, which under identity is no longer the record.

package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/gin-gonic/gin"
)

// inviteLinkTokenMarker is where core's invite link carries the token:
// "/invite#token=<token>" (core/auth/invites.go inviteLink). The fragment is
// never sent to a server, so the link does not reach any access log.
const inviteLinkTokenMarker = "#token="

// identityAccounts returns core's actor-checked admin handle for a route that
// runs under core identity. ok is false after it answered 503 for a server whose
// Identity carries none.
func identityAccounts(c *gin.Context, id *legacyauth.Identity) (coreauth.Admin, bool) {
	if id.Admin == nil {
		httputil.RespondWithServiceUnavailable(c, "this server's identity wiring has no account administration handle; invitations and e-mail changes are unavailable")
		return nil, false
	}
	return id.Admin, true
}

// requestPrincipal is the request's own core principal, never built here. A
// request without one (or one that names no user) is 401.
func requestPrincipal(c *gin.Context) (*coreauth.Principal, bool) {
	p, ok := coreauth.PrincipalFrom(c.Request.Context())
	if !ok || p == nil || p.User == nil {
		legacyauth.RespondNoUser(c, "authentication required")
		return nil, false
	}
	return p, true
}

// respondAccountError maps a refusal of core's account calls to HEAD's
// statuses: a bad address 400, an authority refusal 403, no such user or
// invite 404, a username or address held elsewhere 409. A refusal the caller
// recognises by its own wording (an address another account holds, at create and
// at set-email) is answered before this is called.
func respondAccountError(c *gin.Context, what string, err error) {
	switch {
	case errors.Is(err, coreauth.ErrNotFound), errors.Is(err, coreauth.ErrInviteInvalid):
		httputil.RespondWithNotFound(c, what, "")
	case errors.Is(err, coreauth.ErrUserExists), errors.Is(err, coreauth.ErrEmailInUse):
		httputil.RespondWithConflict(c, "that username or email address is already taken by another account")
	case errors.Is(err, coreauth.ErrBadInput), errors.Is(err, coreauth.ErrEmailNotPlain):
		httputil.RespondWithBadRequest(c, err.Error())
	case errors.Is(err, coreauth.ErrDenied), errors.Is(err, coreauth.ErrScopeEscalation),
		errors.Is(err, coreauth.ErrDominance), errors.Is(err, coreauth.ErrLastAdmin):
		httputil.RespondWithForbidden(c, "you may not do that")
	case errors.Is(err, coreauth.ErrContended), errors.Is(err, coreauth.ErrBusy):
		httputil.RespondWithServiceUnavailable(c, "the server is busy; try again")
	default:
		httputil.InternalError(c, what, err)
	}
}

// inviteLinkParts splits core's fragment link into the token (the bearer secret
// of the invite: shown once to the creator and mailed, never stored) and the
// absolute link built on base.
func inviteLinkParts(base, link string) (token, absolute string) {
	_, token, _ = strings.Cut(link, inviteLinkTokenMarker)
	return token, strings.TrimRight(base, "/") + link
}

// inviteRecordView is the admin's view of an open core invite. Core never
// returns a token for an existing invite, so Token is only ever set by the
// create and resend responses, from the in-process return.
func inviteRecordView(r coreauth.InviteView, mail *database.InviteMailRecord) *pendingInviteView {
	v := &pendingInviteView{
		ID: r.ID, Username: r.Username, Email: r.Email, RoleID: string(r.Role),
		CreatedByUserID: string(r.CreatedBy), CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
	}
	if mail != nil && !mail.SentAt.IsZero() {
		at := mail.SentAt
		v.EmailSentAt, v.EmailSentTo = &at, mail.To
	}
	return v
}

// inviteForEmail is the invite value the shared mail code renders from. It is
// built for the render only and never stored: it carries no token.
func inviteForEmail(r coreauth.InviteView) *database.Invite {
	return &database.Invite{Username: r.Username, Email: r.Email, RoleID: string(r.Role), ExpiresAt: r.ExpiresAt}
}

// willBind reports whether accepting the invite will bind its address verified,
// for the two promises the email makes. It is core's own condition seen from
// here: the invite is (or is about to be) flagged and its marker may set the
// flag, which needs core:admin. It is wording only; core decides at accept.
func willBind(ctx context.Context, flagged bool) bool {
	return flagged && !legacyauth.AdminRefused(ctx)
}

// inviteLimitKey is the key of the per-invite resend bucket under core identity.
// A resend rotates the invite, so its ID changes; the invitee's username does
// not, and core allows one open invite per username, so the bucket survives
// every rotation of the same invitation.
func inviteLimitKey(username string) string { return "user:" + coreauth.FoldUsername(username) }

// putMailRecord, getMailRecord and dropMailRecord keep the delivery record. All
// three are best effort and no-ops when the store lacks the capability: the
// record is UI and resend data, never a gate.
func (h *UserHandler) putMailRecord(rec database.InviteMailRecord) {
	if h.mailStore == nil {
		return
	}
	if err := h.mailStore.PutInviteMail(rec); err != nil {
		inviteLog.Warn("invite email: could not record the delivery of invite %s: %v", rec.InviteID, err)
	}
}

func (h *UserHandler) getMailRecord(id string) *database.InviteMailRecord {
	if h.mailStore == nil {
		return nil
	}
	rec, err := h.mailStore.GetInviteMail(id)
	if err != nil {
		inviteLog.Warn("invite email: could not read the delivery record of invite %s: %v", id, err)
		return nil
	}
	return rec
}

func (h *UserHandler) dropMailRecord(id string) {
	if h.mailStore == nil {
		return
	}
	if err := h.mailStore.DeleteInviteMail(id); err != nil {
		inviteLog.Warn("invite email: could not drop the delivery record of invite %s: %v", id, err)
	}
}

// deliverIdentityInvite mails an invite under core identity and, only once the
// mailer reported success, records the delivery and (when mark) asks core to
// flag the invite so that accepting it binds the address. A send failure sets
// nothing. markErr is non-empty when the mail went out but the invite was not
// flagged: core refused the flag, or the sender does not hold core:admin and so
// cannot vouch for the address (core's MarkInviteEmailDelivered is admin-only).
// The admin is told, and the address will not be bound on accept. mark false
// makes no call to core. warnUnbound makes a sender who cannot flag an invite
// that WAS flagged hear it (a resend); a create by such a sender is silent, as
// HEAD's creator_not_admin invite was.
func (h *UserHandler) deliverIdentityInvite(ctx context.Context, acc coreauth.InviteAdmin, rec coreauth.InviteView, absLink string, mark, warnUnbound bool) (sentAt *time.Time, errMsg, markErr string) {
	bind := willBind(ctx, mark)
	to, errMsg := h.mailInvite(ctx, inviteForEmail(rec), func() (string, bool, bool) {
		return absLink,
			h.delivery.policySync() && rec.Email != "" && bind,
			h.delivery.serviceTokens() && rec.Email != "" && bind
	})
	if errMsg != "" {
		return nil, errMsg, ""
	}
	now := h.delivery.now()
	h.putMailRecord(database.InviteMailRecord{InviteID: rec.ID, To: to, SentAt: now, ExpiresAt: rec.ExpiresAt})
	inviteLog.Info("invite email: sent invitation for %s", logger.SanitizeLogValue(rec.Username))
	switch {
	case bind:
		if err := acc.MarkInviteEmailDelivered(ctx, rec.ID); err != nil {
			inviteLog.Warn("invite email: sent, but core would not flag the invite of %s to bind its address: %v", logger.SanitizeLogValue(rec.Username), err)
			markErr = notMarkedEmailedMessage
		}
	case mark && warnUnbound:
		markErr = notMarkedEmailedMessage
	}
	return &now, "", markErr
}

// notMarkedEmailedMessage is what an admin is told when the invitation went out
// but the invite was not flagged as e-mailed, so accepting it will not mark the
// address verified. It is not an error: the mail was sent.
const notMarkedEmailedMessage = "the invitation was sent, but it was not marked as e-mailed, so accepting it will not mark the address verified"

// createInviteUnderIdentity is CreateInvite under core identity (B1). Core's
// CreateInvite runs every rule with the request's principal. The link comes
// from its in-process return and is never persisted; the mail goes out through
// HEAD's delivery code, and only after it reports success is the invite
// flagged. A failed or unconfigured send never fails the request, as at HEAD.
func (h *UserHandler) createInviteUnderIdentity(c *gin.Context, id *legacyauth.Identity) {
	acc, ok := identityAccounts(c, id)
	if !ok {
		return
	}
	var req struct {
		Username  string `json:"username" binding:"required"`
		RoleID    string `json:"role_id" binding:"required"`
		ExpiresIn int    `json:"expires_in"` // hours; 0 = core's default
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
	actor, ok := requestPrincipal(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	spec := coreauth.InviteSpec{Username: strings.TrimSpace(req.Username), Email: email, Role: rbac.RoleName(req.RoleID)}
	if req.ExpiresIn > 0 {
		spec.TTL = time.Duration(req.ExpiresIn) * time.Hour
	}
	link, rec, err := acc.CreateInvite(ctx, actor, spec)
	if err != nil {
		if errors.Is(err, coreauth.ErrEmailInUse) {
			// HEAD's answer to an address another account holds verified.
			httputil.RespondWithConflict(c, database.ErrInviteEmailTaken.Error())
			return
		}
		respondAccountError(c, "failed to create invite", err)
		return
	}
	token, absLink := inviteLinkParts(h.delivery.baseURL(), link)
	view := inviteRecordView(rec, nil)
	view.Token = token
	resp := gin.H{"invite": view, "token": token, "invite_url": absLink, "email_sent": false}
	if req.SendEmail {
		if blocked := h.delivery.sendBlocked(); blocked != "" {
			resp["email_error"] = blocked
		} else if why := h.sendLimiter.allow(inviteLimitKey(rec.Username), string(actor.User.ID)); why != "" {
			resp["email_error"] = why
		} else if sentAt, errMsg, markErr := h.deliverIdentityInvite(ctx, acc, rec, absLink, true, false); errMsg != "" {
			resp["email_error"] = errMsg
		} else {
			resp["email_sent"] = true
			view.EmailSentAt, view.EmailSentTo = sentAt, rec.Email
			if markErr != "" {
				resp["email_binding_error"] = markErr
			}
		}
	}
	httputil.RespondWithCreated(c, resp)
}

// resendInviteUnderIdentity is ResendInvite under core identity (B2): it
// ROTATES the invite. Core's RotateInvite replaces the open invite with one for
// the same username, address and role and exactly the same expiry, in one batch,
// so a failed rotation leaves the old link alive and the lifetime is never
// extended. The new link is mailed and the old one dies.
//
// Everything books decides runs BEFORE the rotation, so a refused resend changes
// nothing: the invite exists and is not expired (404), has an address (400),
// mail is configured (503), and the limits allow it (429). The per-invite limit
// keys on the folded username (inviteLimitKey), which a rotation keeps, so
// rotating cannot reset it.
//
// The rotated invite is born unflagged. Only when the OLD invite was flagged
// (InviteRotation.WasEmailBound) is it flagged again, and only after the mailer
// succeeded: a resend never makes an address verify where the original invite
// would not have. A sender who does not hold core:admin cannot flag it, so the
// resend is "sent, but not marked as e-mailed" (email_binding_error), not an
// error. That is stricter than HEAD, whose emailed state belonged to the
// invite's creator and survived a resend by anyone.
func (h *UserHandler) resendInviteUnderIdentity(c *gin.Context, id *legacyauth.Identity) {
	acc, ok := identityAccounts(c, id)
	if !ok {
		return
	}
	actor, ok := requestPrincipal(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	oldID := c.Param("id")
	old, err := acc.Invite(ctx, oldID)
	if err != nil {
		respondAccountError(c, "invite", err)
		return
	}
	if old.Email == "" {
		httputil.RespondWithBadRequest(c, "this invitation has no email address; delete it and create a new one with an email")
		return
	}
	switch h.delivery.sendBlocked() {
	case "":
	case mailNotConfiguredReason:
		httputil.RespondWithError(c, http.StatusServiceUnavailable, "email is not configured on this server", "MAIL_NOT_CONFIGURED")
		return
	default:
		httputil.RespondWithError(c, http.StatusServiceUnavailable, publicURLMissingReason, "PUBLIC_URL_NOT_CONFIGURED")
		return
	}
	if why := h.sendLimiter.allow(inviteLimitKey(old.Username), string(actor.User.ID)); why != "" {
		httputil.RespondWithError(c, http.StatusTooManyRequests, why, "TOO_MANY_REQUESTS")
		return
	}
	link, rot, err := acc.RotateInvite(ctx, oldID)
	if err != nil {
		respondAccountError(c, "invite", err)
		return
	}
	rec := rot.Invite
	// The last send moves to the new invite before anything is mailed, so a
	// failed send leaves the Users page as it was.
	prev := h.getMailRecord(oldID)
	next := database.InviteMailRecord{InviteID: rec.ID, ExpiresAt: rec.ExpiresAt}
	if prev != nil {
		next.To, next.SentAt = prev.To, prev.SentAt
	}
	h.putMailRecord(next)
	h.dropMailRecord(oldID)

	token, absLink := inviteLinkParts(h.delivery.baseURL(), link)
	view := inviteRecordView(rec, &next)
	view.Token = token
	rotated := gin.H{"invite": view, "token": token, "invite_url": absLink}
	sentAt, errMsg, markErr := h.deliverIdentityInvite(ctx, acc, rec, absLink, rot.WasEmailBound, true)
	if errMsg != "" {
		httputil.RespondWithErrorFields(c, http.StatusBadGateway, errMsg, "MAIL_SEND_FAILED", rotated)
		return
	}
	resp := gin.H{"email_sent": true, "email_sent_at": sentAt}
	for k, v := range rotated {
		resp[k] = v
	}
	if markErr != "" {
		resp["email_binding_error"] = markErr
	}
	httputil.RespondWithOK(c, resp)
}

// listInvitesUnderIdentity is ListInvites under core identity: core's open
// invites through the actor-checked read (InviteAdmin.AdminInvites, HEAD's
// reach for this route: users.manage through any method, or auth off), each with the delivery record's
// sent-at and recipient and never a token (core keeps none). The accepted
// members are books' own accept records (acceptedInvitesUnderIdentity),
// read only after core's gate has let the caller through: AdminInvites checks
// inside its sequence, so a refused caller has been answered before the family
// is touched.
func (h *UserHandler) listInvitesUnderIdentity(c *gin.Context, id *legacyauth.Identity) {
	acc, ok := identityAccounts(c, id)
	if !ok {
		return
	}
	views := make([]*pendingInviteView, 0)
	for r, err := range acc.AdminInvites(c.Request.Context()) {
		if err != nil {
			respondAccountError(c, "failed to list invites", err)
			return
		}
		views = append(views, inviteRecordView(r, h.getMailRecord(r.ID)))
	}
	h.pruneMailRecords()
	httputil.RespondWithOK(c, gin.H{
		"invites": views, "count": len(views),
		"accepted":                     h.acceptedInvitesUnderIdentity(),
		"mail_configured":              h.delivery.sendBlocked() == "",
		"mail_unavailable_reason":      h.delivery.sendBlocked(),
		"guest_policy_sync_configured": h.delivery.policySync(),
	})
}

// acceptedInvitesUnderIdentity is acceptedInvites under core identity, the same
// view over the same 30 days, newest first. The invite is core's and is gone once
// accepted, so the rows are books' own accept records (database.InviteAccept,
// written from core's InviteAccepted event, and for an accept made before the flip
// by M3 from HEAD's used invite), each joined to the guest-policy sync's outcome:
//
//   - username, email, accepted_user_id, email_binding, sso_grant and
//     displaced_user_ids are the accept row's, which is what HEAD's invite held;
//   - accepted_at is the accept row's time: when the accept happened (core's time
//     for it, or HEAD's used_at), never the time the sync reported;
//   - guest_policy_status, guest_policy_detail and guest_policy_at are the sync's
//     outcome for the invite id (database.GuestPolicyStore) when there is one. A
//     row with no outcome (the sync never ran, as for an invite with no address, or
//     has not finished) shows the guest-policy members the row itself holds, which
//     are HEAD's own for a carried accept and empty otherwise.
//
// A row is window-filtered on the accept time, so an accept whose sync reported
// after the window edge is judged by when it was accepted. It reads and never
// writes. Like HEAD's, it answers [] when there is nothing to show or the family
// cannot be read (logged), so the pending half of the body still reaches the admin;
// an outcome that cannot be read costs that row only its sync members.
func (h *UserHandler) acceptedInvitesUnderIdentity() []acceptedInviteView {
	out := []acceptedInviteView{}
	if h.accepts == nil {
		return out
	}
	list, err := h.accepts.ListInviteAcceptsSince(h.delivery.now().Add(-acceptedInviteWindow))
	if err != nil {
		inviteLog.Warn("list accepted invites: %v", err)
		return out
	}
	for _, a := range list {
		at := a.AcceptedAt
		v := acceptedInviteView{
			Username: a.Username, Email: a.Email, AcceptedAt: &at, AcceptedUserID: a.UserID,
			EmailBinding: a.EmailBinding, SSOGrant: a.SSOGrant,
			GuestPolicyStatus: a.GuestPolicyStatus, GuestPolicyDetail: a.GuestPolicyDetail, GuestPolicyAt: a.GuestPolicyAt,
			DisplacedUserIDs: a.DisplacedUserIDs,
		}
		if h.outcomes != nil {
			o, err := h.outcomes.GetGuestPolicyOutcome(a.InviteID)
			if err != nil {
				inviteLog.Warn("list accepted invites: reading the guest-policy outcome of invite %s failed: %v", logger.SanitizeLogValue(a.InviteID), err)
			} else if o != nil {
				oat := o.At
				v.GuestPolicyStatus, v.GuestPolicyDetail, v.GuestPolicyAt = o.Status, o.Detail, &oat
			}
		}
		out = append(out, v)
	}
	return out
}

// mailRecordGrace is how long past its invite's expiry a delivery record is
// kept. A record is only UI and resend data; the sweep exists so that the
// recipient addresses of invites that can no longer be resent do not stay.
const mailRecordGrace = 24 * time.Hour

// pruneMailRecords drops the delivery records of invites that expired more than
// mailRecordGrace ago. It keys on the record's own expiry, never on "not in the
// open list", so it cannot drop the record of an invite created after the list
// was read.
func (h *UserHandler) pruneMailRecords() {
	if h.mailStore == nil {
		return
	}
	recs, err := h.mailStore.ListInviteMail()
	if err != nil {
		inviteLog.Warn("invite email: could not list delivery records to sweep: %v", err)
		return
	}
	cutoff := h.delivery.now().Add(-mailRecordGrace)
	for _, r := range recs {
		if r.ExpiresAt.Before(cutoff) {
			h.dropMailRecord(r.InviteID)
		}
	}
}

// deleteInviteUnderIdentity is DeleteInvite under core identity: core deletes
// the invite for the request's principal under HEAD's reach for this route
// (InviteAdmin.AdminDeleteInvite: users.manage through any method, or auth
// off), and the delivery record goes with it. It asks for no user: HEAD's
// route needed none, and a 401 here would end the SPA's session (F4-08 B10y).
func (h *UserHandler) deleteInviteUnderIdentity(c *gin.Context, id *legacyauth.Identity) {
	acc, ok := identityAccounts(c, id)
	if !ok {
		return
	}
	inviteID := c.Param("id")
	if err := acc.AdminDeleteInvite(c.Request.Context(), inviteID); err != nil {
		respondAccountError(c, "invite", err)
		return
	}
	h.dropMailRecord(inviteID)
	httputil.RespondWithOK(c, gin.H{"deleted": inviteID})
}

// setUserEmailUnderIdentity is SetUserEmail's store write under core identity
// (B4): core's SetUserEmail binds the address verified (source admin) with the
// request's principal, which re-checks users.manage, core:admin, an interactive
// sign-in and dominance over the target. SetUserEmail's own admin gates have
// already run. Revoking what the previous address was granted is the account
// event's job under identity (core's EmailChanged hook), so books' own revoker
// is not called here.
func (h *UserHandler) setUserEmailUnderIdentity(c *gin.Context, id *legacyauth.Identity, target, email string) {
	acc, ok := identityAccounts(c, id)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := acc.SetUserEmail(ctx, coreauth.UserID(target), email); err != nil {
		if errors.Is(err, coreauth.ErrUserExists) || errors.Is(err, coreauth.ErrEmailInUse) {
			httputil.RespondWithConflict(c, "another account already has that email address verified; clear it there or merge the two accounts first")
			return
		}
		respondAccountError(c, "user", err)
		return
	}
	u, err := acc.AdminUser(ctx, coreauth.UserID(target))
	if err != nil || u == nil {
		httputil.InternalError(c, "get user", err)
		return
	}
	// Safe shape only: never a raw user record.
	roles := make([]string, len(u.Roles))
	for i, r := range u.Roles {
		roles[i] = string(r)
	}
	httputil.RespondWithOK(c, gin.H{"user": buildAuthUserResponse(&database.User{
		ID: string(u.ID), Username: u.Username, Email: u.Email, Roles: roles,
		Status: string(u.Status), CreatedAt: u.CreatedAt,
	})})
}
