// file: core/storage/engine_sample.go
// version: 1.0.0
// guid: 5b0c8f3e-7d21-4a96-b8e4-2f6a1c9d0e73
// last-edited: 2026-11-08

package storage

import cpebble "github.com/cockroachdb/pebble/v2"

// EngineLevels is the number of LSM levels the engine reports (L0..L6).
const EngineLevels = 7

// EngineSample is one point-in-time read of the engine's counters: the
// figures an app's Pebble collector serves. Every field comes straight from
// pebble.Metrics, as float64 because that is the value type a Prometheus
// collector hands on. Field for field it is ABO's internal/metrics.PebbleSample,
// so ABO keeps serving its <namespace>_pebble_* families, names unchanged,
// from the store core opened (R1-23). It carries no Pebble type, so the
// engine stays inside this package.
type EngineSample struct {
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
	// WALBytes is the live (unflushed) data in the WAL: what the memtables
	// hold. Unlike MemTableBytes, which is arena capacity, it reads 0 on a
	// store with nothing unflushed.
	WALBytes         float64
	WALPhysicalBytes float64
	WALFiles         float64
	DiskUsageBytes   float64

	LevelBytes          [EngineLevels]float64
	LevelFiles          [EngineLevels]float64
	LevelBytesIn        [EngineLevels]float64
	LevelBytesFlushed   [EngineLevels]float64
	LevelBytesCompacted [EngineLevels]float64
	LevelWriteAmp       [EngineLevels]float64
}

// engineSampleFromMetrics maps one Metrics read to an EngineSample.
func engineSampleFromMetrics(m *cpebble.Metrics) EngineSample {
	s := EngineSample{
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
