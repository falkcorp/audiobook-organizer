// file: internal/plugins/maintenance/book_hash_index_repair.go
// version: 1.1.1
// guid: 6f2a8c15-4d93-4e70-b1a6-3c9e0d7b5a28
// last-edited: 2026-11-13

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
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// maintenance.book-hash-index-repair checks, and with dry_run=false repairs,
// the three multi-valued file-hash indexes (book_hash:, book_originalhash:,
// book_organizedhash:, one key per hash and book) against the book rows' own
// hash columns.
//
// The indexes used to be single-valued (book:hash:<h> held ONE book id), so two
// books holding one file shared a key and DeleteBook, run over about 2,520
// combine shells on 2026-10-21, erased live books' entries. A missing entry
// makes GetBookByFileHash and its two twins answer "no such book", which the
// scanner and the importers read as "this file is not in the library yet". The
// per-book keys cannot be erased by another book's write; this op writes the
// keys the existing rows lack, which is also the one-time backfill of the new
// layout. The rules (what a lookup returns, the sentinel, the order of the
// legacy sweep) are in internal/books/store/pebble_store_book_hash_index.go.
//
// Modes:
//   - preview (default, owner rule 2026-09-25): counts the missing keys and the
//     extra ones (a key whose book is gone or no longer holds the hash) and
//     samples them; writes nothing.
//   - dry_run=false: writes every missing key; when every book row decoded it
//     then sets the index's sentinel and deletes the legacy single-valued keys;
//     last it deletes the extra keys, each after a fresh read of its row under
//     the book's write stripe.
//   - ensure=true (boot enqueues this on every start): the same as dry_run=false,
//     for the server itself. Every boot, because a rollback to a build that
//     predates the index writes books with no keys and the sentinel cannot
//     know. Until it finishes, lookups also consult the legacy keys. A run that
//     finds a book row it cannot decode fails, and the sentinel stays unset.
//
// It deletes a new-family key only when its book is gone or no longer holds the
// hash (re-read under the book's write stripe), and never writes a book row, so
// it is safe to run beside the library. Idempotent, so ResumeRestart. It reads every book
// row once with the decoding and the key reads and writes spread across the
// CPUs; the store's methods are resolved through database.AsCapability because
// OpsStore() can be a decorator that hides *PebbleStore's methods.
const bookHashIndexRepairID = "maintenance.book-hash-index-repair"

// t6g2KindBookHashIndexRepair is the native kind (F5-07, rule R3).
const t6g2KindBookHashIndexRepair tasks.Kind = "books.maintenance.book-hash-index-repair"

// DisplayName and Description of the def, shared by HEAD's def and the native
// kind so the two cannot drift.
const (
	t6g2BookHashIndexRepairTitle = "Book file-hash index check and repair"
	t6g2BookHashIndexRepairHelp  = "Checks that every file hash a book holds has its key in the book_hash:, " +
		"book_originalhash: and book_organizedhash: indexes (one key per hash and book). Preview " +
		"by default: counts the missing keys and shows a sample, writes nothing. With dry_run=false " +
		"it writes each missing key and, once every book row decoded, marks the index complete and " +
		"deletes the retired single-valued book:hash: keys, then deletes each extra key (a key whose " +
		"book is gone or no longer holds that hash, re-read under the book's write stripe). Boot " +
		"runs it with ensure=true. It never writes or deletes a book row or a book_file row."
)

type bookHashIndexChecker interface {
	CheckBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (database.BookHashIndexReport, error)
	RepairBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (database.BookHashIndexReport, error)
}

// bookHashIndexParams: dry_run/dryRun as every preview-default op, plus ensure
// (the boot mode).
type bookHashIndexParams struct {
	opmode.DryRunParams
	Ensure bool `json:"ensure,omitempty"`
}

// bookHashIndexStartCheck refuses ensure=true at start: a start through the API
// always carries dry_run and Run refuses ensure beside dry_run, so the run could
// only fail. ensure is the boot path, enqueued by the server itself.
func bookHashIndexStartCheck(params map[string]json.RawMessage, _ bool) error {
	raw, ok := params["ensure"]
	if !ok {
		return nil
	}
	var ensure bool
	if err := json.Unmarshal(raw, &ensure); err != nil {
		return fmt.Errorf("ensure must be true or false")
	}
	if ensure {
		return fmt.Errorf("%s: ensure=true is the boot mode and cannot be combined with dry_run, which every start carries; "+
			"send dry_run alone (preview) or dry_run=false (repair)", bookHashIndexRepairID)
	}
	return nil
}

func (p *Plugin) bookHashIndexRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              bookHashIndexRepairID,
		Preview:         sdk.PreviewSpec{Params: bookHashIndexParams{}, StartCheck: bookHashIndexStartCheck},
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     t6g2BookHashIndexRepairTitle,
		Description:     t6g2BookHashIndexRepairHelp,
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  bookHashIndexRepairID,
		Writes:          []sdk.Resource{sdk.ResBooks},
		Reads:           []sdk.Resource{sdk.ResBooks},
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runBookHashIndexRepair,
	}
}

func (p *Plugin) runBookHashIndexRepair(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params bookHashIndexParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", bookHashIndexRepairID, err)
		}
	}
	if params.Ensure && (params.DryRun != nil || params.DryRunCamel != nil) {
		return fmt.Errorf("%s: ensure=true cannot be combined with dry_run; send one of {\"ensure\": true} "+
			"(repair, as boot does) or {\"dry_run\": false} (repair)", bookHashIndexRepairID)
	}
	dryRun, err := opmode.ResolveDryRun(bookHashIndexRepairID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	if params.Ensure {
		dryRun = false
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_, err = runBookHashIndexRepairCore(ctx, t6g2HashRepairParams{Ensure: params.Ensure, DryRun: dryRun},
		headT6G2Wire(headT6G2Env(p, reporter), p, store))
	return err
}

// t6g2HashRepairParams is the core's params: the mode is already resolved (HEAD
// from dry_run, native from the run's mode), so the core knows neither spelling.
type t6g2HashRepairParams struct {
	// Ensure is the boot mode: the repair for the server itself, every start.
	Ensure bool
	// DryRun is the resolved mode: true counts and samples and writes nothing.
	DryRun bool
}

// runBookHashIndexRepairCore is the body shared by HEAD's shell and the native
// kind. It probes and checks through env.Store and repairs through env.Writer.
func runBookHashIndexRepairCore(ctx context.Context, params t6g2HashRepairParams, env t6g2Env) (t6g2NoResult, error) {
	dryRun := params.DryRun
	if !env.Store.Supports(t6g2CapHashIndex) {
		if params.Ensure {
			// Boot enqueues the ensure on every start. A store with no index
			// (not Pebble) has nothing to ensure, so a red row on every boot
			// would be noise. An operator's run still fails below.
			env.Log(slog.LevelWarn, fmt.Sprintf("store is %s, which has no book_hash: indexes "+
				"(and no store in its decorator chain does); nothing to ensure", env.Store.Describe()), env.NoOp)
			return t6g2NoResult{}, nil
		}
		return t6g2NoResult{}, fmt.Errorf("%s: store is %s, which has no book hash indexes to check", bookHashIndexRepairID, env.Store.Describe())
	}

	// The callback runs inside the store's worker goroutines; Progress is safe
	// for concurrent use and the callback keeps no state.
	progress := func(phase string, done, total int) {
		env.Progress(done, total, fmt.Sprintf("book hash index: %s: %d%s", phase, done, ofTotal(total)))
	}

	verb := "repairing"
	run := env.Writer.RepairBookHashIndex
	if dryRun {
		verb = "checking (preview, writes nothing)"
		run = env.Store.CheckBookHashIndex
	}
	env.Progress(0, 0, "book hash index: "+verb)
	out, err := run(ctx, progress)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			env.Log(slog.LevelInfo, "book hash index run interrupted; the sentinel is unchanged and "+
				"the next boot's ensure resumes", slog.String("error", err.Error()))
		}
		return t6g2NoResult{}, fmt.Errorf("%s: %w", bookHashIndexRepairID, err)
	}
	rep := out.Report

	for _, k := range rep.Kinds {
		env.Log(slog.LevelInfo, fmt.Sprintf("%s: checked=%d ok=%d missing=%d written=%d extra=%d removed=%d",
			k.Kind, k.Checked, k.OK, k.Missing, k.Written, k.Extra, k.Removed))
	}
	for _, s := range rep.Samples {
		env.Log(slog.LevelInfo, fmt.Sprintf("sample %s %s: no key for book %s", s.Kind, s.Hash, s.BookID))
	}
	for _, s := range rep.ExtraSamples {
		env.Log(slog.LevelInfo, fmt.Sprintf("sample %s %s: extra key for book %s (gone, or its row no longer holds the hash)", s.Kind, s.Hash, s.BookID))
	}
	msg := fmt.Sprintf("book hash index: %d books (%d live); %d missing keys, %d extra keys", rep.Books, rep.LiveBooks, out.Missing, out.Extra)
	switch {
	case dryRun:
		msg += "; preview, nothing written (run with dry_run=false to repair)"
	default:
		msg += fmt.Sprintf("; %d written, %d extra removed; index complete=%t; legacy keys deleted=%t", out.Written, out.Removed, rep.Trusted, rep.LegacySwept)
	}
	if rep.Unreadable > 0 {
		msg += fmt.Sprintf("; %d book row(s) cannot be decoded", rep.Unreadable)
		env.Log(slog.LevelError, msg)
		env.Progress(1, 1, msg)
		return t6g2NoResult{}, fmt.Errorf("%s: %d book row(s) cannot be decoded (sample %v), so their hashes are not indexed and the "+
			"index is not marked complete; lookups also consult the legacy keys until each row is rewritten or removed "+
			"and this op runs again", bookHashIndexRepairID, rep.Unreadable, rep.SampleUnreadable)
	}
	if params.Ensure && out.Written == 0 && out.Removed == 0 && !rep.LegacySwept {
		// The usual boot outcome once built: nothing to do, hidden from the
		// default activity feed.
		env.Log(slog.LevelInfo, msg, env.NoOp)
	} else {
		env.Log(slog.LevelInfo, msg)
	}
	env.Progress(1, 1, msg)
	return t6g2NoResult{}, nil
}

func ofTotal(total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf(" of %d", total)
}

// ---------------------------------------------------------------------------
// Native kind (F5-07).
// ---------------------------------------------------------------------------

// t6g2BookHashIndexRepairParams is the native params: the run's mode replaces
// dry_run, so only ensure is left.
type t6g2BookHashIndexRepairParams struct {
	Ensure bool `json:"ensure,omitempty"`
}

// HEAD declares Writes books and Reads books, and a Preview.
func t6g2BookHashIndexRepairMeta() t6g2Meta {
	return t6g2Meta{
		Kind:    t6g2KindBookHashIndexRepair,
		Title:   t6g2BookHashIndexRepairTitle,
		Help:    t6g2BookHashIndexRepairHelp,
		Effects: tasks.Writes(t6g2Res("books")...).AlsoReads(t6g2Res("books")...),
		// ResumeRestart at HEAD, but nothing here ever checkpoints: a restart
		// verifies by reading again, so the kind says it runs from zero.
		Resume:   tasks.ResumeFromZero,
		Priority: tasks.PriorityLow,
		Timeout:  30 * time.Minute,
		Key:      bookHashIndexRepairID,
	}
}

func t6g2BookHashIndexRepairKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	m := t6g2BookHashIndexRepairMeta()
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		if err := t6g2RefuseModeKeys(m.Kind, raw); err != nil {
			return t6g2NoResult{}, err
		}
		var params t6g2BookHashIndexRepairParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return t6g2NoResult{}, fmt.Errorf("%s: decode params: %w", bookHashIndexRepairID, err)
			}
		}
		live := t6g2Live(rc)
		if params.Ensure && !live {
			// ensure is the boot mode and writes; a preview writes nothing.
			return t6g2NoResult{}, fmt.Errorf("%s: ensure=true is the boot mode and repairs; start it with mode live", bookHashIndexRepairID)
		}
		env, err := t6g2NativeEnv(rc, deps, tasks.Parallelism{})
		if err != nil {
			return t6g2NoResult{}, err
		}
		return runBookHashIndexRepairCore(rc.Context(), t6g2HashRepairParams{Ensure: params.Ensure, DryRun: !live}, env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}
