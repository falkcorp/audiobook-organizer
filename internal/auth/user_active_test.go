// file: internal/auth/user_active_test.go
// version: 2.0.0
// guid: 8d41f0a7-3c26-4e19-b5a8-7f20c6d913e4
// last-edited: 2026-10-25

package auth

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestUserIsActive(t *testing.T) {
	cases := []struct {
		name string
		user *database.User
		want bool
	}{
		{"nil user", nil, false},
		{"empty status counts as active (row written before the field existed)", &database.User{}, true},
		{"active", &database.User{Status: "active"}, true},
		{"case and space insensitive", &database.User{Status: " ACTIVE "}, true},
		{"inactive", &database.User{Status: "inactive"}, false},
		{"revoked, case and space insensitive", &database.User{Status: " Revoked "}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UserIsActive(tc.user); got != tc.want {
				t.Fatalf("UserIsActive = %v, want %v", got, tc.want)
			}
		})
	}
}
