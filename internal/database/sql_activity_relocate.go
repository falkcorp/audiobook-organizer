// file: internal/database/sql_activity_relocate.go
// version: 1.1.0
// guid: 4d9a1e83-7b25-4c06-9f18-3e6c0a72b5d1
// last-edited: 2026-09-07

// Package database — relocation of the SQLite activity database between paths.
//
// WHY THIS IS NOT os.Rename: the activity DB is a live, multi-gigabyte SQLite
// database (29 GB on production as of 2026-09-07) whose configured location can
// change — the operator edits it in Settings, or the default moves. The naive
// move loses data in three distinct ways, all of which this file exists to avoid:
//
//  1. A raw `cp` of the `.sqlite` while a `-wal` holds committed frames — the
//     state any killed process leaves behind — discards them; on a crashed
//     snapshot the main file can lack even the schema. Carrying the `-wal` across
//     instead pairs it with a database it may not match, which is a corruption
//     path. The correct sequence is open → CHECKPOINT(TRUNCATE) → close → copy the
//     single `.sqlite` → let SQLite recreate the sidecars. (Note that closing the
//     last connection also checkpoints; see checkpointAndCount for what the
//     explicit call adds beyond that, which is the busy detection.)
//  2. The source and destination are frequently on DIFFERENT filesystems (on
//     production `/var/lib` → `/mnt/bigdata`), so os.Rename fails with EXDEV and
//     the copy has to be real.
//  3. A copy that is verified by size, or not verified at all, cannot tell a
//     complete database from a truncated one. Verification here reopens the
//     destination and compares an actual row COUNT against a count taken before
//     the move.
//
// The source is NEVER unlinked until the destination has been reopened and its
// row count matched. Every failure path leaves the source untouched, because
// losing the activity log is strictly worse than failing to relocate it.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// relocateProgressEvery bounds how often a long copy emits a progress line. A
// 29 GB copy across filesystems takes minutes; without this it is indistinguishable
// from a hang, which is the same defect the Pebble→SQLite backfill had.
const relocateProgressEvery = 15 * time.Second

// relocateCopyBuffer is the copy chunk. 4 MiB is large enough that per-call
// overhead is irrelevant on a multi-GB file and small enough to keep the
// heartbeat responsive.
const relocateCopyBuffer = 4 << 20

// relocateBusyTimeoutMS is how long a checkpoint waits for other connections to
// clear before reporting busy. Generous on purpose — a relocation is rare and a
// spurious refusal is more annoying than a slow start. A variable, not a constant,
// so tests can exercise the busy path without waiting it out.
var relocateBusyTimeoutMS = 30000

// ErrRelocateSourceMissing means there is nothing at the source path to move —
// the caller should simply open the destination and carry on.
var ErrRelocateSourceMissing = errors.New("activity-relocate: no database at source path")

// ActivitySQLPathKey records where the SQLite activity database was last opened.
//
// The configured path says where the database SHOULD be; only this says where it
// actually IS. Without it a boot that finds a new configured path cannot tell
// "the operator moved it, bring the data along" from "this is simply the first
// boot at this path" — and guessing wrong either strands the history or hunts for
// a file that was never there. It lives in Pebble because Pebble is already open
// before the activity store is built.
const ActivitySQLPathKey = "system:activity:sqlite_path"

// LastActivityDBPath returns the path the activity database was last opened at,
// and whether such a record exists. Absent on first boot, and on any install
// predating the record.
func (s *PebbleActivityStore) LastActivityDBPath() (string, bool) {
	v, closer, err := s.db.Get([]byte(ActivitySQLPathKey))
	if err != nil {
		return "", false
	}
	defer func() { _ = closer.Close() }()
	return string(v), len(v) > 0
}

// SetLastActivityDBPath records where the activity database is now open.
//
// Written with pebble.Sync: if this write were lost to a crash while the file
// itself had already moved, the next boot would look for the database at the old
// path, find nothing, and silently start an empty one.
func (s *PebbleActivityStore) SetLastActivityDBPath(path string) error {
	return s.db.Set([]byte(ActivitySQLPathKey), []byte(path), pebble.Sync)
}

// RelocateStats reports what a relocation actually did, for logging and tests.
type RelocateStats struct {
	SrcPath  string        `json:"src_path"`
	DstPath  string        `json:"dst_path"`
	Bytes    int64         `json:"bytes"`
	Rows     int64         `json:"rows"`
	Duration time.Duration `json:"duration"`
}

// RelocateActivityDB copies the SQLite activity database at src to dst, verifies
// the copy by row count, and only then removes the source.
//
// It always COPIES, never renames, even when both paths are on one filesystem.
// A rename would be faster, but it destroys the source before anything has been
// verified: if the destination then turns out to be unusable there is nothing to
// fall back to. Copy-verify-unlink keeps a complete, openable database on disk at
// every instant, which for an append-only audit log is worth the extra I/O.
//
// dst must not already hold a database; a caller that wants to replace one must
// remove it deliberately. Returns ErrRelocateSourceMissing if src does not exist.
func RelocateActivityDB(ctx context.Context, src, dst string) (RelocateStats, error) {
	start := time.Now()
	st := RelocateStats{SrcPath: src, DstPath: dst}

	srcInfo, err := os.Stat(src)
	if os.IsNotExist(err) {
		return st, ErrRelocateSourceMissing
	}
	if err != nil {
		return st, fmt.Errorf("activity-relocate: stat source: %w", err)
	}

	if _, err := os.Stat(dst); err == nil {
		return st, fmt.Errorf("activity-relocate: destination %s already exists — refusing to overwrite", dst)
	} else if !os.IsNotExist(err) {
		return st, fmt.Errorf("activity-relocate: stat destination: %w", err)
	}

	// Fold the WAL back into the main file and count the rows we must find
	// again on the other side. Both happen under one open/close so the count
	// describes exactly the bytes about to be copied.
	rows, err := checkpointAndCount(src)
	if err != nil {
		return st, err
	}
	st.Rows = rows

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return st, fmt.Errorf("activity-relocate: create destination directory: %w", err)
	}

	slog.Info("[activity-relocate] copying activity database",
		"src", src, "dst", dst,
		"bytes", srcInfo.Size(), "rows", rows)

	// Copy through a .partial so an interrupted copy can never be mistaken for a
	// finished database, then rename into place (same directory, so atomic).
	partial := dst + ".partial"
	_ = os.Remove(partial)
	written, err := copyFileWithProgress(ctx, src, partial, srcInfo.Size())
	if err != nil {
		_ = os.Remove(partial)
		return st, err
	}
	st.Bytes = written

	if err := os.Rename(partial, dst); err != nil {
		_ = os.Remove(partial)
		return st, fmt.Errorf("activity-relocate: publish destination: %w", err)
	}

	// Verify by reopening and counting. A size comparison would pass on a file
	// that SQLite cannot open, and this is the last moment at which failing is
	// still free.
	dstRows, err := checkpointAndCount(dst)
	if err != nil {
		_ = os.Remove(dst)
		return st, fmt.Errorf("activity-relocate: destination unusable, source left intact: %w", err)
	}
	if dstRows != rows {
		_ = os.Remove(dst)
		return st, fmt.Errorf("activity-relocate: row count mismatch — source %d, destination %d; "+
			"destination discarded, source left intact", rows, dstRows)
	}

	// Verified. Only now is it safe to drop the source, sidecars included: the
	// checkpoint truncated the WAL, so they hold nothing the copy is missing.
	for _, p := range []string{src + "-wal", src + "-shm", src} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			// The move succeeded; a leftover source is untidy, not data loss.
			slog.Warn("[activity-relocate] could not remove source file after a verified move",
				"path", p, "err", err)
		}
	}

	st.Duration = time.Since(start)
	slog.Info("[activity-relocate] relocation complete — verified by row count",
		"src", src, "dst", dst,
		"bytes", st.Bytes, "rows", st.Rows,
		"elapsed", st.Duration.Round(time.Second).String())
	return st, nil
}

// checkpointAndCount opens the database at path, folds any WAL back into the main
// file, and returns the activity row count.
//
// On the explicit TRUNCATE, and what it is really for: closing the last connection
// to a WAL database already checkpoints it, so the fold-back is not what this call
// uniquely buys — measured, a copy taken after a plain open/close keeps every row
// even with this PRAGMA removed. What it buys is the BUSY signal. If any other
// connection still holds the database, SQLite reports busy=1 (frames may be written
// back, but the WAL cannot be reset) instead of failing, and a close-time checkpoint
// would skip silently. Relocating a database another connection is actively using is
// wrong regardless of whether this copy happened to catch every frame, so busy is
// treated as a hard refusal rather than a warning.
//
// TRUNCATE rather than PASSIVE so that, in the normal single-connection case, the
// `-wal` is emptied and the `.sqlite` is demonstrably self-contained before the copy.
func checkpointAndCount(path string) (int64, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(%d)", path, relocateBusyTimeoutMS)
	db, err := sql.Open(sqliteDialect{}.driverName(), dsn)
	if err != nil {
		return 0, fmt.Errorf("activity-relocate: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()

	// busy/blocked are reported in the result columns rather than as an error,
	// so they are checked explicitly — a checkpoint that silently did nothing
	// would leave frames behind in the WAL and lose them on copy.
	var busy, wal, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &wal, &checkpointed); err != nil {
		return 0, fmt.Errorf("activity-relocate: checkpoint %s: %w", path, err)
	}
	if busy != 0 {
		return 0, fmt.Errorf("activity-relocate: checkpoint of %s was blocked by another connection "+
			"(busy=%d) — refusing to copy a database with unmerged WAL frames", path, busy)
	}

	var rows int64
	if err := db.QueryRow("SELECT COUNT(*) FROM activity").Scan(&rows); err != nil {
		return 0, fmt.Errorf("activity-relocate: count rows in %s: %w", path, err)
	}
	return rows, nil
}

// copyFileWithProgress streams src to dst, emitting a heartbeat so a multi-minute
// copy is visibly making progress, and fsyncs before returning so the verification
// read cannot be served from a write cache that has not reached the disk.
func copyFileWithProgress(ctx context.Context, src, dst string, total int64) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("activity-relocate: open source: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("activity-relocate: create destination: %w", err)
	}
	defer func() { _ = out.Close() }()

	buf := make([]byte, relocateCopyBuffer)
	var written int64
	start := time.Now()
	lastLog, lastBytes := start, int64(0)

	for {
		// Checked per chunk rather than per byte: a cancelled shutdown should
		// abandon the copy promptly, and the caller discards the partial file.
		select {
		case <-ctx.Done():
			return written, fmt.Errorf("activity-relocate: copy cancelled after %d bytes: %w", written, ctx.Err())
		default:
		}

		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return written, fmt.Errorf("activity-relocate: write destination: %w", werr)
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return written, fmt.Errorf("activity-relocate: read source: %w", rerr)
		}

		if now := time.Now(); now.Sub(lastLog) >= relocateProgressEvery {
			window := now.Sub(lastLog).Seconds()
			var pct float64
			if total > 0 {
				pct = float64(written) / float64(total) * 100
			}
			slog.Info("[activity-relocate] copy progress",
				"copied_bytes", written, "total_bytes", total,
				"percent", int(pct),
				"mb_per_sec", int(float64(written-lastBytes)/window/(1<<20)),
				"elapsed", now.Sub(start).Round(time.Second).String())
			lastLog, lastBytes = now, written
		}
	}

	// Durability before verification: without this the reopen could read back
	// bytes that are still only in the page cache, so a verified copy would not
	// prove the data survives the power loss the move is meant to be safe against.
	if err := out.Sync(); err != nil {
		return written, fmt.Errorf("activity-relocate: fsync destination: %w", err)
	}
	return written, nil
}
