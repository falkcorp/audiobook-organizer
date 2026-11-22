// file: internal/books/store/ops_reconcile_cutover.go
// version: 0.1.0
// guid: b3e7a5d2-91c4-4f68-a0d3-5c8e2f1b7a49
// last-edited: 2026-11-22

package store

import (
	"context"

	"github.com/falkcorp/mediacore/storage"
)

// ReconcileOpsV3ForCutover is HEAD's ReconcileOpsV3WithV2 over books' legacy
// range r, for the tasks cutover step (M4; 05-tasks 5T.8.3 order step 1). The
// step runs it once before it decodes a run, so every v3 record agrees with its
// v2 row first.
//
// It differs from HEAD's every-boot call in what a repair can do. By the time M4
// runs, the build's claims over the ops families are retired, and a retired
// claim refuses a Set (storage.ClaimRetired): a record the reconcile would
// rebuild cannot be written, and the call fails with the claim error instead.
// That is the intended outcome. HEAD's own boot repairs every record on every
// start, so a store that reaches the flip with a stale one was last written by a
// binary without the opv3 projection; the boot stops with the checkpoint open
// and nothing changed, and the owner runs HEAD's build once. A store whose
// records are all current is only read, and an orphan record's delete is
// admitted by a retired claim, so that repair still runs.
//
// The store is the migration-step kind (no memdb warmup, never closed: it owns
// nothing), as the legacy steps' are.
func ReconcileOpsV3ForCutover(ctx context.Context, r storage.LegacyRange) (OpsV3ReconcileReport, error) {
	s, err := newStepStore(r)
	if err != nil {
		return OpsV3ReconcileReport{}, err
	}
	return s.ReconcileOpsV3WithV2(ctx)
}
