// file: internal/metafetch/series_name_bare_test.go
// version: 1.0.0
// guid: 2b7e9d14-6a3f-4c58-b0e1-9f4d7c2a8e63
// last-edited: 2026-09-13

package metafetch

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestApplyMetadataCandidate_SeriesNameStaysBare pins the owner's condition of
// 2026-09-13 for rows whose title number survives as the series position: the
// series is stored as the BARE name, the number only in the position field.
// "Party Hard: Pixel Dust, Book 1" must become title "Party Hard", series
// "Pixel Dust", position "1" -- never a series named "Pixel Dust #1" or
// "Pixel Dust, Book 1". Every series name the apply looks up or creates is
// checked, not just the last one.
func TestApplyMetadataCandidate_SeriesNameStaysBare(t *testing.T) {
	cases := []struct {
		name string
		cand MetadataCandidate
	}{
		{"clean candidate", MetadataCandidate{Title: "Party Hard", Series: "Pixel Dust", SeriesPosition: "1", Source: "Audible"}},
		{"provider baked ', Book 1' into the series name", MetadataCandidate{Title: "Party Hard", Series: "Pixel Dust, Book 1", Source: "Audible"}},
		{"provider baked '#1' into the series name", MetadataCandidate{Title: "Party Hard", Series: "Pixel Dust #1", Source: "Audible"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var names []string
			var updated *database.Book
			mock := &database.MockStore{
				GetBookByIDFunc: func(id string) (*database.Book, error) {
					return &database.Book{ID: id, Title: "Party Hard: Pixel Dust, Book 1"}, nil
				},
				UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
					mu.Lock()
					defer mu.Unlock()
					c := *b
					updated = &c
					return b, nil
				},
				GetSeriesByNameFunc: func(name string, _ *int) (*database.Series, error) {
					mu.Lock()
					names = append(names, name)
					mu.Unlock()
					return nil, nil
				},
				CreateSeriesFunc: func(name string, _ *int) (*database.Series, error) {
					mu.Lock()
					names = append(names, name)
					mu.Unlock()
					return &database.Series{ID: 9, Name: name}, nil
				},
			}
			_, err := NewService(mock).ApplyMetadataCandidate("b1", tc.cand, nil)
			require.NoError(t, err)
			require.NotNil(t, updated, "UpdateBook was not called")

			require.NotEmpty(t, names, "no series was written")
			for _, n := range names {
				assert.Equal(t, "Pixel Dust", n, "series name must be bare")
				assert.False(t, strings.ContainsAny(n, "#") || strings.Contains(strings.ToLower(n), "book"), "number leaked into the series name: %q", n)
			}
			assert.Equal(t, "Party Hard", updated.Title)
			require.NotNil(t, updated.SeriesPositionRaw, "position not written")
			assert.Equal(t, "1", *updated.SeriesPositionRaw)
			require.NotNil(t, updated.SeriesSequence)
			assert.Equal(t, 1, *updated.SeriesSequence)
		})
	}
}
