// file: core/kernel/storage.go
// version: 1.0.0
// guid: 129333ce-8ab8-40ac-a70a-ae2c593dfd6c
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/buildid"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"

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
}

// DeclaresStorage reports whether m declares any storage: a keyspace, legacy
// claims, migrations or any other member of storage.Spec. Package app
// resolves core.storage.path only when a module of the app declares storage.
func DeclaresStorage(m module.Manifest) bool { return !reflect.ValueOf(m.Storage).IsZero() }

// storageWanted gates P2 to P4: Config.StoragePath set, and not Describe,
// which never opens the store. With no path, an enabled module that declares
// storage refuses the boot, naming the option; a disabled one does not,
// since nothing of it runs.
func (k *Kernel) storageWanted(describe bool) (bool, error) {
	if describe {
		return false, nil
	}
	if k.cfg.StoragePath != "" {
		return true, nil
	}
	for _, e := range k.entries {
		if e.enabled && DeclaresStorage(e.man) {
			return false, fmt.Errorf("kernel: module %s declares storage and core.storage.path is not set (kernel.Config.StoragePath)", e.man.ID)
		}
	}
	return false, nil
}

// openStorage is P2 (06 §6.5.1; 05-storage §5S.4 steps 1-5): open the store
// at core.storage.path with core/storage and every module that declares
// storage as owners, enabled or not (a disabled module's claims stay
// validated and its keyspace reserved), apply the store guard and the
// upgrade floors, validate every spec and claim, check a marker-less store
// for foreign keys and plan the migrations. It writes nothing to the store.
func (k *Kernel) openStorage(ctx context.Context) error {
	var owners []storage.OwnerSpec
	for _, e := range k.entries {
		if DeclaresStorage(e.man) {
			owners = append(owners, storage.OwnerSpec{Owner: storage.Owner(e.man.ID), Spec: e.man.Storage, Enabled: e.enabled})
		}
	}
	// Every build field of the store names the binary ID, never the core
	// version constant (05-storage §5S.3.7 "Binary ID"); once per process.
	build, err := buildid.Binary(k.cfg.Version)
	if err != nil {
		return fmt.Errorf("kernel: binary ID for the store's build fields: %w", err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:        k.cfg.StoragePath,
		FS:          k.cfg.StorageFS,
		Owners:      owners,
		Build:       build,
		Logger:      k.coreLog("storage"),
		ExtendStart: k.extendStart(),
	})
	if err != nil {
		return fmt.Errorf("kernel: open storage (P2): %w", err)
	}
	k.store = &kernelStore{opened: o}
	return nil
}

// prepareStorage is P4a (06 §6.5.1; 05-storage §5S.4 steps 6-7): on an
// empty store the one fresh-install batch; otherwise, when the boot will
// write, the garbage sweep, the headroom check and the pre-migration
// checkpoint, then the install marker. Nothing reaches the store before it.
func (k *Kernel) prepareStorage(ctx context.Context) error {
	db, err := k.store.opened.Prepare(ctx)
	if err != nil {
		return fmt.Errorf("kernel: prepare storage (P4a): %w", err)
	}
	k.store.db = db
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
	var h module.StorageHandles
	if k.store == nil || k.store.db == nil {
		return h
	}
	h.Keyspace, _ = k.store.db.Keyspace(storage.Owner(id))
	h.Legacy, _ = k.store.db.Legacy(storage.Owner(id))
	return h
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
	scope, err := k.provider.Core("storage", "owner", "kind", "state")
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
	stored, supported := k.store.db.Levels()
	for _, o := range slices.Sorted(maps.Keys(supported)) {
		owner := attribute.String("owner", string(o))
		level.Record(ctx, int64(stored[o]), metric.WithAttributes(owner, attribute.String("kind", "store")))
		level.Record(ctx, int64(supported[o]), metric.WithAttributes(owner, attribute.String("kind", "supported")))
	}
	open, completed, err := k.store.db.Admin().CheckpointBytes()
	if err != nil {
		k.log.Warn("kernel: pre-migration checkpoints not sized; storage_checkpoint_bytes not recorded", "err", err)
		return nil
	}
	checkpoint.Record(ctx, int64(open), metric.WithAttributes(attribute.String("state", "open")))
	checkpoint.Record(ctx, int64(completed), metric.WithAttributes(attribute.String("state", "completed")))
	return nil
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
	if k.store == nil || k.store.opened == nil {
		return nil
	}
	if err := k.store.opened.Close(); err != nil && !errors.Is(err, storage.ErrClosed) {
		return fmt.Errorf("kernel: storage close: %w", err)
	}
	return nil
}
