// file: internal/server/server_title_helpers.go
// version: 1.2.1
// guid: b4b0048c-d778-43c9-871e-21f9a9b6705d
// last-edited: 2026-10-18

package server

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func computeSeriesPrunePreview(store seriesPruneReader) (*seriesPrunePreviewResult, error) {
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("failed to get series: %w", err)
	}

	// Group by LOWER(TRIM(name)) + author_id
	type groupKey struct {
		name     string
		authorID int // 0 means nil
	}
	groups := make(map[groupKey][]database.Series)
	for _, s := range allSeries {
		aid := 0
		if s.AuthorID != nil {
			aid = *s.AuthorID
		}
		key := groupKey{name: strings.ToLower(strings.TrimSpace(s.Name)), authorID: aid}
		groups[key] = append(groups[key], s)
	}

	result := &seriesPrunePreviewResult{}

	// Find duplicate groups (>1 entry with same normalized name + author_id)
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}

		// Pick canonical: most books attached, then lowest ID
		canonicalIdx := 0
		canonicalBookCount := 0
		for i, s := range group {
			books, err := store.GetBooksBySeriesIDCore(s.ID)
			if err != nil {
				continue
			}
			bc := len(books)
			if bc > canonicalBookCount || (bc == canonicalBookCount && s.ID < group[canonicalIdx].ID) {
				canonicalIdx = i
				canonicalBookCount = bc
			}
		}

		var mergeIDs []int
		totalBooks := 0
		for i, s := range group {
			if i == canonicalIdx {
				continue
			}
			mergeIDs = append(mergeIDs, s.ID)
			books, _ := store.GetBooksBySeriesIDCore(s.ID)
			totalBooks += len(books)
		}
		books, _ := store.GetBooksBySeriesIDCore(group[canonicalIdx].ID)
		totalBooks += len(books)

		result.Groups = append(result.Groups, seriesPrunePreviewGroup{
			Name:        group[canonicalIdx].Name,
			CanonicalID: group[canonicalIdx].ID,
			MergeIDs:    mergeIDs,
			BookCount:   totalBooks,
			Type:        "duplicate",
		})
		result.DuplicateCount += len(mergeIDs)
	}

	// Find orphan series with 0 books
	for _, s := range allSeries {
		books, err := store.GetBooksBySeriesIDCore(s.ID)
		if err != nil {
			continue
		}
		if len(books) == 0 {
			result.Groups = append(result.Groups, seriesPrunePreviewGroup{
				Name:        s.Name,
				CanonicalID: s.ID,
				MergeIDs:    nil,
				BookCount:   0,
				Type:        "orphan",
			})
			result.OrphanCount++
		}
	}

	result.TotalCount = result.DuplicateCount + result.OrphanCount
	return result, nil
}
