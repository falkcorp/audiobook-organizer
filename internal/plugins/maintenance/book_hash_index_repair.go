// file: internal/plugins/maintenance/book_hash_index_repair.go
// version: 1.0.0
// guid: 6f2a8c15-4d93-4e70-b1a6-3c9e0d7b5a28
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.book-hash-index-repair checks, and with dry_run=false repairs,
// the three single-valued file-hash indexes (book:hash:, book:originalhash:,
// book:organizedhash:) against the live books' own hash columns.
//
// DeleteBook used to delete those keys unconditionally, so removing one book
// erased the lookup entry of the live book that owned the same hash, and the
// purge of about 2,520 combine shells on 2026-10-21 may have removed live
// entries that way. A missing entry makes GetBookByFileHash and its two twins
// answer "no such book", which the scanner and the importers read as "this
// file is not in the library yet". The rules (what counts as broken, how a
// key is repaired, what is never touched) are in
// internal/database/pebble_store_book_hash_index.go.
//
// Preview by default (owner rule 2026-09-25): the report only. dry_run=false
// points each broken key at a live holder, one compare-and-set per key; it
// never deletes a key or writes a book row, so it is safe to run beside the
// library. Idempotent, so ResumeRestart: a resumed run recomputes and finds
// only what the first did not reach.
//
// It reads every book row once (the decoding is spread across the CPUs) and
// then checks the keys with a bounded worker pool; the store's methods are
// resolved through database.AsCapability because OpsStore() can be a
// decorator that hides *PebbleStore's methods.
const bookHashIndexRepairID = "maintenance.book-hash-index-repair"

type bookHashIndexChecker interface {
	CheckBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (database.BookHashIndexReport, error)
	RepairBookHashIndex(ctx context.Context, progress database.BookHashIndexProgress) (database.BookHashIndexReport, error)
}

func (p *Plugin) bookHashIndexRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          bookHashIndexRepairID,
		Preview:     sdk.PreviewSpec{Params: opmode.DryRunParams{}},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Book file-hash index check and repair",
		Description: "Checks that every file hash a live book holds resolves, through the book:hash:, " +
			"book:originalhash: and book:organizedhash: indexes, to a live book that holds it. Reports " +
			"entries that are missing, dangling (the book is gone), pointing at a book that does not hold " +
			"the hash, or pointing at a trashed book while a live one holds it too. Preview by default: " +
			"counts and a sample, writes nothing. With dry_run=false it points each broken key at the live " +
			"holder with the smallest id; it never deletes a key or writes a book row.",
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
	var params opmode.DryRunParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", bookHashIndexRepairID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(bookHashIndexRepairID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	c, ok := database.AsCapability[bookHashIndexChecker](store)
	if !ok {
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
		return fmt.Errorf("%s: %w", bookHashIndexRepairID, err)
	}

	for _, k := range rep.Kinds {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("%s: checked=%d ok=%d missing=%d dangling=%d wrong_holder=%d trashed_owner=%d unreadable=%d repointed=%d raced=%d",
			k.Kind, k.Checked, k.OK, k.Missing, k.Dangling, k.WrongHolder, k.TrashedOwner, k.Unreadable, k.Repointed, k.Raced))
	}
	for _, s := range rep.Samples {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("sample %s %s: %s (key names %q, live holder %s)", s.Kind, s.Hash, s.Class, s.Names, s.LiveHolder))
	}
	msg := fmt.Sprintf("book hash index: %d books (%d live); %d broken entries", rep.Books, rep.LiveBooks, rep.Broken())
	if dryRun {
		msg += "; preview, nothing written (run with dry_run=false to repair)"
	} else {
		msg += fmt.Sprintf("; %d repointed", rep.Repointed())
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

func ofTotal(total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf(" of %d", total)
}
