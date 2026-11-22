// file: internal/books/module.go
// version: 1.3.12
// guid: 1eb7c933-3c90-4be5-a58c-52b621bd1dfe
// last-edited: 2026-11-19

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
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
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

// taskResourceKinds are the resource kinds books declares with
// Registrar.Resources (G8, 05-tasks 5T.3.4): a task definition's Effects may
// name only these, and the kernel refuses a registration whose kind reads or
// writes a books resource missing here ("writes resource kind ... which module
// books did not declare"). It is the whole pkg/ops resource vocabulary, spelled
// with the constants, never a literal: every native kind names its resources
// as string(ops.ResX) (the converted maintenance groups, the legacy jobs and
// the repairs fixers), so declaring all of them covers the kinds registered now
// and the ones later rows add. TestRegister_DeclaresEveryResourceKind keeps it
// equal to pkg/ops/effects.go, so a constant added there fails until it is
// listed here. A fresh slice every call.
func taskResourceKinds() []string {
	return []string{
		string(ops.ResBooks),
		string(ops.ResBookFiles),
		string(ops.ResAuthors),
		string(ops.ResSeries),
		string(ops.ResReviewItems),
		string(ops.ResEmbeddings),
		string(ops.ResOperations),
		string(ops.ResCatalog),
		string(ops.ResFingerprintIndex),
		string(ops.ResSettings),
		string(ops.ResActivity),
		string(ops.ResFiles),
		string(ops.ResExternalIDs),
		string(ops.ResUserState),
		string(ops.ResMetadataCache),
		string(ops.ResFieldStates),
		string(ops.ResIndexes),
		string(ops.ResAIJournal),
		string(ops.ResAuthorityLists),
		string(ops.ResSyncIdentity),
		string(ops.ResMetrics),
		string(ops.ResCoverText),
		string(ops.ResFileProvenance),
		string(ops.ResAuxStores),
		string(ops.ResAIScans),
		string(ops.ResCollections),
		string(ops.ResSplitBookCandidates),
		string(ops.ResASINMisses),
		string(ops.ResUsers),
		string(ops.ResAuthCredentials),
		string(ops.ResAuthSessions),
	}
}

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
// before the store opens, so that is the value from the environment
// (ABO_BOOKS_ABS_API_ENABLED, modules.books.abs.api_enabled), and Register
// refuses a boot whose settings, once the store is open, turn it the other
// way (errABSFlagDrift).
func Module() module.Module {
	return &booksModule{abs: absSurfaceFromEnv()}
}

// absEnabledEnv is the environment name of modules.books.abs.api_enabled (the
// layered store's <APP>_<PATH> grammar, the leading "modules." dropped).
const absEnabledEnv = "ABO_BOOKS_ABS_API_ENABLED"

// absSurfaceFromEnv is whether the environment turns the ABS surface on. An
// unset or unparsable value is off: core's config boot reports the unparsable
// one (it fails boot, naming the variable) before Register runs.
func absSurfaceFromEnv() bool {
	on, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(absEnabledEnv)))
	return err == nil && on
}

// booksModule is books' module.Module.
type booksModule struct {
	abs bool

	// steps is serve's startup runner and stateDir the secure state
	// directory, both set by Options before app.New, whose Register reads
	// them on the same goroutine.
	steps    *startup.Steps
	stateDir string
	// swapVoid is why this boot must not trust the HNSW clean-stop marker
	// because of the swap slot, "" when it may (swapVoidReason, read by
	// Options before app.New runs P0a), and migrated is set by the first of
	// books' migration steps inside New (beforeMigration). Register takes
	// the marker for the store it builds and voids it for either.
	swapVoid string
	migrated atomic.Bool
	// dbPath is core.storage.path and version serve's Options.Version,
	// both set by Options: Attach builds the restore stager from them.
	dbPath, version string
	// requestRestart is the kernel's Lifecycle.RequestRestart, set by
	// Register: the restore stager asks for the restart (exit 75) with it.
	requestRestart func(reason string)
	// beforeSettings, when set (tests), runs on the store Register built,
	// before the settings boot: a stored settings blob, the seeded roles.
	beforeSettings func(*database.PebbleStore)

	// idw is the identity boot's late handles, built by Options when the
	// identity predicate is on and finished by Register; nil otherwise.
	idw *identityWiring
	// cutoverErr is why the cutover func could not build M3 (Manifest.Cutover has
	// no error return), recorded for Register to refuse the boot with.
	cutoverErr atomic.Pointer[error]

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
	// appConfigInstalled is set when Register bound the config Facade
	// (config_wire.go); closeStore unbinds it.
	appConfigInstalled atomic.Bool

	// gapFillIn is what the strip of config.yaml's gap-fill keys needs from
	// Register (the config access and books' legacy range, set when the Facade
	// was bound) and from Attach (the storage admin that says whether M2a
	// committed); startRepairs runs the step only when both arrived and the flip
	// is declared (config_strip_boot.go).
	gapFillIn    atomic.Pointer[gapFillInputs]
	storageAdmin atomic.Pointer[storage.Admin]
}

// Manifest is books' static declaration. It is pure: the legacy prefixes
// (server.LegacyPrefixes: every route's subtree, every top segment and
// "login") follow from the ABS flag Module captured.
func (m *booksModule) Manifest() module.Manifest {
	mf := module.Manifest{
		ID:             ID,
		Version:        moduleVersion,
		DisplayName:    "Audiobooks",
		MinCoreVersion: minCoreVersion,
		DefaultEnabled: true,
		LegacyPrefixes: server.LegacyPrefixes(m.abs),
		// Books' claims on HEAD's key families, its legacy migrations and
		// its versioned ones, and the projection HEAD's guard reads
		// (05-storage §5S.3.4, §5S.9.3; internal/books/store booksspec.go).
		// The first step discards the HNSW clean-stop marker and marks this
		// boot migrated (beforeMigration).
		Storage: m.storageSpec(),
		// HEAD's factory-reset file effects (the Open Library dumps and,
		// only on request, the library folder's contents), run at the boot
		// that applies a ResetFactory swap (reset_hook.go).
		ResetHook: m.resetHook(),
		// Books' config sections (config.go), registered by the kernel at P3
		// when the config boot is on (F3-07 b); a boot without it never
		// reads them.
		Settings: booksSettings(),
		// F3's cutover, when the flip switch is on (cutover.go).
		Cutover: m.cutoverSteps(),
		// The App's configuration surface: Deps.Config and the secrets the
		// rename table maps (config_wire.go).
		AppConfig: appConfigSpec(),
		Rewrite:   m.rewriteStep(),
		// The legacy-auth bridge re-derives the principal stage 12 bound
		// (after a password reset or a role change), so books receives
		// Deps.AuthRefresh on an identity boot; the kernel hands it to no
		// other module, and none else declares it (TestAuthRefresh_...).
		AuthRefresh: true,
		// The Cloudflare Access guest policy and the guest's phone-app key follow
		// core's account events (account_lifecycle_wire.go): books receives
		// Deps.AuthLifecycle on an identity boot and registers the one hook from
		// Register. Declared statically like AuthRefresh; the kernel hands it to
		// no other module (TestAuthLifecycle_...).
		AuthLifecycle: true,
		// No native route yet, so no public one (the gin mount's public
		// routes are the legacy manifest's Public rows).
	}
	if identityOn() {
		// The scopes the atom table, the seed sets and the ambient list name,
		// and books' one seed role (identity_declarations.go): declared only
		// when the flip is, so a boot without it declares nothing to rbac.
		mf.Permissions, mf.Roles, mf.ImportedRoleIDs = booksPermissions(), booksRoles(), booksImportedRoleIDs()
	}
	return mf
}

// beforeMigration is the observer the manifest passes database.BooksSpec:
// it runs before each of books' migration steps inside app.New. The first
// marks this boot migrated and discards the HNSW clean-stop marker beside
// the store durably, before the step writes the rows the snapshot projects
// through a step store no verdict watch counts, so a step that crashes
// cannot leave the marker vouching (R1-15 follow-up review, MINOR-1).
// Register takes what is left. A marker that may still vouch fails the
// step, and with it the boot. Without Options (dbPath "") there is no
// marker to discard.
func (m *booksModule) beforeMigration() error {
	if m.migrated.Swap(true) || m.dbPath == "" {
		return nil
	}
	return database.DiscardHNSWCleanStop(m.dbPath)
}

// cleanStopVoid is why Register's HNSW clean-stop claim must not vouch for
// this boot, "" when it may: the pre-boot swap may have replaced the store
// (swapVoid), or a books migration step wrote it (migrated).
func (m *booksModule) cleanStopVoid() string {
	switch {
	case m.swapVoid != "":
		return m.swapVoid
	case m.migrated.Load():
		return database.HNSWCleanStopVoidMigrated
	}
	return ""
}

// errABSFlagDrift is Register's refusal when the stored settings turn the ABS
// surface the other way from the environment Module read: the manifest's
// legacy prefixes were fixed from the environment before the store opened.
// errABSMisconfigured is the boot refusal for an enabled ABS surface whose settings
// cannot build it (see server.ValidateABSConfig).
var errABSMisconfigured = errors.New("books: modules.books.abs.api_enabled is on but the Audiobookshelf API is misconfigured")

var errABSFlagDrift = errors.New("books: the stored settings set abs_api_enabled differently from the environment; " +
	"set " + absEnabledEnv + " in the environment (it is environment-authoritative), so the legacy prefixes fixed before the store opened match the server")

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
	m.requestRestart = r.Deps().RequestRestart
	// The resource kinds task definitions may name in their Effects. Nothing
	// registers a task yet (F5-18 passes the kind lists to Registrar.Tasks), and
	// the kernel checks a declaration only against registered tasks, so this
	// changes no behaviour until then. It comes before the no-storage return:
	// the declaration does not depend on a store.
	r.Resources(taskResourceKinds()...)
	legacy := r.Deps().Storage.Legacy
	if legacy == (storage.LegacyRange{}) {
		return nil
	}
	// The compat method, before anything reads Deps: the kernel binds
	// Deps.AuthCompat when the module registers its method (Registrar.AuthMethod),
	// so the identity the server is built under (below) reads the handle from
	// r.Deps() after this call, never from a copy taken before it.
	r.AuthMethod(auth.MethodSpec{Method: absAuthMethod})
	if cerr := m.cutoverErr.Load(); cerr != nil {
		// The cutover list could not be built (cutover.go): the store was opened
		// under a manifest whose M3 is missing, so nothing may serve over it.
		return fmt.Errorf("books: the identity cutover could not be built: %w", *cerr)
	}
	// The memdb sort indexes and the counts-cache interval are read once, as
	// the store opens. cmd's initConfig set them from the operator layers;
	// the booted store's effective values (a stored one included) win here.
	if acc := r.Deps().Config; acc != nil {
		idx, interval, err := storeTunablesFrom(acc)
		if err != nil {
			return err
		}
		ApplyStoreTunables(idx, interval)
	}
	store, err := database.NewBooksStore(legacy)
	if err != nil {
		return fmt.Errorf("books: %w", err)
	}
	// The HNSW clean-stop marker beside the store, before anything writes
	// through it; the server's snapshot import asks the store for it. Here,
	// not in Options: the kernel has opened the store and holds its lock, so
	// no other process writes the rows or the marker between this take and
	// this run's own writes (R1-15 round-9 review); a take before the open
	// could miss the marker the store's last holder wrote as it let go, and
	// leave it vouching on disk through this whole run. Prepare wrote only
	// core's own keys; a books step that ran discarded the marker before it
	// wrote (beforeMigration). A marker that may still vouch fails the
	// boot, as it fails books' first migration step (R1-25 review S-1).
	//
	// The store is kept first, so Release closes it when the take or a
	// later step refuses.
	m.store.Store(store)
	if m.dbPath != "" {
		if err := store.ClaimHNSWCleanStop(m.dbPath, m.cleanStopVoid()); err != nil {
			return fmt.Errorf("books: %w", err)
		}
	}
	database.SetGlobalStore(store)
	// With the core config boot on, the Facade is bound before the settings
	// boot and the server read AppConfig (config_wire.go); without it Deps.Config
	// is nil and HEAD's loaders below stay the source.
	bound, err := m.installAppConfig(context.Background(), r, store)
	if err != nil {
		return err
	}
	if bound && m.declaresFlip() {
		m.gapFillIn.Store(&gapFillInputs{acc: r.Deps().Config, legacy: legacy})
	}
	if m.beforeSettings != nil {
		m.beforeSettings(store)
	}
	steps := m.steps
	if steps == nil {
		steps = newSteps()
	}
	if err := bootSettings(store, steps, m.stateDir, m.dbPath); err != nil {
		return err
	}
	if config.AppConfig.ABSAPIEnabled != m.abs {
		return errABSFlagDrift
	}
	// The ABS surface's own settings, checked here because NewServer, which
	// checks them again, can only exit the process: a returned error reaches
	// app.New's caller with its reason, and Release still tears the store down.
	if err := server.ValidateABSConfig(config.Snapshot()); err != nil {
		return fmt.Errorf("%w: %w", errABSMisconfigured, err)
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
	deps := r.Deps()
	srvOpts := []server.ServerOption{server.WithLogger(deps.Log)}
	identity, err := m.identityFor(deps)
	if err != nil {
		return err
	}
	if identity != nil {
		srvOpts = append(srvOpts, server.WithIdentity(identity), server.WithAccessVerifier(m.idw.verifier),
			// The guest policy's keep rule reads the same key the admission policy
			// does, with the same semantics (identityPolicyFrom).
			server.WithAccessAllowList(func() (string, error) { return cfaccessAllowedEmails(deps.Config) }))
	}
	srv := server.NewServer(store, srvOpts...)
	// Stored before any check can fail, so Release tears it down when New
	// then refuses.
	m.srv.Store(srv)
	if identity != nil {
		if err := registerAccountLifecycle(deps, srv); err != nil {
			return err
		}
	}
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

	// The repairs hook is registered first, so it starts before the server
	// hook and stops after it (the kernel stops hooks in reverse): the
	// store closes once HEAD's stop has drained, and before the kernel
	// closes the core DB.
	r.Hook(module.Hook{
		Name: string(ID) + ".repairs",
		// The config.yaml gap-fill strip, then HEAD's open-time writers
		// and the opv3 reconcile (05-storage §5S.9.3; 06 §6.5.2 step 2),
		// then the memdb warmup, before the server's start resumes
		// operations. A failure closes the store here: the kernel calls no
		// Stop for a hook whose Start failed.
		Start: func(ctx context.Context) error {
			m.repairsBegan.Store(true)
			return m.startRepairs(ctx, steps, store)
		},
		Stop: func(context.Context) error { return m.closeStore() },
	})
	// Stage 5's values for the mount's responses (R1-17): HEAD's
	// per-response CSP, its report URL and the embedded bundle's build ID.
	csp, cspReport, buildID := srv.LegacyPerimeter()
	mount := httpapi.LegacyMount{
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
		// Stage 8: the Basic gate that was gin's middleware (F3-07 e4).
		BasicGate:         server.LegacyBasicGate(),
		BasicExemptAssets: server.LegacyBasicExemptAssets,
	}
	// The one place the mount's authentication can differ from the literal
	// above: the identity predicate (identity_flip.go), off in production
	// until B10g, so the shipped mount is the literal's LegacyAuthGin.
	// reachMountAuth (internal/books/cutover) accepts exactly this statement,
	// in exactly this shape, and fails closed on any other write to .Auth.
	if identityOn() {
		mount.Auth = httpapi.LegacyAuthCore
	}
	if err := identityTies(identityOn(), identity != nil, mount.Auth); err != nil {
		return err
	}
	r.MountLegacy(mount)
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
// startRepairs is the repairs hook's Start once it has marked the hook
// begun: the config.yaml gap-fill strip, then the boot repairs, whose
// failure closes the store (the kernel calls no Stop for a hook whose Start
// failed).
func (m *booksModule) startRepairs(ctx context.Context, steps *startup.Steps, store *database.PebbleStore) error {
	// A failed or refused strip degrades /health, it does not stop the boot: the
	// file is untouched or already stripped, and the next boot runs it again.
	if err := steps.Run("gapFillStrip", func() error { return m.stripGapFill(ctx) }); err != nil {
		return errors.Join(err, m.closeStore())
	}
	if err := store.RunBootRepairs(ctx); err != nil {
		return errors.Join(fmt.Errorf("books: boot repairs: %w", err), m.closeStore())
	}
	return nil
}

func (m *booksModule) closeStore() error {
	store := m.store.Load()
	if store == nil {
		return nil
	}
	database.SetGlobalStore(nil)
	m.releaseAppConfig()
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
