// file: internal/plugins/maintenance/title_backfill.go
// version: 1.9.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/titleutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

type titleBackfillParams struct {
	// DryRun defaults to TRUE when omitted (opmode.ResolveDryRun): a request
	// that states no mode is a preview. dry_run is accepted as an alias, and
	// sending both with different values is refused rather than guessed.
	DryRun      *bool `json:"dryRun,omitempty"`
	DryRunSnake *bool `json:"dry_run,omitempty"`
}

// pendingUpdate holds a book whose title needs stripping. book is Core (slim)
// — Title/ID are Core-safe fields, and the actual writeback in phase 2
// hydrates a full row via GetBookByID before applying newTitle, so it never
// wipes the denormalized Author/Series on Pebble.
type pendingUpdate struct {
	book     database.BookCore
	newTitle string
}

// titleBackfillHelp is the def's Description, which the native kind carries as
// its Help.
const titleBackfillHelp = "Scans all books and removes leading chapter/track markers from Book.Title (e.g. '(76/85) Tarkin' → 'Tarkin'). Default dry-run previews changes; set dryRun=false to apply."

func (p *Plugin) titleBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.title-backfill",
		Preview:         sdk.PreviewSpec{Params: titleBackfillParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Strip chapter prefixes from book titles",
		Description:     titleBackfillHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.title-backfill",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runTitleBackfill,
	}
}

func (p *Plugin) runTitleBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params titleBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun("maintenance.title-backfill", params.DryRunSnake, params.DryRun)
	if err != nil {
		return err
	}

	env := titleBackfillEnv{t6g7Env: t6g7HeadEnv(reporter, !dryRun)}
	if store := p.deps.OpsStore(); store != nil {
		env.Store = store
		env.Writer = store
	}
	return titleBackfillCore(ctx, dryRun, env)
}

// titleBackfillKind is rule R3's kind for the def above.
const titleBackfillKind = tasks.Kind("books.maintenance.title-backfill")

// titleBackfillStore is the reads the scan makes.
type titleBackfillStore interface {
	// CountPrimaryBooks is the O(1) counter that gives the progress bar its total.
	CountPrimaryBooks() (int, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

// titleBackfillWriter is the one write: the title, under the book's write lock.
type titleBackfillWriter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// titleBackfillEnv is what the core takes from its shell.
type titleBackfillEnv struct {
	t6g7Env
	Store  titleBackfillStore
	Writer titleBackfillWriter
}

// titleBackfillNative is the native kind. Preview is the dry run HEAD's default
// was; Live writes the stripped titles.
//
// The retitle loop is sequential, as HEAD's was: one ModifyBook per book that
// needs a new title, in listing order. Parallelism is Sequential to keep R4's
// 1:1; a pool over disjoint books is the follow-up in todo.d.
func (p *Plugin) titleBackfillNative(deps t6g7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g7Common(titleBackfillKind, sched)
	c.Title = "Strip chapter prefixes from book titles"
	c.Help = titleBackfillHelp
	// ModifyBook writes the whole row: books, the metadata cache stamp, the ident
	// rows and the author join (the shared row, kinds_resources.go).
	c.Effects = tasks.Writes(t6g7Res(t6WriterResources["ModifyBook"]...)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.title-backfill")
	c.Timeout = 30 * time.Minute
	return tasks.Task(titleBackfillKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			// HEAD's only params are the dry-run switches, which the run's mode decides.
			if err := t6g7RefuseModeKeys(titleBackfillKind, raw); err != nil {
				return struct{}{}, err
			}
			live := rc.Mode() == tasks.ModeLive
			env := titleBackfillEnv{t6g7Env: t6g7NativeEnv(rc)}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			if live {
				w, err := t6g7NewWriter(titleBackfillKind, deps, rc)
				if err != nil {
					return struct{}{}, err
				}
				env.Writer = w
			}
			return struct{}{}, titleBackfillCore(rc.Context(), !live, env)
		},
	})
}

// titleBackfillCore is the body of runTitleBackfill.
func titleBackfillCore(ctx context.Context, dryRun bool, env titleBackfillEnv) error {
	store := env.Store
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if dryRun {
		env.Log(slog.LevelInfo, "DRY RUN — no changes will be written")
	}

	// Phase 1: get real total upfront — O(1) counter lookup in PebbleDB.
	// Falls back to 0 (indeterminate bar) if the count isn't available.
	totalBooks, countErr := store.CountPrimaryBooks()
	if countErr != nil || totalBooks <= 0 {
		totalBooks = 0
	}
	env.Progress(0, totalBooks, "Phase 1/2: scanning titles…")

	var scanned, skipped int
	var toUpdate []pendingUpdate

	// One limit-0 call = one consistent snapshot; offset pages over the async
	// memdb can skip or repeat rows on snapshot swap (reconcile #2443).
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}
	for _, book := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		scanned++

		cleaned := titleutil.StripChapterPrefix(book.Title)
		switch {
		case cleaned == book.Title:
			skipped++
		case cleaned == "":
			env.Log(slog.LevelWarn, fmt.Sprintf(
				"book %s: title %q stripped to empty, skipping", book.ID, book.Title))
			skipped++
		default:
			toUpdate = append(toUpdate, pendingUpdate{book: book, newTitle: cleaned})
		}
	}
	{
		// Use real total if available; fall back to scanned so bar moves.
		total := totalBooks
		if total == 0 {
			total = scanned
		}
		env.Progress(scanned, total,
			fmt.Sprintf("Phase 1/2: scanned %d/%d — %d titles to fix", scanned, total, len(toUpdate)))
	}

	// Phase 2: apply (or preview). We now know exactly how many to change,
	// so the bar switches to changed/toChange with real percentages.
	toChange := len(toUpdate)
	if toChange == 0 {
		suffix := ""
		if dryRun {
			suffix = " (dry run)"
		}
		result := fmt.Sprintf("Scanned %d books: 0 titles need updating, %d skipped%s", scanned, skipped, suffix)
		env.Log(slog.LevelInfo, result)
		env.Progress(1, 1, result)
		return nil
	}

	env.Progress(0, toChange,
		fmt.Sprintf("Phase 2/2: updating %d titles…", toChange))

	var changed, changedUnder, errCount int
	for i, u := range toUpdate {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		env.Log(slog.LevelInfo, fmt.Sprintf(
			"book %s: %q → %q", u.book.ID, u.book.Title, u.newTitle))

		if !dryRun {
			// u.book is Core (slim); retitleBook re-reads the full row and
			// writes only Title, so nothing else on the row is disturbed.
			if err := retitleBook(env.Writer, u.book.ID, u.book.Title, u.newTitle); err != nil {
				if errors.Is(err, errRetitleChangedUnderneath) {
					changedUnder++
					env.Log(slog.LevelInfo, fmt.Sprintf(
						"book %s: title changed underneath, not retitled: %v", u.book.ID, err))
					continue
				}
				errCount++
				env.Log(slog.LevelWarn, fmt.Sprintf(
					"book %s: retitle failed: %v", u.book.ID, err))
				continue
			}
		}
		changed++

		env.Progress(i+1, toChange,
			fmt.Sprintf("Phase 2/2: updated %d/%d titles", i+1, toChange))
	}

	suffix := ""
	if dryRun {
		suffix = " (dry run — no writes)"
	}
	result := fmt.Sprintf("Scanned %d books: %d titles updated, %d skipped, %d changed underneath, %d errors%s",
		scanned, changed, skipped, changedUnder, errCount, suffix)
	env.Log(slog.LevelInfo, result)
	env.Progress(toChange, toChange, result)

	if errCount > 0 {
		return fmt.Errorf("%d retitle errors (see op log for details)", errCount)
	}
	return nil
}
