// file: internal/plugins/maintenance/build_folder_book_files.go
// version: 1.0.1
// guid: 92622b6c-f340-42c4-b6b1-2fdba32b770f
// last-edited: 2026-09-25

// Package maintenance — op maintenance.build-folder-book-files.
//
// Builds book_file rows, one per audio file, for books ABS lists that own no
// book_file row at all while their file_path is a folder full of audio.
//
// THE POPULATION (zero-duration investigation, 2026-09-25, "class C"): about
// 1,400 primary + organized books with zero book_file rows and a nil or 0
// Duration, whose folder holds 10-142 audio files. ABS sums row durations and
// falls back to book.Duration only when there are no rows, so these read as 0.
// maintenance.duration-backfill cannot fix them: with no rows it probes
// book.FilePath, which is a directory, and records a read error.
//
// WHY A SIBLING OP AND NOT A MODE OF AN EXISTING ONE. Three ops create rows for
// rowless books; none has this scope or can reach this population:
//   - relink-unlinked-books classifies with nil durations, so ClassifyDir sends
//     every multi-file folder to review. Its apply loop is sequential.
//   - probe-directory-books probes, but re-runs ClassifyDir as a GATE. Any
//     folder whose files carry chapter titles ("01 - Arrival", "02 - The Road")
//     has several distinct stems and stays in review. These books are already
//     single ABS items; the owner approved building their rows from the folder,
//     so here the classifier verdict is reported per book (looks_multi_work),
//     never used to refuse.
//   - the legacy backfill-book-files job has no scope, no ownership check, no
//     iTunes guard, no book_ids and no track numbers.
//
// The row builder is shared: buildBookFileFor (relink_unlinked.go) stats the
// file and fills Duration via bookfileaudio.EnsureDuration, and the rows go in
// through BatchCreateBookFiles, which recomputes the book's aggregates once.
//
// SAFETY
//   - Scope is database.ABSLibraryFilter, the predicate the ABS handler lists
//     by, so this repairs exactly the books ABS shows.
//   - A file already referenced by another live book (a book_file row at the
//     path, via BookFilesAtPath, or a book whose file_path IS the file, via
//     LiveBookIDsAtPath) is skipped and reported, never claimed twice.
//   - Both lookups fail closed: while memdb is not serving (about 130s after a
//     restart) BookFilesAtPath errors, and the op aborts rather than reading
//     "unavailable" as "unowned".
//   - Nothing under the iTunes tree (books/itunes/**) is touched.
//   - No UpdateBook call: the only write is the additive BatchCreateBookFiles.
//   - DRY RUN unless {"apply": true}. The dry run probes too, so it reports the
//     per-book duration it would write.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// folderBuildParams configures the op.
type folderBuildParams struct {
	// Apply gates every write. False (the default) is a full dry run: it
	// probes and reports, and writes nothing.
	Apply bool `json:"apply"`
	// BookIDs restricts the run to these books. Every listed id gets a
	// per-book entry in the result, including the ones that are out of scope,
	// so a one-off repair says why a book was not built.
	BookIDs []string `json:"book_ids"`
	// Limit caps how many in-scope books are probed (and, with apply, built),
	// in book-id order. 0 = no cap. Detection still covers every id, so the
	// counts show how many were left for the next run.
	Limit int `json:"limit"`
}

// Decisions, one per examined book.
const (
	fbWouldBuild      = "would_build"
	fbBuilt           = "built"
	fbNotFound        = "not_found"
	fbOutOfScope      = "out_of_scope" // not primary + organized, soft-deleted or quarantined
	fbHasRows         = "has_rows"
	fbOnlyMissingRows = "only_missing_rows"
	fbNoPath          = "no_path"
	fbPathUnreadable  = "path_unreadable"
	fbNotDirectory    = "not_directory"
	fbITunesRoot      = "itunes_root"
	fbSharedFolder    = "shared_folder"
	fbNoAudio         = "no_audio"
	fbAudioInSubdirs  = "audio_only_in_subdirs"
	fbAllFilesOwned   = "all_files_owned"
	fbRowsAppeared    = "rows_appeared"
	fbLimitReached    = "limit_reached"
	fbError           = "error"
)

// folderBuildStore is what the op reads and writes. BookFilesAtPath,
// LiveBookIDsAtPath and BookAtPathIndexBuilt are on database.Store but not on
// OpsStore, so the run reaches them through a checked type assertion (the
// orphan repoint planner does the same); the production indexedStore embeds
// Store and forwards all three.
type folderBuildStore interface {
	bookByIDReader
	bookFileLister
	bookFileBatchCreator
	ListBookIDs() ([]string, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	BookAtPathIndexBuilt() (bool, error)
}

// The run reaches folderBuildStore by type assertion on the ops store, and the
// production ops store is a wrapper that embeds database.Store. A method that
// is not on database.Store would make that assertion fail only in production,
// so this fails the build instead.
var _ folderBuildStore = database.Store(nil)

// fbOwnedListCap bounds the owned paths listed per book. The count
// (OwnedSkippedCount) stays exact; the list is evidence, not a census, and a
// 142-file folder owned elsewhere would otherwise put 142 paths in the result.
const fbOwnedListCap = 10

// folderBuildOwned is one audio file the op left alone because another live
// book already references it.
type folderBuildOwned struct {
	Path         string   `json:"path"`
	OwnerBookIDs []string `json:"owner_book_ids"`
}

// folderBuildBook is the per-book result entry.
type folderBuildBook struct {
	BookID   string `json:"book_id"`
	Title    string `json:"title"`
	Folder   string `json:"folder,omitempty"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	// FileCount is the rows built (or, in a dry run, that would be built).
	FileCount        int `json:"file_count"`
	TotalDurationSec int `json:"total_duration_sec"`
	// UnprobedFiles is how many of those rows carry Duration 0 because the
	// header could not be read. The row is still built, so
	// maintenance.duration-backfill can retry it later.
	UnprobedFiles int `json:"unprobed_files,omitempty"`
	// OwnedSkippedCount is every file skipped as owned by another live book;
	// OwnedSkipped lists the first fbOwnedListCap of them.
	OwnedSkippedCount int                `json:"owned_skipped_count,omitempty"`
	OwnedSkipped      []folderBuildOwned `json:"owned_skipped,omitempty"`
	// Classifier is ClassifyDirProbed's reading of the folder. Information
	// only: LooksMultiWork flags folders that may hold several works, for the
	// owner to review in the dry run.
	Classifier     string `json:"classifier,omitempty"`
	LooksMultiWork bool   `json:"looks_multi_work,omitempty"`
}

// folderBuildReport is the op's persisted result.
type folderBuildReport struct {
	DryRun              bool              `json:"dry_run"`
	Examined            int               `json:"examined"`
	Counts              map[string]int    `json:"counts"`
	RowsBuilt           int               `json:"rows_built"`
	TotalDurationSec    int64             `json:"total_duration_sec"`
	UnprobedFiles       int               `json:"unprobed_files"`
	OwnedFilesSkipped   int               `json:"owned_files_skipped"`
	BooksWithOwnedFiles int               `json:"books_with_owned_files"`
	LooksMultiWork      int               `json:"looks_multi_work"`
	Books               []folderBuildBook `json:"books"`
}

func (p *Plugin) buildFolderBookFilesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.build-folder-book-files",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Build book_file rows from a book's folder",
		Description: "For books ABS lists (primary + organized) that own no book_file row while their file_path is " +
			"a folder of audio: creates one row per audio file, in natural filename order, with each file's size " +
			"and ffprobe duration, then recomputes the book's totals. Files another live book already references " +
			"are skipped and reported. Never touches the iTunes tree. Params: book_ids, limit. " +
			"DRY RUN unless {\"apply\": true}.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		// Shares relink's key: relink, probe-directory-books and this op all
		// create rows for rowless books, and two of them at once would each
		// see the other's half-built state.
		ConcurrencyKey: "maintenance.relink-unlinked-books",
		Cancellable:    true,
		Isolate:        false,
		Timeout:        240 * time.Minute,
		Capabilities:   []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapLibraryWrite},
		Run:            p.runBuildFolderBookFiles,
	}
}

func (p *Plugin) runBuildFolderBookFiles(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := folderBuildParams{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	if params.Limit < 0 {
		return fmt.Errorf("invalid params: limit must be >= 0")
	}
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	store, ok := any(ops).(folderBuildStore)
	if !ok {
		// Without the path lookups the ownership check cannot run, and
		// building without it could claim another book's files.
		return fmt.Errorf("store %T cannot list book_file rows by path; refusing to build without the ownership check", ops)
	}
	report, err := buildFolderBookFiles(ctx, store, params, reporter)
	if report != nil {
		if serr := registry.ReporterSetResult(reporter, report); serr != nil {
			return errors.Join(err, fmt.Errorf("persist result: %w", serr))
		}
	}
	return err
}

// fbCandidate is one in-scope book headed for the build pass.
type fbCandidate struct {
	book   *database.Book
	folder string
	result folderBuildBook
}

// buildFolderBookFiles is the op body, separated from the plugin wiring so
// tests can hand it a store directly.
func buildFolderBookFiles(ctx context.Context, store folderBuildStore, params folderBuildParams, reporter sdk.Reporter) (*folderBuildReport, error) {
	// LiveBookIDsAtPath answers from a scan of EVERY book row until the
	// book_atpath index is built. This op asks once per audio file, so an
	// unbuilt index would be tens of thousands of full scans.
	built, err := store.BookAtPathIndexBuilt()
	if err != nil {
		return nil, fmt.Errorf("book_atpath index status: %w", err)
	}
	if !built {
		return nil, fmt.Errorf("the book_atpath index is not built yet; run its backfill first (the ownership check asks it once per file)")
	}

	if !params.Apply {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN: no book_file rows will be created")
	}

	explicit := len(params.BookIDs) > 0
	ids := params.BookIDs
	if !explicit {
		_ = reporter.UpdateProgress(0, 0, "Phase 1/2: enumerating library…")
		if ids, err = store.ListBookIDs(); err != nil {
			return nil, fmt.Errorf("ListBookIDs: %w", err)
		}
	}

	report := &folderBuildReport{DryRun: !params.Apply, Counts: map[string]int{}}
	scope := database.ABSLibraryFilter()

	// ── Phase 1: scope ───────────────────────────────────────────────────────
	// CONCURRENCY (CLAUDE.md): a DB read and an os.Stat per book over the
	// whole library, so a bounded pool. Read-only; results collect under mu.
	var mu sync.Mutex
	var candidates []*fbCandidate
	var entries []folderBuildBook
	record := func(e folderBuildBook, always bool) {
		mu.Lock()
		defer mu.Unlock()
		report.Counts[e.Decision]++
		if always || explicit {
			entries = append(entries, e)
		}
	}

	scanErr := registry.RunItems(ctx, reporter, ids, func(_ context.Context, id string) error {
		e := folderBuildBook{BookID: id}
		b, err := store.GetBookByID(id)
		if err != nil {
			e.Decision, e.Reason = fbError, err.Error()
			record(e, true)
			return nil
		}
		if b == nil {
			e.Decision = fbNotFound
			record(e, false)
			return nil
		}
		e.Title = b.Title
		if !scope.Matches(b) {
			e.Decision = fbOutOfScope
			record(e, false)
			return nil
		}
		files, err := store.GetBookFiles(id)
		if err != nil {
			e.Decision, e.Reason = fbError, err.Error()
			record(e, true)
			return nil
		}
		if len(files) > 0 {
			// A book whose only rows are marked missing needs a repoint of
			// those rows, not a second set beside them, so it is reported
			// separately and left alone.
			e.Decision = fbOnlyMissingRows
			for _, f := range files {
				if !f.Missing {
					e.Decision = fbHasRows
					break
				}
			}
			record(e, false)
			return nil
		}
		path := strings.TrimSpace(b.FilePath)
		e.Folder = path
		switch {
		case path == "":
			e.Decision = fbNoPath
			record(e, false)
			return nil
		case config.UnderFrozenITunesTree(path):
			e.Decision, e.Reason = fbITunesRoot, "file_path is under the iTunes tree, which is never written"
			record(e, true)
			return nil
		}
		st, err := os.Stat(path)
		if err != nil {
			e.Decision, e.Reason = fbPathUnreadable, err.Error()
			record(e, false)
			return nil
		}
		if !st.IsDir() {
			e.Decision = fbNotDirectory
			record(e, false)
			return nil
		}
		mu.Lock()
		candidates = append(candidates, &fbCandidate{book: b, folder: filepath.Clean(path), result: e})
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: len(ids),
		ErrMode:       registry.ErrModeCollect,
		Label: func(i, total int) string {
			return fmt.Sprintf("Phase 1/2: checking book %d/%d…", i+1, total)
		},
	})
	if scanErr != nil {
		return nil, fmt.Errorf("library scan: %w", scanErr)
	}
	report.Examined = len(ids)

	// Deterministic order: two dry runs diff cleanly and a limited run always
	// takes the same prefix.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].book.ID < candidates[j].book.ID })

	// PARTITION BY FOLDER. The build pass is parallel, and each worker checks
	// ownership then writes. Two in-scope books at one folder would both pass
	// the check and both claim the same files, so every such book is skipped
	// and reported instead. What remains is one book per folder; the listing
	// is flat (direct children only), so no two workers can ever build a row
	// for the same file.
	perFolder := map[string]int{}
	for _, c := range candidates {
		perFolder[c.folder]++
	}
	work := make([]*fbCandidate, 0, len(candidates))
	for _, c := range candidates {
		if perFolder[c.folder] > 1 {
			c.result.Decision = fbSharedFolder
			c.result.Reason = fmt.Sprintf("%d books ABS lists point at this folder; which one owns the files is ambiguous", perFolder[c.folder])
			record(c.result, true)
			continue
		}
		if params.Limit > 0 && len(work) >= params.Limit {
			c.result.Decision = fbLimitReached
			record(c.result, false)
			continue
		}
		work = append(work, c)
	}

	// ── Phase 2: build ───────────────────────────────────────────────────────
	// CONCURRENCY: one pool at the BOOK level, sized to NumCPU because each
	// book is an ffprobe subprocess per file. Files within a book run in
	// sequence: a nested pool would put NumCPU² probes on the box at once.
	var done atomic.Int64
	total := len(work)
	buildErr := registry.RunItems(ctx, reporter, work, func(ctx context.Context, c *fbCandidate) error {
		onFile := func(i, n int, name string) {
			// Stamp mid-book: RunItems only stamps between items, and a
			// 142-file folder of slow reads can outlast the progress watchdog.
			_ = reporter.UpdateProgress(int(done.Load()), total, fmt.Sprintf(
				"Phase 2/2: %s: file %d/%d (%s)", linkintegrity.DirNameOf(c.folder), i+1, n, name))
		}
		err := buildOneFolder(ctx, store, c, params.Apply, onFile)
		done.Add(1)
		return err
	}, registry.RunItemsOptions{
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: total,
		// ErrModeFail: the only error a worker returns is a fail-closed
		// lookup (the ownership index is unavailable). Carrying on would
		// report the rest of the library as built on no ownership check.
		ErrMode: registry.ErrModeFail,
		Label: func(i, n int) string {
			return fmt.Sprintf("Phase 2/2: building book %d/%d…", i+1, n)
		},
	})
	for _, c := range work {
		if c.result.Decision == "" {
			continue // never reached: the pass aborted first
		}
		record(c.result, true)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].BookID < entries[j].BookID })
	report.Books = entries
	for _, e := range entries {
		if e.Decision == fbBuilt || e.Decision == fbWouldBuild {
			report.RowsBuilt += e.FileCount
			report.TotalDurationSec += int64(e.TotalDurationSec)
			report.UnprobedFiles += e.UnprobedFiles
		}
		if e.OwnedSkippedCount > 0 {
			report.BooksWithOwnedFiles++
			report.OwnedFilesSkipped += e.OwnedSkippedCount
		}
		if e.LooksMultiWork {
			report.LooksMultiWork++
		}
	}

	_ = reporter.Log(slog.LevelInfo, report.summary())
	_ = reporter.UpdateProgress(total, total, report.summary())
	if buildErr != nil {
		return report, fmt.Errorf("build pass aborted: %w", buildErr)
	}
	return report, nil
}

// summary is the one-line count of every decision.
func (r *folderBuildReport) summary() string {
	keys := make([]string, 0, len(r.Counts))
	for k := range r.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, r.Counts[k]))
	}
	mode := "APPLY"
	if r.DryRun {
		mode = "DRY RUN"
	}
	return fmt.Sprintf("%s examined=%d %s rows=%d duration_sec=%d unprobed_files=%d owned_files_skipped=%d looks_multi_work=%d",
		mode, r.Examined, strings.Join(parts, " "), r.RowsBuilt, r.TotalDurationSec,
		r.UnprobedFiles, r.OwnedFilesSkipped, r.LooksMultiWork)
}

// buildOneFolder lists, checks, probes and (with apply) writes one book's rows,
// setting c.result. It returns an error only when the ownership lookup cannot
// answer; every per-book problem is a decision instead.
func buildOneFolder(ctx context.Context, store folderBuildStore, c *fbCandidate, apply bool, onFile func(i, n int, name string)) error {
	res := &c.result
	audio, names, subdirs, err := folderAudioFiles(c.folder)
	if err != nil {
		res.Decision, res.Reason = fbPathUnreadable, err.Error()
		return nil
	}
	if len(audio) == 0 {
		res.Decision = fbNoAudio
		if subdirs > 0 {
			res.Decision = fbAudioInSubdirs
			res.Reason = fmt.Sprintf("no audio directly in the folder; %d subdirectories", subdirs)
		}
		return nil
	}

	rows := make([]*database.BookFile, 0, len(audio))
	probes := make([]linkintegrity.ProbedDuration, 0, len(audio))
	for i, path := range audio {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.Base(path)
		if onFile != nil {
			onFile(i, len(audio), name)
		}
		if config.UnderFrozenITunesTree(path) {
			res.Decision, res.Reason = fbITunesRoot, "an audio file resolves under the iTunes tree"
			return nil
		}
		owners, err := liveOwnersOf(store, path, c.book.ID)
		if err != nil {
			return err
		}
		if len(owners) > 0 {
			res.OwnedSkippedCount++
			if len(res.OwnedSkipped) < fbOwnedListCap {
				res.OwnedSkipped = append(res.OwnedSkipped, folderBuildOwned{Path: path, OwnerBookIDs: owners})
			}
			continue
		}
		// Track number = position among the rows built, in natural filename
		// order, so "2.mp3" precedes "10.mp3".
		bf := buildBookFileFor(c.book, path, 0, len(rows)+1)
		rows = append(rows, bf)
		probes = append(probes, linkintegrity.ProbedDuration{Name: name, Sec: bf.Duration, OK: bf.Duration > 0})
		res.TotalDurationSec += bf.Duration
		if bf.Duration <= 0 {
			res.UnprobedFiles++
		}
	}
	res.FileCount = len(rows)
	if len(rows) == 0 {
		res.Decision = fbAllFilesOwned
		res.Reason = fmt.Sprintf("all %d audio files are referenced by other live books", len(audio))
		return nil
	}

	v := linkintegrity.ClassifyDirProbed(names, subdirs, probes)
	res.Classifier = v.Reason
	// Several titles, or one title over book-length files. A verdict held only
	// for unprobeable files is not evidence of several works.
	res.LooksMultiWork = !v.OneBook && (v.DistinctStems > 1 || v.ProbesFailed == 0)

	if !apply {
		res.Decision = fbWouldBuild
		return nil
	}
	// Re-check under the write: an import or another op may have given the
	// book rows since Phase 1. Never add a second set.
	existing, err := store.GetBookFiles(c.book.ID)
	if err != nil {
		res.Decision, res.Reason = fbError, fmt.Sprintf("recheck rows: %v", err)
		return nil
	}
	if len(existing) > 0 {
		res.Decision = fbRowsAppeared
		return nil
	}
	// One batch: atomic, and the book's aggregates (Duration, FileSize) are
	// recomputed once rather than once per row.
	if err := store.BatchCreateBookFiles(rows); err != nil {
		res.Decision, res.Reason = fbError, fmt.Sprintf("BatchCreateBookFiles: %v", err)
		return nil
	}
	res.Decision = fbBuilt
	return nil
}

// folderAudioFiles returns the folder's audio files (full paths, natural
// order), every entry name, and how many entries are directories. Audio means
// the configured supported_extensions, the set the scanner imports by.
func folderAudioFiles(dir string) (audio, names []string, subdirs int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, 0, err
	}
	exts := config.SupportedExtensionSet()
	for _, e := range entries {
		names = append(names, e.Name())
		if e.IsDir() {
			subdirs++
			continue
		}
		if exts.MatchPath(e.Name()) {
			audio = append(audio, filepath.Join(dir, e.Name()))
		}
	}
	sort.Slice(audio, func(i, j int) bool { return util.NaturalLess(filepath.Base(audio[i]), filepath.Base(audio[j])) })
	return audio, names, subdirs, nil
}

// liveOwnersOf returns the ids of live books other than self that reference
// path: through a book_file row at it, or as their own file_path. Either
// lookup failing is an error, never "no owner".
func liveOwnersOf(store folderBuildStore, path, self string) ([]string, error) {
	rows, err := store.BookFilesAtPath(path)
	if err != nil {
		return nil, fmt.Errorf("book_file rows at %s: %w", path, err)
	}
	ids, err := store.LiveBookIDsAtPath(path)
	if err != nil {
		return nil, fmt.Errorf("live books at %s: %w", path, err)
	}
	cand := make(map[string]struct{}, len(rows)+len(ids))
	for _, r := range rows {
		if r.BookID != self {
			cand[r.BookID] = struct{}{}
		}
	}
	for _, id := range ids {
		if id != self {
			cand[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(cand))
	for id := range cand {
		b, err := store.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("owner %s of %s: %w", id, path, err)
		}
		// A row left behind by a deleted book does not own the file.
		if b == nil || b.IsSoftDeleted() {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
