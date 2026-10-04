// file: internal/metrics/pebble_collector.go
// version: 1.0.0
// guid: 7dc3409f-5419-47d8-8770-916f33b019ae
// last-edited: 2026-10-04

package metrics

import (
	"log/slog"
	"sort"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Store label values for the pebble_* series.
const (
	PebbleStoreMain        = "main"
	PebbleStoreOpenLibrary = "openlibrary"
)

// pebbleLevels is the number of LSM levels Pebble reports (L0..L6).
const pebbleLevels = 7

// PebbleSample is one point-in-time read of a Pebble database's Metrics().
// Every field is a float64 so the collector hands values straight to
// MustNewConstMetric. internal/metrics must not import internal/database (the
// dependency points the other way), so the database package fills this type.
type PebbleSample struct {
	BlockCacheBytes           float64
	BlockCacheBlocks          float64
	BlockCacheHits            float64
	BlockCacheMisses          float64
	FilterHits                float64
	FilterMisses              float64
	ReadAmp                   float64
	L0Files                   float64
	L0Sublevels               float64
	Compactions               float64
	CompactionDebtBytes       float64
	CompactionsInProgress     float64
	CompactionInProgressBytes float64
	MemTableBytes             float64
	MemTables                 float64
	WALBytes                  float64
	WALPhysicalBytes          float64
	WALFiles                  float64
	DiskUsageBytes            float64

	LevelBytes          [pebbleLevels]float64
	LevelFiles          [pebbleLevels]float64
	LevelBytesIn        [pebbleLevels]float64
	LevelBytesFlushed   [pebbleLevels]float64
	LevelBytesCompacted [pebbleLevels]float64
	LevelWriteAmp       [pebbleLevels]float64
}

// PebbleSource returns the current sample for one store. ok == false means
// "no database right now" (closed, removed, not opened); the collector then
// emits nothing for that store, never zeros.
type PebbleSource func() (PebbleSample, bool)

var (
	pebbleSourcesMu sync.RWMutex
	pebbleSources   = map[string]PebbleSource{}

	pebbleCollectorInstance = &pebbleCollector{}
)

// SetPebbleSource installs (or replaces) the source for a store label. A nil
// src removes the entry. Replacing is expected: NewServer runs many times in
// one test binary.
func SetPebbleSource(store string, src PebbleSource) {
	pebbleSourcesMu.Lock()
	defer pebbleSourcesMu.Unlock()
	if src == nil {
		delete(pebbleSources, store)
		return
	}
	pebbleSources[store] = src
}

type pebbleDesc struct {
	desc *prometheus.Desc
	typ  prometheus.ValueType
}

func newPebbleDesc(name, help string, typ prometheus.ValueType, perLevel bool) pebbleDesc {
	labels := []string{"store"}
	if perLevel {
		labels = append(labels, "level")
	}
	return pebbleDesc{
		desc: prometheus.NewDesc(prometheus.BuildFQName("audiobook_organizer", "", name), help, labels, nil),
		typ:  typ,
	}
}

// Per-store scalar series. NOTE for the block-cache fields: once the stores
// share one block cache (storage plan A7), the main and openlibrary
// pebble_block_cache_* series report the SAME cache. Do not sum them.
var (
	pdBlockCacheBytes  = newPebbleDesc("pebble_block_cache_bytes", "Pebble Metrics().BlockCache.Size: bytes held in the block cache", prometheus.GaugeValue, false)
	pdBlockCacheBlocks = newPebbleDesc("pebble_block_cache_blocks", "Pebble Metrics().BlockCache.Count: blocks held in the block cache", prometheus.GaugeValue, false)
	pdBlockCacheHits   = newPebbleDesc("pebble_block_cache_hits_total", "Pebble Metrics().BlockCache.Hits, cumulative since the store opened", prometheus.CounterValue, false)
	pdBlockCacheMisses = newPebbleDesc("pebble_block_cache_misses_total", "Pebble Metrics().BlockCache.Misses, cumulative since the store opened", prometheus.CounterValue, false)
	pdFilterHits       = newPebbleDesc("pebble_filter_hits_total", "Pebble Metrics().Filter.Hits (bloom filter avoided a read), cumulative since the store opened", prometheus.CounterValue, false)
	pdFilterMisses     = newPebbleDesc("pebble_filter_misses_total", "Pebble Metrics().Filter.Misses (bloom filter could not rule the table out), cumulative since the store opened", prometheus.CounterValue, false)
	pdReadAmp          = newPebbleDesc("pebble_read_amplification", "Pebble Metrics().ReadAmp(): L0 sublevels plus non-empty lower levels", prometheus.GaugeValue, false)
	pdL0Files          = newPebbleDesc("pebble_l0_files", "Pebble Metrics().Levels[0].TablesCount", prometheus.GaugeValue, false)
	pdL0Sublevels      = newPebbleDesc("pebble_l0_sublevels", "Pebble Metrics().Levels[0].Sublevels", prometheus.GaugeValue, false)
	pdCompactions      = newPebbleDesc("pebble_compactions_total", "Pebble Metrics().Compact.Count, cumulative since the store opened", prometheus.CounterValue, false)
	pdCompactionDebt   = newPebbleDesc("pebble_compaction_debt_bytes", "Pebble Metrics().Compact.EstimatedDebt: estimated bytes of compaction work outstanding", prometheus.GaugeValue, false)
	pdCompactionsInPrg = newPebbleDesc("pebble_compactions_in_progress", "Pebble Metrics().Compact.NumInProgress", prometheus.GaugeValue, false)
	pdCompactionInPrgB = newPebbleDesc("pebble_compaction_in_progress_bytes", "Pebble Metrics().Compact.InProgressBytes", prometheus.GaugeValue, false)
	pdMemTableBytes    = newPebbleDesc("pebble_memtable_bytes", "Pebble Metrics().MemTable.Size", prometheus.GaugeValue, false)
	pdMemTables        = newPebbleDesc("pebble_memtables", "Pebble Metrics().MemTable.Count", prometheus.GaugeValue, false)
	pdWALBytes         = newPebbleDesc("pebble_wal_bytes", "Pebble Metrics().WAL.Size: live data in the WAL", prometheus.GaugeValue, false)
	pdWALPhysical      = newPebbleDesc("pebble_wal_physical_bytes", "Pebble Metrics().WAL.PhysicalSize: on-disk size of the WAL files", prometheus.GaugeValue, false)
	pdWALFiles         = newPebbleDesc("pebble_wal_files", "Pebble Metrics().WAL.Files", prometheus.GaugeValue, false)
	pdDiskUsage        = newPebbleDesc("pebble_disk_usage_bytes", "Pebble Metrics().DiskSpaceUsage()", prometheus.GaugeValue, false)

	pdLevelBytes = newPebbleDesc("pebble_level_bytes", "Pebble Metrics().Levels[level].TablesSize", prometheus.GaugeValue, true)
	pdLevelFiles = newPebbleDesc("pebble_level_files", "Pebble Metrics().Levels[level].TablesCount", prometheus.GaugeValue, true)
	pdLevelWA    = newPebbleDesc("pebble_level_write_amplification", "Pebble Metrics().Levels[level].WriteAmp(), cumulative since the store opened; use the three pebble_level_bytes_*_total counters for a windowed figure", prometheus.GaugeValue, true)

	pdLevelBytesIn = newPebbleDesc("pebble_level_bytes_in_total",
		"Pebble Metrics().Levels[level].TableBytesIn (for L0, bytes written to the WAL), cumulative since the store opened. Windowed write amplification of a level = (rate of flushed + rate of compacted) / rate of bytes in; of the whole store = sum over levels of (flushed + compacted) / L0 bytes in",
		prometheus.CounterValue, true)
	pdLevelBytesFlushed = newPebbleDesc("pebble_level_bytes_flushed_total",
		"Pebble Metrics().Levels[level].TableBytesFlushed + BlobBytesFlushed, cumulative since the store opened. Numerator term of write amplification: (rate of flushed + rate of compacted) / rate of bytes in",
		prometheus.CounterValue, true)
	pdLevelBytesCompacted = newPebbleDesc("pebble_level_bytes_compacted_total",
		"Pebble Metrics().Levels[level].TableBytesCompacted + BlobBytesCompacted, cumulative since the store opened. Numerator term of write amplification: (rate of flushed + rate of compacted) / rate of bytes in",
		prometheus.CounterValue, true)
)

var pebbleAllDescs = []pebbleDesc{
	pdBlockCacheBytes, pdBlockCacheBlocks, pdBlockCacheHits, pdBlockCacheMisses, pdFilterHits, pdFilterMisses,
	pdReadAmp, pdL0Files, pdL0Sublevels, pdCompactions, pdCompactionDebt, pdCompactionsInPrg, pdCompactionInPrgB,
	pdMemTableBytes, pdMemTables, pdWALBytes, pdWALPhysical, pdWALFiles, pdDiskUsage,
	pdLevelBytes, pdLevelFiles, pdLevelWA, pdLevelBytesIn, pdLevelBytesFlushed, pdLevelBytesCompacted,
}

// pebbleCollector reads every registered PebbleSource at scrape time. Reading
// at scrape time keeps the cumulative values true Prometheus counters and
// needs no ticker.
type pebbleCollector struct{}

func (*pebbleCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range pebbleAllDescs {
		ch <- d.desc
	}
}

func (*pebbleCollector) Collect(ch chan<- prometheus.Metric) {
	pebbleSourcesMu.RLock()
	srcs := make(map[string]PebbleSource, len(pebbleSources))
	for k, v := range pebbleSources {
		srcs[k] = v
	}
	pebbleSourcesMu.RUnlock()

	stores := make([]string, 0, len(srcs))
	for k := range srcs {
		stores = append(stores, k)
	}
	sort.Strings(stores)

	for _, store := range stores {
		sample, ok := samplePebbleSource(store, srcs[store])
		if !ok {
			continue
		}
		emitPebbleSample(ch, store, sample)
	}
}

// samplePebbleSource calls src, containing a panic to this one store.
func samplePebbleSource(store string, src PebbleSource) (s PebbleSample, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Warn("pebble metrics source panicked; dropping this store from the scrape", "store", store, "panic", rec)
			s, ok = PebbleSample{}, false
		}
	}()
	return src()
}

func emitPebbleSample(ch chan<- prometheus.Metric, store string, s PebbleSample) {
	one := func(d pebbleDesc, v float64) {
		ch <- prometheus.MustNewConstMetric(d.desc, d.typ, v, store)
	}
	one(pdBlockCacheBytes, s.BlockCacheBytes)
	one(pdBlockCacheBlocks, s.BlockCacheBlocks)
	one(pdBlockCacheHits, s.BlockCacheHits)
	one(pdBlockCacheMisses, s.BlockCacheMisses)
	one(pdFilterHits, s.FilterHits)
	one(pdFilterMisses, s.FilterMisses)
	one(pdReadAmp, s.ReadAmp)
	one(pdL0Files, s.L0Files)
	one(pdL0Sublevels, s.L0Sublevels)
	one(pdCompactions, s.Compactions)
	one(pdCompactionDebt, s.CompactionDebtBytes)
	one(pdCompactionsInPrg, s.CompactionsInProgress)
	one(pdCompactionInPrgB, s.CompactionInProgressBytes)
	one(pdMemTableBytes, s.MemTableBytes)
	one(pdMemTables, s.MemTables)
	one(pdWALBytes, s.WALBytes)
	one(pdWALPhysical, s.WALPhysicalBytes)
	one(pdWALFiles, s.WALFiles)
	one(pdDiskUsage, s.DiskUsageBytes)

	for i := 0; i < pebbleLevels; i++ {
		lvl := strconv.Itoa(i)
		perLevel := func(d pebbleDesc, v float64) {
			ch <- prometheus.MustNewConstMetric(d.desc, d.typ, v, store, lvl)
		}
		perLevel(pdLevelBytes, s.LevelBytes[i])
		perLevel(pdLevelFiles, s.LevelFiles[i])
		perLevel(pdLevelWA, s.LevelWriteAmp[i])
		perLevel(pdLevelBytesIn, s.LevelBytesIn[i])
		perLevel(pdLevelBytesFlushed, s.LevelBytesFlushed[i])
		perLevel(pdLevelBytesCompacted, s.LevelBytesCompacted[i])
	}
}
