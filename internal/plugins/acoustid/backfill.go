// file: internal/plugins/acoustid/backfill.go
// version: 1.11.0
// guid: f6a7b8c9-d0e1-2345-def0-123456789abc
// last-edited: 2026-09-07

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// BackfillParams is the checkpoint state written by reporter.Checkpoint and
// restored into params on ResumeRestart. RunItems drives the loop; progress
// counters are handled by reporter.UpdateProgress.
type BackfillParams struct {
	// LastProcessedBookID is the pre-2026-09-07 checkpoint format: the ID of the
	// last book finished by a strictly sequential run. It is still READ so a
	// checkpoint written by an older binary resumes instead of starting over, but
	// it is no longer written. Under a worker pool "the last book finished" is not
	// a resume point at all — workers complete out of order, so the newest
	// finished ID can sit above books that are still running.
	LastProcessedBookID string `json:"last_processed_book_id,omitempty"`

	// Watermark is the contiguous-completion watermark from RunItems: the number
	// of books from the START of the collection that are ALL finished. Everything
	// below it is provably done regardless of the order the pool completed in.
	Watermark int `json:"watermark,omitempty"`

	// WatermarkBookID is the ID of the book at Watermark-1, stored so the index
	// can be validated on resume. An index alone is meaningless against a
	// collection that changed between runs: GetAllBooksFullFrom is ID-ordered, so
	// a book imported with an earlier-sorting ID shifts every later position down
	// one and a bare index would skip a book permanently. If the ID at that
	// position no longer matches, the run restarts from the beginning — the
	// operation is idempotent, so redoing work is only slow, while skipping it
	// leaves files unfingerprinted with nothing to revisit them.
	WatermarkBookID string `json:"watermark_book_id,omitempty"`
}

// resolveResumePoint decides where a resumed run restarts, returning the index to
// resume from and a non-empty reason when a stored checkpoint had to be rejected.
//
// Kept separate from runBackfill so the decision can be tested against a shifted
// collection without a fingerprint backend, a store, or an operations reporter.
func resolveResumePoint(books []database.Book, state BackfillParams) (int, string) {
	if state.Watermark > 0 {
		switch {
		case state.Watermark > len(books):
			return 0, fmt.Sprintf(
				"checkpoint watermark %d is beyond the %d books now present; restarting from the beginning",
				state.Watermark, len(books))
		case state.WatermarkBookID == "":
			return 0, fmt.Sprintf(
				"checkpoint watermark %d carries no book ID to validate against; restarting from the beginning",
				state.Watermark)
		case books[state.Watermark-1].ID != state.WatermarkBookID:
			return 0, fmt.Sprintf(
				"checkpoint watermark %d was written for book %s but that position now holds %s; the collection shifted, restarting from the beginning",
				state.Watermark, state.WatermarkBookID, books[state.Watermark-1].ID)
		}
		return state.Watermark, ""
	}

	// Legacy checkpoint from a sequential run: locate the ID rather than trusting
	// any index, since none was stored.
	if state.LastProcessedBookID != "" {
		for i, b := range books {
			if b.ID == state.LastProcessedBookID {
				return i + 1, ""
			}
		}
		return 0, fmt.Sprintf(
			"checkpoint names book %s, which is no longer in the collection; restarting from the beginning",
			state.LastProcessedBookID)
	}

	return 0, ""
}

// backfillTally counts per-file outcomes across the whole run.
//
// The fields are atomic rather than plain ints because they are written from
// every worker goroutine AND read from the RunItems Label closure, which
// run_items.go invokes inside each worker too. Under a plain int the reads are a
// race and the increments are a lost update: two workers read the same value,
// both add one, and one file's outcome disappears from the total. The same shape
// cost 163 of 200 API-key usage records before it was fixed on 2026-09-07.
type backfillTally struct {
	fingerprinted atomic.Int64
	skipped       atomic.Int64
	failed        atomic.Int64
}

// backfillBook fingerprints every file of one book and records the outcomes.
//
// Split out of the RunItems callback so the concurrency claim is testable: as an
// inline closure this body could only be reached by running the whole op, which
// needs a real fpcalc binary and so cannot run in CI. As a method it can be
// driven from N goroutines against a fake store.
//
// Always returns nil. A book whose files cannot be listed is skipped rather than
// failing the run — one unreadable book must not abort a nightly whole-library
// pass — and the signature-synthesis error is logged, matching the behaviour
// before the split.
func (p *Plugin) backfillBook(b database.Book, tally *backfillTally, logger *slog.Logger) error {
	files, ferr := p.store.GetBookFiles(b.ID)
	if ferr != nil {
		return nil // non-fatal: skip this book
	}

	bookModified := false
	for _, f := range files {
		switch fingerprintBookFile(p.store, f, false) {
		case fingerprintOutcomeFingerprinted:
			tally.fingerprinted.Add(1)
			bookModified = true
			// Now a PER-WORKER pause, so the aggregate rate is roughly
			// workers/throttle rather than 1/throttle. That is the point — the
			// throttle exists to leave headroom between fpcalc invocations, not
			// to cap total throughput at one core.
			time.Sleep(fingerprintThrottle)
		case fingerprintOutcomeSkipped:
			tally.skipped.Add(1)
		case fingerprintOutcomeFailed:
			tally.failed.Add(1)
		}
	}

	if bookModified || b.BookSigV1 == nil {
		if err := synthesizeBookSignatureForBook(p.store, b.ID); err != nil {
			logger.Warn("synthesize book signature", "book_id", b.ID, "error", err)
		}
	}

	return nil
}

// backfillRunOptions builds the RunItems options for a backfill pass.
//
// A named function rather than a literal at the call site so that a test can run
// the real options instead of a hand-written copy of them. A test that builds its
// own options proves only that RunItems is concurrent; this one stays honest if
// Concurrency is ever dropped here, which is precisely the regression that made
// this op sequential for months.
func backfillRunOptions(books []database.Book, startIdx int, tally *backfillTally, reporter sdk.Reporter) registry.RunItemsOptions {
	return registry.RunItemsOptions{
		Concurrency: backfillWorkers(),
		ResumeFrom:  startIdx,
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d (fp=%d skip=%d fail=%d)",
				i+1, t, tally.fingerprinted.Load(), tally.skipped.Load(), tally.failed.Load())
		},
		// Once per 50 books rather than per book. CheckpointStateFn calls are
		// serialized, so checkpointing every item would funnel the whole pool
		// through one lock plus a store write and give back much of the
		// parallelism. 50 books is at most a few minutes of redone work on
		// resume, against an op that takes hours.
		CheckpointEvery: 50,
		// CheckpointStateFn, not CheckpointFn: the latter is documented as
		// sequential-only, and the "last book finished" it was built around is
		// not a resume point once workers complete out of order. The watermark
		// is the contiguous completed prefix, so every book below it is done no
		// matter what order the pool finished in.
		CheckpointStateFn: func(ctx context.Context, watermark int) error {
			cp := BackfillParams{Watermark: watermark}
			if watermark > 0 && watermark <= len(books) {
				cp.WatermarkBookID = books[watermark-1].ID
			}
			return reporter.Checkpoint(cp)
		},
	}
}

// backfillWorkers returns the fingerprint worker-pool size, reading the same
// FP_PARALLEL_WORKERS knob as the rescan op so an operator has one dial for
// fpcalc pressure rather than two that disagree.
func backfillWorkers() int {
	if n := config.AppConfig.FPParallelWorkers; n >= 1 && n <= 32 {
		return n
	}
	return 4
}

func (p *Plugin) backfillDef() sdk.OperationDef {
	sched := "0 3 * * *"
	return sdk.OperationDef{
		ID:              "acoustid.backfill",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "acoustid",
		DisplayName:     "AcoustID backfill",
		Description:     "Generates AcoustID fingerprints for files missing acoustid_seg0.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		// Writes verified 2026-08-07: UpdateBookFile (fingerprint columns,
		// whole-row) and GetBookByID→mutate→UpdateBook (whole-row write-back).
		// Declared so the dispatcher's write-set gate serializes this against
		// other Book/BookFile writers (e.g. maintenance.repair-transcribe-status)
		// instead of silently losing fields via concurrent read-modify-write.
		Writes:   []sdk.Resource{sdk.ResBooks, sdk.ResBookFiles},
		Schedule: &sched,
		Isolate:  false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:  24 * time.Hour,
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

	// Start with N=0 until we've loaded books and know the real total.
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Loading books for fingerprint backfill...")

	books, err := p.store.GetAllBooksFullFrom("", 0)
	if err != nil {
		reporter.Logger().Error("load books", "error", err)
		return fmt.Errorf("load books: %w", err)
	}

	startIdx, resumeReason := resolveResumePoint(books, state)
	if resumeReason != "" {
		// Logged at Warn, not swallowed: silently restarting a nightly job that
		// had already fingerprinted most of the library looks identical to the
		// job simply being slow, and it is the only signal that a checkpoint was
		// discarded.
		reporter.Logger().Warn("acoustid backfill: discarding checkpoint", "reason", resumeReason)
	}

	var tally backfillTally
	total := len(books)

	// Rebuild with the real N once it's known.
	prog = sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Backfilling %d books…", total))

	// Nothing to do — still emit Start + Done so the bar never stays at 0/0.
	if total == 0 || startIdx >= total {
		prog.Done(fmt.Sprintf("Acoustid backfill complete: fingerprinted=%d skipped=%d failed=%d",
			tally.fingerprinted.Load(), tally.skipped.Load(), tally.failed.Load()))
		return nil
	}

	// The FULL collection is handed to RunItems, not books[startIdx:]. ResumeFrom
	// does the slicing itself AND shifts ProgressOffset by the same amount, so
	// pre-slicing here and also setting ProgressOffset would shift the bar twice.
	// It also keeps the checkpoint watermark an index into the collection the
	// checkpoint is validated against, rather than into a slice whose origin is
	// only recoverable by remembering what it was cut from.
	err = registry.RunItems(ctx, reporter, books, func(ctx context.Context, b database.Book) error {
		return p.backfillBook(b, &tally, reporter.Logger())
	}, backfillRunOptions(books, startIdx, &tally, reporter))
	if err != nil {
		return err
	}

	prog.Done(fmt.Sprintf("Acoustid backfill complete: fingerprinted=%d skipped=%d failed=%d",
		tally.fingerprinted.Load(), tally.skipped.Load(), tally.failed.Load()))
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

// fingerprintEligibility classifies whether a BookFile is a candidate for
// fingerprinting. Returns the terminal outcome, a human-readable reason (only
// meaningful when outcome == fingerprintOutcomeIneligible), and `true` when
// the caller should stop. Returns the zero value and `false` when the caller
// should proceed with fpcalc. Pure function, no I/O except os.Stat.
func fingerprintEligibility(f database.BookFile, force bool) (fingerprintFileOutcome, string, bool) {
	// AcoustIDFingerprintDurationSec > 0 is the memdb-safe proxy for "has whole-file fp"
	// (AcoustIDFingerprint itself is stripped from memdb rows by stripBookFileForMemdb).
	if (len(f.AcoustIDFingerprint) > 0 || f.AcoustIDSeg0 != "" || f.AcoustIDFingerprintDurationSec > 0) && !force {
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
// book_file row. Prefers whole-file mode (fpcalc) when available; falls back
// to 7-segment ffmpeg mode when fpcalc is not installed. In segment mode only
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
func doFingerprintFile(store pluginStore, f database.BookFile, force bool) fingerprintFileOutcome {
	wf, err := fingerprint.FileWholeFingerprint(f.FilePath)
	if err != nil && !errors.Is(err, fingerprint.ErrNotAvailable) {
		slog.Warn("fingerprint", "path", f.FilePath, "err", err)
		markFingerprintFailure(store, f, "fpcalc_error", err.Error())
		return fingerprintOutcomeFailed
	}

	updated := f

	if err == nil {
		// Whole-file path (fpcalc available). Clear any prior failure tombstone.
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
