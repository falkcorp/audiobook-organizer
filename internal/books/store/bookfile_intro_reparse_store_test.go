// file: internal/books/store/bookfile_intro_reparse_store_test.go
// version: 1.2.1
// guid: 9a4f2c68-3e17-4d85-b0c6-5e8d1f7a2b94
// last-edited: 2026-11-17

package store

import (
	"errors"
	"reflect"
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

func okParse(parse IntroParse) func(IntroReparseRow) (*IntroParse, bool) {
	return func(IntroReparseRow) (*IntroParse, bool) { return &parse, true }
}

// The re-parse writes the classification and NOTHING else: not the identity, the
// status, the transcript, the done marker, the run id, the model, nor the times.
func TestReparseBookFileIntro_WritesOnlyTheParse(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One. Written by Frank Miller.", IntroStatusUnparsed, IntroReparseIdentity{})
	before := f.row(t)

	parse := IntroParse{Version: 1, Kind: "title_announcement", Title: "Batman: Year One", Author: "Frank Miller"}
	var seen IntroReparseRow
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (*IntroParse, bool) {
		seen = cur
		return &parse, true
	})
	if err != nil || out != IntroReparseWrote {
		t.Fatalf("ReparseBookFileIntro: out=%v err=%v", out, err)
	}
	if seen.Transcript != "Batman: Year One. Written by Frank Miller." || seen.Identity != (IntroReparseIdentity{}) || seen.Parse != nil {
		t.Errorf("decide saw %+v, want the stored text, no identity and no parse", seen)
	}

	got := f.row(t)
	if got.IntroParse == nil || *got.IntroParse != parse {
		t.Errorf("IntroParse = %+v, want %+v", got.IntroParse, parse)
	}
	// Everything but IntroParse is as stored: compare the rows with the parse
	// blanked on both sides.
	a, b := *before, *got
	a.IntroParse, b.IntroParse = nil, nil
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("the re-parse changed more than IntroParse:\nbefore %+v\nafter  %+v", a, b)
	}
}

// The identity the decision is shown is the stored one, and it is still stored
// after the write.
func TestReparseBookFileIntro_ShowsAndKeepsIdentityAndStatus(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Chapter 4. The Long Halloween.", IntroStatusOK, IntroReparseIdentity{Title: "The Long Halloween", Author: "Jeph Loeb"})

	parse := IntroParse{Version: 1, Kind: "chapter_marker", ChapterNumber: 4, HasChapterNumber: true}
	var seen IntroReparseIdentity
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (*IntroParse, bool) {
		seen = cur.Identity
		return &parse, true
	})
	if err != nil || out != IntroReparseWrote {
		t.Fatalf("ReparseBookFileIntro: out=%v err=%v", out, err)
	}
	if seen != (IntroReparseIdentity{Title: "The Long Halloween", Author: "Jeph Loeb"}) {
		t.Errorf("decide saw identity %+v, want the stored one", seen)
	}
	got := f.row(t)
	if got.TranscribedTitle == nil || *got.TranscribedTitle != "The Long Halloween" || *got.TranscribedAuthor != "Jeph Loeb" || *got.TranscribeStatus != IntroStatusOK {
		t.Errorf("identity/status changed by a re-parse: %v/%v/%v", got.TranscribedTitle, got.TranscribedAuthor, got.TranscribeStatus)
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

	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(IntroReparseRow) (*IntroParse, bool) {
		return nil, false
	})
	if err != nil || out != IntroReparseSkipped {
		t.Fatalf("out=%v err=%v, want skipped", out, err)
	}
	if got := f.row(t); !got.UpdatedAt.Equal(before.UpdatedAt) || got.IntroParse != nil {
		t.Errorf("a declined re-parse wrote the row: updated %v -> %v, parse %+v", before.UpdatedAt, got.UpdatedAt, got.IntroParse)
	}

	parse := IntroParse{Version: 1, Kind: "narration"}
	if _, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okParse(parse)); err != nil {
		t.Fatal(err)
	}
	var stored *IntroParse
	if _, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (*IntroParse, bool) {
		stored = cur.Parse
		return nil, false
	}); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != parse {
		t.Errorf("decide saw stored parse %+v, want %+v", stored, parse)
	}
}

// A decision that says "write" but hands back no parse is a caller bug and is
// refused loudly, not written.
func TestReparseBookFileIntro_WriteWithoutParseIsRefused(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Hush.", IntroStatusUnparsed, IntroReparseIdentity{})
	_, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(IntroReparseRow) (*IntroParse, bool) {
		return nil, true
	})
	if err == nil {
		t.Fatal("a write decision with no parse was accepted")
	}
	if got := f.row(t); got.IntroParse != nil {
		t.Errorf("IntroParse = %+v, want the refused decision not to have written", got.IntroParse)
	}
}

// A row with no transcript is never offered to decide; a missing row is Gone.
func TestReparseBookFileIntro_NoTranscriptAndGone(t *testing.T) {
	f := newCASFixture(t)
	called := false
	decide := func(IntroReparseRow) (*IntroParse, bool) { called = true; return nil, false }

	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, decide)
	if err != nil || out != IntroReparseNoTranscript || called {
		t.Errorf("no transcript: out=%v err=%v called=%v", out, err, called)
	}
	// A transcript that is present but empty is the same class as an absent one.
	seedReparseRow(t, f, "", IntroStatusUnparsed, IntroReparseIdentity{})
	if row := f.row(t); row.IntroTranscription == nil || *row.IntroTranscription != "" {
		t.Fatalf("fixture: want a present, empty transcript, got %v", row.IntroTranscription)
	}
	out, err = f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, decide)
	if err != nil || out != IntroReparseNoTranscript || called {
		t.Errorf("empty transcript: out=%v err=%v called=%v", out, err, called)
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
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(IntroReparseRow) (*IntroParse, bool) {
		runs++
		return &parse, true
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
	out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, func(cur IntroReparseRow) (*IntroParse, bool) {
		texts = append(texts, cur.Transcript)
		p := IntroParse{Version: 1, Kind: "narration", Title: cur.Transcript}
		return &p, true
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
	_, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okParse(parse))
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	bookFileBeforeCommitHook = nil
	if got := f.row(t); got.IntroParse != nil {
		t.Errorf("IntroParse = %+v, want nothing written after the budget ran out", got.IntroParse)
	}
}

// The row is encoded when the write happens: later changes to the caller's value
// never reach the stored row, so one decision value can be reused across rows.
func TestReparseBookFileIntro_StoresACopy(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One.", IntroStatusUnparsed, IntroReparseIdentity{})
	parse := IntroParse{Version: 1, Kind: "narration"}
	if _, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okParse(parse)); err != nil {
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

// n4. The live write re-encodes the WHOLE row, fingerprint included, and stores
// it under the same key. This pins both halves of what that costs, with a
// realistic fingerprint: the stored row grows by only the encoded IntroParse, and
// the bytes handed to Pebble per write are the full row. The numbers are logged
// for the PR.
func TestReparseBookFileIntro_WriteSizeIsTheWholeRowAndGrowthIsTheParse(t *testing.T) {
	f := newCASFixture(t)
	seedReparseRow(t, f, "Batman: Year One. Written by Frank Miller. Read by Selina Kyle.", IntroStatusOK, IntroReparseIdentity{Title: "Batman: Year One", Author: "Frank Miller", Narrator: "Selina Kyle"})
	// A 6 hour book's whole-file chromaprint is about 8 hashes a second of 4 bytes:
	// 6 * 3600 * 8 * 4 = 691,200 bytes, stored base64 in the JSON row.
	const fingerprintBytes = 6 * 3600 * 8 * 4
	f.strayWrite(t, func(r *BookFile) { r.AcoustIDFingerprint = make([]byte, fingerprintBytes) })

	size := func() int {
		v, closer, err := f.s.db.Get(bookFileKey(f.book.ID, f.file.ID))
		if err != nil {
			t.Fatal(err)
		}
		defer closer.Close()
		return len(v)
	}
	before := size()

	parse := IntroParse{
		Version: 1, Kind: "title_announcement", Title: "Batman: Year One", Author: "Frank Miller", Narrator: "Selina Kyle",
		Publisher: "Gotham Audio", Marker: "Part 1", ChapterTitle: "The Long Halloween",
	}
	if out, err := f.s.ReparseBookFileIntro(f.book.ID, f.file.ID, okParse(parse)); err != nil || out != IntroReparseWrote {
		t.Fatalf("out=%v err=%v", out, err)
	}
	after := size()
	growth := after - before
	t.Logf("stored row: %d bytes before, %d after; one write hands Pebble %d bytes to add %d bytes of live data", before, after, after, growth)

	if before < fingerprintBytes {
		t.Fatalf("fixture row is %d bytes, smaller than its fingerprint (%d)", before, fingerprintBytes)
	}
	// A fully populated parse (every text field set) must stay small: the growth of
	// the live set is 66,000 of these.
	if growth <= 0 || growth > 512 {
		t.Errorf("stored row grew by %d bytes, want 1..512", growth)
	}
	// The write carries the whole row, so its size tracks the fingerprint, not the parse.
	if after < fingerprintBytes {
		t.Errorf("written row is %d bytes, want the whole row (fingerprint %d)", after, fingerprintBytes)
	}
}
