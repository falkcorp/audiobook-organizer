// file: internal/plugins/maintenance/author.go
// version: 1.16.0
// guid: e5f6a7b8-c9d0-1234-ef01-456789012345
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// maintenance.author-dedup-scan was retired into dedup.author-scan (04-P14a); its
// alias was dropped 2026-10-31 after the soak. See the survivor in
// internal/server/duplicates_ops.go.

// author-split-scan and resolve-production-authors take only a mode. Both
// ignored their params until 2026-09-25 and wrote on every run, including the
// Monday 02:00 schedule. They resolve it through opmode.ParseDryRun: dry_run
// defaults to TRUE when absent, dryRun (camelCase) is accepted as an alias, and
// sending both with different values is an error rather than a guess -- the
// same contract as author-id-repair and duration-reextract, whose comment
// records the prod run that silently previewed because of a misspelled key.

// --- author-split-scan ---

// authorSplitScanDef is the only registration of this op.
// scheduler.author-split-scan ran a second copy under its own ID and
// ConcurrencyKey until 2026-10-13 (04-P4e); it resolves here as a FormerID and
// the author_split_scan scheduler task enqueues this def with an empty body, so
// a scheduled run is a preview.
//
// The two bodies were the same algorithm, but this one carried every later fix
// and the scheduler copy lacked them: the primary-author write here skips a
// book whose primary moved off the composite meanwhile (ErrSkipBookWrite); a
// GetBookAuthors failure is counted and logged here instead of silently
// skipping the book; and "books updated" here counts only primary-author
// writes that landed, where the scheduler copy counted every relinked book.
// The survivor's behaviour is kept in all three, so nothing was ported.
//
// Permissions and Timeout are the scheduler copy's (it gated on settings.manage
// and carried 2h), so the alias neither widens who can run a deleting op nor
// shortens the budget prod runs were sized against.
//
// Neither copy used to hold the scan stand-down or declare a write set, though
// the apply rewrites author and book rows; the survivor now does both, like
// author-duplicate-merge and author-id-repair. The stand-down is taken only for
// an apply that found a composite, never for a preview, and every book checks
// the lease before its write (RunItems beats it per item).
//
// The apply runs on a bounded pool (authorSplitScanWorkers). It is partitioned
// so workers never touch the same row: the parts are found or created
// sequentially, once each; the work list is deduplicated by book ID, one item
// applying every composite substitution for its book; the pool only rewrites
// those books' credits and primary author; and the composites are deleted
// sequentially after the pool drains.
//
// ResumePolicy stays ResumeRequeue (the scheduler copy was ResumeDrop) because
// the apply is idempotent and crash-safe, so a requeue after a restart cannot
// split an author twice or lose a credit. A composite is deleted only after
// VerifyAuthorUnlinked passes, and that check re-reads
// GetBooksByAuthorIDForRelinkCore, which lists a book by its junction row OR its
// legacy AuthorID, in any state including the trash. Each book takes its two
// writes in an order that keeps that evidence until the credits are safe: the
// parts are credited FIRST (the composite stays listed through the primary
// AuthorID, or the junction row not yet removed), and only then is the primary
// repointed. A failure or crash between the writes leaves the book still listed
// under the composite, so the composite survives, and the rerun finds the parts
// already credited, repoints the primary and deletes the composite. A part that
// already exists is found by name rather than created again, an already-linked
// part is not linked again, and both writes skip a book that no longer names the
// composite (TestAuthorSplitScan_NoCreditLostWhenEitherWriteFails,
// TestAuthorSplitScan_RequeueAfterPartialApply).
// authorSplitScanHelp is the def's Description, which the native kind carries as
// its Help.
const authorSplitScanHelp = "Finds and splits composite author names (e.g. 'Smith, J. & Jones, A.'). " +
	"DRY RUN BY DEFAULT, including the scheduled run: lists each composite and its parts; " +
	"pass dry_run=false to create the parts, relink books and delete the composite."

func (p *Plugin) authorSplitScanDef() sdk.OperationDef {
	sched := "0 2 * * 1" // 02:00 every Monday
	return sdk.OperationDef{
		ID:              "maintenance.author-split-scan",
		Preview:         sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		FormerIDs:       []string{"scheduler.author-split-scan"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Writes:          []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Author split scan",
		Description:     authorSplitScanHelp,
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-split-scan",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorSplitScan,
	}
}

// authorSplitScanWorkers is the apply's book pool size. The work is Pebble reads
// and writes on disjoint books (see authorSplitItem), not network or CPU, so a
// small fixed pool keeps the store busy without piling writers on one stripe.
const authorSplitScanWorkers = 4

// authorSplitLog is the server-log channel for the apply's failures; the op
// reporter's log reaches only the operation row.
var authorSplitLog = logger.New("maintenance")

// authorSplitComposite is a composite author found by the classification pass.
type authorSplitComposite struct {
	author database.Author
	parts  []string
}

// authorSplitPlan is a composite whose parts all resolved. newAuthors is final
// before any worker starts; workers only read it.
type authorSplitPlan struct {
	composite  database.Author
	parts      []string
	newAuthors []database.Author
	// skipDelete is set (sequentially, before the pool starts) when the
	// composite's book list could not be read, so it must not be deleted.
	skipDelete bool
}

// authorSplitItem is one book and every composite substitution it takes. A book
// credited to two composites is ONE item that applies both, so two workers can
// never rewrite the same book's credits or primary author: the pool's items are
// disjoint by book ID. Author rows are only ever created or deleted outside the
// pool (before it starts and after it has drained), so the pool shares no
// mutable state beyond the atomic counters.
type authorSplitItem struct {
	book  database.BookCore
	plans []*authorSplitPlan
}

// authorSplitRun holds the apply's shared counters. Every field is read by the
// RunItems Label closure from worker goroutines, so all are atomics.
type authorSplitRun struct {
	store  authorSplitStore
	writer authorSplitWriter
	env    t6g1Env

	attempted    atomic.Int64
	booksUpdated atomic.Int64
	relinkErrs   atomic.Int64
	errCount     atomic.Int64
	// wrote records that any author or book row changed, so the dedup cache is
	// dropped even when the apply stops early.
	wrote atomic.Bool
}

// relinkBook applies every substitution of one book, in two writes whose ORDER
// is the crash-safety argument.
//
// First the credits: the composite is replaced by its parts inside
// ModifyBookAuthors, so a credit list another writer changes meanwhile is not
// reverted and a book that no longer carries the composite is skipped. The
// composite counts as credited if the junction names it OR the fresh row's
// AuthorID does: a book credited only through its legacy AuthorID (the default
// shape CreateBook leaves) has no junction row, and its parts must still be
// credited. The fresh row is read inside the callback with GetBookByID, which
// takes no lock on the Pebble store (the callback runs under the book_authors
// stripe, and setBookAuthorsLocked already makes the same read there), so the
// decision sees what a writer committed after the listing. Credits go first
// because the legacy AuthorID is the only evidence of the composite on such a
// book: while it is unchanged, GetBooksByAuthorIDForRelinkCore keeps listing the
// book, VerifyAuthorUnlinked refuses to delete the composite, and a failure or
// crash after this write loses nothing.
//
// Then the primary author, only if the credits write did not fail: ModifyBook
// repoints AuthorID, deciding on the FRESH row. `book` is a BookCore (heavy
// fields nil), so its ToBook() projection is never written: it carries nil
// Author/Series AND, because this op changes AuthorID, the guard-preserved
// Author would be STALE (it still names the composite). ModifyBook re-reads the
// full stored row under its write lock and sets BOTH the new AuthorID and a
// fresh denormalized Author, so the row stays consistent (Author.ID ==
// AuthorID), not preserved-stale (STOREFID W5d-1 / #1887), and a column another
// writer commits meanwhile is not reverted (audit A1#15). A primary moved off
// the composite meanwhile is left alone.
func (r *authorSplitRun) relinkBook(it *authorSplitItem) {
	book := it.book

	wroteCredits := false
	_, err := r.writer.ModifyBookAuthors(book.ID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		var fresh *database.Book
		freshRead := false
		updated := cur
		changed := false
		for _, pl := range it.plans {
			credited := false
			role := "author"
			for _, ba := range updated {
				if ba.AuthorID == pl.composite.ID {
					credited, role = true, ba.Role
					break
				}
			}
			if !credited {
				if !freshRead {
					var gerr error
					if fresh, gerr = r.store.GetBookByID(book.ID); gerr != nil {
						return nil, fmt.Errorf("read book %s for its primary author: %w", book.ID, gerr)
					}
					freshRead = true
				}
				if fresh == nil || fresh.AuthorID == nil || *fresh.AuthorID != pl.composite.ID {
					continue // the composite left this book meanwhile
				}
			}
			var next []database.BookAuthor
			for _, ba := range updated {
				if ba.AuthorID != pl.composite.ID {
					next = append(next, ba)
				}
			}
			for _, na := range pl.newAuthors {
				linked := false
				for _, ba := range next {
					if ba.AuthorID == na.ID {
						linked = true
						break
					}
				}
				if !linked {
					next = append(next, database.BookAuthor{BookID: book.ID, AuthorID: na.ID, Role: role, Position: len(next)})
					changed = true
				}
			}
			if credited {
				changed = true
			}
			updated = next
		}
		if !changed {
			return nil, database.ErrSkipBookAuthorsWrite
		}
		wroteCredits = true
		return updated, nil
	})
	if err != nil {
		r.relinkErrs.Add(1)
		r.errCount.Add(1)
		r.env.Log(slog.LevelWarn, fmt.Sprintf("author split: credits of book %s not relinked: %v", book.ID, err))
		authorSplitLog.Warn("author split: credits of book %s not relinked: %s", logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(err.Error()))
		return // the primary stays on the composite, which keeps the book listed under it
	}
	if wroteCredits {
		r.wrote.Store(true)
	}

	// The one plan, if any, whose composite the listed row names as primary.
	var primaryPlan *authorSplitPlan
	if book.AuthorID != nil {
		for _, cand := range it.plans {
			if cand.composite.ID == *book.AuthorID {
				primaryPlan = cand
				break
			}
		}
	}
	if primaryPlan == nil {
		return
	}
	composite := primaryPlan.composite
	newPrimary := primaryPlan.newAuthors[0]
	firstID := newPrimary.ID
	applied := false
	written, err := r.writer.ModifyBook(book.ID, func(full *database.Book) error {
		if full.AuthorID == nil || *full.AuthorID != composite.ID {
			return database.ErrSkipBookWrite
		}
		applied = true
		full.AuthorID = &firstID
		full.Author = &newPrimary
		return nil
	})
	switch {
	case err != nil:
		r.errCount.Add(1)
		r.env.Log(slog.LevelWarn, fmt.Sprintf("author split: failed to update primary author on book %s: %v", book.ID, err))
		authorSplitLog.Warn("author split: primary author write failed: book=%s author=%q err=%s",
			logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(composite.Name), logger.SanitizeLogValue(err.Error()))
	case written == nil:
		r.errCount.Add(1)
		r.env.Log(slog.LevelWarn, fmt.Sprintf("author split: book %s no longer exists, primary author not updated", book.ID))
	case !applied:
		r.env.Log(slog.LevelInfo, fmt.Sprintf("author split: book %s left author %q meanwhile, primary author not updated", book.ID, composite.Name))
	default:
		r.booksUpdated.Add(1)
		r.wrote.Store(true)
	}
}

func (p *Plugin) runAuthorSplitScan(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) (retErr error) {
	dryRun, err := opmode.ParseDryRun("maintenance.author-split-scan", raw)
	if err != nil {
		return err
	}
	env := authorSplitEnv{
		t6g1Env:              t6g1HeadEnv(reporter, !dryRun),
		Items:                t6g1HeadItems(reporter),
		Hold:                 p.t6g1HeadHold(reporter),
		InvalidateDedupCache: p.deps.InvalidateDedupCache,
	}
	// A nil OpsStore stays a nil interface in env.Store, which the core reports
	// as "database not initialized".
	if store := p.deps.OpsStore(); store != nil {
		env.Store, env.Writer = store, store
	}
	return authorSplitScanCore(ctx, env)
}

// authorSplitScanKind is rule R3's kind for the def above.
const authorSplitScanKind = tasks.Kind("books.maintenance.author-split-scan")

// authorSplitStore is the reads the op makes.
type authorSplitStore interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetBookByID(id string) (*database.Book, error)
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
}

// authorSplitWriter is the writes the op makes: the parts it creates, the two
// per-book writes and the composite's delete.
type authorSplitWriter interface {
	CreateAuthor(name string) (*database.Author, error)
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	DeleteAuthor(id int) error
}

// authorSplitEnv is what the core takes from its shell.
type authorSplitEnv struct {
	t6g1Env
	Store  authorSplitStore
	Writer authorSplitWriter
	// Items runs the apply's book pool.
	Items t6g1ItemsFunc
	// Hold takes the library scan stand-down for the apply.
	Hold func(ctx context.Context, reason string) (t6g1Held, error)
	// InvalidateDedupCache drops the duplicate-author cache after a write.
	InvalidateDedupCache func()
}

// authorSplitScanParallelism is the apply's book pool (authorSplitScanWorkers).
func authorSplitScanParallelism() tasks.Parallelism {
	return tasks.Fixed(authorSplitScanWorkers, "Pebble reads and writes on disjoint books; a small fixed pool keeps the store busy without piling writers on one stripe")
}

// authorSplitScanNative is the native kind. Preview is the dry run (reads
// only); Live applies.
func (p *Plugin) authorSplitScanNative(deps t6g1Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g1Common(authorSplitScanKind, sched)
	c.Title = "Author split scan"
	c.Help = authorSplitScanHelp
	c.Effects = tasks.Writes(tasks.Res("books", "books"), tasks.Res("books", "authors"))
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.author-split-scan")
	c.Timeout = 2 * time.Hour
	return tasks.Task(authorSplitScanKind, tasks.TaskSpec[struct{}, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			base := t6g1NativeEnv(rc)
			items := t6g1NativeItems(rc)
			env := authorSplitEnv{
				t6g1Env:              base,
				Writer:               deps.NewWriter(rc.Writer()),
				Items:                items,
				Hold:                 t6g1NativeHold(rc, base, items, deps.StandDownKey),
				InvalidateDedupCache: deps.InvalidateDedupCache,
			}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			return struct{}{}, authorSplitScanCore(rc.Context(), env)
		},
	})
}

// authorSplitScanCore is the body of runAuthorSplitScan from the store check
// on; env.Live is the apply switch (dry_run=false).
func authorSplitScanCore(ctx context.Context, env authorSplitEnv) (retErr error) {
	dryRun := !env.Live
	store := env.Store
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("Starting author split scan (dry_run=%v)", dryRun))

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("Scanning %d authors for composite names...", len(authors)))

	// Classification: pure name splitting, no store call, so it stays one loop.
	total := len(authors)
	prog := sdk.NewProgress(t6g1ProgressSink{fn: env.Progress}, total)
	prog.Start(fmt.Sprintf("Scanning %d authors for composite names...", total))
	var composites []authorSplitComposite
	for i, author := range authors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if parts := dedup.SplitCompositeAuthorName(author.Name); len(parts) > 1 {
			composites = append(composites, authorSplitComposite{author: author, parts: parts})
		}
		if (i+1)%200 == 0 {
			prog.StepN(i+1, fmt.Sprintf("Checked %d/%d authors", i+1, total))
		}
	}

	if dryRun {
		return previewAuthorSplit(ctx, store, env.t6g1Env, prog, composites)
	}
	if len(composites) == 0 {
		resultMsg := "Split 0 composite authors, updated 0 books (0 errors, 0 books not relinked)"
		env.Log(slog.LevelInfo, resultMsg)
		prog.Done(resultMsg)
		return nil
	}

	// The apply rewrites author and book rows, so the library scan stands down
	// for it, as it does for author-duplicate-merge and author-id-repair. It is
	// taken only now that a composite exists: a pass that finds nothing to split
	// must not park a running scan. RunItems beats the lease per book and stops
	// before the next write once it is lost.
	hold, sdErr := env.Hold(ctx, "maintenance.author-split-scan apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	// The hold rebinds the context (canceled when the lease is lost) and the
	// progress/log/pool the write phase reports through.
	ctx, env.t6g1Env, env.Items = hold.Ctx, hold.Env, hold.Items

	run := &authorSplitRun{store: store, writer: env.Writer, env: env.t6g1Env}
	// Any row written means the duplicate-author cache is stale, including when
	// the apply stops early (cancel, Timeout, lost lease).
	defer func() {
		if run.wrote.Load() {
			env.InvalidateDedupCache()
		}
	}()

	// Step 1, sequential: find or create each composite's parts, exactly once.
	// Doing it here, not in the pool, is what keeps two composites that share a
	// part ("A & B" and "A & C") from both creating "A".
	var plans []*authorSplitPlan
	for _, c := range composites {
		if cerr := hold.Checkpoint(); cerr != nil {
			return fmt.Errorf("author-split-scan stopped before creating the parts of %q: %w", c.author.Name, cerr)
		}
		var newAuthors []database.Author
		partFailed := false
		for _, name := range c.parts {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			existing, gerr := store.GetAuthorByName(name)
			if gerr == nil && existing != nil {
				newAuthors = append(newAuthors, *existing)
				continue
			}
			created, cerr := env.Writer.CreateAuthor(name)
			if errors.Is(cerr, database.ErrImplausibleAuthorName) {
				// A junk part ("read by narrator" split out of
				// "Stephen King, read by narrator") is dropped on
				// purpose: it was never a credit worth keeping.
				continue
			}
			if cerr != nil {
				run.errCount.Add(1)
				partFailed = true
				env.Log(slog.LevelWarn, fmt.Sprintf("Failed to create author %q: %v", name, cerr))
				continue
			}
			run.wrote.Store(true)
			newAuthors = append(newAuthors, *created)
		}
		// Every real part must resolve, or the composite is left alone. The
		// relink below REPLACES the composite credit with newAuthors and the
		// composite row is then deleted, so a store error on one part would
		// silently drop that person's credit from every book. Only a part
		// the creation gate refuses (database.ErrImplausibleAuthorName) is
		// dropped, because it was junk rather than a person.
		if partFailed || len(newAuthors) == 0 {
			continue
		}
		plans = append(plans, &authorSplitPlan{composite: c.author, parts: c.parts, newAuthors: newAuthors})
	}

	// Step 2, sequential: the work list, deduplicated by book ID. ForRelink
	// includes the trash: DeleteAuthor below sweeps trashed books' junction rows
	// too, so a trashed book skipped here would lose its credit.
	var items []*authorSplitItem
	byBook := make(map[string]*authorSplitItem)
	for _, pl := range plans {
		// Listing falls back to a Pebble scan when memdb is incomplete, and the
		// parts are already created, so the lease is checked per composite here
		// too.
		if cerr := hold.Checkpoint(); cerr != nil {
			return fmt.Errorf("author-split-scan stopped before listing books of %q: %w", pl.composite.Name, cerr)
		}
		books, lerr := store.GetBooksByAuthorIDForRelinkCore(pl.composite.ID)
		if lerr != nil {
			run.errCount.Add(1)
			pl.skipDelete = true
			env.Log(slog.LevelWarn, fmt.Sprintf("Failed to get books for author %q: %v", pl.composite.Name, lerr))
			continue
		}
		for _, b := range books {
			it := byBook[b.ID]
			if it == nil {
				it = &authorSplitItem{book: b}
				byBook[b.ID] = it
				items = append(items, it)
			}
			it.plans = append(it.plans, pl)
		}
	}

	// Step 3, the pool: one item per book, disjoint by book ID.
	runErr := env.Items(ctx, len(items), t6g1ItemsOptions{
		Concurrency: authorSplitScanWorkers,
		Label: func(i, n int) string {
			return fmt.Sprintf("Book %d/%d (%d primary authors updated, %d errors)", i+1, n, run.booksUpdated.Load(), run.errCount.Load())
		},
	}, func(_ context.Context, i int) error {
		if env.Canceled() {
			return context.Canceled
		}
		run.attempted.Add(1)
		run.relinkBook(items[i])
		return nil
	})
	if runErr != nil {
		env.Log(slog.LevelWarn, fmt.Sprintf("Stopped after %d of %d books: %v", run.attempted.Load(), len(items), runErr))
		return fmt.Errorf("author-split-scan stopped after %d of %d books: %w", run.attempted.Load(), len(items), runErr)
	}

	// Step 4, sequential: delete each composite, only once nothing still credits
	// it. Every per-book failure above is logged and skipped, so the composite
	// may still be credited, and deleting it then would erase that credit.
	splitCount := 0
	for _, pl := range plans {
		if pl.skipDelete {
			continue
		}
		if cerr := hold.Checkpoint(); cerr != nil {
			return fmt.Errorf("author-split-scan stopped before deleting composite %q: %w", pl.composite.Name, cerr)
		}
		if verr := database.VerifyAuthorUnlinked(store, pl.composite.ID); verr != nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("Composite author %q NOT deleted: %v", pl.composite.Name, verr))
			run.errCount.Add(1)
		} else if derr := env.Writer.DeleteAuthor(pl.composite.ID); derr != nil {
			env.Log(slog.LevelWarn, fmt.Sprintf("Failed to delete composite author %q: %v", pl.composite.Name, derr))
			run.errCount.Add(1)
		} else {
			splitCount++
			run.wrote.Store(true)
			env.Log(slog.LevelInfo, fmt.Sprintf("Split %q → %v", pl.composite.Name, pl.parts))
		}
	}

	resultMsg := fmt.Sprintf("Split %d composite authors, updated %d books (%d errors, %d books not relinked due to credit-list errors)",
		splitCount, run.booksUpdated.Load(), run.errCount.Load(), run.relinkErrs.Load())
	env.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}

// previewAuthorSplit is the dry run: reads only, no CreateAuthor, SetBookAuthors,
// ModifyBook or DeleteAuthor, and no scan stand-down. Every composite is logged
// with its parts, because the splitter is known to cut titles into "authors" and
// the list is what an operator reviews before passing dry_run=false. It loops
// over the composites alone (a short list next to the library) and costs two
// indexed reads each, so it stays sequential.
func previewAuthorSplit(ctx context.Context, store authorSplitStore, env t6g1Env, prog *sdk.Progress, composites []authorSplitComposite) error {
	errCount := 0
	wouldSplit, wouldCreate, wouldRelink := 0, 0, 0
	for _, c := range composites {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		missing := 0
		for _, name := range c.parts {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if existing, lerr := store.GetAuthorByName(name); lerr != nil || existing == nil {
				missing++
			}
		}
		books, berr := store.GetBooksByAuthorIDForRelinkCore(c.author.ID)
		if berr != nil {
			errCount++
			env.Log(slog.LevelWarn, fmt.Sprintf("Failed to get books for author %q: %v", c.author.Name, berr))
			continue
		}
		wouldSplit++
		wouldCreate += missing
		wouldRelink += len(books)
		env.Log(slog.LevelInfo, fmt.Sprintf("Would split %q (id %d) → %q (%d new authors, %d books)",
			c.author.Name, c.author.ID, c.parts, missing, len(books)))
	}
	resultMsg := fmt.Sprintf("DRY RUN: would split %d composite authors, create %d authors, relink %d books (%d errors); "+
		"pass dry_run=false to write", wouldSplit, wouldCreate, wouldRelink, errCount)
	env.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}

// --- resolve-production-authors ---

// resolveProductionAuthorsDef is the only registration of this op.
// scheduler.resolve-production-authors ran a second copy under its own ID and
// ConcurrencyKey until 2026-10-11 (04-P4d); it resolves here as a FormerID and
// the resolve_production_authors scheduler task enqueues this def.
//
// The copies were not twins. The scheduler copy's apply re-fetched metadata by
// title for every book credited to a production company; this def's apply
// resolved nothing (its loop only reported progress) and only invalidated the
// dedup cache. The fetching body moved here, so an apply under either ID does
// the work the scheduler copy did.
//
// Permissions and Timeout are the scheduler copy's (it gated on settings.manage
// and carried 2h), so the alias neither widens who can run a writing op nor
// shortens the budget prod runs were sized against. ResumeDrop is the scheduler
// copy's, not this def's old ResumeRequeue: the apply issues one metadata
// provider lookup per book, a requeue after a restart repeats the lookups for
// every book that did not resolve, against provider quotas, with nobody having
// asked for the second pass, and the preview is cheap enough that the next
// scheduled run replaces a dropped one (entities.resolve-production-author
// drops for the same reason).
//
// Capabilities are the ones scheduler.metadata-upgrade declares for the same
// fetch path: the title search queries the whole metadata source chain
// (Audible among the providers), and candidate scoring calls the embedding
// scorer, an OpenAI-backed client, when metadata_scoring.embedding_enabled is
// on. The scheduler copy's network.openai was right; this def's
// network.generic alone under-declared it.
//
// Writes: neither copy declared a write set, which made the dispatcher's
// write-set gate skip the op. The apply rewrites book rows (fill-only fields
// and the author credit join), can create author rows for the credits it
// resolves and series rows for the series it fills, so it declares those three
// tables, as the author ops in this plugin (author-id-repair, author-path-link)
// declare theirs: it then waits for, rather than interleaves with, another
// running writer of the same rows.
// resolveProductionAuthorsHelp is the def's Description, which the native kind
// carries as its Help.
const resolveProductionAuthorsHelp = "Resolves real authors for production company entries using external metadata. " +
	"DRY RUN BY DEFAULT, including the scheduled run: counts the books it would re-fetch; " +
	"pass dry_run=false to fetch and write."

func (p *Plugin) resolveProductionAuthorsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.resolve-production-authors",
		Preview:         sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		FormerIDs:       []string{"scheduler.resolve-production-authors"},
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Resolve production company authors",
		Description:     resolveProductionAuthorsHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.resolve-production-authors",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Schedule:        nil,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite,
			sdk.CapNetworkAudible, sdk.CapNetworkGeneric, sdk.CapNetworkOpenAI,
		},
		Writes: []sdk.Resource{sdk.ResBooks, sdk.ResAuthors, sdk.ResSeries},
		Run:    p.runResolveProductionAuthors,
	}
}

const (
	// resolveProductionAuthorsDefaultWorkers is the apply's book pool when
	// metadata_scoring.bulk_fetch_workers is unset: the default the bulk
	// metadata fetch uses for the same provider-bound work
	// (server.defaultBulkFetchWorkers).
	resolveProductionAuthorsDefaultWorkers = 4
	// resolveProductionAuthorsMaxWorkers caps a configured value, as
	// metafetch.asin-backfill caps its book pool.
	resolveProductionAuthorsMaxWorkers = 8
)

// resolveProductionAuthorsWorkers is the apply's book pool size. Network-bound,
// so deliberately below NumCPU: each fetch already fans out over the metadata
// source chain (source_fanout_workers per book), and every provider has its own
// token bucket in internal/metadata/providerhttp, so the buckets are the rate
// limit and the pool only keeps them busy.
func resolveProductionAuthorsWorkers() int {
	w := config.AppConfig.MetadataScoring.BulkFetchWorkers
	if w <= 0 {
		w = resolveProductionAuthorsDefaultWorkers
	}
	return min(w, resolveProductionAuthorsMaxWorkers)
}

func (p *Plugin) runResolveProductionAuthors(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) (retErr error) {
	dryRun, err := opmode.ParseDryRun("maintenance.resolve-production-authors", raw)
	if err != nil {
		return err
	}
	env := resolveProductionEnv{
		t6g1Env:              t6g1HeadEnv(reporter, !dryRun),
		Items:                t6g1HeadItems(reporter),
		Hold:                 p.t6g1HeadHold(reporter),
		InvalidateDedupCache: p.deps.InvalidateDedupCache,
		Fetch:                t6g1HeadFetcher{p.deps},
		Workers:              resolveProductionAuthorsWorkers(),
	}
	// A nil OpsStore stays a nil interface in env.Store, which the core reports
	// as "database not initialized".
	if store := p.deps.OpsStore(); store != nil {
		env.Store = store
	}
	return resolveProductionAuthorsCore(ctx, env)
}

// resolveProductionAuthorsKind is rule R3's kind for the def above.
const resolveProductionAuthorsKind = tasks.Kind("books.maintenance.resolve-production-authors")

// resolveProductionStore is the reads the op makes.
type resolveProductionStore interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
}

// t6g1MetadataFetcher is the metadata fetch the apply runs per book. The fetch
// applies what it finds (fill-only book fields, the author credit join, series
// rows), so the native fetcher is built per run over that run's writer
// (t6g1Deps.NewMetadataFetcher): its writes do not go around the gate.
type t6g1MetadataFetcher interface {
	// HasService reports whether the metadata fetch service is wired.
	HasService() bool
	// FetchByTitle re-fetches the book's metadata by title and applies it,
	// returning the author id the book now has.
	FetchByTitle(ctx context.Context, bookID string) (*int, error)
}

// resolveProductionEnv is what the core takes from its shell. The op's writes
// are the fetcher's: the core itself writes nothing, so it has no writer.
type resolveProductionEnv struct {
	t6g1Env
	Store resolveProductionStore
	Fetch t6g1MetadataFetcher
	// Items runs the apply's book pool.
	Items t6g1ItemsFunc
	// Workers is the pool size (resolveProductionAuthorsWorkers).
	Workers int
	// Hold takes the library scan stand-down for the apply.
	Hold func(ctx context.Context, reason string) (t6g1Held, error)
	// InvalidateDedupCache drops the duplicate-author cache after a fetch ran.
	InvalidateDedupCache func()
}

// resolveProductionAuthorsParallelism is the apply's book pool: network-bound,
// sized by metadata_scoring.bulk_fetch_workers.
func resolveProductionAuthorsParallelism() tasks.Parallelism {
	return tasks.Fixed(resolveProductionAuthorsWorkers(), "network-bound metadata fetches; each provider has its own token bucket, the pool only keeps them busy")
}

// resolveProductionAuthorsNative is the native kind. Preview counts the books;
// Live fetches and writes.
func (p *Plugin) resolveProductionAuthorsNative(deps t6g1Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g1Common(resolveProductionAuthorsKind, sched)
	c.Title = "Resolve production company authors"
	c.Help = resolveProductionAuthorsHelp
	c.Effects = tasks.Writes(tasks.Res("books", "books"), tasks.Res("books", "authors"), tasks.Res("books", "series"))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.maintenance.resolve-production-authors")
	c.Timeout = 2 * time.Hour
	return tasks.Task(resolveProductionAuthorsKind, tasks.TaskSpec[struct{}, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			base := t6g1NativeEnv(rc)
			items := t6g1NativeItems(rc)
			env := resolveProductionEnv{
				t6g1Env:              base,
				Items:                items,
				Hold:                 t6g1NativeHold(rc, base, items, deps.StandDownKey),
				InvalidateDedupCache: deps.InvalidateDedupCache,
				Workers:              resolveProductionAuthorsWorkers(),
			}
			if deps.NewMetadataFetcher != nil {
				env.Fetch = deps.NewMetadataFetcher(rc.Writer())
			} else {
				env.Fetch = t6g1NoFetcher{}
			}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			return struct{}{}, resolveProductionAuthorsCore(rc.Context(), env)
		},
	})
}

// resolveProductionAuthorsCore is the body of runResolveProductionAuthors from
// the store check on; env.Live is the apply switch (dry_run=false).
func resolveProductionAuthorsCore(ctx context.Context, env resolveProductionEnv) (retErr error) {
	dryRun := !env.Live
	store := env.Store
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if !dryRun && !env.Fetch.HasService() {
		// The apply is a metadata fetch per book. Without the service it would
		// resolve nothing and report success, which is the silent no-op this
		// def used to be.
		return fmt.Errorf("metadata fetch service not initialized")
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("Starting production author resolution (dry_run=%v)", dryRun))

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}

	var prodAuthors []database.Author
	for _, a := range authors {
		if dedup.IsProductionCompany(a.Name) {
			prodAuthors = append(prodAuthors, a)
		}
	}

	env.Log(slog.LevelInfo, fmt.Sprintf("Found %d production company authors", len(prodAuthors)))
	total := len(prodAuthors)
	lookupFailed := 0
	prog := sdk.NewProgress(t6g1ProgressSink{fn: env.Progress}, total)
	prog.Start(fmt.Sprintf("Listing books for %d production companies", total))

	// The work list, built sequentially: production companies are few and each
	// costs one indexed read. A book credited to two production companies is
	// listed ONCE, so the apply pool below works on disjoint items and two
	// workers can never fetch-and-apply the same book row at the same time.
	var bookIDs []string
	seen := make(map[string]bool)
	for i, author := range prodAuthors {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if env.Canceled() {
			return context.Canceled
		}
		books, err := store.GetBooksByAuthorIDWithRoleCore(author.ID)
		if err != nil {
			lookupFailed++
			env.Log(slog.LevelWarn, fmt.Sprintf("Could not list books for production company %q (id %d): %v", author.Name, author.ID, err))
			prog.StepN(i+1, fmt.Sprintf("Listed %d/%d production companies", i+1, total))
			continue
		}
		for _, b := range books {
			if !seen[b.ID] {
				seen[b.ID] = true
				bookIDs = append(bookIDs, b.ID)
			}
		}
		if dryRun {
			env.Log(slog.LevelInfo, fmt.Sprintf("Would re-fetch metadata for %d books credited to %q (id %d)",
				len(books), author.Name, author.ID))
		}
		prog.StepN(i+1, fmt.Sprintf("Listed %d/%d production companies", i+1, total))
	}

	if dryRun {
		// The fetch applies as well as searches, so the dry run never calls
		// it; it counts the books instead.
		resultMsg := fmt.Sprintf("DRY RUN: would re-fetch metadata for %d books across %d production companies; "+
			"pass dry_run=false to write", len(bookIDs), total)
		env.Log(slog.LevelInfo, resultMsg)
		prog.Done(resultMsg)
		return nil
	}
	if len(bookIDs) == 0 {
		// Nothing to fetch: finish without taking the scan stand-down, which
		// would park a running scan for no write.
		resultMsg := fmt.Sprintf("Resolved 0 books across %d production companies (no books to re-fetch, %d companies' book lists unreadable)",
			total, lookupFailed)
		env.Log(slog.LevelInfo, resultMsg)
		prog.Done(resultMsg)
		return nil
	}

	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first fetch (each one applies), as maintenance.metadata-refresh
	// and scheduler.metadata-upgrade do. A running scan does not make the op
	// refuse: the acquire asks it to park and waits, and the op fails only if it
	// never parks. RunItems renews the lease per book and stops before the next
	// fetch once it is lost; the hold's ctx, which every fetch runs under, is
	// cancelled then too, so an in-flight fetch stops searching and refuses its
	// write if the loss lands before its apply starts. A loss that lands during
	// the apply itself can still complete that one book's write.
	hold, sdErr := env.Hold(ctx, "maintenance.resolve-production-authors apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	// The hold rebinds the context (canceled when the lease is lost) and the
	// progress/log/pool the write phase reports through.
	ctx, env.t6g1Env, env.Items = hold.Ctx, hold.Env, hold.Items

	// Counters are atomics: the fetch callback and the Label closure both run
	// inside RunItems' worker goroutines.
	var attempted, resolved, fetchFailed atomic.Int64
	// Every fetch that ran may have rewritten a book's author credit, so the
	// dedup cache is invalidated whenever one ran, including when the apply
	// stops early (cancel, Timeout, lost lease): the books fetched before the
	// stop keep their new credits.
	defer func() {
		if attempted.Load() > 0 {
			env.InvalidateDedupCache()
		}
	}()

	runErr := env.Items(ctx, len(bookIDs), t6g1ItemsOptions{
		Concurrency: env.Workers,
		Label: func(i, n int) string {
			return fmt.Sprintf("Book %d/%d (%d resolved, %d fetches failed)", i+1, n, resolved.Load(), fetchFailed.Load())
		},
	}, func(itemCtx context.Context, i int) error {
		bookID := bookIDs[i]
		if env.Canceled() {
			return context.Canceled
		}
		attempted.Add(1)
		newAuthorID, fetchErr := env.Fetch.FetchByTitle(itemCtx, bookID)
		if fetchErr != nil {
			if ctx.Err() != nil {
				// The op stopped (cancel, Timeout, lost lease), not this book.
				return context.Cause(ctx)
			}
			fetchFailed.Add(1)
			return nil
		}
		if newAuthorID == nil {
			return nil
		}
		if newAuthor, _ := store.GetAuthorByID(*newAuthorID); newAuthor != nil && !dedup.IsProductionCompany(newAuthor.Name) {
			resolved.Add(1)
		}
		return nil
	})

	resultMsg := fmt.Sprintf("Resolved %d books across %d production companies (%d fetches failed, %d companies' book lists unreadable)",
		resolved.Load(), total, fetchFailed.Load(), lookupFailed)
	if runErr != nil {
		env.Log(slog.LevelWarn, fmt.Sprintf("Stopped after %d of %d books: %s: %v", attempted.Load(), len(bookIDs), resultMsg, runErr))
		return runErr
	}
	env.Log(slog.LevelInfo, resultMsg)
	prog.Done(resultMsg)
	return nil
}
