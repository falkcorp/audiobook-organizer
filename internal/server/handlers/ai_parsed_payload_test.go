// file: internal/server/handlers/ai_parsed_payload_test.go
// version: 1.3.0
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
	payload := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Title: "Saga 3", Series: "Saga", SeriesNum: 3}, &database.Book{}, "")
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
	if got := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{}, nil, ""); len(got) != 0 {
		t.Fatalf("empty parse produced %v", got)
	}
	if got := handlers.AIParsedUpdatePayload(nil, nil, ""); len(got) != 0 {
		t.Fatalf("nil parse produced %v", got)
	}
}

// For the book's own series the parse only fills a missing position: a
// stored "1.5" (or any stored number) is not overwritten by the parse's whole
// number, which the update service would also lock like a user edit.
func TestAIParsedUpdatePayload_DoesNotOverwriteAStoredPosition(t *testing.T) {
	raw, seq := "1.5", 1
	for name, book := range map[string]*database.Book{
		"raw only": {SeriesPositionRaw: &raw},
		"int only": {SeriesSequence: &seq},
		"both":     {SeriesSequence: &seq, SeriesPositionRaw: &raw},
	} {
		payload := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Saga", SeriesNum: 1}, book, "saga")
		if _, ok := payload["series_position"]; ok {
			t.Errorf("%s: payload overwrites the stored position: %v", name, payload)
		}
		if payload["series_name"] != "Saga" {
			t.Errorf("%s: series name dropped: %v", name, payload)
		}
	}
}

// A parse that moves the book to a DIFFERENT series sends its number even
// when the book has one (the old number belongs to the old series), and with
// no parsed number clears the position. A stored 0 counts as no position.
func TestAIParsedUpdatePayload_SeriesChangeReplacesOrClearsThePosition(t *testing.T) {
	raw, seq, zero := "1", 1, 0
	book := &database.Book{SeriesSequence: &seq, SeriesPositionRaw: &raw}

	payload := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Other Saga", SeriesNum: 3}, book, "The Saga")
	if payload["series_position"] != 3 {
		t.Fatalf("series change: series_position = %v, want 3", payload["series_position"])
	}
	payload = handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Other Saga"}, book, "The Saga")
	if v, ok := payload["series_position"]; !ok || v != nil {
		t.Fatalf("series change without a number: series_position = %v (sent %v), want null", v, ok)
	}
	payload = handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "the  saga", SeriesNum: 3}, book, "The Saga")
	if _, ok := payload["series_position"]; ok {
		t.Fatalf("same series (other spelling) overwrote the position: %v", payload)
	}
	payload = handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "The Saga", SeriesNum: 3}, &database.Book{SeriesSequence: &zero}, "The Saga")
	if payload["series_position"] != 3 {
		t.Fatalf("stored 0 should count as no position: %v", payload)
	}

	// Through the update service: the move stores the new number.
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s, err := store.CreateSeries("The Saga", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.CreateBook(&database.Book{Title: "T", FilePath: "/library/m.m4b", Format: "m4b",
		SeriesID: &s.ID, Series: s, SeriesSequence: &seq, SeriesPositionRaw: &raw})
	if err != nil {
		t.Fatal(err)
	}
	p := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Other Saga", SeriesNum: 3}, b, "The Saga")
	if _, err := audiobooks.NewAudiobookUpdateService(store).UpdateAudiobook(context.Background(), b.ID, p); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SeriesSequence == nil || *row.SeriesSequence != 3 || row.SeriesPositionRaw == nil || *row.SeriesPositionRaw != "3" {
		t.Fatalf("moved book position = %v / %v, want 3 / \"3\"", row.SeriesSequence, row.SeriesPositionRaw)
	}
}

// A book that shows no series (a dangling link, no embedded object) has no
// other series' number to replace: fill-only, and never a null that would
// clear the stored position.
func TestAIParsedUpdatePayload_BookShowingNoSeriesIsFillOnly(t *testing.T) {
	seq, raw := 4, "4"
	book := &database.Book{SeriesSequence: &seq, SeriesPositionRaw: &raw}
	if p := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Foo", SeriesNum: 2}, book, ""); p["series_position"] != nil {
		t.Fatalf("overwrote the stored position: %v", p)
	}
	if p := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Foo"}, book, ""); func() bool { _, ok := p["series_position"]; return ok }() {
		t.Fatalf("sent a series_position for a book showing no series: %v", p)
	}
	if p := handlers.AIParsedUpdatePayload(&ai.ParsedMetadata{Series: "Foo", SeriesNum: 2}, &database.Book{}, ""); p["series_position"] != 2 {
		t.Fatalf("did not fill a missing position: %v", p)
	}
}
