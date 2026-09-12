// file: internal/database/signal_coverage.go
// version: 1.1.0
// guid: 3933e507-6dbe-4a9c-be3f-fc3512d67d44
// last-edited: 2026-09-12

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"
)

// Signal names reported by GET /api/v1/signals/coverage. Stable JSON keys: the
// coverage endpoint is an instrument that later backfill decisions are sized
// from, so renaming one silently breaks every saved comparison.
const (
	SignalFileHash         = "file_hash"
	SignalOriginalFileHash = "original_file_hash"
	// SignalFingerprintDuration is AcoustIDFingerprintDurationSec > 0. It is
	// the memdb-safe PROXY for "has a raw fpcalc print": the only writer of the
	// duration (acoustid doFingerprintFile) writes it together with the raw
	// bytes. It undercounts raw prints written before the duration field
	// existed (the rows acoustid.duration-backfill targets). Pass deep=true for
	// the exact raw count.
	SignalFingerprintDuration = "fingerprint_duration_proxy"
	SignalDuration            = "duration"
	SignalRawTags             = "raw_tags"

	// Deep-only signals: these fields are stripped from memdb rows
	// (stripBookFileForMemdb), so only a Pebble scan can count them.
	SignalRawFingerprint = "raw_fingerprint"
	// SignalSegOnlyFingerprint is Seg0 present with no raw print — the legacy
	// rows the pre-2026-09-12 backfill eligibility check skipped forever.
	SignalSegOnlyFingerprint = "seg_only_fingerprint"
)

// signalIndex orders every counted signal so the hot loop indexes a fixed
// array instead of hashing map keys 742k times per signal.
var signalNames = []string{
	SignalFileHash,
	SignalOriginalFileHash,
	SignalFingerprintDuration,
	SignalDuration,
	SignalRawTags,
	SignalRawFingerprint,
	SignalSegOnlyFingerprint,
	"acoustid_seg0", "acoustid_seg1", "acoustid_seg2", "acoustid_seg3",
	"acoustid_seg4", "acoustid_seg5", "acoustid_seg6",
}

const (
	sigFileHash = iota
	sigOrigHash
	sigFPDuration
	sigDuration
	sigRawTags
	sigRawFP // first deep-only index
	sigSegOnly
	sigSeg0    // sigSeg0..sigSeg0+6
	numSignals = sigSeg0 + 7
)

// ErrMemDBNotReady is returned by the fast coverage path when the memdb layer
// is not serving reads: memdb is disabled on the store, or it has not been
// published (warmup still running, or warmup failed and reads fell back to
// Pebble). The returned error wraps it with whichever of those is true. The
// caller should retry or ask for the deep scan explicitly — the fast path must
// never silently turn a sub-second request into a multi-minute Pebble scan.
var ErrMemDBNotReady = errors.New("memdb fast path unavailable")

// ErrDeepCoverageBusy is returned when a deep (Pebble) coverage scan is
// requested while another is still running on the same store. One deep scan
// reads every book_file row; two at once only double the I/O and memory for
// the same answer.
var ErrDeepCoverageBusy = errors.New("a deep signal-coverage scan is already running; wait for it to finish and retry")

// SignalCount is one signal's coverage split by the row's stored Missing flag.
// "Present on disk" means the row is NOT flagged missing — the file is not
// stat'ed; the flag is whatever the last scan or repair wrote.
type SignalCount struct {
	Have                 int64 `json:"have"`
	Missing              int64 `json:"missing"`
	HaveFileMissing      int64 `json:"have_file_missing"`
	MissingPresentOnDisk int64 `json:"missing_present_on_disk"`
	MissingFileMissing   int64 `json:"missing_file_missing"`
}

// BookFileSignalCoverage is the per-signal coverage of book_file rows.
type BookFileSignalCoverage struct {
	// Source is "memdb" (fast, proxy fingerprint fields), "pebble" (deep
	// scan, exact fingerprint fields) or "core" (non-Pebble store fallback).
	Source string `json:"source"`
	// ExactFingerprintFields is true only when raw and Seg0..6 were read from
	// Pebble. When false those signals are absent from Signals and named in
	// Unavailable.
	ExactFingerprintFields bool  `json:"exact_fingerprint_fields"`
	TotalBookFiles         int64 `json:"total_book_files"`
	FileMissingRows        int64 `json:"file_missing_rows"`
	PresentOnDiskRows      int64 `json:"present_on_disk_rows"`
	SkipScanRows           int64 `json:"skip_scan_rows"`

	Signals map[string]SignalCount `json:"signals"`

	// FingerprintFailures counts rows carrying a fingerprint failure tombstone
	// (FingerprintFailedAt set). The reason is stripped from memdb, so
	// FingerprintFailuresByReason is filled only by the deep scan.
	FingerprintFailures         int64            `json:"fingerprint_failures"`
	FingerprintFailuresByReason map[string]int64 `json:"fingerprint_failures_by_reason,omitempty"`

	// Unavailable names signals this response could not count and why.
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// signalAcc is one worker's private tally. Workers never share one, so the hot
// loop needs no atomics; merge() folds them together after the pool drains.
type signalAcc struct {
	counts      [numSignals]SignalCount
	total       int64
	missingRows int64
	skipScan    int64
	failed      int64
	reasons     map[string]int64
}

func (a *signalAcc) mark(idx int, have, fileMissing bool) {
	c := &a.counts[idx]
	switch {
	case have && fileMissing:
		c.Have++
		c.HaveFileMissing++
	case have:
		c.Have++
	case fileMissing:
		c.Missing++
		c.MissingFileMissing++
	default:
		c.Missing++
		c.MissingPresentOnDisk++
	}
}

// add tallies one row. exact says the heavy fingerprint fields are populated
// (a Pebble-sourced row); on a memdb row they are always blank, so counting
// them would report every file as unfingerprinted.
func (a *signalAcc) add(f *BookFile, exact bool) {
	a.total++
	fm := f.Missing
	if fm {
		a.missingRows++
	}
	if f.SkipScan {
		a.skipScan++
	}
	a.mark(sigFileHash, f.FileHash != "", fm)
	a.mark(sigOrigHash, f.OriginalFileHash != "", fm)
	a.mark(sigFPDuration, f.AcoustIDFingerprintDurationSec > 0, fm)
	a.mark(sigDuration, f.Duration > 0, fm)
	a.mark(sigRawTags, len(f.RawTags) > 0, fm)
	if f.FingerprintFailedAt != nil {
		a.failed++
		if exact {
			reason := "unrecorded"
			if f.FingerprintFailureReason != nil && *f.FingerprintFailureReason != "" {
				reason = *f.FingerprintFailureReason
			}
			if a.reasons == nil {
				a.reasons = make(map[string]int64)
			}
			a.reasons[reason]++
		}
	}
	if !exact {
		return
	}
	hasRaw := len(f.AcoustIDFingerprint) > 0
	a.mark(sigRawFP, hasRaw, fm)
	a.mark(sigSegOnly, !hasRaw && f.AcoustIDSeg0 != "", fm)
	segs := [7]string{f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3,
		f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6}
	for i, s := range segs {
		a.mark(sigSeg0+i, s != "", fm)
	}
}

func (a *signalAcc) merge(b *signalAcc) {
	for i := range a.counts {
		x, y := &a.counts[i], &b.counts[i]
		x.Have += y.Have
		x.Missing += y.Missing
		x.HaveFileMissing += y.HaveFileMissing
		x.MissingPresentOnDisk += y.MissingPresentOnDisk
		x.MissingFileMissing += y.MissingFileMissing
	}
	a.total += b.total
	a.missingRows += b.missingRows
	a.skipScan += b.skipScan
	a.failed += b.failed
	for k, v := range b.reasons {
		if a.reasons == nil {
			a.reasons = make(map[string]int64)
		}
		a.reasons[k] += v
	}
}

func (a *signalAcc) result(source string, exact bool) *BookFileSignalCoverage {
	out := &BookFileSignalCoverage{
		Source:                 source,
		ExactFingerprintFields: exact,
		TotalBookFiles:         a.total,
		FileMissingRows:        a.missingRows,
		PresentOnDiskRows:      a.total - a.missingRows,
		SkipScanRows:           a.skipScan,
		Signals:                make(map[string]SignalCount, numSignals),
		FingerprintFailures:    a.failed,
		Unavailable: map[string]string{
			"cover_hash":      "no cover content or perceptual hash exists in the codebase yet",
			"file_hash_error": "hash failures are not recorded on book_file rows",
		},
	}
	limit := numSignals
	if !exact {
		limit = sigRawFP
		for _, name := range signalNames[sigRawFP:] {
			out.Unavailable[name] = "stripped from memdb rows; pass deep=true for a Pebble scan"
		}
		out.Unavailable["fingerprint_failures_by_reason"] = "failure reason is stripped from memdb rows; pass deep=true"
	}
	for i := 0; i < limit; i++ {
		out.Signals[signalNames[i]] = a.counts[i]
	}
	if exact {
		out.FingerprintFailuresByReason = a.reasons
		if out.FingerprintFailuresByReason == nil {
			out.FingerprintFailuresByReason = map[string]int64{}
		}
	}
	return out
}

// CountBookFileSignals tallies n rows, fetched by index through at, across a
// bounded pool of workers. Each worker owns a contiguous shard and a private
// accumulator, merged once at the end, so there is no shared mutable state in
// the loop. workers < 1 means runtime.NumCPU().
func CountBookFileSignals(ctx context.Context, n int, at func(i int) *BookFile, exact bool, workers int, source string) (*BookFileSignalCoverage, error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	shards := workers * 4 // smaller shards than workers keeps stragglers short
	if shards > n {
		shards = n
	}
	if shards < 1 {
		shards = 1
	}
	accs := make([]signalAcc, shards)
	var g errgroup.Group
	g.SetLimit(workers)
	per := (n + shards - 1) / shards
	for s := 0; s < shards; s++ {
		lo, hi := s*per, min((s+1)*per, n)
		acc := &accs[s]
		g.Go(func() error {
			for i := lo; i < hi; i++ {
				if i%4096 == 0 && ctx.Err() != nil {
					return ctx.Err()
				}
				if f := at(i); f != nil {
					acc.add(f, exact)
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var total signalAcc
	for i := range accs {
		total.merge(&accs[i])
	}
	return total.result(source, exact), nil
}

// CountBookFileSignalsFromCores is the fallback for stores that are not a
// PebbleStore (tests, mocks): it counts the memdb-safe signals from the Core
// projection. Fingerprint fields are proxy-only, exactly as on the memdb path.
func CountBookFileSignalsFromCores(ctx context.Context, cores []BookFileCore, workers int) (*BookFileSignalCoverage, error) {
	return CountBookFileSignals(ctx, len(cores), func(i int) *BookFile {
		c := &cores[i]
		return &BookFile{
			FileHash:                       c.FileHash,
			OriginalFileHash:               c.OriginalFileHash,
			AcoustIDFingerprintDurationSec: c.AcoustIDFingerprintDurationSec,
			Duration:                       c.Duration,
			RawTags:                        c.RawTags,
			Missing:                        c.Missing,
			SkipScan:                       c.SkipScan,
			FingerprintFailedAt:            c.FingerprintFailedAt,
		}
	}, false, workers, "core")
}

// bookFilePointers returns the live memdb row pointers. Callers must treat
// them as READ-ONLY: memdb rows are replaced on write, never mutated in place,
// so a pointer taken under a read txn stays a consistent snapshot of that row.
// Collecting pointers (8 bytes each) rather than Core copies is what keeps a
// 742k-row count from allocating hundreds of MB per request.
func (m *MemStore) bookFilePointers() ([]*BookFile, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	iter, err := txn.Get(memTableBookFiles, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files scan: %w", err)
	}
	var out []*BookFile
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		if bf, ok := obj.(*BookFile); ok {
			out = append(out, bf)
		}
	}
	return out, nil
}

// GetBookFileSignalCoverage reports per-signal coverage of every book_file.
//
// deep=false reads memdb row pointers (fast; fingerprint fields proxy-only) and
// returns ErrMemDBNotReady when memdb is not serving reads rather than falling
// back to a Pebble scan. deep=true scans Pebble and decodes only the presence of
// the heavy fields, for exact raw / Seg0..6 / failure-reason counts; one deep
// scan runs per store at a time, and a concurrent request gets
// ErrDeepCoverageBusy.
func (p *PebbleStore) GetBookFileSignalCoverage(ctx context.Context, deep bool, workers int) (*BookFileSignalCoverage, error) {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	if !deep {
		if !p.UseMemDB {
			return nil, fmt.Errorf("%w: memdb is disabled on this store (UseMemDB=false); pass deep=true for a Pebble scan", ErrMemDBNotReady)
		}
		mem := p.mem()
		if mem == nil {
			return nil, fmt.Errorf("%w: memdb is not published yet (warmup still running, or it failed and reads fell back to Pebble); retry later, or pass deep=true for a Pebble scan", ErrMemDBNotReady)
		}
		ptrs, err := mem.bookFilePointers()
		if err != nil {
			return nil, err
		}
		return CountBookFileSignals(ctx, len(ptrs), func(i int) *BookFile { return ptrs[i] }, false, workers, "memdb")
	}
	if !p.deepCoverageBusy.CompareAndSwap(false, true) {
		return nil, ErrDeepCoverageBusy
	}
	defer p.deepCoverageBusy.Store(false)
	return p.deepBookFileSignalCoverage(ctx, workers)
}

// presence decodes any JSON value to "was it non-empty" without keeping the
// bytes: the raw fingerprint is kilobytes per row, and the deep scan only needs
// to know it exists.
type presence bool

func (p *presence) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "null", `""`, "{}", "[]":
		*p = false
	default:
		*p = true
	}
	return nil
}

// bookFileSignalRow is the slim decode target for the deep scan.
type bookFileSignalRow struct {
	FileHash         string   `json:"file_hash"`
	OriginalFileHash string   `json:"original_file_hash"`
	Duration         int      `json:"duration"`
	RawTags          presence `json:"raw_tags"`
	RawFP            presence `json:"acoustid_fingerprint"`
	FPDuration       float64  `json:"acoustid_fingerprint_duration_sec"`
	Seg0             presence `json:"acoustid_seg0"`
	Seg1             presence `json:"acoustid_seg1"`
	Seg2             presence `json:"acoustid_seg2"`
	Seg3             presence `json:"acoustid_seg3"`
	Seg4             presence `json:"acoustid_seg4"`
	Seg5             presence `json:"acoustid_seg5"`
	Seg6             presence `json:"acoustid_seg6"`
	FailedAt         presence `json:"fingerprint_failed_at"`
	FailureReason    *string  `json:"fingerprint_failure_reason"`
	Missing          bool     `json:"missing"`
	SkipScan         bool     `json:"skip_scan"`
}

// deepBookFileSignalCoverage streams primary book_file rows out of one Pebble
// iterator into a bounded pool of decoders, each with a private accumulator.
func (p *PebbleStore) deepBookFileSignalCoverage(ctx context.Context, workers int) (*BookFileSignalCoverage, error) {
	// Each queued row is a full copied value, raw fingerprint included (KBs).
	// At most workers+2 batches are alive at once (one per decoder, one in
	// the channel, one being filled), so in-flight rows stay near
	// (workers+2)*batchSize rather than the (3*workers+1)*512 the old
	// workers*2 buffer allowed.
	const batchSize = 128
	batches := make(chan [][]byte, 1)
	accs := make([]signalAcc, workers)

	g, gctx := errgroup.WithContext(ctx)
	for w := 0; w < workers; w++ {
		acc := &accs[w]
		g.Go(func() error {
			for batch := range batches {
				for _, val := range batch {
					var row bookFileSignalRow
					if err := json.Unmarshal(val, &row); err != nil {
						return fmt.Errorf("decode book_file: %w", err)
					}
					acc.add(row.asBookFile(), true)
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
		batch := make([][]byte, 0, batchSize)
		for iter.First(); iter.Valid(); iter.Next() {
			// Primary keys only: book_file:<bookID>:<fileID>. The prefix range
			// also holds no secondary indexes (those are book_file_<x>:), but
			// the colon count guards against any future sub-key.
			if strings.Count(string(iter.Key()), ":") != 2 {
				continue
			}
			batch = append(batch, append([]byte(nil), iter.Value()...))
			if len(batch) == batchSize {
				select {
				case batches <- batch:
				case <-gctx.Done():
					return gctx.Err()
				}
				batch = make([][]byte, 0, batchSize)
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
	var total signalAcc
	for i := range accs {
		total.merge(&accs[i])
	}
	return total.result("pebble", true), nil
}

// asBookFile maps presence flags onto a BookFile carrying placeholder values,
// so the deep scan and the memdb path share one counting function.
func (r *bookFileSignalRow) asBookFile() *BookFile {
	mark := func(p presence) string {
		if p {
			return "x"
		}
		return ""
	}
	f := &BookFile{
		FileHash:                       r.FileHash,
		OriginalFileHash:               r.OriginalFileHash,
		Duration:                       r.Duration,
		AcoustIDFingerprintDurationSec: r.FPDuration,
		AcoustIDSeg0:                   mark(r.Seg0),
		AcoustIDSeg1:                   mark(r.Seg1),
		AcoustIDSeg2:                   mark(r.Seg2),
		AcoustIDSeg3:                   mark(r.Seg3),
		AcoustIDSeg4:                   mark(r.Seg4),
		AcoustIDSeg5:                   mark(r.Seg5),
		AcoustIDSeg6:                   mark(r.Seg6),
		FingerprintFailureReason:       r.FailureReason,
		Missing:                        r.Missing,
		SkipScan:                       r.SkipScan,
	}
	if r.RawTags {
		f.RawTags = map[string]string{"": ""}
	}
	if r.RawFP {
		f.AcoustIDFingerprint = []byte{1}
	}
	if r.FailedAt {
		f.FingerprintFailedAt = new(time.Time)
	}
	return f
}
