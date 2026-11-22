// file: core/kernel/storage.go
// version: 1.1.9
// guid: 129333ce-8ab8-40ac-a70a-ae2c593dfd6c
// last-edited: 2026-11-19

package kernel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The storage phases' labels on <namespace>_kernel_boot_phase_seconds,
// spelled as 06 §6.9's one label set (OPS3-16).
const (
	phaseP2  = "P2"
	phaseP4a = "P4a"
	phaseP4b = "P4b"
)

// disabledByStorage is ModuleState.Disabled for an enabled module that
// storage does not serve after P4b: a step it needs waits on an owner this
// process does not run, so the module is not started, and every later boot
// re-checks it (05-storage §5S.3.5 "Barrier and serving"). Health's degraded
// reason arrives with health's checks.
const disabledByStorage = "storage"

// kernelStore is what the storage phases hold. opened is set the moment P2's
// Open returns, so closeCore closes a store whose P4a or P4b failed (COR-24);
// db is set by P4a; scope after P5.
type kernelStore struct {
	opened *storage.Opened
	db     *storage.DB
	scope  *metrics.Scope // "<namespace>.storage"
	// checkpoint is <namespace>_storage_checkpoint_bytes{state}, recorded at
	// boot and again after every checkpoint sweep, whose deletions change it.
	checkpoint metric.Int64Gauge
	// sweeper is the checkpoint sweeper (sweeper.go), registered by boot
	// after P5 and started by Run after readiness.
	sweeper *sweeper
}

// DeclaresStorage reports whether m declares any storage: a keyspace, legacy
// claims, migrations or any other member of storage.Spec. It answers "does
// this MODULE declare storage" (its owner, its keyspace, its handles); "does
// this APP have a store" is Config.WantsStore, which also counts the identity
// boot.
func DeclaresStorage(m module.Manifest) bool { return !reflect.ValueOf(m.Storage).IsZero() }

// WantsStore reports whether the app c describes has a store: a module of it
// declares storage (enabled or not), or it declared the identity boot
// (Config.IdentityBoot), whose users, roles, sessions and revocation journal
// live in core's store even when no module owns a keyspace. It is the one
// answer to "does this app have a store" at every site that asks: P0's path
// and state directory resolution, the offline commands' resolution, and
// package app's claims mode and state-file table. Sites that mean "this
// module declares storage" (a module's owner spec, its handles) use
// DeclaresStorage instead and are not changed by the identity boot.
func (c Config) WantsStore() bool {
	return c.IdentityBoot || slices.ContainsFunc(c.Modules, func(m module.Module) bool {
		return m != nil && DeclaresStorage(m.Manifest())
	})
}

// storageWanted gates P2 to P4: Config.StoragePath set, and not Describe,
// which never opens the store. P0 sets the path for every app whose modules
// have a store (Config.WantsStore: a module declares storage, or the identity
// boot is declared; core.storage.path, the app's compiled path, or
// <state dir>/storage/main.pebble), so such an app always has one by here; an
// app with none and no compiled path opens no store.
func (k *Kernel) storageWanted(describe bool) bool {
	return !describe && k.cfg.StoragePath != ""
}

// openStorage is P2 (06 §6.5.1; 05-storage §5S.4 steps 1-5): open the store
// at core.storage.path with core/storage and every module that declares
// storage as owners, enabled or not (a disabled module's claims stay
// validated and its keyspace reserved), apply the store guard and the
// upgrade floors, validate every spec and claim, check a marker-less store
// for foreign keys and plan the migrations. It writes nothing to the store.
func (k *Kernel) openStorage(ctx context.Context) error {
	opts, err := k.storageOptions(ctx)
	if err != nil {
		return err
	}
	if opts.ClaimsShadow {
		k.log.Warn("kernel: core.storage.claims.mode=shadow: out-of-claim storage accesses are logged and allowed, not refused (the emergency dial; strict is the default)")
	}
	if err := k.swapBeforeOpen(ctx, &opts); err != nil {
		return err
	}
	o, err := storage.Open(ctx, opts)
	if err != nil {
		return fmt.Errorf("kernel: open storage (P2): %w", err)
	}
	k.store = &kernelStore{opened: o}
	return nil
}

// storageOptions are the options of every open of the store at
// core.storage.path: P2's, and the offline opens' (offline.go), which add
// ReadOnly and MustExist.
func (k *Kernel) storageOptions(ctx context.Context) (storage.OpenOptions, error) {
	// Every build field of the store names the binary ID, never the core
	// version constant (05-storage §5S.3.7 "Binary ID"); once per process.
	build, err := k.binaryID(ctx)
	if err != nil {
		return storage.OpenOptions{}, fmt.Errorf("kernel: binary ID for the store's build fields: %w", err)
	}
	owners := k.storageOwners()
	steps, err := k.dataSteps(ctx, owners, build, bootClock(ctx))
	if err != nil {
		return storage.OpenOptions{}, err
	}
	return storage.OpenOptions{
		Path:         k.cfg.StoragePath,
		FS:           k.cfg.StorageFS,
		Owners:       owners,
		DataSteps:    steps,
		Build:        build,
		Logger:       k.coreLog("storage"),
		ExtendStart:  k.extendStart(coreOwner),
		Clock:        bootClock(ctx),
		StateFiles:   slices.Clone(k.cfg.StorageStateFiles),
		FromRelease:  k.cfg.FromRelease,
		ClaimsShadow: k.cfg.StorageClaimsShadow,
		JournalHead:  journalHeadSeq,
	}, nil
}

// dataSteps builds the data-migration runner over every manifest's
// DataMigrations and the steps its Cutover func returns, enabled or not (a
// disabled module's migration still blocks its owner's later steps,
// 05-storage 5S.3.5), and returns its P4b steps. Cutover is called with the
// kernel's late-bound importers (cutover_importers.go), which refuse until P4b
// binds them. The App's one Manifest.Rewrite is M8: the runner appends it last
// and runs it through P0a's swapper (or, on an offline open, one built for it).
// It keeps the runner for P4a's preflight and P5's metrics. No manifest
// declaring a migration or a Rewrite is no runner: ABO's shape adds nothing to
// the plan. Quarantine, LevelStep and Preflight are bound to core's secret
// store, level owner and cutover preflight (config_boot.go).
func (k *Kernel) dataSteps(ctx context.Context, owners []storage.OwnerSpec, build string, clock func() time.Time) ([]storage.DataStep, error) {
	var migs []datamigrate.DataMigration
	imp := k.importers().values()
	for _, e := range k.entries {
		migs = append(migs, manifestMigrations(e.man, imp)...)
	}
	rw := k.rewriteStep()
	if len(migs) == 0 && rw == nil {
		k.dataMig = nil
		return nil, nil
	}
	cfg := DataMigrationsConfig{
		Build: build, Logger: k.coreLog("storage"), Clock: clock, ExtendStart: k.extendStart(coreOwner), Rewrite: rw,
	}
	// The three seams DataMigrationsConfig leaves to the config flip: the
	// quarantine seals into the secret store (late-bound, it opens at P4b), a
	// level step raises the owner's level, and P4a runs the cutover preflight.
	cfg.Quarantine = storage.QuarantineWriter(quarantineSealer{k})
	cfg.LevelStep = LevelStep
	cfg.Preflight = CutoverPreflight(k.coreLog("storage"))
	if rw != nil {
		sw, err := k.rewriteSwapper(ctx)
		if err != nil {
			return nil, err
		}
		cfg.Swapper = sw
		cfg.BeforeRewrite = k.restoreAppliedBeforeRewrite
	}
	dm, err := NewDataMigrations(cfg, owners, migs...)
	if err != nil {
		return nil, err
	}
	k.dataMig = dm
	return dm.Steps(), nil
}

// rewriteStep is the App's M8: the Rewrite of the first manifest that declares
// one (P1 refuses a second, rewriteProblems), enabled or not; nil when none.
func (k *Kernel) rewriteStep() *storage.RewriteStep {
	for _, e := range k.entries {
		if e.man.Rewrite != nil {
			rw := *e.man.Rewrite
			return &rw
		}
	}
	return nil
}

// rewriteID is M8's step ID, the one restoreswap.Options.RewriteStep must
// name ("" = the build has no M8).
func (k *Kernel) rewriteID() string {
	if rw := k.rewriteStep(); rw != nil {
		return rw.ID
	}
	return ""
}

// rewriteSwapper is the swapper M8 runs through: the boot's own (P0a built it
// with the same RewriteStep), else, on an offline open, a fresh one. M8 swaps
// the store's directory under the state directory, so a build that declares
// it needs one.
func (k *Kernel) rewriteSwapper(ctx context.Context) (*restoreswap.Swapper, error) {
	if k.cfg.StateDir == "" {
		return nil, fmt.Errorf("kernel: module declares Rewrite (M8, %s) and kernel.Config.StateDir is not set; the store rewrite swaps the store's directory under it", k.rewriteID())
	}
	if k.swap != nil {
		return k.swap.s, nil
	}
	return k.swapper(ctx)
}

// prepareStorage is P4a (06 §6.5.1; 05-storage §5S.4 steps 6-7): on an
// empty store the one fresh-install batch; otherwise, when the boot will
// write, the garbage sweep, the headroom check and the pre-migration
// checkpoint, then the install marker. Nothing reaches the store before it.
//
// Around Prepare the F2 foundation adds two calls (F2-16): the data
// migrations' cutover preflight before it (a nil hook, as F2-06 leaves it
// until F3-04, does nothing), and the audit store after it, once the c/audit/
// keyspace exists and before P4b writes anything the audit log could record
// (05-rbac §5 item 7). Building the audit store writes nothing.
func (k *Kernel) prepareStorage(ctx context.Context) error {
	if k.dataMig != nil {
		if err := k.dataMig.Preflight(ctx, k.store.opened); err != nil {
			return err
		}
	}
	db, err := k.store.opened.Prepare(ctx)
	if err != nil {
		return fmt.Errorf("kernel: prepare storage (P4a): %w", err)
	}
	k.store.db = db
	if k.audit, err = buildAudit(ctx, db, k.coreLog("audit"), bootClock(ctx)); err != nil {
		return err
	}
	k.coreLog("audit").DebugContext(ctx, "kernel: audit store built (P4a, before the P4b migrations)")
	return nil
}

// migrateStorage is P4b's migration list at R1 (06 §6.5.1; 05-storage §5S.4
// steps 8-9): every pending versioned step of core and of every enabled
// module in DAG order, Seed steps included, then the claim ledger, the store
// generation and the checkpoint's completion. The keyring before the list
// arrives with secrets (R2's config flip). An enabled module that storage
// does not serve afterwards is held: not registered, reported with Disabled
// "storage".
func (k *Kernel) migrateStorage(ctx context.Context) error {
	db := k.store.db
	// The secrets store opens first and the config store binds to c/config/
	// (06 §6.5.1 P4b; config_boot.go).
	if k.cfgBoot != nil {
		if err := k.bindCoreStores(ctx); err != nil {
			return err
		}
	}
	// The identity stores, and with them Importers.Auth, Roles and Audit, bind
	// here too, from the same instances the P5 services are built over; an app
	// that did not declare the identity boot builds none of it.
	if k.identityBootDeclared() {
		if err := k.buildIdentityStores(ctx); err != nil {
			return err
		}
	}
	// The Cutover importers bind before the first step can run (06 §6.5.1
	// P4b); with no config or secrets store wired (the gate off) they stay
	// unbound and a step that reaches one fails with module.ErrImporterNotBound.
	k.importers().bind(k.importerCfg, k.importerSec)
	// Importers.Legacy and AllowKeyRegen bind with them: the legacy ranges exist
	// as soon as Prepare returned the DB, gate or no gate.
	k.importers().bindLegacy(db, k.importerAllowRegen)
	if k.cfgBoot != nil && k.cfgBoot.store != nil {
		k.importers().bindLookup(k.cfgBoot.store.Lookup)
	}
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("kernel: migrate storage (P4b): %w", err)
	}
	_, deferred := db.Plan()
	for _, e := range k.entries {
		owner := storage.Owner(e.man.ID)
		if !e.enabled || !DeclaresStorage(e.man) || db.Serves(owner) {
			continue
		}
		e.enabled, e.held = false, disabledByStorage
		var pending []string
		for _, s := range deferred {
			if s.Owner == owner {
				pending = append(pending, s.String())
			}
		}
		k.log.Warn("kernel: module not started: storage does not serve it until its pending steps run",
			"module", string(e.man.ID), "pending", pending)
	}
	return nil
}

// storageHandles is a module's Deps.Storage: its keyspace and legacy range
// from P4a's store; zero values, whose every method fails, when no store is
// open (Describe, or no path) or the module declared neither.
func (k *Kernel) storageHandles(id module.ID) module.StorageHandles {
	if k.store == nil || k.store.db == nil {
		return module.StorageHandles{}
	}
	return handlesOf(k.store.db, id)
}

// StorageAdmin is the open store's admin surface, nil when no store is open.
// Release R1 only (app.App.StorageAdmin says why).
func (k *Kernel) StorageAdmin() *storage.Admin {
	if k.store == nil || k.store.db == nil {
		return nil
	}
	return k.store.db.Admin()
}

// storageMetrics records <namespace>_storage_level{owner,kind} and
// <namespace>_storage_checkpoint_bytes{state} (05-storage §5S.8, 05-metrics
// §12) once P5 has built the Provider: kernel code records them during the
// boot, never the Provider. The levels are read after P4b, so they include
// every raise the boot committed. A checkpoint that cannot be sized is
// logged and leaves that gauge unrecorded; it does not fail the boot.
func (k *Kernel) storageMetrics(ctx context.Context) error {
	if k.store == nil || k.store.db == nil {
		return nil
	}
	// migration and outcome label the data-migration series
	// (DataMigrations.RecordMetrics), step and sensitive the quarantine's
	// (QuarantineGauge): one scope per subsystem.
	scope, err := k.provider.Core("storage", "owner", "kind", "state", "op", "mode", "migration", "outcome", "step", "sensitive")
	if err != nil {
		return fmt.Errorf("kernel: storage metrics scope: %w", err)
	}
	k.store.scope = scope
	level, err := scope.Meter().Int64Gauge(scope.Prefix()+".level",
		metric.WithDescription("Minimum-reader level per owner: the store's committed stamp (kind=store) and the build's supported level (kind=supported) (05-storage §5S.3.7)."))
	if err != nil {
		return fmt.Errorf("kernel: storage level gauge: %w", err)
	}
	checkpoint, err := scope.Meter().Int64Gauge(scope.Prefix()+".checkpoint", metric.WithUnit("By"),
		metric.WithDescription("Bytes held by pre-migration checkpoints, by state (05-storage §5S.3.12)."))
	if err != nil {
		return fmt.Errorf("kernel: storage checkpoint gauge: %w", err)
	}
	// <namespace>_storage_claim_violations_total{owner,op,mode} (§5S.8): the
	// out-of-claim accesses storage refused, or the shadow dial allowed
	// (§5S.9.4 step 2). A series appears from its first access.
	db := k.store.db
	if _, err := scope.Meter().Int64ObservableCounter(scope.Prefix()+".claim_violations",
		metric.WithDescription("Out-of-claim storage accesses by owner and op class: refused under strict claims, allowed by the shadow dial core.storage.claims.mode=shadow (05-storage §5S.8, §5S.9.4 step 2)."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for v, n := range db.ClaimViolations() {
				o.Observe(int64(n), metric.WithAttributes(attribute.String("owner", string(v.Owner)),
					attribute.String("op", v.Op), attribute.String("mode", v.Mode)))
			}
			return nil
		})); err != nil {
		return fmt.Errorf("kernel: storage claim violations counter: %w", err)
	}
	stored, supported := k.store.db.Levels()
	for _, o := range slices.Sorted(maps.Keys(supported)) {
		owner := attribute.String("owner", string(o))
		level.Record(ctx, int64(stored[o]), metric.WithAttributes(owner, attribute.String("kind", "store")))
		level.Record(ctx, int64(supported[o]), metric.WithAttributes(owner, attribute.String("kind", "supported")))
	}
	k.store.checkpoint = checkpoint
	k.recordCheckpointBytes(ctx)
	// The data migrations' series, on the scope just made (F2-06's seam).
	if k.dataMig != nil {
		if err := k.dataMig.RecordMetrics(ctx, db, scope, k.bootPhase); err != nil {
			return err
		}
	}
	return nil
}

// recordCheckpointBytes records <namespace>_storage_checkpoint_bytes{state}
// from the checkpoint records now: at boot, and after each sweep. A
// checkpoint that cannot be sized is logged and leaves the gauge as it was;
// it fails nothing.
func (k *Kernel) recordCheckpointBytes(ctx context.Context) {
	if k.store == nil || k.store.db == nil || k.store.checkpoint == nil {
		return
	}
	open, completed, err := k.store.db.Admin().CheckpointBytes()
	if err != nil {
		k.log.Warn("kernel: pre-migration checkpoints not sized; storage_checkpoint_bytes not recorded", "err", err)
		return
	}
	k.store.checkpoint.Record(ctx, int64(open), metric.WithAttributes(attribute.String("state", "open")))
	k.store.checkpoint.Record(ctx, int64(completed), metric.WithAttributes(attribute.String("state", "completed")))
}

// markServed is 05-storage §5S.3.12's served write, before Run's start step
// (1): ready_at and status served on every open checkpoint record of the live
// store, so no later boot reuses a checkpoint that predates what this boot's
// modules write. Run calls it before any module code that can write runs, so
// a boot that writes from a start hook and dies before READY=1 has still
// marked the record. A failure stops the app: running on with the record
// unwritten would let the next upgrade reuse that checkpoint, and a rollback
// to it would drop every write made meanwhile without asking for
// --accept-loss.
func (k *Kernel) markServed(now time.Time) error {
	if k.store == nil || k.store.db == nil {
		return nil
	}
	if err := k.store.db.MarkServed(now); err != nil {
		return fmt.Errorf("kernel: checkpoint served write (ready_at, 05-storage §5S.3.12): %w", err)
	}
	return nil
}

// closeStorage closes the store P2 opened, after the metrics Provider and
// before logging (06 §6.5.2: storage closes regardless of the shutdown
// budget, with its own core.storage.close_timeout to drain its leases).
func (k *Kernel) closeStorage() error {
	// The secret store writes through the store, so it closes first (wiping its
	// key material); every close of the store goes through here, the failed
	// restore boot's step x included.
	serr := k.closeSecrets()
	if k.store == nil || k.store.opened == nil {
		return serr
	}
	if err := k.store.opened.Close(); err != nil && !errors.Is(err, storage.ErrClosed) {
		return errors.Join(serr, fmt.Errorf("kernel: storage close: %w", err))
	}
	return serr
}

// journalHeadSeq is storage.OpenOptions.JournalHead: the committed head seq of
// the revocation journal the pre-migration checkpoint records as its
// journal_seq (F4-08 B10k).
func journalHeadSeq(ctx context.Context, ks storage.Keyspace) (uint64, error) {
	h, err := journal.ReadHead(ctx, ks)
	return h.Seq, err
}
