// file: internal/database/pebble_metrics_export.go
// version: 1.4.0
// guid: 7efe29c0-d5f8-489a-bdaa-2715b790979d
// last-edited: 2026-10-11

package database

import (
	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// PebbleMetricsSampler is implemented by stores backed by a Pebble database
// that can be sampled for the pebble_* Prometheus series.
type PebbleMetricsSampler interface {
	PebbleMetricsSample() (metrics.PebbleSample, bool)
}

var _ PebbleMetricsSampler = (*PebbleStore)(nil)

// PebbleMetricsSample samples the main store's database.
func (p *PebbleStore) PebbleMetricsSample() (metrics.PebbleSample, bool) {
	// TryRLock: a Close in progress (it holds the write lock across db.Close)
	// makes this sample not-ok rather than making the scrape wait for it.
	if !p.sampleMu.TryRLock() {
		return metrics.PebbleSample{}, false
	}
	defer p.sampleMu.RUnlock()
	if p.dbClosed.Load() {
		return metrics.PebbleSample{}, false
	}
	return PebbleSampleFromDB(p.db)
}

// PebbleSampleFromDB reads db.Metrics() into a metrics.PebbleSample. ok is
// false for a nil db and for a store that has begun closing.
//
// pebble v2.1.7's DB.Metrics() does NOT check for a closed DB and is not safe
// concurrently with Close (Close tears down the file cache after releasing
// the engine mutex). The gated Metrics call holds a lease, so Close cannot
// close the engine under it (pebble_gate.go), and once Close has begun it is
// refused instead. Probing the engine instead was tried and rejected:
// db.NewSnapshot() as a probe can leave a snapshot open when Close lands, and
// Close then fails with "leaked snapshots".
func PebbleSampleFromDB(db *GatedDB) (s metrics.PebbleSample, ok bool) {
	if db == nil {
		return metrics.PebbleSample{}, false
	}
	m, err := db.Metrics()
	if err != nil {
		return metrics.PebbleSample{}, false
	}
	return pebbleSampleFromMetrics(m), true
}

// pebbleSampleFromMetrics maps one Metrics sample to a metrics.PebbleSample.
func pebbleSampleFromMetrics(m *pebble.Metrics) (s metrics.PebbleSample) {
	s = metrics.PebbleSample{
		BlockCacheBytes:           float64(m.BlockCache.Size),
		BlockCacheBlocks:          float64(m.BlockCache.Count),
		BlockCacheHits:            float64(m.BlockCache.Hits),
		BlockCacheMisses:          float64(m.BlockCache.Misses),
		FilterHits:                float64(m.Filter.Hits),
		FilterMisses:              float64(m.Filter.Misses),
		ReadAmp:                   float64(m.ReadAmp()),
		L0Files:                   float64(m.Levels[0].TablesCount),
		L0Sublevels:               float64(m.Levels[0].Sublevels),
		Compactions:               float64(m.Compact.Count),
		CompactionDebtBytes:       float64(m.Compact.EstimatedDebt),
		CompactionsInProgress:     float64(m.Compact.NumInProgress),
		CompactionInProgressBytes: float64(m.Compact.InProgressBytes),
		MemTableBytes:             float64(m.MemTable.Size),
		MemTables:                 float64(m.MemTable.Count),
		WALBytes:                  float64(m.WAL.Size),
		WALPhysicalBytes:          float64(m.WAL.PhysicalSize),
		WALFiles:                  float64(m.WAL.Files),
		DiskUsageBytes:            float64(m.DiskSpaceUsage()),
	}
	for i := range s.LevelBytes {
		l := &m.Levels[i]
		s.LevelBytes[i] = float64(l.TablesSize)
		s.LevelFiles[i] = float64(l.TablesCount)
		s.LevelBytesIn[i] = float64(l.TableBytesIn)
		s.LevelBytesFlushed[i] = float64(l.TableBytesFlushed + l.BlobBytesFlushed)
		s.LevelBytesCompacted[i] = float64(l.TableBytesCompacted + l.BlobBytesCompacted)
		s.LevelWriteAmp[i] = l.WriteAmp()
	}
	return s
}
