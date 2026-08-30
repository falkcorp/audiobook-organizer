// file: internal/database/pebble_activity_index_pushdown_bench_test.go
// version: 1.0.0
// guid: e60d064c-d3a5-4f0d-8c1b-1c1fa0b27a7b
// last-edited: 2026-08-30

// Package database — before/after benchmark for the activity index limit
// pushdown.
//
// The two benchmarks run the SAME fixture through the two implementations that
// queryByIndexPrefix dispatches between, so the ratio between them is the whole
// claim of the change and not a comparison against a remembered number from an
// older build:
//
//	BenchmarkQueryByIndexPrefixFull  — one db.Get and one json.Unmarshal per
//	                                   entry of the operation, then a sort, then
//	                                   a slice.
//	BenchmarkQueryByIndexPrefixPaged — one reverse index scan, one key-only
//	                                   existence merge, and exactly len(page)
//	                                   Gets and decodes.
//
// Run them with:
//
//	go test ./internal/database/ -run '^$' \
//	  -bench 'BenchmarkQueryByIndexPrefix' -benchtime 5x
//
// Report RANGES over repeated runs rather than a single figure: this path warms
// monotonically within a run (Pebble's block cache fills on the first
// iteration), so a median taken from one run overstates the steady state and
// the first iteration overstates the cost.
package database

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// benchFatOpEntries is the size of the fat operation both benchmarks read.
// Production operations of this size are the reason the pushdown exists: a
// library scan writes tens of thousands of activity rows under one op id, and
// GET /api/v1/operations/:id/activity asks for 1,000 of them.
const benchFatOpEntries = 50000

// benchFatOpID is the operation id both benchmarks query.
const benchFatOpID = "op-bench-fat"

// seedFatOperation writes benchFatOpEntries rows under one operation id,
// spread across four tiers so the existence merge has to walk more than one
// primary range — a single-tier fixture would make the merge look artificially
// sequential.
func seedFatOperation(b *testing.B, n int) *PebbleActivityStore {
	b.Helper()
	dir := b.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "bench.pebble"), &pebble.Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	s := NewPebbleActivityStore(db)

	base := time.Now().UTC().Add(-time.Duration(n) * time.Millisecond)
	tiers := []string{"change", "info", "debug", "batch"}
	batch := make([]ActivityEntry, 0, 1000)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if _, err := s.RecordBatch(batch); err != nil {
			b.Fatal(err)
		}
		batch = batch[:0]
	}
	for i := 0; i < n; i++ {
		batch = append(batch, ActivityEntry{
			Timestamp:   base.Add(time.Duration(i) * time.Millisecond),
			Tier:        tiers[i%len(tiers)],
			Type:        "seeded",
			Level:       "info",
			Source:      fmt.Sprintf("src-%d", i%5),
			OperationID: benchFatOpID,
			BookID:      fmt.Sprintf("book-%d", i%97),
			Summary:     fmt.Sprintf("seeded entry %d with a reasonably long summary line", i),
			Details:     map[string]any{"idx": i, "path": "/library/some/long/path/file.m4b", "state": "ok"},
			Tags:        []string{"alpha", "beta"},
		})
		if len(batch) == cap(batch) {
			flush()
		}
	}
	flush()
	if err := db.Flush(); err != nil {
		b.Fatal(err)
	}
	return s
}

func benchIndexPrefix(b *testing.B, limit int, run func(*PebbleActivityStore, context.Context, string, ActivityFilter) ([]ActivityEntry, int, error)) {
	s := seedFatOperation(b, benchFatOpEntries)
	prefix := "act:op:" + benchFatOpID + ":"
	f := ActivityFilter{OperationID: benchFatOpID, Limit: limit}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, total, err := run(s, ctx, prefix, f)
		if err != nil {
			b.Fatal(err)
		}
		// Assert the work actually happened: a benchmark of a query that
		// returned nothing would measure the wrong thing entirely.
		if total != benchFatOpEntries || len(entries) != limit {
			b.Fatalf("total=%d len=%d", total, len(entries))
		}
	}
}

// BenchmarkQueryByIndexPrefixFull_Limit1000 is the pre-pushdown cost at the
// default page size of GET /api/v1/operations/:id/activity.
func BenchmarkQueryByIndexPrefixFull_Limit1000(b *testing.B) {
	benchIndexPrefix(b, 1000, (*PebbleActivityStore).queryByIndexPrefixFull)
}

// BenchmarkQueryByIndexPrefixPaged_Limit1000 is the same query after the
// pushdown.
func BenchmarkQueryByIndexPrefixPaged_Limit1000(b *testing.B) {
	benchIndexPrefix(b, 1000, (*PebbleActivityStore).queryByIndexPrefixPaged)
}

// BenchmarkQueryByIndexPrefixFull_Limit50 is the pre-pushdown cost at the
// default page size of GET /api/v1/activity?operation_id=...
func BenchmarkQueryByIndexPrefixFull_Limit50(b *testing.B) {
	benchIndexPrefix(b, 50, (*PebbleActivityStore).queryByIndexPrefixFull)
}

// BenchmarkQueryByIndexPrefixPaged_Limit50 is the same query after the
// pushdown. The gap is wider here than at limit 1000 because the pushdown's
// remaining per-page cost scales with the page while the full path's does not
// scale with anything the caller asked for.
func BenchmarkQueryByIndexPrefixPaged_Limit50(b *testing.B) {
	benchIndexPrefix(b, 50, (*PebbleActivityStore).queryByIndexPrefixPaged)
}
