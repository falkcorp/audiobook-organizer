// file: internal/operations/opswriter/evidence.go
// version: 2.2.0
// guid: 86a05fa0-d0ef-43d9-a47d-4e1763aad9cc
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// MarkRowApplied and RowApplied (ops.Writer) are the evidence a resumed
// Fixer apply needs before it reports a row as applied by an earlier attempt
// of the same run (05-PR5b). The evidence is per ROW: applyRow records a
// marker for the row as soon as the row's own Apply returns nil, naming the
// history Apply's writes recorded (each write notes its metadata history
// batch or operation journal row ids on the item, ops.NoteHistory). A marker
// can only exist because this run applied that row, so the attempt that
// wrote it does not matter. History is still read, but only to see whether
// what the marker names was undone since.
//
// Why not per subject: a run's history for a book proves the run wrote SOME
// row of that book, not this one. Two rows of one book (title, then
// publisher), the first written and the second not, with a foreign edit
// setting the publisher to the second row's Proposed between the attempts,
// made per-subject evidence report the foreign edit as the run's own.

// RowMarkStore stores the per-row apply markers (opv3:rowmark:).
type RowMarkStore interface {
	PutRowMarkV3(id string, m database.RowMarkV3) error
	GetRowMarkV3(id, rowID string) (*database.RowMarkV3, error)
}

// ChangeReader reads one operation change row by id (a point read).
type ChangeReader interface {
	GetOperationChange(operationID, changeID string) (*database.OperationChange, error)
}

// BookHistoryReader reads a book's metadata history rows.
type BookHistoryReader interface {
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
}

// completeHistory is the limit RowApplied passes to GetBookChangeHistory.
// The store reads every row of the book before it truncates at limit, and
// the truncation is by key (metadata_change:<book>:<field>:<nanos>), so a
// smaller limit drops rows by field name, not by age: a book with more rows
// than the limit could lose exactly the batch's rows or its undo.
const completeHistory = math.MaxInt

// historyRef is what a write notes on its item (ops.NoteHistory): one
// metadata history batch of a book, or one operation journal row.
type historyRef struct {
	Book   string `json:"book,omitempty"`
	Batch  string `json:"batch,omitempty"`
	Change string `json:"change,omitempty"`
}

func noteRef(ctx context.Context, r historyRef) {
	b, err := json.Marshal(r)
	if err != nil {
		// Three strings cannot fail to marshal; dropping the reference
		// would only make a later marker name less history.
		return
	}
	ops.NoteHistory(ctx, string(b))
}

// noteBatch notes a metadata history batch ModifyBook recorded.
func noteBatch(ctx context.Context, bookID, batchID string) {
	noteRef(ctx, historyRef{Book: bookID, Batch: batchID})
}

// noteChange notes an operation journal row journalRows recorded.
func noteChange(ctx context.Context, changeID string) {
	noteRef(ctx, historyRef{Change: changeID})
}

// MarkRowApplied records the apply marker of row rowID (ops.Writer): the
// row, and every batch and journal row its Apply's writes noted on ctx's
// item. Preview, a Writer without a marker store, or an empty rowID records
// nothing. Gated like a write: a fenced run or a canceled ctx records
// nothing. Nothing is returned: the row's writes committed, so a marker that
// cannot be recorded is logged, and costs only the evidence (a resume skips
// the row as changed since plan; it never writes it twice).
func (w *Writer) MarkRowApplied(ctx context.Context, rowID string) {
	if w.cfg.Mode == ops.Preview || w.cfg.RowMarks == nil || rowID == "" {
		return
	}
	what := "apply marker of row " + rowID
	if err := w.check(ctx, what); err != nil {
		pkgLog.Warn("op %s: %s", logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(err.Error()))
		return
	}
	m := database.RowMarkV3{RowID: rowID, MarkedAt: time.Now().UTC()}
	for _, raw := range ops.NotedHistory(ctx) {
		var r historyRef
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			// A marker that names less than the row wrote could outlive an
			// undo of the part it leaves out: record none.
			pkgLog.Warn("op %s: %s not written: unreadable history reference %q",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(what), logger.SanitizeLogValue(raw))
			return
		}
		switch {
		case r.Change != "":
			m.Changes = append(m.Changes, r.Change)
		case r.Book != "" && r.Batch != "":
			m.BookBatches = append(m.BookBatches, database.RowMarkBatch{BookID: r.Book, BatchID: r.Batch})
		}
	}
	if err := w.cfg.RowMarks.PutRowMarkV3(w.cfg.OpID, m); err != nil {
		pkgLog.Warn("op %s: %s not written (the row's writes committed; a resume will skip it): %s",
			logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(what), logger.SanitizeLogValue(err.Error()))
	}
}

// RowApplied reports whether some attempt of this run marked row rowID
// applied and nothing the marker names has been undone since (ops.Writer):
// every journal row it names is still there, neither voided nor reverted,
// and no batch it names is covered by an undo (batchesStand). A
// read is gated like a write, so a fenced or canceled run reads nothing and
// a resumed run that is only recognizing rows still renews its stand-down
// lease. One point read for the marker, one per journal row it names, one
// history read per book its batches touch.
func (w *Writer) RowApplied(ctx context.Context, rowID string) (bool, error) {
	if err := w.checkAs(ctx, "apply marker of resumed row "+rowID, refusedRead); err != nil {
		return false, err
	}
	if w.cfg.Mode == ops.Preview || w.cfg.RowMarks == nil || rowID == "" {
		return false, nil
	}
	m, err := w.cfg.RowMarks.GetRowMarkV3(w.cfg.OpID, rowID)
	if err != nil {
		return false, fmt.Errorf("read the apply marker of row %s: %w", rowID, err)
	}
	if m == nil {
		return false, nil
	}
	ok, err := w.changesStand(m.Changes)
	if err != nil || !ok {
		return false, err
	}
	return w.batchesStand(m.BookBatches)
}

// changesStand reports whether every journal row of ids (under this op id)
// is still there and neither voided nor reverted. A row that cannot be
// found cannot be shown to stand. Voided is checked on its own: a voided
// row normally has RevertedAt set too, but nothing older guarantees it.
func (w *Writer) changesStand(ids []string) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	if w.cfg.Changes == nil {
		return false, nil
	}
	for _, id := range ids {
		c, err := w.cfg.Changes.GetOperationChange(w.cfg.OpID, id)
		if err != nil {
			return false, fmt.Errorf("read journal row %s of op %s: %w", id, w.cfg.OpID, err)
		}
		if c == nil || c.Voided || c.RevertedAt != nil {
			return false, nil
		}
	}
	return true, nil
}

// batchesStand reports whether every batch still stands: its rows are in
// its book's history and no undo row (ChangeTypeUndo) was recorded at or
// after it on a field it wrote. That covers every undo there is:
// undo-last-apply (undo rows in the batch, one per field it put back), a
// single-field undo (no batch id) and an operation revert (its op's id as
// the batch). It can also see an undo of a later write to the same field
// and refuse a batch that still stands; that only skips the row as changed
// since plan.
func (w *Writer) batchesStand(batches []database.RowMarkBatch) (bool, error) {
	if len(batches) == 0 {
		return true, nil
	}
	if w.cfg.HistoryReader == nil {
		return false, nil
	}
	byBook := map[string]map[string]bool{}
	for _, b := range batches {
		if byBook[b.BookID] == nil {
			byBook[b.BookID] = map[string]bool{}
		}
		byBook[b.BookID][b.BatchID] = true
	}
	for bookID, want := range byBook {
		rows, err := w.cfg.HistoryReader.GetBookChangeHistory(bookID, completeHistory)
		if err != nil {
			return false, fmt.Errorf("read the metadata history of book %s: %w", bookID, err)
		}
		if !batchesStandIn(rows, want) {
			return false, nil
		}
	}
	return true, nil
}

// batchesStandIn applies batchesStand's rule to one book's history rows.
func batchesStandIn(rows []database.MetadataChangeRecord, want map[string]bool) bool {
	// found: per wanted batch, the fields it wrote and when.
	type wrote struct {
		at     time.Time
		fields map[string]bool
	}
	found := map[string]*wrote{}
	for _, r := range rows {
		if !want[r.BatchID] || r.ChangeType == ChangeTypeUndo {
			continue
		}
		f := found[r.BatchID]
		if f == nil {
			f = &wrote{at: r.ChangedAt, fields: map[string]bool{}}
			found[r.BatchID] = f
		}
		if r.ChangedAt.Before(f.at) {
			f.at = r.ChangedAt
		}
		f.fields[r.Field] = true
	}
	if len(found) != len(want) {
		return false
	}
	for _, r := range rows {
		if r.ChangeType != ChangeTypeUndo {
			continue
		}
		for _, f := range found {
			if f.fields[r.Field] && !r.ChangedAt.Before(f.at) {
				return false
			}
		}
	}
	return true
}
