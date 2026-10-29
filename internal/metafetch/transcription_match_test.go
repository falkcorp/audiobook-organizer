// file: internal/metafetch/transcription_match_test.go
// version: 1.0.3
// guid: 9d3f1a6c-2b85-4e07-bc41-7a8e0f2d5c39
// last-edited: 2026-10-29

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPickBestMatch_TranscriptionBoost verifies the audio-derived transcription
// hints steer the scorer toward the candidate that matches what Whisper heard in
// the intro — even when a non-matching candidate has a higher base score.
func TestPickBestMatch_TranscriptionBoost(t *testing.T) {
	t.Run("transcribed_title_match_wins_over_higher_base", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "The Wrong Book", Author: "A", Narrator: "N", DurationSec: 36000},
			{Title: "The Long Halloween", Author: "A", Narrator: "N", DurationSec: 36000},
		}
		// The wrong book has the higher base score; the transcription pulls the
		// right one ahead via the ×2.0 exact-title multiplier.
		scores := []float64{0.9, 0.6}
		words := SignificantWords("The Long Halloween")
		th := transcriptionHints{title: "The Long Halloween"}

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", localRuntime{Sec: 36000}, th)
		require.NotEmpty(t, matched)
		assert.Equal(t, "The Long Halloween", matched[0].Title)
	})

	t.Run("transcribed_author_match_boosts", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Knightfall", Author: "Wrong Author", Narrator: "N", DurationSec: 36000},
			{Title: "Knightfall", Author: "Bruce Wayne", Narrator: "N", DurationSec: 36000},
		}
		scores := []float64{0.8, 0.7}
		words := SignificantWords("Knightfall")
		th := transcriptionHints{author: "Bruce Wayne"}

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", localRuntime{Sec: 36000}, th)
		require.NotEmpty(t, matched)
		assert.Equal(t, "Bruce Wayne", matched[0].Author)
	})

	t.Run("no_hints_leaves_behavior_unchanged", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Knightfall", Author: "Bruce Wayne"},
			{Title: "Elantris", Author: "Bruce Wayne"},
		}
		scores := []float64{0.9, 0.5}
		words := SignificantWords("Knightfall")

		// Empty variadic (no hints) and an explicit-empty hint must both match
		// the pre-feature result: highest base score wins.
		withoutArg := pickBestMatchFromScored(results, scores, "f1", words, "", "", localRuntime{})
		withEmpty := pickBestMatchFromScored(results, scores, "f1", words, "", "", localRuntime{}, transcriptionHints{})
		require.NotEmpty(t, withoutArg)
		require.NotEmpty(t, withEmpty)
		assert.Equal(t, "Knightfall", withoutArg[0].Title)
		assert.Equal(t, withoutArg[0].Title, withEmpty[0].Title)
	})
}

func TestSearchMetadataForBook_TranscriptionTitleHintBoostsManualResults(t *testing.T) {
	transcribedTitle := "Wrong Correct Shared"
	tenHours := 36000
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{
				ID:               id,
				Title:            "Wrong Shared",
				Duration:         &tenHours, // 10h: the candidates' runtime agrees, so the transcription boost applies
				TranscribedTitle: &transcribedTitle,
			}, nil
		},
	}
	svc := NewService(mock)
	svc.SetOverrideSources([]metadata.MetadataSource{
		&mockMetadataSource{
			name: "test-source",
			results: []metadata.BookMetadata{
				{Title: "Wrong Shared", Author: "A", DurationSec: 36000},
				{Title: "Wrong Correct Shared", Author: "A", DurationSec: 36000},
			},
		},
	})

	resp, err := svc.SearchMetadataForBook("b1", "Wrong Shared")

	require.NoError(t, err)
	require.Len(t, resp.Results, 2)
	assert.Equal(t, "Wrong Correct Shared", resp.Results[0].Title)
	assert.Greater(t, resp.Results[0].Score, resp.Results[1].Score)
}

func TestContainsCI(t *testing.T) {
	assert.True(t, containsCI("Bruce Wayne", "wayne"))
	assert.True(t, containsCI("wayne", "Bruce Wayne")) // both-ways
	assert.False(t, containsCI("Bruce Wayne", "Tolkien"))
	assert.False(t, containsCI("", "x"))
	assert.False(t, containsCI("x", ""))
}

func TestHintsFromBook_DropsGarbage(t *testing.T) {
	good := "The Long Halloween"
	garbage := "Unknown" // matches IsGarbageValue blocklist
	empty := "   "
	th := hintsFromBook(&database.Book{
		TranscribedTitle:    &good,
		TranscribedAuthor:   &garbage,
		TranscribedNarrator: &empty,
	})
	assert.Equal(t, "The Long Halloween", th.title)
	assert.Equal(t, "", th.author, "garbage transcribed author must be dropped")
	assert.Equal(t, "", th.narrator, "blank transcribed narrator must be dropped")
}
