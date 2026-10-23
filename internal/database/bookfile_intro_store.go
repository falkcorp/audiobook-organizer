// file: internal/database/bookfile_intro_store.go
// version: 1.0.0
// guid: 8c1e5a47-2f93-4d06-b7a8-3e90d14c6b25
// last-edited: 2026-10-22

package database

import (
	"fmt"
	"time"
)

// BookFileIntroStore is the narrow capability maintenance.transcribe-file-intros
// writes per-file intro results through, and clears them with. It is a
// capability (resolved with AsCapability, never a bare assertion: the
// production store is wrapped in a decorator that hides capabilities from one)
// rather than methods on Store, so the 398-method interface does not widen for
// one op.
//
// Both methods are field-scoped writers in the PatchBookFileFields mould, not
// UpdateBookFile callers, for three reasons the op depends on:
//
//   - They re-read the row under its lockBookFile stripe and write only the
//     intro group, so a concurrent writer's Duration, Missing or fingerprint
//     is never reverted (the whole-row read-modify-write is what that stripe
//     exists to prevent).
//   - They write the book_file key and the memdb projection and NOTHING else:
//     no secondary-index rewrite (none of the indexed fields changes) and no
//     book-aggregate recompute (nothing the aggregates read changes). Going
//     through ModifyBookFile would recompute the book's Duration/FileSize once
//     per file over a library of hundreds of thousands of files.
//   - ClearBookFileIntro has to write a nil IntroTranscription and a nil
//     IntroMeta, which UpdateBookFile's merge deliberately refuses (they are
//     bfPreserveAlways: a nil there means "slim memdb round trip", never
//     "clear").
type BookFileIntroStore interface {
	SetBookFileIntro(bookID, fileID string, r BookFileIntroResult) (BookFileIntroWrite, error)
	ClearBookFileIntro(bookID, fileID, runOpID string) (before *BookFile, cleared bool, err error)
}

// BookFileIntroWrite is what SetBookFileIntro did with a result.
type BookFileIntroWrite uint8

const (
	// IntroWriteSkipped: nothing was written because the row already says the
	// same thing (a repeated failure, an identical transcript) or, for a
	// FillOnly result, because the row already carries a transcript.
	IntroWriteSkipped BookFileIntroWrite = iota
	// IntroWriteWrote: the row changed.
	IntroWriteWrote
	// IntroWriteStale: the row is gone, or its FileHash is no longer the hash
	// the result was made from. The result describes bytes the row no longer
	// holds, so it is dropped.
	IntroWriteStale
)

// BookFileIntroResult is one file's attempt as the op hands it to the store.
type BookFileIntroResult struct {
	// SourceHash is the FileHash of the bytes that were transcribed. It must
	// equal the stored FileHash and must not be empty.
	SourceHash string
	// RunOpID is the writing operation's id.
	RunOpID string
	// Status is the attempt's TranscribeStatus ("ok", "unparsed", "empty",
	// "whisper_error", "source_file_missing", ...); Error its short detail.
	Status string
	Error  string
	// Transcript is the raw transcript, or the silence sentinel. Nil means the
	// attempt produced none: only the status fields are written and the stored
	// transcript (from an earlier run) is left alone.
	Transcript *string
	// Parsed credits, written exactly as given (empty clears) whenever a
	// Transcript is written, because they describe THAT transcript.
	Title, Author, Narrator, Translator, CoverArtist string
	// Meta is how the clip was made; written with a Transcript.
	Meta *IntroMeta
	// At is the attempt time.
	At time.Time
	// Done stamps IntroSourceHash = SourceHash, the op's done marker. Set it
	// only for outcomes re-running cannot improve (a transcript, or silence).
	Done bool
	// FillOnly writes a transcript only into a file that has none. A file that
	// gained one between selection and write (another op, a manual apply) keeps
	// it.
	FillOnly bool
}

// hasIntroTranscript reports whether a row already carries a per-file result.
func hasIntroTranscript(f *BookFile) bool {
	return f.IntroTranscribedAt != nil || (f.IntroSourceHash != "" && f.IntroSourceHash == f.FileHash)
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// SetBookFileIntro writes one file's intro result. See BookFileIntroStore.
func (s *PebbleStore) SetBookFileIntro(bookID, fileID string, r BookFileIntroResult) (BookFileIntroWrite, error) {
	if r.SourceHash == "" {
		return IntroWriteStale, fmt.Errorf("SetBookFileIntro: %s: empty source hash", fileID)
	}
	unlock := s.lockBookFile(fileID)
	defer unlock()

	row, err := s.getBookFileByID(bookID, fileID)
	if err != nil {
		return IntroWriteSkipped, fmt.Errorf("SetBookFileIntro: read %s: %w", fileID, err)
	}
	if row == nil || row.FileHash != r.SourceHash {
		return IntroWriteStale, nil
	}

	if r.Transcript != nil {
		if r.FillOnly && hasIntroTranscript(row) {
			return IntroWriteSkipped, nil
		}
		status := r.Status
		if strPtrEqual(row.TranscribeStatus, &status) && strPtrEqual(row.IntroTranscription, r.Transcript) &&
			row.IntroSourceHash == r.SourceHash && (!r.Done || row.IntroRunOpID != "") {
			return IntroWriteSkipped, nil
		}
		row.IntroTranscription = r.Transcript
		row.TranscribedTitle = optString(r.Title)
		row.TranscribedAuthor = optString(r.Author)
		row.TranscribedNarrator = optString(r.Narrator)
		row.TranscribedTranslator = optString(r.Translator)
		row.TranscribedCoverArtist = optString(r.CoverArtist)
		at := r.At
		row.IntroTranscribedAt = &at
		row.IntroMeta = r.Meta
		row.IntroRunOpID = r.RunOpID
	} else {
		status, detail := r.Status, optString(r.Error)
		if strPtrEqual(row.TranscribeStatus, &status) && strPtrEqual(row.TranscribeError, detail) {
			return IntroWriteSkipped, nil
		}
		// A failure names this run on a row that has no result at all, so that
		// clearing the run also removes the status noise it left. It never
		// takes over a row that holds an earlier run's result.
		if !hasIntroTranscript(row) {
			row.IntroRunOpID = r.RunOpID
		}
	}
	status := r.Status
	row.TranscribeStatus = &status
	row.TranscribeError = optString(r.Error)
	attempted := r.At
	row.TranscribeAttemptedAt = &attempted
	if r.Done {
		row.IntroSourceHash = r.SourceHash
	}
	return s.writeIntroRow(bookID, fileID, row, "SetBookFileIntro")
}

// ClearBookFileIntro erases the per-file intro result a run wrote: the
// transcript, the parsed credits, the status fields and the provenance, which
// returns the file to "never transcribed". It does nothing, and reports
// cleared=false, unless the stored row still names runOpID, re-checked under
// the row's stripe, so a file a later run rewrote is left alone.
//
// It does NOT restore whatever the file held before the run. A run that only
// filled empty files leaves nothing to restore; a forced re-run
// (only_missing=false) replaced earlier transcripts, and clearing it is
// roll-forward only.
//
// before is the row as it was (nil when the row is gone or names another run),
// which the caller needs to undo the book-level fill the run may have made.
func (s *PebbleStore) ClearBookFileIntro(bookID, fileID, runOpID string) (before *BookFile, cleared bool, err error) {
	if runOpID == "" {
		return nil, false, fmt.Errorf("ClearBookFileIntro: %s: empty run id", fileID)
	}
	unlock := s.lockBookFile(fileID)
	defer unlock()

	row, err := s.getBookFileByID(bookID, fileID)
	if err != nil {
		return nil, false, fmt.Errorf("ClearBookFileIntro: read %s: %w", fileID, err)
	}
	if row == nil || row.IntroRunOpID != runOpID {
		return nil, false, nil
	}
	prior := *row
	row.IntroTranscription = nil
	row.TranscribedTitle = nil
	row.TranscribedAuthor = nil
	row.TranscribedNarrator = nil
	row.TranscribedTranslator = nil
	row.TranscribedCoverArtist = nil
	row.IntroTranscribedAt = nil
	row.TranscribeStatus = nil
	row.TranscribeError = nil
	row.TranscribeAttemptedAt = nil
	row.IntroSourceHash = ""
	row.IntroRunOpID = ""
	row.IntroMeta = nil
	res, err := s.writeIntroRow(bookID, fileID, row, "ClearBookFileIntro")
	if err != nil {
		return nil, res == IntroWriteWrote, err
	}
	return &prior, res == IntroWriteWrote, nil
}

// writeIntroRow commits an edited row: the primary key and memdb only, under
// the caller's stripe. A row deleted meanwhile is reported stale.
func (s *PebbleStore) writeIntroRow(bookID, fileID string, row *BookFile, who string) (BookFileIntroWrite, error) {
	row.UpdatedAt = time.Now()
	data, err := marshalBookFileDropSegs(row)
	if err != nil {
		return IntroWriteSkipped, fmt.Errorf("%s: encode %s: %w", who, fileID, err)
	}
	wrote, err := s.setBookFileRowIfPresent(bookID, bookFileKey(bookID, fileID), data)
	if !bookFileApplied(err) {
		return IntroWriteSkipped, fmt.Errorf("%s: write %s: %w", who, fileID, err)
	}
	if !wrote {
		return IntroWriteStale, nil
	}
	s.UpsertBookFileToMemDB(row)
	if err != nil {
		// Applied but not known durable: memdb already mirrors it; say so.
		return IntroWriteWrote, fmt.Errorf("%s: %s: %w", who, fileID, err)
	}
	return IntroWriteWrote, nil
}
