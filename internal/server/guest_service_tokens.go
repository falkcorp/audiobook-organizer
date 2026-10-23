// file: internal/server/guest_service_tokens.go
// version: 1.0.0
// guid: 9e4b7c15-2a83-4d60-b1f9-6c8d0a3e5f72
// last-edited: 2026-10-23

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	imail "github.com/falkcorp/audiobook-organizer/internal/mail"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Per-guest Cloudflare service tokens (owner decision 2026-10-22).
//
// A guest who uses the Audiobookshelf-compatible phone app cannot do
// Cloudflare's browser login, so accepting an emailed invitation (from an
// active admin, the same gate as the guest-policy sync) mints that guest's OWN
// service token, adds that ONE token id to the non-identity "books" policy
// (CF_SERVICE_AUTH_POLICY_ID; never any_valid_service_token), and returns the
// client id and secret in the accept response, once. The server keeps only the
// token id and a SHA-256 of the secret (database.GuestServiceToken).
//
// Off by default (CF_GUEST_SERVICE_TOKENS). It fails closed: when Cloudflare
// answers 403 (the API token lacks "Access: Service Tokens Edit") nothing is
// minted, the accept still succeeds, the guest is told to ask the admin, and
// the admin sees the exact reason on the Users page.
//
// Ordering and races. The record is written (pending) BEFORE Cloudflare is
// called, then moves pending -> minted (token id recorded) -> active (in the
// policy). Every move is an UpdateGuestServiceToken under the store lock that
// checks the status it expects, and the revoke sets "revoking" under that same
// lock before touching Cloudflare, so exactly one side sees the other:
//   - a revoke that lands mid-mint makes the mint's next move fail its status
//     check, and the mint deletes the token it just made;
//   - a revoke that landed before the record existed is caught by re-reading
//     the user after the pending record is written: a deactivation locks the
//     user first and revokes second, so either the lock is visible or the
//     record is.
// The revoke always tries the policy-rule removal when a token id is recorded
// (idempotent, a read when nothing is there), so it does not depend on the
// PolicyAdded flag having won a race.

// guestTokenTimeout bounds one mint or revoke (token call plus policy edit).
const guestTokenTimeout = 45 * time.Second

// Admin-facing wording. None of it goes to the guest.
const (
	guestTokenForbiddenMsg = "Cloudflare refused (HTTP 403): the server's API token (CF_POLICY_API_TOKEN) is missing the " +
		"Account > Access: Service Tokens > Edit permission. Add it, then invite this guest again"
	// guestTokenUnavailableMsg is all the guest is told when a mint fails.
	guestTokenUnavailableMsg = "Phone app access could not be set up automatically. Your account works in a browser; ask the person who invited you about the phone app."
)

// Audit entry types (activity tier "audit"). None carries a secret, a client id
// or an email address.
const (
	auditGuestTokenMinted       = "guest_service_token_minted"
	auditGuestTokenMintFailed   = "guest_service_token_mint_failed"
	auditGuestTokenRevoked      = "guest_service_token_revoked"
	auditGuestTokenRevokeFailed = "guest_service_token_revoke_failed"
)

// guestTokenCloudflare is the slice of *cfservicetoken.Client the feature uses.
type guestTokenCloudflare interface {
	Create(ctx context.Context, name string) (*cfservicetoken.Created, error)
	Revoke(ctx context.Context, id string) (bool, error)
	List(ctx context.Context) ([]cfservicetoken.Token, error)
}

// guestTokenPolicy is the slice of *cfaccesspolicy.Client for the books policy.
type guestTokenPolicy interface {
	EnsureServiceToken(ctx context.Context, tokenID string) (cfaccesspolicy.Result, error)
	RemoveServiceToken(ctx context.Context, tokenID string) (cfaccesspolicy.Result, error)
}

// guestTokenState holds the two shared clients, built once (the settings are
// environment-only and fixed for the life of the process). The books-policy
// client is its own instance with its own mutex, separate from the guest-email
// policy client. The overrides, set before first use, replace them (tests).
type guestTokenState struct {
	once           sync.Once
	api            guestTokenCloudflare
	policy         guestTokenPolicy
	err            error
	apiOverride    guestTokenCloudflare
	policyOverride guestTokenPolicy
}

// guestTokenClients returns the clients, or an error when the settings are
// missing or invalid. They are built from the Cloudflare settings alone, not
// the feature flag, so an admin can still revoke tokens after the flag is
// switched off.
func (s *Server) guestTokenClients() (guestTokenCloudflare, guestTokenPolicy, error) {
	st := &s.guestTokens
	st.once.Do(func() {
		if st.apiOverride != nil && st.policyOverride != nil {
			st.api, st.policy = st.apiOverride, st.policyOverride
			return
		}
		snap := config.Snapshot()
		if snap.CFAccountID == "" || snap.CFPolicyAPIToken == "" || snap.CFServiceAuthPolicyID == "" {
			st.err = errors.New("CF_ACCOUNT_ID, CF_POLICY_API_TOKEN and CF_SERVICE_AUTH_POLICY_ID must all be set")
			return
		}
		api, err := cfservicetoken.New(cfservicetoken.Config{AccountID: snap.CFAccountID, APIToken: snap.CFPolicyAPIToken})
		if err != nil {
			st.err = err
			return
		}
		pol, err := cfaccesspolicy.New(cfaccesspolicy.Config{
			AccountID: snap.CFAccountID, PolicyID: snap.CFServiceAuthPolicyID, APIToken: snap.CFPolicyAPIToken,
		})
		if err != nil {
			st.err = err
			return
		}
		st.api, st.policy = api, pol
	})
	return st.api, st.policy, st.err
}

// warnGuestServiceTokensMisconfigured logs, at startup, the flag being on
// without the settings it needs.
func warnGuestServiceTokensMisconfigured(snap config.Config) {
	if snap.GuestServiceTokensMisconfigured() {
		inviteAccessLog.Warn("CF_GUEST_SERVICE_TOKENS is on but CF_ACCOUNT_ID, CF_POLICY_API_TOKEN and CF_SERVICE_AUTH_POLICY_ID " +
			"are not all set: no phone-app tokens will be minted until they are")
	}
}

// guestTokenIssue is what the accept response carries about the phone-app
// key: the credential once, or the fact that none could be made. It is the
// only place the client secret lives after Cloudflare answers, so every fmt
// verb prints it redacted.
type guestTokenIssue struct {
	Issued       bool
	ClientID     string
	ClientSecret string
	Message      string
}

func (g guestTokenIssue) redacted() string {
	return fmt.Sprintf("guestTokenIssue{Issued:%v ClientSecret:[redacted]}", g.Issued)
}
func (g guestTokenIssue) String() string   { return g.redacted() }
func (g guestTokenIssue) GoString() string { return g.redacted() }
func (g guestTokenIssue) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(g.redacted()))
}

// response is the accept response's phone_app object.
func (g *guestTokenIssue) response() gin.H {
	if g.Issued {
		return gin.H{
			"status": "issued", "client_id": g.ClientID, "client_secret": g.ClientSecret,
			"id_header": "CF-Access-Client-Id", "secret_header": "CF-Access-Client-Secret",
		}
	}
	return gin.H{"status": "unavailable", "message": g.Message}
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// guestTokenErrText turns a Cloudflare error into the admin-facing detail.
// Only the HTTP status picks the wording; Cloudflare's own text is never
// interpreted (it is capped and scrubbed by the clients and appended for the
// admin to read).
func guestTokenErrText(step string, err error) string {
	switch {
	case errors.Is(err, cfservicetoken.ErrForbidden), cfaccesspolicy.IsForbidden(err):
		return step + ": " + guestTokenForbiddenMsg
	case errors.Is(err, context.Canceled):
		return step + ": the server shut down before Cloudflare answered"
	case errors.Is(err, context.DeadlineExceeded):
		return step + ": Cloudflare did not answer in time"
	}
	return step + ": " + logger.SanitizeLogValue(err.Error())
}

// mintGuestServiceToken is called by handleAcceptInvite after the account and
// session exist. It returns nil when the feature does not apply to this accept
// (off, or the invite did not earn an SSO grant), otherwise the issue to put
// in the response. It never fails the accept and never logs the secret.
func (s *Server) mintGuestServiceToken(parent context.Context, inviteToken string, user *database.User) *guestTokenIssue {
	snap := config.Snapshot()
	if !snap.CFGuestServiceTokens {
		return nil
	}
	store := s.storeForWiring()
	inv, err := store.GetInvite(inviteToken)
	if err != nil || inv == nil || inv.SSOGrant != database.InviteSSOGranted {
		return nil // same gate as the guest-policy sync: emailed, active admin created it
	}
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](store)
	if !ok {
		inviteAccessLog.Warn("guest service token: the store cannot hold token records, none minted")
		return &guestTokenIssue{Message: guestTokenUnavailableMsg}
	}
	name := cfservicetoken.Name(user.ID)
	if err := tokens.CreateGuestServiceToken(database.GuestServiceToken{
		UserID: user.ID, Name: name, Status: database.GuestTokenPending,
	}); err != nil {
		inviteAccessLog.Warn("guest service token: writing the pending record failed, none minted: %v", logger.SanitizeLogValue(err.Error()))
		return &guestTokenIssue{Message: guestTokenUnavailableMsg}
	}
	fail := func(detail string) *guestTokenIssue {
		s.finishGuestTokenFailed(tokens, user.ID, detail)
		return &guestTokenIssue{Message: guestTokenUnavailableMsg}
	}

	if !snap.GuestServiceTokensEnabled() {
		return fail("CF_GUEST_SERVICE_TOKENS is on but CF_ACCOUNT_ID, CF_POLICY_API_TOKEN and CF_SERVICE_AUTH_POLICY_ID are not all set")
	}
	api, policy, err := s.guestTokenClients()
	if err != nil {
		return fail("the Cloudflare settings are invalid: " + logger.SanitizeLogValue(err.Error()))
	}
	// A deactivation locks the user BEFORE it looks for a record, so after the
	// pending record exists either the lock is visible here or the revoke sees
	// the record.
	if u, uerr := store.GetUserByID(user.ID); uerr != nil || u == nil || u.Status != "active" {
		return fail("the account was deactivated before the token was made; nothing was minted")
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), guestTokenTimeout)
	defer cancel()
	created, err := api.Create(ctx, name)
	if err != nil {
		if created != nil && created.ID != "" {
			// Cloudflare made a token but its answer was unusable: remove it.
			if _, rerr := api.Revoke(ctx, created.ID); rerr != nil {
				return s.finishRevokeFailed(tokens, user.ID, created.ID,
					guestTokenErrText("minted token unusable, and removing it failed", rerr))
			}
		}
		return fail(guestTokenErrText("creating the service token", err))
	}

	// cleanup removes the token (and any policy rule) this mint made.
	cleanup := func(why string) *guestTokenIssue {
		var problems []string
		if _, rerr := api.Revoke(ctx, created.ID); rerr != nil {
			problems = append(problems, guestTokenErrText("deleting the token", rerr))
		}
		if _, rerr := policy.RemoveServiceToken(ctx, created.ID); rerr != nil && !errors.Is(rerr, cfaccesspolicy.ErrProtectedToken) {
			problems = append(problems, guestTokenErrText("removing the policy rule", rerr))
		}
		if len(problems) > 0 {
			return s.finishRevokeFailed(tokens, user.ID, created.ID, why+"; cleanup incomplete: "+joinProblems(problems))
		}
		s.finishGuestTokenFailed(tokens, user.ID, why)
		return &guestTokenIssue{Message: guestTokenUnavailableMsg}
	}

	if _, err := tokens.UpdateGuestServiceToken(user.ID, func(g *database.GuestServiceToken) error {
		if g.Status != database.GuestTokenPending {
			return errGuestTokenLostRace
		}
		g.TokenID, g.SecretHash, g.Status = created.ID, hashSecret(created.ClientSecret), database.GuestTokenMinted
		return nil
	}); err != nil {
		if errors.Is(err, errGuestTokenLostRace) || errors.Is(err, database.ErrGuestServiceTokenNotFound) {
			// Revoked while Cloudflare was answering: delete what was just made.
			s.cleanupLostMint(ctx, api, policy, user.ID, created.ID)
			return &guestTokenIssue{Message: guestTokenUnavailableMsg}
		}
		return cleanup("recording the minted token failed: " + logger.SanitizeLogValue(err.Error()))
	}

	_, err = policy.EnsureServiceToken(ctx, created.ID)
	unverified := errors.Is(err, cfaccesspolicy.ErrWriteUnverified)
	if err != nil && !unverified {
		return cleanup(guestTokenErrText("adding the token to the books policy", err))
	}
	note := ""
	if unverified {
		note = "added, but Cloudflare's answer could not confirm it; check the books policy"
	}
	if _, err := tokens.UpdateGuestServiceToken(user.ID, func(g *database.GuestServiceToken) error {
		if g.Status != database.GuestTokenMinted {
			return errGuestTokenLostRace
		}
		g.Status, g.PolicyAdded, g.Detail = database.GuestTokenActive, true, note
		return nil
	}); err != nil {
		if errors.Is(err, errGuestTokenLostRace) {
			s.cleanupLostMint(ctx, api, policy, user.ID, created.ID)
			return &guestTokenIssue{Message: guestTokenUnavailableMsg}
		}
		return cleanup("recording the active token failed: " + logger.SanitizeLogValue(err.Error()))
	}

	inviteAccessLog.Info("guest service token: minted and added to the books policy for account %s (token %s)", user.ID, created.ID)
	s.recordGuestTokenAudit(auditGuestTokenMinted, "info", user.ID, created.ID, "", "a per-guest Cloudflare service token was minted and added to the books policy")
	issue := &guestTokenIssue{Issued: true, ClientID: created.ClientID, ClientSecret: created.ClientSecret}
	s.emailGuestToken(user, issue)
	return issue
}

var errGuestTokenLostRace = errors.New("the record was revoked while the token was being made")

func joinProblems(p []string) string {
	out := ""
	for i, x := range p {
		if i > 0 {
			out += "; "
		}
		out += x
	}
	return out
}

// cleanupLostMint removes a token whose mint lost a race with a revoke, and
// marks the record revoked when that succeeds.
func (s *Server) cleanupLostMint(ctx context.Context, api guestTokenCloudflare, policy guestTokenPolicy, userID, tokenID string) {
	_, aerr := api.Revoke(ctx, tokenID)
	_, perr := policy.RemoveServiceToken(ctx, tokenID)
	if aerr != nil || perr != nil {
		err := aerr
		if err == nil {
			err = perr
		}
		// The record may already say "revoked" (that revoke saw no token yet);
		// a token is still out there, so say so where the admin looks.
		if tokens, ok := database.AsCapability[database.GuestServiceTokenStore](s.storeForWiring()); ok {
			s.finishRevokeFailed(tokens, userID, tokenID, "revoked during the mint, and removing the new token failed: "+guestTokenErrText("cleanup", err))
		}
		return
	}
	inviteAccessLog.Info("guest service token: account %s was revoked during the mint; token %s removed again", userID, tokenID)
}

// finishGuestTokenFailed records a mint that failed with nothing left behind.
func (s *Server) finishGuestTokenFailed(tokens database.GuestServiceTokenStore, userID, detail string) {
	if _, err := tokens.UpdateGuestServiceToken(userID, func(g *database.GuestServiceToken) error {
		if g.Status == database.GuestTokenRevoked || g.Status == database.GuestTokenRevoking {
			return errGuestTokenLostRace // a revoke already settled it; keep that
		}
		g.Status, g.Detail = database.GuestTokenFailed, detail
		return nil
	}); err != nil && !errors.Is(err, errGuestTokenLostRace) {
		inviteAccessLog.Warn("guest service token: recording the failed mint failed: %v", logger.SanitizeLogValue(err.Error()))
	}
	inviteAccessLog.Warn("guest service token: not minted for account %s: %s", userID, logger.SanitizeLogValue(detail))
	s.recordGuestTokenAudit(auditGuestTokenMintFailed, "warn", userID, "", detail, "a per-guest Cloudflare service token could not be minted")
}

// finishRevokeFailed records a token that exists at Cloudflare (or may) and
// could not be removed, so the admin can retry the revoke.
func (s *Server) finishRevokeFailed(tokens database.GuestServiceTokenStore, userID, tokenID, detail string) *guestTokenIssue {
	if _, err := tokens.UpdateGuestServiceToken(userID, func(g *database.GuestServiceToken) error {
		if g.TokenID == "" {
			g.TokenID = tokenID
		}
		g.Status, g.Detail = database.GuestTokenRevokeFailed, detail
		return nil
	}); err != nil {
		inviteAccessLog.Warn("guest service token: recording the failed cleanup failed: %v", logger.SanitizeLogValue(err.Error()))
	}
	inviteAccessLog.Warn("guest service token: account %s: token %s may still exist, remove it by hand: %s", userID, tokenID, logger.SanitizeLogValue(detail))
	s.recordGuestTokenAudit(auditGuestTokenRevokeFailed, "warn", userID, tokenID, detail, "a per-guest Cloudflare service token may still exist")
	return &guestTokenIssue{Message: guestTokenUnavailableMsg}
}

// errGuestTokenNone: the user has no token record.
var errGuestTokenNone = errors.New("no phone-app token record for this user")

// revokeGuestServiceToken revokes userID's token: it marks the record
// "revoking" (so a mint in flight sees it), deletes the token at Cloudflare
// (the credential dies first), then removes the policy rule. reason is a short
// label for the audit entry; actorID is the admin who asked, "" for the
// automatic revoke on deactivation. A record with no Cloudflare token behind it
// is simply closed. A second call on a revoked record is a no-op.
func (s *Server) revokeGuestServiceToken(parent context.Context, userID, reason, actorID string) error {
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](s.storeForWiring())
	if !ok {
		return errGuestTokenNone
	}
	var snapshot database.GuestServiceToken
	alreadyRevoked := false
	if _, err := tokens.UpdateGuestServiceToken(userID, func(g *database.GuestServiceToken) error {
		snapshot = *g
		if g.Status == database.GuestTokenRevoked {
			alreadyRevoked = true
			return errGuestTokenLostRace // no write
		}
		g.Status, g.Detail = database.GuestTokenRevoking, ""
		return nil
	}); err != nil {
		if alreadyRevoked {
			return nil
		}
		if errors.Is(err, database.ErrGuestServiceTokenNotFound) {
			return errGuestTokenNone
		}
		return err
	}

	closeRecord := func() {
		now := time.Now().UTC()
		if _, err := tokens.UpdateGuestServiceToken(userID, func(g *database.GuestServiceToken) error {
			g.Status, g.Detail, g.PolicyAdded, g.RevokedAt, g.RevokeReason = database.GuestTokenRevoked, "", false, &now, reason
			return nil
		}); err != nil {
			inviteAccessLog.Warn("guest service token: recording the revoke failed: %v", logger.SanitizeLogValue(err.Error()))
		}
	}
	if snapshot.TokenID == "" {
		// Nothing was made yet (pending or failed): a mint in flight sees
		// "revoking" and deletes its own token.
		closeRecord()
		s.recordGuestTokenAudit(auditGuestTokenRevoked, "info", userID, "", reason, "a guest's phone-app token record was closed (no Cloudflare token existed yet)")
		return nil
	}

	fail := func(detail string) error {
		_, _ = tokens.UpdateGuestServiceToken(userID, func(g *database.GuestServiceToken) error {
			g.Status, g.Detail = database.GuestTokenRevokeFailed, detail
			return nil
		})
		inviteAccessLog.Warn("guest service token: revoking account %s token %s failed: %s", userID, snapshot.TokenID, logger.SanitizeLogValue(detail))
		s.recordGuestTokenAudit(auditGuestTokenRevokeFailed, "warn", userID, snapshot.TokenID, detail, "revoking a guest's Cloudflare service token failed")
		return errors.New(detail)
	}
	api, policy, err := s.guestTokenClients()
	if err != nil {
		return fail("the Cloudflare settings are missing or invalid, so the token was not deleted; delete " + snapshot.Name + " in the Cloudflare dashboard by hand: " + logger.SanitizeLogValue(err.Error()))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), guestTokenTimeout)
	defer cancel()
	if _, err := api.Revoke(ctx, snapshot.TokenID); err != nil {
		return fail(guestTokenErrText("deleting the token", err))
	}
	if _, err := policy.RemoveServiceToken(ctx, snapshot.TokenID); err != nil {
		return fail("the token is deleted but its rule is still in the books policy (it admits nothing; remove it by hand): " + guestTokenErrText("removing the policy rule", err))
	}
	closeRecord()
	inviteAccessLog.Info("guest service token: revoked token %s for account %s (%s)", snapshot.TokenID, userID, reason)
	details := reason
	if actorID != "" {
		details += " (by admin account " + actorID + ")"
	}
	s.recordGuestTokenAudit(auditGuestTokenRevoked, "info", userID, snapshot.TokenID, details, "a guest's Cloudflare service token was revoked")
	return nil
}

// revokeGuestServiceTokenAsync is the deactivation hook (handlers.GuestTokenRevoker):
// best-effort, in the background, a no-op for a user with no token record.
func (s *Server) revokeGuestServiceTokenAsync(userID, reason string) {
	if tokens, ok := database.AsCapability[database.GuestServiceTokenStore](s.storeForWiring()); ok {
		if g, err := tokens.GetGuestServiceToken(userID); err == nil && g == nil {
			// No record yet: a mint in flight is caught by its own user re-read.
			return
		}
	}
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	s.bgWG.Go("guest-service-token-revoke", func() {
		if err := s.revokeGuestServiceToken(parent, userID, reason, ""); err != nil && !errors.Is(err, errGuestTokenNone) {
			inviteAccessLog.Warn("guest service token revoke (%s) did not finish: %v", reason, logger.SanitizeLogValue(err.Error()))
		}
	})
}

// recordGuestTokenAudit writes an audit-tier activity entry. It carries account
// and token ids only: never a secret, a client id or an address.
func (s *Server) recordGuestTokenAudit(typ, level, userID, tokenID, detail, summary string) {
	if s.activityService == nil {
		return
	}
	d := map[string]any{"user_id": userID}
	if tokenID != "" {
		d["token_id"] = tokenID
	}
	if detail != "" {
		d["detail"] = logger.SanitizeLogValue(detail)
	}
	if err := s.activityService.Record(database.ActivityEntry{
		Timestamp: time.Now().UTC(), Tier: "audit", Type: typ, Level: level, Source: "invite",
		Summary: fmt.Sprintf("%s (account %s)", summary, userID), Details: d,
	}); err != nil {
		inviteAccessLog.Warn("recording the %s audit entry failed: %v", typ, err)
	}
}

// emailGuestToken sends the credential to the guest's verified address when the
// owner turned INVITE_EMAIL_SERVICE_TOKEN on. Off by default: an email is a
// weaker place for a secret than the one-time page. Background, so a slow relay
// never delays the accept; the secret stays in memory only.
func (s *Server) emailGuestToken(user *database.User, issue *guestTokenIssue) {
	snap := config.Snapshot()
	if !snap.InviteEmailServiceToken || user.Email == "" {
		return
	}
	sender := s.inviteMailSender()
	if sender == nil {
		return
	}
	msg := imail.Message{
		To:      user.Email,
		Subject: "Your Audiobook Organizer phone-app key",
		Text: "Hello " + user.Username + ",\n\n" +
			"Your phone app needs two custom headers on every request to " + snap.InviteBaseURL(s.externalURL) + ":\n\n" +
			"  CF-Access-Client-Id: " + issue.ClientID + "\n" +
			"  CF-Access-Client-Secret: " + issue.ClientSecret + "\n\n" +
			"This key belongs to you alone. Do not share it. Delete this email once the app works.\n",
	}
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	s.bgWG.Go("guest-service-token-email", func() {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		if err := sender.Send(ctx, msg); err != nil {
			inviteAccessLog.Warn("guest service token email: send failed for account %s: %v", user.ID, logger.SanitizeLogValue(err.Error()))
			return
		}
		inviteAccessLog.Info("guest service token email: sent to account %s", user.ID)
	})
}

// ── Admin routes ────────────────────────────────────────────────────────────

// guestTokenView is one row of the admin list. No secret, hash or client id.
type guestTokenView struct {
	UserID       string     `json:"user_id"`
	Username     string     `json:"username,omitempty"`
	TokenID      string     `json:"token_id,omitempty"`
	Status       string     `json:"status"`
	Detail       string     `json:"detail,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokeReason string     `json:"revoke_reason,omitempty"`
	// LastSeenAt is what Cloudflare reports; absent when it did not say.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

func (s *Server) guestTokenViews(ctx context.Context) (views []guestTokenView, lastSeenNote string, err error) {
	store := s.storeForWiring()
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](store)
	if !ok {
		return []guestTokenView{}, "", nil
	}
	recs, err := tokens.ListGuestServiceTokens()
	if err != nil {
		return nil, "", err
	}
	// ONE list call, joined on the ids we hold: nothing about any other token
	// (the owner's included) reaches the response.
	seen := map[string]*time.Time{}
	live := false
	for _, r := range recs {
		if r.TokenID != "" && r.Status != database.GuestTokenRevoked && r.Status != database.GuestTokenFailed {
			live = true
			break
		}
	}
	if live {
		if api, _, cerr := s.guestTokenClients(); cerr != nil {
			lastSeenNote = "Cloudflare was not asked: its settings are missing"
		} else if list, lerr := api.List(ctx); lerr != nil {
			lastSeenNote = guestTokenErrText("asking Cloudflare for last-seen times", lerr)
		} else {
			for _, t := range list {
				seen[t.ID] = t.LastSeenAt
			}
		}
	}
	for _, r := range recs {
		v := guestTokenView{
			UserID: r.UserID, TokenID: r.TokenID, Status: r.Status, Detail: r.Detail,
			CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt, RevokeReason: r.RevokeReason,
		}
		if u, uerr := store.GetUserByID(r.UserID); uerr == nil && u != nil {
			v.Username = u.Username
		}
		if r.TokenID != "" {
			v.LastSeenAt = seen[r.TokenID]
		}
		views = append(views, v)
	}
	if views == nil {
		views = []guestTokenView{}
	}
	return views, lastSeenNote, nil
}

// handleListGuestServiceTokens handles GET /api/v1/admin/guest-service-tokens.
func (s *Server) handleListGuestServiceTokens(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(c.Request.Context(), guestTokenTimeout)
	defer cancel()
	views, note, err := s.guestTokenViews(ctx)
	if err != nil {
		httputil.InternalError(c, "list guest service tokens", err)
		return
	}
	snap := config.Snapshot()
	httputil.RespondWithOK(c, gin.H{
		"enabled":        snap.GuestServiceTokensEnabled(),
		"flag_on":        snap.CFGuestServiceTokens,
		"last_seen_note": note,
		"tokens":         views,
	})
}

// handleRevokeGuestServiceToken handles
// POST /api/v1/admin/guest-service-tokens/:userId/revoke: an admin revokes one
// guest's token now. It runs the same revoke as a deactivation, synchronously,
// so the admin sees whether Cloudflare confirmed it.
func (s *Server) handleRevokeGuestServiceToken(c *gin.Context) {
	actor, ok := servermiddleware.CurrentUser(c)
	if !ok || actor == nil {
		httputil.RespondWithForbidden(c, "revoking a guest token needs the admin role")
		return
	}
	userID := c.Param("userId")
	if userID == "" || len(userID) > 64 {
		httputil.RespondWithBadRequest(c, "user id required")
		return
	}
	err := s.revokeGuestServiceToken(c.Request.Context(), userID, "revoked by an admin", actor.ID)
	switch {
	case errors.Is(err, errGuestTokenNone):
		httputil.RespondWithNotFound(c, "guest service token", userID)
		return
	case err != nil:
		httputil.RespondWithError(c, http.StatusBadGateway, err.Error(), "GUEST_TOKEN_REVOKE_FAILED")
		return
	}
	httputil.RespondWithOK(c, gin.H{"revoked": true, "user_id": userID})
}
