// file: internal/server/account_lifecycle_test.go
// version: 0.1.0
// guid: 7d3a0c5e-92b4-4e16-a8f7-0b61c4d9e285
// last-edited: 2026-11-22

package server

import (
	"context"
	"iter"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/stretchr/testify/require"
)

// The two concerns behind books' one Lifecycle (AccountLifecycle): the guest
// policy and the guest phone-app key. These are the per-event cells; the full-boot
// tests in internal/books run the same events through real core.

func (h guestHook) seedClaim(t *testing.T) {
	t.Helper()
	require.True(t, h.srv.guestKeyClaims.offer(gUserID, "inv-1"))
}

// claimSurvives reports whether the entitlement is still there (it consumes it).
func (h guestHook) claimSurvives() bool {
	_, why := h.srv.guestKeyClaims.take(gUserID)
	return why == ""
}

func fireEvent(name string, l coreauth.Lifecycle, ctx context.Context) {
	switch name {
	case "disabled":
		l.StatusChanged(ctx, coreauth.StatusChanged{UserID: gUserID, Email: gSelina, From: coreauth.StatusActive, To: coreauth.StatusDisabled})
	case "re-enabled":
		l.StatusChanged(ctx, coreauth.StatusChanged{UserID: gUserID, Email: gSelina, From: coreauth.StatusDisabled, To: coreauth.StatusActive})
	case "email changed":
		l.EmailChanged(ctx, coreauth.EmailChanged{UserID: gUserID, From: gSelina, To: "oswald.cobblepot@example.test"})
	case "deleted":
		l.UserDeleted(ctx, coreauth.UserDeleted{UserID: gUserID, Email: gSelina})
	case "merged away":
		l.UserMergedAway(ctx, coreauth.UserMergedAway{UserID: gUserID, Email: gSelina, MergedInto: "u-other"})
	}
}

// Each event runs exactly the concerns HEAD ran at the matching call site (and
// the two events HEAD ignored run both, which only takes access away).
func TestAccountLifecycle_EachEventRunsItsConcerns(t *testing.T) {
	cases := []struct {
		event           string
		wantPolicyEnds  bool
		wantKeyForgot   bool
		keyKnownDiffers string
	}{
		{"disabled", true, true, ""},
		{"re-enabled", false, false, ""},
		// HEAD's e-mail routes revoked the Cloudflare guest-policy entry of the old
		// address and never touched the key: it belongs to the account.
		{"email changed", true, false, ""},
		{"deleted", true, true, "HEAD kept the key of a deleted account"},
		{"merged away", true, true, "HEAD kept the key of a merged-away account"},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
			h.seedAdded(t, gSelina)
			h.seedClaim(t)
			fireEvent(tc.event, h.srv.AccountLifecycle(), t.Context())
			h.srv.bgWG.Wait()
			_, removed := h.cf.snapshot()
			if tc.wantPolicyEnds {
				require.Equal(t, []string{gSelina}, removed, "the guest-policy concern did not run for %q", tc.event)
			} else {
				require.Empty(t, removed, "the guest-policy concern ran for %q", tc.event)
			}
			require.Equal(t, !tc.wantKeyForgot, h.claimSurvives(), "the guest-key concern for %q", tc.event)
		})
	}
}

// An accept runs both concerns from one event: the grant (the guest policy) and
// the one-time key entitlement (OfferGuestKeyClaim) share HEAD's gates.
func TestAccountLifecycle_AcceptOffersTheKeyBesideTheGrant(t *testing.T) {
	cases := []struct {
		name            string
		mailed, creator bool
		wantClaim       bool
	}{
		{"mailed by an admin", true, true, true},
		{"not mailed", false, true, false},
		{"creator not admin", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
			config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = true })
			h.setUsers(guestUser(gUserID, gSelina, true, coreauth.StatusActive))
			h.accept(t, accepted(gSelina, tc.mailed, tc.creator))
			h.srv.bgWG.Wait()
			// The guest-policy concern always answers an accepted invitation with an address.
			require.NotNil(t, h.outcome(t, "inv-1"), "the guest-policy concern did not run")
			require.Equal(t, tc.wantClaim, h.claimSurvives(), "the guest-key concern")
		})
	}
	t.Run("feature off offers nothing", func(t *testing.T) {
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = false })
		h.setUsers(guestUser(gUserID, gSelina, true, coreauth.StatusActive))
		h.accept(t, accepted(gSelina, true, true))
		h.srv.bgWG.Wait()
		require.False(t, h.claimSurvives())
	})
}

// panickingUsers is core's identity view when a read of it blows up: the
// guest-policy concern reads it for the keep rule on a revoke.
type panickingUsers struct{ coreauth.Identities }

func (panickingUsers) Users(context.Context, coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	panic("the identity view blew up")
}

// panickingTokens is a store whose guest-token lookup blows up: the guest-key
// concern reads it first on a revoke.
type panickingTokens struct{ *database.PebbleStore }

func (panickingTokens) GetGuestServiceToken(string) (*database.GuestServiceToken, error) {
	panic("the token store blew up")
}

// A panic in the guest-policy half of an event must not skip the key half: core
// recovers a panic one level up, which would end the whole callback.
func TestAccountLifecycle_PanicInThePolicyHalfStillRevokesTheKey(t *testing.T) {
	for _, event := range []string{"disabled", "deleted", "merged away"} {
		t.Run(event, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
			h.srv.identity.Users = panickingUsers{}
			h.seedAdded(t, gSelina)
			h.seedClaim(t)
			require.NotPanics(t, func() { fireEvent(event, h.srv.AccountLifecycle(), t.Context()) })
			h.srv.bgWG.Wait()
			require.False(t, h.claimSurvives(), "the key was left alive by a panic in the guest-policy half")
		})
	}
}

// And the reverse: a panic in the key half must not skip the policy half.
func TestAccountLifecycle_PanicInTheKeyHalfStillRevokesThePolicy(t *testing.T) {
	for _, event := range []string{"disabled", "deleted", "merged away"} {
		t.Run(event, func(t *testing.T) {
			h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
			h.srv.store = panickingTokens{h.store}
			h.seedAdded(t, gSelina)
			require.NotPanics(t, func() { fireEvent(event, h.srv.AccountLifecycle(), t.Context()) })
			h.waitRemoved(t, gSelina)
		})
	}
	t.Run("accept", func(t *testing.T) {
		// The key half of an accept panics (the clock of the entitlement table):
		// the grant still goes in.
		h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
		config.Mutate(func(c *config.Config) { c.CFGuestServiceTokens = true })
		h.srv.guestKeyClaims.now = func() time.Time { panic("the clock blew up") }
		h.setUsers(guestUser(gUserID, gSelina, true, coreauth.StatusActive))
		require.NotPanics(t, func() { h.accept(t, accepted(gSelina, true, true)) })
		h.waitOutcome(t, "inv-1", database.InviteGuestPolicyAdded)
	})
}
