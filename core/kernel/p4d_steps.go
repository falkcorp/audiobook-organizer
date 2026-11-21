// file: core/kernel/p4d_steps.go
// version: 0.1.0
// guid: 4c7e2a19-8b3d-4f60-a5e1-92d0c6b7f381
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/backup/journal"
)

// errP4dRefuse marks a P4d step 2-4 failure: the boot refuses to serve (05-backup
// §5B.3.6, 05-authn §4.14, DATA3-18). Every error runP4dSteps returns wraps it.
var errP4dRefuse = errors.New("kernel: P4d identity restore steps failed; refusing to serve")

// p4dStepsInput is everything P4d steps 2-4 read (F4-08 A10). The hooks' input
// (step 3) is derived from Reapply, so steps 2 and 3 cannot be handed two
// different journals, keyspaces, installs or restores.
type p4dStepsInput struct {
	// Reapply is step 2's input; its Restore, Journal, KS, Install, Identity,
	// SwappedBack, Clock and Log are step 3's too.
	Reapply reapplyInput
	// OpenPrevious opens the pre-swap store directory read-only (step 3).
	OpenPrevious func(ctx context.Context, dir string) (previousView, error)
	// Hooks builds step 3's hook list, in the order auth, rbac, audit.
	Hooks func(env hookEnv) ([]journal.RestoreHook, error)
	// BootWrites is auth's every-boot writes (auth.BootWrites over the P4c
	// handles), step 4: after the hooks, never before (DATA3-18), and on every
	// boot whether or not a restore ran.
	BootWrites func(ctx context.Context) error
}

// p4dStepsResult is what steps 2 and 3 did, for the boot's health tokens and log.
type p4dStepsResult struct {
	Reapply reapplyResult
	Hooks   restoreHooksResult
}

// runP4dSteps runs P4d steps 2-4 in order: the revocation re-apply
// (reapplyRevocations), the restore hooks (runRestoreHooks, handed step 2's
// result) and auth's every-boot writes. Any step's error ends the run there,
// later steps do not run, and the error wraps errP4dRefuse: the kernel serves
// nothing after it. A journal that is broken or a fail-closed fallback is not an
// error (the steps report it in the result and the app stays open, §5B.5); only
// what the steps return as an error refuses.
//
// It imports no journal file system (core/internal/journalfs): the caller binds
// the journal component and the file operations, which is the wiring F4-08's
// boot does (and which TestJournalRowsAreUnboundUntilF408 watches for).
func runP4dSteps(ctx context.Context, in p4dStepsInput) (res p4dStepsResult, err error) {
	if in.Hooks == nil || in.BootWrites == nil {
		return res, fmt.Errorf("%w: the restore hooks or the boot writes are not wired", errP4dRefuse)
	}
	if res.Reapply, err = reapplyRevocations(ctx, in.Reapply); err != nil {
		return res, fmt.Errorf("%w: step 2 (revocation re-apply): %w", errP4dRefuse, err)
	}
	r := in.Reapply
	res.Hooks, err = runRestoreHooks(ctx, restoreHooksInput{
		Journal: r.Journal, KS: r.KS, Install: r.Install, Identity: r.Identity,
		Result: res.Reapply, Restore: r.Restore, SwappedBack: r.SwappedBack,
		OpenPrevious: in.OpenPrevious, Hooks: in.Hooks, Clock: r.Clock, Log: r.Log,
	})
	if err != nil {
		return res, fmt.Errorf("%w: step 3 (restore hooks): %w", errP4dRefuse, err)
	}
	if err := in.BootWrites(ctx); err != nil {
		return res, fmt.Errorf("%w: step 4 (auth boot writes): %w", errP4dRefuse, err)
	}
	return res, nil
}
