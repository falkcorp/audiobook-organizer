// file: internal/audiobooks/update_service.go
// version: 1.4.0
// guid: b2c3d4e5-f6g7-h8i9-j0k1-l2m3n4o5p6q7
// last-edited: 2026-08-18

package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// AudiobookUpdateService reuses audiobookStore rather than declaring its own
// slice. A probe of its former audiobookUpdateStore found exactly one direct
// call (GetBookByID) -- a strict subset -- and NewAudiobookUpdateService
// forwards db straight into NewAudiobookService below, so the second
// declaration only ever restated audiobookStore under another name.


type AudiobookUpdateService struct {
	db               audiobookStore
	audiobookService *AudiobookService
}

func NewAudiobookUpdateService(db audiobookStore) *AudiobookUpdateService {
	return &AudiobookUpdateService{
		db:               db,
		audiobookService: NewAudiobookService(db),
	}
}

// ExtractOverrides extracts and marshals the overrides map from payload
func (aus *AudiobookUpdateService) ExtractOverrides(payload map[string]any) (map[string]any, bool) {
	val, ok := payload["overrides"]
	if !ok {
		return nil, false
	}

	overridesMap, ok := val.(map[string]any)
	if !ok {
		return nil, false
	}

	return overridesMap, true
}

// UpdateAudiobook is the main business logic method
func (aus *AudiobookUpdateService) UpdateAudiobook(ctx context.Context, id string, payload map[string]any) (*database.Book, error) {
	if id == "" {
		return nil, fmt.Errorf("audiobook ID is required")
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("no updates provided")
	}
	if aus.audiobookService == nil {
		return nil, fmt.Errorf("audiobook service not initialized")
	}

	currentBook, err := aus.db.GetBookByID(id)
	if err != nil || currentBook == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	bookCopy := *currentBook
	updates := &AudiobookUpdate{Book: &bookCopy}

	if title, ok := util.ExtractStringField(payload, "title"); ok {
		updates.Title = title
	}
	if authorID, ok := util.ExtractIntField(payload, "author_id"); ok {
		updates.AuthorID = &authorID
	}
	if seriesID, ok := util.ExtractIntField(payload, "series_id"); ok {
		updates.SeriesID = &seriesID
	}
	if authorName, ok := util.ExtractStringField(payload, "author_name"); ok {
		updates.AuthorName = &authorName
	}
	if seriesName, ok := util.ExtractStringField(payload, "series_name"); ok {
		updates.SeriesName = &seriesName
	}
	if format, ok := util.ExtractStringField(payload, "format"); ok {
		updates.Format = format
	}
	if filePath, ok := util.ExtractStringField(payload, "file_path"); ok {
		updates.FilePath = filePath
	}
	if narrator, ok := util.ExtractStringField(payload, "narrator"); ok {
		updates.Narrator = &narrator
	}
	if publisher, ok := util.ExtractStringField(payload, "publisher"); ok {
		updates.Publisher = &publisher
	}
	if language, ok := util.ExtractStringField(payload, "language"); ok {
		updates.Language = &language
	}
	if year, ok := util.ExtractIntField(payload, "audiobook_release_year"); ok {
		updates.AudiobookReleaseYear = &year
	}
	if isbn10, ok := util.ExtractStringField(payload, "isbn10"); ok {
		updates.ISBN10 = &isbn10
	}
	if isbn13, ok := util.ExtractStringField(payload, "isbn13"); ok {
		updates.ISBN13 = &isbn13
	}
	if desc, ok := util.ExtractStringField(payload, "description"); ok {
		updates.Description = &desc
	}

	if overridesMap, ok := aus.ExtractOverrides(payload); ok {
		updates.Overrides = make(map[string]OverridePayload)
		for key, value := range overridesMap {
			overrideValue, ok := value.(map[string]any)
			if !ok {
				continue
			}

			override := OverridePayload{}
			if val, ok := overrideValue["value"]; ok {
				if valBytes, err := json.Marshal(val); err == nil {
					override.Value = valBytes
				}
			}
			if locked, ok := overrideValue["locked"].(bool); ok {
				override.Locked = &locked
			}
			if fetchedVal, ok := overrideValue["fetched_value"]; ok {
				if fetchedBytes, err := json.Marshal(fetchedVal); err == nil {
					override.FetchedValue = fetchedBytes
				}
			}
			if clear, ok := overrideValue["clear"].(bool); ok {
				override.Clear = clear
			}
			updates.Overrides[key] = override
		}
	}

	if unlockOverridesRaw, ok := payload["unlock_overrides"].([]any); ok {
		updates.UnlockOverrides = make([]string, 0, len(unlockOverridesRaw))
		for _, value := range unlockOverridesRaw {
			if str, ok := value.(string); ok {
				updates.UnlockOverrides = append(updates.UnlockOverrides, str)
			}
		}
	}

	rawPayload := make(map[string]json.RawMessage, len(payload))
	for key, value := range payload {
		rawValue, err := json.Marshal(value)
		if err != nil {
			continue
		}
		rawPayload[key] = rawValue
	}

	req := &UpdateAudiobookRequest{
		Updates:    updates,
		RawPayload: rawPayload,
	}

	return aus.audiobookService.UpdateAudiobook(ctx, id, req)
}
