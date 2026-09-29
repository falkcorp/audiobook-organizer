// file: internal/repairs/writer.go
// version: 1.2.0
// guid: c71e0d93-4b28-4a5f-8e6c-2f9a1d7b3e48
// last-edited: 2026-09-28

package repairs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ChangeTypeApplyIncomplete matches metafetch.ChangeTypeApplyIncomplete:
// UndoLastApply refuses a batch carrying it. Spelled out rather than imported
// so the framework does not depend on the metafetch service.
const ChangeTypeApplyIncomplete = "apply_incomplete"

// ChangeTypeApplyOpJournaled matches metafetch.ChangeTypeApplyOpJournaled: a
// Writer wired to an op journal (WithJournal) writes it into every history
// batch, and UndoLastApply refuses such a batch. The book fields a Repairs
// apply records are one part of a step whose other parts (book_file rows moved
// or repointed, books retired) only the operation revert puts back.
const ChangeTypeApplyOpJournaled = "apply_op_journaled"

// BookModifier is the one book write primitive a fixer gets.
type BookModifier interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// HistoryRecorder records metadata-history rows.
type HistoryRecorder interface {
	RecordMetadataChange(record *database.MetadataChangeRecord) error
}

// Writer is the only write path the framework hands a fixer. It has no
// delete method on purpose: no book, and no book_file row, can be deleted
// through the Repairs lane (standing owner rule: never delete book_file rows
// as a repair). A test pins the method set.
//
// Every Modify records one metadata-history row per changed field, AFTER the
// write it describes, all under one batch id per call, so "undo last apply"
// on a book reverts that call's change to it. If a row cannot be recorded, an
// apply_incomplete marker is written so undo refuses the batch rather than
// half-reverting it. A Writer wired to an op journal marks every batch
// apply_op_journaled instead: that apply is undone from its operation, never
// field by field. Safe for concurrent use.
type Writer struct {
	store   BookModifier
	history HistoryRecorder
	// Source / ChangeType go on every history row; BatchPrefix starts every
	// batch id.
	source, changeType, batchPrefix string
	log                             logger.LevelLogger

	// Operation journal and book_file surface (writer_files.go); nil until
	// WithJournal.
	files   BookFileWriter
	journal ChangeJournal
	opID    string
	index   *journalIndex
	touch   func()

	writes        atomic.Int64
	historyRows   atomic.Int64
	historyFailed atomic.Int64
	journaled     atomic.Int64
}

// NewWriter builds a Writer. Its warnings go to the "repairs" subsystem
// logger (printf-style, values sanitized).
func NewWriter(store BookModifier, history HistoryRecorder, source, changeType, batchPrefix string) *Writer {
	return &Writer{store: store, history: history, source: source, changeType: changeType,
		batchPrefix: batchPrefix, log: logger.New("repairs")}
}

// Modify runs fn on the book row inside ModifyBook (so a check fn makes and
// the write are one atomic step; fn may be retried) and records history for
// every tracked field that changed. It returns the changed field names in
// database.TrackedBookFields order. An error from fn aborts the write and is
// returned as is.
func (w *Writer) Modify(bookID string, fn func(*database.Book) error) ([]string, error) {
	var before, after *database.Book
	written, err := w.store.ModifyBook(bookID, func(row *database.Book) error {
		snap, serr := database.SnapshotBook(row)
		if serr != nil {
			return serr
		}
		if ferr := fn(row); ferr != nil {
			return ferr
		}
		post, perr := database.SnapshotBook(row)
		if perr != nil {
			return perr
		}
		// Captured on every invocation, so a retried callback leaves the
		// values of the attempt that committed.
		before, after = snap, post
		return nil
	})
	if err != nil {
		return nil, err
	}
	if written == nil {
		return nil, fmt.Errorf("book %s vanished before the write", bookID)
	}
	w.writes.Add(1)
	return w.recordHistory(bookID, before, after), nil
}

func (w *Writer) recordHistory(bookID string, before, after *database.Book) []string {
	batchID := w.batchPrefix + ulid.Make().String()
	now := time.Now()
	var changed []string
	failed := 0
	for _, field := range database.TrackedBookFields() {
		oldV, oerr := database.RenderBookField(before, field)
		newV, nerr := database.RenderBookField(after, field)
		if oerr != nil || nerr != nil || oldV == newV {
			continue
		}
		changed = append(changed, field)
		oldJSON, newJSON := jsonString(oldV), jsonString(newV)
		if err := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID:        bookID,
			Field:         field,
			PreviousValue: &oldJSON,
			NewValue:      &newJSON,
			ChangeType:    w.changeType,
			Source:        w.source,
			ChangedAt:     now,
			BatchID:       batchID,
		}); err != nil {
			failed++
			w.log.Warn("%s: history row not recorded (the write itself committed): book_id=%s field=%s err=%s",
				w.source, logger.SanitizeLogValue(bookID), field, logger.SanitizeLogValue(err.Error()))
			continue
		}
		w.historyRows.Add(1)
	}
	if len(changed) > 0 && w.journal != nil {
		if err := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID: bookID, Field: "apply", ChangeType: ChangeTypeApplyOpJournaled,
			Source: w.source, ChangedAt: now, BatchID: batchID,
		}); err != nil {
			// Without the marker, undo-last-apply would revert this batch's
			// fields alone; fall through to the incomplete marker, which it
			// also refuses.
			failed++
			w.log.Warn("%s: op-journaled marker not recorded: book_id=%s batch_id=%s err=%s",
				w.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(err.Error()))
		}
	}
	if failed > 0 {
		w.historyFailed.Add(int64(failed))
		if err := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID: bookID, Field: "apply", ChangeType: ChangeTypeApplyIncomplete,
			Source: w.source, ChangedAt: now, BatchID: batchID,
		}); err != nil {
			w.log.Error("%s: neither the history nor the incomplete marker was recorded: book_id=%s batch_id=%s err=%s",
				w.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(err.Error()))
		}
	}
	return changed
}

// Writes is how many Modify calls committed.
func (w *Writer) Writes() int { return int(w.writes.Load()) }

// HistoryRows is how many history rows were recorded.
func (w *Writer) HistoryRows() int { return int(w.historyRows.Load()) }

// HistoryFailed is how many history rows could not be recorded.
func (w *Writer) HistoryFailed() int { return int(w.historyFailed.Load()) }

// jsonString encodes s as a JSON string, the shape every history value has.
// Marshalling a Go string cannot fail; the fallback only keeps the linter
// honest.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return string(b)
}
