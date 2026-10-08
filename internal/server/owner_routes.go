// file: internal/server/owner_routes.go
// version: 1.3.0
// guid: 3e9a6c25-1f74-4b8d-a0c2-5d7e8b4f1a63
// last-edited: 2026-10-07

package server

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// ownerGateWhenOwnerSet is the owner gate for routes that can UNSET the
// owner: system reset and factory reset (config.ResetToDefaults clears
// owner_email) and backup restore (the restored database can predate it).
// Unset, the first owner_email may be claimed by whoever signs in through
// Access as that email (plan D15), so letting a non-owner reach these would
// hand ownership to the next Access user. While owner_email is set they need
// the owner; before any owner exists they keep their ordinary guards. Not
// skipped with local auth off. The permission leg is left to the route's own
// s.perm / RequireAdmin.
func (s *Server) ownerGateWhenOwnerSet() gin.HandlerFunc {
	ownerEmail := func() string { return config.Snapshot().OwnerEmail }
	return servermiddleware.RequireOwner(ownerEmail, "", func(*gin.Context) bool {
		return strings.TrimSpace(ownerEmail()) != ""
	})
}
