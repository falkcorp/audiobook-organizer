// file: internal/plugins/acoustid/backfill.go
// version: 1.5.0
// guid: f6a7b8c9-d0e1-2345-def0-123456789abc
// last-edited: 2026-06-24

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
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// BackfillParams is the checkpoint state written by reporter.Checkpoint and
// restored into params on ResumeRestart. RunItems drives the loop; progress
// counters are handled by reporter.UpdateProgress.
type BackfillParams struct {
	LastProcessedBookID string `json:"last_processed_book_id,omitempty"`
}

func (p *Plugin) backfillDef() sdk.OperationDef {
	sched := "0 3 * * *"
	return sdk.OperationDef{
		ID:              "acoustid.backfill",
		Plugin:          "acoustid",
		DisplayName:     "AcoustID backfill",
		Description:     "Generates AcoustID fingerprints for files missing acoustid_seg0.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		Schedule:        &sched,
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         24 * time.Hour,
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

	books, err := p.store.GetAllBooks(100000, 0)
	if err != nil {
		reporter.Logger().Error("load books", "error", err)
		return fmt.Errorf("load books: %w", err)
	}

	var startIdx int
	if state.LastProcessedBookID != "" {
		for i, b := range books {
			if b.ID == state.LastProcessedBookID {
				startIdx = i + 1
				break
			}
		}
	}

	var fingerprinted, skipped, failed int
	total := len(books)

	// Rebuild with the real N once it's known.
	prog = sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Backfilling %d books…", total))

	// Nothing to do — still emit Start + Done so the bar never stays at 0/0.
	if total == 0 || startIdx >= total {
		prog.Done(fmt.Sprintf("Acoustid backfill complete: fingerprinted=%d skipped=%d failed=%d",
			fingerprinted, skipped, failed))
		return nil
	}

	slice := books[startIdx:]

	// lastID is captured by both the item fn and CheckpointFn to thread the
	// current book ID through without a shared struct that would need a mutex.
	var lastID string

	err = registry.RunItems(ctx, reporter, slice, func(ctx context.Context, b database.Book) error {
		files, ferr := p.store.GetBookFiles(b.ID)
		if ferr != nil {
			return nil // non-fatal: skip this book
		}

		bookModified := false
		for _, f := range files {
			outcome := fingerprintBookFile(p.store, f, false)
			switch outcome {
			case fingerprintOutcomeFingerprinted:
				fingerprinted++
				bookModified = true
				time.Sleep(fingerprintThrottle)
			case fingerprintOutcomeSkipped:
				skipped++
			case fingerprintOutcomeFailed:
				failed++
			}
		}

		if bookModified || b.BookSigV1 == nil {
			if err := synthesizeBookSignatureForBook(p.store, b.ID); err != nil {
				reporter.Logger().Warn("synthesize book signature", "book_id", b.ID, "error", err)
			}
		}

		lastID = b.ID
		return nil
	}, registry.RunItemsOptions{
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d (fp=%d skip=%d fail=%d)",
				startIdx+i+1, total, fingerprinted, skipped, failed)
		},
		CheckpointFn: func(ctx context.Context) error {
			cp := BackfillParams{LastProcessedBookID: lastID}
			return reporter.Checkpoint(cp)
		},
	})
	if err != nil {
		return err
	}

	prog.Done(fmt.Sprintf("Acoustid backfill complete: fingerprinted=%d skipped=%d failed=%d",
		fingerprinted, skipped, failed))
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

// audioExtensions maps audio file extensions to true (from internal/server/acoustid_backfill.go pattern).
var audioExtensions = map[string]bool{
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
	if _, ok := audioExtensions[strings.ToLower(filepath.Ext(f.FilePath))]; !ok {
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
func fingerprintBookFile(store database.Store, f database.BookFile, force bool) fingerprintFileOutcome {
	if outcome, _, stop := fingerprintEligibility(f, force); stop {
		return outcome
	}
	return doFingerprintFile(store, f, force)
}

// doFingerprintFile runs fpcalc/ffmpeg and persists the result. Callers must
// have already confirmed eligibility via fingerprintEligibility. force=true
// additionally clears legacy Seg1..6 fields.
func doFingerprintFile(store database.Store, f database.BookFile, force bool) fingerprintFileOutcome {
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
func markFingerprintFailure(store database.Store, f database.BookFile, reason, detail string) {
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
func synthesizeBookSignatureForBook(store database.Store, bookID string) error {
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
