// file: internal/server/handlers/ai_parsed_payload_test.go
// version: 1.1.0
// guid: 5154d5a5-0191-49dc-95fa-0d8b4f784a05
// last-edited: 2026-10-03

package handlers_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// The AI parse sent the series number as series_sequence, a key the update
// service never reads, so every AI-parsed series number was dropped. It goes
// as series_position now, and the update service stores it.
func TestAIParsedUpdatePayload_SeriesNumberIsStored(t *testing.T) {
	payload := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Title: "Saga 3", Series: "Saga", SeriesNum: 3}, &database.Book{})
	if _, ok := payload["series_sequence"]; ok {
		t.Fatalf("payload still uses series_sequence: %v", payload)
	}
	if payload["series_position"] != 3 {
		t.Fatalf("series_position = %v, want 3", payload["series_position"])
	}

	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/s.m4b", Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), book.ID, payload); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBookByID(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SeriesID == nil || row.SeriesSequence == nil || *row.SeriesSequence != 3 {
		t.Fatalf("series not stored from the AI payload: id=%v seq=%v", row.SeriesID, row.SeriesSequence)
	}
	if row.SeriesPositionRaw == nil || *row.SeriesPositionRaw != "3" {
		t.Fatalf("raw position = %v, want \"3\"", row.SeriesPositionRaw)
	}
}

func TestAIParsedUpdatePayload_OnlyFilledFields(t *testing.T) {
	if got := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{}, nil); len(got) != 0 {
		t.Fatalf("empty parse produced %v", got)
	}
	if got := handlers.AIParsedUpdatePayload(nil, nil); len(got) != 0 {
		t.Fatalf("nil parse produced %v", got)
	}
}

// The parse only fills a missing position: a stored "1.5" (or any stored
// number) is not overwritten by the parse's whole number, which the update
// service would also lock like a user edit.
func TestAIParsedUpdatePayload_DoesNotOverwriteAStoredPosition(t *testing.T) {
	raw, seq := "1.5", 1
	for name, book := range map[string]*database.Book{
		"raw only": {SeriesPositionRaw: &raw},
		"int only": {SeriesSequence: &seq},
		"both":     {SeriesSequence: &seq, SeriesPositionRaw: &raw},
	} {
		payload := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Saga", SeriesNum: 1}, book)
		if _, ok := payload["series_position"]; ok {
			t.Errorf("%s: payload overwrites the stored position: %v", name, payload)
		}
		if payload["series_name"] != "Saga" {
			t.Errorf("%s: series name dropped: %v", name, payload)
		}
	}
}
