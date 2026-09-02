// file: internal/audiobooks/field_locks_conformance_test.go
// version: 1.0.0
// guid: 8517d74f-37bb-483a-8f1a-ac589f71c89f
// last-edited: 2026-09-02

package audiobooks

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The writer's keys must all be in the shared lock vocabulary. This is the
// writer-side half of the contract; internal/metafetch and internal/scanner
// hold the reader-side halves (every vocabulary key blocks its column). A key
// written here that is NOT in database.UserLockableFields would be a lock no
// guard consults -- exactly the 2026-08 author/series/series_sequence hole,
// from the other direction.
func TestUserEditFieldExtractorsAreInTheLockVocabulary(t *testing.T) {
	vocab := map[string]bool{}
	for _, f := range database.UserLockableFields {
		vocab[f.Key] = true
	}

	// A fully populated payload, so every extractor yields a value: an
	// extractor that returned (nil,false) here would never write a lock, and
	// this test would be asserting on a key that is never used.
	book := &database.Book{
		Title:                "T",
		Narrator:             new("N"),
		Publisher:            new("P"),
		Language:             new("en"),
		AudiobookReleaseYear: new(2020),
		ISBN10:               new("1111111111"),
		ISBN13:               new("9781111111111"),
	}
	extractors := userEditFieldExtractors(&AudiobookUpdate{Book: book}, "Author", "Series")

	if len(extractors) == 0 {
		t.Fatal("no extractors: the writer writes nothing, so nothing can ever be locked")
	}
	for key, extract := range extractors {
		if !vocab[key] {
			t.Errorf("writer key %q is not in database.UserLockableFields: a lock under this "+
				"key is consulted by no guard", key)
		}
		if _, ok := extract(); !ok {
			t.Errorf("extractor for %q yielded nothing on a fully populated payload", key)
		}
	}

	// The literal spellings the UI sends (FIELD_TO_API / FIELD_STATE_KEYS in
	// MetadataEditDialog.tsx and BookDetail.tsx). The frontend cannot be
	// compile-tied to the Go constants, so pin the strings by hand here.
	for _, uiKey := range []string{
		"title", "author_name", "narrator", "series_name", "series_position", "genre",
		"audiobook_release_year", "language", "publisher", "isbn10", "isbn13", "description",
	} {
		if !vocab[uiKey] {
			t.Errorf("UI lock key %q is not in database.UserLockableFields", uiKey)
		}
	}
}

// ApplyOverrideToPayload projects an override onto the row using the same
// vocabulary. Every key it handles must set the payload field the vocabulary
// names, and an unknown key must be a no-op (stored as state, not applied).
func TestApplyOverrideToPayloadUsesTheLockVocabulary(t *testing.T) {
	cases := []struct {
		key   string
		value any
		check func(p *AudiobookUpdate) bool
	}{
		{database.FieldKeyTitle, "T", func(p *AudiobookUpdate) bool { return p.Title == "T" }},
		{database.FieldKeyAuthorName, "A", func(p *AudiobookUpdate) bool { return p.AuthorName != nil && *p.AuthorName == "A" }},
		{database.FieldKeySeriesName, "S", func(p *AudiobookUpdate) bool { return p.SeriesName != nil && *p.SeriesName == "S" }},
		{database.FieldKeyNarrator, "N", func(p *AudiobookUpdate) bool { return p.Narrator != nil && *p.Narrator == "N" }},
		{database.FieldKeyPublisher, "P", func(p *AudiobookUpdate) bool { return p.Publisher != nil && *p.Publisher == "P" }},
		{database.FieldKeyLanguage, "de", func(p *AudiobookUpdate) bool { return p.Language != nil && *p.Language == "de" }},
		{database.FieldKeyAudiobookReleaseYear, float64(2019), func(p *AudiobookUpdate) bool {
			return p.AudiobookReleaseYear != nil && *p.AudiobookReleaseYear == 2019
		}},
		{database.FieldKeyISBN10, "1111111111", func(p *AudiobookUpdate) bool { return p.ISBN10 != nil && *p.ISBN10 == "1111111111" }},
		{database.FieldKeyISBN13, "9781111111111", func(p *AudiobookUpdate) bool { return p.ISBN13 != nil && *p.ISBN13 == "9781111111111" }},
		{database.FieldKeyASIN, "B000000000", func(p *AudiobookUpdate) bool { return p.ASIN != nil && *p.ASIN == "B000000000" }},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			p := &AudiobookUpdate{Book: &database.Book{}}
			ApplyOverrideToPayload(p, tc.key, tc.value)
			if !tc.check(p) {
				t.Errorf("override %q=%v was not projected onto the payload", tc.key, tc.value)
			}
		})
	}

	t.Run("unknown key is a no-op", func(t *testing.T) {
		p := &AudiobookUpdate{Book: &database.Book{Title: "keep"}}
		ApplyOverrideToPayload(p, "not_a_field", "x")
		if p.Title != "keep" || p.Narrator != nil || p.ASIN != nil {
			t.Errorf("unknown key mutated the payload: %+v", p.Book)
		}
	})
}
