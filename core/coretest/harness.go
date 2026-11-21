// file: core/coretest/harness.go
// version: 1.0.5
// guid: a95c80b7-1b2c-4c57-aa24-2ea9397da171
// last-edited: 2026-11-19

// Package coretest is core's public test kit (06 §6.4.8): an in-process App
// harness that boots core through app.New, a client that sends requests to
// the App's handler, and the R6 and G1 assertions. It is test support: it
// may import any core package, and no non-test core package imports it. The
// internal tests of app, kernel and the packages below them cannot import it
// either (Go refuses the cycle through app), so the log capture they share
// lives in the leaf coretest/logcapture.
package coretest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// harnessAppID is the AppID every harness boots with.
const harnessAppID = "coretest"

// Harness is an in-process App (06 §6.4.8): a temp state directory
// (Options.StateDir), no listeners; requests go straight to App.Handler().
//
// An App with a storage-declaring module boots with the identity boot (core
// auth, rbac and stages 12 and 13; identity.go), which WithUser, As and
// AssertPermission need. app.New starts no hook, worker or listener (06
// §6.5.2's start order is Run's, and Run opens a listener). A module that
// declares storage (hello's keyspace) gets an in-memory Pebble store
// (Options.StorageFS = storage.MemFS(), 06 §6.4.8), so no store lands under
// the temp state directory; the identity boot's revocation journal is a plain
// file there. A harness therefore boots P1 to P8 and serves requests; nothing
// it boots runs on its own, and its cleanup closes the App. Each harness owns
// its logger (a logcapture.Capture handed to the App as Options.Logger), its
// state directory and its App: it never touches slog.Default or any other
// process global (03 R5), so harnesses run in parallel tests.
type Harness struct {
	tb       testing.TB
	app      *app.App
	k        *kernel.Kernel // the App's kernel, from bootseam.Seams.Built: the task seams no App member offers
	logs     *logcapture.Capture
	stateDir string
	fs       storage.FS                   // the in-memory store's filesystem, kept so Reboot reopens the same store
	mods     map[module.ID]module.Module  // every module passed to WithModules, by ID (the first of a duplicate)
	watched  map[module.ID]*watchedModule // the modules that boot disabled, as handed to app.New
	users    map[string][]rbac.Permission // WithUser's principals
	id       *identity                    // core auth and rbac over the identity boot: seeds users, mints their sessions
	cookies  map[string]*http.Cookie      // the session cookie of each WithUser principal
}

// Option configures a Harness.
type Option func(*settings)

// settings is what the options record before Boot reads them.
type settings struct {
	mods     []module.Module
	disabled []module.ID
	config   map[string]any
	users    map[string][]rbac.Permission
	runs     []bootseam.TaskRunSeed // WithRuns
}

// WithModules adds modules to the App, in order. With none, the harness boots
// core with zero modules (gate G7).
func WithModules(mods ...module.Module) Option {
	return func(s *settings) { s.mods = append(s.mods, mods...) }
}

// WithDisabled disables the named modules, which must also be passed to
// WithModules. The spec sets core.modules.<id>.enabled=false in the boot
// record; until config lands (F3) no layer can set that key and the kernel
// reads only Manifest.DefaultEnabled, so at R1 the harness serves the
// module's manifest with DefaultEnabled false. App.Modules reports the
// module disabled with the reason "default"; F3 switches this to the boot
// record and the reason to "config".
func WithDisabled(ids ...module.ID) Option {
	return func(s *settings) { s.disabled = append(s.disabled, ids...) }
}

// WithConfig is the config overrides layer. Core config lands at F3; until
// then Boot refuses a non-empty map, naming its keys, because a setting the
// App never reads would let a test pass for the wrong reason.
func WithConfig(kv map[string]any) Option {
	return func(s *settings) {
		if s.config == nil {
			s.config = map[string]any{}
		}
		maps.Copy(s.config, kv)
	}
}

// WithUser pre-seeds a principal named name holding exactly perms: a stored
// core auth user whose one role, a custom role made for the set, holds those
// permissions and nothing else (none means no role at all). It is not
// core:admin and implies nothing wider; a permission the App's catalog does
// not declare fails the boot. Reach the principal with As. A later WithUser of
// the same name replaces the earlier one.
func WithUser(name string, perms ...rbac.Permission) Option {
	return func(s *settings) {
		if s.users == nil {
			s.users = map[string][]rbac.Permission{}
		}
		s.users[name] = slices.Clone(perms)
	}
}

// New boots core with the given options. With no WithModules option it boots
// core with ZERO modules (gate G7). It fails the test on any boot error.
//
// It registers t.Cleanup for shutdown (06 §6.4.8): App.Close, which closes
// the store, the metrics Provider and logging that boot built. A cleanup
// registered inside a synctest bubble runs inside it, so the bubble ends with
// none of Pebble's goroutines left.
func New(t testing.TB, opts ...Option) *Harness {
	t.Helper()
	h, err := Boot(t, opts...)
	if err != nil {
		t.Fatalf("coretest: boot: %v", err)
	}
	return h
}

// Boot is New without failing the test: it returns the boot error, for
// validation tests that expect a *module.ValidationError. An error from
// app.New is returned unwrapped; a nil module goes to app.New as given, so it
// comes back as the kernel's *module.ValidationError too.
func Boot(t testing.TB, opts ...Option) (*Harness, error) {
	t.Helper()
	return boot(t, nil, "", opts)
}

// boot is Boot over fs and stateDir when a Reboot hands the previous
// harness's (nil and "": a fresh in-memory store and a new temp directory).
func boot(t testing.TB, fs *storage.FS, stateDir string, opts []Option) (*Harness, error) {
	t.Helper()
	var s settings
	for _, o := range opts {
		if o != nil {
			o(&s)
		}
	}
	if len(s.config) > 0 {
		return nil, fmt.Errorf("coretest: WithConfig needs core config, which lands at F3 (keys %s)",
			strings.Join(slices.Sorted(maps.Keys(s.config)), ", "))
	}
	h := &Harness{
		tb:      t,
		logs:    logcapture.New(t),
		mods:    map[module.ID]module.Module{},
		watched: map[module.ID]*watchedModule{},
		users:   s.users,
		cookies: map[string]*http.Cookie{},
	}
	// A nil module and a duplicate ID are the kernel's to refuse (a
	// ValidationError), so every module goes to app.New; the map keeps the
	// first module of an ID, and first its index. Modules are told apart by
	// index, never by comparing them: a module's value need not be
	// comparable (a struct with a slice or func field), and == on such an
	// interface value panics.
	first := map[module.ID]int{}
	for i, m := range s.mods {
		if m == nil {
			continue
		}
		if id := m.Manifest().ID; h.mods[id] == nil {
			h.mods[id] = m
			first[id] = i
		}
	}
	disable := map[module.ID]bool{}
	var errs []error
	for _, id := range s.disabled {
		if h.mods[id] == nil {
			errs = append(errs, fmt.Errorf("coretest: WithDisabled(%q): no module with that ID was passed to WithModules", id))
			continue
		}
		disable[id] = true
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	// Only a module that boots disabled is wrapped: one WithDisabled names,
	// or one whose own manifest says DefaultEnabled false. An enabled module
	// goes to app.New as the value the test passed.
	mods := make([]module.Module, len(s.mods))
	for i, m := range s.mods {
		mods[i] = m
		if m == nil {
			continue
		}
		id := m.Manifest().ID
		if first[id] != i || !(disable[id] || !m.Manifest().DefaultEnabled) {
			continue
		}
		w := &watchedModule{Module: m, disabled: disable[id]}
		h.watched[id] = w
		mods[i] = w
	}
	h.stateDir = stateDir
	if h.stateDir == "" {
		h.stateDir = t.TempDir()
	}
	// Held so a Reboot reopens the same store (a fresh MemFS per call would
	// be an empty one).
	if fs != nil {
		h.fs = *fs
	} else {
		h.fs = memFS()
	}
	appOpts := app.Options{
		AppID:    harnessAppID,
		StateDir: h.stateDir,
		// Non-nil and empty, so nothing reads the process's flags or env.
		Args:   []string{},
		Env:    []string{},
		Logger: h.logs.Logger(),
		// In-memory Pebble (06 §6.4.8): a store hello's keyspace opens
		// leaves nothing under the state directory.
		StorageFS: h.fs,
		// The identity boot is on whenever the App will have a store (see
		// identityWanted): stages 12 and 13 then stand in front of every
		// route, a route that needs a permission is judged by one, and
		// WithUser, As and AssertPermission have a real principal to be.
		// Core at R2 has identity unconditionally (06 §6.5.1 P5) and this is
		// the closest a harness gets to a shipped boot. It is not always on
		// because the identity boot needs core's store, and P0 opens a store
		// only for an App one of whose modules declares storage (or that
		// compiled in a path), so an App of zero or storage-less modules
		// cannot honour it yet: B3 left that gap (a declared identity boot
		// does not make the kernel want a store) and a boot with the switch
		// on fails there with "P5 needs the stores". Such a harness boots as
		// it did before F4-08, with no identity, and says so to As,
		// AssertPermission and WithUser instead of pretending.
		IdentityBoot: identityWanted(s.mods),
	}
	// TaskRuns (WithRuns): the kernel inserts them at P4c, before the tasks
	// start rule looks for stored runs. Built hands the harness the kernel,
	// so RunTask and its siblings reach the executor's Client and a run's
	// stored record without a member on app.App, which a module is handed:
	// both are raw (no principal, no permission rule, no redaction).
	ctx := bootseam.With(t.Context(), &bootseam.Seams{
		TaskRuns: s.runs,
		Built:    func(k any) { h.k, _ = k.(*kernel.Kernel) },
	})
	a, err := app.New(ctx, appOpts, mods...)
	if err != nil {
		return nil, err
	}
	h.app = a
	if h.k == nil {
		return nil, errors.Join(errors.New("coretest: app.New did not hand the harness its kernel (bootseam.Seams.Built)"), a.Close(context.Background()))
	}
	t.Cleanup(func() {
		if err := a.Close(context.Background()); err != nil {
			t.Errorf("coretest: close: %v", err)
		}
	})
	// Run's step (5) and (6) for a harness that never calls App.Run: the
	// executor and scheduler start when the tasks start rule held, so RunTask
	// has something to run on. Close stops them first. A held run is read-only
	// and never dispatched, started or not (5T.3.11).
	if err := h.k.StartTasks(t.Context()); err != nil {
		return nil, errors.Join(fmt.Errorf("coretest: start tasks: %w", err), a.Close(context.Background()))
	}
	// WithUser's principals, through the services the identity boot built.
	switch {
	case appOpts.IdentityBoot:
		if h.id, err = newIdentity(h.k); err != nil {
			return nil, errors.Join(err, a.Close(context.Background()))
		}
		if err := h.seedAll(); err != nil {
			return nil, errors.Join(fmt.Errorf("coretest: %w", err), a.Close(context.Background()))
		}
	case len(h.users) > 0:
		return nil, errors.Join(fmt.Errorf("coretest: WithUser (%s) needs the identity boot, which the harness declares when a module declares storage; none does", strings.Join(sortedKeys(h.users), ", ")), a.Close(context.Background()))
	}
	return h, nil
}

// identityWanted reports whether the harness declares the identity boot for
// mods: when the kernel will open core's store for them, which it does for an
// App one of whose modules declares storage (kernel.DeclaresStorage, the rule
// P0 applies). A nil module is the kernel's to refuse and declares nothing.
func identityWanted(mods []module.Module) bool {
	return slices.ContainsFunc(mods, func(m module.Module) bool {
		return m != nil && kernel.DeclaresStorage(m.Manifest())
	})
}

// memFS is an in-memory filesystem that reports ample free space. Pebble's
// MemFS measures none, and P4a measures the filesystem when a boot will write
// to a store that already exists (a pending migration, the pre-migration
// checkpoint): a Reboot that enables a module over a store it left behind
// would otherwise fail there.
func memFS() storage.FS {
	m := vfs.NewMem()
	m.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	return storage.FSOf(m)
}

// Reboot closes this harness's App and boots a new one over the same store
// and state directory, with opts as the new App's options (the previous
// ones are not repeated: pass WithModules again). It is how a test shows what
// a restart does, such as the runs a re-enabled module resumes. It fails the
// test on a boot error.
func (h *Harness) Reboot(t testing.TB, opts ...Option) *Harness {
	t.Helper()
	if err := h.app.Close(context.Background()); err != nil {
		t.Fatalf("coretest: reboot: close the previous App: %v", err)
	}
	nh, err := boot(t, &h.fs, h.stateDir, opts)
	if err != nil {
		t.Fatalf("coretest: reboot: %v", err)
	}
	return nh
}

// App returns the booted App.
func (h *Harness) App() *app.App { return h.app }

// As returns a client whose requests reach stage 13 as the principal WithUser
// seeded under user: they carry the interactive session cookie core auth
// issued for that user, so stage 12 binds it as a person's sign-in, an
// Interactive guard admits it, and it holds exactly the permissions it was
// seeded with. A name no WithUser seeded fails the test; there is no fallback
// to an administrator or to Anonymous.
func (h *Harness) As(user string) *Client {
	h.tb.Helper()
	ck, ok := h.cookies[user]
	if !ok {
		h.tb.Fatalf("coretest: As(%q): no WithUser seeded that user", user)
		return nil
	}
	return &Client{handler: h.app.Handler(), cookie: ck}
}

// Anonymous returns a client whose requests carry no credential.
func (h *Harness) Anonymous() *Client { return &Client{handler: h.app.Handler()} }

// watchedModule is a module that boots disabled, as the harness hands it to
// app.New: it serves the manifest with DefaultEnabled false when WithDisabled
// names it, and records whether the kernel called Register, which
// AssertModuleInert reads (06 §6.3 A5: a disabled module's Register, and so
// every hook and worker it would record, is never invoked). The kernel calls
// only Manifest and Register; a later optional module interface the kernel
// asserts of a disabled module must be forwarded here.
type watchedModule struct {
	module.Module
	disabled   bool
	registered atomic.Bool
}

// Manifest is the module's manifest, disabled when WithDisabled names it.
func (w *watchedModule) Manifest() module.Manifest {
	m := w.Module.Manifest()
	if w.disabled {
		m.DefaultEnabled = false
	}
	return m
}

// Register records the call and runs the module's Register.
func (w *watchedModule) Register(r *module.Registrar) error {
	w.registered.Store(true)
	return w.Module.Register(r)
}
