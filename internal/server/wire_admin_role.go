// file: internal/server/wire_admin_role.go
// version: 1.0.0
// guid: 8ecd4031-8da3-4bae-9ef0-5066fcaab271
// last-edited: 2026-10-30

package server

import (
	"path"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Admin-role routes and EnableAuth=false.
//
// servermiddleware.RequireAdmin answers 401 to every request when
// authentication is disabled, because no request carries a user. s.perm passes
// everything in that mode (every request is then anonymous with full access),
// so a route behind RequireAdmin is the one part of the API a no-auth run
// cannot reach. Whether such a route SHOULD open in that mode is decided per
// route, by one rule:
//
//   - read-only admin introspection (a route that only returns state) opens
//     with authentication off. It is wired through adminRole, which is
//     RequireAdmin with authentication on and a pass-through with it off, read
//     once at wiring like s.perm. The master-list routes use adminRole too.
//   - everything else keeps servermiddleware.RequireAdmin and so keeps
//     answering 401 with authentication off. That is every route that deletes
//     or rewrites data (the store wipes, the debug field edits and their
//     undo, the retired wipe and cache-flush stubs, which are the same verbs
//     behind a 410) and every credential route (the guest service token list,
//     plan and revoke: they name guests and call Cloudflare, so a read of them
//     is not introspection). An unauthenticated caller on the LAN must not be
//     able to wipe the store or reach a credential surface just because the
//     operator turned login off for a dev run.
//
// RequireAdmin itself is deliberately not changed: it is the right answer for
// the second group. The routes of both kinds are registered through
// adminRoute / adminGroup, which record them in Server.adminRoutes;
// admin_routes_test.go fails when a route behind RequireAdmin is not on its
// reviewed allowlist, and when RequireAdmin is wired anywhere else, so a new
// admin route has to be classified. The per-route table lives in
// docs/decisions/2026-10-30-admin-routes-auth-off.md.

// adminRouteKind says which side of the rule above a recorded route is on.
type adminRouteKind string

const (
	// adminRouteReadOnly routes only return state; they open with auth off.
	adminRouteReadOnly adminRouteKind = "read-only"
	// adminRouteAlwaysAuthenticated routes delete or rewrite data, or are a
	// credential surface; they answer 401 with auth off.
	adminRouteAlwaysAuthenticated adminRouteKind = "always-authenticated"
)

var adminRoutesMu sync.Mutex

// recordAdminRoute notes "METHOD /full/path" as admin-gated of the given kind.
func (s *Server) recordAdminRoute(method, fullPath string, kind adminRouteKind) {
	adminRoutesMu.Lock()
	defer adminRoutesMu.Unlock()
	if s.adminRoutes == nil {
		s.adminRoutes = make(map[string]adminRouteKind)
	}
	s.adminRoutes[method+" "+fullPath] = kind
}

// adminRole is the admin-role gate for a route that must also work with
// authentication disabled: RequireAdmin with authentication on, a pass-through
// with it off (read once, at wiring, like s.perm). Only read-only introspection
// belongs behind it; see the rule at the top of this file.
func (s *Server) adminRole() gin.HandlerFunc {
	if !config.AppConfig.EnableAuth {
		return func(c *gin.Context) { c.Next() }
	}
	return servermiddleware.RequireAdmin()
}

// adminGate returns the middleware for a route of the given kind.
func (s *Server) adminGate(kind adminRouteKind) gin.HandlerFunc {
	if kind == adminRouteReadOnly {
		return s.adminRole()
	}
	return servermiddleware.RequireAdmin()
}

// adminGroup returns a one-route child group of g whose only middleware is the
// admin gate for kind, and records the route. The caller registers the route
// on the returned group with the same method and rel (handlers directly, or
// through s.credRoute so the credential guard runs after the admin gate, as it
// did when the gate was a group-wide Use).
func (s *Server) adminGroup(g *gin.RouterGroup, kind adminRouteKind, method, rel string) *gin.RouterGroup {
	s.recordAdminRoute(method, path.Join(g.BasePath(), rel), kind)
	sub := g.Group("")
	sub.Use(s.adminGate(kind))
	return sub
}

// adminRoute registers method rel on g behind the admin gate for kind.
func (s *Server) adminRoute(g *gin.RouterGroup, kind adminRouteKind, method, rel string, h ...gin.HandlerFunc) {
	s.adminGroup(g, kind, method, rel).Handle(method, rel, h...)
}
