// file: internal/server/handlers/audiobooks/handler_permission.go
// version: 1.2.0
// guid: b2ad1676-bf02-4c1d-aa05-4a9b36df896c
// last-edited: 2026-10-25

package audiobookshandler

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// PermissionCheck reports whether the request's caller holds p; when it does
// not, it has written the 401/403 response and the handler stops.
type PermissionCheck func(c *gin.Context, p auth.Permission) bool

// PermissionHolds reports whether the request's caller holds p, writing no
// response: the question PermissionCheck asks, for a decision that is not a
// refusal of the whole request.
type PermissionHolds func(c *gin.Context, p auth.Permission) bool

// SetPermissionHolds sets how the handler asks, without refusing, whether the
// caller holds a permission: the server passes the same semantics as its route
// middleware (auth disabled, first-run bypass). nil keeps the strict answer.
func (h *Handler) SetPermissionHolds(holds PermissionHolds) {
	h.permHolds = holds
}

// StrictPermissionHolds is auth.Can on the request: no first-run bypass, no
// auth-disabled bypass. It fails closed for a handler the server did not wire.
func StrictPermissionHolds(c *gin.Context, p auth.Permission) bool {
	return auth.Can(c.Request.Context(), p)
}

// trashGrants is what the caller may do to books' place in the library, for the
// batch service to judge each row against (batch.TrashGrants).
func (h *Handler) trashGrants(c *gin.Context) *batch.TrashGrants {
	holds := h.permHolds
	if holds == nil {
		holds = StrictPermissionHolds
	}
	return &batch.TrashGrants{
		Trash:   holds(c, auth.PermLibraryDelete),
		Restore: holds(c, auth.PermLibraryOrganize),
	}
}

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

// permittedForTrashEffects checks the permissions a batch request needs beyond
// the route's library.edit_metadata, for what it does to books' place in the
// library (batch.TrashEffects), each the one the single-book route for the same
// act requires: a hard delete or a soft delete (move to the trash) needs
// library.delete (DELETE /audiobooks/:id), a restore needs library.organize
// (POST /audiobooks/:id/restore). It is called after the request is bound, on
// the effects of the very request about to be executed, so what is checked is
// what runs. False means the 401/403 is written and nothing may run.
func (h *Handler) permittedForTrashEffects(c *gin.Context, e batch.TrashEffects) bool {
	if (e.HardDelete || e.Trash) && !h.permitted(c, auth.PermLibraryDelete) {
		return false
	}
	if e.Restore && !h.permitted(c, auth.PermLibraryOrganize) {
		return false
	}
	return true
}
