// file: internal/plugins/maintenance/booksig_recovery_audit.go
// version: 1.0.0
// guid: 5f2a7c14-9b3e-4d6a-8e1f-2c0d5a9b7e34
// last-edited: 2026-07-03

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
// This op is a read-only, dry-run audit that sizes existing damage: it scans
// every book's current (Pebble-direct) row for missing Description/BookSigV1 and,
// for each missing field, checks whether an older snapshot still carries it. It
// never writes, never prunes snapshots, and must run before any STOR-2 pruning.
//
// IMPORTANT: recoverability is tracked PER FIELD, not per book. A book may have
// had its Description and BookSigV1 wiped at different times, so the newest
// snapshot carrying Description can differ from the newest carrying BookSigV1.

// bookSigRecoveryAuditParams controls the audit. DryRun defaults to true; this
// task ships dry-run only. Apply mode (restoring wiped fields from snapshots) is
// intentionally out of scope — it is an owner-greenlight-only follow-up, see the
// op Description string.
type bookSigRecoveryAuditParams struct {
	DryRun bool `json:"dryRun"`
}

// auditExample is one concrete finding surfaced in the report.
type auditExample struct {
	bookID  string
	title   string
	field   string // "description" or "booksig_v1"
	snapTS  *time.Time
	details string
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
		DisplayName: "Audit wiped Description/BookSig recoverability from snapshots",
		Description: "Read-only dry-run audit (STOR-1/STOR-2). Scans every book's " +
			"Pebble-direct current row for missing Description and for BookSigV1 that " +
			"was built (BookSigBuiltAt set) but is now nil, then checks each book's " +
			"book_ver: CoW snapshots for the newest snapshot still carrying each missing " +
			"field. Reports how much wipe damage from the memdb full-replacement bug is " +
			"recoverable before STOR-2 snapshot pruning is ever enabled. This op is " +
			"read-only: it never writes and never prunes snapshots. Applying recovered " +
			"fields back to prod is a separate, owner-greenlight-only follow-up and is " +
			"intentionally NOT implemented here.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.booksig-recovery-audit",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		// Read-only path: no CapLibraryWrite. This op never writes.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runBookSigRecoveryAudit,
	}
}

// newestSnapshotWithField scans snapshots (already newest-first) and returns the
// timestamp of the newest snapshot whose Book has the named field non-nil, or nil
// if no snapshot carries it. field is "description" or "booksig_v1".
func newestSnapshotWithField(snaps []database.BookSnapshot, field string) *time.Time {
	for _, snap := range snaps {
		var b database.Book
		if err := json.Unmarshal(snap.Data, &b); err != nil {
			continue // skip corrupt snapshot; keep scanning older ones
		}
		switch field {
		case "description":
			if b.Description != nil {
				ts := snap.Timestamp
				return &ts
			}
		case "booksig_v1":
			if b.BookSigV1 != nil {
				ts := snap.Timestamp
				return &ts
			}
		}
	}
	return nil
}

func (p *Plugin) runBookSigRecoveryAudit(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := bookSigRecoveryAuditParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	// This task ships dry-run only. Apply mode is an owner-greenlight follow-up
	// and is intentionally not implemented; refuse rather than silently no-op so
	// a caller passing dryRun=false gets a clear signal.
	if !params.DryRun {
		return fmt.Errorf("apply mode is not implemented: this op is read-only " +
			"(STOR-2 sequencing). Restoring wiped fields from snapshots is an " +
			"owner-greenlight-only follow-up; run with dryRun=true")
	}
	_ = reporter.Log(slog.LevelInfo, "DRY RUN — read-only audit, no changes will be written")

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
		scanned int
		errCount int

		descMissing        int // Description == nil on current row
		descRecoverable    int // ...and a snapshot carries Description
		descNotRecoverable int // ...and no snapshot carries it

		sigMissing        int // BookSigBuiltAt != nil && BookSigV1 == nil (built then wiped)
		sigRecoverable    int // ...and a snapshot carries BookSigV1
		sigNotRecoverable int // ...and no snapshot carries it

		booksWithSnapshotLookup int // candidates for which we scanned snapshots
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

		if descIsMissing {
			descMissing++
			if ts := newestSnapshotWithField(snaps, "description"); ts != nil {
				descRecoverable++
				addExample(auditExample{bookID: id, title: title, field: "description", snapTS: ts})
			} else {
				descNotRecoverable++
				addExample(auditExample{bookID: id, title: title, field: "description"})
			}
		}
		if sigIsMissing {
			sigMissing++
			if ts := newestSnapshotWithField(snaps, "booksig_v1"); ts != nil {
				sigRecoverable++
				addExample(auditExample{bookID: id, title: title, field: "booksig_v1", snapTS: ts})
			} else {
				sigNotRecoverable++
				addExample(auditExample{bookID: id, title: title, field: "booksig_v1"})
			}
		}

		heartbeat(false)
	}

	heartbeat(true)

	report := fmt.Sprintf(
		"DRY RUN — audited %d books (%d candidates snapshot-scanned, %d read errors). "+
			"Description missing: %d (recoverable %d, not-recoverable %d). "+
			"BookSigV1 built-then-wiped: %d (recoverable %d, not-recoverable %d).",
		scanned, booksWithSnapshotLookup, errCount,
		descMissing, descRecoverable, descNotRecoverable,
		sigMissing, sigRecoverable, sigNotRecoverable)
	_ = reporter.Log(slog.LevelInfo, report)
	_ = reporter.UpdateProgress(total, total, report)
	return nil
}
