// file: internal/books/store/pebble_store_invite_accepts_test.go
// version: 0.1.0
// guid: 5a90e3c8-4d17-4f26-9b84-0c1d7e63a2f9
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An accepted-invite row keeps every member the Users page shows, byte for byte
// through JSON, and holds nothing that is a token.
func TestInviteAccept_RoundTripsEveryMember(t *testing.T) {
	s := newInviteTestStore(t)
	at := time.Date(2026, 11, 20, 12, 30, 45, 123456789, time.UTC)
	gpAt := at.Add(3 * time.Second)
	want := InviteAccept{
		InviteID: "01BATMANACCEPT000000000001", UserID: "u-barbara", Username: "barbara.gordon",
		Email: "barbara.gordon@example.test", AcceptedAt: at,
		EmailBinding: InviteEmailBound, SSOGrant: InviteSSOGranted,
		DisplacedUserIDs:  []string{"u-selina", "u-harvey"},
		GuestPolicyStatus: InviteGuestPolicyAdded, GuestPolicyDetail: "added", GuestPolicyAt: &gpAt,
	}
	if err := s.RecordInviteAccept(want); err != nil {
		t.Fatal(err)
	}
	raw, err := s.GetRaw(inviteAcceptPrefix + want.InviteID)
	if err != nil || raw == nil {
		t.Fatalf("no row under %s%s: %v", inviteAcceptPrefix, want.InviteID, err)
	}
	var got InviteAccept
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.AcceptedAt.Equal(want.AcceptedAt) || got.GuestPolicyAt == nil || !got.GuestPolicyAt.Equal(gpAt) {
		t.Fatalf("times = %v / %v, want %v / %v", got.AcceptedAt, got.GuestPolicyAt, at, gpAt)
	}
	got.AcceptedAt, got.GuestPolicyAt = want.AcceptedAt, want.GuestPolicyAt
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("row = %s, want %s", gb, wb)
	}
	for _, k := range []string{"token", "invite_token", "password", "secret"} {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if _, ok := m[k]; ok {
			t.Errorf("the row has a %q member", k)
		}
	}
}

// A row that could never be listed (no time) or never joined (no ids) is refused,
// not written.
func TestInviteAccept_RefusesARowWithNoIdentityOrTime(t *testing.T) {
	s := newInviteTestStore(t)
	at := time.Now()
	for name, a := range map[string]InviteAccept{
		"no invite id": {UserID: "u", AcceptedAt: at},
		"no user id":   {InviteID: "i", AcceptedAt: at},
		"no time":      {InviteID: "i", UserID: "u"},
	} {
		if err := s.RecordInviteAccept(a); err == nil {
			t.Errorf("%s: the row was stored", name)
		}
	}
	if raw, _ := s.GetRaw(inviteAcceptPrefix + "i"); raw != nil {
		t.Errorf("a refused row left %s behind", raw)
	}
}

// A second record of one invite replaces the first (one row per invite).
func TestInviteAccept_OneRowPerInvite(t *testing.T) {
	s := newInviteTestStore(t)
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"first", "second"} {
		if err := s.RecordInviteAccept(InviteAccept{InviteID: "i", UserID: "u", Username: name, AcceptedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := s.GetRaw(inviteAcceptPrefix + "i")
	var got InviteAccept
	if err := json.Unmarshal(raw, &got); err != nil || got.Username != "second" {
		t.Fatalf("row = %s (%v), want the second record", raw, err)
	}
}

func TestInviteAcceptFamily_IsRegistered(t *testing.T) {
	for _, f := range KeyFamilies() {
		if f.Prefix == inviteAcceptPrefix {
			return
		}
	}
	t.Errorf("%s is not a registered key family", inviteAcceptPrefix)
}

// The list is the family only: windowed on the accept time (the edge itself is in),
// newest first, deterministic on a tie, skipping a row that does not decode, never
// nil, and it writes nothing.
func TestInviteAccept_ListSinceIsWindowedNewestFirstAndFamilyOnly(t *testing.T) {
	s := newInviteTestStore(t)
	if got, err := s.ListInviteAcceptsSince(time.Time{}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty family = %#v, %v, want a non-nil empty list", got, err)
	}
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, o := range []struct {
		inv string
		at  time.Time
	}{
		{"inv-a", base}, {"inv-b", base.Add(time.Hour)}, {"inv-c", base.Add(time.Hour)}, {"inv-old", base.Add(-time.Second)},
	} {
		if err := s.RecordInviteAccept(InviteAccept{InviteID: o.inv, UserID: "u-" + o.inv, Username: o.inv, AcceptedAt: o.at}); err != nil {
			t.Fatal(err)
		}
	}
	// What the scan must not return: a row of the outcome family, which the list
	// joins by id and never lists, and a corrupt row inside this family.
	if err := s.RecordGuestPolicyOutcome("inv-outcome", "u-x", InviteGuestPolicyAdded, "", base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte(inviteAcceptPrefix+"inv-corrupt"), []byte("{not json"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListInviteAcceptsSince(base)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range got {
		ids = append(ids, a.InviteID)
	}
	if want := "inv-c,inv-b,inv-a"; strings.Join(ids, ",") != want {
		t.Fatalf("list = %v, want %s (window edge included, newest first, tie by id descending, corrupt row skipped)", ids, want)
	}
	if all, _ := s.ListInviteAcceptsSince(time.Time{}); len(all) != 4 {
		t.Fatalf("an unbounded list has %d rows, want 4", len(all))
	}
}
