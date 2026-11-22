// file: internal/books/store/pebble_store_invite_accepts_test.go
// version: 0.1.0
// guid: 5a90e3c8-4d17-4f26-9b84-0c1d7e63a2f9
// last-edited: 2026-11-22

package store

import (
	"encoding/json"
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
