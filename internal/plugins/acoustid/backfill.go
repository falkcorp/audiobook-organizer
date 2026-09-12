// file: internal/plugins/acoustid/backfill.go
// version: 2.0.0
// guid: f6a7b8c9-d0e1-2345-def0-123456789abc
// last-edited: 2026-09-12

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// backfillPageSize is how many books one page loads. The op used to load the
// whole book table up front (GetAllBooksFullFrom("", 0)), which held ~862 MB of
// live heap in production and was implicated in three OOM kills in one night
// (server_lifecycle.go's acoustid_backfill gate). A page is a few MB.
const backfillPageSize = 500

// BackfillParams is the checkpoint state written by reporter.Checkpoint and
// restored into params on ResumeRestart.
type BackfillParams struct {
	// AfterBookID is the cursor: every book up to and including this ID, in
	// GetAllBooksFullFrom order, is finished. It is written only after a whole
	// page has drained, so out-of-order completion inside a page can never put
	// an unfinished book below it.
	AfterBookID string `json:"after_book_id,omitempty"`

	// LastProcessedBookID (pre-2026-09-07, sequential runs) and
	// Watermark/WatermarkBookID (2026-09-07 to 2026-09-12, whole-table load)
	// are older checkpoint formats. Both are READ so a checkpoint from an older
	// binary resumes instead of starting over; neither is written. Each names a
	// book with every earlier book already done, which is exactly a cursor.
	LastProcessedBookID string `json:"last_processed_book_id,omitempty"`
	Watermark           int    `json:"watermark,omitempty"`
	WatermarkBookID     string `json:"watermark_book_id,omitempty"`
}

// resumeCursor returns the book ID to resume after, preferring the newest
// checkpoint format present.
func resumeCursor(state BackfillParams) string {
	switch {
	case state.AfterBookID != "":
		return state.AfterBookID
	case state.WatermarkBookID != "":
		return state.WatermarkBookID
	default:
		return state.LastProcessedBookID
	}
}

// backfillTally counts per-file outcomes across the whole run.
//
// The fields are atomic rather than plain ints because they are written from
// every worker goroutine AND read from the RunItems Label closure, which
// run_items.go invokes inside each worker too. Under a plain int the reads are a
// race and the increments are a lost update.
type backfillTally struct {
	fingerprinted atomic.Int64
	skipped       atomic.Int64
	ineligible    atomic.Int64
	failed        atomic.Int64

	// reasons breaks ineligible down by fingerprintEligibility's reason. Until
	// 2026-09-12 ineligible outcomes were dropped on the floor, which is how an
	// 08-12 run could report "fingerprinted=0 skipped=476256" with ~266k rows
	// unaccounted for.
	mu      sync.Mutex
	reasons map[string]int64
}

func (t *backfillTally) record(outcome fingerprintFileOutcome, reason string) {
	switch outcome {
	case fingerprintOutcomeFingerprinted:
		t.fingerprinted.Add(1)
	case fingerprintOutcomeSkipped:
		t.skipped.Add(1)
	case fingerprintOutcomeFailed:
		t.failed.Add(1)
	case fingerprintOutcomeIneligible:
		t.ineligible.Add(1)
		if strings.HasPrefix(reason, "non_audio_ext:") {
			reason = "non_audio_ext"
		}
		t.mu.Lock()
		if t.reasons == nil {
			t.reasons = make(map[string]int64)
		}
		t.reasons[reason]++
		t.mu.Unlock()
	}
}

func (t *backfillTally) reasonCounts() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.reasons)
}

func (t *backfillTally) summary() string {
	return fmt.Sprintf("fingerprinted=%d skipped=%d ineligible=%d failed=%d",
		t.fingerprinted.Load(), t.skipped.Load(), t.ineligible.Load(), t.failed.Load())
}

// fingerprintFileFn runs fpcalc for one eligible file and persists the result.
// A variable so tests can substitute a fake: the real one needs an fpcalc
// binary, which CI does not have.
var fingerprintFileFn = doFingerprintFile

// backfillBook fingerprints the eligible files of one book, each in its own
// goroutine, and then refreshes the book signature once.
//
// The BOOK stays the unit RunItems schedules, because synthesizeBookSignatureForBook
// is a read-modify-write of the Book row and must run once, after every file of
// that book is done. The FILES fan out underneath it, bounded by fpSem — one
// semaphore shared by every book in the run, so total in-flight fpcalc stays at
// cap(fpSem) whether that is sixteen books with one file each or one book with
// a hundred chapters. Before 2026-09-12 a book's files ran sequentially inside
// one worker, so a many-chapter book pinned the run to a single fpcalc and
// parallelism collapsed near the end of a run (fingerprint timing note §6).
// Files of one book never share a row, so their UpdateBookFile writes are
// disjoint.
//
// Returns ctx.Err() when canceled so the page is not checkpointed; otherwise
// nil — one unreadable book must not abort a whole-library pass.
func (p *Plugin) backfillBook(ctx context.Context, b database.Book, tally *backfillTally, fpSem chan struct{}, logger *slog.Logger) error {
	files, ferr := p.store.GetBookFiles(b.ID)
	if ferr != nil {
		logger.Warn("acoustid backfill: list book files", "book_id", b.ID, "error", ferr)
		return nil
	}

	var (
		modified atomic.Bool
		wg       sync.WaitGroup
	)
	for _, f := range files {
		outcome, reason, stop := fingerprintEligibility(f, false)
		if stop {
			tally.record(outcome, reason)
			continue
		}
		// Acquire BEFORE spawning, so the goroutine count per book is bounded
		// by the semaphore too, not by the book's file count.
		select {
		case fpSem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-fpSem }()
			out := fingerprintFileFn(p.store, f, false)
			tally.record(out, "")
			if out == fingerprintOutcomeFingerprinted {
				modified.Store(true)
				// Per-slot pause, held while the slot is: it leaves headroom
				// between fpcalc invocations without capping total throughput.
				time.Sleep(fingerprintThrottle)
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	if modified.Load() || b.BookSigV1 == nil {
		if err := synthesizeBookSignatureForBook(p.store, b.ID); err != nil {
			logger.Warn("synthesize book signature", "book_id", b.ID, "error", err)
		}
	}
	return nil
}

// backfillRunOptions builds the RunItems options for one page.
//
// A named function rather than a literal at the call site so that a test runs
// the real options instead of a hand-written copy of them: dropping Concurrency
// here is exactly the regression that made this op sequential for months.
func backfillRunOptions(offset, total int, tally *backfillTally) registry.RunItemsOptions {
	return registry.RunItemsOptions{
		Concurrency:    backfillWorkers(),
		ProgressOffset: offset,
		ProgressTotal:  total,
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d (%s)", offset+i+1, t, tally.summary())
		},
	}
}

// backfillWorkers returns the fingerprint worker-pool size, reading the same
// FP_PARALLEL_WORKERS knob as the rescan op so an operator has one dial for
// fpcalc pressure rather than two that disagree. It sizes both the book pool
// and the shared per-file semaphore.
func backfillWorkers() int {
	if n := config.AppConfig.FPParallelWorkers; n >= 1 && n <= 32 {
		return n
	}
	return 4
}

// fingerprintLengthSec maps config.FingerprintLengthSec onto fpcalc's -length.
// Unset/0 is fpcalc's own 120 s default — the window every stored fingerprint
// was made with — so an unconfigured deployment behaves exactly as before.
// Negative asks for the whole file.
func fingerprintLengthSec() int {
	switch n := config.AppConfig.FingerprintLengthSec; {
	case n == 0:
		return fingerprint.DefaultAnalysisLengthSec
	case n < 0:
		return fingerprint.WholeFileAnalysisLength
	default:
		return n
	}
}

func (p *Plugin) backfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.backfill",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "acoustid",
		DisplayName:     "AcoustID backfill",
		Description:     "Generates raw AcoustID (fpcalc) fingerprints for files that have none, including legacy segment-only rows.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		// Writes verified 2026-08-07: UpdateBookFile (fingerprint columns,
		// whole-row) and GetBookByID→mutate→UpdateBook (whole-row write-back).
		// Declared so the dispatcher's write-set gate serializes this against
		// other Book/BookFile writers (e.g. maintenance.repair-transcribe-status)
		// instead of silently losing fields via concurrent read-modify-write.
		Writes: []sdk.Resource{sdk.ResBooks, sdk.ResBookFiles},
		// No Schedule. The "0 3 * * *" this def carried until 2026-09-12 was
		// read by nothing — OperationDef.Schedule is never evaluated as a cron
		// — so the op never ran unattended. The schedule now lives in the
		// TaskScheduler (internal/scheduler/tasks.go, "acoustid_backfill",
		// disabled by default). The one thing the string DID do was make
		// EnqueueOp dedupe a second request onto the queued one by def id; the
		// op takes no selection params, so that behaviour is kept explicitly.
		DedupeQueuedRuns: true,
		Isolate:          false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:          24 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runBackfill,
	}
}

func (p *Plugin) runBackfill(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}

	if !fingerprint.Available() {
		reporter.Logger().Info("acoustid backfill: no fingerprint backend found, skipping")
		return nil
	}

	if p.store == nil {
		return fmt.Errorf("database store not available")
	}

	var state BackfillParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &state); err != nil {
			reporter.Logger().Error("failed to unmarshal checkpoint", "error", err)
			state = BackfillParams{}
		}
	}

	var tally backfillTally
	if err := p.backfillPages(ctx, reporter, state, &tally); err != nil {
		return err
	}
	if reasons := tally.reasonCounts(); len(reasons) > 0 {
		reporter.Logger().Info("acoustid backfill: ineligible reason breakdown", "reasons", reasons)
	}
	return nil
}

// backfillPages walks the book table one page at a time from the resume
// cursor, running each page through a RunItems pool and checkpointing the
// cursor once the page has fully drained. Split out of runBackfill so tests can
// drive it without a dedup engine or an fpcalc binary.
func (p *Plugin) backfillPages(ctx context.Context, reporter sdk.Reporter, state BackfillParams, tally *backfillTally) error {
	logger := reporter.Logger()
	cursor := resumeCursor(state)

	// Progress denominator only. A failed count leaves the bar unsized; it
	// does not stop the run.
	total, cerr := p.store.CountAllBooks()
	if cerr != nil {
		logger.Warn("acoustid backfill: count books", "error", cerr)
		total = 0
	}
	prog := sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Backfilling fingerprints for %d books…", total))

	fpSem := make(chan struct{}, backfillWorkers())
	done := 0
	restarted := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := p.store.GetAllBooksFullFrom(cursor, backfillPageSize)
		if err != nil {
			return fmt.Errorf("load books after %q: %w", cursor, err)
		}
		if len(page) == 0 {
			// GetAllBooksFullFrom ends iteration on a cursor it cannot find
			// (a book deleted since the checkpoint) rather than restarting.
			// On the first page of a resumed run that would report success
			// having done nothing, so start over instead: the op is
			// idempotent, and redoing work is only slow.
			if done == 0 && cursor != "" && !restarted {
				logger.Warn("acoustid backfill: checkpoint cursor yields no books; restarting from the beginning",
					"cursor", cursor)
				cursor, restarted = "", true
				continue
			}
			break
		}

		err = registry.RunItems(ctx, reporter, page, func(ctx context.Context, b database.Book) error {
			return p.backfillBook(ctx, b, tally, fpSem, logger)
		}, backfillRunOptions(done, total, tally))
		if err != nil {
			return err
		}

		done += len(page)
		cursor = page[len(page)-1].ID
		if err := reporter.Checkpoint(BackfillParams{AfterBookID: cursor}); err != nil {
			logger.Warn("acoustid backfill: checkpoint", "cursor", cursor, "error", err)
		}
		if len(page) < backfillPageSize {
			break
		}
	}

	prog.Done("Acoustid backfill complete: " + tally.summary())
	return nil
}

// fingerprintFileOutcome is the result of attempting to fingerprint a single book_file.
type fingerprintFileOutcome int

const (
	fingerprintOutcomeFingerprinted fingerprintFileOutcome = iota
	fingerprintOutcomeSkipped
	fingerprintOutcomeIneligible
	fingerprintOutcomeFailed
)

// fingerprintThrottle is the sleep between successful fingerprint operations.
const fingerprintThrottle = 10 * time.Millisecond

// fpcalcDecodableExtensions are the containers fpcalc can decode. This is a
// CAPABILITY list, not the library's supported_extensions, and the two
// deliberately disagree in both directions: it carries .alac/.ape/.wv, which
// are not library extensions, and it omits .aax/.aaxc, which are — those are
// DRM-encrypted and this application cannot decode them (internal/audioutil
// .DetectDRM). Routing this through config would both drop three formats and
// queue every Audible file for a fingerprint that can only fail.
var fpcalcDecodableExtensions = map[string]bool{
	".aac":  true,
	".aiff": true,
	".alac": true,
	".ape":  true,
	".flac": true,
	".m4a":  true,
	".m4b":  true,
	".mp3":  true,
	".ogg":  true,
	".opus": true,
	".wav":  true,
	".wma":  true,
	".wv":   true,
}

// fpcalcAvailable reports whether a raw (fpcalc) print can be produced. A
// variable so eligibility tests do not depend on the test host's PATH.
var fpcalcAvailable = fingerprint.WholeFileAvailable

// hasUsableFingerprint reports whether f already carries the fingerprint the
// backfill would produce, so re-running fpcalc would gain nothing.
//
// A raw print, or its memdb-safe proxy AcoustIDFingerprintDurationSec > 0, is
// done. A legacy Seg0-only row is NOT done while fpcalc is available: counting
// it as done (as this check did until 2026-09-12) left ~81k segment-only rows
// with no raw print forever, because the default run skipped them and only
// force=true — which recomputes everything in scope — would reach them. With no
// fpcalc the segment fallback is the best available, so Seg0 counts.
func hasUsableFingerprint(f database.BookFile) bool {
	if len(f.AcoustIDFingerprint) > 0 || f.AcoustIDFingerprintDurationSec > 0 {
		return true
	}
	return f.AcoustIDSeg0 != "" && !fpcalcAvailable()
}

// fingerprintEligibility classifies whether a BookFile is a candidate for
// fingerprinting. Returns the terminal outcome, a human-readable reason (only
// meaningful when outcome == fingerprintOutcomeIneligible), and `true` when
// the caller should stop. Returns the zero value and `false` when the caller
// should proceed with fpcalc. Pure function, no I/O except os.Stat.
func fingerprintEligibility(f database.BookFile, force bool) (fingerprintFileOutcome, string, bool) {
	if !force && hasUsableFingerprint(f) {
		return fingerprintOutcomeSkipped, "", true
	}
	// Permanently-failed files (too short, corrupt, DRM) are skipped unless
	// force=true, which allows an operator to retry specific files intentionally.
	if f.FingerprintFailedAt != nil && !force {
		return fingerprintOutcomeIneligible, "permanent_failure", true
	}
	if f.FilePath == "" {
		return fingerprintOutcomeIneligible, "empty_path", true
	}
	if f.Missing {
		return fingerprintOutcomeIneligible, "marked_missing", true
	}
	if _, ok := fpcalcDecodableExtensions[strings.ToLower(filepath.Ext(f.FilePath))]; !ok {
		return fingerprintOutcomeIneligible, "non_audio_ext:" + strings.ToLower(filepath.Ext(f.FilePath)), true
	}
	if _, err := os.Stat(f.FilePath); err != nil {
		return fingerprintOutcomeIneligible, "file_not_found", true
	}
	return 0, "", false
}

// fingerprintBookFile generates and persists a chromaprint for a single
// book_file row. Prefers fpcalc when available; falls back to 7-segment
// ffmpeg mode when fpcalc is not installed. In segment mode only
// Seg0-Seg6 are written; AcoustIDFingerprint stays empty until fpcalc is
// installed and a force-rescan is run.
func fingerprintBookFile(store pluginStore, f database.BookFile, force bool) fingerprintFileOutcome {
	if outcome, _, stop := fingerprintEligibility(f, force); stop {
		return outcome
	}
	return doFingerprintFile(store, f, force)
}

// doFingerprintFile runs fpcalc/ffmpeg and persists the result. Callers must
// have already confirmed eligibility via fingerprintEligibility. force=true
// additionally clears legacy Seg1..6 fields.
//
// fpcalc analyses the first fingerprintLengthSec() seconds (120 by default),
// not the whole file. The window used is not persisted: recording it needs a
// new BookFile field (and the matching BookFileCore/strip plumbing), which is
// deferred; until then every stored print is the 120 s default unless an
// operator changed fingerprint_length_sec.
func doFingerprintFile(store pluginStore, f database.BookFile, force bool) fingerprintFileOutcome {
	wf, err := fingerprint.FileFingerprintLength(f.FilePath, fingerprintLengthSec())
	if err != nil && !errors.Is(err, fingerprint.ErrNotAvailable) {
		slog.Warn("fingerprint", "path", f.FilePath, "err", err)
		markFingerprintFailure(store, f, "fpcalc_error", err.Error())
		return fingerprintOutcomeFailed
	}

	updated := f

	if err == nil {
		// fpcalc path. Clear any prior failure tombstone.
		updated.AcoustIDFingerprint = wf.Raw
		updated.AcoustIDFingerprintDurationSec = wf.DurationSec
		updated.AcoustIDSeg0 = fingerprint.NormalizeForStorage(fingerprint.DeriveSeg0(wf.Raw))
		updated.FingerprintFailedAt = nil
		updated.FingerprintFailureReason = nil
		updated.FingerprintFailureDetail = nil
		if force {
			updated.AcoustIDSeg1 = ""
			updated.AcoustIDSeg2 = ""
			updated.AcoustIDSeg3 = ""
			updated.AcoustIDSeg4 = ""
			updated.AcoustIDSeg5 = ""
			updated.AcoustIDSeg6 = ""
		}
	} else {
		// Segment fallback (ffmpeg available, fpcalc not installed).
		segs, serr := fingerprint.FileSegments(f.FilePath, f.Duration)
		if serr != nil {
			slog.Warn("fingerprint segments", "path", f.FilePath, "err", serr)
			markFingerprintFailure(store, f, "ffmpeg_error", serr.Error())
			return fingerprintOutcomeFailed
		}
		updated.AcoustIDSeg0 = fingerprint.NormalizeForStorage(segs[0])
		updated.AcoustIDSeg1 = fingerprint.NormalizeForStorage(segs[1])
		updated.AcoustIDSeg2 = fingerprint.NormalizeForStorage(segs[2])
		updated.AcoustIDSeg3 = fingerprint.NormalizeForStorage(segs[3])
		updated.AcoustIDSeg4 = fingerprint.NormalizeForStorage(segs[4])
		updated.AcoustIDSeg5 = fingerprint.NormalizeForStorage(segs[5])
		updated.AcoustIDSeg6 = fingerprint.NormalizeForStorage(segs[6])
		updated.FingerprintFailedAt = nil
		updated.FingerprintFailureReason = nil
		updated.FingerprintFailureDetail = nil
	}

	if err := store.UpdateBookFile(f.ID, &updated); err != nil {
		slog.Warn("fingerprint update", "id", f.ID, "err", err)
		return fingerprintOutcomeFailed
	}
	return fingerprintOutcomeFingerprinted
}

// markFingerprintFailure writes a permanent failure tombstone to the BookFile row so
// future fingerprint scans skip it (unless force=true). The tombstone survives
// service restarts — only a successful fingerprinting run clears it.
func markFingerprintFailure(store pluginStore, f database.BookFile, reason, detail string) {
	now := time.Now()
	updated := f
	updated.FingerprintFailedAt = &now
	updated.FingerprintFailureReason = &reason
	updated.FingerprintFailureDetail = &detail
	if err := store.UpdateBookFile(f.ID, &updated); err != nil {
		slog.Warn("fingerprint: mark failure", "id", f.ID, "reason", reason, "err", err)
	}
}

// synthesizeBookSignatureForBook generates and persists the unified book
// signature for a single book from its files' chromaprint fingerprints.
//
// Uses SynthesizePartialBookSignature so books with partial file coverage
// (some files failed to fingerprint, some still missing whole-file fp) still
// produce a usable book sig with a coverage mask + percentage. The strict
// SynthesizeBookSignature was dropping ~71% of books in production because
// any one failing file caused the whole synthesis to bail.
func synthesizeBookSignatureForBook(store pluginStore, bookID string) error {
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		return fmt.Errorf("get book files: %w", err)
	}

	var orderedFiles []fingerprint.FileWithSegments
	for _, f := range files {
		orderedFiles = append(orderedFiles, fingerprint.FileWithSegments{
			SortOrder: f.TrackNumber,
			Filename:  f.OriginalFilename,
			Segments: fingerprint.FileSegmentData{
				Seg0: f.AcoustIDSeg0,
				Seg1: f.AcoustIDSeg1,
				Seg2: f.AcoustIDSeg2,
				Seg3: f.AcoustIDSeg3,
				Seg4: f.AcoustIDSeg4,
				Seg5: f.AcoustIDSeg5,
				Seg6: f.AcoustIDSeg6,
			},
		})
	}
	fingerprint.SortFilesByOrder(orderedFiles)

	// Build per-file input including Missing flag + EstimatedLen so
	// partial synthesis can keep positional alignment.
	inputs := make([]fingerprint.FileSegmentInput, 0, len(orderedFiles))
	for i, sf := range orderedFiles {
		inp := fingerprint.FileSegmentInput{Segments: sf.Segments}
		// A file is "missing" for synthesis purposes when it has neither
		// the whole-file fp nor seg0.
		src := files[0]
		for _, ff := range files {
			if ff.OriginalFilename == sf.Filename && ff.TrackNumber == sf.SortOrder {
				src = ff
				break
			}
		}
		if len(src.AcoustIDFingerprint) == 0 && sf.Segments.Seg0 == "" {
			inp.Missing = true
			inp.EstimatedLen = fingerprint.EstimateSegmentCount(
				src.Duration, int(src.FileSize), src.BitrateKbps, 0,
			)
		}
		_ = i
		inputs = append(inputs, inp)
	}

	sig, mask, coverage, preLen, err := fingerprint.SynthesizePartialBookSignature(inputs)
	if err != nil {
		if err == fingerprint.ErrIncompleteFingerprint {
			return nil
		}
		return fmt.Errorf("synthesize signature: %w", err)
	}

	now := time.Now()
	book, err := store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("get book: %w", err)
	}

	book.BookSigV1 = &sig
	book.BookSigSegments = &preLen
	book.BookSigBuiltAt = &now
	book.BookSigV1Mask = &mask
	book.BookSigCoveragePct = &coverage

	_, err = store.UpdateBook(book.ID, book)
	if err != nil {
		return fmt.Errorf("update book: %w", err)
	}

	return nil
}
