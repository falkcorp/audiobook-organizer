// file: internal/plugins/maintenance/intro_transcribe_lock_columns_test.go
// version: 1.0.1
// guid: d4a82f16-7b39-4e05-9c61-3f0e8b2a5d79
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// changedColumns names the exported Book fields that differ between two rows.
func changedColumns(before, after *database.Book) []string {
	var out []string
	bv, av := reflect.ValueOf(*before), reflect.ValueOf(*after)
	for i := range bv.NumField() {
		if !bv.Type().Field(i).IsExported() {
			continue
		}
		if !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			out = append(out, bv.Type().Field(i).Name)
		}
	}
	return out
}

// The intro transcription writers (the reparse of stored transcripts and the
// per-book outcome write) change only the book's transcribe columns. None of
// those is a column a user can lock (database.UserLockableFields), which is why
// these writers carry no lock check: a user's locked title, author, narrator or
// description is not something they can reach. The day one of them writes a
// lockable column, this test fails and the writer needs the lock helper.
func TestIntroTranscribeWritersChangeNoUserLockableColumn(t *testing.T) {
	lockable := map[string]bool{}
	for _, f := range database.UserLockableFields {
		lockable[f.Column] = true
	}
	newRow := func() *database.Book {
		return &database.Book{
			ID:                 "book-intro",
			Title:              "Batman: Year One",
			Narrator:           new("Selina Kyle"),
			Description:        new("A rookie captain arrives in Gotham."),
			IntroTranscription: new("Batman Year One Written by Lucius Fox Performed by Alfred Pennyworth"),
		}
	}

	t.Run("reparse", func(t *testing.T) {
		before := newRow()
		store := newReparseStore(before)
		p := &Plugin{}
		if err := p.reparseStoredIntros(context.Background(), store, &fakeReporter{}, store.order, false); err != nil {
			t.Fatal(err)
		}
		after := store.books[before.ID]
		cols := changedColumns(before, after)
		if len(cols) == 0 {
			t.Fatal("the reparse changed nothing, so this test proves nothing about its columns")
		}
		for _, c := range cols {
			if lockable[c] {
				t.Errorf("reparse changed the user-lockable column %s", c)
			}
		}
	})

	t.Run("outcome", func(t *testing.T) {
		before := newRow()
		before.IntroTranscription = nil
		store := newReparseStore(before)
		p := &Plugin{}
		fields := transcribe.IntroFields{Title: "Batman: Year One", Author: "Lucius Fox", Narrator: "Alfred Pennyworth"}
		if !p.writeOutcome(store, slog.Default(), before, statusOK, "", "Batman Year One Written by Lucius Fox", fields, time.Now(), false) {
			t.Fatal("the outcome was not written")
		}
		after := store.books[before.ID]
		cols := changedColumns(before, after)
		if !slices.Contains(cols, "IntroTranscription") {
			t.Fatalf("changed columns %v lack the transcript, so the write did not run", cols)
		}
		for _, c := range cols {
			if lockable[c] {
				t.Errorf("the outcome write changed the user-lockable column %s", c)
			}
		}
	})
}
