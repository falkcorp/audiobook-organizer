// file: internal/server/guest_key_reissue.go
// version: 1.1.3
// guid: 06852e7f-0413-46b5-9a54-623e28379772
// last-edited: 2026-11-10

package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// An admin issues a guest's phone-app key from the Phone app keys panel, and the
// server emails it to the account's email address: the admin never sees it
// (owner decision 2026-11-07). One action covers two cases:
//
//   - a guest who HAS a key record gets a new key: the old one is revoked first
//     and its record reopened (see the ordering notes at the top of
//     guest_service_tokens.go);
//   - an active, non-admin user with a trusted email address and NO record gets a
//     first key: the record is created, exactly as accept would have (owner
//     decision, same day). Admins and the owner never get one this way: they sign
//     in through Cloudflare's own login (owner decision 2026-11-07, later). A
//     record that already exists is reissued whoever holds it.
//
// "Trusted" is the store's rule (trustedEmailSource in internal/books/store): an
// address an admin set counts, one the user typed in themselves does not. The
// admin-facing wording therefore says "the account's email address", not
// "verified".
//
// It works with INVITE_EMAIL_SERVICE_TOKEN off: that setting governs only the
// copy mailed at accept time. It needs CF_GUEST_SERVICE_TOKENS on, the
// Cloudflare settings complete, mail configured and an absolute public URL, and
// every one of those is checked BEFORE anything is revoked or minted.

// Codes the issue endpoint answers with.
const (
	guestKeyBlockedCode = "GUEST_TOKEN_REISSUE_BLOCKED"
	guestKeyBusyCode    = "GUEST_TOKEN_REISSUE_BUSY"
	guestKeyFailedCode  = "GUEST_TOKEN_REISSUE_FAILED"
)

// guestKeyIssuingNow is the row's blocked reason while a worker holds the
// per-user guard (after a 202 the record already reads "active", so the record
// alone would show the button enabled and a click would 409).
const guestKeyIssuingNow = "a key is being issued now"

const (
	// guestKeyHandlerWait is how long the handler waits for the work before it
	// answers 202. Cloudflare cuts a proxied request off at 100 seconds (524);
	// the worst case here is about 140: revoke 45, mint 45, reap about 20, mail
	// 30. The work runs in the background either way.
	guestKeyHandlerWait = 85 * time.Second
	guestKeyMailTimeout = 30 * time.Second
)

// guestKeyStuckHint is appended where the admin may need to clear a stuck record.
const guestKeyStuckHint = "a key is being set up or revoked right now; if it is stuck (for example after a restart), use Revoke on its row, then try again"

// guestKeyFailure is an issue that did not complete, with the response it earns.
// The message is admin-facing and built from fixed wording (guestTokenErrText),
// never Cloudflare's free text, and never the secret or the client id.
type guestKeyFailure struct {
	Status int
	Code   string
	Msg    string
}

func guestKeyBlocked(msg string) *guestKeyFailure {
	return &guestKeyFailure{http.StatusConflict, guestKeyBlockedCode, msg}
}
func guestKeyBusy(msg string) *guestKeyFailure {
	return &guestKeyFailure{http.StatusConflict, guestKeyBusyCode, msg}
}
func guestKeyFailed(msg string) *guestKeyFailure {
	return &guestKeyFailure{http.StatusBadGateway, guestKeyFailedCode, msg}
}

// guestKeyShutdown is an issue stopped because the server is shutting down.
func guestKeyShutdown(msg string) *guestKeyFailure {
	return &guestKeyFailure{http.StatusServiceUnavailable, guestKeyFailedCode, msg}
}

// guestKeyResult is a completed issue. It carries ids only.
type guestKeyResult struct {
	TokenID         string
	PreviousTokenID string
	// FirstKey is true when the user had no key record before.
	FirstKey bool
}

// guestKeyDeploymentBlocked returns why NO key can be issued by email right now
// on this deployment, or "". It is checked before any token write or Cloudflare
// call, and the list reuses it for each row, so the panel and the endpoint
// cannot disagree. It does not call s.inviteDelivery(), which logs startup
// warnings on every call.
func (s *Server) guestKeyDeploymentBlocked() string {
	snap := config.Snapshot()
	if !snap.GuestServiceTokensEnabled() {
		return "phone-app keys are switched off or their Cloudflare settings are incomplete " +
			"(CF_GUEST_SERVICE_TOKENS, CF_ACCOUNT_ID, CF_POLICY_API_TOKEN, CF_SERVICE_AUTH_POLICY_ID)"
	}
	if _, _, err := s.guestTokenClients(); err != nil {
		return "the Cloudflare settings are invalid: " + logger.SanitizeLogValue(err.Error())
	}
	if _, ok := database.AsCapability[database.GuestServiceTokenStore](s.storeForWiring()); !ok {
		return "the store cannot hold phone-app key records"
	}
	if why := handlers.EmailSendBlocked(s.inviteMailSender(), snap.InviteBaseURL(s.externalURL)); why != "" {
		return why + ", so the key cannot be emailed"
	}
	return ""
}

// guestKeyUserBlocked returns why u cannot be issued a key, or "": the account
// must be active and its email address trusted (an admin-set address counts; one
// the user typed in does not). trustedEmailSource is unexported, so the
// exported check is the store's verified-email lookup naming this same user.
func guestKeyUserBlocked(store database.UserReader, u *database.User) string {
	if u == nil || u.Status != "active" {
		return "the account is not active"
	}
	v, err := store.GetUserByVerifiedEmail(u.Email)
	if err != nil {
		return "the account's email address could not be checked"
	}
	if v == nil || v.ID != u.ID {
		return "the account has no trusted email address (one the user typed in themselves does not count); set one on the Users page first"
	}
	return ""
}

// guestKeyFirstKeyBlocked returns why u cannot be issued a FIRST key, or "":
// admins and the owner (the account whose email is OWNER_EMAIL) sign in through
// Cloudflare's own login and have no use for a phone-app key. It does not apply
// to a user who already has a record: reissue is unchanged for them.
func guestKeyFirstKeyBlocked(u *database.User) string {
	const why = "admins sign in through Cloudflare's own login and don't need a phone-app key"
	for _, role := range u.Roles {
		if role == auth.SeedRoleAdmin {
			return why
		}
	}
	if owner := coreauth.NormalizeEmail(config.Snapshot().OwnerEmail); owner != "" && coreauth.NormalizeEmail(u.Email) == owner {
		return why
	}
	return ""
}

// checkGuestKeyIssue runs every check that does not change anything: the
// deployment, the user and the record's state. It returns the fresh user and
// the record (nil when the user has none: a first key).
func (s *Server) checkGuestKeyIssue(userID string) (*database.User, *database.GuestServiceToken, *guestKeyFailure) {
	if why := s.guestKeyDeploymentBlocked(); why != "" {
		return nil, nil, guestKeyBlocked(why)
	}
	store := s.storeForWiring()
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](store)
	if !ok {
		return nil, nil, guestKeyBlocked("the store cannot hold phone-app key records")
	}
	user, err := store.GetUserByID(userID)
	if err != nil {
		return nil, nil, &guestKeyFailure{http.StatusInternalServerError, guestKeyFailedCode, "the account could not be read"}
	}
	if user == nil {
		return nil, nil, &guestKeyFailure{http.StatusNotFound, "NOT_FOUND", "no such user"}
	}
	if why := guestKeyUserBlocked(store, user); why != "" {
		return nil, nil, guestKeyBlocked(why)
	}
	rec, err := tokens.GetGuestServiceToken(userID)
	if err != nil {
		return nil, nil, &guestKeyFailure{http.StatusInternalServerError, guestKeyFailedCode, "the key record could not be read"}
	}
	if rec != nil && guestTokenInFlight(rec.Status) {
		return nil, nil, guestKeyBusy(guestKeyStuckHint)
	}
	if rec == nil {
		if why := guestKeyFirstKeyBlocked(user); why != "" {
			return nil, nil, guestKeyBlocked(why)
		}
	}
	return user, rec, nil
}

// guestKeyRowBlocked is the per-row reason the panel disables its button, "" when
// the click would be accepted. deployment is guestKeyDeploymentBlocked's answer,
// computed once per list. rec is nil for a user with no record. issuing is true
// while a worker holds the user's per-user guard: it comes first, because the
// stuck-record hint would send the admin to Revoke a key that is being mailed.
func guestKeyRowBlocked(store database.UserReader, deployment string, u *database.User, rec *database.GuestServiceToken, issuing bool) string {
	if issuing {
		return guestKeyIssuingNow
	}
	if rec != nil && guestTokenInFlight(rec.Status) {
		return guestKeyStuckHint
	}
	if deployment != "" {
		return deployment
	}
	return guestKeyUserBlocked(store, u)
}

// guestKeyOutcome is what the background worker hands the handler.
type guestKeyOutcome struct {
	res  *guestKeyResult
	fail *guestKeyFailure
}

// handleReissueGuestServiceToken handles
// POST /api/v1/admin/guest-service-tokens/:userId/reissue: issue (first key) or
// reissue (new key) a guest's phone-app key and email it to the account's
// email address. The checks run synchronously, so a refusal is an immediate 409; the
// work itself runs in the background under a per-user guard and the handler
// waits for it for up to guestKeyHandlerWait, then answers 202 and the list
// shows the outcome.
func (s *Server) handleReissueGuestServiceToken(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, ok := servermiddleware.CurrentUser(c)
	if !ok || actor == nil {
		httputil.RespondWithForbidden(c, "issuing a guest key needs the admin role")
		return
	}
	userID := c.Param("userId")
	if userID == "" || len(userID) > 64 {
		httputil.RespondWithBadRequest(c, "user id required")
		return
	}
	if _, _, f := s.checkGuestKeyIssue(userID); f != nil {
		httputil.RespondWithError(c, f.Status, f.Msg, f.Code)
		return
	}
	// The guard is taken HERE, so a second click gets a synchronous 409, and is
	// released by the worker, never the handler: after a 202 the worker is
	// still mailing while the record already reads "active" (issuable), and a
	// handler-side release would let a second click revoke the key being mailed.
	// The store's status checks remain the backstop.
	if _, loaded := s.guestTokens.reissueGuard.LoadOrStore(userID, struct{}{}); loaded {
		httputil.RespondWithError(c, http.StatusConflict, "a key is already being issued for this user", guestKeyBusyCode)
		return
	}
	parent := s.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	done := make(chan guestKeyOutcome, 1)
	s.bgWG.Go("guest-service-token-reissue", func() {
		defer s.guestTokens.reissueGuard.Delete(userID)
		res, fail := s.issueGuestKey(parent, userID, actor.ID)
		done <- guestKeyOutcome{res, fail}
	})
	wait := s.guestTokens.reissueWait
	if wait <= 0 {
		wait = guestKeyHandlerWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case o := <-done:
		if o.fail != nil {
			httputil.RespondWithError(c, o.fail.Status, o.fail.Msg, o.fail.Code)
			return
		}
		httputil.RespondWithOK(c, gin.H{
			"reissued": true, "user_id": userID, "token_id": o.res.TokenID,
			"emailed": true, "first_key": o.res.FirstKey,
		})
	case <-timer.C:
		httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{"status": "running"})
	case <-c.Request.Context().Done():
		// The caller went away; the worker finishes on its own.
	}
}

// issueGuestKey does the work for one user: revoke the old key (when there is
// one), reopen or create the record, mint through the shared core, check the
// account is still fine, email the key to the account's address, and audit it.
// The caller holds the per-user guard. The secret lives only in the issue
// returned by the core and in the rendered message; it reaches no log, audit
// entry, record or response.
//
// Shutdown. The whole run can take about 140 s (see guestKeyHandlerWait) and
// shutdown waits only 30 s for bgWG before the store closes, while the revoke,
// mint and reap run on context.WithoutCancel(parent). So parent (the server's
// background context) is checked between the phases: before anything changes,
// again after the old key is revoked and BEFORE the record is reopened (so a
// stop leaves it "revoked", which Reissue can start from, never "pending",
// which looks stuck), and after the mint and before the email (an unsent key
// is revoked, never left "active"). RESIDUAL WINDOW: a Cloudflare call that is
// already in flight when the process dies, or that outlives the 30 s wait, is
// not covered: the store may close under it, so a token can be left that no
// record names. The next Revoke of that guest reaps it by name when the record
// is still pending or revoking (reapOrphanedMint);
// docs/operations/cloudflare-guest-service-tokens.md says so for operators.
func (s *Server) issueGuestKey(parent context.Context, userID, actorID string) (*guestKeyResult, *guestKeyFailure) {
	// Re-run the checks now that the guard is held: the handler's pass was only
	// there to refuse early.
	user, rec, f := s.checkGuestKeyIssue(userID)
	if f != nil {
		return nil, f
	}
	store := s.storeForWiring()
	tokens, _ := database.AsCapability[database.GuestServiceTokenStore](store) // checked above
	res := &guestKeyResult{FirstKey: rec == nil}
	if parent.Err() != nil {
		return nil, guestKeyShutdown("The server is shutting down; nothing was changed. Try again once it is back")
	}

	if rec == nil {
		// A first key: the record accept would have written.
		if err := tokens.CreateGuestServiceToken(database.GuestServiceToken{
			UserID: userID, Name: cfservicetoken.Name(userID), Status: database.GuestTokenPending,
		}); err != nil {
			if errors.Is(err, database.ErrGuestServiceTokenExists) {
				return nil, guestKeyBusy("a key record appeared for this user a moment ago; reload the list and try again")
			}
			inviteAccessLog.Warn("guest key issue: writing the pending record for account %s failed: %v", userID, logger.SanitizeLogValue(err.Error()))
			return nil, guestKeyFailed("the key record could not be written")
		}
	} else {
		res.PreviousTokenID = rec.TokenID
		if f := s.reopenGuestKeyRecord(parent, tokens, userID, actorID, res); f != nil {
			return nil, f
		}
	}

	issue, detail := s.mintGuestTokenCore(parent, store, tokens, user, actorID)
	if !issue.Issued {
		if res.FirstKey {
			return nil, guestKeyFailed("No phone-app key could be made: " + detail)
		}
		return nil, guestKeyFailed("The old key is revoked; no new key exists: " + detail)
	}
	res.TokenID = issue.TokenID

	// Between "active" and the email the account may have been deactivated or
	// the key revoked. A revoked record means whoever revoked it already removed
	// the key; an account that is no longer eligible leaves a live key nobody
	// holds, which is revoked again here.
	//
	// Fail closed: a record we cannot read, or one in any state other than
	// active-with-this-token or already revoked, may still hold a live key that
	// nobody will receive, so it is revoked here before anything is reported.
	cur, curErr := tokens.GetGuestServiceToken(userID)
	switch {
	case curErr == nil && cur != nil && cur.Status == database.GuestTokenActive && cur.TokenID == issue.TokenID:
		// Still ours: go on to the eligibility re-check and the email.
	case curErr == nil && cur != nil && (cur.Status == database.GuestTokenRevoked || cur.Status == database.GuestTokenRevoking):
		return nil, guestKeyBusy("the key was revoked while the new key was being made; nothing was emailed")
	default:
		reason := "the key record changed before the key was emailed"
		if curErr != nil {
			inviteAccessLog.Warn("guest key issue: re-reading the record for account %s failed: %v", userID, logger.SanitizeLogValue(curErr.Error()))
			reason = "the key record could not be re-read before the key was emailed"
		}
		return nil, s.discardUnsentGuestKeyResult(parent, userID, actorID, res, reason)
	}
	fresh, freshErr := store.GetUserByID(userID)
	if freshErr != nil {
		inviteAccessLog.Warn("guest key issue: re-reading account %s failed: %v", userID, logger.SanitizeLogValue(freshErr.Error()))
		return nil, s.discardUnsentGuestKeyResult(parent, userID, actorID, res, "the account could not be re-read before the key was emailed")
	}
	if why := guestKeyUserBlocked(store, fresh); why != "" {
		if err := s.discardUnsentGuestKey(parent, userID, actorID, res, "the account stopped being eligible before the key was emailed: "+why); err != nil {
			return nil, guestKeyFailed(why + "; nothing was emailed, but the new key could not be revoked either, so it may still be active: use Revoke on its row")
		}
		return nil, guestKeyBlocked(why + "; the new key was revoked and nothing was emailed")
	}
	if parent.Err() != nil {
		return nil, s.discardUnsentGuestKeyResult(parent, userID, actorID, res, "the server was shutting down before the key was emailed")
	}

	if sendErr := s.emailGuestKey(parent, fresh, issue, !res.FirstKey); sendErr != "" {
		revokeErr := s.discardUnsentGuestKey(parent, userID, actorID, res, "reissue email could not be sent")
		name := fresh.Username
		if revokeErr != nil {
			return nil, guestKeyFailed("The key for " + name + " could not be emailed (" + sendErr + "), and the new key could not be revoked either, " +
				"so it may still be active: use Revoke on its row")
		}
		return nil, guestKeyFailed("No phone-app key is active for " + name + ": the email could not be sent (" + sendErr + "). Check the mail settings and try again")
	}

	details := "emailed to the account's email address, by admin account " + actorID
	summary := "a guest's phone-app key was reissued and emailed to them"
	if res.FirstKey {
		summary = "a first phone-app key was issued to a guest and emailed to them"
	}
	extra := map[string]any{"first_key": res.FirstKey}
	if res.PreviousTokenID != "" {
		extra["previous_token_id"] = res.PreviousTokenID
	}
	s.recordGuestTokenAuditWith(auditGuestTokenReissued, "info", userID, res.TokenID, details, summary, extra)
	inviteAccessLog.Info("guest key issue: account %s: token %s emailed to the account's address (replaces %q, first key %v)",
		userID, res.TokenID, res.PreviousTokenID, res.FirstKey)
	return res, nil
}

// reopenGuestKeyRecord revokes userID's old key and reopens its record as
// pending, ready for the mint core. The revoke runs first and refuses a record
// that is in flight (beginGuestTokenRevoke). A record that is already revoked
// is simply reopened. The reopen is a status-checked update, so only one request
// ever mints. When the server begins shutting down after the revoke, the record
// is left revoked (not reopened) and the stop is audited as reissue_failed.
func (s *Server) reopenGuestKeyRecord(parent context.Context, tokens database.GuestServiceTokenStore, userID, actorID string, res *guestKeyResult) *guestKeyFailure {
	if err := s.revokeGuestServiceTokenOpts(parent, userID, "reissued by an admin", actorID, true); err != nil {
		switch {
		case errors.Is(err, errGuestTokenBusy):
			return guestKeyBusy(guestKeyStuckHint)
		case errors.Is(err, errGuestTokenNone):
			return guestKeyBusy("the key record changed while the request was running; reload the list and try again")
		}
		// The detail goes to the log only: err can be a store error, which is
		// not admin wording. A Cloudflare failure's reason is already on the
		// key's row (the record is revoke_failed with the detail).
		inviteAccessLog.Warn("guest key issue: revoking the old key of account %s failed: %v", userID, logger.SanitizeLogValue(err.Error()))
		return guestKeyFailed("The old key could not be confirmed deleted, so no new key was made; it may still work. See the key's row for the reason, then try again")
	}
	if parent.Err() != nil {
		// Second shutdown check: the old key is gone, the record reads revoked,
		// and nothing new has been started.
		extra := map[string]any{"first_key": false}
		if res.PreviousTokenID != "" {
			extra["previous_token_id"] = res.PreviousTokenID
		}
		s.recordGuestTokenAuditWith(auditGuestTokenReissueFailed, "warn", userID, "",
			"the server was shutting down after the old key was revoked, so no new key was made; the record is left revoked, by admin account "+actorID,
			"a guest's key reissue stopped: the server was shutting down", extra)
		return guestKeyShutdown("The server is shutting down. The old key is revoked and no new key was made; use Reissue again once it is back")
	}
	now := time.Now().UTC()
	if _, err := tokens.UpdateGuestServiceToken(userID, func(g *database.GuestServiceToken) error {
		if g.Status != database.GuestTokenRevoked {
			return errGuestTokenBusy
		}
		g.Status, g.TokenID, g.SecretHash, g.PolicyAdded, g.Detail = database.GuestTokenPending, "", "", false, ""
		g.RevokedAt, g.RevokeReason = nil, ""
		g.CreatedAt, g.ReissuedAt = now, &now
		return nil
	}); err != nil {
		if errors.Is(err, errGuestTokenBusy) || errors.Is(err, database.ErrGuestServiceTokenNotFound) {
			return guestKeyBusy("the key record changed while the request was running; reload the list and try again")
		}
		inviteAccessLog.Warn("guest key issue: reopening the record of account %s failed: %v", userID, logger.SanitizeLogValue(err.Error()))
		return guestKeyFailed("The old key is revoked, but its record could not be reopened, so no new key exists: use Reissue to try again")
	}
	return nil
}

// emailGuestKey sends the key to u's email address and returns "" on
// success, or fixed wording for why not. It never returns, logs or audits the
// message body.
func (s *Server) emailGuestKey(parent context.Context, u *database.User, issue *guestTokenIssue, reissue bool) string {
	snap := config.Snapshot()
	base := snap.InviteBaseURL(s.externalURL)
	sender := s.inviteMailSender()
	if why := handlers.EmailSendBlocked(sender, base); why != "" {
		inviteAccessLog.Warn("guest key email: not sent for account %s: %s", u.ID, why)
		return why
	}
	msg, err := handlers.RenderPhoneKeyEmail(handlers.PhoneKeyEmail{
		Username: u.Username, ServerURL: base,
		ClientID: issue.ClientID, ClientSecret: issue.ClientSecret, Reissue: reissue,
	})
	if err != nil {
		inviteAccessLog.Error("guest key email: building the message failed for account %s: %v", u.ID, logger.SanitizeLogValue(err.Error()))
		return "the email could not be built"
	}
	msg.To = u.Email
	ctx, cancel := context.WithTimeout(parent, guestKeyMailTimeout)
	defer cancel()
	if err := sender.Send(ctx, msg); err != nil {
		inviteAccessLog.Warn("guest key email: send failed for account %s: %v", u.ID, logger.SanitizeLogValue(err.Error()))
		return "the mail server did not accept it"
	}
	return ""
}

// discardUnsentGuestKeyResult revokes a new key that will not be emailed and
// returns the admin-facing failure, which says whether the revoke worked.
func (s *Server) discardUnsentGuestKeyResult(parent context.Context, userID, actorID string, res *guestKeyResult, reason string) *guestKeyFailure {
	if err := s.discardUnsentGuestKey(parent, userID, actorID, res, reason); err != nil {
		return guestKeyFailed(reason + "; nothing was emailed, and the new key could not be revoked, so it may still be active: use Revoke on its row")
	}
	return guestKeyFailed(reason + "; the new key was revoked and nothing was emailed")
}

// discardUnsentGuestKey revokes a key that was made but never reached the guest:
// its secret is held nowhere (only a hash is stored), so it is a live credential
// nobody has. It goes through the normal revoke path (record "revoked"), and the
// reissue_failed audit entry names both ids. It returns the revoke's error: when
// the revoke fails the record is "revoke_failed" and the admin retries it.
func (s *Server) discardUnsentGuestKey(parent context.Context, userID, actorID string, res *guestKeyResult, reason string) error {
	err := s.revokeGuestServiceToken(parent, userID, reason, actorID)
	detail := reason + ", by admin account " + actorID
	if err != nil {
		detail += "; the new key could not be revoked either: " + err.Error()
	}
	extra := map[string]any{"first_key": res.FirstKey}
	if res.PreviousTokenID != "" {
		extra["previous_token_id"] = res.PreviousTokenID
	}
	s.recordGuestTokenAuditWith(auditGuestTokenReissueFailed, "warn", userID, res.TokenID, detail,
		"a guest's new phone-app key could not be delivered", extra)
	return err
}
