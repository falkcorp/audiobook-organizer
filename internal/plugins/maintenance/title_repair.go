// file: internal/plugins/maintenance/title_repair.go
// version: 1.6.0
// guid: 13bedd46-9b61-41a2-b791-36813d7ffcb9
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
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
		if st.HasUserOverride() {
			return titleRepairDecision{Action: actionSkipProvenance, Reason: "title has user override", DirPath: dir, MixedDir: mixed}
		}
		if st.HasProviderValue() {
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

// titleRepairHelp is the def's Description, which the native kind carries as its
// Help.
const titleRepairHelp = "Re-derives each multi-file book's title via the CONS-17b all-chapters-agree check and repairs stored titles that are per-chapter tag residue (e.g. 'Big Finish Ident'). Skips single-file books and any title with override/fetched provenance. Default dry-run previews old→new; set apply=true to write."

func (p *Plugin) titleRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.title-repair",
		Preview:         sdk.PreviewSpec{Params: titleRepairParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Repair leaked chapter-tag titles (CONS-17b)",
		Description:     titleRepairHelp,
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

// errRetitleChangedUnderneath reports that the stored title was no longer the
// one the new title was derived from when the write took the row.
var errRetitleChangedUnderneath = errors.New("title changed underneath, left as it is")

// retitleBook is the title write shared by title-repair, title-backfill and
// repair-junk-titles. It sets only Title, under the book's write lock
// (ModifyBook), so a column another writer commits between the listing read
// and this write is not reverted (audit A1#15), and only while the stored
// title is still oldTitle, the value newTitle was derived from; otherwise it
// returns errRetitleChangedUnderneath and writes nothing. Provenance note:
// the persisted field-state store only holds fetched/override values; the
// file-level value IS Book.Title, and the provenance map is recomputed at read
// time, so no separate provenance write is needed.
func retitleBook(store bookModifier, id, oldTitle, newTitle string) error {
	changed := false
	written, err := store.ModifyBook(id, func(cur *database.Book) error {
		if cur.Title != oldTitle {
			return database.ErrSkipBookWrite
		}
		cur.Title = newTitle
		changed = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("ModifyBook: %w", err)
	}
	if written == nil {
		return errors.New("book not found")
	}
	if !changed {
		return errRetitleChangedUnderneath
	}
	return nil
}

// titleRepairWorkers caps the RunItems pool: the per-book work is disk-IO +
// tag-read bound, so more than 8 workers just thrashes the disk.
func titleRepairWorkers() int {
	n := max(min(runtime.NumCPU(), 8), 1)
	return n
}

// titleRepairParallelism is the op's concurrency: the RunItems pool of
// titleRepairWorkers() workers.
func titleRepairParallelism() tasks.Parallelism {
	return tasks.Fixed(titleRepairWorkers(), "per-book work is disk-IO and tag-read bound, so more than 8 workers just thrashes the disk; each worker takes a disjoint book, so no two touch the same row")
}

func (p *Plugin) runTitleRepair(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params titleRepairParams // Apply=false → dry-run (safe default)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	env := titleRepairEnv{
		t6g7Env: t6g7HeadEnv(reporter, params.Apply),
		AgreedTitle: func(dir string) (string, bool) {
			return metadata.AgreedChapterTitle(dir, config.AppConfig.SupportedExtensions)
		},
	}
	if store := p.deps.OpsStore(); store != nil {
		env.Store = store
		env.Writer = store
	}
	return titleRepairCore(ctx, params.Apply, env)
}

// titleRepairKind is rule R3's kind for the def above.
const titleRepairKind = tasks.Kind("books.maintenance.title-repair")

// titleRepairStore is the reads the sweep makes.
type titleRepairStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
}

// titleRepairWriter is the one write: the title, under the book's write lock.
type titleRepairWriter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// titleRepairEnv is what the core takes from its shell.
type titleRepairEnv struct {
	t6g7Env
	Store  titleRepairStore
	Writer titleRepairWriter
	// AgreedTitle is the CONS-17b all-chapters-agree read of a directory's tags
	// (HEAD: metadata.AgreedChapterTitle over the configured extensions).
	AgreedTitle func(dir string) (agreed string, multi bool)
}

// titleRepairNative is the native kind. Preview is the dry run HEAD's default
// was; Live writes the repaired titles.
func (p *Plugin) titleRepairNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(titleRepairKind, sched)
	c.Title = "Repair leaked chapter-tag titles (CONS-17b)"
	c.Help = titleRepairHelp
	// ModifyBook writes the whole row: books, the metadata cache stamp, the ident
	// rows and the author join (the shared row, kinds_resources.go).
	c.Effects = tasks.Writes(t6g7Res(t6WriterResources["ModifyBook"]...)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.title-repair")
	c.Timeout = 4 * time.Hour
	return tasks.Task(titleRepairKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			// HEAD's only param is apply, which the run's mode decides.
			if err := t6g7RefuseModeKeys(titleRepairKind, raw); err != nil {
				return struct{}{}, err
			}
			if err := t6g7NeedDep(titleRepairKind, "AgreedChapterTitle", deps.AgreedChapterTitle != nil); err != nil {
				return struct{}{}, err
			}
			live := rc.Mode() == tasks.ModeLive
			env := titleRepairEnv{t6g7Env: t6g7NativeEnv(rc), AgreedTitle: deps.AgreedChapterTitle}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			if live {
				w, err := t6g7NewWriter(titleRepairKind, deps, rc)
				if err != nil {
					return struct{}{}, err
				}
				env.Writer = w
			}
			return struct{}{}, titleRepairCore(rc.Context(), live, env)
		},
	})
}

// titleRepairCore is the body of runTitleRepair.
func titleRepairCore(ctx context.Context, apply bool, env titleRepairEnv) error {
	store := env.Store
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if !apply {
		env.Log(slog.LevelInfo, "DRY RUN — no changes will be written (set apply=true to write)")
	}

	// Load the full book list up front (Core rows are slim). Paged — no
	// silent cap: the loop runs until a short page.
	// One limit-0 call = one consistent snapshot; offset pages over the async
	// memdb can skip or repeat rows on snapshot swap (reconcile #2443).
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}

	total := len(books)
	par := titleRepairParallelism()
	env.Log(slog.LevelInfo, fmt.Sprintf(
		"title-repair starting: %d books, %d workers, apply=%v", total, par.Workers(), apply))
	if total == 0 {
		env.Progress(1, 1, "title-repair: 0 books — nothing to do")
		return nil
	}

	// Counters are atomics — the pool below is concurrent. Each
	// worker touches a disjoint book, so writes never collide on a row, but
	// counters are shared.
	var examined, retitled, skipSingle, skipNoAgree, skipProv, skipTitleOK, skipDeleted, mixedDir, changedUnder, errs atomic.Int64

	verb := "would retitle"
	if apply {
		verb = "retitled"
	}

	err = env.Items(ctx, par, total, t6g7ItemsOptions{
		// The label reads the running counters, so only HEAD's pool carries it.
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d (%s=%d err=%d)", i+1, t, verb, retitled.Load(), errs.Load())
		},
	}, func(ctx context.Context, i int) error {
		b := books[i]
		n := examined.Add(1)
		if n%500 == 0 {
			env.Log(slog.LevelInfo, fmt.Sprintf(
				"title-repair progress: %d/%d examined, %d %s, %d errors", n, total, retitled.Load(), verb, errs.Load()))
		}

		// Cheap pre-gate before any per-book DB reads.
		if b.IsSoftDeleted() {
			skipDeleted.Add(1)
			return nil
		}

		files, ferr := env.Store.GetBookFiles(b.ID)
		if ferr != nil {
			errs.Add(1)
			env.Log(slog.LevelWarn, fmt.Sprintf("book %s: GetBookFiles failed: %v", b.ID, ferr))
			return nil // non-fatal: keep sweeping
		}
		paths := make([]string, 0, len(files))
		for _, f := range files {
			if f.FilePath != "" {
				paths = append(paths, f.FilePath)
			}
		}

		var titleState *database.MetadataFieldState
		states, serr := env.Store.GetMetadataFieldStates(b.ID)
		if serr != nil {
			errs.Add(1)
			env.Log(slog.LevelWarn, fmt.Sprintf("book %s: GetMetadataFieldStates failed: %v", b.ID, serr))
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
		}, env.AgreedTitle)

		if d.MixedDir {
			mixedDir.Add(1)
			env.Log(slog.LevelDebug, fmt.Sprintf(
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
			env.Log(slog.LevelInfo, fmt.Sprintf(
				"book %s: %s %q → %q", b.ID, verb, b.Title, d.NewTitle))
			if apply {
				// retitleBook patches ONLY Title under the book's write lock,
				// and only while the stored title is still the one the
				// decision above was made against.
				if uerr := retitleBook(env.Writer, b.ID, b.Title, d.NewTitle); uerr != nil {
					if errors.Is(uerr, errRetitleChangedUnderneath) {
						changedUnder.Add(1)
						env.Log(slog.LevelInfo, fmt.Sprintf("book %s: title changed underneath, not retitled: %v", b.ID, uerr))
						return nil
					}
					errs.Add(1)
					env.Log(slog.LevelWarn, fmt.Sprintf("book %s: retitle failed: %v", b.ID, uerr))
					return nil
				}
			}
			retitled.Add(1)
		}
		if d.Action != actionRetitle && d.Reason != "" {
			env.Log(slog.LevelDebug, fmt.Sprintf("book %s: skip — %s", b.ID, d.Reason))
		}
		return nil
		return nil
	})
	if err != nil {
		return err
	}

	suffix := ""
	if !apply {
		suffix = " (dry run — no writes)"
	}
	result := fmt.Sprintf(
		"title-repair complete: examined=%d %s=%d skipped_single_file=%d skipped_no_agreement=%d skipped_provenance=%d skipped_title_ok=%d skipped_deleted=%d mixed_dir=%d changed_underneath=%d errors=%d%s",
		examined.Load(), strings.ReplaceAll(verb, " ", "_"), retitled.Load(), skipSingle.Load(), skipNoAgree.Load(),
		skipProv.Load(), skipTitleOK.Load(), skipDeleted.Load(), mixedDir.Load(), changedUnder.Load(), errs.Load(), suffix)
	env.Log(slog.LevelInfo, result)
	env.Progress(total, total, result)

	if errs.Load() > 0 {
		return fmt.Errorf("%d per-book errors (see op log for details)", errs.Load())
	}
	return nil
}
