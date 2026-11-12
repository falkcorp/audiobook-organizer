// file: core/coretest/harness.go
// version: 1.0.3
// guid: a95c80b7-1b2c-4c57-aa24-2ea9397da171
// last-edited: 2026-11-11

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
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// harnessAppID is the AppID every harness boots with.
const harnessAppID = "coretest"

// Harness is an in-process App (06 §6.4.8): a temp state directory
// (Options.StateDir), no listeners; requests go straight to App.Handler().
//
// At release R1 the App has no config layer and no core authentication, and
// app.New starts no hook, worker or listener (06 §6.5.2's start order is
// Run's, and Run opens a listener). A module that declares storage (hello's
// keyspace) gets an in-memory Pebble store (Options.StorageFS =
// storage.MemFS(), 06 §6.4.8), so nothing lands under the temp state
// directory. A harness therefore boots P1 to P8 and serves requests; nothing
// it boots runs on its own, and its cleanup closes the App. Each harness owns its
// logger (a logcapture.Capture handed to the App as Options.Logger), its state
// directory and its App: it never touches slog.Default or any other process
// global (03 R5), so harnesses run in parallel tests.
type Harness struct {
	tb       testing.TB
	app      *app.App
	logs     *logcapture.Capture
	stateDir string
	fs       storage.FS                   // the in-memory store's filesystem, kept so Reboot reopens the same store
	mods     map[module.ID]module.Module  // every module passed to WithModules, by ID (the first of a duplicate)
	watched  map[module.ID]*watchedModule // the modules that boot disabled, as handed to app.New
	users    map[string][]rbac.Permission // WithUser's principals
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

// WithUser pre-seeds a principal named name holding exactly perms. At R1 it
// only records the principal: core authentication lands at R2's identity
// flip, and until then As fails the test (see As).
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
	}
	ctx := t.Context()
	if len(s.runs) > 0 {
		// WithRuns: the kernel inserts them at P4c, before the tasks start
		// rule looks for stored runs.
		ctx = bootseam.With(ctx, &bootseam.Seams{TaskRuns: s.runs})
	}
	a, err := app.New(ctx, appOpts, mods...)
	if err != nil {
		return nil, err
	}
	h.app = a
	t.Cleanup(func() {
		if err := a.Close(context.Background()); err != nil {
			t.Errorf("coretest: close: %v", err)
		}
	})
	// Run's step (5) and (6) for a harness that never calls App.Run: the
	// executor and scheduler start when the tasks start rule held, so RunTask
	// has something to run on. Close stops them first. A held run is read-only
	// and never dispatched, started or not (5T.3.11).
	if err := a.StartTasks(t.Context()); err != nil {
		return nil, errors.Join(fmt.Errorf("coretest: start tasks: %w", err), a.Close(context.Background()))
	}
	return h, nil
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

// As returns an authenticated client for a user seeded with WithUser. Core
// authentication lands at R2's identity flip (F4-08 fills this body); until
// then As fails the test.
func (h *Harness) As(user string) *Client {
	h.tb.Helper()
	if _, ok := h.users[user]; !ok {
		h.tb.Fatalf("coretest: As(%q): no WithUser seeded that user", user)
		return nil
	}
	h.tb.Fatalf("coretest: As needs core authentication, which lands at R2's identity flip")
	return nil
}

// Anonymous returns a client whose requests carry no principal.
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
