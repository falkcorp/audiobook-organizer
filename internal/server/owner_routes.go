// file: internal/server/owner_routes.go
// version: 1.4.1
// guid: 3e9a6c25-1f74-4b8d-a0c2-5d7e8b4f1a63
// last-edited: 2026-11-09

package server

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// ownerGateWhenOwnerSet is the owner gate for routes that can UNSET the owner:
// backup restore (the restored database can predate it). HEAD's system and
// factory reset routes, which cleared owner_email too, are gone (R1-25: a
// reset runs offline only); F4's core reset route takes the gate back when it
// lands. Unset, the first owner_email may be claimed by whoever signs in
// through Access as that email (plan D15), so letting a non-owner reach these
// would hand ownership to the next Access user. While owner_email is set they
// need the owner; before any owner exists they keep their ordinary guards. Not
// skipped with local auth off. The permission leg is left to the route's own
// s.perm / RequireAdmin.
func (s *Server) ownerGateWhenOwnerSet() gin.HandlerFunc {
	ownerEmail := func() string { return config.Snapshot().OwnerEmail }
	return servermiddleware.RequireOwner(ownerEmail, "", func(*gin.Context) bool {
		return strings.TrimSpace(ownerEmail()) != ""
	})
}

// ownerCredRoute is credRoute for a route behind ownerGateWhenOwnerSet. The
// owner gate runs after every handler of h but the last, so the route's own
// permission leg (its s.perm, or the admin gate adminGroup added) is checked
// first, as at every owner route. The route is recorded in s.ownerRoutes
// ("METHOD /full/path"), so the legacy manifest's Guards.Owner rows are
// compared with the registrations exactly (legacy_manifest_test.go).
func (s *Server) ownerCredRoute(g *gin.RouterGroup, method, rel string, h ...gin.HandlerFunc) {
	credRoutesMu.Lock()
	if s.ownerRoutes == nil {
		s.ownerRoutes = make(map[string]bool)
	}
	s.ownerRoutes[routeKey(g, method, rel)] = true
	credRoutesMu.Unlock()
	chain := make([]gin.HandlerFunc, 0, len(h)+1)
	if n := len(h); n > 0 {
		chain = append(chain, h[:n-1]...)
		chain = append(chain, s.ownerGateWhenOwnerSet(), h[n-1])
	} else {
		chain = append(chain, s.ownerGateWhenOwnerSet())
	}
	s.credRoute(g, method, rel, chain...)
}
