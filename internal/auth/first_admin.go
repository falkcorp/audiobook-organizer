// file: internal/auth/first_admin.go
// version: 1.0.0
// guid: 5e2a9c14-7b30-4d68-a1f5-c80d3e96b247
// last-edited: 2026-10-18

package auth

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// UserIsActive reports whether u may sign in, by the same rule the auth
// middleware applies to a session's user: a status that is empty (a row written
// before the field existed) or "active", compared case-insensitively after
// trimming. Anything else ("inactive", "revoked", ...) is not active.
func UserIsActive(u *database.User) bool {
	if u == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(u.Status))
	return status == "" || status == "active"
}

// FirstActiveAdmin returns the active user holding the admin role (SeedRoleAdmin)
// with the earliest CreatedAt, ties broken by the smaller ID, or nil when there
// is none. "First" is the account the install was set up with, so it is the one
// a fallback such as the dynamic-collection evaluator can name without a
// setting. The result points into a copy, never into users.
func FirstActiveAdmin(users []database.User) *database.User {
	var best *database.User
	for i := range users {
		u := &users[i]
		if !UserIsActive(u) || !hasRole(u, SeedRoleAdmin) {
			continue
		}
		if best == nil ||
			u.CreatedAt.Before(best.CreatedAt) ||
			(u.CreatedAt.Equal(best.CreatedAt) && u.ID < best.ID) {
			best = u
		}
	}
	if best == nil {
		return nil
	}
	cp := *best
	return &cp
}

func hasRole(u *database.User, role string) bool {
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}
