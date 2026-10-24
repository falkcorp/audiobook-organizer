// file: internal/plugins/maintenance/book_hash_index_repair.go
// version: 2.1.1
// guid: 6f2a8c15-4d93-4e70-b1a6-3c9e0d7b5a28
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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
// legacy sweep) are in internal/database/pebble_store_book_hash_index.go.
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
		ID:          bookHashIndexRepairID,
		Preview:     sdk.PreviewSpec{Params: bookHashIndexParams{}, StartCheck: bookHashIndexStartCheck},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Book file-hash index check and repair",
		Description: "Checks that every file hash a book holds has its key in the book_hash:, " +
			"book_originalhash: and book_organizedhash: indexes (one key per hash and book). Preview " +
			"by default: counts the missing keys and shows a sample, writes nothing. With dry_run=false " +
			"it writes each missing key and, once every book row decoded, marks the index complete and " +
			"deletes the retired single-valued book:hash: keys, then deletes each extra key (a key whose " +
			"book is gone or no longer holds that hash, re-read under the book's write stripe). Boot " +
			"runs it with ensure=true. It never writes or deletes a book row or a book_file row.",
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
	c, ok := database.AsCapability[bookHashIndexChecker](store)
	if !ok {
		if params.Ensure {
			// Boot enqueues the ensure on every start. A store with no index
			// (not Pebble) has nothing to ensure, so a red row on every boot
			// would be noise. An operator's run still fails below.
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("store is %T, which has no book_hash: indexes "+
				"(and no store in its decorator chain does); nothing to ensure", store), opsregistry.NoOpAttr())
			return nil
		}
		return fmt.Errorf("%s: store is %T, which has no book hash indexes to check", bookHashIndexRepairID, store)
	}

	// The callback runs inside the store's worker goroutines; UpdateProgress
	// is safe for concurrent use and the callback keeps no state.
	progress := func(phase string, done, total int) {
		_ = reporter.UpdateProgress(done, total, fmt.Sprintf("book hash index: %s: %d%s", phase, done, ofTotal(total)))
	}

	verb := "repairing"
	run := c.RepairBookHashIndex
	if dryRun {
		verb = "checking (preview, writes nothing)"
		run = c.CheckBookHashIndex
	}
	_ = reporter.UpdateProgress(0, 0, "book hash index: "+verb)
	rep, err := run(ctx, progress)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_ = reporter.Log(slog.LevelInfo, "book hash index run interrupted; the sentinel is unchanged and "+
				"the next boot's ensure resumes", slog.String("error", err.Error()))
		}
		return fmt.Errorf("%s: %w", bookHashIndexRepairID, err)
	}

	for _, k := range rep.Kinds {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("%s: checked=%d ok=%d missing=%d written=%d extra=%d removed=%d",
			k.Kind, k.Checked, k.OK, k.Missing, k.Written, k.Extra, k.Removed))
	}
	for _, s := range rep.Samples {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("sample %s %s: no key for book %s", s.Kind, s.Hash, s.BookID))
	}
	for _, s := range rep.ExtraSamples {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("sample %s %s: extra key for book %s (gone, or its row no longer holds the hash)", s.Kind, s.Hash, s.BookID))
	}
	msg := fmt.Sprintf("book hash index: %d books (%d live); %d missing keys, %d extra keys", rep.Books, rep.LiveBooks, rep.Missing(), rep.Extra())
	switch {
	case dryRun:
		msg += "; preview, nothing written (run with dry_run=false to repair)"
	default:
		msg += fmt.Sprintf("; %d written, %d extra removed; index complete=%t; legacy keys deleted=%t", rep.Written(), rep.Removed(), rep.Trusted, rep.LegacySwept)
	}
	if rep.Unreadable > 0 {
		msg += fmt.Sprintf("; %d book row(s) cannot be decoded", rep.Unreadable)
		_ = reporter.Log(slog.LevelError, msg)
		_ = reporter.UpdateProgress(1, 1, msg)
		return fmt.Errorf("%s: %d book row(s) cannot be decoded (sample %v), so their hashes are not indexed and the "+
			"index is not marked complete; lookups also consult the legacy keys until each row is rewritten or removed "+
			"and this op runs again", bookHashIndexRepairID, rep.Unreadable, rep.SampleUnreadable)
	}
	if params.Ensure && rep.Written() == 0 && rep.Removed() == 0 && !rep.LegacySwept {
		// The usual boot outcome once built: nothing to do, hidden from the
		// default activity feed.
		_ = reporter.Log(slog.LevelInfo, msg, opsregistry.NoOpAttr())
	} else {
		_ = reporter.Log(slog.LevelInfo, msg)
	}
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

func ofTotal(total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf(" of %d", total)
}
