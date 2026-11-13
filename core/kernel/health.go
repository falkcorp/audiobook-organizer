// file: core/kernel/health.go
// version: 1.1.0
// guid: c95c4573-5303-4883-ab14-5c79d5e895ea
// last-edited: 2026-11-13

package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/storage"
)

// The kernel's own health components (05-health §5H.3.1, §5H.10 F2). They
// need no module handle: the kernel holds the store and the keyring itself, so
// it registers them kernel-side with the App's one health.Registry, which is
// what lets F3's cutover raise storage_quarantine on core.storage and
// secrets_undecryptable / secrets_keyring_not_exported on core.secrets from
// its first boot (IMPL-12).
const (
	// componentStorage is the open store: Critical and Readiness, probed once
	// at registration by an engine ping (replacing HEAD's CountAuthors).
	componentStorage health.ComponentID = "core.storage"
	// componentSecrets is the keyring: neither Critical nor Readiness, push-only,
	// Up until F3-03's secrets raises a token on it.
	componentSecrets health.ComponentID = "core.secrets"
)

// The Reasons the engine ping reports.
const reasonEnginePingFailed = "engine_ping_failed"

// enginePinger is the part of the open store the core.storage probe reads:
// *storage.Admin, from storage.DB.Admin. EngineSample reads the engine's
// in-memory counters under the store's lease, so it is cheap and a closed
// store answers storage.ErrClosed.
type enginePinger interface {
	EngineSample() (storage.EngineSample, error)
}

// kernelReporters are the push handles of the kernel's own components. The
// kernel keeps them for later phases: F3's storage and secrets tokens, the
// executor's Ready wait on core.storage.
type kernelReporters struct {
	Storage health.Reporter // core.storage
	Secrets health.Reporter // core.secrets
}

// buildHealth builds the App's health.Registry and registers core.storage and
// core.secrets on it. core.storage's first Result is its probe's: Up when
// the engine answers, Down with engine_ping_failed when it does not (an
// error is returned only when the registration itself fails). core.secrets
// starts Up. F2-16's boot calls this once the store is open; it exposes
// nothing on app.App, and the probe is not scheduled (F8).
func buildHealth(store enginePinger) (*health.Registry, kernelReporters, error) {
	if store == nil {
		return nil, kernelReporters{}, errors.New("kernel: health: core.storage needs the open store")
	}
	reg := health.NewRegistry()
	var rep kernelReporters
	var err error
	if rep.Storage, err = reg.Register(health.Check{
		ID:        componentStorage,
		Title:     "Storage engine",
		Critical:  true,
		Readiness: true,
		Probe:     enginePing(store),
	}); err != nil {
		return nil, kernelReporters{}, fmt.Errorf("kernel: health: %w", err)
	}
	if rep.Secrets, err = reg.Register(health.Check{
		ID:    componentSecrets,
		Title: "Secrets keyring",
	}); err != nil {
		return nil, kernelReporters{}, fmt.Errorf("kernel: health: %w", err)
	}
	rep.Secrets.Report(health.Result{Status: health.StatusUp})
	return reg, rep, nil
}

// enginePing is core.storage's probe: one engine read.
func enginePing(store enginePinger) func(context.Context) health.Result {
	return func(ctx context.Context) health.Result {
		if err := ctx.Err(); err != nil {
			return health.Result{Status: health.StatusDown, Reason: reasonEnginePingFailed, Detail: err.Error()}
		}
		if _, err := store.EngineSample(); err != nil {
			return health.Result{Status: health.StatusDown, Reason: reasonEnginePingFailed, Detail: err.Error()}
		}
		return health.Result{Status: health.StatusUp}
	}
}

// The remaining core components of 05-health §5H.3.1 and the loops that keep
// them fresh (F8-03, dark: nothing calls these until F8-04 wires the boot).
const (
	componentConfig    health.ComponentID = "core.config"
	componentTasks     health.ComponentID = "core.tasks"
	componentScheduler health.ComponentID = "core.scheduler"
	componentOutbox    health.ComponentID = "core.outbox"
	componentEvents    health.ComponentID = "core.events"
	componentMetrics   health.ComponentID = "core.metrics"
	componentUpdater   health.ComponentID = "core.updater"
	componentBackup    health.ComponentID = "core.backup"
)

// The Reasons the probes below report (fixed tokens, [a-z0-9_]{1,40}).
const (
	reasonDeadLetters     = "dead_letters"
	reasonExecutorStopped = "executor_stopped"
	reasonTasksPaused     = "paused"
	reasonZombieCap       = "zombie_cap"
	reasonEventsDropped   = "events_dropped"
)

// coreComponentSources are the probes of the core components other than
// core.storage and core.secrets. A nil source is not an error: core.config and
// core.backup then register push-only, because their advisory and posture
// tokens are pushed by the config and backup services (F3, F8-08); the
// executor, scheduler, outbox, events and metrics components are left out
// (their service is not part of this build); core.updater is registered only
// when the updater is enabled, which is a non-nil Updater.
type coreComponentSources struct {
	Config, Tasks, Scheduler, Outbox, Events, Metrics, Updater, Backup func(ctx context.Context) health.Result
}

// registerCoreComponents registers the remaining core components on reg and
// returns their push handles by ID. It registers every one it is given a
// source for (or, for core.config and core.backup, always) or fails on the
// first error; the registry refuses a duplicate ID.
func registerCoreComponents(reg *health.Registry, src coreComponentSources) (map[health.ComponentID]health.Reporter, error) {
	if reg == nil {
		return nil, errors.New("kernel: health: registerCoreComponents needs the registry")
	}
	rows := []struct {
		id     health.ComponentID
		title  string
		probe  func(context.Context) health.Result
		always bool
	}{
		{componentConfig, "Configuration", src.Config, true},
		{componentTasks, "Task executor", src.Tasks, false},
		{componentScheduler, "Scheduler", src.Scheduler, false},
		{componentOutbox, "Event outbox", src.Outbox, false},
		{componentEvents, "Event bus", src.Events, false},
		{componentMetrics, "Metrics", src.Metrics, false},
		{componentUpdater, "Updater", src.Updater, false},
		{componentBackup, "Backup and restore", src.Backup, true},
	}
	out := make(map[health.ComponentID]health.Reporter, len(rows))
	for _, row := range rows {
		if row.probe == nil && !row.always {
			continue
		}
		rep, err := reg.Register(health.Check{ID: row.id, Title: row.title, Probe: row.probe})
		if err != nil {
			return nil, fmt.Errorf("kernel: health: %w", err)
		}
		out[row.id] = rep
	}
	return out, nil
}

// outboxProbe is core.outbox's probe: Degraded while any dead letter exists
// (05-health §5H.3.1). A failing count is Down: the outbox cannot be read.
func outboxProbe(deadLetters func(ctx context.Context) (int, error)) func(context.Context) health.Result {
	return func(ctx context.Context) health.Result {
		n, err := deadLetters(ctx)
		switch {
		case err != nil:
			return health.Result{Status: health.StatusDown, Reason: "outbox_unreadable", Detail: err.Error()}
		case n > 0:
			return health.Result{Status: health.StatusDegraded, Reason: reasonDeadLetters, Detail: fmt.Sprintf("%d dead letters", n)}
		}
		return health.Result{Status: health.StatusUp}
	}
}

// tasksState is what core.tasks' probe reads of the executor.
type tasksState struct {
	Running      bool
	Paused       bool
	ZombieCapHit bool
}

// tasksProbe is core.tasks' probe: Down while the executor is not running,
// Degraded when it is paused or its zombie cap is hit.
func tasksProbe(state func() tasksState) func(context.Context) health.Result {
	return func(context.Context) health.Result {
		st := state()
		switch {
		case !st.Running:
			return health.Result{Status: health.StatusDown, Reason: reasonExecutorStopped}
		case st.Paused:
			return health.Result{Status: health.StatusDegraded, Reason: reasonTasksPaused}
		case st.ZombieCapHit:
			return health.Result{Status: health.StatusDegraded, Reason: reasonZombieCap}
		}
		return health.Result{Status: health.StatusUp}
	}
}

// eventsProbe is core.events' probe: Degraded when more than maxPerCheck
// events were dropped since the previous check. A registry never runs a probe
// concurrently with itself (§5H.6), and the mutex covers a direct caller.
func eventsProbe(dropped func() uint64, maxPerCheck uint64) func(context.Context) health.Result {
	var (
		mu   sync.Mutex
		last uint64
		seen bool
	)
	return func(context.Context) health.Result {
		now := dropped()
		mu.Lock()
		prev, first := last, !seen
		last, seen = now, true
		mu.Unlock()
		// The first reading only sets the baseline, and a counter that went
		// backwards was reset: neither is a burst.
		if !first && now >= prev && now-prev > maxPerCheck {
			return health.Result{Status: health.StatusDegraded, Reason: reasonEventsDropped, Detail: fmt.Sprintf("%d events dropped since the last check", now-prev)}
		}
		return health.Result{Status: health.StatusUp}
	}
}

// startHealthLoops starts the registry's periodic component checks and the
// kernel heartbeat, which also carries the systemd watchdog ping: a wedged
// process stops beating, so /livez fails and the pings stop. work runs on
// every beat, before the beat is recorded, so a work func that wedges stops
// the beat. stop ends both. The first beat is recorded before it returns.
func startHealthLoops(ctx context.Context, reg *health.Registry, sup *health.Supervisor, work ...func()) (stop func()) {
	stopChecks := reg.StartChecks(ctx)
	stopBeat := reg.StartHeartbeat(ctx, sup, work...)
	return func() {
		stopBeat()
		stopChecks()
	}
}

// beginHealthShutdown flips readiness and tells the supervisor, the first
// thing shutdown does and before the HTTP listener drains (§5H.4): /readyz
// answers 503 core.kernel:stopping from here, the status topic publishes
// ready:false once, and systemd gets STOPPING=1. It is idempotent.
func beginHealthShutdown(reg *health.Registry, sup *health.Supervisor) {
	reg.BeginShutdown()
	sup.Stopping()
}
