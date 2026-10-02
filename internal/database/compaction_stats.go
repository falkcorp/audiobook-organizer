// file: internal/database/compaction_stats.go
// version: 1.0.0
// guid: e3014758-0b25-4fcb-8a87-c8b158b6be42
// last-edited: 2026-10-02

package database

import "github.com/cockroachdb/pebble/v2"

// The compaction-stats type lives HERE, not in internal/compactprogress, so
// the dependency points from the progress sampler to the store and never the
// other way: internal/database is in the plugin SDK's dependency closure
// (make sdkguard), and the sampler package has no business being in it.
// The main store, the AI-scan store and the OpenLibrary cache all produce
// this type; compactprogress.Stats is an alias of it.

// CompactionStats is a point-in-time read of a Pebble database's compaction
// and LSM counters. Every field comes straight from pebble.Metrics; nothing
// here is estimated.
//
// It exists so a caller running a long Optimize() can report LIVENESS from
// numbers the engine actually produces, rather than from a bare ticker. That
// distinction is the whole point: the operations registry cancels an op that
// reports no progress for five minutes, and a heartbeat that fires regardless
// of whether work is happening would defeat that check instead of satisfying
// it. See internal/operations/registry/types.go:200 — declaring liveness is a
// contract precisely so a wedged op stays distinguishable from a working one.
type CompactionStats struct {
	// Count is the total number of completed compactions. Monotonic.
	Count int64
	// EstimatedDebt is Pebble's estimate of the bytes background compaction
	// still owes to reach a stable LSM shape. It is NOT a denominator for a
	// manual full compaction -- a manual Compact rewrites data the background
	// scheduler considers settled -- so it is shown as a figure, never turned
	// into a percentage.
	EstimatedDebt uint64
	// InProgressBytes is the bytes written so far by compactions that are
	// running right now. Pebble bumps it on every write to a compaction
	// output (compactionWritable.Write), so unlike Count it MOVES during a
	// single long compaction, and drops back when that compaction finishes and
	// its bytes are accounted to the levels' TableBytesCompacted.
	InProgressBytes int64
	// NumInProgress is how many compactions are running.
	NumInProgress int64
	// DiskSpaceUsage is the engine's view of bytes on disk, INCLUDING obsolete
	// files a compaction has superseded but Pebble has not yet deleted (that
	// deletion is asynchronous). It therefore rises during a compaction and
	// can read higher than the starting size right after Compact returns.
	DiskSpaceUsage uint64
	// CompletedBytes is the bytes written by compactions that have finished:
	// the sum of TableBytesCompacted and BlobBytesCompacted over all levels.
	CompletedBytes uint64
	// LiveTableSize is the summed size of every live sstable across all
	// levels. Unlike DiskSpaceUsage it excludes obsolete files, so it is the
	// figure to compare before and after a compaction.
	LiveTableSize int64
	// ObsoleteSize is the bytes in obsolete tables still on disk.
	ObsoleteSize uint64
	// L0Files and L0Sublevels describe level 0, where flushed memtables land.
	L0Files     int64
	L0Sublevels int32
	// BottomLevelSize is the size of the bottom level (L6), where a full
	// compaction moves everything.
	BottomLevelSize int64
	// TombstoneCount is Pebble's count of point tombstones in live tables.
	// It is derived from table stats that Pebble collects in the background,
	// so it is approximate and is displayed with a "~".
	TombstoneCount uint64
	// GarbageBytes is Pebble's estimate of the bytes that point and range
	// deletions would reclaim if compacted away. Approximate, like
	// TombstoneCount.
	GarbageBytes uint64
	// Absent marks a sample taken when there was no database to read: the
	// store was closed or removed (e.g. the OpenLibrary cache deleted
	// mid-run), or sampling panicked. Its zero counters are not
	// measurements, so Moved never treats them as progress.
	Absent bool
}

// WrittenBytes is the bytes written by compactions so far, finished or not.
// Comparing it between two samples gives "bytes compacted in between", and it
// keeps rising during one long compaction because InProgressBytes does.
func (s CompactionStats) WrittenBytes() uint64 {
	in := s.InProgressBytes
	if in < 0 {
		in = 0
	}
	return s.CompletedBytes + uint64(in)
}

// CollectCompactionStats samples a Pebble database. Cheap: Metrics() reads in-memory
// counters and does not touch the LSM on disk.
func CollectCompactionStats(db *pebble.DB) CompactionStats {
	if db == nil {
		return CompactionStats{Absent: true}
	}
	m := db.Metrics()
	s := CompactionStats{
		Count:           m.Compact.Count,
		EstimatedDebt:   m.Compact.EstimatedDebt,
		InProgressBytes: m.Compact.InProgressBytes,
		NumInProgress:   m.Compact.NumInProgress,
		DiskSpaceUsage:  m.DiskSpaceUsage(),
		ObsoleteSize:    m.Table.ObsoleteSize,
		L0Files:         m.Levels[0].TablesCount,
		L0Sublevels:     m.Levels[0].Sublevels,
		BottomLevelSize: m.Levels[len(m.Levels)-1].TablesSize,
		TombstoneCount:  m.Keys.TombstoneCount,
		GarbageBytes:    m.Table.Garbage.PointDeletionsBytesEstimate + m.Table.Garbage.RangeDeletionsBytesEstimate,
	}
	for i := range m.Levels {
		s.CompletedBytes += m.Levels[i].TableBytesCompacted + m.Levels[i].BlobBytesCompacted
		s.LiveTableSize += m.Levels[i].TablesSize
	}
	return s
}
