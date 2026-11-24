// file: internal/books/invite_accept_list_boot_test.go
// version: 0.1.0
// guid: 90b3e7d1-2c58-4a46-8e19-f4a6d35b0c72
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The accepted-invites list is read from the accept rows (F4-08 B10x2), through a
// real flipped boot over a store HEAD's own invite code wrote: the accepts HEAD's
// Users page listed on the day of the flip, then accepts made under core, each with
// what the guest-policy sync did (or failed to do) joined by invite id.

// acHeadView is HEAD's acceptedInviteView (handlers/invite_delivery.go), member for
// member and tag for tag: the oracle the served JSON is compared to.
type acHeadView struct {
	Username          string     `json:"username"`
	Email             string     `json:"email,omitempty"`
	AcceptedAt        *time.Time `json:"accepted_at"`
	AcceptedUserID    string     `json:"accepted_user_id,omitempty"`
	EmailBinding      string     `json:"email_binding,omitempty"`
	SSOGrant          string     `json:"sso_grant,omitempty"`
	GuestPolicyStatus string     `json:"guest_policy_status,omitempty"`
	GuestPolicyDetail string     `json:"guest_policy_detail,omitempty"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at,omitempty"`
	DisplacedUserIDs  []string   `json:"displaced_user_ids,omitempty"`
}

func acHeadViewOf(i database.Invite) acHeadView {
	return acHeadView{
		Username: i.Username, Email: i.Email, AcceptedAt: i.UsedAt, AcceptedUserID: i.AcceptedUserID,
		EmailBinding: i.EmailBinding, SSOGrant: i.SSOGrant,
		GuestPolicyStatus: i.GuestPolicyStatus, GuestPolicyDetail: i.GuestPolicyDetail, GuestPolicyAt: i.GuestPolicyAt,
		DisplacedUserIDs: i.DisplacedUserIDs,
	}
}

// acServed is the list as the admin gets it, one generic map per row so a member the
// view should not have, or lacks, is visible.
func acServed(t *testing.T, e *gpEnv, addr *int) []map[string]any {
	t.Helper()
	*addr++
	code, raw, body := acceptedList(t, e.runningApp, "", e.admin, "192.0.2."+strconv.Itoa(120+*addr)+":41000")
	require.Equal(t, http.StatusOK, code, body)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(raw, &rows), body)
	require.NotNil(t, rows, "accepted must be [] and never null: %s", body)
	return rows
}

func acServedBy(t *testing.T, rows []map[string]any, username string) map[string]any {
	t.Helper()
	for _, r := range rows {
		if r["username"] == username {
			return r
		}
	}
	t.Fatalf("no row for %s in %v", username, rows)
	return nil
}

func acKeys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func TestInviteAcceptList_FullBoot(t *testing.T) {
	var head []acHeadAccept
	e := acBoot(t, acSeedHeadAccepts(t, &head))
	var addr int

	// What HEAD showed for the three accepts it took before the flip, member for
	// member and value for value, in HEAD's order. The set is exact: no row more, none
	// fewer, no member HEAD's view did not have, and the bound one carries every member
	// HEAD's has (the address, the binding and the grant, the displaced account, the
	// sync's status, detail and time).
	t.Run("01_pre_flip_accepts_are_listed_with_HEADs_fields", func(t *testing.T) {
		rows := acServed(t, e, &addr)
		require.Len(t, rows, len(head), "served %v", rows)
		for i, h := range head {
			want, err := json.Marshal(acHeadViewOf(h.view))
			require.NoError(t, err)
			got, err := json.Marshal(rows[i])
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got), "row %d (%s)", i, h.view.Username)
		}
		assert.Equal(t, []string{"accepted_at", "accepted_user_id", "displaced_user_ids", "email", "email_binding", "guest_policy_at",
			"guest_policy_detail", "guest_policy_status", "sso_grant", "username"}, acKeys(acServedBy(t, rows, "lucius.fox")),
			"the bound accept lacks a member HEAD's view carries")
	})

	// An accept with no address: the guest-policy concern records nothing for it, so
	// only the accept row can show it.
	t.Run("02_an_accept_with_no_address_is_listed", func(t *testing.T) {
		_, tok := e.invite(t, e.admin, "harvey.dent", "")
		e.accept(t, tok)
		r := acServedBy(t, acServed(t, e, &addr), "harvey.dent")
		assert.Equal(t, acAcceptedUserID(t, e, "harvey.dent"), r["accepted_user_id"])
		assert.Equal(t, []string{"accepted_at", "accepted_user_id", "username"}, acKeys(r), "an accept with no address has no other member (HEAD's omitempty)")
	})

	// The accept time is core's, the sync's is later, and the two are reported
	// separately: accepted_at is when the guest accepted, guest_policy_at when the
	// sync reported. The sync is held for far longer than clock noise.
	t.Run("03_accepted_at_is_the_accept_time_not_the_sync_time", func(t *testing.T) {
		id, tok := e.invite(t, e.admin, "will.magnus", acWill)
		e.markDelivered(t, id)
		e.cf.mu.Lock()
		e.accept(t, tok)
		const held = 400 * time.Millisecond
		time.Sleep(held)
		e.cf.mu.Unlock()
		e.waitOutcome(t, id, database.InviteGuestPolicyAdded)

		r := acServedBy(t, acServed(t, e, &addr), "will.magnus")
		created := acCoreUser(t, e, acAcceptedUserID(t, e, "will.magnus")).CreatedAt
		var at, gpAt time.Time
		require.NoError(t, at.UnmarshalJSON([]byte(strconv.Quote(r["accepted_at"].(string)))))
		require.NoError(t, gpAt.UnmarshalJSON([]byte(strconv.Quote(r["guest_policy_at"].(string)))))
		assert.True(t, at.Equal(created), "accepted_at = %v, the accept's own time = %v", at, created)
		assert.GreaterOrEqual(t, gpAt.Sub(at), held-50*time.Millisecond, "guest_policy_at %v is not the later sync time against accepted_at %v", gpAt, at)
		assert.Equal(t, database.InviteGuestPolicyAdded, r["guest_policy_status"])
		assert.Equal(t, database.InviteEmailBound, r["email_binding"])
		assert.Equal(t, database.InviteSSOGranted, r["sso_grant"])
	})

	// A failed sync does not cost the accept its place in the list: it is there with
	// the failure the sync recorded.
	t.Run("04_a_sync_failure_still_lists_the_accept", func(t *testing.T) {
		e.cf.setFail(true)
		t.Cleanup(func() { e.cf.setFail(false) })
		id, tok := e.invite(t, e.admin, "luke.fox", "luke.fox@example.test")
		e.markDelivered(t, id)
		e.accept(t, tok)
		o := e.waitOutcome(t, id, database.InviteGuestPolicyFailed)
		r := acServedBy(t, acServed(t, e, &addr), "luke.fox")
		assert.Equal(t, database.InviteGuestPolicyFailed, r["guest_policy_status"])
		assert.Equal(t, o.Detail, r["guest_policy_detail"])
		assert.Equal(t, database.InviteEmailBound, r["email_binding"])
	})

	// An accept the sync has not reported on yet is listed with no sync members at
	// all, as HEAD listed an invite before its sync recorded anything.
	t.Run("05_an_accept_with_no_outcome_yet_has_no_sync_members", func(t *testing.T) {
		_, tok := e.invite(t, e.admin, "oswald.cobblepot", "")
		e.accept(t, tok)
		r := acServedBy(t, acServed(t, e, &addr), "oswald.cobblepot")
		for _, k := range []string{"guest_policy_status", "guest_policy_detail", "guest_policy_at"} {
			assert.NotContains(t, r, k)
		}
	})

	t.Run("06_the_read_writes_nothing", func(t *testing.T) {
		ps := database.AsPebbleStore(e.store)
		before, err := ps.ListInviteAcceptsSince(time.Time{})
		require.NoError(t, err)
		for range 3 {
			acServed(t, e, &addr)
		}
		after, err := ps.ListInviteAcceptsSince(time.Time{})
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})
}
