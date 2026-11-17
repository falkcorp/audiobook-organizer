// file: internal/plugins/maintenance/file_intro_reparse_pebble_test.go
// version: 1.0.0
// guid: 4c7e1a95-d2b8-4f36-9a05-e83b6d1c7f42
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// The op against the real PebbleStore, not only the fake: the listing is the real
// stripped core projection, the read is the real full row, and the write is the
// real field-scoped compare-and-swap writer. Invented text only.
func TestReparseFileIntros_AgainstThePebbleStore(t *testing.T) {
	s := newRepairPebble(t)
	book, err := s.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: "/lib/Gotham/Batman-Year-One"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(n int) string {
		f := &database.BookFile{
			BookID: book.ID, FilePath: fmt.Sprintf("/lib/Gotham/Batman-Year-One/%02d.m4b", n),
			FileHash: fmt.Sprintf("gotham-hash-%02d", n), Duration: database.Secs(600), FileSize: 4096,
			// A fingerprint, so the whole-row re-encode is the realistic shape.
			AcoustIDFingerprint: make([]byte, 2048),
		}
		if err := s.CreateBookFile(f); err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	ids := []string{mk(1), mk(2), mk(3), mk(4)}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	hashOf := func(id string) string { f, _ := s.GetBookFileByID(book.ID, id); return f.FileHash }
	set := func(id, text, status string, r database.BookFileIntroResult) {
		t.Helper()
		r.SourceHash, r.RunOpID, r.Status, r.Done, r.At = hashOf(id), "op-batman", status, true, at
		if text != "" {
			r.Transcript = &text
		}
		if _, err := s.SetBookFileIntro(book.ID, id, r); err != nil {
			t.Fatalf("SetBookFileIntro(%s): %v", id, err)
		}
	}
	set(ids[0], rpTitle, "unparsed", database.BookFileIntroResult{})
	set(ids[1], rpChapter, "ok", database.BookFileIntroResult{Title: "Hush"})
	set(ids[2], "", "whisper_error", database.BookFileIntroResult{})
	// ids[3] was never transcribed.

	w, ok := database.AsCapability[database.BookFileIntroReparser](s)
	if !ok {
		t.Fatal("the Pebble store does not offer the re-parse writer")
	}
	env := reparseFileIntrosEnv{reads: s, writer: w, queue: reparseQueue{}, reporter: &fakeReporter{}, workers: 3}
	ctx := context.Background()

	snapshot := func() []database.BookFile {
		var out []database.BookFile
		for _, id := range ids {
			f, err := s.GetBookFileByID(book.ID, id)
			if err != nil || f == nil {
				t.Fatalf("read %s: %v %v", id, f, err)
			}
			cp := *f
			cp.IntroParse, cp.UpdatedAt = nil, time.Time{}
			out = append(out, cp)
		}
		return out
	}
	before := snapshot()

	prev, err := runReparseFileIntros(ctx, env, reparseResumeKey{}, true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.Selected != 3 || prev.NotSelected != 1 || prev.Examined != 3 || prev.ParseWrites != 2 || prev.Unreparsable[reparseNoTranscript] != 1 {
		t.Fatalf("preview = %+v", prev)
	}
	if f, _ := s.GetBookFileByID(book.ID, ids[0]); f.IntroParse != nil {
		t.Fatal("the preview stored a parse")
	}

	live, err := runReparseFileIntros(ctx, env, reparseResumeKey{}, false)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if live.ParseWrites != 2 || live.Errors != 0 || live.Identity.WouldFill != 1 {
		t.Fatalf("live = %+v", live)
	}
	if !reflect.DeepEqual(live.KindAfter, prev.KindAfter) || !reflect.DeepEqual(live.Identity, prev.Identity) {
		t.Errorf("live and preview disagree:\nlive    %+v %+v\npreview %+v %+v", live.KindAfter, live.Identity, prev.KindAfter, prev.Identity)
	}
	// Every column but IntroParse (and UpdatedAt) is exactly as stored, on the real
	// store, including the fingerprint the whole-row encode carries through.
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("a live run changed a column other than IntroParse:\nbefore %+v\nafter  %+v", before, after)
	}
	f0, _ := s.GetBookFileByID(book.ID, ids[0])
	if f0.IntroParse == nil || f0.IntroParse.Kind != "title_announcement" || f0.IntroParse.Version != transcribe.ClipParserVersion || f0.IntroParse.Title != "The Killing Joke" {
		t.Errorf("stored parse = %+v", f0.IntroParse)
	}
	if f0.TranscribedTitle != nil || f0.TranscribeStatus == nil || *f0.TranscribeStatus != "unparsed" {
		t.Errorf("identity/status touched: title %v status %v", f0.TranscribedTitle, f0.TranscribeStatus)
	}
	if len(f0.AcoustIDFingerprint) != 2048 {
		t.Errorf("fingerprint length = %d, want it carried through the write", len(f0.AcoustIDFingerprint))
	}

	again, err := runReparseFileIntros(ctx, env, reparseResumeKey{}, false)
	if err != nil || again.ParseWrites != 0 || again.Errors != 0 {
		t.Errorf("second live run: writes %d errors %d err %v, want none", again.ParseWrites, again.Errors, err)
	}
}
