// file: internal/server/handlers/audiobooks/handler_permission.go
// version: 1.0.0
// guid: b2ad1676-bf02-4c1d-aa05-4a9b36df896c
// last-edited: 2026-10-25

package audiobookshandler

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// PermissionCheck reports whether the request's caller holds p; when it does
// not, it has written the 401/403 response and the handler stops.
type PermissionCheck func(c *gin.Context, p auth.Permission) bool

// SetPermissionCheck sets how the handler checks a permission it learns it
// needs from the request body: the server passes the same semantics as its
// route middleware (auth disabled, first-run bypass). nil keeps the strict
// check.
func (h *Handler) SetPermissionCheck(check PermissionCheck) {
	h.permCheck = check
}

// StrictPermissionCheck is servermiddleware.CheckPermission with no first-run
// bypass: the caller must hold p. It fails closed for a handler the server did
// not wire.
func StrictPermissionCheck(c *gin.Context, p auth.Permission) bool {
	return servermiddleware.CheckPermission(nil, c, p)
}

// permitted checks p with the installed check, or the strict one when none is
// installed. False means the 401/403 is written.
func (h *Handler) permitted(c *gin.Context, p auth.Permission) bool {
	check := h.permCheck
	if check == nil {
		check = StrictPermissionCheck
	}
	return check(c, p)
}
