// file: internal/database/signal_coverage_fpwin.go
// version: 1.1.0
// guid: 5ab03e39-dbc3-43e9-8008-dd58c0f20f7d
// last-edited: 2026-09-19

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"
)

// Windowed-fingerprint coverage: a census of which PRESENT book_file rows hold
// windowed prints in the fpwin: sidecar (design PR 5,
// .claude/notes/windowed-fingerprint-design-2026-09-12.md).
//
// Census, not sample: every book_file row is visited once and every fpwin: /
// fpwin_fail: key is visited once. The window keyspace is streamed straight off
// Pebble and never enters memdb (its keys sit outside the book_file:/book:
// ranges the warmup loads).
//
// Two depths, mirroring GetBookFileSignalCoverage:
//   - fast (deep=false): KEYS ONLY. Ref, kind and slot are all in the key, so
//     "has a window" / "has a tombstone" / "none" is exact without touching a
//     value. Whether a window is CURRENT lives in the value and is listed
//     under Unavailable.
//   - deep (deep=true): values of window/whole rows are decoded (pipeline,
//     window set, tool versions) across a bounded pool, and holders of the
//     single deep-coverage slot are serialized with the book_file deep scan.

// Tier names in FingerprintWindowCoverage.Tiers. They follow the design's
// backfill priority tiers (section e); T0 needs the recover-missing-files
// report and is listed as unavailable.
const (
	WindowTierT1 = "t1_books_with_missing_rows"
	WindowTierT2 = "t2_other_present"
)

// WindowCoverageCriteria is what "current" means for a stored window. The
// caller supplies it (the server takes it from internal/fingerprint), so the
// store keeps no dependency on the package that computes prints.
//
// Pipeline and WindowSet are required on the deep path. An empty tool version
// is NOT compared: currency then falls back to pipeline + window set and the
// response says so, rather than calling every window stale.
type WindowCoverageCriteria struct {
	Pipeline      string `json:"pipeline"`
	WindowSet     string `json:"window_set"`
	FpcalcVersion string `json:"fpcalc_version,omitempty"`
	FFmpegVersion string `json:"ffmpeg_version,omitempty"`
}

// WindowCoverageBuckets partitions present files. On the deep path
// WithCurrentWindows + WithStaleWindowsOnly + TombstonedNoWindows + NoWindows
// == PresentFiles, by a fixed priority ladder: current > stale > tombstone >
// none. A file with a tombstone AND stored windows counts as windowed; the
// overlap is reported separately in WithTombstoneAny. On the fast path the two
// currency buckets stay zero and WithWindows takes their place.
//
// A "window" is a stored row of kind window or whole. A stored kind=head row is
// the intro again and does not count; the virtual legacy head never appears
// here because only stored keys are read.
type WindowCoverageBuckets struct {
	PresentFiles         int64 `json:"present_files"`
	WithCurrentWindows   int64 `json:"with_current_windows"`
	WithStaleWindowsOnly int64 `json:"with_stale_windows_only"`
	WithWindows          int64 `json:"with_windows"`
	TombstonedNoWindows  int64 `json:"tombstoned_no_windows"`
	NoWindows            int64 `json:"no_windows"`
}

// FingerprintWindowCoverage is the windows section of GET
// /api/v1/signals/coverage?windows=true.
type FingerprintWindowCoverage struct {
	// Source is where the book_file rows came from: "memdb" or "pebble".
	Source            string                  `json:"source"`
	CurrencyEvaluated bool                    `json:"currency_evaluated"`
	Criteria          *WindowCoverageCriteria `json:"criteria,omitempty"`

	WindowCoverageBuckets
	WithTombstoneAny  int64                            `json:"with_tombstone_any"`
	WithStoredHeadRow int64                            `json:"with_stored_head_row"`
	Tiers             map[string]WindowCoverageBuckets `json:"tiers"`

	// Refs that are not present book_file rows.
	MissingRowsWithWindows int64 `json:"missing_rows_with_windows"`
	OrphanFileRefs         int64 `json:"orphan_file_refs"`
	OrphanTombstones       int64 `json:"orphan_tombstones"`
	PathRefsWithWindows    int64 `json:"path_refs_with_windows"`
	PathRefTombstones      int64 `json:"path_ref_tombstones"`

	// Row-level totals over the whole fpwin: keyspace (all refs).
	StoredWindowRows  int64            `json:"stored_window_rows"`
	StoredRowsByKind  map[string]int64 `json:"stored_rows_by_kind"`
	WindowRowsByTools map[string]int64 `json:"window_rows_by_tools,omitempty"`

	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// Per-file flags gathered from the fpwin keyspaces.
const (
	fwWindow  uint8 = 1 << iota // ≥1 stored kind=window|whole row
	fwCurrent                   // ≥1 of those matches the criteria (deep only)
	fwHead                      // a stored kind=head row
	fwTomb                      // an fpwin_fail: tombstone
)

// fpwinCensus is the fpwin keyspace folded to one flag byte per file ID. It is
// built before the row pass and read-only during it, so the sharded row pass
// needs no lock.
type fpwinCensus struct {
	files       map[string]uint8
	pathWindows int64
	pathTombs   int64
	rows        int64
	byKind      map[string]int64
	byTools     map[string]int64
}

// windowValue is the slim decode target for a window row's currency fields.
// Raw is decoded as presence only, so the print bytes are never kept.
type windowValue struct {
	WindowSet     string   `json:"window_set"`
	Pipeline      string   `json:"pipeline"`
	FpcalcVersion string   `json:"fpcalc_version"`
	FFmpegVersion string   `json:"ffmpeg_version"`
	Raw           presence `json:"raw"`
}

// parseFpwinKey splits "fpwin:<t>:<id>:<kind>:<slot>" into the ref type
// ('f' or 'p'), the id and the kind. ok is false for a key of another shape.
func parseFpwinKey(key []byte) (refType byte, id, kind string, ok bool) {
	s, found := strings.CutPrefix(string(key), fpwinKeyPrefix)
	if !found || len(s) < 3 || s[1] != ':' {
		return 0, "", "", false
	}
	parts := strings.Split(s[2:], ":")
	if len(parts) != 3 {
		return 0, "", "", false
	}
	return s[0], parts[0], parts[1], true
}

type fpwinJob struct {
	fileID string // "" for a p: ref
	val    []byte
}

// scanFpwinKeyspace streams every fpwin: key once. The key walk is one
// iterator on this goroutine (key parsing only); with deep set, the values of
// window/whole rows go to a bounded pool of decoders, each with a private
// result set merged after the pool drains.
func (p *PebbleStore) scanFpwinKeyspace(ctx context.Context, deep bool, crit WindowCoverageCriteria, workers int) (*fpwinCensus, error) {
	c := &fpwinCensus{
		files:  make(map[string]uint8),
		byKind: map[string]int64{string(WindowKindHead): 0, string(WindowKindWindow): 0, string(WindowKindWhole): 0},
	}
	type workerOut struct {
		current map[string]struct{}
		tools   map[string]int64
	}
	outs := make([]workerOut, workers)
	const batchSize = 256
	jobs := make(chan []fpwinJob, 1)

	g, gctx := errgroup.WithContext(ctx)
	if deep {
		c.byTools = make(map[string]int64)
		for w := range workers {
			out := &outs[w]
			out.current = make(map[string]struct{})
			out.tools = make(map[string]int64)
			g.Go(func() error {
				for batch := range jobs {
					for _, j := range batch {
						var v windowValue
						if err := json.Unmarshal(j.val, &v); err != nil {
							return fmt.Errorf("decode fingerprint window of %q: %w", j.fileID, err)
						}
						out.tools["fpcalc "+v.FpcalcVersion+" / ffmpeg "+v.FFmpegVersion]++
						if j.fileID != "" && v.matches(crit) {
							out.current[j.fileID] = struct{}{}
						}
					}
				}
				return nil
			})
		}
	}

	g.Go(func() error {
		defer close(jobs)
		iter, err := p.db.NewIter(&pebble.IterOptions{
			LowerBound: []byte(fpwinKeyPrefix),
			UpperBound: prefixEnd([]byte(fpwinKeyPrefix)),
		})
		if err != nil {
			return err
		}
		defer iter.Close()
		var batch []fpwinJob
		lastPath := ""
		n := 0
		for iter.First(); iter.Valid(); iter.Next() {
			if n++; n%4096 == 0 && gctx.Err() != nil {
				return gctx.Err()
			}
			refType, id, kind, ok := parseFpwinKey(iter.Key())
			if !ok {
				continue
			}
			c.rows++
			c.byKind[kind]++
			isWindow := kind == string(WindowKindWindow) || kind == string(WindowKindWhole)
			switch refType {
			case 'f':
				if isWindow {
					c.files[id] |= fwWindow
				} else if kind == string(WindowKindHead) {
					c.files[id] |= fwHead
				}
			case 'p':
				// Keys of one ref are contiguous, so a change of id is a new ref.
				if isWindow && id != lastPath {
					c.pathWindows++
					lastPath = id
				}
			}
			if !deep || !isWindow {
				continue
			}
			fileID := ""
			if refType == 'f' {
				fileID = id
			}
			batch = append(batch, fpwinJob{fileID: fileID, val: append([]byte(nil), iter.Value()...)})
			if len(batch) == batchSize {
				select {
				case jobs <- batch:
				case <-gctx.Done():
					return gctx.Err()
				}
				batch = nil
			}
		}
		if len(batch) > 0 {
			select {
			case jobs <- batch:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return iter.Error()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for i := range outs {
		for id := range outs[i].current {
			c.files[id] |= fwCurrent
		}
		for k, v := range outs[i].tools {
			c.byTools[k] += v
		}
	}

	// Tombstones sort OUTSIDE ["fpwin:", "fpwin;"): a second, keys-only range.
	err := forEachKeyInRange(p.db, []byte(fpwinFailKeyPrefix), prefixEnd([]byte(fpwinFailKeyPrefix)), func(k, _ []byte) error {
		ref := strings.TrimPrefix(string(k), fpwinFailKeyPrefix)
		switch {
		case strings.HasPrefix(ref, "f:"):
			c.files[ref[2:]] |= fwTomb
		case strings.HasPrefix(ref, "p:"):
			c.pathTombs++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (v *windowValue) matches(crit WindowCoverageCriteria) bool {
	if !bool(v.Raw) || v.Pipeline != crit.Pipeline || v.WindowSet != crit.WindowSet {
		return false
	}
	if crit.FpcalcVersion != "" && v.FpcalcVersion != crit.FpcalcVersion {
		return false
	}
	if crit.FFmpegVersion != "" && v.FFmpegVersion != crit.FFmpegVersion {
		return false
	}
	return true
}

// fpwinCovAcc is one shard's private tally.
type fpwinCovAcc struct {
	all, t1, t2    WindowCoverageBuckets
	tombAny, head  int64
	missingWithWin int64
	matchedRefs    int64 // rows (present or missing) whose ID holds stored window rows
	matchedTombs   int64 // rows whose ID holds a tombstone
}

func (b *WindowCoverageBuckets) add(flags uint8, exact bool) {
	b.PresentFiles++
	switch {
	case exact && flags&fwCurrent != 0:
		b.WithCurrentWindows++
	case flags&fwWindow != 0:
		if exact {
			b.WithStaleWindowsOnly++
		}
	case flags&fwTomb != 0:
		b.TombstonedNoWindows++
	default:
		b.NoWindows++
	}
	if flags&fwWindow != 0 {
		b.WithWindows++
	}
}

func (b *WindowCoverageBuckets) merge(o WindowCoverageBuckets) {
	b.PresentFiles += o.PresentFiles
	b.WithCurrentWindows += o.WithCurrentWindows
	b.WithStaleWindowsOnly += o.WithStaleWindowsOnly
	b.WithWindows += o.WithWindows
	b.TombstonedNoWindows += o.TombstonedNoWindows
	b.NoWindows += o.NoWindows
}

// countWindowCoverage folds n book_file rows (fetched by index through at)
// against the census. The pass that finds books with a missing row is a
// sequential map insert per missing row; the classification pass runs on
// contiguous shards with private accumulators, reading census.files only.
func countWindowCoverage(ctx context.Context, n int, at func(i int) *BookFile, census *fpwinCensus, exact bool, workers int) (*FingerprintWindowCoverage, error) {
	missingBooks := make(map[string]struct{})
	for i := range n {
		if f := at(i); f != nil && f.Missing {
			missingBooks[f.BookID] = struct{}{}
		}
	}
	shards := max(min(workers*4, n), 1)
	accs := make([]fpwinCovAcc, shards)
	var g errgroup.Group
	g.SetLimit(workers)
	per := (n + shards - 1) / shards
	for s := range shards {
		lo, hi := s*per, min((s+1)*per, n)
		acc := &accs[s]
		g.Go(func() error {
			for i := lo; i < hi; i++ {
				if i%4096 == 0 && ctx.Err() != nil {
					return ctx.Err()
				}
				f := at(i)
				if f == nil {
					continue
				}
				flags := census.files[f.ID]
				if flags&(fwWindow|fwHead) != 0 {
					acc.matchedRefs++
				}
				if flags&fwTomb != 0 {
					acc.matchedTombs++
				}
				if f.Missing {
					if flags&fwWindow != 0 {
						acc.missingWithWin++
					}
					continue
				}
				acc.all.add(flags, exact)
				if _, ok := missingBooks[f.BookID]; ok {
					acc.t1.add(flags, exact)
				} else {
					acc.t2.add(flags, exact)
				}
				if flags&fwTomb != 0 {
					acc.tombAny++
				}
				if flags&fwHead != 0 {
					acc.head++
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	out := &FingerprintWindowCoverage{
		CurrencyEvaluated:   exact,
		Tiers:               map[string]WindowCoverageBuckets{},
		PathRefsWithWindows: census.pathWindows,
		PathRefTombstones:   census.pathTombs,
		StoredWindowRows:    census.rows,
		StoredRowsByKind:    census.byKind,
		WindowRowsByTools:   census.byTools,
	}
	var t1, t2 WindowCoverageBuckets
	var matchedRefs, matchedTombs int64
	for i := range accs {
		a := &accs[i]
		out.WindowCoverageBuckets.merge(a.all)
		t1.merge(a.t1)
		t2.merge(a.t2)
		out.WithTombstoneAny += a.tombAny
		out.WithStoredHeadRow += a.head
		out.MissingRowsWithWindows += a.missingWithWin
		matchedRefs += a.matchedRefs
		matchedTombs += a.matchedTombs
	}
	out.Tiers[WindowTierT1] = t1
	out.Tiers[WindowTierT2] = t2
	var refs, tombs int64
	for _, fl := range census.files {
		if fl&(fwWindow|fwHead) != 0 {
			refs++
		}
		if fl&fwTomb != 0 {
			tombs++
		}
	}
	out.OrphanFileRefs = refs - matchedRefs
	out.OrphanTombstones = tombs - matchedTombs
	return out, nil
}

// GetFingerprintWindowCoverage reports windowed-fingerprint coverage of every
// present book_file row. See the file comment for the fast/deep split.
//
// Row source follows GetBookFileSignalCoverage so the two sections of one
// response count the same rows: the fast path reads memdb pointers (and
// refuses with ErrMemDBNotReady when memdb is not serving — it must stay
// cheap), the deep path always reads the rows from Pebble, exactly as the deep
// book_file scan does. Mixing them would give "windowed / present" two
// different denominators in one report.
func (p *PebbleStore) GetFingerprintWindowCoverage(ctx context.Context, deep bool, crit WindowCoverageCriteria, workers int) (*FingerprintWindowCoverage, error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	if deep {
		if crit.Pipeline == "" || crit.WindowSet == "" {
			return nil, errors.New("window coverage: deep currency needs a pipeline and a window set")
		}
		if !p.TryAcquireDeepCoverageScan() {
			return nil, ErrDeepCoverageBusy
		}
		defer p.ReleaseDeepCoverageScan()
	}

	var (
		n      int
		at     func(i int) *BookFile
		source string
	)
	if mem := p.mem(); !deep && p.UseMemDB && mem != nil {
		ptrs, err := mem.bookFilePointers()
		if err != nil {
			return nil, err
		}
		n, at, source = len(ptrs), func(i int) *BookFile { return ptrs[i] }, "memdb"
	} else if !deep {
		return nil, fmt.Errorf("%w: memdb is not serving reads; retry later, or pass deep=true for a Pebble scan", ErrMemDBNotReady)
	} else {
		rows, err := p.slimBookFileRows(ctx, workers)
		if err != nil {
			return nil, err
		}
		n, at, source = len(rows), func(i int) *BookFile { return &rows[i] }, "pebble"
	}

	census, err := p.scanFpwinKeyspace(ctx, deep, crit, workers)
	if err != nil {
		return nil, err
	}
	out, err := countWindowCoverage(ctx, n, at, census, deep, workers)
	if err != nil {
		return nil, err
	}
	out.Source = source
	out.Unavailable = map[string]string{
		"tier_t0":          "T0 (recover-missing-files ambiguous/size-collision candidates) needs that op's report, which this endpoint does not read",
		"source_staleness": "a window is also stale when the file's size or mtime changed; that needs a live stat and this endpoint never stats files",
	}
	if deep {
		c := crit
		out.Criteria = &c
		if crit.FpcalcVersion == "" || crit.FFmpegVersion == "" {
			out.Unavailable["tool_version_currency"] = "current tool versions are unknown, so currency compares pipeline and window set only"
		}
	} else {
		for _, k := range []string{"with_current_windows", "with_stale_windows_only", "window_rows_by_tools"} {
			out.Unavailable[k] = "window currency is in the row values; the fast path reads keys only, pass deep=true"
		}
	}
	return out, nil
}

// slimBookFileRows reads ID, BookID and Missing of every primary book_file row
// from Pebble: IDs from the key, Missing from a one-field decode spread across
// a bounded pool (values carry raw prints, so the decode is the cost).
func (p *PebbleStore) slimBookFileRows(ctx context.Context, workers int) ([]BookFile, error) {
	type item struct {
		bookID, fileID string
		val            []byte
	}
	const batchSize = 128
	batches := make(chan []item, 1)
	outs := make([][]BookFile, workers)
	g, gctx := errgroup.WithContext(ctx)
	for w := range workers {
		g.Go(func() error {
			for batch := range batches {
				for _, it := range batch {
					var row struct {
						Missing bool `json:"missing"`
					}
					if err := json.Unmarshal(it.val, &row); err != nil {
						return fmt.Errorf("decode book_file %s: %w", it.fileID, err)
					}
					outs[w] = append(outs[w], BookFile{ID: it.fileID, BookID: it.bookID, Missing: row.Missing})
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(batches)
		iter, err := p.db.NewIter(&pebble.IterOptions{
			LowerBound: []byte("book_file:"),
			UpperBound: []byte("book_file;"),
		})
		if err != nil {
			return err
		}
		defer iter.Close()
		var batch []item
		for iter.First(); iter.Valid(); iter.Next() {
			parts := strings.Split(string(iter.Key()), ":")
			if len(parts) != 3 {
				continue
			}
			batch = append(batch, item{bookID: parts[1], fileID: parts[2], val: append([]byte(nil), iter.Value()...)})
			if len(batch) == batchSize {
				select {
				case batches <- batch:
				case <-gctx.Done():
					return gctx.Err()
				}
				batch = nil
			}
		}
		if len(batch) > 0 {
			select {
			case batches <- batch:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return iter.Error()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var rows []BookFile
	for _, o := range outs {
		rows = append(rows, o...)
	}
	return rows, nil
}
