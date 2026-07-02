// file: internal/database/nuts_activity_store.go
// version: 1.4.1
// guid: c3d4e5f6-a7b8-0003-cdef-000000000003
// last-edited: 2026-07-01

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nutsdb/nutsdb"
	"github.com/oklog/ulid/v2"
)

// isNutsEmptyScan returns true for the family of nutsdb errors that all
// mean "no entries to scan" — none of which should be propagated as a
// 500 error to the activity-log API. nutsdb v1.1.0 has THREE relevant
// sentinels (note the two near-identical names):
//
//   - ErrBucketNotFound ("bucket not found")  — tx_bucket.go etc.
//   - ErrNotFoundBucket ("bucket not found")  — tx_btree.go RangeScanEntries
//   - ErrRangeScan      ("range scans not found") — RangeScanEntries when
//     the bucket exists but yields zero results
//   - ErrBucketEmpty    — the (older) empty-bucket sentinel.
//
// nutsdb.IsBucketNotFound only matches the first. Without this helper, a
// fresh activity store (no buckets created yet) returns 500 on every
// /api/v1/activity request — because Query() → scanTier() →
// RangeScanEntries returns ErrNotFoundBucket which IsBucketNotFound
// doesn't catch. Same applies on the first request to a freshly-deployed
// server when a tier bucket has never had a write committed.
func isNutsEmptyScan(err error) bool {
	if err == nil {
		return false
	}
	return nutsdb.IsBucketNotFound(err) ||
		nutsdb.IsBucketEmpty(err) ||
		errors.Is(err, nutsdb.ErrNotFoundBucket) ||
		errors.Is(err, nutsdb.ErrRangeScan)
}

// NutsActivityStore persists activity log entries in a NutsDB directory.
// It is a drop-in replacement for ActivityStore (SQLite).
//
// Key design:
//
//	Tier buckets:  "act:<tier>"  key = <20-digit-unix-nano>:<ulid>  value = JSON(ActivityEntry)
//	Op index:      "act:op:<op_id>"   key = <timekey>  value = <tier>:<timekey>
//	Book index:    "act:bk:<book_id>" key = <timekey>  value = <tier>:<timekey>
//
// Entries are keyed by unix nanoseconds (zero-padded to 20 digits) so
// RangeScan and reverse iteration naturally produce time-ordered results.
type NutsActivityStore struct {
	db      *nutsdb.DB
	counter atomic.Int64
}

func actBucket(tier string) string       { return "act:" + tier }
func actOpBucket(opID string) string     { return "act:op:" + opID }
func actBookBucket(bookID string) string { return "act:bk:" + bookID }

// actTiers is the full list of tier buckets used for queries, sources, wipe,
// and compaction. digest must be last (it is excluded from compaction but
// included in queries). Add new tiers here when introducing them.
var actTiers = []string{"change", "debug", "audit", "info", "batch", "system", "digest"}

// actCompactableTiers returns all tiers eligible for compaction (everything
// except digest, which is the compaction output and must not be re-compacted).
func actCompactableTiers() []string { return actTiers[:len(actTiers)-1] }

func actTimeKey(t time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%020d:%s", t.UnixNano(), id))
}

// NewNutsActivityStore opens (or creates) a NutsDB activity store at dirPath.
func NewNutsActivityStore(dirPath string) (*NutsActivityStore, error) {
	opts := nutsdb.DefaultOptions
	opts.Dir = dirPath
	opts.EntryIdxMode = nutsdb.HintKeyAndRAMIdxMode
	opts.SyncEnable = false
	opts.GCWhenClose = true
	opts.MergeInterval = 6 * time.Hour
	opts.SegmentSize = 256 << 20 // 256 MB segments for the append-heavy log

	db, err := nutsdb.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("nuts_activity_store: open %q: %w", dirPath, err)
	}
	s := &NutsActivityStore{db: db}
	// Seed counter from current time so IDs don't collide across restarts.
	s.counter.Store(time.Now().UnixNano())
	return s, nil
}

// Close shuts down the underlying NutsDB.
// Close shuts down the underlying NutsDB handle. Note: nutsdb v1.1.0 starts a
// background TTL time-wheel goroutine on Open() (db.go: `go db.tm.run()`)
// regardless of whether TTL/expiry is ever used — it is not, here (every
// tx.Put(...) call in this file passes 0 for the TTL argument). db.Close()
// already calls tm.close() internally to stop the time-wheel; there is no
// additional goroutine owned by this file that needs signaling. This is
// benign in prod (the activity store is a process-lifetime singleton, see
// internal/activity/register.go) — it only accumulates goroutines in
// short-lived test opens. See TODO.md NUTSDB-CLOSE-GOROUTINE-LEAK. Do NOT
// fork/upgrade nutsdb to chase this further (nuts_activity_store.go is
// coupled to v1.1.0-specific error sentinels).
func (s *NutsActivityStore) Close() error { return s.db.Close() }

// Record inserts an ActivityEntry and returns a synthetic int64 ID.
func (s *NutsActivityStore) Record(e ActivityEntry) (int64, error) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	if e.Tier == "" {
		e.Tier = "change"
	}

	id := s.counter.Add(1)
	e.ID = id

	entryID := ulid.Make().String()
	key := actTimeKey(e.Timestamp, entryID)

	b, err := json.Marshal(e)
	if err != nil {
		return 0, fmt.Errorf("activity_store: marshal: %w", err)
	}

	return id, s.db.Update(func(tx *nutsdb.Tx) error {
		bucket := actBucket(e.Tier)
		if err := ensureBucket(tx, bucket); err != nil {
			return err
		}
		if err := tx.Put(bucket, key, b, 0); err != nil {
			return fmt.Errorf("put entry: %w", err)
		}
		// Secondary index: op_id
		if e.OperationID != "" {
			ref := []byte(e.Tier + ":" + string(key))
			opBucket := actOpBucket(e.OperationID)
			if err := ensureBucket(tx, opBucket); err != nil {
				return err
			}
			if err := tx.Put(opBucket, key, ref, 0); err != nil {
				return err
			}
		}
		// Secondary index: book_id
		if e.BookID != "" {
			ref := []byte(e.Tier + ":" + string(key))
			bookBucket := actBookBucket(e.BookID)
			if err := ensureBucket(tx, bookBucket); err != nil {
				return err
			}
			if err := tx.Put(bookBucket, key, ref, 0); err != nil {
				return err
			}
		}
		return nil
	})
}

// Query returns entries matching f, newest-first, plus the total matching count.
func (s *NutsActivityStore) Query(f ActivityFilter) ([]ActivityEntry, int, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}

	// Fast path: op_id or book_id filter → use secondary index.
	if f.OperationID != "" {
		return s.queryByIndex(actOpBucket(f.OperationID), f)
	}
	if f.BookID != "" {
		return s.queryByIndex(actBookBucket(f.BookID), f)
	}

	// General path: scan tier bucket(s) by time range.
	tiers := actTiers
	if f.Tier != "" {
		tiers = []string{f.Tier}
	}

	var all []ActivityEntry
	for _, tier := range tiers {
		entries, err := s.scanTier(tier, f.Since, f.Until)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, entries...)
	}

	// Apply remaining filters in Go.
	filtered := make([]ActivityEntry, 0, len(all))
	for _, e := range all {
		if matchesFilter(e, f) {
			filtered = append(filtered, e)
		}
	}

	// Sort newest-first (compacted/digest entries last, matching SQL ORDER BY compacted ASC, timestamp DESC).
	sort.Slice(filtered, func(i, j int) bool {
		ci := boolInt(filtered[i].Tier == "digest")
		cj := boolInt(filtered[j].Tier == "digest")
		if ci != cj {
			return ci < cj
		}
		return filtered[i].Timestamp.After(filtered[j].Timestamp)
	})

	total := len(filtered)

	// Paginate.
	start := f.Offset
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + f.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], total, nil
}

// Summarize groups old entries by (operation_id, type), writes a summary row,
// and deletes the originals. Returns count of deleted rows.
func (s *NutsActivityStore) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	entries, err := s.scanTier(tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}

	type groupKey struct{ opID, typ string }
	type group struct {
		entries []ActivityEntry
	}
	groups := make(map[groupKey]*group)

	for _, e := range entries {
		if e.PrunedAt != nil {
			continue
		}
		k := groupKey{opID: e.OperationID, typ: e.Type}
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].entries = append(groups[k].entries, e)
	}
	if len(groups) == 0 {
		return 0, nil
	}

	// Rebuild key lookup for deletes.
	keyLookup, err := s.scanTierKeysAndValues(tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}

	totalDeleted := 0
	now := time.Now().UTC()

	for gk, g := range groups {
		select {
		case <-ctx.Done():
			return totalDeleted, ctx.Err()
		default:
		}

		first := g.entries[0].Timestamp
		last := g.entries[len(g.entries)-1].Timestamp
		summaryText := fmt.Sprintf("Summary: %d %s entries (%s to %s)",
			len(g.entries), gk.typ,
			first.Format(time.RFC3339), last.Format(time.RFC3339),
		)

		summary := ActivityEntry{
			ID:          s.counter.Add(1),
			Timestamp:   now,
			Tier:        tier,
			Type:        gk.typ,
			Level:       "info",
			Source:      "summarize",
			OperationID: gk.opID,
			Summary:     summaryText,
		}
		summaryKey := actTimeKey(now, ulid.Make().String())
		summaryBytes, err := json.Marshal(summary)
		if err != nil {
			return totalDeleted, err
		}

		// Collect keys to delete from primary bucket and indexes.
		var keysToDelete [][]byte
		for _, e := range g.entries {
			for _, kv := range keyLookup {
				if kv.entry.ID == e.ID {
					keysToDelete = append(keysToDelete, kv.key)
					break
				}
			}
		}

		if err := s.db.Update(func(tx *nutsdb.Tx) error {
			bucket := actBucket(tier)
			if err := ensureBucket(tx, bucket); err != nil {
				return err
			}
			if err := tx.Put(bucket, summaryKey, summaryBytes, 0); err != nil {
				return err
			}
			for _, k := range keysToDelete {
				_ = tx.Delete(bucket, k)
			}
			return nil
		}); err != nil {
			return totalDeleted, fmt.Errorf("summarize commit: %w", err)
		}
		totalDeleted += len(keysToDelete)
	}
	return totalDeleted, nil
}

// Prune hard-deletes all entries of the given tier older than olderThan.
func (s *NutsActivityStore) Prune(olderThan time.Time, tier string) (int, error) {
	kvs, err := s.scanTierKeysAndValues(tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}
	if len(kvs) == 0 {
		return 0, nil
	}

	bucket := actBucket(tier)
	deleted := 0
	// Delete in batches of 500 to keep transaction size reasonable.
	for i := 0; i < len(kvs); i += 500 {
		end := i + 500
		if end > len(kvs) {
			end = len(kvs)
		}
		batch := kvs[i:end]
		if err := s.db.Update(func(tx *nutsdb.Tx) error {
			for _, kv := range batch {
				if err := tx.Delete(bucket, kv.key); err != nil && !nutsdb.IsKeyNotFound(err) {
					return err
				}
			}
			return nil
		}); err != nil {
			return deleted, fmt.Errorf("prune batch: %w", err)
		}
		deleted += len(batch)
	}
	return deleted, nil
}

// GetDistinctSources returns unique sources with entry counts, ordered by count desc.
func (s *NutsActivityStore) GetDistinctSources(f ActivityFilter) ([]SourceCount, error) {
	tiers := actTiers
	if f.Tier != "" {
		tiers = []string{f.Tier}
	}
	counts := make(map[string]int)
	for _, tier := range tiers {
		entries, err := s.scanTier(tier, f.Since, f.Until)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if matchesFilter(e, f) {
				counts[e.Source]++
			}
		}
	}
	out := make([]SourceCount, 0, len(counts))
	for src, cnt := range counts {
		out = append(out, SourceCount{Source: src, Count: cnt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// WipeAllActivity deletes every entry from all tier buckets. Returns total count.
func (s *NutsActivityStore) WipeAllActivity() (int64, error) {
	var total int64
	for _, tier := range actTiers {
		kvs, err := s.scanTierKeysAndValues(tier, nil, nil)
		if err != nil {
			return total, err
		}
		bucket := actBucket(tier)
		for i := 0; i < len(kvs); i += 500 {
			end := i + 500
			if end > len(kvs) {
				end = len(kvs)
			}
			batch := kvs[i:end]
			if err := s.db.Update(func(tx *nutsdb.Tx) error {
				for _, kv := range batch {
					if err := tx.Delete(bucket, kv.key); err != nil && !nutsdb.IsKeyNotFound(err) {
						return err
					}
				}
				return nil
			}); err != nil {
				return total, fmt.Errorf("wipe batch: %w", err)
			}
			total += int64(len(batch))
		}
	}
	return total, nil
}

// CompactByDay collapses all compactable tier entries into daily digest rows.
// Every tier except "digest" is eligible — the denylist approach means newly-
// introduced tiers are automatically compacted without an allowlist update.
// Each day is processed atomically.
func (s *NutsActivityStore) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	var result CompactResult

	// Load all compactable entries (all tiers except "digest").
	var all []ActivityEntry
	for _, tier := range actCompactableTiers() {
		entries, err := s.scanTier(tier, nil, &olderThan)
		if err != nil {
			return result, err
		}
		all = append(all, entries...)
	}
	if len(all) == 0 {
		return result, nil
	}

	// Group by date.
	type dayGroup struct {
		entries []ActivityEntry
	}
	days := make(map[string]*dayGroup)
	var dayOrder []string
	for _, e := range all {
		dk := e.Timestamp.UTC().Format("2006-01-02")
		if _, ok := days[dk]; !ok {
			days[dk] = &dayGroup{}
			dayOrder = append(dayOrder, dk)
		}
		days[dk].entries = append(days[dk].entries, e)
	}
	sort.Strings(dayOrder)

	// Build key lookup for bulk deletes.
	keyLookup := make(map[int64]entryKV)
	for _, tier := range actCompactableTiers() {
		kvs, err := s.scanTierKeysAndValues(tier, nil, &olderThan)
		if err != nil {
			return result, err
		}
		for _, kv := range kvs {
			keyLookup[kv.entry.ID] = kv
		}
	}

	for _, dateKey := range dayOrder {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		dg := days[dateKey]
		counts := make(map[string]int)
		for _, e := range dg.entries {
			counts[e.Type]++
		}

		var auditItems, errItems, normalItems []DigestItem
		for _, e := range dg.entries {
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
			switch {
			case e.Tier == "audit":
				auditItems = append(auditItems, item)
			case e.Level == "error" || e.Level == "warn":
				item.Details = extractErrorDetails(e)
				errItems = append(errItems, item)
			default:
				normalItems = append(normalItems, item)
			}
		}
		items := append(auditItems, errItems...)
		items = append(items, normalItems...)

		truncated := false
		truncatedCount := 0
		if len(items) > maxDigestItems {
			truncatedCount = len(items) - maxDigestItems
			items = items[:maxDigestItems]
			truncated = true
		}

		dd := DigestDetails{
			Date:           dateKey,
			OriginalCount:  len(dg.entries),
			Counts:         counts,
			Items:          items,
			Truncated:      truncated,
			TruncatedCount: truncatedCount,
		}

		// Check for existing digest for this date to merge into.
		existing, existingKey, err := s.findExistingDigest(dateKey)
		if err != nil {
			return result, err
		}
		if existingKey != nil {
			// Merge existing digest into dd.
			for k, v := range existing.Counts {
				dd.Counts[k] += v
			}
			dd.OriginalCount += existing.OriginalCount
			combined := append(existing.Items, dd.Items...)
			if existing.Truncated {
				dd.Truncated = true
				dd.TruncatedCount += existing.TruncatedCount
			}
			if len(combined) > maxDigestItems {
				dd.TruncatedCount += len(combined) - maxDigestItems
				combined = combined[:maxDigestItems]
				dd.Truncated = true
			}
			dd.Items = combined
		}

		detailsBytes, err := json.Marshal(dd)
		if err != nil {
			return result, fmt.Errorf("compact marshal: %w", err)
		}

		startOfDay, err := time.Parse("2006-01-02", dateKey)
		if err != nil {
			return result, fmt.Errorf("compact parse date: %w", err)
		}

		// Populate Details directly so it survives the ActivityEntry round-trip.
		// The digestWithDetails workaround stored details under "digest_details"
		// at the top level, which json.Unmarshal into ActivityEntry silently
		// dropped — leaving details nil on every query.
		var ddMap map[string]any
		if mapErr := json.Unmarshal(detailsBytes, &ddMap); mapErr != nil {
			return result, fmt.Errorf("compact unmarshal dd map: %w", mapErr)
		}
		digest := ActivityEntry{
			ID:        s.counter.Add(1),
			Timestamp: startOfDay,
			Tier:      "digest",
			Type:      "daily_digest",
			Level:     "info",
			Source:    "compaction",
			Summary:   fmt.Sprintf("Daily digest for %s (%d entries)", dateKey, dd.OriginalCount),
			Details:   ddMap,
		}
		digestKey := actTimeKey(startOfDay, ulid.Make().String())
		digestBytes, err := json.Marshal(digest)
		if err != nil {
			return result, fmt.Errorf("compact marshal digest: %w", err)
		}

		var deletedCount int
		if err := s.db.Update(func(tx *nutsdb.Tx) error {
			if err := ensureBucket(tx, actBucket("digest")); err != nil {
				return err
			}
			// Delete old digest if present.
			if existingKey != nil {
				_ = tx.Delete(actBucket("digest"), existingKey)
			}
			// Write new digest.
			if err := tx.Put(actBucket("digest"), digestKey, digestBytes, 0); err != nil {
				return err
			}
			// Delete originals.
			for _, e := range dg.entries {
				if kv, ok := keyLookup[e.ID]; ok {
					if err := tx.Delete(actBucket(e.Tier), kv.key); err != nil && !nutsdb.IsKeyNotFound(err) {
						return err
					}
					deletedCount++
				}
			}
			return nil
		}); err != nil {
			return result, fmt.Errorf("compact commit day %s: %w", dateKey, err)
		}

		result.DaysCompacted++
		result.EntriesDeleted += deletedCount
	}
	return result, nil
}

// MigrateSystemActivityLogs is a no-op for NutsActivityStore since it's not backed by SQLite.
// It returns 0 entries migrated (they're already in the unified store).
func (s *NutsActivityStore) MigrateSystemActivityLogs() (int, error) {
	return 0, nil
}

// RecompactDigests re-derives type, tier, and tags on every stored daily-digest
// entry in NutsDB whose items were compacted before enrichment was added.
// It mirrors ActivityStore.RecompactDigests but uses NutsDB iteration instead of SQL.
//
// Algorithm:
//  1. Range-scan the entire "act:digest" bucket (all keys).
//  2. Decode each entry's Details map as DigestDetails.
//  3. Skip entries where no items are legacy (idempotent guard).
//  4. For each legacy item: call deriveTypeFromMessage + enrichLegacyLogTags.
//  5. Rebuild Counts and TagCounts, marshal back, and write with the same key.
func (s *NutsActivityStore) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	var result RecompactResult

	// Collect all digest keys+values first so we can update outside the View tx.
	type digestKV struct {
		key   []byte
		entry ActivityEntry
		dd    DigestDetails
	}

	var candidates []digestKV

	err := s.db.View(func(tx *nutsdb.Tx) error {
		keys, vals, err := tx.RangeScanEntries(
			actBucket("digest"),
			[]byte("00000000000000000000:"),
			[]byte("99999999999999999999:\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"),
			true, true,
		)
		if err != nil {
			if isNutsEmptyScan(err) {
				return nil
			}
			return err
		}
		for i, v := range vals {
			var e ActivityEntry
			if jsonErr := json.Unmarshal(v, &e); jsonErr != nil {
				continue
			}
			if e.Type != "daily_digest" {
				continue
			}
			// Decode Details map into DigestDetails.
			var dd DigestDetails
			if e.Details != nil {
				if b, merr := json.Marshal(e.Details); merr == nil {
					_ = json.Unmarshal(b, &dd)
				}
			}
			candidates = append(candidates, digestKV{key: keys[i], entry: e, dd: dd})
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("nuts_activity_store: recompact scan digests: %w", err)
	}

	slog.Info("[activity] recompact: starting digest re-derivation (nuts)",
		"digest_count", len(candidates))

	for _, c := range candidates {
		// Check if any items need updating.
		needsUpdate := false
		for _, item := range c.dd.Items {
			if isLegacyItem(item) {
				needsUpdate = true
				break
			}
		}
		if !needsUpdate {
			result.Skipped++
			continue
		}

		// Re-derive type, tier, and tags on each legacy item.
		for i, item := range c.dd.Items {
			if !isLegacyItem(item) {
				continue
			}
			derivedType, derivedTier := deriveTypeFromMessage(item.Summary, "")
			derivedTags := enrichLegacyLogTags(item.Summary, "", "info")
			c.dd.Items[i].Type = derivedType
			c.dd.Items[i].Tier = derivedTier
			c.dd.Items[i].Tags = derivedTags
		}

		// Rebuild Counts from updated items.
		newCounts := make(map[string]int)
		for _, item := range c.dd.Items {
			newCounts[item.Type]++
		}
		c.dd.Counts = newCounts

		// Rebuild TagCounts from updated items.
		newTagCounts := make(map[string]map[string]int)
		for _, item := range c.dd.Items {
			for _, tag := range item.Tags {
				colonIdx := strings.Index(tag, ":")
				if colonIdx < 1 {
					continue
				}
				ns := tag[:colonIdx]
				val := tag[colonIdx+1:]
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
			c.dd.TagCounts = newTagCounts
		}

		// Merge updated DigestDetails back into the entry's Details map.
		ddBytes, merr := json.Marshal(c.dd)
		if merr != nil {
			return result, fmt.Errorf("nuts_activity_store: recompact marshal digest key=%s: %w", c.key, merr)
		}
		var detailsMap map[string]any
		if err := json.Unmarshal(ddBytes, &detailsMap); err != nil {
			return result, fmt.Errorf("nuts_activity_store: recompact unmarshal detailsMap key=%s: %w", c.key, err)
		}
		c.entry.Details = detailsMap
		c.entry.Summary = fmt.Sprintf("Daily digest for %s (%d entries)", c.dd.Date, c.dd.OriginalCount)

		entryBytes, merr := json.Marshal(c.entry)
		if merr != nil {
			return result, fmt.Errorf("nuts_activity_store: recompact marshal entry key=%s: %w", c.key, merr)
		}

		key := c.key // capture for closure
		if uerr := s.db.Update(func(tx *nutsdb.Tx) error {
			return tx.Put(actBucket("digest"), key, entryBytes, 0)
		}); uerr != nil {
			return result, fmt.Errorf("nuts_activity_store: recompact write key=%s: %w", key, uerr)
		}

		slog.Info("[activity] recompact: updated digest (nuts)",
			"key", string(c.key), "date", c.dd.Date, "items", len(c.dd.Items))
		result.Touched++
	}

	slog.Info("[activity] recompact: complete (nuts)",
		"touched", result.Touched, "skipped", result.Skipped)
	return result, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

type entryKV struct {
	key   []byte
	entry ActivityEntry
}

// scanTier returns all entries from a tier bucket within [since, until].
// nil bounds mean "no bound". Results are in ascending timestamp order.
func (s *NutsActivityStore) scanTier(tier string, since, until *time.Time) ([]ActivityEntry, error) {
	kvs, err := s.scanTierKeysAndValues(tier, since, until)
	if err != nil {
		return nil, err
	}
	entries := make([]ActivityEntry, len(kvs))
	for i, kv := range kvs {
		entries[i] = kv.entry
	}
	return entries, nil
}

// scanTierKeysAndValues returns key+entry pairs for a tier within the time range.
func (s *NutsActivityStore) scanTierKeysAndValues(tier string, since, until *time.Time) ([]entryKV, error) {
	bucket := actBucket(tier)

	start := []byte("00000000000000000000:")
	end := []byte("99999999999999999999:\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff")
	if since != nil {
		start = actTimeKey(*since, "")
	}
	if until != nil {
		end = actTimeKey(*until, "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff")
	}

	var out []entryKV
	err := s.db.View(func(tx *nutsdb.Tx) error {
		keys, vals, err := tx.RangeScanEntries(bucket, start, end, true, true)
		if err != nil {
			if isNutsEmptyScan(err) {
				return nil
			}
			return err
		}
		for i, v := range vals {
			var e ActivityEntry
			if jsonErr := json.Unmarshal(v, &e); jsonErr != nil {
				continue
			}
			out = append(out, entryKV{key: keys[i], entry: e})
		}
		return nil
	})
	return out, err
}

// queryByIndex fetches entries referenced by a secondary index bucket.
func (s *NutsActivityStore) queryByIndex(indexBucket string, f ActivityFilter) ([]ActivityEntry, int, error) {
	var refs [][]byte
	_ = s.db.View(func(tx *nutsdb.Tx) error {
		_, vals, err := tx.RangeScanEntries(indexBucket,
			[]byte("00000000000000000000:"),
			[]byte("99999999999999999999:\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"),
			false, true,
		)
		if err != nil {
			if isNutsEmptyScan(err) {
				return nil
			}
			return err
		}
		refs = vals
		return nil
	})

	var all []ActivityEntry
	for _, ref := range refs {
		// ref = "<tier>:<timekey>"
		parts := strings.SplitN(string(ref), ":", 2)
		if len(parts) != 2 {
			continue
		}
		tier, timekey := parts[0], parts[1]
		var entry ActivityEntry
		err := s.db.View(func(tx *nutsdb.Tx) error {
			v, err := tx.Get(actBucket(tier), []byte(timekey))
			if err != nil {
				return err
			}
			return json.Unmarshal(v, &entry)
		})
		if err != nil {
			continue
		}
		if matchesFilter(entry, f) {
			all = append(all, entry)
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})

	total := len(all)
	start := f.Offset
	if start > len(all) {
		start = len(all)
	}
	end := start + f.Limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

// findExistingDigest looks for a digest row for the given date string ("2006-01-02").
func (s *NutsActivityStore) findExistingDigest(dateKey string) (DigestDetails, []byte, error) {
	day, err := time.Parse("2006-01-02", dateKey)
	if err != nil {
		return DigestDetails{}, nil, err
	}
	dayEnd := day.Add(24 * time.Hour)

	var foundDD DigestDetails
	var foundKey []byte

	_ = s.db.View(func(tx *nutsdb.Tx) error {
		keys, vals, err := tx.RangeScanEntries(actBucket("digest"),
			actTimeKey(day, ""),
			actTimeKey(dayEnd, ""),
			true, true,
		)
		if err != nil {
			if isNutsEmptyScan(err) {
				return nil
			}
			return err
		}
		for i, v := range vals {
			var row struct {
				ActivityEntry
				// Legacy field: digest details were stored here before the fix.
				DigestDetails json.RawMessage `json:"digest_details,omitempty"`
			}
			if jsonErr := json.Unmarshal(v, &row); jsonErr != nil {
				continue
			}
			if row.ActivityEntry.Type == "daily_digest" {
				if row.DigestDetails != nil {
					// Old format: digest_details at top level.
					_ = json.Unmarshal(row.DigestDetails, &foundDD)
				} else if row.ActivityEntry.Details != nil {
					// New format: stored in the ActivityEntry.Details map.
					if b, merr := json.Marshal(row.ActivityEntry.Details); merr == nil {
						_ = json.Unmarshal(b, &foundDD)
					}
				}
				foundKey = keys[i]
				return nil // take the first one
			}
		}
		return nil
	})
	return foundDD, foundKey, nil
}

// matchesFilter returns true if entry e satisfies all non-time fields in f.
// Time filtering is handled by scanTier's range scan.
func matchesFilter(e ActivityEntry, f ActivityFilter) bool {
	if f.Tier != "" && e.Tier != f.Tier {
		return false
	}
	if f.Type != "" && e.Type != f.Type {
		return false
	}
	if f.Level != "" && e.Level != f.Level {
		return false
	}
	if f.Source != "" && e.Source != f.Source {
		return false
	}
	if f.OperationID != "" && e.OperationID != f.OperationID {
		return false
	}
	if f.BookID != "" && e.BookID != f.BookID {
		return false
	}
	if f.Search != "" && !strings.Contains(e.Summary, f.Search) {
		return false
	}
	for _, tag := range f.Tags {
		if !containsTag(e.Tags, tag) {
			return false
		}
	}
	for _, src := range f.ExcludeSources {
		if e.Source == src {
			return false
		}
	}
	for _, tier := range f.ExcludeTiers {
		if e.Tier == tier {
			return false
		}
	}
	for _, tag := range f.ExcludeTags {
		if containsTag(e.Tags, tag) {
			return false
		}
	}
	return true
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
