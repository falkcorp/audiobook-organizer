// file: internal/database/census.go
// version: 1.5.0
// guid: be43e3c6-39e4-4a3e-a43c-cd5651ff141c
// last-edited: 2026-10-04

package database

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// CensusFamilyFiguresBasis labels every per-family figure the census reports.
// Consumers that show per-family figures show this label next to them.
const CensusFamilyFiguresBasis = "on-disk entries incl. tombstones and shadowed versions; deletions reported separately; figures move only after compaction"

// Census kinds (DBCensus.Kind) and family methods (FamilyCensus.Method).
const (
	// CensusKindEstimated is the cheap census the endpoint computes on
	// request: sstable properties apportioned by span bytes.
	CensusKindEstimated = "estimated"
	// CensusKindExact is the census the db-census-exact maintenance op writes:
	// every family counted by a rate-limited keys-only pass.
	CensusKindExact = "exact"

	CensusMethodExact     = "exact"
	CensusMethodEstimated = "estimated"
	CensusMethodEmpty     = "empty"
)

const (
	censusNoteKeyDefinition = "estimated census: keys = sstable entries minus deletions; overwritten versions not yet compacted count once each; per-family figures are apportioned by span bytes within shared tables (error_bound_keys bounds the error); exact per-family counts are in last_exact, written by the maintenance.db-census-exact op"
	censusNoteMemdbCold     = "memdb not warm: retired and signal counts unavailable"
	censusNoteHiddenSystem  = "some system records live inside other families and are counted there: settings-backed markers under setting: (repairs_last_*_op:, *_index_v1_done flags), the _system user's records under pref:_system: (broken out below it by sub-prefix), and global preferences under preference:"

	censusCacheTTL = 5 * time.Minute
	// censusColdCacheTTL caches a census taken while memdb is still cold
	// only briefly, so the retired/signal counts appear soon after warmup
	// but a burst of requests during warmup does not recompute each time.
	censusColdCacheTTL = 15 * time.Second
	// censusFlushMinInterval and censusFlushMinMemtable gate the flush at the
	// start of a census: at most one per store per interval (fresh=true
	// included), and none while less than censusFlushMinMemtable bytes of
	// writes are unflushed (Metrics().WAL.Size).
	censusFlushMinInterval = 60 * time.Second
	censusFlushMinMemtable = 1 << 20
	censusCacheMaxStores   = 8
	censusHistoryTopN      = 20
	censusFlightTimeout    = 2 * time.Minute
)

// Bound substitutes for the open ends of the key space. Pebble's
// WithApproximateSpanBytes requires both bounds to be non-nil.
var (
	censusMinKey = []byte{0x00}
	censusMaxKey = []byte{0xff, 0xff, 0xff, 0xff}
)

// CensusOptions selects what DBCensus computes.
type CensusOptions struct {
	// Fresh bypasses the cache for the read and refreshes it.
	Fresh bool `json:"fresh"`
}

// FamilyCensus is one key family's figures.
//
// In an estimated census every non-empty family has Method "estimated"; in an
// exact census every family has Method "exact" (or "empty"). See the census
// notes for what Keys and Deletions mean in each.
type FamilyCensus struct {
	Prefix      string `json:"prefix"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	Method      string `json:"method"`
	Keys        int64  `json:"keys"`
	Deletions   int64  `json:"deletions"`
	// RangeDeletedKeys (exact census only) is what Pebble's iterator reports
	// as points covered by an uncompacted range deletion. Pebble skips most
	// covered points without visiting them, so this undercounts; families
	// whose ranges overlap range deletions are named in the notes.
	RangeDeletedKeys int64 `json:"range_deleted_keys"`
	// Entries is every internal entry attributed to the family: keys plus
	// point tombstones plus shadowed versions. In an estimated census
	// Σ Entries over all families equals TotalEntries (the memtable is flushed
	// first). In an exact census it is what the keys-only pass stepped over,
	// which includes writes that landed while the pass ran.
	Entries       int64  `json:"entries"`
	RawKeyBytes   int64  `json:"raw_key_bytes"`
	RawValueBytes int64  `json:"raw_value_bytes"`
	DiskBytes     uint64 `json:"disk_bytes"`
	Tables        int    `json:"tables"`
	// Estimated is Method == CensusMethodEstimated, kept for consumers that
	// read the boolean.
	Estimated bool `json:"estimated"`
	// ErrorBoundKeys bounds |Keys − true on-disk keys| for an estimated
	// family: per table it shares with other families, the larger of its
	// byte share and the rest times the table's entries; per table holding
	// range deletions, its share of the table's entries (any of them could
	// be covered). Zero for exact and empty families.
	ErrorBoundKeys int64 `json:"error_bound_keys"`

	// rangeDel: some table overlapping the family's ranges holds range
	// deletions (set by censusFamilies, used for the notes).
	rangeDel bool
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

// ExactCensusProgress describes an exact census run that has not finished.
type ExactCensusProgress struct {
	RunID        string    `json:"run_id"`
	Registry     string    `json:"registry"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	FamiliesDone int       `json:"families_done"`
	FamiliesAll  int       `json:"families_all"`
	BytesRead    int64     `json:"bytes_read"`
	// Stale is true when the saved run would not be resumed: it was last
	// updated more than exactCensusProgressMaxAge ago, or under a different
	// registry or census format.
	Stale bool `json:"stale"`
}

// DBCensus is a per-family census of the main Pebble store.
type DBCensus struct {
	Kind           string    `json:"kind"`
	GeneratedAt    time.Time `json:"generated_at"`
	DurationMS     int64     `json:"duration_ms"`
	Cached         bool      `json:"cached"`
	TotalKeys      int64     `json:"total_keys"`
	TotalDeletions int64     `json:"total_deletions"`
	// TotalEntries is Σ sstable NumEntries (keys, deletions and shadowed
	// versions).
	TotalEntries    int64  `json:"total_entries"`
	TotalTables     int    `json:"total_tables"`
	TotalTableBytes uint64 `json:"total_table_bytes"`
	DiskSpaceUsage  uint64 `json:"disk_space_usage"`
	// BytesRead (exact census) is the key and value bytes the pass read.
	BytesRead          int64          `json:"bytes_read,omitempty"`
	Families           []FamilyCensus `json:"families"`
	Retired            *RetiredCensus `json:"retired"`
	Signals            *SignalCensus  `json:"signals"`
	History            *HistoryCensus `json:"history"`
	FamilyFiguresBasis string         `json:"family_figures_basis"`
	Notes              []string       `json:"notes"`
	// LastExact (estimated census only) is the last completed exact census,
	// or nil when none has run. Its GeneratedAt says how old it is.
	LastExact *DBCensus `json:"last_exact"`
	// ExactInProgress (estimated census only) is set while an exact census
	// run has unfinished progress saved.
	ExactInProgress *ExactCensusProgress `json:"exact_in_progress"`

	// memdbCold marks a census taken before memdb was published; it is not
	// cached, so the retired/signal counts appear as soon as warmup ends.
	memdbCold bool
}

// DBCensusProvider is the capability the db-census endpoint resolves.
type DBCensusProvider interface {
	DBCensus(ctx context.Context, opts CensusOptions) (*DBCensus, error)
}

var _ DBCensusProvider = (*PebbleStore)(nil)

// censusCacheEntry is one cached census, when it was computed and how long
// it may be served.
type censusCacheEntry struct {
	value *DBCensus
	at    time.Time
	ttl   time.Duration
}

// censusCache holds the estimated census per *pebble.DB, for at most
// censusCacheMaxStores stores; the store with the oldest entry is evicted
// first, so closed stores (tests open thousands) do not accumulate.
//
// TODO(storage-a3+): move into PebbleStore (and drop it on Close) once
// pebble_store.go is free. It is package-level only because TASK-A4 owns
// pebble_store.go in this wave.
var censusCache = struct {
	mu sync.Mutex
	m  map[*pebble.DB]censusCacheEntry
}{m: map[*pebble.DB]censusCacheEntry{}}

var censusFlight singleflight.Group

var censusLog = logger.New("database.census")

// censusBeforeCompute, when set (tests only), runs at the start of every
// shared census computation.
var censusBeforeCompute func()

func censusCacheGet(db *pebble.DB, now time.Time) *DBCensus {
	censusCache.mu.Lock()
	defer censusCache.mu.Unlock()
	e, ok := censusCache.m[db]
	if !ok || e.value == nil || now.Sub(e.at) > e.ttl {
		return nil
	}
	return e.value
}

func censusCachePut(db *pebble.DB, v *DBCensus, at time.Time, ttl time.Duration) {
	censusCache.mu.Lock()
	defer censusCache.mu.Unlock()
	if _, ok := censusCache.m[db]; !ok {
		for len(censusCache.m) >= censusCacheMaxStores {
			var victim *pebble.DB
			var victimAt time.Time
			for k, e := range censusCache.m {
				if victim == nil || e.at.Before(victimAt) {
					victim, victimAt = k, e.at
				}
			}
			delete(censusCache.m, victim)
		}
	}
	censusCache.m[db] = censusCacheEntry{value: v, at: at, ttl: ttl}
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
	if c.LastExact != nil {
		cp.LastExact = c.LastExact.clone()
	}
	if c.ExactInProgress != nil {
		pr := *c.ExactInProgress
		cp.ExactInProgress = &pr
	}
	return &cp
}

// DBCensus returns the cheap estimated census — sstable properties apportioned
// by span bytes, plus one bounded seek per family range — together with the
// last completed exact census (LastExact) and any exact run in progress. It
// reads no values and iterates no family. The exact census is produced only
// by the maintenance.db-census-exact op (RunExactCensus).
//
// The estimated part is cached for 5 minutes per store; concurrent callers
// share one computation, which runs detached from any one caller's context
// (bounded by censusFlightTimeout), and each caller waits on its own context,
// so one client disconnecting does not fail the others. LastExact is read
// fresh on every call. opts.Fresh skips the cached read and refreshes it.
func (p *PebbleStore) DBCensus(ctx context.Context, opts CensusOptions) (_ *DBCensus, err error) {
	defer recoverPebbleClosed("DBCensus", &err)
	if p == nil || p.db == nil {
		return nil, errors.New("db census: store not open")
	}
	var est *DBCensus
	cached := false
	if !opts.Fresh {
		if hit := censusCacheGet(p.db, time.Now()); hit != nil {
			est, cached = hit.clone(), true
		}
	}
	if est == nil {
		key := fmt.Sprintf("%p", p.db)
		ch := censusFlight.DoChan(key, func() (_ any, ferr error) {
			// DoChan re-panics a panic from here on a fresh goroutine,
			// which would take the process down; a diagnostics endpoint
			// must not.
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
			c, cerr := p.computeDBCensus(fctx)
			if cerr != nil {
				return nil, cerr
			}
			ttl := censusCacheTTL
			if c.memdbCold {
				ttl = censusColdCacheTTL
			}
			censusCachePut(p.db, c, c.GeneratedAt, ttl)
			return c, nil
		})
		select {
		case res := <-ch:
			if res.Err != nil {
				return nil, res.Err
			}
			est = res.Val.(*DBCensus).clone()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	est.Cached = cached

	last, lerr := p.LastExactCensus()
	if lerr != nil {
		est.Notes = append(est.Notes, "last exact census unreadable: "+lerr.Error())
	}
	est.LastExact = last
	prog, perr := p.exactCensusProgress()
	if perr != nil {
		est.Notes = append(est.Notes, "exact census progress unreadable: "+perr.Error())
	}
	est.ExactInProgress = prog
	return est, nil
}

// computeDBCensus does the work behind DBCensus, uncached.
func (p *PebbleStore) computeDBCensus(ctx context.Context) (_ *DBCensus, err error) {
	defer recoverPebbleClosed("DBCensus", &err)
	start := time.Now()

	// EstimateDiskUsage is documented to panic with ErrClosed on a closed DB,
	// which recoverPebbleClosed turns into an error. Probe it first so a
	// closed store fails here, before SSTables can return stale metadata.
	if _, err := p.db.EstimateDiskUsage(censusMinKey, censusMaxKey); err != nil {
		return nil, fmt.Errorf("db census: estimate disk usage: %w", err)
	}
	flushNote, err := p.censusMaybeFlush(start)
	if err != nil {
		return nil, err
	}

	out := &DBCensus{
		Kind:               CensusKindEstimated,
		GeneratedAt:        start,
		FamilyFiguresBasis: CensusFamilyFiguresBasis,
		Notes:              []string{censusNoteKeyDefinition, flushNote, censusNoteHiddenSystem},
	}
	if err := p.censusFamilies(ctx, out); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.censusMemdb(out)
	out.DurationMS = time.Since(start).Milliseconds()
	return out, nil
}

// censusFlushes remembers the last census flush per store (bounded like the
// cache).
var censusFlushes = struct {
	mu sync.Mutex
	m  map[*pebble.DB]time.Time
}{m: map[*pebble.DB]time.Time{}}

// censusMaybeFlush flushes the memtable so the sstable properties cover recent
// writes — unless the memtable is nearly empty or this store was flushed for a
// census less than censusFlushMinInterval ago — and returns the note saying
// which happened. A request, fresh or not, can therefore cause at most one
// flush per store per interval.
func (p *PebbleStore) censusMaybeFlush(now time.Time) (string, error) {
	// WAL.Size is the live (unflushed) data in the WAL: what the memtable and
	// any queued immutable memtables hold. MemTable.Size would be the wrong
	// gauge — it is arena capacity (256 KiB on a fresh store, 4 MiB once the
	// memtables have grown), not bytes in use, so it passes any threshold
	// below that capacity whether or not anything was written.
	mt := p.db.Metrics().WAL.Size
	if mt < censusFlushMinMemtable {
		return fmt.Sprintf("memtable not flushed: only %d bytes of writes are unflushed; those writes are not in the figures", mt), nil
	}
	censusFlushes.mu.Lock()
	last, seen := censusFlushes.m[p.db]
	if seen && now.Sub(last) < censusFlushMinInterval {
		censusFlushes.mu.Unlock()
		return fmt.Sprintf("memtable not flushed: the last census flush was %s ago (minimum interval %s); its %d bytes are not in the figures",
			now.Sub(last).Round(time.Second), censusFlushMinInterval, mt), nil
	}
	if !seen {
		for len(censusFlushes.m) >= censusCacheMaxStores {
			var victim *pebble.DB
			var victimAt time.Time
			for k, at := range censusFlushes.m {
				if victim == nil || at.Before(victimAt) {
					victim, victimAt = k, at
				}
			}
			delete(censusFlushes.m, victim)
		}
	}
	censusFlushes.m[p.db] = now
	censusFlushes.mu.Unlock()
	if err := p.db.Flush(); err != nil {
		return "", fmt.Errorf("db census: flush: %w", err)
	}
	return "the memtable was flushed at the start of the census so sstable properties cover recent writes", nil
}

// censusMemdb fills the retired and signal counts from memdb, or a note.
func (p *PebbleStore) censusMemdb(out *DBCensus) {
	if m := p.mem(); m == nil {
		out.memdbCold = true
		out.Notes = append(out.Notes, censusNoteMemdbCold)
	} else if retired, signals, merr := m.retiredAndSignalCensus(); merr != nil {
		out.Notes = append(out.Notes, merr.Error())
	} else {
		out.Retired, out.Signals = &retired, &signals
	}
}
