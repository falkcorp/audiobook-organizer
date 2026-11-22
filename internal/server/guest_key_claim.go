// file: internal/server/guest_key_claim.go
// version: 0.1.0
// guid: c3a96e51-0d78-4b24-8f1c-27e5b9d4a063
// last-edited: 2026-11-22

package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfservicetoken"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// The guest's phone-app key under core identity (F4-08 B10s).
//
// At HEAD the accept response carried the key: books consumed the invite and
// minted in the same request (mintGuestServiceToken). Under core identity core
// consumes the invite and its accept response cannot carry a books secret, so
// the guest's page makes a second call, POST /api/v1/auth/phone-key/claim, with
// the session the accept just gave it. This file is that call.
//
// What the claim is bound to. A claim consumes an ENTITLEMENT: (user id, invite
// id, expiry), written only by OfferGuestKeyClaim, which the InviteAccepted hook
// calls once core has committed the accept. The claim looks the entitlement up
// by the CALLER's own user id (the session's user), so a different user finds
// nothing and the rightful user's entitlement stays untouched. Nothing else
// creates one, so a temp-login session, a link-only invitee or an account an
// admin created never mints a key here: HEAD minted only for an invitation that
// was emailed to the address by an active admin (SSOGrant), and the hook
// payload's BindEmailOnAccept && CreatorHeldAdmin is exactly that fact, derived
// once, in OfferGuestKeyClaim.
//
// Single use, short expiry. The entitlement is taken (removed) under the
// store's lock BEFORE Cloudflare is called, so two concurrent claims mint once,
// and a failed mint stays consumed ("ask the person who invited you", as at
// HEAD, where the accept had already succeeded). It expires guestKeyClaimWindow
// after the accept; HEAD had no window (the response was the only copy), so the
// window is a new bound, 15 minutes, chosen by the coordinator.
//
// Where the mint runs: lazily, at claim. The mint itself is mintGuestTokenCore,
// unchanged: pending -> minted -> active, the revoke race rules and the fail
// closed ordering of guest_service_tokens.go all hold, because the pending
// record is written when the claim runs and the deactivation hook locks the
// account first and revokes second, exactly as before. The secret goes from
// Cloudflare to the response and is held nowhere (there is nothing to seal).
//
// What this costs, stated plainly:
//   - the key (and the opt-in e-mail copy) exist only if the guest claims, so a
//     guest who accepts and never reaches the page gets no key and no e-mail;
//   - the entitlement lives in memory (it carries ids only, no secret), so a
//     restart inside the window is an "ask the admin to reissue" outcome, as
//     when HEAD's mint failed. An admin Reissue works without an entitlement.
//
// No claim exists on a deployment whose hook is not wired: production refuses
// every claim until the hook calls OfferGuestKeyClaim.

// guestKeyClaimWindow is how long after the accept the guest may claim.
const guestKeyClaimWindow = 15 * time.Minute

// guestKeyClaimMax bounds the entitlements held at once. The table holds one
// small row per accepted-and-unclaimed invitation; the bound is a backstop
// against a flood of accepts. Past it new offers are refused (fail closed: the
// guest asks the admin to reissue), never an old entitlement evicted.
const guestKeyClaimMax = 4096

const guestKeyClaimRefusedMsg = "no phone-app key is waiting to be claimed for this account"

// Audit entry types for the claim. Like the rest, none carries a secret, a
// client id or an address.
const (
	// auditGuestTokenClaimed: the entitlement was consumed (the mint's own
	// minted / mint_failed entry follows from mintGuestTokenCore).
	auditGuestTokenClaimed = activity.TypeGuestServiceTokenPrefix + "claimed"
	// auditGuestTokenClaimExpired: a claim arrived after the entitlement's
	// window. Plain "nothing waiting" claims are not audited (the page makes the
	// call after every accept).
	auditGuestTokenClaimExpired = activity.TypeGuestServiceTokenPrefix + "claim_expired"
)

// GuestKeyOffer is what the InviteAccepted hook hands OfferGuestKeyClaim: ids
// and the two facts the hook payload carries. It holds no credential.
type GuestKeyOffer struct {
	UserID   string
	InviteID string
	// BindEmailOnAccept and CreatorHeldAdmin are the hook payload's flags:
	// the invitation was emailed to the address (core's flag, set only after a
	// successful send by an admin) and its creator held core:admin.
	BindEmailOnAccept bool
	CreatorHeldAdmin  bool
}

// guestKeyClaim is one entitlement.
type guestKeyClaim struct {
	inviteID string
	expires  time.Time
}

// claimRefusal is why take found nothing; "" means it found one.
type claimRefusal string

const (
	claimNone    claimRefusal = "none"
	claimExpired claimRefusal = "expired"
)

// guestKeyClaims is the entitlement table. The zero value is ready.
type guestKeyClaims struct {
	mu     sync.Mutex
	now    func() time.Time // tests; nil means time.Now
	byUser map[string]guestKeyClaim
}

func (g *guestKeyClaims) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// offer records an entitlement for userID, replacing none (one per user: an
// account is created by exactly one invite). It reports false when the table
// is full.
func (g *guestKeyClaims) offer(userID, inviteID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	for id, c := range g.byUser {
		if !now.Before(c.expires) {
			delete(g.byUser, id)
		}
	}
	if len(g.byUser) >= guestKeyClaimMax {
		return false
	}
	if g.byUser == nil {
		g.byUser = map[string]guestKeyClaim{}
	}
	g.byUser[userID] = guestKeyClaim{inviteID: inviteID, expires: now.Add(guestKeyClaimWindow)}
	return true
}

// take removes and returns userID's entitlement. An expired one is removed too
// and answered claimExpired. The removal is the consumption: it happens under
// the lock, before anything is minted.
func (g *guestKeyClaims) take(userID string) (guestKeyClaim, claimRefusal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.byUser[userID]
	if !ok {
		return guestKeyClaim{}, claimNone
	}
	delete(g.byUser, userID)
	if !g.clock().Before(c.expires) {
		return guestKeyClaim{}, claimExpired
	}
	return c, ""
}

// forget drops userID's entitlement (a deactivation).
func (g *guestKeyClaims) forget(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.byUser, userID)
}

// OfferGuestKeyClaim is the trigger the InviteAccepted hook calls after core's
// accept has committed. It writes the entitlement only when the feature is on
// and the invitation earned a phone-app key (BindEmailOnAccept && CreatorHeldAdmin,
// HEAD's SSOGrant); it never calls Cloudflare and never fails the accept. It
// reports whether an entitlement now exists.
func (s *Server) OfferGuestKeyClaim(o GuestKeyOffer) bool {
	if !config.Snapshot().CFGuestServiceTokens {
		return false
	}
	if o.UserID == "" || o.InviteID == "" || !o.BindEmailOnAccept || !o.CreatorHeldAdmin {
		return false
	}
	if !s.guestKeyClaims.offer(o.UserID, o.InviteID) {
		inviteAccessLog.Warn("guest key claim: too many unclaimed entitlements, none recorded for account %s; ask the admin to reissue", o.UserID)
		return false
	}
	return true
}

// refuseGuestKeyClaim is the one answer to every refused claim (no entitlement,
// a second claim, another user's, an expired one): the same status, code and
// text, so the route tells a caller nothing about whose entitlement exists.
func refuseGuestKeyClaim(c *gin.Context) {
	httputil.RespondWithError(c, http.StatusNotFound, guestKeyClaimRefusedMsg, "PHONE_KEY_CLAIM_REFUSED")
}

// handleClaimGuestKey handles POST /api/v1/auth/phone-key/claim.
//
// It is registered through credRoute (an API key, an ABS token or an unknown
// method is refused before this runs; a delegated session, which is what an
// accept gives, is allowed) and carries the manifest's fRated class: the /api
// rate limiter that guards the other credential routes (POST /auth/temp-tokens,
// POST /auth/api-keys, PUT /auth/me/password), core's stage 10 RateDefault
// bucket under identity and gin's /api limiter otherwise. The route has no
// guessable input (the entitlement is the caller's own), so the limiter only
// bounds hammering. Every answer, refusals included, is no-store.
func (s *Server) handleClaimGuestKey(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, ok := servermiddleware.CurrentUser(c)
	if !ok || actor == nil {
		refuseGuestKeyClaim(c)
		return
	}
	if !config.Snapshot().CFGuestServiceTokens {
		refuseGuestKeyClaim(c)
		return
	}
	ctx := c.Request.Context()
	claim, why := s.guestKeyClaims.take(actor.ID)
	if why != "" {
		if why == claimExpired {
			s.recordGuestTokenAudit(auditGuestTokenClaimExpired, "info", actor.ID, "", "", "a phone-app key claim arrived after its window")
		}
		refuseGuestKeyClaim(c)
		return
	}

	issue := s.claimGuestKey(ctx, actor)
	s.recordGuestTokenAuditWith(auditGuestTokenClaimed, "info", actor.ID, issue.TokenID, "", "a guest claimed the phone-app key entitlement of their accepted invitation",
		map[string]any{"invite_id": claim.inviteID, "issued": issue.Issued})
	if issue.Issued {
		s.emailGuestToken(actor, issue)
	}
	httputil.RespondWithOK(c, gin.H{"phone_app": issue.response()})
}

// claimGuestKey writes the pending record and mints through the shared core,
// as accept did. It never returns nil: a failure is the "unavailable" issue.
func (s *Server) claimGuestKey(ctx context.Context, user *database.User) *guestTokenIssue {
	unavailable := &guestTokenIssue{Message: guestTokenUnavailableMsg}
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](s.storeForWiring())
	if !ok {
		inviteAccessLog.Warn("guest service token: the store cannot hold token records, none minted")
		return unavailable
	}
	if err := tokens.CreateGuestServiceToken(database.GuestServiceToken{
		UserID: user.ID, Name: cfservicetoken.Name(user.ID), Status: database.GuestTokenPending,
	}); err != nil {
		inviteAccessLog.Warn("guest service token: writing the pending record failed, none minted: %v", logger.SanitizeLogValue(err.Error()))
		return unavailable
	}
	issue, _ := s.mintGuestTokenCore(ctx, s.guestUsers(), tokens, user, "")
	return issue
}
