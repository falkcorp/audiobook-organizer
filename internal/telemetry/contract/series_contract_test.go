// file: internal/telemetry/contract/series_contract_test.go
// version: 1.2.3
// guid: c0ffe83b-1164-4b85-915e-820f693efdc1
// last-edited: 2026-10-10

// Package contract pins the /metrics series-name contract: the name, type and
// label names of every Prometheus family the binary exports, read from
// testdata/series.golden.
//
// It lives in its own directory so it is its own test binary (its own
// process). internal/telemetry's own tests install a no-op meter provider
// first, and the OTel global delegates only to the first provider set, so a
// test here sharing that binary could silently scrape nothing from OTel.
//
// Adding, renaming or removing a family is a change to the golden in the same
// PR, plus a row in seedingTable below so the family is guaranteed to appear in
// the scrape (an unused client_golang Vec, an OTel instrument with no data
// point and the Pebble collector without a source all export nothing).
package contract

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

const (
	goldenPath   = "testdata/series.golden"
	reservedPath = "testdata/series_reserved.txt"

	sourceClientGolang = "client_golang" // the default when the 4th column is empty
	sourceOTel         = "otel"          // our own instruments, via telemetry.Meter
	sourceOtelgin      = "otelgin"       // otelgin.Middleware in internal/server
)

// inScopePrefixes are the family-name prefixes the contract owns. go_*,
// process_*, promhttp_* and scrape_* vary with the Go version and OS and are
// not asserted on.
var inScopePrefixes = []string{"audiobook_organizer_", "ai_dispatch_", "ai_", "http_server_"}

// targetInfo is the OTel exporter's resource family; it is in scope but not in
// the golden (it carries the resource, not an instrument).
const targetInfo = "target_info"

// ---------------------------------------------------------------------------
// Golden

type goldenRow struct {
	name   string
	typ    string
	labels []string // sorted
	source string
}

func (r goldenRow) String() string {
	return fmt.Sprintf("%s (%s, labels [%s], source %s)", r.name, r.typ, strings.Join(r.labels, ","), r.source)
}

func readGolden() ([]goldenRow, error) {
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		return nil, err
	}
	var rows []goldenRow
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		n := i + 1
		if line == "" {
			return nil, fmt.Errorf("%s:%d: blank line (the file is one row per family, no blanks or comments)", goldenPath, n)
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 3 || len(cols) > 4 {
			return nil, fmt.Errorf("%s:%d: want name<TAB>type<TAB>labels[<TAB>source], got %d columns", goldenPath, n, len(cols))
		}
		r := goldenRow{name: cols[0], typ: cols[1], source: sourceClientGolang}
		if len(cols) == 4 && cols[3] != "" {
			r.source = cols[3]
		}
		switch r.typ {
		case "counter", "gauge", "histogram", "summary", "untyped":
		default:
			return nil, fmt.Errorf("%s:%d: unknown type %q", goldenPath, n, r.typ)
		}
		switch r.source {
		case sourceClientGolang, sourceOTel, sourceOtelgin:
		default:
			return nil, fmt.Errorf("%s:%d: unknown source %q", goldenPath, n, r.source)
		}
		if cols[2] != "" {
			r.labels = strings.Split(cols[2], ",")
			if !slices.IsSorted(r.labels) || len(slices.Compact(slices.Clone(r.labels))) != len(r.labels) {
				return nil, fmt.Errorf("%s:%d: labels %q must be sorted and unique", goldenPath, n, cols[2])
			}
		}
		if seen[r.name] {
			return nil, fmt.Errorf("%s:%d: duplicate family %s", goldenPath, n, r.name)
		}
		seen[r.name] = true
		rows = append(rows, r)
	}
	return rows, nil
}

func readReserved() ([]string, error) {
	b, err := os.ReadFile(reservedPath)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}

// ---------------------------------------------------------------------------
// Seeding: one row per golden family, saying how the test makes it appear.

type seedRow struct {
	family string
	via    string
	seed   func() error
}

func do(f func()) func() error { return func() error { f(); return nil } }

// once wraps a seed shared by several rows so it runs a single time.
func once(f func() error) func() error {
	var o sync.Once
	var err error
	return func() error { o.Do(func() { err = f() }); return err }
}

var seedingTable = buildSeedingTable()

func buildSeedingTable() []seedRow {
	pebble := once(seedPebble)
	failover := once(seedDispatchFailover)
	noCapable := once(seedDispatchNoCapable)
	gin := once(seedOtelgin)
	opsSeed := once(seedOpsMetrics)
	pebbleRow := func(name string) seedRow {
		return seedRow{"audiobook_organizer_pebble_" + name, "metrics.SetPebbleSource(main, fixedSample)", pebble}
	}
	return []seedRow{
		{"audiobook_organizer_memdb_fallback_reads_total", "(&database.PebbleStore{UseMemDB: true}).GetBooksByMetadataSourceHashInMemory on an unpublished memdb", once(seedMemdbFallback)},
		{"audiobook_organizer_operations_started_total", "metrics.IncOperationStarted", do(func() { metrics.IncOperationStarted("contract") })},
		{"audiobook_organizer_operations_completed_total", "metrics.IncOperationCompleted", do(func() { metrics.IncOperationCompleted("contract") })},
		{"audiobook_organizer_operations_failed_total", "metrics.IncOperationFailed", do(func() { metrics.IncOperationFailed("contract") })},
		{"audiobook_organizer_operations_canceled_total", "metrics.IncOperationCanceled", do(func() { metrics.IncOperationCanceled("contract") })},
		{"audiobook_organizer_operation_duration_seconds", "metrics.ObserveOperationDuration", do(func() { metrics.ObserveOperationDuration("contract", time.Second) })},
		{"audiobook_organizer_books_total", "metrics.SetBooks", do(func() { metrics.SetBooks(1) })},
		{"audiobook_organizer_search_index_docs_total", "metrics.SetSearchIndexDocs", do(func() { metrics.SetSearchIndexDocs(1) })},
		{"audiobook_organizer_search_index_dropped_total", "metrics.IncSearchIndexDropped", do(metrics.IncSearchIndexDropped)},
		{"audiobook_organizer_op_activity_mirror_dropped_total", "metrics.IncOpActivityMirrorDropped", do(metrics.IncOpActivityMirrorDropped)},
		{"audiobook_organizer_sort_by_requested_total", "metrics.IncSortByRequested", do(func() { metrics.IncSortByRequested("title") })},
		{"audiobook_organizer_operation_deprecated_def_id_total", "metrics.IncOperationDeprecatedDefID", do(func() { metrics.IncOperationDeprecatedDefID("old", "new") })},
		{"audiobook_organizer_search_cache_patch_cap_rebuilds_total", "metrics.IncSearchCachePatchCapRebuild", do(metrics.IncSearchCachePatchCapRebuild)},
		{"audiobook_organizer_search_cache_rebuilds_total", "metrics.IncSearchCacheRebuild", do(metrics.IncSearchCacheRebuild)},
		{"audiobook_organizer_search_index_dirty_backlog", "metrics.SetSearchIndexDirtyBacklog", do(func() { metrics.SetSearchIndexDirtyBacklog(1) })},
		{"audiobook_organizer_opchange_by_book_index_trusted", "metrics.SetOpChangeByBookIndexTrusted", do(func() { metrics.SetOpChangeByBookIndexTrusted(true) })},
		{"audiobook_organizer_opsv2_timeline_index_trusted", "metrics.SetOpsV2TimelineIndexTrusted", do(func() { metrics.SetOpsV2TimelineIndexTrusted(true) })},
		{"audiobook_organizer_merge_user_state_pending", "metrics.SetMergeUserStatePending", do(func() { metrics.SetMergeUserStatePending(1) })},
		{"audiobook_organizer_harvest_authors", "metrics.SetCatalogHarvestAuthors", do(func() { metrics.SetCatalogHarvestAuthors(map[string]int{"complete": 1}) })},
		{"audiobook_organizer_catalog_entries_stale", "metrics.SetCatalogEntriesStale", do(func() { metrics.SetCatalogEntriesStale(1) })},
		{"audiobook_organizer_import_paths_total", "metrics.SetFolders", do(func() { metrics.SetFolders(1) })},
		{"audiobook_organizer_process_memory_alloc_bytes", "metrics.SetMemoryAlloc", do(func() { metrics.SetMemoryAlloc(1) })},
		{"audiobook_organizer_process_goroutines", "metrics.SetGoroutines", do(func() { metrics.SetGoroutines(1) })},
		{"audiobook_organizer_cache_hits_total", "metrics.RecordCacheHit", do(func() { metrics.RecordCacheHit("contract") })},
		{"audiobook_organizer_cache_misses_total", "metrics.RecordCacheMiss", do(func() { metrics.RecordCacheMiss("contract", "not_found") })},
		{"audiobook_organizer_cache_sets_total", "metrics.RecordCacheSet", do(func() { metrics.RecordCacheSet("contract") })},
		{"audiobook_organizer_cache_invalidations_total", "metrics.RecordCacheInvalidation", do(func() { metrics.RecordCacheInvalidation("contract", "all") })},
		{"audiobook_organizer_cache_evictions_total", "metrics.RecordCacheEviction", do(func() { metrics.RecordCacheEviction("contract", "size") })},
		{"audiobook_organizer_cache_size", "metrics.SetCacheSize", do(func() { metrics.SetCacheSize("contract", 1) })},
		{"audiobook_organizer_cache_get_duration_seconds", "metrics.ObserveCacheGetDuration", do(func() { metrics.ObserveCacheGetDuration("contract", time.Microsecond) })},
		{"audiobook_organizer_organize_target_path_collision_total", "metrics.RecordOrganizeTargetPathCollision", do(metrics.RecordOrganizeTargetPathCollision)},
		{"audiobook_organizer_ai_backend_available", "metrics.SetBackendAvailable", do(func() { metrics.SetBackendAvailable("contract", true) })},
		{"audiobook_organizer_op_items_processed", "metrics.SetOpProgress", do(func() { metrics.SetOpProgress("contract-op", "contract", 1, 2) })},
		{"audiobook_organizer_op_items_total", "metrics.SetOpProgress", do(func() { metrics.SetOpProgress("contract-op", "contract", 1, 2) })},
		{"audiobook_organizer_abs_listening_stats_read_failures_total", "metrics.IncABSListeningStatsReadFailures", do(metrics.IncABSListeningStatsReadFailures)},

		{"audiobook_organizer_filename_parse_total", "metrics.IncFilenameParse", do(func() { metrics.IncFilenameParse("contract", "ok") })},
		{"audiobook_organizer_metadata_fetch_total", "metrics.IncMetadataFetch", do(func() { metrics.IncMetadataFetch("contract", "api") })},
		{"audiobook_organizer_review_index_request_seconds", "metrics.ObserveReviewIndexRequest", do(func() { metrics.ObserveReviewIndexRequest("contract", time.Second) })},
		{"audiobook_organizer_number_leading_titles", "metrics.SetNumberLeadingTitles", do(func() { metrics.SetNumberLeadingTitles(1) })},
		{"audiobook_organizer_fixer_duration_seconds", "metrics.ObserveFixerDuration", do(func() { metrics.ObserveFixerDuration("contract", "trial", time.Second) })},

		{"audiobook_organizer_ops_runs_total", "opsmetrics Started and Finished", opsSeed},
		{"audiobook_organizer_ops_run_duration_seconds", "opsmetrics Finished", opsSeed},
		{"audiobook_organizer_ops_items_total", "opsmetrics Items", opsSeed},
		{"audiobook_organizer_ops_inflight", "opsmetrics RegisterInflight", opsSeed},

		{"ai_dispatch_requests_total", "aidispatch.Call with failover", failover},
		{"ai_dispatch_inflight", "aidispatch.Call with failover", failover},
		{"ai_dispatch_failover_total", "aidispatch.Call with failover", failover},
		{"ai_dispatch_slot_wait_seconds", "aidispatch.Call with failover", failover},
		{"ai_dispatch_no_capable_total", "aidispatch.Call with no capable endpoint", noCapable},

		pebbleRow("block_cache_bytes"),
		pebbleRow("block_cache_blocks"),
		pebbleRow("block_cache_hits_total"),
		pebbleRow("block_cache_misses_total"),
		pebbleRow("filter_hits_total"),
		pebbleRow("filter_misses_total"),
		pebbleRow("read_amplification"),
		pebbleRow("l0_files"),
		pebbleRow("l0_sublevels"),
		pebbleRow("compactions_total"),
		pebbleRow("compaction_debt_bytes"),
		pebbleRow("compactions_in_progress"),
		pebbleRow("compaction_in_progress_bytes"),
		pebbleRow("memtable_bytes"),
		pebbleRow("memtables"),
		pebbleRow("wal_bytes"),
		pebbleRow("wal_physical_bytes"),
		pebbleRow("wal_files"),
		pebbleRow("disk_usage_bytes"),
		pebbleRow("level_bytes"),
		pebbleRow("level_files"),
		pebbleRow("level_write_amplification"),
		pebbleRow("level_bytes_in_total"),
		pebbleRow("level_bytes_flushed_total"),
		pebbleRow("level_bytes_compacted_total"),

		{"http_server_request_duration_seconds", "one GET through otelgin.Middleware", gin},
		{"http_server_request_body_size_bytes", "one GET through otelgin.Middleware", gin},
		{"http_server_response_body_size_bytes", "one GET through otelgin.Middleware", gin},
	}
}

// fixedSample is a copy of internal/metrics/pebble_collector_test.go's
// fixedSample (test-only in package metrics).
func fixedSample() metrics.PebbleSample {
	s := metrics.PebbleSample{BlockCacheBytes: 1, BlockCacheBlocks: 2, BlockCacheHits: 3, BlockCacheMisses: 4,
		FilterHits: 5, FilterMisses: 6, ReadAmp: 7, L0Files: 8, L0Sublevels: 9, Compactions: 10,
		CompactionDebtBytes: 11, CompactionsInProgress: 12, CompactionInProgressBytes: 13,
		MemTableBytes: 14, MemTables: 15, WALBytes: 16, WALPhysicalBytes: 17, WALFiles: 18, DiskUsageBytes: 19}
	for i := range s.LevelBytes {
		s.LevelBytes[i], s.LevelFiles[i], s.LevelBytesIn[i] = float64(i+1), float64(i+2), float64(i+3)
		s.LevelBytesFlushed[i], s.LevelBytesCompacted[i], s.LevelWriteAmp[i] = float64(i+4), float64(i+5), float64(i+6)
	}
	return s
}

func seedPebble() error {
	// Never released: the source lives as long as the test process.
	metrics.SetPebbleSource(metrics.PebbleStoreMain, func() (metrics.PebbleSample, bool) { return fixedSample(), true })
	return nil
}

// chatEP, isolated and dialRefused re-create the internal/aidispatch test
// fixtures (dispatch_test.go) with the exported API.
func chatEP(id string, prio int, caps ...string) aidispatch.Endpoint {
	return aidispatch.Endpoint{ID: id, Protocol: aidispatch.ProtocolOpenAICompat, ChatModel: "chat-m", EmbedModel: "embed-m",
		Priority: prio, Enabled: true, Capabilities: caps}
}

func isolated(eps []aidispatch.Endpoint) *aidispatch.Dispatcher {
	return aidispatch.New(eps, aidispatch.WithSlots(aidispatch.NewSlots()), aidispatch.WithHealth(aidispatch.NewHealth()),
		aidispatch.WithAttribution(aidispatch.NewAttribution()))
}

// dialRefused is a connection-refused transport error, which aidispatch
// classifies as a failover.
var dialRefused = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

// seedDispatchFailover follows TestCall_FailoverOnTransportAndBench: the first
// endpoint refuses the connection, the second answers. That gives children to
// requests_total, inflight, slot_wait_seconds and failover_total.
func seedDispatchFailover() error {
	fp := aidispatch.LLMFilenameParse.ID()
	d := isolated([]aidispatch.Endpoint{chatEP("contract-primary", 1, fp), chatEP("contract-secondary", 2, fp)})
	var seen []string
	got, err := aidispatch.Call(context.Background(), d, aidispatch.LLMFilenameParse,
		func(_ context.Context, tg aidispatch.Target) (string, error) {
			seen = append(seen, tg.Endpoint.ID)
			if tg.Endpoint.ID == "contract-primary" {
				return "", dialRefused
			}
			return "ok", nil
		})
	if err != nil || got != "ok" || len(seen) != 2 {
		return fmt.Errorf("failover seed: got %q, err %v, attempts %v; want a failover from contract-primary to contract-secondary", got, err, seen)
	}
	return nil
}

// seedDispatchNoCapable calls a capability no configured endpoint carries.
func seedDispatchNoCapable() error {
	d := isolated([]aidispatch.Endpoint{chatEP("contract-other", 1, aidispatch.LLMAudiobookParse.ID())})
	_, err := aidispatch.Call(context.Background(), d, aidispatch.LLMFilenameParse,
		func(context.Context, aidispatch.Target) (int, error) { return 0, nil })
	if !errors.Is(err, aidispatch.ErrNoCapableEndpoint) {
		return fmt.Errorf("no-capable seed: err %v, want ErrNoCapableEndpoint", err)
	}
	return nil
}

// seedMemdbFallback makes one read that wants the in-memory layer find it
// unpublished: a zero-value store with UseMemDB on has no memdb, and this
// method refuses without touching Pebble, so it counts outcome=refused.
func seedMemdbFallback() error {
	s := &database.PebbleStore{UseMemDB: true}
	s.SetMeterProvider(otel.GetMeterProvider())
	if _, err := s.GetBooksByMetadataSourceHashInMemory("contract"); !errors.Is(err, database.ErrMemDBNotReady) {
		return fmt.Errorf("memdb fallback seed: err %v, want ErrMemDBNotReady", err)
	}
	return nil
}

// seedOpsMetrics records one point on every instrument the v2 registry wires.
// The in-flight callback is never unregistered: the source lives as long as
// the test process.
func seedOpsMetrics() error {
	opsmetrics.RegisterDefID("contract")
	r := opsmetrics.Default()
	r.Started("contract")
	if !r.Finished("contract", "completed", time.Second) {
		return fmt.Errorf("ops seed: Finished(completed) recorded nothing")
	}
	r.Items("contract", opsmetrics.ItemsProcessed, 1)
	r.RegisterInflight(func() map[opsmetrics.InflightKey]int64 {
		return map[opsmetrics.InflightKey]int64{{DefID: "contract", Plugin: "contract"}: 1}
	})
	return nil
}

// seedOtelgin serves one request through the same middleware the server
// installs (internal/server/server.go router.Use(otelgin.Middleware(...))).
func seedOtelgin() error {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(otelgin.Middleware("audiobook-organizer"))
	r.GET("/contract", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	rec := httptest.NewRecorder()
	// A host:port target, as production requests carry, so server_port is
	// pinned in the golden too.
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test:8484/contract", nil))
	if rec.Code != http.StatusOK {
		return fmt.Errorf("otelgin seed: status %d", rec.Code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// TestMain: the OTel meter provider first, then every seed.

func TestMain(m *testing.M) {
	ctx := context.Background()
	shutdown, err := telemetry.InitOTEL(ctx, telemetry.LoadConfig("contract-test", ""))
	if err != nil {
		fmt.Fprintln(os.Stderr, "InitOTEL:", err)
		os.Exit(1)
	}
	metrics.Register()
	aidispatch.RegisterMetrics()
	for _, row := range seedingTable {
		if err := row.seed(); err != nil {
			fmt.Fprintf(os.Stderr, "seeding %s via %s: %v\n", row.family, row.via, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = shutdown(ctx)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Scrape

type scrapedFamily struct {
	typ    string
	labels map[string]bool    // label names across all samples, le/quantile excluded
	les    map[float64]string // histogram bucket bounds (finite only) -> raw le text
}

// scrape serves one GET /metrics through promhttp.Handler() on the default
// registry, exactly as the server mounts it, and parses the text exposition.
func scrape(t *testing.T) map[string]*scrapedFamily {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics returned %d: %s", rec.Code, rec.Body.String())
	}
	fams, err := parseExposition(rec.Body.String())
	if err != nil {
		t.Fatalf("parse /metrics: %v", err)
	}
	return fams
}

// parseExposition reads the Prometheus text format: "# TYPE name type" opens a
// family and the sample lines that follow belong to it.
func parseExposition(body string) (map[string]*scrapedFamily, error) {
	fams := map[string]*scrapedFamily{}
	var cur *scrapedFamily
	for n, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, typ, ok := strings.Cut(rest, " ")
			if !ok {
				return nil, fmt.Errorf("line %d: malformed TYPE line %q", n+1, line)
			}
			cur = &scrapedFamily{typ: typ, labels: map[string]bool{}, les: map[float64]string{}}
			fams[name] = cur
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d: sample before any TYPE line: %q", n+1, line)
		}
		open := strings.IndexByte(line, '{')
		if open < 0 {
			continue // a sample with no labels
		}
		labels, err := parseLabels(line[open+1:])
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n+1, err)
		}
		for k, v := range labels {
			switch k {
			case "le":
				if v != "+Inf" {
					f, err := strconv.ParseFloat(v, 64)
					if err != nil {
						return nil, fmt.Errorf("line %d: le %q: %v", n+1, v, err)
					}
					cur.les[f] = v
				}
			case "quantile":
			default:
				cur.labels[k] = true
			}
		}
	}
	return fams, nil
}

// parseLabels reads `a="x",b="y\"z"} value` up to the closing brace.
func parseLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	i := 0
	for {
		if i >= len(s) {
			return nil, fmt.Errorf("unterminated label set")
		}
		if s[i] == '}' {
			return out, nil
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 || i+eq+1 >= len(s) || s[i+eq+1] != '"' {
			return nil, fmt.Errorf("malformed label at %q", s[i:])
		}
		name := s[i : i+eq]
		j := i + eq + 2
		var val strings.Builder
		for ; j < len(s) && s[j] != '"'; j++ {
			if s[j] == '\\' && j+1 < len(s) {
				j++
			}
			val.WriteByte(s[j])
		}
		if j >= len(s) {
			return nil, fmt.Errorf("unterminated value for label %s", name)
		}
		out[name] = val.String()
		i = j + 1
		if i < len(s) && s[i] == ',' {
			i++
		}
	}
}

func inScope(name string) bool {
	if name == targetInfo {
		return true
	}
	for _, p := range inScopePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string { return slices.Sorted(maps.Keys(m)) }

func mustGolden(t *testing.T) []goldenRow {
	t.Helper()
	rows, err := readGolden()
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return rows
}

// ---------------------------------------------------------------------------
// Tests

// TestSeriesContract is the guard for owner decision D66 (Prometheus
// compatibility) over the seeded scrape:
//
//   - (A) every golden family is on /metrics with its golden type and label
//     names, so a rename, retype, relabel or removal fails here;
//   - (B) every in-scope family on /metrics is in the golden.
//
// (B) can only see families that export something, i.e. that a seedingTable
// row touched. A new family nobody seeds is caught statically instead:
// TestDeclaredFamiliesInGolden (declared_families_test.go) parses every
// client_golang constructor and OTel instrument call in internal/, pkg/ and
// cmd/ and fails on any name without a golden row. Families created by
// third-party code (otelgin) are seen only through seeding.
func TestSeriesContract(t *testing.T) {
	rows := mustGolden(t)

	// The seeding table and the golden must name the same families.
	golden := map[string]goldenRow{}
	for _, r := range rows {
		golden[r.name] = r
	}
	seeded := map[string]bool{}
	for _, s := range seedingTable {
		seeded[s.family] = true
	}
	var unseeded, orphanSeeds []string
	for _, r := range rows {
		if !seeded[r.name] {
			unseeded = append(unseeded, r.name)
		}
	}
	for name := range seeded {
		if _, ok := golden[name]; !ok {
			orphanSeeds = append(orphanSeeds, name)
		}
	}
	slices.Sort(orphanSeeds)
	t.Logf("seeding rows: %d", len(seeded))
	t.Logf("golden rows: %d", len(rows))
	if len(unseeded) > 0 {
		t.Errorf("%d golden families have no row in seedingTable (add one, or the scrape may not expose them):\n  %s",
			len(unseeded), strings.Join(unseeded, "\n  "))
	}
	if len(orphanSeeds) > 0 {
		t.Errorf("%d seedingTable rows name a family that is not in %s (add the golden row or drop the seed):\n  %s",
			len(orphanSeeds), goldenPath, strings.Join(orphanSeeds, "\n  "))
	}

	fams := scrape(t)

	// (A) every golden family is exposed with its golden type and label names.
	var missing, wrongType, wrongLabels []string
	for _, r := range rows {
		f, ok := fams[r.name]
		if !ok {
			missing = append(missing, r.String())
			continue
		}
		if f.typ != r.typ {
			wrongType = append(wrongType, fmt.Sprintf("%s: golden type %s, /metrics type %s", r.name, r.typ, f.typ))
		}
		if got := sortedKeys(f.labels); !slices.Equal(got, r.labels) {
			wrongLabels = append(wrongLabels, fmt.Sprintf("%s: golden labels [%s], /metrics labels [%s]",
				r.name, strings.Join(r.labels, ","), strings.Join(got, ",")))
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d golden families are missing from /metrics: renamed or removed. A dashboard or alert may read them; "+
			"restore the name, or, if the removal is intended, delete the row from %s and its seedingTable row in this PR "+
			"and ship a recording rule for any dashboard or alert that uses it:\n  %s",
			len(missing), goldenPath, strings.Join(missing, "\n  "))
	}
	if len(wrongType) > 0 {
		t.Errorf("%d families changed type (a type change breaks rate()/histogram_quantile() queries):\n  %s",
			len(wrongType), strings.Join(wrongType, "\n  "))
	}
	if len(wrongLabels) > 0 {
		t.Errorf("%d families changed label names (a relabel breaks every query that groups or filters by them):\n  %s",
			len(wrongLabels), strings.Join(wrongLabels, "\n  "))
	}

	// (B) every in-scope family on /metrics is in the golden.
	var extra []string
	for name, f := range fams {
		if !inScope(name) || name == targetInfo {
			continue
		}
		if _, ok := golden[name]; !ok {
			extra = append(extra, fmt.Sprintf("%s\t%s\t%s", name, f.typ, strings.Join(sortedKeys(f.labels), ",")))
		}
	}
	slices.Sort(extra)
	if len(extra) > 0 {
		t.Errorf("%d families on /metrics are not in the contract: add it to series.golden in this PR "+
			"(and a seedingTable row); the golden-format line for each is:\n  %s",
			len(extra), strings.Join(extra, "\n  "))
	}
	if _, ok := fams[targetInfo]; !ok {
		t.Errorf("/metrics has no %s: the OTel Prometheus exporter is not registered", targetInfo)
	}
}

// TestReservedAISeries: the AI call families 11-PR7 will add are reserved; no
// family may claim one of those names before PR7 moves them into the golden.
func TestReservedAISeries(t *testing.T) {
	reserved, err := readReserved()
	if err != nil {
		t.Fatalf("read reserved: %v", err)
	}
	if len(reserved) == 0 {
		t.Fatalf("%s lists no names", reservedPath)
	}
	golden := map[string]bool{}
	for _, r := range mustGolden(t) {
		golden[r.name] = true
	}
	fams := scrape(t)
	for _, name := range reserved {
		if _, ok := fams[name]; ok {
			t.Errorf("reserved family %s is exposed on /metrics before 11-PR7: only PR7 may claim it, moving it from %s to %s",
				name, reservedPath, goldenPath)
		}
		if golden[name] {
			t.Errorf("%s is both reserved and in the golden: remove it from %s", name, reservedPath)
		}
	}
}

// TestNoScopeLabels: with WithoutScopeInfo no OTel series carries
// instrumentation-scope labels, so OTel families keep the label sets of the
// client_golang families they replace. otelgin's series in the scrape (seeded
// in TestMain) are the real-world check; a probe through telemetry.Meter is
// the second. The probe's name is outside the contract's prefixes so it does
// not show up as an extra family in TestSeriesContract.
func TestNoScopeLabels(t *testing.T) {
	c, err := telemetry.Meter("contract").Int64Counter("contract.scope_probe")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 1)

	fams := scrape(t)
	if _, ok := fams["contract_scope_probe_total"]; !ok {
		t.Fatal("the probe counter did not reach /metrics: the scope check would prove nothing")
	}
	for name, f := range fams {
		for label := range f.labels {
			if strings.HasPrefix(label, "otel_scope_") {
				t.Errorf("%s carries scope label %s: prometheus.WithoutScopeInfo() is not in effect", name, label)
			}
		}
	}
}

// TestAttributeKeysAllowlisted (rule C3): every label in the golden and on the
// in-scope part of the scrape is a key from internal/telemetry/attr.go, in
// either the instrument list or the scrape-only list (which must not overlap).
func TestAttributeKeysAllowlisted(t *testing.T) {
	for _, k := range telemetry.ScrapeOnlyAttributeKeys() {
		if slices.Contains(telemetry.AttributeKeys(), k) {
			t.Errorf("attr.go key %q is in both the instrument and the scrape-only list", k)
		}
	}
	allowed := map[string]attribute.Key{}
	for _, k := range append(telemetry.AttributeKeys(), telemetry.ScrapeOnlyAttributeKeys()...) {
		pn := telemetry.PrometheusLabelName(k)
		if prev, dup := allowed[pn]; dup {
			t.Errorf("attr.go keys %q and %q both export as label %q", prev, k, pn)
		}
		allowed[pn] = k
	}
	for _, r := range mustGolden(t) {
		for _, l := range r.labels {
			if _, ok := allowed[l]; !ok {
				t.Errorf("golden family %s uses label %q, which is not a key in internal/telemetry/attr.go", r.name, l)
			}
		}
	}
	for name, f := range scrape(t) {
		if !inScope(name) || name == targetInfo {
			continue
		}
		for l := range f.labels {
			if _, ok := allowed[l]; !ok {
				t.Errorf("/metrics family %s uses label %q, which is not a key in internal/telemetry/attr.go", name, l)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Instrument naming (spec 11 §3.1)

type instrumentKind int

const (
	kindCounter instrumentKind = iota
	kindUpDownCounter
	kindGauge
	kindHistogram
)

type instrumentSpec struct {
	name string // OTel instrument name, dotted
	kind instrumentKind
	unit string
	keys []attribute.Key
}

// ourInstruments declares every instrument the binary creates through
// telemetry.Meter, one row per golden row with source "otel". It is empty
// until the first family migrates (11-PR3); each migration adds its rows here
// and its golden rows' source becomes "otel".
var ourInstruments = []instrumentSpec{
	{name: "audiobook_organizer.memdb.fallback_reads", kind: kindCounter, keys: []attribute.Key{telemetry.Outcome, telemetry.Site}},
	{name: "audiobook_organizer.ops.runs", kind: kindCounter, unit: "{run}", keys: []attribute.Key{telemetry.DefID, telemetry.Outcome}},
	{name: "audiobook_organizer.ops.run.duration", kind: kindHistogram, unit: "s", keys: []attribute.Key{telemetry.DefID, telemetry.Outcome}},
	{name: "audiobook_organizer.ops.items", kind: kindCounter, unit: "{item}", keys: []attribute.Key{telemetry.DefID, telemetry.Outcome}},
	{name: "audiobook_organizer.ops.inflight", kind: kindGauge, unit: "{run}", keys: []attribute.Key{telemetry.DefID, telemetry.Plugin}},
	{name: "ai_dispatch.requests", kind: kindCounter, unit: "{request}", keys: []attribute.Key{telemetry.Capability, telemetry.Endpoint, telemetry.Outcome}},
	{name: "ai_dispatch.inflight", kind: kindUpDownCounter, unit: "{request}", keys: []attribute.Key{telemetry.Endpoint}},
	{name: "ai_dispatch.failover", kind: kindCounter, unit: "{request}", keys: []attribute.Key{telemetry.Capability, telemetry.Endpoint, telemetry.Class}},
	{name: "ai_dispatch.no_capable", kind: kindCounter, unit: "{request}", keys: []attribute.Key{telemetry.Capability}},
	{name: "ai_dispatch.slot_wait", kind: kindHistogram, unit: "s", keys: []attribute.Key{telemetry.Endpoint}},
}

var allowedNamePrefixes = []string{"audiobook_organizer.", "ai_dispatch.", "ai."}

// checkInstrument returns every naming-rule violation of spec.
func checkInstrument(spec instrumentSpec) []string {
	var problems []string
	if spec.name != strings.ToLower(spec.name) {
		problems = append(problems, "name must be lowercase")
	}
	if !slices.ContainsFunc(allowedNamePrefixes, func(p string) bool { return strings.HasPrefix(spec.name, p) }) {
		problems = append(problems, fmt.Sprintf("name must start with one of %v", allowedNamePrefixes))
	}
	if (spec.kind == kindCounter || spec.kind == kindUpDownCounter) && strings.HasSuffix(spec.name, "_total") {
		problems = append(problems, `a counter name must not end in "_total": the exporter appends it`)
	}
	if spec.unit == "1" {
		problems = append(problems, `unit "1" makes the exporter append "_ratio"; use a bracket unit such as "{book}"`)
	}
	if spec.kind == kindHistogram {
		if _, ok := telemetry.HistogramBuckets()[spec.name]; !ok {
			problems = append(problems, "histogram has no entry in internal/telemetry/views.go histogramBuckets")
		}
	}
	allowed := telemetry.AttributeKeys()
	for _, k := range spec.keys {
		switch {
		case slices.Contains(allowed, k):
		case slices.Contains(telemetry.ScrapeOnlyAttributeKeys(), k):
			problems = append(problems, fmt.Sprintf("attribute key %q is scrape-only in internal/telemetry/attr.go (legacy or otelgin); a new instrument may not use it", k))
		default:
			problems = append(problems, fmt.Sprintf("attribute key %q is not in internal/telemetry/attr.go", k))
		}
	}
	if len(spec.keys) > 4 {
		problems = append(problems, "more than 4 attributes (rule C2)")
	}
	return problems
}

// exportedName is the Prometheus family name the exporter derives.
func exportedName(spec instrumentSpec) string {
	n := strings.ReplaceAll(spec.name, ".", "_")
	switch spec.unit {
	case "s":
		n += "_seconds"
	case "By":
		n += "_bytes"
	}
	// Only a monotonic counter gets "_total"; an up-down counter exports as a
	// gauge.
	if spec.kind == kindCounter {
		n += "_total"
	}
	return n
}

// create makes spec's instrument on m and records one point.
func create(m otelmetric.Meter, spec instrumentSpec) error {
	ctx := context.Background()
	attrs := make([]attribute.KeyValue, 0, len(spec.keys))
	for _, k := range spec.keys {
		attrs = append(attrs, k.String("x"))
	}
	set := otelmetric.WithAttributes(attrs...)
	switch spec.kind {
	case kindCounter:
		c, err := m.Int64Counter(spec.name, otelmetric.WithUnit(spec.unit))
		if err != nil {
			return err
		}
		c.Add(ctx, 1, set)
	case kindUpDownCounter:
		c, err := m.Int64UpDownCounter(spec.name, otelmetric.WithUnit(spec.unit))
		if err != nil {
			return err
		}
		c.Add(ctx, 1, set)
	case kindGauge:
		g, err := m.Float64Gauge(spec.name, otelmetric.WithUnit(spec.unit))
		if err != nil {
			return err
		}
		g.Record(ctx, 1, set)
	case kindHistogram:
		h, err := m.Float64Histogram(spec.name, otelmetric.WithUnit(spec.unit))
		if err != nil {
			return err
		}
		h.Record(ctx, 1, set)
	}
	return nil
}

func TestInstrumentNames(t *testing.T) {
	// The rules themselves: each bad spec must be rejected, the good one not.
	bad := map[string]instrumentSpec{
		"counter ending in _total": {name: "audiobook_organizer.books.removed_total", kind: kindCounter, unit: "{book}"},
		`unit "1"`:                 {name: "audiobook_organizer.books.share", kind: kindGauge, unit: "1"},
		"key outside attr.go":      {name: "audiobook_organizer.books.removed", kind: kindCounter, keys: []attribute.Key{"book_id"}},
		"legacy op_id key":         {name: "audiobook_organizer.ops.items", kind: kindGauge, unit: "{item}", keys: []attribute.Key{telemetry.OpID}},
		"otelgin semconv key":      {name: "audiobook_organizer.http.hits", kind: kindCounter, keys: []attribute.Key{telemetry.HTTPRoute}},
		"no prefix":                {name: "books.removed", kind: kindCounter},
		"uppercase":                {name: "audiobook_organizer.Books.removed", kind: kindCounter},
		"histogram with no view":   {name: "audiobook_organizer.books.scan.duration", kind: kindHistogram, unit: "s"},
		"five attributes":          {name: "audiobook_organizer.books.removed", kind: kindCounter, keys: []attribute.Key{telemetry.Outcome, telemetry.Reason, telemetry.Kind, telemetry.Class, telemetry.Phase}},
	}
	for _, why := range slices.Sorted(maps.Keys(bad)) {
		if len(checkInstrument(bad[why])) == 0 {
			t.Errorf("rule check accepted the bad spec %q (%+v)", why, bad[why])
		}
	}
	good := instrumentSpec{name: "audiobook_organizer.ops.schedule_lag", kind: kindHistogram, unit: "s", keys: []attribute.Key{telemetry.DefID}}
	if p := checkInstrument(good); len(p) > 0 {
		t.Errorf("rule check rejected a valid spec: %v", p)
	}
	if got, want := exportedName(good), "audiobook_organizer_ops_schedule_lag_seconds"; got != want {
		t.Errorf("exportedName = %q, want %q", got, want)
	}

	// Every declared instrument obeys the rules, is creatable, and maps to a
	// golden row with source otel; every such golden row has a declaration.
	golden := map[string]goldenRow{}
	for _, r := range mustGolden(t) {
		golden[r.name] = r
	}
	declared := map[string]bool{}
	m := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader())).Meter("contract")
	for _, spec := range ourInstruments {
		if p := checkInstrument(spec); len(p) > 0 {
			t.Errorf("instrument %s: %s", spec.name, strings.Join(p, "; "))
		}
		if err := create(m, spec); err != nil {
			t.Errorf("instrument %s: create: %v", spec.name, err)
		}
		exp := exportedName(spec)
		declared[exp] = true
		if r, ok := golden[exp]; !ok || r.source != sourceOTel {
			t.Errorf("instrument %s exports %s, which is not a golden row with source %s", spec.name, exp, sourceOTel)
		}
	}
	for name, r := range golden {
		if r.source == sourceOTel && !declared[name] {
			t.Errorf("golden row %s has source %s but no ourInstruments declaration", name, sourceOTel)
		}
	}
}

// ---------------------------------------------------------------------------
// Views

// viewKeyFor finds the views-table entry whose exported name is family.
func viewKeyFor(family string, views map[string][]float64) (string, bool) {
	for key := range views {
		base := strings.ReplaceAll(key, ".", "_")
		if family == base || family == base+"_seconds" || family == base+"_bytes" {
			return key, true
		}
	}
	return "", false
}

// TestEveryHistogramHasAView: every histogram family in the golden has an
// entry in the views table, and for the OTel ones the view is what the SDK
// actually applies to an instrument of that name (a ManualReader provider
// built with telemetry.Views(), not the global one).
func TestEveryHistogramHasAView(t *testing.T) {
	views := telemetry.HistogramBuckets()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	m := provider.Meter("contract")

	want := map[string][]float64{}
	for _, r := range mustGolden(t) {
		if r.typ != "histogram" {
			continue
		}
		key, ok := viewKeyFor(r.name, views)
		if !ok {
			t.Errorf("histogram %s (source %s) has no entry in internal/telemetry/views.go histogramBuckets", r.name, r.source)
			continue
		}
		if r.source == sourceClientGolang {
			continue
		}
		h, err := m.Float64Histogram(key)
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		h.Record(context.Background(), 1)
		want[key] = views[key]
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string][]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			if h, ok := md.Data.(metricdata.Histogram[float64]); ok && len(h.DataPoints) > 0 {
				got[md.Name] = h.DataPoints[0].Bounds
			}
		}
	}
	for key, w := range want {
		if !slices.Equal(got[key], w) {
			t.Errorf("histogram %s: SDK applied bounds %v, views table says %v", key, got[key], w)
		}
	}
}

// TestHistogramBucketsPinned: the `le` text of every histogram family is
// pinned in testdata/histogram_buckets.golden, a file independent of
// internal/telemetry/views.go. Both the live scrape and the views table must
// match it, so neither a constructor edit nor a views.go edit (which is what
// shapes the otelgin and OTel scrape) can move a bucket without changing the
// pinned file in the same PR. Dashboards and recording rules select buckets
// by their `le` string, so the text is compared, not just the value.
func TestHistogramBucketsPinned(t *testing.T) {
	pinned, err := readPinnedBuckets()
	if err != nil {
		t.Fatalf("read %s: %v", bucketsPath, err)
	}
	views := telemetry.HistogramBuckets()
	fams := scrape(t)
	histograms := map[string]bool{}
	for _, r := range mustGolden(t) {
		if r.typ != "histogram" {
			continue
		}
		histograms[r.name] = true
		want, ok := pinned[r.name]
		if !ok {
			t.Errorf("histogram %s has no row in %s: add its le list", r.name, bucketsPath)
			continue
		}
		if f, ok := fams[r.name]; ok {
			if got := leText(f); !slices.Equal(got, want) {
				t.Errorf("histogram %s: /metrics le [%s], %s pins [%s]",
					r.name, strings.Join(got, ","), bucketsPath, strings.Join(want, ","))
			}
		}
		key, ok := viewKeyFor(r.name, views)
		if !ok {
			continue // TestEveryHistogramHasAView reports it
		}
		var got []string
		for _, b := range views[key] {
			if math.IsInf(b, 0) || math.IsNaN(b) {
				t.Errorf("views table %q has a non-finite bound %v", key, b)
			}
			got = append(got, strconv.FormatFloat(b, 'g', -1, 64))
		}
		if !slices.Equal(got, want) {
			t.Errorf("views table %q: [%s], %s pins %s at [%s]",
				key, strings.Join(got, ","), bucketsPath, r.name, strings.Join(want, ","))
		}
	}
	for name := range pinned {
		if !histograms[name] {
			t.Errorf("%s pins %s, which is not a histogram row in %s", bucketsPath, name, goldenPath)
		}
	}
}

const bucketsPath = "testdata/histogram_buckets.golden"

// readPinnedBuckets reads `family<TAB>le,le,...` rows (finite bounds only, in
// ascending order, exactly as /metrics writes them).
func readPinnedBuckets() (map[string][]string, error) {
	b, err := os.ReadFile(bucketsPath)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		name, les, ok := strings.Cut(line, "\t")
		if !ok || name == "" || les == "" {
			return nil, fmt.Errorf("line %d: want family<TAB>le,le,..., got %q", i+1, line)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("line %d: duplicate family %s", i+1, name)
		}
		out[name] = strings.Split(les, ",")
	}
	return out, nil
}

// leText returns a family's finite bucket bounds as /metrics wrote them, in
// ascending order.
func leText(f *scrapedFamily) []string {
	var out []string
	for _, v := range slices.Sorted(maps.Keys(f.les)) {
		out = append(out, f.les[v])
	}
	return out
}
