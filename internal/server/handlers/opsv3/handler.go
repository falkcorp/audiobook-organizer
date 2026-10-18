// file: internal/server/handlers/opsv3/handler.go
// version: 1.3.0
// guid: b12d50f5-488d-4f11-bf35-03e56a5ca698
// last-edited: 2026-10-18

// Package opsv3 serves /api/v3/ops/*: the operations v3 run model over HTTP.
//
// Reads (defs, runs, counts, timeline, a run's rows, the SSE feed) need
// library.view. Starting a run needs scan.trigger, the floor v1's
// POST /operations/v2 has, plus the definition's own permission (an empty
// permission means settings.manage, D1). Cancelling needs being the run's
// starter, or scan.trigger plus the definition's permission. Approving needs
// the definition's approver permission plus a verified auth method (a
// session, an API token or a verified Cloudflare Access sign-in; never the
// unsigned forwarded header).
//
// Census versus timeline. runs and counts are exact: totals come from the
// store's indexes, never from the page, and every count carries an href whose
// list has exactly that many rows. timeline is a time window over finished
// runs and says complete=false when the scan cap cut it short.
//
// The handlers take narrow interfaces (RunReader, Defs, Sched, Enqueuer,
// ZombieSource, Hub, Approver), never database.Store. The server resolves
// RunReader from the store with database.AsCapability.
package opsv3

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Prefix is where the routes live.
const Prefix = "/api/v3/ops"

// Page size bounds of the paged listings.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// RunReader is the part of database.OpsV3Store the read endpoints call.
type RunReader interface {
	GetRunV3(id string) (*database.RunRecord, error)
	ListRunsV3(q database.RunQuery) (database.RunPage, error)
	OpCountsV3() (database.OpCounts, error)
	ListDoneRunsSinceV3(since time.Time, limit int) (runs []database.RunRecord, complete bool, err error)
	ListItemOutcomesV3(id string, q database.ItemQuery) (database.ItemPage, error)
}

// LastRunFinder is the store's per-definition lookup of the newest finished
// run (database.PebbleStore.LatestSettledRunV3, which walks the
// opv3:ix:def: index). It is not part of database.Store; the server resolves
// it with database.AsCapability.
type LastRunFinder interface {
	LatestSettledRunV3(defID string) (*database.RunRecord, error)
}

// DefInfo is what the endpoints know about one definition. The server builds
// it from the registry catalog; the handlers never see ops.Definition.
type DefInfo struct {
	ID    string
	Title string
	Help  string
	Kind  string // task | batch | fixer | pipeline | adapted
	// Permissions are all required to start the definition (AND). Empty means
	// settings.manage (D1).
	Permissions []auth.Permission
	// Approver is the permission an approval needs; empty means settings.manage.
	Approver auth.Permission
	// ReadOnly and the resource sets are the declared effects.
	ReadOnly bool
	Reads    []string
	Writes   []string
	Deletes  []string
	// ParamsSchema is the JSON Schema params are validated against before a
	// run is queued; empty means the definition declares none.
	ParamsSchema []byte
	// Writer is whether a run can change anything, so its mode matters. For
	// a native definition it is !ReadOnly; for an adapted v2 op it is what
	// its capabilities say (undeclared counts as writing).
	Writer bool
	// Preview is how a run of a writing definition writes nothing: the
	// op's own declaration (OperationDef.Preview), whose params type names
	// every mode key /api/v3/ops writes. Zero means it has no preview a
	// caller can ask for by params, so only mode "live" starts it. For an
	// adapted v2 op it is never inferred: an undeclared declaration, or one
	// that does not check, is zero. Native writing definitions preview with
	// dry_run (the runner resolves the mode). Meaningless when !Writer.
	Preview opsregistry.PreviewSpec
}

// HasPreview reports whether a "preview" run of the definition writes
// nothing.
func (d DefInfo) HasPreview() bool { return !d.Preview.IsZero() }

// Defs is the definition catalog.
type Defs interface {
	ListDefs() []DefInfo
	GetDef(id string) (DefInfo, bool)
	// Ready reports whether a run of the definition could start now (D44: the
	// store or its indexes are still warming) and, when not, why.
	Ready(id string) (ok bool, reason string)
}

// ScheduleInfo is one definition's timer.
type ScheduleInfo struct {
	Kind     string // every | daily_at | cron | window | disabled
	Active   bool
	Live     bool
	NextFire *time.Time
}

// Sched reports every registered schedule by definition id. A definition
// without an entry is manual.
type Sched interface {
	Schedules() map[string]ScheduleInfo
}

// Enqueuer starts and stops runs.
type Enqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
	Cancel(opID string) error
}

// ZombieSource lists the runs whose goroutine outlived the abandon grace.
type ZombieSource interface {
	Zombies() []opsregistry.ZombieInfo
}

// Approver records a person's decision on a run. Errors: ErrRunNotApprovable
// (409), ErrRunNotFound (404).
type Approver interface {
	Approve(ctx context.Context, runID string, rec database.ApprovalRecord) error
}

// Errors an Approver returns.
var (
	ErrRunNotFound      = errors.New("opsv3: run not found")
	ErrRunNotApprovable = errors.New("opsv3: run is not waiting for a decision")
)

// Authz is how the handlers check permissions the route table cannot know:
// the definition's own.
type Authz struct {
	// Require reports whether the caller holds p; when it does not it has
	// written the 401/403 and the handler stops.
	Require func(c *gin.Context, p auth.Permission) bool
	// Holds is Require without the response.
	Holds func(c *gin.Context, p auth.Permission) bool
}

// StrictAuthz holds exactly the permissions the request's context carries, with
// no first-run or auth-disabled bypass. It is the default for a handler the
// server did not wire.
func StrictAuthz() Authz {
	return Authz{
		Require: func(c *gin.Context, p auth.Permission) bool { return servermiddleware.CheckPermission(nil, c, p) },
		Holds:   func(c *gin.Context, p auth.Permission) bool { return auth.Can(c.Request.Context(), p) },
	}
}

// Deps is everything a Handler needs. Only Runs is required for the reads;
// a missing collaborator makes the routes that need it answer 503.
type Deps struct {
	Runs RunReader
	// LastRuns finds each definition's last finished run for /defs; nil
	// leaves last_run null.
	LastRuns LastRunFinder
	Defs     Defs
	Sched    Sched
	Enqueuer Enqueuer
	Zombies  ZombieSource
	Approver Approver
	Broker   *Broker
	Authz    Authz
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Closing is closed when the server starts shutting down, so open event
	// streams end at once instead of holding the HTTP shutdown for its whole
	// timeout. nil never closes.
	Closing <-chan struct{}
}

// Handler serves the routes.
type Handler struct {
	d Deps

	lastCache lastRunCache
}

// New builds a Handler.
func New(d Deps) *Handler {
	s := StrictAuthz()
	if d.Authz.Require == nil {
		d.Authz.Require = s.Require
	}
	if d.Authz.Holds == nil {
		d.Authz.Holds = s.Holds
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handler{d: d}
}

// Register adds the routes to g (the /api/v3 protected group). perm builds the
// route-level permission middleware (the server's s.perm).
func (h *Handler) Register(g *gin.RouterGroup, perm func(auth.Permission) gin.HandlerFunc) {
	view := perm(auth.PermLibraryView)
	g.GET("/ops/defs", view, h.ListDefs)
	g.GET("/ops/defs/:id", view, h.GetDef)
	g.GET("/ops/runs", view, h.ListRuns)
	g.GET("/ops/runs/:id", view, h.GetRun)
	g.GET("/ops/runs/:id/rows", view, h.ListRows)
	g.GET("/ops/counts", view, h.Counts)
	g.GET("/ops/timeline", view, h.Timeline)
	g.GET("/ops/events", view, h.Events)
	// Starting a run has scan.trigger as its floor, as v1's
	// POST /operations/v2 does: without it a viewer could start any def that
	// declares only library.view. The definition's own permission is then
	// checked inside the handler, on top. Cancelling and approving are
	// checked inside the handler (the run's starter may cancel it, so the
	// route cannot ask for more than library.view).
	trigger := perm(auth.PermScanTrigger)
	g.POST("/ops/defs/:id/runs", trigger, h.StartRun)
	g.POST("/ops/runs", trigger, h.StartRun)
	g.POST("/ops/runs/:id/cancel", view, h.CancelRun)
	g.POST("/ops/runs/:id/approve", view, h.ApproveRun)
}

// --- shared helpers ---

// requiredPerms is what starting (or cancelling someone else's run of) def
// takes: its declared permissions, or settings.manage when it declares none.
func requiredPerms(def DefInfo, known bool) []auth.Permission {
	if !known {
		return []auth.Permission{auth.PermSettingsManage}
	}
	var out []auth.Permission
	for _, p := range def.Permissions {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []auth.Permission{auth.PermSettingsManage}
	}
	return out
}

func (h *Handler) requireAll(c *gin.Context, perms []auth.Permission) bool {
	for _, p := range perms {
		if !h.d.Authz.Require(c, p) {
			return false
		}
	}
	return true
}

func (h *Handler) holdsAll(c *gin.Context, perms []auth.Permission) bool {
	for _, p := range perms {
		if !h.d.Authz.Holds(c, p) {
			return false
		}
	}
	return true
}

func (h *Handler) runs(c *gin.Context) (RunReader, bool) {
	if h.d.Runs == nil {
		httputil.RespondWithServiceUnavailable(c, "operations v3 store not available")
		return nil, false
	}
	return h.d.Runs, true
}

// limitParam reads ?limit=; a value that is not a positive integer is a 400
// rather than a silent default, so a typo cannot read as "the first page".
func limitParam(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return DefaultLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		httputil.RespondWithValidationError(c, "limit", "must be a positive integer")
		return 0, false
	}
	return min(n, MaxLimit), true
}

// storeErr answers a store error: a cursor the listing did not issue is the
// caller's mistake (400), anything else is ours.
func storeErr(c *gin.Context, what string, err error) {
	if errors.Is(err, database.ErrBadCursorV3) {
		httputil.RespondWithValidationError(c, "cursor", "not a cursor of this listing")
		return
	}
	httputil.InternalError(c, what+" failed", err)
}

// listHref is the census list URL that returns exactly the runs of one
// counted group.
func listHref(key, val string) string {
	return Prefix + "/runs?" + key + "=" + url.QueryEscape(val)
}

// parseState validates ?state=.
func parseState(c *gin.Context) (state.V3, bool) {
	raw := c.Query("state")
	if raw == "" {
		return "", true
	}
	for _, s := range state.AllV3() {
		if string(s) == raw {
			return s, true
		}
	}
	httputil.RespondWithValidationError(c, "state", "unknown run state "+raw)
	return "", false
}

func accepted(c *gin.Context, body any) { c.JSON(http.StatusAccepted, body) }
