// file: internal/compactprogress/stats.go
// version: 1.1.0
// guid: 6b2e4f0a-8c1d-4e7a-9f35-2d6c8a1b7e40
// last-edited: 2026-10-02

// Package compactprogress reports what a long Pebble compaction is doing
// while it runs.
//
// A full-keyspace `db.Compact` is one blocking call with no callback. On
// production it takes ~28 minutes, and until this package existed the
// db-optimize ops showed "Optimizing main database (0/3)" for that whole time
// and nothing else. The engine does expose live counters through
// `(*pebble.DB).Metrics()`; this package samples them on a ticker while the
// compaction runs and turns them into operator-readable progress lines.
//
// It is a leaf package (Pebble + internal/logger only) so the main store
// (internal/database), the OpenLibrary cache (internal/openlibrary) and the
// ops that drive them can all share one Stats type without an import cycle.
package compactprogress

import "github.com/cockroachdb/pebble/v2"

// Stats is a point-in-time read of a Pebble database's compaction and LSM
// counters. Every field comes straight from pebble.Metrics; nothing here is
// estimated by this package.
type Stats struct {
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
func (s Stats) WrittenBytes() uint64 {
	in := s.InProgressBytes
	if in < 0 {
		in = 0
	}
	return s.CompletedBytes + uint64(in)
}

// Collect samples a Pebble database. Cheap: Metrics() reads in-memory
// counters and does not touch the LSM on disk.
func Collect(db *pebble.DB) Stats {
	if db == nil {
		return Stats{Absent: true}
	}
	m := db.Metrics()
	s := Stats{
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
