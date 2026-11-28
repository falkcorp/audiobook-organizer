// file: internal/server/gone.go
// version: 1.0.0
// guid: 5c8e1a37-d2f4-4b69-8a05-3e7b9c0d6f21
// last-edited: 2026-10-12

package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// gone returns the handler for a route that has been retired. It answers 410
// Gone with the one JSON shape every retired route uses, so a client (or the
// route-table tests) never has to learn a second one:
//
//	{"error": "gone", "message": "..."}
//
// replacement names where the caller should go instead, written as it should
// read in the message (for example "GET /api/v1/operations/timeline"). Pass ""
// when nothing replaces the route.
//
// Register the stub on the same route group the live route used. Auth and
// permission middleware then still run first, so a caller who was not allowed
// to use the route gets the same 401/403 as before and only an allowed caller
// learns that it is gone.
func gone(replacement string) gin.HandlerFunc {
	message := "this endpoint has been removed and has no replacement"
	if replacement != "" {
		message = "this endpoint has been removed; use " + replacement + " instead"
	}
	return func(c *gin.Context) {
		c.JSON(http.StatusGone, gin.H{"error": "gone", "message": message})
	}
}

// goneToAUser is gone for a route whose live handler refused a caller with no
// user (the auth-disabled principal) before anything else, as HEAD's own
// account routes do (legacyauth.RespondNoUser, 401). That refusal is kept, so
// the set of callers the route refuses is the live handler's, and only a
// caller the live handler answered learns that the route is gone.
func goneToAUser(replacement string) gin.HandlerFunc {
	retired := gone(replacement)
	return func(c *gin.Context) {
		if _, ok := servermiddleware.CurrentUser(c); !ok {
			legacyauth.RespondNoUser(c, "not authenticated")
			return
		}
		retired(c)
	}
}
