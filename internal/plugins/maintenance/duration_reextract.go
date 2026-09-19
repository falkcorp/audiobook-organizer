// file: internal/plugins/maintenance/duration_reextract.go
// version: 3.16.0
// guid: 9c2f7a14-6d83-4e51-b0a9-2f5c8e1d4b67
// last-edited: 2026-09-19

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
//     re-extract EVERY segment, correct each BookFile.Duration that drifted, and
//     sum the corrected segments into Book.Duration. The drifted segments of one
//     book are written in ONE UpdateBookFiles call: each row is written by ID
//     with UpdateBookFile's semantics (AcoustID fingerprint preserved, PR #1552;
//     memdb refreshed, PR #1560), and the book aggregates are recomputed ONCE
//     after the rows, not once per row. Per-row UpdateBookFile recomputed the
//     book after every segment, re-reading all of its rows each time; a
//     1,494-file book took ~25 minutes that way on 2026-09-19 and the stuck-op
//     watchdog killed the run.
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
// Liveness and cancellation: the collector reports progress at most every ~15s
// between books, and INSIDE a book's segment writes it stamps the watchdog's
// liveness clock after every row (registry.TouchLiveness) and reports a
// "segment i/n" progress line on the same ~15s cadence, so one very large book
// is not mistaken for a stuck op. ctx is checked before every book and between
// segment writes; on cancellation the op stops (each row already written is a
// complete write and its book's aggregates are recomputed) and returns
// ctx.Err(). Workers select on ctx for their result send, so a collector that
// stops early does not leave them blocked forever.
//
// Idempotent: a re-run finds already-corrected rows within tolerance and skips
// them.

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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
	info, err := boundedCall(ctx, extractTimeout, func() (*mediainfo.MediaInfo, error) {
		return mediainfo.Extract(filePath)
	})
	if errors.Is(err, errBoundedCallTimeout) {
		return nil, fmt.Errorf("extract: %w", err)
	}
	return info, err
}

func (p *Plugin) durationReextractDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.duration-reextract",
		Liveness:    sdk.LivenessManual,
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
func processBookForReextract(ctx context.Context, store bookFileLister, book database.Book, skipBefore time.Time) bookProcessResult {
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

// reextractSegmentProgressInterval throttles the "segment i/n" progress line
// written while one book's segments are being written. Liveness itself is
// stamped on every row regardless. A var so tests can set it to zero.
var reextractSegmentProgressInterval = 15 * time.Second

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

	store := p.deps.OpsStore()
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
		examined       int
		eligible       int
		wouldChange    int
		roughlyDouble  int
		estimated      int
		readErr        int
		noPath         int
		fpBooks        int
		ffprobeBooks   int
		storedDurBooks int
		written        int
		examples       = make([]string, 0, exampleCap)
		lastLog        = time.Now()
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
		wg.Go(func() {
			for book := range jobCh {
				res := processBookForReextract(ctx, store, book, skipBefore)
				select {
				case resultCh <- res:
				case <-ctx.Done():
					// The collector stops draining on cancel; without this
					// select every worker would block on the send forever.
					return
				}
			}
		})
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
		if err := ctx.Err(); err != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
				"cancelled after %d books examined, %d corrected", examined, written))
			return err
		}
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

		// Apply. Drifted segments go through ONE UpdateBookFiles call, which
		// recomputes the book's aggregates once after the rows. A book whose
		// total drifted with no segment drifting needs the recompute alone.
		if len(res.changedBFs) > 0 {
			rows := make([]*database.BookFile, len(res.changedBFs))
			for ci := range res.changedBFs {
				rows[ci] = &res.changedBFs[ci]
			}
			bookID, nSeg := res.book.ID, len(rows)
			lastSegLog := time.Now()
			_, uErr := store.UpdateBookFiles(ctx, rows, func(done int) {
				// A row write just finished: real work, so stamp liveness.
				registry.TouchLiveness(reporter)
				if time.Since(lastSegLog) >= reextractSegmentProgressInterval {
					total := totalBooks
					if total == 0 {
						total = examined
					}
					_ = reporter.UpdateProgress(examined, total, fmt.Sprintf(
						"book %s: wrote segment %d/%d (examined=%d corrected=%d)",
						bookID, done, nSeg, examined, written))
					lastSegLog = time.Now()
				}
			})
			if uErr != nil {
				if ctx.Err() != nil {
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
						"cancelled while writing book %s's segments, after %d books examined, %d corrected",
						bookID, examined, written))
					return ctx.Err()
				}
				// Not stamped verified, so the next run retries this book.
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: segment write or aggregate recompute failed: %v", bookID, uErr))
				readErr++
				continue
			}
		} else if len(res.segs) > 0 {
			if rErr := store.RecomputeBookAggregates(res.book.ID); rErr != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: RecomputeBookAggregates failed: %v", res.book.ID, rErr))
				readErr++
				continue
			}
		} else {
			// The new duration was measured against res.book's stored value
			// before the slow fingerprint/ffprobe work. Write only Duration,
			// under the book's write lock (ModifyBook), so a column another
			// writer commits meanwhile is not reverted (audit A1#15), and only
			// while the stored duration is still the one the diff was
			// measured against.
			nd := res.newDur
			oldDur := res.book.Duration
			applied := false
			row, uErr := store.ModifyBook(res.book.ID, func(cur *database.Book) error {
				if !sameIntPtr(cur.Duration, oldDur) {
					return database.ErrSkipBookWrite
				}
				cur.Duration = &nd
				applied = true
				return nil
			})
			if uErr != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: ModifyBook failed: %v", res.book.ID, uErr))
				readErr++
				continue
			}
			if row == nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: gone before the duration write", res.book.ID))
				readErr++
				continue
			}
			if !applied {
				_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
					"book %s: duration changed underneath, left for the next run", res.book.ID))
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
func stampVerifiedAt(store bookModifier, reporter sdk.Reporter, bookID string) {
	now := time.Now()
	// Only DurationVerifiedAt changes, under the book's write lock
	// (ModifyBook), so a column another writer commits meanwhile is not
	// reverted (audit A1#15). A book gone meanwhile is not stamped, as before.
	if _, err := store.ModifyBook(bookID, func(cur *database.Book) error {
		cur.DurationVerifiedAt = &now
		return nil
	}); err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: failed to stamp DurationVerifiedAt: %v", bookID, err))
	}
}

// sameIntPtr reports whether two optional ints hold the same value.
func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
