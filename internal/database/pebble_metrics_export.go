// file: internal/database/pebble_metrics_export.go
// version: 1.2.0
// guid: 7efe29c0-d5f8-489a-bdaa-2715b790979d
// last-edited: 2026-10-04

package database

import (
	"errors"

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
// false for a nil db, and for a panic matching pebble.ErrClosed; any other
// panic is re-raised, the same rule recoverPebbleClosed applies.
//
// pebble v2.1.7's DB.Metrics() does NOT check for a closed DB, and it is not
// safe to call concurrently with Close: Close tears down the file cache after
// releasing the engine mutex, and Metrics() then reads it and can panic with
// something other than ErrClosed. This function cannot tell a closed DB from an
// open one and does not make that call safe. The store owners serialise it
// against Close (PebbleStore.sampleMu, OLStore.sampleMu: read lock held from the
// closed-flag check through Metrics(), write lock held by Close). Any other
// caller must do the same; the only containment otherwise is the recover in
// metrics' samplePebbleSource, which drops that store from one scrape. Probing
// the engine instead was tried and rejected: db.NewSnapshot() as a probe can
// leave a snapshot open when Close lands, and Close then fails with "leaked
// snapshots".
func PebbleSampleFromDB(db *pebble.DB) (s metrics.PebbleSample, ok bool) {
	if db == nil {
		return metrics.PebbleSample{}, false
	}
	defer func() {
		if rec := recover(); rec != nil {
			if err, isErr := rec.(error); isErr && errors.Is(err, pebble.ErrClosed) {
				s, ok = metrics.PebbleSample{}, false
				return
			}
			panic(rec)
		}
	}()
	m := db.Metrics()
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
	return s, true
}
