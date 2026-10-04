// file: internal/database/census_exact.go
// version: 1.0.0
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
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/time/rate"
)

// Store keys of the exact census. Both sit under the registered
// "system:census:" family.
const (
	exactCensusResultKey   = "system:census:exact"
	exactCensusProgressKey = "system:census:exact_progress"
)

const (
	// exactCensusIterRefresh bounds how long one iterator (and so one pinned
	// Pebble version) lives: past it the pass reopens after the last key, so
	// obsolete sstables can be deleted while a large family is being read.
	exactCensusIterRefresh = 30 * time.Second
	// exactCensusCheckBytes is how many key+value bytes the pass reads between
	// rate-limit, context and refresh checks.
	exactCensusCheckBytes = 256 << 10
	// exactCensusProgressMaxAge: older saved progress is discarded instead of
	// resumed, so a run abandoned long ago does not mix with today's data.
	exactCensusProgressMaxAge = 7 * 24 * time.Hour

	censusNoteExactDefinition = "exact census: every family counted by a keys-only pass; keys = live keys when the family was read; deletions = point tombstones plus shadowed versions not yet compacted (points under an uncompacted range deletion are mostly skipped unseen, see range_deleted_keys and the range-deletion note); families were read one after another, so the census is not a single point-in-time snapshot; disk bytes and table counts are apportioned from sstable properties at the end of the run"
)

// ExactCensusOptions configures RunExactCensus.
type ExactCensusOptions struct {
	// ReadBytesPerSec caps the key+value bytes the pass reads per second;
	// <= 0 means unlimited (tests only — production always sets a budget).
	ReadBytesPerSec int64
	// Progress, when set, is called after every family and at least every
	// few seconds inside a large one.
	Progress func(p ExactCensusProgress, family string)
}

// DBCensusExactRunner is the capability the maintenance.db-census-exact op
// resolves.
type DBCensusExactRunner interface {
	RunExactCensus(ctx context.Context, opts ExactCensusOptions) (*DBCensus, error)
	LastExactCensus() (*DBCensus, error)
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
// far, so a restarted run resumes after the last one.
type exactCensusState struct {
	Registry  string                      `json:"registry"`
	StartedAt time.Time                   `json:"started_at"`
	UpdatedAt time.Time                   `json:"updated_at"`
	Done      map[string]exactFamilyCount `json:"done"`
	BytesRead int64                       `json:"bytes_read"`
	History   *HistoryCensus              `json:"history,omitempty"`
}

// censusRegistryFingerprint identifies the family registry, so progress saved
// under a different registry is not resumed.
func censusRegistryFingerprint() string {
	h := sha256.New()
	for _, f := range KeyFamilies() {
		h.Write([]byte(f.Prefix))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LastExactCensus returns the last completed exact census, or nil when none
// has been written.
func (p *PebbleStore) LastExactCensus() (_ *DBCensus, err error) {
	defer recoverPebbleClosed("LastExactCensus", &err)
	raw, closer, err := p.db.Get([]byte(exactCensusResultKey))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read exact census: %w", err)
	}
	defer func() {
		if cerr := closer.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("read exact census: %w", cerr)
		}
	}()
	var c DBCensus
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("decode exact census: %w", err)
	}
	return &c, nil
}

// exactCensusProgress returns the progress of an unfinished run, or nil.
func (p *PebbleStore) exactCensusProgress() (*ExactCensusProgress, error) {
	st, err := p.loadExactCensusState()
	if err != nil || st == nil {
		return nil, err
	}
	return &ExactCensusProgress{
		StartedAt: st.StartedAt, UpdatedAt: st.UpdatedAt,
		FamiliesDone: len(st.Done), FamiliesAll: len(KeyFamilies()) + 1, BytesRead: st.BytesRead,
	}, nil
}

func (p *PebbleStore) loadExactCensusState() (_ *exactCensusState, err error) {
	defer recoverPebbleClosed("exactCensusProgress", &err)
	raw, closer, err := p.db.Get([]byte(exactCensusProgressKey))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read exact census progress: %w", err)
	}
	defer func() {
		if cerr := closer.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("read exact census progress: %w", cerr)
		}
	}()
	var st exactCensusState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decode exact census progress: %w", err)
	}
	return &st, nil
}

// saveExactCensusState stores the progress. sync is false after a family
// that read nothing: losing that record costs one more empty seek on resume,
// not worth an fsync for each of the ~150 empty families.
func (p *PebbleStore) saveExactCensusState(st *exactCensusState, sync bool) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode exact census progress: %w", err)
	}
	opt := pebble.NoSync
	if sync {
		opt = pebble.Sync
	}
	if err := p.db.Set([]byte(exactCensusProgressKey), data, opt); err != nil {
		return fmt.Errorf("save exact census progress: %w", err)
	}
	return nil
}

// RunExactCensus counts every key family exactly with a rate-limited
// keys-only pass, family by family, and stores the result for the db-census
// endpoint (LastExactCensus). It also builds the book_ver: history
// distribution while reading that family.
//
// It resumes: progress is saved after every family, and a later call with the
// same registry continues after the last finished family (progress older than
// a week is discarded). Values are not decoded, but Pebble reads them with
// their blocks, so the budget counts key and value bytes.
//
// The families are read one after another: each family is internally
// consistent, but the census as a whole is not one point-in-time snapshot.
func (p *PebbleStore) RunExactCensus(ctx context.Context, opts ExactCensusOptions) (_ *DBCensus, err error) {
	defer recoverPebbleClosed("RunExactCensus", &err)
	start := time.Now()
	if err := p.db.Flush(); err != nil {
		return nil, fmt.Errorf("exact census: flush: %w", err)
	}

	reg := censusRegistryFingerprint()
	st, err := p.loadExactCensusState()
	if err != nil {
		return nil, err
	}
	resumed := st != nil && st.Registry == reg && time.Since(st.StartedAt) < exactCensusProgressMaxAge
	if !resumed {
		st = &exactCensusState{Registry: reg, StartedAt: start, Done: map[string]exactFamilyCount{}}
	}

	var lim *rate.Limiter
	if opts.ReadBytesPerSec > 0 {
		burst := int(opts.ReadBytesPerSec)
		if burst < exactCensusCheckBytes*2 {
			burst = exactCensusCheckBytes * 2
		}
		lim = rate.NewLimiter(rate.Limit(opts.ReadBytesPerSec), burst)
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

	report := func(fam string) {
		if opts.Progress != nil {
			opts.Progress(ExactCensusProgress{
				StartedAt: st.StartedAt, UpdatedAt: time.Now(),
				FamiliesDone: len(st.Done), FamiliesAll: len(names), BytesRead: st.BytesRead,
			}, fam)
		}
	}

	for _, fam := range names {
		if _, done := st.Done[fam]; done {
			continue
		}
		var hist map[string]int64
		if fam == "book_ver:" {
			hist = map[string]int64{}
		}
		c := exactFamilyCount{}
		for _, r := range pieces[fam] {
			if err := p.exactCountRange(ctx, r, lim, &c, hist, &st.BytesRead, func() { report(fam) }); err != nil {
				return nil, err
			}
		}
		st.Done[fam] = c
		if hist != nil {
			st.History = p.historyFromCounts(hist)
		}
		st.UpdatedAt = time.Now()
		if err := p.saveExactCensusState(st, c.Points > 0); err != nil {
			return nil, err
		}
		report(fam)
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
	if err := b.Delete([]byte(exactCensusProgressKey), nil); err != nil {
		return nil, errors.Join(fmt.Errorf("clear exact census progress: %w", err), b.Close())
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, errors.Join(fmt.Errorf("store exact census: %w", err), b.Close())
	}
	if err := b.Close(); err != nil {
		return nil, fmt.Errorf("store exact census: %w", err)
	}
	return out, nil
}

// exactCountRange counts one range keys-only into c. The iterator is reopened
// after the last key every exactCensusIterRefresh, and the byte budget,
// context and progress are checked every exactCensusCheckBytes.
func (p *PebbleStore) exactCountRange(
	ctx context.Context, r keyRange, lim *rate.Limiter, c *exactFamilyCount,
	hist map[string]int64, bytesRead *int64, progress func(),
) (err error) {
	lower := r.Lo
	for {
		it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: r.Hi})
		if err != nil {
			return fmt.Errorf("exact census iterator: %w", err)
		}
		opened := time.Now()
		lastProgress := opened
		var pending int64
		var resumeAt []byte
		var loopErr error
		for valid := it.First(); valid; valid = it.Next() {
			k := it.Key()
			lv := it.LazyValue()
			pending += int64(len(k) + lv.Len())
			c.Live++
			if hist != nil {
				rest := k[len("book_ver:"):]
				if i := bytes.LastIndexByte(rest, ':'); i > 0 {
					hist[string(rest[:i])]++
				}
			}
			if pending < exactCensusCheckBytes {
				continue
			}
			*bytesRead += pending
			if werr := censusWaitBytes(ctx, lim, pending); werr != nil {
				loopErr = werr
				break
			}
			pending = 0
			if cerr := ctx.Err(); cerr != nil {
				loopErr = cerr
				break
			}
			if time.Since(lastProgress) > 5*time.Second {
				progress()
				lastProgress = time.Now()
			}
			if time.Since(opened) > exactCensusIterRefresh {
				resumeAt = append(append([]byte(nil), k...), 0x00)
				break
			}
		}
		*bytesRead += pending
		stats := it.Stats().InternalStats
		c.Points += int64(stats.PointCount)
		c.Covered += int64(stats.PointsCoveredByRangeTombstones)
		c.KeyBytes += int64(stats.KeyBytes)
		c.ValBytes += int64(stats.ValueBytes)
		iterErr := it.Error()
		closeErr := it.Close()
		if loopErr != nil {
			return loopErr
		}
		if iterErr != nil || closeErr != nil {
			return fmt.Errorf("exact census iterator: %w", errors.Join(iterErr, closeErr))
		}
		if resumeAt == nil {
			if c.Points < c.Live {
				c.Points = c.Live
			}
			return nil
		}
		lower = resumeAt
	}
}

// censusWaitBytes charges n bytes to the limiter, in burst-sized chunks so a
// single large value can never exceed the limiter's burst.
func censusWaitBytes(ctx context.Context, lim *rate.Limiter, n int64) error {
	if lim == nil {
		return nil
	}
	for n > 0 {
		chunk := int64(lim.Burst())
		if chunk > n {
			chunk = n
		}
		if err := lim.WaitN(ctx, int(chunk)); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// assembleExactCensus turns the per-family counts into a DBCensus. Disk bytes
// and table counts come from an estimated census taken now.
func (p *PebbleStore) assembleExactCensus(ctx context.Context, st *exactCensusState, names []string, resumed bool, start time.Time) (*DBCensus, error) {
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
		TotalKeys:          est.TotalKeys,
		TotalDeletions:     est.TotalDeletions,
		TotalEntries:       est.TotalEntries,
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
	}
	if note := censusRangeDelNote(out.Families); note != "" {
		out.Notes = append(out.Notes, note)
	}
	p.censusMemdb(out)
	return out, nil
}

// historyFromCounts builds the distribution and the memdb orphan counts.
func (p *PebbleStore) historyFromCounts(counts map[string]int64) *HistoryCensus {
	h := buildHistoryCensus(counts)
	if m := p.mem(); m != nil {
		if ob, oe, err := m.historyOrphans(counts); err == nil {
			h.OrphanBooks, h.OrphanEntries, h.OrphansKnown = ob, oe, true
		}
	}
	return h
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
