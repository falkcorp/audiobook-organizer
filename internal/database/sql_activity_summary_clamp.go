// file: internal/database/sql_activity_summary_clamp.go
// version: 1.0.0
// guid: 3f1d8a24-6c05-4b9e-8d72-51ac07e4b6f3
// last-edited: 2026-09-08

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
	"fmt"
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
	// discarding a successful multi-GB rebuild.
	if _, err := s.writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return time.Since(start), fmt.Errorf("sql_activity: vacuum succeeded but WAL truncate failed (space still held): %w", err)
	}
	return time.Since(start), nil
}
