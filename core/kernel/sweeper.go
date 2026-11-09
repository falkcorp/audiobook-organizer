// file: core/kernel/sweeper.go
// version: 1.0.0
// guid: 3a7d2e91-58c4-4b0f-9e16-c2f8a05d47b9
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// checkpointSweepInterval is the checkpoint sweeper's period while serving
// (05-storage §5S.3.12 "Retention"). It is fixed: no setting changes it,
// and none lengthens the 7 days a checkpoint is kept after it stops being
// needed (storage's own constant).
const checkpointSweepInterval = time.Hour

// startCompleteMessage is the kernel's record that Run's start sequence is
// done: readiness (start step 8), then 05-storage §5S.4 step 15's first
// checkpoint sweep. It is logged on every Run that reaches readiness, with
// or without a store. The crash-matrix kit stops recording there, so the
// first sweep's writes are part of the writer table's range (06 §6.5.1, the
// "After readiness" row).
const startCompleteMessage = "kernel: start complete"

// sweeper is the checkpoint sweeper, a kernel lifecycle component (06
// §6.5.1 writer table, "After readiness"; 05-storage §5S.4 step 15): Run
// starts it after readiness with one sweep at once, then one per interval
// while serving, and the stop order stops it first. It is unexported
// kernel machinery (R1-24 code contract): apps see only its findings,
// through Kernel.CheckpointHealth.
//
// It runs sequentially in one goroutine: checkpoints are counted in single
// digits, so a pool would buy nothing, and the crash-matrix labeller needs
// the first sweep's writes on one goroutine.
type sweeper struct {
	sweep    func(context.Context) (storage.SweepReport, error)
	after    func(context.Context, storage.SweepReport) // records the checkpoint gauge
	log      *slog.Logger
	interval time.Duration

	mu         sync.Mutex
	loose      []string // the last successful sweep's storage_checkpoint_mode paths
	looseTotal int
	cancel     context.CancelFunc
	done       chan struct{}
}

// runOnce is one sweep. A failure is logged and keeps the last sweep's mode
// findings: the next period tries again, and a sweep another process holds
// the lock for (storage prune-checkpoints) is not an error.
func (s *sweeper) runOnce(ctx context.Context) {
	rep, err := s.sweep(ctx)
	switch {
	case errors.Is(err, storage.ErrCheckpointSweepBusy):
		s.log.InfoContext(ctx, "kernel: checkpoint sweep skipped; another sweep or prune holds the checkpoint directory", "err", err)
		return
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		s.log.ErrorContext(ctx, "kernel: checkpoint sweep failed; it runs again at the next period", "err", err)
		return
	}
	s.mu.Lock()
	s.loose, s.looseTotal = slices.Clone(rep.Loose), rep.LooseTotal
	s.mu.Unlock()
	if rep.LooseTotal > 0 {
		// The copy holds plaintext secrets and raw session tokens: a mode
		// loosened by hand or by a copy is reported, never changed back
		// (05-storage §5S.3.12 "Modes", SEC-9).
		s.log.ErrorContext(ctx, "kernel: a checkpoint directory or file is group- or world-accessible (storage_checkpoint_mode); restore mode 0700 on directories and 0600 on files",
			"paths", rep.Loose, "count", rep.LooseTotal)
	}
	if s.after != nil {
		s.after(ctx, rep)
	}
}

// start runs the first sweep and then starts the periodic loop. The first
// sweep finishes even when ctx is already done (a boot stopped at once
// still keeps the 7-day promise); the loop ends at stop.
func (s *sweeper) start(ctx context.Context) {
	base := context.WithoutCancel(ctx)
	s.runOnce(base)
	loop, cancel := context.WithCancel(base)
	s.mu.Lock()
	s.cancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-loop.Done():
				return
			case <-t.C:
				s.runOnce(loop)
			}
		}
	}()
}

// stop cancels the loop and waits for a sweep in progress until deadline;
// storage checks ctx between checkpoints, so a deletion once begun
// finishes. A sweeper that never started stops at once.
func (s *sweeper) stop(deadline time.Time) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-t.C:
		s.log.Error("kernel: checkpoint sweep abandoned at the shutdown budget")
		return fmt.Errorf("kernel: checkpoint sweeper abandoned: %w", context.DeadlineExceeded)
	}
}

// findings is the last successful sweep's storage_checkpoint_mode paths and
// their count.
func (s *sweeper) findings() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.loose), s.looseTotal
}

// newSweeper registers the checkpoint sweeper on the open store, after P5
// built the checkpoint gauge (boot). Run starts it after readiness. With no
// store there is none.
func (k *Kernel) newSweeper() {
	if k.store == nil || k.store.db == nil {
		return
	}
	db, stateDir := k.store.db, k.cfg.StateDir
	k.store.sweeper = &sweeper{
		sweep: func(ctx context.Context) (storage.SweepReport, error) {
			return db.SweepCheckpoints(ctx, stateDir)
		},
		after:    func(ctx context.Context, _ storage.SweepReport) { k.recordCheckpointBytes(ctx) },
		log:      k.coreLog("storage"),
		interval: checkpointSweepInterval,
	}
}

// startSweeper is 05-storage §5S.4 step 15, after readiness: the first
// sweep, then the hourly loop. It then logs startCompleteMessage, on every
// Run that reached readiness.
func (k *Kernel) startSweeper(ctx context.Context) {
	if k.store != nil && k.store.sweeper != nil {
		k.store.sweeper.start(ctx)
	}
	k.log.InfoContext(ctx, startCompleteMessage)
}

// stopSweeper stops the sweeper first in the stop order: it started last.
func (k *Kernel) stopSweeper(deadline time.Time) error {
	if k.store == nil || k.store.sweeper == nil {
		return nil
	}
	return k.store.sweeper.stop(deadline)
}

// CheckpointHealth is what health reports about the store's checkpoints
// (05-storage §5S.3.12; 05-health §5H.3.1): each checkpoint and scratch
// entry as it is now, and the last sweep's storage_checkpoint_mode paths.
// Release R1 only (app.App.CheckpointHealth says why). Zero, with no error,
// when no store is open.
func (k *Kernel) CheckpointHealth() (storage.CheckpointHealth, error) {
	if k.store == nil || k.store.db == nil {
		return storage.CheckpointHealth{}, nil
	}
	h, err := k.store.db.CheckpointHealth(k.cfg.StateDir)
	if err != nil {
		return h, err
	}
	if s := k.store.sweeper; s != nil {
		h.Loose, h.LooseTotal = s.findings()
	}
	return h, nil
}
