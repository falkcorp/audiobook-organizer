// file: internal/books/store/pebble_store_author_books_trust.go
// version: 0.1.0
// guid: 3c9e7a52-6d18-4b0f-a2e4-8f51b7c0d936
// last-edited: 2026-11-20

package store

import (
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Keeping the author -> books index's per-boot trust true only while it holds
// (pebble_store_author_books_index.go, TRUST).
//
// The reconcile proves, once per process, that every listable book's
// Book.AuthorID is in its credit row. The writes after it can break that: a book
// row given an AuthorID its credits do not hold, a book made listable again
// with such an AuthorID, or a credit row losing an author. The index then
// cannot list that book under its AuthorID while the scan and memdb do, so
// those writes revoke the trust (authorBooksVerifiedThisBoot) for the rest of
// the process; readers fall back to the scan. No write restores it: a later
// credit write cannot prove that no OTHER book is uncovered, so only a later
// reconcile, which checks every book again, can.
//
// Writes no key: revocation is in memory only, so a book-row or credit write
// still writes no authors-family key beyond the credit-row keys it already
// owned (F5-18j's narrowing of the book write).
//
// ORDERING. A revoking write revokes twice, before its batch commits and after:
//
//   - Before the commit, for readers. booksByAuthorIDFromIndex loads the flag,
//     takes its snapshot, and loads the flag again. A snapshot that contains the
//     revoking commit was taken after the pre-commit revoke, so the second load
//     sees false and the reader scans instead.
//   - After the commit, for a concurrent reconcile. The reconcile samples
//     authorBooksTrustGen before its scans and, after storing a clean result,
//     reads it again and withdraws the trust if it moved. A commit its scans
//     missed (its iterators are point-in-time) landed after they began, so the
//     post-commit bump either precedes the reconcile's second read (which then
//     withdraws) or follows its Store(true) (and the post-commit revoke clears
//     it). The pre-commit revoke alone is not enough: a Store(true) can land
//     between it and the commit.
//
// DELETE. deleteBook removes a book row and its credit row in one batch, under
// the book stripe and the book_authors stripe, so no instant exists in which a
// listable row has lost its credit; it revokes nothing. A credit write that
// commits after the delete (SetBookAuthors holds only the book_authors stripe)
// leaves credit-row keys naming a book that is gone: extra keys, which the
// reader's point-verify drops.

// revokeAuthorBooksTrust withdraws this process's trust in the index and moves
// the generation a running reconcile checks (see ORDERING). Logs once, on the
// transition.
func (p *PebbleStore) revokeAuthorBooksTrust(reason, bookID string) {
	p.authorBooksTrustGen.Add(1)
	if p.authorBooksVerifiedThisBoot.Swap(false) {
		authorBooksIndexLog.Info("author-books-index: trust revoked: reason=%q book=%s; author listings scan until a later reconcile finishes clean",
			reason, logger.SanitizeLogValue(bookID))
	}
}

// abandonAuthorBooksTrust is revokeAuthorBooksTrust for a memdb warmup that
// ended without publishing. Reads then stay on Pebble for the life of the
// process, so the index would answer every cold author listing for as long as
// the process runs; the trust is withdrawn and no later reconcile may restore
// it (memWarmupUnpublished is sticky). The process scans instead: slower, and
// correct whatever any writer outside this file does.
func (p *PebbleStore) abandonAuthorBooksTrust(reason string) {
	p.memWarmupUnpublished.Store(true)
	p.revokeAuthorBooksTrust("memdb warmup ended unpublished: "+reason, "")
}

// bookListableByAuthor reports whether the author listings return b: the
// reconcile's own listable rule (authorIDCoverageRow.listable).
func bookListableByAuthor(b *Book) bool {
	return authorIDCoverageRow{IsPrimaryVersion: b.IsPrimaryVersion, MarkedForDeletion: b.MarkedForDeletion}.listable()
}

// bookRowNeedsCoverageCheck reports whether writing next over old (nil on
// create) can make next an uncovered listable book: next is listable with an
// AuthorID, and either the AuthorID is new or the book was not listable
// before. Any other write leaves coverage as the reconcile (or the last check)
// found it, so it costs no read.
func bookRowNeedsCoverageCheck(old, next *Book) bool {
	if next.AuthorID == nil || !bookListableByAuthor(next) {
		return false
	}
	if old == nil || old.AuthorID == nil || *old.AuthorID != *next.AuthorID {
		return true
	}
	return !bookListableByAuthor(old)
}

// authorIDUncredited reports whether book bookID's credit row lacks authorID.
// pending, when given, is the credit row the same batch writes (ModifyBookCredits
// stages it beside the book row); otherwise the stored row is read. A missing,
// undecodable or unreadable row counts as lacking it: revoking is always safe.
func (p *PebbleStore) authorIDUncredited(bookID string, authorID int, pending []int, pendingGiven bool) bool {
	if pendingGiven {
		return !slices.Contains(pending, authorID)
	}
	ids, found, ok, err := readCreditAuthorIDs(p.db, bookID)
	if err != nil {
		authorBooksIndexLog.Warn("author-books-index: credit row of book %s unreadable during a book write, revoking trust: err=%s",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(err.Error()))
		return true
	}
	return !found || !ok || !slices.Contains(ids, authorID)
}

// creditRowDropsAnAuthor reports whether a credit row moving from oldIDs to
// next removes any author id. An undecodable old row (oldOK false) counts as
// dropping one: what it held is unknown.
func creditRowDropsAnAuthor(oldIDs []int, oldOK bool, next []int) bool {
	if !oldOK {
		return true
	}
	for _, id := range oldIDs {
		if !slices.Contains(next, id) {
			return true
		}
	}
	return false
}
