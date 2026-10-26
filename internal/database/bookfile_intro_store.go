// file: internal/database/bookfile_intro_store.go
// version: 1.2.0
// guid: 8c1e5a47-2f93-4d06-b7a8-3e90d14c6b25
// last-edited: 2026-10-26

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
// SetBookFileIntro and ClearBookFileIntro are field-scoped writers in the PatchBookFileFields mould, not
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
//
// RecordIntroTimeout and ClearIntroTimeouts are the exception, on purpose: they
// go through ModifyBookFile. They write a handful of rows per run (one per
// timed-out file, not one per file in the library), so the book-aggregate
// recompute that rules ModifyBookFile out for the bulk writers costs nothing
// here, and in return they share the stripe-held read-modify-write every other
// ModifyBookFile caller uses (one callback run under the row's stripe, not a
// compare-and-swap) instead of adding one more raw read-modify-write. The
// scanner's batch upsert takes no stripe, so the ops still wait out a running
// scan before they write.
type BookFileIntroStore interface {
	SetBookFileIntro(bookID, fileID string, r BookFileIntroResult) (BookFileIntroWrite, error)
	ClearBookFileIntro(bookID, fileID, runOpID string) (before *BookFile, cleared bool, err error)
	// RecordIntroTimeout counts one transcribe_timeout strike against a file and,
	// at the limit, makes it a permanent per-file result. See IntroTimeoutStrike.
	RecordIntroTimeout(bookID, fileID string, s IntroTimeoutStrike) (IntroTimeoutOutcome, error)
	// ClearIntroTimeouts drops a file's timeout strikes (and a transcribe_timeout
	// verdict they produced) once the audio has been decoded after all. It does
	// nothing unless the stored FileHash and the strikes' hash both equal
	// sourceHash. cleared reports whether anything was written.
	ClearIntroTimeouts(bookID, fileID, sourceHash string) (cleared bool, err error)
}

// IntroTimeoutStrike is one file whose decode ran past the worker's per-clip
// deadline. The worker killed and restarted its decoder and reported a
// transcribe_timeout for exactly this file: a verdict about its audio, not an
// outage. One strike is retried next run (a transient stall also looks like
// this); Limit strikes make the file permanently skipped, so one clip that
// sends the decoder into an endless loop costs a bounded number of attempts
// instead of wedging a worker every run.
type IntroTimeoutStrike struct {
	// SourceHash is the FileHash of the bytes that timed out. It must equal the
	// stored FileHash and must not be empty.
	SourceHash string
	RunOpID    string
	// Detail is the worker's short error text.
	Detail string
	At     time.Time
	// Limit is the strike count at which the file becomes permanent (>= 1).
	Limit int
	// MinGap is the least time between two counted strikes on the same bytes. A
	// run id only separates two runs of ONE op; the book op and the per-file op
	// each have their own ids, so a single stall that both of them hit would
	// otherwise count twice. 0 disables the check.
	MinGap time.Duration
}

// IntroTimeoutOutcome is what RecordIntroTimeout did.
type IntroTimeoutOutcome struct {
	// Strikes is the file's strike count after this call.
	Strikes int
	// Permanent: Strikes reached the limit and the file now carries the
	// transcribe_timeout status.
	Permanent bool
	// Skipped: nothing was written because the row already holds a transcript or
	// result (a timeout says nothing about a file that has one), or the row is
	// gone / no longer has the hash that timed out (Stale).
	Skipped bool
	Stale   bool
	// HeldText and HeldStatus are what the row held when Skipped is true because
	// it already carries a result: the stored transcript ("" when it has none) and
	// the stored TranscribeStatus ("" when unset), read under the same row lock as
	// the refusal. A caller that needs the file's real answer (the book op's retry
	// 2) uses them instead of guessing. Empty for Stale and for every outcome that
	// is not a held-result refusal.
	HeldText, HeldStatus string
	// TooSoon: the previous strike on these bytes was counted less than the
	// caller's MinGap ago (by any run), so this one is the same stall seen again
	// and nothing was written. Strikes/Permanent are the stored values.
	TooSoon bool
	// SameRun: this run already counted a strike for these bytes, so nothing was
	// written and Strikes/Permanent are the stored values. A strike is one per
	// (file, run); callers treat SameRun as "deferred", never as a new strike.
	SameRun bool
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
	// IntroStatusTranscribeTimeout: the decoder timed out on this file at least
	// Limit times (see IntroTimeoutStrike). Permanent: both ops skip it.
	IntroStatusTranscribeTimeout = "transcribe_timeout"
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
		// A transcript supersedes any timeout record: the file decodes.
		row.IntroTimeouts, row.IntroTimeoutHash, row.IntroTimeoutRunID, row.IntroTimeoutAt = 0, "", "", nil
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
	row.IntroTimeouts, row.IntroTimeoutHash, row.IntroTimeoutRunID, row.IntroTimeoutAt = 0, "", "", nil
	res, err := s.writeIntroRow(bookID, fileID, row, "ClearBookFileIntro")
	if err != nil {
		return nil, res == IntroWriteWrote, err
	}
	return &prior, res == IntroWriteWrote, nil
}

// RecordIntroTimeout counts a transcribe_timeout strike. See IntroTimeoutStrike.
//
// Below the limit only the strike fields and the attempt time are written: the
// file's status is left alone, so a file is not labelled a failure on the
// strength of what may have been a transient stall. At the limit the status
// becomes transcribe_timeout and the row names this run, so clear-by-run-id
// removes the verdict with the rest of what the run wrote. Strikes are counted
// against the stored FileHash and restart when the file is replaced. One strike
// per (file, run): a second timeout recorded by the same run for the same bytes
// writes nothing and reports SameRun.
//
// It goes through ModifyBookFile: under the row's stripe it reads the stored
// row, runs the callback once on a copy and writes the result, so the decision
// and the write are one step against every other stripe-holding writer. That is
// NOT compare-and-swap (the commit only checks that the key still exists), and
// the library scanner's batch upsert takes no file stripe at all, so it can
// still race this write; the book op closes that gap by waiting out a running
// scan before it writes (see waitWhileScanBusy), the per-file op likewise.
func (s *PebbleStore) RecordIntroTimeout(bookID, fileID string, st IntroTimeoutStrike) (IntroTimeoutOutcome, error) {
	if st.SourceHash == "" {
		return IntroTimeoutOutcome{Stale: true, Skipped: true}, fmt.Errorf("RecordIntroTimeout: %s: empty source hash", fileID)
	}
	if st.Limit < 1 {
		return IntroTimeoutOutcome{Skipped: true}, fmt.Errorf("RecordIntroTimeout: %s: limit must be >= 1, got %d", fileID, st.Limit)
	}
	var out IntroTimeoutOutcome
	row, err := s.ModifyBookFile(bookID, fileID, func(row *BookFile) error {
		out = IntroTimeoutOutcome{}
		if row.FileHash != st.SourceHash {
			out = IntroTimeoutOutcome{Stale: true, Skipped: true}
			return ErrSkipBookFileWrite
		}
		if row.HasIntroResult() || row.IntroTranscription != nil {
			out = IntroTimeoutOutcome{Skipped: true}
			if row.IntroTranscription != nil {
				out.HeldText = *row.IntroTranscription
			}
			if row.TranscribeStatus != nil {
				out.HeldStatus = *row.TranscribeStatus
			}
			return ErrSkipBookFileWrite
		}
		counted := row.IntroTimeoutHash == st.SourceHash
		if counted && st.RunOpID != "" && row.IntroTimeoutRunID == st.RunOpID {
			out = IntroTimeoutOutcome{
				Strikes:   row.IntroTimeouts,
				Permanent: row.IntroTimeouts >= st.Limit,
				SameRun:   true,
			}
			return ErrSkipBookFileWrite
		}
		// Another run (usually the other transcribe op) counted a strike on these
		// bytes moments ago: the same stall, not a second piece of evidence.
		if counted && st.MinGap > 0 && row.IntroTimeoutAt != nil && st.At.Sub(*row.IntroTimeoutAt) < st.MinGap {
			out = IntroTimeoutOutcome{
				Strikes:   row.IntroTimeouts,
				Permanent: row.IntroTimeouts >= st.Limit,
				TooSoon:   true,
			}
			return ErrSkipBookFileWrite
		}
		strikes := 1
		if counted {
			strikes = row.IntroTimeouts + 1
		}
		row.IntroTimeouts, row.IntroTimeoutHash, row.IntroTimeoutRunID = strikes, st.SourceHash, st.RunOpID
		struck := st.At
		row.IntroTimeoutAt = &struck
		attempted := st.At
		row.TranscribeAttemptedAt = &attempted
		permanent := strikes >= st.Limit
		if permanent {
			status := IntroStatusTranscribeTimeout
			row.TranscribeStatus = &status
			row.TranscribeError = optString(st.Detail)
			row.IntroRunOpID = st.RunOpID
		}
		out = IntroTimeoutOutcome{Strikes: strikes, Permanent: permanent}
		return nil
	})
	if err != nil {
		// A write that was applied but not known durable still counted.
		if bookFileApplied(err) && !out.Skipped {
			return out, fmt.Errorf("RecordIntroTimeout: %s: %w", fileID, err)
		}
		return IntroTimeoutOutcome{Skipped: true}, fmt.Errorf("RecordIntroTimeout: %s: %w", fileID, err)
	}
	if row == nil {
		return IntroTimeoutOutcome{Stale: true, Skipped: true}, nil
	}
	return out, nil
}

// ClearIntroTimeouts drops a file's strikes after its audio decoded. See
// BookFileIntroStore. A transcribe_timeout verdict that those strikes produced
// is dropped with them (status, error and the run that stamped it), because a
// file that decodes is not one the decoder cannot handle; any other status is
// left alone.
func (s *PebbleStore) ClearIntroTimeouts(bookID, fileID, sourceHash string) (bool, error) {
	if sourceHash == "" {
		return false, fmt.Errorf("ClearIntroTimeouts: %s: empty source hash", fileID)
	}
	cleared := false
	_, err := s.ModifyBookFile(bookID, fileID, func(row *BookFile) error {
		cleared = false
		if row.FileHash != sourceHash || row.IntroTimeoutHash != sourceHash || (row.IntroTimeouts == 0 && row.IntroTimeoutRunID == "") {
			return ErrSkipBookFileWrite
		}
		row.IntroTimeouts, row.IntroTimeoutHash, row.IntroTimeoutRunID, row.IntroTimeoutAt = 0, "", "", nil
		if row.TranscribeStatus != nil && *row.TranscribeStatus == IntroStatusTranscribeTimeout {
			row.TranscribeStatus, row.TranscribeError = nil, nil
			row.IntroRunOpID = ""
		}
		cleared = true
		return nil
	})
	if err != nil {
		return cleared && bookFileApplied(err), fmt.Errorf("ClearIntroTimeouts: %s: %w", fileID, err)
	}
	return cleared, nil
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
