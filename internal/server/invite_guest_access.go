// file: internal/server/invite_guest_access.go
// version: 1.3.0
// guid: 5b2e9c47-8a13-4d6f-b3e1-0c7a4f8d2e95
// last-edited: 2026-10-19

package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	imail "github.com/falkcorp/audiobook-organizer/internal/mail"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

var inviteAccessLog = logger.New("server.invites")

// Public invite endpoints (GET /auth/invite/:token and POST
// /auth/accept-invite) share one limiter. The token space is 256 bits, so this
// is about load, not guessing. Behind the shared reverse proxy every request
// has the proxy's address (SetTrustedProxies(nil)), so the bucket is in effect
// shared by all callers; real use is one page load and one accept per guest.
const (
	inviteAuthRatePerMinute = 20
	inviteAuthRateBurst     = 10
)

// guestPolicySyncTimeout bounds one accept-time Cloudflare sync (GET + PUT).
const guestPolicySyncTimeout = 30 * time.Second

// inviteGrantAllowed returns the oauth.Config.ExtraAllowed check for SSO
// grants written by accepted invites, or nil when the store cannot answer.
//
// Why not edit OAuthAllowedEmails: that setting is environment-authoritative
// (applyEnvAuthoritativeConfig rewrites it from OAUTH_ALLOWED_EMAILS on every
// boot, so a stored value is discarded), it is an owner trust-root field
// (UpdateService refuses changes from anyone but the owner), and the
// allowlist is parsed once at wiring, so a runtime edit would reach no login
// path until a restart. The grant instead lives in the store, written in the
// same batch as the accept.
//
// A grant exists only when the invitation was emailed to that address and
// its creator was an active admin (ConsumeInvite). It counts only while it
// still describes a live account: the user it names exists, is active, and
// still has exactly this email, so it fails closed even before
// revokeGuestAccess deletes it on deactivation or an email change. Any error
// is a refusal.
func inviteGrantAllowed(store any) func(string) bool {
	grants, ok := database.AsCapability[database.InviteEmailGrantStore](store)
	if !ok {
		return nil
	}
	users, ok := store.(interface {
		GetUserByID(id string) (*database.User, error)
	})
	if !ok {
		return nil
	}
	return func(email string) bool {
		email = strings.ToLower(strings.TrimSpace(email))
		g, err := grants.GetInviteEmailGrant(email)
		if err != nil || g == nil || g.UserID == "" {
			return false
		}
		u, err := users.GetUserByID(g.UserID)
		if err != nil || u == nil {
			return false
		}
		return u.Status == "active" && strings.EqualFold(strings.TrimSpace(u.Email), email)
	}
}

// inviteDelivery builds the handler's delivery dependencies from config. Each
// func reads the config snapshot when called.
func (s *Server) inviteDelivery() *handlers.InviteDelivery {
	return &handlers.InviteDelivery{
		Sender: func() imail.Sender {
			snap := config.Snapshot()
			if !snap.InviteMailConfigured() {
				return nil
			}
			sender, err := imail.NewSMTPSender(imail.SMTPConfig{
				Host: snap.SMTPHost, Port: snap.SMTPPort,
				Username: snap.SMTPUsername, Password: snap.SMTPPassword,
				From: snap.SMTPFrom,
			})
			if err != nil {
				inviteAccessLog.Warn("invite email: SMTP settings are invalid, email is off: %v", err)
				return nil
			}
			return sender
		},
		BaseURL: func() string {
			snap := config.Snapshot()
			return snap.InviteBaseURL(s.externalURL)
		},
		GuestHeaders: func() (handlers.GuestHeaders, bool) {
			snap := config.Snapshot()
			if !snap.InviteHeadersConfigured() {
				return handlers.GuestHeaders{}, false
			}
			return handlers.GuestHeaders{ClientID: snap.InviteCFAccessClientID, ClientSecret: snap.InviteCFAccessClientSecret}, true
		},
		GuestPolicySyncConfigured: func() bool {
			snap := config.Snapshot()
			return snap.GuestPolicySyncConfigured()
		},
	}
}

// guestPolicyEnsurer is the slice of *cfaccesspolicy.Client the invite flow
// uses: add on accept, remove on revoke.
type guestPolicyEnsurer interface {
	EnsureEmail(ctx context.Context, email string) (cfaccesspolicy.Result, error)
	RemoveEmail(ctx context.Context, email string) (cfaccesspolicy.Result, error)
}

// guestPolicyClient returns the ONE shared policy client (its mutex is what
// serializes concurrent accepts), nil when the sync is not configured, or the
// construction error when the settings are invalid. Built once: the settings
// are environment-only and fixed for the life of the process.
func (s *Server) guestPolicyClient() (guestPolicyEnsurer, error) {
	s.guestPolicyOnce.Do(func() {
		if s.guestPolicyOverride != nil {
			s.guestPolicy = s.guestPolicyOverride
			return
		}
		snap := config.Snapshot()
		if !snap.GuestPolicySyncConfigured() {
			return
		}
		c, err := cfaccesspolicy.New(cfaccesspolicy.Config{
			AccountID: snap.CFAccountID, PolicyID: snap.CFGuestsPolicyID, APIToken: snap.CFPolicyAPIToken,
		})
		if err != nil {
			s.guestPolicyErr = err
			return
		}
		s.guestPolicy = c
	})
	return s.guestPolicy, s.guestPolicyErr
}

// startGuestPolicySync adds an accepted invitee's email to the Cloudflare
// Access guest policy, in the background, and records the outcome on the
// invite for the admin (Users page, accepted invites). It never affects the
// accept: the account and session already exist when this runs.
//
// It runs only for an invite whose accept wrote an SSO grant (emailed to that
// address, created by an active admin); any other emailed invite is recorded
// as skipped with the reason, and Cloudflare is not touched.
//
// A revoke (deactivation, email change) can land while the add is in flight.
// The revoke deletes the grant and removes the Cloudflare entry only if the
// grant says the sync added it (CFAdded); the add sets CFAdded only after
// Cloudflare answers. SetInviteEmailGrantCFAdded and DeleteInviteEmailGrant
// run under one store lock, so exactly one side sees the add: either the flag
// lands first and the revoke removes the entry, or the delete lands first,
// SetInviteEmailGrantCFAdded reports the grant gone, and this goroutine
// removes the entry it just added. A revoke that lands before Cloudflare is
// called is caught by the re-read of the grant just before the call.
//
// A revoke does not treat an in-flight add as an add: if Cloudflare answers
// that the email was already present, someone put it there by hand, and only
// this goroutine learns that.
func (s *Server) startGuestPolicySync(token, userID string) {
	inv, err := s.storeForWiring().GetInvite(token)
	if err != nil || inv == nil || inv.Email == "" {
		return
	}
	email := inv.Email
	record := func(status, detail string) {
		ds, ok := database.AsCapability[database.InviteDeliveryStore](s.storeForWiring())
		if !ok {
			return
		}
		if err := ds.RecordInviteGuestPolicy(token, status, detail, time.Now()); err != nil {
			inviteAccessLog.Warn("invite accept: recording the guest-policy outcome failed: %v", err)
		}
	}
	switch {
	case inv.EmailBinding == database.InviteEmailNotEmailed:
		record(database.InviteGuestPolicySkipped, "the invitation was not emailed to this address, so the address was not verified; set the user's email and add it to Cloudflare by hand if wanted")
		return
	case inv.SSOGrant == database.InviteSSOCreatorNotAdmin:
		record(database.InviteGuestPolicySkipped, "the invitation was created by someone without the admin role, so no SSO or Cloudflare access was granted")
		return
	case inv.SSOGrant != database.InviteSSOGranted:
		return
	}
	client, err := s.guestPolicyClient()
	if err != nil {
		inviteAccessLog.Warn("invite accept: guest-policy settings are invalid, add the email by hand: %v", err)
		record(database.InviteGuestPolicyFailed, err.Error())
		return
	}
	if client == nil {
		record(database.InviteGuestPolicyNotConfigured, "")
		return
	}
	grants, ok := database.AsCapability[database.InviteEmailGrantStore](s.storeForWiring())
	if !ok {
		record(database.InviteGuestPolicyFailed, "the store cannot track SSO grants, so the Cloudflare entry could not be managed; add it by hand")
		return
	}
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	s.bgWG.Go("invite-guest-policy-sync", func() {
		s.syncGuestPolicy(parent, client, grants, record, email, userID)
	})
}

// syncGuestPolicy is the body of the accept-time sync goroutine; see
// startGuestPolicySync for the ordering argument.
func (s *Server) syncGuestPolicy(parent context.Context, client guestPolicyEnsurer, grants database.InviteEmailGrantStore,
	record func(status, detail string), email, userID string) {
	granted := func() (bool, error) {
		g, err := grants.GetInviteEmailGrant(email)
		if err != nil {
			return false, err
		}
		return g != nil && g.UserID == userID, nil
	}
	if ok, err := granted(); err != nil {
		record(database.InviteGuestPolicyFailed, "the SSO grant could not be read, so Cloudflare was not updated: "+err.Error())
		return
	} else if !ok {
		record(database.InviteGuestPolicyRevoked, "the access was revoked before Cloudflare was updated; nothing was added")
		return
	}
	record(database.InviteGuestPolicyPending, "")

	ctx, cancel := context.WithTimeout(parent, guestPolicySyncTimeout)
	defer cancel()
	res, err := client.EnsureEmail(ctx, email)
	switch {
	case err != nil:
		detail := err.Error()
		if errors.Is(err, context.Canceled) {
			detail = "server shut down before Cloudflare answered"
		}
		if ok, gerr := granted(); gerr == nil && !ok {
			detail += "; the access was also revoked meanwhile, so if the address is in the policy, remove it by hand"
		}
		inviteAccessLog.Warn("invite accept: adding the invitee to the Cloudflare guest policy failed, add it by hand: %v", logger.SanitizeLogValue(detail))
		record(database.InviteGuestPolicyFailed, detail)
		return
	case res == cfaccesspolicy.AlreadyPresent:
		// Put there by hand: never removed by this code, revoked or not.
		record(database.InviteGuestPolicyAlreadyPresent, "")
		return
	}

	still, err := grants.SetInviteEmailGrantCFAdded(email, userID)
	if err != nil {
		inviteAccessLog.Warn("invite accept: recording the Cloudflare add on the grant failed: %v", err)
		record(database.InviteGuestPolicyAdded, "added, but the server could not note that it added it, so revoking this user will not remove it: remove it by hand then")
		return
	}
	if still {
		inviteAccessLog.Info("invite accept: invitee added to the Cloudflare guest policy")
		record(database.InviteGuestPolicyAdded, "")
		return
	}
	// The grant was revoked while Cloudflare was answering. The revoke saw
	// CFAdded=false and left the entry; this goroutine added it, so it
	// removes it, under the same keep rules as a revoke.
	if why := s.keepGuestPolicyEmail(userID, email); why != "" {
		inviteAccessLog.Info("invite accept: access was revoked during the Cloudflare update; entry kept: %s", why)
		record(database.InviteGuestPolicyAdded, "the access was revoked during the update; the entry was kept because "+why)
		return
	}
	rctx, rcancel := context.WithTimeout(parent, guestPolicySyncTimeout)
	defer rcancel()
	if _, err := client.RemoveEmail(rctx, email); err != nil {
		inviteAccessLog.Warn("invite accept: access was revoked during the Cloudflare update and removing the entry again failed, remove it by hand: %v", logger.SanitizeLogValue(err.Error()))
		record(database.InviteGuestPolicyFailed, "the access was revoked while Cloudflare was being updated, and removing the address again failed: remove it by hand")
		return
	}
	inviteAccessLog.Info("invite accept: access was revoked during the Cloudflare update; entry removed again")
	record(database.InviteGuestPolicyRemoved, "the access was revoked while Cloudflare was being updated, so the address was removed again")
}

// revokeGuestAccess ends the access an accepted invite granted to email for
// userID: it deletes the SSO grant and, when the accept-time sync itself added
// the email to the Cloudflare guest policy, removes it from that one policy.
//
// Best-effort and idempotent: failures are logged, never returned, and a
// second call finds nothing to do. The Cloudflare entry is left alone when
// another active user still holds the address, when it is the owner's email,
// or when it is on the operator's OAUTH_ALLOWED_EMAILS: in each case someone
// still intends that address to have access.
func (s *Server) revokeGuestAccess(userID, email, reason string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || userID == "" {
		return
	}
	gs, ok := database.AsCapability[database.InviteEmailGrantStore](s.storeForWiring())
	if !ok {
		return
	}
	grant, err := gs.DeleteInviteEmailGrant(email, userID)
	if err != nil {
		inviteAccessLog.Warn("guest access revoke (%s): deleting the SSO grant failed: %v", reason, err)
		return
	}
	if grant == nil {
		return // nothing granted to this user for this address
	}
	inviteAccessLog.Info("guest access revoke (%s): SSO grant removed", reason)
	if !grant.CFAdded {
		return
	}
	if why := s.keepGuestPolicyEmail(userID, email); why != "" {
		inviteAccessLog.Info("guest access revoke (%s): Cloudflare guest-policy entry kept: %s", reason, why)
		return
	}
	client, err := s.guestPolicyClient()
	if err != nil || client == nil {
		inviteAccessLog.Warn("guest access revoke (%s): the email was added to the Cloudflare guest policy but the sync is no longer configured; remove it by hand", reason)
		return
	}
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	s.bgWG.Go("invite-guest-policy-revoke", func() {
		ctx, cancel := context.WithTimeout(parent, guestPolicySyncTimeout)
		defer cancel()
		res, err := client.RemoveEmail(ctx, email)
		if err != nil {
			inviteAccessLog.Warn("guest access revoke (%s): removing the email from the Cloudflare guest policy failed, remove it by hand: %v", reason, logger.SanitizeLogValue(err.Error()))
			return
		}
		inviteAccessLog.Info("guest access revoke (%s): Cloudflare guest policy: %s", reason, string(res))
	})
}

// keepGuestPolicyEmail returns why email must stay in the Cloudflare guest
// policy after userID gave it up, or "" when it may be removed.
//
// Only operator configuration and verified holders count. A user's email is
// self-service (PATCH /auth/me), so a matching address alone proves nothing:
// counting it would let anyone pin an address in the policy by typing it into
// their profile. Another active user holding the address keeps the entry only
// when that holding was verified, either by an accepted invite emailed to the
// address (their own inviteallow grant) or by an identity provider (a linked
// OAuth identity recorded with that email, which ResolveUser links only after
// the IdP verified it). If the users cannot be read the entry is kept: a
// failed removal is fixed by hand, a wrong one cuts someone off.
func (s *Server) keepGuestPolicyEmail(userID, email string) string {
	snap := config.Snapshot()
	if strings.EqualFold(strings.TrimSpace(snap.OwnerEmail), email) {
		return "it is the owner's email"
	}
	for _, allowed := range oauth.ParseAllowedEmails(snap.OAuthAllowedEmails) {
		if strings.EqualFold(allowed, email) {
			return "it is on OAUTH_ALLOWED_EMAILS"
		}
	}
	store := s.storeForWiring()
	users, err := store.ListUsers()
	if err != nil {
		return "the user list could not be read, so another verified holder cannot be ruled out"
	}
	grants, _ := database.AsCapability[database.InviteEmailGrantStore](store)
	for _, u := range users {
		if u.ID == userID || u.Status != "active" || !strings.EqualFold(strings.TrimSpace(u.Email), email) {
			continue
		}
		if grants != nil {
			if g, err := grants.GetInviteEmailGrant(email); err != nil {
				return "an SSO grant could not be read, so another verified holder cannot be ruled out"
			} else if g != nil && g.UserID == u.ID {
				return "another active user holds this email through an accepted invite"
			}
		}
		ids, err := store.GetOAuthIdentitiesForUser(u.ID)
		if err != nil {
			return "linked identities could not be read, so another verified holder cannot be ruled out"
		}
		for _, id := range ids {
			if strings.EqualFold(strings.TrimSpace(id.Email), email) {
				return "another active user holds this email, verified by an identity provider"
			}
		}
		inviteAccessLog.Info("guest access revoke: another account has this email set on its profile without verification; not counted")
	}
	return ""
}
