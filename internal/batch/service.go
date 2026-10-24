// file: internal/batch/service.go
// version: 1.17.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-28

package batch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// batchBookStore is the slice of the book store that batch operations need:
// the book read, modify and delete methods and the narrow capabilities
// embedded below, each with the reason it is here. Was database.BookStore
// (51 methods), most of which every caller carried purely to satisfy the
// parameter.
type batchBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	// ModifyBook is the write path for every batch edit of a book: it sets
	// only the columns the action owns on the stored row under the book's
	// write lock, so a column another writer committed between the batch's
	// read and its write is kept. See database.BookMutator.
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	DeleteBook(id string) error
	// UserOverrideRecorder: a batch update is a HUMAN editing many books at
	// once. It must record a lock row per edited field, exactly as the
	// single-book edit handler does, or the next fetch or scan overwrites
	// every one of them (database.RecordUserOverrides).
	database.UserOverrideRecorder
	// The lock vocabulary's author_name/series_name lock a NAME, while this
	// package's payload carries an ID, so the id has to be resolved before a
	// lock row can be written. Storing the id under a name key would store a
	// value no reader can compare against.
	GetAuthorByID(id int) (*database.Author, error)
	GetSeriesByID(id int) (*database.Series, error)
	// RecordMetadataChange: a bulk edit records a "manual" history row for
	// every field it changed (database.RecordBookEditHistory), like the
	// single-book edit. Without it a queued apply cannot see the edit and
	// overwrites it, and the book's history never shows it.
	RecordMetadataChange(record *database.MetadataChangeRecord) error
	// A batch edit that changes a book's primary flag, deletion state or
	// version group keeps each group it touched at one primary
	// (handOffPrimary).
	versionprimary.EnsureStore
}

// TrashPurger purges books that are in the trash through the purge's own path
// (the nightly purge and the trash page's "Purge now"): the undo hold, the
// listening-state carry, the file-row refusal. *audiobooks.AudiobookService
// implements it (BeginTrashPurges).
//
// BeginTrashPurges starts ONE run of such deletions (one batch request) and
// returns its two steps:
//
//   - checkRoot makes the library-root check that deleting files needs, and
//     must be called BEFORE the merge lock is taken for the book: the check
//     samples files and stats them, which can wait on a slow or hung mount, and
//     the lock is global. The first call does the work; later ones are answered
//     from it until it ages out. It does nothing when the deployment keeps files.
//   - purgeLocked purges one book. The caller holds the merge lock
//     (merge.TryLockMergeRMWWithin) and the purge runs under that hold; it does
//     not take the lock, so calling it without the lock is a bug, and it has no
//     unlocked twin here on purpose.
//
// The steps are not safe for concurrent use; the batch runs its items one after
// another.
type TrashPurger interface {
	BeginTrashPurges() (checkRoot func() error, purgeLocked func(book *database.Book) error)
}

// trashPurges is one batch request's run of trashed-book purges. It starts the
// purger's run on first use and remembers the first root-check failure, so the
// rest of the request's trashed hard deletes fail at once with the same error
// instead of asking the library again for each one (a hung mount would
// otherwise cost a stat timeout per book).
type trashPurges struct {
	purger  TrashPurger
	began   bool
	check   func() error
	purge   func(*database.Book) error
	failure error
}

func (t *trashPurges) begin() {
	if !t.began {
		t.began = true
		t.check, t.purge = t.purger.BeginTrashPurges()
	}
}

// checkRoot is the purger's root check, outside the merge lock. A nil purger
// has nothing to check here (hardDeleteLocked refuses the book itself).
func (t *trashPurges) checkRoot() error {
	if t == nil || t.purger == nil {
		return nil
	}
	if t.failure != nil {
		return t.failure
	}
	t.begin()
	if err := t.check(); err != nil {
		t.failure = err
		return err
	}
	return nil
}

func (t *trashPurges) purgeLocked(book *database.Book) error {
	t.begin()
	return t.purge(book)
}

// BatchService handles bulk operations on audiobooks.
type BatchService struct {
	db     batchBookStore
	purger TrashPurger
}

// Option configures a BatchService.
type Option func(*BatchService)

// WithTrashPurger wires the purge path a hard delete of a trashed book takes.
// Without it a batch hard delete of a trashed book is refused.
func WithTrashPurger(p TrashPurger) Option {
	return func(bs *BatchService) { bs.purger = p }
}

func NewBatchService(db batchBookStore, opts ...Option) *BatchService {
	bs := &BatchService{db: db}
	for _, o := range opts {
		o(bs)
	}
	return bs
}

// ---------------------------------------------------------------------------
// Shared types
// ---------------------------------------------------------------------------

// BatchResult tracks the outcome of one item in a batch operation.
type BatchResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
	// Code is a stable reason the web can branch on instead of the message
	// text; empty for an error that has none (CodeRootNotReady).
	Code string `json:"code,omitempty"`
}

// CodeRootNotReady marks a trashed book's hard delete refused because the
// library root is not mounted or not answering: nothing was deleted, and it
// works again when the mount is back. Same code as the single-book DELETE's 503.
const CodeRootNotReady = "ROOT_NOT_READY"

// rootNotReady is what the library-root check's error satisfies
// (audiobooks.ErrLibraryRootNotReady): the batch package cannot import
// audiobooks, so it asks the error rather than naming the sentinel.
type rootNotReady interface{ LibraryRootNotReady() bool }

// BatchResponse is the standard response for all batch operations.
type BatchResponse struct {
	Results []BatchResult `json:"results"`
	Success int           `json:"success"`
	Failed  int           `json:"failed"`
	Total   int           `json:"total"`
}

func newBatchResponse(total int) *BatchResponse {
	return &BatchResponse{Results: []BatchResult{}, Total: total}
}

func (r *BatchResponse) addSuccess(id string) {
	r.Results = append(r.Results, BatchResult{ID: id, Success: true})
	r.Success++
}

func (r *BatchResponse) addError(id, msg string) {
	r.addErrorCode(id, msg, "")
}

func (r *BatchResponse) addErrorCode(id, msg, code string) {
	r.Results = append(r.Results, BatchResult{ID: id, Error: msg, Code: code})
	r.Failed++
}

// ---------------------------------------------------------------------------
// Batch Update — same updates applied to all listed IDs
// ---------------------------------------------------------------------------

// BatchUpdateRequest applies the same set of updates to every listed book.
type BatchUpdateRequest struct {
	IDs     []string       `json:"ids"`
	Updates map[string]any `json:"updates"`
	// Caller is what the HTTP caller may do to books' place in the library,
	// set by the handler (see TrashGrants); never read from the body.
	Caller *TrashGrants `json:"-"`
}

// Legacy alias for backward compat in tests
type BatchUpdateResult = BatchResult
type BatchUpdateResponse = BatchResponse

// newMergeGate returns the batch's bounded take of the merge lock, which a
// restore and a hard delete run under and which a running combine holds for its
// whole run. It is a synchronous request, so the first item that finds the lock
// busy for merge.InteractiveMergeLockWait fails with merge.ErrMergeBusy, and
// once one has, the rest of the batch's lock-taking items fail at once with the
// same error instead of waiting again: the request costs one bounded wait
// however many items it holds. An item that gets the lock keeps it (the
// returned unlock) for its whole action, which runs through the ...Locked
// functions, so a combine that starts after the wait cannot hang the request
// either. The batch runs its items one after another, so the gate is not safe
// for concurrent use and does not need to be.
func newMergeGate() func() (unlock func(), err error) {
	busy := false
	return func() (func(), error) {
		if busy {
			return nil, merge.ErrMergeBusy
		}
		unlock, err := merge.TryLockMergeRMWWithin(merge.InteractiveMergeLockWait)
		if err != nil {
			busy = true
			return nil, err
		}
		return unlock, nil
	}
}

func (bs *BatchService) UpdateAudiobooks(req *BatchUpdateRequest) *BatchResponse {
	resp := newBatchResponse(len(req.IDs))
	if len(req.IDs) == 0 {
		return resp
	}
	mergeGate := newMergeGate()
	for _, id := range req.IDs {
		book, err := bs.db.GetBookByID(id)
		if err != nil || book == nil {
			resp.addError(id, "not found")
			continue
		}
		// pre is the stored row as this edit read it, under the book's write
		// lock: the history diff must not credit this edit with a change
		// another writer committed after GetBookByID above. applyUpdates
		// replaces fields rather than writing through their pointers, so a
		// shallow copy keeps the old values.
		var pre database.Book
		var updated *database.Book
		if isRestore(req.Updates) {
			if gerr := req.Caller.checkRestore(book); gerr != nil {
				resp.addError(id, gerr.Error())
				continue
			}
			// marked_for_deletion=false is a restore like the "restore"
			// action: the shared rule (merge.RestoreFromTrashLocked) runs first, in
			// the same write as the rest of the payload. The payload's
			// hand-off runs inside it too, before it decides the restored
			// row's merge redirect, so it is not repeated below.
			unlock, gerr := mergeGate()
			if gerr != nil {
				resp.addError(id, gerr.Error())
				continue
			}
			var res merge.TrashRestoreResult
			func() {
				// Released by defer: a panic in the restore must not leave the
				// merge lock held for every later merge and purge.
				defer unlock()
				res, err = merge.RestoreFromTrashLocked(bs.db, id, func(b *database.Book) { applyUpdates(b, req.Updates) },
					req.Caller.restoreJudge(req.Updates),
					func(_, after *database.Book) { bs.handOffPrimary(book, after, req.Updates) }, joinGroups(req.Updates)...)
			}()
			pre, updated = res.Before, res.Book
		} else {
			updated, book, err = bs.lockedModify(id, book, req.Updates, func(b *database.Book) error {
				if len(req.Updates) == 0 {
					return database.ErrSkipBookWrite
				}
				pre = *b
				applyUpdates(b, req.Updates)
				// Judged on the row as locked, after the payload: a
				// library_state edit that takes a trashed book out of the
				// trash is a restore whatever the request looked like.
				return req.Caller.checkRow(&pre, b, req.Updates)
			})
		}
		if err != nil {
			resp.addError(id, err.Error())
			continue
		}
		if updated == nil {
			resp.addError(id, "not found")
			continue
		}
		if !isRestore(req.Updates) {
			bs.handOffPrimary(book, updated, req.Updates)
		}
		var problems []string
		if pre.ID != "" {
			if _, herr := database.RecordBookEditHistory(bs.db, &pre, updated,
				database.ChangeTypeManual, "manual", time.Now(), nil); herr != nil {
				batchLog.Error("batch: %v", herr)
				problems = append(problems, "the edit was saved but its change history was not fully recorded: "+herr.Error())
			}
		}
		if err := bs.recordUserLocks(id, req.Updates); err != nil {
			problems = append(problems, err.Error())
		}
		if len(problems) > 0 {
			resp.addError(id, strings.Join(problems, "; "))
			continue
		}
		resp.addSuccess(id)
	}
	return resp
}

// ---------------------------------------------------------------------------
// Batch Operations — per-item different operations
// ---------------------------------------------------------------------------

// BatchOperationItem describes one operation to perform on one book.
type BatchOperationItem struct {
	ID         string         `json:"id"`
	Action     string         `json:"action"`                // "update", "delete", "restore"
	Updates    map[string]any `json:"updates,omitempty"`     // for action=update
	HardDelete bool           `json:"hard_delete,omitempty"` // for action=delete
}

// BatchOperationsRequest allows different operations per item.
type BatchOperationsRequest struct {
	Operations []BatchOperationItem `json:"operations"`
	// Caller is what the HTTP caller may do to books' place in the library,
	// set by the handler (see TrashGrants); never read from the body.
	Caller *TrashGrants `json:"-"`
}

func (bs *BatchService) ExecuteOperations(req *BatchOperationsRequest) *BatchResponse {
	resp := newBatchResponse(len(req.Operations))
	// A restore and a hard delete run under the merge lock: see newMergeGate.
	mergeGate := newMergeGate()
	purges := &trashPurges{purger: bs.purger}
	for _, op := range req.Operations {
		switch op.Action {
		case "update":
			book, err := bs.db.GetBookByID(op.ID)
			if err != nil || book == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			var updated *database.Book
			if isRestore(op.Updates) {
				if gerr := req.Caller.checkRestore(book); gerr != nil {
					resp.addError(op.ID, gerr.Error())
					continue
				}
				unlock, gerr := mergeGate()
				if gerr != nil {
					resp.addError(op.ID, gerr.Error())
					continue
				}
				var res merge.TrashRestoreResult
				func() {
					defer unlock() // a panic in the restore must not strand the merge lock
					res, err = merge.RestoreFromTrashLocked(bs.db, op.ID, func(b *database.Book) { applyUpdates(b, op.Updates) },
						req.Caller.restoreJudge(op.Updates),
						func(_, after *database.Book) { bs.handOffPrimary(book, after, op.Updates) }, joinGroups(op.Updates)...)
				}()
				updated = res.Book
			} else {
				updated, book, err = bs.lockedModify(op.ID, book, op.Updates, func(b *database.Book) error {
					if len(op.Updates) == 0 {
						return database.ErrSkipBookWrite
					}
					before := *b
					applyUpdates(b, op.Updates)
					return req.Caller.checkRow(&before, b, op.Updates)
				})
			}
			if err != nil {
				resp.addError(op.ID, err.Error())
				continue
			}
			if updated == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			if !isRestore(op.Updates) {
				bs.handOffPrimary(book, updated, op.Updates)
			}
			if err := bs.recordUserLocks(op.ID, op.Updates); err != nil {
				resp.addError(op.ID, err.Error())
				continue
			}
			resp.addSuccess(op.ID)

		case "delete":
			// Judged before the book is read: a soft or hard delete moves the
			// book into the trash or removes it, and needs library.delete.
			action := "move the book to the trash"
			if op.HardDelete {
				action = "permanently delete the book"
			}
			if gerr := req.Caller.checkTrash(action); gerr != nil {
				resp.addError(op.ID, gerr.Error())
				continue
			}
			book, err := bs.db.GetBookByID(op.ID)
			if err != nil || book == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			if op.HardDelete {
				// A trashed book is purged with its files per the setting, and the
				// library root is checked BEFORE the merge lock (the check stats
				// files, the lock is global); one failed check fails the rest of
				// the request's trashed deletes without asking again.
				if database.IsInTrash(book) {
					if rootErr := purges.checkRoot(); rootErr != nil {
						msg := fmt.Sprintf("purge %s refused, nothing deleted: %v", op.ID, rootErr)
						var nr rootNotReady
						if errors.As(rootErr, &nr) && nr.LibraryRootNotReady() {
							resp.addErrorCode(op.ID, msg, CodeRootNotReady)
						} else {
							resp.addError(op.ID, msg)
						}
						continue
					}
				} else if oerr := database.CheckBookOwnsNoFiles(bs.db, book.ID, "hard delete"); oerr != nil {
					// A live book that owns file rows is refused before it takes
					// the merge lock, and again under it (hardDeleteLocked), where
					// a merge can no longer move a file onto it before the carry.
					resp.addError(op.ID, oerr.Error())
					continue
				}
				unlock, gerr := mergeGate()
				if gerr != nil {
					resp.addError(op.ID, gerr.Error())
					continue
				}
				var err error
				func() {
					defer unlock() // a panic in the delete must not strand the merge lock
					err = bs.hardDeleteLocked(book.ID, purges)
				}()
				if err != nil {
					resp.addError(op.ID, err.Error())
				} else {
					resp.addSuccess(op.ID)
				}
			} else {
				// Soft delete: only the two deletion columns change.
				updated, err := bs.db.ModifyBook(op.ID, func(b *database.Book) error {
					if b.MarkedForDeletion != nil && *b.MarkedForDeletion && b.MarkedForDeletionAt != nil {
						return database.ErrSkipBookWrite
					}
					marked := true
					now := time.Now()
					b.MarkedForDeletion = &marked
					b.MarkedForDeletionAt = &now
					return nil
				})
				switch {
				case err != nil:
					resp.addError(op.ID, err.Error())
				case updated == nil:
					resp.addError(op.ID, "not found")
				default:
					bs.handOffPrimary(book, updated, map[string]any{"marked_for_deletion": true})
					resp.addSuccess(op.ID)
				}
			}

		case "restore":
			book, err := bs.db.GetBookByID(op.ID)
			if err != nil || book == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			// Restore: the shared rule (merge.RestoreFromTrashLocked), the same as
			// the single-book restore. Until 2026-10-01 this cleared only the
			// two deletion columns, so a book DeleteAudiobook had labelled
			// "deleted" came back live but still labelled "deleted": hidden
			// from ABS, and refused by a second DeleteAudiobook as "already
			// soft deleted". A row that is not in the trash (database.IsInTrash:
			// a nil or false flag and no trash label) is left alone; until the
			// review of #3649 only an explicit false was, so a live nil-flag
			// primary went through the restore and yielded its flag.
			// It hands off the group's primary itself, before it decides the
			// restored row's merge redirect.
			if gerr := req.Caller.checkRestore(book); gerr != nil {
				resp.addError(op.ID, gerr.Error())
				continue
			}
			unlock, gerr := mergeGate()
			if gerr != nil {
				resp.addError(op.ID, gerr.Error())
				continue
			}
			var res merge.TrashRestoreResult
			func() {
				defer unlock() // a panic in the restore must not strand the merge lock
				res, err = merge.RestoreFromTrashLocked(bs.db, op.ID, nil, req.Caller.restoreJudge(nil), nil)
			}()
			switch {
			case err != nil:
				resp.addError(op.ID, err.Error())
			case res.Book == nil:
				resp.addError(op.ID, "not found")
			default:
				resp.addSuccess(op.ID)
			}

		default:
			resp.addError(op.ID, fmt.Sprintf("unknown action: %s", op.Action))
		}
	}
	return resp
}

// ---------------------------------------------------------------------------
// applyUpdates — maps JSON fields to Book struct fields
// ---------------------------------------------------------------------------

// batchKeyToLockField maps this package's update-payload keys onto the shared
// lock vocabulary. It is spelled out rather than derived because the two
// vocabularies genuinely differ (series_sequence here, series_position there)
// and because most payload keys are NOT lockable -- file_path, library_state,
// version_group_id and the rest are plumbing, not user metadata, and a lock row
// under one of those keys would be read by no guard.
//
// author_id/series_id are absent from this table because they need a lookup,
// not a copy: applyUpdates writes an ID and the vocabulary locks a NAME.
// recordUserLocks below resolves them separately.
var batchKeyToLockField = map[string]string{
	"title":                  database.FieldKeyTitle,
	"narrator":               database.FieldKeyNarrator,
	"publisher":              database.FieldKeyPublisher,
	"language":               database.FieldKeyLanguage,
	"description":            database.FieldKeyDescription,
	"audiobook_release_year": database.FieldKeyAudiobookReleaseYear,
	"series_sequence":        database.FieldKeySeriesPosition,
}

// recordUserLocks writes a lock row for each lockable field this batch update
// actually set, reading the value back off the payload so the stored override
// is what the user asked for.
func (bs *BatchService) recordUserLocks(bookID string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	userSet := map[string]any{}
	for payloadKey, lockField := range batchKeyToLockField {
		raw, ok := updates[payloadKey]
		if !ok || raw == nil {
			// A nil value (an explicit `"series_id": null`, i.e. "clear this")
			// records no lock. That deliberately matches the single-book path
			// in audiobooks.service_mutation, whose extractors also return
			// ok=false for a nil column, so a cleared field is left unlocked
			// by both. Diverging here would make a bulk clear lock a field the
			// same edit on one book does not.
			continue
		}
		switch v := raw.(type) {
		case string:
			userSet[lockField] = v
		case float64:
			// JSON numbers arrive as float64; the two numeric fields are ints.
			userSet[lockField] = int(v)
		case int:
			userSet[lockField] = v
		}
	}
	// author_id / series_id: resolve to the name the vocabulary locks. A
	// lookup failure is reported rather than skipped -- silently dropping the
	// lock is the exact defect this function exists to close.
	if v, ok := updates["author_id"].(float64); ok {
		author, err := bs.db.GetAuthorByID(int(v))
		if err != nil {
			return fmt.Errorf("resolve author %d for lock row: %w", int(v), err)
		}
		if author != nil {
			userSet[database.FieldKeyAuthorName] = author.Name
		}
	}
	if v, ok := updates["series_id"].(float64); ok {
		series, err := bs.db.GetSeriesByID(int(v))
		if err != nil {
			return fmt.Errorf("resolve series %d for lock row: %w", int(v), err)
		}
		if series != nil {
			userSet[database.FieldKeySeriesName] = series.Name
		}
	}
	return database.RecordUserOverrides(bs.db, bookID, userSet)
}

// isRestore reports whether a batch payload sets marked_for_deletion=false,
// which is a restore like the "restore" action.
func isRestore(updates map[string]any) bool {
	v, ok := updates["marked_for_deletion"].(bool)
	return ok && !v
}

func applyUpdates(book *database.Book, updates map[string]any) {
	if updates == nil {
		return
	}

	if v, ok := updates["title"].(string); ok {
		book.Title = v
	}
	if v, ok := updates["format"].(string); ok {
		book.Format = v
	}
	if v, ok := updates["author_id"].(float64); ok {
		aid := int(v)
		book.AuthorID = &aid
	}
	if v, ok := updates["series_id"].(float64); ok {
		sid := int(v)
		book.SeriesID = &sid
	}
	// Only an EXPLICIT null clears the series. A request that does not name
	// series_id must leave it alone: the map lookup reads an absent key as
	// nil too, and until 2026-09-14 every batch update that omitted the
	// field silently unlinked the book from its series.
	if v, present := updates["series_id"]; present && v == nil {
		book.SeriesID = nil
	}
	if v, ok := updates["series_sequence"].(float64); ok {
		seq := int(v)
		book.SeriesSequence = &seq
	}
	if v, ok := updates["version_group_id"].(string); ok {
		book.VersionGroupID = &v
	}
	if v, ok := updates["is_primary_version"].(bool); ok {
		book.IsPrimaryVersion = &v
	}
	if v, ok := updates["narrator"].(string); ok {
		book.Narrator = &v
	}
	if v, ok := updates["publisher"].(string); ok {
		book.Publisher = &v
	}
	if v, ok := updates["language"].(string); ok {
		book.Language = &v
	}
	if v, ok := updates["description"].(string); ok {
		book.Description = &v
	}
	if v, ok := updates["audiobook_release_year"].(float64); ok {
		year := int(v)
		book.AudiobookReleaseYear = &year
	}
	if v, ok := updates["marked_for_deletion"].(bool); ok {
		book.MarkedForDeletion = &v
		if v {
			now := time.Now()
			book.MarkedForDeletionAt = &now
		} else {
			book.MarkedForDeletionAt = nil
		}
	}
	if v, ok := updates["version_notes"].(string); ok {
		book.VersionNotes = &v
	}
	if v, ok := updates["file_path"].(string); ok {
		book.FilePath = v
	}
	if v, ok := updates["library_state"].(string); ok {
		// The trash label overwrites the state; remember it for a restore,
		// as the other two writers of the label do.
		if strings.EqualFold(strings.TrimSpace(v), "deleted") {
			database.RememberLibraryStateBeforeTrash(book)
		}
		book.LibraryState = &v
	}
}

var batchLog = logger.New("batch")

// joinGroups is the version group a batch payload moves its book into, for
// merge.RestoreFromTrashLocked to lock beside the row's current one; none when the
// payload leaves the group alone.
func joinGroups(updates map[string]any) []string {
	if v, ok := updates["version_group_id"].(string); ok {
		return []string{v}
	}
	return nil
}

// lockedModify writes one batch edit through ModifyBook. An edit that sets
// version_group_id or is_primary_version changes a group's membership or its
// primary, so it holds the hand-off locks of the group the book is in and
// the group it is set to (versionprimary.LockGroups) across the write: a
// reader holding one of those groups' locks (itunes.regroup's apply-time
// recheck, a hand-off) sees no member leave, join or change its flag until
// it releases. The locks are released before the caller's handOffPrimary,
// which takes them itself (they are not reentrant).
//
// before is the row the caller read; its group is the one locked, and the
// write refuses (versionprimary.CheckMembership) if the book has changed
// group since. That is retried from a fresh read, at most three times. It
// returns the written row and the row the write was locked from, which the
// caller's hand-off must use as "before".
//
// Not for a restore: merge.RestoreFromTrashLocked takes the merge lock and then
// the group locks itself (passed joinGroups), and taking a group lock first
// would invert that.
func (bs *BatchService) lockedModify(id string, before *database.Book, updates map[string]any, fn func(*database.Book) error) (*database.Book, *database.Book, error) {
	_, grp := updates["version_group_id"]
	_, flag := updates["is_primary_version"]
	if !grp && !flag {
		updated, err := bs.db.ModifyBook(id, fn)
		return updated, before, err
	}
	for attempt := 1; ; attempt++ {
		oldG := groupOf(before)
		newG := oldG
		if v, ok := updates["version_group_id"].(string); ok {
			newG = v
		}
		unlock := versionprimary.LockGroups(oldG, newG)
		updated, err := bs.db.ModifyBook(id, func(b *database.Book) error {
			if err := versionprimary.CheckMembership(b, oldG); err != nil {
				return err
			}
			return fn(b)
		})
		unlock()
		if !errors.Is(err, versionprimary.ErrMembershipChanged) || attempt == 3 {
			return updated, before, err
		}
		fresh, gerr := bs.db.GetBookByID(id)
		if gerr != nil {
			return nil, before, fmt.Errorf("re-read %s after its version group changed: %w", id, gerr)
		}
		if fresh == nil {
			return nil, before, nil
		}
		before = fresh
	}
}

// handOffPrimary keeps every version group a batch edit touched at exactly
// one live primary. before is the row read before the write, after the row
// as written; updates names the fields the edit set.
//
//   - is_primary_version=true on a live book is the user's explicit choice:
//     versionprimary.Crown makes it the primary and demotes the rest (until
//     2026-09-24 it was written beside the incumbent, leaving two).
//   - is_primary_version=false on a live book is also the user's choice, and
//     a lock row records it: its group is NOT re-elected, because Elect
//     could crown the same book again. The group may be left with no
//     primary; the user picks one, or version-group-primary-repair does.
//   - A change to the deletion mark or the group runs
//     versionprimary.EnsureSinglePrimary on the book's group, so a deleted
//     primary is handed on.
//   - The group the book LEFT is always re-checked.
//
// Best-effort: the edit itself has committed; a failed hand-off is logged
// and left for version-group-primary-repair.
func (bs *BatchService) handOffPrimary(before, after *database.Book, updates map[string]any) {
	_, flag := updates["is_primary_version"]
	_, del := updates["marked_for_deletion"]
	_, grp := updates["version_group_id"]
	if !flag && !del && !grp {
		return
	}
	oldG, newG := groupOf(before), groupOf(after)
	env := versionprimary.Env{RootDir: config.AppConfig.RootDir}
	if v, ok := updates["is_primary_version"].(bool); ok && v && newG != "" && !after.IsSoftDeleted() {
		if _, err := versionprimary.Crown(bs.db, newG, after.ID); err != nil {
			batchLog.Warn("batch: crowning %s in version group %s failed: %v",
				logger.SanitizeLogValue(after.ID), logger.SanitizeLogValue(newG), err)
		}
	} else if v, ok := updates["is_primary_version"].(bool); ok && !v && !after.IsSoftDeleted() {
		// Explicit demotion: leave newG as the user set it.
	} else if newG != "" {
		if _, err := versionprimary.EnsureSinglePrimary(context.Background(), bs.db, newG, env); err != nil {
			batchLog.Warn("batch: primary hand-off in version group %s failed: %v", logger.SanitizeLogValue(newG), err)
		}
	}
	if oldG != "" && oldG != newG {
		if _, err := versionprimary.EnsureSinglePrimary(context.Background(), bs.db, oldG, env); err != nil {
			batchLog.Warn("batch: primary hand-off in version group %s failed: %v", logger.SanitizeLogValue(oldG), err)
		}
	}
}

func groupOf(b *database.Book) string {
	if b == nil || b.VersionGroupID == nil {
		return ""
	}
	return *b.VersionGroupID
}

// hardDeleteLocked is the batch's hard delete of one book. The caller holds
// the merge lock (newMergeGate), and the whole delete runs under that hold.
// Like the single-book delete and the trash page's "Purge now" (owner decision
// 2026-10-05), a book users have listening state on (positions, status,
// progress, listened time, hide, bookmarks) is deleted only after that state is
// carried to a version Audiobookshelf lists, and is refused otherwise
// (merge.HardDeleteKeepingUserStateLocked: merge.ErrBookHasListeningState). A
// store that cannot check or carry the state refuses every hard delete.
//
// A book that still owns book_file rows is refused FIRST, before anything is
// read for or written to its users' state: DeleteBook never deletes those rows
// and refuses (database.ErrBookOwnsFiles), but it does so after the carry, so
// until 2026-10-25 a batch hard delete of such a book moved its users'
// progress to another version and then left the book standing, its state
// duplicated or gone from it. The check is database.CheckBookOwnsNoFiles, the
// question DeleteBook asks (retired rows included), made twice: once here, so
// a book that is refused never takes the global merge lock, and again as the
// precheck under the merge lock the carry and the delete run in, where a merge
// can no longer move a file onto the book before the carry starts. The store's
// own check inside DeleteBook stays the last line: a book_file writer that is
// not a merge does not take that lock.
//
// The book is read again here, under the lock: the row the caller read came
// from before it, and a restore or a merge revert (both take the lock) may have
// moved it in or out of the trash since, which decides the path below.
//
// The batch runs its operations one after another, and so this: each carry
// holds the global merge lock for its whole check-carry-delete, so a worker
// pool would only queue on that lock.
//
// A book that is in the trash is not deleted here at all: it goes through the
// purge's own path (TrashPurger), exactly as the single delete does, so a
// combine's shell, a maintenance operation's soft-deleted shell and an
// auto-merge's loser (undoHold) are refused instead of hard-deleted out from
// under an undo that can still run. Without a wired purger the book is refused.
//
// The library-root check a purge with files needs has been made by the caller,
// outside the lock (trashPurges.checkRoot), when the book it read before the
// lock was in the trash. A book that moved into the trash after that read is
// purged here without it and the purge checks the root itself, under the lock:
// rare, and still bounded by the stat timeout.
func (bs *BatchService) hardDeleteLocked(id string, purges *trashPurges) error {
	book, err := bs.db.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	if book == nil {
		return errors.New("not found")
	}
	if database.IsInTrash(book) {
		if bs.purger == nil {
			return errors.New("hard delete of a trashed book refused: the purge path is not wired, so an undo that still needs the book cannot be checked")
		}
		return purges.purgeLocked(book)
	}
	// A retire shell owns no live row, so the owned-files check below passes
	// it; deleting it would drop the record of where its retired versions came
	// from. A trashed shell is refused by the purge above (purgeGate); this is
	// the same refusal for a shell that is not in the trash.
	if err := merge.RefuseRetiredShell(bs.db, id, "hard delete"); err != nil {
		return err
	}
	merger, ok := database.AsCapability[merge.UserProgressMerger](bs.db)
	if !ok {
		return errors.New("hard delete refused: the store cannot check or carry users' listening state")
	}
	_, err = merge.HardDeleteKeepingUserStateLocked(merger, bs.db, book,
		func() error {
			if err := database.CheckBookOwnsNoFiles(bs.db, book.ID, "hard delete"); err != nil {
				return err
			}
			// Under the merge lock the retire repair also takes: a shell
			// retired since the check above is caught before the carry.
			return merge.RefuseRetiredShell(bs.db, book.ID, "hard delete")
		},
		func() error { return bs.db.DeleteBook(book.ID) })
	if err == nil {
		return nil
	}
	if errors.Is(err, merge.ErrBookHasListeningState) {
		return fmt.Errorf("%w; soft-delete it and purge it from the trash instead, where the progress can be moved or discarded on purpose", err)
	}
	if cause := merge.PrecheckCause(err); cause != nil {
		// Refused under the merge lock with nothing moved (the files check, or
		// the carry target no longer listed): report the precheck's own words,
		// as the first check would, not the merge package's wrapper.
		return cause
	}
	// Includes a late refusal after the carry (merge.ErrStateMovedBookKept: a
	// file row landed from a writer that does not take the merge lock): its
	// text says the state was moved and where.
	return err
}
