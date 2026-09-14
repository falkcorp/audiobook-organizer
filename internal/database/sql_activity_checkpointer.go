// file: internal/database/sql_activity_checkpointer.go
// version: 1.0.0
// guid: 5e0b7c2a-91d4-4f3e-8a6b-2c7d9e1f4a08
// last-edited: 2026-09-14

// WAL checkpointing for SQLActivityStore.
//
// WHY THIS EXISTS. Until 2026-09-14 the store left SQLite's wal_autocheckpoint
// at its default (1000 pages). Under that default, the COMMIT that pushes the
// WAL past the threshold runs the checkpoint itself, synchronously, inside
// whatever call made the commit. On 2026-09-14 that call was the
// "Server started" activity Record inside server.NewServer: a deploy had
// killed maintenance.compact-activity-log mid-run, the WAL it left behind was
// many gigabytes, and the first write after boot copied all of it back into
// the database file (the goroutine dump showed pwrite at offset ~19 GB) before
// the HTTP listener opened. The app was down for about six minutes.
//
// THE RULE NOW: no caller of Record, RecordBatch or any other write ever pays
// for a checkpoint. wal_autocheckpoint is 0 on every connection, and all
// checkpoints run on a dedicated connection (s.ckpt):
//
//   - A background goroutine, started by OpenSQLiteActivityStore and stopped by
//     Close, runs a PASSIVE checkpoint every sqlActCheckpointInterval. PASSIVE
//     never takes the writer lock and never waits on the busy handler, so a
//     live Record is never blocked by it. When nothing was written since the
//     previous tick and PASSIVE backfilled every frame, it follows up with
//     TRUNCATE, which is then only a cheap reset of an already-copied WAL.
//   - Long deleting maintenance (CompactByDay) checkpoints PASSIVE after every
//     committed chunk, so the WAL is bounded by one chunk's pages plus whatever
//     a reader snapshot pins, not by a whole day's millions of rows. That is
//     what keeps an interrupted compaction from leaving a multi-GB WAL behind.
//   - journal_size_limit caps the on-disk size SQLite leaves the -wal at after
//     it is reset, so one burst does not keep a huge file around.
//
// Starting the goroutine from the constructor (not a separate Start call) is
// deliberate: with autocheckpoint off, a caller that forgot to start it would
// silently get an unbounded WAL.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

const (
	// sqlActCheckpointInterval is how often the background checkpointer runs.
	sqlActCheckpointInterval = 30 * time.Second

	// sqlActCloseCheckpointBudget bounds the final checkpoint in Close. The
	// production unit stops with TimeoutStopSec=30 and Server.Start spends
	// part of that on the HTTP drain and the ops registry before it gets
	// here, so a checkpoint that cannot finish in this budget is abandoned
	// (see Close) rather than allowed to run into SIGKILL.
	sqlActCloseCheckpointBudget = 5 * time.Second

	// sqlActJournalSizeLimit is the size, in bytes, SQLite truncates the -wal
	// file to after a reset (PRAGMA journal_size_limit).
	sqlActJournalSizeLimit = 64 << 20

	// sqlActCkptBusyTimeoutMS is the checkpoint connection's busy_timeout. Only
	// TRUNCATE consults the busy handler (PASSIVE never does); a short wait is
	// right because TRUNCATE blocks new writers while it waits, and the
	// checkpointer will simply try again on the next tick.
	sqlActCkptBusyTimeoutMS = 1000
)

var ckptLog = logger.New("activity-checkpoint")

// walCheckpointResult is the row PRAGMA wal_checkpoint returns: busy is 1 when
// the checkpoint could not complete, log is the number of frames in the WAL
// and checkpointed is how many of them are now in the database file.
type walCheckpointResult struct {
	Busy, Log, Checkpointed int
}

// complete reports whether every WAL frame is in the database file.
func (r walCheckpointResult) complete() bool {
	return r.Busy == 0 && r.Checkpointed >= r.Log
}

// sqlActCheckpointer is the background checkpoint goroutine's state.
type sqlActCheckpointer struct {
	interval time.Duration
	ctx      context.Context // cancelled by stop: interrupts an in-flight checkpoint
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once

	// writes counts foreground write commits; the loop compares it across ticks
	// to decide whether the store is idle enough for TRUNCATE.
	writes atomic.Uint64
	// runs counts completed background ticks (for tests).
	runs atomic.Uint64

	// hook, when set, observes every checkpoint the store issues (tests only).
	// Atomic because the background goroutine is already running when a test
	// installs it.
	hook atomic.Pointer[ckptHookFn]
}

// ckptHookFn is the test observer type for sqlActCheckpointer.hook.
type ckptHookFn func(mode string, res walCheckpointResult, err error)

// openCheckpointConn opens the dedicated one-connection handle checkpoints run
// on. A separate handle matters because the writer handle has exactly one
// connection: a checkpoint issued on it would queue every Record behind it.
func openCheckpointConn(driver, path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(wal)&_pragma=wal_autocheckpoint(0)&_pragma=busy_timeout(%d)",
		path, sqlActCkptBusyTimeoutMS)
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// startCheckpointer launches the background loop. Called once, by the
// constructor.
func (s *SQLActivityStore) startCheckpointer(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	s.ckptr.interval = interval
	s.ckptr.ctx = ctx
	s.ckptr.cancel = cancel
	s.ckptr.done = make(chan struct{})
	go s.runCheckpointer()
}

func (s *SQLActivityStore) runCheckpointer() {
	defer close(s.ckptr.done)
	ticker := time.NewTicker(s.ckptr.interval)
	defer ticker.Stop()
	lastWrites := s.ckptr.writes.Load()
	for {
		select {
		case <-s.ckptr.ctx.Done():
			return
		case <-ticker.C:
		}
		w := s.ckptr.writes.Load()
		idle := w == lastWrites
		lastWrites = w
		s.checkpointAndMaybeTruncate(s.ckptr.ctx, idle)
		s.ckptr.runs.Add(1)
	}
}

// stopCheckpointer cancels the loop (interrupting a checkpoint in flight) and
// waits for it to return. Idempotent.
func (s *SQLActivityStore) stopCheckpointer() {
	s.ckptr.stopOnce.Do(func() {
		if s.ckptr.cancel == nil {
			return
		}
		s.ckptr.cancel()
		<-s.ckptr.done
	})
}

// noteWrite records a foreground commit for the idle check.
func (s *SQLActivityStore) noteWrite() { s.ckptr.writes.Add(1) }

// walCheckpoint runs PRAGMA wal_checkpoint(mode) on the checkpoint connection.
// ctx cancellation interrupts it (modernc wires ctx to sqlite3_interrupt, and
// the checkpoint's copy loop honours the interrupt).
func (s *SQLActivityStore) walCheckpoint(ctx context.Context, mode string) (walCheckpointResult, error) {
	var res walCheckpointResult
	err := s.ckpt.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").
		Scan(&res.Busy, &res.Log, &res.Checkpointed)
	if hook := s.ckptr.hook.Load(); hook != nil {
		(*hook)(mode, res, err)
	}
	return res, err
}

// checkpointAndMaybeTruncate runs PASSIVE, and when idle and PASSIVE copied
// every frame, TRUNCATE to reset the -wal to zero bytes. Errors are logged, not
// returned: a checkpoint that fails is retried on the next tick or chunk and
// costs nothing but WAL size in the meantime.
func (s *SQLActivityStore) checkpointAndMaybeTruncate(ctx context.Context, idle bool) {
	res, err := s.walCheckpoint(ctx, "PASSIVE")
	if err != nil {
		if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			ckptLog.Warn("PASSIVE checkpoint of %s failed: %v", s.path, err)
		}
		return
	}
	if !idle || res.Log == 0 || !res.complete() {
		return
	}
	if _, err := s.walCheckpoint(ctx, "TRUNCATE"); err != nil && ctx.Err() == nil {
		// SQLITE_BUSY here is ordinary (a writer arrived); the next idle tick
		// retries.
		ckptLog.Debug("TRUNCATE checkpoint of %s skipped: %v", s.path, err)
	}
}

// checkpointBetweenBatches is the incremental checkpoint a long deleting pass
// runs after each committed batch, on its own goroutine.
func (s *SQLActivityStore) checkpointBetweenBatches(ctx context.Context) {
	if _, err := s.walCheckpoint(ctx, "PASSIVE"); err != nil && ctx.Err() == nil {
		ckptLog.Warn("between-batch checkpoint of %s failed: %v", s.path, err)
	}
}
