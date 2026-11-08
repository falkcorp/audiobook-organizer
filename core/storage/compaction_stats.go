// file: core/storage/compaction_stats.go
// version: 1.0.0
// guid: 78bf89aa-6938-499d-94b4-fdede2a62b60
// last-edited: 2026-11-07

package storage

import cpebble "github.com/cockroachdb/pebble/v2"

// CompactionStats is a point-in-time read of a Pebble database's compaction
// and LSM counters. Every field comes straight from pebble.Metrics; nothing
// here is estimated. Field for field it is ABO's
// internal/database.CompactionStats, so ABO's stores convert to it with a
// plain struct conversion until R1-23 re-homes them onto core.
//
// It exists so a caller running a long compaction can report LIVENESS from
// numbers the engine actually produces rather than from a bare ticker: an
// operation that reports no progress for five minutes is cancelled, and a
// heartbeat that fired regardless of whether work happens would defeat that
// check instead of satisfying it.
type CompactionStats struct {
	// Count is the total number of completed compactions. Monotonic.
	Count int64
	// EstimatedDebt is Pebble's estimate of the bytes background compaction
	// still owes to reach a stable LSM shape. It is NOT a denominator for a
	// manual full compaction, so it is shown as a figure, never a percentage.
	EstimatedDebt uint64
	// InProgressBytes is the bytes written so far by compactions running now;
	// unlike Count it MOVES during a single long compaction.
	InProgressBytes int64
	// NumInProgress is how many compactions are running.
	NumInProgress int64
	// DiskSpaceUsage is the engine's view of bytes on disk, including obsolete
	// files not yet deleted, so it can rise during a compaction.
	DiskSpaceUsage uint64
	// CompletedBytes is the bytes written by finished compactions: the sum of
	// TableBytesCompacted and BlobBytesCompacted over all levels.
	CompletedBytes uint64
	// LiveTableSize is the summed size of every live sstable across all
	// levels, the figure to compare before and after a compaction.
	LiveTableSize int64
	// ObsoleteSize is the bytes in obsolete tables still on disk.
	ObsoleteSize uint64
	// L0Files and L0Sublevels describe level 0, where flushed memtables land.
	L0Files     int64
	L0Sublevels int32
	// BottomLevelSize is the size of the bottom level (L6).
	BottomLevelSize int64
	// TombstoneCount is Pebble's approximate count of point tombstones.
	TombstoneCount uint64
	// GarbageBytes is Pebble's estimate of the bytes deletions would reclaim.
	GarbageBytes uint64
	// Absent marks a sample taken when there was no database to read; its
	// zero counters are not measurements.
	Absent bool
}

// WrittenBytes is the bytes written by compactions so far, finished or not.
func (s CompactionStats) WrittenBytes() uint64 {
	in := max(s.InProgressBytes, 0)
	return s.CompletedBytes + uint64(in)
}

// Stats is the counter snapshot of the compaction progress reporter
// (compact_progress.go: Step, RunStep, Moved; ABO's internal/compactprogress
// merged into storage, 04 §8.5 R1). An alias, so Admin.CompactionStats can be
// handed straight to Step.Stats. A full-keyspace Compact is one blocking call
// with no callback; the reporter samples these counters on a ticker while it
// runs and turns them into operator-readable progress lines.
type Stats = CompactionStats

// compactionStatsFromMetrics maps one Metrics sample to CompactionStats.
func compactionStatsFromMetrics(m *cpebble.Metrics) CompactionStats {
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
