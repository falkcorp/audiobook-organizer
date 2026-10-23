// file: internal/plugins/maintenance/file_intro_clear.go
// version: 1.0.0
// guid: 6f3b8d25-e471-4a09-b2c6-1d8a95e7f034
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// clearFileIntros is rollback for maintenance.transcribe-file-intros
// (clear_run_id): it removes the per-file results the named run wrote and the
// book-level transcripts that were filled from them. It writes no undo-ledger
// rows (owner decision: rollback is by run id).
//
// A preview, which is the default, counts both and writes nothing, so the
// owner sees the size of the rollback before it happens.
//
// What it does NOT do is restore earlier content. A run that only filled empty
// files (only_missing, the default) leaves nothing to restore. A forced re-run
// (only_missing=false) replaced earlier transcripts, and clearing it returns
// those files to "never transcribed", not to what they held: roll-forward only.
//
// A book fill is reverted only when the book still holds EXACTLY what the fill
// wrote (same text, same transcribed-at, same attempted-at), so a transcript
// the book op or the owner wrote afterwards is never erased.
func (p *Plugin) clearFileIntros(ctx context.Context, store fileIntroStore, reporter sdk.Reporter, runID string, dryRun bool) error {
	mark := introModeMarker(dryRun)
	log := reporter.Logger()

	var intros database.BookFileIntroStore
	if !dryRun {
		var ok bool
		intros, ok = database.AsCapability[database.BookFileIntroStore](store)
		if !ok {
			return fmt.Errorf("%s: the store does not support per-file intro writes", fileIntroOpID)
		}
	}

	_ = reporter.UpdateProgress(0, 1, mark+"Clearing — finding the files run "+runID+" wrote")
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("%s: read book files: %w", fileIntroOpID, err)
	}
	type ref struct{ bookID, fileID string }
	var refs []ref
	for i := range cores {
		if cores[i].IntroRunOpID == runID {
			refs = append(refs, ref{cores[i].BookID, cores[i].ID})
		}
	}
	cores = nil

	var files, bookReverts, errs atomic.Int64
	const chunk = 64
	chunks := make([][]ref, 0, (len(refs)+chunk-1)/chunk)
	for i := 0; i < len(refs); i += chunk {
		chunks = append(chunks, refs[i:min(i+chunk, len(refs))])
	}

	// Each file is its own row and each book revert is guarded by ModifyBook's
	// per-book lock plus an exact-match check, so workers cannot double-revert;
	// the pool is bounded to the CPU count (the work is store reads/writes).
	err = registry.RunItems(ctx, reporter, chunks, func(ctx context.Context, rs []ref) error {
		for _, r := range rs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var before *database.BookFile
			if dryRun {
				row, gerr := store.GetBookFileByID(r.bookID, r.fileID)
				if gerr != nil {
					errs.Add(1)
					log.Warn("transcribe-file-intros: clear preview read failed", "file_id", r.fileID, "err", gerr)
					continue
				}
				if row == nil || row.IntroRunOpID != runID {
					continue
				}
				before = row
				files.Add(1)
				if b, berr := store.GetBookByID(r.bookID); berr == nil && b != nil && bookHoldsFileFill(b, before) {
					bookReverts.Add(1)
				}
				continue
			}
			prior, cleared, cerr := intros.ClearBookFileIntro(r.bookID, r.fileID, runID)
			if cerr != nil {
				errs.Add(1)
				log.Warn("transcribe-file-intros: clear failed", "file_id", r.fileID, "err", cerr)
			}
			if !cleared {
				continue
			}
			files.Add(1)
			reverted, rerr := revertBookFill(store, r.bookID, prior)
			if rerr != nil {
				errs.Add(1)
				log.Warn("transcribe-file-intros: book revert failed", "book_id", r.bookID, "err", rerr)
			}
			if reverted {
				bookReverts.Add(1)
			}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   max(runtime.NumCPU(), 1),
		ProgressTotal: len(chunks),
		Label: func(i, t int) string {
			return mark + fmt.Sprintf("Clearing run %s — chunk %d/%d, %d files so far", runID, i+1, t, files.Load())
		},
	})
	if err != nil {
		return err
	}

	verb := "cleared"
	tail := ""
	if dryRun {
		verb = "would clear"
		tail = " Pass dry_run=false to clear."
	}
	msg := mark + fmt.Sprintf("%s per-file results on %d files and %d book transcripts that run %s wrote (%d errors). "+
		"Clearing returns files to untranscribed; it cannot restore a transcript a forced re-run replaced.%s",
		verb, files.Load(), bookReverts.Load(), runID, errs.Load(), tail)
	log.Info("transcribe-file-intros: clear", "run_id", runID, "dry_run", dryRun,
		"files", files.Load(), "book_reverts", bookReverts.Load(), "errors", errs.Load(), "at", time.Now().UTC())
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

func ptrStrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrTimeEq(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// bookHoldsFileFill reports whether the book still holds exactly the
// transcript the file's result filled it with.
func bookHoldsFileFill(b *database.Book, f *database.BookFile) bool {
	return f.IntroTranscription != nil && b.IntroTranscription != nil &&
		*b.IntroTranscription == *f.IntroTranscription &&
		f.IntroTranscribedAt != nil && ptrTimeEq(b.IntroTranscribedAt, f.IntroTranscribedAt) &&
		f.TranscribeAttemptedAt != nil && ptrTimeEq(b.TranscribeAttemptedAt, f.TranscribeAttemptedAt)
}

// revertBookFill undoes a book-level fill made from the (just cleared) file
// row prior, if the book still holds it exactly. Parsed credits are cleared
// only where they still equal the file's.
func revertBookFill(store fileIntroStore, bookID string, prior *database.BookFile) (bool, error) {
	if prior == nil {
		return false, nil
	}
	reverted := false
	_, err := store.ModifyBook(bookID, func(cur *database.Book) error {
		if !bookHoldsFileFill(cur, prior) {
			return database.ErrSkipBookWrite
		}
		cur.IntroTranscription = nil
		cur.IntroTranscribedAt = nil
		cur.TranscribeAttemptedAt = nil
		cur.TranscribeStatus = nil
		cur.TranscribeError = nil
		clearIfEq := func(cv **string, fv *string) {
			if fv != nil && ptrStrEq(*cv, fv) {
				*cv = nil
			}
		}
		clearIfEq(&cur.TranscribedTitle, prior.TranscribedTitle)
		clearIfEq(&cur.TranscribedAuthor, prior.TranscribedAuthor)
		clearIfEq(&cur.TranscribedNarrator, prior.TranscribedNarrator)
		clearIfEq(&cur.TranscribedTranslator, prior.TranscribedTranslator)
		clearIfEq(&cur.TranscribedCoverArtist, prior.TranscribedCoverArtist)
		reverted = true
		return nil
	})
	return reverted, err
}
