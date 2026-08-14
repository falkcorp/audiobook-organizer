// file: internal/plugins/maintenance/title_repair.go
// version: 1.0.0
// guid: 13bedd46-9b61-41a2-b791-36813d7ffcb9
// last-edited: 2026-07-17

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// titleRepairParams controls maintenance.title-repair. Dry-run is the default:
// nothing is written unless apply=true.
type titleRepairParams struct {
	Apply bool `json:"apply"`
}

// repairAction is the per-book outcome of the title-repair decision.
type repairAction int

const (
	// actionRetitle: the CONS-17b agreed chapter title differs from the stored
	// title — retitle (or report in dry-run).
	actionRetitle repairAction = iota
	// actionSkipDeleted: book is soft-deleted (MarkedForDeletion).
	actionSkipDeleted
	// actionSkipSingleFile: <2 files — a single-file book cannot have the
	// chapter-title leak (CONS-17b definition).
	actionSkipSingleFile
	// actionSkipProvenance: title has an override / override-lock / fetched
	// value, or the book's metadata came from a fetched provider — never
	// clobber curated or provider-supplied titles.
	actionSkipProvenance
	// actionSkipNoAgreement: chapters do not agree on a stripped title (or the
	// directory holds <2 audio files on disk).
	actionSkipNoAgreement
	// actionSkipTitleOK: the agreed title already equals the stored title —
	// idempotent no-op.
	actionSkipTitleOK
)

// titleRepairBook carries everything the pure decision function needs about
// one book, pre-gathered by the runner so the decision itself does no IO.
type titleRepairBook struct {
	Title             string
	MarkedForDeletion bool
	// MetadataSource is Book.MetadataSource ("" when nil) — a non-empty value
	// means a provider supplied the last applied metadata.
	MetadataSource string
	// TitleState is the metadata_state:<ulid>:title row, nil when absent.
	TitleState *database.MetadataFieldState
	FilePaths  []string
}

// titleRepairDecision is the outcome of decideTitleRepair.
type titleRepairDecision struct {
	Action repairAction
	// Reason is a short human-readable skip reason for Debug logging.
	Reason string
	// DirPath is the (majority) directory the agreement check ran against.
	DirPath string
	// MixedDir reports that the book's files span more than one directory
	// (counted separately; the majority directory is still used).
	MixedDir bool
	// NewTitle is set only when Action == actionRetitle.
	NewTitle string
}

// majorityDir returns the directory shared by most of paths, and whether the
// paths span more than one distinct directory. Ties break lexicographically
// (smallest dir wins) so the result is deterministic.
func majorityDir(paths []string) (dir string, mixed bool) {
	counts := make(map[string]int, 1)
	for _, p := range paths {
		counts[filepath.Dir(p)]++
	}
	best, bestN := "", 0
	for d, n := range counts {
		if n > bestN || (n == bestN && (best == "" || d < best)) {
			best, bestN = d, n
		}
	}
	return best, len(counts) > 1
}

// decideTitleRepair is the pure per-book decision for maintenance.title-repair.
// agreedFn abstracts metadata.AgreedChapterTitle so tests can stub the on-disk
// tag reads; it is only invoked after all cheap gates pass. Check order is
// deliberately cheapest-first: soft-delete → file count → provenance →
// agreement (the only gate that reads audio tags off disk).
func decideTitleRepair(in titleRepairBook, agreedFn func(dirPath string) (agreed string, multi bool)) titleRepairDecision {
	if in.MarkedForDeletion {
		return titleRepairDecision{Action: actionSkipDeleted, Reason: "soft-deleted"}
	}
	if len(in.FilePaths) < 2 {
		return titleRepairDecision{Action: actionSkipSingleFile, Reason: "single-file book"}
	}
	dir, mixed := majorityDir(in.FilePaths)

	// Provenance guard: only proceed when the stored title is file/filename
	// derived or has no field state at all.
	if st := in.TitleState; st != nil {
		if st.OverrideLocked || st.OverrideValue != nil {
			return titleRepairDecision{Action: actionSkipProvenance, Reason: "title has user override", DirPath: dir, MixedDir: mixed}
		}
		if st.FetchedValue != nil {
			return titleRepairDecision{Action: actionSkipProvenance, Reason: "title has fetched value", DirPath: dir, MixedDir: mixed}
		}
	}
	if in.MetadataSource != "" {
		return titleRepairDecision{
			Action:   actionSkipProvenance,
			Reason:   "book metadata applied from provider " + in.MetadataSource,
			DirPath:  dir,
			MixedDir: mixed,
		}
	}

	agreed, multi := agreedFn(dir)
	if !multi || agreed == "" {
		return titleRepairDecision{Action: actionSkipNoAgreement, Reason: "chapters do not agree on a title", DirPath: dir, MixedDir: mixed}
	}
	if strings.EqualFold(agreed, in.Title) {
		return titleRepairDecision{Action: actionSkipTitleOK, Reason: "stored title already matches agreed title", DirPath: dir, MixedDir: mixed}
	}
	return titleRepairDecision{Action: actionRetitle, DirPath: dir, MixedDir: mixed, NewTitle: agreed}
}

func (p *Plugin) titleRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.title-repair",
		Plugin:          "maintenance",
		DisplayName:     "Repair leaked chapter-tag titles (CONS-17b)",
		Description:     "Re-derives each multi-file book's title via the CONS-17b all-chapters-agree check and repairs stored titles that are per-chapter tag residue (e.g. 'Big Finish Ident'). Skips single-file books and any title with override/fetched provenance. Default dry-run previews old→new; set apply=true to write.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.title-repair",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTitleRepair,
	}
}

// titleRepairWorkers caps the RunItems pool: the per-book work is disk-IO +
// tag-read bound, so more than 8 workers just thrashes the disk.
func titleRepairWorkers() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

func (p *Plugin) runTitleRepair(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params titleRepairParams // Apply=false → dry-run (safe default)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if !params.Apply {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no changes will be written (set apply=true to write)")
	}

	// Load the full book list up front (Core rows are slim). Paged — no
	// silent cap: the loop runs until a short page.
	const pageSize = 500
	var books []database.BookCore
	for offset := 0; ; {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		page, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return fmt.Errorf("GetAllBooksCore offset=%d: %w", offset, err)
		}
		books = append(books, page...)
		offset += len(page)
		if len(page) < pageSize {
			break
		}
	}

	total := len(books)
	exts := config.AppConfig.SupportedExtensions
	workers := titleRepairWorkers()
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"title-repair starting: %d books, %d workers, apply=%v", total, workers, params.Apply))
	if total == 0 {
		_ = reporter.UpdateProgress(1, 1, "title-repair: 0 books — nothing to do")
		return nil
	}

	// Counters are atomics — the RunItems pool below is concurrent. Each
	// worker touches a disjoint book, so writes never collide on a row, but
	// counters are shared.
	var examined, retitled, skipSingle, skipNoAgree, skipProv, skipTitleOK, skipDeleted, mixedDir, errs atomic.Int64

	verb := "would retitle"
	if params.Apply {
		verb = "retitled"
	}

	err := registry.RunItems(ctx, reporter, books, func(ctx context.Context, b database.BookCore) error {
		n := examined.Add(1)
		if n%500 == 0 {
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
				"title-repair progress: %d/%d examined, %d %s, %d errors", n, total, retitled.Load(), verb, errs.Load()))
		}

		// Cheap pre-gate before any per-book DB reads.
		if b.IsSoftDeleted() {
			skipDeleted.Add(1)
			return nil
		}

		files, ferr := store.GetBookFiles(b.ID)
		if ferr != nil {
			errs.Add(1)
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: GetBookFiles failed: %v", b.ID, ferr))
			return nil // non-fatal: keep sweeping
		}
		paths := make([]string, 0, len(files))
		for _, f := range files {
			if f.FilePath != "" {
				paths = append(paths, f.FilePath)
			}
		}

		var titleState *database.MetadataFieldState
		states, serr := store.GetMetadataFieldStates(b.ID)
		if serr != nil {
			errs.Add(1)
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: GetMetadataFieldStates failed: %v", b.ID, serr))
			return nil
		}
		for i := range states {
			if states[i].Field == "title" {
				titleState = &states[i]
				break
			}
		}

		metaSource := ""
		if b.MetadataSource != nil {
			metaSource = *b.MetadataSource
		}
		deleted := b.IsSoftDeleted()

		d := decideTitleRepair(titleRepairBook{
			Title:             b.Title,
			MarkedForDeletion: deleted,
			MetadataSource:    metaSource,
			TitleState:        titleState,
			FilePaths:         paths,
		}, func(dir string) (string, bool) {
			return metadata.AgreedChapterTitle(dir, exts)
		})

		if d.MixedDir {
			mixedDir.Add(1)
			_ = reporter.Log(slog.LevelDebug, fmt.Sprintf(
				"book %s: files span multiple directories; using majority dir %s", b.ID, d.DirPath))
		}

		switch d.Action {
		case actionSkipDeleted:
			skipDeleted.Add(1)
		case actionSkipSingleFile:
			skipSingle.Add(1)
		case actionSkipNoAgreement:
			skipNoAgree.Add(1)
		case actionSkipTitleOK:
			skipTitleOK.Add(1)
		case actionSkipProvenance:
			skipProv.Add(1)
		case actionRetitle:
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
				"book %s: %s %q → %q", b.ID, verb, b.Title, d.NewTitle))
			if params.Apply {
				// UpdateBook is a FULL replacement — hydrate the full row and
				// patch ONLY Title so denormalized Author/Series/fingerprints
				// survive. Provenance note: the persisted field-state store
				// only holds fetched/override values; the file-level value IS
				// Book.Title, and the provenance map is recomputed at read
				// time, so no separate provenance write is needed.
				full, herr := store.GetBookByID(b.ID)
				if herr != nil || full == nil {
					errs.Add(1)
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: hydrate failed: %v", b.ID, herr))
					return nil
				}
				full.Title = d.NewTitle
				if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
					errs.Add(1)
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: UpdateBook failed: %v", b.ID, uerr))
					return nil
				}
			}
			retitled.Add(1)
		}
		if d.Action != actionRetitle && d.Reason != "" {
			_ = reporter.Log(slog.LevelDebug, fmt.Sprintf("book %s: skip — %s", b.ID, d.Reason))
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d (%s=%d err=%d)", i+1, t, verb, retitled.Load(), errs.Load())
		},
	})
	if err != nil {
		return err
	}

	suffix := ""
	if !params.Apply {
		suffix = " (dry run — no writes)"
	}
	result := fmt.Sprintf(
		"title-repair complete: examined=%d %s=%d skipped_single_file=%d skipped_no_agreement=%d skipped_provenance=%d skipped_title_ok=%d skipped_deleted=%d mixed_dir=%d errors=%d%s",
		examined.Load(), strings.ReplaceAll(verb, " ", "_"), retitled.Load(), skipSingle.Load(), skipNoAgree.Load(),
		skipProv.Load(), skipTitleOK.Load(), skipDeleted.Load(), mixedDir.Load(), errs.Load(), suffix)
	_ = reporter.Log(slog.LevelInfo, result)
	_ = reporter.UpdateProgress(total, total, result)

	if errs.Load() > 0 {
		return fmt.Errorf("%d per-book errors (see op log for details)", errs.Load())
	}
	return nil
}
