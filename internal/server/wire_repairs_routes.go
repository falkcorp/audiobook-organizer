// file: internal/server/wire_repairs_routes.go
// version: 1.10.0
// guid: 4a9c1e62-3f75-4d08-b8a6-5e2d7c0b9f13
// last-edited: 2026-10-26

package server

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	repairshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/repairs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// wireRepairsRoutes registers the Repairs lane routes on the protected group.
// The route gates are library.view for the two list reads and the rows read,
// and library.edit_metadata for starting a plan or an apply (which change, or
// lead to changing, the library), matching the review-queue mutations. They
// are only the floor: once the handler knows the fixer it checks every
// permission the fixer requires (repairs.RequiredPermissions: the lane's
// library.edit_metadata, or what a repairs.PermissionScoped fixer names, e.g.
// the missing-files fixer's settings.manage), with the same semantics as
// s.perm, on the plan, apply and owner-apply POSTs and also on the rows read,
// because plan rows carry other users' data and a guest holds library.view.
// GET /repairs lists every fixer to a library.view caller but leaves the
// last-run pointers off the ones the caller may not use.
func (s *Server) wireRepairsRoutes(protected *gin.RouterGroup) {
	var enq repairshandler.Enqueuer
	if s.opRegistry != nil {
		enq = s.opRegistry
	}
	h := repairshandler.New(s.repairFixers, enq, s.storeForWiring()).WithPermissionCheck(s.permHolds())
	protected.GET("/repairs", s.perm(auth.PermLibraryView), h.ListFixers)
	protected.GET("/repairs/owner-status", s.perm(auth.PermLibraryView), h.GetOwnerStatus)
	protected.POST("/repairs/:fixer/plan", s.perm(auth.PermLibraryEditMetadata), h.StartPlan)
	protected.GET("/repairs/:fixer/plan/:op_id/rows", s.perm(auth.PermLibraryView), h.ListPlanRows)
	protected.POST("/repairs/:fixer/apply", s.perm(auth.PermLibraryEditMetadata), h.StartApply)
	// The owner's own apply of one owner-only row: the handler also requires
	// a verified Cloudflare Access sign-in as owner_email, the admin role and
	// a same-origin request.
	protected.POST("/repairs/:fixer/owner-apply", s.perm(auth.PermLibraryEditMetadata), h.OwnerApply)
}

// permCheck is s.perm as a check a handler runs inline, for a permission it
// learns only from the request: every permission is held with auth disabled
// (read once, as s.perm does) and while no user exists (first run). When the
// caller does not hold the permission it writes the 401/403 itself, which is
// what the ops v3 handlers (opsV3Authz.Require) rely on: they return on false
// and write nothing else. A caller that must ask without answering the
// request uses permHolds.
func (s *Server) permCheck() func(*gin.Context, auth.Permission) bool {
	holds := s.permHolds()
	return func(c *gin.Context, p auth.Permission) bool {
		if holds(c, p) {
			return true
		}
		servermiddleware.RespondPermissionDenied(c, p)
		return false
	}
}

// permHolds is permCheck's question without the answer: it reports whether the
// caller holds p and writes nothing, for a handler that decides what to do on
// a refusal itself (the Repairs lane, which lists a fixer the caller may not
// use without its last runs, and refuses a read with its own 403).
func (s *Server) permHolds() repairshandler.PermissionCheck {
	if !config.AppConfig.EnableAuth {
		return func(*gin.Context, auth.Permission) bool { return true }
	}
	store := s.storeForWiring()
	return func(c *gin.Context, p auth.Permission) bool {
		return servermiddleware.HoldsPermission(store, c, p)
	}
}

// permHoldsLive is permHolds for a request that outlives its permission check:
// an event stream, which stays open for hours. It answers permHolds' question
// (the permissions the request was authenticated with, which for an API key are
// already narrowed to the key's scopes) and then asks the account's current
// roles, so a caller whose role was lowered, or whose account was disabled or
// removed after the stream opened, stops holding what it lost. It can only
// narrow: a permission granted after the stream opened is not held until the
// caller reconnects. Auth disabled and a first run hold everything, as
// permHolds does.
func (s *Server) permHoldsLive() repairshandler.PermissionCheck {
	connected := s.permHolds()
	if !config.AppConfig.EnableAuth {
		return connected
	}
	store := s.storeForWiring()
	return func(c *gin.Context, p auth.Permission) bool {
		if !connected(c, p) {
			return false
		}
		if n, err := store.CountUsers(); err == nil && n == 0 {
			return true
		}
		u, ok := auth.UserFromContext(c.Request.Context())
		if !ok || u == nil {
			return false
		}
		fresh, err := store.GetUserByID(u.ID)
		if err != nil || fresh == nil {
			return false
		}
		if status := strings.ToLower(strings.TrimSpace(fresh.Status)); status != "" && status != "active" {
			return false
		}
		return slices.Contains(servermiddleware.EffectivePermissionsFor(store, fresh), p)
	}
}

// repairsOpAccess is the check the generic operations routes apply to a
// Repairs operation's data (result, logs, change journal, listings): the
// caller must hold the op's fixer's permissions, as the Repairs routes require
// (see opaccess.Repairs). It asks permHolds, so auth disabled and a first run
// hold everything, as everywhere else.
//
// There is one instance per server (every route family asks the same one), so a
// route family cannot be left on a stale or differently built check.
func (s *Server) repairsOpAccess() *opaccess.Repairs {
	s.repairsOpAccessOnce.Do(func() {
		s.repairsOpAccessCheck = s.newRepairsOpAccess(s.permHolds()).WithLiveHolds(s.permHoldsLive())
	})
	return s.repairsOpAccessCheck
}

// newRepairsOpAccess builds the opaccess check over this server's fixers and
// store for a given way of answering "does the caller hold p". repairsOpAccess
// passes the request's own (permHolds); a job that runs after its request is
// gone (the diagnostics export) passes the permissions of the account that
// queued it (opActorAccess).
func (s *Server) newRepairsOpAccess(holds func(c *gin.Context, p auth.Permission) bool) *opaccess.Repairs {
	// A nil *repairs.Registry must not become a non-nil interface: the check
	// then refuses every repairs op rather than calling Get on nil.
	var fixers opaccess.FixerLookup
	if s.repairFixers != nil {
		fixers = s.repairFixers
	}
	// The op lookup scopes a journal row journaled before Source stamping by
	// its op row. Same typed-nil care: no store leaves the check unwired.
	var ops opaccess.OpLookup
	if s.store != nil {
		ops = s.Ops()
	}
	return opaccess.NewRepairs(fixers, ops, holds)
}

// opActorAccess is the opaccess reader for the account that queued operation
// opID, for a job that runs after its request is gone. It reads what the
// account holds now, from the actor stamped on the op row, never from the
// job's params: params can be set by any caller of the generic start routes,
// the stamped actor cannot. It fails closed: an op with no actor (started
// through a route that does not stamp one), an actor that no longer exists or
// holds no role, or a store failure, holds nothing, so every Repairs op and
// every user-data result is refused. Auth disabled, and a first run with no
// users yet, hold everything, as permHolds does.
func (s *Server) opActorAccess(opID string) *opaccess.Reader {
	if !config.AppConfig.EnableAuth {
		return s.newRepairsOpAccess(func(*gin.Context, auth.Permission) bool { return true }).For(nil)
	}
	store := s.storeForWiring()
	held := map[auth.Permission]bool{}
	if n, err := store.CountUsers(); err == nil && n == 0 {
		return s.newRepairsOpAccess(func(*gin.Context, auth.Permission) bool { return true }).For(nil)
	}
	if row, err := store.GetOperationV2(opID); err == nil && row != nil && row.ActorUserID != nil {
		if user, err := store.GetUserByID(*row.ActorUserID); err == nil && user != nil {
			for _, p := range servermiddleware.EffectivePermissionsFor(store, user) {
				held[p] = true
			}
		}
	}
	return s.newRepairsOpAccess(func(_ *gin.Context, p auth.Permission) bool { return held[p] }).For(nil)
}

// othersUserDataAccess reports whether the request's caller may read other
// users' own data -- their listening state and positions -- where a
// library.view route serves it alongside library data (the merge journals'
// progress, the trash purge's activity row). users.manage, the same
// permission opaccess.Reader.MayReadChange requires for a user-data journal
// row (owner decision, 2026-10-24). It asks permHolds, so auth disabled and a
// first run hold it, as everywhere else.
func (s *Server) othersUserDataAccess() func(*gin.Context) bool {
	holds := s.permHolds()
	return func(c *gin.Context) bool { return holds(c, auth.PermUsersManage) }
}
