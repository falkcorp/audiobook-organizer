// file: internal/database/pebble_store_ops_v3_bench_test.go
// version: 1.0.0
// guid: cf430f80-a2d9-4202-b40e-407105f7a0de
// last-edited: 2026-10-11

package database

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// benchOpsV3TickStore seeds one running v2 row with a 4 KiB Params blob and a
// 2 KiB ResultData blob (synthetic) through the façade, so it has its opv3
// projection, and returns the store and the row id.
func benchOpsV3TickStore(b *testing.B) (*PebbleStore, string) {
	b.Helper()
	p := newTimelineStore(b)
	params := `{"items":["` + strings.Repeat("x", 4096) + `"]}`
	result := `{"summary":"` + strings.Repeat("y", 2048) + `"}`
	now := time.Now().UTC()
	row := OperationV2Row{ID: newRunID(), DefID: "test.bench-tick", Plugin: "test", Status: "queued", QueuedAt: now, Params: params, Priority: 5}
	if err := p.InsertOperationV2(row); err != nil {
		b.Fatal(err)
	}
	if err := p.UpdateOperationV2Status(row.ID, "running", &now, nil, nil); err != nil {
		b.Fatal(err)
	}
	if err := p.SetOperationV2Result(row.ID, result); err != nil {
		b.Fatal(err)
	}
	return p, row.ID
}

// reportTickBytes reports the WAL bytes one tick wrote, the v2 row's share of
// them (the opv2:op: key and value, which the v2 store has always written per
// tick), and the remainder: the opv3 projection plus batch framing.
func reportTickBytes(b *testing.B, p *PebbleStore, id string, walBefore uint64, n int) {
	b.Helper()
	wal := float64(walBytesIn(b, p)-walBefore) / float64(n)
	raw, closer, err := p.db.Get(opv2OpKey(id))
	if err != nil {
		b.Fatal(err)
	}
	v2 := float64(len(raw) + len(opv2OpKey(id)))
	_ = closer.Close()
	b.ReportMetric(wal, "wal-B/tick")
	b.ReportMetric(v2, "v2row-B/tick")
	b.ReportMetric(wal-v2, "opv3-B/tick")
}

// BenchmarkOpsV3ProgressTick measures one v2 progress tick
// (UpdateOpProgressV2) on a running op: the write the registry makes on every
// progress report, now carrying its opv3 projection in the same batch.
func BenchmarkOpsV3ProgressTick(b *testing.B) {
	p, id := benchOpsV3TickStore(b)
	before := walBytesIn(b, p)
	b.ResetTimer()
	i := 0
	for b.Loop() {
		i++
		if err := p.UpdateOpProgressV2(id, i, 1_000_000, fmt.Sprintf("item %d", i)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportTickBytes(b, p, id, before, i)
}

// BenchmarkOpsV3PhaseTick measures one v2 phase change (UpdateOpPhaseV2).
func BenchmarkOpsV3PhaseTick(b *testing.B) {
	p, id := benchOpsV3TickStore(b)
	phases := []string{"scan", "match", "apply"}
	before := walBytesIn(b, p)
	b.ResetTimer()
	i := 0
	for b.Loop() {
		ph := phases[i%len(phases)]
		i++
		if err := p.UpdateOpPhaseV2(id, &ph); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportTickBytes(b, p, id, before, i)
}

func walBytesIn(b *testing.B, p *PebbleStore) uint64 {
	b.Helper()
	m, err := p.db.Metrics()
	if err != nil {
		b.Fatal(err)
	}
	return m.WAL.BytesIn
}
