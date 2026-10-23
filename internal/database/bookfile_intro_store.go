// file: internal/database/bookfile_intro_store.go
// version: 1.0.2
// guid: 8c1e5a47-2f93-4d06-b7a8-3e90d14c6b25
// last-edited: 2026-10-23

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
	// IntroWriteWrote: the row changed and held no result before, so coverage
	// grew by one.
	IntroWriteWrote
	// IntroWriteReplaced: the row changed and already held a result that this
	// write replaced (a forced re-run). Coverage did not grow.
	IntroWriteReplaced
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
	// Silence marks Transcript as the silence sentinel (an empty clip). A
	// silence result never replaces a transcript the row already holds, even in
	// a forced re-run: "nothing heard in 90 s" must not erase real text a
	// longer clip or an earlier run found.
	Silence bool
	// FillOnly writes a transcript only into a file that has none. A file that
	// gained one between selection and write (another op, a manual apply) keeps
	// it.
	FillOnly bool
}

// Transcribe statuses a stored per-file result can carry. They mirror the
// maintenance package's constants (it cannot be imported from here).
const (
	IntroStatusOK       = "ok"
	IntroStatusUnparsed = "unparsed"
	IntroStatusEmpty    = "empty"
)

// IntroResultPresent is THE definition of "this book_file already carries a
// per-file intro result", shared by the op's selection (BookFileCore) and the
// store's write rules (BookFile) so the two can never disagree about which rows
// are done:
//
//   - the done marker matches the current hash (the op wrote it for exactly
//     these bytes), or
//   - a result written before the marker existed (the book op's single-file
//     migration, an earlier apply): a transcript time and a status that says a
//     transcript was stored (ok, unparsed, or the silence sentinel's empty).
//     Trusted as done; never used as a donor because it names no hash.
//
// A failure status alone (whisper_error, source_file_missing, ...) is not a
// result.
func IntroResultPresent(fileHash, sourceHash string, transcribedAt *time.Time, status *string) bool {
	if fileHash != "" && sourceHash == fileHash {
		return true
	}
	if transcribedAt != nil && status != nil {
		switch *status {
		case IntroStatusOK, IntroStatusUnparsed, IntroStatusEmpty:
			return true
		}
	}
	return false
}

// HasIntroResult reports whether the row carries a per-file intro result.
func (f *BookFile) HasIntroResult() bool {
	return IntroResultPresent(f.FileHash, f.IntroSourceHash, f.IntroTranscribedAt, f.TranscribeStatus)
}

// HasIntroResult reports whether the projection carries a per-file intro result.
func (f *BookFileCore) HasIntroResult() bool {
	return IntroResultPresent(f.FileHash, f.IntroSourceHash, f.IntroTranscribedAt, f.TranscribeStatus)
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

	had := row.HasIntroResult()
	// holdsText: the row carries a transcript even though it does not count as
	// done. A row the book op wrote can hold real text under a failure status
	// (applyOutcome keeps the text and sets whisper_error), and a sentinel with
	// no timestamp or status. Such text must not be overwritten by a fill-only
	// write, and a failure must not stamp this run on it.
	holdsText := row.IntroTranscription != nil
	// A silence result over a stored transcript that is exactly the same
	// sentinel, on a row that is not yet done (the book op wrote the sentinel
	// without a marker): let it write the same text so the marker and status get
	// stamped and the row stops being re-selected on every run.
	stampsSentinel := !had && r.Silence && r.Transcript != nil && holdsText && *row.IntroTranscription == *r.Transcript
	if r.Transcript != nil {
		if r.FillOnly && (had || holdsText) && !stampsSentinel {
			return IntroWriteSkipped, nil
		}
		if r.Silence && holdsText && !stampsSentinel {
			// Silence never replaces a stored transcript (real text or an
			// earlier sentinel), forced run or not.
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
		if stampsSentinel {
			// The sentinel was not written by this run, so the run id is not
			// claimed: clear_run_id must never erase it.
			row.IntroRunOpID = ""
		} else {
			row.IntroRunOpID = r.RunOpID
		}
	} else {
		// A failure never touches a row that holds a result or any transcript:
		// overwriting its status with whisper_error would leave a transcript
		// labelled as a failure, and stamping this run on it would let
		// clear_run_id erase text this run never wrote.
		if had || holdsText {
			return IntroWriteSkipped, nil
		}
		status, detail := r.Status, optString(r.Error)
		if strPtrEqual(row.TranscribeStatus, &status) && strPtrEqual(row.TranscribeError, detail) {
			return IntroWriteSkipped, nil
		}
		// A failure names this run on a row that has no result at all, so that
		// clearing the run also removes the status noise it left.
		row.IntroRunOpID = r.RunOpID
	}
	status := r.Status
	row.TranscribeStatus = &status
	row.TranscribeError = optString(r.Error)
	attempted := r.At
	row.TranscribeAttemptedAt = &attempted
	if r.Done {
		row.IntroSourceHash = r.SourceHash
	}
	res, err := s.writeIntroRow(bookID, fileID, row, "SetBookFileIntro")
	if res == IntroWriteWrote && had {
		res = IntroWriteReplaced
	}
	return res, err
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
