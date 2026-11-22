// file: internal/books/store/pebble_store_guest_policy_test.go
// version: 0.1.0
// guid: 0d2c8e41-6b97-4f53-a1d8-93e5c7b40f26
// last-edited: 2026-11-22

package store

import (
	"strings"
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

// The outcome list is the family only: windowed, newest first, deterministic on a
// tie, skipping a row that does not decode, and it writes nothing.
func TestGuestPolicyOutcome_ListSinceIsWindowedNewestFirstAndFamilyOnly(t *testing.T) {
	s := newInviteTestStore(t)
	if got, err := s.ListGuestPolicyOutcomesSince(time.Time{}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty family = %#v, %v, want a non-nil empty list", got, err)
	}
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, o := range []struct {
		inv string
		at  time.Time
	}{
		{"inv-a", base}, {"inv-b", base.Add(time.Hour)}, {"inv-c", base.Add(time.Hour)}, {"inv-old", base.Add(-48 * time.Hour)},
	} {
		if err := s.RecordGuestPolicyOutcome(o.inv, "u-"+o.inv, InviteGuestPolicyAdded, "", o.at); err != nil {
			t.Fatal(err)
		}
	}
	// Neighbours the scan must not read: the grant family, a corrupt outcome row.
	if err := s.PutGuestPolicyGrant(GuestPolicyGrant{Email: "selina.kyle@example.test", UserID: "u-selina", GrantedAt: base.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte(guestPolicyOutcomePrefix+"inv-corrupt"), []byte("{not json"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListGuestPolicyOutcomesSince(base)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, o := range got {
		ids = append(ids, o.InviteID)
	}
	if want := "inv-c,inv-b,inv-a"; strings.Join(ids, ",") != want {
		t.Fatalf("list = %v, want %s (window, newest first, tie by id descending, corrupt row skipped)", ids, want)
	}
	if all, _ := s.ListGuestPolicyOutcomesSince(time.Time{}); len(all) != 4 {
		t.Fatalf("an unbounded list has %d rows, want 4", len(all))
	}
}
