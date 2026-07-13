// file: internal/plugins/maintenance/booksig_recovery_audit.go
// version: 1.1.1
// guid: 5f2a7c14-9b3e-4d6a-8e1f-2c0d5a9b7e34
// last-edited: 2026-07-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// STOR-1/STOR-2: UpdateBook's memdb-stripped-Book full-replacement bug silently
// wiped Description and BookSigV1 dedup signatures on every Book touched by a
// GetAllBooks-then-UpdateBook read-modify-write path (reconcile, migrations,
// quarantine, merge). The advisor pass confirmed the damage is recoverable from
// UpdateBook's own book_ver: CoW snapshots — before overwriting, UpdateBook
// snapshots the full un-stripped old row to book_ver:<id>:<unixnano>. That
// snapshot therefore still carries the pre-wipe Description/BookSigV1, but ONLY
// until STOR-2's snapshot pruning is ever enabled.
//
// This op audits (always) and, only when explicitly requested via
// dryRun=false, restores existing damage: it scans every book's current
// (Pebble-direct) row for missing Description/BookSigV1 and, for each missing
// field, checks whether an older snapshot still carries it. In apply mode it
// writes back ONLY the missing field(s) recovered from the newest snapshot
// that has them — it never prunes snapshots, and never overwrites a field
// that already has a non-empty current value.
//
// IMPORTANT: recoverability is tracked PER FIELD, not per book. A book may have
// had its Description and BookSigV1 wiped at different times, so the newest
// snapshot carrying Description can differ from the newest carrying BookSigV1.
//
// Apply mode (dryRun=false) was greenlit by the owner on 2026-07-03 after a
// prod dry-run audited 44,929 books and found 397 books with a recoverable
// Description (0 BookSigV1 wipes). The BookSigV1 restore path shares the same
// code but is exercised by tests only until real wipe damage appears.

// bookSigRecoveryAuditParams controls the audit/restore. DryRun defaults to
// true (safe default); callers must explicitly pass dryRun=false to write.
type bookSigRecoveryAuditParams struct {
	DryRun bool `json:"dryRun"`
}

// auditExample is one concrete finding surfaced in the report.
type auditExample struct {
	bookID string
	title  string
	field  string // "description" or "booksig_v1"
	snapTS *time.Time
}

func (e auditExample) String() string {
	if e.snapTS != nil {
		return fmt.Sprintf("%s (%q) %s recoverable@%s", e.bookID, e.title, e.field, e.snapTS.Format(time.RFC3339))
	}
	return fmt.Sprintf("%s (%q) %s NOT recoverable", e.bookID, e.title, e.field)
}

func (p *Plugin) bookSigRecoveryAuditDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.booksig-recovery-audit",
		Plugin:      "maintenance",
		DisplayName: "Audit/restore wiped Description/BookSig from snapshots",
		Description: "Dry-run audit (default) or owner-greenlit apply mode (STOR-1/STOR-2). " +
			"Scans every book's Pebble-direct current row for missing Description and for " +
			"BookSigV1 that was built (BookSigBuiltAt set) but is now nil, then checks each " +
			"book's book_ver: CoW snapshots for the newest snapshot still carrying each " +
			"missing field. Dry-run (default) only reports how much wipe damage from the " +
			"memdb full-replacement bug is recoverable; it never writes and never prunes " +
			"snapshots. Apply mode (dryRun=false, explicit only) writes back ONLY the " +
			"recovered field(s) onto a freshly re-read current row, skipping any field " +
			"that is no longer empty at write time. It never prunes snapshots.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.booksig-recovery-audit",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		// Apply mode (dryRun=false) writes recovered fields back, so this op
		// needs both read and write capabilities; the dry-run path itself
		// never writes.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runBookSigRecoveryAudit,
	}
}

// newestSnapshotBookWithField scans snapshots (already newest-first) and
// returns the parsed Book of the newest snapshot whose Book has the named
// field non-nil, along with its timestamp, or (nil, nil) if no snapshot
// carries it. field is "description" or "booksig_v1". Callers needing only
// the timestamp (dry-run reporting) can ignore the returned Book; apply mode
// uses it to source the actual recovered value(s).
func newestSnapshotBookWithField(snaps []database.BookSnapshot, field string) (*database.Book, *time.Time) {
	for _, snap := range snaps {
		var b database.Book
		if err := json.Unmarshal(snap.Data, &b); err != nil {
			continue // skip corrupt snapshot; keep scanning older ones
		}
		switch field {
		case "description":
			if b.Description != nil {
				ts := snap.Timestamp
				return &b, &ts
			}
		case "booksig_v1":
			if b.BookSigV1 != nil {
				ts := snap.Timestamp
				return &b, &ts
			}
		}
	}
	return nil, nil
}

func (p *Plugin) runBookSigRecoveryAudit(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := bookSigRecoveryAuditParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — read-only audit, no changes will be written")
	} else {
		_ = reporter.Log(slog.LevelInfo, "APPLY MODE — restoring recoverable fields from book_ver: snapshots (owner-greenlit)")
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	// Enumerate book IDs only (memdb-safe, ~a few MB of strings for the whole
	// library). We deliberately do NOT materialize full Book structs for the
	// whole library — each row is fetched one at a time below via GetBookByID
	// (Pebble-direct), which reflects true on-disk state rather than the
	// memdb-stripped projection GetAllBooks would return.
	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("ListBookIDs: %w", err)
	}
	total := len(ids)
	_ = reporter.UpdateProgress(0, total, "Auditing Description/BookSig recoverability…")

	const (
		logInterval  = 15 * time.Second
		exampleCount = 5
	)

	var (
		scanned  int
		errCount int

		descMissing        int // Description == nil on current row
		descRecoverable    int // ...and a snapshot carries Description
		descNotRecoverable int // ...and no snapshot carries it

		sigMissing        int // BookSigBuiltAt != nil && BookSigV1 == nil (built then wiped)
		sigRecoverable    int // ...and a snapshot carries BookSigV1
		sigNotRecoverable int // ...and no snapshot carries it

		booksWithSnapshotLookup int // candidates for which we scanned snapshots

		// Apply-mode-only counters (stay zero in dry-run).
		restoredCount     int // fields actually written back
		skippedNonEmpty   int // recoverable, but current value was non-empty at write time — skipped
		restoreErrorCount int // GetBookByID re-read or UpdateBook failures during restore
	)

	// Small ring of recoverable + not-recoverable examples for heartbeat/report.
	examples := make([]auditExample, 0, exampleCount)
	addExample := func(e auditExample) {
		if len(examples) < cap(examples) {
			examples = append(examples, e)
		}
	}

	lastLog := time.Now()
	heartbeat := func(force bool) {
		if !force && time.Since(lastLog) < logInterval {
			return
		}
		sample := make([]string, 0, len(examples))
		for _, e := range examples {
			sample = append(sample, e.String())
		}
		msg := fmt.Sprintf("Audited %d/%d — desc missing %d (rec %d), sig missing %d (rec %d)",
			scanned, total, descMissing, descRecoverable, sigMissing, sigRecoverable)
		if !params.DryRun {
			msg += fmt.Sprintf(" — restored %d, skipped-nonempty %d, restore errors %d",
				restoredCount, skippedNonEmpty, restoreErrorCount)
		}
		if len(sample) > 0 {
			msg += " — e.g. " + strings.Join(sample, "; ")
		}
		_ = reporter.Log(slog.LevelInfo, msg)
		_ = reporter.UpdateProgress(scanned, total, fmt.Sprintf("Audited %d/%d", scanned, total))
		lastLog = time.Now()
	}

	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		scanned++

		book, gerr := store.GetBookByID(id)
		if gerr != nil || book == nil {
			if gerr != nil {
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: GetBookByID failed: %v", id, gerr))
			}
			errCount++
			heartbeat(false)
			continue
		}

		descIsMissing := book.Description == nil
		// "was built but now nil" — the damage signal. A book that never had a
		// signature (BookSigBuiltAt nil) is NOT a candidate: it isn't missing
		// anything, it just never had one.
		sigIsMissing := book.BookSigBuiltAt != nil && book.BookSigV1 == nil

		if !descIsMissing && !sigIsMissing {
			heartbeat(false)
			continue
		}

		// Only now (a real candidate) do we prefix-scan snapshots — this keeps us
		// from doing ~50K needless book_ver: scans across the whole library.
		booksWithSnapshotLookup++
		snaps, serr := store.GetBookSnapshots(id, 0) // 0 => all snapshots, newest-first
		if serr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: GetBookSnapshots failed: %v", id, serr))
			errCount++
			// Without snapshots we cannot judge recoverability; count the missing
			// field(s) but classify as not-recoverable for this run.
			snaps = nil
		}

		title := book.Title

		var descSnapBook *database.Book
		var sigSnapBook *database.Book

		if descIsMissing {
			descMissing++
			snapBook, ts := newestSnapshotBookWithField(snaps, "description")
			if ts != nil {
				descRecoverable++
				descSnapBook = snapBook
				addExample(auditExample{bookID: id, title: title, field: "description", snapTS: ts})
			} else {
				descNotRecoverable++
				addExample(auditExample{bookID: id, title: title, field: "description"})
			}
		}
		if sigIsMissing {
			sigMissing++
			snapBook, ts := newestSnapshotBookWithField(snaps, "booksig_v1")
			if ts != nil {
				sigRecoverable++
				sigSnapBook = snapBook
				addExample(auditExample{bookID: id, title: title, field: "booksig_v1", snapTS: ts})
			} else {
				sigNotRecoverable++
				addExample(auditExample{bookID: id, title: title, field: "booksig_v1"})
			}
		}

		// Apply mode: restore ONLY fields we just proved recoverable, writing
		// back onto a freshly re-read current row (never the `book` struct
		// captured above, which may be stale by the time we get here — the
		// snapshot lookup above can take a moment on a large snapshot chain).
		// This is the memdb-round-trip-footgun guard: we never construct a
		// Book from scratch or from a memdb projection, only mutate a row we
		// just read Pebble-direct.
		if !params.DryRun && (descSnapBook != nil || sigSnapBook != nil) {
			restored, skipped, rerr := restoreRecoverableFields(store, id, descSnapBook, sigSnapBook, reporter)
			restoredCount += restored
			skippedNonEmpty += skipped
			if rerr != nil {
				restoreErrorCount++
				errCount++
			}
		}

		heartbeat(false)
	}

	heartbeat(true)

	mode := "DRY RUN"
	if !params.DryRun {
		mode = "APPLY MODE"
	}
	report := fmt.Sprintf(
		"%s — audited %d books (%d candidates snapshot-scanned, %d read errors). "+
			"Description missing: %d (recoverable %d, not-recoverable %d). "+
			"BookSigV1 built-then-wiped: %d (recoverable %d, not-recoverable %d).",
		mode, scanned, booksWithSnapshotLookup, errCount,
		descMissing, descRecoverable, descNotRecoverable,
		sigMissing, sigRecoverable, sigNotRecoverable)
	if !params.DryRun {
		report += fmt.Sprintf(" Restored %d, skipped-nonempty %d, restore errors %d.",
			restoredCount, skippedNonEmpty, restoreErrorCount)
	}
	_ = reporter.Log(slog.LevelInfo, report)
	_ = reporter.UpdateProgress(total, total, report)
	return nil
}

// restoreRecoverableFields re-reads book id fresh from the authoritative
// store (Pebble-direct GetBookByID — never the memdb-stripped projection,
// and never a struct captured earlier in the caller's scan loop) and writes
// back ONLY the field(s) the caller already proved recoverable, skipping any
// field that is no longer empty by the time we get here (a concurrent writer
// may have filled it in between the scan and this restore). It never
// constructs a Book from scratch and never full-replaces from a stale or
// partial struct, which is the memdb round-trip footgun (STOR-1) this whole
// op exists to remediate — the row we write is the same row we just read,
// mutated in place for the recovered field(s) only.
//
// descSnapBook/sigSnapBook are the parsed snapshot Books already known (by
// the caller) to carry a non-nil Description/BookSigV1 respectively; either
// may be nil if that field isn't recoverable for this book.
func restoreRecoverableFields(
	store database.Store,
	id string,
	descSnapBook *database.Book,
	sigSnapBook *database.Book,
	reporter sdk.Reporter,
) (restored int, skippedNonEmpty int, err error) {
	fresh, gerr := store.GetBookByID(id)
	if gerr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: restore re-read failed: %v", id, gerr))
		return 0, 0, gerr
	}
	if fresh == nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: restore re-read found no row, skipping", id))
		return 0, 0, fmt.Errorf("book %s: not found on restore re-read", id)
	}

	changed := false

	if descSnapBook != nil {
		if fresh.Description != nil {
			skippedNonEmpty++
			_ = reporter.Log(slog.LevelDebug, fmt.Sprintf("book %s: description no longer empty at write time, skipping", id))
		} else {
			fresh.Description = descSnapBook.Description
			changed = true
		}
	}

	if sigSnapBook != nil {
		if fresh.BookSigV1 != nil {
			skippedNonEmpty++
			_ = reporter.Log(slog.LevelDebug, fmt.Sprintf("book %s: booksig_v1 no longer empty at write time, skipping", id))
		} else {
			fresh.BookSigV1 = sigSnapBook.BookSigV1
			fresh.BookSigV1Mask = sigSnapBook.BookSigV1Mask
			fresh.BookSigSegments = sigSnapBook.BookSigSegments
			fresh.BookSigBuiltAt = sigSnapBook.BookSigBuiltAt
			fresh.BookSigCoveragePct = sigSnapBook.BookSigCoveragePct
			changed = true
		}
	}

	if !changed {
		return 0, skippedNonEmpty, nil
	}

	if _, uerr := store.UpdateBook(id, fresh); uerr != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("book %s: UpdateBook restore failed: %v", id, uerr))
		return 0, skippedNonEmpty, uerr
	}

	_ = reporter.Log(slog.LevelDebug, fmt.Sprintf("book %s (%q): restored recovered field(s)", id, fresh.Title))
	return 1, skippedNonEmpty, nil
}
