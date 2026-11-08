// file: core/kernel/kernel.go
// version: 1.0.10
// guid: 2f2f54dc-093c-4cea-9ec3-7078e672dc38
// last-edited: 2026-11-08

// Package kernel is core's boot machinery (06 §6.4.6): the boot-phase runner
// of 06 §6.5.1, the Registrar and Deps of each module, the collision checks
// across modules and core, and the supervisor of 06 §6.5.2. Apps never import
// it; package app is its only caller.
//
// The phases whose members exist at release R1 run: P1 (manifest validation
// and enablement), P2, P4a and P4b (storage open, checkpoint or fresh
// install, migrations; storage.go) when core.storage.path is set, P5
// (metrics and the router), P6 (Register), P7 (consumption of registrations)
// and P8 (the root handler). P0a, P0 and P3 arrive with config and the swap;
// their members are absent, not stubbed, and those phases are skipped (06
// §6.4.10). 06 §6.4.6 sketches kernel/catalog, kernel/services and
// kernel/supervisor packages; here they are files of this one package
// (kernel.go, boot.go, storage.go, supervisor.go) until the phases that fill
// them land.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/ui"
	"github.com/falkcorp/mediacore/version"

	"go.opentelemetry.io/otel/metric"
)

// Config is what app.New hands the kernel: the app.Options fields the kernel
// consumes, plus the kernel-only seams its tests inject.
type Config struct {
	AppID                 string // [a-z][a-z0-9]{1,15} (03 §3.6), checked in P1
	MetricNamespace       string // "" = metrics.DefaultNamespace
	ServiceName           string // "" = AppID
	Version               string // the app's release version: the resource's service.version
	ReservedAPINamespaces []string
	LegacyAuthGinUntil    module.ID
	// R1Values is app.Options.R1Values, converted. The supervisor calls it
	// once per Run, at start step (7), before the listener opens. nil = every
	// value at core's default.
	R1Values func() R1Values
	// Logger replaces the log.Logging the kernel would build (tests).
	Logger *slog.Logger
	// LogOutput is where the kernel's log.Logging writes; nil = os.Stderr.
	// Ignored when Logger is set.
	LogOutput io.Writer
	// Notifier receives the readiness signals; nil = systemd's notify
	// protocol over NOTIFY_SOCKET (a no-op while that is unset).
	Notifier ReadinessNotifier
	// ExtendStart replaces the request every module's Deps.ExtendStart makes
	// to the service manager (tests); nil = EXTEND_TIMEOUT_USEC over
	// NOTIFY_SOCKET.
	ExtendStart func(step string)
	// Listen opens the listener of start step (7); nil = httpapi.Listen.
	Listen func(httpapi.ListenOptions, http.Handler, *slog.Logger) (Server, error)
	// Exit ends the process after a restart request's stop order; nil =
	// os.Exit. It is called with restartExitCode only, never 0.
	Exit func(code int)
	// BeforeTelemetry runs once in P5, after logging is built and before the
	// metrics Provider (and with it the resource and any OTLP exporter), with
	// core's "otel" logger. app.New sets it for Options.ProcessDefaults to
	// install the OTel SDK's value-dropping logger and error handler before
	// the SDK reads OTEL_* from the environment. An error fails boot.
	// Describe never calls it.
	BeforeTelemetry func(log *slog.Logger) error
	// Telemetry is app.Options.Telemetry, converted: the part of the
	// Provider's metrics.Options that the core.metrics and core.tracing
	// sections describe; from F3 core config fills it. nil = the Prometheus
	// scrape only. Describe ignores it (an offline generator builds no
	// exporter).
	Telemetry *Telemetry
	// Modules are the modules compiled into the app, enabled or not.
	Modules []module.Module
	// StoragePath is core.storage.path, absolute (05-storage §5S.4 step 1).
	// Set, the kernel runs P2, P4a and P4b over it with core/storage and
	// every module that declares storage as owners. "" = no store: boot is
	// refused when an enabled module declares storage. Describe never opens
	// the store.
	StoragePath string
	// StorageFS is the filesystem P2 opens the store on; the zero value is
	// the OS's. Tests only: coretest's harness passes storage.MemFS().
	StorageFS storage.FS
	// StateDir is core.paths.state_dir, absolute, resolved once from
	// pre-boot inputs (06 §6.4.5 StateDir). Set, the kernel runs P0a, the
	// pre-boot swap, over <StateDir>/restore/ before the store opens, and
	// New refuses it without StoragePath. "" = no swap. Describe never runs
	// P0a.
	StateDir string
}

// R1Values mirrors app.R1Values field for field (05-config §1.12.1), so app
// converts one into the other without copying fields by hand; the kernel
// cannot import app. F3 deletes both.
type R1Values struct {
	Listen                       string
	TLSCertFile, TLSKeyFile      string
	PeerAllowlist                []string
	TrustedProxies               []string
	RateLimitEnabled             bool
	RateLimitPerMinute           int
	JSONMaxBytes, UploadMaxBytes int64
	ShutdownBudget               time.Duration
}

// Telemetry mirrors app.Telemetry field for field, so app converts one into
// the other without copying fields by hand (the kernel cannot import app):
// the OTLP metric push, the OTLP span exporter and the resource attributes
// of the kernel's Provider. The propagator needs no option: the Provider's
// is always W3C trace context plus baggage.
type Telemetry struct {
	OTLP        *metrics.OTLPMetrics
	Tracing     *metrics.OTLPTracing
	Environment string
	InstanceID  string
}

// ModuleState mirrors app.ModuleState (06 §6.4.5): one module's resolved
// state.
type ModuleState struct {
	ID              module.ID
	Version         string
	Enabled         bool
	Disabled        string
	RestartRequired bool
}

// Description mirrors app.Description's R1 fields: core's OpenAPI document and
// each module's.
type Description struct {
	Core    []byte
	OpenAPI map[string][]byte
}

// disabledByDefault is ModuleState.Disabled for a module whose manifest says
// DefaultEnabled: false. Until config lands (F3) no layer can set
// core.modules.<id>.enabled, so the default is the only reason.
const disabledByDefault = "default"

// Kernel is one booted app: the catalog of modules, the services P5 built, the
// registrations P6 recorded and the handler P8 assembled. Run supervises it.
type Kernel struct {
	cfg      Config
	notifier ReadinessNotifier
	exit     func(int)
	listen   func(httpapi.ListenOptions, http.Handler, *slog.Logger) (Server, error)

	// startWin is the running hook Start's window, which Deps.ExtendStart
	// extends (supervisor.go).
	startWin startWindow

	entries []*entry // every module, in ID order (P1)
	timings []phaseTiming

	log       *slog.Logger // core's kernel logger
	logging   *log.Logging // nil when Config.Logger is set
	redactor  *log.Redactor
	provider  *metrics.Provider
	scope     *metrics.Scope // "<namespace>.kernel"
	bootPhase metric.Float64Gauge
	router    *httpapi.Router
	handler   http.Handler

	store *kernelStore // P2 on; nil when no store was opened (storage.go)
	swap  *kernelSwap  // P0a on; nil when P0a did not run (swap.go)
	build string       // the binary ID, once computed (swap.go)

	hooks     []moduleHook
	workers   []moduleWorker
	lifecycle *Lifecycle

	lateMu    sync.Mutex
	lateStops []chan struct{} // closed when each stopInBackground goroutine ends (supervisor.go)

	ran       atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// entry is one module as the catalog sees it.
type entry struct {
	mod     module.Module
	man     module.Manifest
	enabled bool
	held    string               // ModuleState.Disabled when storage holds an enabled module (storage.go)
	rec     module.Registrations // P6, enabled modules only
}

// New runs the boot phases that exist (P1, P5, P6, P7, P8). It starts no
// module code that runs on its own (hooks, workers) and no listener. On any
// error everything P5 built is closed in reverse and nil is returned
// (COR-24).
func New(ctx context.Context, c Config) (*Kernel, error) {
	return boot(ctx, c, false)
}

// Describe runs P1, P5, P6 and P7 with every module enabled and its logging
// discarded, then Router.Build without serving (06 §6.4.5 Describe, COR2-13),
// and returns core's OpenAPI document and each module's. Nothing is left
// running. It skips P2 to P4 whatever Config.StoragePath says, so it opens no
// store, touches no state directory and does no I/O beyond what Register
// itself is forbidden to do (§6.4.1); a module's Deps.Storage handles are
// zero values there.
func Describe(ctx context.Context, c Config) (Description, error) {
	k, err := boot(ctx, c, true)
	if err != nil {
		return Description{}, err
	}
	d, err := k.describe()
	if cerr := k.closeCore(ctx, time.Now().Add(defaultShutdownBudget)); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return Description{}, err
	}
	return d, nil
}

// Handler is the root handler P8 built. Serving it without Run marks no
// checkpoint record served (Run's start step (0), markServed); App.Handler
// says what that means for an embedder.
func (k *Kernel) Handler() http.Handler { return k.handler }

// Routes is the router's Routes(): every route P8 built, for coretest's R6
// gate (06 §6.4.8 AssertRoutesHavePermissions; R1-37's seam).
func (k *Kernel) Routes() iter.Seq[httpapi.RouteInfo] { return k.router.Routes() }

// Provider is the metrics Provider P5 built: the App's private registry,
// MeterProvider, TracerProvider and propagator. package app reads it for
// InstallProcessDefaults and for the composition root's R1 handle on the
// served registry (05-metrics §6 R1 row).
func (k *Kernel) Provider() *metrics.Provider { return k.provider }

// RootLogger is the App's process default logger: Logging.Root(), which
// carries no module attribute (core's own loggers, CoreLogger, carry
// module=core), or Config.Logger when the kernel adopted one.
// InstallProcessDefaults makes it slog's default.
func (k *Kernel) RootLogger() *slog.Logger {
	if k.logging != nil {
		return k.logging.Root()
	}
	if k.cfg.Logger != nil {
		return k.cfg.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// CoreLogger is core's logger for one subsystem (module=core).
func (k *Kernel) CoreLogger(subsystem string) *slog.Logger { return k.coreLog(subsystem) }

// familyTaken is the boot error for a second owner of a Prometheus family
// refused while the app was booting (05-metrics §8: fail-closed during boot;
// after it the second owner stays a no-op and the token is raised). The
// Provider logged the family and both owners when it refused.
func (k *Kernel) familyTaken(when string) error {
	if k.provider == nil || !slices.Contains(k.provider.Degraded(), metrics.DegradedFamilyTaken) {
		return nil
	}
	return fmt.Errorf("kernel: %w %s (the %s log line names the family and both owners)",
		metrics.ErrFamilyTaken, when, metrics.DegradedFamilyTaken)
}

// Modules reports every module compiled into the app, in ID order.
func (k *Kernel) Modules() []ModuleState {
	out := make([]ModuleState, 0, len(k.entries))
	for _, e := range k.entries {
		s := ModuleState{ID: e.man.ID, Version: e.man.Version, Enabled: e.enabled}
		if !e.enabled {
			s.Disabled = disabledByDefault
			if e.held != "" {
				s.Disabled = e.held
			}
		}
		out = append(out, s)
	}
	return out
}

// describe builds the OpenAPI documents of core and of every enabled module.
func (k *Kernel) describe() (Description, error) {
	core, err := k.document("core", version.Version)
	if err != nil {
		return Description{}, err
	}
	d := Description{Core: core, OpenAPI: make(map[string][]byte)}
	for _, e := range k.entries {
		if !e.enabled {
			continue
		}
		b, err := k.document(string(e.man.ID), e.man.Version)
		if err != nil {
			return Description{}, err
		}
		d.OpenAPI[string(e.man.ID)] = b
	}
	return d, nil
}

// document is one owner's OpenAPI document with info.version set: the
// module's Manifest.Version, version.Version for core (05-http-api §7).
func (k *Kernel) document(owner, ver string) ([]byte, error) {
	doc, err := k.router.OpenAPI(owner)
	if err != nil {
		return nil, fmt.Errorf("kernel: OpenAPI of %s: %w", owner, err)
	}
	doc.Info.Version = ver
	return doc.JSON()
}

// catalog is P1: every manifest, enabled or not; IDs against the reserved
// names; MinCoreVersion against version.Version; enablement (06 §6.5.1 P1).
// Static checks only. The legacy-claims collision check of 06 §6.4.6 is
// httpapi's one rule (Router.Claim, 05-http-api §4.9); the kernel hands every
// manifest's claims to the router in P7, where a collision is reported. At
// R1 nothing between P1 and P7 opens or writes anything, so the later check
// fails boot at the same point of no consequence.
func (k *Kernel) catalog(describe bool) error {
	var probs []module.Problem
	if !validName(k.cfg.AppID) {
		probs = append(probs, module.Problem{Kind: module.ProblemInvalidID,
			Detail: fmt.Sprintf("app ID %q is not [a-z][a-z0-9]{1,15} (app.Options.AppID, 03 §3.6)", k.cfg.AppID)})
	}
	// The first segment of every manifest's LegacyPrefixes is a reserved ID
	// for every other module (FE2-11).
	legacy := make(map[string][]module.ID)
	for _, m := range k.cfg.Modules {
		if m == nil {
			continue
		}
		man := m.Manifest()
		for _, p := range man.LegacyPrefixes {
			if s := firstSegment(p); s != "" && !slices.Contains(legacy[s], man.ID) {
				legacy[s] = append(legacy[s], man.ID)
			}
		}
	}
	core := module.ReservedIDs()
	seen := make(map[module.ID]bool)
	for i, m := range k.cfg.Modules {
		if m == nil {
			probs = append(probs, module.Problem{Kind: module.ProblemInvalidID,
				Detail: fmt.Sprintf("module %d of app.New is nil", i)})
			continue
		}
		man := m.Manifest()
		id := man.ID
		switch {
		case !validName(string(id)):
			probs = append(probs, module.Problem{Module: id, Kind: module.ProblemInvalidID,
				Detail: fmt.Sprintf("ID %q is not ^[a-z][a-z0-9]{1,15}$ (03 §3.6)", id)})
		case module.IsReserved(id, core):
			probs = append(probs, module.Problem{Module: id, Kind: module.ProblemReservedID,
				Detail: fmt.Sprintf("ID %q is a path segment or namespace core owns (module.ReservedIDs)", id)})
		case slices.Contains(k.cfg.ReservedAPINamespaces, string(id)):
			probs = append(probs, module.Problem{Module: id, Kind: module.ProblemReservedID,
				Detail: fmt.Sprintf("ID %q is reserved by app.Options.ReservedAPINamespaces", id)})
		default:
			if owners := slices.DeleteFunc(slices.Clone(legacy[string(id)]), func(o module.ID) bool { return o == id }); len(owners) > 0 {
				probs = append(probs, module.Problem{Module: id, Kind: module.ProblemReservedID,
					Detail: fmt.Sprintf("ID %q is the first segment of a LegacyPrefixes entry of module %s", id, owners[0])})
			}
		}
		if seen[id] {
			probs = append(probs, module.Problem{Module: id, Kind: module.ProblemDuplicateID,
				Detail: fmt.Sprintf("two modules declare ID %q", id)})
			continue
		}
		seen[id] = true
		if p, ok := coreTooOld(id, man.MinCoreVersion); ok {
			probs = append(probs, p)
		}
		k.entries = append(k.entries, &entry{mod: m, man: man, enabled: describe || man.DefaultEnabled})
	}
	slices.SortFunc(k.entries, func(a, b *entry) int { return strings.Compare(string(a.man.ID), string(b.man.ID)) })
	return joined(probs)
}

// register is P6: for each enabled module in ID order, its Registrar and
// Deps, Register, Err, Close. The Registrar's Context is cancelled when New
// returns or New's ctx is cancelled (06 §6.4.3, COR-24).
func (k *Kernel) register(ctx context.Context) error {
	regCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var probs []module.Problem
	var errs []error
	for _, e := range k.entries {
		if !e.enabled {
			continue
		}
		id := e.man.ID
		scope, err := k.provider.Module(string(id))
		if err != nil {
			errs = append(errs, fmt.Errorf("kernel: metrics scope of module %s: %w", id, err))
			continue
		}
		deps := module.Deps{Module: id, Log: k.moduleLog(id), Metrics: scope, ExtendStart: k.extendStart(id),
			RequestRestart: k.lifecycle.RequestRestart, Storage: k.storageHandles(id)}
		r := module.NewRegistrar(module.RegistrarConfig{
			Context:       regCtx,
			Deps:          deps,
			HTTP:          k.router.ModuleGroup(string(id)),
			LegacyAuthGin: k.cfg.LegacyAuthGinUntil != "" && id == k.cfg.LegacyAuthGinUntil,
			// Err is read once, below: a call after Close (from a goroutine
			// Register started, against 06 §6.4.1) would otherwise vanish.
			AfterClose: func(p module.Problem) {
				k.log.Error("kernel: registrar call after Register returned; ignored", "module", p.Module, "problem", p.Error())
			},
		})
		err = callRegister(e.mod, r)
		e.rec = r.Close()
		recorded, hasRecorded := errors.AsType[*module.ValidationError](r.Err())
		if hasRecorded {
			probs = append(probs, recorded.Problems...)
		}
		// Register commonly ends with "return r.Err()" (06 §6.12.1): that
		// error is the registrar's own problems, already collected above.
		if _, isOwn := errors.AsType[*module.ValidationError](err); err != nil && !(isOwn && hasRecorded) {
			errs = append(errs, fmt.Errorf("kernel: module %s: Register: %w", id, err))
		}
	}
	return errors.Join(append(errs, joined(probs))...)
}

// callRegister calls Register, turning a panic into an error: a module that
// panics while registering fails boot like one that returns an error.
func callRegister(m module.Module, r *module.Registrar) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return m.Register(r)
}

// consume is P7 at R1: every manifest's claims (a disabled module's stay
// reserved), each enabled module's public route names, its legacy mounts with
// the one "/" owner (ProblemSPAOwner), the mount post-checks of 03 §3.6, and
// the hooks and workers the supervisor runs, and the router's legacy-mount
// admission (the module's auth method names).
// AuthMethods' specs, LogPathRules and LogOtherUserData stay recorded
// otherwise: their consumers (auth, the redactor's live rules, the journal)
// land later.
func (k *Kernel) consume() error {
	var probs []module.Problem
	// The router records each refusal for Build as well; P7 fails on the
	// first report, so Build never repeats one.
	//
	// A disabled module's names and claims stay reserved (06 §6.5.1 P1;
	// 05-http-api §4.9): its /api/<id>/ is claimed like an enabled module's
	// (ModuleGroup claimed those at P6), and core's /api/core/ was claimed at
	// P5, so a legacy prefix over either collides now, not on the day the
	// module is enabled. They are claimed before any legacy prefix, so the
	// collision is the prefix owner's.
	for _, e := range k.entries {
		if !e.enabled {
			id := string(e.man.ID)
			probs = append(probs, problemsFrom(e.man.ID, k.router.Claim(id, "/api/"+id+"/"))...)
		}
	}
	for _, e := range k.entries {
		for _, p := range e.man.LegacyPrefixes {
			probs = append(probs, problemsFrom(e.man.ID, k.router.Claim(string(e.man.ID), p))...)
		}
	}
	spaOwner := module.ID("")
	for _, e := range k.entries {
		if !e.enabled {
			continue
		}
		id := e.man.ID
		k.router.AllowPublic(string(id), e.man.PublicRoutes...)
		declared := make([]string, 0, len(e.man.LegacyPrefixes))
		for _, p := range e.man.LegacyPrefixes {
			declared = append(declared, firstSegment(p))
		}
		for _, m := range e.rec.Mounts {
			if m.ServesSPA {
				if spaOwner != "" {
					probs = append(probs, module.Problem{Module: id, Kind: module.ProblemSPAOwner,
						Detail: fmt.Sprintf("a second ServesSPA legacy mount; module %s already owns \"/\" (FE-1)", spaOwner)})
					continue
				}
				spaOwner = id
			}
			probs = append(probs, k.checkMountSegments(id, declared, m)...)
			probs = append(probs, problemsFrom(id, k.router.MountLegacy(m))...)
		}
		// The admission Build applies to the mounts' manifests (05-http-api
		// §8.2, SEC2-17): an OwnAuth route names a method this module
		// registered. LegacyAuthGin is the Registrar's check (P6).
		methods := make([]string, 0, len(e.rec.AuthMethods))
		for _, spec := range e.rec.AuthMethods {
			methods = append(methods, string(spec.Method))
		}
		k.router.AllowAuthMethods(string(id), methods...)
		for _, h := range e.rec.Hooks {
			k.hooks = append(k.hooks, moduleHook{module: id, hook: h})
		}
		for _, w := range e.rec.Workers {
			k.workers = append(k.workers, moduleWorker{module: id, worker: w})
		}
	}
	return joined(probs)
}

// checkMountSegments is P7's post-check of one mount (06 §6.5.1 P7, 03 §3.6):
// every TopSegments and SPAShared entry is the first segment of one of its
// module's declared LegacyPrefixes, and no other module's ID equals one.
func (k *Kernel) checkMountSegments(owner module.ID, declared []string, m httpapi.LegacyMount) []module.Problem {
	var probs []module.Problem
	for _, seg := range slices.Concat(m.TopSegments, m.SPAShared) {
		if !slices.Contains(declared, seg) {
			probs = append(probs, module.Problem{Module: owner, Kind: module.ProblemRouteOutsidePrefix,
				Detail: fmt.Sprintf("legacy mount segment %q is not the first segment of a Manifest.LegacyPrefixes entry", seg)})
		}
		for _, e := range k.entries {
			if e.man.ID != owner && string(e.man.ID) == seg {
				probs = append(probs, module.Problem{Module: e.man.ID, Kind: module.ProblemReservedID,
					Detail: fmt.Sprintf("ID %q is a top-level segment of module %s's legacy mount", seg, owner)})
			}
		}
	}
	return probs
}

// handlerPhase is P8: Router.Build, its errors mapped to problems, and the
// shell-segment check (06 §6.5.1 P8). There is no core "/" owner before the
// shell (ui.Handler lands with it), so a ServesSPA mount or nothing owns "/".
func (k *Kernel) handlerPhase() error {
	h, err := k.router.Build()
	probs := problemsFrom("", err)
	nonSPA := k.router.NonSPASegments()
	for _, seg := range ui.ReservedSegments() {
		if slices.Contains(nonSPA, seg) {
			probs = append(probs, module.Problem{Kind: module.ProblemSPASegmentCollision,
				Detail: fmt.Sprintf("shell route segment %q is a legacy mount's TopSegments entry and not in its SPAShared, so a page load could never reach the shell (FE2-7)", seg)})
		}
	}
	if err := joined(probs); err != nil {
		return err
	}
	k.handler = h
	return nil
}

// routerKinds maps the registration errors of httpapi's Router to the
// ProblemKind that names the same rule (06 §6.4.7). Anything else is a
// registration httpapi refuses for its shape: ProblemInvalidRegistration.
func routerKinds() []struct {
	err  error
	kind module.ProblemKind
} {
	return []struct {
		err  error
		kind module.ProblemKind
	}{
		{httpapi.ErrNoPermission, module.ProblemRouteWithoutPermission},
		{httpapi.ErrOutsideClaim, module.ProblemRouteOutsidePrefix},
		{httpapi.ErrPrefixCollision, module.ProblemLegacyPrefixCollision},
		{httpapi.ErrForeignScope, module.ProblemPermissionNamespace},
		{httpapi.ErrUnknownScope, module.ProblemUndeclaredPermission},
		{httpapi.ErrLegacyAuthGin, module.ProblemLegacyAuth},
		{httpapi.ErrOwnAuthMethod, module.ProblemLegacyAuth},
	}
}

// problemsFrom turns a Router error (errors.Join of several, from Build) into
// one Problem per leaf, each keeping its httpapi error for errors.Is. Build's
// errors name their owner in the text only, so their Problem.Module is "".
func problemsFrom(owner module.ID, err error) []module.Problem {
	var probs []module.Problem
	for _, leaf := range leaves(err) {
		kind := module.ProblemInvalidRegistration
		for _, bk := range routerKinds() {
			if errors.Is(leaf, bk.err) {
				kind = bk.kind
				break
			}
		}
		probs = append(probs, module.NewProblem(owner, kind, leaf))
	}
	return probs
}

// leaves flattens a tree of errors.Join values into its leaves.
func leaves(err error) []error {
	if err == nil {
		return nil
	}
	j, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []error{err}
	}
	var out []error
	for _, e := range j.Unwrap() {
		out = append(out, leaves(e)...)
	}
	return out
}

// joined is a *module.ValidationError of probs, or nil.
func joined(probs []module.Problem) error {
	if len(probs) == 0 {
		return nil
	}
	return &module.ValidationError{Problems: probs}
}

// coreTooOld compares MinCoreVersion with version.Version. An empty value is
// no check; a value that is not MAJOR.MINOR.PATCH cannot be shown to be met,
// so it fails closed with the same kind.
func coreTooOld(id module.ID, min string) (module.Problem, bool) {
	if min == "" {
		return module.Problem{}, false
	}
	need, ok := parseVersion(min)
	if !ok {
		return module.Problem{Module: id, Kind: module.ProblemCoreTooOld,
			Detail: fmt.Sprintf("MinCoreVersion %q is not MAJOR.MINOR.PATCH", min)}, true
	}
	have, ok := parseVersion(version.Version)
	if !ok || slices.Compare(have[:], need[:]) < 0 {
		return module.Problem{Module: id, Kind: module.ProblemCoreTooOld,
			Detail: fmt.Sprintf("needs core %s, this is core %s", min, version.Version)}, true
	}
	return module.Problem{}, false
}

// parseVersion reads "MAJOR.MINOR.PATCH" with decimal parts and no prefix or
// suffix.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return v, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return v, false
			}
			v[i] = v[i]*10 + int(c-'0')
		}
	}
	return v, true
}

// validName reports [a-z][a-z0-9]{1,15}, the module ID and app ID pattern
// (03 §3.6).
func validName(s string) bool {
	if len(s) < 2 || len(s) > 16 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// firstSegment is the first path segment of a claim: "api" of "/api/v1/",
// "ping" of "/ping"; "" for "/" or a value that is not a path.
func firstSegment(p string) string {
	rest, ok := strings.CutPrefix(p, "/")
	if !ok {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	return seg
}
