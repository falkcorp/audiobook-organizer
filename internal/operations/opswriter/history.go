// file: internal/operations/opswriter/history.go
// version: 1.5.1
// guid: 00dad0e6-6243-4100-875e-bbe6f68ba728
// last-edited: 2026-10-30

package opswriter

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// History rows are written only AFTER the write they describe committed
// (sdk-api.md §3 step 4): a row written first could describe a change that
// never landed. Book fields go to metadata history, as internal/repairs has
// always recorded them, so "undo last apply" keeps working. Everything else a
// Writer changes (book_file fields, a repoint, credits, an Effect) is an
// operation change row (opchange:) under the run's op id. All three row kinds
// (a repoint, a book_file field edit, a credit-list edit) use change types the
// operation revert reverses.

// Change types of the operation change rows this package writes.
const (
	// ChangeTypeApplyIncomplete is written into a history batch some of whose
	// rows could not be recorded; UndoLastApply refuses such a batch. Same
	// value as metafetch.ChangeTypeApplyIncomplete.
	ChangeTypeApplyIncomplete = "apply_incomplete"
	// ChangeTypeApplyOpJournaled marks a batch on a book that also has
	// operation change rows; UndoLastApply refuses it (the op revert undoes
	// it). Same value as metafetch.ChangeTypeApplyOpJournaled.
	ChangeTypeApplyOpJournaled = "apply_op_journaled"
	// ChangeTypeBookFileModify is one book_file field a Writer changed
	// (FieldName "file.<json field>:<file id>"). The operation revert
	// reverses it; the constant lives in database beside the other change
	// types the revert owns (database.ChangeTypeBookFileModify).
	ChangeTypeBookFileModify = database.ChangeTypeBookFileModify
	// ChangeTypeBookAuthorsModify is a book's whole credit list before and
	// after a ModifyBookAuthors. The operation revert reverses it
	// (database.ChangeTypeBookAuthorsModify).
	ChangeTypeBookAuthorsModify = database.ChangeTypeBookAuthorsModify
	// ChangeTypeEffectPrefix prefixes an Effect's rows: "effect:<name>".
	ChangeTypeEffectPrefix = "effect:"
)

// HistoryRecorder records metadata-history rows.
type HistoryRecorder interface {
	RecordMetadataChange(record *database.MetadataChangeRecord) error
}

// ChangeJournal records operation change rows.
type ChangeJournal interface {
	CreateOperationChange(change *database.OperationChange) error
}

// BookHistory records a writer's book-field history: one metadata-history row
// per changed tracked field, all under one batch id per write, plus the
// markers undo-last-apply reads. Safe for concurrent use.
type BookHistory struct {
	rec                             HistoryRecorder
	source, changeType, batchPrefix string
	log                             logger.LevelLogger
	rows, failed                    atomic.Int64
}

// NewBookHistory builds a BookHistory. source and changeType go on every row;
// batchPrefix starts every batch id.
func NewBookHistory(rec HistoryRecorder, source, changeType, batchPrefix string, log logger.LevelLogger) *BookHistory {
	if log == nil {
		log = logger.New("opswriter")
	}
	return &BookHistory{rec: rec, source: source, changeType: changeType, batchPrefix: batchPrefix, log: log}
}

// recording reports whether h has a recorder to write rows to.
func (h *BookHistory) recording() bool { return h != nil && h.rec != nil }

// Rows is how many history rows were recorded.
func (h *BookHistory) Rows() int { return int(h.rows.Load()) }

// Failed is how many history rows could not be recorded.
func (h *BookHistory) Failed() int { return int(h.failed.Load()) }

// BatchPrefix is the batch prefix of every metadata history batch a v3
// run's Writer records (registry newV3Writer). RowApplied trusts a batch as
// the run's own only by this prefix (standsIn), so the two must not drift.
func BatchPrefix(opID string) string { return "op-" + opID + "-" }

// NewBatchID returns a fresh batch id.
func (h *BookHistory) NewBatchID() string { return h.batchPrefix + ulid.Make().String() }

// RecordDiff records one row per tracked field that differs between before
// and after, all in batchID (NewBatchID; the caller mints it so it can name
// the batch), and returns the changed field names in
// database.TrackedBookFields order. journaled marks a book that also has operation change rows (its
// batch gets the apply_op_journaled marker). A row that cannot be recorded
// makes the batch apply_incomplete. With no recorder it records nothing but
// still reports the changes. complete is false when a changed field (or the
// journaled marker) was not recorded, including when there is no recorder:
// the Writer then leaves the write's intent in place.
func (h *BookHistory) RecordDiff(bookID, batchID string, before, after *database.Book, journaled bool) (changed []string, complete bool) {
	now := time.Now()
	failed := 0
	for _, field := range database.TrackedBookFields() {
		oldV, oerr := database.RenderBookField(before, field)
		newV, nerr := database.RenderBookField(after, field)
		if oerr != nil || nerr != nil || oldV == newV {
			continue
		}
		changed = append(changed, field)
		if h.rec == nil {
			continue
		}
		oldJSON, newJSON := jsonString(oldV), jsonString(newV)
		rec := &database.MetadataChangeRecord{
			BookID: bookID, Field: field, PreviousValue: &oldJSON, NewValue: &newJSON,
			ChangeType: h.changeType, Source: h.source, ChangedAt: now, BatchID: batchID,
		}
		// "Undo last apply" restores a foreign-key column from the row's refs,
		// never from the rendered value, and fails the field without them; a
		// series_id write through the Writer was not undoable until these
		// were recorded. author_id is left without refs on purpose: undo of
		// that column also needs the book_authors join (BookAuthorsKnown),
		// which the Writer does not read, and reverting the column alone
		// would leave the join naming the new author.
		if field == "series_id" {
			rec.PreviousRef = &database.MetadataChangeRef{SeriesID: copyInt(before.SeriesID)}
			rec.NewRef = &database.MetadataChangeRef{SeriesID: copyInt(after.SeriesID)}
		}
		if err := h.rec.RecordMetadataChange(rec); err != nil {
			failed++
			h.log.Warn("%s: history row not recorded (the write itself committed): book_id=%s field=%s err=%s",
				h.source, logger.SanitizeLogValue(bookID), field, logger.SanitizeLogValue(err.Error()))
			continue
		}
		h.rows.Add(1)
	}
	if h.rec == nil {
		return changed, len(changed) == 0
	}
	if len(changed) > 0 && journaled {
		if err := h.RecordMarker(bookID, ChangeTypeApplyOpJournaled, batchID, now); err != nil {
			// Without the marker, undo-last-apply would revert this batch's
			// fields alone; fall through to the incomplete marker, which it
			// also refuses.
			failed++
			h.log.Warn("%s: op-journaled marker not recorded: book_id=%s batch_id=%s err=%s",
				h.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(err.Error()))
		}
	}
	if failed > 0 {
		h.failed.Add(int64(failed))
		if err := h.RecordMarker(bookID, ChangeTypeApplyIncomplete, batchID, now); err != nil {
			h.log.Error("%s: neither the history nor the incomplete marker was recorded: book_id=%s batch_id=%s err=%s",
				h.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(err.Error()))
		}
	}
	return changed, failed == 0
}

// RecordField records one already-rendered field change (values are JSON
// strings) in batchID, counting it as recorded or failed.
func (h *BookHistory) RecordField(bookID, field, oldJSON, newJSON, batchID string, at time.Time) {
	if h.rec == nil {
		return
	}
	if err := h.rec.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID: bookID, Field: field, PreviousValue: &oldJSON, NewValue: &newJSON,
		ChangeType: h.changeType, Source: h.source, ChangedAt: at, BatchID: batchID,
	}); err != nil {
		h.failed.Add(1)
		h.log.Warn("%s: history row not recorded (the write itself committed): book_id=%s field=%s err=%s",
			h.source, logger.SanitizeLogValue(bookID), field, logger.SanitizeLogValue(err.Error()))
		return
	}
	h.rows.Add(1)
}

// RecordMarker records an undo-last-apply marker row (apply_incomplete or
// apply_op_journaled) in batchID.
func (h *BookHistory) RecordMarker(bookID, changeType, batchID string, at time.Time) error {
	if h.rec == nil {
		return nil
	}
	return h.rec.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID: bookID, Field: "apply", ChangeType: changeType,
		Source: h.source, ChangedAt: at, BatchID: batchID,
	})
}

// JSONString encodes s as a JSON string, the shape every history value has.
func JSONString(s string) string { return jsonString(s) }

// jsonString encodes s as a JSON string. Marshalling a Go string cannot
// fail; the fallback only satisfies the linter.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return string(b)
}

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
