// file: core/kernel/reset.go
// version: 1.0.0
// guid: 7ac5a55b-45c3-44d5-81f4-746d3c88d1cf
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// defaultResetHookTimeout is core.storage.swap.reset_hook_timeout's default
// (05-storage §5S.3.15): the reset hook's Run stops there and what it did
// not reach is recorded as failed.
const defaultResetHookTimeout = 30 * time.Minute

// cleanupIncomplete is the 05-health token of a reset hook that left
// entries behind (OPS8-5); until health's checks land the kernel logs it.
const cleanupIncomplete = "factory_reset_cleanup_incomplete"

// resetEntry is the module that declares Manifest.ResetHook, nil when none
// does. P1 refuses a second one (ProblemResetHook), so there is at most one.
// It is the declaring module whether enabled or not: the files its reset
// deletes exist either way.
func (k *Kernel) resetEntry() *entry {
	for _, e := range k.entries {
		if e.man.ResetHook != nil {
			return e
		}
	}
	return nil
}

// StageResetOffline is kernel.stageReset for the offline storage reset
// (06 §6.4.6; 05-storage §5S.3.15 "Stage"), release R1's only reset path,
// with the service stopped. In this order, and nothing is staged when any
// step refuses:
//
//  1. the request's own checks (storage.ResetRequest.Validate), then this
//     process must be the state directory's owner (ErrNotStateDirOwner,
//     R1-25 review B-1): the intent and journal it writes, and the hook's
//     run at the next boot, must be the service's to read and replace;
//  2. the swap lock, taken exclusively and held until the reset is staged
//     (R1-25 review B1): a service that runs or is starting holds it shared,
//     so the reset refuses with ErrServiceRunning, and no service starts
//     while it stages; then a staged or in-flight swap refuses
//     (restore_pending, restore_inflight), before the store is opened;
//  3. with no reset hook, delete_library_files refuses
//     (storage.ErrResetRequestInvalid, COR10-9);
//  4. for ResetFactory with a hook, its Plan, with the module's storage
//     opened read-only (module.ResetPlanStorage) when the store opens and
//     the reset's kept paths (module.ResetPlanKept, the confinement's rule
//     (4)) always; a dangerous library root with the wipe requested is the
//     hook's error wrapping storage.ErrResetRefused;
//  5. with delete_library_files, the confirmation: Plan's root and count
//     must equal the request's (a *storage.LibraryConfirmError otherwise),
//     and a Plan with no root cannot confirm one; without it, no hook path
//     may hold the library root;
//  6. the store's lock: an opener that takes no swap lock refuses
//     (ErrStoreHeld);
//  7. Config.BeforeSwap, then the Swapper's StageReset, which pre-validates
//     every set-aside rename (storage.ErrRenameRefused: another
//     filesystem, read-only, a mount point, a single-file bind mount) and
//     creates the one intent in the slot.
//
// The reset applies at the next start of the service (P0a).
func StageResetOffline(ctx context.Context, c Config, requestedBy string, req storage.ResetRequest) (_ storage.ResetStaged, err error) {
	if err := req.Validate(); err != nil {
		return storage.ResetStaged{}, err
	}
	if err := refuseNotOwner(ctx, c.StateDir, "storage reset"); err != nil {
		return storage.ResetStaged{}, err
	}
	k, err := offlineKernel(c)
	if err != nil {
		return storage.ResetStaged{}, err
	}
	defer func() { err = errors.Join(err, k.closeOffline(ctx)) }()
	if err := k.lockSwap(ctx, true); err != nil {
		return storage.ResetStaged{}, err
	}
	if err := k.refuseBusySlot(ctx); err != nil {
		return storage.ResetStaged{}, err
	}
	hook := k.resetEntry()
	if hook == nil && req.DeleteLibraryFiles {
		return storage.ResetStaged{}, fmt.Errorf("%w: this app declares no reset hook, so a reset deletes no library file", storage.ErrResetRequestInvalid)
	}
	sw, err := k.swapper(ctx)
	if err != nil {
		return storage.ResetStaged{}, err
	}
	// The R1 kernel opens no auxiliary DB, so neither PlanKept nor the
	// StageReset call below names one.
	installID, plan, err := k.planReset(ctx, hook, req, sw.PlanKept(c.StoragePath, nil))
	if err != nil {
		return storage.ResetStaged{}, err
	}
	if err := checkResetPlan(req, plan); err != nil {
		return storage.ResetStaged{}, err
	}
	if err := refuseHeldStore(bootseam.FS(ctx), c.StoragePath); err != nil {
		return storage.ResetStaged{}, err
	}
	if f := c.BeforeSwap; f != nil {
		if err := f(c.StoragePath); err != nil {
			return storage.ResetStaged{}, fmt.Errorf("kernel: before the reset is staged: %w", err)
		}
	}
	in, err := sw.StageReset(ctx, restoreswap.ResetSpec{DBPath: c.StoragePath, Reset: req.Kind,
		DeleteLibraryFiles: req.DeleteLibraryFiles, Plan: plan, InstallID: installID, RequestedBy: requestedBy})
	if err != nil {
		return storage.ResetStaged{}, fmt.Errorf("kernel: stage the %s reset: %w", req.Kind, err)
	}
	k.log.Warn("kernel: reset staged; it applies at the next start of the service", "id", in.ID,
		"kind", string(req.Kind), "delete_library_files", req.DeleteLibraryFiles)
	return storage.ResetStaged{ID: in.ID, LibraryRoot: plan.LibraryRoot, LibraryFileCount: plan.LibraryFiles}, nil
}

// planReset reads what staging records from the live store, opened
// read-only and closed again before the lock is taken: the install ID
// (set_aside_started's, DATA9-15) and, for a ResetFactory with a hook, the
// hook's Plan with the module's storage and, always, kept, rule (4) of the
// reset hook (module.ResetPlanKept), so its preflight counts what the
// confined filesystem will let Run remove. A store that does not open
// read-only is logged and the reset goes on without them (Plan then runs
// without storage), because a store too broken to open is what a reset
// replaces; the confirmation check refuses a library wipe Plan could not
// confirm.
func (k *Kernel) planReset(ctx context.Context, hook *entry, req storage.ResetRequest, kept func(path string) bool) (string, storage.ResetPlan, error) {
	var installID string
	pctx := module.WithResetPlanKept(ctx, kept)
	o, err := k.liveStore(ctx)
	if err != nil {
		k.log.Warn("kernel: the store does not open read-only; staging the reset without its install ID or the hook's settings",
			"path", k.cfg.StoragePath, "err", err)
	} else {
		defer func() {
			// Read-only, so nothing is lost; the lock it held is released
			// either way before refuseHeldStore takes it.
			if cerr := o.Close(); cerr != nil {
				k.log.Warn("kernel: closing the read-only store", "path", k.cfg.StoragePath, "err", cerr)
			}
		}()
		installID = o.InstallID()
		if hook != nil && req.Kind == restorejournal.ResetFactory {
			if db, err := o.Offline(); err != nil {
				k.log.Warn("kernel: the reset hook plans without its storage", "module", string(hook.man.ID), "err", err)
			} else {
				pctx = module.WithResetPlanStorage(pctx, handlesOf(db, hook.man.ID))
			}
		}
	}
	if hook == nil || req.Kind != restorejournal.ResetFactory {
		return installID, storage.ResetPlan{}, nil
	}
	plan, err := hook.man.ResetHook.Plan(pctx, req)
	if err != nil {
		return "", storage.ResetPlan{}, fmt.Errorf("kernel: module %s refused the reset: %w", hook.man.ID, err)
	}
	return installID, plan, nil
}

// checkResetPlan is step 5 of StageResetOffline: the library wipe is
// confirmed against the hook's fresh preflight, and without the wipe no
// hook path reaches the library root.
func checkResetPlan(req storage.ResetRequest, plan storage.ResetPlan) error {
	if req.DeleteLibraryFiles {
		if plan.LibraryRoot == "" {
			return fmt.Errorf("%w: the reset hook named no library root, so there is nothing to confirm a wipe against", storage.ErrResetRequestInvalid)
		}
		if plan.LibraryRoot != req.LibraryRoot || plan.LibraryFiles != req.LibraryFileCount {
			return &storage.LibraryConfirmError{Root: plan.LibraryRoot, Files: plan.LibraryFiles}
		}
		return nil
	}
	if plan.LibraryRoot == "" {
		return nil
	}
	for _, p := range plan.Paths {
		if restorejournal.InTree(plan.LibraryRoot, p) {
			return fmt.Errorf("%w: hook path %s holds the library root %s and delete_library_files is not set", storage.ErrResetRequestInvalid, p, plan.LibraryRoot)
		}
	}
	return nil
}

// runResetHook is Run step (2)'s reset hook (06 §6.5.1 P0a; 05-storage
// §5S.3.15 "The app's reset hook"; COR8-4, COR8-8, OPS8-5): on a reset boot,
// and on every later boot whose journal reads applied without hook_done, a
// ResetFactory swap with a declared hook runs Manifest.ResetHook.Run after
// applied and before any listener, with the intent's hook_args, delete
// entries and delete_library_files, the confined state filesystem, an
// ExtendStart that asks the service manager for start time per batch, and
// Done. It never fails boot: Run is bounded by the reset hook timeout, a
// panic is recovered, and when Run returns (or the timeout passes) without
// Done the kernel calls Done itself with what failed; an error from Run, a
// failed entry or a journal write that did not happen is logged as
// factory_reset_cleanup_incomplete. Only a crash before Done journals
// hook_done reruns the hook, at the next boot. Once the kernel stops
// waiting, the hook's context is cancelled, so a hook that honours it
// stops, and its confined filesystem refuses every later call
// (storage.ErrConfined), so a hook goroutine that ignores its context
// removes nothing after hook_done is journaled (R1-25 review).
func (k *Kernel) runResetHook(ctx context.Context) {
	if k.swap == nil || !k.swap.hookOwed {
		return
	}
	e, id := k.resetEntry(), k.swap.out.ID
	log := k.log.With("id", id, "module", string(e.man.ID))
	in, err := k.swap.s.HookIntent(id)
	if err != nil {
		log.Error("kernel: the reset hook cannot run; the next boot retries it", "reason", cleanupIncomplete, "err", err)
		return
	}
	timeout := k.cfg.ResetHookTimeout
	if timeout <= 0 {
		timeout = defaultResetHookTimeout
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := &hookRun{s: k.swap.s, id: id}
	files := &endedFiles{files: k.swap.s.Confined(in, run.keep)}
	rr := storage.ResetRun{
		ID: id, Args: in.HookArgs, Delete: in.Delete, DeleteLibraryFiles: in.DeleteLibraryFiles,
		Files:       files,
		ExtendStart: func(step string) { k.sendExtend(e.man.ID, step) },
		Done:        run.done,
	}
	ran := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ran <- fmt.Errorf("reset hook panicked: %v", r)
			}
		}()
		ran <- e.man.ResetHook.Run(hctx, rr)
	}()
	var herr error
	select {
	case herr = <-ran:
	case <-hctx.Done():
		herr = fmt.Errorf("reset hook did not return within %s: %w", timeout, hctx.Err())
	}
	// The kernel stops waiting: the hook's context ends here, and its
	// filesystem with it, before hook_done can be journaled without it.
	cancel()
	files.ended.Store(true)
	var extra []string
	if herr != nil {
		extra = []string{"hook: " + herr.Error()}
	}
	if err := run.fallback(context.WithoutCancel(ctx), extra); err != nil {
		log.Error("kernel: hook_done not journaled; the next boot reruns the reset hook", "reason", cleanupIncomplete, "err", err)
		return
	}
	failed, kept := run.result()
	if herr != nil || len(failed) > 0 {
		log.Error("kernel: factory reset cleanup incomplete; the store was reset", "reason", cleanupIncomplete,
			"failed", failed, "kept", kept, "err", herr)
		return
	}
	log.Info("kernel: factory reset cleanup done", "kept", kept)
}

// endedFiles is the reset hook's confined filesystem for as long as the
// kernel waits on its run: once it stopped waiting (Run returned, or the
// timeout passed), every call is refused, so a hook goroutine still
// running cannot remove an entry after hook_done recorded the run's end.
type endedFiles struct {
	files storage.StateFiles
	ended atomic.Bool
}

func (f *endedFiles) refuse(path string) error {
	return fmt.Errorf("%w: %s: the reset hook's run already ended", storage.ErrConfined, path)
}

// ReadDir is the confined ReadDir while the run lasts.
func (f *endedFiles) ReadDir(path string) ([]iofs.DirEntry, error) {
	if f.ended.Load() {
		return nil, f.refuse(path)
	}
	return f.files.ReadDir(path)
}

// RemoveAll is the confined RemoveAll while the run lasts.
func (f *endedFiles) RemoveAll(path string) error {
	if f.ended.Load() {
		return f.refuse(path)
	}
	return f.files.RemoveAll(path)
}

// hookRun is one reset hook run's Done (05-storage §5S.3.15 "The app's
// reset hook"): serialized, so a hook that calls Done from several
// goroutines journals hook_done once, and every failed entry it reports is
// kept for the record and the log. The kernel's fallback after Run returns
// journals hook_done only when no Done call did. The restoreswap package
// takes no lock (its slot is decided by link(2) alone), so the
// serialization lives here.
type hookRun struct {
	s  *restoreswap.Swapper
	id string

	mu      sync.Mutex
	written bool     // hook_done journaled and the in-flight file deleted
	failed  []string // every entry any Done call reported
	kept    []string // every kept path the confined filesystem refused
}

// keep records a kept path the confined filesystem refused to remove.
func (r *hookRun) keep(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kept = append(r.kept, path)
}

// done is ResetRun.Done: it records failed and, unless an earlier call
// already did, journals hook_done with every failed entry and kept path so
// far. A call after hook_done is written changes nothing on disk.
func (r *hookRun) done(ctx context.Context, failed []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, failed...)
	if r.written {
		return nil
	}
	if err := r.s.HookDone(ctx, r.id, slices.Clone(r.failed), slices.Clone(r.kept)); err != nil {
		return err
	}
	r.written = true
	return nil
}

// fallback is the kernel's Done once Run returned or timed out: extra (the
// hook's error) is recorded, and hook_done is journaled when no Done call
// wrote it.
func (r *hookRun) fallback(ctx context.Context, extra []string) error {
	return r.done(ctx, extra)
}

// result is every failed entry and kept path the run reported.
func (r *hookRun) result() (failed, kept []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.failed), slices.Clone(r.kept)
}
