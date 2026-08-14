// file: internal/database/pebble_quick_queries.go
// version: 1.2.0
// guid: 7f3a1b2c-4d5e-6f7a-8b9c-0d1e2f3a4b5c
// last-edited: 2026-08-13

package database

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// quickQueryCacheKeyPrefix is the PebbleDB key prefix for per-query count caches.
const quickQueryCacheKeyPrefix = "quick_query_cache:"

// quickQueryCacheTTL is the maximum age of a cached quick-query count before it is
// recomputed unconditionally (even without a dirty flag). Set to 1 hour so stale
// data never lingers past the next maintenance window.
const quickQueryCacheTTL = 1 * time.Hour

// QuickQueryEntry is the JSON value stored under quick_query_cache:<id>.
type QuickQueryEntry struct {
	Count      int       `json:"count"`
	ComputedAt time.Time `json:"computed_at"`
	Dirty      bool      `json:"dirty"`
}

// QuickQueryResult is one item in the GET /api/v1/library/quick-queries response.
type QuickQueryResult struct {
	ID     string                 `json:"id"`
	Label  string                 `json:"label"`
	Count  int                    `json:"count"`
	Filter map[string]interface{} `json:"filter"`
}

// quickQueryDefs defines the six preset quick queries: id, human label, and the
// filter object expressed in the same shape the frontend FilterOptions/URL-params
// system already understands.
var quickQueryDefs = []struct {
	id     string
	label  string
	filter map[string]interface{}
}{
	{
		id:    "missing_covers",
		label: "Missing covers",
		filter: map[string]interface{}{
			"missingCovers": true,
		},
	},
	{
		id:    "broken_files",
		label: "Broken files",
		filter: map[string]interface{}{
			"hasFileErrors": true,
		},
	},
	{
		id:    "no_fingerprints",
		label: "No fingerprints",
		filter: map[string]interface{}{
			"fingerprintStatus": "none",
		},
	},
	{
		id:    "in_import_path",
		label: "In import path",
		filter: map[string]interface{}{
			"inImportPath": true,
		},
	},
	{
		id:    "no_isbn",
		label: "No ISBN",
		filter: map[string]interface{}{
			"noIsbn": true,
		},
	},
	{
		id:    "duplicates_flagged",
		label: "Duplicates flagged",
		filter: map[string]interface{}{
			"duplicatesFlagged": true,
		},
	},
}

// MarkQuickQueryDirty marks a single quick-query cache entry dirty without
// recomputing. The next call to GetQuickQueryCounts will recompute it.
// NoSync is intentional: a crash before flush leaves a dirty=false entry that
// will be recomputed when TTL expires — same as missing-key behaviour.
func (p *PebbleStore) MarkQuickQueryDirty(id, reason string) {
	key := []byte(quickQueryCacheKeyPrefix + id)
	val, closer, err := p.db.Get(key)
	if err != nil {
		// Cache miss — nothing to mark dirty; recompute will happen on next read.
		slog.Debug("quick_query marked dirty (cache miss)", "id", id, "reason", reason)
		return
	}
	var entry QuickQueryEntry
	_ = json.Unmarshal(val, &entry)
	closer.Close()

	entry.Dirty = true
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := p.db.Set(key, data, pebble.NoSync); err != nil {
		slog.Warn("quick_query mark dirty failed", "id", id, "error", err)
		return
	}
	slog.Debug("quick_query marked dirty", "id", id, "reason", reason)
}

// MarkAllQuickQueriesDirty marks all quick-query cache entries dirty.
// Used on wide mutations (CreateBook, DeleteBook) where all six queries may change.
func (p *PebbleStore) MarkAllQuickQueriesDirty(reason string) {
	for _, def := range quickQueryDefs {
		if def.id == "broken_files" {
			// broken_files is served from LibraryStats; no separate cache entry to dirty.
			continue
		}
		p.MarkQuickQueryDirty(def.id, reason)
	}
}

// readCachedQuickQuery loads the cached entry for id. Returns nil when the
// entry is missing, dirty, or older than quickQueryCacheTTL.
func (p *PebbleStore) readCachedQuickQuery(id string) *QuickQueryEntry {
	key := []byte(quickQueryCacheKeyPrefix + id)
	val, closer, err := p.db.Get(key)
	if err != nil {
		return nil
	}
	defer closer.Close()
	var entry QuickQueryEntry
	if err := json.Unmarshal(val, &entry); err != nil {
		return nil
	}
	if entry.Dirty {
		return nil
	}
	if time.Since(entry.ComputedAt) > quickQueryCacheTTL {
		return nil
	}
	return &entry
}

// writeCachedQuickQuery persists a fresh entry for id.
func (p *PebbleStore) writeCachedQuickQuery(id string, count int) {
	entry := QuickQueryEntry{
		Count:      count,
		ComputedAt: time.Now(),
		Dirty:      false,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := p.db.Set([]byte(quickQueryCacheKeyPrefix+id), data, pebble.Sync); err != nil {
		slog.Error("quick_query cache write failed", "id", id, "error", err)
	}
}

// computeQuickQueryCount runs the appropriate book scan for a single query id
// and returns the matching book count.
func (p *PebbleStore) computeQuickQueryCount(id string) (int, error) {
	start := time.Now()

	// Load import paths once for in_import_path query.
	var importPaths []ImportPath
	if id == "in_import_path" {
		importPaths, _ = p.GetAllImportPaths()
	}

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if bookIsSoftDeleted(&b) {
			continue
		}
		// Primary versions only (consistent with LibraryStats).
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}

		switch id {
		case "missing_covers":
			if b.CoverURL == nil || *b.CoverURL == "" {
				count++
			}
		case "no_fingerprints":
			if b.FingerprintStatus == "" || b.FingerprintStatus == "none" {
				count++
			}
		case "in_import_path":
			matched := false
			for _, ip := range importPaths {
				if strings.HasPrefix(b.FilePath, ip.Path) {
					matched = true
					break
				}
			}
			if matched {
				count++
			}
		case "no_isbn":
			isbn10 := b.ISBN10 == nil || *b.ISBN10 == ""
			isbn13 := b.ISBN13 == nil || *b.ISBN13 == ""
			if isbn10 && isbn13 {
				count++
			}
		}
	}

	elapsed := time.Since(start).Milliseconds()
	slog.Info("quick_query cache recomputed", "id", id, "count", count, "duration_ms", elapsed)
	return count, nil
}

// computeDuplicatesFlaggedCount counts books that appear in at least one
// EmbeddingStore dedup candidate with status "pending".
// Uses a key-only scan of the dedup_candidate: prefix with full JSON decode
// only for entity_type=book entries. O(candidates) — typically in the hundreds.
func (p *PebbleStore) computeDuplicatesFlaggedCount() (int, error) {
	start := time.Now()

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("dedup_candidate:"),
		UpperBound: []byte("dedup_candidate;"),
	})
	if err != nil {
		// No candidates table yet — not an error.
		slog.Info("quick_query cache recomputed", "id", "duplicates_flagged", "count", 0, "duration_ms", 0)
		return 0, nil
	}
	defer iter.Close()

	type candRec struct {
		EntityType string `json:"entity_type"`
		EntityAID  string `json:"entity_a_id"`
		EntityBID  string `json:"entity_b_id"`
		Status     string `json:"status"`
	}

	flagged := make(map[string]struct{})
	for iter.First(); iter.Valid(); iter.Next() {
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.EntityType != "book" || rec.Status != "pending" {
			continue
		}
		flagged[rec.EntityAID] = struct{}{}
		flagged[rec.EntityBID] = struct{}{}
	}

	elapsed := time.Since(start).Milliseconds()
	slog.Info("quick_query cache recomputed", "id", "duplicates_flagged", "count", len(flagged), "duration_ms", elapsed)
	return len(flagged), nil
}

// GetAllBookIDsForQuickQuery returns the IDs of all non-deleted, primary-version
// books that match the given quick-query id. Supported ids:
// "missing_covers", "no_fingerprints", "in_import_path", "no_isbn", "duplicates_flagged".
// "broken_files" is handled by a separate code-path (ListBooksWithFileErrors) and
// is not supported here; an empty slice is returned for unknown ids.
//
// This is used by the listAudiobooks fast-path so that pagination operates on the
// full matching-ID slice rather than a single page of books.
func (p *PebbleStore) GetAllBookIDsForQuickQuery(id string) ([]string, error) {
	start := time.Now()

	if id == "duplicates_flagged" {
		ids, err := p.getAllBookIDsDuplicatesFlagged()
		slog.Info("quick_query id scan", "id", id, "count", len(ids), "duration_ms", time.Since(start).Milliseconds())
		return ids, err
	}

	// Load import paths once for in_import_path query.
	var importPaths []ImportPath
	if id == "in_import_path" {
		importPaths, _ = p.GetAllImportPaths()
	}

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var ids []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if bookIsSoftDeleted(&b) {
			continue
		}
		// Primary versions only (consistent with LibraryStats and computeQuickQueryCount).
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}

		switch id {
		case "missing_covers":
			if b.CoverURL == nil || *b.CoverURL == "" {
				ids = append(ids, b.ID)
			}
		case "no_fingerprints":
			if b.FingerprintStatus == "" || b.FingerprintStatus == "none" {
				ids = append(ids, b.ID)
			}
		case "in_import_path":
			for _, ip := range importPaths {
				if strings.HasPrefix(b.FilePath, ip.Path) {
					ids = append(ids, b.ID)
					break
				}
			}
		case "no_isbn":
			isbn10 := b.ISBN10 == nil || *b.ISBN10 == ""
			isbn13 := b.ISBN13 == nil || *b.ISBN13 == ""
			if isbn10 && isbn13 {
				ids = append(ids, b.ID)
			}
		}
	}

	slog.Info("quick_query id scan", "id", id, "count", len(ids), "duration_ms", time.Since(start).Milliseconds())
	return ids, nil
}

// getAllBookIDsDuplicatesFlagged returns IDs of books in a "pending" dedup candidate.
func (p *PebbleStore) getAllBookIDsDuplicatesFlagged() ([]string, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("dedup_candidate:"),
		UpperBound: []byte("dedup_candidate;"),
	})
	if err != nil {
		return nil, nil //nolint:nilerr // no candidates table yet
	}
	defer iter.Close()

	type candRec struct {
		EntityType string `json:"entity_type"`
		EntityAID  string `json:"entity_a_id"`
		EntityBID  string `json:"entity_b_id"`
		Status     string `json:"status"`
	}

	flagged := make(map[string]struct{})
	for iter.First(); iter.Valid(); iter.Next() {
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.EntityType != "book" || rec.Status != "pending" {
			continue
		}
		flagged[rec.EntityAID] = struct{}{}
		flagged[rec.EntityBID] = struct{}{}
	}

	ids := make([]string, 0, len(flagged))
	for id := range flagged {
		ids = append(ids, id)
	}
	return ids, nil
}

// GetQuickQueryCounts returns the six preset quick-query results. Each entry's count
// is served from the per-query PebbleDB cache when fresh; stale or dirty entries are
// recomputed inline. broken_files is sourced from the existing LibraryStats cache so
// there is no separate cache entry for it.
func (p *PebbleStore) GetQuickQueryCounts() ([]QuickQueryResult, error) {
	// Load LibraryStats once for broken_files (already cached).
	var brokenFiles int
	if stats, err := p.GetDashboardStats(); err == nil {
		brokenFiles = stats.BrokenFiles
	}

	results := make([]QuickQueryResult, 0, len(quickQueryDefs))
	for _, def := range quickQueryDefs {
		var count int

		if def.id == "broken_files" {
			// Reuse LibraryStats; no per-query cache entry.
			count = brokenFiles
			slog.Info("quick_query cache hit (from stats:library)", "id", def.id, "count", count)
		} else if cached := p.readCachedQuickQuery(def.id); cached != nil {
			count = cached.Count
			slog.Info("quick_query cache hit",
				"id", def.id,
				"count", cached.Count,
				"age_seconds", time.Since(cached.ComputedAt).Seconds(),
			)
		} else {
			// Cache miss or dirty — recompute.
			var err error
			if def.id == "duplicates_flagged" {
				count, err = p.computeDuplicatesFlaggedCount()
			} else {
				count, err = p.computeQuickQueryCount(def.id)
			}
			if err != nil {
				slog.Warn("quick_query recompute failed", "id", def.id, "error", err)
				count = 0
			}
			p.writeCachedQuickQuery(def.id, count)
		}

		results = append(results, QuickQueryResult{
			ID:     def.id,
			Label:  def.label,
			Count:  count,
			Filter: def.filter,
		})
	}
	return results, nil
}
