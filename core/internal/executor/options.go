// file: core/internal/executor/options.go
// version: 1.1.0
// guid: 0e70fc5b-cdb1-48ee-bd63-a7f133df6d3b
// last-edited: 2026-11-11

package executor

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/sdnotify"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Config carries the core.tasks.* values of 05-tasks §5T.10 that the
// executor reads. A zero field takes the key's default. The kernel fills it
// from core config at F5 (F3-01 owns the section); until then hello and
// coretest pass it directly. Hot keys are re-read through Reconfigure.
type Config struct {
	Workers               int                           // core.tasks.workers (8); restart
	SystemWorkers         int                           // core.tasks.system_workers (2), reserved for PrioritySystem; restart
	CPUSlots              int                           // core.tasks.cpu_slots (NumCPU); hot: resizes the semaphore for new acquisitions
	Kinds                 map[tasks.Kind]tasks.KindDial // core.tasks.kinds; hot (next item dispatch)
	ShutdownDrain         time.Duration                 // core.tasks.shutdown_drain (10s); hot
	ShutdownExtendMax     time.Duration                 // core.tasks.shutdown_extend_max (60s); hot
	MinCheckpointInterval time.Duration                 // core.tasks.min_checkpoint_interval (30s); hot
	MaxYields             int                           // core.tasks.max_yields (1000); hot
	ZombieCap             int                           // core.tasks.zombie_cap (4 per module); hot
	StandDownLease        time.Duration                 // core.tasks.standdown_lease (2m); hot
	IdempotencyTTL        time.Duration                 // core.tasks.idempotency_ttl (7d); hot
	ModuleMaxConcurrent   map[string]int                // modules.<id>.tasks.max_concurrent (0 = unlimited); hot

	// Not config keys: fixed by 5T.4 and 5T.3.6, settable for tests.
	DispatchTick     time.Duration // 100ms
	WatchdogInterval time.Duration // 30s
	ProgressInterval time.Duration // 1s: the per-run writer's progress-tick throttle
	MaxAttempts      int           // 10: the infinite-restart guard (HEAD worker.go's)
}

// withDefaults returns c with every zero field set to its default.
func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	defInt := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	defInt(&c.Workers, 8)
	if c.SystemWorkers <= 0 {
		c.SystemWorkers = 2
	}
	c.SystemWorkers = min(c.SystemWorkers, max(c.Workers-1, 0))
	defInt(&c.CPUSlots, runtime.NumCPU())
	def(&c.ShutdownDrain, 10*time.Second)
	def(&c.ShutdownExtendMax, 60*time.Second)
	def(&c.MinCheckpointInterval, 30*time.Second)
	defInt(&c.MaxYields, 1000)
	defInt(&c.ZombieCap, 4)
	def(&c.StandDownLease, 2*time.Minute)
	def(&c.IdempotencyTTL, 7*24*time.Hour)
	def(&c.DispatchTick, 100*time.Millisecond)
	def(&c.WatchdogInterval, 30*time.Second)
	def(&c.ProgressInterval, time.Second)
	defInt(&c.MaxAttempts, 10)
	c.Kinds = maps.Clone(c.Kinds)
	c.ModuleMaxConcurrent = maps.Clone(c.ModuleMaxConcurrent)
	return c
}

// Manifest is what the executor needs of one module manifest (06): whether
// the module is enabled (held runs, 5T.3.11), the kinds its ledger declares
// (TaskLedger: kinds a disabled module's stored runs may carry) and the
// foreign or core kinds its system actor may start (TaskStarts, SEC2-15).
type Manifest struct {
	Module     string
	Enabled    bool
	TaskLedger []tasks.Kind
	TaskStarts []tasks.Kind
}

// Principal resolves the caller of a Client method from ctx: the stamped
// actor and its permission checker (06 COR-21). ok is false outside a
// request; the start then acts as req.Actor, as a kernel-internal start does.
type Principal func(ctx context.Context) (actor tasks.Actor, who rbac.Checker, ok bool)

// Options builds an executor. Stores is required; every other field has a
// default. Names coined by F2-03 (seams.d/F2-03.md).
type Options struct {
	Config Config

	// Stores are the run, clock, pause and stand-down stores (NativeStores
	// over c/tasks/, 5T.3.13).
	Stores tasks.Stores
	// NewBatch opens the indexed batch the executor's own builds commit;
	// nil: Stores.Runs' NewBatch (NativeStores offers one).
	NewBatch func() *storage.Batch
	// Keyspace is c/tasks/'s keyspace, for the HTTP start route's
	// idempotency records (idem/<kind>/<key>); nil refuses a start that
	// carries an IdempotencyKey.
	Keyspace *storage.Keyspace

	// Definitions are every registered kind (module and core); a
	// definition with problems (tasks.Problems) fails New.
	Definitions []tasks.Definition
	// Manifests decide held runs and the module system actor's TaskStarts.
	// A module with no manifest here is enabled.
	Manifests []Manifest
	// Catalog is the permission catalog the module system actor's checker
	// is built over (authz.ForSystem); nil: system actors hold nothing.
	Catalog *rbac.Catalog
	// Principal stamps Client calls; nil: every call acts as req.Actor
	// with no permission check beyond the system-actor rule.
	Principal Principal
	// WindowMembers lists the kinds window name would start now, in order
	// (the scheduler's Members). A start of a window kind (core.window.<name>)
	// by a caller with a checker must satisfy every member's Perms.Start,
	// because the window's run starts them as their own modules' system
	// actors: a holder of core:tasks.manage alone must not reach a member
	// kind through it. A nil func with a caller present refuses every window
	// start (fail closed); a start with no caller (the scheduler's StartIn)
	// is not checked. The kernel closes over Scheduler.Members (F2-16).
	WindowMembers func(window string) []tasks.Kind

	// Health is the readiness registry dispatch waits on (Common.Ready);
	// nil: every signal counts as ready.
	Health *health.Registry
	// Lanes are the registered lanes (5T.3.9).
	Lanes []tasks.Lane
	// ChangeSinks are the modules' journals, by module ID (5T.3.5).
	ChangeSinks map[string]tasks.ChangeSink
	// OnTerminal is called inside every terminal transition's batch
	// (5T.4 step 3, COR2-3); its error aborts that commit.
	OnTerminal func(ctx context.Context, b *storage.Batch, v tasks.RunView) error

	// Metrics is the core ops scope (<ns>.ops); InstrumentsOff creates no
	// instrument (ABO before F5, 05-tasks §5T.11). A nil scope with
	// instruments on is refused: the caller must say which it wants.
	Metrics        *metrics.Scope
	InstrumentsOff bool

	Logger *slog.Logger
	Now    func() time.Time
	// Getenv and ExtendTimeout are the systemd seam of shutdown (5T.4
	// step 4); nil: os.Getenv and sdnotify.ExtendTimeout.
	Getenv        func(string) string
	ExtendTimeout func(time.Duration) error
}

// errNoStores: Options.Stores.Runs is nil.
var errNoStores = errors.New("executor: Options.Stores.Runs is required")

// errNoScope: instruments on without a scope.
var errNoScope = errors.New("executor: Options.Metrics is nil and InstrumentsOff is false; pass the core ops scope or turn instruments off")

// batchOpener is NativeStores' run store's batch opener.
type batchOpener interface{ NewBatch() *storage.Batch }

func (o *Options) fill() error {
	if o.Stores.Runs == nil {
		return errNoStores
	}
	if !o.InstrumentsOff && o.Metrics == nil {
		return errNoScope
	}
	if o.NewBatch == nil {
		bo, ok := o.Stores.Runs.(batchOpener)
		if !ok {
			return errors.New("executor: Options.NewBatch is nil and Stores.Runs opens no batch")
		}
		o.NewBatch = bo.NewBatch
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.ExtendTimeout == nil {
		o.ExtendTimeout = sdnotify.ExtendTimeout
	}
	return nil
}

// Reconfigure applies the hot core.tasks.* keys of c to an executor built
// by New (cpu_slots resizes the semaphore for new acquisitions; the rest
// apply from their next read). Workers and SystemWorkers are restart-bound
// and keep their boot values. Name coined by F2-03.
func Reconfigure(x tasks.Executor, c Config) error {
	e, ok := x.(*executor)
	if !ok {
		return errors.New("executor: Reconfigure of an executor this package did not build")
	}
	cur := e.config()
	c = c.withDefaults()
	c.Workers, c.SystemWorkers = cur.Workers, cur.SystemWorkers
	e.cfg.Store(&c)
	e.cpu.resize(c.CPUSlots)
	e.wakeDispatcher()
	return nil
}
