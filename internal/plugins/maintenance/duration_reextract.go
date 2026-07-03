// file: internal/plugins/maintenance/duration_reextract.go
// version: 3.9.0
// guid: 9c2f7a14-6d83-4e51-b0a9-2f5c8e1d4b67
// last-edited: 2026-07-03

// Package maintenance — op maintenance.duration-reextract.
//
// PR #1555 fixed internal/mediainfo to read the TRUE audio-stream duration via
// ffprobe instead of the old fileSize÷assumed-bitrate estimate. The old estimator
// assumed 128 kbps for m4b/m4a, so those durations were routinely ~2× too short.
// Every Book row imported before that fix still carries the wrong duration, which
// poisons dedup duration-matching (checkDurationMatch) and metadata scoring.
//
// This op re-derives the real duration and corrects durations when the new value
// differs meaningfully from the stored one (see "Source priority (v3)" below for
// where the real value comes from). It NEVER overwrites a stored value with an
// ffprobe-fallback ESTIMATE (DurationEstimated==true) and skips books with any
// unreadable segment. Dry-run by default: previews counts; set dryRun=false to
// apply.
//
// Scope (v2): handles BOTH book layouts.
//   - Multi-file books (audio across BookFiles; Book.FilePath may be a directory):
//     re-extract EVERY segment, correct each BookFile.Duration that drifted, then
//     RecomputeBookAggregates to sum the corrected segments into Book.Duration.
//     Segment writes use UpdateBookFile on pebble-direct rows, so the AcoustID
//     fingerprint is preserved (PR #1552) and memdb is refreshed (PR #1560).
//   - Virtual single-file books (no BookFile rows): probe Book.FilePath and write
//     Book.Duration directly.
// A book is corrected only when ALL present segments yield a real (non-estimated)
// duration — a single missing/unreadable/estimated segment makes the total
// untrustworthy, so the whole book is skipped (counted) rather than half-written.
// Book.Duration is what dedup's checkDurationMatch consumes.
//
// Source priority (v3): fingerprinting already measured and stored the real
// decode duration in BookFile.AcoustIDFingerprintDurationSec for ~275K files. v3
// reads that stored value FIRST and treats it as authoritative — no stat, no
// ffprobe — so the fingerprinted majority of the backlog is a fast pure-DB pass.
// ffprobe is the fallback only for never-fingerprinted segments (and virtual
// single-file books, which have no BookFile row to carry the value). The summary
// reports from-fingerprint vs from-ffprobe so a dry-run reveals the fast/slow
// split. The FingerprintFailedAt tombstone does NOT gate the ffprobe fallback:
// ffprobe can still read a container header even when full-decode fingerprinting
// failed, and the worst case is simply skipping the book.
//
// Parallelism (v4): the Workers param controls how many books are processed
// concurrently (default 4, max 16). The fp-fast path is pure-DB and near-instant;
// the ffprobe path shells out per file and is the real bottleneck. Parallelising
// over books means up to Workers ffprobe subprocesses run at once, cutting wall
// clock for the ffprobe tail proportionally. All DB writes happen on a single
// collector goroutine — no locking required on counters or PebbleDB writes.
//
// Idempotent: a re-run finds already-corrected rows within tolerance and skips
// them. The ffprobe tail is slow by design — it shells out once per
// non-fingerprinted segment — so the op heartbeats progress every ~15s and is
// cancellable.

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type durationReextractParams struct {
	DryRun bool `json:"dryRun"`
	// Limit caps the number of books examined in one run (0 = no cap). Useful for
	// a bounded first pass over a large library.
	Limit int `json:"limit"`
	// Workers sets the number of concurrent book-processing goroutines (default 4,
	// clamped 1–16). More workers = more concurrent ffprobe subprocesses, which
	// cuts wall-clock time for the non-fingerprinted tail proportionally.
	Workers int `json:"workers"`
	// SkipAgeDays skips books whose DurationVerifiedAt is more recent than this
	// many days (default 90). Set to 0 to disable age-based skipping. Has no
	// effect when Force is true.
	SkipAgeDays int `json:"skipAgeDays"`
	// Force ignores DurationVerifiedAt entirely and re-examines every book.
	Force bool `json:"force"`
	// OnlyMissingDuration, if true, skips books whose Duration is already known
	// and positive (Book.Duration != nil && *Book.Duration > 0). Use this to
	// scope a run to the Duration=0/nil residual (DEDUP-4) instead of
	// re-checking the whole library. Default false (preserves existing
	// whole-library behavior for all current callers/schedules).
	OnlyMissingDuration bool `json:"onlyMissingDuration"`
}

// durationChangeThresholds: a book is corrected only when the freshly extracted
// real duration differs from the stored value by more than BOTH a relative and an
// absolute floor, so we never churn rows over sub-second rounding noise.
const (
	durationRelTolerance  = 0.02 // 2%
	durationAbsToleranceS = 5    // seconds
)

// durationDiffMeaningful reports whether newDur differs from oldDur by enough to
// warrant a write: >2% AND >5s. Both floors must be exceeded.
func durationDiffMeaningful(oldDur, newDur int) bool {
	if oldDur <= 0 {
		return newDur > 0 // no usable stored value — any real value is an improvement
	}
	delta := int(math.Abs(float64(newDur - oldDur)))
	if delta <= durationAbsToleranceS {
		return false
	}
	rel := float64(delta) / float64(oldDur)
	return rel > durationRelTolerance
}

// extractTimeout is the per-file wall-clock cap for mediainfo.Extract. The call
// chain (os.Open → tag.ReadFrom → ffprobe) contains several blocking syscalls
// that do not respect Go context cancellation and can hang indefinitely on a
// slow or unresponsive filesystem. The goroutine is intentionally leaked on
// timeout — it will unblock whenever the kernel recovers the I/O.
const extractTimeout = 30 * time.Second

// extractWithTimeout runs mediainfo.Extract in a goroutine and returns an error
// if it does not complete within extractTimeout. It also respects ctx so the op
// can be cancelled between files.
func extractWithTimeout(ctx context.Context, filePath string) (*mediainfo.MediaInfo, error) {
	type result struct {
		info *mediainfo.MediaInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := mediainfo.Extract(filePath)
		ch <- result{info, err}
	}()
	timer := time.NewTimer(extractTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.info, r.err
	case <-timer.C:
		return nil, fmt.Errorf("extract timed out after %v", extractTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Plugin) durationReextractDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.duration-reextract",
		Plugin:      "maintenance",
		DisplayName: "Re-extract real book durations (fingerprint-first)",
		Description: "Reads the real per-file duration already stored from fingerprinting (AcoustIDFingerprintDurationSec) first — a fast DB pass — " +
			"and falls back to ffprobe only for never-fingerprinted files. Handles both multi-file and virtual single-file books. " +
			"Corrects Book.Duration where the old fileSize÷bitrate estimate was wrong (PR #1555; m4b/m4a were ~2× too short). " +
			"Never overwrites a real duration with an estimate, and skips books with any unreadable segment. " +
			"Default dry-run previews counts (incl. fingerprint vs ffprobe split); set dryRun=false to apply. " +
			"Workers param (default 4) controls ffprobe concurrency — higher = faster ffprobe tail. " +
			"Set onlyMissingDuration=true to scope the run to books with no known duration.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.duration-reextract",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDurationReextract,
	}
}

// bookProcessResult holds the outcome of processing one book. Returned by
// processBookForReextract and consumed by the collector goroutine, which owns
// all counter mutations and DB writes.
type bookProcessResult struct {
	book             database.Book
	segs             []database.BookFile // may be nil for virtual single-file books
	newDur           int
	changedBFs       []database.BookFile
	eligible         bool
	usedFfprobe      bool
	usedStoredDur    bool // used stored segment Duration instead of ffprobe (non-iTunes fast path)
	wouldChange      bool
	roughDouble      bool
	readErr          bool
	estimated        bool
	noPath           bool
	recentlyVerified bool // skipped because DurationVerifiedAt is within skipBefore threshold
	example          string
}

// processBookForReextract evaluates one book: computes the real duration from
// stored fingerprint values (fast) or ffprobe (slow), and returns what should
// be written. It never writes to the store. skipBefore is the age threshold:
// books verified after skipBefore are returned with recentlyVerified=true.
func processBookForReextract(ctx context.Context, store database.Store, book database.Book, skipBefore time.Time) bookProcessResult {
	res := bookProcessResult{book: book}
	if !skipBefore.IsZero() && book.DurationVerifiedAt != nil && book.DurationVerifiedAt.After(skipBefore) {
		res.recentlyVerified = true
		return res
	}

	segs, _ := store.GetBookFiles(book.ID)
	res.segs = segs

	var (
		newDur      int
		changedBFs  []database.BookFile
		skip        bool
		usedFfprobe bool
	)

	if len(segs) > 0 {
		for si := range segs {
			f := segs[si]
			if f.FilePath == "" {
				continue
			}
			var segDur int
			if f.AcoustIDFingerprintDurationSec > 0 {
				segDur = int(math.Round(f.AcoustIDFingerprintDurationSec))
			} else if f.ITunesPersistentID == "" && book.ITunesPersistentID == nil && f.Duration > 0 {
				// Stored-duration fast path: non-iTunes segment with a known Duration.
				// The iTunes-ms bug (durations stored as milliseconds instead of seconds)
				// only affects iTunes-imported segments; organized-library files have
				// durations measured by the scanner and can be trusted without ffprobe.
				segDur = f.Duration
				res.usedStoredDur = true
			} else {
				usedFfprobe = true
				info, mErr := extractWithTimeout(ctx, f.FilePath)
				if mErr != nil || info == nil || info.Duration <= 0 {
					res.readErr = true
					skip = true
					break
				}
				if info.DurationEstimated {
					res.estimated = true
					skip = true
					break
				}
				segDur = info.Duration
			}
			newDur += segDur
			if durationDiffMeaningful(f.Duration, segDur) {
				nf := f
				nf.Duration = segDur
				changedBFs = append(changedBFs, nf)
			}
		}
	} else {
		usedFfprobe = true
		if book.FilePath == "" {
			res.noPath = true
			return res
		}
		info, mErr := extractWithTimeout(ctx, book.FilePath)
		if mErr != nil || info == nil || info.Duration <= 0 {
			res.readErr = true
			return res
		}
		if info.DurationEstimated {
			res.estimated = true
			return res
		}
		newDur = info.Duration
	}

	if skip || newDur <= 0 {
		return res
	}

	res.eligible = true
	res.usedFfprobe = usedFfprobe
	res.newDur = newDur
	res.changedBFs = changedBFs

	oldDur := 0
	if book.Duration != nil {
		oldDur = *book.Duration
	}
	if !durationDiffMeaningful(oldDur, newDur) && len(changedBFs) == 0 {
		return res // already correct
	}
	res.wouldChange = true
	if oldDur > 0 {
		ratio := float64(newDur) / float64(oldDur)
		if ratio >= 1.8 && ratio <= 2.2 {
			res.roughDouble = true
		}
	}
	res.example = fmt.Sprintf("%s %ds→%ds (%d seg)", book.ID, oldDur, newDur, len(segs))
	return res
}

func (p *Plugin) runDurationReextract(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := durationReextractParams{DryRun: true, Workers: 4, SkipAgeDays: 90} // safe defaults
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	// Clamp worker count.
	if params.Workers < 1 {
		params.Workers = 4
	}
	if params.Workers > 16 {
		params.Workers = 16
	}
	// Compute skip threshold once; zero SkipAgeDays or Force disables age skipping.
	var skipBefore time.Time
	if !params.Force && params.SkipAgeDays > 0 {
		skipBefore = time.Now().AddDate(0, 0, -params.SkipAgeDays)
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	totalBooks, countErr := store.CountAllBooks()
	if countErr != nil || totalBooks <= 0 {
		totalBooks = 0
	}
	_ = reporter.UpdateProgress(0, totalBooks, "Correcting real durations (fingerprint-first, ffprobe fallback)…")

	const (
		pageSize    = 500
		logInterval = 15 * time.Second
		exampleCap  = 5
	)

	// All counters and the examples slice are owned exclusively by the collector
	// goroutine (the main goroutine after the workers start). No locking needed.
	var (
		examined      int
		eligible      int
		wouldChange   int
		roughlyDouble int
		estimated     int
		readErr       int
		noPath        int
		fpBooks        int
		ffprobeBooks   int
		storedDurBooks int
		written        int
		examples      = make([]string, 0, exampleCap)
		lastLog       = time.Now()
	)

	heartbeat := func(force bool) {
		if !force && time.Since(lastLog) < logInterval {
			return
		}
		total := totalBooks
		if total == 0 {
			total = examined
		}
		_ = reporter.UpdateProgress(examined, total, fmt.Sprintf(
			"examined=%d eligible=%d (fp=%d stored=%d ffprobe=%d) would-change=%d (~2x=%d) est-skip=%d read-err=%d",
			examined, eligible, fpBooks, storedDurBooks, ffprobeBooks, wouldChange, roughlyDouble, estimated, readErr))
		lastLog = time.Now()
	}

	errLimitReached := fmt.Errorf("limit reached")

	// jobCh feeds books from the PageBooks producer to workers.
	// resultCh carries per-book results back to the collector (this goroutine).
	jobCh := make(chan database.Book, params.Workers*2)
	resultCh := make(chan bookProcessResult, params.Workers*2)

	// Start worker pool.
	var wg sync.WaitGroup
	for i := 0; i < params.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for book := range jobCh {
				resultCh <- processBookForReextract(ctx, store, book, skipBefore)
			}
		}()
	}
	// Close resultCh once all workers finish.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Producer: iterate all books and feed them into jobCh.
	// Runs in its own goroutine so the collector can drain resultCh concurrently.
	producerErr := make(chan error, 1)
	go func() {
		dispatched := 0
		err := sdk.PageBooks(ctx, store, reporter, pageSize, func(book database.Book) error {
			if params.Limit > 0 && dispatched >= params.Limit {
				return errLimitReached
			}
			if params.OnlyMissingDuration && book.Duration != nil && *book.Duration > 0 {
				return nil // skip: duration already known, out of scope for this run
			}
			dispatched++
			select {
			case jobCh <- book:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
		close(jobCh)
		producerErr <- err
	}()

	// Collector: drain results, update counters, apply writes.
	for res := range resultCh {
		examined++
		heartbeat(false)

		if res.recentlyVerified {
			estimated++ // counted as "skipped" — already verified recently
			continue
		}
		if res.noPath {
			noPath++
			continue
		}
		if res.readErr {
			readErr++
			continue
		}
		if res.estimated {
			estimated++
			continue
		}
		if !res.eligible {
			continue
		}

		eligible++
		if res.usedFfprobe {
			ffprobeBooks++
		} else if res.usedStoredDur {
			storedDurBooks++
		} else {
			fpBooks++
		}
		if !res.wouldChange {
			// Duration is already correct — stamp verified and move on.
			if !params.DryRun {
				stampVerifiedAt(store, reporter, res.book.ID)
			}
			continue
		}

		wouldChange++
		if res.roughDouble {
			roughlyDouble++
		}
		if len(examples) < exampleCap {
			examples = append(examples, res.example)
		}

		if params.DryRun {
			continue
		}

		// Apply: write changed segments then recompute aggregates.
		for ci := range res.changedBFs {
			cf := res.changedBFs[ci]
			if uErr := store.UpdateBookFile(cf.ID, &cf); uErr != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s seg %s: UpdateBookFile failed: %v", res.book.ID, cf.ID, uErr))
				readErr++
			}
		}
		if len(res.segs) > 0 {
			if rErr := store.RecomputeBookAggregates(res.book.ID); rErr != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: RecomputeBookAggregates failed: %v", res.book.ID, rErr))
				readErr++
				continue
			}
		} else {
			full, gErr := store.GetBookByID(res.book.ID)
			if gErr != nil || full == nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: GetBookByID failed: %v", res.book.ID, gErr))
				readErr++
				continue
			}
			nd := res.newDur
			full.Duration = &nd
			if _, uErr := store.UpdateBook(res.book.ID, full); uErr != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: UpdateBook failed: %v", res.book.ID, uErr))
				readErr++
				continue
			}
		}
		written++
		stampVerifiedAt(store, reporter, res.book.ID)
		heartbeat(false)
	}

	// Wait for producer and surface any non-limit error.
	if err := <-producerErr; err != nil && err != errLimitReached {
		return fmt.Errorf("book scan: %w", err)
	}

	verb := "would correct"
	if !params.DryRun {
		verb = fmt.Sprintf("corrected %d;", written)
	}
	summary := fmt.Sprintf(
		"examined=%d eligible=%d (from-fingerprint=%d from-stored=%d from-ffprobe=%d) %s would-change=%d (~2x=%d) estimated-skipped=%d read-errors=%d no-filepath=%d | e.g. %s",
		examined, eligible, fpBooks, storedDurBooks, ffprobeBooks, verb, wouldChange, roughlyDouble, estimated, readErr, noPath,
		strings.Join(examples, ", "))
	_ = reporter.Log(slog.LevelInfo, summary)
	total := totalBooks
	if total == 0 {
		total = examined
	}
	_ = reporter.UpdateProgress(total, total, summary)
	return nil
}

// stampVerifiedAt writes DurationVerifiedAt=now to the book record. Called
// after confirming or correcting a book's duration so future runs can skip it.
func stampVerifiedAt(store database.Store, reporter sdk.Reporter, bookID string) {
	full, err := store.GetBookByID(bookID)
	if err != nil || full == nil {
		return
	}
	now := time.Now()
	full.DurationVerifiedAt = &now
	if _, err := store.UpdateBook(bookID, full); err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: failed to stamp DurationVerifiedAt: %v", bookID, err))
	}
}
