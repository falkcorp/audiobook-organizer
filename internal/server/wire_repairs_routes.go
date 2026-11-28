// file: internal/server/wire_repairs_routes.go
// version: 1.18.3
// guid: 4a9c1e62-3f75-4d08-b8a6-5e2d7c0b9f13
// last-edited: 2026-11-21

package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	repairshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
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
	h := repairshandler.New(s.repairFixers, enq, s.storeForWiring()).WithPermissionCheck(s.permHolds()).WithOpAccess(s.repairsOpAccess())
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
	// Under core identity (F4-08 B7) auth off is the auth-disabled principal,
	// not books' EnableAuth, and the atom is judged through the principal stage
	// 12 bound; there is no first-run bypass. A context the identity's Bind did
	// not mark holds nothing there: HEAD's chain would count books' user rows,
	// which M3 drains, and take its first-run bypass on every migrated install.
	if s.identity != nil {
		return func(c *gin.Context, p auth.Permission) bool {
			held, bridged := legacyauth.HeldOrAuthOff(c.Request.Context(), string(p))
			return bridged && held
		}
	}
	head := s.permHoldsHead()
	return func(c *gin.Context, p auth.Permission) bool {
		return head(c, p)
	}
}

// permHoldsHead is permHolds under HEAD's own chain (LegacyAuthGin): books'
// EnableAuth read once when the server wires it, and HoldsPermission's
// first-run bypass.
func (s *Server) permHoldsHead() repairshandler.PermissionCheck {
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
	authOn := config.Snapshot().EnableAuth
	store := s.storeForWiring()
	return func(c *gin.Context, p auth.Permission) bool {
		// Under core identity (F4-08 B7) the recheck is core's: the principal is
		// re-resolved (Identity.Refresh), so a disabled, removed or demoted
		// account stops holding what it lost, as HEAD's recheck below did
		// against books' store, which is no longer the account record.
		if held, bridged := legacyauth.HeldLive(c.Request.Context(), string(p)); bridged {
			return held
		}
		if !authOn {
			return connected(c, p)
		}
		// The account count is read only for a request with no user on it (a
		// first run): an authenticated request has users by definition, and the
		// count walks the whole user keyspace.
		u, ok := auth.UserFromContext(c.Request.Context())
		if !ok || u == nil {
			return connected(c, p)
		}
		if !auth.Can(c.Request.Context(), p) {
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

// liveRecheckTTL is the shortest time between two live questions an open event
// stream puts to the store about its caller (opaccess.Repairs.WithLiveRecheck).
// It is also how long a role lowered after a stream opened keeps being served,
// and a disabled account or revoked key keeps its stream open.
const liveRecheckTTL = 5 * time.Second

// sessionStillStands reports whether the credential a request came in on still
// stands, for a stream that outlives it: its account exists and is active, the
// API key it came in on (if any) is not revoked, inactive or expired, and the
// session (if any) is not revoked or expired. It fails closed, a store failure
// included: the stream ends and the client reconnects, which re-authenticates
// it. Auth disabled, and a first run with no accounts yet, stand.
func (s *Server) sessionStillStands() func(c *gin.Context) bool {
	// Under core identity (F4-08 B10b2) the question goes to core: the
	// principal stage 12 bound is re-resolved (Identity.Refresh), which fails
	// when its user is disabled or removed, its session or key is gone or
	// expired, its credential epoch moved, or an extension's token ended, and
	// answers for the auth-disabled principal too, so books' EnableAuth is not
	// read. Books' store is no longer the account record there, so HEAD's reads
	// below would answer about rows M3 deletes. It fails closed: no principal
	// is not a first run (HEAD's no-accounts bypass reads books' user count),
	// it is a request core did not authenticate. Identity is the wiring's
	// (WithIdentity), never the request's.
	if id := s.identity; id != nil {
		return func(c *gin.Context) bool {
			p, ok := coreauth.PrincipalFrom(c.Request.Context())
			if !ok || p == nil || id.Refresh == nil {
				return false
			}
			np, err := id.Refresh(c.Request.Context(), p)
			return err == nil && np != nil
		}
	}
	if !config.Snapshot().EnableAuth {
		return func(*gin.Context) bool { return true }
	}
	store := s.storeForWiring()
	return func(c *gin.Context) bool {
		u, ok := auth.UserFromContext(c.Request.Context())
		if !ok || u == nil {
			n, err := store.CountUsers()
			return err == nil && n == 0
		}
		fresh, err := store.GetUserByID(u.ID)
		if err != nil || fresh == nil {
			return false
		}
		if status := strings.ToLower(strings.TrimSpace(fresh.Status)); status != "" && status != "active" {
			return false
		}
		if key, ok := servermiddleware.CurrentAPIKey(c); ok {
			k, err := store.GetAPIKey(key.ID)
			if err != nil || k == nil || k.Status == "revoked" || k.Status == "inactive" {
				return false
			}
			if _, _, refused := servermiddleware.APIKeyExpiryRefusal(k, time.Now()); refused {
				return false
			}
		}
		if sess, ok := servermiddleware.CurrentSession(c); ok {
			freshSess, err := store.GetSession(sess.ID)
			if err != nil || freshSess == nil || freshSess.Revoked || time.Now().After(freshSess.ExpiresAt) {
				return false
			}
			// An open stream outlives the request that authenticated it, so a
			// password change or reset after it opened must end it too: the
			// session's stamp against the user's epoch, both freshly read.
			if !freshSess.CredentialsCurrent(fresh) {
				return false
			}
		}
		return true
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
		s.repairsOpAccessCheck = s.newRepairsOpAccess(s.permHolds()).
			WithLiveHolds(s.permHoldsLive()).
			WithLiveSession(s.sessionStillStands()).
			WithLiveRecheck(liveRecheckTTL, nil)
	})
	return s.repairsOpAccessCheck
}

// newRepairsOpAccess builds the opaccess check over this server's fixers and
// store for a given way of answering "does the caller hold p". repairsOpAccess
// passes the request's own (permHolds); a job that runs after its request is
// gone (the diagnostics export) passes the permissions of the account that
// queued it (opActorHolds).
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

// opActorPermissions is what the request that queued op row could do, for a
// job that runs after its request is gone: the account's current roles, capped
// at the permission set the request held (row.ActorPermissions, stamped by
// middleware.ActorOptions: an API key's scopes already intersected with its
// roles). A row with no stamped set (written before 2026-10-26) is capped at
// the roles alone; a stamped empty set (non-nil, empty) is a request that held
// nothing, and caps the job at nothing. An op with no actor, an actor that no
// longer exists or is not active, or a store failure, holds nothing.
//
// This is the resolver for a server NOT under core identity, where the account
// is a books record. Under core identity opActorHolds resolves the actor with
// opActorPermissionsCore instead, which returns its errors: a read failure there
// must fail the job, not become the empty set this function's fail-closed
// contract returns. Nothing under identity may call this (it reads books' user
// rows, which M3 drains).
func (s *Server) opActorPermissions(ctx context.Context, row *database.OperationV2Row) map[auth.Permission]bool {
	held := map[auth.Permission]bool{}
	if row == nil || row.ActorUserID == nil {
		return held
	}
	store := s.storeForWiring()
	user, err := store.GetUserByID(*row.ActorUserID)
	if err != nil || user == nil {
		return held
	}
	if status := strings.ToLower(strings.TrimSpace(user.Status)); status != "" && status != "active" {
		return held
	}
	var scoped map[string]bool
	if row.ActorPermissions != nil {
		scoped = make(map[string]bool, len(row.ActorPermissions))
		for _, p := range row.ActorPermissions {
			scoped[p] = true
		}
	}
	for _, p := range servermiddleware.EffectivePermissionsFor(store, user) {
		if scoped == nil || scoped[string(p)] {
			held[p] = true
		}
	}
	return held
}

// errNoExportActor and errExportActorNotStanding are why a job that runs
// after its request cannot be built for anyone under core identity. Each fails
// the job with a message the user sees: a job narrowed to nothing would finish
// "successfully" with an empty result, which reads as data that is not there.
var (
	errNoExportActor = errors.New("this operation was started without an account, so there is no account to build " +
		"it for; a diagnostics export is built for the signed-in account that starts it, and cannot be started " +
		"while core authentication is disabled")
	errExportActorNotStanding = errors.New("the account that started this operation is not active, or no longer " +
		"exists; start it again from an active account")
)

// opActorPermissionsCore is opActorPermissions under core identity, where the
// account record is core's (books' user and role rows are drained by M3): the
// actor is read through core's identity view and must be active, and what its
// current roles hold is the checker core compiles for it (Compat.PrincipalFor,
// with no scope narrowing: the role permissions, at its current credential
// epoch), turned into atoms as stage 12 does (Bridge.AtomsSatisfied). That is
// capped at the stamped set exactly as HEAD caps it. Compat's method is not
// interactive, so a Sensitive scope (05-rbac §4.4) is never held through this
// path: the job is narrowed by it, never widened.
//
// An op with no actor is errNoExportActor (the diagnostics export start refuses
// such a request before it queues, diagnosticsStartCheck; what still reaches
// here is a start by a route that does not refuse it, such as the generic start
// routes with core's auth disabled); an actor core does not hold, holds
// as not active, or refuses a principal for is errExportActorNotStanding; a
// read failure is returned as it is. The map is empty whenever err is set.
func (s *Server) opActorPermissionsCore(ctx context.Context, row *database.OperationV2Row) (map[auth.Permission]bool, error) {
	held := map[auth.Permission]bool{}
	if row == nil || row.ActorUserID == nil {
		return held, errNoExportActor
	}
	id := s.identity
	if id == nil || id.Users == nil || id.Compat == nil || id.Bridge == nil {
		return held, errNoCoreUserView
	}
	u, err := id.Users.User(ctx, coreauth.UserID(*row.ActorUserID))
	switch {
	case errors.Is(err, coreauth.ErrNotFound), err == nil && u == nil:
		return held, errExportActorNotStanding
	case err != nil:
		return held, fmt.Errorf("read the account that started operation %s: %w", row.ID, err)
	case u.Status != coreauth.StatusActive:
		return held, errExportActorNotStanding
	}
	p, err := id.Compat.PrincipalFor(ctx, u, nil, u.CredentialEpoch, coreauth.ClientInfo{})
	if err != nil || p == nil || p.Perms == nil {
		return held, errExportActorNotStanding
	}
	var scoped map[string]bool
	if row.ActorPermissions != nil {
		scoped = make(map[string]bool, len(row.ActorPermissions))
		for _, perm := range row.ActorPermissions {
			scoped[perm] = true
		}
	}
	for _, perm := range id.Bridge.AtomsSatisfied(p.Perms) {
		if scoped == nil || scoped[string(perm)] {
			held[perm] = true
		}
	}
	return held, nil
}

// opActorHolds is a snapshot of what the account that queued operation opID
// may do, for a job that runs after its request is gone: the permissions
// opActorPermissions resolves, read once, from the actor stamped on the op row,
// never from the job's params (params can be set by any caller of the generic
// start routes, the stamped actor cannot). It fails closed: an op with no actor
// (started through a route that does not stamp one), an actor that no longer
// exists, holds no role or is disabled, or a store failure, holds nothing, so
// every Repairs op and every user-data result is refused. Auth disabled, and a
// first run with no users yet, hold everything, as permHolds does.
//
// Under core identity neither bypass applies. There is no first run (HEAD's
// read books' user count, which M3 drains, so it held on every migrated
// install), and books' EnableAuth is not core's mode. The actor is resolved
// through core (opActorPermissionsCore), and a job it cannot be resolved for
// (no actor: with core's auth off a request binds no user; or an actor that
// does not stand) is an error, so the job fails visibly instead of finishing
// with an empty result. Off core identity the error is always nil.
func (s *Server) opActorHolds(ctx context.Context, opID string) (func(auth.Permission) bool, error) {
	store := s.storeForWiring()
	if s.identity == nil {
		all := func(auth.Permission) bool { return true }
		if !config.Snapshot().EnableAuth {
			return all, nil
		}
		if n, err := store.CountUsers(); err == nil && n == 0 {
			return all, nil
		}
		row, err := store.GetOperationV2(opID)
		if err != nil {
			row = nil
		}
		held := s.opActorPermissions(ctx, row)
		return func(p auth.Permission) bool { return held[p] }, nil
	}
	row, err := store.GetOperationV2(opID)
	if err != nil {
		return nil, fmt.Errorf("read operation %s: %w", opID, err)
	}
	held, err := s.opActorPermissionsCore(ctx, row)
	if err != nil {
		return nil, err
	}
	return func(p auth.Permission) bool { return held[p] }, nil
}

// holdsAccess is the opaccess reader for a fixed permission set.
func (s *Server) holdsAccess(holds func(auth.Permission) bool) *opaccess.Reader {
	return s.newRepairsOpAccess(func(_ *gin.Context, p auth.Permission) bool { return holds(p) }).For(nil)
}

// exportNarrowingPermissions is every permission an export's contents depend
// on: users.manage (user data) and each registered fixer's required permissions
// (which Repairs operations a reader may see). Two accounts that hold the same
// subset of these get the same export.
func (s *Server) exportNarrowingPermissions() []auth.Permission {
	set := map[auth.Permission]bool{auth.PermUsersManage: true}
	if s.repairFixers != nil {
		for _, f := range s.repairFixers.List() {
			for _, p := range repairs.RequiredPermissions(f) {
				set[p] = true
			}
		}
	}
	out := make([]auth.Permission, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// exportNarrowedTo is the part of exportNarrowingPermissions that holds
// reports as held: the set an export built for that account is narrowed to, and
// the one the download check (diagnosticsDownloadCheck) holds the caller to.
func (s *Server) exportNarrowedTo(holds func(auth.Permission) bool) []string {
	out := []string{}
	for _, p := range s.exportNarrowingPermissions() {
		if holds(p) {
			out = append(out, string(p))
		}
	}
	return out
}

// diagnosticsDownloadCheck is the scope check of GET
// /diagnostics/export/:operationId/download, run by DownloadExport on the row it
// is about to serve (handlers.ExportDownloadCheck), not by a middleware that
// reads the row on its own: a check made on one read of the row and a file
// served from another can disagree, and a check that cannot decide must not
// pass.
//
// The zip was narrowed, when it was built, to what the account it was built for
// could read, and the build recorded that set on the export's result
// (diagnostics.ExportResult.NarrowedTo). The download is served only to a
// caller whose own request holds every permission in it: the request's set,
// which for an API key is its scopes intersected with its account's roles
// (permHolds), so a narrowly scoped key of the account that built an admin's
// export is refused like any other narrow caller. There is no same-account
// shortcut for that reason.
//
// It fails closed. A result with no recorded set (an export built before the
// record existed) is served to nobody: start a new one. An export whose account
// no longer exists or is not active is served to nobody either. A store failure
// (the user count, the account lookup) is an error, which DownloadExport
// answers 500: nothing is served on a guess. The record is compared as stored,
// never against what the account holds now, so a role change after the build
// neither widens nor narrows it. With auth off, and on a first run before any
// account exists, there is no scope to compare and the download is allowed.
func (s *Server) diagnosticsDownloadCheck() handlers.ExportDownloadCheck {
	holds := s.permHolds()
	if id := s.identity; id != nil {
		return func(c *gin.Context, row *database.OperationV2Row, res diagnostics.ExportResult) (string, error) {
			return diagnosticsDownloadCheckCore(c, id, holds, row, res)
		}
	}
	return func(c *gin.Context, row *database.OperationV2Row, res diagnostics.ExportResult) (string, error) {
		if !config.Snapshot().EnableAuth {
			return "", nil
		}
		store := s.storeForWiring()
		n, err := store.CountUsers()
		if err != nil {
			return "", fmt.Errorf("count users: %w", err)
		}
		if n == 0 {
			return "", nil
		}
		if res.NarrowedTo == nil {
			return "this export records no scope it was built for; start a new export", nil
		}
		if row.ActorUserID != nil {
			u, err := store.GetUserByID(*row.ActorUserID)
			if err != nil {
				return "", fmt.Errorf("read the account export %s was built for: %w", row.ID, err)
			}
			if u == nil {
				return "the account this export was built for cannot be read; start a new export", nil
			}
			if status := strings.ToLower(strings.TrimSpace(u.Status)); status != "" && status != "active" {
				return "the account this export was built for is not active; start a new export", nil
			}
		}
		for _, p := range *res.NarrowedTo {
			if !holds(c, auth.Permission(p)) {
				return "this export was built for an account that holds more than you do; start your own export", nil
			}
		}
		return "", nil
	}
}

// diagnosticsDownloadCheckCore is diagnosticsDownloadCheck under core identity.
// Auth off is core's auth-disabled principal (not books' EnableAuth), there is
// no first-run allowance (HEAD's read books' user count, which M3 drains, and
// allowed every download on a migrated install), and the account the export
// was built for is read through core's identity view. Everything else is
// HEAD's rule: no recorded set is served to nobody, an account that is gone or
// not active is served to nobody, a read failure is an error (500), and the
// caller must hold every permission in the recorded set.
func diagnosticsDownloadCheckCore(c *gin.Context, id *legacyauth.Identity, holds repairshandler.PermissionCheck,
	row *database.OperationV2Row, res diagnostics.ExportResult) (string, error) {
	ctx := c.Request.Context()
	if id.Bridge.AuthOff(ctx) {
		return "", nil
	}
	if res.NarrowedTo == nil {
		return "this export records no scope it was built for; start a new export", nil
	}
	if row.ActorUserID != nil {
		if id.Users == nil {
			return "", fmt.Errorf("read the account export %s was built for: %w", row.ID, errNoCoreUserView)
		}
		u, err := id.Users.User(ctx, coreauth.UserID(*row.ActorUserID))
		switch {
		case errors.Is(err, coreauth.ErrNotFound), err == nil && u == nil:
			return "the account this export was built for cannot be read; start a new export", nil
		case err != nil:
			return "", fmt.Errorf("read the account export %s was built for: %w", row.ID, err)
		case u.Status != coreauth.StatusActive:
			return "the account this export was built for is not active; start a new export", nil
		}
	}
	for _, p := range *res.NarrowedTo {
		if !holds(c, auth.Permission(p)) {
			return "this export was built for an account that holds more than you do; start your own export", nil
		}
	}
	return "", nil
}

// errNoCoreUserView is a server under core identity that was given no read of
// core's accounts (Identity.Users is optional): the account an export names
// cannot be checked, so nothing is served.
var errNoCoreUserView = errors.New("this server was given no read of core's accounts")

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
