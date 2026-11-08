// file: internal/books/module.go
// version: 1.0.5
// guid: 1eb7c933-3c90-4be5-a58c-52b621bd1dfe
// last-edited: 2026-11-08

// Package books is ABO's audiobook module and its composition-root glue (04
// §2.4, 06 §6.11.1 step 2): Module(store) is the module.Module ABO's main
// hands to app.New, Options is the app.Options of release R1, and Attach
// binds the App's served registry once New has returned. It is computed
// layer 6 and imported only by the main package and cmd (03 §3.5.1 item 1);
// every package below it receives what it needs as values this package
// wires, never mediacore/module or mediacore/app.
//
// In release R1 books wraps HEAD's server: Register builds today's
// *server.Server and mounts its gin engine with Registrar.MountLegacy
// (LegacyAuthGin, ServesSPA); its one hook runs HEAD's start up to the
// listener and HEAD's post-drain stop; the listener stack itself is HEAD's,
// handed to the kernel through app.Options.R1Listen until R1-17 builds core's
// perimeter.
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
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
)

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

// Module returns books for the composition root. store is the store serve's
// pre-server steps opened (MOD3-1); R1-23 moves the open into the kernel and
// drops the argument. Module does no work: it reads whether the ABS surface
// is on, which the effective config fixed before this call and which decides
// the legacy prefixes the manifest declares.
func Module(store *database.PebbleStore) module.Module {
	return &booksModule{store: store, abs: config.AppConfig.ABSAPIEnabled}
}

// booksModule is books' module.Module.
type booksModule struct {
	store *database.PebbleStore
	abs   bool

	// cfg is serve's listener flags, set by Options before app.New; the hook
	// and R1Listen read it.
	cfg atomic.Pointer[server.ServerConfig]
	// srv is the server Register built.
	srv atomic.Pointer[server.Server]
	// registered counts Register calls (TestBooksDisabledInert).
	registered atomic.Int32
	// logOut is the App's log output (Options' LogOutput).
	logOut logTee
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
		// No native route yet, so no public one (the gin mount's public
		// routes are the legacy manifest's Public rows).
	}
}

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

// Register builds HEAD's server over the store and records books' mount,
// auth method and hook (06 §6.11.1 step 2). Building the server here is
// R1's documented exception to Register's no-I/O rule (06 §6.4.1): NewServer
// constructs HEAD's whole service container, and books has no other place
// to build the engine MountLegacy needs before P7. R1-23 and the rows that
// split the server retire it (seams.d/R1-15.md).
func (m *booksModule) Register(r *module.Registrar) error {
	m.registered.Add(1)
	if m.store == nil {
		return errors.New("books: Module was given no store")
	}
	srv := server.NewServer(m.store)
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
	r.MountLegacy(httpapi.LegacyMount{
		Handler:     srv.LegacyHandler(),
		Auth:        httpapi.LegacyAuthGin,
		ServesSPA:   true,
		Routes:      slices.Values(routes),
		Redirects:   srv.LegacyRedirects(),
		TopSegments: server.LegacyTopSegments(),
		SPAShared:   server.LegacySPAShared(),
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

// Release tears down the server Register built when the App never started
// it: boot failed after Register (app.New refusing at P7, the process
// defaults or Attach failing), or Run failed before the start hook, so no
// hook will stop it. Stopped is closed afterwards. It does nothing once the
// start hook has begun, or when no server was built
// (server.ReleaseUnstarted).
func Release(mod module.Module) {
	if m, err := asBooks(mod); err == nil {
		if srv := m.srv.Load(); srv != nil {
			srv.ReleaseUnstarted()
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
