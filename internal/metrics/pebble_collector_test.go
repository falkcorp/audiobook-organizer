// file: internal/metrics/pebble_collector_test.go
// version: 1.0.0
// guid: 8944f3d7-bd99-440c-990c-923b3b696d06
// last-edited: 2026-10-04

package metrics

import (
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func fixedSample() PebbleSample {
	s := PebbleSample{BlockCacheBytes: 1, BlockCacheBlocks: 2, BlockCacheHits: 3, BlockCacheMisses: 4,
		FilterHits: 5, FilterMisses: 6, ReadAmp: 7, L0Files: 8, L0Sublevels: 9, Compactions: 10,
		CompactionDebtBytes: 11, CompactionsInProgress: 12, CompactionInProgressBytes: 13,
		MemTableBytes: 14, MemTables: 15, WALBytes: 16, WALPhysicalBytes: 17, WALFiles: 18, DiskUsageBytes: 19}
	for i := 0; i < pebbleLevels; i++ {
		s.LevelBytes[i], s.LevelFiles[i], s.LevelBytesIn[i] = float64(i+1), float64(i+2), float64(i+3)
		s.LevelBytesFlushed[i], s.LevelBytesCompacted[i], s.LevelWriteAmp[i] = float64(i+4), float64(i+5), float64(i+6)
	}
	return s
}

// withPebbleSources runs fn with only the given sources registered and
// restores the previous map afterwards.
func withPebbleSources(t *testing.T, srcs map[string]PebbleSource) {
	t.Helper()
	pebbleSourcesMu.Lock()
	saved := pebbleSources
	pebbleSources = srcs
	pebbleSourcesMu.Unlock()
	t.Cleanup(func() {
		pebbleSourcesMu.Lock()
		pebbleSources = saved
		pebbleSourcesMu.Unlock()
	})
}

func gatherPebble(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(&pebbleCollector{})
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, f := range fams {
		out[f.GetName()] = f
	}
	return out
}

func labelOf(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func TestPebbleCollector_EmitsAllSeriesPerStore(t *testing.T) {
	withPebbleSources(t, map[string]PebbleSource{
		PebbleStoreMain: func() (PebbleSample, bool) { return fixedSample(), true },
	})
	fams := gatherPebble(t)

	scalar := []string{"block_cache_bytes", "block_cache_blocks", "read_amplification", "l0_files", "l0_sublevels",
		"compaction_debt_bytes", "compactions_in_progress", "compaction_in_progress_bytes", "memtable_bytes",
		"memtables", "wal_bytes", "wal_physical_bytes", "wal_files", "disk_usage_bytes"}
	scalarCounters := []string{"block_cache_hits_total", "block_cache_misses_total", "filter_hits_total",
		"filter_misses_total", "compactions_total"}
	perLevelGauges := []string{"level_bytes", "level_files", "level_write_amplification"}
	perLevelCounters := []string{"level_bytes_in_total", "level_bytes_flushed_total", "level_bytes_compacted_total"}

	check := func(name string, typ dto.MetricType, want int) {
		t.Helper()
		f := fams["audiobook_organizer_pebble_"+name]
		if f == nil {
			t.Fatalf("metric %s missing", name)
		}
		if f.GetType() != typ {
			t.Errorf("%s type = %v, want %v", name, f.GetType(), typ)
		}
		if len(f.GetMetric()) != want {
			t.Errorf("%s has %d series, want %d", name, len(f.GetMetric()), want)
		}
		for _, m := range f.GetMetric() {
			if got := labelOf(m, "store"); got != "main" {
				t.Errorf("%s store label = %q", name, got)
			}
		}
	}
	for _, n := range scalar {
		check(n, dto.MetricType_GAUGE, 1)
	}
	for _, n := range scalarCounters {
		check(n, dto.MetricType_COUNTER, 1)
	}
	for _, n := range perLevelGauges {
		check(n, dto.MetricType_GAUGE, pebbleLevels)
	}
	for _, n := range perLevelCounters {
		check(n, dto.MetricType_COUNTER, pebbleLevels)
	}
	// Level label values 0..6 and a value spot check.
	f := fams["audiobook_organizer_pebble_level_bytes_compacted_total"]
	seen := map[string]float64{}
	for _, m := range f.GetMetric() {
		seen[labelOf(m, "level")] = m.GetCounter().GetValue()
	}
	for i := 0; i < pebbleLevels; i++ {
		if got, ok := seen[strconv.Itoa(i)]; !ok || got != float64(i+5) {
			t.Errorf("level %d compacted = %v (present=%v), want %d", i, got, ok, i+5)
		}
	}
}

func TestPebbleCollector_SourceNotOKEmitsNothing(t *testing.T) {
	withPebbleSources(t, map[string]PebbleSource{
		PebbleStoreOpenLibrary: func() (PebbleSample, bool) { return fixedSample(), false },
	})
	if fams := gatherPebble(t); len(fams) != 0 {
		t.Fatalf("expected no series, got %d families", len(fams))
	}
}

func TestPebbleCollector_PanickingSourceIsolated(t *testing.T) {
	withPebbleSources(t, map[string]PebbleSource{
		PebbleStoreMain:        func() (PebbleSample, bool) { return fixedSample(), true },
		PebbleStoreOpenLibrary: func() (PebbleSample, bool) { panic("boom") },
	})
	fams := gatherPebble(t)
	f := fams["audiobook_organizer_pebble_block_cache_bytes"]
	if f == nil || len(f.GetMetric()) != 1 || labelOf(f.GetMetric()[0], "store") != "main" {
		t.Fatalf("working store's series missing or polluted: %v", f)
	}
}

func TestPebbleCollector_SetPebbleSourceReplacesAndNilDeletes(t *testing.T) {
	withPebbleSources(t, map[string]PebbleSource{})
	val := func(v float64) PebbleSource {
		return func() (PebbleSample, bool) { return PebbleSample{ReadAmp: v}, true }
	}
	readAmp := func() (float64, bool) {
		f := gatherPebble(t)["audiobook_organizer_pebble_read_amplification"]
		if f == nil {
			return 0, false
		}
		return f.GetMetric()[0].GetGauge().GetValue(), true
	}
	SetPebbleSource("x", val(1))
	if v, ok := readAmp(); !ok || v != 1 {
		t.Fatalf("first source: %v %v", v, ok)
	}
	SetPebbleSource("x", val(2))
	if v, ok := readAmp(); !ok || v != 2 {
		t.Fatalf("replaced source: %v %v", v, ok)
	}
	SetPebbleSource("x", nil)
	if _, ok := readAmp(); ok {
		t.Fatal("nil should delete the source")
	}
}
