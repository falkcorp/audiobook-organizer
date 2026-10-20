// file: internal/server/bootstrap_admin_test.go
// version: 1.0.0
// guid: 7b2a193f-fbf5-4b5e-8135-6f8d8fc03b4b
// last-edited: 2026-10-19

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newBootstrapAdminStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, _, err := auth.SeedRoles(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Nit 6: bootstrap must end with an ACTIVE admin it can mint a key for. A
// locked "admin" account (holding admin@localhost) used to be returned as
// the admin, so the key it minted was refused; and creating a fresh one
// collided with that row's username and address.
func TestFindOrCreateAdminUser(t *testing.T) {
	t.Run("fresh store creates admin", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		u, pw, err := findOrCreateAdminUser(s)
		if err != nil || u == nil || pw == "" || u.Username != "admin" || u.Status != "active" {
			t.Fatalf("got %+v, pw set=%v, %v", u, pw != "", err)
		}
	})
	t.Run("an active admin is reused", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		owner, err := s.CreateUser("owner", "owner@example.com", "bcrypt", "h", []string{"admin"}, "active")
		if err != nil {
			t.Fatal(err)
		}
		u, pw, err := findOrCreateAdminUser(s)
		if err != nil || u == nil || u.ID != owner.ID || pw != "" {
			t.Fatalf("got %+v, %v; want the existing admin", u, err)
		}
	})
	t.Run("a locked admin holding admin@localhost is skipped", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		old, err := s.CreateUser("admin", "admin@localhost", "bcrypt", "h", []string{"admin"}, "active")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetUserStatus(old.ID, "locked"); err != nil {
			t.Fatal(err)
		}
		u, pw, err := findOrCreateAdminUser(s)
		if err != nil || u == nil {
			t.Fatalf("bootstrap refused: %v", err)
		}
		if u.ID == old.ID || u.Status != "active" || pw == "" {
			t.Fatalf("got %+v; want a new active admin, not the locked one", u)
		}
		if again, _, err := findOrCreateAdminUser(s); err != nil || again.ID != u.ID {
			t.Fatalf("a second bootstrap must reuse the new admin: %+v, %v", again, err)
		}
	})
	t.Run("a non-admin named admin does not block", func(t *testing.T) {
		s := newBootstrapAdminStore(t)
		if _, err := s.CreateUser("admin", "admin@localhost", "bcrypt", "h", []string{"viewer"}, "active"); err != nil {
			t.Fatal(err)
		}
		u, _, err := findOrCreateAdminUser(s)
		if err != nil || u == nil || u.Username == "admin" || u.Status != "active" {
			t.Fatalf("got %+v, %v; want a new admin under a free name", u, err)
		}
	})
}
