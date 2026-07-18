// file: internal/itunes/service/path_repair.go
// version: 1.4.0
// guid: 01ad6c79-5f3f-4ee1-a07a-1f4b3a8c0d12
// last-edited: 2026-07-18
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
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// pathRepairWorkers bounds the track-loop worker pool (R-9). 8 is a fixed,
// modest concurrency for network/NAS-bound per-track DB + filesystem work —
// not runtime.NumCPU(), since the loop is I/O-bound (DB reads/writes, disk
// stat), not CPU-bound.
const pathRepairWorkers = 8

// bookWriteLocks serializes applyResolution calls that target the SAME
// bookID across worker goroutines. Two tracks belonging to one multi-file
// audiobook could otherwise both take applyResolution's book-level fallback
// path (GetBookByID → mutate → UpdateBook) concurrently — a classic
// fetch-full-mutate lost-update race (the same shape as the prior
// Author/Series write-back wipe bug). Different bookIDs still run fully in
// parallel; only same-book writes are serialized. Locks are created lazily
// and live only for the duration of one repair run.
type bookWriteLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (l *bookWriteLocks) forBook(bookID string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	m, ok := l.locks[bookID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[bookID] = m
	}
	return m
}

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
	//
	// getTierB is called from multiple worker goroutines below, so the
	// lazy-build itself is guarded by sync.Once — the previous plain
	// `if tierB == nil { tierB = ... }` was a data race (and possible
	// double-build) once the track loop went concurrent.
	var (
		tierBOnce sync.Once
		tierB     tagScanner
	)
	getTierB := func() tagScanner {
		tierBOnce.Do(func() {
			if r.cfg.AudiobookRoot == "" || r.bookIDExtractor == nil {
				tierB = noopTagScanner{}
				return
			}
			_ = progress.Log("info",
				fmt.Sprintf("tier B: scanning audiobook root in parallel root=%s workers=%d",
					r.cfg.AudiobookRoot, runtime.NumCPU()*4), nil)
			scanner := newFSTagScanner(r.cfg.AudiobookRoot, r.bookIDExtractor).
				withProgress(func(done, total int) {
					_ = progress.Log("info",
						fmt.Sprintf("tier B: tag scan progress %d/%d", done, total), nil)
				}, 500)
			tierB = scanner
		})
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

	// Main track loop, parallelized (R-9): each track does a per-track DB
	// read (lookupBookID/resolveTierA/B) and, when resolved outside
	// dry-run, a per-track DB write (applyResolution) — exactly the
	// per-item-DB-I/O shape CLAUDE.md's concurrency mandate requires a
	// bounded worker pool for. Fixed at pathRepairWorkers (8): this is
	// I/O-bound work (DB + disk stat), not CPU-bound, so it isn't sized to
	// runtime.NumCPU().
	//
	// Shared mutable state crossing goroutines, and how each is protected:
	//   - `result` (counters + slices: Missing/AutoResolved/NeedsReview/
	//     Unresolved/Undecodable/Enqueued/ReportPath/Resolutions/
	//     NeedsReviewItems/UnresolvedPIDs/Errors) — every read AND write
	//     goes through resMu. applyResolution itself no longer touches
	//     `result` (see its doc comment) so its bookID-scoped lock and
	//     resMu never nest.
	//   - `scanned` — atomic.AddInt64, so the "every Nth track" progress/
	//     persist cadence never double-fires (each returned value is unique).
	//   - `tierB` (lazy fsTagScanner) — built exactly once via sync.Once
	//     (see getTierB above); was a plain nil-check race before.
	//   - book-level writes — applyResolution does a fetch-full-mutate
	//     (GetBookFiles/GetBookByID → mutate → UpdateBookFile/UpdateBook).
	//     Two tracks that resolve to the SAME bookID (a multi-file
	//     audiobook with more than one missing track) must never run that
	//     fetch-mutate concurrently, so the write is serialized per-bookID
	//     via bookLocks — disjoint books still run fully in parallel.
	var (
		resMu     sync.Mutex
		scanned   int64
		bookLocks bookWriteLocks
		// persistMu serializes the periodic partial-report write (both the
		// report-file os.WriteFile and the DB persist call). `n` is a
		// completion-order counter, not a track index, so with >= 2 workers
		// alive at once, two different `n` values can each satisfy
		// n%persistEvery==0 concurrently; without this lock they'd both call
		// writeReportFile with the SAME path (itunes-repair-<opID>.json) at
		// the same time, tearing the file. persist calls are rare (every
		// 2000 tracks), so this lock sees negligible contention.
		persistMu sync.Mutex
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(pathRepairWorkers)

	for _, track := range lib.Tracks {
		track := track
		g.Go(func() error {
			select {
			case <-gctx.Done():
				return gctx.Err()
			default:
			}

			n := atomic.AddInt64(&scanned, 1)
			if n%progressEvery == 0 {
				resMu.Lock()
				snap := result
				resMu.Unlock()
				_ = progress.UpdateProgress(int(n), len(lib.Tracks),
					fmt.Sprintf("scanning iTunes locations: %d/%d missing=%d auto=%d review=%d unresolved=%d",
						n, len(lib.Tracks), snap.Missing, snap.AutoResolved, snap.NeedsReview, snap.Unresolved))
			}
			if n%persistEvery == 0 {
				// Snapshot the partial result so an interrupted run still
				// leaves something the operator can review. persistMu
				// serializes the actual file/DB I/O below — see its doc
				// comment for why (two different `n` values can both hit
				// this branch concurrently and would otherwise tear the
				// shared report file).
				resMu.Lock()
				snap := result
				resMu.Unlock()
				persistMu.Lock()
				if r.cfg.ReportDir != "" {
					if reportPath, werr := writeReportFile(r.cfg.ReportDir, opID, snap); werr == nil {
						resMu.Lock()
						result.ReportPath = reportPath
						resMu.Unlock()
						snap.ReportPath = reportPath
					}
				}
				_ = persistRepairResult(r.store, opID, snap)
				persistMu.Unlock()
			}
			if !itunes.IsAudiobook(track) {
				return nil
			}
			decoded, derr := itunes.DecodeLocation(track.Location)
			if derr != nil || decoded == "" {
				// M6: count skipped-undecodable locations so the repair summary
				// reflects them instead of silently dropping them.
				resMu.Lock()
				result.Undecodable++
				resMu.Unlock()
				return nil
			}
			if pathExists(decoded) {
				return nil
			}
			resMu.Lock()
			result.Missing++
			resMu.Unlock()

			// Single PID → bookID lookup shared by tier A and tier B.
			bookID := lookupBookID(r.store, track.PersistentID)

			// Tier A: bookID → DB-known on-disk path
			if newPath, ok := resolveTierA(r.store, track.PersistentID, bookID, pathExists); ok {
				resMu.Lock()
				result.AutoResolved++
				result.Resolutions = append(result.Resolutions, resolvedTrack{
					PID: track.PersistentID, OldPath: decoded, NewPath: newPath, Tier: "A", BookID: bookID,
				})
				arCount := result.AutoResolved
				resMu.Unlock()
				if r.activityWriter != nil {
					activity.LogBatch(r.activityWriter, opID, "path-repair", "path-repairer",
						activity.BatchItem{Name: filepath.Base(decoded), Detail: "tier-A"})
				}
				if !dryRun {
					bl := bookLocks.forBook(bookID)
					bl.Lock()
					enq, aerr := r.applyResolution(track.PersistentID, bookID, decoded, newPath)
					bl.Unlock()
					resMu.Lock()
					if enq {
						result.Enqueued++
					}
					if aerr != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("apply tier=A pid=%s: %v", track.PersistentID, aerr))
					}
					resMu.Unlock()
				}
				if arCount%detailLogEvery == 1 {
					_ = progress.Log("info",
						fmt.Sprintf("sample tier=A pid=%s old=%s new=%s action=%s",
							track.PersistentID, decoded, newPath, applyAction(dryRun)), nil)
				}
				return nil
			}

			// Tier B: embedded AUDIOBOOK_ORGANIZER_ID tag scan
			if newPath, ok := resolveTierB(getTierB(), bookID, pathExists); ok {
				resMu.Lock()
				result.AutoResolved++
				result.Resolutions = append(result.Resolutions, resolvedTrack{
					PID: track.PersistentID, OldPath: decoded, NewPath: newPath, Tier: "B", BookID: bookID,
				})
				arCount := result.AutoResolved
				resMu.Unlock()
				if r.activityWriter != nil {
					activity.LogBatch(r.activityWriter, opID, "path-repair", "path-repairer",
						activity.BatchItem{Name: filepath.Base(decoded), Detail: "tier-B"})
				}
				if !dryRun {
					bl := bookLocks.forBook(bookID)
					bl.Lock()
					enq, aerr := r.applyResolution(track.PersistentID, bookID, decoded, newPath)
					bl.Unlock()
					resMu.Lock()
					if enq {
						result.Enqueued++
					}
					if aerr != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("apply tier=B pid=%s: %v", track.PersistentID, aerr))
					}
					resMu.Unlock()
				}
				if arCount%detailLogEvery == 1 {
					_ = progress.Log("info",
						fmt.Sprintf("sample tier=B pid=%s old=%s new=%s action=%s",
							track.PersistentID, decoded, newPath, applyAction(dryRun)), nil)
				}
				return nil
			}

			// Tier C: fuzzy candidates for human review. Never auto-applied.
			info := trackInfo{Title: track.Name, OldBasename: filepath.Base(decoded)}
			candidates := resolveTierC(getTierB().allPaths(), info, tierCThreshold, tierCTopN)
			if len(candidates) > 0 {
				resMu.Lock()
				result.NeedsReview++
				result.NeedsReviewItems = append(result.NeedsReviewItems, needsReviewItem{
					PID:        track.PersistentID,
					OldPath:    decoded,
					Title:      track.Name,
					Candidates: candidates,
				})
				resMu.Unlock()
				return nil
			}

			resMu.Lock()
			result.Unresolved++
			result.UnresolvedPIDs = append(result.UnresolvedPIDs, track.PersistentID)
			resMu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return result, err
	}
	scannedTotal := int(atomic.LoadInt64(&scanned))

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
	_ = progress.UpdateProgress(scannedTotal, scannedTotal, summary)
	return result, nil
}

// applyResolution writes the discovered new path back into the DB
// (BookFile.FilePath/ITunesPath preferred; falls back to Book), records
// a path-history entry, and enqueues the book through the
// WriteBackBatcher so the existing flush loop pushes the corrected
// location to the .itl on its normal cadence.
//
// Returns whether the book was enqueued so the caller can fold that into
// the shared result counters under its own lock — this function must not
// touch iTunesPathRepairResult directly, since the main loop now calls it
// from multiple worker goroutines (see repairWithResult's bookLocks: this
// call is already serialized per-bookID, but the result struct is shared
// across ALL books' goroutines, so any field write here would race).
func (r *PathRepairer) applyResolution(pid, bookID, oldPath, newPath string) (enqueued bool, err error) {
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
				return false, fmt.Errorf("update book_file %s: %w", bf.ID, err)
			}
			updated = true
			break
		}
	}

	// Fall back to book-level fields when no matching BookFile.
	if !updated {
		book, err := r.store.GetBookByID(bookID)
		if err != nil || book == nil {
			return false, fmt.Errorf("get book %s: %w", bookID, err)
		}
		changed := false
		if book.FilePath != newPath {
			book.FilePath = newPath
			changed = true
		}
		if changed {
			if _, err := r.store.UpdateBook(bookID, book); err != nil {
				return false, fmt.Errorf("update book %s: %w", bookID, err)
			}
		}
	}

	if err := r.store.RecordPathChange(&database.BookPathChange{
		BookID:     bookID,
		OldPath:    oldPath,
		NewPath:    newPath,
		ChangeType: "itunes_path_repair",
	}); err != nil {
		return false, fmt.Errorf("record path change: %w", err)
	}

	if r.enqueuer != nil {
		r.enqueuer.Enqueue(bookID)
		return true, nil
	}
	return false, nil
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
