// file: internal/books/store/bookfile_intro_reparse_store.go
// version: 1.3.0
// guid: 4c9e1b72-6d38-4a05-9f4e-2b7a8d1c5e63
// last-edited: 2026-11-17

package store

import (
	"errors"
	"fmt"
	"time"
)

// BookFileIntroReparser is the narrow capability maintenance.reparse-file-intros
// writes through. Like BookFileIntroStore it is a capability resolved with
// AsCapability, not a method on Store, and a field-scoped writer in the
// PatchBookFileFields mould: it re-reads the row under its lockBookFile stripe,
// writes ONLY the intro-parse group (IntroParse), and commits as a
// compare-and-swap on the bytes read (writeIntroRow), so a concurrent writer's
// Duration, Missing or fingerprint is never reverted. After maxStageRetries lost
// races it returns an error wrapping ErrBookFileConflict and writes nothing.
//
// It deliberately writes NONE of the identity and none of the provenance a
// transcription run owns: TranscribedTitle/Author/Narrator/Translator/CoverArtist,
// TranscribeStatus, IntroTranscription, IntroTranscribedAt, IntroSourceHash,
// IntroRunOpID, IntroMeta, TranscribeError, TranscribeAttemptedAt and the timeout
// strikes are all left as stored. A re-parse is not a run, and clear_run_id must
// neither reach what it wrote nor lose what it did not.
type BookFileIntroReparser interface {
	// ReparseBookFileIntro offers decide the row as it stands NOW (re-read under
	// the stripe, on every retry) and stores the parse decide returns. decide
	// returning ok=false writes nothing. decide may run more than once for one
	// call (a lost race re-reads and re-decides), so it must be a pure function of
	// its argument.
	//
	// A row that holds no transcript is never offered to decide: there is nothing
	// to re-parse.
	ReparseBookFileIntro(bookID, fileID string, decide func(IntroReparseRow) (*IntroParse, bool)) (IntroReparseOutcome, error)
}

// IntroReparseIdentity is the five identity columns of a per-file intro result,
// in their stored (pointer) shape flattened: "" means the column is unset. A
// re-parse reads them (to measure how its parse compares) and never writes them.
type IntroReparseIdentity struct {
	Title, Author, Narrator, Translator, CoverArtist string
}

// IntroReparseRow is what a decision is made from: the stored transcript and the
// columns the decision may read, as they are right now.
type IntroReparseRow struct {
	// Transcript is the stored raw text (never empty here).
	Transcript string
	// Identity is the stored identity, read-only.
	Identity IntroReparseIdentity
	// Parse is the stored classification, nil when the row was never classified.
	Parse *IntroParse
	// SizeWith is the size, in bytes, the row will have once stored with the given
	// parse: this very row, the timestamp this attempt will stamp, the encoding the
	// write uses. It is exact for the write that follows in the same attempt. Nil from
	// a writer that cannot size the row.
	SizeWith func(IntroParse) (int, error)
}

// IntroReparseOutcome is what ReparseBookFileIntro did.
type IntroReparseOutcome uint8

const (
	// IntroReparseSkipped: decide returned ok=false; nothing was written.
	IntroReparseSkipped IntroReparseOutcome = iota
	// IntroReparseWrote: the row changed.
	IntroReparseWrote
	// IntroReparseGone: the row no longer exists.
	IntroReparseGone
	// IntroReparseNoTranscript: the row holds no transcript, so there was nothing
	// to re-parse and decide was not called.
	IntroReparseNoTranscript
)

// ReparseBookFileIntro implements BookFileIntroReparser.
func (s *PebbleStore) ReparseBookFileIntro(bookID, fileID string, decide func(IntroReparseRow) (*IntroParse, bool)) (IntroReparseOutcome, error) {
	unlock := s.lockBookFile(fileID)
	defer unlock()

	book := bookID
	out, err := rmwBookFile(&book, fileID, func() (IntroReparseOutcome, error) {
		return s.reparseBookFileIntroOnce(bookID, fileID, decide)
	})
	if errors.Is(err, ErrBookFileConflict) {
		return IntroReparseSkipped, fmt.Errorf("ReparseBookFileIntro: write %s: %w", fileID, err)
	}
	return out, err
}

// reparseBookFileIntroOnce is one read-decide-commit pass. Like
// setBookFileIntroOnce it returns errBookFileRowVanished when the commit found
// the row changed since the read, and the caller re-reads and decides again.
func (s *PebbleStore) reparseBookFileIntroOnce(bookID, fileID string, decide func(IntroReparseRow) (*IntroParse, bool)) (IntroReparseOutcome, error) {
	row, raw, err := s.readBookFileRow(bookID, fileID)
	if err != nil {
		return IntroReparseSkipped, fmt.Errorf("ReparseBookFileIntro: read %s: %w", fileID, err)
	}
	if row == nil {
		return IntroReparseGone, nil
	}
	if row.IntroTranscription == nil || *row.IntroTranscription == "" {
		return IntroReparseNoTranscript, nil
	}

	// One timestamp per attempt: SizeWith encodes the row with it and the write
	// stamps it, so the size decide reports is the size stored.
	at := s.introNow()
	cur := IntroReparseRow{Transcript: *row.IntroTranscription, Parse: row.IntroParse}
	sized := *row
	cur.SizeWith = func(p IntroParse) (int, error) { return IntroParseRowBytes(&sized, p, at) }
	cur.Identity = IntroReparseIdentity{
		Title:       derefString(row.TranscribedTitle),
		Author:      derefString(row.TranscribedAuthor),
		Narrator:    derefString(row.TranscribedNarrator),
		Translator:  derefString(row.TranscribedTranslator),
		CoverArtist: derefString(row.TranscribedCoverArtist),
	}
	next, ok := decide(cur)
	if !ok {
		return IntroReparseSkipped, nil
	}
	if next == nil {
		return IntroReparseSkipped, fmt.Errorf("ReparseBookFileIntro: %s: decision without a parse", fileID)
	}

	// Nothing else on the row is assigned, so the encoded row differs from the
	// stored one only in IntroParse. The row is encoded before this call returns,
	// so the caller's value is never retained.
	row.IntroParse = next

	res, err := s.writeIntroRow(bookID, fileID, row, raw, "ReparseBookFileIntro", at)
	switch res {
	case IntroWriteStale:
		return IntroReparseGone, err
	case IntroWriteWrote:
		return IntroReparseWrote, err
	}
	return IntroReparseSkipped, err
}

// IntroParseRowBytes is the size, in bytes, of row as ReparseBookFileIntro stores
// it with parse as its IntroParse and at as its UpdatedAt: the same encoding the write
// uses (marshalBookFileDropSegs), so with the timestamp the write stamps the figure is
// the stored length, not an estimate of it. It never mutates row: the parse and the
// stamp are set on a copy.
//
// A dry run cannot know the timestamp a later write will stamp, and a time.Time
// encodes with its trailing fractional zeros trimmed (RFC3339Nano), so its width
// varies by up to 10 bytes (a whole second drops the dot and all nine digits). A
// preview passes the current time and its figure is within 10 bytes per row of what a
// write then stores; a live write sizes through IntroReparseRow.SizeWith instead,
// which is exact.
func IntroParseRowBytes(row *BookFile, parse IntroParse, at time.Time) (int, error) {
	c := *row
	c.IntroParse = &parse
	c.UpdatedAt = at
	b, err := marshalBookFileDropSegs(&c)
	if err != nil {
		return 0, fmt.Errorf("IntroParseRowBytes: encode %s: %w", row.ID, err)
	}
	return len(b), nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
