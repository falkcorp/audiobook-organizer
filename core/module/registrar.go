// file: core/module/registrar.go
// version: 1.2.0
// guid: b007ae28-2b2a-42a1-aa5a-047aa14d3b77
// last-edited: 2026-11-18

package module

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/tasks"
)

// Registrar is handed to Module.Register (06 §6.4.3). It is not
// goroutine-safe and is closed when Register returns; calls after that record
// ProblemRegistrarClosed and take no effect. Methods record problems rather
// than return errors (A4); Err reports them. Nothing recorded here takes
// effect before P7 (A8). These are the R1 members, Tasks and Resources (F2),
// Topics and Subscribe (the identity flip's events surface, recorded
// only until F4-08's boot wiring reads them), and Lanes, ChangeSink and Health
// (the tasks flip, F5-18); the rest arrive with their packages (06 §6.4.10).
type Registrar struct {
	id            ID
	ctx           context.Context
	deps          Deps
	http          *httpapi.Group
	legacyAuthGin bool
	settings      map[config.Path]any
	compat        func(auth.MethodSpec) auth.Compat

	mu         sync.Mutex // guards closed, problems and deps.AuthCompat against a late call from another goroutine
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
	// Settings holds one entry per section the module's Manifest.Settings
	// declares, keyed by path: the live *config.Handle[T] the kernel
	// registered at P3, or nil when no live handle exists (app.Describe, a
	// boot without core config). module.Setting reads it: a path absent
	// here is an undeclared section.
	Settings map[config.Path]any
	// AfterClose, when set, receives each ProblemRegistrarClosed recorded
	// after Close, which no Err call reads any more: the kernel logs it.
	AfterClose func(Problem)
	// Compat returns the auth.Compat handle bound to a method this module
	// registered with Registrar.AuthMethod (the kernel hands the specs to auth
	// at P7, so the handle it returns here is late-bound: it refuses until
	// then). Registrar.Deps().AuthCompat is it for the module's first
	// registered method from that call on, and nil before it, so the field is
	// non-nil only for a module that registered one (SEC-1). nil: no module
	// gets a handle (app.Describe, an app that did not declare the identity
	// flip).
	Compat func(auth.MethodSpec) auth.Compat
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
	// Tasks are the definitions the module registered, in call order, and
	// Resources the resource kinds it declared (F2): recorded only, checked
	// at P7 (kernel/tasks.go).
	Tasks     []tasks.Definition
	Resources []string
	// Topics are the catalog entries the module declared and Subscriptions
	// its bus subscriptions, each in call order (Registrar.Topics,
	// Registrar.Subscribe). Recorded as declared: the kernel declares the
	// topics and then the subscriptions on the events bus at P7, which
	// refuses a bad entry (kernelEvents.declare, kernelEvents.subscribe).
	Topics        []events.TopicSpec
	Subscriptions []events.Subscription
	// Lanes are the remote work lanes the module declared and ChangeSink its
	// change journal, nil when it registered none (F5-18): recorded only,
	// handed to the executor when the kernel builds it at P7.
	Lanes      []tasks.Lane
	ChangeSink tasks.ChangeSink
	// Health are the components the module declared with Registrar.Health,
	// in call order: the kernel registers them with the health registry at P7
	// and binds Deps.Health's reporters to them.
	Health []health.Check
	// OutboxHandlers are the module's outbox consumers, in call order
	// (Registrar.OutboxHandler). Recorded as declared: the kernel declares
	// every manifest's OutboxConsumers first and then registers these with the
	// outbox at P7, which refuses a name the manifest did not list, a topic that
	// is not Durable and a second handler of one name.
	OutboxHandlers []outbox.Handler
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
		settings: c.Settings, afterClose: c.AfterClose, compat: c.Compat}
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
	rec.Tasks = slices.Clone(rec.Tasks)
	rec.Resources = slices.Clone(rec.Resources)
	rec.Topics = slices.Clone(rec.Topics)
	rec.Subscriptions = slices.Clone(rec.Subscriptions)
	rec.Lanes = slices.Clone(rec.Lanes)
	rec.Health = slices.Clone(rec.Health)
	rec.OutboxHandlers = slices.Clone(rec.OutboxHandlers)
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

// Deps returns the module-scoped dependency handles (see Deps for which are
// nil: Config, AuthCompat and, on an app that did not declare the identity
// flip, Auth, AuthAdmin and Audit). AuthCompat is set by the first AuthMethod
// call, so a Deps read before it has none.
func (r *Registrar) Deps() Deps {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deps
}

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
		if r.deps.AuthCompat == nil && r.compat != nil {
			r.deps.AuthCompat = r.compat(spec)
		}
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

// Tasks registers task definitions; kinds must be "<ID>.<name>". Schedules are
// part of each definition (tasks.Common.Schedule, bound with
// tasks.ScheduleFromConfig to a declared key in the module's own namespace,
// modules.<ID>.<subsystem>.schedules.<name>, no hyphens; the one
// schedule-key rule <owner>.<subsystem>.schedules.<name>, R2-7, 05-tasks
// 5T.3.8); there is no second timer API. It only records (06 §6.5.1 P6):
// P7 checks every kind against the manifest's TaskLedger and the
// registration rules of 05-tasks 5T.3.1, and a nil definition is
// ProblemInvalidRegistration.
func (r *Registrar) Tasks(defs ...tasks.Definition) {
	r.record("Tasks", func() {
		for _, d := range defs {
			if d == nil {
				r.problem(ProblemInvalidRegistration, "Tasks: a nil definition")
				continue
			}
			r.rec.Tasks = append(r.rec.Tasks, d)
		}
	})
}

// Resources declares the resource kinds this module owns (G8, 05-tasks
// 5T.3.4): a definition's Effects may name only these, in this module. A
// kind that is empty or declared twice is ProblemInvalidRegistration.
func (r *Registrar) Resources(kinds ...string) {
	r.record("Resources", func() {
		for _, k := range kinds {
			switch {
			case k == "":
				r.problem(ProblemInvalidRegistration, "Resources: an empty resource kind")
			case slices.Contains(r.rec.Resources, k):
				r.problem(ProblemInvalidRegistration, "Resources: kind %q declared twice", k)
			default:
				r.rec.Resources = append(r.rec.Resources, k)
			}
		}
	})
}

// Lanes declares the remote work lanes the module's tasks may name (05-tasks
// 5T.3.9). It only records (06 §6.5.1 P6): the kernel hands them to the
// executor at P7, which refuses a malformed or duplicate lane. A lane with an
// empty ID is ProblemInvalidRegistration here, and an ID declared twice by this
// module is too.
func (r *Registrar) Lanes(l ...tasks.Lane) {
	r.record("Lanes", func() {
		for _, ln := range l {
			switch {
			case ln.ID == "":
				r.problem(ProblemInvalidRegistration, "Lanes: a lane with no ID")
			case slices.ContainsFunc(r.rec.Lanes, func(o tasks.Lane) bool { return o.ID == ln.ID }):
				r.problem(ProblemInvalidRegistration, "Lanes: lane %q declared twice", ln.ID)
			default:
				r.rec.Lanes = append(r.rec.Lanes, ln)
			}
		}
	})
}

// ChangeSink registers the module's change journal (books: the operation-change
// journal and undo): tasks.Writer records every gated write's changes into it
// (05-tasks 5T.3.5). At most one per module: a second registration, and a nil
// sink, record ProblemChangeSinkDuplicate and ProblemInvalidRegistration. It
// only records; the kernel wires it into the executor at P7.
func (r *Registrar) ChangeSink(s tasks.ChangeSink) {
	r.record("ChangeSink", func() {
		switch {
		case s == nil:
			r.problem(ProblemInvalidRegistration, "ChangeSink(nil)")
		case r.rec.ChangeSink != nil:
			r.problem(ProblemChangeSinkDuplicate, "ChangeSink registered twice: at most one change journal per module")
		default:
			r.rec.ChangeSink = s
		}
	})
}

// Health declares the module's health components (05-health §5H.3.1): each
// check's ID must be "<ID>.<name>" in this module's namespace and unique among
// the module's own, else ProblemInvalidRegistration. It only records; the
// kernel registers the checks with the App's registry at P7 (where the
// registry refuses an ID another module or core already holds), and
// Deps.Health then hands the module the push Reporter of each. A task
// definition's Common.Ready names such a component (books: books.memdb), so a
// run that needs it is not dispatched before it reports Up.
func (r *Registrar) Health(c ...health.Check) {
	r.record("Health", func() {
		for _, ck := range c {
			name, ok := strings.CutPrefix(string(ck.ID), string(r.id)+".")
			switch {
			case !ok || name == "":
				r.problem(ProblemInvalidRegistration, "Health: component %q is not \"%s.<name>\"", ck.ID, r.id)
			case slices.ContainsFunc(r.rec.Health, func(o health.Check) bool { return o.ID == ck.ID }):
				r.problem(ProblemInvalidRegistration, "Health: component %q declared twice", ck.ID)
			default:
				r.rec.Health = append(r.rec.Health, ck)
			}
		}
	})
}

// Topics declares the module's entries in the ONE topic catalog (05-events
// §5E.3.1, SIMP-5): every bus or durable topic it publishes, with its schema,
// its read Permission, optional Visible filter and Durable, Notify and
// Realtime metadata. Build entries with events.NewTopic[T](name).Spec(...) or
// the outbox topic's Spec. Names are "<ID>.<noun>.<verb-past>.v<N>". It only
// records (06 §6.5.1 P6): the bus checks every name, permission and schema when
// the kernel declares the catalog at P7, and notify and realtime read the same
// catalog.
func (r *Registrar) Topics(t ...events.TopicSpec) {
	r.record("Topics", func() { r.rec.Topics = append(r.rec.Topics, t...) })
}

// Subscribe declares bus subscriptions built with events.On (the module's own
// topics or core.* topics, typed) or events.OnRaw (another module's topic by
// name, R3). They are frozen when Register returns and delivery begins at
// Bus.Open (06 §6.5.2, COR-8). It only records: the bus checks each one at P7,
// after every topic is declared.
func (r *Registrar) Subscribe(s ...events.Subscription) {
	r.record("Subscribe", func() { r.rec.Subscriptions = append(r.rec.Subscriptions, s...) })
}

// OutboxHandler registers outbox consumers built with outbox.Consume,
// ConsumeTx, ConsumeIntoTask or the raw forms (06 §6.4.3). A module's handlers
// are ConsumeIntoTask only (core/archtest.OutboxModuleConsumersAreTasks);
// each consumer name must be listed in Manifest.OutboxConsumers and each
// topic must be a Durable catalog entry. It only records (06 §6.5.1 P6): the
// kernel registers the handlers at P7, after every consumer name and topic is
// declared, and delivery begins when the relay starts (06 §6.5.2).
func (r *Registrar) OutboxHandler(h ...outbox.Handler) {
	r.record("OutboxHandler", func() { r.rec.OutboxHandlers = append(r.rec.OutboxHandlers, h...) })
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
	Name string // "<ID>.<name>", unique
	// Start may be nil. Its ctx has no deadline (Deadline reports ok=false):
	// the kernel's start window can move later through Deps.ExtendStart, and
	// a context's deadline cannot. When the window closes the kernel cancels
	// ctx with the cause context.DeadlineExceeded (ctx.Err() reports
	// context.DeadlineExceeded as well). A context derived from ctx should
	// ask context.Cause(ctx), not only ctx.Err(), why it ended.
	Start func(ctx context.Context) error
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
