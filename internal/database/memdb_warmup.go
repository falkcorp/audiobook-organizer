// file: internal/database/memdb_warmup.go
// version: 1.3.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000004
// last-edited: 2026-08-11

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

	// safeInsert tries to insert an object, logging+counting failures rather
	// than aborting the warmup. Returns nil so warmIter keeps going.
	safeInsert := func(table string, obj interface{}, keyForLog string) error {
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
		}
		return nil
	}

	// Books: book:<id> where id has no further colons.
	// Strip heavy fields (Description, BookSigV1, etc.) before insertion
	// — see memdb_strip.go. Cuts radix-tree footprint from ~10GB to
	// ~2GB on the 392K-book production library.
	if n, err := warmIter(ctx, p.db, "book:", func(key string, val []byte) error {
		if strings.Count(key, ":") != 1 {
			return nil
		}
		var b Book
		if err := json.Unmarshal(val, &b); err != nil {
			return nil
		}
		return safeInsert(memTableBooks, stripBookForMemdb(&b), key)
	}); err != nil {
		return fmt.Errorf("warmup books: %w", err)
	} else {
		counts[memTableBooks] = n
	}

	// Authors: author:<id> (skip author:name:* index)
	if n, err := warmIter(ctx, p.db, "author:", func(key string, val []byte) error {
		if strings.Contains(key, ":name:") {
			return nil
		}
		if strings.Count(key, ":") != 1 {
			return nil
		}
		var a Author
		if err := json.Unmarshal(val, &a); err != nil {
			return nil
		}
		return safeInsert(memTableAuthors, &a, key)
	}); err != nil {
		return fmt.Errorf("warmup authors: %w", err)
	} else {
		counts[memTableAuthors] = n
	}

	// Series: series:<id>
	if n, err := warmIter(ctx, p.db, "series:", func(key string, val []byte) error {
		if strings.Count(key, ":") != 1 {
			return nil
		}
		var s Series
		if err := json.Unmarshal(val, &s); err != nil {
			return nil
		}
		return safeInsert(memTableSeries, &s, key)
	}); err != nil {
		return fmt.Errorf("warmup series: %w", err)
	} else {
		counts[memTableSeries] = n
	}

	// BookFiles: book_file:<bookID>:<fileID>
	// Strip AcoustIDSeg1..6 and fingerprint-diagnostic fields before
	// insertion — see memdb_strip.go. Cuts ~70MB heap across 308K rows.
	if n, err := warmIter(ctx, p.db, "book_file:", func(key string, val []byte) error {
		if strings.Count(key, ":") != 2 {
			return nil
		}
		var bf BookFile
		if err := json.Unmarshal(val, &bf); err != nil {
			return nil
		}
		return safeInsert(memTableBookFiles, stripBookFileForMemdb(&bf), key)
	}); err != nil {
		return fmt.Errorf("warmup book_files: %w", err)
	} else {
		counts[memTableBookFiles] = n
	}

	// BookAuthors: book_authors:<bookID> contains []BookAuthor; flatten.
	if n, err := warmIter(ctx, p.db, "book_authors:", func(key string, val []byte) error {
		var list []BookAuthor
		if err := json.Unmarshal(val, &list); err != nil {
			return nil
		}
		for i := range list {
			ba := list[i]
			_ = safeInsert(memTableBookAuthors, &ba, key)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("warmup book_authors: %w", err)
	} else {
		counts[memTableBookAuthors] = n
	}

	// BookNarrators: book_narrators:<bookID> contains []BookNarrator; flatten.
	if n, err := warmIter(ctx, p.db, "book_narrators:", func(key string, val []byte) error {
		var list []BookNarrator
		if err := json.Unmarshal(val, &list); err != nil {
			return nil
		}
		for i := range list {
			bn := list[i]
			_ = safeInsert(memTableBookNarrators, &bn, key)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("warmup book_narrators: %w", err)
	} else {
		counts[memTableBookNarrators] = n
	}

	// Narrators: narrator:<id>
	if n, err := warmIter(ctx, p.db, "narrator:", func(key string, val []byte) error {
		var nrt Narrator
		if err := json.Unmarshal(val, &nrt); err != nil {
			return nil
		}
		return safeInsert(memTableNarrators, &nrt, key)
	}); err != nil {
		return fmt.Errorf("warmup narrators: %w", err)
	} else {
		counts[memTableNarrators] = n
	}

	// ImportPaths: import_path:<id> (skip import_path:path:* index)
	if n, err := warmIter(ctx, p.db, "import_path:", func(key string, val []byte) error {
		if strings.Contains(key, ":path:") {
			return nil
		}
		var ip ImportPath
		if err := json.Unmarshal(val, &ip); err != nil {
			return nil
		}
		return safeInsert(memTableImportPaths, &ip, key)
	}); err != nil {
		return fmt.Errorf("warmup import_paths: %w", err)
	} else {
		counts[memTableImportPaths] = n
	}

	// AuthorAliases: author_alias:<id>
	if n, err := warmIter(ctx, p.db, "author_alias:", func(key string, val []byte) error {
		var aa AuthorAlias
		if err := json.Unmarshal(val, &aa); err != nil {
			return nil
		}
		return safeInsert(memTableAuthorAliases, &aa, key)
	}); err != nil {
		return fmt.Errorf("warmup author_aliases: %w", err)
	} else {
		counts[memTableAuthorAliases] = n
	}

	// BlockedHashes: blocked:hash:<hash>
	if n, err := warmIter(ctx, p.db, "blocked:hash:", func(key string, val []byte) error {
		var bh DoNotImport
		if err := json.Unmarshal(val, &bh); err != nil {
			return nil
		}
		return safeInsert(memTableBlockedHashes, &bh, key)
	}); err != nil {
		return fmt.Errorf("warmup blocked_hashes: %w", err)
	} else {
		counts[memTableBlockedHashes] = n
	}

	// Works: intentionally NOT warmed into memdb. Works are queried in
	// <0.1% of requests and a 211K-row × ~590B memdb residency cost
	// ~120MB of heap for no measurable read-path win. GetAllWorks
	// now routes through PebbleStore.GetAllWorks_Pebble (a streaming
	// prefix scan + JSON unmarshal). The scanner uses a single
	// GetAllWorks at scan start, which is the only meaningful caller.

	txn.Commit()

	m.warmCountsMu.Lock()
	m.lastWarmCounts = maps.Clone(counts)
	m.lastWarmScanned = maps.Clone(scanned)
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
// Returns the number of times the callback was invoked. Stops early if ctx
// is cancelled or the callback returns an error.
func warmIter(ctx context.Context, db *pebble.DB, prefix string, fn func(key string, val []byte) error) (int, error) {
	// Bail before creating an iterator if the warmup was canceled (Close). This
	// keeps cancellation prompt and avoids calling NewIter on a DB that is about
	// to be closed.
	if err := ctx.Err(); err != nil {
		return 0, err
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
		return 0, err
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if ctx.Err() != nil {
			return count, ctx.Err()
		}
		if err := fn(string(iter.Key()), iter.Value()); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
