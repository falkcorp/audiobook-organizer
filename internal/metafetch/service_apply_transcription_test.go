// file: internal/metafetch/service_apply_transcription_test.go
// version: 1.1.0
// guid: 7d4c8f5a-23e6-4d91-a38f-97b5a6c2e1d0
// last-edited: 2026-06-28

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyMetadataCandidateRecordsAudioConfirmedMarker(t *testing.T) {
	tests := []struct {
		name             string
		transcribedTitle string
		transcribedAuth  string
		candidate        MetadataCandidate
		wantMarker       bool
	}{
		{
			name:             "exact_normalized_title_and_author_substring",
			transcribedTitle: "The Way of Kings",
			transcribedAuth:  "Sanderson",
			candidate: MetadataCandidate{
				Title:  "The Way of Kings",
				Author: "Brandon Sanderson",
				Source: "audible",
			},
			wantMarker: true,
		},
		{
			name:             "different_title_is_not_confirmed",
			transcribedTitle: "The Way of Kings",
			transcribedAuth:  "Sanderson",
			candidate: MetadataCandidate{
				Title:  "Words of Radiance",
				Author: "Brandon Sanderson",
				Source: "audible",
			},
			wantMarker: false,
		},
		{
			name:             "short_author_token_with_matching_title_is_confirmed",
			transcribedTitle: "The Way of Kings",
			transcribedAuth:  "BS",
			candidate: MetadataCandidate{
				Title:  "The Way of Kings",
				Author: "Brandon Sanderson",
				Source: "audible",
			},
			wantMarker: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book := &database.Book{
				ID:                "book-1",
				Title:             "Unknown",
				TranscribedTitle:  &tt.transcribedTitle,
				TranscribedAuthor: &tt.transcribedAuth,
			}
			var updated *database.Book
			store := &database.MockStore{
				GetBookByIDFunc: func(id string) (*database.Book, error) {
					return book, nil
				},
				UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
					cp := *book
					updated = &cp
					return &cp, nil
				},
				GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
					return nil, nil
				},
			}

			resp, err := NewService(store).ApplyMetadataCandidate(book.ID, tt.candidate, nil)

			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, updated)
			require.NotNil(t, updated.MetadataReviewStatus)
			if tt.wantMarker {
				assert.Equal(t, "audio_confirmed", *updated.MetadataReviewStatus)
				require.NotNil(t, updated.VersionNotes)
				assert.Contains(t, *updated.VersionNotes, "audio_confirmed")
			} else {
				assert.Equal(t, "matched", *updated.MetadataReviewStatus)
				if updated.VersionNotes != nil {
					assert.NotContains(t, *updated.VersionNotes, "audio_confirmed")
				}
			}
		})
	}
}
