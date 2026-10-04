// file: internal/database/census.go
// version: 1.0.0
// guid: be43e3c6-39e4-4a3e-a43c-cd5651ff141c
// last-edited: 2026-10-03

package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/singleflight"
)

// CensusFamilyFiguresBasis labels every per-family figure the census reports.
// Consumers that show per-family figures show this label next to them.
const CensusFamilyFiguresBasis = "on-disk entries incl. tombstones and shadowed versions; deletions reported separately; figures move only after compaction"

const (
	censusNoteKeyDefinition = "keys = sstable entries minus deletions; overwritten versions not yet compacted count once each"
	censusNoteMemtable      = "sstable properties only: unflushed memtable contents (up to 2 x MemTableSize) are not counted"
	censusNoteMemdbCold     = "memdb not warm: retired and signal counts unavailable"
	censusCacheTTL          = 5 * time.Minute
	censusHistoryTopN       = 20
	censusCtxCheckEvery     = 10000
)

// Bound substitutes for the open ends of the key space. Pebble's
// WithApproximateSpanBytes requires both bounds to be non-nil, and
// EstimateDiskUsage takes concrete bounds too.
var (
	censusMinKey = []byte{0x00}
	censusMaxKey = []byte{0xff, 0xff, 0xff, 0xff}
)

// CensusOptions selects what DBCensus computes.
type CensusOptions struct {
	// Deep adds the book_ver: keys-only pass (History). It iterates millions
	// of keys in production and is never the default.
	Deep bool `json:"deep"`
	// Fresh bypasses the cache for the read and refreshes it.
	Fresh bool `json:"fresh"`
}

// FamilyCensus is one key family's figures, estimated from sstable metadata.
type FamilyCensus struct {
	Prefix        string `json:"prefix"`
	Description   string `json:"description"`
	Owner         string `json:"owner"`
	Keys          int64  `json:"keys"`
	Deletions     int64  `json:"deletions"`
	RawKeyBytes   int64  `json:"raw_key_bytes"`
	RawValueBytes int64  `json:"raw_value_bytes"`
	DiskBytes     uint64 `json:"disk_bytes"`
	Tables        int    `json:"tables"`
	// Estimated is true when part of the figures came from a table that
	// straddles several families and was apportioned by span bytes.
	Estimated bool `json:"estimated"`
}

// RetiredCensus counts books that are retired (soft-deleted, or merged into
// another book) and the book_file rows they still own.
type RetiredCensus struct {
	SoftDeletedBooks int `json:"soft_deleted_books"`
	MergedBooks      int `json:"merged_books"`
	// RetiredBooks counts each retired book once, even when it is both
	// soft-deleted and merged.
	RetiredBooks     int `json:"retired_books"`
	RetiredBookFiles int `json:"retired_book_files"`
}

// SignalCensus counts stored fingerprints and intro transcripts. A fingerprint
// counts when AcoustIDFingerprintDurationSec > 0 (the proxy signal_coverage.go
// uses); a transcript counts when IntroTranscribedAt is set. Every row counts,
// retired or not.
type SignalCensus struct {
	FilesWithFingerprint int `json:"files_with_fingerprint"`
	FilesWithTranscript  int `json:"files_with_transcript"`
	BooksWithTranscript  int `json:"books_with_transcript"`
}

// BookHistoryCount is one book's book_ver: entry count.
type BookHistoryCount struct {
	BookID  string `json:"book_id"`
	Entries int64  `json:"entries"`
}

// HistoryCensus is the distribution of book_ver: entries per book.
type HistoryCensus struct {
	BooksWithHistory int64              `json:"books_with_history"`
	Entries          int64              `json:"entries"`
	Mean             float64            `json:"mean"`
	P50              int64              `json:"p50"`
	P90              int64              `json:"p90"`
	P99              int64              `json:"p99"`
	Max              int64              `json:"max"`
	Buckets          map[string]int64   `json:"buckets"`
	Top              []BookHistoryCount `json:"top"`
	// OrphanBooks/OrphanEntries count book ids with history but no row in
	// memdb's books table. OrphansKnown is false when memdb was not warm (or
	// not intact), in which case both are zero and mean nothing.
	OrphanBooks   int64 `json:"orphan_books"`
	OrphanEntries int64 `json:"orphan_entries"`
	OrphansKnown  bool  `json:"orphans_known"`
}

// DBCensus is the per-family census of the main Pebble store.
type DBCensus struct {
	GeneratedAt        time.Time      `json:"generated_at"`
	DurationMS         int64          `json:"duration_ms"`
	Cached             bool           `json:"cached"`
	TotalKeys          int64          `json:"total_keys"`
	TotalDeletions     int64          `json:"total_deletions"`
	TotalTables        int            `json:"total_tables"`
	TotalTableBytes    uint64         `json:"total_table_bytes"`
	DiskSpaceUsage     uint64         `json:"disk_space_usage"`
	Families           []FamilyCensus `json:"families"`
	Retired            *RetiredCensus `json:"retired"`
	Signals            *SignalCensus  `json:"signals"`
	History            *HistoryCensus `json:"history"`
	FamilyFiguresBasis string         `json:"family_figures_basis"`
	Notes              []string       `json:"notes"`
}

// DBCensusProvider is the capability the db-census endpoint resolves.
type DBCensusProvider interface {
	DBCensus(ctx context.Context, opts CensusOptions) (*DBCensus, error)
}

var _ DBCensusProvider = (*PebbleStore)(nil)

// censusCacheEntry is one cached census and when it was computed.
type censusCacheEntry struct {
	value *DBCensus
	at    time.Time
}

// censusCache holds two slots (shallow, deep) per *pebble.DB.
//
// TODO(storage-a3+): move into PebbleStore once pebble_store.go is free. It is
// package-level only because TASK-A4 owns pebble_store.go in this wave.
var censusCache = struct {
	mu sync.Mutex
	m  map[*pebble.DB]*[2]censusCacheEntry
}{m: map[*pebble.DB]*[2]censusCacheEntry{}}

var censusFlight singleflight.Group

func censusSlot(deep bool) int {
	if deep {
		return 1
	}
	return 0
}

func censusCacheGet(db *pebble.DB, deep bool, now time.Time) *DBCensus {
	censusCache.mu.Lock()
	defer censusCache.mu.Unlock()
	slots := censusCache.m[db]
	if slots == nil {
		return nil
	}
	e := slots[censusSlot(deep)]
	if e.value == nil || now.Sub(e.at) > censusCacheTTL {
		return nil
	}
	return e.value
}

func censusCachePut(db *pebble.DB, deep bool, v *DBCensus, at time.Time) {
	censusCache.mu.Lock()
	defer censusCache.mu.Unlock()
	slots := censusCache.m[db]
	if slots == nil {
		slots = &[2]censusCacheEntry{}
		censusCache.m[db] = slots
	}
	slots[censusSlot(deep)] = censusCacheEntry{value: v, at: at}
}

// clone returns a copy that shares nothing mutable with c, so a cached value
// can be handed out without a caller being able to edit the cache.
func (c *DBCensus) clone() *DBCensus {
	cp := *c
	cp.Families = append([]FamilyCensus(nil), c.Families...)
	cp.Notes = append([]string(nil), c.Notes...)
	if c.Retired != nil {
		r := *c.Retired
		cp.Retired = &r
	}
	if c.Signals != nil {
		s := *c.Signals
		cp.Signals = &s
	}
	if c.History != nil {
		h := *c.History
		h.Top = append([]BookHistoryCount(nil), c.History.Top...)
		h.Buckets = make(map[string]int64, len(c.History.Buckets))
		for k, v := range c.History.Buckets {
			h.Buckets[k] = v
		}
		cp.History = &h
	}
	return &cp
}

// DBCensus reports, per registered key family, an estimated key count and byte
// size read from sstable metadata — it never iterates the store, except for
// the opt-in Deep pass over book_ver: keys (keys only, no values). It adds the
// retired-book and signal counts from memdb when memdb is warm.
//
// Results are cached for 5 minutes per (store, deep); concurrent callers share
// one computation. opts.Fresh skips the cached read and refreshes the cache.
func (p *PebbleStore) DBCensus(ctx context.Context, opts CensusOptions) (_ *DBCensus, err error) {
	defer recoverPebbleClosed("DBCensus", &err)
	if p == nil || p.db == nil {
		return nil, errors.New("db census: store not open")
	}
	if !opts.Fresh {
		if hit := censusCacheGet(p.db, opts.Deep, time.Now()); hit != nil {
			cp := hit.clone()
			cp.Cached = true
			return cp, nil
		}
	}
	key := fmt.Sprintf("%p/%t", p.db, opts.Deep)
	v, err, _ := censusFlight.Do(key, func() (any, error) {
		c, cerr := p.computeDBCensus(ctx, opts.Deep)
		if cerr != nil {
			return nil, cerr
		}
		censusCachePut(p.db, opts.Deep, c, c.GeneratedAt)
		return c, nil
	})
	if err != nil {
		return nil, err
	}
	cp := v.(*DBCensus).clone()
	cp.Cached = false
	return cp, nil
}

// censusAcc accumulates one family's figures as floats so apportioned shares
// are rounded only once, at the end.
type censusAcc struct {
	keys, deletions, rawKey, rawValue float64
	disk                              uint64
	tables                            map[uint64]struct{}
	estimated                         bool
}

// censusTable is one sstable's per-table figures.
type censusTable struct {
	smallest, largest                  []byte
	size                               uint64
	virtual, noProps                   bool
	entries, deletions, rawKey, rawVal float64
}

// computeDBCensus does the work behind DBCensus, uncached. The recover lives
// here too because singleflight re-panics a panic in its function in every
// waiting caller.
func (p *PebbleStore) computeDBCensus(ctx context.Context, deep bool) (_ *DBCensus, err error) {
	defer recoverPebbleClosed("DBCensus", &err)
	start := time.Now()

	// EstimateDiskUsage is documented to panic with ErrClosed on a closed DB,
	// which recoverPebbleClosed turns into an error. Probe it first so a
	// closed store fails here, before SSTables can return stale metadata.
	if _, err := p.db.EstimateDiskUsage(censusMinKey, censusMaxKey); err != nil {
		return nil, fmt.Errorf("db census: estimate disk usage: %w", err)
	}

	out := &DBCensus{
		GeneratedAt:        start,
		FamilyFiguresBasis: CensusFamilyFiguresBasis,
		Notes:              []string{censusNoteKeyDefinition, censusNoteMemtable},
	}

	if err := p.censusFamilies(out); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if m := p.mem(); m == nil {
		out.Notes = append(out.Notes, censusNoteMemdbCold)
	} else if retired, signals, merr := m.retiredAndSignalCensus(); merr != nil {
		out.Notes = append(out.Notes, merr.Error())
	} else {
		out.Retired, out.Signals = &retired, &signals
	}

	if deep {
		h, note, herr := p.censusHistory(ctx)
		if herr != nil {
			return nil, herr
		}
		out.History = h
		if note != "" {
			out.Notes = append(out.Notes, note)
		}
	}

	out.DurationMS = time.Since(start).Milliseconds()
	return out, nil
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

// censusMaxAttempts bounds how often censusFamilies re-reads the table set
// when a flush or compaction changed it mid-census.
const censusMaxAttempts = 3

// censusFamilies fills the totals and per-family figures from sstable
// properties. See TASK-A2 step 8 for the key-count definition.
//
// It makes one SSTables call with properties, then one span call per key
// range, then lists the tables again. Compactions run concurrently, so a
// table from the first listing can be gone by the time the span calls run,
// which would hand its figures to the wrong family. When the table set
// changed, the whole pass is retried; after censusMaxAttempts, tables that
// disappeared are split evenly across the ranges their key bounds overlap,
// and a note says how many.
func (p *PebbleStore) censusFamilies(out *DBCensus) error {
	ranges := keyFamilyRanges(keyFamilies)
	var (
		tables map[uint64]*censusTable
		span   map[uint64]map[int]uint64
		disk   []uint64
		gone   map[uint64]bool
		err    error
	)
	for attempt := 1; attempt <= censusMaxAttempts; attempt++ {
		tables, span, disk, gone, err = p.censusSnapshot(ranges)
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
		out.Notes = append(out.Notes, fmt.Sprintf("%d virtual sstables report their backing table's properties, so their figures can be overstated", virtual))
	}
	if len(gone) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d sstables were compacted away during the census after %d attempts; their figures were split evenly across the families their key bounds overlap", len(gone), censusMaxAttempts))
	}

	accs := map[string]*censusAcc{}
	acc := func(fam string) *censusAcc {
		a := accs[fam]
		if a == nil {
			a = &censusAcc{tables: map[uint64]struct{}{}}
			accs[fam] = a
		}
		return a
	}
	for ri, du := range disk {
		acc(ranges[ri].Family).disk += du
	}

	for fn, t := range tables {
		overlaps := span[fn]
		if gone[fn] || len(overlaps) == 0 {
			overlaps = censusBoundsOverlap(ranges, t.smallest, t.largest)
		}
		var total uint64
		for _, b := range overlaps {
			total += b
		}
		// Shares per family: a parent split into several pieces sums its
		// pieces before deciding whether it received the whole table.
		famShare := map[string]float64{}
		for ri, b := range overlaps {
			var share float64
			if total > 0 {
				share = float64(b) / float64(total)
			} else {
				share = 1 / float64(len(overlaps))
			}
			famShare[ranges[ri].Family] += share
		}
		// Every family a straddling table overlaps is Estimated, including one
		// whose share came out 0: span bytes are block-granular, so a range
		// inside the table's last data block can read 0 while holding keys.
		straddles := len(famShare) > 1
		for fam, share := range famShare {
			if straddles {
				acc(fam).estimated = true
			}
			if share <= 0 {
				continue
			}
			a := acc(fam)
			a.keys += share * t.entries
			a.deletions += share * t.deletions
			a.rawKey += share * t.rawKey
			a.rawValue += share * t.rawVal
			a.tables[fn] = struct{}{}
			if share < 1-1e-9 {
				a.estimated = true
			}
		}
	}

	fams := KeyFamilies()
	fams = append(fams, KeyFamily{
		Prefix:      unregisteredFamily,
		Description: "keys outside every registered family",
	})
	out.Families = make([]FamilyCensus, 0, len(fams))
	for _, f := range fams {
		fc := FamilyCensus{Prefix: f.Prefix, Description: f.Description, Owner: f.Owner}
		if a := accs[f.Prefix]; a != nil {
			fc.Keys = int64(math.Round(a.keys))
			fc.Deletions = int64(math.Round(a.deletions))
			fc.RawKeyBytes = int64(math.Round(a.rawKey))
			fc.RawValueBytes = int64(math.Round(a.rawValue))
			fc.DiskBytes = a.disk
			fc.Tables = len(a.tables)
			fc.Estimated = a.estimated
		}
		out.Families = append(out.Families, fc)
	}

	m := p.db.Metrics()
	out.DiskSpaceUsage = m.DiskSpaceUsage()
	return nil
}

// censusSnapshot reads the table set with properties, the per-range span bytes
// and disk usage, then the table set again. gone holds the tables of the first
// listing that the second no longer has.
func (p *PebbleStore) censusSnapshot(ranges []keyRange) (
	tables map[uint64]*censusTable, span map[uint64]map[int]uint64, disk []uint64, gone map[uint64]bool, err error,
) {
	levels, err := p.db.SSTables(pebble.WithProperties())
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("db census: list sstables: %w", err)
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
			if pr := info.Properties; pr == nil {
				t.noProps = true
			} else {
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
	disk = make([]uint64, len(ranges))
	for ri, r := range ranges {
		lo, hi := censusBounds(r)
		got, err := p.db.SSTables(pebble.WithKeyRangeFilter(lo, hi), pebble.WithApproximateSpanBytes())
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("db census: sstables for %s: %w", r.Family, err)
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
		du, err := p.db.EstimateDiskUsage(lo, hi)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("db census: disk usage for %s: %w", r.Family, err)
		}
		disk[ri] = du
	}

	after, err := p.db.SSTables()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("db census: re-list sstables: %w", err)
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
	return tables, span, disk, gone, nil
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

// censusHistory iterates book_ver: keys (never values) and returns the
// per-book distribution. One iterator, no per-item I/O, so it is sequential.
func (p *PebbleStore) censusHistory(ctx context.Context) (_ *HistoryCensus, note string, err error) {
	prefix := []byte("book_ver:")
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, "", fmt.Errorf("db census: history iterator: %w", err)
	}
	counts := map[string]int64{}
	var malformed, n int64
	for valid := it.First(); valid; valid = it.Next() {
		n++
		if n%censusCtxCheckEvery == 0 {
			if cerr := ctx.Err(); cerr != nil {
				if closeErr := it.Close(); closeErr != nil {
					return nil, "", errors.Join(cerr, closeErr)
				}
				return nil, "", cerr
			}
		}
		rest := it.Key()[len(prefix):]
		idx := bytes.LastIndexByte(rest, ':')
		if idx <= 0 {
			malformed++
			continue
		}
		counts[string(rest[:idx])]++
	}
	if cerr := it.Close(); cerr != nil {
		return nil, "", fmt.Errorf("db census: history iterator: %w", cerr)
	}

	h := buildHistoryCensus(counts)
	if malformed > 0 {
		note = fmt.Sprintf("%d book_ver: keys had no <id>:<nanos> shape and were skipped", malformed)
	}
	if m := p.mem(); m != nil {
		ob, oe, merr := m.historyOrphans(counts)
		if merr != nil {
			note = strings.TrimPrefix(note+"; history orphans unavailable: "+merr.Error(), "; ")
		} else {
			h.OrphanBooks, h.OrphanEntries, h.OrphansKnown = ob, oe, true
		}
	}
	return h, note, nil
}

// buildHistoryCensus turns per-book counts into the distribution.
func buildHistoryCensus(counts map[string]int64) *HistoryCensus {
	h := &HistoryCensus{Buckets: map[string]int64{
		"1-9": 0, "10-49": 0, "50-99": 0, "100-499": 0, "500-999": 0, "1000+": 0,
	}}
	if len(counts) == 0 {
		h.Top = []BookHistoryCount{}
		return h
	}
	all := make([]BookHistoryCount, 0, len(counts))
	for id, c := range counts {
		all = append(all, BookHistoryCount{BookID: id, Entries: c})
		h.Entries += c
		switch {
		case c < 10:
			h.Buckets["1-9"]++
		case c < 50:
			h.Buckets["10-49"]++
		case c < 100:
			h.Buckets["50-99"]++
		case c < 500:
			h.Buckets["100-499"]++
		case c < 1000:
			h.Buckets["500-999"]++
		default:
			h.Buckets["1000+"]++
		}
	}
	h.BooksWithHistory = int64(len(all))
	h.Mean = float64(h.Entries) / float64(len(all))

	sort.Slice(all, func(i, j int) bool {
		if all[i].Entries != all[j].Entries {
			return all[i].Entries > all[j].Entries
		}
		return all[i].BookID < all[j].BookID
	})
	h.Max = all[0].Entries
	// Nearest-rank percentiles over the ascending order (all is descending).
	pct := func(p float64) int64 {
		rank := int(math.Ceil(p * float64(len(all))))
		if rank < 1 {
			rank = 1
		}
		return all[len(all)-rank].Entries
	}
	h.P50, h.P90, h.P99 = pct(0.50), pct(0.90), pct(0.99)
	top := censusHistoryTopN
	if top > len(all) {
		top = len(all)
	}
	h.Top = append([]BookHistoryCount(nil), all[:top]...)
	return h
}
