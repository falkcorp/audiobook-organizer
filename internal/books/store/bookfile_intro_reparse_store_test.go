// file: internal/books/store/bookfile_intro_reparse_store_test.go
// version: 1.0.0
// guid: 9a4f2c68-3e17-4d85-b0c6-5e8d1f7a2b94
// last-edited: 2026-11-17

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// seedReparseRow gives the CAS fixture's file a stored intro result: text,
// identity, status, provenance. The values are Batman material only.
func seedReparseRow(t *testing.T, f *casFixture, text, status string, id IntroReparseIdentity) {
	t.Helper()
	res, err := f.s.SetBookFileIntro(f.book.ID, f.file.ID, BookFileIntroResult{
		SourceHash: "gotham-hash-old", RunOpID: "op-batman", Status: status, Transcript: &text, Done: true,
		At:    time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Title: id.Title, Author: id.Author, Narrator: id.Narrator,
		Meta: &IntroMeta{Model: "model-x", ClipLengthSec: 90},
	})
	if err != nil || res != IntroWriteWrote {
		t.Fatalf("seeding the intro: res=%v err=%v", res, err)
	}
}

func okPatch(parse IntroParse) func(IntroReparseRow) (IntroReparsePatch, bool) {
	return func(IntroReparseRow) (IntroReparsePatch, bool) {
		return IntroReparsePatch{Parse: &parse}, true
	}
}

// The re-parse writes the classification, the identity and the status it is given
// and NOTHING of the run's provenance: not the transcript, the done marker, the
// run id, the model, nor the times.
func TestReparseBookFileIntro_WritesOnlyWhatThePatchNames(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One. Written by Frank Miller.", IntroStatusUnparsed, IntroReparseIdentity{})
	before := f.row(t)

	parse := IntroParse{Version: 1, Kind: "title_announcement", Title: "Batman: Year One", Author: "Frank Miller"}
	var seen IntroReparseRow
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (IntroReparsePatch, bool) {
		seen = cur
		return IntroReparsePatch{
			Parse:    &parse,
			Identity: &IntroReparseIdentity{Title: "Batman: Year One", Author: "Frank Miller"},
			Status:   IntroStatusOK,
		}, true
	})
	if err != nil || out != IntroReparseWrote {
		t.Fatalf("ReparseBookFileIntro: out=%v err=%v", out, err)
	}
	if seen.Transcript != "Batman: Year One. Written by Frank Miller." || seen.Status != IntroStatusUnparsed || seen.Parse != nil {
		t.Errorf("decide saw %+v, want the stored text, the unparsed status and no parse", seen)
	}

	got := f.row(t)
	if got.IntroParse == nil || *got.IntroParse != parse {
		t.Errorf("IntroParse = %+v, want %+v", got.IntroParse, parse)
	}
	if got.TranscribedTitle == nil || *got.TranscribedTitle != "Batman: Year One" ||
		got.TranscribedAuthor == nil || *got.TranscribedAuthor != "Frank Miller" || got.TranscribedNarrator != nil {
		t.Errorf("identity = %v/%v/%v, want the patch's title and author and no narrator", got.TranscribedTitle, got.TranscribedAuthor, got.TranscribedNarrator)
	}
	if got.TranscribeStatus == nil || *got.TranscribeStatus != IntroStatusOK {
		t.Errorf("status = %v, want ok", got.TranscribeStatus)
	}
	// Provenance untouched.
	if *got.IntroTranscription != *before.IntroTranscription || got.IntroSourceHash != before.IntroSourceHash ||
		got.IntroRunOpID != before.IntroRunOpID || *got.IntroMeta != *before.IntroMeta ||
		!got.IntroTranscribedAt.Equal(*before.IntroTranscribedAt) || !got.TranscribeAttemptedAt.Equal(*before.TranscribeAttemptedAt) {
		t.Errorf("the re-parse rewrote run provenance:\nbefore %+v\nafter  %+v", before, got)
	}
}

// A patch with no identity and no status leaves both columns exactly as stored.
func TestReparseBookFileIntro_ParseOnlyKeepsIdentityAndStatus(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Chapter 4. The Long Halloween.", IntroStatusOK, IntroReparseIdentity{Title: "The Long Halloween", Author: "Jeph Loeb"})

	parse := IntroParse{Version: 1, Kind: "chapter_marker", ChapterNumber: 4, HasChapterNumber: true}
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okPatch(parse))
	if err != nil || out != IntroReparseWrote {
		t.Fatalf("ReparseBookFileIntro: out=%v err=%v", out, err)
	}
	got := f.row(t)
	if got.TranscribedTitle == nil || *got.TranscribedTitle != "The Long Halloween" || *got.TranscribedAuthor != "Jeph Loeb" || *got.TranscribeStatus != IntroStatusOK {
		t.Errorf("identity/status changed by a parse-only patch: %v/%v/%v", got.TranscribedTitle, got.TranscribedAuthor, got.TranscribeStatus)
	}
	if got.IntroParse == nil || got.IntroParse.Kind != "chapter_marker" || got.IntroParse.ChapterNumber != 4 {
		t.Errorf("IntroParse = %+v", got.IntroParse)
	}
}

// decide returning false writes nothing, and the stored parse handed to the next
// decision is the one that was written.
func TestReparseBookFileIntro_DeclinedWritesNothing(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Hush.", IntroStatusUnparsed, IntroReparseIdentity{})
	before := f.row(t)

	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(IntroReparseRow) (IntroReparsePatch, bool) {
		return IntroReparsePatch{}, false
	})
	if err != nil || out != IntroReparseSkipped {
		t.Fatalf("out=%v err=%v, want skipped", out, err)
	}
	if got := f.row(t); !got.UpdatedAt.Equal(before.UpdatedAt) || got.IntroParse != nil {
		t.Errorf("a declined re-parse wrote the row: updated %v -> %v, parse %+v", before.UpdatedAt, got.UpdatedAt, got.IntroParse)
	}

	parse := IntroParse{Version: 1, Kind: "narration"}
	if _, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okPatch(parse)); err != nil {
		t.Fatal(err)
	}
	var stored *IntroParse
	if _, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (IntroReparsePatch, bool) {
		stored = cur.Parse
		return IntroReparsePatch{}, false
	}); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != parse {
		t.Errorf("decide saw stored parse %+v, want %+v", stored, parse)
	}
}

// A patch without a parse is a caller bug and is refused loudly, not written.
func TestReparseBookFileIntro_PatchWithoutParseIsRefused(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Hush.", IntroStatusUnparsed, IntroReparseIdentity{})
	_, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(IntroReparseRow) (IntroReparsePatch, bool) {
		return IntroReparsePatch{Status: IntroStatusOK}, true
	})
	if err == nil {
		t.Fatal("a patch with no parse was accepted")
	}
	if got := f.row(t); *got.TranscribeStatus != IntroStatusUnparsed {
		t.Errorf("status = %q, want the refused patch not to have written", *got.TranscribeStatus)
	}
}

// A row with no transcript is never offered to decide; a missing row is Gone.
func TestReparseBookFileIntro_NoTranscriptAndGone(t *testing.T) {
	f := newCASFixture(t)
	called := false
	decide := func(IntroReparseRow) (IntroReparsePatch, bool) { called = true; return IntroReparsePatch{}, false }

	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, decide)
	if err != nil || out != IntroReparseNoTranscript || called {
		t.Errorf("no transcript: out=%v err=%v called=%v", out, err, called)
	}
	out, err = f.s.ReparseBookFileIntro(f.book.ID, "no-such-file", decide)
	if err != nil || out != IntroReparseGone || called {
		t.Errorf("missing row: out=%v err=%v called=%v", out, err, called)
	}
}

// The decision is re-made on the fresh row after a lost race: a concurrent
// writer's Duration survives and decide runs again on the row as it now is.
func TestReparseBookFileIntro_RedecidesOnTheFreshRow(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One.", IntroStatusUnparsed, IntroReparseIdentity{})
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-old", 250, 2500) })

	runs := 0
	parse := IntroParse{Version: 1, Kind: "garbage"}
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(IntroReparseRow) (IntroReparsePatch, bool) {
		runs++
		return IntroReparsePatch{Parse: &parse}, true
	})
	if err != nil || out != IntroReparseWrote {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if runs != 2 {
		t.Errorf("decide ran %d times, want 2 (the lost race re-reads and re-decides)", runs)
	}
	got := f.row(t)
	if durSec := durtest.Sec(got.Duration); durSec != 250 || got.FileSize != 2500 {
		t.Errorf("row = %d s / %d bytes, want the concurrent writer's 250 / 2500 kept", durSec, got.FileSize)
	}
	if got.IntroParse == nil || got.IntroParse.Kind != "garbage" {
		t.Errorf("IntroParse = %+v, want the write to land on the fresh row", got.IntroParse)
	}
}

// If the concurrent writer replaced the transcript, the retry decides from the
// NEW text: a verdict about the old text is never stamped on it.
func TestReparseBookFileIntro_DecidesFromTheTextTheRowHoldsNow(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Old text about Gotham.", IntroStatusUnparsed, IntroReparseIdentity{})
	injectBeforeCommit(t, 1, func(int) {
		f.strayWrite(t, func(r *BookFile) {
			text := "New text about Arkham."
			r.IntroTranscription = &text
		})
	})

	var texts []string
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (IntroReparsePatch, bool) {
		texts = append(texts, cur.Transcript)
		p := IntroParse{Version: 1, Kind: "narration", Title: cur.Transcript}
		return IntroReparsePatch{Parse: &p}, true
	})
	if err != nil || out != IntroReparseWrote {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if len(texts) != 2 || texts[0] != "Old text about Gotham." || texts[1] != "New text about Arkham." {
		t.Fatalf("decide saw %q, want the old text then the replaced one", texts)
	}
	if got := f.row(t); got.IntroParse == nil || got.IntroParse.Title != "New text about Arkham." {
		t.Errorf("IntroParse = %+v, want the parse of the new text", got.IntroParse)
	}
}

// A writer that wins every round exhausts the budget: ErrBookFileConflict and
// nothing written.
func TestReparseBookFileIntro_GivesUpAfterTheBudget(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One.", IntroStatusUnparsed, IntroReparseIdentity{})
	injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, "gotham-hash-old", 100+i, 1000) })

	parse := IntroParse{Version: 1, Kind: "garbage"}
	_, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okPatch(parse))
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	bookFileBeforeCommitHook = nil
	if got := f.row(t); got.IntroParse != nil {
		t.Errorf("IntroParse = %+v, want nothing written after the budget ran out", got.IntroParse)
	}
}

// The patch's parse is copied: later changes to the caller's value never reach
// the stored row, and one patch value can be reused across rows.
func TestReparseBookFileIntro_StoresACopy(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One.", IntroStatusUnparsed, IntroReparseIdentity{})
	parse := IntroParse{Version: 1, Kind: "narration"}
	if _, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okPatch(parse)); err != nil {
		t.Fatal(err)
	}
	parse.Kind = "garbage"
	if got := f.row(t); got.IntroParse.Kind != "narration" {
		t.Errorf("stored kind = %q, want it unaffected by the caller's later change", got.IntroParse.Kind)
	}
}

// SetBookFileIntro stores the parse with the transcript it describes, and a
// replacement transcript with no parse clears the old one: a stored parse always
// describes the stored text.
func TestSetBookFileIntro_ParseTravelsWithTheTranscript(t *testing.T) {
	f := newCASFixture(t)
	first := "Batman: Year One. Written by Frank Miller."
	p := &IntroParse{Version: 1, Kind: "title_announcement", Title: "Batman: Year One"}
	if res, err := f.s.SetBookFileIntro(f.book.ID, f.file.ID, BookFileIntroResult{
		SourceHash: "gotham-hash-old", RunOpID: "op-1", Status: IntroStatusOK, Transcript: &first, Done: true, Parse: p,
	}); err != nil || res != IntroWriteWrote {
		t.Fatalf("first write: res=%v err=%v", res, err)
	}
	if got := f.row(t); got.IntroParse == nil || *got.IntroParse != *p {
		t.Fatalf("IntroParse = %+v, want %+v", got.IntroParse, p)
	}

	second := "Chapter 2."
	if res, err := f.s.SetBookFileIntro(f.book.ID, f.file.ID, BookFileIntroResult{
		SourceHash: "gotham-hash-old", RunOpID: "op-2", Status: IntroStatusUnparsed, Transcript: &second, Done: true,
	}); err != nil || res != IntroWriteReplaced {
		t.Fatalf("second write: res=%v err=%v", res, err)
	}
	if got := f.row(t); got.IntroParse != nil {
		t.Errorf("IntroParse = %+v, want the old text's parse gone with the old text", got.IntroParse)
	}
}

// Clearing a run removes the classification with the transcript it described.
func TestClearBookFileIntro_ClearsTheParse(t *testing.T) {
	f := newCASFixture(t)
	text := "Batman: Year One."
	p := &IntroParse{Version: 1, Kind: "title_announcement", Title: "Batman: Year One"}
	if _, err := f.s.SetBookFileIntro(f.book.ID, f.file.ID, BookFileIntroResult{
		SourceHash: "gotham-hash-old", RunOpID: "op-1", Status: IntroStatusOK, Transcript: &text, Done: true, Parse: p,
	}); err != nil {
		t.Fatal(err)
	}
	if _, cleared, err := f.s.ClearBookFileIntro(f.book.ID, f.file.ID, "op-1"); err != nil || !cleared {
		t.Fatalf("clear: cleared=%v err=%v", cleared, err)
	}
	if got := f.row(t); got.IntroParse != nil || got.IntroTranscription != nil {
		t.Errorf("row after clear: parse %+v transcript %v, want both gone", got.IntroParse, got.IntroTranscription)
	}
}
