// file: internal/server/library_list_warmer.go
// version: 2.2.1
// guid: 7e8d9a0b-1c2d-3e4f-5a6b-7c8d9e0f1a2b
// last-edited: 2026-08-11

// Pre-warms svc.audiobookService.listCache by firing the queries the UI
// is most likely to hit on first load — library page (first few pages,
// title asc + desc), default plain list. Runs once at startup after
// memdb is published; otherwise the first user load eats the full
// pushdown + cache-miss cost (~3+ minutes on a 50K-book library).

package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// readHeapAllocMB returns the process's current heap allocation in MB.
// Used as a guard before each warmer query — skip the tick if we're
// already pressuring memory rather than pile on. HeapAlloc is what GC
// sees as "live"; RSS includes returned-to-OS memory that doesn't matter.
func readHeapAllocMB() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc / (1024 * 1024)
}

// warmerMemoryDeltaMB is how many MB above the post-eager baseline the
// trickle is allowed to grow heap by. Production measurement (full-library
// library, ~13GB baseline): a single trickle query allocates ~1.8GB
// transient before GC. Default 4096 (4GB) gives one-query headroom +
// GC reclaim buffer. Tunable via LIST_WARMER_HEAP_DELTA_MB.
func warmerMemoryDeltaMB() uint64 {
	if v := os.Getenv("LIST_WARMER_HEAP_DELTA_MB"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	// Back-compat: the old var name now also means "delta", with sane min.
	if v := os.Getenv("LIST_WARMER_MAX_HEAP_MB"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n >= 256 {
			return n
		}
	}
	return 4096
}

// warmerTrickleInterval is the gap between trickle ticks. Default 10s
// → ~30 min to drain a 180-query backlog. Tunable via
// LIST_WARMER_TRICKLE_INTERVAL_MS.
func warmerTrickleInterval() time.Duration {
	if v := os.Getenv("LIST_WARMER_TRICKLE_INTERVAL_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 500 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 10 * time.Second
}

// buildListCacheRawQuery constructs the URL.RawQuery string in the exact
// order/format the React UI's URLSearchParams emits in getBooks(). Must
// match web/src/services/api.ts:getBooks line-for-line or the cache key
// won't collide with what the handler computes from c.Request.URL.RawQuery.
func buildListCacheRawQuery(limit, offset int, f audiobookspkg.ListFilters) string {
	var parts []string
	add := func(k, v string) { parts = append(parts, k+"="+url.QueryEscape(v)) }

	add("limit", strconv.Itoa(limit))
	add("offset", strconv.Itoa(offset))
	if f.SortBy != "" {
		add("sort_by", f.SortBy)
	}
	if f.SortOrder != "" {
		add("sort_order", f.SortOrder)
	}
	if len(f.Tags) > 0 {
		for _, t := range f.Tags {
			add("tags[]", t)
		}
	} else if f.Tag != "" {
		add("tag", f.Tag)
	}
	if f.LibraryState != "" {
		add("library_state", f.LibraryState)
	}
	// FieldFilters + PerUserFilters travel together in the UI's `filters`
	// JSON param; the handler splits them after Unmarshal. Combine here so
	// the cache key matches.
	combined := append([]audiobookspkg.FieldFilter{}, f.FieldFilters...)
	combined = append(combined, f.PerUserFilters...)
	if len(combined) > 0 {
		if b, err := json.Marshal(combined); err == nil {
			add("filters", string(b))
		}
	}
	if f.FingerprintStatus != "" {
		add("fingerprint_status", f.FingerprintStatus)
	}
	if f.CoveragePercentMin != nil {
		add("coverage_percent_min", strconv.Itoa(*f.CoveragePercentMin))
	}
	if f.CoveragePercentMax != nil {
		add("coverage_percent_max", strconv.Itoa(*f.CoveragePercentMax))
	}
	// UI always sets is_primary_version=true last; we only set it when the
	// caller asked for primary-only (the UI's default).
	if f.IsPrimaryVersion != nil && *f.IsPrimaryVersion {
		add("is_primary_version", "true")
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "&"
		}
		out += p
	}
	return out
}

func typeName(v interface{}) string { return fmt.Sprintf("%T", v) }

// qry is one cache-warming target. Hoisted to package scope so both the
// eager phase (warmAudiobookListCache) and the background trickle
// (runTrickleWarmer) can pass them around.
type qry struct {
	name    string
	limit   int
	offset  int
	filters audiobookspkg.ListFilters
}

// memReadyChecker is satisfied by *database.PebbleStore. Decoupled
// behind an interface so tests can stub it.
type memReadyChecker interface {
	IsMemReady() bool
}

// storeUnwrapper is implemented by decorator types (e.g. indexedStore)
// that wrap an inner Store. Used to peel layers and reach the concrete
// PebbleStore for capability checks like IsMemReady.
type storeUnwrapper interface {
	Unwrap() database.Store
}

// unwrapStore peels Unwrap()-implementing decorators until reaching the
// innermost Store. Bounded to 8 levels as a sanity guard against cycles.
func unwrapStore(s database.Store) database.Store {
	for i := 0; i < 8; i++ {
		w, ok := s.(storeUnwrapper)
		if !ok {
			return s
		}
		inner := w.Unwrap()
		if inner == nil || inner == s {
			return s
		}
		s = inner
	}
	return s
}

// resolveDefaultUserID returns a UserID to warm per-user filtered
// queries against (read_status, progress_pct, ...). Prefers "admin",
// falls back to the first user in ListUsers. Returns "" if nothing
// is available — caller should skip per-user warm-up in that case.
func (s *Server) resolveDefaultUserID() string {
	store := s.Store()
	if store == nil {
		return ""
	}
	if u, err := store.GetUserByUsername("admin"); err == nil && u != nil {
		return u.ID
	}
	users, err := store.ListUsers()
	if err == nil && len(users) > 0 {
		return users[0].ID
	}
	return ""
}

// warmAudiobookListCache waits for memdb to publish, then fires the
// common library-page queries to populate svc.audiobookService.listCache.
// All queries run sequentially against the AudiobookService — no extra
// HTTP overhead.
func (s *Server) warmAudiobookListCache() {
	if s.audiobookService == nil {
		return
	}
	// The Server's store is wrapped by indexedStore (and possibly other
	// decorators). Peel them to reach the concrete PebbleStore so the
	// IsMemReady() type assertion succeeds.
	rawStore := unwrapStore(s.Store())
	checker, ok := rawStore.(memReadyChecker)
	if !ok {
		slog.Warn("library list warm-up: store doesn't expose IsMemReady, skipping",
			"store_type", typeName(rawStore))
		return
	}

	// Wait up to 5 minutes for memdb. Memdb warmup on a 50K-book DB
	// typically takes ~2.5 min, so 5 min is comfortable headroom.
	deadline := time.Now().Add(5 * time.Minute)
	for !checker.IsMemReady() {
		if time.Now().After(deadline) {
			slog.Warn("library list warm-up: memdb not ready after 5 min, skipping")
			return
		}
		select {
		case <-s.bgCtx.Done():
			return // server shutting down — never touch the store again
		case <-time.After(2 * time.Second):
		}
	}

	started := time.Now()
	// Warm-up queries run under bgCtx so Stop()'s bgCancel aborts them
	// before the store closes. context.Background() here let the warmer
	// keep reading the store after Close (panic "pebble: closed").
	ctx := s.bgCtx

	// Each query mirrors what the UI sends on a fresh library-page load.
	// Pagination keys are independent cache entries, so we warm a lot of
	// pages — RAM here means less Pebble cache thrash + zero cold-miss
	// for the user.
	primaryTrue := true
	queries := []qry{}
	// Default UI sort: title asc, primary only — warm the first 20 pages
	// (400 books deep). Covers nearly all browsing without the user
	// hitting a cold cache.
	for off := 0; off < 400; off += 20 {
		queries = append(queries, qry{
			name:    "title-asc-primary",
			limit:   20,
			offset:  off,
			filters: audiobookspkg.ListFilters{IsPrimaryVersion: &primaryTrue, SortBy: "title", SortOrder: "asc"},
		})
	}
	// Title desc — first 5 pages.
	for off := 0; off < 100; off += 20 {
		queries = append(queries, qry{
			name:    "title-desc-primary",
			limit:   20,
			offset:  off,
			filters: audiobookspkg.ListFilters{IsPrimaryVersion: &primaryTrue, SortBy: "title", SortOrder: "desc"},
		})
	}
	// "-review:matched" — books that have NOT yet been matched to metadata.
	// One of the most common filters per user feedback. First 10 pages.
	for off := 0; off < 200; off += 20 {
		queries = append(queries, qry{
			name:   "review-not-matched",
			limit:  20,
			offset: off,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primaryTrue,
				SortBy:           "title",
				SortOrder:        "asc",
				FieldFilters: []audiobookspkg.FieldFilter{
					{Field: "review", Value: "matched", Negated: true},
				},
			},
		})
	}
	// "review:matched" inverse (already-matched books).
	for off := 0; off < 100; off += 20 {
		queries = append(queries, qry{
			name:   "review-matched",
			limit:  20,
			offset: off,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primaryTrue,
				SortBy:           "title",
				SortOrder:        "asc",
				FieldFilters: []audiobookspkg.FieldFilter{
					{Field: "review", Value: "matched"},
				},
			},
		})
	}
	// library_state filter — organized / imported / suspicious. Common
	// left-rail filter, first 5 pages each.
	for _, state := range []string{"organized", "imported", "suspicious"} {
		for off := 0; off < 100; off += 20 {
			queries = append(queries, qry{
				name:   "library-state-" + state,
				limit:  20,
				offset: off,
				filters: audiobookspkg.ListFilters{
					IsPrimaryVersion: &primaryTrue,
					LibraryState:     state,
					SortBy:           "title",
					SortOrder:        "asc",
				},
			})
		}
	}
	// Tag filters — favorites and read are the popular ones; first 5 pages each.
	for _, tag := range []string{"favorites", "read"} {
		for off := 0; off < 100; off += 20 {
			queries = append(queries, qry{
				name:   "tag-" + tag,
				limit:  20,
				offset: off,
				filters: audiobookspkg.ListFilters{
					IsPrimaryVersion: &primaryTrue,
					Tag:              tag,
					SortBy:           "title",
					SortOrder:        "asc",
				},
			})
		}
	}
	// -tag:read (unread) — first 5 pages.
	for off := 0; off < 100; off += 20 {
		queries = append(queries, qry{
			name:   "not-tag-read",
			limit:  20,
			offset: off,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primaryTrue,
				SortBy:           "title",
				SortOrder:        "asc",
				FieldFilters: []audiobookspkg.FieldFilter{
					{Field: "tag", Value: "read", Negated: true},
				},
			},
		})
	}
	// FieldFilter-style binary toggles: yes/no for has_cover, has_written,
	// needs_writeback, has_organized. First 5 pages each.
	binaryFields := []string{"has_cover", "has_written", "needs_writeback", "has_organized"}
	for _, field := range binaryFields {
		for _, val := range []string{"yes", "no"} {
			// needs_writeback:no isn't useful — skip to halve count.
			if field == "needs_writeback" && val == "no" {
				continue
			}
			for off := 0; off < 100; off += 20 {
				queries = append(queries, qry{
					name:   field + "-" + val,
					limit:  20,
					offset: off,
					filters: audiobookspkg.ListFilters{
						IsPrimaryVersion: &primaryTrue,
						SortBy:           "title",
						SortOrder:        "asc",
						FieldFilters: []audiobookspkg.FieldFilter{
							{Field: field, Value: val},
						},
					},
				})
			}
		}
	}
	// review:no_match — already covered review:matched + -review:matched
	// above; add the explicit "rejected" path.
	for off := 0; off < 60; off += 20 {
		queries = append(queries, qry{
			name:   "review-no-match",
			limit:  20,
			offset: off,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primaryTrue,
				SortBy:           "title",
				SortOrder:        "asc",
				FieldFilters: []audiobookspkg.FieldFilter{
					{Field: "review", Value: "no_match"},
				},
			},
		})
	}
	// language:en — top-of-list locale filter, first 5 pages.
	for off := 0; off < 100; off += 20 {
		queries = append(queries, qry{
			name:   "language-en",
			limit:  20,
			offset: off,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primaryTrue,
				SortBy:           "title",
				SortOrder:        "asc",
				FieldFilters: []audiobookspkg.FieldFilter{
					{Field: "language", Value: "en"},
				},
			},
		})
	}
	// NOT author:Unknown — exclude untagged books, first 5 pages.
	for off := 0; off < 100; off += 20 {
		queries = append(queries, qry{
			name:   "not-author-unknown",
			limit:  20,
			offset: off,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primaryTrue,
				SortBy:           "title",
				SortOrder:        "asc",
				FieldFilters: []audiobookspkg.FieldFilter{
					{Field: "author", Value: "Unknown", Negated: true},
				},
			},
		})
	}
	// Format filter — m4b, mp3, m4a. First 5 pages each.
	for _, fmt := range []string{"m4b", "mp3", "m4a"} {
		for off := 0; off < 100; off += 20 {
			queries = append(queries, qry{
				name:   "format-" + fmt,
				limit:  20,
				offset: off,
				filters: audiobookspkg.ListFilters{
					IsPrimaryVersion: &primaryTrue,
					SortBy:           "title",
					SortOrder:        "asc",
					FieldFilters: []audiobookspkg.FieldFilter{
						{Field: "format", Value: fmt},
					},
				},
			})
		}
	}
	// Compound triage queries straight from the filter cheatsheet — these
	// are how the user actually finds work to do. First 5 pages each.
	type compound struct {
		name string
		ff   []audiobookspkg.FieldFilter
		ls   string
	}
	compounds := []compound{
		// Fully processed books: review:matched has_written:yes has_organized:yes
		{name: "fully-processed", ff: []audiobookspkg.FieldFilter{
			{Field: "review", Value: "matched"},
			{Field: "has_written", Value: "yes"},
			{Field: "has_organized", Value: "yes"},
		}},
		// Organized but needs metadata + file write
		{name: "organized-needs-metadata-write", ls: "organized", ff: []audiobookspkg.FieldFilter{
			{Field: "review", Value: "matched", Negated: true},
			{Field: "has_written", Value: "yes", Negated: true},
		}},
		// Metadata applied but not written to files
		{name: "matched-not-written", ff: []audiobookspkg.FieldFilter{
			{Field: "review", Value: "matched"},
			{Field: "has_written", Value: "yes", Negated: true},
		}},
		// Written but not organized
		{name: "written-not-organized", ff: []audiobookspkg.FieldFilter{
			{Field: "review", Value: "matched"},
			{Field: "has_written", Value: "yes"},
			{Field: "has_organized", Value: "yes", Negated: true},
		}},
		// Matched but missing cover art
		{name: "matched-no-cover", ff: []audiobookspkg.FieldFilter{
			{Field: "review", Value: "matched"},
			{Field: "has_cover", Value: "no"},
		}},
		// Imported books needing metadata
		{name: "imported-needs-metadata", ls: "imported", ff: []audiobookspkg.FieldFilter{
			{Field: "review", Value: "matched", Negated: true},
		}},
	}
	for _, c := range compounds {
		for off := 0; off < 100; off += 20 {
			queries = append(queries, qry{
				name:   c.name,
				limit:  20,
				offset: off,
				filters: audiobookspkg.ListFilters{
					IsPrimaryVersion: &primaryTrue,
					LibraryState:     c.ls,
					SortBy:           "title",
					SortOrder:        "asc",
					FieldFilters:     c.ff,
				},
			})
		}
	}
	// Per-user state filters (read_status / progress_pct). These require
	// a UserID — only fire if we can resolve a default admin user.
	// Without UserID the per-user filter is silently skipped and the
	// warm-up entry would be a duplicate of an existing one.
	if adminID := s.resolveDefaultUserID(); adminID != "" {
		perUserGroups := []struct {
			name string
			pu   []audiobookspkg.FieldFilter
		}{
			{name: "read-finished", pu: []audiobookspkg.FieldFilter{{Field: "read_status", Value: "finished"}}},
			{name: "read-in-progress", pu: []audiobookspkg.FieldFilter{{Field: "read_status", Value: "in_progress"}}},
			{name: "read-not-finished", pu: []audiobookspkg.FieldFilter{{Field: "read_status", Value: "finished", Negated: true}}},
			{name: "progress-gt-75", pu: []audiobookspkg.FieldFilter{{Field: "progress_pct", Value: ">75"}}},
		}
		for _, g := range perUserGroups {
			for off := 0; off < 60; off += 20 {
				queries = append(queries, qry{
					name:   g.name,
					limit:  20,
					offset: off,
					filters: audiobookspkg.ListFilters{
						IsPrimaryVersion: &primaryTrue,
						SortBy:           "title",
						SortOrder:        "asc",
						PerUserFilters:   g.pu,
						UserID:           adminID,
					},
				})
			}
		}
	}
	// Plain list, no filter — sidebar/import flow first page.
	queries = append(queries, qry{name: "plain-page-1", limit: 20, offset: 0})
	queries = append(queries, qry{
		name:    "primary-no-sort",
		limit:   20,
		offset:  0,
		filters: audiobookspkg.ListFilters{IsPrimaryVersion: &primaryTrue},
	})

	// Split the backlog into EAGER (run immediately, sequential, small) and
	// TRICKLE (run one-per-tick in background, paced, with GC between).
	// Eager covers the default UI load so the first "All Books" click after
	// restart is instant. Trickle fills in everything else over ~30 min so
	// peak memory never trampolines.
	eagerQueries := make([]qry, 0, 2)
	trickleQueries := make([]qry, 0, len(queries))
	for _, q := range queries {
		if q.name == "title-asc-primary" && q.offset < 40 {
			eagerQueries = append(eagerQueries, q)
		} else {
			trickleQueries = append(trickleQueries, q)
		}
	}

	// F1: heap-pressure guard for the eager phase. Sample baseline ONCE,
	// post-GC, right after memdb publish. If concurrent work (chromem
	// hydrate, etc.) has already pressured the heap above baseline+delta,
	// skip the rest of eager rather than piling on. Uses the same
	// warmerMemoryDeltaMB() ceiling concept as the trickle warmer.
	runtime.GC()
	debug.FreeOSMemory()
	eagerBaselineMB := readHeapAllocMB()
	eagerDeltaMB := warmerMemoryDeltaMB()
	eagerCeilingMB := eagerBaselineMB + eagerDeltaMB
	slog.Info("library list warm-up eager starting",
		"eager_queries", len(eagerQueries),
		"trickle_queries", len(trickleQueries),
		"eager_baseline_mb", eagerBaselineMB,
		"eager_delta_mb", eagerDeltaMB,
		"eager_ceiling_mb", eagerCeilingMB,
	)
	hits, misses, cached, eagerSkipped := 0, 0, 0, 0
	// Pre-flight heap check BEFORE the first eager query, in case
	// concurrent hydrate has already pushed us over the line.
	if heap := readHeapAllocMB(); heap > eagerCeilingMB {
		slog.Warn("library list warm-up eager: heap above ceiling before first query, skipping eager phase",
			"heap_mb", heap, "ceiling_mb", eagerCeilingMB, "baseline_mb", eagerBaselineMB)
		eagerSkipped = len(eagerQueries)
		eagerQueries = nil
	}
	for i, q := range eagerQueries {
		// Abort between queries once shutdown begins — the store is about
		// to close and any further read panics "pebble: closed".
		if ctx.Err() != nil {
			slog.Info("library list warm-up eager: aborting, server shutting down",
				"completed", i, "remaining", len(eagerQueries)-i)
			return
		}
		// Heap-pressure guard BETWEEN eager queries (skip for i==0;
		// already covered by the pre-flight check above).
		if i > 0 {
			if heap := readHeapAllocMB(); heap > eagerCeilingMB {
				eagerSkipped = len(eagerQueries) - i
				slog.Warn("library list warm-up eager: heap above ceiling, skipping remaining eager queries",
					"heap_mb", heap, "ceiling_mb", eagerCeilingMB,
					"baseline_mb", eagerBaselineMB, "skipped", eagerSkipped)
				break
			}
		}
		qStart := time.Now()
		resp, err := s.buildAudiobookListResponse(ctx, q.limit, q.offset, "", nil, nil, q.filters, false)
		if err != nil {
			misses++
			slog.Warn("library list warm-up eager query failed",
				"name", q.name, "offset", q.offset, "err", err)
			continue
		}
		hits++
		if len(q.filters.PerUserFilters) == 0 {
			raw := buildListCacheRawQuery(q.limit, q.offset, q.filters)
			s.listCache.Set("list:"+raw, resp)
			cached++
		}
		slog.Debug("library list warm-up query ok",
			"name", q.name, "offset", q.offset,
			"duration_ms", time.Since(qStart).Milliseconds())
	}
	slog.Info("library list warm-up eager complete",
		"queries_warmed", hits,
		"cache_entries_populated", cached,
		"failures", misses,
		"skipped_pressure", eagerSkipped,
		"duration_ms", time.Since(started).Milliseconds(),
	)

	// Kick off the trickle warmer in its own goroutine so the startup
	// path returns immediately. Trickle owns its own lifetime (runs until
	// the backlog drains or bgCtx is canceled). Enrolled in bgWG — this
	// goroutine runs ~30 min at the default 10s interval and, when it was
	// fire-and-forget, outlived test servers and panicked "pebble: closed"
	// against their closed stores. The Add happens while the parent
	// warmer's own bgWG entry is still held, so it can never race a
	// completed bgWG.Wait in Stop().
	s.bgWG.Add("library-list-trickle-warmer")
	go func() {
		defer s.bgWG.Done("library-list-trickle-warmer")
		s.runTrickleWarmer(trickleQueries)
	}()
}

// runTrickleWarmer pops one query per tick from the backlog, runs it,
// caches the result, and forces a GC before the next tick. Memory-guard:
// if HeapAlloc > LIST_WARMER_MAX_HEAP_MB (default 1GB) at tick start,
// skip and back off. Already-cached entries are skipped (the user may
// have beat us to it). Total time at default 10s interval ≈ 30 min for
// ~180 queries.
func (s *Server) runTrickleWarmer(backlog []qry) {
	if len(backlog) == 0 {
		return
	}
	interval := warmerTrickleInterval()
	deltaMB := warmerMemoryDeltaMB()
	// Establish the baseline heap RIGHT NOW (post-eager, post-GC).
	// Anything above baseline+delta means our own queries are pressuring
	// memory; back off. Below means there's headroom — proceed. This
	// adapts to the process's actual steady-state heap (memdb + caches)
	// instead of an absolute ceiling we have to guess at deploy time.
	runtime.GC()
	debug.FreeOSMemory()
	baselineMB := readHeapAllocMB()
	ceilingMB := baselineMB + deltaMB
	// Trickle queries run under bgCtx so Stop()'s bgCancel aborts the
	// warmer before the store closes. This loop runs ~30 min at the
	// default 10s interval; with context.Background() it outlived test
	// servers and panicked "pebble: closed" against their closed stores.
	ctx := s.bgCtx
	started := time.Now()
	// F2: re-sample the baseline every 5 minutes inside the trickle loop
	// to track real heap drift from concurrent workloads (dedup,
	// embedding, chromem hydrate release). Median of last 3 samples
	// dampens jitter so one transient spike/drop doesn't move the
	// ceiling.
	const resampleInterval = 5 * time.Minute
	const ceilingChangeLogThreshold = 0.10 // log if |delta| > 10%
	baselineSamples := []uint64{baselineMB, baselineMB, baselineMB}
	lastResample := time.Now()
	slog.Info("library list trickle warmer starting",
		"queries", len(backlog),
		"interval_ms", interval.Milliseconds(),
		"baseline_mb", baselineMB,
		"delta_mb", deltaMB,
		"effective_ceiling_mb", ceilingMB,
		"resample_interval_ms", resampleInterval.Milliseconds(),
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var (
		idx       int
		hits      int
		misses    int
		cached    int
		skipped   int
		resampled int
		backoff   time.Duration
	)
	for idx < len(backlog) {
		select {
		case <-ctx.Done():
			slog.Info("library list trickle warmer: aborting, server shutting down",
				"completed", idx, "remaining", len(backlog)-idx)
			return
		case <-ticker.C:
		}
		if backoff > 0 {
			select {
			case <-ctx.Done():
				slog.Info("library list trickle warmer: aborting during backoff, server shutting down",
					"completed", idx, "remaining", len(backlog)-idx)
				return
			case <-time.After(backoff):
			}
		}

		// F2: periodically re-sample baseline + recompute ceiling.
		if time.Since(lastResample) >= resampleInterval {
			lastResample = time.Now()
			newSample := readHeapAllocMB()
			// shift ring buffer (oldest out, newest in)
			baselineSamples = append(baselineSamples[1:], newSample)
			newBaseline := medianUint64(baselineSamples)
			newCeiling := newBaseline + deltaMB
			oldCeiling := ceilingMB
			oldBaseline := baselineMB
			changeRatio := 0.0
			if oldCeiling > 0 {
				diff := float64(newCeiling) - float64(oldCeiling)
				if diff < 0 {
					diff = -diff
				}
				changeRatio = diff / float64(oldCeiling)
			}
			baselineMB = newBaseline
			ceilingMB = newCeiling
			resampled++
			if changeRatio > ceilingChangeLogThreshold {
				slog.Info("library list trickle warmer ceiling resampled",
					"old_baseline_mb", oldBaseline,
					"new_baseline_mb", newBaseline,
					"old_ceiling_mb", oldCeiling,
					"new_ceiling_mb", newCeiling,
					"sample_mb", newSample,
					"change_pct", int(changeRatio*100),
				)
			} else {
				slog.Debug("library list trickle warmer ceiling resampled (small change)",
					"old_ceiling_mb", oldCeiling,
					"new_ceiling_mb", newCeiling,
					"sample_mb", newSample,
				)
			}
		}

		// Memory-pressure guard: skip this tick if heap is above
		// baseline+delta. Double the backoff (capped 60s); reset on
		// successful tick. HeapAlloc, not RSS — we care about live
		// objects, not returned-to-OS memory.
		if heap := readHeapAllocMB(); heap > ceilingMB {
			skipped++
			if backoff == 0 {
				backoff = interval
			} else if backoff < 60*time.Second {
				backoff *= 2
			}
			slog.Warn("library list trickle warmer: heap above ceiling, backing off",
				"heap_mb", heap, "ceiling_mb", ceilingMB, "baseline_mb", baselineMB,
				"backoff_ms", backoff.Milliseconds())
			continue
		}
		backoff = 0

		q := backlog[idx]
		idx++

		// Skip if already cached (user query or earlier warmer round beat us).
		raw := buildListCacheRawQuery(q.limit, q.offset, q.filters)
		cacheKey := "list:" + raw
		if len(q.filters.PerUserFilters) == 0 {
			if _, ok := s.listCache.Get(cacheKey); ok {
				continue
			}
		}

		qStart := time.Now()
		resp, err := s.buildAudiobookListResponse(ctx, q.limit, q.offset, "", nil, nil, q.filters, false)
		if err != nil {
			misses++
			slog.Warn("library list trickle warmer query failed",
				"name", q.name, "offset", q.offset, "err", err)
		} else {
			hits++
			if len(q.filters.PerUserFilters) == 0 {
				s.listCache.Set(cacheKey, resp)
				cached++
			}
			slog.Debug("library list trickle warmer query ok",
				"name", q.name, "offset", q.offset,
				"duration_ms", time.Since(qStart).Milliseconds(),
				"progress", fmt.Sprintf("%d/%d", idx, len(backlog)),
			)
		}

		// Force GC + return-to-OS so the next tick starts from a clean
		// allocation baseline. The cost (~5-20ms on a 1GB heap) is
		// negligible against the interval.
		runtime.GC()
		debug.FreeOSMemory()
	}

	slog.Info("library list trickle warmer complete",
		"queries_warmed", hits,
		"cache_entries_populated", cached,
		"failures", misses,
		"skipped_pressure", skipped,
		"baseline_resamples", resampled,
		"final_baseline_mb", baselineMB,
		"final_ceiling_mb", ceilingMB,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

// medianUint64 returns the median of a small slice of uint64 values
// (used for the trickle warmer's 3-sample baseline ring buffer).
// Operates on a copy so the caller's slice is not reordered.
func medianUint64(in []uint64) uint64 {
	n := len(in)
	if n == 0 {
		return 0
	}
	cp := make([]uint64, n)
	copy(cp, in)
	// Tiny n (3) — insertion sort is fine and avoids importing sort.
	for i := 1; i < n; i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}
