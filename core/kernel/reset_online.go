// file: core/kernel/reset_online.go
// version: 0.1.0
// guid: 5d2e9a47-1c68-4b03-9f7e-a3b8c40d6e15
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
)

// errResetNoStore refuses a reset staged by a kernel that opened no store: the
// install ID a reset records is the open store's.
var errResetNoStore = errors.New("kernel: this app opened no store, so there is nothing to reset")

// resetStager is storagehttp.Stager: the factory-reset route's staging, over
// the running service (05-storage §5S.3.15 "Stage"; 03 §3.6 storagehttp). It is
// the offline command's staging (StageResetOffline) with what a running
// service already has in place of what the command has to take:
//
//   - no state-directory owner check and no swap lock: this process is the
//     owner and holds the lock shared for its whole life;
//   - no read-only open of the store: the install ID is the open store's, and
//     the hook's Plan reads its own Deps.Storage (the module is running), so
//     module.ResetPlanStorage is not set;
//   - no refuseHeldStore: the store is held, by this process;
//   - a restart request after the intent is staged, because the reset applies
//     at P0a of the next start.
//
// The shared middle is hookPlan (with module.ResetPlanKept on the context,
// rule (4)), checkResetPlan and commitReset (BeforeSwap, then StageReset), so
// the two entry points cannot drift on what is confirmed or what is staged.
//
// The Swapper is built once, at boot, over the boot's filesystem and clock, so
// a request staged under a test's in-memory filesystem sees that filesystem and
// a request context carries none of the boot's seams.
type resetStager struct {
	k  *Kernel
	sw *restoreswap.Swapper
	fs vfs.FS

	// mu serializes stagers: the swap slot holds one intent, and two requests
	// reading the slot as free would both reach StageReset.
	mu sync.Mutex
}

// newResetStager builds the online Stager from the boot's context.
func (k *Kernel) newResetStager(ctx context.Context) (*resetStager, error) {
	sw, err := k.swapper(ctx)
	if err != nil {
		return nil, err
	}
	return &resetStager{k: k, sw: sw, fs: bootseam.FS(ctx)}, nil
}

// StageReset stages the reset and, only when it is staged, asks the service to
// restart. Nothing is staged, and no restart is requested, when any step
// refuses. requestedBy is the user ID of the principal that asked.
func (s *resetStager) StageReset(ctx context.Context, requestedBy string, req storage.ResetRequest) (storage.ResetStaged, error) {
	k := s.k
	if err := req.Validate(); err != nil {
		return storage.ResetStaged{}, err
	}
	if k.store == nil || k.store.db == nil {
		return storage.ResetStaged{}, errResetNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hook := k.resetEntry()
	if hook == nil && req.DeleteLibraryFiles {
		return storage.ResetStaged{}, fmt.Errorf("%w: this app declares no reset hook, so a reset deletes no library file", storage.ErrResetRequestInvalid)
	}
	// A staged or in-flight swap of any kind refuses, before the hook runs.
	if err := refuseBusySlotOf(s.fs, k.cfg.StateDir); err != nil {
		return storage.ResetStaged{}, err
	}
	// The R1 kernel opens no auxiliary DB, so neither PlanKept nor the
	// StageReset call names one.
	pctx := module.WithResetPlanKept(ctx, s.sw.PlanKept(k.cfg.StoragePath, nil))
	plan, err := hookPlan(pctx, hook, req)
	if err != nil {
		return storage.ResetStaged{}, err
	}
	if err := checkResetPlan(req, plan); err != nil {
		return storage.ResetStaged{}, err
	}
	staged, err := k.commitReset(ctx, s.sw, requestedBy, req, plan, k.store.db.Admin().InstallID())
	if err != nil {
		return storage.ResetStaged{}, err
	}
	k.lifecycle.RequestRestart("a storage reset was staged and applies at the next start")
	return staged, nil
}
