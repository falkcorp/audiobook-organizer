// file: internal/database/guest_tokens_test.go
// version: 1.0.0
// guid: 7a1f4d92-3c68-4b05-8e27-f9b0c5d2a841
// last-edited: 2026-10-23

package database

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
