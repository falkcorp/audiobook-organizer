// file: internal/server/middleware/bridge_user.go
// version: 0.1.0
// guid: 6e1d9a47-3b82-4c15-a9f0-2d7c4b8e1a53
// last-edited: 2026-11-21

package middleware

import (
	"github.com/gin-gonic/gin"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// BindCurrentUser sets the user CurrentUser reads. It is the setter the
// legacyauth bridge's Authenticate adapter takes (F4-08 B7): under core
// identity the user is the one core's stage 12 resolved, mapped to HEAD's
// record, and only a stage that has fully verified an identity may call it
// (the key is load-bearing: see contextUserKey).
func BindCurrentUser(c *gin.Context, user *database.User) { c.Set(contextUserKey, user) }
