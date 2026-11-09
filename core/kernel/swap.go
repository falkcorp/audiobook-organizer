// file: core/kernel/swap.go
// version: 1.0.9
// guid: 2fd2ee78-9c6b-4e9f-82de-80def73f26d5
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/buildid"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// phaseP0a is the pre-boot swap's label on <namespace>_kernel_boot_phase_seconds
// (06 §6.9's label set).
const phaseP0a = "P0a"

// ErrSwapInFlight: a restore or factory reset is in flight (a
// restore/<id>/inflight.json exists) that this boot's P0a did not take over,
// so the kernel does not open the store (05-storage §5S.3.7 reader check,
// for the kernel's own open). Boot the current build once to finish the
// swap. Openers without P0a (the offline commands, the updater's rollback)
// get 05-storage's CheckReader with its restore_inflight token (R1-25, F8).
var ErrSwapInFlight = errors.New("kernel: a restore or factory reset is in flight (restore_inflight); boot the current build once to finish it")

// ErrSwappedBack: a restore boot failed and step x swapped the original store
// back; the restart boots it (05-backup §5B.3.4 step x). Compared with
// errors.Is.
var ErrSwappedBack = restoreswap.ErrSwappedBack //archtest:allow-var the swap's sentinel re-exported for the kernel's callers, compared with errors.Is

// ErrCheckpointRestoreFailedHere: this binary failed a checkpoint restore's
// migrations and refuses every later boot until a fixed build is installed
// or the previous binary's storage restore-offline --checkpoint runs
// (DATA7-12, DATA8-6). Compared with errors.Is.
var ErrCheckpointRestoreFailedHere = restoreswap.ErrCheckpointRestoreFailedHere //archtest:allow-var the swap's sentinel re-exported for the kernel's callers, compared with errors.Is

// kernelSwap is what P0a leaves the storage phases: the swapper the kernel
// holds (storage.Admin has no staging or swap method, COR8-5) and what P0a
// did.
type kernelSwap struct {
	s   *restoreswap.Swapper
	out restoreswap.Outcome
	// hookOwed: a ResetFactory swap whose app declares a reset hook reached
	// P4b, so hook_done is the hook's (Run step (2), reset.go), not P4b's.
	hookOwed bool
}

// swapWanted gates P0a: a state directory, and not Describe, which performs
// no I/O and never opens the store (COR2-13).
func (k *Kernel) swapWanted(describe bool) bool { return !describe && k.cfg.StateDir != "" }

// storageOwners is every module that declares storage, as the store's
// owners: enabled or not (a disabled module's claims stay validated and its
// keyspace reserved).
func (k *Kernel) storageOwners() []storage.OwnerSpec {
	var owners []storage.OwnerSpec
	for _, e := range k.entries {
		if DeclaresStorage(e.man) {
			owners = append(owners, storage.OwnerSpec{Owner: storage.Owner(e.man.ID), Spec: e.man.Storage, Enabled: e.enabled})
		}
	}
	return owners
}

// binaryID is the binary ID (05-storage §5S.3.7 "Binary ID"), computed once
// per process: the swap's staged_by_build check and every build field of
// the store name it, never the core version constant. The crash-matrix kit
// hands one in through bootseam, so its thousands of boots hash no
// executable.
func (k *Kernel) binaryID(ctx context.Context) (string, error) {
	if k.build != "" {
		return k.build, nil
	}
	if s := bootseam.From(ctx); s != nil && s.BuildID != "" {
		k.build = s.BuildID
		return k.build, nil
	}
	b, err := buildid.Binary(k.cfg.Version)
	if err != nil {
		return "", fmt.Errorf("kernel: binary ID: %w", err)
	}
	k.build = b
	return b, nil
}

// bootClock is the boot's clock: time.Now, or the crash-matrix kit's fixed
// clock through bootseam, so two runs of one boot make the same operations
// (the swap's expires_at check, the store's timestamps and checkpoint names).
func bootClock(ctx context.Context) func() time.Time {
	if s := bootseam.From(ctx); s != nil && s.Clock != nil {
		return s.Clock
	}
	return time.Now
}

// bootProbe is the swap's device probe for a boot over fs: the seam's
// (bootseam.Seams.Probe, a test's model of a second filesystem), else the
// operating system's on the real filesystem, else none on an in-memory one
// (*vfs.MemFS), which has no devices. Any other filesystem gets refuseProbe,
// so the device checks P0a runs reject the staged swap there instead of
// being skipped (R1-21 verification review).
func bootProbe(ctx context.Context, fs vfs.FS) restorejournal.Probe {
	if s := bootseam.From(ctx); s != nil && s.Probe != nil {
		return s.Probe
	}
	root := vfs.Root(fs)
	if root == vfs.Default {
		return restorejournal.OSProbe()
	}
	if _, mem := root.(*vfs.MemFS); mem {
		return nil
	}
	return refuseProbe{root: root}
}

// refuseProbe is the device probe of a filesystem that is neither the real
// one nor in memory: it cannot tell a device, an absence or a writable
// directory, so every query fails, and its errors never match
// os.ErrNotExist. Step b's rename pre-check (restorejournal.Validate) then
// refuses every move, and step a's delete rule (ValidateDelete) every
// entry, so the staged swap is rejected. Today the swap's journal already
// halts P0a on such a filesystem (restorejournal.ChmodFS cannot make it
// 0600); this keeps the device checks closed on their own.
type refuseProbe struct{ root vfs.FS }

func (p refuseProbe) refuse(path string) error {
	return fmt.Errorf("kernel: no device probe for %s: filesystem %T is neither the real one nor in memory", path, p.root)
}

func (p refuseProbe) ItemDev(path string) (uint64, error) { return 0, p.refuse(path) }
func (p refuseProbe) DirDev(path string) (uint64, error)  { return 0, p.refuse(path) }
func (p refuseProbe) Writable(dir string) error           { return p.refuse(dir) }

// runP0a is boot phase P0a (06 §6.5.1; 05-backup §5B.3.4; 05-storage
// §5S.3.15): the swapper resumes the swap whose in-flight file exists, else
// applies the staged intent, before the store opens. It runs after P1 and
// logging (decision D14: step e needs the app's owners and the swap a
// logger; nothing before it is durable). A swap P0a rejected or swapped back
// itself leaves the untouched store to boot, degraded. Then the reader check
// of 05-storage §5S.3.7 for the kernel's own open (decision D9): the store
// opens with an in-flight file only for the journal P0a just took over.
func (k *Kernel) runP0a(ctx context.Context) error {
	if k.cfg.StoragePath == "" {
		// A swap applies to a store (R1-21 review m10): run with none, a
		// staged restore would start and wait at swapped, in flight, on
		// every boot. Refused before the state directory is read.
		return errors.New("kernel: Config.StateDir is set without Config.StoragePath; the pre-boot swap (P0a) needs the store it swaps")
	}
	fs := bootseam.FS(ctx)
	build, err := k.binaryID(ctx)
	if err != nil {
		return err
	}
	clock := bootClock(ctx)
	sw, err := restoreswap.New(restoreswap.Options{FS: fs, StateDir: k.cfg.StateDir, BuildID: build, Clock: clock,
		Owners: k.storageOwners(), Probe: bootProbe(ctx, fs), Logger: k.coreLog("restoreswap")})
	if err != nil {
		return fmt.Errorf("kernel: pre-boot swap (P0a): %w", err)
	}
	out, err := sw.RunP0a(ctx)
	if err != nil {
		return fmt.Errorf("kernel: pre-boot swap (P0a): %w", err)
	}
	k.swap = &kernelSwap{s: sw, out: out}
	if out.Degraded != "" {
		// Health's degraded reasons arrive with health's checks; until then
		// the token is logged with its reason.
		k.log.Error("kernel: degraded: the staged swap did not apply; booting the untouched store",
			"reason", out.Degraded, "detail", out.Reason, "id", out.ID)
	}
	id, ok, err := restoreswap.InFlight(fs, k.cfg.StateDir)
	switch {
	case err != nil:
		return fmt.Errorf("kernel: pre-boot swap (P0a): %w", err)
	case ok && (out.Boot == restoreswap.BootPlain || out.ID != id):
		return fmt.Errorf("%w: restore/%s/inflight.json, which this boot's P0a did not take over", ErrSwapInFlight, id)
	}
	return nil
}

// swapBeforeOpen is the restore boot's hand-over to P2: storage must open at
// the restored path (DATA2-25), else step x runs; a marker-less restored
// store gets the intent's install ID (the stager's, read from the live store
// it replaced). A reset boot passes none: its fresh install takes a new one.
func (k *Kernel) swapBeforeOpen(ctx context.Context, opts *storage.OpenOptions) error {
	if k.swap == nil || k.swap.out.Boot != restoreswap.BootRestore {
		return nil
	}
	in := k.swap.out.Intent
	if filepath.Clean(opts.Path) != filepath.Clean(in.DBPath) {
		return fmt.Errorf("kernel: core.storage.path %s is not the restored store's path %s", opts.Path, in.DBPath)
	}
	opts.InstallID = in.InstallID
	return nil
}

// swapPhase wraps one storage phase of a restore or reset boot: after the
// phase succeeds, the journal step it owes; when it fails, the store is
// closed and the restore's failure exit runs. A plain boot's phase runs as
// it is.
func (k *Kernel) swapPhase(ctx context.Context, phase string, fn func() error) func() error {
	return func() error {
		err := fn()
		if k.swap == nil || k.swap.out.Boot == restoreswap.BootPlain {
			return err
		}
		if err != nil {
			return k.swapFailed(ctx, phase, err)
		}
		return k.swapAfter(ctx, phase)
	}
}

// swapAfter journals what a storage phase owes the swap (05-backup §5B.3.4
// steps g and h; 05-storage §5S.3.15 "Journal and keyring", "Completion"):
//   - restore, after P2: opened;
//   - restore, after P4b: marker_written{HooksOwed} and applied, then the
//     in-flight file deleted (R1 has no P4d steps 2-3: machine 1's one edge
//     opened → marker_written);
//   - reset, after P4a's fresh install: applied (R1 has no keyring, so at
//     the point secrets.Open would rewrite its header, the start of P4b);
//   - reset, after P4b (P4d step 1, after the migration list; R1 has no
//     P4c or P4d phase): a ResetStore swap, or any reset of an app with no
//     reset hook, has the delete entries removed, then hook_done and the
//     in-flight file deleted (COR8-4); a ResetFactory swap of an app with a
//     reset hook leaves hook_done to the hook, which runs at Run step (2)
//     (runResetHook). A boot that resumes a journal at applied does only
//     this step.
//
// A journal write that fails halts boot; the next boot resumes the journal.
func (k *Kernel) swapAfter(ctx context.Context, phase string) error {
	s, out := k.swap.s, k.swap.out
	var err error
	switch {
	case out.Boot == restoreswap.BootRestore && phase == phaseP2:
		err = s.Opened(ctx, out.ID)
	case out.Boot == restoreswap.BootRestore && phase == phaseP4b:
		err = s.RestoreApplied(ctx, out.ID)
	case out.Boot == restoreswap.BootReset && phase == phaseP4a:
		err = s.ResetApplied(ctx, out.ID)
	case (out.Boot == restoreswap.BootReset || out.Boot == restoreswap.BootResetApplied) && phase == phaseP4b:
		if out.Intent.Reset == restorejournal.ResetFactory && k.resetEntry() != nil {
			k.swap.hookOwed = true
			break
		}
		err = s.ResetDone(ctx, out.ID)
	}
	if err != nil {
		return fmt.Errorf("kernel: swap journal %s after %s: %w", out.ID, phase, err)
	}
	return nil
}

// swapFailed is a restore boot's failure exit from P2, P4a or P4b (05-backup
// §5B.3.4 step x; DATA7-12): the store closed first, then step x, which
// returns ErrSwappedBack once the original is back (the boot exits and the
// restart boots it). A KindCheckpoint restore in P4b is the exception: when
// a migration step itself failed (a *storage.StepError, "the migration DAG
// fails"), it journals failed with this binary's ID and keeps its in-flight
// file; any other P4b failure (a sidecar, the checkpoint's record or
// pointer, a cancelled boot) says nothing about the steps, so it journals
// nothing and the next boot of this binary resumes the restore boot at
// opened (R1-21 review (f)). A reset boot's failure halts boot and the next
// boot resumes it.
func (k *Kernel) swapFailed(ctx context.Context, phase string, cause error) error {
	out := k.swap.out
	if out.Boot != restoreswap.BootRestore {
		return cause
	}
	if err := k.closeStorage(); err != nil {
		return errors.Join(cause, fmt.Errorf("kernel: step x of %s needs the store closed: %w", out.ID, err))
	}
	k.store = nil
	if phase == phaseP4b && out.Intent.Kind == restorejournal.KindCheckpoint {
		if _, ok := errors.AsType[*storage.StepError](cause); !ok {
			return cause
		}
		if err := k.swap.s.CheckpointFailed(ctx, out.ID); err != nil {
			return errors.Join(cause, err)
		}
		return fmt.Errorf("%w (journal %s): %w", ErrCheckpointRestoreFailedHere, out.ID, cause)
	}
	return k.swap.s.SwapBack(ctx, out.ID, fmt.Errorf("%s: %w", phase, cause))
}
