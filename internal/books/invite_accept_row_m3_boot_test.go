// file: internal/books/invite_accept_row_m3_boot_test.go
// version: 0.1.0
// guid: d4a81f36-5c20-4e97-b3d8-70e1f9a25c64
// last-edited: 2026-11-22

package books

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// acHeadAccept is one invite HEAD's own code created, mailed and accepted before the
// flip, and what HEAD's own Users page said of it.
type acHeadAccept struct {
	token string
	view  database.Invite // HEAD's row after the accept and the sync
}

// acSeedHeadAccepts drives HEAD's real invite code (CreateInvite, ConsumeInvite,
// RecordInviteGuestPolicy) for three accepts and returns each as HEAD stored it: the
// invites the cutover has to carry. The oracle is HEAD's own ListAcceptedInvitesSince.
//
//	bound    mailed to an address another account had typed in: bound, granted, displaced
//	         that account, and the sync failed
//	plain    no address at all
//	unmailed an address the invitation was never mailed to: not_emailed, no grant
func acSeedHeadAccepts(t *testing.T, out *[]acHeadAccept) func(*database.PebbleStore, map[string]string) {
	return func(s *database.PebbleStore, ids map[string]string) {
		hash, err := bcrypt.GenerateFromPassword([]byte(gpPass), bcrypt.MinCost)
		require.NoError(t, err)
		admin := ids[gpAdminName]
		mk := func(name, email string, mailed bool) string {
			tok := "ac-head-token-" + name + "-0123456789abcdef"
			inv := &database.Invite{Token: tok, Username: name, Email: email, RoleID: "viewer", CreatedByUserID: admin}
			if mailed {
				at := time.Now().Add(-time.Hour)
				inv.EmailSentAt, inv.EmailSentTo = &at, email
			}
			_, err := s.CreateInvite(inv)
			require.NoError(t, err)
			_, err = s.ConsumeInvite(tok, "bcrypt", string(hash))
			require.NoError(t, err)
			return tok
		}
		bound := mk("lucius.fox", acKate, true)
		require.NoError(t, s.RecordInviteGuestPolicy(bound, database.InviteGuestPolicyFailed, "the Access policy refused the update", time.Now().Add(3*time.Second)))
		plain := mk("alfred.pennyworth", "", false)
		unmailed := mk("selina.kyle", acLuke, false)

		list, err := s.ListAcceptedInvitesSince(time.Time{})
		require.NoError(t, err)
		require.Len(t, list, 3, "HEAD's own list should show the three accepts")
		for _, tok := range []string{bound, plain, unmailed} {
			for _, i := range list {
				if i.Token == tok {
					*out = append(*out, acHeadAccept{token: tok, view: i})
				}
			}
		}
		require.Len(t, *out, 3)
	}
}

// A HEAD invite used inside the 30 days survives the flip as a books-owned accept row
// with HEAD's own values, keyed by the invite's derived ID (never the token), and the
// invite itself, with its token, is gone (F4-08 B10x2: the M3 carry, through a real
// boot of the flipped build over a store HEAD's own code wrote).
func TestInviteAcceptRow_PreFlipAcceptsSurviveM3(t *testing.T) {
	var head []acHeadAccept
	e := acBoot(t, acSeedHeadAccepts(t, &head))
	ps := database.AsPebbleStore(e.store)
	require.NotNil(t, ps)

	for _, h := range head {
		id := database.InviteID(h.token)
		a := acRow(t, e, id)
		require.NotNil(t, a, "no accept row for %s; log:\n%s", h.view.Username, e.logs())
		v := h.view
		assert.Equal(t, id, a.InviteID)
		assert.Equal(t, v.Username, a.Username)
		assert.Equal(t, v.AcceptedUserID, a.UserID)
		assert.Equal(t, v.Email, a.Email)
		require.NotNil(t, v.UsedAt)
		assert.True(t, a.AcceptedAt.Equal(*v.UsedAt), "%s: accepted_at = %v, HEAD's used_at = %v", v.Username, a.AcceptedAt, *v.UsedAt)
		assert.Equal(t, v.EmailBinding, a.EmailBinding, v.Username)
		assert.Equal(t, v.SSOGrant, a.SSOGrant, v.Username)
		assert.True(t, slices.Equal(v.DisplacedUserIDs, a.DisplacedUserIDs), "%s: displaced = %v, HEAD's = %v", v.Username, a.DisplacedUserIDs, v.DisplacedUserIDs)
		assert.Equal(t, v.GuestPolicyStatus, a.GuestPolicyStatus, v.Username)
		assert.Equal(t, v.GuestPolicyDetail, a.GuestPolicyDetail, v.Username)
		if v.GuestPolicyAt == nil {
			assert.Nil(t, a.GuestPolicyAt, v.Username)
		} else if assert.NotNil(t, a.GuestPolicyAt, v.Username) {
			assert.True(t, a.GuestPolicyAt.Equal(*v.GuestPolicyAt), v.Username)
		}

		raw, err := ps.GetRaw("invite:" + h.token)
		require.NoError(t, err)
		assert.Nil(t, raw, "%s: HEAD's invite row (and its token) survived the cutover", v.Username)
	}

	// The bound accept really did what the fixture says, so the fields above were
	// not all empty on both sides.
	b := head[0].view
	require.Equal(t, database.InviteEmailBound, b.EmailBinding)
	require.Equal(t, database.InviteSSOGranted, b.SSOGrant)
	require.NotEmpty(t, b.DisplacedUserIDs)
	require.Equal(t, database.InviteGuestPolicyFailed, b.GuestPolicyStatus)
	require.Equal(t, database.InviteEmailNotEmailed, head[2].view.EmailBinding)
	require.Empty(t, head[1].view.EmailBinding)
}
