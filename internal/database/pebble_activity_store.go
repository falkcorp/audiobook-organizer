// file: internal/database/pebble_activity_store.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0004-def0-000000000004
// last-edited: 2026-08-11

// Package database — PebbleDB-backed activity log store.
//
// WHY a Pebble backend:
//   - The NutsDB activity store (nuts_activity_store.go) works but carries an
//     entire extra storage engine dependency (nutsdb/nutsdb). Pebble is already
//     the primary database engine. Removing NutsDB requires a Pebble backend
//     that satisfies the same ActivityStorer interface.
//   - Key layout mirrors NutsDB verbatim so lexicographic ordering and range
//     scans behave identically (no behavioral change to callers).
//
// Key layout (all keys are []byte; prefixes end with ':'):
//
//	act:<tier>:<20d-unix-nano>:<ulid>        = JSON(ActivityEntry)   primary
//	act:op:<op_id>:<20d-unix-nano>:<ulid>    = []byte("<tier>:<20d-unix-nano>:<ulid>")  op index
//	act:bk:<book_id>:<20d-unix-nano>:<ulid>  = []byte("<tier>:<20d-unix-nano>:<ulid>")  book index
//
// Compared with NutsDB, Pebble is a single shared key-space so every key is
// scoped with "act:" to avoid collisions with other prefixes. The "tier" component
// in the primary key separates tiers without needing separate buckets; Pebble range
// scans over ["act:<tier>:", "act:<tier>;") return exactly that tier's entries in
// timestamp order because ';' (0x3B) is one above ':' (0x3A) in ASCII.
package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
)

// Tunables for the bounded query path. These are vars (not consts) so tests can
// shrink them and exercise the bound without seeding a huge store.
var (
	// activityQueryScanBudget caps how many stored entries a single Query or
	// GetDistinctSources call will decode. Before this bound existed, a default
	// Query decoded EVERY entry in the log (8.86 GB of heap on production,
	// 71.9% of the process, and a 120s non-returning GET /api/v1/activity?limit=5).
	// Hitting the bound is logged, never silent.
	activityQueryScanBudget = 20000

	// activitySourcesCacheTTL is how long a GetDistinctSources result is reused.
	// The source list feeds a filter picker, so slightly stale counts are fine;
	// re-scanning on every page load was half the OOM.
	activitySourcesCacheTTL = 45 * time.Second
)

// PebbleActivityStore persists activity log entries in a shared PebbleDB database.
// It satisfies the ActivityStorer interface and is a drop-in replacement for both
// ActivityStore (SQLite) and NutsActivityStore.
//
// The caller retains ownership of the *pebble.DB — Close() on this store is a no-op.
type PebbleActivityStore struct {
	db      *pebble.DB
	counter atomic.Int64

	// entriesDecoded counts every attempt to json.Unmarshal a stored
	// ActivityEntry made by ANY scan path on this store (bounded query,
	// scanTierKVs, ...). It is the instrument that proves a paged query is
	// bounded: it must be incremented at every decode site, otherwise a
	// regression test asserting "few decodes" would pass against an
	// unbounded implementation that simply decodes somewhere else.
	entriesDecoded atomic.Int64

	// decodeFailures counts entries dropped because their stored JSON could
	// not be decoded. Dropped rows are also logged per-scan in aggregate.
	decodeFailures atomic.Int64

	sourcesMu    sync.Mutex
	sourcesCache map[string]pactSourcesCacheEntry
}

// pactSourcesCacheEntry is one memoized GetDistinctSources result.
type pactSourcesCacheEntry struct {
	at  time.Time
	out []SourceCount
}

// NewPebbleActivityStore creates a PebbleActivityStore backed by the provided DB.
// The caller retains ownership of db; Close() on this store does NOT close db.
func NewPebbleActivityStore(db *pebble.DB) *PebbleActivityStore {
	s := &PebbleActivityStore{db: db, sourcesCache: make(map[string]pactSourcesCacheEntry)}
	// Seed counter from current time so IDs don't collide across restarts.
	s.counter.Store(time.Now().UnixNano())
	return s
}

// EntriesDecoded returns the cumulative number of stored-entry JSON decode
// attempts made by this store's scan paths, including failures. Exported for
// tests and for operational assertions about query cost.
func (s *PebbleActivityStore) EntriesDecoded() int64 { return s.entriesDecoded.Load() }

// DecodeFailures returns the cumulative number of stored entries dropped
// because their JSON could not be decoded.
func (s *PebbleActivityStore) DecodeFailures() int64 { return s.decodeFailures.Load() }

// Close is a no-op: the caller owns the PebbleDB instance.
func (s *PebbleActivityStore) Close() error { return nil }

// DB returns the underlying *pebble.DB. Used by callers (e.g., backfill, registry wiring)
// that need to check the backfill sentinel key directly.
func (s *PebbleActivityStore) DB() *pebble.DB { return s.db }

// ── Key construction ──────────────────────────────────────────────────────────

// pactPrimaryKey builds the primary key for a tier entry:
//
//	act:<tier>:<20d-unix-nano>:<ulid>
func pactPrimaryKey(tier string, t time.Time, id string) []byte {
	return []byte(fmt.Sprintf("act:%s:%020d:%s", tier, t.UnixNano(), id))
}

// pactPrimaryPrefix returns the inclusive lower-bound prefix for a tier range scan:
//
//	act:<tier>:
func pactPrimaryPrefix(tier string) []byte {
	return []byte("act:" + tier + ":")
}

// pactPrimaryUpperBound returns the exclusive upper-bound for a tier range scan.
// ';' is ASCII 0x3B — one above ':' (0x3A) — so the range covers exactly all
// entries for the tier.
func pactPrimaryUpperBound(tier string) []byte {
	return []byte("act:" + tier + ";")
}

// pactTierBounds computes the Pebble iterator bounds for one tier's primary key
// range, narrowed by the optional since/until instants.
//
// Extracted so the bounded query path and scanTierKVs share one definition of
// the time-range semantics rather than each keeping its own copy that can drift.
func pactTierBounds(tier string, since, until *time.Time) (lower, upper []byte) {
	lower = pactPrimaryPrefix(tier)
	upper = pactPrimaryUpperBound(tier)
	if since != nil {
		lower = pactPrimaryKey(tier, *since, "")
	}
	if until != nil {
		upper = pactPrimaryKey(tier, *until, "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff")
	}
	return lower, upper
}

// pactKeyTimeField returns the zero-padded 20-digit unix-nano component of a
// primary key ("act:<tier>:<nanos>:<ulid>"), or "" if the key is malformed.
//
// The field is fixed-width and zero-padded, so comparing two of these as
// strings is equivalent to comparing the instants numerically. That is what
// lets the query path merge several tiers newest-first WITHOUT decoding each
// row's JSON just to read its timestamp — only the rows actually taken get
// decoded.
func pactKeyTimeField(key []byte) string {
	s := string(key)
	if !strings.HasPrefix(s, "act:") {
		return ""
	}
	rest := s[len("act:"):]
	i := strings.IndexByte(rest, ':') // end of <tier>
	if i < 0 {
		return ""
	}
	rest = rest[i+1:]
	j := strings.IndexByte(rest, ':') // end of <nanos>
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// pactIndexRef encodes the cross-reference value stored in secondary indexes:
// "<tier>:<20d-unix-nano>:<ulid>" — enough to reconstruct the primary key.
func pactIndexRef(tier string, t time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%s:%020d:%s", tier, t.UnixNano(), id))
}

// pactPrimaryKeyFromRef reconstructs the primary key from an index reference value.
// ref = "<tier>:<20d-unix-nano>:<ulid>"
func pactPrimaryKeyFromRef(ref []byte) ([]byte, bool) {
	s := string(ref)
	if !strings.Contains(s, ":") {
		return nil, false
	}
	return []byte("act:" + s), true
}

// ── ActivityStorer implementation ─────────────────────────────────────────────

// Record inserts an ActivityEntry and returns a synthetic int64 ID.
func (s *PebbleActivityStore) Record(e ActivityEntry) (int64, error) {
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
	primaryKey := pactPrimaryKey(e.Tier, e.Timestamp, entryID)

	b, err := json.Marshal(e)
	if err != nil {
		return 0, fmt.Errorf("pebble_activity_store: marshal: %w", err)
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	if err := batch.Set(primaryKey, b, nil); err != nil {
		return 0, fmt.Errorf("pebble_activity_store: set primary: %w", err)
	}

	// Secondary index: op_id → primary ref
	if e.OperationID != "" {
		opKey := []byte(fmt.Sprintf("act:op:%s:%020d:%s", e.OperationID, e.Timestamp.UnixNano(), entryID))
		ref := pactIndexRef(e.Tier, e.Timestamp, entryID)
		if err := batch.Set(opKey, ref, nil); err != nil {
			return 0, fmt.Errorf("pebble_activity_store: set op index: %w", err)
		}
	}

	// Secondary index: book_id → primary ref
	if e.BookID != "" {
		bkKey := []byte(fmt.Sprintf("act:bk:%s:%020d:%s", e.BookID, e.Timestamp.UnixNano(), entryID))
		ref := pactIndexRef(e.Tier, e.Timestamp, entryID)
		if err := batch.Set(bkKey, ref, nil); err != nil {
			return 0, fmt.Errorf("pebble_activity_store: set book index: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("pebble_activity_store: commit: %w", err)
	}

	return id, nil
}

// Query returns entries matching f, newest-first, plus the total matching count.
func (s *PebbleActivityStore) Query(f ActivityFilter) ([]ActivityEntry, int, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}

	// Fast path: op_id or book_id filter → use secondary index.
	if f.OperationID != "" {
		return s.queryByIndexPrefix(fmt.Sprintf("act:op:%s:", f.OperationID), f)
	}
	if f.BookID != "" {
		return s.queryByIndexPrefix(fmt.Sprintf("act:bk:%s:", f.BookID), f)
	}

	// General path: bounded newest-first merge over the tier key ranges.
	//
	// Activity keys embed the timestamp, so the store can be read in result
	// order and abandoned as soon as the requested page is filled. The previous
	// implementation instead decoded EVERY entry in EVERY tier into one slice,
	// filtered it, sorted it, and then sliced out the page — 8.86 GB of heap on
	// production for a limit=5 request, and the cause of the OOM kills.
	nonDigest, digestTiers := pactSelectTiers(f)

	// Collect one match past the requested page. That extra row is what tells
	// the caller another page exists without counting the rest of the log.
	probe := f.Offset + f.Limit + 1

	pageCap := f.Limit
	if pageCap < 0 {
		pageCap = 0
	}
	page := make([]ActivityEntry, 0, pageCap)
	matched := 0
	collect := func(e ActivityEntry) bool {
		matched++
		if matched > f.Offset && len(page) < f.Limit {
			page = append(page, e)
		}
		return matched < probe
	}

	examined := 0
	exhausted := true
	for _, phase := range [][]string{nonDigest, digestTiers} {
		if len(phase) == 0 {
			continue
		}
		remaining := activityQueryScanBudget - examined
		if matched >= probe || remaining <= 0 {
			exhausted = false
			break
		}
		ex, phaseExhausted, err := s.scanNewestFirst(phase, f, remaining, "query", collect)
		examined += ex
		if err != nil {
			return nil, 0, err
		}
		if !phaseExhausted {
			exhausted = false
			break
		}
	}

	// total is exact when the walk drained the input. Otherwise it is a lower
	// bound: either it equals probe (one row past this page, so pagination
	// still advances) or the scan budget cut the walk short.
	total := matched
	if !exhausted && matched < probe {
		slog.Warn("[activity] query hit the scan budget; total is a lower bound and older matches were not examined",
			"budget", activityQueryScanBudget,
			"examined", examined,
			"matched", matched,
			"limit", f.Limit,
			"offset", f.Offset,
			"tier", f.Tier,
			"type", f.Type,
			"level", f.Level,
			"source", f.Source,
			"search", f.Search)
	}
	return page, total, nil
}

// Summarize groups old entries by (operation_id, type), writes a summary row,
// and deletes the originals. Returns count of deleted rows.
func (s *PebbleActivityStore) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	kvs, err := s.scanTierKVs(tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}

	type groupKey struct{ opID, typ string }
	type group struct{ kvs []pactKV }
	groups := make(map[groupKey]*group)

	for _, kv := range kvs {
		if kv.entry.PrunedAt != nil {
			continue
		}
		k := groupKey{opID: kv.entry.OperationID, typ: kv.entry.Type}
		if groups[k] == nil {
			groups[k] = &group{}
		}
		groups[k].kvs = append(groups[k].kvs, kv)
	}
	if len(groups) == 0 {
		return 0, nil
	}

	totalDeleted := 0
	now := time.Now().UTC()

	for gk, g := range groups {
		select {
		case <-ctx.Done():
			return totalDeleted, ctx.Err()
		default:
		}

		entries := make([]ActivityEntry, len(g.kvs))
		for i, kv := range g.kvs {
			entries[i] = kv.entry
		}

		first := entries[0].Timestamp
		last := entries[len(entries)-1].Timestamp
		summaryText := fmt.Sprintf("Summary: %d %s entries (%s to %s)",
			len(entries), gk.typ,
			first.Format(time.RFC3339), last.Format(time.RFC3339),
		)

		prunedAt := now
		summary := ActivityEntry{
			ID:          s.counter.Add(1),
			Timestamp:   now,
			Tier:        tier,
			Type:        gk.typ,
			Level:       "info",
			Source:      "summarize",
			OperationID: gk.opID,
			Summary:     summaryText,
			PrunedAt:    &prunedAt,
		}

		summaryID := ulid.Make().String()
		summaryKey := pactPrimaryKey(tier, now, summaryID)
		summaryBytes, err := json.Marshal(summary)
		if err != nil {
			return totalDeleted, err
		}

		batch := s.db.NewBatch()
		if setErr := batch.Set(summaryKey, summaryBytes, nil); setErr != nil {
			batch.Close()
			return totalDeleted, setErr
		}
		for _, kv := range g.kvs {
			if delErr := batch.Delete(kv.key, nil); delErr != nil {
				batch.Close()
				return totalDeleted, delErr
			}
		}
		if commitErr := batch.Commit(pebble.Sync); commitErr != nil {
			batch.Close()
			return totalDeleted, fmt.Errorf("pebble_activity_store: summarize commit: %w", commitErr)
		}
		batch.Close()
		totalDeleted += len(g.kvs)
	}
	return totalDeleted, nil
}

// Prune hard-deletes all entries of the given tier older than olderThan.
func (s *PebbleActivityStore) Prune(olderThan time.Time, tier string) (int, error) {
	kvs, err := s.scanTierKVs(tier, nil, &olderThan)
	if err != nil {
		return 0, err
	}
	if len(kvs) == 0 {
		return 0, nil
	}

	deleted := 0
	// Delete in batches of 500 to keep batch size reasonable.
	for i := 0; i < len(kvs); i += 500 {
		end := i + 500
		if end > len(kvs) {
			end = len(kvs)
		}
		batch := s.db.NewBatch()
		for _, kv := range kvs[i:end] {
			if err := batch.Delete(kv.key, nil); err != nil {
				batch.Close()
				return deleted, fmt.Errorf("pebble_activity_store: prune batch delete: %w", err)
			}
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			batch.Close()
			return deleted, fmt.Errorf("pebble_activity_store: prune batch commit: %w", err)
		}
		batch.Close()
		deleted += end - i
	}
	return deleted, nil
}

// pactSourcesCacheKey derives a cache key covering every filter field that can
// change the result, so one filter's counts are never served for another.
func pactSourcesCacheKey(f ActivityFilter) string {
	since, until := "", ""
	if f.Since != nil {
		since = fmt.Sprintf("%d", f.Since.UnixNano())
	}
	if f.Until != nil {
		until = fmt.Sprintf("%d", f.Until.UnixNano())
	}
	return strings.Join([]string{
		f.Tier, f.Type, f.Level, f.Source, f.OperationID, f.BookID, f.Search,
		since, until,
		strings.Join(f.Tags, ","),
		strings.Join(f.ExcludeSources, ","),
		strings.Join(f.ExcludeTiers, ","),
		strings.Join(f.ExcludeTags, ","),
	}, "\x00")
}

// GetDistinctSources returns unique sources with entry counts, ordered by count desc.
//
// Bounded + memoized. This used to full-scan and JSON-decode every entry in
// every tier on EVERY page load, concurrently with Query — 3.21 GB of the
// production heap profile. It now scans at most activityQueryScanBudget of the
// newest entries and reuses the result for activitySourcesCacheTTL.
//
// Consequence, deliberate: when the log is larger than the budget, a source
// that only ever appears in older entries will be missing, and counts are of
// the newest window rather than of all time. This feeds a filter picker, where
// a bounded, recent view is the useful one; hitting the bound is logged.
func (s *PebbleActivityStore) GetDistinctSources(f ActivityFilter) ([]SourceCount, error) {
	key := pactSourcesCacheKey(f)
	if cached, ok := s.lookupSourcesCache(key); ok {
		return cached, nil
	}

	nonDigest, digestTiers := pactSelectTiers(f)
	tiers := append(append([]string{}, nonDigest...), digestTiers...)

	counts := make(map[string]int)
	examined, exhausted, err := s.scanNewestFirst(tiers, f, activityQueryScanBudget, "sources",
		func(e ActivityEntry) bool {
			counts[e.Source]++
			return true
		})
	if err != nil {
		return nil, err
	}
	if !exhausted {
		slog.Warn("[activity] distinct-sources scan hit the budget; counts cover only the newest entries",
			"budget", activityQueryScanBudget,
			"examined", examined,
			"sources", len(counts),
			"tier", f.Tier,
			"level", f.Level)
	}

	out := make([]SourceCount, 0, len(counts))
	for src, cnt := range counts {
		out = append(out, SourceCount{Source: src, Count: cnt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })

	s.storeSourcesCache(key, out)
	return out, nil
}

// lookupSourcesCache returns a copy of a fresh cached result, if one exists.
// A copy is returned so a caller mutating the slice cannot corrupt the cache.
func (s *PebbleActivityStore) lookupSourcesCache(key string) ([]SourceCount, bool) {
	s.sourcesMu.Lock()
	defer s.sourcesMu.Unlock()
	entry, ok := s.sourcesCache[key]
	if !ok || time.Since(entry.at) > activitySourcesCacheTTL {
		return nil, false
	}
	out := make([]SourceCount, len(entry.out))
	copy(out, entry.out)
	return out, true
}

// storeSourcesCache memoizes a result and drops expired keys. Expiry is by TTL
// only — writes do not invalidate, so counts trail new activity by up to
// activitySourcesCacheTTL.
func (s *PebbleActivityStore) storeSourcesCache(key string, out []SourceCount) {
	stored := make([]SourceCount, len(out))
	copy(stored, out)

	s.sourcesMu.Lock()
	defer s.sourcesMu.Unlock()
	if s.sourcesCache == nil {
		s.sourcesCache = make(map[string]pactSourcesCacheEntry)
	}
	now := time.Now()
	for k, v := range s.sourcesCache {
		if now.Sub(v.at) > activitySourcesCacheTTL {
			delete(s.sourcesCache, k)
		}
	}
	s.sourcesCache[key] = pactSourcesCacheEntry{at: now, out: stored}
}

// WipeAllActivity deletes every entry from all tier buckets. Returns total count.
func (s *PebbleActivityStore) WipeAllActivity() (int64, error) {
	var total int64
	for _, tier := range actTiers {
		kvs, err := s.scanTierKVs(tier, nil, nil)
		if err != nil {
			return total, err
		}
		for i := 0; i < len(kvs); i += 500 {
			end := i + 500
			if end > len(kvs) {
				end = len(kvs)
			}
			batch := s.db.NewBatch()
			for _, kv := range kvs[i:end] {
				if err := batch.Delete(kv.key, nil); err != nil {
					batch.Close()
					return total, fmt.Errorf("pebble_activity_store: wipe batch delete: %w", err)
				}
			}
			if err := batch.Commit(pebble.Sync); err != nil {
				batch.Close()
				return total, fmt.Errorf("pebble_activity_store: wipe batch commit: %w", err)
			}
			batch.Close()
			total += int64(end - i)
		}
	}
	return total, nil
}

// CompactByDay collapses all compactable tier entries into daily digest rows.
// Every tier except "digest" is eligible — newly-introduced tiers are automatically
// compacted without an allowlist update (denylist approach).
// Each day is processed atomically.
func (s *PebbleActivityStore) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	var result CompactResult

	// Load all compactable entries (all tiers except "digest").
	var all []pactKV
	for _, tier := range actCompactableTiers() {
		kvs, err := s.scanTierKVs(tier, nil, &olderThan)
		if err != nil {
			return result, err
		}
		all = append(all, kvs...)
	}
	if len(all) == 0 {
		return result, nil
	}

	// Group by date.
	type dayGroup struct{ kvs []pactKV }
	days := make(map[string]*dayGroup)
	var dayOrder []string
	for _, kv := range all {
		dk := kv.entry.Timestamp.UTC().Format("2006-01-02")
		if _, ok := days[dk]; !ok {
			days[dk] = &dayGroup{}
			dayOrder = append(dayOrder, dk)
		}
		days[dk].kvs = append(days[dk].kvs, kv)
	}
	sort.Strings(dayOrder)

	for _, dateKey := range dayOrder {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		dg := days[dateKey]
		entries := make([]ActivityEntry, len(dg.kvs))
		for i, kv := range dg.kvs {
			entries[i] = kv.entry
		}

		counts := make(map[string]int)
		for _, e := range entries {
			counts[e.Type]++
		}

		var auditItems, errItems, normalItems []DigestItem
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
			OriginalCount:  len(entries),
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
			return result, fmt.Errorf("pebble_activity_store: compact marshal: %w", err)
		}

		startOfDay, err := time.Parse("2006-01-02", dateKey)
		if err != nil {
			return result, fmt.Errorf("pebble_activity_store: compact parse date: %w", err)
		}

		// Populate Details from the DigestDetails map so it survives the ActivityEntry round-trip.
		var ddMap map[string]any
		if mapErr := json.Unmarshal(detailsBytes, &ddMap); mapErr != nil {
			return result, fmt.Errorf("pebble_activity_store: compact unmarshal dd map: %w", mapErr)
		}
		digestID := ulid.Make().String()
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
		digestKey := pactPrimaryKey("digest", startOfDay, digestID)
		digestBytes, err := json.Marshal(digest)
		if err != nil {
			return result, fmt.Errorf("pebble_activity_store: compact marshal digest: %w", err)
		}

		batch := s.db.NewBatch()

		// Delete old digest if present.
		if existingKey != nil {
			if err := batch.Delete(existingKey, nil); err != nil {
				batch.Close()
				return result, fmt.Errorf("pebble_activity_store: compact delete old digest: %w", err)
			}
		}

		// Write new digest.
		if err := batch.Set(digestKey, digestBytes, nil); err != nil {
			batch.Close()
			return result, fmt.Errorf("pebble_activity_store: compact set digest: %w", err)
		}

		// Delete originals.
		for _, kv := range dg.kvs {
			if err := batch.Delete(kv.key, nil); err != nil {
				batch.Close()
				return result, fmt.Errorf("pebble_activity_store: compact delete original: %w", err)
			}
		}

		if err := batch.Commit(pebble.Sync); err != nil {
			batch.Close()
			return result, fmt.Errorf("pebble_activity_store: compact commit day %s: %w", dateKey, err)
		}
		batch.Close()

		result.DaysCompacted++
		result.EntriesDeleted += len(dg.kvs)
	}
	return result, nil
}

// MigrateSystemActivityLogs is a no-op for PebbleActivityStore since it's not backed by SQLite.
func (s *PebbleActivityStore) MigrateSystemActivityLogs() (int, error) {
	return 0, nil
}

// RecompactDigests re-derives type, tier, and tags on every stored daily-digest
// entry whose items were compacted before enrichment was added.
//
// Algorithm:
//  1. Range-scan the entire "act:digest:" prefix (all digest keys).
//  2. Decode each entry's Details map as DigestDetails.
//  3. Skip entries where no items are legacy (idempotent guard).
//  4. For each legacy item: call deriveTypeFromMessage + enrichLegacyLogTags.
//  5. Rebuild Counts and TagCounts, marshal back, and overwrite with the same key.
func (s *PebbleActivityStore) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	var result RecompactResult

	type digestKV struct {
		key   []byte
		entry ActivityEntry
		dd    DigestDetails
	}

	var candidates []digestKV

	// Scan all digest entries.
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: pactPrimaryPrefix("digest"),
		UpperBound: pactPrimaryUpperBound("digest"),
	})
	if err != nil {
		return result, fmt.Errorf("pebble_activity_store: recompact new iter: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var e ActivityEntry
		if jsonErr := json.Unmarshal(iter.Value(), &e); jsonErr != nil {
			continue
		}
		if e.Type != "daily_digest" {
			continue
		}
		var dd DigestDetails
		if e.Details != nil {
			if b, merr := json.Marshal(e.Details); merr == nil {
				_ = json.Unmarshal(b, &dd)
			}
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		candidates = append(candidates, digestKV{key: keyCopy, entry: e, dd: dd})
	}
	if err := iter.Close(); err != nil {
		return result, fmt.Errorf("pebble_activity_store: recompact iter close: %w", err)
	}

	slog.Info("[activity] recompact: starting digest re-derivation (pebble)",
		"digest_count", len(candidates))

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

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

		// Rebuild TagCounts from updated items (action: and source: namespaces only).
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
			return result, fmt.Errorf("pebble_activity_store: recompact marshal digest key=%s: %w", c.key, merr)
		}
		var detailsMap map[string]any
		if err := json.Unmarshal(ddBytes, &detailsMap); err != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact unmarshal detailsMap key=%s: %w", c.key, err)
		}
		c.entry.Details = detailsMap
		c.entry.Summary = fmt.Sprintf("Daily digest for %s (%d entries)", c.dd.Date, c.dd.OriginalCount)

		entryBytes, merr := json.Marshal(c.entry)
		if merr != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact marshal entry key=%s: %w", c.key, merr)
		}

		if err := s.db.Set(c.key, entryBytes, pebble.Sync); err != nil {
			return result, fmt.Errorf("pebble_activity_store: recompact write key=%s: %w", c.key, err)
		}

		slog.Info("[activity] recompact: updated digest (pebble)",
			"key", string(c.key), "date", c.dd.Date, "items", len(c.dd.Items))
		result.Touched++
	}

	slog.Info("[activity] recompact: complete (pebble)",
		"touched", result.Touched, "skipped", result.Skipped)
	return result, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// pactKV holds a Pebble key and its decoded ActivityEntry.
type pactKV struct {
	key   []byte
	entry ActivityEntry
}

// pactDecodeTally accumulates JSON decode failures across a single scan so the
// scan can emit one aggregate warning instead of flooding the log when a whole
// key range is corrupt. Requirement: dropped rows are counted and reported,
// never silently skipped.
type pactDecodeTally struct {
	dropped  int
	firstErr error
	firstKey string
}

func (t *pactDecodeTally) record(key []byte, err error) {
	t.dropped++
	if t.firstErr == nil {
		t.firstErr = err
		t.firstKey = string(key)
	}
}

func (t *pactDecodeTally) log(op string) {
	if t.dropped == 0 {
		return
	}
	slog.Warn("[activity] pebble store dropped undecodable entries",
		"op", op,
		"dropped", t.dropped,
		"first_key", t.firstKey,
		"error", t.firstErr)
}

// pactCursor is one tier's reverse (newest-first) iterator plus the cached time
// field of the key it currently sits on.
type pactCursor struct {
	tier string
	iter *pebble.Iterator
	tf   string
}

// pactSelectTiers splits the tiers a filter is allowed to touch into the
// non-digest group and the digest group.
//
// The two groups exist because the historical result order is a TWO-GROUP
// order (matching the old SQL "ORDER BY compacted ASC, timestamp DESC"):
// every non-digest entry sorts before every digest entry, and within a group
// entries are newest-first. Walking non-digest tiers to exhaustion before
// touching digest reproduces that order without a global sort.
//
// f.Tier and f.ExcludeTiers are applied here rather than left to matchesFilter
// so excluded tiers never open an iterator or consume scan budget.
func pactSelectTiers(f ActivityFilter) (nonDigest, digest []string) {
	base := actTiers
	if f.Tier != "" {
		base = []string{f.Tier}
	}
	for _, tier := range base {
		excluded := false
		for _, ex := range f.ExcludeTiers {
			if tier == ex {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if tier == "digest" {
			digest = append(digest, tier)
		} else {
			nonDigest = append(nonDigest, tier)
		}
	}
	return nonDigest, digest
}

// scanNewestFirst walks the given tiers newest-first as a k-way merge over
// per-tier reverse iterators, decoding at most `budget` entries, and invokes
// visit for every decoded entry that passes matchesFilter. visit returns false
// to stop the walk early.
//
// Returns the number of entries decoded and whether the input was exhausted.
// exhausted == false means the walk stopped early — either visit asked to stop
// or the budget ran out — so any count derived from it is a LOWER BOUND.
//
// Ordering note: the merge orders on the key's embedded timestamp, not on the
// decoded Timestamp field. Those are written together by Record, so they agree;
// a row whose stored Tier/Timestamp disagrees with its key (already-inconsistent
// data) now groups by its key.
//
// Deliberately single-goroutine: this is a stop-early walk, and fanning it out
// over workers would mean over-reading, which is the very cost being removed.
func (s *PebbleActivityStore) scanNewestFirst(
	tiers []string,
	f ActivityFilter,
	budget int,
	op string,
	visit func(ActivityEntry) bool,
) (examined int, exhausted bool, err error) {
	if len(tiers) == 0 {
		return 0, true, nil
	}
	if budget <= 0 {
		return 0, false, nil
	}

	cursors := make([]*pactCursor, 0, len(tiers))
	defer func() {
		for _, c := range cursors {
			if cerr := c.iter.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("pebble_activity_store: %s iter close (tier=%s): %w", op, c.tier, cerr)
			}
		}
	}()

	for _, tier := range tiers {
		lower, upper := pactTierBounds(tier, f.Since, f.Until)
		it, iterErr := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
		if iterErr != nil {
			return examined, false, fmt.Errorf("pebble_activity_store: %s new iter (tier=%s): %w", op, tier, iterErr)
		}
		if !it.Last() {
			// Empty range for this tier — close now rather than carrying it.
			if cerr := it.Close(); cerr != nil {
				return examined, false, fmt.Errorf("pebble_activity_store: %s iter close (tier=%s): %w", op, tier, cerr)
			}
			continue
		}
		cursors = append(cursors, &pactCursor{tier: tier, iter: it, tf: pactKeyTimeField(it.Key())})
	}

	tally := &pactDecodeTally{}
	defer tally.log(op)

	for {
		// Pick the cursor sitting on the newest remaining key. Ties break on
		// the full key bytes so the order is deterministic.
		best := -1
		for i, c := range cursors {
			if !c.iter.Valid() {
				continue
			}
			if best < 0 {
				best = i
				continue
			}
			b := cursors[best]
			if c.tf > b.tf || (c.tf == b.tf && bytes.Compare(c.iter.Key(), b.iter.Key()) > 0) {
				best = i
			}
		}
		if best < 0 {
			return examined, true, nil // every cursor drained
		}
		if examined >= budget {
			return examined, false, nil // caller logs the truncation
		}

		c := cursors[best]
		examined++
		s.entriesDecoded.Add(1)
		var e ActivityEntry
		if jsonErr := json.Unmarshal(c.iter.Value(), &e); jsonErr != nil {
			s.decodeFailures.Add(1)
			tally.record(c.iter.Key(), jsonErr)
		} else if matchesFilter(e, f) {
			if !visit(e) {
				return examined, false, nil
			}
		}

		if c.iter.Prev() {
			c.tf = pactKeyTimeField(c.iter.Key())
		}
	}
}

// NOTE: scanTier (a materializing []ActivityEntry wrapper over scanTierKVs)
// was removed here. Its only callers were Query and GetDistinctSources, both of
// which now use the bounded scanNewestFirst walk. The maintenance ops that
// legitimately need every row call scanTierKVs directly.

// scanTierKVs returns key+entry pairs for a tier within the time range.
//
// This is a FULL materializing scan and is used only by the maintenance
// operations (Summarize, Prune, WipeAllActivity, CompactByDay), which
// legitimately need every row. Query and GetDistinctSources deliberately do NOT
// use it — see scanNewestFirst.
func (s *PebbleActivityStore) scanTierKVs(tier string, since, until *time.Time) ([]pactKV, error) {
	lower, upper := pactTierBounds(tier, since, until)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return nil, fmt.Errorf("pebble_activity_store: scanTierKVs new iter (tier=%s): %w", tier, err)
	}
	defer iter.Close()

	tally := &pactDecodeTally{}
	defer tally.log("scanTierKVs tier=" + tier)

	var out []pactKV
	for iter.First(); iter.Valid(); iter.Next() {
		var e ActivityEntry
		s.entriesDecoded.Add(1)
		if jsonErr := json.Unmarshal(iter.Value(), &e); jsonErr != nil {
			// Counted and reported in aggregate by the deferred tally.log —
			// previously a bare `continue` that dropped rows silently.
			s.decodeFailures.Add(1)
			tally.record(iter.Key(), jsonErr)
			continue
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		out = append(out, pactKV{key: keyCopy, entry: e})
	}
	return out, nil
}

// queryByIndexPrefix reads ref values from an index prefix, then fetches primary entries.
// Handles both op and book secondary indexes.
func (s *PebbleActivityStore) queryByIndexPrefix(prefix string, f ActivityFilter) ([]ActivityEntry, int, error) {
	// Upper bound: replace trailing ':' with ';' to cover the entire id sub-namespace.
	upperPrefix := prefix[:len(prefix)-1] + ";"

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(upperPrefix),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("pebble_activity_store: queryByIndex new iter: %w", err)
	}
	defer iter.Close()

	var refs [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		valCopy := make([]byte, len(iter.Value()))
		copy(valCopy, iter.Value())
		refs = append(refs, valCopy)
	}

	var all []ActivityEntry
	for _, ref := range refs {
		primaryKey, ok := pactPrimaryKeyFromRef(ref)
		if !ok {
			continue
		}
		val, closer, err := s.db.Get(primaryKey)
		if err != nil {
			// Entry may have been deleted (e.g., pruned); skip stale index refs.
			continue
		}
		var entry ActivityEntry
		jsonErr := json.Unmarshal(val, &entry)
		closer.Close()
		if jsonErr != nil {
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
// Returns the DigestDetails, the Pebble key, and any error.
func (s *PebbleActivityStore) findExistingDigest(dateKey string) (DigestDetails, []byte, error) {
	day, err := time.Parse("2006-01-02", dateKey)
	if err != nil {
		return DigestDetails{}, nil, err
	}
	dayEnd := day.Add(24 * time.Hour)

	lower := pactPrimaryKey("digest", day, "")
	upper := pactPrimaryKey("digest", dayEnd, "")

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return DigestDetails{}, nil, fmt.Errorf("pebble_activity_store: findExistingDigest new iter: %w", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var row struct {
			ActivityEntry
			DigestDetails json.RawMessage `json:"digest_details,omitempty"`
		}
		if jsonErr := json.Unmarshal(iter.Value(), &row); jsonErr != nil {
			continue
		}
		if row.ActivityEntry.Type != "daily_digest" {
			continue
		}

		var dd DigestDetails
		if row.DigestDetails != nil {
			// Old format: digest_details at top level.
			_ = json.Unmarshal(row.DigestDetails, &dd)
		} else if row.ActivityEntry.Details != nil {
			// New format: stored in ActivityEntry.Details.
			if b, merr := json.Marshal(row.ActivityEntry.Details); merr == nil {
				_ = json.Unmarshal(b, &dd)
			}
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		return dd, keyCopy, nil
	}
	return DigestDetails{}, nil, nil
}
