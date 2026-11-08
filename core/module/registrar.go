// file: core/module/registrar.go
// version: 1.0.2
// guid: b007ae28-2b2a-42a1-aa5a-047aa14d3b77
// last-edited: 2026-11-07

package module

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
)

// Registrar is handed to Module.Register (06 §6.4.3). It is not
// goroutine-safe and is closed when Register returns; calls after that record
// ProblemRegistrarClosed and take no effect. Methods record problems rather
// than return errors (A4); Err reports them. Nothing recorded here takes
// effect before P7 (A8). These are the R1 members; Tasks, Topics, Health and
// the rest arrive with their packages (06 §6.4.10).
type Registrar struct {
	id            ID
	ctx           context.Context
	deps          Deps
	http          *httpapi.Group
	legacyAuthGin bool

	mu         sync.Mutex // guards closed and problems against a late call from another goroutine
	closed     bool
	problems   []Problem
	rec        Registrations
	afterClose func(Problem)
}

// RegistrarConfig is what the kernel hands NewRegistrar. Kernel-only.
type RegistrarConfig struct {
	Context context.Context // derived from app.New's ctx, cancelled when New returns
	Deps    Deps            // Deps.Module is the module's ID
	HTTP    *httpapi.Group  // the module's group, httpapi.Router.ModuleGroup(id)
	// LegacyAuthGin is true for the one module app.Options.LegacyAuthGinUntil
	// names: only its MountLegacy may use httpapi.LegacyAuthGin (SEC2-17).
	LegacyAuthGin bool
	// AfterClose, when set, receives each ProblemRegistrarClosed recorded
	// after Close, which no Err call reads any more: the kernel logs it.
	AfterClose func(Problem)
}

// Registrations is what one module recorded. Kernel-only: the kernel reads
// it from Close and consumes it in P7.
type Registrations struct {
	Mounts        []httpapi.LegacyMount
	AuthMethods   []auth.MethodSpec
	PathRules     []log.PathRule
	OtherUserData log.OtherUserPredicate // nil when none was registered
	Hooks         []Hook
	Workers       []Worker
}

// NewRegistrar returns the registrar of one module's Register call.
// Kernel-only: apps never construct one (06 §6.4.6; the kernel builds the
// Registrar and Deps of each module, P6).
func NewRegistrar(c RegistrarConfig) *Registrar {
	ctx := c.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return &Registrar{id: c.Deps.Module, ctx: ctx, deps: c.Deps, http: c.HTTP, legacyAuthGin: c.LegacyAuthGin,
		afterClose: c.AfterClose}
}

// Close ends registration and returns what was recorded. Kernel-only. It is
// idempotent and records nothing itself: every call returns the same
// registrations, so a module that calls Close inside Register loses nothing.
// Every other registrar call after Close records ProblemRegistrarClosed and
// takes no effect, and Err keeps reporting it.
func (r *Registrar) Close() Registrations {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	rec := r.rec
	rec.Mounts = slices.Clone(rec.Mounts)
	rec.AuthMethods = slices.Clone(rec.AuthMethods)
	rec.PathRules = slices.Clone(rec.PathRules)
	rec.Hooks = slices.Clone(rec.Hooks)
	rec.Workers = slices.Clone(rec.Workers)
	return rec
}

// record runs fn while open; after Close it records ProblemRegistrarClosed
// naming call instead, and hands it to RegistrarConfig.AfterClose.
func (r *Registrar) record(call string, fn func()) {
	r.mu.Lock()
	if r.closed {
		p := Problem{Module: r.id, Kind: ProblemRegistrarClosed,
			Detail: call + " called after Register returned; register everything inside Register"}
		r.problems = append(r.problems, p)
		after := r.afterClose
		r.mu.Unlock()
		if after != nil {
			after(p)
		}
		return
	}
	defer r.mu.Unlock()
	fn()
}

func (r *Registrar) problem(kind ProblemKind, format string, args ...any) {
	r.problems = append(r.problems, Problem{Module: r.id, Kind: kind, Detail: fmt.Sprintf(format, args...)})
}

// Context is derived from app.New's ctx: cancelled when app.New is cancelled
// or returns. Register uses it for its own reads (COR-24).
func (r *Registrar) Context() context.Context { return r.ctx }

// Deps returns the module-scoped dependency handles. Every field is non-nil.
func (r *Registrar) Deps() Deps { return r.deps }

// HTTP returns the module's route group rooted at /api/<ID>/v1 (05-http-api
// §3.1). Register routes with httpapi.Handle(r.HTTP(), httpapi.Route{...},
// fn). Every route needs Perm (R6). A route registered after Register
// returned is never served (the router is built at P7) and records
// ProblemRegistrarClosed.
func (r *Registrar) HTTP() *httpapi.Group {
	r.record("HTTP", func() {})
	return r.http
}

// MountLegacy mounts an http.Handler on prefixes declared in
// Manifest.LegacyPrefixes, behind core's perimeter stages (05-http-api §8.2).
// m.Owner is set by the registrar. m.ServesSPA lets the mount own the "/"
// SPA fallback; at most one mount per app may set it (ProblemSPAOwner, P7).
// m.Auth = httpapi.LegacyAuthGin is accepted only for the module named by
// app.Options.LegacyAuthGinUntil; any other module records ProblemLegacyAuth
// (SEC2-17). The manifest half (Routes, OwnAuth) is R1-14's.
func (r *Registrar) MountLegacy(m httpapi.LegacyMount) {
	r.record("MountLegacy", func() {
		m.Owner = string(r.id)
		if m.Auth != httpapi.LegacyAuthCore && m.Auth != httpapi.LegacyAuthGin {
			r.problem(ProblemLegacyAuth, "MountLegacy with Auth %d: set httpapi.LegacyAuthCore (or LegacyAuthGin for the module app.Options.LegacyAuthGinUntil names)", m.Auth)
			return
		}
		if m.Auth == httpapi.LegacyAuthGin && !r.legacyAuthGin {
			r.problem(ProblemLegacyAuth, "MountLegacy with LegacyAuthGin: only the module app.Options.LegacyAuthGinUntil names may skip core authentication; use LegacyAuthCore")
			return
		}
		r.rec.Mounts = append(r.rec.Mounts, m)
	})
}

// AuthMethod registers an authentication method of this module, named
// "<ID>.<name>" (05-authn §3.3). It refuses (ProblemAuthMethod) any spec
// that is Interactive, sets MayChangeCredentials or OwnerVerifier: only
// app.Options.Authenticators may carry those (SEC2-16). In R1 it only
// records the declaration, so P7's OwnAuth admission works before the auth
// service exists (SEC3-14); the kernel accepts at most one AppHandoff per app.
func (r *Registrar) AuthMethod(spec auth.MethodSpec) {
	r.record("AuthMethod", func() {
		var bad []string
		if name, ok := strings.CutPrefix(string(spec.Method), string(r.id)+"."); !ok || name == "" {
			bad = append(bad, fmt.Sprintf("method %q is not \"%s.<name>\"", spec.Method, r.id))
		}
		if spec.Interactive {
			bad = append(bad, "Interactive")
		}
		if spec.MayChangeCredentials {
			bad = append(bad, "MayChangeCredentials")
		}
		if spec.OwnerVerifier {
			bad = append(bad, "OwnerVerifier")
		}
		if len(bad) > 0 {
			r.problem(ProblemAuthMethod, "AuthMethod %q refused (%s): a module method is non-interactive and cannot change credentials or assert owner assurance; those belong in app.Options.Authenticators",
				spec.Method, strings.Join(bad, ", "))
			return
		}
		r.rec.AuthMethods = append(r.rec.AuthMethods, spec)
	})
}

// LogPathRules adds the module's token-bearing path shapes to the kernel's
// Redactor (05-logging §3). Recorded only in R1-13: see the R1-13 seams for
// when the kernel's Redactor takes them.
func (r *Registrar) LogPathRules(p ...log.PathRule) {
	r.record("LogPathRules", func() { r.rec.PathRules = append(r.rec.PathRules, p...) })
}

// LogOtherUserData registers the predicate that classifies a system-journal
// record as naming another user's data (05-logging §3, §5). At most one per
// module; recorded from R1, consumed when the journal query lands.
func (r *Registrar) LogOtherUserData(pred log.OtherUserPredicate) {
	r.record("LogOtherUserData", func() {
		switch {
		case pred == nil:
			r.problem(ProblemInvalidRegistration, "LogOtherUserData(nil)")
		case r.rec.OtherUserData != nil:
			r.problem(ProblemInvalidRegistration, "LogOtherUserData called twice: at most one predicate per module")
		default:
			r.rec.OtherUserData = pred
		}
	})
}

// Hook registers a Start/Stop pair. Hooks of one module run in registration
// order on start and reverse order on stop. Name must be "<ID>.<name>" and
// unique (ProblemInvalidRegistration).
func (r *Registrar) Hook(h Hook) {
	r.record("Hook", func() {
		name, ok := strings.CutPrefix(h.Name, string(r.id)+".")
		switch {
		case !ok || name == "":
			r.problem(ProblemInvalidRegistration, "hook name %q is not \"%s.<name>\"", h.Name, r.id)
		case slices.ContainsFunc(r.rec.Hooks, func(o Hook) bool { return o.Name == h.Name }):
			r.problem(ProblemInvalidRegistration, "hook %q registered twice", h.Name)
		default:
			r.rec.Hooks = append(r.rec.Hooks, h)
		}
	})
}

// Worker registers a supervised long-running goroutine, started after all
// hooks have started and stopped (ctx cancelled) before any hook stops. A
// zero Policy is refused (ProblemWorkerPolicy); a missing Name or Run, or a
// Name used twice, is ProblemInvalidRegistration.
func (r *Registrar) Worker(w Worker) {
	r.record("Worker", func() {
		switch {
		case w.Policy != DegradeOnExit && w.Policy != StopAppOnExit:
			r.problem(ProblemWorkerPolicy, "worker %q: Policy must be DegradeOnExit or StopAppOnExit", w.Name)
		case w.Name == "" || w.Run == nil:
			r.problem(ProblemInvalidRegistration, "worker %q needs a Name and a Run func", w.Name)
		case slices.ContainsFunc(r.rec.Workers, func(o Worker) bool { return o.Name == w.Name }):
			r.problem(ProblemInvalidRegistration, "worker %q registered twice", w.Name)
		default:
			r.rec.Workers = append(r.rec.Workers, w)
		}
	})
}

// Err returns the problems recorded so far, as a *ValidationError; nil if
// none.
func (r *Registrar) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: slices.Clone(r.problems)}
}

// Hook is a Start/Stop pair (06 §6.4.3).
type Hook struct {
	Name  string                          // "<ID>.<name>", unique
	Start func(ctx context.Context) error // may be nil
	Stop  func(ctx context.Context) error // may be nil; ctx carries the stop deadline
}

// WorkerPolicy says what a worker's early exit means.
type WorkerPolicy int

const (
	// DegradeOnExit: an early return or panic marks health "<ID>.<worker>"
	// unhealthy and is logged; the app keeps serving.
	DegradeOnExit WorkerPolicy = iota + 1
	// StopAppOnExit: an early return or panic begins a graceful shutdown and
	// App.Run returns the worker's error. Only for workers whose loss would
	// make continued operation unsafe for data.
	StopAppOnExit
)

// Worker is a supervised long-running goroutine (06 §6.4.3).
type Worker struct {
	Name   string
	Run    func(ctx context.Context) error // must return promptly after ctx is done
	Policy WorkerPolicy                    // zero value refused: the module must say which
}
