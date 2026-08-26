// file: internal/metafetch/service_writeback_filtertags_test.go
// version: 1.1.0
// guid: 7c1d9a04-2b6e-4f38-9d51-0a8e3b7c62f4
// last-edited: 2026-08-15

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
)

// The pre-existing FilterUnchangedTags tests all point at nonexistent paths, so
// ExtractMetadata fails, the function returns tagMap untouched, and the mapping
// is never executed. These tests drive the pure comparison directly so the
// mapping is actually measured.

// TestFilterTagsAgainst_MultiFileBookCanSkip is the regression test for the
// defect this file was created for.
//
// The multi-file write-back branch always emits a "track" tag ("n/total"). Until
// "track" was mapped in currentVals it hit the unknown-key branch and was always
// written, so `len(tagMap) == 0` — the skip condition — was UNREACHABLE for every
// multi-file book. Each one rewrote every one of its files on every run forever,
// and since each write costs multiple full-file copies + hashes of the audio,
// that was the dominant cost of write-back.
//
// With the fix, a book whose tags already match filters down to empty.
func TestFilterTagsAgainst_MultiFileBookCanSkip(t *testing.T) {
	current := metadata.Metadata{
		Title:       "01 - Test Book",
		Album:       "Test Book",
		Artist:      "Test Author",
		Genre:       "Audiobook",
		TrackNumber: 1,
		TrackTotal:  12,
	}

	// Exactly what the multi-file branch builds when nothing has changed.
	tagMap := map[string]any{
		"title":  "01 - Test Book",
		"album":  "Test Book",
		"artist": "Test Author",
		"genre":  "Audiobook",
		"track":  "1/12",
	}

	filtered := filterTagsAgainst("/tmp/test.m4b", current, tagMap)

	assert.Empty(t, filtered,
		"an unchanged multi-file book must filter to zero tags so write-back can skip it; "+
			"a non-empty result here means every multi-file book rewrites every file every run")
}

// TestFilterTagsAgainst_TrackMatching pins the "n/total" rendering, including
// the convergence case where the file carries a bare track number.
func TestFilterTagsAgainst_TrackMatching(t *testing.T) {
	tests := []struct {
		name        string
		trackNumber int
		trackTotal  int
		desired     string
		wantWritten bool
		why         string
	}{
		{
			name:        "exact pair matches",
			trackNumber: 3,
			trackTotal:  12,
			desired:     "3/12",
			wantWritten: false,
			why:         "identical track pair must not be rewritten",
		},
		{
			name:        "different track number is written",
			trackNumber: 4,
			trackTotal:  12,
			desired:     "3/12",
			wantWritten: true,
			why:         "a genuinely different track must still be written",
		},
		{
			name:        "different total is written",
			trackNumber: 3,
			trackTotal:  99,
			desired:     "3/12",
			wantWritten: true,
			why:         "total is part of the value and must be corrected",
		},
		{
			name:        "bare track number converges after one write",
			trackNumber: 3,
			trackTotal:  0,
			desired:     "3/12",
			wantWritten: true,
			why: "file tagged '3' renders as '3', differs from '3/12', written once; " +
				"after that write it carries the pair and matches on the next run",
		},
		{
			name:        "unreadable track is written",
			trackNumber: 0,
			trackTotal:  0,
			desired:     "3/12",
			wantWritten: true,
			why:         "TrackNumber==0 means we could not read a track; writing is the safe call",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := metadata.Metadata{
				TrackNumber: tt.trackNumber,
				TrackTotal:  tt.trackTotal,
			}
			filtered := filterTagsAgainst("/tmp/test.m4b", current, map[string]any{"track": tt.desired})

			if tt.wantWritten {
				assert.Contains(t, filtered, "track", tt.why)
			} else {
				assert.NotContains(t, filtered, "track", tt.why)
			}
		})
	}
}

// TestChapterTitleFor covers the per-chapter title preservation rule.
//
// Both multi-file write paths used to overwrite the title tag of every file with
// a synthetic "NN - Book Title" on every run, so real chapter titles were
// destroyed and could not be recovered. "" means "leave the existing title".
func TestChapterTitleFor(t *testing.T) {
	const bookTitle = "The Long Way Home"

	tests := []struct {
		name    string
		current string
		want    string
		why     string
	}{
		{
			name:    "real chapter title is preserved",
			current: "Chapter 1: Departure",
			want:    "",
			why:     "publisher chapter titles are the metadata the owner wants kept",
		},
		{
			name:    "single-word chapter title is preserved",
			current: "Prologue",
			want:    "",
			why:     "short titles are still real titles",
		},
		{
			name:    "numbered title for a DIFFERENT book is preserved",
			current: "05 - Some Other Book",
			want:    "",
			why:     "matches the numeric shape but the suffix is not this book, so it is not ours",
		},
		{
			name:    "empty title is synthesized",
			current: "",
			want:    "01 - The Long Way Home",
			why:     "nothing to preserve",
		},
		{
			name:    "whitespace-only title is synthesized",
			current: "   ",
			want:    "01 - The Long Way Home",
			why:     "blank after trimming carries no information",
		},
		{
			name:    "bare book title is synthesized",
			current: bookTitle,
			want:    "01 - The Long Way Home",
			why:     "every file carrying the same book title says nothing about which chapter it is",
		},
		{
			name:    "our own previous output is refreshed",
			current: "07 - The Long Way Home",
			want:    "01 - The Long Way Home",
			why:     "renumbering must still propagate when the track count changed",
		},
		{
			name:    "already correct value is returned unchanged",
			current: "01 - The Long Way Home",
			want:    "01 - The Long Way Home",
			why:     "returning it lets the unchanged-tag filter drop it as a no-op",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chapterTitleFor(tt.current, "01 - The Long Way Home", bookTitle)
			assert.Equal(t, tt.want, got, tt.why)
		})
	}
}

// TestFilterTagsAgainst_ChangedValuesStillWritten is the counterweight: the fix
// must not turn the filter into a blanket "skip everything". Without decoy
// changed fields, a broken implementation that drops all tags would pass the
// skip test above.
func TestFilterTagsAgainst_ChangedValuesStillWritten(t *testing.T) {
	current := metadata.Metadata{
		Title:       "01 - Old Title",
		Album:       "Old Album",
		Artist:      "Old Author",
		TrackNumber: 1,
		TrackTotal:  12,
	}

	tagMap := map[string]any{
		"title":  "01 - New Title",
		"album":  "New Album",
		"artist": "Old Author", // unchanged — must be dropped
		"track":  "1/12",       // unchanged — must be dropped
	}

	filtered := filterTagsAgainst("/tmp/test.m4b", current, tagMap)

	assert.Equal(t, map[string]any{
		"title": "01 - New Title",
		"album": "New Album",
	}, filtered, "only genuinely changed fields may survive the filter")
}
