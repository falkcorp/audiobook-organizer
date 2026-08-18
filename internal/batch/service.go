// file: internal/batch/service.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-18

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
		} else {
			resp.addSuccess(id)
		}
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
			} else {
				resp.addSuccess(op.ID)
			}

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
