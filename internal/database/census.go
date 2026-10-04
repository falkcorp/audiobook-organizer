// file: internal/database/census.go
// version: 1.2.0
// guid: be43e3c6-39e4-4a3e-a43c-cd5651ff141c
// last-edited: 2026-10-04

package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// CensusFamilyFiguresBasis labels every per-family figure the census reports.
// Consumers that show per-family figures show this label next to them.
const CensusFamilyFiguresBasis = "on-disk entries incl. tombstones and shadowed versions; deletions reported separately; figures move only after compaction"

// Census family methods (FamilyCensus.Method).
const (
	// CensusMethodExact: counted by a bounded keys-only iteration of the
	// family's ranges. Keys are live keys at census time (memtable included).
	CensusMethodExact = "exact"
	// CensusMethodEstimated: apportioned from sstable properties by span
	// bytes; ErrorBoundKeys bounds the error.
	CensusMethodEstimated = "estimated"
	// CensusMethodEmpty: the family's ranges hold no entry at all.
	CensusMethodEmpty = "empty"
)

const (
	censusNoteKeyDefinition = "estimated families: keys = sstable entries minus deletions; overwritten versions not yet compacted count once each"
	censusNoteExact         = "exact families: keys = live keys at census time (memtable included), counted keys-only; deletions = tombstones plus shadowed versions not yet compacted; disk bytes are apportioned table bytes"
	censusNoteMemtable      = "estimated families read sstable properties only: unflushed memtable contents (up to 2 x MemTableSize) are not counted"
	censusNoteMemdbCold     = "memdb not warm: retired and signal counts unavailable"
	censusNoteHiddenSystem  = "some system records live inside other families and are counted there: settings-backed markers under setting: (repairs_last_*_op:, *_index_v1_done flags), and the _system user's records under pref:_system: (broken out below it by sub-prefix), plus global preferences under preference:"

	censusCacheTTL        = 5 * time.Minute
	censusCacheMaxStores  = 8
	censusHistoryTopN     = 20
	censusCtxCheckEvery   = 10000
	censusFlightTimeout   = 5 * time.Minute
	censusDeepBudget      = 2 * time.Minute
	censusExactBudget     = 60 * time.Second
	censusDeepPartialNote = "deep pass stopped at its %s time budget after %d book_ver: keys (last key %q): history is partial"
)

// Bound substitutes for the open ends of the key space. Pebble's
// WithApproximateSpanBytes requires both bounds to be non-nil.
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

// FamilyCensus is one key family's figures.
//
// Method says how they were obtained (CensusMethodExact, CensusMethodEstimated
// or CensusMethodEmpty); the meaning of Keys and Deletions differs slightly
// between exact and estimated families (see the census notes).
type FamilyCensus struct {
	Prefix      string `json:"prefix"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	Method      string `json:"method"`
	Keys        int64  `json:"keys"`
	Deletions   int64  `json:"deletions"`
	// Entries is every internal entry attributed to the family: keys plus
	// deletions plus shadowed versions. Σ Entries over all families equals
	// the census TotalEntries (plus memtable points of exact families).
	Entries       int64  `json:"entries"`
	RawKeyBytes   int64  `json:"raw_key_bytes"`
	RawValueBytes int64  `json:"raw_value_bytes"`
	DiskBytes     uint64 `json:"disk_bytes"`
	Tables        int    `json:"tables"`
	// Estimated is Method == CensusMethodEstimated, kept for consumers that
	// read the boolean.
	Estimated bool `json:"estimated"`
	// ErrorBoundKeys bounds |Keys − true on-disk keys| for an estimated
	// family. Per table it shares: when several estimated families split the
	// table, the larger of its byte share and the rest, times the entries left
	// after exact families; plus its share of the entries of exact families
	// that span several tables and were placed by span weight. Zero for exact
	// and empty families.
	ErrorBoundKeys int64 `json:"error_bound_keys"`
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
	// Partial is true when the pass hit its time budget; the figures then
	// cover only the keys before the last one reached (see the notes).
	Partial bool `json:"partial"`
}

// DBCensus is the per-family census of the main Pebble store.
type DBCensus struct {
	GeneratedAt    time.Time `json:"generated_at"`
	DurationMS     int64     `json:"duration_ms"`
	ExactPassMS    int64     `json:"exact_pass_ms"`
	ExactPassKeys  int64     `json:"exact_pass_keys"`
	Cached         bool      `json:"cached"`
	TotalKeys      int64     `json:"total_keys"`
	TotalDeletions int64     `json:"total_deletions"`
	// TotalEntries is Σ sstable NumEntries (keys, deletions and shadowed
	// versions).
	TotalEntries       int64          `json:"total_entries"`
	TotalTables        int            `json:"total_tables"`
	TotalTableBytes    uint64         `json:"total_table_bytes"`
	DiskSpaceUsage     uint64         `json:"disk_space_usage"`
	Families           []FamilyCensus `json:"families"`
	Retired            *RetiredCensus `json:"retired"`
	Signals            *SignalCensus  `json:"signals"`
	History            *HistoryCensus `json:"history"`
	FamilyFiguresBasis string         `json:"family_figures_basis"`
	Notes              []string       `json:"notes"`

	// memdbCold marks a census taken before memdb was published; it is not
	// cached, so the retired/signal counts appear as soon as warmup ends.
	memdbCold bool
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

// censusCache holds two slots (shallow, deep) per *pebble.DB, for at most
// censusCacheMaxStores stores; the store with the oldest entry is evicted
// first, so closed stores (tests open thousands) do not accumulate.
//
// TODO(storage-a3+): move into PebbleStore (and drop it on Close) once
// pebble_store.go is free. It is package-level only because TASK-A4 owns
// pebble_store.go in this wave.
var censusCache = struct {
	mu sync.Mutex
	m  map[*pebble.DB]*[2]censusCacheEntry
}{m: map[*pebble.DB]*[2]censusCacheEntry{}}

var censusFlight singleflight.Group

// censusExactThreshold is the preliminary entry estimate below which a family
// is counted exactly by a keys-only pass. A variable only so tests can force
// the apportioning path; nothing changes it in production.
var censusExactThreshold float64 = 1_000_000

// censusBeforeCompute, when set (tests only), runs at the start of every
// shared census computation.
var censusBeforeCompute func()

var censusLog = logger.New("database.census")

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
		for len(censusCache.m) >= censusCacheMaxStores {
			var victim *pebble.DB
			var victimAt time.Time
			for k, s := range censusCache.m {
				latest := s[0].at
				if s[1].at.After(latest) {
					latest = s[1].at
				}
				if victim == nil || latest.Before(victimAt) {
					victim, victimAt = k, latest
				}
			}
			delete(censusCache.m, victim)
		}
		slots = &[2]censusCacheEntry{}
		censusCache.m[db] = slots
	}
	slots[censusSlot(deep)] = censusCacheEntry{value: v, at: at}
}

// censusCacheable reports whether a census may be cached: not when memdb was
// still cold (its retired/signal counts would stay missing for the whole TTL
// although warmup ends within minutes) and not when the deep pass was cut
// short. A memdb that refuses because it lost rows is a lasting state, so
// that census is cached like any other.
func censusCacheable(c *DBCensus) bool {
	if c.memdbCold {
		return false
	}
	if c.History != nil && c.History.Partial {
		return false
	}
	return true
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

// DBCensus reports, per registered key family, key counts and byte sizes.
// Large families are apportioned from sstable metadata; families estimated
// below censusExactThreshold entries are counted exactly by a bounded
// keys-only pass (within censusExactBudget). It adds the retired-book and
// signal counts from memdb when memdb is warm, and, with opts.Deep, the
// book_ver: history distribution.
//
// Results are cached for 5 minutes per (store, deep); concurrent callers share
// one computation. The shared computation runs detached from any one caller's
// context (bounded by censusFlightTimeout), and each caller waits on its own
// context, so one client disconnecting does not fail the others.
// opts.Fresh skips the cached read and refreshes the cache.
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
	ch := censusFlight.DoChan(key, func() (_ any, ferr error) {
		// DoChan re-panics a panic from here on a fresh goroutine, which
		// would take the process down; a diagnostics endpoint must not.
		defer func() {
			if rec := recover(); rec != nil {
				censusLog.Error("db census panicked: %v\n%s", rec, debug.Stack())
				ferr = fmt.Errorf("db census: panic: %v", rec)
			}
		}()
		if censusBeforeCompute != nil {
			censusBeforeCompute()
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), censusFlightTimeout)
		defer cancel()
		c, cerr := p.computeDBCensus(fctx, opts.Deep)
		if cerr != nil {
			return nil, cerr
		}
		if censusCacheable(c) {
			censusCachePut(p.db, opts.Deep, c, c.GeneratedAt)
		}
		return c, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		cp := res.Val.(*DBCensus).clone()
		cp.Cached = false
		return cp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// computeDBCensus does the work behind DBCensus, uncached.
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
		Notes:              []string{censusNoteKeyDefinition, censusNoteExact, censusNoteMemtable, censusNoteHiddenSystem},
	}

	if err := p.censusFamilies(ctx, out, censusExactThreshold, time.Now().Add(censusExactBudget)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if m := p.mem(); m == nil {
		out.memdbCold = true
		out.Notes = append(out.Notes, censusNoteMemdbCold)
	} else if retired, signals, merr := m.retiredAndSignalCensus(); merr != nil {
		out.Notes = append(out.Notes, merr.Error())
	} else {
		out.Retired, out.Signals = &retired, &signals
	}

	if deep {
		h, notes, herr := p.censusHistory(ctx, time.Now().Add(censusDeepBudget))
		if herr != nil {
			return nil, herr
		}
		out.History = h
		out.Notes = append(out.Notes, notes...)
	}

	out.DurationMS = time.Since(start).Milliseconds()
	return out, nil
}

// censusHistory iterates book_ver: keys (never values) and returns the
// per-book distribution. One iterator, no per-item I/O, so it is sequential.
// It stops at the deadline and returns what it has, marked Partial.
func (p *PebbleStore) censusHistory(ctx context.Context, deadline time.Time) (_ *HistoryCensus, notes []string, err error) {
	prefix := []byte("book_ver:")
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, nil, fmt.Errorf("db census: history iterator: %w", err)
	}
	counts := map[string]int64{}
	var malformed, n int64
	partial := false
	var lastKey []byte
	for valid := it.First(); valid; valid = it.Next() {
		n++
		if n%censusCtxCheckEvery == 0 {
			if cerr := ctx.Err(); cerr != nil {
				if closeErr := it.Close(); closeErr != nil {
					return nil, nil, errors.Join(cerr, closeErr)
				}
				return nil, nil, cerr
			}
			if time.Now().After(deadline) {
				partial = true
				lastKey = append([]byte(nil), it.Key()...)
				break
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
		return nil, nil, fmt.Errorf("db census: history iterator: %w", cerr)
	}

	h := buildHistoryCensus(counts)
	h.Partial = partial
	if partial {
		notes = append(notes, fmt.Sprintf(censusDeepPartialNote, censusDeepBudget, n, lastKey))
	}
	if malformed > 0 {
		notes = append(notes, fmt.Sprintf("%d book_ver: keys had no <id>:<nanos> shape and were skipped", malformed))
	}
	if m := p.mem(); m != nil {
		ob, oe, merr := m.historyOrphans(counts)
		if merr != nil {
			notes = append(notes, "history orphans unavailable: "+merr.Error())
		} else {
			h.OrphanBooks, h.OrphanEntries, h.OrphansKnown = ob, oe, true
		}
	}
	return h, notes, nil
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
