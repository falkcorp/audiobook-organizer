// file: core/app/app.go
// version: 1.5.5
// guid: 5afb1db8-55c9-498d-8a73-d5b71909b728
// last-edited: 2026-11-11

// Package app is core's composition root (06 §6.4.5, 03 §3.3 row app): an
// app's main builds Options and its modules and calls New, then Run. Signal
// handling stays in main, which cancels Run's context, so Run is testable and
// a host that embeds the app controls its own lifecycle.
//
// At release R1 New runs the boot phases whose members exist (06 §6.4.10):
// manifest validation and enablement, logging, storage when a module
// declares it, metrics, each module's Register, the consumption of
// registrations and the root handler. Config, auth and tasks, and the
// Options fields and phases that need them, land with their packages.
package app

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/sdnotify"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// App is one booted process: the kernel behind New.
type App struct {
	k *kernel.Kernel
	// otel is the OTel SDK logger and error handler New installed for
	// Options.ProcessDefaults, which InstallProcessDefaults adopts; nil when
	// New installed none.
	otel *otelDiagnostics
	// slog is the slog default and standard library log output New installed
	// before module Register for Options.ProcessDefaults, which
	// InstallProcessDefaults adopts; nil when New installed none.
	slog *slogDefaults
}

// New runs the boot phases that exist at R1 (06 §6.5.1 P1, P2, P4a, P4b, P5,
// P6, P7, P8): manifest validation and enablement, the store with its
// checkpoint or fresh install and its migrations when a module declares
// storage, core services, Register of each enabled module, consumption of
// registrations, and the HTTP handler. It starts no
// module code that runs on its own (hooks, workers) and no listener. On
// error nothing is left open (COR-24); a validation failure is a
// *module.ValidationError listing every problem found.
func New(ctx context.Context, opts Options, mods ...module.Module) (*App, error) {
	c, err := kernelConfig(opts, mods)
	if err != nil {
		return nil, err
	}
	c.StorageFS = opts.StorageFS
	if c.StoragePath, c.StateDir, err = storagePath(opts, mods); err != nil {
		return nil, err
	}
	if c.StoragePath != "" {
		if c.StorageClaimsShadow, err = claimsShadow(opts); err != nil {
			return nil, err
		}
		c.StorageStateFiles = slices.Clone(opts.StorageStateFiles)
	}
	// The SDK's logger and error handler go in once the App's logging exists
	// and before the Provider is built (Options.ProcessDefaults).
	var diag *otelDiagnostics
	var sd *slogDefaults
	if opts.ProcessDefaults {
		// The pipeline is the process default before any module's Register
		// builds a service that captures slog.Default().
		c.ProcessLogging = func(root *slog.Logger, stdlib slog.Handler) error {
			sd = installSlogDefault(root, stdlib)
			return nil
		}
		c.BeforeTelemetry = func(l *slog.Logger) error {
			d, err := installOTelDiagnostics(l)
			if err != nil {
				return err
			}
			diag = d
			return nil
		}
	}
	if s := opts.R1Log; s != nil && s.Output == "file" && opts.Logger == nil {
		// The file output lives under the state directory (05-logging §4
		// step 1): resolved here, not in Describe, which does no I/O.
		if c.LogDir, err = ResolveStateDir(opts); err != nil {
			return nil, err
		}
	}
	k, err := kernel.New(ctx, c)
	if err != nil {
		if sd != nil {
			sd.restore()
		}
		if diag != nil {
			diag.restore()
		}
		return nil, err
	}
	if sm := bootseam.From(ctx); sm != nil && sm.Built != nil {
		// coretest's hold on the kernel; production boots carry no Seams.
		sm.Built(k)
	}
	return &App{k: k, otel: diag, slog: sd}, nil
}

// Run starts the app in the order of 06 §6.5.2; blocks until ctx is done or a
// StopAppOnExit worker exits; then shuts down in the reverse order within
// core.shutdown.budget (05-health §5H.4). A clean shutdown after ctx
// cancellation returns nil; a Stop hook's error or work abandoned at the
// budget is returned joined with the cause of the stop.
func (a *App) Run(ctx context.Context) error { return a.k.Run(ctx) }

// Close releases what New built, for an App that never Ran (coretest's
// harness, a command that only boots): the store, the metrics Provider and
// logging (06 §6.4.5). It is a no-op once Run has started: Run owns the
// shutdown. Run after Close returns the kernel's errRunTwice, and a second
// Close does nothing. Added with storage, the first thing New opens that must
// be closed (R1-20).
func (a *App) Close(ctx context.Context) error { return a.k.Close(ctx) }

// StorageAdmin is the store's admin surface (storage.Admin), nil when New
// opened no store. Release R1 only: ABO's books module still holds the
// store-wide administration that HEAD ran on its own engine (the backup
// checkpoint, the compaction op, the census flush and the Pebble collector's
// sample), and reaches it here once New returns. Each one moves into core
// with its subsystem (05-storage §5S.9.1: the census mechanism; 05-backup),
// and the accessor is deleted with the last of them.
func (a *App) StorageAdmin() *storage.Admin { return a.k.StorageAdmin() }

// CheckpointHealth is what health reports about the store's checkpoints
// (05-storage §5S.3.12; 05-health §5H.3.1): every checkpoint and scratch
// entry as it is now, and the last sweep's group- or world-accessible
// paths. Release R1 only: until F2's health registry and F8's core
// endpoints, the storage_checkpoint_* tokens travel the authenticated
// degraded field of ABO's GET /api/v1/system/status, which ABO's books
// module builds from this; the accessor is deleted when core reports them
// itself. Zero, with no error, when New opened no store.
func (a *App) CheckpointHealth() (storage.CheckpointHealth, error) { return a.k.CheckpointHealth() }

// Handler is the fully assembled root handler (for tests and embedding in
// another server). Valid after New. Embedding still needs Run: Handler alone
// starts no hooks or workers and does not mark the store's open checkpoint
// records served (06 §6.5.2 start step (0)), so a request served through it
// before Run writes past an upgrade checkpoint the next upgrade could still
// reuse. coretest's harness serves it without Run because a test store is
// never rolled back.
func (a *App) Handler() http.Handler { return a.k.Handler() }

// Routes describes every route of the built handler: native routes, the
// legacy manifest routes and the "/" owner (httpapi.Router.Routes). It is
// what coretest.AssertRoutesHavePermissions walks (03 R6, 06 §6.4.8); valid
// after New. Not in 06 §6.4.5's App: R1-37's seam, recorded in its seams file.
func (a *App) Routes() iter.Seq[httpapi.RouteInfo] { return a.k.Routes() }

// Metrics is the App's metrics Provider, valid after New. In release R1 it
// is the composition root's handle on the served registry: ABO registers its
// legacy client_golang families through Registerer() and its legacy gin
// /metrics route serves Handler() until F8 mounts /metrics in the kernel
// (05-metrics §6, §13 item 2); coretest.MetricsProvider returns it to series
// contract tests. Modules never see it: they get a Scope.
func (a *App) Metrics() *metrics.Provider { return a.k.Provider() }

// Logging is the App's log pipeline, valid after New: the composition root
// registers Logging.Tap consumers on it (ABO's activity feed). nil when
// Options.Logger replaced the pipeline. The App owns it and closes it last in
// shutdown (05-logging §4 step 4): a caller never calls Close.
func (a *App) Logging() *log.Logging { return a.k.Logging() }

// Modules reports each module's resolved state (enabled, disabled, reason),
// in ID order.
func (a *App) Modules() []ModuleState {
	ks := a.k.Modules()
	out := make([]ModuleState, 0, len(ks))
	for _, s := range ks {
		out = append(out, ModuleState(s))
	}
	return out
}

// ModuleState is one module's resolved state (06 §6.4.5).
type ModuleState struct {
	ID      module.ID
	Version string
	Enabled bool
	// Disabled is the reason when !Enabled. Until config lands (F3) no layer
	// can set core.modules.<id>.enabled, so the one reason is "default"
	// (Manifest.DefaultEnabled is false); from F3, "config" with the layer
	// that set it.
	Disabled        string
	RestartRequired bool // stored enablement differs from the running state (06 §6.5.3); false until config
}

// Describe runs the boot phases up to P7 with defaults only and every module
// treated as enabled, then Router.Build without serving (06 §6.4.5, COR2-13).
// It returns the OpenAPI 3.1 documents. No listener, no Run, and no log
// output. Register's side-effect rule (06 §6.4.1) is what makes it safe. Used
// by an app's "openapi" subcommand (05-http-api §7) and by contract tests.
func Describe(ctx context.Context, opts Options, mods ...module.Module) (Description, error) {
	c, err := kernelConfig(opts, mods)
	if err != nil {
		return Description{}, err
	}
	d, err := kernel.Describe(ctx, c)
	if err != nil {
		return Description{}, err
	}
	return Description{Core: d.Core, OpenAPI: d.OpenAPI}, nil
}

// Description is what Describe returns. UIManifests (07 §7.6.1) arrives with
// the manifests' permission names.
type Description struct {
	Core    []byte            // core's OpenAPI document; emitted ONLY by core/examples/hello's "openapi" subcommand (FE-20)
	OpenAPI map[string][]byte // per module ID, JSON (05-http-api Router.OpenAPI)
}

// ExtendStart asks the service manager for more start time before a startup
// step that makes real progress, for app code that runs before New (a store
// open, a migration step). It is stateless and never returns an error
// (05-health §5H.3.4): the request is EXTEND_TIMEOUT_USEC over NOTIFY_SOCKET,
// which moves the start deadline to at least sdnotify.StartupStepBudget from
// now and never closer. It is a no-op without NOTIFY_SOCKET, and a failed
// write is logged at WARN, never fatal. Call it per step, never from a timer:
// a hung step must still be killed once its own budget runs out. Modules use
// Deps.ExtendStart instead, which also extends the kernel's own start window.
func ExtendStart(step string) {
	if err := sdnotify.ExtendTimeout(sdnotify.StartupStepBudget); err != nil {
		slog.Warn("app: systemd start-timeout extension failed", "step", step, "err", err)
	}
}

// ResolveStateDir returns the state directory for opts by 05-config §1.7's
// pre-boot rule, which is config.ResolveStateDir: core's kernel and ABO's
// internal/config call the same function, so the three cannot drift. An
// explicit <EnvPrefix>_CORE_PATHS_STATE_DIR is trimmed, cleaned, created 0700
// and never falls back; a blank one is unset. Otherwise Options.StateDir is
// created 0700. When that cannot be created, the directory of an absolute
// <EnvPrefix>_CORE_STORAGE_PATH is used if it can be created, with HEAD's
// warning. Otherwise the error names the variable that chooses a directory,
// and the directory that could not be used is returned with it, as
// config.ResolveStateDir returns it. An app that touches state files before
// or outside New calls it instead of a resolver of its own (OPS4-8). The flag
// layer and the operator file's core.storage.path arrive with config (F3); in
// R1 the app passes its state-directory default as Options.StateDir
// (05-config §1.7).
func ResolveStateDir(opts Options) (string, error) {
	prefix, err := envPrefix(opts)
	if err != nil {
		return "", err
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	stateVar, dbVar := prefix+"_CORE_PATHS_STATE_DIR", prefix+"_CORE_STORAGE_PATH"
	explicit, dbPath := lookupEnv(env, stateVar), lookupEnv(env, dbVar)
	dir, fellBack, err := config.ResolveStateDir(explicit, opts.StateDir, dbPath,
		func(d string) error { return os.MkdirAll(d, 0o700) })
	if err != nil {
		// config.ResolveStateDir treats a blank explicit value as unset, so
		// the branch trims too.
		if strings.TrimSpace(explicit) != "" {
			return dir, fmt.Errorf("app: the state directory %q (%s) cannot be used: %w", dir, stateVar, err)
		}
		// With no default, config's error already says so; it is not repeated.
		what := fmt.Errorf("the default state directory %q cannot be created (%w)", dir, err)
		if strings.TrimSpace(opts.StateDir) == "" {
			what = err
		}
		return dir, fmt.Errorf("app: %w, and %s %q gives no absolute directory that can be created to keep state beside; set %s to a directory that can be created",
			what, dbVar, dbPath, stateVar)
	}
	if fellBack {
		logger := opts.Logger
		if logger == nil {
			logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
		}
		logger.Warn("app: the default state directory cannot be created; keeping state files beside the database",
			"default", opts.StateDir, "using", dir, "choose_with", stateVar)
	}
	return dir, nil
}

// lookupEnv returns name's value in env (KEY=value entries; the last wins, as
// os.Getenv), "" when unset or set to "" (05-config C3: an empty value is
// unset).
func lookupEnv(env []string, name string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// storagePath is core.storage.path and P0a's state directory for New when
// a module of the app declares storage, both "" otherwise, so an app without
// storage needs no state directory and creates none (R1: storage lands
// dark). Until config (F3) the store path's one operator layer is the
// environment: an absolute <EnvPrefix>_CORE_STORAGE_PATH, a relative one
// refused (05-storage §5S.4 step 1); otherwise the compiled default
// <state_dir>/storage/main.pebble (05-storage §5S.7). The state directory is
// ResolveStateDir's, made absolute, resolved once from pre-boot inputs only,
// even when <EnvPrefix>_CORE_STORAGE_PATH names the store (06 §6.5.1).
// Describe never calls it.
func storagePath(opts Options, mods []module.Module) (dbPath, stateDir string, err error) {
	if !slices.ContainsFunc(mods, func(m module.Module) bool { return m != nil && kernel.DeclaresStorage(m.Manifest()) }) {
		return "", "", nil
	}
	prefix, err := envPrefix(opts)
	if err != nil {
		return "", "", err
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	dbVar := prefix + "_CORE_STORAGE_PATH"
	dbPath = lookupEnv(env, dbVar)
	if dbPath != "" && !filepath.IsAbs(dbPath) {
		return "", "", fmt.Errorf("app: %s %q is not an absolute path (core.storage.path)", dbVar, dbPath)
	}
	if r1 := opts.R1StoragePath; r1 != "" {
		if !filepath.IsAbs(r1) {
			return "", "", fmt.Errorf("app: Options.R1StoragePath %q is not an absolute path (core.storage.path)", r1)
		}
		// Two sources naming two stores would let serve and the app's own
		// offline commands open different databases: refuse rather than
		// pick one.
		if dbPath != "" && filepath.Clean(dbPath) != filepath.Clean(r1) {
			return "", "", fmt.Errorf("app: %s %q and the app's database path %q (Options.R1StoragePath) name different stores; set one, or set both to the same path",
				dbVar, dbPath, r1)
		}
		dbPath = r1
	}
	if stateDir, err = ResolveStateDir(opts); err != nil {
		return "", "", err
	}
	if stateDir, err = filepath.Abs(stateDir); err != nil {
		return "", "", fmt.Errorf("app: state directory: %w", err)
	}
	if dbPath != "" {
		return filepath.Clean(dbPath), stateDir, nil
	}
	return filepath.Join(stateDir, "storage", "main.pebble"), stateDir, nil
}

// claimsShadow is core.storage.claims.mode (05-storage §5S.0 D-S2, §5S.9.4
// step 2): until config (F3) its one layer is <EnvPrefix>_CORE_STORAGE_CLAIMS_MODE.
// "strict" (the default, also when unset) refuses every out-of-claim access;
// "shadow", the emergency dial, logs, counts and allows it. Any other value
// fails the boot: a mistyped dial must not silently mean either.
func claimsShadow(opts Options) (bool, error) {
	prefix, err := envPrefix(opts)
	if err != nil {
		return false, err
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	name := prefix + "_CORE_STORAGE_CLAIMS_MODE"
	switch v := lookupEnv(env, name); v {
	case "", "strict":
		return false, nil
	case "shadow":
		return true, nil
	default:
		return false, fmt.Errorf("app: %s=%q is not a claims mode (strict or shadow; core.storage.claims.mode)", name, v)
	}
}

// envPrefix is Options.EnvPrefix, default strings.ToUpper(AppID), checked
// against [A-Z][A-Z0-9]{1,15} (05-config §1.3).
func envPrefix(opts Options) (string, error) {
	p := opts.EnvPrefix
	if p == "" {
		p = strings.ToUpper(opts.AppID)
	}
	ok := len(p) >= 2 && len(p) <= 16 && p[0] >= 'A' && p[0] <= 'Z'
	for _, c := range p {
		ok = ok && (c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
	}
	if !ok {
		return "", fmt.Errorf("app: env prefix %q is not [A-Z][A-Z0-9]{1,15} (Options.EnvPrefix, default the upper-cased AppID)", p)
	}
	return p, nil
}

// kernelConfig is the kernel's view of opts.
func kernelConfig(opts Options, mods []module.Module) (kernel.Config, error) {
	if opts.EnvPrefix != "" {
		if _, err := envPrefix(opts); err != nil {
			return kernel.Config{}, err
		}
	}
	c := kernel.Config{
		AppID:                 opts.AppID,
		MetricNamespace:       opts.MetricNamespace,
		ServiceName:           opts.ServiceName,
		Version:               opts.Version,
		ReservedAPINamespaces: slices.Clone(opts.ReservedAPINamespaces),
		LegacyAuthGinUntil:    opts.LegacyAuthGinUntil,
		Logger:                opts.Logger,
		Modules:               slices.Clone(mods),
	}
	if f := opts.R1Values; f != nil {
		c.R1Values = func() kernel.R1Values { return kernel.R1Values(f()) }
	}
	if opts.Logger == nil {
		c.LogOutput = opts.LogOutput
		if opts.R1Log != nil {
			s := *opts.R1Log
			s.Levels = maps.Clone(s.Levels)
			s.RedactKeys = slices.Clone(s.RedactKeys)
			c.Log = &s
		}
	}
	if f := opts.R1Listen; f != nil {
		c.Listen = func(o httpapi.ListenOptions, h http.Handler, l *slog.Logger) (kernel.Server, error) {
			srv, err := f(o, h, l)
			if err != nil || srv == nil {
				if err == nil {
					err = errors.New("app: Options.R1Listen returned no server")
				}
				return nil, err
			}
			return srv, nil
		}
	}
	if t := opts.Telemetry; t != nil {
		kt := kernel.Telemetry(*t)
		c.Telemetry = &kt
	}
	return c, nil
}
