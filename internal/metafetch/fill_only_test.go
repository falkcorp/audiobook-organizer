// file: internal/metafetch/fill_only_test.go
// version: 1.0.0
// guid: 4c8e1f27-9a6b-4d35-8e02-b7f1c3a9d640
// last-edited: 2026-09-14

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// A filled descriptive field is kept; an empty or garbage one is filled; the
// identity fields pass through untouched.
func TestStripFilledFields_KeepsFilledFillsEmpty(t *testing.T) {
	abr := true
	book := &database.Book{
		Title:                "Old Title",
		Description:          new("The owner's description"),
		Narrator:             new("Kate Reading"),
		Publisher:            nil,
		Genre:                new("Unknown"), // garbage counts as empty
		CoverURL:             new("/covers/b1.jpg"),
		AudiobookReleaseYear: new(2011),
		AudibleRuntimeMin:    new(613),
		SeriesSecondary:      new("Legend of Drizzt"),
		Abridged:             &abr,
	}
	no := false
	meta := metadata.BookMetadata{
		Title: "New Title", Author: "R.A. Salvatore", Series: "Dark Elf", SeriesPosition: "3",
		ASIN: "B0000TEST1", ISBN13: "9780000000001",
		Description: "Provider blurb", Narrator: "Victor Bevine", Publisher: "Audible Studios",
		Genre: "Fantasy", CoverURL: "https://example.invalid/c.jpg",
		PublishYear: 2019, PublishYearIsAudiobookRelease: true, DurationSec: 700 * 60,
		SeriesSecondary: "Forgotten Realms", SeriesSecondaryPosition: "7", Abridged: &no,
		Subtitle: "A Subtitle", Language: "English", PageCount: 300,
	}

	got, stripped := StripFilledFields(book, meta)

	assert.ElementsMatch(t, []string{
		"description", "narrator", "cover_url", "audiobook_release_year",
		"audible_runtime_min", "series_secondary", "abridged",
	}, stripped)
	assert.Empty(t, got.Description)
	assert.Empty(t, got.Narrator)
	assert.Empty(t, got.CoverURL)
	assert.Zero(t, got.PublishYear)
	assert.Zero(t, got.DurationSec)
	assert.Empty(t, got.SeriesSecondary)
	assert.Empty(t, got.SeriesSecondaryPosition, "the secondary position goes with its series")
	assert.Nil(t, got.Abridged)

	// Empty or garbage columns are filled.
	assert.Equal(t, "Audible Studios", got.Publisher)
	assert.Equal(t, "Fantasy", got.Genre)
	assert.Equal(t, "A Subtitle", got.Subtitle)
	assert.Equal(t, "English", got.Language)
	assert.Equal(t, 300, got.PageCount)

	// Identity fields are the gate's to judge, never stripped here.
	assert.Equal(t, "New Title", got.Title)
	assert.Equal(t, "R.A. Salvatore", got.Author)
	assert.Equal(t, "Dark Elf", got.Series)
	assert.Equal(t, "3", got.SeriesPosition)
	assert.Equal(t, "B0000TEST1", got.ASIN)
	assert.Equal(t, "9780000000001", got.ISBN13)
}

// A print-kind year is already fill-only in the apply body (PrintYear), so the
// strip leaves it alone even when the release year is filled.
func TestStripFilledFields_PrintYearPassesThrough(t *testing.T) {
	book := &database.Book{AudiobookReleaseYear: new(2011)}
	got, stripped := StripFilledFields(book, metadata.BookMetadata{PublishYear: 1988})
	assert.Equal(t, 1988, got.PublishYear)
	assert.Empty(t, stripped)
}

// The bulk-apply preview is what the owner reviews before a batch apply, so it
// must show the fill-only result, while the hand-picked plan still shows the
// overwrite.
func TestPreviewMetadataCandidate_BatchPreviewIsFillOnly(t *testing.T) {
	book := &database.Book{
		ID: "b1", Title: "Sojourn",
		Description: new("The owner's description"),
		Narrator:    new("Victor Bevine"),
	}
	svc := NewService(&database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) { c := *book; return &c, nil },
	})
	cand := MetadataCandidate{
		Title: "Sojourn", Description: "Provider blurb", Narrator: "Someone Else", Publisher: "Audible Studios",
	}
	fieldsOf := func(pv *ApplyPreview) []string {
		var out []string
		for _, c := range pv.Changes {
			out = append(out, c.Field)
		}
		return out
	}

	batch, err := svc.PreviewMetadataCandidate("b1", cand, false)
	require.NoError(t, err)
	assert.Contains(t, fieldsOf(batch), "publisher", "an empty field is filled")
	assert.NotContains(t, fieldsOf(batch), "description", "a batch apply must not overwrite a filled description")
	assert.NotContains(t, fieldsOf(batch), "narrator", "a batch apply must not overwrite a filled narrator")

	picked, err := svc.previewMetadataCandidate("b1", cand, nil, false, false)
	require.NoError(t, err)
	assert.Contains(t, fieldsOf(picked), "description", "the hand-picked apply may overwrite")
	assert.Contains(t, fieldsOf(picked), "narrator")
}
