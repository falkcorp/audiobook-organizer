// file: internal/books/store/bookfile_intro_reparse_store.go
// version: 1.0.0
// guid: 4c9e1b72-6d38-4a05-9f4e-2b7a8d1c5e63
// last-edited: 2026-11-17

package store

import (
	"errors"
	"fmt"
)

// BookFileIntroReparser is the narrow capability maintenance.reparse-file-intros
// writes through. Like BookFileIntroStore it is a capability resolved with
// AsCapability, not a method on Store, and a field-scoped writer in the
// PatchBookFileFields mould: it re-reads the row under its lockBookFile stripe,
// writes only the intro-parse group and the identity/status columns the caller's
// decision names, and commits as a compare-and-swap on the bytes read
// (writeIntroRow), so a concurrent writer's Duration, Missing or fingerprint is
// never reverted. After maxStageRetries lost races it returns an error wrapping
// ErrBookFileConflict and writes nothing.
//
// It deliberately writes NONE of the provenance a transcription run owns:
// IntroTranscription, IntroTranscribedAt, IntroSourceHash, IntroRunOpID,
// IntroMeta, TranscribeError, TranscribeAttemptedAt and the timeout strikes are
// all left as stored. A re-parse is not a run, and clear_run_id must neither
// reach what it wrote nor lose what it did not.
type BookFileIntroReparser interface {
	// ReparseBookFileIntro offers decide the row as it stands NOW (re-read under
	// the stripe, on every retry) and applies the patch decide returns. decide
	// returning ok=false writes nothing. decide may run more than once for one
	// call (a lost race re-reads and re-decides), so it must be a pure function of
	// its argument.
	//
	// A row that holds no transcript is never offered to decide: there is nothing
	// to re-parse.
	ReparseBookFileIntro(bookID, fileID string, decide func(IntroReparseRow) (IntroReparsePatch, bool)) (IntroReparseOutcome, error)
}

// IntroReparseIdentity is the five identity columns of a per-file intro result,
// in their stored (pointer) shape flattened: "" means the column is unset.
type IntroReparseIdentity struct {
	Title, Author, Narrator, Translator, CoverArtist string
}

// IntroReparseRow is what a decision is made from: the stored transcript and the
// columns a re-parse may change, as they are right now.
type IntroReparseRow struct {
	// Transcript is the stored raw text (never empty here).
	Transcript string
	// Status is the stored TranscribeStatus ("" when unset).
	Status   string
	Identity IntroReparseIdentity
	// Parse is the stored classification, nil when the row was never classified.
	Parse *IntroParse
}

// IntroReparsePatch is what a re-parse writes.
type IntroReparsePatch struct {
	// Parse replaces the stored classification. Required: a patch that changes
	// nothing is expressed as ok=false from the decision.
	Parse *IntroParse
	// Identity, when non-nil, replaces the five identity columns (an empty member
	// clears its column, so the caller passes the stored value for a field it
	// keeps).
	Identity *IntroReparseIdentity
	// Status, when non-empty, replaces TranscribeStatus.
	Status string
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
func (s *PebbleStore) ReparseBookFileIntro(bookID, fileID string, decide func(IntroReparseRow) (IntroReparsePatch, bool)) (IntroReparseOutcome, error) {
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
func (s *PebbleStore) reparseBookFileIntroOnce(bookID, fileID string, decide func(IntroReparseRow) (IntroReparsePatch, bool)) (IntroReparseOutcome, error) {
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

	cur := IntroReparseRow{Transcript: *row.IntroTranscription, Parse: row.IntroParse}
	if row.TranscribeStatus != nil {
		cur.Status = *row.TranscribeStatus
	}
	cur.Identity = IntroReparseIdentity{
		Title:       derefString(row.TranscribedTitle),
		Author:      derefString(row.TranscribedAuthor),
		Narrator:    derefString(row.TranscribedNarrator),
		Translator:  derefString(row.TranscribedTranslator),
		CoverArtist: derefString(row.TranscribedCoverArtist),
	}
	patch, ok := decide(cur)
	if !ok {
		return IntroReparseSkipped, nil
	}
	if patch.Parse == nil {
		return IntroReparseSkipped, fmt.Errorf("ReparseBookFileIntro: %s: patch without a parse", fileID)
	}

	// The parse is cloned: the caller's patch may be reused across retries and
	// across rows, and the stored value must not alias it.
	parse := *patch.Parse
	row.IntroParse = &parse
	if id := patch.Identity; id != nil {
		row.TranscribedTitle = optString(id.Title)
		row.TranscribedAuthor = optString(id.Author)
		row.TranscribedNarrator = optString(id.Narrator)
		row.TranscribedTranslator = optString(id.Translator)
		row.TranscribedCoverArtist = optString(id.CoverArtist)
	}
	if patch.Status != "" {
		status := patch.Status
		row.TranscribeStatus = &status
	}

	res, err := s.writeIntroRow(bookID, fileID, row, raw, "ReparseBookFileIntro")
	switch res {
	case IntroWriteStale:
		return IntroReparseGone, err
	case IntroWriteWrote:
		return IntroReparseWrote, err
	}
	return IntroReparseSkipped, err
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
