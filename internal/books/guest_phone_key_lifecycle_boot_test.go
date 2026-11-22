// file: internal/books/guest_phone_key_lifecycle_boot_test.go
// version: 0.1.0
// guid: 8c41e0a7-2f65-4b19-9d3a-5e07b6c8d124
// last-edited: 2026-11-22

package books

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// The guest phone-app key against core's account lifecycle (F4-08 B10s S2).
//
// B10r-p2 owns books' ONE auth.Lifecycle and its single RegisterLifecycle call.
// This file does not stand in for it in production: guestKeyLifecycle is a
// test-only registration that maps core's events onto the two entry points
// B10s supplies (Server.OfferGuestKeyClaim, Server.RevokeGuestKeyForAccount) the
// way the books Lifecycle will, so the whole path (core's accept and core's
// disable and delete, through a real kernel, to books' store and Cloudflare)
// is exercised before that wiring lands.
//
// When B10r-p2 registers the real Lifecycle (and sets Manifest.AuthLifecycle),
// guestKeyLifecycle's registration is refused with ErrLifecycleRegistered and
// it stands down (ours stays false): the same tests then run against the real
// wiring, which is what B10g's "every lifecycle event has a registered
// subscriber" check relies on.

// guestKeyLifecycle wraps the books module for a boot that observes account
// events. It declares Manifest.AuthLifecycle (so the kernel hands the module
// Deps.AuthLifecycle) and registers after the module's own Register, so a real
// registration, when there is one, comes first.
type guestKeyLifecycle struct {
	module.Module
	books *booksModule
	state *lifecycleSeen
}

// lifecycleSeen is what the test hook saw, for assertions that the event
// really reached the hook (a missing event must not read as a passing refusal).
type lifecycleSeen struct {
	mu       sync.Mutex
	ours     bool
	accepted []coreauth.InviteAccepted
	status   []coreauth.StatusChanged
	deleted  []coreauth.UserDeleted
}

func (s *lifecycleSeen) snapshot() (ours bool, accepted []coreauth.InviteAccepted, status []coreauth.StatusChanged, deleted []coreauth.UserDeleted) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ours,
		append([]coreauth.InviteAccepted(nil), s.accepted...),
		append([]coreauth.StatusChanged(nil), s.status...),
		append([]coreauth.UserDeleted(nil), s.deleted...)
}

func (g guestKeyLifecycle) Manifest() module.Manifest {
	m := g.Module.Manifest()
	m.AuthLifecycle = true
	return m
}

func (g guestKeyLifecycle) Register(r *module.Registrar) error {
	if err := g.Module.Register(r); err != nil {
		return err
	}
	register := r.Deps().AuthLifecycle
	if register == nil {
		return errors.New("Deps.AuthLifecycle is nil: the boot is not an identity boot, or the manifest did not declare it")
	}
	srv := func() *server.Server { return g.books.srv.Load() }
	err := register(coreauth.Lifecycle{
		InviteAccepted: func(_ context.Context, e coreauth.InviteAccepted) {
			g.state.mu.Lock()
			g.state.accepted = append(g.state.accepted, e)
			g.state.mu.Unlock()
			srv().OfferGuestKeyClaim(server.GuestKeyOffer{
				UserID:            string(e.UserID),
				InviteID:          e.InviteID,
				BindEmailOnAccept: e.BindEmailOnAccept,
				CreatorHeldAdmin:  e.CreatorHeldAdmin,
			})
		},
		StatusChanged: func(_ context.Context, e coreauth.StatusChanged) {
			g.state.mu.Lock()
			g.state.status = append(g.state.status, e)
			g.state.mu.Unlock()
			if e.To == coreauth.StatusDisabled {
				srv().RevokeGuestKeyForAccount(string(e.UserID), "user deactivated")
			}
		},
		UserDeleted: func(_ context.Context, e coreauth.UserDeleted) {
			g.state.mu.Lock()
			g.state.deleted = append(g.state.deleted, e)
			g.state.mu.Unlock()
			srv().RevokeGuestKeyForAccount(string(e.UserID), "user deleted")
		},
	})
	switch {
	case err == nil:
		g.state.mu.Lock()
		g.state.ours = true
		g.state.mu.Unlock()
		return nil
	case errors.Is(err, coreauth.ErrLifecycleRegistered):
		// The module registered its own Lifecycle (B10r-p2): that one is in
		// force and these tests prove it.
		return nil
	default:
		return err
	}
}

// bootLifecycleApp is bootGuestKeyApp with guestKeyLifecycle wrapped around the
// books module.
func bootLifecycleApp(t *testing.T) (*runningApp, *cloudflareFake, *lifecycleSeen) {
	t.Helper()
	state := &lifecycleSeen{}
	r, cf := bootGuestKeyApp(t, func(m module.Module) module.Module {
		books, ok := m.(*booksModule)
		require.True(t, ok, "the module to wrap is %T, not the books module", m)
		return guestKeyLifecycle{Module: m, books: books, state: state}
	})
	return r, cf, state
}

func loginAs(t *testing.T, r *runningApp, username, password string) *http.Cookie {
	t.Helper()
	rec := doJSON(t, r.app.Handler(), http.MethodPost, "/api/core/v1/auth/login",
		map[string]string{"username": username, "password": password}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return sessionCookie(t, rec)
}

func adminPost(t *testing.T, r *runningApp, admin *http.Cookie, path string) {
	t.Helper()
	rec := doJSON(t, r.app.Handler(), http.MethodPost, path, nil, admin)
	require.Less(t, rec.Code, 300, "%s: %s", path, rec.Body.String())
}

func tokenRow(t *testing.T, r *runningApp, userID string) *database.GuestServiceToken {
	t.Helper()
	tokens, ok := database.AsCapability[database.GuestServiceTokenStore](r.store)
	require.True(t, ok)
	row, err := tokens.GetGuestServiceToken(userID)
	require.NoError(t, err)
	return row
}

// waitRevoked waits for the background revoke the lifecycle hook starts (the
// hook must return quickly, so it never blocks on Cloudflare).
func waitRevoked(t *testing.T, r *runningApp, userID string) *database.GuestServiceToken {
	t.Helper()
	require.Eventually(t, func() bool {
		row := tokenRow(t, r, userID)
		return row != nil && row.Status == database.GuestTokenRevoked
	}, 10*time.Second, 20*time.Millisecond, "the guest's key was not revoked")
	return tokenRow(t, r, userID)
}

// TestIdentityBoot_UnboundAcceptOffersNoPhoneKey: an invite nobody e-mailed (the
// address stays unverified) reaches the InviteAccepted hook with
// BindEmailOnAccept false, so no entitlement is offered, the claim is the
// uniform refusal and no key is minted.
func TestIdentityBoot_UnboundAcceptOffersNoPhoneKey(t *testing.T) {
	r, cf, seen := bootLifecycleApp(t)
	guest, guestID, _ := inviteAndAccept(t, r, "robin", "robin@wayne.example")

	ours, accepted, _, _ := seen.snapshot()
	if ours {
		// Not vacuous: the hook did see the accept, and saw it unbound.
		require.Len(t, accepted, 1)
		assert.Equal(t, guestID, string(accepted[0].UserID))
		assert.False(t, accepted[0].BindEmailOnAccept, "nothing mailed this invite")
	}

	refused := claimKey(t, r, guest)
	require.Equal(t, http.StatusNotFound, refused.Code, refused.Body.String())
	assert.Contains(t, refused.Body.String(), "PHONE_KEY_CLAIM_REFUSED")
	assert.Contains(t, refused.Header().Get("Cache-Control"), "no-store")
	assert.Zero(t, cf.minted(), "an unbound accept minted a key")
	assert.Nil(t, tokenRow(t, r, guestID), "an unbound accept left a key record")
}

// TestIdentityBoot_DisablingTheGuestRevokesTheKey: core's disable (StatusChanged
// to disabled) revokes the guest's token at Cloudflare and closes its record, and
// an unclaimed entitlement does not survive a disable and re-enable.
func TestIdentityBoot_DisablingTheGuestRevokesTheKey(t *testing.T) {
	r, cf, seen := bootLifecycleApp(t)
	admin := bootstrapAdmin(t, r)
	srv := r.mod.srv.Load()
	require.NotNil(t, srv)

	// A claimed key: disabled, the token dies at Cloudflare and the record closes.
	claimed, claimedID, _ := inviteGuest(t, r, admin, "robin", "robin@wayne.example")
	require.True(t, srv.OfferGuestKeyClaim(server.GuestKeyOffer{UserID: claimedID, InviteID: "inv-batcave-1", BindEmailOnAccept: true, CreatorHeldAdmin: true}))
	w := claimKey(t, r, claimed)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, cf.minted())
	require.Equal(t, database.GuestTokenActive, tokenRow(t, r, claimedID).Status)

	adminPost(t, r, admin, "/api/core/v1/users/"+claimedID+"/disable")
	row := waitRevoked(t, r, claimedID)
	assert.Equal(t, "user deactivated", row.RevokeReason)
	assert.Equal(t, 1, cf.revoked(), "the service token was not deleted at Cloudflare")

	// An unclaimed entitlement: a disable drops it, so a re-enable does not
	// bring the one-time key back.
	_, unclaimedID, _ := inviteGuest(t, r, admin, "alfred", "alfred@wayne.example")
	require.True(t, srv.OfferGuestKeyClaim(server.GuestKeyOffer{UserID: unclaimedID, InviteID: "inv-batcave-2", BindEmailOnAccept: true, CreatorHeldAdmin: true}))
	adminPost(t, r, admin, "/api/core/v1/users/"+unclaimedID+"/disable")
	adminPost(t, r, admin, "/api/core/v1/users/"+unclaimedID+"/enable")
	again := loginAs(t, r, "alfred", claimGuestPass)
	w = claimKey(t, r, again)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), claimSecret)
	assert.Equal(t, 1, cf.minted(), "a disabled account's entitlement survived and minted a key")

	if ours, _, status, _ := seen.snapshot(); ours {
		var disabled int
		for _, e := range status {
			if e.To == coreauth.StatusDisabled {
				disabled++
			}
		}
		assert.Equal(t, 2, disabled, "core did not report both disables to the hook")
	}
	assert.NotContains(t, r.newLog.String(), claimSecret)
	assertSecretNotStored(t, r, claimSecret)
}

// TestIdentityBoot_DeletingTheGuestRevokesTheKey: core's delete (UserDeleted) is
// the same revoke: the account is gone, its Cloudflare token must not outlive it.
func TestIdentityBoot_DeletingTheGuestRevokesTheKey(t *testing.T) {
	r, cf, seen := bootLifecycleApp(t)
	admin := bootstrapAdmin(t, r)
	guest, guestID, _ := inviteGuest(t, r, admin, "robin", "robin@wayne.example")
	require.True(t, r.mod.srv.Load().OfferGuestKeyClaim(server.GuestKeyOffer{UserID: guestID, InviteID: "inv-batcave-1", BindEmailOnAccept: true, CreatorHeldAdmin: true}))
	require.Equal(t, http.StatusOK, claimKey(t, r, guest).Code)
	require.Equal(t, 1, cf.minted())

	rec := doJSON(t, r.app.Handler(), http.MethodDelete, "/api/core/v1/users/"+guestID, nil, admin)
	require.Less(t, rec.Code, 300, rec.Body.String())
	row := waitRevoked(t, r, guestID)
	assert.Equal(t, "user deleted", row.RevokeReason)
	assert.Equal(t, 1, cf.revoked())

	if ours, _, _, deleted := seen.snapshot(); ours {
		require.Len(t, deleted, 1)
		assert.Equal(t, guestID, string(deleted[0].UserID))
	}
}

// The two things the SSO-granted cases need that production cannot supply yet.
// They are variables, not constants, so each skip below is a runtime decision:
// flipping one to true when its dependency lands runs the cases it gates.
var (
	// invitesCanBeMailed: books' invite mailer can flag an invite e-mailed
	// (core's AccountAdmin.MarkInviteEmailDelivered). Its only writer is
	// B10t-books' mailer, and core hands a module no AccountAdmin until it lands.
	invitesCanBeMailed = false
	// accountsReaderWired: legacyauth.Identity.Accounts (B10t-books) is supplied,
	// so the admin reissue can read other accounts under core identity.
	accountsReaderWired = false
)

func skipUntilInvitesCanBeMailed(t *testing.T) {
	t.Helper()
	if !invitesCanBeMailed {
		t.Skip("B10t-books: nothing can flag an invite e-mailed (Admin.MarkInviteEmailDelivered has no caller) " +
			"and Identity.Accounts (core's AdminUser/AdminUsers) is not supplied, so an SSO-granted accept and the " +
			"guest's reissue eligibility cannot be driven end to end until B10t-books lands; B10r-p2 registers the " +
			"books Lifecycle these tests then prove")
	}
}

func skipUntilAccountsReaderWired(t *testing.T) {
	t.Helper()
	if !accountsReaderWired {
		t.Skip("B10t-books: Identity.Accounts (core's AdminUser/AdminUsers) is not supplied, so every admin reissue is " +
			"refused today and the refusal would pass without exercising the verified-holder rule; " +
			"B10r-p2 registers the books Lifecycle that makes the unbound case meaningful")
	}
}

// TestIdentityBoot_SSOGrantedAcceptIsClaimableAndReissueEligible: an invite an
// admin e-mailed binds its address verified, so the InviteAccepted hook offers
// the one-time key (the guest claims it) and the guest is eligible for an
// admin reissue.
func TestIdentityBoot_SSOGrantedAcceptIsClaimableAndReissueEligible(t *testing.T) {
	skipUntilInvitesCanBeMailed(t)
	r, cf, seen := bootLifecycleApp(t)
	admin := bootstrapAdmin(t, r)
	// The guest accepts a MAILED invite: flagging it before the accept is the
	// step production cannot do yet (see invitesCanBeMailed). Then:
	//   markInviteEmailDelivered(ctx as admin, inviteID)
	guest, guestID, _ := inviteGuest(t, r, admin, "robin", "robin@wayne.example")

	if ours, accepted, _, _ := seen.snapshot(); ours {
		require.Len(t, accepted, 1)
		assert.True(t, accepted[0].BindEmailOnAccept && accepted[0].CreatorHeldAdmin)
	}
	w := claimKey(t, r, guest)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), claimSecret)
	assert.Equal(t, 1, cf.minted())

	// Eligible for an admin reissue: the guest holds the address verified.
	rec := doJSON(t, r.app.Handler(), http.MethodPost, "/api/v1/admin/guest-service-tokens/"+guestID+"/reissue", nil, admin)
	assert.Contains(t, []int{http.StatusOK, http.StatusAccepted}, rec.Code, rec.Body.String())
}

// TestIdentityBoot_UnboundAcceptRefusesReissue: the unbound accept's address is
// unverified, so the admin reissue refuses; it reads other accounts through core's
// AdminUser/AdminUsers (Identity.Accounts, B10t-books), which is absent today, so every reissue is
// refused and the refusal proves nothing about the verified-holder rule.
func TestIdentityBoot_UnboundAcceptRefusesReissue(t *testing.T) {
	skipUntilAccountsReaderWired(t)
	r, _, _ := bootLifecycleApp(t)
	admin := bootstrapAdmin(t, r)
	_, guestID, _ := inviteGuest(t, r, admin, "robin", "robin@wayne.example")
	rec := doJSON(t, r.app.Handler(), http.MethodPost, "/api/v1/admin/guest-service-tokens/"+guestID+"/reissue", nil, admin)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "GUEST_TOKEN_REISSUE_BLOCKED")
}
