// file: internal/auth/first_admin_test.go
// version: 1.0.0
// guid: 8d41f0a7-3c26-4e19-b5a8-7f20c6d913e4
// last-edited: 2026-10-18

package auth

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestFirstActiveAdmin(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	admin := []string{SeedRoleAdmin}
	viewer := []string{SeedRoleViewer}

	cases := []struct {
		name  string
		users []database.User
		want  string // "" = nil
	}{
		{"no users", nil, ""},
		{"no admin", []database.User{{ID: "u1", Roles: viewer, CreatedAt: t0}}, ""},
		{"earliest admin wins", []database.User{
			{ID: "alice", Roles: admin, CreatedAt: t0.Add(time.Hour)},
			{ID: "bob", Roles: admin, CreatedAt: t0},
		}, "bob"},
		{"tie on CreatedAt broken by smaller ID", []database.User{
			{ID: "u-b", Roles: admin, CreatedAt: t0},
			{ID: "u-a", Roles: admin, CreatedAt: t0},
		}, "u-a"},
		{"inactive admin skipped", []database.User{
			{ID: "old", Roles: admin, Status: "inactive", CreatedAt: t0},
			{ID: "new", Roles: admin, Status: "active", CreatedAt: t0.Add(time.Hour)},
		}, "new"},
		{"revoked admin skipped, case and space insensitive", []database.User{
			{ID: "old", Roles: admin, Status: " Revoked ", CreatedAt: t0},
			{ID: "new", Roles: admin, Status: " ACTIVE ", CreatedAt: t0.Add(time.Hour)},
		}, "new"},
		{"empty status counts as active", []database.User{
			{ID: "legacy", Roles: admin, CreatedAt: t0},
		}, "legacy"},
		{"non-admin earlier than admin ignored", []database.User{
			{ID: "v", Roles: viewer, CreatedAt: t0},
			{ID: "a", Roles: []string{SeedRoleViewer, SeedRoleAdmin}, CreatedAt: t0.Add(time.Hour)},
		}, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstActiveAdmin(tc.users)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("got %q, want nil", got.ID)
				}
				return
			}
			if got == nil || got.ID != tc.want {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
}
