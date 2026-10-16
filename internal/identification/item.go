// file: internal/identification/item.go
// version: 1.0.1
// guid: 6d14d647-d925-4db9-8df1-4aac425555bf
// last-edited: 2026-10-16

package identification

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// FetchResult is what one run of the per-book pipeline reports to Item.
type FetchResult struct {
	// Outcome is OutcomeFetched, OutcomeDeferred, OutcomeSkippedDone (the
	// pipeline found nothing it could ask) or OutcomeError.
	Outcome Outcome
	// Reason is the identification reason the state derivation cannot see
	// (database.IdentReasonNoUsableTitle, IdentReasonFragment,
	// IdentReasonManualOnly, IdentReasonQuotaDeferred), or "".
	Reason string
	// Err describes an OutcomeError.
	Err error
}

// ItemStore is the store capability Item writes through.
type ItemStore interface {
	ClearIdentDirty(bookID string, epoch uint64) (bool, error)
	SetIdentReason(bookID, reason, questionFP string) error
}

// ItemDeps are Item's injected dependencies.
type ItemDeps struct {
	// LoadBook reads the book as it is now: (nil, nil) when it is gone.
	LoadBook func(bookID string) (*database.Book, error)
	// Store clears marks and records reasons.
	Store ItemStore
	// FetchOne runs the existing per-book pipeline for book: the candidate
	// cache, the catalog stage when enabled, the providers, scoring, and the
	// candidate row write. In production it is an adapter to the server's
	// candidate-fetch per-book function; there is no second pipeline.
	FetchOne func(ctx context.Context, book *database.Book) FetchResult
}

// settled reports whether the book's identification is decided by a person:
// applied (MetadataApplied) or held (the owner's no_match ruling). These are
// the derivation's own first two rules (database deriveIdent), read off the
// book itself, never off the index.
func settled(b *database.Book) bool {
	return database.MetadataApplied(b.MetadataReviewStatus) ||
		(b.MetadataReviewStatus != nil && *b.MetadataReviewStatus == "no_match")
}

// NewItem returns the per-book pipeline (Q -> B -> S -> X) as one idempotent
// function over a Mark:
//
//  1. Re-derive, never trust the mark: load the book. A book that is gone,
//     not in the primary scope, quarantined, applied or held has nothing to
//     ask; its mark is compare-and-deleted and no provider is called.
//  2. Run the existing pipeline through FetchOne. A repeat of an unchanged
//     question is answered from the candidate cache and the shared provider
//     cache at zero provider calls.
//  3. Record the reason the derivation cannot see (SetIdentReason) under the
//     book's question fingerprint, or clear a reason that no longer applies.
//  4. Clear the mark by compare-and-delete on the epoch read with the page:
//     a writer that re-marked the book while this ran left a newer epoch,
//     the mark survives, and the book is processed again.
//
// A cancel stops before step 3: the mark survives, so the rerun after a
// restart processes the book again (the cache makes that free).
//
// Every other outcome clears the mark, an error included. The 5-minute tick
// re-enqueues the driver whenever the dirty set is non-empty, so a mark kept
// after a provider error would be retried every 5 minutes for as long as the
// error lasts. The 6-hour candidate_fetch safety net re-marks a book that is
// still unanswered, which bounds the retry rate. The one exception is a
// failure to read the book or write the reason or the mark: those keep the
// mark (nothing about the book was learned).
func NewItem(d ItemDeps) ItemFunc {
	return func(ctx context.Context, m Mark) (Outcome, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		book, err := d.LoadBook(m.BookID)
		if err != nil {
			return OutcomeError, fmt.Errorf("identification: load book %s: %w", m.BookID, err)
		}
		if book == nil || !database.InLibraryDefaultView(book) || settled(book) {
			if book == nil {
				// A deleted book's reason is an orphan; drop it.
				if err := d.Store.SetIdentReason(m.BookID, "", ""); err != nil {
					return OutcomeError, fmt.Errorf("identification: clear reason of %s: %w", m.BookID, err)
				}
			}
			if _, err := d.Store.ClearIdentDirty(m.BookID, m.Epoch); err != nil {
				return OutcomeError, fmt.Errorf("identification: clear mark of %s: %w", m.BookID, err)
			}
			return OutcomeSkippedDone, nil
		}
		// The fingerprint of the question as the book reads NOW, before the
		// fetch: a reason recorded under it stops applying as soon as the
		// book is retitled or re-credited.
		fp := database.IdentQuestionFP(book)
		// Checked again after the load: a run cancelled meanwhile must not
		// start a provider fetch; the mark stays for the next run.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		res := d.FetchOne(ctx, book)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := d.Store.SetIdentReason(book.ID, res.Reason, fp); err != nil {
			return OutcomeError, fmt.Errorf("identification: record reason of %s: %w", book.ID, err)
		}
		if _, err := d.Store.ClearIdentDirty(m.BookID, m.Epoch); err != nil {
			return OutcomeError, fmt.Errorf("identification: clear mark of %s: %w", m.BookID, err)
		}
		if res.Outcome == "" {
			res.Outcome = OutcomeFetched
		}
		if res.Outcome == OutcomeError {
			err := res.Err
			if err == nil {
				err = fmt.Errorf("identification: book %s: pipeline error", book.ID)
			}
			return OutcomeError, err
		}
		return res.Outcome, nil
	}
}
