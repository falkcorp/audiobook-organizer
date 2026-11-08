// file: core/storage/tables.go
// version: 1.0.0
// guid: 5b0e9d24-8c13-4f7a-a6e2-1d9c4b7f3e80
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"

	cpebble "github.com/cockroachdb/pebble/v2"
)

// TableInfo is one sstable of the store as the admin surface reports it,
// for an app's estimated census (ABO's db census reads per-table properties
// and per-range span bytes to estimate each key family without a scan).
// Pebble's types stay inside storage.
type TableInfo struct {
	Level    int
	FileNum  uint64
	Smallest []byte // the table's smallest user key
	Largest  []byte // the table's largest user key
	Size     uint64 // bytes on disk
	Virtual  bool   // a virtual table: its properties are scaled to its share of the backing table

	// The table's properties, with TablesOptions.Properties; HasProperties
	// is false when Pebble had none to give.
	HasProperties  bool
	Entries        uint64 // point entries, deletions included
	Deletions      uint64
	RawKeyBytes    uint64
	RawValueBytes  uint64
	RangeDeletions uint64

	// SpanBytes is the table's approximate bytes inside [TablesOptions.Start,
	// TablesOptions.End), with TablesOptions.SpanBytes.
	SpanBytes uint64
}

// TablesOptions selects what Tables reports.
type TablesOptions struct {
	Properties bool   // read each table's properties
	Start, End []byte // only the tables overlapping [Start, End); both or neither
	SpanBytes  bool   // each table's approximate bytes inside [Start, End); needs the range
}

// errTablesSpanNeedsRange is Tables' refusal of SpanBytes without a range.
var errTablesSpanNeedsRange = errors.New("storage: tables: span bytes need a key range")

// Tables lists the store's sstables, level by level, under a lease. The list
// is a moment's view: a flush or compaction may change it at once, which a
// caller detects by listing again.
func (a *Admin) Tables(ctx context.Context, o TablesOptions) ([]TableInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if (o.Start == nil) != (o.End == nil) || (o.SpanBytes && o.Start == nil) {
		return nil, errTablesSpanNeedsRange
	}
	var opts []cpebble.SSTablesOption
	if o.Properties {
		opts = append(opts, cpebble.WithProperties())
	}
	if o.Start != nil {
		opts = append(opts, cpebble.WithKeyRangeFilter(slices.Clone(o.Start), slices.Clone(o.End)))
	}
	if o.SpanBytes {
		opts = append(opts, cpebble.WithApproximateSpanBytes())
	}
	release, err := a.db.lease()
	if err != nil {
		return nil, err
	}
	defer release()
	levels, err := a.db.eng.DB().SSTables(opts...)
	if err != nil {
		return nil, fmt.Errorf("storage: tables: %w", err)
	}
	var out []TableInfo
	for level, tables := range levels {
		for i := range tables {
			info := &tables[i]
			t := TableInfo{
				Level:     level,
				FileNum:   uint64(info.FileNum),
				Smallest:  slices.Clone(info.Smallest.UserKey),
				Largest:   slices.Clone(info.Largest.UserKey),
				Size:      info.Size,
				Virtual:   info.Virtual,
				SpanBytes: info.ApproximateSpanBytes,
			}
			if pr := info.Properties; pr != nil {
				t.HasProperties = true
				t.Entries, t.Deletions = pr.NumEntries, pr.NumDeletions
				t.RawKeyBytes, t.RawValueBytes = pr.RawKeySize, pr.RawValueSize
				t.RangeDeletions = pr.NumRangeDeletions
			}
			out = append(out, t)
		}
	}
	return out, nil
}
