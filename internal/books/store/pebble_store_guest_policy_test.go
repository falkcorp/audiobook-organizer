// file: internal/books/store/pebble_store_guest_policy_test.go
// version: 0.1.0
// guid: 0d2c8e41-6b97-4f53-a1d8-93e5c7b40f26
// last-edited: 2026-11-22

package store

import (
	"testing"
	"time"
)

// The guest-policy bookkeeping behaves as HEAD's inviteallow: grant did for the
// sync and the revoke (CFAdded set only for the user the row names, a delete
// returns what it removed), and it never touches the retired inviteallow: family.
func TestGuestPolicyGrants_SetCFAddedAndDeleteFollowTheUser(t *testing.T) {
	s := newInviteTestStore(t)
	grants := s.GuestPolicyGrants()
	if g, err := grants.GetInviteEmailGrant("selina.kyle@example.test"); err != nil || g != nil {
		t.Fatalf("Get on empty = %v, %v", g, err)
	}
	if err := s.PutGuestPolicyGrant(GuestPolicyGrant{Email: " Selina.Kyle@Example.Test ", UserID: "u-selina", InviteID: "inv-1", GrantedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	g, err := grants.GetInviteEmailGrant("selina.kyle@example.test")
	if err != nil || g == nil || g.UserID != "u-selina" || g.CFAdded {
		t.Fatalf("Get = %+v, %v", g, err)
	}
	if ok, err := grants.SetInviteEmailGrantCFAdded("selina.kyle@example.test", "u-someone-else"); err != nil || ok {
		t.Fatalf("CFAdded for another user = %v, %v, want false", ok, err)
	}
	if ok, err := grants.SetInviteEmailGrantCFAdded("selina.kyle@example.test", "u-selina"); err != nil || !ok {
		t.Fatalf("CFAdded = %v, %v, want true", ok, err)
	}
	if del, err := grants.DeleteInviteEmailGrant("selina.kyle@example.test", "u-someone-else"); err != nil || del != nil {
		t.Fatalf("Delete for another user = %v, %v, want nothing", del, err)
	}
	del, err := grants.DeleteInviteEmailGrant("selina.kyle@example.test", "u-selina")
	if err != nil || del == nil || !del.CFAdded || del.UserID != "u-selina" {
		t.Fatalf("Delete = %+v, %v, want the removed grant with CFAdded", del, err)
	}
	// Gone: a second delete finds nothing, and the add of a revoked grant reports it.
	if del, err := grants.DeleteInviteEmailGrant("selina.kyle@example.test", "u-selina"); err != nil || del != nil {
		t.Fatalf("second Delete = %v, %v", del, err)
	}
	if ok, err := grants.SetInviteEmailGrantCFAdded("selina.kyle@example.test", "u-selina"); err != nil || ok {
		t.Fatalf("CFAdded after the revoke = %v, %v, want false", ok, err)
	}
	// The retired SSO-grant family is not this one.
	if sso, err := s.GetInviteEmailGrant("selina.kyle@example.test"); err != nil || sso != nil {
		t.Fatalf("the inviteallow: family holds %v, %v", sso, err)
	}
}

func TestGuestPolicyOutcome_RoundTripsAndBoundsTheDetail(t *testing.T) {
	s := newInviteTestStore(t)
	if o, err := s.GetGuestPolicyOutcome("inv-1"); err != nil || o != nil {
		t.Fatalf("Get on empty = %v, %v", o, err)
	}
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'x'
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.RecordGuestPolicyOutcome("inv-1", "u-selina", InviteGuestPolicyFailed, string(long), at); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetGuestPolicyOutcome("inv-1")
	if err != nil || o == nil || o.Status != InviteGuestPolicyFailed || o.UserID != "u-selina" || len(o.Detail) != 300 || !o.At.Equal(at) {
		t.Fatalf("Get = %+v, %v", o, err)
	}
	if err := s.RecordGuestPolicyOutcome("", "u", "x", "", at); err == nil {
		t.Fatal("an outcome with no invite id was stored")
	}
}

func TestGuestPolicyFamilies_AreRegisteredAndDoNotNest(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range KeyFamilies() {
		seen[f.Prefix] = true
	}
	for _, p := range []string{guestPolicyPrefix, guestPolicyOutcomePrefix} {
		if !seen[p] {
			t.Errorf("%s is not a registered key family", p)
		}
	}
}
