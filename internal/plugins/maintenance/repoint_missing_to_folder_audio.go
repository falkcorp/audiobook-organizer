// file: internal/plugins/maintenance/repoint_missing_to_folder_audio.go
// version: 1.0.0
// guid: 5169412c-6469-4f70-9073-b544025dd08e
// last-edited: 2026-09-25

// Package maintenance — maintenance.repoint-missing-to-folder-audio.
//
// WHY: about 7,000 ABS-visible books (primary + library_state organized) read
// duration 0 because their active book_file rows point at audio that is no
// longer on disk. The usual shape is a chapter-split book whose chapters were
// later consolidated into one file in the same folder:
//
//	row says   …/Eldest/Eldest/Eldest - 330.mp3   (gone; 330 such rows)
//	folder has …/Eldest/Eldest/Eldest.m4b         (one file)
//
// The rows still say Missing=false (the files API's file_exists is only
// !Missing, it never stats), and ABS sums the per-file durations, so the book
// reads 0. maintenance.missing-file-repoint cannot reach these: it derives a
// target from the row's own name (track-slash shape) or from a single-file
// book's path with an exact size match, and a consolidated file matches
// neither. This op looks at what is ACTUALLY in the book's folder instead.
//
// Per book (owner decision 2026-09-25, "Repoint to the real file"):
//
//  1. Stat every active (Missing=false) row. Only os.IsNotExist is "missing".
//  2. Pick the book's folder: the one existing directory of its missing rows,
//     else book.file_path (a directory, or the directory of a file). It must
//     lie under the library root and outside every iTunes root.
//  3. List the audio files in that folder (non-recursive) and drop every file
//     another live book already references (BookFilesAtPath for rows,
//     LiveBookIDsAtPath for books that own a file through book.file_path) and
//     every file this book's own rows already point at.
//  4. Decide:
//     - consolidated: every active row is missing, and the folder holds exactly
//     one unowned audio file whose size is plausible for the rows' total
//     size. ONE row (the first by disc, track, path) is repointed to the file;
//     the others are marked Missing=true.
//     - size-match: every missing row has exactly one unowned file of exactly
//     its recorded size, and no two rows share a file. Each row is
//     repointed; nothing else changes.
//     - ambiguous: anything else, with a reason. Nothing is written.
//  5. Fill the repointed rows' Duration with bookfileaudio.EnsureDuration (a
//     bounded header read, no decode), then write the book's rows in ONE
//     UpdateBookFiles call, which recomputes the book's aggregates.
//
// SUPERSEDED ROWS: the book_file model has no "superseded" field. A row that no
// longer describes a file on disk is represented the same way
// maintenance.mark-missing-files represents it: Missing=true, every other field
// untouched. Rows are NEVER deleted.
//
// Guards, each a reason in the report:
//   - folder-shared-with-other-book: the folder holds audio another live book
//     references. A lone unowned file beside another book's chapters is a
//     stray (a 284 KB "53.mp3" beside "Core Construction - NN.mp3" on prod),
//     not the consolidation of this book.
//   - size-implausible / size-unverifiable: the one candidate's size is far
//     from the missing rows' total, or no row recorded a size.
//   - superseded-rows-carry-duration: a row that would be marked missing has a
//     known duration. ABS sums EVERY row including missing ones, and
//     database.ComputeBookRuntime counts a missing row with no content twin, so
//     the book would read the chapters' time PLUS the consolidated file's.
//   - target-collision: two books plan the same target. Decided in a serial
//     pass over every book's plan, so the outcome never depends on scheduling.
//   - itunes-linked / itunes-path: iTunes rows and paths are never written.
//
// CONCURRENCY: three phases, like missing-file-repoint. (1) a parallel stat of
// every scoped book's active rows; (2) a parallel per-book plan (directory
// listing, ownership lookups, header probe), read-only; a serial collision
// pass; (3) a parallel per-book write. Items are books, targets are disjoint
// after the collision pass, so no two workers ever touch the same row or file.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	rfOpName = "repoint-missing-to-folder-audio"

	// rfStatWorkers matches the stat sweeps of the other missing-file ops.
	rfStatWorkers = missingFileStatConcurrency
	// rfPlanWorkers bounds the per-book plan: a directory listing, a few index
	// lookups and one header probe per book. Header probes spawn ffprobe, so
	// this stays well below the stat pool.
	rfPlanWorkers = 4
	// rfWriteWorkers bounds the write phase, one book per item.
	rfWriteWorkers = 4

	// rfSizeRatioMin / rfSizeRatioMax bound candidate size ÷ the missing rows'
	// total recorded size for a consolidation. Wide on purpose: a re-encode
	// from 128 kbps mp3 chapters to a 64 kbps m4b halves the bytes. A stray
	// chapter beside the book is orders of magnitude outside it.
	rfSizeRatioMin = 0.2
	rfSizeRatioMax = 5.0

	// rfMaxChangesPerBook caps the per-row changes listed in the op result;
	// the TSV report carries every row.
	rfMaxChangesPerBook = 20

	rfDecisionConsolidated = "consolidated"
	rfDecisionSizeMatch    = "size-match"
	rfDecisionAmbiguous    = "ambiguous"

	rfActionRepoint     = "repoint"
	rfActionMarkMissing = "mark-missing"

	rfOutcomeApplied = "applied"
	rfOutcomeChanged = "changed_since_plan"
	rfOutcomeFailed  = "failed"
	rfOutcomeSkipped = "not_attempted"
)

type rfParams struct {
	// DryRun defaults to TRUE. dry_run is accepted as an alias; both present
	// and disagreeing is an error (same contract as duration-backfill).
	DryRun      *bool `json:"dryRun,omitempty"`
	DryRunSnake *bool `json:"dry_run,omitempty"`
	// BookIDs limits the run to these books (still subject to the
	// primary + organized scope). bookIds is accepted as an alias.
	BookIDs      []string `json:"book_ids,omitempty"`
	BookIDsCamel []string `json:"bookIds,omitempty"`
	// Limit caps how many books with missing rows are planned (and, on apply,
	// written), taking the lowest book IDs first so a re-run is stable.
	// 0 = no cap.
	Limit int `json:"limit,omitempty"`
	// ReportPath overrides where the per-row TSV lands.
	ReportPath string `json:"reportPath,omitempty"`
}

func (p rfParams) dryRun() (bool, error) {
	if p.DryRun != nil && p.DryRunSnake != nil && *p.DryRun != *p.DryRunSnake {
		return true, fmt.Errorf("dryRun=%v and dry_run=%v disagree; send one", *p.DryRun, *p.DryRunSnake)
	}
	if p.DryRun != nil {
		return *p.DryRun, nil
	}
	if p.DryRunSnake != nil {
		return *p.DryRunSnake, nil
	}
	return true, nil
}

func (p rfParams) bookIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range append(append([]string(nil), p.BookIDs...), p.BookIDsCamel...) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// rfChange is one row this op changes (or would change).
type rfChange struct {
	FileID  string `json:"file_id"`
	Action  string `json:"action"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path,omitempty"`
}

// rfBookResult is one book's decision, in the op result's "books" list.
type rfBookResult struct {
	BookID   string `json:"book_id"`
	Title    string `json:"title,omitempty"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	Folder   string `json:"folder,omitempty"`
	// ActiveRows / MissingRows count the book's Missing=false rows and those
	// of them whose file is gone.
	ActiveRows  int `json:"active_rows"`
	MissingRows int `json:"missing_rows"`
	// Candidates are the unowned audio files the decision was made over.
	Candidates []string `json:"candidates,omitempty"`
	// SizeRatio is candidate size ÷ the missing rows' total recorded size
	// (consolidation only), so the plausibility band can be tuned.
	SizeRatio float64 `json:"size_ratio,omitempty"`
	// NewPaths are the files the book's rows now point at.
	NewPaths []string `json:"new_paths,omitempty"`
	// DurationSec is the summed duration of the repointed files as probed
	// (0 when the header gave no real duration); DurationSource says where
	// EnsureDuration took it from.
	DurationSec    int    `json:"duration_sec,omitempty"`
	DurationSource string `json:"duration_source,omitempty"`
	// Changes lists up to rfMaxChangesPerBook rows; ChangeCount is the total.
	Changes     []rfChange `json:"changes,omitempty"`
	ChangeCount int        `json:"change_count,omitempty"`
	Outcome     string     `json:"outcome,omitempty"`
	Error       string     `json:"error,omitempty"`

	// plan is the full write plan; not serialized.
	plan *rfPlan
}

type rfReport struct {
	DryRun bool `json:"dry_run"`
	// ScopedBooks is how many primary + organized books were examined;
	// BooksWithMissing how many had at least one active row whose file is gone;
	// Planned how many of those were decided (Limit may cap it).
	ScopedBooks       int            `json:"scoped_books"`
	BooksWithMissing  int            `json:"books_with_missing"`
	Planned           int            `json:"planned"`
	CappedAt          int            `json:"capped_at,omitempty"`
	ByDecision        map[string]int `json:"by_decision"`
	ByReason          map[string]int `json:"by_reason"`
	ByOutcome         map[string]int `json:"by_outcome,omitempty"`
	RowsRepointed     int            `json:"rows_repointed"`
	RowsMarkedMissing int            `json:"rows_marked_missing"`
	ReportPath        string         `json:"report_path,omitempty"`
	Aborted           string         `json:"aborted,omitempty"`
	Books             []rfBookResult `json:"books"`
}

func (r *rfReport) summary() string {
	mode := "DRY RUN"
	if !r.DryRun {
		mode = "APPLIED"
	}
	s := fmt.Sprintf("%s scoped=%d with_missing=%d planned=%d decisions=%v reasons=%v outcomes=%v repointed=%d marked_missing=%d",
		mode, r.ScopedBooks, r.BooksWithMissing, r.Planned, r.ByDecision, r.ByReason, r.ByOutcome,
		r.RowsRepointed, r.RowsMarkedMissing)
	if r.Aborted != "" {
		s += " ABORTED: " + r.Aborted
	}
	return s
}

// rfPlan is what the write phase does to one book.
type rfPlan struct {
	// Repoints maps a row ID to its new path. Consolidation fills one entry.
	Repoints map[string]string
	// OldPaths maps each planned row ID to the path the plan saw, so the
	// write can refuse a row that changed since.
	OldPaths map[string]string
	// MarkMissing are rows to flag Missing=true (consolidation only).
	MarkMissing []string
	// ResetContent is true when the repointed row's bytes are a DIFFERENT
	// file (consolidation with a size change): every field that describes the
	// old bytes is cleared before the duration is filled.
	ResetContent bool
	// Probes holds the header read of each target, keyed by path.
	Probes map[string]mediainfo.MediaInfo
}

// rfStore is the narrow store the op needs. Every method is on
// database.Store, so the production indexedStore satisfies it.
type rfStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	UpdateBookFiles(ctx context.Context, files []*database.BookFile, afterRow func(i int, applied bool)) (int, error)
}

// rfEnv is everything one run reads besides its params.
type rfEnv struct {
	store       rfStore
	rootDir     string
	itunesRoots []string
	scan        ScanController
	queue       OpQueueReader
}

func (p *Plugin) repointMissingToFolderAudioDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.repoint-missing-to-folder-audio",
		DisplayName: "Repoint missing book files to the audio in their folder",
		Description: "For primary + organized books whose active book_file rows point at files that no longer exist, " +
			"repoints them at the audio actually in the book's folder: N chapter rows collapsed into one file keep ONE " +
			"row (repointed) and mark the rest missing; rows with an exact size match each repoint 1:1. Anything " +
			"ambiguous (several candidates, none, a file another book references, iTunes) is reported and left alone. " +
			"Never deletes a row. Fills Duration with a header read. DRY-RUN BY DEFAULT: {\"dryRun\":false} to write; " +
			"optional book_ids and limit.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.repoint-missing-to-folder-audio",
		// Same write-set as missing-file-repoint: never beside another op that
		// rewrites book_file rows (fs-regroup-xml looks a path up and then
		// creates a row for it).
		Writes: []sdk.Resource{sdk.ResBookFiles},
		// A write op interrupted midway must not resume itself; a re-run is
		// safe (a repointed row is no longer missing, so it is not selected).
		ResumePolicy: sdk.ResumeDrop,
		Cancellable:  true,
		// The prologue (every book_file row, every book page) runs before the
		// first RunItems tick, like missing-file-repoint's; all three phases
		// then stamp liveness per item.
		Liveness:     sdk.LivenessRunItems,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runRepointMissingToFolderAudio,
	}
}

func (p *Plugin) runRepointMissingToFolderAudio(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params rfParams
	if err := decodeStrictParams(raw, &params); err != nil {
		return fmt.Errorf("%s: decode params: %w", rfOpName, err)
	}
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	store, ok := any(ops).(rfStore)
	if !ok {
		return fmt.Errorf("%s: store cannot list book_file rows at a path; refusing to run", rfOpName)
	}
	// Fail closed like repoint-unrecorded-renames: with no resolvable iTunes
	// roots the op cannot prove a path is outside them.
	roots, err := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if err != nil {
		return fmt.Errorf("%s: resolve iTunes roots: %w", rfOpName, err)
	}
	reportPath, err := p.resolveReportPath(params.ReportPath, opReportFileName(reporter, rfOpName))
	if err != nil {
		return fmt.Errorf("%s: %w", rfOpName, err)
	}
	env := rfEnv{store: store, rootDir: p.deps.RootDir(), itunesRoots: roots,
		scan: p.deps, queue: p.deps.OperationQueueStore()}
	report, runErr := repointMissingToFolderAudio(ctx, env, params, reporter)
	if report != nil {
		if len(report.Books) > 0 {
			if wErr := writeRFReport(reportPath, report.Books); wErr != nil {
				reporter.Logger().Error(rfOpName+": FAILED to write the per-row report", "path", reportPath, "err", wErr)
			} else {
				report.ReportPath = reportPath
			}
		}
		if sErr := registry.ReporterSetResult(reporter, report); sErr != nil {
			reporter.Logger().Warn(rfOpName+": result not persisted", "err", sErr)
		}
		reporter.Logger().Info(rfOpName+" complete", "summary", report.summary())
	}
	return runErr
}

// rfBook is one scoped book and its active rows.
type rfBook struct {
	core database.BookCore
	rows []database.BookFileCore // active (Missing=false) rows
	// missing[i] is true when rows[i]'s file is gone; unreadable when stat
	// failed for any other reason.
	missing    []bool
	unreadable bool
}

func (b *rfBook) missingCount() int {
	n := 0
	for _, m := range b.missing {
		if m {
			n++
		}
	}
	return n
}

func repointMissingToFolderAudio(ctx context.Context, env rfEnv, params rfParams, reporter sdk.Reporter) (*rfReport, error) {
	log := reporter.Logger()
	dryRun, err := params.dryRun()
	report := &rfReport{DryRun: true, ByDecision: map[string]int{}, ByReason: map[string]int{}, ByOutcome: map[string]int{}}
	if err != nil {
		return report, fmt.Errorf("%s: %w", rfOpName, err)
	}
	report.DryRun = dryRun
	if params.Limit < 0 {
		return report, fmt.Errorf("%s: limit must be >= 0", rfOpName)
	}
	if strings.TrimSpace(env.rootDir) == "" {
		return report, fmt.Errorf("%s: root_dir is not set; cannot tell which folders are in the library", rfOpName)
	}

	// --- scope: primary + organized books, and their active rows ---
	books, err := rfScopedBooks(env.store, params.bookIDs())
	if err != nil {
		return report, err
	}
	liveBooks := books.live
	files, err := env.store.GetAllBookFilesCore()
	if err != nil {
		return report, fmt.Errorf("%s: load book files: %w", rfOpName, err)
	}
	byBook := map[string]*rfBook{}
	for _, c := range books.scoped {
		byBook[c.ID] = &rfBook{core: c}
	}
	for i := range files {
		if b := byBook[files[i].BookID]; b != nil && !files[i].Missing && strings.TrimSpace(files[i].FilePath) != "" {
			b.rows = append(b.rows, files[i])
		}
	}
	scoped := make([]*rfBook, 0, len(byBook))
	for _, b := range byBook {
		if len(b.rows) > 0 {
			scoped = append(scoped, b)
		}
	}
	sort.Slice(scoped, func(i, j int) bool { return scoped[i].core.ID < scoped[j].core.ID })
	report.ScopedBooks = len(scoped)
	log.Info(rfOpName+" start", "dry_run", dryRun, "scoped_books", len(scoped), "limit", params.Limit)

	// --- phase 1: stat every active row (parallel, I/O bound) ---
	var statDone atomic.Int64
	err = registry.RunItems(ctx, reporter, scoped, func(_ context.Context, b *rfBook) error {
		defer statDone.Add(1)
		b.missing = make([]bool, len(b.rows))
		for i := range b.rows {
			_, serr := os.Stat(b.rows[i].FilePath)
			switch {
			case serr == nil:
			case os.IsNotExist(serr):
				b.missing[i] = true
			default:
				b.unreadable = true
			}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: rfStatWorkers,
		ErrMode:     registry.ErrModeCollect,
		Label: func(_, t int) string {
			return fmt.Sprintf("Stat: %d/%d books", statDone.Load(), t)
		},
	})
	if err != nil {
		return report, fmt.Errorf("%s: stat sweep: %w", rfOpName, err)
	}
	var affected []*rfBook
	for _, b := range scoped {
		if b.missingCount() > 0 {
			affected = append(affected, b)
		}
	}
	report.BooksWithMissing = len(affected)
	if params.Limit > 0 && len(affected) > params.Limit {
		report.CappedAt = params.Limit
		affected = affected[:params.Limit]
	}
	report.Planned = len(affected)

	// --- phase 2: plan each book (parallel, read-only) ---
	results := make([]rfBookResult, len(affected))
	idx := map[*rfBook]int{}
	for i, b := range affected {
		idx[b] = i
	}
	var planDone atomic.Int64
	var ownerCheckDown atomic.Bool
	err = registry.RunItems(ctx, reporter, affected, func(_ context.Context, b *rfBook) error {
		defer planDone.Add(1)
		r := planRFBook(env, liveBooks, b)
		if r.Reason == "owner-check-unavailable" {
			ownerCheckDown.Store(true)
		}
		results[idx[b]] = r
		return nil
	}, registry.RunItemsOptions{
		Concurrency: rfPlanWorkers,
		ErrMode:     registry.ErrModeCollect,
		Label: func(_, t int) string {
			return fmt.Sprintf("Plan: %d/%d books", planDone.Load(), t)
		},
	})
	if err != nil {
		return report, fmt.Errorf("%s: plan: %w", rfOpName, err)
	}

	// Serial collision pass: a target planned by two books is ambiguous for
	// both, whichever worker happened to plan first.
	targetBooks := map[string][]int{}
	for i := range results {
		if results[i].plan == nil {
			continue
		}
		for _, target := range results[i].NewPaths {
			targetBooks[target] = append(targetBooks[target], i)
		}
	}
	for target, owners := range targetBooks {
		if len(owners) < 2 {
			continue
		}
		for _, i := range owners {
			if results[i].plan == nil {
				continue
			}
			results[i] = rfAmbiguous(results[i], "target-collision",
				fmt.Sprintf("%d books plan %s", len(owners), target))
		}
	}

	if ownerCheckDown.Load() {
		// memdb is not complete (e.g. the ~130 s warmup after a restart), so
		// "no row references this file" cannot be proved for ANY book. Refuse
		// the whole apply rather than write the books that happened to be
		// planned before or after the gap.
		report.Aborted = "BookFilesAtPath unavailable (memdb not serving or incomplete); re-run after warmup"
	}

	if dryRun || report.Aborted != "" {
		finishRFReport(report, results)
		if report.Aborted != "" {
			return report, fmt.Errorf("%s: %s", rfOpName, report.Aborted)
		}
		return report, nil
	}

	// --- phase 3: write (parallel, one book per item) ---
	if err := refuseWhileLibraryScanActive(env.queue, rfOpName); err != nil {
		finishRFReport(report, results)
		return report, err
	}
	holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, env.scan, reporter, rfOpName+" apply")
	if sdErr != nil {
		finishRFReport(report, results)
		return report, fmt.Errorf("%s: acquire scan stand-down: %w", rfOpName, sdErr)
	}
	defer release()

	var work []int
	for i := range results {
		if results[i].plan != nil {
			results[i].Outcome = rfOutcomeSkipped
			work = append(work, i)
		}
	}
	// Each index i is written by exactly one worker and read only after
	// RunItems returns, so results needs no lock.
	var lost atomic.Bool
	var writeDone atomic.Int64
	err = registry.RunItems(ctx, reporter, work, func(itemCtx context.Context, i int) error {
		defer writeDone.Add(1)
		if lost.Load() {
			return nil
		}
		if scanStandDownLostForApply(env.scan, holderID, held) {
			lost.Store(true)
			log.Warn(rfOpName + ": scan stand-down lease lost; aborting remaining writes")
			return nil
		}
		r := results[i]
		outcome, errText := applyRFBook(itemCtx, env, liveBooks, r, reporter, func() bool {
			if lost.Load() {
				return true
			}
			if scanStandDownLostForApply(env.scan, holderID, held) {
				lost.Store(true)
				return true
			}
			return false
		})
		results[i].Outcome = outcome
		results[i].Error = errText
		return nil
	}, registry.RunItemsOptions{
		Concurrency: rfWriteWorkers,
		ErrMode:     registry.ErrModeCollect,
		Label: func(_, t int) string {
			return fmt.Sprintf("Write: %d/%d books", writeDone.Load(), t)
		},
	})
	finishRFReport(report, results)
	if err != nil {
		return report, fmt.Errorf("%s: write: %w", rfOpName, err)
	}
	if lost.Load() {
		report.Aborted = "scan stand-down lease lapsed mid-apply; books marked not_attempted were not written"
		return report, fmt.Errorf("%s: %s", rfOpName, report.Aborted)
	}
	return report, nil
}

// rfScope is the primary + organized books and every book's liveness.
type rfScope struct {
	scoped []database.BookCore
	// live maps EVERY book ID to whether it is live (not marked for deletion),
	// so a candidate's owner check is a map read, not a store read.
	live map[string]bool
}

func rfScopedBooks(store rfStore, only []string) (rfScope, error) {
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	s := rfScope{live: map[string]bool{}}
	for offset := 0; ; offset += bookPageSize {
		page, err := store.GetAllBooksCore(bookPageSize, offset)
		if err != nil {
			return s, fmt.Errorf("%s: load books: %w", rfOpName, err)
		}
		for i := range page {
			b := page[i]
			live := b.MarkedForDeletion == nil || !*b.MarkedForDeletion
			s.live[b.ID] = live
			if !live || b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion ||
				b.LibraryState == nil || *b.LibraryState != "organized" {
				continue
			}
			if len(want) > 0 && !want[b.ID] {
				continue
			}
			s.scoped = append(s.scoped, b)
		}
		if len(page) < bookPageSize {
			break
		}
	}
	return s, nil
}

func rfAmbiguous(r rfBookResult, reason, detail string) rfBookResult {
	r.Decision = rfDecisionAmbiguous
	r.Reason = reason
	if detail != "" {
		r.Error = detail
	}
	r.plan = nil
	r.NewPaths = nil
	r.Changes = nil
	r.ChangeCount = 0
	r.DurationSec = 0
	r.DurationSource = ""
	return r
}

// planRFBook decides one book. Read-only apart from the header probe.
func planRFBook(env rfEnv, liveBooks map[string]bool, b *rfBook) rfBookResult {
	r := rfBookResult{BookID: b.core.ID, Title: b.core.Title, ActiveRows: len(b.rows), MissingRows: b.missingCount()}
	amb := func(reason, detail string) rfBookResult { return rfAmbiguous(r, reason, detail) }

	if b.unreadable {
		return amb("unreadable", "stat failed for a reason other than not-exist")
	}
	for i := range b.rows {
		f := &b.rows[i]
		if f.ITunesPath != "" || f.ITunesPersistentID != "" {
			return amb("itunes-linked", "row "+f.ID+" carries an iTunes link")
		}
		if underITunes(f.FilePath, env.itunesRoots) {
			return amb("itunes-path", f.FilePath)
		}
	}

	folder, reason := rfFolder(env, b)
	if reason != "" {
		return amb(reason, folder)
	}
	r.Folder = folder

	entries, err := os.ReadDir(folder)
	if err != nil {
		return amb("folder-unreadable", err.Error())
	}
	own := map[string]bool{}
	for i := range b.rows {
		own[filepath.Clean(b.rows[i].FilePath)] = true
	}
	type cand struct {
		path string
		size int64
	}
	var cands []cand
	for _, e := range entries {
		if !e.Type().IsRegular() || !linkintegrity.IsAudioFile(e.Name()) {
			continue
		}
		path := filepath.Join(folder, e.Name())
		if own[path] {
			continue // this book already points at it
		}
		owner, oerr := rfOtherOwner(env.store, liveBooks, b.core.ID, path)
		if oerr != nil {
			if errors.Is(oerr, database.ErrBookFilesAtPathUnavailable) {
				return amb("owner-check-unavailable", oerr.Error())
			}
			return amb("owner-check-failed", oerr.Error())
		}
		if owner != "" {
			// Any other book's audio in the folder makes a lone unowned file
			// a stray, not this book's consolidation.
			return amb("folder-shared-with-other-book", path+" is referenced by book "+owner)
		}
		info, serr := e.Info()
		if serr != nil {
			return amb("folder-unreadable", serr.Error())
		}
		cands = append(cands, cand{path: path, size: info.Size()})
	}
	for _, c := range cands {
		r.Candidates = append(r.Candidates, c.path)
	}
	if len(cands) == 0 {
		return amb("no-candidate", "")
	}

	allMissing := r.MissingRows == r.ActiveRows
	plan := &rfPlan{Repoints: map[string]string{}, OldPaths: map[string]string{}, Probes: map[string]mediainfo.MediaInfo{}}

	switch {
	case allMissing && len(cands) == 1:
		target := cands[0]
		var total int64
		for i := range b.rows {
			total += b.rows[i].FileSize
		}
		if total <= 0 {
			return amb("size-unverifiable", "no row recorded a file size")
		}
		r.SizeRatio = float64(target.size) / float64(total)
		if r.SizeRatio < rfSizeRatioMin || r.SizeRatio > rfSizeRatioMax {
			return amb("size-implausible", fmt.Sprintf("candidate %d bytes vs rows %d bytes", target.size, total))
		}
		rows := append([]database.BookFileCore(nil), b.rows...)
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].DiscNumber != rows[j].DiscNumber {
				return rows[i].DiscNumber < rows[j].DiscNumber
			}
			if rows[i].TrackNumber != rows[j].TrackNumber {
				return rows[i].TrackNumber < rows[j].TrackNumber
			}
			return rows[i].FilePath < rows[j].FilePath
		})
		keep := rows[0]
		for _, f := range rows[1:] {
			bf := database.BookFile{Duration: f.Duration, FileSize: f.FileSize,
				AcoustIDFingerprintDurationSec: f.AcoustIDFingerprintDurationSec}
			if database.BookFileRuntimeSec(&bf) > 0 {
				return amb("superseded-rows-carry-duration",
					"row "+f.ID+" would be marked missing but carries a duration ABS would still sum")
			}
		}
		r.Decision = rfDecisionConsolidated
		r.Reason = fmt.Sprintf("%d row(s) -> 1 file", len(rows))
		plan.Repoints[keep.ID] = target.path
		plan.OldPaths[keep.ID] = keep.FilePath
		plan.ResetContent = keep.FileSize != target.size
		r.Changes = append(r.Changes, rfChange{FileID: keep.ID, Action: rfActionRepoint, OldPath: keep.FilePath, NewPath: target.path})
		for _, f := range rows[1:] {
			plan.MarkMissing = append(plan.MarkMissing, f.ID)
			plan.OldPaths[f.ID] = f.FilePath
			r.Changes = append(r.Changes, rfChange{FileID: f.ID, Action: rfActionMarkMissing, OldPath: f.FilePath})
		}
		r.NewPaths = []string{target.path}
	default:
		bySize := map[int64][]string{}
		for _, c := range cands {
			bySize[c.size] = append(bySize[c.size], c.path)
		}
		used := map[string]string{}
		for i := range b.rows {
			if !b.missing[i] {
				continue
			}
			f := b.rows[i]
			if f.FileSize <= 0 {
				return amb("size-unverifiable", "missing row "+f.ID+" has no recorded size")
			}
			match := bySize[f.FileSize]
			switch {
			case len(match) == 0:
				if allMissing {
					return amb("multiple-candidates", fmt.Sprintf("%d unowned audio files and no exact size match for row %s", len(cands), f.ID))
				}
				return amb("no-size-match", "missing row "+f.ID)
			case len(match) > 1:
				return amb("multiple-size-matches", fmt.Sprintf("%d files of %d bytes", len(match), f.FileSize))
			}
			if prev, dup := used[match[0]]; dup {
				return amb("size-match-shared", "rows "+prev+" and "+f.ID+" match the same file")
			}
			used[match[0]] = f.ID
			plan.Repoints[f.ID] = match[0]
			plan.OldPaths[f.ID] = f.FilePath
			r.Changes = append(r.Changes, rfChange{FileID: f.ID, Action: rfActionRepoint, OldPath: f.FilePath, NewPath: match[0]})
			r.NewPaths = append(r.NewPaths, match[0])
		}
		r.Decision = rfDecisionSizeMatch
		r.Reason = fmt.Sprintf("%d row(s) matched 1:1 by size", len(plan.Repoints))
	}
	for _, target := range r.NewPaths {
		if underITunes(target, env.itunesRoots) {
			return amb("itunes-path", target)
		}
	}

	// Header-read each target once, here, so the dry run reports the duration
	// the apply will write. The scratch row carries only the path: Known{} (no
	// book duration) because a class-A book's Duration is nil or small and
	// would otherwise be written as real.
	sources := map[string]bool{}
	for _, target := range r.NewPaths {
		scratch := database.BookFile{FilePath: target}
		src := bookfileaudio.EnsureDuration(&scratch, bookfileaudio.Known{}, nil)
		sources[src.String()] = true
		plan.Probes[target] = mediainfo.MediaInfo{Duration: scratch.Duration, Codec: scratch.Codec}
		r.DurationSec += scratch.Duration
	}
	var srcs []string
	for s := range sources {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	r.DurationSource = strings.Join(srcs, ",")

	// The full change list stays on the result until finishRFReport trims
	// the serialized copy; the TSV is written from the plan.
	r.ChangeCount = len(r.Changes)
	r.plan = plan
	return r
}

// rfFolder picks the book's folder: the one existing directory of its missing
// rows, else book.file_path's directory. The folder must be inside the library
// root and outside every iTunes root.
func rfFolder(env rfEnv, b *rfBook) (string, string) {
	dirs := map[string]bool{}
	for i := range b.rows {
		if !b.missing[i] {
			continue
		}
		d := filepath.Dir(filepath.Clean(b.rows[i].FilePath))
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs[d] = true
		}
	}
	if len(dirs) > 1 {
		var list []string
		for d := range dirs {
			list = append(list, d)
		}
		sort.Strings(list)
		return strings.Join(list, " | "), "multiple-folders"
	}
	folder := ""
	for d := range dirs {
		folder = d
	}
	if folder == "" {
		bp := strings.TrimSpace(b.core.FilePath)
		if bp == "" {
			return "", "folder-gone"
		}
		bp = filepath.Clean(bp)
		st, err := os.Stat(bp)
		switch {
		case err == nil && st.IsDir():
			folder = bp
		case err == nil:
			folder = filepath.Dir(bp)
		default:
			return bp, "folder-gone"
		}
	}
	if !pathutil.IsWithin(folder, filepath.Clean(env.rootDir)) || filepath.Clean(folder) == filepath.Clean(env.rootDir) {
		return folder, "folder-outside-library"
	}
	if underITunes(folder, env.itunesRoots) {
		return folder, "itunes-path"
	}
	return folder, ""
}

// rfOtherOwner returns the ID of a live book OTHER than bookID that references
// path through a book_file row or its own file_path, or "".
func rfOtherOwner(store rfStore, liveBooks map[string]bool, bookID, path string) (string, error) {
	rows, err := store.BookFilesAtPath(path)
	if err != nil {
		return "", err
	}
	for i := range rows {
		if rows[i].BookID != bookID && liveBooks[rows[i].BookID] {
			return rows[i].BookID, nil
		}
	}
	ids, err := store.LiveBookIDsAtPath(path)
	if err != nil {
		return "", fmt.Errorf("live books at %s: %w", path, err)
	}
	for _, id := range ids {
		if id != bookID {
			return id, nil
		}
	}
	return "", nil
}

// applyRFBook writes one planned book. It re-reads the rows and re-checks
// every precondition first; a book that changed since the plan is skipped.
func applyRFBook(ctx context.Context, env rfEnv, liveBooks map[string]bool, r rfBookResult, reporter sdk.Reporter, abort func() bool) (string, string) {
	plan := r.plan
	for _, target := range r.NewPaths {
		st, err := os.Stat(target)
		if err != nil || !st.Mode().IsRegular() {
			return rfOutcomeChanged, "target no longer a regular file: " + target
		}
		owner, err := rfOtherOwner(env.store, liveBooks, r.BookID, target)
		if err != nil {
			return rfOutcomeFailed, "owner re-check: " + err.Error()
		}
		if owner != "" {
			return rfOutcomeChanged, target + " is now referenced by book " + owner
		}
	}
	rows, err := env.store.GetBookFiles(r.BookID)
	if err != nil {
		return rfOutcomeFailed, "load rows: " + err.Error()
	}
	byID := make(map[string]*database.BookFile, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	var out []*database.BookFile
	for id, old := range plan.OldPaths {
		f := byID[id]
		if f == nil || f.Missing || f.FilePath != old {
			return rfOutcomeChanged, "row " + id + " changed since the plan"
		}
		if _, serr := os.Stat(old); !os.IsNotExist(serr) {
			return rfOutcomeChanged, "row " + id + "'s old path is present again"
		}
	}
	// Deterministic write order: repointed rows first (by ID), then the rows
	// marked missing, so an abort mid-book never leaves a book with every row
	// missing and none repointed.
	var repointIDs []string
	for id := range plan.Repoints {
		repointIDs = append(repointIDs, id)
	}
	sort.Strings(repointIDs)
	for _, id := range repointIDs {
		f := byID[id]
		target := plan.Repoints[id]
		f.FilePath = target
		if plan.ResetContent {
			st, serr := os.Stat(target)
			if serr != nil {
				return rfOutcomeChanged, "target vanished: " + target
			}
			rfResetContent(f, target, st.Size())
		}
		probe := plan.Probes[target]
		bookfileaudio.EnsureDuration(f, bookfileaudio.Known{Info: &probe}, nil)
		out = append(out, f)
	}
	for _, id := range plan.MarkMissing {
		f := byID[id]
		f.Missing = true
		out = append(out, f)
	}
	res := writeBookFileBatch(ctx, env.store, out, bookFileBatchOpts{Reporter: reporter, Abort: abort})
	switch {
	case res.RowErrs > 0:
		return rfOutcomeFailed, fmt.Sprintf("%d of %d row writes failed", res.RowErrs, len(out))
	case res.Cancelled:
		return rfOutcomeFailed, fmt.Sprintf("stopped after %d of %d rows (cancelled or stand-down lost)", res.Applied, len(out))
	case res.RecomputeErrs > 0:
		return rfOutcomeApplied, "rows written; book aggregate recompute failed"
	}
	return rfOutcomeApplied, ""
}

// rfResetContent clears every field of f that describes the bytes it USED to
// point at, for a row repointed onto a different file (a consolidation). Left
// in place, the old chapter's hash would make content matching treat the row
// as that chapter, its duration would be kept by EnsureDuration (which never
// overwrites a positive value), and its fingerprint would describe audio the
// file does not start with. Every cleared field is recomputable from the file
// (the scanner re-hashes a row whose scan stamps are empty; the fingerprint
// and duration backfills refill theirs). Transcription and descriptive fields
// (title, track number, tags) are kept: they are not recomputable here, and
// the kept row is the first chapter, whose intro is the file's intro.
func rfResetContent(f *database.BookFile, target string, size int64) {
	f.FileSize = size
	f.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(target)), ".")
	f.OriginalFilename = filepath.Base(target)
	f.Duration = 0
	f.Codec = ""
	f.BitrateKbps, f.SampleRateHz, f.Channels, f.BitDepth = 0, 0, 0, 0
	f.FileHash, f.OriginalFileHash, f.OriginalFileHashKind, f.PostMetadataHash = "", "", "", ""
	f.LastScanMtime, f.LastScanSize, f.NeedsRescan = nil, nil, nil
	f.AcoustIDFingerprint = nil
	f.AcoustIDFingerprintDurationSec = 0
	f.AcoustIDFPVersion = 0
	f.AcoustIDSeg0, f.AcoustIDSeg1, f.AcoustIDSeg2, f.AcoustIDSeg3 = "", "", "", ""
	f.AcoustIDSeg4, f.AcoustIDSeg5, f.AcoustIDSeg6 = "", "", ""
	f.AcoustIDOnlineRecordingID, f.AcoustIDOnlineScore, f.AcoustIDOnlineLookedUpAt = "", 0, nil
	f.FingerprintFailedAt, f.FingerprintFailureReason = nil, nil
	f.FingerprintFailureDetail, f.FingerprintDiagnosticJSON = nil, nil
	f.UpdatedAt = time.Now()
}

// finishRFReport fills the counts and the serialized book list.
func finishRFReport(report *rfReport, results []rfBookResult) {
	report.Books = make([]rfBookResult, 0, len(results))
	for _, r := range results {
		report.ByDecision[r.Decision]++
		key := r.Reason
		if r.Decision != rfDecisionAmbiguous {
			key = r.Decision
		}
		report.ByReason[key]++
		if r.Outcome != "" {
			report.ByOutcome[r.Outcome]++
		}
		if r.plan != nil && (report.DryRun || r.Outcome == rfOutcomeApplied) {
			report.RowsRepointed += len(r.plan.Repoints)
			report.RowsMarkedMissing += len(r.plan.MarkMissing)
		}
		out := r
		if len(out.Changes) > rfMaxChangesPerBook {
			out.Changes = out.Changes[:rfMaxChangesPerBook]
		}
		report.Books = append(report.Books, out)
	}
}

// writeRFReport writes every planned row change (and every ambiguous book as
// one line) as TSV: the result lists at most rfMaxChangesPerBook rows per
// book, and a 330-chapter book's full list belongs in a file.
func writeRFReport(path string, books []rfBookResult) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("book_id\tdecision\treason\toutcome\tfile_id\taction\told_path\tnew_path\n")
	for _, r := range books {
		if r.plan == nil {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t\t\t%s\t\n", r.BookID, r.Decision, clean(r.Reason), r.Outcome, clean(r.Folder))
			continue
		}
		for id, old := range r.plan.OldPaths {
			action, newPath := rfActionMarkMissing, ""
			if t, ok := r.plan.Repoints[id]; ok {
				action, newPath = rfActionRepoint, t
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.BookID, r.Decision, clean(r.Reason), r.Outcome,
				id, action, clean(old), clean(newPath))
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}

// Every method rfStore needs is on database.Store, so the production
// indexedStore (which embeds it) satisfies the runtime assertion above.
var _ rfStore = database.Store(nil)
