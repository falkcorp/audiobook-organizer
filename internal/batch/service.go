// file: internal/batch/service.go
// version: 1.2.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-02

package batch

import (
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// batchBookStore is the three-method slice of the book store that batch
// operations need. Was database.BookStore (51 methods) — the other 48 were
// carried by every caller purely to satisfy the parameter.
type batchBookStore interface {
	GetBookByID(id string) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
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
}

// BatchService handles bulk operations on audiobooks.
type BatchService struct {
	db batchBookStore
}

func NewBatchService(db batchBookStore) *BatchService {
	return &BatchService{db: db}
}

// ---------------------------------------------------------------------------
// Shared types
// ---------------------------------------------------------------------------

// BatchResult tracks the outcome of one item in a batch operation.
type BatchResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
}

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
	r.Results = append(r.Results, BatchResult{ID: id, Error: msg})
	r.Failed++
}

// ---------------------------------------------------------------------------
// Batch Update — same updates applied to all listed IDs
// ---------------------------------------------------------------------------

// BatchUpdateRequest applies the same set of updates to every listed book.
type BatchUpdateRequest struct {
	IDs     []string       `json:"ids"`
	Updates map[string]any `json:"updates"`
}

// Legacy alias for backward compat in tests
type BatchUpdateResult = BatchResult
type BatchUpdateResponse = BatchResponse

func (bs *BatchService) UpdateAudiobooks(req *BatchUpdateRequest) *BatchResponse {
	resp := newBatchResponse(len(req.IDs))
	if len(req.IDs) == 0 {
		return resp
	}
	for _, id := range req.IDs {
		book, err := bs.db.GetBookByID(id)
		if err != nil || book == nil {
			resp.addError(id, "not found")
			continue
		}
		applyUpdates(book, req.Updates)
		if _, err := bs.db.UpdateBook(id, book); err != nil {
			resp.addError(id, err.Error())
			continue
		}
		if err := bs.recordUserLocks(id, req.Updates); err != nil {
			resp.addError(id, err.Error())
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
}

func (bs *BatchService) ExecuteOperations(req *BatchOperationsRequest) *BatchResponse {
	resp := newBatchResponse(len(req.Operations))
	for _, op := range req.Operations {
		switch op.Action {
		case "update":
			book, err := bs.db.GetBookByID(op.ID)
			if err != nil || book == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			applyUpdates(book, op.Updates)
			if _, err := bs.db.UpdateBook(op.ID, book); err != nil {
				resp.addError(op.ID, err.Error())
				continue
			}
			if err := bs.recordUserLocks(op.ID, op.Updates); err != nil {
				resp.addError(op.ID, err.Error())
				continue
			}
			resp.addSuccess(op.ID)

		case "delete":
			book, err := bs.db.GetBookByID(op.ID)
			if err != nil || book == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			if op.HardDelete {
				if err := bs.db.DeleteBook(op.ID); err != nil {
					resp.addError(op.ID, err.Error())
				} else {
					resp.addSuccess(op.ID)
				}
			} else {
				// Soft delete
				marked := true
				book.MarkedForDeletion = &marked
				now := time.Now()
				book.MarkedForDeletionAt = &now
				if _, err := bs.db.UpdateBook(op.ID, book); err != nil {
					resp.addError(op.ID, err.Error())
				} else {
					resp.addSuccess(op.ID)
				}
			}

		case "restore":
			book, err := bs.db.GetBookByID(op.ID)
			if err != nil || book == nil {
				resp.addError(op.ID, "not found")
				continue
			}
			notMarked := false
			book.MarkedForDeletion = &notMarked
			book.MarkedForDeletionAt = nil
			if _, err := bs.db.UpdateBook(op.ID, book); err != nil {
				resp.addError(op.ID, err.Error())
			} else {
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
	if updates["series_id"] == nil {
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
		book.LibraryState = &v
	}
}
