// file: internal/metafetch/apply_history.go
// version: 1.11.0
// guid: 4b9d7e21-0c3a-4f58-b6e2-8a1f5d3c9e07
// last-edited: 2026-10-06

package metafetch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var applyHistoryLog = logger.New("metafetch-apply-history")

// History field names that differ from the Book JSON name: the shared
// vocabulary in internal/database (book_edit_history.go), which the user-edit
// paths record with too.
const (
	historyFieldAuthor   = database.HistoryFieldAuthor
	historyFieldSeries   = database.HistoryFieldSeries
	historyFieldSeriesNo = database.HistoryFieldSeriesNo
)

var historyToJSON = database.HistoryFieldToJSON

func jsonToHistory(jsonName string) string {
	return database.HistoryFieldName(jsonName)
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

// ChangeTypeApplyOpJournaled marks a batch written by an apply that also
// journaled its steps as operation changes (the Repairs lane, which moves and
// repoints book_file rows and retires books alongside the book fields it
// records here). Undoing only this batch's book fields would leave the rest of
// that apply in place, so UndoLastApply refuses it and points at the
// operation revert. repairs.ChangeTypeApplyOpJournaled spells the same value.
const ChangeTypeApplyOpJournaled = "apply_op_journaled"

// ErrApplyUndoneFromOperation: the book's last apply was part of an
// operation-journaled apply; undo it from its operation instead.
var ErrApplyUndoneFromOperation = errors.New("the last apply on this book was a Repairs apply; undo it from its operation (Operations page, Revert)")

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
// write that failed, is never recorded. credits is the book_authors join the
// apply read and wrote under the store's lock (nil when unknown); undo removes
// exactly the credits it added (undoAuthorCredits).
//
// Every row shares one batch id, which is returned ("" when nothing changed).
//
// A row that cannot be written is logged at Error; the batch is then marked
// incomplete (markApplyIncomplete) and ErrApplyHistoryIncomplete returned, so
// UndoLastApply refuses it instead of undoing part of it or an older apply.
func (mfs *Service) RecordApplyHistory(before, after *database.Book, credits *AuthorCredits, source string) (string, error) {
	return mfs.recordApplyHistory(before, after, credits, source, "")
}

// recordApplyHistory is RecordApplyHistory with the batch id fixed by the
// caller (ApplyOptions.BatchID); "" draws a fresh one.
func (mfs *Service) recordApplyHistory(before, after *database.Book, credits *AuthorCredits, source, fixedBatch string) (string, error) {
	if before == nil || after == nil {
		return "", nil
	}
	batchOr := func() string {
		if fixedBatch != "" {
			return fixedBatch
		}
		return newApplyBatchID()
	}
	changed, err := database.ChangedBookFields(before, after)
	if err != nil {
		batchID := batchOr()
		applyHistoryLog.Error("apply history for %s not recorded: %v", logger.SanitizeLogValue(after.ID), err)
		mfs.markApplyIncomplete(after.ID, batchID, source)
		return batchID, fmt.Errorf("%w: %v", ErrApplyHistoryIncomplete, err)
	}
	// An add-only apply (applyAuthorCredit) can add an author credit without
	// moving author_id, so the column diff alone would record nothing and the
	// added credit could never be undone. Record the author row whenever the
	// join this apply wrote differs from the join it read.
	if credits != nil && !slices.Contains(changed, "author_id") && !sameAuthorCredits(credits.Before, credits.After) {
		changed = append(changed, "author_id")
	}
	if len(changed) == 0 {
		return "", nil
	}
	batchID := batchOr()
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
			// The joins are the ones applyAuthorCredit read and wrote under
			// the store's book_authors lock, never a read taken around it: a
			// "before" read outside the lock could miss another apply's
			// credit, and undo would then remove it (#3410 review).
			known := credits != nil
			var prevJoin, newJoin []database.BookAuthor
			if known {
				prevJoin, newJoin = credits.Before, credits.After
				if oldVal == newVal {
					// Credits-only change (add-only append): show the lists.
					oldVal, newVal = mfs.authorCreditNames(prevJoin), mfs.authorCreditNames(newJoin)
				}
			}
			rec.PreviousRef = &database.MetadataChangeRef{AuthorID: before.AuthorID, BookAuthors: prevJoin, BookAuthorsKnown: known}
			rec.NewRef = &database.MetadataChangeRef{AuthorID: after.AuthorID, BookAuthors: newJoin, BookAuthorsKnown: known}
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

// AuthorCredits is the book_authors join an apply read and the join it wrote,
// both taken inside the store's ModifyBookAuthors, under the book's
// book_authors lock. History records these as the author row's refs, and undo
// removes exactly After minus Before. A nil *AuthorCredits means the join is
// unknown; undo then refuses the author row. A path that does not write the
// join records Before == After, so undoing it removes no credit.
type AuthorCredits struct {
	Before []database.BookAuthor
	After  []database.BookAuthor
}

// Changed reports whether the apply changed the credit list, by the same
// comparison RecordApplyHistory uses to record the author row. A nil receiver
// (no author applied, or the join unknown) reports false.
func (c *AuthorCredits) Changed() bool {
	return c != nil && !sameAuthorCredits(c.Before, c.After)
}

// addedAuthorIDs is the set of author ids an author row's apply added to the
// join (NewRef minus PreviousRef). Author apply is add-only, so this is the
// whole of what undoing it may take away.
func addedAuthorIDs(r *database.MetadataChangeRecord) map[int]bool {
	out := map[int]bool{}
	if r.PreviousRef == nil || r.NewRef == nil {
		return out
	}
	prev := map[int]bool{}
	for _, ba := range r.PreviousRef.BookAuthors {
		prev[ba.AuthorID] = true
	}
	for _, ba := range r.NewRef.BookAuthors {
		if !prev[ba.AuthorID] {
			out[ba.AuthorID] = true
		}
	}
	return out
}

// creditedElsewhere reports whether a live author row other than self (not an
// undo row, and not in an undone batch) also added id. Undo leaves such an id
// in place, since another change still depends on it.
func creditedElsewhere(history []database.MetadataChangeRecord, self *database.MetadataChangeRecord, id int) bool {
	undone := map[string]bool{}
	for _, h := range history {
		if h.ChangeType == ChangeTypeApplyUndo && h.BatchID != "" {
			undone[h.BatchID] = true
		}
	}
	for i := range history {
		h := &history[i]
		if h.Field != historyFieldAuthor || h.ChangeType == ChangeTypeApplyUndo {
			continue
		}
		if h.BatchID != "" && undone[h.BatchID] {
			continue
		}
		if h.BatchID == self.BatchID && h.ChangedAt.Equal(self.ChangedAt) {
			continue
		}
		if addedAuthorIDs(h)[id] {
			return true
		}
	}
	return false
}

// undoAuthorCredits removes the credits an author row's apply added, and
// nothing else, inside ModifyBookAuthors so it cannot race another credit
// write. It never overwrites the join with the row's PreviousRef: that list
// was the join at the apply's moment, and writing it back deleted every credit
// added since. A failure is logged and reported as AuthorCreditsLeft.
func (mfs *Service) undoAuthorCredits(bookID string, r *database.MetadataChangeRecord, history []database.MetadataChangeRecord, res *UndoApplyResult) {
	remove := addedAuthorIDs(r)
	for id := range remove {
		if creditedElsewhere(history, r, id) {
			delete(remove, id)
		}
	}
	if len(remove) == 0 {
		return
	}
	_, err := mfs.db.ModifyBookAuthors(bookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		kept := make([]database.BookAuthor, 0, len(cur))
		for _, ba := range cur {
			if !remove[ba.AuthorID] {
				kept = append(kept, ba)
			}
		}
		if len(kept) == len(cur) {
			return nil, database.ErrSkipBookAuthorsWrite
		}
		return kept, nil
	})
	if err != nil {
		applyHistoryLog.Error("undo: removing author credits added by %s on %s failed: %v",
			logger.SanitizeLogValue(r.BatchID), logger.SanitizeLogValue(bookID), err)
		res.AuthorCreditsLeft = true
	}
}

// sameAuthorCredits reports whether two book_authors joins credit the same
// authors in the same roles and positions.
func sameAuthorCredits(a, b []database.BookAuthor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].AuthorID != b[i].AuthorID || a[i].Role != b[i].Role || a[i].Position != b[i].Position {
			return false
		}
	}
	return true
}

// authorCreditNames renders a join as "Name1, Name2" for history display.
func (mfs *Service) authorCreditNames(credits []database.BookAuthor) string {
	names := make([]string, 0, len(credits))
	for _, ba := range credits {
		id := ba.AuthorID
		names = append(names, mfs.authorName(&id))
	}
	return strings.Join(names, ", ")
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
	// FailedReasons says why a field in Failed or ChangedSince was refused
	// when the bare outcome does not (a series row that no longer exists, a
	// position whose series was not put back).
	FailedReasons map[string]string `json:"failed_reasons,omitempty"`
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
	return mfs.undoBatch(bookID, latest.BatchID, latest.Source, history, true)
}

// UndoApplyBatch reverts the one metadata apply whose change-history rows
// carry batchID on bookID, whether or not it is the book's newest, by the same
// per-field compare-and-set UndoLastApply uses: a field still holding what the
// apply wrote is put back, one edited since is left (ChangedSince). It is the
// op revert of a Repairs apply that journaled its history batch
// (undo.ChangeTypeMetadataApply): a later apply on the book does not stop it,
// because each field is checked on its own.
//
// ErrApplyAlreadyUndone when the batch's undo rows are already recorded
// (UndoLastApply or an earlier revert undid it); ErrNoApplyToUndo when no
// history row carries batchID; ErrApplyHistoryIncomplete when the apply's
// history was not fully written. An op-journaled marker does not refuse it:
// this IS the operation's revert.
func (mfs *Service) UndoApplyBatch(bookID, batchID string) (*UndoApplyResult, error) {
	if batchID == "" {
		return nil, ErrNoApplyToUndo
	}
	history, err := mfs.db.GetBookChangeHistory(bookID, 1<<30)
	if err != nil {
		return nil, fmt.Errorf("read change history: %w", err)
	}
	source := ""
	found := false
	for i := range history {
		r := &history[i]
		if r.BatchID != batchID {
			continue
		}
		if r.ChangeType == ChangeTypeApplyUndo {
			return nil, ErrApplyAlreadyUndone
		}
		found = true
		if source == "" {
			source = r.Source
		}
	}
	if !found {
		return nil, ErrNoApplyToUndo
	}
	return mfs.undoBatch(bookID, batchID, source, history, false)
}

// undoBatch reverts the rows of batchID in history (the book's whole change
// history). refuseOpJournaled refuses a batch carrying the op-journaled
// marker, which only the operation's own revert may undo.
func (mfs *Service) undoBatch(bookID, batchID, source string, history []database.MetadataChangeRecord, refuseOpJournaled bool) (*UndoApplyResult, error) {
	latest := &database.MetadataChangeRecord{BatchID: batchID, Source: source}
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
		if r.ChangeType == ChangeTypeApplyOpJournaled {
			if refuseOpJournaled {
				return nil, ErrApplyUndoneFromOperation
			}
			continue
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
			res.FailedReasons = nil
			env := undoEnv{seriesExists: mfs.seriesExists, pair: batchSeriesRow(rows)}
			// The series link before its position: a position is put back
			// only into the series it was numbered in (undoOneField).
			for _, i := range seriesFirst(rows) {
				o, why := undoOneField(b, &rows[i], env)
				switch o {
				case undoReverted:
					res.Reverted = append(res.Reverted, rows[i].Field)
				case undoChangedSince:
					res.ChangedSince = append(res.ChangedSince, rows[i].Field)
				case undoAlready:
					res.AlreadyRestored = append(res.AlreadyRestored, rows[i].Field)
				default:
					res.Failed = append(res.Failed, rows[i].Field)
				}
				res.refusedBecause(rows[i].Field, why)
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
	// of the callback like every other write). Only the credits this apply
	// ADDED are removed, atomically (undoAuthorCredits); a credit another
	// apply or an edit added since stays. An add-only apply usually leaves
	// author_id alone, so the column counts as already restored, and the
	// credits are still undone then.
	for i := range rows {
		if rows[i].Field != historyFieldAuthor {
			continue
		}
		if !slices.Contains(res.Reverted, historyFieldAuthor) && !slices.Contains(res.AlreadyRestored, historyFieldAuthor) {
			continue
		}
		mfs.undoAuthorCredits(bookID, &rows[i], history, res)
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

// undoEnv is what undoOneField reads besides the book row: whether a series
// row exists, and the series row of the batch being undone (nil when the
// batch changed no series link).
type undoEnv struct {
	seriesExists func(id int) (bool, error)
	pair         *database.MetadataChangeRecord
}

// undoOneField is the compare-and-set for one history row, run on the row as
// read under the book's write stripe. It only touches the struct. why says
// why a row was refused when the outcome alone does not.
//
// Two rules beyond the compare-and-set keep a series undo from doing damage:
//   - a series link goes back only while its series row exists. The orphan
//     prune, a merge or a hand delete may have removed it since, and
//     restoring the id then leaves the book naming a series that does not
//     exist (the phantom-series damage, database/series_bookref.go);
//   - a series position goes back only into the series it was numbered in:
//     when the batch also changed the link (env.pair), the book must be in
//     that link's previous series by now (put back by the pair just before,
//     or already). A position restored into whatever series the book holds
//     now would number it in a series it was never numbered in.
func undoOneField(b *database.Book, r *database.MetadataChangeRecord, env undoEnv) (undoOutcome, string) {
	jsonName := r.Field
	if j, ok := historyToJSON[r.Field]; ok {
		jsonName = j
	}
	if jsonName == "author_id" || jsonName == "series_id" {
		if r.PreviousRef == nil || r.NewRef == nil {
			return undoFailed, ""
		}
		cur, prev, next := &b.AuthorID, r.PreviousRef.AuthorID, r.NewRef.AuthorID
		if jsonName == "series_id" {
			cur, prev, next = &b.SeriesID, r.PreviousRef.SeriesID, r.NewRef.SeriesID
		}
		switch {
		case eqIntPtr(*cur, next):
			if jsonName == "series_id" && prev != nil {
				if why := seriesGone(env, *prev); why != "" {
					return undoFailed, why
				}
			}
			*cur = copyIntPtr(prev)
			return undoReverted, ""
		case eqIntPtr(*cur, prev):
			return undoAlready, ""
		default:
			return undoChangedSince, ""
		}
	}
	if jsonName == "series_sequence" && env.pair != nil && env.pair.PreviousRef != nil &&
		!eqIntPtr(b.SeriesID, env.pair.PreviousRef.SeriesID) {
		return undoChangedSince, fmt.Sprintf("the book is not back in series %s, the series this position was numbered in; the position is not restored",
			intPtrText(env.pair.PreviousRef.SeriesID))
	}
	current, err := database.RenderBookField(b, jsonName)
	if err != nil {
		return undoFailed, ""
	}
	prev, next := decodeHistoryValue(r.PreviousValue), decodeHistoryValue(r.NewValue)
	switch {
	case current == next && prev != next:
		if database.RestoreRenderedBookField(b, jsonName, prev) != nil {
			return undoFailed, ""
		}
		return undoReverted, ""
	case current == prev:
		return undoAlready, ""
	default:
		return undoChangedSince, ""
	}
}

// seriesGone returns why series id cannot be linked again ("" when its row
// exists).
func seriesGone(env undoEnv, id int) string {
	if env.seriesExists == nil {
		return fmt.Sprintf("series %d cannot be checked (no series reader); the link is not restored", id)
	}
	ok, err := env.seriesExists(id)
	switch {
	case err != nil:
		return fmt.Sprintf("series %d could not be read (%v); the link is not restored", id, err)
	case !ok:
		return fmt.Sprintf("series %d no longer exists; restoring the link would leave the book naming a missing series", id)
	}
	return ""
}

// seriesExists reports whether series row id exists.
func (mfs *Service) seriesExists(id int) (bool, error) {
	s, err := mfs.db.GetSeriesByID(id)
	return s != nil, err
}

// batchSeriesRow returns the batch's series-link row, or nil.
func batchSeriesRow(rows []database.MetadataChangeRecord) *database.MetadataChangeRecord {
	for i := range rows {
		if historyJSONName(rows[i].Field) == "series_id" && rows[i].ChangeType != ChangeTypeApplyUndo {
			return &rows[i]
		}
	}
	return nil
}

// sameBatch returns the history rows of r's batch (r alone when it has none).
func sameBatch(history []database.MetadataChangeRecord, r *database.MetadataChangeRecord) []database.MetadataChangeRecord {
	if r.BatchID == "" {
		return []database.MetadataChangeRecord{*r}
	}
	var out []database.MetadataChangeRecord
	for _, h := range history {
		if h.BatchID == r.BatchID && h.ChangeType != ChangeTypeApplyUndo {
			out = append(out, h)
		}
	}
	return out
}

// seriesFirst returns rows' indexes with the series-link rows first, so the
// position rows after them see the link already put back.
func seriesFirst(rows []database.MetadataChangeRecord) []int {
	idx := make([]int, 0, len(rows))
	for i := range rows {
		if historyJSONName(rows[i].Field) == "series_id" {
			idx = append(idx, i)
		}
	}
	for i := range rows {
		if historyJSONName(rows[i].Field) != "series_id" {
			idx = append(idx, i)
		}
	}
	return idx
}

func historyJSONName(field string) string {
	if j, ok := historyToJSON[field]; ok {
		return j
	}
	return field
}

func intPtrText(p *int) string {
	if p == nil {
		return "(none)"
	}
	return strconv.Itoa(*p)
}

// refusedBecause records why field was refused, when there is a reason.
func (r *UndoApplyResult) refusedBecause(field, why string) {
	if why == "" {
		return
	}
	if r.FailedReasons == nil {
		r.FailedReasons = map[string]string{}
	}
	r.FailedReasons[field] = why
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
		// The refs must be known: they are the joins the apply read and wrote
		// under the store's lock, and undo removes exactly their difference.
		// This replaces an exact-match check of the current join against
		// NewRef. That check guarded a wholesale SetBookAuthors(PreviousRef)
		// which would have wiped any later credit. Undo now removes only this
		// apply's own additions, inside ModifyBookAuthors, so a later credit
		// survives and needs no refusal. An unreadable join still refuses.
		ok := r.PreviousRef != nil && r.NewRef != nil && r.PreviousRef.BookAuthorsKnown && r.NewRef.BookAuthorsKnown
		if ok {
			_, err := mfs.db.GetBookAuthors(bookID)
			ok = err == nil
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
	var why string
	// A position is put back only into the series it was numbered in: the
	// series row of the same batch names it.
	env := undoEnv{seriesExists: mfs.seriesExists, pair: batchSeriesRow(sameBatch(history, latest))}
	updated, err := mfs.db.ModifyBook(bookID, func(row *database.Book) error {
		restored, lockErr := database.ApplyRespectingLocks(mfs.db, row, func(b *database.Book) {
			outcome, why = undoOneField(b, &rows[0], env)
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
		res.refusedBecause(field, why)
		return res, ErrFieldChangedSince
	case outcome == undoAlready:
		res.AlreadyRestored = []string{field}
		res.RevertedTo = rows[0].PreviousValue
		if field == historyFieldAuthor {
			// An add-only apply leaves author_id alone; its credits still go.
			mfs.undoAuthorCredits(bookID, &rows[0], history, res)
		}
		return res, nil
	case outcome != undoReverted:
		res.Failed = []string{field}
		res.refusedBecause(field, why)
		return res, ErrFieldNotUndoable
	}
	res.Reverted = []string{field}
	res.RevertedTo = rows[0].PreviousValue
	if field == historyFieldAuthor {
		mfs.undoAuthorCredits(bookID, &rows[0], history, res)
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
//
// credits is the author join the apply read and wrote under the store's
// book_authors lock (guardedApply returns it); nil means unknown.
func (mfs *Service) CommitApply(id string, before, book *database.Book, credits *AuthorCredits, source string) (*database.Book, error) {
	return mfs.commitApply(id, before, book, credits, source, "", nil)
}

// commitApply is CommitApply with an optional guard run on the fresh row
// under the book's write lock, before anything is merged. A guard error
// aborts the write (nil book, the guard's error). The automatic apply uses
// it to re-check "no match": its earlier check read a row that can be
// minutes old in a bulk run, and the owner may have marked the book since.
//
// batchID fixes the history batch id (ApplyOptions.BatchID); "" draws one.
func (mfs *Service) commitApply(id string, before, book *database.Book, credits *AuthorCredits, source, batchID string, guard func(fresh *database.Book) error) (*database.Book, error) {
	var mergedFields []string
	updated, err := mfs.db.ModifyBook(id, func(fresh *database.Book) error {
		if guard != nil {
			if gErr := guard(fresh); gErr != nil {
				return gErr
			}
		}
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
		incomplete := batchID
		if incomplete == "" {
			incomplete = newApplyBatchID()
		}
		mfs.markApplyIncomplete(id, incomplete, source)
		return updated, fmt.Errorf("%w: %v", ErrApplyHistoryIncomplete, snapErr)
	}
	if _, herr := mfs.recordApplyHistory(before, written, credits, source, batchID); herr != nil {
		return updated, herr
	}
	return updated, nil
}
