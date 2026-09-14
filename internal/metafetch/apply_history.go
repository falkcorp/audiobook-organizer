// file: internal/metafetch/apply_history.go
// version: 1.2.0
// guid: 4b9d7e21-0c3a-4f58-b6e2-8a1f5d3c9e07
// last-edited: 2026-09-13

package metafetch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var applyHistoryLog = logger.New("metafetch-apply-history")

// History field names that differ from the Book JSON name. author_id and
// series_id are foreign keys: their history rows carry the display names in
// PreviousValue/NewValue (what the UI and the revert-metadata-fetch job read)
// and the ids in PreviousRef/NewRef (what undo restores). series_sequence has
// always been recorded as series_position.
const (
	historyFieldAuthor   = "author_name"
	historyFieldSeries   = "series"
	historyFieldSeriesNo = "series_position"
)

var historyToJSON = map[string]string{
	historyFieldAuthor:   "author_id",
	historyFieldSeries:   "series_id",
	historyFieldSeriesNo: "series_sequence",
}

func jsonToHistory(jsonName string) string {
	for h, j := range historyToJSON {
		if j == jsonName {
			return h
		}
	}
	return jsonName
}

// ChangeTypeApplyUndo is the change_type of the rows UndoLastApply records.
const ChangeTypeApplyUndo = "undo"

// ChangeTypeApplyIncomplete marks an apply whose history rows were not all
// recorded. UndoLastApply refuses that batch, rather than undo part of it or
// reach past it to the apply before.
const ChangeTypeApplyIncomplete = "apply_incomplete"

// ErrApplyHistoryIncomplete: an apply's write committed but its history rows
// were not all recorded, so undo is refused for it.
var ErrApplyHistoryIncomplete = errors.New("the apply's change history was not fully recorded; it cannot be undone")

// ErrFieldAlreadyUndone: the newest change to the field is an undo. Treating
// that undo as a change to undo put the provider value back on a second click.
var ErrFieldAlreadyUndone = errors.New("the field's last change has already been undone")

// newApplyBatchID returns a random id shared by every history row one apply
// writes, so "undo last apply" can find exactly that apply's rows.
func newApplyBatchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("apply-%d", time.Now().UnixNano())
	}
	return "apply-" + hex.EncodeToString(b[:])
}

// RecordApplyHistory records one history row per Book field the apply
// actually changed, from the committed diff: before is the row as read before
// the apply (database.SnapshotBook), after is the row as written. It must run
// AFTER the write commits, so a value the apply's IsBetter checks refused, or a
// write that failed, is never recorded. prevAuthors is the book_authors join as
// it stood before the apply (nil when unknown); undo restores it.
//
// Every row shares one batch id, which is returned ("" when nothing changed).
//
// A row that cannot be written is logged at Error; the batch is then marked
// incomplete (markApplyIncomplete) and ErrApplyHistoryIncomplete returned, so
// UndoLastApply refuses it instead of undoing part of it or an older apply.
func (mfs *Service) RecordApplyHistory(before, after *database.Book, prevAuthors []database.BookAuthor, source string) (string, error) {
	if before == nil || after == nil {
		return "", nil
	}
	changed, err := database.ChangedBookFields(before, after)
	if err != nil {
		batchID := newApplyBatchID()
		applyHistoryLog.Error("apply history for %s not recorded: %v", logger.SanitizeLogValue(after.ID), err)
		mfs.markApplyIncomplete(after.ID, batchID, source)
		return batchID, fmt.Errorf("%w: %v", ErrApplyHistoryIncomplete, err)
	}
	if len(changed) == 0 {
		return "", nil
	}
	batchID := newApplyBatchID()
	failed := 0
	now := time.Now()
	activityTitle := after.Title
	if activityTitle == "" {
		activityTitle = after.ID
	}
	for _, jsonName := range changed {
		rec := &database.MetadataChangeRecord{
			BookID:     after.ID,
			Field:      jsonToHistory(jsonName),
			ChangeType: "fetched",
			Source:     source,
			ChangedAt:  now,
			BatchID:    batchID,
		}
		var oldVal, newVal string
		switch jsonName {
		case "author_id":
			oldVal, newVal = mfs.authorName(before.AuthorID), mfs.authorName(after.AuthorID)
			rec.PreviousRef = &database.MetadataChangeRef{AuthorID: before.AuthorID, BookAuthors: prevAuthors, BookAuthorsKnown: prevAuthors != nil}
			newAuthors, aerr := mfs.db.GetBookAuthors(after.ID)
			rec.NewRef = &database.MetadataChangeRef{AuthorID: after.AuthorID, BookAuthors: newAuthors, BookAuthorsKnown: aerr == nil}
		case "series_id":
			oldVal, newVal = mfs.seriesName(before.SeriesID), mfs.seriesName(after.SeriesID)
			rec.PreviousRef = &database.MetadataChangeRef{SeriesID: before.SeriesID}
			rec.NewRef = &database.MetadataChangeRef{SeriesID: after.SeriesID}
		default:
			oldVal, _ = database.RenderBookField(before, jsonName)
			newVal, _ = database.RenderBookField(after, jsonName)
		}
		oldJSON, newJSON := jsonEncodeString(oldVal), jsonEncodeString(newVal)
		rec.PreviousValue, rec.NewValue = &oldJSON, &newJSON
		if err := mfs.db.RecordMetadataChange(rec); err != nil {
			failed++
			applyHistoryLog.Error("failed to record metadata change for %s.%s: %v", logger.SanitizeLogValue(after.ID), rec.Field, err)
		}
		if mfs.activityService != nil {
			_ = mfs.activityService.Record(database.ActivityEntry{
				Tier:    "change",
				Type:    "metadata_apply",
				Level:   "info",
				Source:  "background",
				BookID:  after.ID,
				Summary: fmt.Sprintf("%s: Applied %s: %s → %s", activityTitle, rec.Field, displayOrNone(truncateActivity(oldVal, 50)), truncateActivity(newVal, 50)),
				Details: map[string]any{"field": rec.Field, "old_value": oldVal, "new_value": newVal, "source": source, "batch_id": batchID},
			})
		}
	}
	if failed > 0 {
		mfs.markApplyIncomplete(after.ID, batchID, source)
		return batchID, fmt.Errorf("%w: %d of %d rows of %s not recorded", ErrApplyHistoryIncomplete, failed, len(changed), after.ID)
	}
	return batchID, nil
}

// markApplyIncomplete records that batchID's history is not complete, so
// UndoLastApply refuses the batch. If even this row cannot be written the
// apply leaves no trace in history, and undo could reach the apply before it;
// that is logged at Error.
func (mfs *Service) markApplyIncomplete(bookID, batchID, source string) {
	if err := mfs.db.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:     bookID,
		Field:      "apply",
		ChangeType: ChangeTypeApplyIncomplete,
		Source:     source,
		ChangedAt:  time.Now(),
		BatchID:    batchID,
	}); err != nil {
		applyHistoryLog.Error("apply %s on %s: neither its history nor the incomplete marker was recorded; undo last apply may reach an older apply: %v", batchID, logger.SanitizeLogValue(bookID), err)
	}
}

// KnownBookAuthors normalises a GetBookAuthors result for RecordApplyHistory,
// where a nil join means "not known": a successful read of a book with no
// credits becomes an empty, non-nil slice.
func KnownBookAuthors(authors []database.BookAuthor, err error) ([]database.BookAuthor, error) {
	if err != nil {
		return nil, err
	}
	if authors == nil {
		authors = []database.BookAuthor{}
	}
	return authors, nil
}

func (mfs *Service) authorName(id *int) string {
	if id == nil {
		return ""
	}
	if a, err := mfs.db.GetAuthorByID(*id); err == nil && a != nil {
		return a.Name
	}
	return ""
}

func (mfs *Service) seriesName(id *int) string {
	if id == nil {
		return ""
	}
	if s, err := mfs.db.GetSeriesByID(*id); err == nil && s != nil {
		return s.Name
	}
	return ""
}

// ErrNoApplyToUndo: the book has no recorded metadata apply left to undo.
var ErrNoApplyToUndo = errors.New("no metadata apply to undo")

// ErrApplyPredatesBatches: the book's only applies were recorded before
// history rows carried a batch id, so which rows belong to one apply is not
// known. Undoing a guessed set is what the old ±2s window did; it is refused.
var ErrApplyPredatesBatches = errors.New("the last metadata apply was recorded before applies were grouped; undo its fields one at a time")

// ErrApplyAlreadyUndone: the book's newest apply has already been undone.
// Undo-last-apply only ever undoes the newest apply; it does not walk back to
// an older one.
var ErrApplyAlreadyUndone = errors.New("the last metadata apply has already been undone")

// isApplyRow reports whether r was written by a metadata apply: a batched
// row, or a pre-batch "fetched" row that names its provider. The per-field
// state service also records "fetched" rows, with no source; those are not
// applies to the book row.
func isApplyRow(r *database.MetadataChangeRecord) bool {
	if r.ChangeType == ChangeTypeApplyUndo {
		return false
	}
	if r.BatchID != "" {
		return true
	}
	return r.ChangeType == "fetched" && r.Source != ""
}

// UndoApplyResult reports what UndoLastApply did with each field of the apply.
type UndoApplyResult struct {
	BatchID string `json:"batch_id"`
	Source  string `json:"source,omitempty"`
	// Reverted fields were set back to the value before the apply.
	Reverted []string `json:"reverted"`
	// ChangedSince fields no longer hold what the apply wrote; they were left.
	ChangedSince []string `json:"changed_since,omitempty"`
	// AlreadyRestored fields already hold the pre-apply value.
	AlreadyRestored []string `json:"already_restored,omitempty"`
	// Locked fields are user-locked; the undo does not write them.
	Locked []string `json:"locked,omitempty"`
	// Failed fields could not be restored (unparsable or unknown field).
	Failed []string `json:"failed,omitempty"`
	// AuthorCreditsLeft is set when author_name was reverted but the
	// book_authors join changed since the apply (or was not recorded), so the
	// join was left as it is.
	AuthorCreditsLeft bool `json:"author_credits_left,omitempty"`
	// RevertedTo is the history row's pre-change value that UndoFieldChange
	// put back, or found already back. Nil when nothing was restored.
	RevertedTo *string `json:"reverted_to,omitempty"`

	authorRefused []string
}

// UndoLastApply reverts the most recent metadata apply on a book: the rows of
// its newest history batch that has not been undone. Each field is put back
// only while it still holds exactly what the apply wrote (compare-and-set
// inside ModifyBook); a field edited since is left and reported. Nothing is
// written to the per-field override state -- the old undo stored the
// pre-apply value as a user override, which froze it against every later
// fetch and still left the book row holding the applied value.
func (mfs *Service) UndoLastApply(bookID string) (*UndoApplyResult, error) {
	history, err := mfs.db.GetBookChangeHistory(bookID, 1<<30)
	if err != nil {
		return nil, fmt.Errorf("read change history: %w", err)
	}
	undone := map[string]bool{}
	for _, r := range history {
		if r.ChangeType == ChangeTypeApplyUndo && r.BatchID != "" {
			undone[r.BatchID] = true
		}
	}
	// The target is the newest apply on the book, full stop. An apply that
	// has already been undone is refused, never skipped: skipping it made a
	// second click revert the apply before it, which nobody asked for.
	var latest *database.MetadataChangeRecord
	for i := range history {
		r := &history[i]
		if !isApplyRow(r) {
			continue
		}
		if latest == nil || r.ChangedAt.After(latest.ChangedAt) {
			latest = r
		}
	}
	switch {
	case latest == nil:
		return nil, ErrNoApplyToUndo
	case latest.BatchID == "":
		return nil, ErrApplyPredatesBatches
	case undone[latest.BatchID]:
		return nil, ErrApplyAlreadyUndone
	}
	var rows []database.MetadataChangeRecord
	for _, r := range history {
		if r.BatchID != latest.BatchID || r.ChangeType == ChangeTypeApplyUndo {
			continue
		}
		// Some of this apply's rows were never written: undoing the rest
		// would leave the book half undone, so the apply is refused whole.
		if r.ChangeType == ChangeTypeApplyIncomplete {
			return nil, ErrApplyHistoryIncomplete
		}
		rows = append(rows, r)
	}

	res := &UndoApplyResult{BatchID: latest.BatchID, Source: latest.Source}
	rows = mfs.dropUnrevertableAuthorRows(bookID, rows, res)
	var restoredLocked []string
	updated, err := mfs.db.ModifyBook(bookID, func(row *database.Book) error {
		res.Reverted, res.ChangedSince, res.AlreadyRestored = nil, nil, nil
		res.Failed = slices.Clone(res.authorRefused)
		var lockErr error
		restoredLocked, lockErr = database.ApplyRespectingLocks(mfs.db, row, func(b *database.Book) {
			for i := range rows {
				switch undoOneField(b, &rows[i]) {
				case undoReverted:
					res.Reverted = append(res.Reverted, rows[i].Field)
				case undoChangedSince:
					res.ChangedSince = append(res.ChangedSince, rows[i].Field)
				case undoAlready:
					res.AlreadyRestored = append(res.AlreadyRestored, rows[i].Field)
				default:
					res.Failed = append(res.Failed, rows[i].Field)
				}
			}
		})
		if lockErr != nil {
			return lockErr
		}
		// A locked field was put back by the guard: it was not reverted.
		for _, key := range restoredLocked {
			if key == database.FieldKeySeriesName {
				key = historyFieldSeries
			}
			if idx := slices.Index(res.Reverted, key); idx >= 0 {
				res.Reverted = slices.Delete(res.Reverted, idx, idx+1)
				res.Locked = append(res.Locked, key)
			}
		}
		if len(res.Reverted) == 0 {
			return database.ErrSkipBookWrite
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("undo apply %s on %s: %w", latest.BatchID, bookID, err)
	}
	if updated == nil {
		return nil, fmt.Errorf("undo apply: book %s not found", bookID)
	}

	// The book_authors join is a separate key, written after the row (kept out
	// of the callback like every other write). dropUnrevertableAuthorRows has
	// already refused an author_name row whose join is unknown or changed, so
	// the column and the join move back together.
	for _, r := range rows {
		if r.Field != historyFieldAuthor || !slices.Contains(res.Reverted, historyFieldAuthor) {
			continue
		}
		if serr := mfs.db.SetBookAuthors(bookID, r.PreviousRef.BookAuthors); serr != nil {
			applyHistoryLog.Warn("undo apply: restoring author credits of %s failed: %v", logger.SanitizeLogValue(bookID), serr)
			res.AuthorCreditsLeft = true
		}
	}

	now := time.Now()
	for _, r := range rows {
		if !slices.Contains(res.Reverted, r.Field) {
			continue
		}
		if err := mfs.db.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID:        bookID,
			Field:         r.Field,
			PreviousValue: r.NewValue,
			NewValue:      r.PreviousValue,
			PreviousRef:   r.NewRef,
			NewRef:        r.PreviousRef,
			ChangeType:    ChangeTypeApplyUndo,
			Source:        "undo-last-apply",
			ChangedAt:     now,
			BatchID:       latest.BatchID,
		}); err != nil {
			applyHistoryLog.Warn("undo apply: recording undo of %s.%s failed: %v", logger.SanitizeLogValue(bookID), r.Field, err)
		}
	}
	return res, nil
}

type undoOutcome int

const (
	undoFailed undoOutcome = iota
	undoReverted
	undoChangedSince
	undoAlready
)

func decodeHistoryValue(v *string) string {
	if v == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal([]byte(*v), &s); err != nil {
		return *v
	}
	return s
}

// undoOneField is the compare-and-set for one history row, run on the row as
// read under the book's write stripe. It only touches the struct.
func undoOneField(b *database.Book, r *database.MetadataChangeRecord) undoOutcome {
	jsonName := r.Field
	if j, ok := historyToJSON[r.Field]; ok {
		jsonName = j
	}
	if jsonName == "author_id" || jsonName == "series_id" {
		if r.PreviousRef == nil || r.NewRef == nil {
			return undoFailed
		}
		cur, prev, next := &b.AuthorID, r.PreviousRef.AuthorID, r.NewRef.AuthorID
		if jsonName == "series_id" {
			cur, prev, next = &b.SeriesID, r.PreviousRef.SeriesID, r.NewRef.SeriesID
		}
		switch {
		case eqIntPtr(*cur, next):
			*cur = copyIntPtr(prev)
			return undoReverted
		case eqIntPtr(*cur, prev):
			return undoAlready
		default:
			return undoChangedSince
		}
	}
	current, err := database.RenderBookField(b, jsonName)
	if err != nil {
		return undoFailed
	}
	prev, next := decodeHistoryValue(r.PreviousValue), decodeHistoryValue(r.NewValue)
	switch {
	case current == next && prev != next:
		if database.RestoreRenderedBookField(b, jsonName, prev) != nil {
			return undoFailed
		}
		return undoReverted
	case current == prev:
		return undoAlready
	default:
		return undoChangedSince
	}
}

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func sameAuthorIDs(a, b []database.BookAuthor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].AuthorID != b[i].AuthorID || a[i].Role != b[i].Role {
			return false
		}
	}
	return true
}

// dropUnrevertableAuthorRows removes the author_name row from rows when the
// author column cannot go back together with the book_authors join: the join
// was not recorded (BookAuthorsKnown), or it no longer holds what the apply
// left. Reverting AuthorID alone would leave the join naming the applied
// author. The field is reported Failed.
func (mfs *Service) dropUnrevertableAuthorRows(bookID string, rows []database.MetadataChangeRecord, res *UndoApplyResult) []database.MetadataChangeRecord {
	out := rows[:0:0]
	for _, r := range rows {
		if r.Field != historyFieldAuthor {
			out = append(out, r)
			continue
		}
		ok := r.PreviousRef != nil && r.NewRef != nil && r.PreviousRef.BookAuthorsKnown && r.NewRef.BookAuthorsKnown
		if ok {
			cur, err := mfs.db.GetBookAuthors(bookID)
			ok = err == nil && sameAuthorIDs(cur, r.NewRef.BookAuthors)
		}
		if !ok {
			res.authorRefused = append(res.authorRefused, r.Field)
			res.AuthorCreditsLeft = true
			continue
		}
		out = append(out, r)
	}
	return out
}

// ErrFieldChangedSince: the field no longer holds the value its last change
// wrote, so undoing that change would overwrite a later edit.
var ErrFieldChangedSince = errors.New("the field has changed since; not undone")

// ErrFieldNotUndoable: the field's last change cannot be put back on the book
// row (no history, an unknown field, or an author/series row with no ids).
var ErrFieldNotUndoable = errors.New("the field's last change cannot be undone")

// UndoFieldChange undoes the newest recorded change to one field of a book,
// the single-field sibling of UndoLastApply: a compare-and-set on the book
// row inside ModifyBook, no override written, a refusal when the field has
// changed since. It used to store the previous value as a user override and
// leave the book row holding the changed value.
func (mfs *Service) UndoFieldChange(bookID, field string) (*UndoApplyResult, error) {
	history, err := mfs.db.GetBookChangeHistory(bookID, 1<<30)
	if err != nil {
		return nil, fmt.Errorf("read change history: %w", err)
	}
	var latest *database.MetadataChangeRecord
	for i := range history {
		r := &history[i]
		if r.Field != field {
			continue
		}
		if latest == nil || r.ChangedAt.After(latest.ChangedAt) {
			latest = r
		}
	}
	if latest == nil {
		return nil, ErrNoApplyToUndo
	}
	// The newest change is an undo: the field is already back. Undoing that
	// row would put the undone value back, which a second click must not do.
	if latest.ChangeType == ChangeTypeApplyUndo {
		return nil, ErrFieldAlreadyUndone
	}
	rows := []database.MetadataChangeRecord{*latest}
	res := &UndoApplyResult{BatchID: latest.BatchID, Source: latest.Source}
	rows = mfs.dropUnrevertableAuthorRows(bookID, rows, res)
	if len(rows) == 0 {
		return res, ErrFieldNotUndoable
	}
	var outcome undoOutcome
	updated, err := mfs.db.ModifyBook(bookID, func(row *database.Book) error {
		restored, lockErr := database.ApplyRespectingLocks(mfs.db, row, func(b *database.Book) {
			outcome = undoOneField(b, &rows[0])
		})
		if lockErr != nil {
			return lockErr
		}
		if outcome == undoReverted && len(restored) > 0 {
			res.Locked = append(res.Locked, field)
			return database.ErrSkipBookWrite
		}
		if outcome != undoReverted {
			return database.ErrSkipBookWrite
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("undo %s on %s: %w", field, bookID, err)
	}
	if updated == nil {
		return nil, fmt.Errorf("undo: book %s not found", bookID)
	}
	switch {
	case len(res.Locked) > 0:
		return res, ErrFieldChangedSince
	case outcome == undoChangedSince:
		res.ChangedSince = []string{field}
		return res, ErrFieldChangedSince
	case outcome == undoAlready:
		res.AlreadyRestored = []string{field}
		res.RevertedTo = rows[0].PreviousValue
		return res, nil
	case outcome != undoReverted:
		res.Failed = []string{field}
		return res, ErrFieldNotUndoable
	}
	res.Reverted = []string{field}
	res.RevertedTo = rows[0].PreviousValue
	if field == historyFieldAuthor {
		if serr := mfs.db.SetBookAuthors(bookID, rows[0].PreviousRef.BookAuthors); serr != nil {
			applyHistoryLog.Error("undo: restoring author credits of %s failed: %v", logger.SanitizeLogValue(bookID), serr)
			res.AuthorCreditsLeft = true
		}
	}
	if err := mfs.db.RecordMetadataChange(&database.MetadataChangeRecord{
		BookID:        bookID,
		Field:         field,
		PreviousValue: latest.NewValue,
		NewValue:      latest.PreviousValue,
		PreviousRef:   latest.NewRef,
		NewRef:        latest.PreviousRef,
		ChangeType:    ChangeTypeApplyUndo,
		Source:        "manual",
		ChangedAt:     time.Now(),
	}); err != nil {
		applyHistoryLog.Warn("undo: recording undo of %s.%s failed: %v", logger.SanitizeLogValue(bookID), field, err)
	}
	return res, nil
}

// CommitApply writes an apply's changes onto the row as it stands under the
// book's write stripe (only the fields the apply changed relative to before,
// via MergeBookChanges) and then records history from the committed row. It
// is the write every apply path uses, the bulk-fetch handler included, so
// none of them replaces the whole row with a read taken before slow provider
// work.
//
// A nil book means nothing was written. A non-nil book with an error means
// the write committed and its history did not (ErrApplyHistoryIncomplete,
// logged at Error): the batch is marked incomplete, so undo refuses it.
func (mfs *Service) CommitApply(id string, before, book *database.Book, prevAuthors []database.BookAuthor, source string) (*database.Book, error) {
	var mergedFields []string
	updated, err := mfs.db.ModifyBook(id, func(fresh *database.Book) error {
		var mErr error
		mergedFields, mErr = database.MergeBookChanges(fresh, before, book)
		return mErr
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update book: %w", err)
	}
	if updated == nil {
		return nil, fmt.Errorf("update book %s: store reported success but returned no book", id)
	}
	// The new values are read from the committed row, so a value the store
	// normalised on write is recorded as stored and undo's compare-and-set
	// matches it.
	written, snapErr := database.SnapshotBook(before)
	if snapErr == nil {
		snapErr = database.CopyBookFields(written, updated, mergedFields)
	}
	if snapErr != nil {
		applyHistoryLog.Error("apply history for %s not recorded: %v", logger.SanitizeLogValue(id), snapErr)
		mfs.markApplyIncomplete(id, newApplyBatchID(), source)
		return updated, fmt.Errorf("%w: %v", ErrApplyHistoryIncomplete, snapErr)
	}
	if _, herr := mfs.RecordApplyHistory(before, written, prevAuthors, source); herr != nil {
		return updated, herr
	}
	return updated, nil
}
