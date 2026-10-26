// file: internal/auth/user_active.go
// version: 2.0.0
// guid: 5e2a9c14-7b30-4d68-a1f5-c80d3e96b247
// last-edited: 2026-10-25

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
