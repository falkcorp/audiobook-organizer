// file: internal/operations/opswriter/evidence.go
// version: 1.0.0
// guid: 86a05fa0-d0ef-43d9-a47d-4e1763aad9cc
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Wrote (ops.Writer) is the evidence a resumed Fixer apply needs before it
// reports a row as applied by an earlier attempt of the same run (05-PR5b):
// the history that attempt recorded. Every Writer write records history
// after it commits -- book fields as metadata history rows in batches
// "<batchPrefix><ulid>", everything else as operation journal rows under the
// op id -- so a row whose values match the plan's Proposed but has none of
// this run's history for one of its subjects was changed by someone else.
// Intents are no evidence: they are cleared once a write is recorded.

// JournalReader reads one operation's journal rows. GetOperationChanges is a
// complete prefix scan of opchange:<opID>: (no limit).
type JournalReader interface {
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// BookHistoryReader reads a book's metadata history rows.
type BookHistoryReader interface {
	GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error)
}

// completeHistory is the limit Wrote passes to GetBookChangeHistory. The
// store reads every row of the book before it truncates at limit, and the
// truncation is by key (metadata_change:<book>:<field>:<nanos>), so a smaller
// limit drops rows by field name, not by age: a book with more rows than the
// limit could lose exactly the run's own rows.
const completeHistory = math.MaxInt

// journalEvidence indexes the run's operation journal rows from earlier
// attempts by the subject they name. It is loaded on first use and kept for
// the Writer's life: rows this attempt records are never evidence, so a
// later load could add nothing.
type journalEvidence struct {
	mu     sync.Mutex
	loaded bool
	books  map[string]bool
	files  map[string]bool
}

// Wrote reports whether an earlier attempt of this run recorded history for
// every one of subjects (ops.Writer.Wrote). A book counts when a journal row
// under the op id has its BookID or a metadata history row of the book is in
// one of the run's own batches; a book_file counts when a journal row under
// the op id names it in its field ("book_file:<id>" from RepointBookFile,
// "file.<field>:<id>" from ModifyBookFile). Only rows recorded before
// Config.AttemptStart count. The op's journal is read once per Writer; a
// book's metadata history is read when the book has no journal evidence.
func (w *Writer) Wrote(ctx context.Context, subjects []ops.Subject) (bool, error) {
	// A read, but gated like a write: a fenced or canceled run reads
	// nothing, the refusal is flagged on the item like a write's, and a
	// resumed run that is recognizing rows (and so writing none) still
	// renews its stand-down lease.
	if err := w.check(ctx, "resumed row (history evidence read)"); err != nil {
		return false, err
	}
	if w.cfg.Mode == ops.Preview || len(subjects) == 0 {
		return false, nil
	}
	for _, s := range subjects {
		ok, err := w.subjectWritten(s)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// subjectWritten reports whether one subject has history from an earlier
// attempt of this run.
func (w *Writer) subjectWritten(s ops.Subject) (bool, error) {
	j, err := w.journalIndex()
	if err != nil {
		return false, err
	}
	switch s.Type {
	case ops.SubjectBook:
		if j.books[s.ID] {
			return true, nil
		}
		return w.bookHistoryWritten(s.ID)
	case ops.SubjectBookFile:
		return j.files[s.ID], nil
	default:
		return false, nil
	}
}

// journalIndex loads the run's earlier-attempt journal rows once. A failed
// read is returned and not cached, so the next row tries again.
func (w *Writer) journalIndex() (*journalEvidence, error) {
	j := &w.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.loaded {
		return j, nil
	}
	books, files := map[string]bool{}, map[string]bool{}
	if w.cfg.JournalReader != nil && w.cfg.OpID != "" {
		rows, err := w.cfg.JournalReader.GetOperationChanges(w.cfg.OpID)
		if err != nil {
			return nil, fmt.Errorf("read the journal of op %s: %w", w.cfg.OpID, err)
		}
		for _, c := range rows {
			// The store's prefix scan returns only this op's rows; the
			// check is here so the evidence never depends on that.
			if c == nil || c.OperationID != w.cfg.OpID || !w.earlierAttempt(c.CreatedAt) {
				continue
			}
			if c.BookID != "" {
				books[c.BookID] = true
			}
			if id := bookFileOfField(c.FieldName); id != "" {
				files[id] = true
			}
		}
	}
	j.books, j.files, j.loaded = books, files, true
	return j, nil
}

// bookHistoryWritten reports whether the book has a metadata history row in
// one of this run's batches from an earlier attempt.
func (w *Writer) bookHistoryWritten(bookID string) (bool, error) {
	// An empty prefix owns no batch (ownsBatch); skip the read.
	if w.cfg.HistoryReader == nil || w.cfg.History.batchPrefix == "" {
		return false, nil
	}
	rows, err := w.cfg.HistoryReader.GetBookChangeHistory(bookID, completeHistory)
	if err != nil {
		return false, fmt.Errorf("read the metadata history of book %s: %w", bookID, err)
	}
	for _, r := range rows {
		if w.cfg.History.ownsBatch(r.BatchID) && w.earlierAttempt(r.ChangedAt) {
			return true, nil
		}
	}
	return false, nil
}

// earlierAttempt reports whether a row recorded at t predates this attempt.
func (w *Writer) earlierAttempt(t time.Time) bool { return t.Before(w.cfg.AttemptStart) }

// bookFileOfField returns the book_file id a journal row's field names, or "".
func bookFileOfField(field string) string {
	if id, ok := strings.CutPrefix(field, "book_file:"); ok {
		return id
	}
	if rest, ok := strings.CutPrefix(field, "file."); ok {
		if i := strings.LastIndexByte(rest, ':'); i > 0 {
			return rest[i+1:]
		}
	}
	return ""
}
