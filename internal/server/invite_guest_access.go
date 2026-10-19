// file: internal/server/invite_guest_access.go
// version: 1.0.0
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
// The grant counts only while it still describes a live account: the user it
// names exists, is active, and still has exactly this email. Deactivating the
// user, or the user changing their email, ends it, so no separate revoke is
// needed. Any error is a refusal.
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
		id, err := grants.InviteEmailGrantUserID(email)
		if err != nil || id == "" {
			return false
		}
		u, err := users.GetUserByID(id)
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

// guestPolicyEnsurer is the slice of *cfaccesspolicy.Client accept uses.
type guestPolicyEnsurer interface {
	EnsureEmail(ctx context.Context, email string) (cfaccesspolicy.Result, error)
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
func (s *Server) startGuestPolicySync(token, email string) {
	if email == "" {
		return
	}
	record := func(status, detail string) {
		ds, ok := database.AsCapability[database.InviteDeliveryStore](s.storeForWiring())
		if !ok {
			return
		}
		if err := ds.RecordInviteGuestPolicy(token, status, detail, time.Now()); err != nil {
			inviteAccessLog.Warn("invite accept: recording the guest-policy outcome failed: %v", err)
		}
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
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	s.bgWG.Go("invite-guest-policy-sync", func() {
		ctx, cancel := context.WithTimeout(parent, guestPolicySyncTimeout)
		defer cancel()
		res, err := client.EnsureEmail(ctx, email)
		switch {
		case err != nil:
			detail := err.Error()
			if errors.Is(err, context.Canceled) {
				detail = "server shut down before Cloudflare answered"
			}
			inviteAccessLog.Warn("invite accept: adding the invitee to the Cloudflare guest policy failed, add it by hand: %v", logger.SanitizeLogValue(detail))
			record(database.InviteGuestPolicyFailed, detail)
		case res == cfaccesspolicy.AlreadyPresent:
			record(database.InviteGuestPolicyAlreadyPresent, "")
		default:
			inviteAccessLog.Info("invite accept: invitee added to the Cloudflare guest policy")
			record(database.InviteGuestPolicyAdded, "")
		}
	})
}
