// file: internal/books/store/guest_tokens_test.go
// version: 1.1.1
// guid: 7a1f4d92-3c68-4b05-8e27-f9b0c5d2a841
// last-edited: 2026-11-09

package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGuestServiceToken_CreateGetListUpdate(t *testing.T) {
	s := newInviteTestStore(t)
	if g, err := s.GetGuestServiceToken("U1"); err != nil || g != nil {
		t.Fatalf("Get on empty = %v, %v", g, err)
	}
	if err := s.CreateGuestServiceToken(GuestServiceToken{UserID: "U1", Name: "aorg-guest-U1", Status: GuestTokenPending}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGuestServiceToken(GuestServiceToken{UserID: "U1", Name: "x", Status: GuestTokenPending}); !errors.Is(err, ErrGuestServiceTokenExists) {
		t.Fatalf("second create = %v, want exists", err)
	}
	if err := s.CreateGuestServiceToken(GuestServiceToken{Name: "x"}); err == nil {
		t.Fatal("create without a user id succeeded")
	}
	time.Sleep(2 * time.Millisecond)
	if err := s.CreateGuestServiceToken(GuestServiceToken{UserID: "U2", Name: "aorg-guest-U2", Status: GuestTokenPending}); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateGuestServiceToken("U1", func(g *GuestServiceToken) error {
		g.TokenID, g.SecretHash, g.Status = "tok-1", "hash", GuestTokenMinted
		g.Detail = strings.Repeat("x", 1000)
		g.UserID = "someone-else" // must not move the record
		return nil
	})
	if err != nil || got.UserID != "U1" || got.Status != GuestTokenMinted || len(got.Detail) != guestTokenDetailMax {
		t.Fatalf("update = %+v, %v", got, err)
	}
	// A failing callback writes nothing.
	if _, err := s.UpdateGuestServiceToken("U1", func(g *GuestServiceToken) error {
		g.Status = GuestTokenActive
		return errors.New("stop")
	}); err == nil {
		t.Fatal("callback error swallowed")
	}
	if g, _ := s.GetGuestServiceToken("U1"); g.Status != GuestTokenMinted {
		t.Fatalf("aborted update was saved: %s", g.Status)
	}
	if _, err := s.UpdateGuestServiceToken("nobody", func(*GuestServiceToken) error { return nil }); !errors.Is(err, ErrGuestServiceTokenNotFound) {
		t.Fatalf("update of a missing record = %v", err)
	}
	list, err := s.ListGuestServiceTokens()
	if err != nil || len(list) != 2 || list[0].UserID != "U2" {
		t.Fatalf("list = %+v, %v (want newest first)", list, err)
	}
}

func TestGuestServiceToken_KeyFamilyRegistered(t *testing.T) {
	for _, f := range keyFamilies {
		if f.Prefix == guestServiceTokenPrefix {
			return
		}
	}
	t.Fatalf("%s is not in the key-family registry", guestServiceTokenPrefix)
}

// A reissue reopens a revoked record in place: one record per user, the new
// field round-trips, and a status check inside the callback is atomic.
func TestGuestServiceToken_ReopenRevokedRecordRoundTripsReissuedAt(t *testing.T) {
	s := newInviteTestStore(t)
	if err := s.CreateGuestServiceToken(GuestServiceToken{UserID: "U1", Name: "aorg-guest-U1", Status: GuestTokenPending}); err != nil {
		t.Fatal(err)
	}
	revokedAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.UpdateGuestServiceToken("U1", func(g *GuestServiceToken) error {
		g.TokenID, g.SecretHash, g.Status, g.PolicyAdded = "tok-1", "hash", GuestTokenRevoked, true
		g.RevokedAt, g.RevokeReason = &revokedAt, "revoked by an admin"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A second create is still refused: the keyspace keeps one record per user.
	if err := s.CreateGuestServiceToken(GuestServiceToken{UserID: "U1", Name: "x", Status: GuestTokenPending}); !errors.Is(err, ErrGuestServiceTokenExists) {
		t.Fatalf("create over a revoked record = %v, want exists", err)
	}
	errNotRevoked := errors.New("not revoked")
	reopen := func() error {
		now := time.Now().UTC()
		_, err := s.UpdateGuestServiceToken("U1", func(g *GuestServiceToken) error {
			if g.Status != GuestTokenRevoked {
				return errNotRevoked
			}
			g.Status, g.TokenID, g.SecretHash, g.PolicyAdded, g.Detail = GuestTokenPending, "", "", false, ""
			g.RevokedAt, g.RevokeReason, g.CreatedAt, g.ReissuedAt = nil, "", now, &now
			return nil
		})
		return err
	}
	if err := reopen(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetGuestServiceToken("U1")
	if got.Status != GuestTokenPending || got.TokenID != "" || got.RevokedAt != nil || got.ReissuedAt == nil || got.Name != "aorg-guest-U1" {
		t.Fatalf("reopened record = %+v", got)
	}
	if err := reopen(); !errors.Is(err, errNotRevoked) {
		t.Fatalf("second reopen = %v, want the status check to refuse a pending record", err)
	}
	list, _ := s.ListGuestServiceTokens()
	if len(list) != 1 || list[0].ReissuedAt == nil {
		t.Fatalf("list = %+v", list)
	}
}
