// file: internal/audiobooks/service_tags.go
// version: 1.0.0
// guid: f8c2a7b6-c9d0-1e23-df4a-5b6c7d8e9f0a
// last-edited: 2026-06-23

package audiobooks

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ListAllUserTags returns all unique user tags with usage counts.
func (svc *AudiobookService) ListAllUserTags() ([]database.TagWithCount, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return svc.store.ListAllTags()
}

// GetBookUserTags returns all user tags for a specific book.
func (svc *AudiobookService) GetBookUserTags(bookID string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return svc.store.GetBookTags(bookID)
}

// SetBookUserTags replaces all user tags on a book and returns the new set.
func (svc *AudiobookService) SetBookUserTags(bookID string, tags []string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := svc.store.SetBookTags(bookID, tags); err != nil {
		return nil, err
	}
	svc.InvalidateBookCaches()
	return svc.store.GetBookTags(bookID)
}

// AddBookUserTag adds a single user tag to a book and returns all tags.
func (svc *AudiobookService) AddBookUserTag(bookID, tag string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := svc.store.AddBookTag(bookID, tag); err != nil {
		return nil, err
	}
	svc.InvalidateBookCaches()
	return svc.store.GetBookTags(bookID)
}

// RemoveBookUserTag removes a single user tag from a book and returns remaining tags.
func (svc *AudiobookService) RemoveBookUserTag(bookID, tag string) ([]string, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	if err := svc.store.RemoveBookTag(bookID, tag); err != nil {
		return nil, err
	}
	svc.InvalidateBookCaches()
	return svc.store.GetBookTags(bookID)
}

// BatchUpdateUserTags applies tag additions and removals to multiple books.
// Returns the number of books successfully updated.
func (svc *AudiobookService) BatchUpdateUserTags(bookIDs []string, addTags []string, removeTags []string) (int, error) {
	if svc.store == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	updated := 0
	for _, bookID := range bookIDs {
		for _, tag := range addTags {
			if err := svc.store.AddBookTag(bookID, tag); err != nil {
				slog.Warn("BatchUpdateUserTags failed to add tag to book", "tag", tag, "bookID", bookID, "err", err)
				continue
			}
		}
		for _, tag := range removeTags {
			if err := svc.store.RemoveBookTag(bookID, tag); err != nil {
				slog.Warn("BatchUpdateUserTags failed to remove tag from book", "tag", tag, "bookID", bookID, "err", err)
				continue
			}
		}
		updated++
	}
	if updated > 0 {
		svc.InvalidateBookCaches()
	}
	return updated, nil
}
