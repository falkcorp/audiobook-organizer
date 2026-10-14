// file: internal/operations/opswriter/evidence.go
// version: 2.2.0
// guid: 86a05fa0-d0ef-43d9-a47d-4e1763aad9cc
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// ChangeReader reads operation change rows: one by id (a point read), and a
// book's whole journal across every operation (the opchange_by_book index
// once it is trusted, a full journal scan until then).
type ChangeReader interface {
	GetOperationChange(operationID, changeID string) (*database.OperationChange, error)
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

// BookHistoryReader reads a book's whole metadata history, uncapped, and
// counts the rows that did not decode (database.PebbleStore
// ReadBookChangeHistory). GetBookChangeHistory is not enough: it drops an
// undecodable row silently, and the row it drops could be the one that
// overwrote a batch.
type BookHistoryReader interface {
	ReadBookChangeHistory(bookID string) (rows []database.MetadataChangeRecord, undecodable int, err error)
}

// historyRef is what a write notes on its item (ops.NoteHistory): one
// metadata history batch of a book, one operation journal row, or that some
// of a write's history could not be recorded (Incomplete).
type historyRef struct {
	Book   string `json:"book,omitempty"`
	Batch  string `json:"batch,omitempty"`
	Change string `json:"change,omitempty"`
	// Incomplete: a write of the item committed but part of its history
	// was not recorded. A marker naming only the rest could outlive an
	// overwrite or undo of the part it leaves out, so MarkRowApplied
	// records none.
	Incomplete bool `json:"incomplete,omitempty"`
}

func noteRef(ctx context.Context, r historyRef) {
	b, err := json.Marshal(r)
	if err != nil {
		// Three strings and a bool cannot fail to marshal; dropping the
		// reference would only make a later marker name less history.
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

// noteIncomplete notes that a committed write's history was not all
// recorded (historyRef.Incomplete).
func noteIncomplete(ctx context.Context) {
	noteRef(ctx, historyRef{Incomplete: true})
}

// MarkRowApplied records the apply marker of row rowID (ops.Writer): the
// row, and every batch and journal row its Apply's writes noted on ctx's
// item. Preview, a Writer without a marker store, or an empty rowID records
// nothing. Gated like a write: a fenced run or a canceled ctx records
// nothing. So does an item one of whose writes could not record all of its
// history (noteIncomplete). Nothing is returned: the row's writes
// committed, so a marker that cannot be recorded is logged, and costs only
// the evidence (a resume skips the row as changed since plan; it never
// writes it twice).
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
		case r.Incomplete:
			pkgLog.Warn("op %s: %s not written: part of the row's history was not recorded (a resume will skip the row)",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(what))
			return
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
// applied and everything the marker names still stands (ops.Writer): each
// journal row is still there, neither voided nor reverted, and still the
// newest journal row on what it wrote (changesStand); each batch is still
// the newest history on every field it wrote, with no undo of it
// (batchesStand). A read is gated like a write, so a fenced or canceled run
// reads nothing and a resumed run that is only recognizing rows still
// renews its stand-down lease. One point read for the marker, one per
// journal row it names, one journal read per book those rows name, one
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
// still stands: it is still there, neither voided nor reverted (Voided is
// checked on its own: a voided row normally has RevertedAt set too, but
// nothing older guarantees it), and no other journal row of its book, of
// any operation and any change type, was recorded at or after it on what it
// wrote (journalRowsNewest). A row that cannot be found cannot be shown to
// stand.
//
// A row with no book id has no per-book journal to look in, so only the
// first two checks apply to it. Nor can any check see a write that recorded
// no journal row at all: most book_file writers outside the Writer (the
// scanner, the AcoustID and tag backfills) record none, and seeing those
// needs a per-book_file history this store does not keep.
func (w *Writer) changesStand(ids []string) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	if w.cfg.Changes == nil {
		return false, nil
	}
	byBook := map[string][]*database.OperationChange{}
	for _, id := range ids {
		c, err := w.cfg.Changes.GetOperationChange(w.cfg.OpID, id)
		if err != nil {
			return false, fmt.Errorf("read journal row %s of op %s: %w", id, w.cfg.OpID, err)
		}
		if c == nil || c.Voided || c.RevertedAt != nil {
			return false, nil
		}
		if c.BookID != "" {
			byBook[c.BookID] = append(byBook[c.BookID], c)
		}
	}
	for bookID, named := range byBook {
		rows, err := w.cfg.Changes.GetBookChanges(bookID)
		if err != nil {
			return false, fmt.Errorf("read the operation journal of book %s: %w", bookID, err)
		}
		if !journalRowsNewest(rows, named) {
			return false, nil
		}
	}
	return true, nil
}

// journalRowsNewest reports whether no row of rows other than c itself was
// recorded at or after c on what c wrote (journalOverlap), for every c of
// named. A tie counts against c. So does a later row of this same run, or
// one an operation revert has since reverted: the rule only asks whether c
// is still the newest, never whether a later write restored its value.
func journalRowsNewest(rows, named []*database.OperationChange) bool {
	for _, c := range named {
		for _, r := range rows {
			if r.OperationID == c.OperationID && r.ID == c.ID {
				continue
			}
			if journalOverlap(c.FieldName, r.FieldName) && !r.CreatedAt.Before(c.CreatedAt) {
				return false
			}
		}
	}
	return true
}

// journalSubject splits an operation journal FieldName into the book_file it
// names (file, "" for none) and what of it (part). "book_file:<id>" is a
// write to the file row as a whole (a repoint, a move, a reassign, a track
// change: every book_file journal row outside the Writer is named this way),
// part "". "file.<field>:<id>" is one field of it (Writer.ModifyBookFile).
// "file_path", the organizer's and the reconciler's book-level row, names
// no single file but moves the book's files, so it is every file of the
// book: file "*". Anything else (credits, an Effect's own field) is itself.
func journalSubject(field string) (file, part string) {
	if field == "file_path" {
		return "*", ""
	}
	if id, ok := strings.CutPrefix(field, "book_file:"); ok {
		return id, ""
	}
	if rest, ok := strings.CutPrefix(field, "file."); ok {
		if i := strings.LastIndexByte(rest, ':'); i > 0 {
			return rest[i+1:], rest[:i]
		}
	}
	return "", field
}

// journalOverlap reports whether journal rows named a and b can write the
// same thing: the same field of the same file, a file and any field of it,
// any file of the book and any file, or the same non-file field.
func journalOverlap(a, b string) bool {
	fa, pa := journalSubject(a)
	fb, pb := journalSubject(b)
	switch {
	case fa == "" || fb == "":
		return fa == fb && pa == pb
	case fa == "*" || fb == "*":
		return true
	default:
		return fa == fb && (pa == "" || pb == "" || pa == pb)
	}
}

// batchesStand reports whether every batch still stands in its book's
// metadata history (batchesStandIn). A book whose history has a row that
// does not decode cannot be shown to stand: the unreadable row could be the
// one that overwrote or undid the batch.
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
		rows, undecodable, err := w.cfg.HistoryReader.ReadBookChangeHistory(bookID)
		if err != nil {
			return false, fmt.Errorf("read the metadata history of book %s: %w", bookID, err)
		}
		if undecodable > 0 {
			pkgLog.Warn("op %s: %d metadata history rows of book %s do not decode; its batches are not counted as standing",
				logger.SanitizeLogValue(w.cfg.OpID), undecodable, logger.SanitizeLogValue(bookID))
			return false, nil
		}
		if !batchesStandIn(rows, want, w.cfg.OpID) {
			return false, nil
		}
	}
	return true, nil
}

// batchesStandIn applies batchesStand's rule to one book's history rows. A
// wanted batch stands only while all of this holds:
//
//   - Its rows are there.
//   - It is the NEWEST history on every field it wrote, whatever the change
//     type of any later row: a manual edit, another apply, a scan, a
//     single-field undo (which carries no batch id) all make it stop
//     standing, even one that put the batch's own value back. Fields are
//     compared by Book JSON name, since other writers record some under
//     their history names (database.HistoryFieldToJSON).
//   - No undo of it was recorded, judged without the clock, so a skewed
//     timestamp cannot hide one: undo-last-apply records its undo rows in
//     the batch itself, on the fields it puts back, so a batch with two rows
//     on one field was undone; an operation revert records its rows under
//     the reverted op's id, so any row under this run's op id (opID) means
//     the run was reverted on this book.
//
// The undo markers undo-last-apply reads (apply_op_journaled,
// apply_incomplete) are not field writes and are left out. A row whose
// Apply wrote one field twice, in two batches, is not claimed: its first
// batch is no longer the newest. That only skips the row as changed since
// plan.
func batchesStandIn(rows []database.MetadataChangeRecord, want map[string]bool, opID string) bool {
	type newestRow struct {
		at    time.Time
		batch string // "" when two batches tie
	}
	newest := map[string]newestRow{}
	wrote := map[string]map[string]int{} // batch -> field -> rows
	for _, r := range rows {
		if opID != "" && r.BatchID == opID {
			return false
		}
		if r.ChangeType == ChangeTypeApplyOpJournaled || r.ChangeType == ChangeTypeApplyIncomplete {
			continue
		}
		field := r.Field
		if j, ok := database.HistoryFieldToJSON[field]; ok {
			field = j
		}
		switch n, ok := newest[field]; {
		case !ok || r.ChangedAt.After(n.at):
			newest[field] = newestRow{at: r.ChangedAt, batch: r.BatchID}
		case r.ChangedAt.Equal(n.at) && r.BatchID != n.batch:
			newest[field] = newestRow{at: n.at}
		}
		if want[r.BatchID] {
			if wrote[r.BatchID] == nil {
				wrote[r.BatchID] = map[string]int{}
			}
			wrote[r.BatchID][field]++
		}
	}
	if len(wrote) != len(want) {
		return false
	}
	for batch, fields := range wrote {
		for field, n := range fields {
			if n > 1 || newest[field].batch != batch {
				return false
			}
		}
	}
	return true
}
