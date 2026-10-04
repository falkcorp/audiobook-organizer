// file: internal/database/census_families.go
// version: 1.3.0
// guid: c3f3063e-7a83-4402-9443-4ec19031525d
// last-edited: 2026-10-04

package database

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// censusMaxAttempts bounds how often censusFamilies re-reads the table set
// when a flush or compaction changed it mid-census.
const censusMaxAttempts = 3

// censusProbeMaxSpan: a range whose tables put more span bytes than this
// inside it is treated as holding data without a seek. Only ranges with
// almost nothing in them need the seek to tell "empty" from "a few keys" —
// and a seek into a large range of tombstones would walk all of them.
const censusProbeMaxSpan = 1 << 20

// censusTable is one sstable's per-table figures.
type censusTable struct {
	smallest, largest []byte
	size              uint64
	virtual, noProps  bool
	rangeDels         bool // NumRangeDeletions > 0
	// internal = NumEntries; entries = NumEntries − NumDeletions.
	internal, entries, deletions, rawKey, rawVal float64
}

// censusRangeProbe is what one bounded seek learned about a range.
type censusRangeProbe struct {
	live    bool   // a seek ran and First() found a live key (false when not seeked)
	points  uint64 // internal points stepped over when no live key was found
	covered uint64 // points stepped over that a range deletion covers
	// rangeDel: a table overlapping the range holds range deletions, which
	// can hide every point in it from the seek.
	rangeDel bool
	// entries: the range holds keys, tombstones or range-deleted points.
	entries bool
}

// censusAcc accumulates one family's apportioned figures as floats so shares
// are rounded only once, at the end.
type censusAcc struct {
	keys, deletions, entries, rawKey, rawValue, disk, errBound float64
	tables                                                     map[uint64]struct{}
}

// censusFamilies fills the totals and the estimated per-family figures.
//
//  1. censusSnapshot reads the tables and their properties, the per-range
//     span bytes and one bounded seek per range (the probe), then lists the
//     tables again; when a flush or compaction changed the set in between,
//     the whole pass is retried, so the probe always describes the same
//     version as the table list.
//  2. The probe marks the ranges that hold anything: a live key; or, when
//     First() finds none, tombstones, shadowed versions or range-deleted
//     points, read from the iterator stats; or a table with range deletions
//     overlapping the range (range-deleted points are invisible to a seek).
//     Ranges holding nothing get no share: span bytes are block-granular, so
//     without this a small straddling table hands its keys to every empty
//     family it covers.
//  3. Each table's entries, bytes and size are split by span bytes among the
//     non-empty families it overlaps, so Σ Entries = TotalEntries and
//     Σ DiskBytes = TotalTableBytes.
func (p *PebbleStore) censusFamilies(ctx context.Context, out *DBCensus) error {
	ranges := keyFamilyRanges(keyFamilies)
	var (
		snap *censusSnap
		err  error
	)
	for attempt := 1; attempt <= censusMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snap, err = p.censusSnapshot(ctx, ranges)
		if err != nil {
			return err
		}
		if !snap.changed {
			break
		}
	}

	var missingProps, virtual int
	for _, t := range snap.tables {
		out.TotalKeys += int64(t.entries)
		out.TotalDeletions += int64(t.deletions)
		out.TotalEntries += int64(t.internal)
		out.TotalTables++
		out.TotalTableBytes += t.size
		if t.noProps {
			missingProps++
		}
		if t.virtual {
			virtual++
		}
	}
	if missingProps > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d sstables returned no properties and count as zero", missingProps))
	}
	if virtual > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d virtual sstables report their backing table's properties scaled by their share of its size, an approximation", virtual))
	}
	if snap.changed {
		out.Notes = append(out.Notes, fmt.Sprintf("the table set kept changing under compaction for %d attempts; tables that disappeared were split evenly across the families their key bounds overlap", censusMaxAttempts))
	}

	accs := censusApportion(snap, ranges)
	rangeDelFams := map[string]bool{}
	for ri, pr := range snap.probes {
		if pr.rangeDel {
			rangeDelFams[ranges[ri].Family] = true
		}
	}

	fams := KeyFamilies()
	fams = append(fams, KeyFamily{
		Prefix:      unregisteredFamily,
		Description: "keys outside every registered family",
	})
	out.Families = make([]FamilyCensus, 0, len(fams))
	for _, f := range fams {
		fc := FamilyCensus{Prefix: f.Prefix, Description: f.Description, Owner: f.Owner, Method: CensusMethodEmpty, rangeDel: rangeDelFams[f.Prefix]}
		if a := accs[f.Prefix]; a != nil && (a.entries > 0 || a.disk > 0) {
			fc.Method = CensusMethodEstimated
			fc.Estimated = true
			fc.Keys = int64(math.Round(a.keys))
			fc.Deletions = int64(math.Round(a.deletions))
			fc.Entries = int64(math.Round(a.entries))
			fc.RawKeyBytes = int64(math.Round(a.rawKey))
			fc.RawValueBytes = int64(math.Round(a.rawValue))
			fc.DiskBytes = uint64(math.Round(a.disk))
			fc.Tables = len(a.tables)
			fc.ErrorBoundKeys = int64(math.Ceil(a.errBound))
		}
		out.Families = append(out.Families, fc)
	}

	if note := censusRangeDelNote(out.Families); note != "" {
		out.Notes = append(out.Notes, note)
	}
	m := p.db.Metrics()
	out.DiskSpaceUsage = m.DiskSpaceUsage()
	return nil
}

// censusRangeDelNote names the families whose ranges overlap uncompacted
// range deletions, or returns "".
func censusRangeDelNote(fams []FamilyCensus) string {
	var names []string
	for _, f := range fams {
		if f.rangeDel {
			names = append(names, f.Prefix)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "uncompacted range deletions overlap these families; their on-disk figures include covered data until compaction, and an exact pass cannot see the covered keys: " + strings.Join(names, ", ")
}

// censusApportion splits every table between the non-empty families it
// overlaps, by span bytes.
func censusApportion(snap *censusSnap, ranges []keyRange) map[string]*censusAcc {
	accs := map[string]*censusAcc{}
	acc := func(fam string) *censusAcc {
		a := accs[fam]
		if a == nil {
			a = &censusAcc{tables: map[uint64]struct{}{}}
			accs[fam] = a
		}
		return a
	}
	for fn, t := range snap.tables {
		overlaps := snap.span[fn]
		if snap.gone[fn] || len(overlaps) == 0 {
			overlaps = censusBoundsOverlap(ranges, t.smallest, t.largest)
		}
		overlaps = censusOnlyNonEmpty(overlaps, snap.probes)
		fb := map[string]float64{}
		for ri, b := range overlaps {
			fb[ranges[ri].Family] += float64(b)
		}
		shares := censusShares(fb)
		for fam, s := range shares {
			if s <= 0 {
				continue
			}
			a := acc(fam)
			a.keys += s * t.entries
			a.deletions += s * t.deletions
			a.entries += s * t.internal
			a.rawKey += s * t.rawKey
			a.rawValue += s * t.rawVal
			a.disk += s * float64(t.size)
			a.tables[fn] = struct{}{}
			if len(shares) > 1 {
				a.errBound += math.Max(s, 1-s) * t.entries
			}
			if t.rangeDels {
				// Any of the family's points in this table could sit under a
				// range deletion and not be a live key at all.
				a.errBound += s * t.entries
			}
		}
	}
	return accs
}

// censusShares turns per-family span bytes into shares that sum to 1. Equal
// split when every byte count is 0.
func censusShares(fb map[string]float64) map[string]float64 {
	var total float64
	for _, b := range fb {
		total += b
	}
	out := make(map[string]float64, len(fb))
	for fam, b := range fb {
		if total > 0 {
			out[fam] = b / total
		} else {
			out[fam] = 1 / float64(len(fb))
		}
	}
	return out
}

// censusSnap is one consistent read of the table set, the span bytes and the
// probe. changed is true when the table set differed after the probe.
type censusSnap struct {
	tables  map[uint64]*censusTable
	span    map[uint64]map[int]uint64
	probes  []censusRangeProbe
	gone    map[uint64]bool
	changed bool
}

// censusSnapshot reads the table set with properties, the per-range span
// bytes, the range probe, and then the table set again.
func (p *PebbleStore) censusSnapshot(ctx context.Context, ranges []keyRange) (*censusSnap, error) {
	levels, err := p.db.SSTables(pebble.WithProperties())
	if err != nil {
		return nil, fmt.Errorf("db census: list sstables: %w", err)
	}
	snap := &censusSnap{tables: map[uint64]*censusTable{}, span: map[uint64]map[int]uint64{}, gone: map[uint64]bool{}}
	for _, level := range levels {
		for i := range level {
			info := &level[i]
			t := &censusTable{
				smallest: append([]byte(nil), info.Smallest.UserKey...),
				largest:  append([]byte(nil), info.Largest.UserKey...),
				size:     info.Size,
				virtual:  info.Virtual,
			}
			// For a virtual table Pebble already scales these to the
			// table's share of its backing (GetScaledProperties).
			if pr := info.Properties; pr == nil {
				t.noProps = true
			} else {
				t.internal = float64(pr.NumEntries)
				if pr.NumEntries > pr.NumDeletions {
					t.entries = float64(pr.NumEntries - pr.NumDeletions)
				}
				t.deletions = float64(pr.NumDeletions)
				t.rawKey = float64(pr.RawKeySize)
				t.rawVal = float64(pr.RawValueSize)
				t.rangeDels = pr.NumRangeDeletions > 0
			}
			snap.tables[uint64(info.FileNum)] = t
		}
	}

	for ri, r := range ranges {
		lo, hi := censusBounds(r)
		got, err := p.db.SSTables(pebble.WithKeyRangeFilter(lo, hi), pebble.WithApproximateSpanBytes())
		if err != nil {
			return nil, fmt.Errorf("db census: sstables for %s: %w", r.Family, err)
		}
		for _, level := range got {
			for i := range level {
				fn := uint64(level[i].FileNum)
				if snap.span[fn] == nil {
					snap.span[fn] = map[int]uint64{}
				}
				snap.span[fn][ri] = level[i].ApproximateSpanBytes
			}
		}
	}

	spanTotal := make([]uint64, len(ranges))
	for _, overlaps := range snap.span {
		for ri, b := range overlaps {
			spanTotal[ri] += b
		}
	}
	if snap.probes, err = p.censusProbeRanges(ctx, ranges, spanTotal); err != nil {
		return nil, err
	}
	// A range overlapped by a table with range deletions is not known to be
	// empty: a range deletion hides the points it covers from the seek.
	for fn, overlaps := range snap.span {
		t := snap.tables[fn]
		if t == nil || !t.rangeDels {
			continue
		}
		for ri := range overlaps {
			snap.probes[ri].rangeDel = true
			snap.probes[ri].entries = true
		}
	}

	after, err := p.db.SSTables()
	if err != nil {
		return nil, fmt.Errorf("db census: re-list sstables: %w", err)
	}
	still := map[uint64]bool{}
	for _, level := range after {
		for i := range level {
			still[uint64(level[i].FileNum)] = true
		}
	}
	for fn := range snap.tables {
		if !still[fn] {
			snap.gone[fn] = true
		}
	}
	snap.changed = len(still) != len(snap.tables) || len(snap.gone) > 0
	return snap, nil
}

// censusProbeRanges decides, per range, whether it holds anything. A range
// with more than censusProbeMaxSpan span bytes is taken as non-empty without
// a seek. The others get one bounded seek each, on a single iterator, never a
// scan: when First() finds no live key, the iterator stats say whether it
// stepped over tombstones, shadowed versions or range-deleted points, so a
// family whose keys were all deleted still counts as holding entries.
// spanTotal may be nil (every range is seeked).
func (p *PebbleStore) censusProbeRanges(ctx context.Context, ranges []keyRange, spanTotal []uint64) (_ []censusRangeProbe, err error) {
	out := make([]censusRangeProbe, len(ranges))
	if len(ranges) == 0 {
		return out, nil
	}
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: ranges[0].Lo, UpperBound: ranges[0].Hi})
	if err != nil {
		return nil, fmt.Errorf("db census: range probe iterator: %w", err)
	}
	defer func() {
		if cerr := it.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("db census: range probe iterator: %w", cerr)
		}
	}()
	for ri, r := range ranges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if spanTotal != nil && spanTotal[ri] > censusProbeMaxSpan {
			// Not seeked: known to hold entries, not known to hold a live key.
			out[ri] = censusRangeProbe{entries: true}
			continue
		}
		it.SetBounds(r.Lo, r.Hi)
		it.ResetStats()
		pr := censusRangeProbe{live: it.First()}
		if !pr.live {
			st := it.Stats().InternalStats
			pr.points = st.PointCount
			pr.covered = st.PointsCoveredByRangeTombstones
		}
		pr.entries = pr.live || pr.points > 0 || pr.covered > 0
		out[ri] = pr
	}
	if err := it.Error(); err != nil {
		return nil, fmt.Errorf("db census: range probe: %w", err)
	}
	return out, nil
}

// censusOnlyNonEmpty drops the ranges that hold nothing from a table's
// overlap set, so their shares go to the ranges that do. When none of the
// overlapped ranges holds anything the set is returned unchanged, so the
// table still lands somewhere and the totals stay conserved.
func censusOnlyNonEmpty(overlaps map[int]uint64, probes []censusRangeProbe) map[int]uint64 {
	kept := make(map[int]uint64, len(overlaps))
	for ri, b := range overlaps {
		if probes[ri].entries {
			kept[ri] = b
		}
	}
	if len(kept) == 0 {
		return overlaps
	}
	return kept
}

// censusBounds substitutes concrete bounds for the open ends of a keyRange.
func censusBounds(r keyRange) (lo, hi []byte) {
	lo, hi = r.Lo, r.Hi
	if lo == nil {
		lo = censusMinKey
	}
	if hi == nil {
		hi = censusMaxKey
	}
	return lo, hi
}

// censusBoundsOverlap returns, with zero span bytes (so shares split evenly),
// every range that a table with user-key bounds [smallest, largest] overlaps.
func censusBoundsOverlap(ranges []keyRange, smallest, largest []byte) map[int]uint64 {
	out := map[int]uint64{}
	for ri, r := range ranges {
		if r.Hi != nil && bytes.Compare(smallest, r.Hi) >= 0 {
			continue
		}
		if r.Lo != nil && bytes.Compare(largest, r.Lo) < 0 {
			continue
		}
		out[ri] = 0
	}
	if len(out) == 0 {
		out[sort.Search(len(ranges), func(i int) bool {
			return ranges[i].Hi == nil || bytes.Compare(smallest, ranges[i].Hi) < 0
		})] = 0
	}
	return out
}
