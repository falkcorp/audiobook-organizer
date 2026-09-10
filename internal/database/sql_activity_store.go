// file: internal/database/sql_activity_store.go
// version: 1.8.0
// guid: 2c9a7e14-8b30-4d6f-a1e2-5f7b9c0d3e28
// last-edited: 2026-09-10

// Package database — backend-agnostic SQL activity store.
//
// SQLActivityStore implements the full ActivityStorer interface on top of
// database/sql plus the sqlDialect seam (see sql_dialect.go). It is the
// structural fix for the activity log's two chronic problems on the Pebble
// backend:
//
//   - Compaction that loads a whole day into RAM and runs synchronously in the
//     HTTP request, so one heavy day times the browser out. Here CompactByDay is
//     bounded (aggregate COUNT + a capped item SELECT + a set-based range
//     DELETE) and never materializes a day, so it cannot time out no matter how
//     large the day is.
//   - Hand-rolled secondary indexes (act:op:, act:bk:) that leak on every
//     delete and need a nightly RepairActivityIndexes. The SQL engine maintains
//     indexes transactionally, so nothing can orphan and the repair is a no-op.
//
// CONNECTIONS: WAL mode with two handles on the same file — a single-connection
// writer (SQLite allows one writer) and a small reader pool. This is the reason
// a long CompactByDay no longer blocks the activity UI: readers see the last
// committed WAL snapshot while the writer works, and each day commits in its own
// short transaction so live Record calls wait at most one day's write.
//
// FILTER PARITY: Query/GetDistinctSources reproduce the Pebble/Nuts
// matchesFilter semantics exactly — equality on tier/type/level/source/op/book,
// case-sensitive substring on summary (instr, NOT case-insensitive LIKE),
// AND-contains on Tags, and NOT-IN/NOT-contains for the Exclude* sets.
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver (no cgo)
)

// SQLActivityStore is a database/sql-backed ActivityStorer.
type SQLActivityStore struct {
	writer  *sql.DB // one connection: SQLite permits a single writer.
	reader  *sql.DB // small pool: WAL readers run concurrently with the writer.
	dialect sqlDialect
	path    string
}

// sqlActReaderConns bounds the reader pool. WAL lets these run concurrently with
// the single writer; a handful is plenty for the activity UI's read load.
const sqlActReaderConns = 4

// sqlActCountCap bounds the exact-COUNT total Query returns. Above it the total
// is reported as the cap (a lower bound, exactly as the Pebble store returns a
// lower bound once its scan budget is hit) so a Search query — whose instr()
// predicate is unindexable — can never turn pagination into a full-table count.
const sqlActCountCap = 100000

// sqlActDeleteChunk bounds each DELETE so an unbounded wipe/prune cannot build
// one enormous WAL frame; it also makes WipeAllActivity's "rows actually
// deleted on cancel" contract real (it sums committed chunks).
const sqlActDeleteChunk = 5000

// OpenSQLiteActivityStore opens (creating if absent) a SQLite-backed activity
// store at path. journal is always WAL; synchronous is "normal" (the right
// point for a log — durable across app crashes, fsync only at checkpoint).
func OpenSQLiteActivityStore(path string) (*SQLActivityStore, error) {
	d := sqliteDialect{}

	// The default path now lives in a dot-directory under the library root, which
	// need not exist yet on a first boot or after the root moves. SQLite does not
	// create intermediate directories: without this the open fails with a bare
	// "unable to open database file" and the caller degrades to Pebble-only,
	// making a fresh install look like a broken SQLite backend.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sql_activity: create directory %s: %w", dir, err)
		}
	}

	// modernc uses the repeatable ?_pragma=name(value) DSN form. busy_timeout
	// makes a writer wait rather than fail with SQLITE_BUSY under contention.
	writerDSN := fmt.Sprintf("file:%s?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(10000)", path)
	writer, err := sql.Open(d.driverName(), writerDSN)
	if err != nil {
		return nil, fmt.Errorf("sql_activity: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)

	if _, err := writer.Exec(strings.Join(d.ddl(), ";\n")); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("sql_activity: schema: %w", err)
	}

	readerDSN := fmt.Sprintf("file:%s?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(10000)", path)
	reader, err := sql.Open(d.driverName(), readerDSN)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("sql_activity: open reader: %w", err)
	}
	reader.SetMaxOpenConns(sqlActReaderConns)

	s := &SQLActivityStore{writer: writer, reader: reader, dialect: d, path: path}

	// Read the pragmas that actually took effect: a wrong DSN silently leaves
	// the DB in DELETE journal mode / synchronous=FULL — the exact silent
	// mis-configuration this store exists to avoid — so it is verified, not
	// assumed.
	var journal string
	var syncMode int
	if err := writer.QueryRow("PRAGMA journal_mode").Scan(&journal); err == nil {
		_ = writer.QueryRow("PRAGMA synchronous").Scan(&syncMode)
		slog.Info("[activity] SQLite activity store opened",
			"path", path, "journal_mode", journal, "synchronous", syncMode)
		if !strings.EqualFold(journal, "wal") {
			slog.Warn("[activity] SQLite activity store did NOT enter WAL mode — reads and writes will contend",
				"journal_mode", journal)
		}
	}
	return s, nil
}

// ── column plumbing ─────────────────────────────────────────────────────────

const sqlActCols = `id, ts, tier, type, level, source, operation_id, book_id, summary, details, tags, pruned_at`

const sqlActInsert = `INSERT INTO activity
	(src_key, ts, tier, type, level, source, operation_id, book_id, summary, details, tags, pruned_at)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`

// sqlActInsertIgnore is the idempotent insert used by every EVENT-write path
// (live Record, RecordBatch, and the Pebble→SQLite backfill). Because src_key is
// a deterministic content hash of the entry (see activitySrcKey), presenting the
// same event twice — a live dual-write and the backfill's copy of the same
// Pebble row, or a resumed/re-run backfill — conflicts on src_key and is
// skipped, so no event-write path can ever duplicate an event. The WHERE clause
// must match the partial unique index (idx_act_srckey) for SQLite to accept
// src_key as the conflict target.
//
// The maintenance digest/summary writes (commitDayDigest, Summarize) do NOT use
// this constant: they insert via the bare sqlActInsert with a nil src_key on
// purpose. A digest is not an event and its content legitimately CHANGES across
// recompaction, so a content key would be wrong for it. Those rows are made
// idempotent instead by identity: commitDayDigest DELETEs any existing digest
// for the day before inserting the merged one (one digest per day), Summarize
// deletes its source rows in the same tx, and RecompactDigests only UPDATEs in
// place — never inserts. A nil src_key is exempt from idx_act_srckey (the index
// is partial), which is exactly why those rows are free to repeat by day-key.
const sqlActInsertIgnore = sqlActInsert + ` ON CONFLICT(src_key) WHERE src_key IS NOT NULL DO NOTHING`

// activitySrcKey derives the deterministic content key that makes activity
// writes idempotent across the Pebble→SQLite cutover. It hashes the canonical
// JSON of the entry with the two NON-identity fields zeroed:
//
//   - ID: a per-write monotonic counter Pebble stamps INSIDE prepareEntry, on a
//     value copy the dual-write path never sees. The live SQLite row (ID 0) and
//     the backfilled copy of the same event (ID = the Pebble counter) would
//     otherwise hash differently; zeroing it makes them collide and dedup.
//   - PrunedAt: a lifecycle mutation set by Prune long after the event, not part
//     of the event's identity.
//
// encoding/json marshals map keys in sorted order, so json.Marshal is
// deterministic for ActivityEntry (Details is a map; Tags preserves order), and
// the same event always yields the same key. CONSEQUENCE: two byte-identical
// events at the same nanosecond collapse into ONE row (Pebble keeps both, since
// its key carries a random ULID). That is acceptable for a display/audit log and
// is the deliberate price of key-free idempotent dual-write.
func activitySrcKey(e ActivityEntry) (string, error) {
	e.ID = 0
	e.PrunedAt = nil
	b, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("sql_activity: hash entry: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// rowArgs marshals an entry into positional insert args. srcKey is the entry's
// deterministic content key (activitySrcKey); every write path supplies it now,
// so the ON CONFLICT dedup applies uniformly.
func rowArgs(e ActivityEntry, srcKey any) ([]any, error) {
	var det, tg any
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return nil, fmt.Errorf("sql_activity: marshal details: %w", err)
		}
		// Store the details JSON compressed (see sql_activity_details_codec.go):
		// this is the only large column and raw JSON blew activity.sqlite past
		// 30 GB on prod. Stored as a tagged BLOB.
		det = encodeActivityDetails(b)
	}
	if len(e.Tags) > 0 {
		b, err := json.Marshal(e.Tags)
		if err != nil {
			return nil, fmt.Errorf("sql_activity: marshal tags: %w", err)
		}
		tg = string(b)
	}
	var op, bk any
	if e.OperationID != "" {
		op = e.OperationID
	}
	if e.BookID != "" {
		bk = e.BookID
	}
	var pruned any
	if e.PrunedAt != nil {
		pruned = e.PrunedAt.UnixNano()
	}
	return []any{srcKey, e.Timestamp.UnixNano(), e.Tier, e.Type, e.Level, e.Source, op, bk, e.Summary, det, tg, pruned}, nil
}

// scanEntries decodes a *sql.Rows selected with sqlActCols into entries.
func scanEntries(rows *sql.Rows) ([]ActivityEntry, error) {
	defer rows.Close()
	var out []ActivityEntry
	for rows.Next() {
		var (
			e        ActivityEntry
			ts       int64
			op, bk   sql.NullString
			det, tg  sql.NullString
			prunedNS sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &ts, &e.Tier, &e.Type, &e.Level, &e.Source, &op, &bk, &e.Summary, &det, &tg, &prunedNS); err != nil {
			return nil, err
		}
		e.Timestamp = time.Unix(0, ts).UTC()
		e.OperationID, e.BookID = op.String, bk.String
		if det.Valid && det.String != "" {
			raw, derr := decodeActivityDetails([]byte(det.String))
			if derr != nil {
				return nil, derr
			}
			if err := json.Unmarshal(raw, &e.Details); err != nil {
				return nil, fmt.Errorf("sql_activity: unmarshal details: %w", err)
			}
		}
		if tg.Valid && tg.String != "" {
			if err := json.Unmarshal([]byte(tg.String), &e.Tags); err != nil {
				return nil, fmt.Errorf("sql_activity: unmarshal tags: %w", err)
			}
		}
		if prunedNS.Valid {
			t := time.Unix(0, prunedNS.Int64).UTC()
			e.PrunedAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── ActivityWriter ──────────────────────────────────────────────────────────

// Record inserts one entry idempotently, keyed by its content hash, and returns
// the row's autoincrement id (0 on a dedup skip). The return is best-effort:
// every caller ignores it, and re-presenting an already-stored event is a no-op.
func (s *SQLActivityStore) Record(e ActivityEntry) (int64, error) {
	e.Summary = clampActivitySummary(e.Summary)
	key, err := activitySrcKey(e)
	if err != nil {
		return 0, err
	}
	args, err := rowArgs(e, key)
	if err != nil {
		return 0, err
	}
	res, err := s.writer.Exec(s.dialect.rebind(sqlActInsertIgnore), args...)
	if err != nil {
		return 0, fmt.Errorf("sql_activity: record: %w", err)
	}
	return res.LastInsertId()
}

// RecordBatch inserts every entry in one transaction, idempotently by content
// key. Used by the migration backfill and by high-volume writers. It is NOT part
// of ActivityStorer but is the batched analogue of Record; returns the number of
// rows actually inserted (content-conflict skips are excluded).
func (s *SQLActivityStore) RecordBatch(entries []ActivityEntry) (int, error) {
	return s.recordBatch(context.Background(), entries)
}

// recordBatch inserts entries idempotently in one transaction, each keyed by its
// content hash (activitySrcKey) via INSERT … ON CONFLICT(src_key) DO NOTHING.
// Returns rows actually inserted; a re-presented event conflicts and is skipped,
// which is exactly what makes a resumed/re-run backfill safe.
func (s *SQLActivityStore) recordBatch(ctx context.Context, entries []ActivityEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, s.dialect.rebind(sqlActInsertIgnore))
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	inserted := 0
	for i := range entries {
		// Clamp on a local copy, before activitySrcKey: the key is derived from
		// the entry, so clamping after it would make this path compute a
		// different dedup key than Record does for the same row. Copying rather
		// than writing through entries[i] keeps this free of side effects on the
		// caller's slice.
		e := entries[i]
		e.Summary = clampActivitySummary(e.Summary)
		key, kerr := activitySrcKey(e)
		if kerr != nil {
			_ = tx.Rollback()
			return 0, kerr
		}
		args, aerr := rowArgs(e, key)
		if aerr != nil {
			_ = tx.Rollback()
			return 0, aerr
		}
		res, eerr := stmt.ExecContext(ctx, args...)
		if eerr != nil {
			_ = tx.Rollback()
			return 0, eerr
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// ── filter → SQL ────────────────────────────────────────────────────────────

// buildFilter renders an ActivityFilter into a WHERE fragment (without the
// leading "WHERE") and its args, reproducing matchesFilter semantics plus the
// Since/Until range the Pebble store applies via its key bounds.
func (s *SQLActivityStore) buildFilter(f ActivityFilter) (string, []any) {
	var conds []string
	var args []any

	eq := func(col, val string) {
		if val != "" {
			conds = append(conds, col+" = ?")
			args = append(args, val)
		}
	}
	eq("tier", f.Tier)
	eq("type", f.Type)
	eq("level", f.Level)
	eq("source", f.Source)
	eq("operation_id", f.OperationID)
	eq("book_id", f.BookID)

	if f.Since != nil {
		conds = append(conds, "ts >= ?")
		args = append(args, f.Since.UnixNano())
	}
	if f.Until != nil {
		conds = append(conds, "ts < ?")
		args = append(args, f.Until.UnixNano())
	}

	// Search is a case-sensitive substring on summary. The ActivityFilter.Search
	// field doc says "LIKE %search%", but the Pebble/Nuts matchesFilter uses
	// strings.Contains (case-sensitive). instr() matches that behaviour exactly;
	// the divergence from the field comment is deliberate and preserves prod
	// results.
	if f.Search != "" {
		conds = append(conds, s.dialect.substringMatch("summary"))
		args = append(args, f.Search)
	}

	// Tags: entry must contain ALL requested tags (AND).
	for _, tag := range f.Tags {
		conds = append(conds, s.dialect.jsonArrayContains("tags"))
		args = append(args, tag)
	}

	if len(f.ExcludeSources) > 0 {
		conds = append(conds, "source NOT IN ("+placeholders(len(f.ExcludeSources))+")")
		for _, v := range f.ExcludeSources {
			args = append(args, v)
		}
	}
	if len(f.ExcludeTiers) > 0 {
		conds = append(conds, "tier NOT IN ("+placeholders(len(f.ExcludeTiers))+")")
		for _, v := range f.ExcludeTiers {
			args = append(args, v)
		}
	}
	// ExcludeTags: hide entries carrying ANY of these tags.
	for _, tag := range f.ExcludeTags {
		conds = append(conds, "NOT "+s.dialect.jsonArrayContains("tags"))
		args = append(args, tag)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return strings.Join(conds, " AND "), args
}

// ── ActivityReader ──────────────────────────────────────────────────────────

// Query returns a newest-first page plus a total. Total is exact up to
// sqlActCountCap and a lower bound above it (parity with the Pebble store's
// scan-budget lower bound).
func (s *SQLActivityStore) Query(ctx context.Context, f ActivityFilter) ([]ActivityEntry, int, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}
	where, args := s.buildFilter(f)
	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where
	}

	q := "SELECT " + sqlActCols + " FROM activity" + whereClause + " ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?"
	pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	rows, err := s.reader.QueryContext(ctx, s.dialect.rebind(q), pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("sql_activity: query page: %w", err)
	}
	page, err := scanEntries(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("sql_activity: scan page: %w", err)
	}

	// Capped exact count: COUNT over a LIMIT-bounded subquery.
	countQ := "SELECT COUNT(*) FROM (SELECT 1 FROM activity" + whereClause + " LIMIT ?)"
	countArgs := append(append([]any{}, args...), sqlActCountCap)
	var total int
	if err := s.reader.QueryRowContext(ctx, s.dialect.rebind(countQ), countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sql_activity: count: %w", err)
	}
	if total >= sqlActCountCap {
		slog.Warn("[activity] query total hit the count cap; total is a lower bound",
			"cap", sqlActCountCap, "limit", f.Limit, "offset", f.Offset, "search", f.Search)
	}
	return page, total, nil
}

// GetDistinctSources returns sources with counts, highest first. Unlike the
// Pebble store this is exact (a GROUP BY over the source index) rather than a
// budgeted newest-window sample, and needs no memoization — the engine does the
// aggregation. That is a strict improvement, not a regression.
func (s *SQLActivityStore) GetDistinctSources(ctx context.Context, f ActivityFilter) ([]SourceCount, error) {
	where, args := s.buildFilter(f)
	whereClause := ""
	if where != "" {
		whereClause = " WHERE " + where
	}
	q := "SELECT source, COUNT(*) c FROM activity" + whereClause + " GROUP BY source ORDER BY c DESC, source ASC"
	rows, err := s.reader.QueryContext(ctx, s.dialect.rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("sql_activity: distinct sources: %w", err)
	}
	defer rows.Close()
	var out []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Source, &sc.Count); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Summarize groups entries older than olderThan in tier by (day, operation_id,
// type), writes one summary row per group (PrunedAt set), and deletes the
// group's originals — mirroring PebbleActivityStore.Summarize. Returns rows
// deleted.
func (s *SQLActivityStore) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	cutoff := olderThan.UnixNano()
	rows, err := s.reader.QueryContext(ctx, s.dialect.rebind(
		`SELECT date(ts/1000000000,'unixepoch') AS d, operation_id, type, COUNT(*), MIN(ts), MAX(ts)
		 FROM activity WHERE tier = ? AND ts < ? AND pruned_at IS NULL
		 GROUP BY d, operation_id, type`), tier, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sql_activity: summarize scan: %w", err)
	}
	type grp struct {
		day, typ     string
		opID         sql.NullString
		count        int
		minTS, maxTS int64
	}
	var groups []grp
	for rows.Next() {
		var g grp
		if err := rows.Scan(&g.day, &g.opID, &g.typ, &g.count, &g.minTS, &g.maxTS); err != nil {
			rows.Close()
			return 0, err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	total := 0
	for _, g := range groups {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		dayStart, perr := time.Parse("2006-01-02", g.day)
		if perr != nil {
			return total, fmt.Errorf("sql_activity: summarize parse day %q: %w", g.day, perr)
		}
		lo := dayStart.UnixNano()
		hi := dayStart.Add(24 * time.Hour).UnixNano()
		hi = min(hi, cutoff)
		summaryText := fmt.Sprintf("Summary: %d %s entries (%s to %s)",
			g.count, g.typ,
			time.Unix(0, g.minTS).UTC().Format(time.RFC3339),
			time.Unix(0, g.maxTS).UTC().Format(time.RFC3339))

		tx, terr := s.writer.BeginTx(ctx, nil)
		if terr != nil {
			return total, terr
		}
		summary := ActivityEntry{
			Timestamp:   now,
			Tier:        tier,
			Type:        g.typ,
			Level:       "info",
			Source:      "summarize",
			OperationID: g.opID.String,
			Summary:     summaryText,
			PrunedAt:    &now,
		}
		insArgs, aerr := rowArgs(summary, nil)
		if aerr != nil {
			_ = tx.Rollback()
			return total, aerr
		}
		if _, eerr := tx.ExecContext(ctx, s.dialect.rebind(sqlActInsert), insArgs...); eerr != nil {
			_ = tx.Rollback()
			return total, eerr
		}

		delSQL := `DELETE FROM activity WHERE tier = ? AND ts >= ? AND ts < ? AND pruned_at IS NULL AND type = ?`
		delArgs := []any{tier, lo, hi, g.typ}
		if g.opID.Valid {
			delSQL += ` AND operation_id = ?`
			delArgs = append(delArgs, g.opID.String)
		} else {
			delSQL += ` AND operation_id IS NULL`
		}
		res, derr := tx.ExecContext(ctx, s.dialect.rebind(delSQL), delArgs...)
		if derr != nil {
			_ = tx.Rollback()
			return total, derr
		}
		n, _ := res.RowsAffected()
		if err := tx.Commit(); err != nil {
			return total, err
		}
		total += int(n)
	}
	return total, nil
}

// ── ActivityRetention ───────────────────────────────────────────────────────

// Prune hard-deletes every entry of tier older than olderThan. Context-free per
// the interface (scheduled maintenance only). Chunked so one prune cannot build
// an unbounded WAL frame.
func (s *SQLActivityStore) Prune(olderThan time.Time, tier string) (int, error) {
	cutoff := olderThan.UnixNano()
	deleted := 0
	for {
		res, err := s.writer.Exec(s.dialect.rebind(
			`DELETE FROM activity WHERE id IN (SELECT id FROM activity WHERE tier = ? AND ts < ? LIMIT ?)`),
			tier, cutoff, sqlActDeleteChunk)
		if err != nil {
			return deleted, fmt.Errorf("sql_activity: prune: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
		if n < sqlActDeleteChunk {
			break
		}
	}
	return deleted, nil
}

// WipeAllActivity deletes every row, chunked and ctx-aware. On cancellation it
// returns the count actually committed so far (a lower bound, never fabricated)
// alongside ctx.Err(), and a plain retry finishes the rest — satisfying the
// interface contract.
func (s *SQLActivityStore) WipeAllActivity(ctx context.Context) (int64, error) {
	var deleted int64
	for {
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		default:
		}
		res, err := s.writer.ExecContext(ctx, s.dialect.rebind(
			`DELETE FROM activity WHERE id IN (SELECT id FROM activity LIMIT ?)`), sqlActDeleteChunk)
		if err != nil {
			return deleted, fmt.Errorf("sql_activity: wipe: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += n
		if n < sqlActDeleteChunk {
			break
		}
	}
	_, _ = s.writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return deleted, nil
}

// CompactByDay collapses every compactable-tier row older than olderThan into
// one daily digest per calendar day, deleting the originals.
//
// BOUNDED IN BOTH DIMENSIONS. It iterates calendar days rather than rows, and
// each day is consumed in committed chunks of sqlActDeleteChunk rather than in
// one statement. Neither the number of days nor the number of rows in a day can
// make a single transaction large. This matters because rows-per-day is
// genuinely unbounded: measured on a copy of production 2026-09-09, the activity
// table held 4,786,930 rows for 2026-09-07 alone (2,714,387 for 09-05 and
// 4,218,635 for 09-06), because three missing-file repair ops wrote one
// change-tier row per book file. The previous whole-day DELETE was timed against
// that day on a reflink copy and did not finish: 29 minutes 05 seconds elapsed
// with a 1.20 GB WAL and still running when it was killed — inside a job whose
// budget is 30 minutes.
//
// EXACTLY-ONCE COUNTING UNDER INTERRUPTION. Each chunk folds its rows into the
// day's digest AND deletes exactly those rows in ONE transaction, keyed by the
// row ids the same transaction just read. So at every commit boundary a row is
// either still present and not yet counted, or deleted and counted once. A
// process killed mid-day (the production failure mode — see the OOM note on
// CompactActivity) leaves a partial digest that is accurate for the rows it
// covers, and a re-run resumes from the rows that remain.
//
// This ordering is deliberate and was changed on 2026-09-09. The obvious design
// — build the whole day's digest, commit it, then delete the day's rows — is
// what this function used to do, and it DOUBLE-COUNTS on resume: the digest
// claims N rows, the delete is interrupted with S survivors, and the next run
// recounts those S survivors and merges them into a digest that already
// included them, so the digest reports N+S entries for a day that only ever held
// N. Deleting first and digesting last loses the interrupted rows' counts
// outright instead. Only making the two atomic per chunk is correct, and it is
// what TestSQLCompactByDay_InterruptedDeleteDoesNotDoubleCount pins.
//
// On error or cancellation the returned CompactResult reports what was actually
// committed — never a projection — so a caller can log real progress.
func (s *SQLActivityStore) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	var result CompactResult
	cutoff := olderThan.UnixNano()

	// Bound the calendar-day loop by the actual data range, not the wall clock.
	var minNS, maxNS sql.NullInt64
	if err := s.reader.QueryRowContext(ctx, s.dialect.rebind(
		`SELECT MIN(ts), MAX(ts) FROM activity WHERE tier <> 'digest' AND ts < ?`), cutoff).
		Scan(&minNS, &maxNS); err != nil {
		return result, fmt.Errorf("sql_activity: compact range: %w", err)
	}
	if !minNS.Valid {
		return result, nil // nothing older than cutoff
	}

	firstDay := time.Unix(0, minNS.Int64).UTC().Truncate(24 * time.Hour)
	for day := firstDay; day.UnixNano() < cutoff; day = day.Add(24 * time.Hour) {
		lo := day.UnixNano()
		hi := min(day.Add(24*time.Hour).UnixNano(), cutoff)

		dayRows := 0
		for {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			default:
			}
			n, err := s.compactDayChunk(ctx, day, lo, hi)
			if err != nil {
				return result, err
			}
			if n == 0 {
				break
			}
			dayRows += n
			result.EntriesDeleted += n
			// Per chunk, not per day: a single heavy day is many chunks and
			// each one is progress the op watchdog must hear about
			// (activity_compact_progress.go).
			reportCompactProgress(ctx, "sqlite", result)
		}
		if dayRows > 0 {
			// Keep the WAL bounded across a long compaction spanning many days.
			_, _ = s.writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
			result.DaysCompacted++
		}
		reportCompactProgress(ctx, "sqlite", result)
	}
	return result, nil
}

// compactDayChunk folds at most sqlActDeleteChunk of the day's remaining rows
// into that day's digest and deletes exactly those rows, in one transaction.
// It returns the number of rows consumed; 0 means the day is finished.
//
// The delete targets the explicit row ids read at the top of the same
// transaction, not a repeat of the SELECT's predicate. That is what makes
// "counted" and "deleted" the same set by construction rather than by an
// argument about whether the window can change underneath us.
func (s *SQLActivityStore) compactDayChunk(ctx context.Context, day time.Time, lo, hi int64) (int, error) {
	dayStartNS := day.UnixNano()

	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sql_activity: compact begin chunk: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded, so this is safe to defer
	// unconditionally and removes the every-branch rollback the old code needed.
	defer func() { _ = tx.Rollback() }()

	// 1. Claim this chunk. Only id and type are read — never the summary or
	//    details blobs — so consuming a 4.8M-row day never decodes 4.8M values.
	rows, err := tx.QueryContext(ctx, s.dialect.rebind(
		`SELECT id, type FROM activity WHERE tier <> 'digest' AND ts >= ? AND ts < ?
		 ORDER BY ts ASC LIMIT ?`), lo, hi, sqlActDeleteChunk)
	if err != nil {
		return 0, fmt.Errorf("sql_activity: compact claim chunk: %w", err)
	}
	ids := make([]int64, 0, sqlActDeleteChunk)
	chunkCounts := make(map[string]int)
	for rows.Next() {
		var id int64
		var typ string
		if serr := rows.Scan(&id, &typ); serr != nil {
			rows.Close()
			return 0, fmt.Errorf("sql_activity: compact scan chunk: %w", serr)
		}
		ids = append(ids, id)
		chunkCounts[typ]++
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return 0, fmt.Errorf("sql_activity: compact read chunk: %w", rerr)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// 2. Load the day's digest if earlier chunks already created one.
	var dd DigestDetails
	var existingID int64
	haveExisting := false
	var existingDetails sql.NullString
	qerr := tx.QueryRowContext(ctx, s.dialect.rebind(
		`SELECT id, details FROM activity WHERE tier = 'digest' AND type = 'daily_digest' AND ts = ? LIMIT 1`),
		dayStartNS).Scan(&existingID, &existingDetails)
	switch {
	case qerr == sql.ErrNoRows:
		// first chunk of this day
	case qerr != nil:
		return 0, fmt.Errorf("sql_activity: compact read digest: %w", qerr)
	default:
		haveExisting = true
		if existingDetails.Valid && existingDetails.String != "" {
			raw, derr := decodeActivityDetails([]byte(existingDetails.String))
			if derr != nil {
				return 0, fmt.Errorf("sql_activity: compact decode existing digest: %w", derr)
			}
			if uerr := json.Unmarshal(raw, &dd); uerr != nil {
				return 0, fmt.Errorf("sql_activity: compact decode existing digest: %w", uerr)
			}
		}
	}
	if dd.Counts == nil {
		dd.Counts = make(map[string]int)
	}
	dd.Date = day.Format("2006-01-02")

	// 3. Sample the day's items ONCE, on the chunk that creates the digest, over
	//    the whole day window rather than this chunk. Sampling per chunk would
	//    make the sample depend on how many times the job was interrupted, and
	//    would decode rows on every chunk instead of once per day. The sample is
	//    capped at maxDigestItems and keeps the audit → error/warn → normal
	//    precedence the Pebble store uses.
	if !haveExisting {
		items, ierr := s.sampleDayItems(ctx, tx, lo, hi)
		if ierr != nil {
			return 0, ierr
		}
		dd.Items = items
	}

	// 4. Fold this chunk's counts in. Additive is correct HERE, and only here,
	//    because these rows are deleted by this same transaction and so can
	//    never be presented for counting again.
	for k, v := range chunkCounts {
		dd.Counts[k] += v
	}
	dd.OriginalCount += len(ids)
	// Truncation is derived from the totals rather than accumulated, so it stays
	// consistent no matter how many chunks the day took.
	if dd.OriginalCount > len(dd.Items) {
		dd.Truncated = true
		dd.TruncatedCount = dd.OriginalCount - len(dd.Items)
	}

	// 5. Replace the digest row with the updated one.
	if haveExisting {
		if _, derr := tx.ExecContext(ctx, s.dialect.rebind(
			`DELETE FROM activity WHERE id = ?`), existingID); derr != nil {
			return 0, fmt.Errorf("sql_activity: compact replace digest: %w", derr)
		}
	}
	detailsBytes, merr := json.Marshal(dd)
	if merr != nil {
		return 0, fmt.Errorf("sql_activity: compact marshal digest: %w", merr)
	}
	var ddMap map[string]any
	if uerr := json.Unmarshal(detailsBytes, &ddMap); uerr != nil {
		return 0, fmt.Errorf("sql_activity: compact remap digest: %w", uerr)
	}
	insArgs, aerr := rowArgs(ActivityEntry{
		Timestamp: day,
		Tier:      "digest",
		Type:      "daily_digest",
		Level:     "info",
		Source:    "compaction",
		Summary:   fmt.Sprintf("Daily digest for %s (%d entries)", dd.Date, dd.OriginalCount),
		Details:   ddMap,
	}, nil)
	if aerr != nil {
		return 0, aerr
	}
	if _, eerr := tx.ExecContext(ctx, s.dialect.rebind(sqlActInsert), insArgs...); eerr != nil {
		return 0, fmt.Errorf("sql_activity: compact write digest: %w", eerr)
	}

	// 6. Delete exactly the rows counted in step 4.
	var b strings.Builder
	b.WriteString(`DELETE FROM activity WHERE id IN (`)
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args[i] = id
	}
	b.WriteByte(')')
	if _, derr := tx.ExecContext(ctx, s.dialect.rebind(b.String()), args...); derr != nil {
		return 0, fmt.Errorf("sql_activity: compact delete chunk: %w", derr)
	}

	if cerr := tx.Commit(); cerr != nil {
		return 0, fmt.Errorf("sql_activity: compact commit chunk: %w", cerr)
	}
	return len(ids), nil
}

// sampleDayItems returns at most maxDigestItems DigestItems for the day's
// [lo,hi) window, filling audit rows first, then error/warn, then the rest.
// Each category is its own bounded LIMIT query, so the number of rows decoded is
// capped at maxDigestItems regardless of how large the day is.
func (s *SQLActivityStore) sampleDayItems(ctx context.Context, tx *sql.Tx, lo, hi int64) ([]DigestItem, error) {
	remaining := maxDigestItems
	var items []DigestItem
	appendCat := func(whereExtra string) error {
		if remaining <= 0 {
			return nil
		}
		q := "SELECT " + sqlActCols + " FROM activity WHERE tier <> 'digest' AND ts >= ? AND ts < ? AND " +
			whereExtra + " ORDER BY ts ASC LIMIT ?"
		r, qerr := tx.QueryContext(ctx, s.dialect.rebind(q), lo, hi, remaining)
		if qerr != nil {
			return qerr
		}
		entries, serr := scanEntries(r)
		if serr != nil {
			return serr
		}
		for _, e := range entries {
			item := DigestItem{
				Type:        e.Type,
				Tier:        e.Tier,
				Book:        extractBookName(e),
				BookID:      e.BookID,
				OperationID: e.OperationID,
				Summary:     extractItemSummary(e),
				Timestamp:   e.Timestamp,
				Tags:        e.Tags,
			}
			if e.Tier != "audit" && (e.Level == "error" || e.Level == "warn") {
				item.Details = extractErrorDetails(e)
			}
			items = append(items, item)
		}
		remaining -= len(entries)
		return nil
	}
	if err := appendCat("tier = 'audit'"); err != nil {
		return nil, fmt.Errorf("sql_activity: compact audit items: %w", err)
	}
	if err := appendCat("tier <> 'audit' AND level IN ('error','warn')"); err != nil {
		return nil, fmt.Errorf("sql_activity: compact err items: %w", err)
	}
	if err := appendCat("tier <> 'audit' AND level NOT IN ('error','warn')"); err != nil {
		return nil, fmt.Errorf("sql_activity: compact normal items: %w", err)
	}
	return items, nil
}

// RecompactDigests re-derives type/tier/tags on stored daily-digest items that
// were compacted before enrichment (isLegacyItem), rebuilding Counts and
// TagCounts. A faithful port of PebbleActivityStore.RecompactDigests, needed
// because digests backfilled from Pebble can carry legacy items.
func (s *SQLActivityStore) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	var result RecompactResult

	rows, err := s.reader.QueryContext(ctx, s.dialect.rebind(
		`SELECT id, details FROM activity WHERE tier = 'digest' AND type = 'daily_digest'`))
	if err != nil {
		return result, fmt.Errorf("sql_activity: recompact scan: %w", err)
	}
	type cand struct {
		id      int64
		details string
	}
	var candidates []cand
	for rows.Next() {
		var c cand
		var det sql.NullString
		if err := rows.Scan(&c.id, &det); err != nil {
			rows.Close()
			return result, err
		}
		c.details = det.String
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		var dd DigestDetails
		if c.details != "" {
			raw, derr := decodeActivityDetails([]byte(c.details))
			if derr != nil {
				continue // undecodable — leave as-is, do not corrupt
			}
			if uerr := json.Unmarshal(raw, &dd); uerr != nil {
				continue // undecodable — leave as-is, do not corrupt
			}
		}
		if !slices.ContainsFunc(dd.Items, isLegacyItem) {
			result.Skipped++
			continue
		}
		for i, item := range dd.Items {
			if !isLegacyItem(item) {
				continue
			}
			derivedType, derivedTier := deriveTypeFromMessage(item.Summary, "")
			dd.Items[i].Type = derivedType
			dd.Items[i].Tier = derivedTier
			dd.Items[i].Tags = enrichLegacyLogTags(item.Summary, "", "info")
		}
		newCounts := make(map[string]int)
		for _, item := range dd.Items {
			newCounts[item.Type]++
		}
		dd.Counts = newCounts
		newTagCounts := make(map[string]map[string]int)
		for _, item := range dd.Items {
			for _, tag := range item.Tags {
				colonIdx := strings.Index(tag, ":")
				if colonIdx < 1 {
					continue
				}
				ns, val := tag[:colonIdx], tag[colonIdx+1:]
				if ns != "action" && ns != "source" {
					continue
				}
				if newTagCounts[ns] == nil {
					newTagCounts[ns] = make(map[string]int)
				}
				newTagCounts[ns][val]++
			}
		}
		if len(newTagCounts) > 0 {
			dd.TagCounts = newTagCounts
		}
		ddBytes, merr := json.Marshal(dd)
		if merr != nil {
			return result, fmt.Errorf("sql_activity: recompact marshal: %w", merr)
		}
		summary := fmt.Sprintf("Daily digest for %s (%d entries)", dd.Date, dd.OriginalCount)
		if _, uerr := s.writer.ExecContext(ctx, s.dialect.rebind(
			`UPDATE activity SET details = ?, summary = ? WHERE id = ?`),
			encodeActivityDetails(ddBytes), summary, c.id); uerr != nil {
			return result, fmt.Errorf("sql_activity: recompact update: %w", uerr)
		}
		result.Touched++
	}
	return result, nil
}

// RepairActivityIndexes is a no-op for the SQL backend. The engine maintains
// every index transactionally, so a delete can never leave a dangling index
// entry — the leak that motivated this method on the Pebble backend (act:op:
// alone held ~0.783 GiB of orphaned refs) is structurally impossible here. The
// interface doc explicitly sanctions backend-specific no-ops (MigrateSystemActivityLogs
// is one), so this returns zeros rather than hiding behind an optional interface.
func (s *SQLActivityStore) RepairActivityIndexes(_ context.Context) (ActivityIndexRepairResult, error) {
	return ActivityIndexRepairResult{}, nil
}

// sqlActAnalysisLimit caps how many rows ANALYZE samples per index.
//
// This single pragma is the difference between a schedulable job and one that
// cannot run at all. Measured 2026-09-09 on a reflink copy of the production
// activity database (13,256,788 rows, 19.5 GB, 6 indexes, host under load
// average ~30):
//
//	ANALYZE with no analysis_limit  : >600 s, did NOT complete
//	ANALYZE with analysis_limit=400 :  344 s, populated all 6 index stats
//
// 400 is SQLite's own documented recommendation for large databases. It samples
// enough of each index to get the selectivity ordering right — which is all the
// planner needs to stop guessing — without the full-index walk that makes the
// unbounded form open-ended. It is deliberately NOT zero: zero means "no limit"
// in SQLite, so a typo that dropped this value would silently restore the
// >10-minute behaviour.
const sqlActAnalysisLimit = 400

// OptimizeStatistics refreshes the query planner's statistics.
//
// It runs PRAGMA optimize under sqlActAnalysisLimit, which is the incremental
// form: SQLite analyzes only the tables whose statistics it judges stale, so the
// recurring cost is a small fraction of a full ANALYZE. On a database that has
// never been analyzed there is nothing for it to compare against, so this
// bootstraps with an explicit ANALYZE first — the expensive pass happens exactly
// once per database rather than on every schedule.
//
// PRODUCTION STATE THAT MOTIVATED THIS. On 2026-09-09 the live activity database
// had no sqlite_stat1 table at all: ANALYZE had never been run on it in its
// life, so every plan over 13.2M rows came from SQLite's built-in guesses rather
// than from the data. Compaction, Summarize, Prune and the activity UI all share
// those plans.
//
// It is NOT run at startup: 344 s of I/O on the boot path would delay every
// restart, and this host restarts several times a day. It belongs on a schedule.
func (s *SQLActivityStore) OptimizeStatistics(ctx context.Context) (ActivityOptimizeResult, error) {
	start := time.Now()
	result := ActivityOptimizeResult{Supported: true}

	// analysis_limit is per-connection, so it must be set on the same handle
	// that runs the analysis. The writer is single-connection, which guarantees
	// that; setting it on the reader pool would apply to whichever of the four
	// connections happened to serve the pragma.
	if _, err := s.writer.ExecContext(ctx, fmt.Sprintf("PRAGMA analysis_limit=%d", sqlActAnalysisLimit)); err != nil {
		return result, fmt.Errorf("sql_activity: set analysis_limit: %w", err)
	}

	// Keep SQLite's sorter OFF the root filesystem.
	//
	// THIS IS NOT A TUNING KNOB. ANALYZE builds sorter runs over the full
	// b-trees and spills them to SQLite's temp directory, which defaults to
	// TMPDIR and therefore to /tmp. On this deployment /tmp is a ZFS dataset on
	// the ROOT pool, not a ramdisk and not on the pool holding the database. On
	// 2026-09-09 at 13:39 EDT an ANALYZE of this table did exactly that, filled
	// the root pool, and took production down: Pebble died with "fatal commit
	// error: write /var/lib/audiobook-organizer/audiobooks.pebble/892515.log: no
	// space left on device". The activity database was 19 GB across 13,256,825
	// rows at the time.
	//
	// analysis_limit above does NOT bound this. It caps the rows SAMPLED per
	// index; the sort still ranges over the whole index. So the limit makes
	// ANALYZE finish sooner, not spill less, and it cannot be relied on for
	// safety here.
	//
	// The database's own directory is the right target: it is by construction on
	// the volume already sized for this data, it moves automatically if the data
	// directory is relocated, and it needs no environment variable to be set on
	// the unit. Setting it is best-effort — a backend or build that does not
	// support the pragma must not turn statistics maintenance into a hard
	// failure — but a failure is logged rather than swallowed, because the
	// consequence of it silently not applying is the outage above.
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if _, err := s.writer.ExecContext(ctx,
			fmt.Sprintf("PRAGMA temp_store_directory='%s'", strings.ReplaceAll(dir, "'", "''"))); err != nil {
			slog.Warn("sql_activity: could not point SQLite's sorter at the database directory; "+
				"ANALYZE may spill to TMPDIR, which on this deployment is the root pool",
				"dir", dir, "err", err)
		}
	}

	var statRows int
	// A missing sqlite_stat1 makes this SELECT fail rather than return 0, so the
	// existence check comes first and the two cases stay distinguishable.
	var haveStat1 int
	if err := s.writer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&haveStat1); err != nil {
		return result, fmt.Errorf("sql_activity: probe sqlite_stat1: %w", err)
	}
	if haveStat1 > 0 {
		if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_stat1`).Scan(&statRows); err != nil {
			return result, fmt.Errorf("sql_activity: count sqlite_stat1: %w", err)
		}
	}

	if statRows == 0 {
		// No statistics exist. PRAGMA optimize decides what to re-analyze by
		// comparing against stored stats, so with none stored it can reach the
		// wrong conclusion about what needs doing. Bootstrap explicitly.
		result.Bootstrapped = true
		if _, err := s.writer.ExecContext(ctx, `ANALYZE`); err != nil {
			return result, fmt.Errorf("sql_activity: bootstrap analyze: %w", err)
		}
	} else if _, err := s.writer.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return result, fmt.Errorf("sql_activity: optimize: %w", err)
	}

	if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_stat1`).Scan(&result.TablesAnalyzed); err != nil {
		return result, fmt.Errorf("sql_activity: verify sqlite_stat1: %w", err)
	}
	result.Duration = time.Since(start)
	return result, nil
}

// MigrateSystemActivityLogs is a no-op: there is no legacy SQLite→SQL hop.
func (s *SQLActivityStore) MigrateSystemActivityLogs() (int, error) { return 0, nil }

// ── ActivityLifecycle ───────────────────────────────────────────────────────

// Close checkpoints and closes both handles.
func (s *SQLActivityStore) Close() error {
	_, _ = s.writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	rerr := s.reader.Close()
	werr := s.writer.Close()
	if werr != nil {
		return fmt.Errorf("sql_activity: close writer: %w", werr)
	}
	return rerr
}

var _ ActivityStorer = (*SQLActivityStore)(nil)

// CountActivity returns the EXACT number of rows in a tier, optionally bounded
// to rows strictly older than a cutoff.
//
// Query cannot answer this: its total is deliberately capped at sqlActCountCap
// (the count subquery carries a LIMIT), so above the cap it reports the cap as
// a lower bound. That is right for a paginated UI and wrong for a census, which
// is why this exists as its own uncapped COUNT.
func (s *SQLActivityStore) CountActivity(ctx context.Context, tier string, olderThan *time.Time) (int, error) {
	q := "SELECT COUNT(*) FROM activity WHERE tier = ?"
	args := []any{tier}
	if olderThan != nil {
		q += " AND ts < ?"
		args = append(args, olderThan.UnixNano())
	}
	var n int
	if err := s.reader.QueryRowContext(ctx, s.dialect.rebind(q), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("sql_activity: count (tier=%s): %w", tier, err)
	}
	return n, nil
}
