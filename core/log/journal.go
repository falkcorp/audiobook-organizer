// file: core/log/journal.go
// version: 1.0.0
// guid: 3f0b6d9e-52c1-4a8e-9b47-6c1e0d2a7f58
// last-edited: 2026-11-13

package log

import (
	"context"
	"log/slog"
	"time"
)

// JournalStore persists the system log journal (R4-1). It is SDK, frozen by the
// API-stability gate, and names only log types, so log (L0) imports nothing for
// it. It is the stated exception to "every write method takes the caller's
// *storage.Batch" (05-storage "Store interfaces"; COR5-12, MOD5-10): it is
// append-only, each call commits its own batch, and it never joins a
// multi-owner commit. Its one implementation is core's native store
// (core/internal/logjournal), which keeps records in c/log/journal/<ULID>; no
// app supplies a store (D10). logtest.JournalStoreSuite is its contract
// (05-logging §10). Records reaching a store are already redacted.
type JournalStore interface {
	Append(ctx context.Context, recs []Record) error                // one durable batch per call
	Query(ctx context.Context, q JournalQuery) (JournalPage, error) // newest first
	// Prune deletes up to limit records older than before, oldest first, in one
	// bounded batch; more reports whether older records remain (the prune task
	// calls it until more is false or its budget ends).
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, more bool, err error)
}

// Importer is the native journal's one-time import path for an app's pre-core
// system-log rows (05-logging §5; 03 §3.3 "Store interfaces"). The kernel
// builds it over the native store and hands it to the owning app's cutover
// step in module.Importers (06 §6.4.2). Unlike the other importers it does not
// stage into the caller's batch (the one exception 03 §3.3's importer rule
// allows): log is L0 and cannot name a storage type, the same reason
// JournalStore commits its own batches. Instead Import is idempotent: each
// record's key is derived from its Time and SourceID, so a step that resumes
// after a crash between an import and its source deletes rewrites the same keys
// and imports nothing twice.
type Importer interface {
	Import(ctx context.Context, recs []ImportedRecord) error // one durable batch per call
}

// ImportedRecord is one pre-core row, as the app's cutover step decoded it.
type ImportedRecord struct {
	Record
	SourceID string // the app's stable ID of the source row (ABO: its syslog: key)
}

// JournalQuery selects records of a JournalStore, newest first.
type JournalQuery struct {
	MinLevel     slog.Level
	Module       string    // "" = all
	Since, Until time.Time // zero = unbounded
	Text         string    // case-insensitive substring of the message
	Cursor       string    // opaque, from JournalPage.Next
	Limit        int       // capped by the query route
}

// JournalPage is one page of a query's result.
type JournalPage struct {
	Records []Record
	Next    string // "" when exhausted
}
