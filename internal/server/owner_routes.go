// file: internal/server/owner_routes.go
// version: 1.2.0
// guid: 3e9a6c25-1f74-4b8d-a0c2-5d7e8b4f1a63
// last-edited: 2026-10-07

package server

import (
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Owner-only routes (2026-10-07): actions that remove, overwrite or repoint
// tracks in the owner's iTunes library, or turn off its safety checks, are
// registered ONLY through ownerRoute. It attaches the one shared gate
// (servermiddleware.RequireOwner: a verified Cloudflare Access JWT for the
// configured owner_email, never an API key, password, SSO, temp-login or
// invite session) plus PermIntegrationsManage, the admin-level permission the
// other whole-library iTunes endpoints (library upload, restore, download)
// already required; library.edit_metadata, which these routes used before,
// is held by the editor role. owner_routes_test.go walks the router and fails
// when a state-changing /api/v1/itunes/ route is neither recorded here nor on
// its reviewed list with a reason.
//
// The route list and why each route is or is not owner-only:
// docs/plans/2026-10-07-apikey-expiry-and-privilege.md (D14).

// ownerRouteKind says which requests of a recorded route are owner-only.
type ownerRouteKind string

const (
	// ownerRouteAlways: every request.
	ownerRouteAlways ownerRouteKind = "always"
	// ownerRouteApply: every request except a dry-run preview
	// (itunesPreviewOnly), which keeps the route's base permission.
	ownerRouteApply ownerRouteKind = "apply"
)

// ownerPerm is the permission an owner-only request must also hold.
const ownerPerm = auth.PermIntegrationsManage

// ownerRoute registers an owner-only route. base is the permission for the
// requests the owner gate does not cover (a preview of an ownerRouteApply
// route); for ownerRouteAlways it is ownerPerm.
func (s *Server) ownerRoute(g *gin.RouterGroup, method, rel string, kind ownerRouteKind, h ...gin.HandlerFunc) {
	credRoutesMu.Lock()
	if s.ownerRoutes == nil {
		s.ownerRoutes = make(map[string]ownerRouteKind)
	}
	s.ownerRoutes[method+" "+path.Join(g.BasePath(), rel)] = kind
	credRoutesMu.Unlock()

	base := ownerPerm
	var applies func(*gin.Context) bool
	if kind == ownerRouteApply {
		base = auth.PermLibraryEditMetadata
		applies = func(c *gin.Context) bool { return !itunesPreviewOnly(c) }
	}
	// With local auth off s.perm is a no-op and nobody holds a role, so the
	// gate's permission leg is skipped the same way; the owner proof is not.
	gatePerm := ownerPerm
	if !config.AppConfig.EnableAuth {
		gatePerm = ""
	}
	chain := []gin.HandlerFunc{
		s.perm(base),
		servermiddleware.RequireOwner(func() string { return config.Snapshot().OwnerEmail }, gatePerm, applies),
	}
	g.Handle(method, rel, append(chain, h...)...)
}

// itunesPreviewOnly reports whether an iTunes library request asks for a
// dry-run preview. It is the ONE reading of dry_run: the handlers of the
// ownerRouteApply routes call it to decide whether to write, and the owner
// gate calls it to decide whether to check, so the two cannot disagree
// about a request (the 2026-10-07 review's parser-differential class).
// Anything but dry_run=true is an apply.
func itunesPreviewOnly(c *gin.Context) bool {
	return c.Query("dry_run") == "true"
}

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
