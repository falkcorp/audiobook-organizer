// file: internal/server/gone.go
// version: 1.0.0
// guid: 5c8e1a37-d2f4-4b69-8a05-3e7b9c0d6f21
// last-edited: 2026-10-12

package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
