// file: internal/database/census_exact.go
// version: 1.2.0
// guid: 5d2b8e71-0c4a-4f3e-9b6d-2a7c1e8f4b90
// last-edited: 2026-10-04

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/time/rate"
)

// Store keys of the exact census, all under the registered "system:census:"
// family. The state holds the resumable position; the summary is the few
// fields the db-census endpoint shows, kept apart so a GET never decodes the
// state.
const (
	exactCensusResultKey   = "system:census:exact"
	exactCensusProgressKey = "system:census:exact_progress"
	exactCensusSummaryKey  = "system:census:exact_summary"
)

// censusExactFormatVersion is part of the registry fingerprint: bump it when
// the saved state's meaning changes, so old progress is not resumed.
const censusExactFormatVersion = 2

// Tunables of the exact pass. Variables only so tests can shrink them;
// nothing changes them in production.
var (
	// exactCensusIterRefresh bounds how long one iterator (and so one pinned
	// Pebble version) lives, and how often the resumable position is saved.
	exactCensusIterRefresh = 30 * time.Second
	// exactCensusCheckBytes is how many bytes the iterator steps over (keys
	// and values, tombstones and shadowed versions included, from its own
	// stats) between rate-limit, context, progress and refresh checks.
	exactCensusCheckBytes int64 = 256 << 10
	// exactCensusLongWait: a budget wait longer than this happens with the
	// iterator closed, so no version stays pinned while the pass sleeps.
	exactCensusLongWait = time.Second
	// exactCensusSubrangeBytes caps the on-disk span of one iterator's
	// bounds. Pebble steps over a run of tombstones inside a single Next()
	// with no way to interrupt it; with every family piece cut into
	// sub-ranges of at most this many bytes (by EstimateDiskUsage bisection),
	// no single Next() walks more than one sub-range, and the pass checks
	// the budget, the context and the watchdog at every sub-range boundary.
	exactCensusSubrangeBytes uint64 = 64 << 20
	// exactCensusHeartbeat is how often the pass reports progress while it
	// counts, waits for the budget, or crosses sub-ranges.
	exactCensusHeartbeat = 5 * time.Second
	// exactCensusAfterSave, when set (tests only), runs after each saved
	// in-family position.
	exactCensusAfterSave func(family string)
)

const (
	// exactCensusProgressMaxAge: progress not updated for longer than this
	// is discarded instead of resumed, and reported stale.
	exactCensusProgressMaxAge = 24 * time.Hour
	// exactCensusStatsEvery: the iterator stats are read every this many
	// live keys.
	exactCensusStatsEvery = 16

	censusNoteExactDefinition = "exact census: every family counted by a keys-only pass; keys = live keys when the family was read; deletions = point tombstones plus shadowed versions not yet compacted (points under an uncompacted range deletion are mostly skipped unseen, see range_deleted_keys and the range-deletion note); families were read one after another, so the census is not a single point-in-time snapshot; disk bytes and table counts are apportioned from sstable properties at the end of the run"
)

// ExactCensusOptions configures RunExactCensus.
type ExactCensusOptions struct {
	// ReadBytesPerSec caps the bytes the iterator steps over per second;
	// <= 0 means unlimited (tests only — production always sets a budget).
	ReadBytesPerSec int64
	// Progress, when set, is called after every family and at least every
	// exactCensusHeartbeat while the pass works or waits.
	Progress func(p ExactCensusProgress, family string)
	// Restart discards saved progress (of any run) and starts fresh.
	Restart bool
	// RunID tags the saved progress with the caller's run (the op ID), so a
	// caller can tell its own unfinished run from another one.
	RunID string
}

// DBCensusExactRunner is the capability the maintenance.db-census-exact op
// resolves.
type DBCensusExactRunner interface {
	RunExactCensus(ctx context.Context, opts ExactCensusOptions) (*DBCensus, error)
	LastExactCensus() (*DBCensus, error)
	// ExactCensusInProgress returns the saved progress of an unfinished run,
	// or nil.
	ExactCensusInProgress() (*ExactCensusProgress, error)
	// DiscardExactCensusProgress deletes any saved progress.
	DiscardExactCensusProgress() error
}

var _ DBCensusExactRunner = (*PebbleStore)(nil)

// exactFamilyCount is one family's exact figures.
type exactFamilyCount struct {
	Live     int64 `json:"live"`
	Points   int64 `json:"points"`
	Covered  int64 `json:"covered"`
	KeyBytes int64 `json:"key_bytes"`
	ValBytes int64 `json:"val_bytes"`
}

// exactCensusState is the saved progress of a run: the families finished so
// far, plus the position inside the family being read.
type exactCensusState struct {
	Registry  string                      `json:"registry"`
	RunID     string                      `json:"run_id"`
	StartedAt time.Time                   `json:"started_at"`
	UpdatedAt time.Time                   `json:"updated_at"`
	Done      map[string]exactFamilyCount `json:"done"`
	BytesRead int64                       `json:"bytes_read"`
	History   *HistoryCensus              `json:"history,omitempty"`
	// HistoryMalformed counts book_ver: keys without an <id>:<nanos> shape.
	HistoryMalformed int64         `json:"history_malformed,omitempty"`
	Partial          *exactPartial `json:"partial,omitempty"`
}

// exactPartial is the position inside an unfinished family: the range piece,
// the first key not yet counted, and the counts (and history fold) so far.
type exactPartial struct {
	Family   string            `json:"family"`
	Piece    int               `json:"piece"`
	ResumeAt []byte            `json:"resume_at"`
	Count    exactFamilyCount  `json:"count"`
	Fold     *exactHistoryFold `json:"fold,omitempty"`
}

// exactHistoryFold folds book_ver:<id>:<nanos> keys into the history
// distribution in one pass with a few KB of state. The keys of one book are
// contiguous (all keys with prefix "book_ver:<id>:" sort together, and no
// child family is registered under book_ver:), so only the current book's
// count is open at any time; finished books fold into Dist (entries -> number
// of books), the top-N list, the totals and the orphan tally.
type exactHistoryFold struct {
	CurBook        string             `json:"cur_book"`
	CurCount       int64              `json:"cur_count"`
	Dist           map[string]int64   `json:"dist"`
	Top            []BookHistoryCount `json:"top"`
	Entries        int64              `json:"entries"`
	Books          int64              `json:"books"`
	OrphanBooks    int64              `json:"orphan_books"`
	OrphanEntries  int64              `json:"orphan_entries"`
	OrphansUnknown bool               `json:"orphans_unknown"`
	Malformed      int64              `json:"malformed"`

	cur []byte // CurBook as bytes, to compare without allocating
}

func newExactHistoryFold() *exactHistoryFold {
	return &exactHistoryFold{Dist: map[string]int64{}}
}

// add counts one book_ver: key.
func (f *exactHistoryFold) add(key []byte, m *MemStore) {
	rest := key[len("book_ver:"):]
	i := bytes.LastIndexByte(rest, ':')
	if i <= 0 {
		f.Malformed++
		return
	}
	id := rest[:i]
	if f.cur == nil && f.CurBook != "" {
		f.cur = []byte(f.CurBook) // restored from saved state
	}
	if f.CurBook != "" && bytes.Equal(id, f.cur) {
		f.CurCount++
		return
	}
	f.closeBook(m)
	f.cur = append(f.cur[:0], id...)
	f.CurBook, f.CurCount = string(id), 1
}

// closeBook folds the open book into the distribution.
func (f *exactHistoryFold) closeBook(m *MemStore) {
	if f.CurBook == "" || f.CurCount == 0 {
		return
	}
	c := f.CurCount
	f.Dist[strconv.FormatInt(c, 10)]++
	f.Books++
	f.Entries += c
	f.Top = censusTopInsert(f.Top, BookHistoryCount{BookID: f.CurBook, Entries: c}, censusHistoryTopN)
	if m == nil {
		f.OrphansUnknown = true
	} else if ok, err := m.bookRowExists(f.CurBook); err != nil {
		f.OrphansUnknown = true
	} else if !ok {
		f.OrphanBooks++
		f.OrphanEntries += c
	}
	f.CurBook, f.CurCount, f.cur = "", 0, f.cur[:0]
}

// censusTopInsert keeps the n largest by entries (ties by book id) in order.
func censusTopInsert(top []BookHistoryCount, b BookHistoryCount, n int) []BookHistoryCount {
	less := func(x, y BookHistoryCount) bool { // x ranks before y
		if x.Entries != y.Entries {
			return x.Entries > y.Entries
		}
		return x.BookID < y.BookID
	}
	i := sort.Search(len(top), func(i int) bool { return less(b, top[i]) })
	if i >= n {
		return top
	}
	top = append(top, BookHistoryCount{})
	copy(top[i+1:], top[i:])
	top[i] = b
	if len(top) > n {
		top = top[:n]
	}
	return top
}

// finish closes the open book and returns the distribution.
func (f *exactHistoryFold) finish(m *MemStore) *HistoryCensus {
	f.closeBook(m)
	h := &HistoryCensus{Buckets: map[string]int64{
		"1-9": 0, "10-49": 0, "50-99": 0, "100-499": 0, "500-999": 0, "1000+": 0,
	}, Top: append([]BookHistoryCount{}, f.Top...)}
	if f.Books == 0 {
		h.OrphansKnown = !f.OrphansUnknown
		return h
	}
	type bin struct{ entries, books int64 }
	bins := make([]bin, 0, len(f.Dist))
	for k, n := range f.Dist {
		c, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			continue
		}
		bins = append(bins, bin{c, n})
		switch {
		case c < 10:
			h.Buckets["1-9"] += n
		case c < 50:
			h.Buckets["10-49"] += n
		case c < 100:
			h.Buckets["50-99"] += n
		case c < 500:
			h.Buckets["100-499"] += n
		case c < 1000:
			h.Buckets["500-999"] += n
		default:
			h.Buckets["1000+"] += n
		}
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].entries < bins[j].entries })
	h.BooksWithHistory, h.Entries = f.Books, f.Entries
	h.Mean = float64(f.Entries) / float64(f.Books)
	h.Max = bins[len(bins)-1].entries
	// Exact nearest-rank percentiles from the distribution.
	pct := func(p float64) int64 {
		rank := int64(math.Ceil(p * float64(f.Books)))
		if rank < 1 {
			rank = 1
		}
		var cum int64
		for _, b := range bins {
			cum += b.books
			if cum >= rank {
				return b.entries
			}
		}
		return h.Max
	}
	h.P50, h.P90, h.P99 = pct(0.50), pct(0.90), pct(0.99)
	h.OrphanBooks, h.OrphanEntries, h.OrphansKnown = f.OrphanBooks, f.OrphanEntries, !f.OrphansUnknown
	return h
}

// censusRegistryFingerprint identifies the family registry and the census
// format, so progress saved under a different one is not resumed.
func censusRegistryFingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "format:%d\x00", censusExactFormatVersion)
	for _, f := range KeyFamilies() {
		h.Write([]byte(f.Prefix))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// censusGetJSON decodes one stored key into v; found is false when absent.
func (p *PebbleStore) censusGetJSON(key string, v any) (found bool, err error) {
	defer recoverPebbleClosed("db census read "+key, &err)
	raw, closer, err := p.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", key, err)
	}
	defer func() {
		if cerr := closer.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("read %s: %w", key, cerr)
		}
	}()
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("decode %s: %w", key, err)
	}
	return true, nil
}

// LastExactCensus returns the last completed exact census, or nil.
func (p *PebbleStore) LastExactCensus() (*DBCensus, error) {
	var c DBCensus
	found, err := p.censusGetJSON(exactCensusResultKey, &c)
	if err != nil || !found {
		return nil, err
	}
	return &c, nil
}

// ExactCensusInProgress returns the summary of an unfinished run, or nil. It
// reads only the small summary key.
func (p *PebbleStore) ExactCensusInProgress() (*ExactCensusProgress, error) {
	return p.exactCensusProgress()
}

func (p *PebbleStore) exactCensusProgress() (*ExactCensusProgress, error) {
	var pr ExactCensusProgress
	found, err := p.censusGetJSON(exactCensusSummaryKey, &pr)
	if err != nil || !found {
		return nil, err
	}
	pr.Stale = !exactCensusResumable(pr.Registry, pr.UpdatedAt, time.Now())
	return &pr, nil
}

// exactCensusResumable is the one rule for whether saved progress is resumed
// (and so whether exact_in_progress is current rather than stale).
func exactCensusResumable(registry string, updatedAt, now time.Time) bool {
	return registry == censusRegistryFingerprint() && now.Sub(updatedAt) < exactCensusProgressMaxAge
}

// DiscardExactCensusProgress deletes the saved state and summary.
func (p *PebbleStore) DiscardExactCensusProgress() (err error) {
	defer recoverPebbleClosed("DiscardExactCensusProgress", &err)
	b := p.db.NewBatch()
	if err := b.Delete([]byte(exactCensusProgressKey), nil); err != nil {
		return errors.Join(err, b.Close())
	}
	if err := b.Delete([]byte(exactCensusSummaryKey), nil); err != nil {
		return errors.Join(err, b.Close())
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return errors.Join(err, b.Close())
	}
	return b.Close()
}

func (p *PebbleStore) loadExactCensusState() (*exactCensusState, error) {
	var st exactCensusState
	found, err := p.censusGetJSON(exactCensusProgressKey, &st)
	if err != nil || !found {
		return nil, err
	}
	return &st, nil
}

// saveExactCensusState stores the state and its summary in one batch.
func (p *PebbleStore) saveExactCensusState(st *exactCensusState, familiesAll int, sync bool) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode exact census progress: %w", err)
	}
	sum, err := json.Marshal(ExactCensusProgress{
		RunID: st.RunID, Registry: st.Registry, StartedAt: st.StartedAt, UpdatedAt: st.UpdatedAt,
		FamiliesDone: len(st.Done), FamiliesAll: familiesAll, BytesRead: st.BytesRead,
	})
	if err != nil {
		return fmt.Errorf("encode exact census summary: %w", err)
	}
	opt := pebble.NoSync
	if sync {
		opt = pebble.Sync
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte(exactCensusProgressKey), data, nil); err != nil {
		return errors.Join(fmt.Errorf("save exact census progress: %w", err), b.Close())
	}
	if err := b.Set([]byte(exactCensusSummaryKey), sum, nil); err != nil {
		return errors.Join(fmt.Errorf("save exact census summary: %w", err), b.Close())
	}
	if err := b.Commit(opt); err != nil {
		return errors.Join(fmt.Errorf("save exact census progress: %w", err), b.Close())
	}
	return b.Close()
}

// exactCensusRun carries one run's shared state through the counting helpers.
type exactCensusRun struct {
	p        *PebbleStore
	ctx      context.Context
	lim      *rate.Limiter
	st       *exactCensusState
	all      int
	report   func(family string)
	lastBeat time.Time
	lastSave time.Time
}

// heartbeat reports progress if exactCensusHeartbeat has passed.
func (r *exactCensusRun) heartbeat(fam string) {
	if time.Since(r.lastBeat) >= exactCensusHeartbeat {
		r.report(fam)
		r.lastBeat = time.Now()
	}
}

// RunExactCensus counts every key family exactly with a rate-limited
// keys-only pass, family by family, and stores the result for the db-census
// endpoint (LastExactCensus). The same pass folds the book_ver: keys into the
// history distribution.
//
// Every family piece is cut into sub-ranges of at most
// exactCensusSubrangeBytes on disk, so one Next() never walks more than one
// sub-range of tombstones. The budget is charged on what the iterator steps
// over. Progress is reported at least every exactCensusHeartbeat, while
// counting and while waiting for the budget.
//
// It resumes: the position (family, piece, next key, counts, history fold) is
// saved at least every exactCensusIterRefresh and after every family, and a
// later call continues from it unless the progress is older than
// exactCensusProgressMaxAge (measured from its last update), was saved under
// another registry or format, or opts.Restart is set.
//
// A cancelled context stops the pass at the next check and leaves the
// progress unfinished; nothing is published.
func (p *PebbleStore) RunExactCensus(ctx context.Context, opts ExactCensusOptions) (_ *DBCensus, err error) {
	defer recoverPebbleClosed("RunExactCensus", &err)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	if opts.Restart {
		if err := p.DiscardExactCensusProgress(); err != nil {
			return nil, err
		}
	}
	if err := p.db.Flush(); err != nil {
		return nil, fmt.Errorf("exact census: flush: %w", err)
	}

	ranges := keyFamilyRanges(keyFamilies)
	pieces := map[string][]keyRange{}
	for _, r := range ranges {
		pieces[r.Family] = append(pieces[r.Family], r)
	}
	fams := KeyFamilies()
	names := make([]string, 0, len(fams)+1)
	for _, f := range fams {
		names = append(names, f.Prefix)
	}
	names = append(names, unregisteredFamily)

	reg := censusRegistryFingerprint()
	st, err := p.loadExactCensusState()
	if err != nil {
		return nil, err
	}
	resumed := st != nil && exactCensusResumable(st.Registry, st.UpdatedAt, start)
	if !resumed {
		st = &exactCensusState{Registry: reg, RunID: opts.RunID, StartedAt: start, UpdatedAt: start, Done: map[string]exactFamilyCount{}}
		if err := p.saveExactCensusState(st, len(names), true); err != nil {
			return nil, err
		}
	}

	run := &exactCensusRun{p: p, ctx: ctx, st: st, all: len(names), lastBeat: time.Now(), lastSave: time.Now()}
	if opts.ReadBytesPerSec > 0 {
		burst := opts.ReadBytesPerSec
		if burst < exactCensusCheckBytes*2 {
			burst = exactCensusCheckBytes * 2
		}
		run.lim = rate.NewLimiter(rate.Limit(opts.ReadBytesPerSec), int(burst))
	}
	run.report = func(fam string) {
		if opts.Progress != nil {
			opts.Progress(ExactCensusProgress{
				RunID: st.RunID, Registry: st.Registry, StartedAt: st.StartedAt, UpdatedAt: time.Now(),
				FamiliesDone: len(st.Done), FamiliesAll: len(names), BytesRead: st.BytesRead,
			}, fam)
		}
	}

	for _, fam := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, done := st.Done[fam]; done {
			continue
		}
		if err := run.countFamily(fam, pieces[fam]); err != nil {
			return nil, err
		}
		run.report(fam)
		run.lastBeat = time.Now()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out, err := p.assembleExactCensus(ctx, st, names, resumed, start)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode exact census: %w", err)
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte(exactCensusResultKey), data, nil); err != nil {
		return nil, errors.Join(fmt.Errorf("store exact census: %w", err), b.Close())
	}
	for _, k := range []string{exactCensusProgressKey, exactCensusSummaryKey} {
		if err := b.Delete([]byte(k), nil); err != nil {
			return nil, errors.Join(fmt.Errorf("clear exact census progress: %w", err), b.Close())
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, errors.Join(fmt.Errorf("store exact census: %w", err), b.Close())
	}
	if err := b.Close(); err != nil {
		return nil, fmt.Errorf("store exact census: %w", err)
	}
	return out, nil
}

// countFamily counts one family, resuming from the saved partial position.
func (r *exactCensusRun) countFamily(fam string, pieces []keyRange) error {
	st := r.st
	c := exactFamilyCount{}
	var fold *exactHistoryFold
	if fam == "book_ver:" {
		fold = newExactHistoryFold()
	}
	startPiece := 0
	var resumeAt []byte
	if pt := st.Partial; pt != nil && pt.Family == fam {
		c, startPiece, resumeAt = pt.Count, pt.Piece, pt.ResumeAt
		if fold != nil && pt.Fold != nil {
			fold = pt.Fold
			if fold.Dist == nil {
				fold.Dist = map[string]int64{}
			}
		}
	}
	for pi := startPiece; pi < len(pieces); pi++ {
		lo := pieces[pi].Lo
		if pi == startPiece && resumeAt != nil {
			lo = resumeAt
		}
		save := func(at []byte, force bool) error {
			if !force && time.Since(r.lastSave) < exactCensusIterRefresh {
				return nil
			}
			st.Partial = &exactPartial{Family: fam, Piece: pi, ResumeAt: at, Count: c, Fold: fold}
			st.UpdatedAt = time.Now()
			if err := r.p.saveExactCensusState(st, r.all, true); err != nil {
				return err
			}
			r.lastSave = time.Now()
			if exactCensusAfterSave != nil {
				exactCensusAfterSave(fam)
			}
			return nil
		}
		subs, err := r.p.exactSubranges(lo, pieces[pi].Hi)
		if err != nil {
			return err
		}
		for si, sub := range subs {
			if err := r.ctx.Err(); err != nil {
				return err
			}
			if err := r.countRange(fam, sub[0], sub[1], &c, fold, save); err != nil {
				return err
			}
			r.heartbeat(fam)
			if si+1 < len(subs) {
				if err := save(subs[si+1][0], false); err != nil {
					return err
				}
			}
		}
	}
	if c.Points < c.Live {
		c.Points = c.Live
	}
	st.Done[fam] = c
	st.Partial = nil
	if fold != nil {
		st.History = fold.finish(r.p.mem())
		st.HistoryMalformed = fold.Malformed
	}
	st.UpdatedAt = time.Now()
	if err := r.p.saveExactCensusState(st, r.all, c.Points > 0); err != nil {
		return err
	}
	r.lastSave = time.Now()
	return nil
}

// countRange counts [lo, hi) keys-only into c.
//
// The budget is charged on the iterator's own InternalStats KeyBytes +
// ValueBytes (tombstones and shadowed versions included), read every
// exactCensusStatsEvery live keys. Every exactCensusCheckBytes of that the
// pass waits for the budget, checks ctx and heartbeats; past
// exactCensusIterRefresh — or when the budget wait would exceed
// exactCensusLongWait — it steps to the next live key, closes the iterator,
// saves that key as the resume point, waits (iterator closed) and reopens at
// it. Resuming at the next live key, inclusive, means every version of the
// last key counted was stepped over by the closed iterator, so points and
// deletions are exact across refreshes. A long tombstone run between two
// checks is bounded by the sub-range the caller passed.
func (r *exactCensusRun) countRange(fam string, lo, hi []byte, c *exactFamilyCount, fold *exactHistoryFold,
	save func(at []byte, force bool) error,
) error {
	var m *MemStore
	if fold != nil {
		m = r.p.mem()
	}
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		it, err := r.p.db.NewIter(&pebble.IterOptions{LowerBound: lo, UpperBound: hi})
		if err != nil {
			return fmt.Errorf("exact census iterator: %w", err)
		}
		if err := r.ctx.Err(); err != nil {
			return errors.Join(err, it.Close())
		}
		opened := time.Now()
		var charged, waitAfterClose int64
		var resumeAt []byte
		var loopErr error
		stepOff := false // refresh decided: stop at the next live key
		n := 0
		for valid := it.First(); valid; valid = it.Next() {
			k := it.Key()
			if stepOff {
				resumeAt = append([]byte(nil), k...)
				break
			}
			c.Live++
			if fold != nil {
				fold.add(k, m)
			}
			n++
			if n%exactCensusStatsEvery != 0 {
				continue
			}
			ist := it.Stats().InternalStats
			used := int64(ist.KeyBytes + ist.ValueBytes)
			delta := used - charged
			if delta < exactCensusCheckBytes {
				continue
			}
			charged = used
			r.st.BytesRead += delta
			if censusWaitIsLong(r.lim, delta) {
				waitAfterClose = delta
				stepOff = true
				continue
			}
			if werr := censusWaitBytes(r.ctx, r.lim, delta, func() { r.heartbeat(fam) }); werr != nil {
				loopErr = werr
				break
			}
			if cerr := r.ctx.Err(); cerr != nil {
				loopErr = cerr
				break
			}
			r.heartbeat(fam)
			if time.Since(opened) >= exactCensusIterRefresh {
				stepOff = true
			}
		}
		ist := it.Stats().InternalStats
		if rest := int64(ist.KeyBytes+ist.ValueBytes) - charged; rest > 0 && loopErr == nil {
			r.st.BytesRead += rest
			waitAfterClose += rest
		}
		c.Points += int64(ist.PointCount)
		c.Covered += int64(ist.PointsCoveredByRangeTombstones)
		c.KeyBytes += int64(ist.KeyBytes)
		c.ValBytes += int64(ist.ValueBytes)
		if resumeAt != nil {
			// The key we stopped at was stepped onto but not counted; its
			// point belongs to the next iterator. (Its key and value bytes
			// are counted by both, so raw byte figures can run over by one
			// point per refresh.)
			c.Points--
		}
		iterErr := it.Error()
		closeErr := it.Close()
		if loopErr != nil {
			return loopErr
		}
		if iterErr != nil || closeErr != nil {
			return fmt.Errorf("exact census iterator: %w", errors.Join(iterErr, closeErr))
		}
		if resumeAt != nil {
			// Save the position before the wait, so a watchdog kill or
			// restart during a long wait loses nothing.
			if err := save(resumeAt, true); err != nil {
				return err
			}
		}
		if err := censusWaitBytes(r.ctx, r.lim, waitAfterClose, func() { r.heartbeat(fam) }); err != nil {
			return err
		}
		if resumeAt == nil {
			return nil
		}
		lo = resumeAt
	}
}

// exactSubranges cuts [lo, hi) into sub-ranges of at most
// exactCensusSubrangeBytes on disk by bisecting the key space with
// EstimateDiskUsage (metadata and index blocks only, no data reads). A nil
// lo/hi means the open end of the key space.
func (p *PebbleStore) exactSubranges(lo, hi []byte) ([][2][]byte, error) {
	var out [][2][]byte
	var rec func(a, b []byte, depth int) error
	rec = func(a, b []byte, depth int) error {
		ea, eb := a, b
		if ea == nil {
			ea = []byte{}
		}
		if eb == nil {
			eb = censusMaxKey
		}
		if depth < 40 && bytes.Compare(ea, eb) < 0 {
			du, err := p.db.EstimateDiskUsage(ea, eb)
			if err != nil {
				return fmt.Errorf("exact census: disk usage: %w", err)
			}
			if du > exactCensusSubrangeBytes {
				if mid := censusMidKey(ea, eb); mid != nil {
					if err := rec(a, mid, depth+1); err != nil {
						return err
					}
					return rec(mid, b, depth+1)
				}
			}
		}
		out = append(out, [2][]byte{a, b})
		return nil
	}
	if err := rec(lo, hi, 0); err != nil {
		return nil, err
	}
	return out, nil
}

// censusMidKey returns a key strictly between a and b (a < b), or nil when
// there is none at this length.
func censusMidKey(a, b []byte) []byte {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	n++
	pad := func(x []byte) []byte {
		out := make([]byte, n)
		copy(out, x)
		return out
	}
	A := new(big.Int).SetBytes(pad(a))
	B := new(big.Int).SetBytes(pad(b))
	M := new(big.Int).Add(A, B)
	M.Rsh(M, 1)
	if M.Cmp(A) <= 0 || M.Cmp(B) >= 0 {
		return nil
	}
	mid := M.FillBytes(make([]byte, n))
	if bytes.Compare(mid, a) <= 0 || bytes.Compare(mid, b) >= 0 {
		return nil
	}
	return mid
}

// censusWaitIsLong reports whether charging n bytes would block longer than
// exactCensusLongWait.
func censusWaitIsLong(lim *rate.Limiter, n int64) bool {
	if lim == nil {
		return false
	}
	short := float64(n) - lim.Tokens()
	return short > float64(lim.Limit())*exactCensusLongWait.Seconds()
}

// censusWaitBytes charges n bytes to the limiter in chunks of at most one
// heartbeat's worth (and never more than the burst), calling beat between
// chunks so the watchdog sees progress during a long wait. A wait the limiter
// refuses because it would pass the context deadline is reported as
// context.DeadlineExceeded.
func censusWaitBytes(ctx context.Context, lim *rate.Limiter, n int64, beat func()) error {
	if lim == nil {
		return nil
	}
	chunkMax := int64(float64(lim.Limit()) * exactCensusHeartbeat.Seconds())
	if b := int64(lim.Burst()); chunkMax > b || chunkMax < 1 {
		chunkMax = b
	}
	for n > 0 {
		chunk := chunkMax
		if chunk > n {
			chunk = n
		}
		if err := lim.WaitN(ctx, int(chunk)); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if _, ok := ctx.Deadline(); ok {
				return fmt.Errorf("%w: %v", context.DeadlineExceeded, err)
			}
			return err
		}
		n -= chunk
		if beat != nil {
			beat()
		}
	}
	return nil
}

// assembleExactCensus turns the per-family counts into a DBCensus. Disk bytes
// and table counts come from an estimated census taken now.
func (p *PebbleStore) assembleExactCensus(ctx context.Context, st *exactCensusState, names []string, resumed bool, start time.Time) (*DBCensus, error) {
	// Flush so the disk-byte split covers what the pass counted.
	if err := p.db.Flush(); err != nil {
		return nil, fmt.Errorf("exact census: flush: %w", err)
	}
	est := &DBCensus{}
	if err := p.censusFamilies(ctx, est); err != nil {
		return nil, err
	}
	diskByFam := map[string]FamilyCensus{}
	for _, f := range est.Families {
		diskByFam[f.Prefix] = f
	}
	meta := map[string]KeyFamily{}
	for _, f := range keyFamilies {
		meta[f.Prefix] = f
	}
	meta[unregisteredFamily] = KeyFamily{Prefix: unregisteredFamily, Description: "keys outside every registered family"}

	out := &DBCensus{
		Kind:               CensusKindExact,
		GeneratedAt:        time.Now(),
		DurationMS:         time.Since(st.StartedAt).Milliseconds(),
		TotalTables:        est.TotalTables,
		TotalTableBytes:    est.TotalTableBytes,
		DiskSpaceUsage:     est.DiskSpaceUsage,
		BytesRead:          st.BytesRead,
		History:            st.History,
		FamilyFiguresBasis: CensusFamilyFiguresBasis,
		Notes:              []string{censusNoteExactDefinition, censusNoteHiddenSystem},
	}
	if resumed {
		out.Notes = append(out.Notes, fmt.Sprintf("resumed a run started %s; this call began %s", st.StartedAt.Format(time.RFC3339), start.Format(time.RFC3339)))
	}
	if st.HistoryMalformed > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d book_ver: keys had no <id>:<nanos> shape and were left out of the history", st.HistoryMalformed))
	}
	for _, name := range names {
		c := st.Done[name]
		m := meta[name]
		fc := FamilyCensus{Prefix: name, Description: m.Description, Owner: m.Owner, Method: CensusMethodEmpty}
		if c.Points > 0 {
			fc.Method = CensusMethodExact
			fc.Keys = c.Live
			fc.Deletions = c.Points - c.Live
			fc.RangeDeletedKeys = c.Covered
			fc.Entries = c.Points
			fc.RawKeyBytes = c.KeyBytes
			fc.RawValueBytes = c.ValBytes
		}
		d := diskByFam[name]
		fc.DiskBytes, fc.Tables, fc.rangeDel = d.DiskBytes, d.Tables, d.rangeDel
		out.Families = append(out.Families, fc)
		// Totals are the sums of the exact figures, not the estimate's.
		out.TotalKeys += fc.Keys
		out.TotalDeletions += fc.Deletions
		out.TotalEntries += fc.Entries
	}
	if note := censusRangeDelNote(out.Families); note != "" {
		out.Notes = append(out.Notes, note)
	}
	p.censusMemdb(out)
	return out, nil
}
