// file: internal/server/middleware/cfaccess_hooks.go
// version: 1.0.0
// guid: 4d7a2e91-6c03-4b58-9e1f-8a3b5c0d7e24
// last-edited: 2026-11-11

package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
)

// KeyPresented reports whether r presents an "abk_" API key anywhere /api/v1
// accepts a credential (APIKeyFromRequest). It is cfaccess.Hooks.KeyPresented
// and the service-token bouncer's key test: internal/cfaccess cannot import
// this package, so the gin chain hands it the one rule for "the key wins".
func KeyPresented(r *http.Request) bool { return APIKeyFromRequest(r) != "" }

// CFAccessHooks is what internal/cfaccess's gin binder takes from this chain:
// the key test, the setter of the load-bearing contextUserKey (RequireAuth
// skips its own session check for a request that has it) and the role union
// of EffectivePermissionsFor. Deleted with the binder at F4-08.
func CFAccessHooks(roles roleResolver) cfaccess.Hooks {
	return cfaccess.Hooks{
		KeyPresented: KeyPresented,
		BindUser:     func(c *gin.Context, user *database.User) { c.Set(contextUserKey, user) },
		Permissions:  func(user *database.User) []auth.Permission { return EffectivePermissionsFor(roles, user) },
	}
}
