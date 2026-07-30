// file: internal/database/pebble_store_chapters.go
// version: 1.0.0
// guid: 0f6522aa-ef55-433a-a7b2-73cc1c898134
// last-edited: 2026-07-30

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// Chapter is a single navigable chapter in a book's playback timeline,
// persisted per-book in Pebble. Mirrors the four fields a real
// Audiobookshelf 2.36.0 server requires verbatim
// (docs/specs/2026-07-29-abs-sync-api-design.md §1.8.5 item 7): chapters[]
// needs id:Int, start, end, title, with id being an Int index while every
// other ABS id is a String. ID 0 is a valid first chapter, so it is a plain
// int, never a pointer/omitempty.
type Chapter struct {
	ID       int     `json:"id"`
	StartSec float64 `json:"start_sec"`
	EndSec   float64 `json:"end_sec"`
	Title    string  `json:"title"`
}

// chaptersKeyPrefix is the prefix every per-book chapters key shares.
const chaptersKeyPrefix = "chapters:"

func chaptersKey(bookID string) []byte {
	return []byte(chaptersKeyPrefix + bookID)
}

// GetChaptersForBook reads the ordered chapter list for bookID, or returns
// (nil, nil) when no chapters have ever been saved for it. Order is exactly
// as written by SaveChaptersForBook -- this method never re-sorts.
func (p *PebbleStore) GetChaptersForBook(bookID string) ([]Chapter, error) {
	val, closer, err := p.db.Get(chaptersKey(bookID))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("pebble get chapters:%s: %w", bookID, err)
	}
	defer closer.Close()

	var chapters []Chapter
	if err := json.Unmarshal(val, &chapters); err != nil {
		return nil, fmt.Errorf("decode chapters:%s: %w", bookID, err)
	}
	return chapters, nil
}

// SaveChaptersForBook writes (or replaces) the ordered chapter list for
// bookID. The caller controls order; this method never re-sorts. Saving a
// nil or empty slice is equivalent to deleting the entry -- it does not
// store an empty JSON array blob, so a subsequent GetChaptersForBook returns
// (nil, nil) rather than ([]Chapter{}, nil).
func (p *PebbleStore) SaveChaptersForBook(bookID string, chapters []Chapter) error {
	if len(chapters) == 0 {
		return p.DeleteChaptersForBook(bookID)
	}
	data, err := json.Marshal(chapters)
	if err != nil {
		return fmt.Errorf("encode chapters:%s: %w", bookID, err)
	}
	if err := p.db.Set(chaptersKey(bookID), data, pebble.Sync); err != nil {
		return fmt.Errorf("pebble set chapters:%s: %w", bookID, err)
	}
	return nil
}

// DeleteChaptersForBook removes the chapter list for bookID. Missing keys
// are not an error (mirrors DeleteMetadataCache).
func (p *PebbleStore) DeleteChaptersForBook(bookID string) error {
	if err := p.db.Delete(chaptersKey(bookID), pebble.Sync); err != nil {
		return fmt.Errorf("pebble delete chapters:%s: %w", bookID, err)
	}
	return nil
}
