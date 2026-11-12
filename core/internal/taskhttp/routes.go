// file: core/internal/taskhttp/routes.go
// version: 1.0.0
// guid: 09a80470-a31f-483b-b8b4-790a460cd0cb
// last-edited: 2026-11-11

// Package taskhttp is the native HTTP surface of core's tasks subsystem:
// the routes of 05-tasks 5T.3.10 under /api/core/v1/tasks, a re-implementation
// of HEAD's opsv3 handlers over tasks.Client (04 §8.6). It is dark until the
// kernel's start rule mounts it (F2-16): nothing imports it yet.
//
// The package returns its routes as data (Route: method, path, name,
// permission, handler) instead of registering them on httpapi. The start
// route has to set a status that depends on the outcome (202 for a new run,
// 200 for a replay) and a response header (Idempotent-Replayed), which
// httpapi's typed Handle cannot do and its HandleRaw (05-http-api §3.1) is not
// built yet; F2-16 adapts each Route to HandleRaw. Every handler still checks
// its route's permission and every per-definition rule itself, so a Route
// mounted bare enforces what the table says.
//
// The handlers never read the actor or the trigger from a request body. The
// actor is the caller's auth.Principal (Principal, which the kernel installs
// as the executor's Options.Principal), and a start's trigger is always
// tasks.TriggerManual.
package taskhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// BasePath is where the routes mount.
const BasePath = "/api/core/v1/tasks"

// The scopes the table names (05-rbac's fixed core:* catalog).
const (
	scopeView   rbac.Permission = "core:tasks.view"
	scopeManage rbac.Permission = "core:tasks.manage"
)

// Route is one native route. Path is relative to BasePath ("/runs/{id}").
// Perm is the route floor; per-definition rules (Start, View, Approve) are
// applied inside Handler as well.
type Route struct {
	Method  string
	Path    string
	Name    string // operationId, "<verb><Noun>"
	Summary string
	Perm    rbac.Requirement
	Handler http.Handler
}

// Pattern is the route as a ServeMux pattern under BasePath.
func (r Route) Pattern() string { return r.Method + " " + BasePath + r.Path }

// Deps are what the routes call. Every field but Log and VerifiedMethod is
// required; New refuses a missing one rather than serving a route that
// fails open.
type Deps struct {
	// Client is the executor's Client: starts, cancels, reads. It acts as the
	// caller through its Options.Principal (this package's Principal).
	Client tasks.Client
	// Approver is Executor.Approve (the Fixer approval; the Client has none).
	Approver Approver
	// Runs reads what the Client does not return: counts, a plan's rows and a
	// run's item outcomes. tasks.RunStore satisfies it. The routes call it
	// only after Client.Get proved the caller may view the run.
	Runs RunReader
	// Definitions are every registered kind: the listing, the per-definition
	// permissions and the schedule keys come from them.
	Definitions []tasks.Definition
	// Config writes a kind's schedule key. *config.Store satisfies it; Apply
	// records core.settings.update (05-rbac 7.1) atomically with the write.
	Config ConfigPort
	// Audit records approve, reject and cancel-by-another. Required: an
	// approval with no audit sink is refused at New.
	Audit audit.Logger
	// VerifiedMethod answers the verified-method rule of approve and decide
	// (SEC-24, 05-tasks 5T.3.7) for need, the requirement the route checked.
	// nil is the core default (DefaultVerifiedMethod: only an interactive
	// session). The kernel widens it with the app's interactive
	// authenticator extensions and with API keys that name the scope
	// explicitly (F4-05); a delegated session never verifies.
	VerifiedMethod func(p *auth.Principal, need rbac.Requirement) bool
	// Log receives internal errors; nil discards.
	Log *slog.Logger
}

// Errors New returns.
var (
	errNoClient    = errors.New("taskhttp: Deps.Client is required")
	errNoApprover  = errors.New("taskhttp: Deps.Approver is required")
	errNoRuns      = errors.New("taskhttp: Deps.Runs is required")
	errNoConfig    = errors.New("taskhttp: Deps.Config is required")
	errNoAudit     = errors.New("taskhttp: Deps.Audit is required (approve, decide and cancel are audited)")
	errNilDef      = errors.New("taskhttp: a nil Definition in Deps.Definitions")
	errDuplicateOf = errors.New("taskhttp: two definitions share a kind")
)

// Server holds the routes' dependencies.
type Server struct {
	d      Deps
	defs   map[tasks.Kind]tasks.Definition
	order  []tasks.Kind // registration order, for stable listings
	verify func(*auth.Principal, rbac.Requirement) bool
	log    *slog.Logger
	routes []Route
}

// New validates d and builds the routes.
func New(d Deps) (*Server, error) {
	var errs []error
	for _, c := range []struct {
		missing bool
		err     error
	}{
		{d.Client == nil, errNoClient},
		{d.Approver == nil, errNoApprover},
		{d.Runs == nil, errNoRuns},
		{d.Config == nil, errNoConfig},
		{d.Audit == nil, errNoAudit},
	} {
		if c.missing {
			errs = append(errs, c.err)
		}
	}
	s := &Server{d: d, defs: make(map[tasks.Kind]tasks.Definition, len(d.Definitions)), verify: d.VerifiedMethod, log: d.Log}
	for _, def := range d.Definitions {
		switch {
		case def == nil:
			errs = append(errs, errNilDef)
		case s.defs[def.Kind()] != nil:
			errs = append(errs, errDuplicateOf)
		default:
			s.defs[def.Kind()] = def
			s.order = append(s.order, def.Kind())
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if s.verify == nil {
		s.verify = DefaultVerifiedMethod
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.routes = s.buildRoutes()
	return s, nil
}

// Routes returns the route table, in table order.
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

// Handler serves every route under BasePath on its own ServeMux, for tests and
// for a bare mount. A request must already carry the caller's principal
// (auth.WithPrincipal); without one every route answers 401.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes {
		mux.Handle(r.Pattern(), r.Handler)
	}
	return mux
}

func (s *Server) buildRoutes() []Route {
	var (
		view   = rbac.Need(scopeView)
		manage = rbac.Need(scopeManage)
		authed = rbac.AnyAuthenticated
	)
	rt := func(method, path, name, summary string, perm rbac.Requirement, h func(http.ResponseWriter, *http.Request, *auth.Principal)) Route {
		r := Route{Method: method, Path: path, Name: name, Summary: summary, Perm: perm}
		r.Handler = s.guard(r, h)
		return r
	}
	return []Route{
		rt("GET", "/definitions", "listTaskDefinitions", "List the task definitions the caller may view", authed, s.listDefinitions),
		rt("GET", "/definitions/{kind}", "getTaskDefinition", "Read one task definition", authed, s.getDefinition),
		rt("POST", "/definitions/{kind}/runs", "startTaskRun", "Start a run of a definition", authed, s.startRun),
		rt("GET", "/runs", "listTaskRuns", "List runs", authed, s.listRuns),
		rt("GET", "/runs/{id}", "getTaskRun", "Read one run", authed, s.getRun),
		rt("GET", "/runs/{id}/rows", "listTaskRunRows", "List a plan run's rows", authed, s.listRows),
		rt("GET", "/runs/{id}/items", "listTaskRunItems", "List a run's failed and skipped items", authed, s.listItems),
		rt("GET", "/runs/{id}/log", "getTaskRunLog", "Read a run's log", authed, s.getLog),
		rt("POST", "/runs/{id}/cancel", "cancelTaskRun", "Cancel a run", authed, s.cancelRun),
		rt("DELETE", "/runs/{id}", "discardTaskRun", "Delete a finished run and its records", authed, s.discardRun),
		rt("POST", "/runs/{id}/approve", "approveTaskRun", "Approve or reject a plan run", authed, s.approveRun),
		rt("POST", "/runs/{id}/decide", "decideTaskRun", "Resume, finish or drop a run awaiting a decision", authed, s.decideRun),
		rt("GET", "/counts", "getTaskCounts", "Count runs by state", view, s.getCounts),
		rt("GET", "/timeline", "getTaskTimeline", "Runs finished in a window, plus unfinished ones", view, s.getTimeline),
		rt("GET", "/schedules", "listTaskSchedules", "List the schedules the caller may view", view, s.listSchedules),
		rt("PATCH", "/schedules/{kind}", "updateTaskSchedule", "Change a kind's schedule", manage, s.patchSchedule),
		rt("GET", "/pause", "getTaskPause", "Read the operator pause marker", view, s.getPause),
		rt("POST", "/pause", "pauseTasks", "Pause the executor", manage, s.pause),
		rt("POST", "/unpause", "unpauseTasks", "Resume the executor", manage, s.unpause),
	}
}

// guard is every route's front door: the caller's principal must exist and
// satisfy the route floor. h runs only after that.
func (s *Server) guard(rt Route, h func(http.ResponseWriter, *http.Request, *auth.Principal)) http.Handler {
	perm := rt.Perm
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, rt.Pattern()))
		p, ok := auth.PrincipalFrom(r.Context())
		if !ok {
			s.write(w, r, problem{Status: http.StatusUnauthorized, Code: "unauthenticated", Detail: "sign in to use the tasks API"})
			return
		}
		if p.Perms == nil || !p.Perms.Satisfies(perm) {
			s.write(w, r, forbidden(perm))
			return
		}
		h(w, r, p)
	})
}

// routeKey carries the route's pattern, which audit records name instead of
// the raw path.
type routeKey struct{}

// kindParam is the {kind} path value as a Kind, or a 404 problem. A kind
// that cannot parse cannot name a definition.
func kindParam(r *http.Request) (tasks.Kind, bool) {
	k, err := tasks.ParseKind(strings.TrimSpace(r.PathValue("kind")))
	return k, err == nil
}
