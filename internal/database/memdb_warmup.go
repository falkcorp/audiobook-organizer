// file: internal/database/memdb_warmup.go
// version: 1.5.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000004
// last-edited: 2026-08-13

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// WarmFromPebble populates the in-memory store by scanning every relevant
// Pebble key prefix. Must be called once after NewMemStore to make queries
// useful. Safe to re-run.
//
// Resilience model: an individual row failing to insert (uniqueness conflict,
// indexer error, malformed JSON, etc.) is logged and skipped — it does NOT
// abort the whole warmup. Pebble remains source of truth; missing a few
// rows in memdb is recoverable, but having ALL rows missing because of one
// bad apple is a production-breaking bug (and was — see the v1.0.0 incident
// where unique-on-file_path caused memdb to come up empty for the entire
// library list).
func (m *MemStore) WarmFromPebble(ctx context.Context, p *PebbleStore) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("memdb warmup: PebbleStore not initialized")
	}

	started := time.Now()
	txn := m.db.Txn(true)
	defer txn.Abort()

	counts := map[string]int{}
	scanned := map[string]int{}
	skips := map[string]int{}
	durations := map[string]time.Duration{}

	// warm runs one prefix scan and records how long it took.
	//
	// Per-phase timing is the whole point. Warmup published a single total —
	// 115,971 ms on production 2026-08-13 — which establishes that the library
	// is wrong-or-unusable for about two minutes after every restart, but says
	// nothing about which of the ten scans to attack. The ten are not
	// comparable: "book:" scans ~7.5 keys per admitted row and inserts into a
	// heavily indexed table, while "blocked:hash:" is a handful of rows into a
	// table with one index. Optimizing without knowing the split would be
	// guessing, and this codebase has already paid for one extrapolated
	// data-structure cost estimate that was off by several times.
	//
	// Cost of the measurement itself: two time.Now() calls per prefix, twenty
	// for the whole warmup.
	warm := func(prefix, table string, fn func(key string, val []byte) (bool, error)) (int, int, error) {
		phaseStart := time.Now()
		rows, keys, err := warmIter(ctx, p.db, prefix, fn)
		durations[table] = time.Since(phaseStart)
		return rows, keys, err
	}

	// safeInsert tries to insert an object, logging+counting failures rather
	// than aborting the warmup. Returns whether the row actually landed in
	// memdb (never an error, so warmIter keeps going).
	safeInsert := func(table string, obj interface{}, keyForLog string) (bool, error) {
		if err := txn.Insert(table, obj); err != nil {
			skips[table]++
			// Don't spam: log first 10 per table, then drop to debug.
			if skips[table] <= 10 {
				slog.Warn("memdb warmup: skipping row",
					"table", table, "key", keyForLog, "error", err)
			} else if skips[table] == 11 {
				slog.Warn("memdb warmup: further skips muted",
					"table", table, "muting_after", 10)
			}
			return false, nil
		}
		return true, nil
	}

	// Books: book:<id> where id has no further colons. The "book:" prefix is
	// shared with ~7 secondary-index families, so `scanned` here runs about
	// 7.5x `rows` on production — see warmIter.
	// Strip heavy fields (Description, BookSigV1, etc.) before insertion
	// — see memdb_strip.go. Cuts radix-tree footprint from ~10GB to ~2GB
	// on the production library.
	if n, keys, err := warm("book:", memTableBooks, func(key string, val []byte) (bool, error) {
		if strings.Count(key, ":") != 1 {
			return false, nil
		}
		var b Book
		if err := json.Unmarshal(val, &b); err != nil {
			return false, nil
		}
		return safeInsert(memTableBooks, stripBookForMemdb(&b), key)
	}); err != nil {
		return fmt.Errorf("warmup books: %w", err)
	} else {
		counts[memTableBooks] = n
		scanned[memTableBooks] = keys
	}

	// Authors: author:<id> (skip author:name:* index)
	if n, keys, err := warm("author:", memTableAuthors, func(key string, val []byte) (bool, error) {
		if strings.Contains(key, ":name:") {
			return false, nil
		}
		if strings.Count(key, ":") != 1 {
			return false, nil
		}
		var a Author
		if err := json.Unmarshal(val, &a); err != nil {
			return false, nil
		}
		return safeInsert(memTableAuthors, &a, key)
	}); err != nil {
		return fmt.Errorf("warmup authors: %w", err)
	} else {
		counts[memTableAuthors] = n
		scanned[memTableAuthors] = keys
	}

	// Series: series:<id>
	if n, keys, err := warm("series:", memTableSeries, func(key string, val []byte) (bool, error) {
		if strings.Count(key, ":") != 1 {
			return false, nil
		}
		var s Series
		if err := json.Unmarshal(val, &s); err != nil {
			return false, nil
		}
		return safeInsert(memTableSeries, &s, key)
	}); err != nil {
		return fmt.Errorf("warmup series: %w", err)
	} else {
		counts[memTableSeries] = n
		scanned[memTableSeries] = keys
	}

	// BookFiles: book_file:<bookID>:<fileID>
	// Strip AcoustIDSeg1..6 and fingerprint-diagnostic fields before
	// insertion — see memdb_strip.go. Cuts ~70MB heap across 308K rows.
	if n, keys, err := warm("book_file:", memTableBookFiles, func(key string, val []byte) (bool, error) {
		if strings.Count(key, ":") != 2 {
			return false, nil
		}
		var bf BookFile
		if err := json.Unmarshal(val, &bf); err != nil {
			return false, nil
		}
		return safeInsert(memTableBookFiles, stripBookFileForMemdb(&bf), key)
	}); err != nil {
		return fmt.Errorf("warmup book_files: %w", err)
	} else {
		counts[memTableBookFiles] = n
		scanned[memTableBookFiles] = keys
	}

	// BookAuthors: book_authors:<bookID> contains []BookAuthor; flatten.
	// One key yields many rows, so track the row count directly rather than
	// letting warmIter infer it from the per-key admitted flag.
	bookAuthorRows := 0
	if _, keys, err := warm("book_authors:", memTableBookAuthors, func(key string, val []byte) (bool, error) {
		var list []BookAuthor
		if err := json.Unmarshal(val, &list); err != nil {
			return false, nil
		}
		for i := range list {
			ba := list[i]
			if ok, _ := safeInsert(memTableBookAuthors, &ba, key); ok {
				bookAuthorRows++
			}
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("warmup book_authors: %w", err)
	} else {
		counts[memTableBookAuthors] = bookAuthorRows
		scanned[memTableBookAuthors] = keys
	}

	// BookNarrators: book_narrators:<bookID> contains []BookNarrator; flatten.
	bookNarratorRows := 0
	if _, keys, err := warm("book_narrators:", memTableBookNarrators, func(key string, val []byte) (bool, error) {
		var list []BookNarrator
		if err := json.Unmarshal(val, &list); err != nil {
			return false, nil
		}
		for i := range list {
			bn := list[i]
			if ok, _ := safeInsert(memTableBookNarrators, &bn, key); ok {
				bookNarratorRows++
			}
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("warmup book_narrators: %w", err)
	} else {
		counts[memTableBookNarrators] = bookNarratorRows
		scanned[memTableBookNarrators] = keys
	}

	// Narrators: narrator:<id>
	if n, keys, err := warm("narrator:", memTableNarrators, func(key string, val []byte) (bool, error) {
		var nrt Narrator
		if err := json.Unmarshal(val, &nrt); err != nil {
			return false, nil
		}
		return safeInsert(memTableNarrators, &nrt, key)
	}); err != nil {
		return fmt.Errorf("warmup narrators: %w", err)
	} else {
		counts[memTableNarrators] = n
		scanned[memTableNarrators] = keys
	}

	// ImportPaths: import_path:<id> (skip import_path:path:* index)
	if n, keys, err := warm("import_path:", memTableImportPaths, func(key string, val []byte) (bool, error) {
		if strings.Contains(key, ":path:") {
			return false, nil
		}
		var ip ImportPath
		if err := json.Unmarshal(val, &ip); err != nil {
			return false, nil
		}
		return safeInsert(memTableImportPaths, &ip, key)
	}); err != nil {
		return fmt.Errorf("warmup import_paths: %w", err)
	} else {
		counts[memTableImportPaths] = n
		scanned[memTableImportPaths] = keys
	}

	// AuthorAliases: author_alias:<id>
	if n, keys, err := warm("author_alias:", memTableAuthorAliases, func(key string, val []byte) (bool, error) {
		var aa AuthorAlias
		if err := json.Unmarshal(val, &aa); err != nil {
			return false, nil
		}
		return safeInsert(memTableAuthorAliases, &aa, key)
	}); err != nil {
		return fmt.Errorf("warmup author_aliases: %w", err)
	} else {
		counts[memTableAuthorAliases] = n
		scanned[memTableAuthorAliases] = keys
	}

	// BlockedHashes: blocked:hash:<hash>
	if n, keys, err := warm("blocked:hash:", memTableBlockedHashes, func(key string, val []byte) (bool, error) {
		var bh DoNotImport
		if err := json.Unmarshal(val, &bh); err != nil {
			return false, nil
		}
		return safeInsert(memTableBlockedHashes, &bh, key)
	}); err != nil {
		return fmt.Errorf("warmup blocked_hashes: %w", err)
	} else {
		counts[memTableBlockedHashes] = n
		scanned[memTableBlockedHashes] = keys
	}

	// Works: intentionally NOT warmed into memdb. Works are queried in
	// <0.1% of requests and a 211K-row × ~590B memdb residency cost
	// ~120MB of heap for no measurable read-path win. GetAllWorks
	// now routes through PebbleStore.GetAllWorks_Pebble (a streaming
	// prefix scan + JSON unmarshal). The scanner uses a single
	// GetAllWorks at scan start, which is the only meaningful caller.

	// Time the commit separately rather than folding it into the phase total.
	// A single write txn is held open across all ten scans, so if go-memdb
	// defers real work to commit, that cost would otherwise be invisible —
	// attributed to nothing and sitting in the gap between the phase sum and
	// duration_ms. Whether the fix is "parallelize the scans" or "don't hold
	// one txn across all of them" turns on this number.
	commitStart := time.Now()
	txn.Commit()
	commitDur := time.Since(commitStart)
	commitMS := commitDur.Milliseconds()

	durations[WarmupPhaseKeyCommit] = commitDur

	m.warmCountsMu.Lock()
	m.lastWarmCounts = maps.Clone(counts)
	m.lastWarmScanned = maps.Clone(scanned)
	m.lastWarmDurations = maps.Clone(durations)
	m.warmCountsMu.Unlock()

	slog.Info("memdb warmup complete",
		"duration_ms", time.Since(started).Milliseconds(),
		"books", counts[memTableBooks],
		"authors", counts[memTableAuthors],
		"series", counts[memTableSeries],
		"book_files", counts[memTableBookFiles],
		"book_authors", counts[memTableBookAuthors],
		"book_narrators", counts[memTableBookNarrators],
		"narrators", counts[memTableNarrators],
		"import_paths", counts[memTableImportPaths],
		"author_aliases", counts[memTableAuthorAliases],
		"blocked_hashes", counts[memTableBlockedHashes],
		"skipped_total", sumInts(skips),
		// Per-phase milliseconds, keyed by table. The sum is less than
		// duration_ms: the gap is txn.Commit plus the setup either side, and
		// a large gap is itself the finding — it would mean the commit, not
		// the scans, is what holds the library down for two minutes.
		"phase_ms", durationsMillis(durations),
		"commit_ms", commitMS,
	)
	if len(skips) > 0 {
		slog.Warn("memdb warmup: rows skipped by table",
			"skipped_by_table", skips)
	}

	// Emit sampled memdb size telemetry post-warmup.
	if err := emitMemdbSizeTelemetry(ctx, m, counts); err != nil {
		slog.Warn("memdb warmup: telemetry emission failed", "error", err)
		// Don't fail warmup for telemetry errors — they're observability only.
	}

	return nil
}

// durationsMillis renders per-phase durations as whole milliseconds keyed by
// table, so the warmup log carries a breakdown a reader can act on instead of
// a single total that only says "slow".
func durationsMillis(d map[string]time.Duration) map[string]int64 {
	out := make(map[string]int64, len(d))
	for table, dur := range d {
		out[table] = dur.Milliseconds()
	}
	return out
}

func sumInts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// emitMemdbSizeTelemetry samples N=100 random rows per memdb table, marshals them,
// extrapolates total bytes, and logs the per-table size estimates at INFO level.
func emitMemdbSizeTelemetry(ctx context.Context, m *MemStore, counts map[string]int) error {
	const sampleSize = 100

	type memdbtableInfo struct {
		name       string
		indexName  string
		maxSamples int
	}

	tables := []memdbtableInfo{
		{memTableBooks, memIdxID, counts[memTableBooks]},
		{memTableAuthors, memIdxID, counts[memTableAuthors]},
		{memTableSeries, memIdxID, counts[memTableSeries]},
		{memTableBookFiles, memIdxID, counts[memTableBookFiles]},
		{memTableBookAuthors, memIdxID, counts[memTableBookAuthors]},
		{memTableBookNarrators, memIdxID, counts[memTableBookNarrators]},
		{memTableNarrators, memIdxID, counts[memTableNarrators]},
		{memTableImportPaths, memIdxID, counts[memTableImportPaths]},
		{memTableAuthorAliases, memIdxID, counts[memTableAuthorAliases]},
		{memTableBlockedHashes, memIdxID, counts[memTableBlockedHashes]},
	}

	for _, tbl := range tables {
		// Skip empty tables.
		if tbl.maxSamples == 0 {
			continue
		}

		// Sample up to sampleSize rows.
		actualSamples := sampleSize
		if tbl.maxSamples < sampleSize {
			actualSamples = tbl.maxSamples
		}

		// Collect sampled rows.
		var totalBytes int64
		sampled := 0
		txn := m.db.Txn(false)
		it, err := txn.Get(tbl.name, tbl.indexName)
		if err != nil {
			txn.Abort()
			slog.Warn("memdb telemetry: failed to query table",
				"table", tbl.name, "error", err)
			continue
		}

		// Iterate all rows, randomly selecting actualSamples of them.
		stride := tbl.maxSamples / actualSamples
		if stride == 0 {
			stride = 1
		}
		idx := 0

		for row := it.Next(); row != nil; row = it.Next() {
			if idx%stride == 0 {
				data, err := json.Marshal(row)
				if err != nil {
					slog.Warn("memdb telemetry: marshal failed for table",
						"table", tbl.name, "error", err)
					continue
				}
				totalBytes += int64(len(data))
				sampled++
				if sampled >= actualSamples {
					break
				}
			}
			idx++
		}
		txn.Abort()

		// Extrapolate total bytes from sample.
		var estimatedTotalBytes int64
		if sampled > 0 {
			avgBytes := totalBytes / int64(sampled)
			estimatedTotalBytes = avgBytes * int64(tbl.maxSamples)
		}

		slog.Info("memdb telemetry: table size",
			"table", tbl.name,
			"row_count", tbl.maxSamples,
			"sampled_rows", sampled,
			"sample_total_bytes", totalBytes,
			"estimated_total_bytes", estimatedTotalBytes,
		)
	}

	return nil
}

// warmIter iterates every key under a given prefix and invokes the callback.
//
// Returns TWO numbers, and the distinction is load-bearing:
//
//	rows    — times the callback reported it admitted a row into memdb
//	scanned — Pebble keys visited under the prefix
//
// These are wildly different for any prefix shared with secondary indexes.
// "book:" also holds book:path:, book:hash:, book:originalhash:,
// book:organizedhash:, book:versiongroup:, book:work:, book:asin: and
// book:isbn13: — roughly 6.5 index keys per book row on production. "author:"
// also holds author:name:, about 1 index key per author row.
//
// warmIter previously returned only `scanned` and WarmFromPebble published it
// under the label "books". Production therefore logged books=366922 for a
// library of ~49,000 books. Every whole-library iterator was subsequently
// measured against that inflated denominator and appeared to be returning
// 13.3% of the library; a P0 "silent data loss" investigation followed. The
// iterators were correct the whole time. Never conflate the two counts again.
func warmIter(ctx context.Context, db *pebble.DB, prefix string, fn func(key string, val []byte) (bool, error)) (rows int, scanned int, err error) {
	// Bail before creating an iterator if the warmup was canceled (Close). This
	// keeps cancellation prompt and avoids calling NewIter on a DB that is about
	// to be closed.
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	upper := append([]byte(nil), []byte(prefix)...)
	// Replace trailing ':' with ';' so the upper bound sorts immediately past
	// all keys starting with prefix.
	if len(upper) > 0 && upper[len(upper)-1] == ':' {
		upper[len(upper)-1] = ';'
	} else {
		upper = append(upper, 0xFF)
	}
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: upper,
	})
	if err != nil {
		return 0, 0, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		if ctx.Err() != nil {
			return rows, scanned, ctx.Err()
		}
		scanned++
		admitted, err := fn(string(iter.Key()), iter.Value())
		if err != nil {
			return rows, scanned, err
		}
		if admitted {
			rows++
		}
	}
	// An iterator that stops short must say so rather than returning a partial
	// set that reads as complete.
	if err := iter.Error(); err != nil {
		return rows, scanned, fmt.Errorf("warmup scan of %q failed after %d keys: %w", prefix, scanned, err)
	}
	return rows, scanned, nil
}
