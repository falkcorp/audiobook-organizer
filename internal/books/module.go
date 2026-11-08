// file: internal/books/module.go
// version: 1.2.0
// guid: 1eb7c933-3c90-4be5-a58c-52b621bd1dfe
// last-edited: 2026-11-08

// Package books is ABO's audiobook module and its composition-root glue (04
// §2.4, 06 §6.11.1 step 2): Module() is the module.Module ABO's main hands
// to app.New, Options is the app.Options of release R1, and Attach
// binds the App's served registry once New has returned. It is computed
// layer 6 and imported only by the main package and cmd (03 §3.5.1 item 1);
// every package below it receives what it needs as values this package
// wires, never mediacore/module or mediacore/app.
//
// In release R1 books wraps HEAD's server over the store core opens (R1-23,
// 05-storage §5S.9.3): Register builds books' store on its LegacyRange, runs
// HEAD's settings boot over it (boot.go), builds today's *server.Server and
// mounts its gin engine with Registrar.MountLegacy (LegacyAuthGin,
// ServesSPA); its repairs hook runs HEAD's open-time writers and closes the
// store last, and its server hook runs HEAD's start up to the listener and
// HEAD's post-drain stop; the listener stack itself is HEAD's, handed to the
// kernel through app.Options.R1Listen until R1-17 builds core's perimeter.
package books

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// bootLog logs books' boot (the settings boot and the store's lifecycle).
var bootLog = logger.New("books")

// ID is books' module ID.
const ID module.ID = "books"

// minCoreVersion is the lowest core version books needs (MOD-12): the core
// this release was built against.
const minCoreVersion = "0.1.0"

// moduleVersion is books' Manifest.Version (reported by the modules API).
// The module ships inside ABO's binary, whose build version is not semver
// ("dev" on a local build), so the module carries its own.
const moduleVersion = "1.0.0"

// absAuthMethod is the compat auth method books registers for its ABS
// routes (05-authn §3.3); the legacy manifest's OwnAuth rows name it
// (internal/server legacy_manifest.go), and the kernel's admission refuses a
// boot where they differ.
const absAuthMethod auth.Method = "books.abs"

// errNotBooks is what Options and Attach return for a module.Module that
// Module did not build.
var errNotBooks = errors.New("books: not the module books.Module returned")

// Module returns books for the composition root (03 §3.5.1 item 1): core
// opens the store at P2 and books reaches it through its claims (R1-23).
// Module does no work: it reads whether the ABS surface is on, which decides
// the legacy prefixes the manifest declares. The kernel reads the manifest
// before the store opens, so that is the value from the environment and the
// config file; the ABS flag is environment-authoritative (internal/config
// applyEnvAuthoritativeConfig), and Register refuses a boot whose stored
// settings turn it the other way (errABSFlagDrift).
func Module() module.Module {
	return &booksModule{abs: config.AppConfig.ABSAPIEnabled}
}

// booksModule is books' module.Module.
type booksModule struct {
	abs bool

	// steps is serve's startup runner and stateDir the secure state
	// directory, both set by Options before app.New, whose Register reads
	// them on the same goroutine.
	steps    *startup.Steps
	stateDir string
	// beforeSettings, when set (tests), runs on the store Register built,
	// before the settings boot: a stored settings blob, the seeded roles.
	beforeSettings func(*database.PebbleStore)

	// store is the store Register built over books' LegacyRange.
	store atomic.Pointer[database.PebbleStore]
	// repairsBegan is set when the repairs hook starts; from then the
	// kernel's stop order (or the hook's own failure path) closes the store,
	// and Release leaves it alone.
	repairsBegan atomic.Bool

	// cfg is serve's listener flags, set by Options before app.New; the hook
	// and R1Listen read it.
	cfg atomic.Pointer[server.ServerConfig]
	// srv is the server Register built.
	srv atomic.Pointer[server.Server]
	// registered counts Register calls (TestBooksDisabledInert).
	registered atomic.Int32
}

// Manifest is books' static declaration. It is pure: the legacy prefixes
// (server.LegacyPrefixes: every route's subtree, every top segment and
// "login") follow from the ABS flag Module captured.
func (m *booksModule) Manifest() module.Manifest {
	return module.Manifest{
		ID:             ID,
		Version:        moduleVersion,
		DisplayName:    "Audiobooks",
		MinCoreVersion: minCoreVersion,
		DefaultEnabled: true,
		LegacyPrefixes: server.LegacyPrefixes(m.abs),
		// Books' claims on HEAD's key families, its legacy migrations and
		// its versioned ones, and the projection HEAD's guard reads
		// (05-storage §5S.3.4, §5S.9.3; internal/database booksspec.go).
		Storage: database.BooksSpec(),
		// No native route yet, so no public one (the gin mount's public
		// routes are the legacy manifest's Public rows).
	}
}

// errABSFlagDrift is Register's refusal when the stored settings turn the ABS
// surface the other way from the environment Module read: the manifest's
// legacy prefixes were fixed from the environment before the store opened.
var errABSFlagDrift = errors.New("books: the stored settings set abs_api_enabled differently from the environment; " +
	"set ABS_API_ENABLED in the environment (it is environment-authoritative), so the legacy prefixes fixed before the store opened match the server")

// reservedAPINamespaces are the second segments under /api of ABO's
// unversioned surfaces (the ABS protocol's /api/me, /api/items, ...), which
// the kernel adds to the reserved module IDs (03 §3.6): every one the ABS
// surface can register, whether or not it is on, so no module takes a name
// enabling ABS would need. The versioned v<N> segments are reserved already.
func reservedAPINamespaces() []string {
	set := map[string]bool{}
	for _, lr := range server.LegacyRoutes(true) {
		segs := strings.Split(strings.TrimPrefix(lr.GinPath, "/"), "/")
		if segs[0] != "api" || len(segs) < 2 || versionSegment(segs[1]) {
			continue
		}
		set[segs[1]] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// versionSegment reports a "v<digits>" segment.
func versionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Register builds books' store over its LegacyRange, runs HEAD's settings
// boot over it, builds HEAD's server and records books' mount, auth method
// and hooks (06 §6.11.1 step 2). The settings boot and the server are R1's
// documented exception to Register's no-I/O rule (06 §6.4.1): NewServer
// constructs HEAD's whole service container from the effective config, and
// books has no other place to build the engine MountLegacy needs before P7.
// The rows that split the server retire it (seams.d/R1-15.md,
// seams.d/R1-23.md). Under Describe (no store: P2 to P4 skipped) books
// declares nothing beyond its manifest.
func (m *booksModule) Register(r *module.Registrar) error {
	m.registered.Add(1)
	legacy := r.Deps().Storage.Legacy
	if legacy == (storage.LegacyRange{}) {
		return nil
	}
	store, err := database.NewBooksStore(legacy)
	if err != nil {
		return fmt.Errorf("books: %w", err)
	}
	// Stored before any step can fail, so Release closes it when New then
	// refuses.
	m.store.Store(store)
	database.SetGlobalStore(store)
	if m.beforeSettings != nil {
		m.beforeSettings(store)
	}
	steps := m.steps
	if steps == nil {
		steps = newSteps()
	}
	settingsWrites, err := bootSettings(store, steps, m.stateDir)
	if err != nil {
		return err
	}
	if config.AppConfig.ABSAPIEnabled != m.abs {
		return errABSFlagDrift
	}
	// The shared Google Books daily budget persists its count in this store
	// for the life of the process: attached BEFORE the server is built, so
	// no Google call made while it starts counts against an in-memory zero
	// the attach would then discard. Process-scoped, so not inside
	// NewServer (see AttachProviderThrottleStore).
	server.AttachGoogleBooksBudgetStore(store)
	// The registry's and the flight recorder's logger is the module logger: it
	// reaches the journal, the log file and the per-op log store, but its
	// records are not Process, so the registry's per-run info lines (enqueued,
	// dispatched, started, finished) do not each become a system Activity row.
	// The op's own rows come from the registry's activity mirror. Passing nil
	// would capture slog.Default(), the process logger the Activity tap feeds.
	srv := server.NewServer(store, server.WithLogger(r.Deps().Log))
	// Stored before any check can fail, so Release tears it down when New
	// then refuses.
	m.srv.Store(srv)
	// The search index opens in the start hook, a long step: it asks for
	// start time through the kernel's Deps.ExtendStart, which extends the
	// service manager's deadline and this hook's own start window (05-health
	// §5H.3.4).
	extendStart := r.Deps().ExtendStart
	srv.SetStartExtender(extendStart)
	// The drift check: the reviewed manifest must describe this engine
	// exactly, and its routes must lie inside the prefixes Manifest declared
	// (the router refuses a route outside them at P7).
	routes, err := srv.LegacyManifest()
	if err != nil {
		return fmt.Errorf("books: %w", err)
	}
	// Each s.perm(atom) route records the atom table's requirement.
	if routes, err = resolveRoutes(routes); err != nil {
		return fmt.Errorf("books: legacy manifest: %w", err)
	}

	r.AuthMethod(auth.MethodSpec{Method: absAuthMethod})
	// The repairs hook is registered first, so it starts before the server
	// hook and stops after it (the kernel stops hooks in reverse): the
	// store closes once HEAD's stop has drained, and before the kernel
	// closes the core DB.
	r.Hook(module.Hook{
		Name: string(ID) + ".repairs",
		// The settings load's held writes, then HEAD's open-time writers
		// and the opv3 reconcile (05-storage §5S.9.3; 06 §6.5.2 step 2),
		// then the memdb warmup, before the server's start resumes
		// operations. A failure closes the store here: the kernel calls no
		// Stop for a hook whose Start failed.
		Start: func(ctx context.Context) error {
			m.repairsBegan.Store(true)
			if err := settingsWrites.apply(); err != nil {
				return errors.Join(err, m.closeStore())
			}
			if err := store.RunBootRepairs(ctx); err != nil {
				return errors.Join(fmt.Errorf("books: boot repairs: %w", err), m.closeStore())
			}
			return nil
		},
		Stop: func(context.Context) error { return m.closeStore() },
	})
	// Stage 5's values for the mount's responses (R1-17): HEAD's
	// per-response CSP, its report URL and the embedded bundle's build ID.
	csp, cspReport, buildID := srv.LegacyPerimeter()
	r.MountLegacy(httpapi.LegacyMount{
		Handler:     srv.LegacyHandler(),
		Auth:        httpapi.LegacyAuthGin,
		ServesSPA:   true,
		Routes:      slices.Values(routes),
		Redirects:   srv.LegacyRedirects(),
		TopSegments: server.LegacyTopSegments(),
		SPAShared:   server.LegacySPAShared(),
		DefaultCSP:  csp,
		CSPReport:   cspReport,
		BuildID:     buildID,
	})
	cfg := m.serveConfig()
	r.Hook(module.Hook{
		Name: string(ID) + ".server",
		// HEAD's start up to the listener (the op gates, the service
		// container, the search index, the warmers); the kernel opens the
		// listener after it (start step 7).
		Start: func(ctx context.Context) error {
			// HEAD ran this start under the unit's TimeoutStartSec (300 s),
			// and the kernel's own window is core.lifecycle.start_timeout
			// (60 s): ask for the step budget up front so the whole hook,
			// the op gates and the service container included, is not cut
			// off sooner than HEAD's was. Later steps extend again.
			extendStart("books server start")
			return srv.StartHook(ctx, cfg)
		},
		// HEAD's stop after the listener drained, within the budget left.
		Stop: srv.StopHook,
	})
	return nil
}

// serveConfig is the ServerConfig Options recorded, or HEAD's defaults.
func (m *booksModule) serveConfig() server.ServerConfig {
	if c := m.cfg.Load(); c != nil {
		return *c
	}
	return server.GetDefaultServerConfig()
}

// asBooks returns the books module behind mod.
func asBooks(mod module.Module) (*booksModule, error) {
	m, ok := mod.(*booksModule)
	if !ok || m == nil {
		return nil, errNotBooks
	}
	return m, nil
}

// closeStore closes the store Register built and clears the process-global
// reference to it. Close is idempotent.
func (m *booksModule) closeStore() error {
	store := m.store.Load()
	if store == nil {
		return nil
	}
	database.SetGlobalStore(nil)
	if err := store.Close(); err != nil {
		return fmt.Errorf("books: close the store: %w", err)
	}
	return nil
}

// Release tears down what Register built when the App never started it:
// boot failed after Register (app.New refusing at P7, the process defaults
// or Attach failing), or Run failed before the start hooks, so no hook will
// stop it. The server is released (server.ReleaseUnstarted, a no-op once its
// start hook has begun, which closes Stopped), then the store closed unless
// the repairs hook has begun, whose failure path or Stop closes it instead.
// When app.New failed the kernel has closed the core DB already; the store's
// Close then only drains books' own readers.
func Release(mod module.Module) {
	m, err := asBooks(mod)
	if err != nil {
		return
	}
	if srv := m.srv.Load(); srv != nil {
		srv.ReleaseUnstarted()
	}
	if !m.repairsBegan.Load() {
		if err := m.closeStore(); err != nil {
			bootLog.Warn("releasing the store of an unstarted boot: %v", err)
		}
	}
}

// Stopped is closed once books' server has stopped: its stop hook returned,
// its start hook failed (the kernel then calls no stop), or Release tore it
// down unstarted. It is closed at
// once when the server was never built (Register never ran: boot failed
// earlier, or books is disabled). The kernel abandons a stop hook at the
// budget, so serve waits on it before closing the store.
func Stopped(mod module.Module) <-chan struct{} {
	if m, err := asBooks(mod); err == nil {
		if srv := m.srv.Load(); srv != nil {
			return srv.Stopped()
		}
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}
