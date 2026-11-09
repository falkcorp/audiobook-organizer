// file: core/kernel/offline.go
// version: 1.0.1
// guid: 299eb4da-a345-4039-9962-8ce819af3f0f
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// ErrServiceRunning: the service is running or starting. An opener of the
// live store (a serve boot from P0a on, an offline open, ABO's standalone
// open) holds the swap lock (storage.SwapLockPath) shared, so the offline
// restore and reset refuse before they read the slot or move anything
// (R1-25 review B1). It wraps storage.ErrSwapLockHeld. Stop the service
// first.
var ErrServiceRunning = errors.New("kernel: the service is running or starting (it holds the swap lock); stop it first")

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
// runs a cutover step. readOnly opens Pebble read-only and writes nothing to
// the store; a read-write open also refuses what only a serve boot's P4a
// may write (the install marker, a stranded attempt, a format ratchet). An
// absent store is refused, never created. Before the slot is read the open
// takes the swap lock shared and holds it until Close, so an offline
// restore or reset that runs refuses it (storage.ErrSwapLockHeld) and none
// starts while it is open; the lock file is the one thing it may create,
// under the state directory. Every open takes the store's lock, so a
// running service is refused. The offline restore and reset do not come
// here: they use the kernel's own swap path (RestoreOffline,
// StageResetOffline). A read-write open refuses, before anything else,
// unless this process is the state directory's owner (storage.ErrNotStateDirOwner,
// R1-25 review B-1): the files Pebble writes would otherwise belong to
// another user, and the service could not replace them. A read-only open
// writes nothing and may run as any user that can read the store; the swap
// lock then locks the owner's file as it is, or refuses, and never creates
// or changes it.
func OpenOffline(ctx context.Context, c Config, readOnly bool) (_ *Offline, err error) {
	if !readOnly {
		if err := storage.RefuseNotStateDirOwner(ctx, c.StateDir, "a read-write offline open"); err != nil {
			return nil, err
		}
	}
	k, err := offlineKernel(c)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, k.closeOffline(ctx))
		}
	}()
	// The swap lock, shared until Close, as a serve boot holds it: no
	// offline restore or reset moves the store while this open may use it.
	if err := k.lockSwap(ctx, false); err != nil {
		return nil, err
	}
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

// lockSwap takes the swap lock (storage.LockSwapShared, or
// LockSwapExclusive for an offline swap) for this kernel; closeCore
// releases it. A lock held in the other mode is, for an offline swap,
// ErrServiceRunning, and for an opener an offline restore or reset that is
// running; both wrap storage.ErrSwapLockHeld. A serve boot's P0a takes it
// through lockSwapForP0a instead.
func (k *Kernel) lockSwap(ctx context.Context, exclusive bool) error {
	var l io.Closer
	var err error
	if exclusive {
		l, err = storage.LockSwapExclusive(bootseam.FS(ctx), k.cfg.StateDir)
	} else {
		l, err = storage.LockSwapShared(bootseam.FS(ctx), k.cfg.StateDir)
	}
	switch {
	case err == nil:
		k.swapLock = l
		return nil
	case !errors.Is(err, storage.ErrSwapLockHeld):
		return fmt.Errorf("kernel: the swap lock: %w", err)
	case exclusive:
		return fmt.Errorf("%w: %w", ErrServiceRunning, err)
	}
	return fmt.Errorf("kernel: storage restore-offline or storage reset is running (it holds the swap lock); start once it finishes: %w", err)
}

// lockSwapForP0a takes the swap lock exclusively for a serve boot's P0a,
// which turns it shared once the swap step is done (runP0a): P0a may move
// the store, so it runs only while no other opener holds the lock, and no
// two serve boots run a P0a, or a swap one of them started, side by side
// (R1-25 review B3). Refused, it says which kind of holder it met, read
// from one shared take: that succeeds beside openers (another service, an
// offline or standalone open, ABO's op-runner child) and fails under an
// offline swap. The holder may change between the two takes; the boot is
// refused either way, and both errors wrap storage.ErrSwapLockHeld.
func (k *Kernel) lockSwapForP0a(ctx context.Context) (storage.ExclusiveSwapLock, error) {
	fs := bootseam.FS(ctx)
	l, err := storage.LockSwapExclusive(fs, k.cfg.StateDir)
	if err == nil {
		if s := bootseam.From(ctx); s != nil && s.SwapLock != nil {
			l = s.SwapLock(l)
		}
		k.swapLock = l
		return l, nil
	}
	if !errors.Is(err, storage.ErrSwapLockHeld) {
		return nil, fmt.Errorf("kernel: the swap lock: %w", err)
	}
	probe, perr := storage.LockSwapShared(fs, k.cfg.StateDir)
	if perr != nil {
		return nil, fmt.Errorf("kernel: storage restore-offline or storage reset is running (it holds the swap lock); start once it finishes: %w", err)
	}
	err = fmt.Errorf("kernel: another process has the store open (a running service, an offline or standalone open, an op-runner child); "+
		"the pre-boot swap (P0a) runs only with the swap lock to itself, so start once it exits: %w", err)
	if cerr := probe.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("kernel: release the swap lock's probe: %w", cerr))
	}
	return nil, err
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
// offline swap reads from it: its install ID and, for a factory reset with
// a hook, the read-only DB the hook's Plan reads (planReset). The caller
// closes it before refuseHeldStore runs, because the open takes the store's
// lock. An error says why the store did not open; the swap paths log it and
// go on, because a store too broken to open is exactly what a restore or a
// reset replaces.
func (k *Kernel) liveStore(ctx context.Context) (*storage.Opened, error) {
	opts, err := k.storageOptions(ctx)
	if err != nil {
		return nil, err
	}
	opts.ReadOnly, opts.MustExist = true, true
	return storage.Open(ctx, opts)
}

// refuseHeldStore takes the store's lock as Pebble's open does
// (<db>/LOCK) and releases it: a lock another process holds means
// something has the store open now (ErrStoreHeld). The exclusion over the
// swap is the swap lock, taken exclusively before this runs (lockSwap).
// This is a check at one instant for an opener that takes no swap lock (a
// kernel booted without a state directory, a standalone open without one,
// an older binary): it refuses one that has the store open while it runs,
// and no other. Such an opener that starts after it, or that is still
// booting toward its open (a serve boot's P0a renames the store before any
// open takes the LOCK), is not seen; only the swap lock excludes those, and
// they do not take it. It cannot be the exclusion itself: the swap's
// sidecar rewrite opens the placed store at the same path, which Pebble's
// in-process lock table would refuse. A store directory that does not
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
