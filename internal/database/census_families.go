// file: internal/database/census_families.go
// version: 1.0.0
// guid: c3f3063e-7a83-4402-9443-4ec19031525d
// last-edited: 2026-10-04

package database

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// censusMaxAttempts bounds how often censusFamilies re-reads the table set
// when a flush or compaction changed it mid-census.
const censusMaxAttempts = 3

// censusTable is one sstable's per-table figures.
type censusTable struct {
	smallest, largest []byte
	size              uint64
	virtual, noProps  bool
	// internal = NumEntries; entries = NumEntries − NumDeletions.
	internal, entries, deletions, rawKey, rawVal float64
}

// censusRangeProbe is what one bounded seek learned about a range.
type censusRangeProbe struct {
	live    bool   // First() found a live key
	points  uint64 // internal points stepped over when no live key was found
	entries bool   // live || points > 0: the range holds keys or tombstones
}

// censusExact is the keys-only count of one family.
type censusExact struct {
	live, points, keyBytes, valBytes uint64
}

// censusAcc accumulates one family's apportioned figures as floats so shares
// are rounded only once, at the end.
type censusAcc struct {
	keys, deletions, entries, rawKey, rawValue, disk, errBound float64
	tables                                                     map[uint64]struct{}
}

// censusFamilies fills the totals and per-family figures.
//
//  1. Tables and their properties, the per-range span bytes, then the table
//     set again; retried when a compaction changed it (censusSnapshot).
//  2. One bounded seek per range (censusProbeRanges) marks the ranges that
//     hold anything: a live key, or — when First() finds none — tombstones
//     and shadowed versions, read from the iterator's PointCount. Ranges with
//     nothing get no share; span bytes are block-granular, so without this
//     a small straddling table hands its keys to every empty family it covers.
//  3. A preliminary byte-share apportioning estimates each family's entries.
//     Families estimated below exactThreshold are counted exactly by a
//     keys-only pass over their ranges, smallest first, until the deadline.
//  4. Final apportioning: each exact family's entries are placed in the
//     tables it overlaps by span weight; what is left of each table goes to
//     the estimated families by byte share. Disk bytes follow the same split,
//     so Σ family DiskBytes equals TotalTableBytes.
func (p *PebbleStore) censusFamilies(ctx context.Context, out *DBCensus, exactThreshold float64, deadline time.Time) error {
	ranges := keyFamilyRanges(keyFamilies)
	var (
		tables map[uint64]*censusTable
		span   map[uint64]map[int]uint64
		gone   map[uint64]bool
		err    error
	)
	for attempt := 1; attempt <= censusMaxAttempts; attempt++ {
		tables, span, gone, err = p.censusSnapshot(ranges)
		if err != nil {
			return err
		}
		if len(gone) == 0 {
			break
		}
	}

	var missingProps, virtual int
	for _, t := range tables {
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
	if len(gone) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d sstables were compacted away during the census after %d attempts; their figures were split evenly across the families their key bounds overlap", len(gone), censusMaxAttempts))
	}

	probes, err := p.censusProbeRanges(ranges)
	if err != nil {
		return err
	}

	// famBytes[fn][family] = span bytes of the table inside that family's
	// non-empty ranges (all overlapped ranges when none is non-empty).
	famBytes := make(map[uint64]map[string]float64, len(tables))
	for fn, t := range tables {
		overlaps := span[fn]
		if gone[fn] || len(overlaps) == 0 {
			overlaps = censusBoundsOverlap(ranges, t.smallest, t.largest)
		}
		overlaps = censusOnlyNonEmpty(overlaps, probes)
		fb := map[string]float64{}
		for ri, b := range overlaps {
			fb[ranges[ri].Family] += float64(b)
		}
		famBytes[fn] = fb
	}

	// Preliminary estimate of each family's internal entries.
	prelim := map[string]float64{}
	for fn, t := range tables {
		for fam, s := range censusShares(famBytes[fn], nil) {
			prelim[fam] += s * t.internal
		}
	}

	exact, exactNote, err := p.censusExactPass(ctx, ranges, probes, prelim, exactThreshold, deadline, out)
	if err != nil {
		return err
	}
	if exactNote != "" {
		out.Notes = append(out.Notes, exactNote)
	}

	accs := censusApportion(tables, famBytes, exact)

	hasEntries := map[string]bool{}
	for ri, pr := range probes {
		if pr.entries {
			hasEntries[ranges[ri].Family] = true
		}
	}
	fams := KeyFamilies()
	fams = append(fams, KeyFamily{
		Prefix:      unregisteredFamily,
		Description: "keys outside every registered family",
	})
	out.Families = make([]FamilyCensus, 0, len(fams))
	for _, f := range fams {
		fc := FamilyCensus{Prefix: f.Prefix, Description: f.Description, Owner: f.Owner, Method: CensusMethodEmpty}
		a := accs[f.Prefix]
		if a != nil {
			fc.DiskBytes = uint64(math.Round(a.disk))
			fc.Tables = len(a.tables)
		}
		switch e, isExact := exact[f.Prefix]; {
		case isExact:
			fc.Method = CensusMethodExact
			fc.Keys = int64(e.live)
			if e.points > e.live {
				fc.Deletions = int64(e.points - e.live)
			}
			fc.Entries = int64(e.points)
			fc.RawKeyBytes = int64(e.keyBytes)
			fc.RawValueBytes = int64(e.valBytes)
		case a != nil && (a.entries > 0 || a.disk > 0 || hasEntries[f.Prefix]):
			fc.Method = CensusMethodEstimated
			fc.Estimated = true
			fc.Keys = int64(math.Round(a.keys))
			fc.Deletions = int64(math.Round(a.deletions))
			fc.Entries = int64(math.Round(a.entries))
			fc.RawKeyBytes = int64(math.Round(a.rawKey))
			fc.RawValueBytes = int64(math.Round(a.rawValue))
			fc.ErrorBoundKeys = int64(math.Ceil(a.errBound))
		}
		out.Families = append(out.Families, fc)
	}

	m := p.db.Metrics()
	out.DiskSpaceUsage = m.DiskSpaceUsage()
	return nil
}

// censusShares turns per-family span bytes into shares that sum to 1,
// skipping families in skip. Equal split when every byte count is 0.
func censusShares(fb map[string]float64, skip map[string]*censusExact) map[string]float64 {
	var total float64
	n := 0
	for fam, b := range fb {
		if skip != nil && skip[fam] != nil {
			continue
		}
		total += b
		n++
	}
	out := make(map[string]float64, n)
	for fam, b := range fb {
		if skip != nil && skip[fam] != nil {
			continue
		}
		if total > 0 {
			out[fam] = b / total
		} else {
			out[fam] = 1 / float64(n)
		}
	}
	return out
}

// censusApportion splits every table's entries and bytes between families:
// first the exact families' known entries, placed by span weight across the
// tables they overlap; the rest by byte share among the estimated families.
func censusApportion(tables map[uint64]*censusTable, famBytes map[uint64]map[string]float64, exact map[string]*censusExact) map[string]*censusAcc {
	accs := map[string]*censusAcc{}
	acc := func(fam string) *censusAcc {
		a := accs[fam]
		if a == nil {
			a = &censusAcc{tables: map[uint64]struct{}{}}
			accs[fam] = a
		}
		return a
	}

	// Span weight of each exact family across the tables it overlaps.
	weight := map[string]float64{}
	count := map[string]int{}
	for _, fb := range famBytes {
		for fam, b := range fb {
			if exact[fam] != nil {
				weight[fam] += b
				count[fam]++
			}
		}
	}

	for fn, t := range tables {
		fb := famBytes[fn]
		// Exact families' entries placed in this table.
		alloc := map[string]float64{}
		var sumAlloc float64
		for fam, b := range fb {
			e := exact[fam]
			if e == nil {
				continue
			}
			var w float64
			if weight[fam] > 0 {
				w = b / weight[fam]
			} else {
				w = 1 / float64(count[fam])
			}
			alloc[fam] = float64(e.points) * w
			sumAlloc += alloc[fam]
		}
		if sumAlloc > t.internal && sumAlloc > 0 {
			scale := t.internal / sumAlloc
			for fam := range alloc {
				alloc[fam] *= scale
			}
			sumAlloc = t.internal
		}
		frac := 1.0 // share of the table left for estimated families
		if t.internal > 0 {
			frac = (t.internal - sumAlloc) / t.internal
		}

		est := censusShares(fb, exact)
		if len(est) == 0 {
			// Only exact families here: they take all of the table's bytes.
			var total float64
			for _, v := range alloc {
				total += v
			}
			for fam, v := range alloc {
				share := 1 / float64(len(alloc))
				if total > 0 {
					share = v / total
				}
				a := acc(fam)
				a.disk += share * float64(t.size)
				if share > 0 {
					a.tables[fn] = struct{}{}
				}
			}
			continue
		}
		for fam, v := range alloc {
			if t.internal <= 0 || v <= 0 {
				continue
			}
			a := acc(fam)
			a.disk += v / t.internal * float64(t.size)
			a.tables[fn] = struct{}{}
		}
		// Error bound. Two sources: the byte-share split among several
		// estimated families (their true share lies anywhere in [0, 1]), and
		// the placement of exact families that span several tables (their
		// entries could sit in another of those tables instead).
		var movable float64
		for fam, v := range alloc {
			if count[fam] > 1 && t.internal > 0 {
				movable += v / t.internal * t.entries
			}
		}
		for fam, s := range est {
			a := acc(fam)
			sf := s * frac
			if sf <= 0 {
				continue
			}
			a.keys += sf * t.entries
			a.deletions += sf * t.deletions
			a.entries += sf * t.internal
			a.rawKey += sf * t.rawKey
			a.rawValue += sf * t.rawVal
			a.disk += sf * float64(t.size)
			a.tables[fn] = struct{}{}
			if len(est) > 1 {
				a.errBound += math.Max(s, 1-s) * frac * t.entries
			}
			a.errBound += s * movable
		}
	}
	return accs
}

// censusExactPass counts, keys-only, every family whose preliminary estimate
// is below threshold, smallest first, until the deadline. A family cut off
// by the deadline stays estimated.
func (p *PebbleStore) censusExactPass(
	ctx context.Context, ranges []keyRange, probes []censusRangeProbe, prelim map[string]float64,
	threshold float64, deadline time.Time, out *DBCensus,
) (_ map[string]*censusExact, note string, err error) {
	start := time.Now()
	pieces := map[string][]int{}
	for ri, r := range ranges {
		if probes[ri].entries {
			pieces[r.Family] = append(pieces[r.Family], ri)
		}
	}
	type cand struct {
		fam string
		est float64
	}
	var cands []cand
	for fam := range pieces {
		if prelim[fam] < threshold {
			cands = append(cands, cand{fam, prelim[fam]})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].est != cands[j].est {
			return cands[i].est < cands[j].est
		}
		return cands[i].fam < cands[j].fam
	})

	exact := map[string]*censusExact{}
	if len(cands) == 0 {
		return exact, "", nil
	}
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: ranges[0].Lo, UpperBound: ranges[0].Hi})
	if err != nil {
		return nil, "", fmt.Errorf("db census: exact-pass iterator: %w", err)
	}
	defer func() {
		if cerr := it.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("db census: exact-pass iterator: %w", cerr)
		}
	}()

	var stepped int64
	skipped := 0
	stopped := false
	for _, c := range cands {
		if stopped {
			skipped++
			continue
		}
		e := &censusExact{}
		ok := true
		for _, ri := range pieces[c.fam] {
			it.SetBounds(ranges[ri].Lo, ranges[ri].Hi)
			it.ResetStats()
			for valid := it.First(); valid; valid = it.Next() {
				e.live++
				stepped++
				if stepped%censusCtxCheckEvery == 0 {
					if cerr := ctx.Err(); cerr != nil {
						return nil, "", cerr
					}
					if time.Now().After(deadline) {
						ok = false
						break
					}
				}
			}
			if !ok {
				break
			}
			st := it.Stats().InternalStats
			e.points += st.PointCount
			e.keyBytes += st.KeyBytes
			e.valBytes += st.ValueBytes
		}
		if err := it.Error(); err != nil {
			return nil, "", fmt.Errorf("db census: exact pass over %s: %w", c.fam, err)
		}
		if !ok {
			stopped = true
			skipped++
			continue
		}
		if e.points < e.live {
			e.points = e.live
		}
		exact[c.fam] = e
	}
	out.ExactPassMS = time.Since(start).Milliseconds()
	out.ExactPassKeys = stepped
	if skipped > 0 {
		note = fmt.Sprintf("exact pass hit its time budget: %d families below the %d-entry threshold stayed estimated", skipped, int64(threshold))
	}
	return exact, note, nil
}

// censusSnapshot reads the table set with properties and the per-range span
// bytes, then the table set again. gone holds the tables of the first listing
// that the second no longer has.
func (p *PebbleStore) censusSnapshot(ranges []keyRange) (
	tables map[uint64]*censusTable, span map[uint64]map[int]uint64, gone map[uint64]bool, err error,
) {
	levels, err := p.db.SSTables(pebble.WithProperties())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("db census: list sstables: %w", err)
	}
	tables = map[uint64]*censusTable{}
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
			}
			tables[uint64(info.FileNum)] = t
		}
	}

	span = map[uint64]map[int]uint64{}
	for ri, r := range ranges {
		lo, hi := censusBounds(r)
		got, err := p.db.SSTables(pebble.WithKeyRangeFilter(lo, hi), pebble.WithApproximateSpanBytes())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("db census: sstables for %s: %w", r.Family, err)
		}
		for _, level := range got {
			for i := range level {
				fn := uint64(level[i].FileNum)
				if span[fn] == nil {
					span[fn] = map[int]uint64{}
				}
				span[fn][ri] = level[i].ApproximateSpanBytes
			}
		}
	}

	after, err := p.db.SSTables()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("db census: re-list sstables: %w", err)
	}
	still := map[uint64]bool{}
	for _, level := range after {
		for i := range level {
			still[uint64(level[i].FileNum)] = true
		}
	}
	gone = map[uint64]bool{}
	for fn := range tables {
		if !still[fn] {
			gone[fn] = true
		}
	}
	return tables, span, gone, nil
}

// censusProbeRanges does one bounded seek per range on a single iterator,
// never a scan. When First() finds no live key, the iterator's PointCount
// says whether it stepped over tombstones or shadowed versions on the way,
// so a family whose keys were all deleted still counts as holding entries.
func (p *PebbleStore) censusProbeRanges(ranges []keyRange) (_ []censusRangeProbe, err error) {
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
		it.SetBounds(r.Lo, r.Hi)
		it.ResetStats()
		pr := censusRangeProbe{live: it.First()}
		if !pr.live {
			pr.points = it.Stats().InternalStats.PointCount
		}
		pr.entries = pr.live || pr.points > 0
		out[ri] = pr
	}
	if err := it.Error(); err != nil {
		return nil, fmt.Errorf("db census: range probe: %w", err)
	}
	return out, nil
}

// censusOnlyNonEmpty drops the ranges that hold no entry from a table's
// overlap set, so their shares go to the ranges that do. When none of the
// overlapped ranges holds an entry (range deletions only, say) the set is
// returned unchanged, so the table still lands somewhere and the totals stay
// conserved.
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
