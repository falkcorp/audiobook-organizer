// file: internal/plugins/maintenance/author_books_legacy_retire.go
// version: 0.1.0
// guid: 4e8c2a71-9b5d-4f36-a0e2-7d1c6b9f3a58
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// maintenance.author-books-legacy-retire deletes the author_books_legacy: rows
// (F5-18j).
//
// The family was the author -> books index's copy of Book.AuthorID. Since
// F5-18j no build stages or reads it, so the stored rows are inert: deleting
// them changes nothing a current build reads, and a rolled-back build rebuilds
// them from Book.AuthorID in its own boot reconcile. The op exists so the
// family can be emptied deliberately, behind one check, instead of lingering.
//
// THE CHECK. A book whose AuthorID its credit list does not hold has, in its
// legacy row, the last index entry that ever named that author for it. The
// owner rule is that the credit list is the author source, so the op refuses
// to delete while any such book exists, in any state (trashed and non-primary
// included), and lists them: maintenance.credits-backfill (or a person) puts
// the author into the credit list first, maintenance.author-id-repair handles
// an AuthorID with no author row. The check and the refusal live in the
// store's RetireAuthorBooksLegacy, the one call that deletes, so nothing can
// delete without it. pebble_store_author_books_legacy_retire.go has the rules.
//
// Modes:
//   - preview (default, owner rule 2026-09-25): the check and the counts;
//     writes nothing. A preview that finds blocking books reports them and
//     succeeds.
//   - dry_run=false: the same check; when no book blocks, deletes every
//     author_books_legacy: row in Sync-committed chunks; when any book blocks,
//     deletes nothing and fails with the list.
//
// Idempotent and resumable (ResumeRestart): the delete is by prefix, so a run
// cut short leaves fewer rows and the next run deletes the rest, and a run over
// a retired store counts zero and deletes nothing. The book-row check runs on
// the store's worker pool sized to the CPUs (one decode and one point read per
// row). It writes no book, credit or book_file row.
const authorBooksLegacyRetireID = "maintenance.author-books-legacy-retire"

// t6g2KindAuthorBooksLegacyRetire is the native kind (rule R3).
const t6g2KindAuthorBooksLegacyRetire tasks.Kind = "books." + authorBooksLegacyRetireID

// DisplayName and Description of the def, shared by HEAD's def and the native
// kind so the two cannot drift.
const (
	t6g2AuthorBooksLegacyRetireTitle = "Retire legacy author-book index rows"
	t6g2AuthorBooksLegacyRetireHelp  = "Deletes the author_books_legacy: rows, the retired copy of each book's " +
		"AuthorID that no current build reads. First checks every book row (every state): a book whose " +
		"AuthorID is not in its credit list blocks the delete, because the legacy row is the last place " +
		"that author was indexed for it. Preview by default: the check and the counts, writes nothing. " +
		"With dry_run=false it deletes every legacy row when no book blocks, and when any book blocks it " +
		"deletes nothing and lists them (run maintenance.credits-backfill or fix them by hand, and " +
		"maintenance.author-id-repair for an author id with no author). Safe to re-run; a cancelled run " +
		"finishes on the next. It never writes a book, credit or book_file row."
)

// authorBooksLegacyRetirer is the store capability the op needs, resolved
// through database.AsCapability because OpsStore() can be a decorator that
// hides *PebbleStore's methods.
type authorBooksLegacyRetirer interface {
	CheckAuthorBooksLegacyRetire(ctx context.Context, progress database.AuthorBooksLegacyRetireProgress) (database.AuthorBooksLegacyRetireReport, error)
	RetireAuthorBooksLegacy(ctx context.Context, progress database.AuthorBooksLegacyRetireProgress) (database.AuthorBooksLegacyRetireReport, error)
}

func (p *Plugin) authorBooksLegacyRetireDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              authorBooksLegacyRetireID,
		Preview:         sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2AuthorBooksLegacyRetireTitle,
		Description:     t6g2AuthorBooksLegacyRetireHelp,
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  authorBooksLegacyRetireID,
		Writes:          []sdk.Resource{sdk.ResAuthors},
		Reads:           []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorBooksLegacyRetire,
	}
}

func (p *Plugin) runAuthorBooksLegacyRetire(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params opmode.DryRunParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", authorBooksLegacyRetireID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(authorBooksLegacyRetireID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_, err = runAuthorBooksLegacyRetireCore(ctx, dryRun, headT6G2Wire(headT6G2Env(p, reporter), p, store))
	return err
}

// runAuthorBooksLegacyRetireCore is the body shared by HEAD's shell and the
// native kind: the preview through env.Store, the delete through env.Writer.
func runAuthorBooksLegacyRetireCore(ctx context.Context, dryRun bool, env t6g2Env) (t6g2NoResult, error) {
	if !env.Store.Supports(t6g2CapAuthorBooksLegacyRetire) {
		return t6g2NoResult{}, fmt.Errorf("%s: store is %s, which has no author_books_legacy: rows to retire", authorBooksLegacyRetireID, env.Store.Describe())
	}
	// The store calls progress from one goroutine at a time; Progress is safe
	// for concurrent use anyway.
	progress := func(phase string, n int) {
		env.Progress(n, 0, fmt.Sprintf("author_books_legacy: %s: %d", phase, n))
	}
	verb := "checking, then deleting when no book blocks"
	run := env.Writer.RetireAuthorBooksLegacy
	if dryRun {
		verb = "checking (preview, deletes nothing)"
		run = env.Store.CheckAuthorBooksLegacyRetire
	}
	env.Progress(0, 0, "author_books_legacy: "+verb)
	rep, err := run(ctx, progress)

	for _, s := range rep.SampleBlocking {
		env.Log(slog.LevelWarn, "blocking: "+s)
	}
	msg := authorBooksLegacyRetireSummary(rep, dryRun)
	switch {
	case errors.Is(err, database.ErrAuthorBooksLegacyRetireRefused):
		env.Log(slog.LevelError, msg)
		env.Progress(1, 1, msg)
		return t6g2NoResult{}, fmt.Errorf("%s: %w", authorBooksLegacyRetireID, err)
	case err != nil:
		if rep.Deleted > 0 {
			env.Log(slog.LevelWarn, msg)
		}
		return t6g2NoResult{}, fmt.Errorf("%s: %w", authorBooksLegacyRetireID, err)
	}
	level := slog.LevelInfo
	if rep.Refused {
		level = slog.LevelWarn
	}
	if !dryRun && rep.LegacyRows == 0 {
		// The usual outcome once retired: nothing to do.
		env.Log(level, msg, env.NoOp)
	} else {
		env.Log(level, msg)
	}
	env.Progress(1, 1, msg)
	return t6g2NoResult{}, nil
}

// authorBooksLegacyRetireSummary is the one-line outcome: what was read, what
// blocks, what was (or would be) deleted.
func authorBooksLegacyRetireSummary(rep database.AuthorBooksLegacyRetireReport, dryRun bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "author_books_legacy: %d book rows (%d undecodable, never blocking); %d with an AuthorID, %d credited; "+
		"%d blocking (no credit row %d, not in credits %d, unreadable credits %d; %d listable, %d naming a missing author); "+
		"%d legacy rows",
		rep.BookRows, rep.UnreadableBooks, rep.WithAuthorID, rep.Credited,
		rep.Blocking, rep.NoCreditRow, rep.NotInCredits, rep.UnreadableCredits, rep.BlockingListable, rep.BlockingDanglingAuthor,
		rep.LegacyRows)
	switch {
	case dryRun && rep.Refused:
		b.WriteString("; preview, nothing deleted; a live run would refuse until every blocking book's AuthorID is in its credit list " +
			"(maintenance.credits-backfill; maintenance.author-id-repair for a missing author)")
	case dryRun:
		b.WriteString("; preview, nothing deleted (run with dry_run=false to delete them)")
	case rep.Refused:
		b.WriteString("; refused, nothing deleted")
	default:
		fmt.Fprintf(&b, "; %d deleted", rep.Deleted)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Native kind.
// ---------------------------------------------------------------------------

// HEAD declares Writes authors (the author_books_legacy: family, which the
// store-key vocabulary files under authors) and Reads books and authors.
func t6g2AuthorBooksLegacyRetireMeta() t6g2Meta {
	return t6g2Meta{
		Kind:    t6g2KindAuthorBooksLegacyRetire,
		Title:   t6g2AuthorBooksLegacyRetireTitle,
		Help:    t6g2AuthorBooksLegacyRetireHelp,
		Effects: tasks.Writes(t6g2Res(ops.ResAuthors)...).AlsoReads(t6g2Res(ops.ResBooks, ops.ResAuthors)...),
		// ResumeRestart at HEAD, and nothing here checkpoints: the delete is by
		// prefix, so a restart re-checks and deletes what is left.
		Resume:   tasks.ResumeFromZero,
		Priority: tasks.PriorityLow,
		Timeout:  60 * time.Minute,
		Key:      authorBooksLegacyRetireID,
	}
}

func t6g2AuthorBooksLegacyRetireKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	m := t6g2AuthorBooksLegacyRetireMeta()
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		if err := t6g2RefuseModeKeys(m.Kind, raw); err != nil {
			return t6g2NoResult{}, err
		}
		env, err := t6g2NativeEnv(rc, deps, tasks.Parallelism{})
		if err != nil {
			return t6g2NoResult{}, err
		}
		return runAuthorBooksLegacyRetireCore(rc.Context(), !t6g2Live(rc), env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}
