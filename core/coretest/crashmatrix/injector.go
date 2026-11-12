// file: core/coretest/crashmatrix/injector.go
// version: 1.0.3
// guid: 81489850-3e9c-4d6d-a657-d875036d118e
// last-edited: 2026-11-12

package crashmatrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/module"
)

// BuildID is the binary ID every kit boot runs as: the swap's
// staged_by_build check and the store's build fields read it instead of a
// hash of the test binary. Stage stages under it.
const BuildID = "crashmatrix-current-build"

// Now is the clock of every kit boot (the swap's expires_at check, the
// store's timestamps, a checkpoint's name), fixed so two runs of one boot
// make the same operations. Stage's default expiry is an hour after it.
func Now() time.Time { return time.Date(2026, time.March, 14, 15, 9, 26, 0, time.UTC) }

// readyMessage is the kernel's record that Run's start sequence is done:
// readiness (06 §6.5.2 step 8), then the checkpoint sweeper's first sweep
// (05-storage §5S.4 step 15, the writer table's "After readiness" row). It
// is the end of the writer table's range, where the kit stops recording. A
// kernel that renamed it would leave the shutdown's operations recorded,
// and the store's close makes an unlabelled WAL sync, so the kit fails
// loudly rather than silently widening its range.
const readyMessage = "kernel: start complete"

// Injector wires one boot of the kit: app.New with the boot's filesystem,
// binary ID, clock and labeller (05-storage §5S.3.16). Build receives one
// per boot.
type Injector struct {
	f     Fixture
	seams *bootseam.Seams
	rec   *bootseamtest.Recorder // nil for a fixture-building boot (Boot)

	mu    sync.Mutex
	apps  []*app.App
	ready int
	stop  context.CancelFunc // ends the Run in progress (finish)
}

// newInjector is a boot over rec, or over fsys with no recorder. cache, when
// non-nil, is the section cache the boot's core config store builds through:
// the boots of one run share it, so the core sections are built once and not
// once per boot (a cell boots the kernel hundreds of times).
func newInjector(f Fixture, rec *bootseamtest.Recorder, fsys vfs.FS, cache *config.SectionCache) *Injector {
	s := &bootseam.Seams{FS: fsys, BuildID: BuildID, Clock: Now}
	if rec != nil {
		s = rec.Seams()
		s.BuildID, s.Clock = BuildID, Now
	}
	if cache != nil {
		s.SectionCache = cache
	}
	return &Injector{f: f, seams: s, rec: rec}
}

// New is app.New with the injector wired in: the caller passes its own
// Options and modules, so the app's boot writers run. The fixture decides
// where the store and the state directory are: Options.StateDir and
// <EnvPrefix>_CORE_STORAGE_PATH are set to them, any
// <EnvPrefix>_CORE_PATHS_STATE_DIR is dropped, and nil Env means no
// variable rather than the host's. The listener is localhost:0 without
// TLS. The kit runs the app it returns to readiness and stops it; the
// caller does not call Run.
func (inj *Injector) New(ctx context.Context, opts app.Options, mods ...module.Module) (*app.App, error) {
	prefix := opts.EnvPrefix
	if prefix == "" {
		prefix = strings.ToUpper(opts.AppID)
	}
	env := []string{}
	for _, kv := range opts.Env {
		if !strings.HasPrefix(kv, prefix+"_CORE_STORAGE_PATH=") && !strings.HasPrefix(kv, prefix+"_CORE_PATHS_STATE_DIR=") {
			env = append(env, kv)
		}
	}
	opts.Env = append(env, prefix+"_CORE_STORAGE_PATH="+inj.f.dbPath())
	opts.StateDir = inj.f.StateDir
	values := opts.R1Values
	opts.R1Values = func() app.R1Values {
		var v app.R1Values
		if values != nil {
			v = values()
		}
		v.Listen, v.TLSCertFile, v.TLSKeyFile = "localhost:0", "", ""
		return v
	}
	inner := opts.Logger
	if inner == nil {
		inner = slog.New(slog.DiscardHandler)
	}
	opts.Logger = slog.New(readyHandler{inner: inner.Handler(), ready: inj.markReady})
	a, err := app.New(bootseam.With(ctx, inj.seams), opts, mods...)
	if err != nil {
		return nil, err
	}
	inj.mu.Lock()
	inj.apps = append(inj.apps, a)
	inj.mu.Unlock()
	return a, nil
}

// markReady is the kernel's readiness record: the recorder stops there,
// then the Run in progress is stopped.
func (inj *Injector) markReady() {
	inj.mu.Lock()
	inj.ready++
	stop := inj.stop
	inj.mu.Unlock()
	if inj.rec != nil {
		inj.rec.Freeze()
	}
	if stop != nil {
		stop()
	}
}

// errNoApp: Build returned nil without booting an app through Injector.New.
var errNoApp = errors.New("crashmatrix: Build booted no app through Injector.New")

// finish runs every app Build made to readiness, then stops it: Run's
// context is cancelled when the kernel logs readyMessage, after the first
// checkpoint sweep, which runs on the sweeper's goroutine while Run already
// waits for a stop. A Run that ends before that (a failed boot) returns on
// its own. It reports whether every app reached readiness.
func (inj *Injector) finish(ctx context.Context) error {
	inj.mu.Lock()
	apps := append([]*app.App(nil), inj.apps...)
	inj.mu.Unlock()
	if len(apps) == 0 {
		return errNoApp
	}
	var errs []error
	for _, a := range apps {
		run, stop := context.WithCancel(ctx)
		inj.mu.Lock()
		inj.stop = stop
		inj.mu.Unlock()
		err := a.Run(run)
		stop()
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	inj.mu.Lock()
	defer inj.mu.Unlock()
	if inj.ready < len(apps) {
		return fmt.Errorf("crashmatrix: %d of %d apps logged no %q record", len(apps)-inj.ready, len(apps), readyMessage)
	}
	return nil
}

// readyHandler forwards every record to the caller's handler and calls
// ready on the kernel's readiness record.
type readyHandler struct {
	inner slog.Handler
	ready func()
}

func (h readyHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.inner.Enabled(ctx, l)
}

func (h readyHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == readyMessage {
		h.ready()
	}
	if !h.inner.Enabled(ctx, r.Level) {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h readyHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return readyHandler{inner: h.inner.WithAttrs(as), ready: h.ready}
}

func (h readyHandler) WithGroup(name string) slog.Handler {
	return readyHandler{inner: h.inner.WithGroup(name), ready: h.ready}
}
