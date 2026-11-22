// file: internal/books/invite_accept_row_boot_test.go
// version: 0.1.0
// guid: 61c9a4e2-7d38-4b05-a8f3-2e90c1d74b56
// last-edited: 2026-11-22

package books

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The accepted-invite row is written from core's InviteAccepted event, ahead of and
// independent of the guest-policy sync (F4-08 B10x2). One flipped boot over a seeded
// HEAD store answers every case, each its own named subtest: a real kernel, core's
// real invites, accounts and sessions, books' registered lifecycle, and a fake only of
// Cloudflare's API. The row is read as stored; the list that serves it is the
// accepted-list cells' (guest_policy_accepted_list_boot_test.go).
func TestInviteAcceptRow_FullBoot(t *testing.T) {
	e := acBoot(t, nil)

	// An invite that names no address: the guest-policy concern returns before it
	// records anything for one, so only the accept row can show this accept.
	t.Run("01_an_accept_with_no_address_leaves_a_row", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "luke.fox", "")
		e.accept(t, tok)
		a := acRow(t, e, id)
		require.NotNil(t, a, "no accept row for an invite that named no address; log:\n%s", e.logs())
		uid := acAcceptedUserID(t, e, "luke.fox")
		assert.Equal(t, uid, a.UserID)
		assert.Equal(t, "luke.fox", a.Username)
		assert.Empty(t, a.Email)
		assert.Empty(t, a.EmailBinding, "HEAD left the binding empty for an invite with no address")
		assert.Empty(t, a.SSOGrant)
		assert.Empty(t, a.DisplacedUserIDs)
		assert.Empty(t, a.GuestPolicyStatus, "an accept made under core identity carries no sync outcome on its row")
		assert.Nil(t, e.outcome(t, id), "the sync recorded an outcome for an invite with no address, which HEAD never did")
	})

	// The accept time is core's own: the created account's CreatedAt, exactly. The
	// guest-policy sync is held (its first call to Cloudflare blocks on the fake's
	// lock) for far longer than any clock noise, so the outcome row's time is later,
	// and an accept time taken from it, or from the moment books' callback finishes
	// its work, would not equal core's.
	t.Run("02_the_accept_time_is_core_s_not_the_syncs", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "will.magnus", acWill)
		e.markDelivered(t, id)
		e.cf.mu.Lock()
		before := time.Now()
		e.accept(t, tok)
		const held = 400 * time.Millisecond
		time.Sleep(held)
		e.cf.mu.Unlock()
		o := e.waitOutcome(t, id, database.InviteGuestPolicyAdded)

		a := acRow(t, e, id)
		require.NotNil(t, a, "log:\n%s", e.logs())
		uid := acAcceptedUserID(t, e, "will.magnus")
		created := acCoreUser(t, e, uid).CreatedAt
		assert.True(t, a.AcceptedAt.Equal(created), "accept time = %v, core's CreatedAt for the account = %v", a.AcceptedAt, created)
		assert.False(t, a.AcceptedAt.Before(before.Add(-time.Second)), "accept time %v is long before the accept at %v", a.AcceptedAt, before)
		assert.GreaterOrEqual(t, o.At.Sub(a.AcceptedAt), held-50*time.Millisecond,
			"the sync reported at %v, which should be at least %v after the accept at %v", o.At, held, a.AcceptedAt)
	})

	// Every member HEAD showed, for an accept that bound its address and displaced
	// an account that had typed it in.
	t.Run("03_every_member_of_a_bound_accept", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "luke.fox2", acKate)
		e.markDelivered(t, id)
		e.accept(t, tok)
		e.waitOutcome(t, id, database.InviteGuestPolicyAdded)
		a := acRow(t, e, id)
		require.NotNil(t, a, "log:\n%s", e.logs())
		assert.Equal(t, id, a.InviteID)
		assert.Equal(t, acAcceptedUserID(t, e, "luke.fox2"), a.UserID)
		assert.Equal(t, "luke.fox2", a.Username)
		assert.Equal(t, acKate, a.Email)
		assert.Equal(t, database.InviteEmailBound, a.EmailBinding)
		assert.Equal(t, database.InviteSSOGranted, a.SSOGrant)
		assert.Equal(t, []string{e.ids["kate.kane"]}, a.DisplacedUserIDs, "the account that typed the address in lost it to the accept")
	})

	// HEAD's other two bindings: an invitation that was never mailed binds nothing.
	t.Run("04_an_unmailed_invitation_is_not_emailed_and_grants_nothing", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "luke.fox3", acLuke)
		e.accept(t, tok)
		e.waitOutcome(t, id, database.InviteGuestPolicySkipped)
		a := acRow(t, e, id)
		require.NotNil(t, a, "log:\n%s", e.logs())
		assert.Equal(t, acLuke, a.Email, "HEAD showed the address even when it was not bound")
		assert.Equal(t, database.InviteEmailNotEmailed, a.EmailBinding)
		assert.Empty(t, a.SSOGrant)
		assert.Empty(t, a.DisplacedUserIDs)
	})

	// The row is written before the sync and does not depend on it: Cloudflare
	// refuses the update and the sync records "failed", and the accept row is there.
	t.Run("05_a_failed_sync_still_leaves_the_accept_row", func(t *testing.T) {
		e.cf.setFail(true)
		t.Cleanup(func() { e.cf.setFail(false) })
		id, tok := e.invite(t, e.admin, "luke.fox4", "luke.fox4@example.test")
		e.markDelivered(t, id)
		e.accept(t, tok)
		o := e.waitOutcome(t, id, database.InviteGuestPolicyFailed)
		a := acRow(t, e, id)
		require.NotNil(t, a, "the sync failed (%s) and took the accept row with it; log:\n%s", o.Detail, e.logs())
		assert.Equal(t, database.InviteEmailBound, a.EmailBinding)
		assert.Equal(t, "luke.fox4", a.Username)
	})

	// A refused second accept of a spent invite fires nothing, so it changes nothing.
	t.Run("06_a_second_accept_of_a_spent_invite_changes_nothing", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "luke.fox5", "")
		e.accept(t, tok)
		require.NotNil(t, acRow(t, e, id))
		rec := post(t, e.h(), "/api/core/v1/auth/invites/accept", map[string]string{"token": tok, "password": gpPass}, false)
		require.NotEqual(t, http.StatusOK, rec.Code, "a spent invite was accepted twice")
		// Still the one row, still the first account's.
		a := acRow(t, e, id)
		require.NotNil(t, a)
		assert.Equal(t, "luke.fox5", a.Username)
	})
}
