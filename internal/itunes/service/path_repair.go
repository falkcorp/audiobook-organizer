// file: internal/itunes/service/path_repair.go
// version: 1.3.0
// guid: 01ad6c79-5f3f-4ee1-a07a-1f4b3a8c0d12
// last-edited: 2026-07-17
//
// PathRepairer dumps the iTunes XML, finds tracks whose Location no
// longer exists on disk, re-discovers the correct path via three tiers
// (PID → DB lookup; embedded AUDIOBOOK_ORGANIZER_PERSISTENT_ID tag
// scan; fuzzy filename + title match), and enqueues each fix through
// the existing WriteBackBatcher so the ITL learns the new locations
// during normal batched write-back.

package itunesservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// PathRepairConfig holds the immutable inputs the repairer needs:
// where to read the iTunes XML, where the audiobook tree lives for
// tier-B/C disk scanning, and where to drop the JSON report file.
type PathRepairConfig struct {
	XMLPath       string
	AudiobookRoot string
	// ReportDir is the directory where each run drops its JSON
	// report. Empty means no file is written; the result still flows
	// inline via UpdateOperationResultData.
	ReportDir string
}

// pathRepairerStore is the narrow slice of the service Store that
// PathRepairer needs. Identical surface to pathReconcilerStore today,
// declared separately so the two operations can evolve independently.
type pathRepairerStore interface {
	database.BookStore
	database.BookFileStore
	database.OperationStore
	database.ExternalIDStore
	database.PathHistoryStore
}

// PathRepairer is the operation worker.
type PathRepairer struct {
	store    pathRepairerStore
	enqueuer Enqueuer
	cfg      PathRepairConfig
	// bookIDExtractor pulls AUDIOBOOK_ORGANIZER_ID from one audio
	// file. Production wires this to metadata.ExtractMetadata.
	// Tests inject deterministic fakes.
	bookIDExtractor bookIDExtractor
	activityWriter  *activity.Writer
}

// SetActivityWriter wires an activity Writer so repairWithResult can emit
// batched per-track resolution events.
func (r *PathRepairer) SetActivityWriter(w *activity.Writer) {
	r.activityWriter = w
}

// newPathRepairer wires a PathRepairer. nil enqueuer skips the
// write-back enqueue step (used by dry-run-only tests).
func newPathRepairer(store pathRepairerStore, enqueuer Enqueuer, cfg PathRepairConfig) *PathRepairer {
	return &PathRepairer{
		store:           store,
		enqueuer:        enqueuer,
		cfg:             cfg,
		bookIDExtractor: extractBookOrganizerID,
	}
}

// extractBookOrganizerID is the production extractor used by the
// fsTagScanner. Reads embedded metadata and returns the
// AUDIOBOOK_ORGANIZER_ID tag value (book-organizer book ID).
func extractBookOrganizerID(audioFilePath string) (string, error) {
	md, err := metadata.ExtractMetadata(audioFilePath, nil)
	if err != nil {
		return "", err
	}
	return md.BookOrganizerID, nil
}

// iTunesPathRepairResult is the per-run tally returned in progress
// logs and the operation result. Field names mirror the dry-run JSON
// payload that callers consume.
type iTunesPathRepairResult struct {
	XMLTracks    int `json:"xml_tracks"`
	Missing      int `json:"missing"`
	AutoResolved int `json:"auto_resolved"`
	NeedsReview  int `json:"needs_review"`
	Unresolved   int `json:"unresolved"`
	Enqueued     int `json:"enqueued"`
	// Undecodable counts audiobook tracks whose iTunes Location could not
	// be decoded at all (M6) — these are skipped before the missing-file
	// check, so without this counter they'd vanish from the report even
	// though their on-disk state is unknown.
	Undecodable      int               `json:"undecodable"`
	DryRun           bool              `json:"dry_run"`
	ReportPath       string            `json:"report_path,omitempty"`
	Resolutions      []resolvedTrack   `json:"resolutions,omitempty"`
	NeedsReviewItems []needsReviewItem `json:"needs_review_items,omitempty"`
	UnresolvedPIDs   []string          `json:"unresolved_pids,omitempty"`
	Errors           []string          `json:"errors,omitempty"`
}

// needsReviewItem is one fuzzy-resolved missing track requiring human
// confirmation. Emitted by tier C; never auto-applied.
type needsReviewItem struct {
	PID        string           `json:"pid"`
	OldPath    string           `json:"old_path"`
	Title      string           `json:"title,omitempty"`
	Candidates []tierCCandidate `json:"candidates"`
}

// Tier C tuning constants. Threshold matches the user-confirmed 0.85
// Jaro-Winkler equivalent on the matcher 0–100 scale.
const (
	tierCThreshold = 85
	tierCTopN      = 3
)

// Repair is the operation body. Wraps repairWithResult so the
// queue-side closure has the (ctx, id, progress) → error signature
// the operations.Queue expects.
func (r *PathRepairer) Repair(ctx context.Context, opID string, dryRun bool, progress operations.ProgressReporter) error {
	_, err := r.repairWithResult(ctx, opID, dryRun, progress)
	return err
}

// resolvedTrack records one resolution decision. Used inside the
// operation worker for logging + report assembly.
type resolvedTrack struct {
	PID     string `json:"pid"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Tier    string `json:"tier"`
	BookID  string `json:"book_id,omitempty"`
}

// repairWithResult is the operation body. Returns the result struct so
// tests can assert on counts; the JSON-encoded form is also persisted
// to the operation row via UpdateOperationResultData.
//
// Named return so the persist+report defer can mutate result.ReportPath
// after the loop's return statement runs.
func (r *PathRepairer) repairWithResult(ctx context.Context, opID string, dryRun bool, progress operations.ProgressReporter) (result iTunesPathRepairResult, err error) {
	if r.store == nil {
		return iTunesPathRepairResult{}, fmt.Errorf("database not initialized")
	}
	if r.cfg.XMLPath == "" {
		return iTunesPathRepairResult{}, fmt.Errorf("iTunes XMLPath not configured")
	}

	_ = progress.Log("info", fmt.Sprintf("iTunes path repair started: xml=%s dry_run=%t", r.cfg.XMLPath, dryRun), nil)

	lib, parseErr := itunes.ParseLibrary(r.cfg.XMLPath)
	if parseErr != nil {
		return iTunesPathRepairResult{}, fmt.Errorf("parse iTunes library: %w", parseErr)
	}

	result = iTunesPathRepairResult{XMLTracks: len(lib.Tracks), DryRun: dryRun}
	_ = progress.UpdateProgress(0, len(lib.Tracks), "scanning iTunes locations")

	// Tier B is built lazily — only constructed if tier A leaves
	// residue. Walking the audiobook root is the expensive step; we
	// don't want to pay it on libraries where every iTunes path
	// resolves cleanly via the DB. When we DO build it, fan out the
	// tag extraction across runtime.NumCPU()*4 workers and report
	// progress every 250 files so the operator sees the long step
	// is actually moving.
	var tierB tagScanner
	tierBBuiltLogged := false
	getTierB := func() tagScanner {
		if tierB == nil {
			if r.cfg.AudiobookRoot == "" || r.bookIDExtractor == nil {
				tierB = noopTagScanner{}
			} else {
				_ = progress.Log("info",
					fmt.Sprintf("tier B: scanning audiobook root in parallel root=%s workers=%d",
						r.cfg.AudiobookRoot, runtime.NumCPU()*4), nil)
				scanner := newFSTagScanner(r.cfg.AudiobookRoot, r.bookIDExtractor).
					withProgress(func(done, total int) {
						_ = progress.Log("info",
							fmt.Sprintf("tier B: tag scan progress %d/%d", done, total), nil)
					}, 500)
				tierB = scanner
			}
		}
		if !tierBBuiltLogged {
			tierBBuiltLogged = true
		}
		return tierB
	}

	const progressEvery = 500   // emit UpdateProgress every N tracks
	const persistEvery = 2000   // persist partial result every N tracks
	const detailLogEvery = 1000 // emit a sample resolution log every N

	// Defer-persist so even on context cancel / timeout we get a
	// useful partial report. The end-of-function path also persists,
	// but the defer is the safety net for early returns.
	defer func() {
		if r.cfg.ReportDir != "" {
			if reportPath, err := writeReportFile(r.cfg.ReportDir, opID, result); err == nil {
				result.ReportPath = reportPath
			}
		}
		_ = persistRepairResult(r.store, opID, result)
	}()

	scanned := 0
	for _, track := range lib.Tracks {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		scanned++
		if scanned%progressEvery == 0 {
			_ = progress.UpdateProgress(scanned, len(lib.Tracks),
				fmt.Sprintf("scanning iTunes locations: %d/%d missing=%d auto=%d review=%d unresolved=%d",
					scanned, len(lib.Tracks), result.Missing, result.AutoResolved, result.NeedsReview, result.Unresolved))
		}
		if scanned%persistEvery == 0 {
			// Snapshot the partial result so an interrupted run still
			// leaves something the operator can review.
			if r.cfg.ReportDir != "" {
				if reportPath, err := writeReportFile(r.cfg.ReportDir, opID, result); err == nil {
					result.ReportPath = reportPath
				}
			}
			_ = persistRepairResult(r.store, opID, result)
		}
		if !itunes.IsAudiobook(track) {
			continue
		}
		decoded, derr := itunes.DecodeLocation(track.Location)
		if derr != nil || decoded == "" {
			// M6: count skipped-undecodable locations so the repair summary
			// reflects them instead of silently dropping them.
			result.Undecodable++
			continue
		}
		if pathExists(decoded) {
			continue
		}
		result.Missing++

		// Single PID → bookID lookup shared by tier A and tier B.
		bookID := lookupBookID(r.store, track.PersistentID)

		// Tier A: bookID → DB-known on-disk path
		if newPath, ok := resolveTierA(r.store, track.PersistentID, bookID, pathExists); ok {
			result.AutoResolved++
			result.Resolutions = append(result.Resolutions, resolvedTrack{
				PID: track.PersistentID, OldPath: decoded, NewPath: newPath, Tier: "A", BookID: bookID,
			})
			if r.activityWriter != nil {
				activity.LogBatch(r.activityWriter, opID, "path-repair", "path-repairer",
					activity.BatchItem{Name: filepath.Base(decoded), Detail: "tier-A"})
			}
			if !dryRun {
				if err := r.applyResolution(track.PersistentID, bookID, decoded, newPath, &result); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("apply tier=A pid=%s: %v", track.PersistentID, err))
				}
			}
			if result.AutoResolved%detailLogEvery == 1 {
				_ = progress.Log("info",
					fmt.Sprintf("sample tier=A pid=%s old=%s new=%s action=%s",
						track.PersistentID, decoded, newPath, applyAction(dryRun)), nil)
			}
			continue
		}

		// Tier B: embedded AUDIOBOOK_ORGANIZER_ID tag scan
		if newPath, ok := resolveTierB(getTierB(), bookID, pathExists); ok {
			result.AutoResolved++
			result.Resolutions = append(result.Resolutions, resolvedTrack{
				PID: track.PersistentID, OldPath: decoded, NewPath: newPath, Tier: "B", BookID: bookID,
			})
			if r.activityWriter != nil {
				activity.LogBatch(r.activityWriter, opID, "path-repair", "path-repairer",
					activity.BatchItem{Name: filepath.Base(decoded), Detail: "tier-B"})
			}
			if !dryRun {
				if err := r.applyResolution(track.PersistentID, bookID, decoded, newPath, &result); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("apply tier=B pid=%s: %v", track.PersistentID, err))
				}
			}
			if result.AutoResolved%detailLogEvery == 1 {
				_ = progress.Log("info",
					fmt.Sprintf("sample tier=B pid=%s old=%s new=%s action=%s",
						track.PersistentID, decoded, newPath, applyAction(dryRun)), nil)
			}
			continue
		}

		// Tier C: fuzzy candidates for human review. Never auto-applied.
		info := trackInfo{Title: track.Name, OldBasename: filepath.Base(decoded)}
		candidates := resolveTierC(getTierB().allPaths(), info, tierCThreshold, tierCTopN)
		if len(candidates) > 0 {
			result.NeedsReview++
			result.NeedsReviewItems = append(result.NeedsReviewItems, needsReviewItem{
				PID:        track.PersistentID,
				OldPath:    decoded,
				Title:      track.Name,
				Candidates: candidates,
			})
			continue
		}

		result.Unresolved++
		result.UnresolvedPIDs = append(result.UnresolvedPIDs, track.PersistentID)
	}

	// The defer at the top of this function handles the final
	// persistRepairResult + writeReportFile call. Just clear the
	// operation state checkpoint here.
	if r.activityWriter != nil {
		activity.FlushOperation(r.activityWriter, opID)
	}
	_ = operations.ClearState(r.store, opID)

	summary := fmt.Sprintf(
		"iTunes path repair complete: tracks=%d missing=%d auto=%d review=%d unresolved=%d undecodable=%d enqueued=%d dry_run=%t",
		result.XMLTracks, result.Missing, result.AutoResolved, result.NeedsReview, result.Unresolved, result.Undecodable, result.Enqueued, result.DryRun,
	)
	_ = progress.Log("info", summary, nil)
	_ = progress.UpdateProgress(scanned, scanned, summary)
	return result, nil
}

// applyResolution writes the discovered new path back into the DB
// (BookFile.FilePath/ITunesPath preferred; falls back to Book), records
// a path-history entry, and enqueues the book through the
// WriteBackBatcher so the existing flush loop pushes the corrected
// location to the .itl on its normal cadence.
func (r *PathRepairer) applyResolution(pid, bookID, oldPath, newPath string, result *iTunesPathRepairResult) error {
	wantITunesPath := metafetch.ComputeITunesPath(newPath)

	// Prefer the matching BookFile when one exists.
	updated := false
	if files, err := r.store.GetBookFiles(bookID); err == nil {
		for _, bf := range files {
			if bf.ITunesPersistentID != pid {
				continue
			}
			if bf.FilePath == newPath && bf.ITunesPath == wantITunesPath {
				updated = true
				break
			}
			bf.FilePath = newPath
			if wantITunesPath != "" {
				bf.ITunesPath = wantITunesPath
			}
			if err := r.store.UpdateBookFile(bf.ID, &bf); err != nil {
				return fmt.Errorf("update book_file %s: %w", bf.ID, err)
			}
			updated = true
			break
		}
	}

	// Fall back to book-level fields when no matching BookFile.
	if !updated {
		book, err := r.store.GetBookByID(bookID)
		if err != nil || book == nil {
			return fmt.Errorf("get book %s: %w", bookID, err)
		}
		changed := false
		if book.FilePath != newPath {
			book.FilePath = newPath
			changed = true
		}
		if changed {
			if _, err := r.store.UpdateBook(bookID, book); err != nil {
				return fmt.Errorf("update book %s: %w", bookID, err)
			}
		}
	}

	if err := r.store.RecordPathChange(&database.BookPathChange{
		BookID:     bookID,
		OldPath:    oldPath,
		NewPath:    newPath,
		ChangeType: "itunes_path_repair",
	}); err != nil {
		return fmt.Errorf("record path change: %w", err)
	}

	if r.enqueuer != nil {
		r.enqueuer.Enqueue(bookID)
		result.Enqueued++
	}
	return nil
}

// pathExists is the production existsFn for the resolver. Test code
// may inject a fake.
func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// applyAction returns the human-readable action label for a single
// repair, distinguishing dry-run reports from real writes.
func applyAction(dryRun bool) string {
	if dryRun {
		return "report"
	}
	return "enqueue"
}

// persistRepairResult JSON-encodes the result and stores it on the
// operation row so the API can fetch the report after the run.
func persistRepairResult(store database.OperationStore, opID string, result iTunesPathRepairResult) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return store.UpdateOperationResultData(opID, string(b))
}

// writeReportFile persists the result to <reportDir>/itunes-repair-<opID>.json.
// Returns the absolute path of the written file.
func writeReportFile(reportDir, opID string, result iTunesPathRepairResult) (string, error) {
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", reportDir, err)
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("itunes-repair-%s.json", opID)
	out := filepath.Join(reportDir, name)
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return "", err
	}
	return out, nil
}
