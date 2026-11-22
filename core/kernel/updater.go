// file: core/kernel/updater.go
// version: 0.1.0
// guid: cb31ce6d-094c-4aa1-b5ce-2b28612ebca6
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/updaterhttp"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/updater"
)

// The kernel's updater (F8-04b: slice 8b-3's constructors, wired by the flip).
// An app that declared the identity flip (Config.IdentityBoot) and ships an
// updater (Config.Updater, app.Options.Updater) gets, and no other app gets:
//
//   - the topic: coreTopics carries updaterTopicSpecs() (events.go);
//   - the owner: storageOwners appends updaterOwnerSpec() (swap.go), a one-time
//     claim-ledger write at P4b;
//   - the component: wireUpdater registers core.updater on the health registry
//     with updaterHealthSource(u). It does not go through registerCoreComponents,
//     which has no boot caller yet and whose core.config and core.backup rows
//     are already registered by their own wiring (config_boot.go,
//     identity_p4d.go), so calling it would fail as a duplicate;
//   - the routes: mountUpdater(r.CoreGroup(), u, newUpdaterStarter(executor), log)
//     at P7, once the executor exists and before P8 builds the router.
//
// updaterDeclared is the one predicate. With either half off nothing is
// registered, so the running app, today's ABO among them, behaves as before:
// Config.Updater alone is set on every offline command (updater fetch-release
// reads it) and wires nothing.
//
// Not wired here, and so not startable through the routes: the three
// core.updater.* task kinds. Core registers none of its own kinds at this base
// (the kernel's P6 loop is over modules' Registrars, and
// core_kinds_schedule_test's census lists the kinds "the day F5-18 registers
// them"), so a POST to /updater/check, /apply or /rollback finds no kind to
// start. That registration is the next slice (REMAINING in the F8-04b hand-back).
//
// The app, not this file, names ABO: AppID, BinaryName, the release Source and
// the asset namer all arrive as parameters (ABO's come from
// internal/books.FetchOptions through app.Options.Updater), so core holds no
// second literal of them.

// updaterOwnerID is core/updater's owner in the storage claim ledger: the
// owner of the c/updater/ keyspace, whose one key is "seen" (5U.4).
const updaterOwnerID storage.Owner = "core/updater"

// updaterInputs are what the app hands over: app.Options.Updater's fields plus
// the core.updater section's default mode. updater.New refuses a blank AppID or
// BinaryName and a nil Source, so they are not re-checked here. A nil
// AssetName leaves core's RawBinary default; ABO sets it (FetchOptions, owner
// ruling O-5).
type updaterInputs struct {
	AppID       string
	BinaryName  string
	Source      updater.Source
	AssetName   func(r updater.Release, goos, goarch string) string
	DefaultMode updater.Mode
}

// updaterOptions is the updater.Options for in.
func updaterOptions(in updaterInputs) updater.Options {
	return updater.Options{
		AppID: in.AppID, BinaryName: in.BinaryName, Source: in.Source,
		AssetName: in.AssetName, DefaultMode: in.DefaultMode,
	}
}

// updaterServices are the kernel services the updater takes (updater.Deps).
// HTTP and Notify are the two hooks F7-09 fills in, and both may be nil:
//
//   - HTTP nil: the release source uses its own https-only client with a 30 s
//     header timeout (core/updater/github), which is HEAD's behaviour. Nothing
//     sets it from a providerhttp registry until 05-updater §5U.13 item 7 names
//     the integrations.Ref.
//   - Notify nil: no core.update.found.v1 notification is staged
//     (core/updater/tasks.go), which is HEAD's behaviour: HEAD's updater sends
//     none. The topic itself is still published on the bus.
//
// Restarter, Events, Store and ReaderCheck are required: an apply without a
// restarter or a reader check would have nothing to refuse with, and the check
// task reads and writes the c/updater/ store. Health is unused by the updater
// (the kernel registers it as a probe, updaterHealthSource); Log may be nil.
type updaterServices struct {
	HTTP        *providerhttp.Client
	Notify      *notify.Service
	Restarter   updater.Restarter
	Health      *health.Registry
	Events      *events.Bus
	Store       storage.Keyspace
	ReaderCheck updater.ReaderCheck
	Log         *slog.Logger
}

// updaterDeps is updater.Deps for s, or an error naming the first required
// service that is missing.
func updaterDeps(s updaterServices) (updater.Deps, error) {
	switch {
	case s.Restarter == nil:
		return updater.Deps{}, errors.New("kernel: updater needs a Restarter")
	case s.Events == nil:
		return updater.Deps{}, errors.New("kernel: updater needs the event bus")
	case s.Store.Owner() != updaterOwnerID:
		// Keyspace is a struct, so "missing" is the zero value, and a handle
		// of another owner would put the "seen" key in that owner's rows.
		return updater.Deps{}, fmt.Errorf("kernel: updater needs the %s keyspace (c/updater/), got owner %q", updaterOwnerID, s.Store.Owner())
	case s.ReaderCheck == nil:
		return updater.Deps{}, errors.New("kernel: updater needs a ReaderCheck")
	}
	return updater.Deps{
		HTTP: s.HTTP, Notify: s.Notify, Restarter: s.Restarter, Health: s.Health,
		Events: s.Events, Store: s.Store, ReaderCheck: s.ReaderCheck, Log: s.Log,
	}, nil
}

// newKernelUpdater builds the updater for in and s, with the updater.Options
// (WithVersion, WithStateDir, WithSettings and the rest) the caller passes.
func newKernelUpdater(in updaterInputs, s updaterServices, with ...updater.Option) (*updater.Updater, error) {
	d, err := updaterDeps(s)
	if err != nil {
		return nil, err
	}
	return updater.New(updaterOptions(in), d, with...)
}

// updaterTopicSpecs are the topics the updater adds to core's catalog
// (core.update.found.v1). The flip appends them to coreTopics; until then the
// catalog does not list them.
func updaterTopicSpecs() []events.TopicSpec {
	return []events.TopicSpec{updater.FoundTopicSpec()}
}

// updaterOwnerSpec is the storage claim of core/updater: the c/updater/
// keyspace and nothing else (no claims, no migrations; the one row, "seen", is
// written by the check task). Registering a new owner takes a one-time write in
// the claim ledger at P4b, which belongs to the flip that appends this spec to
// storageOwners.
func updaterOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: updaterOwnerID, Spec: storage.Spec{Keyspace: true}, Enabled: true}
}

// updaterHealthSource is coreComponentSources.Updater: the core.updater
// component's probe over u (05-updater §5U.9). A nil u yields a nil source, so
// registerCoreComponents leaves the component out: core.updater is registered
// only when the app ships an updater.
func updaterHealthSource(u *updater.Updater) func(ctx context.Context) health.Result {
	if u == nil {
		return nil
	}
	return u.Probe
}

// updaterStarter is updaterhttp.Starter over the executor: the three routes
// that enqueue a task start it as the request's principal, and nothing runs on
// the request goroutine.
type updaterStarter struct{ tasks exportStarter }

var _ updaterhttp.Starter = updaterStarter{}

// newUpdaterStarter is the Starter the routes enqueue through. tasks is the
// executor's tasks.Client; nil makes every start fail rather than panic.
func newUpdaterStarter(tasks exportStarter) updaterhttp.Starter { return updaterStarter{tasks: tasks} }

// errUpdaterActor is a start whose requester is not the request's principal.
// The executor stamps the run from the principal in ctx, so the mismatch is
// refused before a run that names someone else is queued.
var errUpdaterActor = errors.New("kernel: updater start names a different person than the request")

func (s updaterStarter) start(ctx context.Context, kind tasks.Kind, params any, requestedBy string) (string, error) {
	if s.tasks == nil {
		return "", errors.New("kernel: updater starter has no executor")
	}
	pr, ok := auth.PrincipalFrom(ctx)
	if requestedBy == "" || !ok || pr == nil || pr.User == nil || string(pr.User.ID) != requestedBy {
		return "", errUpdaterActor
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("kernel: updater params: %w", err)
	}
	ref, err := s.tasks.Start(ctx, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: raw, Trigger: tasks.TriggerManual})
	if err != nil {
		return "", err
	}
	return string(ref.ID), nil
}

func (s updaterStarter) StartCheck(ctx context.Context, requestedBy string) (string, error) {
	return s.start(ctx, updater.KindCheck, updater.Empty{}, requestedBy)
}

func (s updaterStarter) StartApply(ctx context.Context, p updater.ApplyParams, requestedBy string) (string, error) {
	return s.start(ctx, updater.KindApply, p, requestedBy)
}

func (s updaterStarter) StartRollback(ctx context.Context, requestedBy string) (string, error) {
	return s.start(ctx, updater.KindRollback, updater.Empty{}, requestedBy)
}

// mountUpdater registers the four updater routes (/updater, /updater/check,
// /updater/apply, /updater/rollback) on g, which must be the router's
// CoreGroup(). It refuses a nil group and a nil updater: an app with no
// updater mounts no routes (05-updater §5U.3), not routes that answer 503.
// Every route repeats its permission and guards in its handler
// (updaterhttp), so a mounting mistake never opens one.
func mountUpdater(g *httpapi.Group, u *updater.Updater, st updaterhttp.Starter, log *slog.Logger) error {
	if g == nil || u == nil {
		return errors.New("kernel: updater mount needs the core group and the updater")
	}
	updaterhttp.Register(g, updaterhttp.Deps{Updater: u, Starter: st, Log: log})
	return nil
}

// updaterDeclared reports whether this boot wires the updater: the app
// declared the identity flip (the routes need its auth service and the router
// it mounts through) and ships an updater (Config.Updater). It is the one
// predicate behind the owner, the topic's publisher, the health component and
// the routes, so none of them can exist without the others, and an app with
// either half off boots as it did before F8-04b. The topic spec is declared
// with every identity boot (coreTopics), which is a catalog entry only; it has
// no publisher unless this is true.
func (k *Kernel) updaterDeclared() bool {
	return k.identityBootDeclared() && k.cfg.Updater != nil
}

// updaterRestarter is updater.Restarter over the supervisor: the graceful
// restart that takes the swapped binary. It refuses until the app is ready,
// like backupRestart: Lifecycle.RequestRestart would log and drop the request,
// and an apply that reported success for a restart that never happened would
// leave the new binary unstarted.
type updaterRestarter struct{ l *Lifecycle }

func (r updaterRestarter) Restart(_ context.Context, reason string) error {
	if r.l == nil || !r.l.ready.Load() {
		return errUpdaterNotReady
	}
	r.l.RequestRestart(reason)
	return nil
}

var errUpdaterNotReady = errors.New("kernel: the app is not ready to restart; restart it to run the swapped binary")

// updaterSettingsOver is updater.WithSettings over the live core.updater
// section: mode, channel and max_download, re-read on every operation. A mode
// or channel the section holds that the updater does not know reads as its
// default (off, stable): the section's Validate refuses them at write time, so
// this is a guard, not a path.
func updaterSettingsOver(get func() *updaterSettings, def updater.Mode) func() updater.Settings {
	return func() updater.Settings {
		out := updater.DefaultSettings(def)
		s := get()
		if s == nil {
			return out
		}
		out.Mode, _ = updater.ParseMode(s.Mode)
		if c := updater.Channel(s.Channel); c == updater.ChannelStable || c == updater.ChannelPrerelease {
			out.Channel = c
		}
		out.MaxDownload = s.MaxDownload
		return out
	}
}

// buildUpdater is the updater of this boot: Config.Updater's source over the
// kernel's services. It leaves Deps.HTTP and Deps.Notify nil (the I10
// fallback, F7-09 not merged): the release source uses its own https-only
// client and no found notification is staged, which is HEAD's behaviour.
func (k *Kernel) buildUpdater() (*updater.Updater, error) {
	o := k.cfg.Updater
	if o == nil {
		return nil, errors.New("kernel: updater: no updater options")
	}
	if k.store == nil || k.store.db == nil || k.events == nil || k.cfgBoot == nil || k.healthReg == nil {
		return nil, errors.New("kernel: updater: the wiring needs the store, the events bus, the config store and the health registry P4 and P5 built")
	}
	ks, ok := k.store.db.Keyspace(updaterOwnerID)
	if !ok {
		return nil, fmt.Errorf("kernel: updater: owner %s has no keyspace: append updaterOwnerSpec() to storageOwners", updaterOwnerID)
	}
	h, ok := handleOf[updaterSettings](k.cfgBoot.handles)
	if !ok {
		return nil, errors.New("kernel: updater: the core.updater section is not registered")
	}
	in := updaterInputs{AppID: o.AppID, BinaryName: o.BinaryName, Source: o.Source, AssetName: o.AssetName, DefaultMode: o.DefaultMode}
	return newKernelUpdater(in, updaterServices{
		Restarter: updaterRestarter{l: k.lifecycle}, Health: k.healthReg, Events: k.events.bus, Store: ks,
		ReaderCheck: updater.NewReaderCheck(updater.ReaderCheckOptions{StateDir: k.cfg.StateDir}),
		Log:         k.coreLog("updater"),
	},
		updater.WithVersion(k.cfg.Version),
		updater.WithSettings(updaterSettingsOver(h.Get, o.DefaultMode)),
		updater.WithStateDir(k.cfg.StateDir),
		updater.WithDBPath(k.cfg.StoragePath),
	)
}

// lateExecutor is exportStarter over whatever executor the boot built, read
// when a request arrives. The routes mount at P5, before P7 decides whether
// the tasks start rule builds an executor at all (tasksNeeded), so the starter
// cannot hold one; a request, which only arrives after the router is built at
// P8, finds it or fails with errUpdaterNoExecutor. It never panics.
type lateExecutor struct{ k *Kernel }

var _ exportStarter = lateExecutor{}

var errUpdaterNoExecutor = errors.New("kernel: the task executor is not running; the updater cannot start a task")

func (l lateExecutor) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	kt := l.k.tasks
	if kt == nil || kt.exec == nil {
		return tasks.RunRef{}, errUpdaterNoExecutor
	}
	return kt.exec.Start(ctx, kind, req)
}

// wireUpdater is the updater's P5 step, after mountIdentity: build the
// updater, register the core.updater health component on the registry (its own
// Register, as registerBackupHealth does, since registerCoreComponents would
// also register core.config and core.backup, which their own wiring owns), and
// mount the four routes on the core group with a starter over the late-bound
// executor. It does nothing unless updaterDeclared. It starts and writes
// nothing.
func (k *Kernel) wireUpdater() error {
	if !k.updaterDeclared() {
		return nil
	}
	u, err := k.buildUpdater()
	if err != nil {
		return err
	}
	if _, err := k.healthReg.Register(health.Check{ID: componentUpdater, Title: titleUpdater, Probe: updaterHealthSource(u)}); err != nil {
		return fmt.Errorf("kernel: updater: health: %w", err)
	}
	return mountUpdater(k.router.CoreGroup(), u, newUpdaterStarter(lateExecutor{k: k}), k.coreLog("updaterhttp"))
}
