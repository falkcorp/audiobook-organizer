// file: internal/database/sql_activity_summary_clamp.go
// version: 1.4.0
// guid: 3f1d8a24-6c05-4b9e-8d72-51ac07e4b6f3
// last-edited: 2026-10-06

// Package database — retroactive clamp of oversized activity `summary` values.
//
// WHY: activitySummaryMax (8 KiB) is enforced at WRITE time by
// clampActivitySummary, which Record and recordBatch both call. That bounds
// every NEW row and nothing else — the rows written before the clamp existed
// are still whatever length they were. Measured on prod 2026-09-07: the activity
// database was 5.3 GB, of which `summary` alone held 5.48 GB, 208,103 `system`
// rows averaged 27 KB of summary, and the single largest was 9,558,930 bytes (an
// iTunes write-back failure that formatted ~100k contract violations into one
// string). Fixing the producer and adding the write-time clamp stopped the
// bleeding; it reclaimed nothing.
//
// This applies the SAME bound retroactively, reusing clampActivitySummary rather
// than reimplementing it, so a backfilled row is byte-identical to what a
// freshly-written row of the same input would be. That matters more than it
// sounds: the function is idempotent by construction (its output, marker
// included, is bounded by the cap), so this whole pass is safe to re-run, to
// interrupt, and to overlap with live writes that are clamping the same way.
//
// WHY CLAMP RATHER THAN COMPRESS: `summary` is searched with instr() and is not
// a BLOB. Compressing it the way `details` is compressed
// (sql_activity_details_codec.go) would require a TEXT→BLOB migration and would
// break substring search, to save space on rows that this pass bounds to 8 KiB
// anyway. Compression is the right answer for `details`, which is opaque to
// every query; it is the wrong answer for a column the UI greps.
//
// WHY SEQUENTIAL, given the repo's concurrency mandate: SQLite permits exactly
// one writer (see SQLActivityStore.writer, a single connection). Fanning this
// out across goroutines would serialize on that connection anyway and would only
// add SQLITE_BUSY contention against live activity writes. The work is bounded
// instead by chunking — each batch is one short transaction, so live writes
// interleave between batches rather than waiting out one enormous one.
package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// sqlActClampBatch is how many oversized rows are read and rewritten per
// transaction. Deliberately far smaller than sqlActDeleteChunk (5000): each row
// here can carry megabytes of summary, so a 5000-row batch could materialize
// gigabytes in memory and in one WAL frame. At 8 KiB+ per row this bounds a
// batch to a few hundred MB worst case and typically far less.
const sqlActClampBatch = 200

// ClampSummariesResult reports what a retroactive clamp pass did.
//
// BytesBefore/BytesAfter are summed over the rows this pass actually rewrote,
// not over the table, so Reclaimed() is the space this pass freed rather than a
// whole-table statistic that would be wrong the moment anything else wrote.
type ClampSummariesResult struct {
	Scanned     int   `json:"scanned"`      // oversized rows read
	Clamped     int   `json:"clamped"`      // rows actually rewritten
	BytesBefore int64 `json:"bytes_before"` // summary bytes across rewritten rows, pre-clamp
	BytesAfter  int64 `json:"bytes_after"`  // …post-clamp
	Truncated   bool  `json:"truncated"`    // stopped on Max before running out of rows
}

// Reclaimed is the number of summary bytes this pass freed.
func (r ClampSummariesResult) Reclaimed() int64 { return r.BytesBefore - r.BytesAfter }

// ClampSummariesOptions bounds a clamp pass.
type ClampSummariesOptions struct {
	// Max caps how many oversized rows are rewritten. Zero means no cap.
	// A capped run sets Truncated so a caller can tell "done" from "more left".
	Max int
	// DryRun reads and measures without writing. The reported byte counts are
	// exactly what a real run would free, because the clamp is computed either
	// way — only the UPDATE is skipped.
	DryRun bool
}

// oversizedSummarySelect finds rows whose summary exceeds the cap, in id order
// so the pass is resumable by cursor.
//
// The predicate casts to BLOB on purpose. SQLite's length() returns CHARACTERS
// for a TEXT value and BYTES only for a BLOB, while clampActivitySummary bounds
// len(s) — Go bytes. Comparing characters against a byte budget silently
// disagrees on every row containing a multi-byte rune: a summary of 8,192
// three-byte runes is 24 KiB on disk but length()==8192, so an uncast predicate
// would skip precisely the rows most worth clamping. It also makes the pass
// non-idempotent in the other direction, re-selecting rows the clamp already
// shortened to exactly the cap in bytes.
const oversizedSummarySelect = `SELECT id, summary FROM activity
	WHERE id > ? AND length(CAST(summary AS BLOB)) > ?
	ORDER BY id LIMIT ?`

// ClampOversizedSummaries rewrites every activity row whose summary exceeds
// activitySummaryMax, applying the same clamp the write path applies.
//
// It is safe to run against a live database: each batch is its own transaction,
// rows are addressed by primary key, and the clamp is idempotent, so a row that
// a concurrent writer clamps first is simply found already-short and skipped.
// Cancelling ctx stops at a batch boundary and returns what was committed —
// committed work is never rolled back, so an interrupted run is progress, not
// waste, and re-running resumes rather than redoing.
func (s *SQLActivityStore) ClampOversizedSummaries(ctx context.Context, opts ClampSummariesOptions) (ClampSummariesResult, error) {
	var res ClampSummariesResult
	var cursor int64

	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		limit := sqlActClampBatch
		if opts.Max > 0 {
			remaining := opts.Max - res.Clamped
			if remaining <= 0 {
				res.Truncated = true
				return res, nil
			}
			if remaining < limit {
				limit = remaining
			}
		}

		batch, next, err := s.readOversizedSummaries(ctx, cursor, limit)
		if err != nil {
			return res, err
		}
		if len(batch) == 0 {
			return res, nil // ran out of oversized rows: the pass is complete
		}
		cursor = next
		res.Scanned += len(batch)

		if err := s.clampSummaryBatch(ctx, batch, opts.DryRun, &res); err != nil {
			return res, err
		}
	}
}

// clampRow is one oversized row awaiting rewrite.
type clampRow struct {
	id      int64
	summary string
}

// readOversizedSummaries returns up to limit oversized rows past cursor, plus
// the new cursor. Reads go to the reader pool so they do not contend with the
// single writer.
func (s *SQLActivityStore) readOversizedSummaries(ctx context.Context, cursor int64, limit int) ([]clampRow, int64, error) {
	rows, err := s.reader.QueryContext(ctx,
		s.dialect.rebind(oversizedSummarySelect), cursor, activitySummaryMax, limit)
	if err != nil {
		return nil, cursor, fmt.Errorf("sql_activity: select oversized summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []clampRow
	for rows.Next() {
		var r clampRow
		if err := rows.Scan(&r.id, &r.summary); err != nil {
			return nil, cursor, fmt.Errorf("sql_activity: scan oversized summary: %w", err)
		}
		out = append(out, r)
		cursor = r.id
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, fmt.Errorf("sql_activity: iterate oversized summaries: %w", err)
	}
	return out, cursor, nil
}

// clampSummaryBatch rewrites one batch inside a single transaction and folds the
// byte accounting into res.
func (s *SQLActivityStore) clampSummaryBatch(ctx context.Context, batch []clampRow, dryRun bool, res *ClampSummariesResult) error {
	var tx *sql.Tx
	var err error
	if !dryRun {
		tx, err = s.writer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sql_activity: begin clamp tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }() // no-op once committed
	}

	for _, r := range batch {
		clamped := clampActivitySummary(r.summary)
		if clamped == r.summary {
			// Already within the cap in bytes. Reachable despite the SQL
			// predicate: a concurrent writer may have clamped this row between
			// the read and now.
			continue
		}
		if !dryRun {
			if _, err := tx.ExecContext(ctx,
				s.dialect.rebind(`UPDATE activity SET summary = ? WHERE id = ?`),
				clamped, r.id); err != nil {
				return fmt.Errorf("sql_activity: clamp summary id=%d: %w", r.id, err)
			}
		}
		res.Clamped++
		res.BytesBefore += int64(len(r.summary))
		res.BytesAfter += int64(len(clamped))
	}

	if dryRun {
		return nil
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql_activity: commit clamp batch: %w", err)
	}
	return nil
}

// ActivitySummaryClamper is the retroactive-clamp surface. Only the SQLite store
// implements it: the clamp exists because SQLite stores `summary` as plain TEXT,
// whereas the Pebble activity keyspace is block-compressed and never grew the
// pathological footprint this repairs.
type ActivitySummaryClamper interface {
	ClampOversizedSummaries(ctx context.Context, opts ClampSummariesOptions) (ClampSummariesResult, error)
	VacuumActivity(ctx context.Context) (time.Duration, error)
}

// FindSummaryClamper digs the SQLite store out from behind whatever wrappers the
// activity wiring put in front of it, returning false when the active backend
// has no SQLite side at all (ActivityBackend=pebble).
//
// A type assertion on the outermost value is not enough: in the normal
// configuration the caller holds a *MigratingActivityStore, which forwards the
// ActivityStorer methods but deliberately does not forward this one — clamping
// is a repair of one specific backend's storage representation, not an activity
// operation that should fan out to every backend.
func FindSummaryClamper(s ActivityStorer) (ActivitySummaryClamper, bool) {
	switch v := s.(type) {
	case ActivitySummaryClamper:
		return v, true
	case *MigratingActivityStore:
		if c, ok := FindSummaryClamper(v.Primary()); ok {
			return c, true
		}
		return FindSummaryClamper(v.Secondary())
	}
	return nil, false
}

// VacuumActivity reclaims the file space a clamp pass freed.
//
// Clamping shortens values but leaves the pages allocated, so the .sqlite file
// does not shrink until this runs — reporting "reclaimed 5 GB" without it would
// be false from the user's point of view, since df would not move.
//
// VACUUM cannot run inside a transaction and rewrites the whole database, so it
// is deliberately a separate call rather than part of the pass: it needs a
// window where its cost (roughly the file size in temp space and IO) is
// acceptable. It is issued on the writer connection because it takes an
// exclusive lock.
func (s *SQLActivityStore) VacuumActivity(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if _, err := s.writer.ExecContext(ctx, `VACUUM`); err != nil {
		return time.Since(start), fmt.Errorf("sql_activity: vacuum: %w", err)
	}

	// VACUUM alone does not finish the job in WAL mode: it writes the rebuilt
	// database THROUGH the WAL, so the bytes it "freed" are still occupying the
	// -wal file afterwards. SQLite's automatic checkpoint is PASSIVE, which
	// recycles the WAL in place at its high-water mark and never shrinks the
	// file — so without an explicit TRUNCATE the space is never returned and no
	// amount of waiting fixes it.
	//
	// Measured on prod 2026-09-08, first run of this pass: the main file fell
	// 22,898,438,144 → 11,447,480,320 (10.66 GB freed) while the WAL sat at
	// 11,514,065,152 and did not move for the next three minutes. Net reclaim
	// was therefore approximately zero until the service restarted. Same reason
	// WipeAllActivity ends with this pragma.
	//
	// Failure here is not fatal to the vacuum, which has already committed:
	// report it so the caller can say the space is still held, rather than
	// discarding a successful multi-GB rebuild. "Still held" means held for
	// now, not stranded: once any checkpoint (this one's last PASSIVE, or the
	// background loop's next tick) has copied every frame, the first commit
	// that then finds no reader on the WAL restarts it from frame 1 and,
	// because journal_size_limit is set (sqlActJournalSizeLimit, 64 MiB), cuts
	// the -wal file down to that size. The error names which of the reasons in
	// walNotResetReason stopped the reset.
	if err := s.truncateWALAfterVacuum(ctx); err != nil {
		return time.Since(start), fmt.Errorf("sql_activity: vacuum succeeded but WAL truncate failed (space still held): %w", err)
	}
	return time.Since(start), nil
}

// The post-vacuum truncate has two retry budgets, because the two ways an
// attempt can fail have different causes and need different waits.
//
//   - NOT READY (vacuumTruncateAttempts, exponential backoff from 10 ms up to
//     vacuumTruncateMaxBackoff): no TRUNCATE was issued because the PASSIVEs
//     could not get the WAL fully and recently copied — a reader's snapshot
//     held PASSIVE short (walReasonReaderHeld), the checkpoint lock was taken
//     (walReasonCheckpointBusy), or writes outpaced the copy
//     (walReasonDidNotConverge). These clear on the scale of a reader's
//     lifetime, so the wait grows. One attempt is one run of passiveUntilShort:
//     a single PASSIVE when it comes back incomplete or busy, up to
//     vacuumPassiveConvergeRounds of them when each is complete but long. The
//     backoff alone sums to 10+20+40+80+160+320+500 ms = 1.13 s across 8
//     attempts; the PASSIVE copies come on top of that.
//   - BUSY TRUNCATE (vacuumTruncateBusyAttempts, a fixed
//     vacuumTruncateBusyRetryPause): a TRUNCATE was issued after a converged
//     PASSIVE, copied every frame, and could not reset the file because a
//     Record held the write lock or a reader was on the WAL past its own
//     vacuumTruncateResetWaitMS wait. That clears on the scale of one Record,
//     so it is retried promptly.
//
// They used to share one budget. Under a steady stream of Records (one every
// millisecond in TestVacuumActivity_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy)
// a reader held for 100 ms used four of the eight attempts on backoff, and
// with a 0 ms busy wait each converged TRUNCATE had to land in a gap between
// two Records; the four left were often not enough, and the vacuum reported
// the space still held with the WAL fully copied (15 of 135 loaded runs).
//
// Giving up does not strand the space. The background checkpointer's idle tick
// (checkpointAndMaybeTruncate) runs the same PASSIVE-then-TRUNCATE sequence
// (TestVacuumActivity_IdleTickReclaimsWhatAGivenUpTruncateLeft), and even
// without it, once every frame is copied the first commit that finds no reader
// on the WAL restarts it and cuts the file to journal_size_limit (64 MiB).
//
// Vars so a test can shorten them.
var (
	vacuumTruncateAttempts   = 8
	vacuumTruncateMaxBackoff = 500 * time.Millisecond

	vacuumTruncateBusyAttempts   = 8
	vacuumTruncateBusyRetryPause = 10 * time.Millisecond

	// vacuumTruncateResetWaitMS is the busy_timeout the post-vacuum TRUNCATE,
	// and only it, runs with (truncateWithResetWait). The checkpoint
	// connection's resting value stays 0 (sqlActCkptBusyTimeoutMS).
	//
	// WHY A WAIT AT ALL, given that the 0 resting value exists so a TRUNCATE
	// never stalls writers: SQLite's TRUNCATE spends its busy handler in two
	// places, and only one of them stalls writers.
	//
	//  1. Acquiring the WAL write lock. While it waits here it holds only the
	//     checkpoint lock, which no Record needs, so Records keep committing.
	//     With 0 it gave up the moment a Record was mid-commit, degraded to a
	//     PASSIVE and reported busy=1 with every frame copied: the
	//     {Busy:1 Log:2153 Checkpointed:2153} rows that failed the test above.
	//     A Record's commit is about a millisecond, so a short wait outlasts it.
	//  2. Waiting, WHILE HOLDING the write lock, for readers still on the WAL to
	//     finish, before it resets the file. Every Record queues behind this.
	//     This is the stall the 0 resting value was introduced against: at
	//     1000 ms a late reader made each attempt hold the lock for a full
	//     second and Records stalled 2.5 s.
	//
	// One busy handler covers both, and its budget is per statement, so 25 ms
	// bounds the second wait, and therefore the extra time any Record can be
	// held behind one TRUNCATE, to 25 ms on top of the copy (which convergence
	// already bounds to the frames written during one short PASSIVE plus those
	// committed during this wait). Records' own busy_timeout is 10,000 ms, 400
	// times that, so none fails. SQLite's default handler polls at 1, 2, 5, 10
	// ms intervals, so 25 ms is five chances for a 1 ms Record to finish.
	vacuumTruncateResetWaitMS = 25

	// vacuumTruncateMaxLeftoverFrames bounds the frames the TRUNCATE may have
	// to copy under the WAL write lock: it is issued only after a complete
	// PASSIVE that itself had at most this many frames left to copy (see
	// passiveNewFrames). 256 frames is about 1 MiB at the 4 KiB page size.
	vacuumTruncateMaxLeftoverFrames = 256
	// vacuumPassiveConvergeRounds caps the back-to-back PASSIVEs one attempt
	// runs waiting for one to come in under vacuumTruncateMaxLeftoverFrames.
	// Hitting it means writes outpace the copy; the attempt is then not ready
	// (walReasonDidNotConverge, no TRUNCATE) and retried after the backoff.
	vacuumPassiveConvergeRounds = 32
)

// walNotResetReason says why an attempt to reset the WAL did not succeed. It is
// carried into the error truncateWALAfterVacuum returns and the log line, so a
// "space still held" report names its actual cause instead of always blaming
// a reader.
type walNotResetReason string

const (
	// walReasonReaderHeld: a PASSIVE came back incomplete (Checkpointed < Log):
	// a reader's snapshot predates frames it would have to overwrite.
	walReasonReaderHeld walNotResetReason = "reader-held"
	// walReasonCheckpointBusy: a PASSIVE returned busy=1 with log and
	// checkpointed of -1. SQLite's checkpoint fills in the counts only after
	// it has taken the checkpoint lock and read the WAL index header, so this
	// row means one of those was busy: another connection was checkpointing,
	// or a commit was updating the header and SQLite could not get a stable
	// read of it. It copied nothing and its counts say nothing about the WAL.
	walReasonCheckpointBusy walNotResetReason = "checkpoint-busy"
	// walReasonDidNotConverge: every PASSIVE in vacuumPassiveConvergeRounds was
	// complete but had more than vacuumTruncateMaxLeftoverFrames new frames,
	// so writes were outpacing the copy and no TRUNCATE was issued. The last
	// counts look complete; the WAL was still not reset.
	walReasonDidNotConverge walNotResetReason = "did-not-converge"
	// walReasonBusyTruncate: a TRUNCATE was issued after a converged PASSIVE,
	// copied every frame (busy=1, log == checkpointed) and could not reset the
	// file within vacuumTruncateResetWaitMS, because a writer kept the write
	// lock or a reader stayed on the WAL.
	walReasonBusyTruncate walNotResetReason = "busy-truncate"
)

// describe is the human-readable cause used in the error and the log.
func (r walNotResetReason) describe() string {
	switch r {
	case walReasonReaderHeld:
		return "a reader's snapshot kept PASSIVE from copying every frame, so no TRUNCATE was issued"
	case walReasonCheckpointBusy:
		return "PASSIVE could not take the checkpoint lock or read the WAL index header, so it copied nothing " +
			"(its counts are -1)"
	case walReasonDidNotConverge:
		return fmt.Sprintf("writes outpaced the copy: no PASSIVE in %d rounds had at most %d new frames, "+
			"so no TRUNCATE was issued (the counts below are from a complete PASSIVE, but the WAL was not reset)",
			vacuumPassiveConvergeRounds, vacuumTruncateMaxLeftoverFrames)
	case walReasonBusyTruncate:
		return fmt.Sprintf("TRUNCATE copied every frame but could not reset the WAL within its %d ms wait: "+
			"a writer kept the write lock or a reader stayed on the WAL", vacuumTruncateResetWaitMS)
	}
	return string(r)
}

// walGeneration identifies one generation of the -wal file: SQLite bumps the
// checkpoint sequence number and salt-1 in the WAL header (bytes 12-19) every
// time it restarts the log from frame 1, and writes the new header with the
// first frame of the restarted log.
type walGeneration struct {
	ckptSeq, salt1, salt2 uint32
}

// readWALGeneration reads the -wal header. ok is false when the file is
// missing, short, or does not start with a WAL magic number; callers treat
// that as "a restart may have happened", the safe direction.
//
// Reading the -wal on a separate descriptor is safe: SQLite takes no POSIX
// locks on the -wal (WAL locks live in the -shm), so closing this descriptor
// cannot drop a lock SQLite holds. A read that races a restart can return a
// torn header; that reads as a different generation, which again only makes
// the caller count more frames as new.
func readWALGeneration(walPath string) (walGeneration, bool) {
	f, err := os.Open(walPath)
	if err != nil {
		return walGeneration{}, false
	}
	var hdr [24]byte
	_, rerr := io.ReadFull(f, hdr[:])
	if cerr := f.Close(); rerr != nil || cerr != nil {
		return walGeneration{}, false
	}
	if magic := binary.BigEndian.Uint32(hdr[0:4]); magic != 0x377f0682 && magic != 0x377f0683 {
		return walGeneration{}, false
	}
	return walGeneration{
		ckptSeq: binary.BigEndian.Uint32(hdr[12:16]),
		salt1:   binary.BigEndian.Uint32(hdr[16:20]),
		salt2:   binary.BigEndian.Uint32(hdr[20:24]),
	}, true
}

// passiveTracker carries what passiveNewFrames needs from one PASSIVE to the
// next, across attempts: the previous PASSIVE's Checkpointed and the WAL
// generation read just BEFORE that PASSIVE started.
type passiveTracker struct {
	walPath          string
	have             bool // a previous PASSIVE with counts exists
	prevCheckpointed int
	prevGen          walGeneration
	prevGenOK        bool
}

// passiveNewFrames is how many frames a checkpoint had to copy that the
// previous one in the same sequence had not: the frames written between the
// start of the previous checkpoint and the start of this one. prevCheckpointed
// is the previous result's Checkpointed (0 for the first in a sequence, so the
// first PASSIVE always counts the whole WAL).
//
// If the WAL was restarted from frame 1 in between, every frame in it is new.
// restarted says so when the WAL header's generation changed anywhere between
// just before the previous PASSIVE and just after this one (passiveTracker), or
// could not be read; that window covers every point a restart could make
// Log - prevCheckpointed wrong, and every uncertainty in it resolves to
// counting all of Log. Log below prevCheckpointed is also a restart, kept as a
// second signal. Overcounting only costs another PASSIVE round, since after a
// restart Log is the true number of new frames; undercounting would let the
// TRUNCATE copy a long PASSIVE's worth of writes under the write lock.
func passiveNewFrames(prevCheckpointed int, res walCheckpointResult, restarted bool) int {
	if restarted || res.Log < prevCheckpointed {
		return res.Log
	}
	return res.Log - prevCheckpointed
}

// truncateWALAfterVacuum empties the -wal VACUUM just filled: a PASSIVE
// checkpoint copies the frames, and wal_checkpoint(TRUNCATE) resets the file
// once nothing is left to copy, retrying until both succeed or a budget runs
// out (see vacuumTruncateAttempts and vacuumTruncateBusyAttempts).
//
// TRUNCATE ONLY AFTER A COMPLETE PASSIVE. TRUNCATE holds the WAL write lock for
// the whole time it copies frames into the database file, and while it waits
// for readers. After a VACUUM the frames are the entire rebuilt database (11 GB
// on prod on 2026-09-08), and every foreground Record waiting on that lock
// gives up after its busy_timeout(10000) with SQLITE_BUSY: a review probe on a
// 66 MB WAL took 33.8 s to copy and failed 3 Records. PASSIVE copies the same
// frames without the write lock, so writers keep committing. It stops short
// while a reader still holds an older snapshot; issuing TRUNCATE then would copy
// the rest UNDER the lock (measured: ~16k frames when the reader let go 300 ms
// in, and a 2.78 s Record when it never did). So TRUNCATE is issued only when
// the PASSIVE before it reports complete().
//
// AND ONLY AFTER A SHORT ONE. complete() means every frame that was in the WAL
// when that PASSIVE STARTED is copied; the frames Records commit while it runs
// are not, and the TRUNCATE copies them under the lock. After a long copy that
// is a lot: on a loaded CI runner (run 37453234558) a complete 6.13 s PASSIVE
// was followed by a 3.79 s TRUNCATE and a 3.84 s Record behind it, and on prod
// an 11 GB PASSIVE would leave minutes of writes. So each attempt repeats
// PASSIVE back to back until one is complete and had at most
// vacuumTruncateMaxLeftoverFrames frames of its own to copy (passiveNewFrames).
// Such a PASSIVE was short, so the frames written during it, which are most of
// what the TRUNCATE copies, are few. If vacuumPassiveConvergeRounds pass
// without that, writes are outpacing the copy: the attempt logs the counts,
// issues no TRUNCATE, and is retried after the backoff like any other
// not-ready attempt. The background loop's checkpointAndMaybeTruncate needs no
// such loop: it truncates only on an idle tick, when no Record committed since
// the last one.
//
// A busy TRUNCATE is not an SQL error. It returns an ordinary row with busy=1
// and leaves the -wal file as it was. When it could not take the write lock it
// carries on as a PASSIVE, so the row has real counts and usually
// log == checkpointed (every frame copied, file not reset; observed
// {Busy:1 Log:1880 Checkpointed:1880}); only when it could not take the
// checkpoint lock or read the WAL index header are log and checkpointed -1. This used to go through
// ExecContext, which discards that row, so a TRUNCATE that lost the race with
// the background checkpointer was reported as success with the WAL still
// holding the space VACUUM freed. Every checkpoint here runs on the checkpoint
// connection, the single-connection handle the background loop also uses, so
// the two queue behind each other instead of colliding.
//
// ctx is checked between attempts and interrupts a checkpoint's copy loop. It
// does not cut short the TRUNCATE's busy-handler wait, which is at most
// vacuumTruncateResetWaitMS; a PASSIVE never waits in the busy handler.
func (s *SQLActivityStore) truncateWALAfterVacuum(ctx context.Context) error {
	tr := passiveTracker{walPath: s.path + "-wal"}
	backoff := 10 * time.Millisecond
	var res walCheckpointResult
	var reason walNotResetReason
	notReady, busyTruncates := 0, 0
	for {
		out, err := s.passiveUntilShort(ctx, &tr)
		if err != nil {
			return fmt.Errorf("passive checkpoint before truncate: %w", err)
		}
		if out.notWAL {
			return nil // not in WAL mode: there is no -wal to reset
		}
		res = out.res
		var wait time.Duration
		if out.converged {
			res, err = s.truncateWithResetWait(ctx)
			if err != nil {
				return err
			}
			if res.complete() {
				return nil
			}
			reason = walReasonBusyTruncate
			busyTruncates++
			if busyTruncates >= vacuumTruncateBusyAttempts {
				break
			}
			wait = vacuumTruncateBusyRetryPause
		} else {
			reason = out.reason
			notReady++
			if notReady >= vacuumTruncateAttempts {
				break
			}
			wait = backoff
			backoff = min(2*backoff, vacuumTruncateMaxBackoff)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("WAL not yet reset (%s; last checkpoint busy=%d wal_frames=%d checkpointed=%d) "+
				"when cancelled: %w", reason, res.Busy, res.Log, res.Checkpointed, ctx.Err())
		case <-timer.C:
		}
	}
	ckptLog.Warn("post-vacuum WAL reset of %s gave up (%s) after %d not-ready attempts and %d busy TRUNCATEs "+
		"(last checkpoint busy=%d wal_frames=%d checkpointed=%d); the background checkpointer retries on its "+
		"next idle tick, and the first commit after a complete checkpoint that finds no reader on the WAL cuts "+
		"the -wal to journal_size_limit (%d bytes)", s.path, reason, notReady, busyTruncates,
		res.Busy, res.Log, res.Checkpointed, sqlActJournalSizeLimit)
	return fmt.Errorf("WAL not reset (%s): %s; %d of %d not-ready attempts, %d of %d busy TRUNCATEs "+
		"(last checkpoint busy=%d wal_frames=%d checkpointed=%d)",
		reason, reason.describe(), notReady, vacuumTruncateAttempts, busyTruncates, vacuumTruncateBusyAttempts,
		res.Busy, res.Log, res.Checkpointed)
}

// truncateWithResetWait runs the post-vacuum TRUNCATE with a busy_timeout of
// vacuumTruncateResetWaitMS (see its comment for why that bound keeps writers
// moving) instead of the checkpoint connection's resting 0. It pins the
// checkpoint handle's one connection for the three statements so nothing else
// can run on it while the wait is raised, and restores the resting value before
// releasing it. If the restore fails the connection is discarded rather than
// returned to the pool: the background loop's TRUNCATE must never inherit the
// wait.
func (s *SQLActivityStore) truncateWithResetWait(ctx context.Context) (walCheckpointResult, error) {
	conn, err := s.ckpt.Conn(ctx)
	if err != nil {
		return walCheckpointResult{}, fmt.Errorf("pin checkpoint connection for truncate: %w", err)
	}
	defer func() {
		// context.Background, not ctx: a cancelled ctx must still restore.
		_, rerr := conn.ExecContext(context.Background(),
			fmt.Sprintf("PRAGMA busy_timeout = %d", sqlActCkptBusyTimeoutMS))
		if rerr != nil {
			// Returning driver.ErrBadConn from Raw makes database/sql close the
			// driver connection instead of pooling it, so the next checkpoint
			// opens a fresh one at the DSN's busy_timeout(0). Raw hands that
			// same ErrBadConn back; anything else means the discard failed.
			// Logged, not returned: the TRUNCATE's own result is still true,
			// and reporting a reset WAL as "space still held" would be false.
			if derr := conn.Raw(func(any) error { return driver.ErrBadConn }); derr != nil &&
				!errors.Is(derr, driver.ErrBadConn) {
				ckptLog.Error("could not restore busy_timeout on the checkpoint connection of %s (%v) nor discard "+
					"it (%v): background TRUNCATEs may wait up to %d ms for readers", s.path, rerr, derr,
					vacuumTruncateResetWaitMS)
			} else {
				ckptLog.Warn("could not restore busy_timeout on the checkpoint connection of %s (%v); "+
					"discarded it", s.path, rerr)
			}
		}
		// After a discard the Conn is already closed and Close reports
		// ErrConnDone; that is the expected outcome, not a failure.
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, sql.ErrConnDone) {
			ckptLog.Warn("release of the checkpoint connection of %s after truncate failed: %v", s.path, cerr)
		}
	}()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", vacuumTruncateResetWaitMS)); err != nil {
		return walCheckpointResult{}, fmt.Errorf("set truncate busy_timeout: %w", err)
	}
	return s.walCheckpointOn(ctx, conn, "TRUNCATE")
}

// passiveOutcome is what one run of passiveUntilShort found.
type passiveOutcome struct {
	res walCheckpointResult // the last PASSIVE's result
	// converged: the last PASSIVE was complete and had at most
	// vacuumTruncateMaxLeftoverFrames new frames, so a TRUNCATE may follow.
	converged bool
	// notWAL: the database is not in WAL mode (busy=0, log=checkpointed=-1);
	// there is nothing to truncate.
	notWAL bool
	// reason is why it did not converge (unset when converged or notWAL).
	reason walNotResetReason
}

// classifyPassive maps one PASSIVE result and its new-frame count to what the
// round loop does next: stop with an outcome, or (done=false) run another round.
func classifyPassive(res walCheckpointResult, newFrames int) (out passiveOutcome, done bool) {
	out.res = res
	switch {
	case res.Busy == 0 && res.Log < 0:
		// (0,-1,-1) is what wal_checkpoint returns for a database that is not
		// in WAL mode. It was treated as complete before the convergence loop
		// existed and must still be: retrying it can never change it.
		out.notWAL = true
		return out, true
	case res.Log < 0:
		// (1,-1,-1): SQLite could not take the checkpoint lock or read the WAL
		// index header, so this PASSIVE neither copied nor counted anything.
		out.reason = walReasonCheckpointBusy
		return out, true
	case !res.complete():
		out.reason = walReasonReaderHeld
		return out, true
	case newFrames <= vacuumTruncateMaxLeftoverFrames:
		out.converged = true
		return out, true
	}
	return out, false
}

// passiveUntilShort runs PASSIVE checkpoints back to back until one is complete
// and had at most vacuumTruncateMaxLeftoverFrames new frames to copy, which is
// when a TRUNCATE may follow it (converged). It stops early, not converged, at
// the first PASSIVE that is not complete (walReasonReaderHeld) or could not
// take the checkpoint lock (walReasonCheckpointBusy): only the caller's backoff
// helps with those. It also stops, not converged, after
// vacuumPassiveConvergeRounds (walReasonDidNotConverge). tr carries the previous
// PASSIVE's Checkpointed and WAL generation across calls, so the first PASSIVE
// of a retry is measured against the last one of the attempt before.
func (s *SQLActivityStore) passiveUntilShort(ctx context.Context, tr *passiveTracker) (passiveOutcome, error) {
	var res walCheckpointResult
	for round := 1; round <= vacuumPassiveConvergeRounds; round++ {
		if err := ctx.Err(); err != nil {
			return passiveOutcome{res: res}, err
		}
		preGen, preOK := readWALGeneration(tr.walPath)
		var err error
		res, err = s.walCheckpoint(ctx, "PASSIVE")
		if err != nil {
			return passiveOutcome{res: res}, err
		}
		newFrames := res.Log
		if res.Log >= 0 {
			postGen, postOK := readWALGeneration(tr.walPath)
			if tr.have {
				restarted := !tr.prevGenOK || !postOK || tr.prevGen != postGen
				newFrames = passiveNewFrames(tr.prevCheckpointed, res, restarted)
			}
			tr.have, tr.prevCheckpointed, tr.prevGen, tr.prevGenOK = true, res.Checkpointed, preGen, preOK
		}
		if out, done := classifyPassive(res, newFrames); done {
			return out, nil
		}
	}
	ckptLog.Warn("post-vacuum PASSIVE of %s did not converge in %d rounds (last: wal_frames=%d checkpointed=%d); "+
		"writes are outpacing the copy, so no TRUNCATE this attempt", s.path, vacuumPassiveConvergeRounds,
		res.Log, res.Checkpointed)
	return passiveOutcome{res: res, reason: walReasonDidNotConverge}, nil
}
