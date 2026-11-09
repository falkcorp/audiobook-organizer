// file: core/kernel/offline.go
// version: 1.0.0
// guid: 299eb4da-a345-4039-9962-8ce819af3f0f
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ErrStoreHeld: another process holds the store's lock, so the service is
// running (05-storage §5S.3.12 "In place": the command refuses while
// another process holds the DB lock). Stop the service first.
var ErrStoreHeld = errors.New("kernel: another process holds the store (stop the service first)")

// Offline is one offline open of the store (06 §6.4.6 "Offline open";
// R7-4, DATA7-11): what app.OpenOffline returns under the app's identity.
type Offline struct {
	k    *Kernel
	db   *storage.DB
	once sync.Once
	err  error
}

// OpenOffline is the one guard-aware open of the store outside a serve boot
// (06 §6.4.6): P1 (the manifests validated, enablement resolved) and
// logging, then the swap slot read without acting on it, refusing a swap of
// any kind that is staged or in flight (restore_pending, restore_inflight;
// SIMP8-9); then storage's open steps with the store guard, the upgrade
// floors and the claim validation (P2, 05-storage §5S.4 steps 1-5), and
// Opened.Offline, which refuses an empty store and a store P4 would migrate
// (ErrOfflinePendingSteps naming the steps), so it never migrates and never
// runs a cutover step. readOnly opens Pebble read-only and writes nothing; a
// read-write open also refuses what only a serve boot's P4a may write (the
// install marker, a stranded attempt, a format ratchet). An absent store is
// refused, never created. Every open takes the store's lock, so a running
// service is refused. The offline restore and reset do not come here: they
// use the kernel's own swap path (RestoreOffline, StageResetOffline).
func OpenOffline(ctx context.Context, c Config, readOnly bool) (_ *Offline, err error) {
	k, err := offlineKernel(c)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, k.closeOffline(ctx))
		}
	}()
	if err := k.refuseBusySlot(ctx); err != nil {
		return nil, err
	}
	opts, err := k.storageOptions(ctx)
	if err != nil {
		return nil, err
	}
	opts.ReadOnly, opts.MustExist = readOnly, true
	o, err := storage.Open(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("kernel: offline open: %w", err)
	}
	k.store = &kernelStore{opened: o}
	db, err := o.Offline()
	if err != nil {
		return nil, fmt.Errorf("kernel: offline open: %w", err)
	}
	k.store.db = db
	return &Offline{k: k, db: db}, nil
}

// Storage is module id's StorageHandles as P4c builds them: its keyspace
// and its legacy range. A module that is not compiled in, or declares no
// storage, is an error.
func (o *Offline) Storage(id module.ID) (module.StorageHandles, error) {
	e := o.k.entry(id)
	if e == nil {
		return module.StorageHandles{}, fmt.Errorf("kernel: offline open: no module %q", id)
	}
	if !DeclaresStorage(e.man) {
		return module.StorageHandles{}, fmt.Errorf("kernel: offline open: module %s declares no storage", id)
	}
	return handlesOf(o.db, id), nil
}

// handlesOf is module id's StorageHandles from db: each zero (every method
// failing) when the module declared no keyspace or no claims.
func handlesOf(db *storage.DB, id module.ID) module.StorageHandles {
	var h module.StorageHandles
	h.Keyspace, _ = db.Keyspace(storage.Owner(id))
	h.Legacy, _ = db.Legacy(storage.Owner(id))
	return h
}

// Close closes the store and the offline kernel's logging, once. Each write
// committed when it returned, so nothing is pending.
func (o *Offline) Close() error {
	o.once.Do(func() { o.err = o.k.closeOffline(context.Background()) })
	return o.err
}

// offlineKernel is the kernel of an offline command: P1 and logging, with
// the store path and the state directory the swap slot lives in, and
// nothing else (no metrics Provider, no Register, no handler).
func offlineKernel(c Config) (*Kernel, error) {
	k := &Kernel{cfg: c, lifecycle: newLifecycle()}
	if err := k.catalog(false); err != nil {
		return nil, err
	}
	if err := k.buildLogging(false); err != nil {
		return nil, err
	}
	switch {
	case c.StoragePath == "":
		return nil, errors.Join(errors.New("kernel: no module of this app declares storage, so there is no store to open offline (core.storage.path)"), k.closeOffline(context.Background()))
	case c.StateDir == "":
		return nil, errors.Join(errors.New("kernel: an offline open needs the state directory (core.paths.state_dir), where the swap slot is"), k.closeOffline(context.Background()))
	}
	return k, nil
}

// closeOffline closes what offlineKernel and the open built.
func (k *Kernel) closeOffline(ctx context.Context) error {
	return k.closeCore(ctx, time.Now().Add(defaultShutdownBudget))
}

// entry is module id's catalog entry, nil when no such module is compiled
// in.
func (k *Kernel) entry(id module.ID) *entry {
	for _, e := range k.entries {
		if e.man.ID == id {
			return e
		}
	}
	return nil
}

// refuseBusySlot reads the swap slot without acting on it (05-storage §5S.4
// "Offline open" rule 1): a staged or in-flight swap of any kind refuses,
// with its token, kind and ID; a slot that cannot be read refuses too.
func (k *Kernel) refuseBusySlot(ctx context.Context) error {
	st, err := restorejournal.ReadSlot(bootseam.FS(ctx), k.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("kernel: the swap slot cannot be read: %w", err)
	}
	if err := st.Err(); err != nil {
		return fmt.Errorf("kernel: %w", err)
	}
	return nil
}

// swapper is the kernel's restoreswap.Swapper over the boot's filesystem,
// as P0a builds it.
func (k *Kernel) swapper(ctx context.Context) (*restoreswap.Swapper, error) {
	fs := bootseam.FS(ctx)
	build, err := k.binaryID(ctx)
	if err != nil {
		return nil, err
	}
	return restoreswap.New(restoreswap.Options{FS: fs, StateDir: k.cfg.StateDir, BuildID: build, Clock: bootClock(ctx),
		Owners: k.storageOwners(), Probe: bootProbe(ctx, fs), Logger: k.coreLog("restoreswap")})
}

// liveStore opens the live store read-only (best effort) for what an
// offline swap reads from it: its install ID and, when withDB, the read-only
// DB for the reset hook's Plan. The caller closes it before
// refuseHeldStore: the open takes the store's lock. An error says why the store did not open;
// the swap paths log it and go on, because a store too broken to open is
// exactly what a restore or a reset replaces.
func (k *Kernel) liveStore(ctx context.Context) (*storage.Opened, error) {
	opts, err := k.storageOptions(ctx)
	if err != nil {
		return nil, err
	}
	opts.ReadOnly, opts.MustExist = true, true
	return storage.Open(ctx, opts)
}

// refuseHeldStore takes the store's lock as Pebble's open does
// (<db>/LOCK) and releases it: a lock another process holds means the
// service is running (ErrStoreHeld). It is the stopped-service check of the
// offline restore and reset, not an exclusion over the swap: the swap's
// sidecar rewrite opens the placed store at the same path, which Pebble's
// in-process lock table would refuse, and a service started meanwhile runs
// P0a before any open takes the lock. A store directory that does not
// exist has no holder. On the real filesystem Pebble also tracks its locks
// in-process, so the caller closes any store it opened first.
func refuseHeldStore(fs vfs.FS, dbPath string) error {
	if _, err := fs.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("kernel: the store %s: %w", dbPath, err)
	}
	l, err := fs.Lock(filepath.Join(dbPath, "LOCK"))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrStoreHeld, dbPath, err)
	}
	return l.Close()
}
