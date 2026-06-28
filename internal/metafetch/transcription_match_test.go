// file: internal/metafetch/transcription_match_test.go
// version: 1.0.0
// guid: 9d3f1a6c-2b85-4e07-bc41-7a8e0f2d5c39
// last-edited: 2026-06-28

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
			{Title: "The Wrong Book", Author: "A", Narrator: "N"},
			{Title: "The Way of Kings", Author: "A", Narrator: "N"},
		}
		// The wrong book has the higher base score; the transcription pulls the
		// right one ahead via the ×2.0 exact-title multiplier.
		scores := []float64{0.9, 0.6}
		words := SignificantWords("The Way of Kings")
		th := transcriptionHints{title: "The Way of Kings"}

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0, th)
		require.NotEmpty(t, matched)
		assert.Equal(t, "The Way of Kings", matched[0].Title)
	})

	t.Run("transcribed_author_match_boosts", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Author: "Wrong Author", Narrator: "N"},
			{Title: "Mistborn", Author: "Brandon Sanderson", Narrator: "N"},
		}
		scores := []float64{0.8, 0.7}
		words := SignificantWords("Mistborn")
		th := transcriptionHints{author: "Brandon Sanderson"}

		matched := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0, th)
		require.NotEmpty(t, matched)
		assert.Equal(t, "Brandon Sanderson", matched[0].Author)
	})

	t.Run("no_hints_leaves_behavior_unchanged", func(t *testing.T) {
		results := []metadata.BookMetadata{
			{Title: "Mistborn", Author: "Brandon Sanderson"},
			{Title: "Elantris", Author: "Brandon Sanderson"},
		}
		scores := []float64{0.9, 0.5}
		words := SignificantWords("Mistborn")

		// Empty variadic (no hints) and an explicit-empty hint must both match
		// the pre-feature result: highest base score wins.
		withoutArg := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0)
		withEmpty := pickBestMatchFromScored(results, scores, "f1", words, "", "", 0, transcriptionHints{})
		require.NotEmpty(t, withoutArg)
		require.NotEmpty(t, withEmpty)
		assert.Equal(t, "Mistborn", withoutArg[0].Title)
		assert.Equal(t, withoutArg[0].Title, withEmpty[0].Title)
	})
}

func TestContainsCI(t *testing.T) {
	assert.True(t, containsCI("Brandon Sanderson", "sanderson"))
	assert.True(t, containsCI("sanderson", "Brandon Sanderson")) // both-ways
	assert.False(t, containsCI("Brandon Sanderson", "Tolkien"))
	assert.False(t, containsCI("", "x"))
	assert.False(t, containsCI("x", ""))
}

func TestHintsFromBook_DropsGarbage(t *testing.T) {
	good := "The Way of Kings"
	garbage := "Unknown" // matches IsGarbageValue blocklist
	empty := "   "
	th := hintsFromBook(&database.Book{
		TranscribedTitle:    &good,
		TranscribedAuthor:   &garbage,
		TranscribedNarrator: &empty,
	})
	assert.Equal(t, "The Way of Kings", th.title)
	assert.Equal(t, "", th.author, "garbage transcribed author must be dropped")
	assert.Equal(t, "", th.narrator, "blank transcribed narrator must be dropped")
}
