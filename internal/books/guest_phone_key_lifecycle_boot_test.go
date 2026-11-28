// file: internal/books/guest_phone_key_lifecycle_boot_test.go
// version: 0.1.0
// guid: 8c41e0a7-2f65-4b19-9d3a-5e07b6c8d124
// last-edited: 2026-11-22

package books

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// The guest phone-app key against core's account lifecycle (F4-08 B10s S2),
// through books' real registration.
//
// Books registers ONE auth.Lifecycle (AccountLifecycle, B10r), and its reactions
// to core's events are the entry points B10s supplies (Server.OfferGuestKeyClaim,
// Server.RevokeGuestKeyForAccount). These boots run the production composition
// with no wrapper and no test registrant: core's accept, disable and delete,
// through a real kernel, to books' store and Cloudflare.
//
// "The event really reached the hook" is observed from what books itself leaves
// behind, never from a test hook: an accept leaves the guest-policy outcome row
// of its invitation (every accepted invitation with an address gets one, skipped
// or not), a disable or delete leaves the closed key record, and the assertions
// that expect a refusal sit beside one that an absent event would fail.

// bootLifecycleApp is bootGuestKeyApp: the real books registration.
func bootLifecycleApp(t *testing.T) (*runningApp, *cloudflareFake) {
	t.Helper()
	return bootGuestKeyApp(t)
}

// acceptReachedTheHook fails the test unless books' lifecycle saw the accept of
// inviteID: the guest-policy half writes an outcome row for it whatever it decided.
func acceptReachedTheHook(t *testing.T, r *runningApp, inviteID string) *database.GuestPolicyOutcome {
	t.Helper()
	gp, ok := database.AsCapability[database.GuestPolicyStore](r.store)
	require.True(t, ok)
	var out *database.GuestPolicyOutcome
	require.Eventually(t, func() bool {
		var err error
		out, err = gp.GetGuestPolicyOutcome(inviteID)
		return err == nil && out != nil
	}, 10*time.Second, 20*time.Millisecond, "core's accept never reached books' lifecycle hook")
	return out
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
	r, cf := bootLifecycleApp(t)
	admin := bootstrapAdmin(t, r)
	guest, guestID, inviteID := inviteGuest(t, r, admin, "robin", "robin@wayne.example")

	// Not vacuous: the hook did see the accept, and decided it was unbound.
	out := acceptReachedTheHook(t, r, inviteID)
	assert.Equal(t, database.InviteGuestPolicySkipped, out.Status, "nothing mailed this invite")

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
	r, cf := bootLifecycleApp(t)
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

	assert.NotContains(t, r.newLog.String(), claimSecret)
	assertSecretNotStored(t, r, claimSecret)
}

// TestIdentityBoot_DeletingTheGuestRevokesTheKey: core's delete (UserDeleted) is
// the same revoke: the account is gone, its Cloudflare token must not outlive it.
func TestIdentityBoot_DeletingTheGuestRevokesTheKey(t *testing.T) {
	r, cf := bootLifecycleApp(t)
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
}

// TestIdentityBoot_SSOGrantedAcceptIsClaimableAndReissueEligible: an invite an
// admin e-mailed (books' invite route, a real mail server, core's flag set after
// the send) binds its address verified, so the InviteAccepted hook offers the
// one-time key by itself (nothing calls OfferGuestKeyClaim here but books'
// registered lifecycle), the guest claims it, and the guest is eligible for an
// admin reissue.
func TestIdentityBoot_SSOGrantedAcceptIsClaimableAndReissueEligible(t *testing.T) {
	r, cf, smtp := bootGuestKeyMailed(t)
	admin := bootstrapAdmin(t, r)
	guest, guestID, inviteID := mailedGuest(t, r, admin, smtp, "robin", "robin@wayne.example")

	out := acceptReachedTheHook(t, r, inviteID)
	assert.NotEqual(t, database.InviteGuestPolicySkipped, out.Status, "the invitation was mailed: the hook must not skip it")
	w := claimKey(t, r, guest)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), claimSecret)
	assert.Equal(t, 1, cf.minted())

	// Eligible for an admin reissue: the guest holds the address verified.
	rec := doJSON(t, r.app.Handler(), http.MethodPost, "/api/v1/admin/guest-service-tokens/"+guestID+"/reissue", nil, admin)
	assert.Contains(t, []int{http.StatusOK, http.StatusAccepted}, rec.Code, rec.Body.String())
}

// TestIdentityBoot_UnboundAcceptRefusesReissue: the unbound accept's address is
// unverified, so the admin reissue refuses. The admin reads the guest through core's
// actor-checked AdminUser/AdminUsers (Identity.Admin) on its own request, so the
// refusal is the verified-holder rule's, not a failed read.
func TestIdentityBoot_UnboundAcceptRefusesReissue(t *testing.T) {
	r, _ := bootLifecycleApp(t)
	admin := bootstrapAdmin(t, r)
	_, guestID, _ := inviteGuest(t, r, admin, "robin", "robin@wayne.example")
	rec := doJSON(t, r.app.Handler(), http.MethodPost, "/api/v1/admin/guest-service-tokens/"+guestID+"/reissue", nil, admin)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "GUEST_TOKEN_REISSUE_BLOCKED")
	// The reason is the verified-holder rule, not a read that failed ("could
	// not be read" / "could not be checked").
	assert.Contains(t, rec.Body.String(), "no trusted email address")
}
