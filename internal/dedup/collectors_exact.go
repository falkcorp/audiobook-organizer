// file: internal/dedup/collectors_exact.go
// version: 1.2.0
// guid: c9d0e1f2-a3b4-4c5d-8e6f-7a8b9c0d1e2f
// last-edited: 2026-07-07

// Package dedup — exact-tier collector family (fable5 T014).
//
// # Design
//
// Three pure collector functions wrap the exact-layer checks from engine.go into
// unified.Signal emitters.  The business logic is UNCHANGED — only the return
// shape changes from "call UpsertCandidate directly" to "return a []Signal".
//
// Collectors in this file:
//
//   - CollectExactFileHash: wraps checkExactFileHash (engine.go:276-333).
//     Emits SigExactFile (conf 1.00) when both books share a whole-file hash
//     for at least one BookFile.
//
//   - CollectISBNASIN: wraps checkExactISBN (engine.go:350-426).
//     Emits SigISBNASIN (conf 0.98) when book and candidate share ISBN10,
//     ISBN13, or ASIN.
//
//   - CollectMetaSrcHash: wraps checkExactMetadataSourceHash (engine.go:428-458).
//     Emits SigMetaSrcHash (conf 0.97) when both books were applied from the
//     same external metadata record (same metadata_source_hash).
//
// All three accept narrow store interfaces so they are unit-testable without
// a real database.

package dedup

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// ─── store interfaces ──────────────────────────────────────────────────────────

// ExactFileHashStore is the subset of database.Store required by
// CollectExactFileHash.
type ExactFileHashStore interface {
	GetBookByFileHash(hash string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// ISBNASINStore is the subset of database.Store required by CollectISBNASIN.
// GetAllBooksCore drives the O(N) scan fallback; GetBookByID hydrates the
// indexed fast path (one point-read per index match).
type ISBNASINStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookByID(id string) (*database.Book, error)
}

// MetaSrcHashStore is the subset of database.Store required by
// CollectMetaSrcHash.
type MetaSrcHashStore interface {
	GetBooksByMetadataSourceHash(hash string) ([]database.Book, error)
}

// ─── exact-file-hash collector ────────────────────────────────────────────────

// CollectExactFileHash looks for books that share a whole-file hash with book.
// It checks both the book-level FileHash field and the per-BookFile FileHash
// fields, mirroring the logic in checkExactFileHash.
//
// A SigExactFile signal (confidence 1.00) is emitted for each unique other book
// that shares at least one file hash.  The pair is expected to be different
// books (book.ID != other.ID) — self-matches are suppressed.
//
// Logic unchanged from checkExactFileHash (engine.go:276-333); emission shape
// only.
//
// Note on gate omission: this collector runs over the unified SCORING pipeline
// (re-scoring pre-existing candidates), NOT the emission path. The
// hasPlausibleAudio gate — which lives on checkExactTitle/checkExactISBN in
// engine.go — is intentionally not applied here; it is an emission-time guard
// and does not belong on a scoring-only collector.
func CollectExactFileHash(
	store ExactFileHashStore,
	book *database.Book,
) ([]unified.Signal, error) {
	if book == nil {
		return nil, nil
	}

	seen := make(map[string]bool) // dedupe by other book ID in this call
	var signals []unified.Signal

	emitIfNew := func(other *database.Book, hashEvidence string) {
		if other == nil || other.ID == book.ID || seen[other.ID] {
			return
		}
		seen[other.ID] = true
		signals = append(signals, unified.Signal{
			Kind:       unified.SigExactFile,
			Raw:        1.0,
			Confidence: 1.0, // certainty: identical whole-file hash
			Evidence:   fmt.Sprintf("whole-file hash match %s: book %s ↔ %s", hashEvidence, book.ID, other.ID),
		})
	}

	// Book-level hash (single-file audiobooks often only have this).
	if book.FileHash != nil && *book.FileHash != "" {
		other, err := store.GetBookByFileHash(*book.FileHash)
		if err != nil {
			return nil, err
		}
		emitIfNew(other, *book.FileHash)
	}

	// Per-BookFile hashes (multi-file audiobooks: each chapter file has its
	// own hash).
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.FileHash == "" {
			continue
		}
		other, err := store.GetBookByFileHash(f.FileHash)
		if err != nil {
			// Single file error: log at the caller level; skip this file.
			continue
		}
		emitIfNew(other, f.FileHash)
	}

	return signals, nil
}

// ─── ISBN/ASIN collector ──────────────────────────────────────────────────────

// CollectISBNASIN finds books that share an ISBN10, ISBN13, or ASIN with book
// and emits a SigISBNASIN signal (confidence 0.98) per unique matching book.
//
// Two paths, mirroring checkExactISBN (engine.go): when a built ISBN index is
// wired, the indexed fast path resolves matches in O(matches) via
// GetBookIDsByISBNASIN + per-match GetBookByID; otherwise it falls back to the
// O(N) GetAllBooksCore scan (the original behavior). Both paths are exercised
// only when the query book has at least one external ID. Prior to this the
// scoring path always took the O(N) scan — an O(N²) hotspot over the full score
// pass (#19). Both paths honor ctx cancellation between units of work.
//
// Logic unchanged from checkExactISBN (engine.go:350-426); emission shape only.
//
// Note on gate omission: this collector runs over the unified SCORING pipeline
// (re-scoring pre-existing candidates), NOT the emission path. The
// hasPlausibleAudio gate — which lives on checkExactISBN in engine.go — is
// intentionally not applied here; it is an emission-time guard and does not
// belong on a scoring-only collector. MarkedForDeletion IS filtered, so the
// indexed set equals what the scan (via GetAllBooksCore's deletion filter) sees.
func CollectISBNASIN(
	ctx context.Context,
	store ISBNASINStore,
	index ISBNIndexStore,
	book *database.Book,
) ([]unified.Signal, error) {
	if book == nil {
		return nil, nil
	}

	bookISBN10 := derefStr(book.ISBN10)
	bookISBN13 := derefStr(book.ISBN13)
	bookASIN := derefStr(book.ASIN)

	if bookISBN10 == "" && bookISBN13 == "" && bookASIN == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// matchFieldFor derives the evidence field with the same precedence the scan
	// used (isbn10 > isbn13 > asin); "" means no shared external ID.
	matchFieldFor := func(oISBN10, oISBN13, oASIN string) string {
		switch {
		case bookISBN10 != "" && oISBN10 == bookISBN10:
			return "isbn10:" + bookISBN10
		case bookISBN13 != "" && oISBN13 == bookISBN13:
			return "isbn13:" + bookISBN13
		case bookASIN != "" && oASIN == bookASIN:
			return "asin:" + bookASIN
		}
		return ""
	}
	emit := func(sigs []unified.Signal, otherID, matchField string) []unified.Signal {
		return append(sigs, unified.Signal{
			Kind:       unified.SigISBNASIN,
			Raw:        1.0,
			Confidence: 0.98,
			Evidence:   fmt.Sprintf("isbn/asin match %s: book %s ↔ %s", matchField, book.ID, otherID),
		})
	}

	seen := make(map[string]bool)
	var signals []unified.Signal

	// Fast path: indexed lookup, O(matches). Mirrors checkExactISBNIndexed.
	if index != nil && index.IsISBNIndexBuilt() {
		ids, err := index.GetBookIDsByISBNASIN(bookISBN10, bookISBN13, bookASIN)
		if err != nil {
			return nil, fmt.Errorf("CollectISBNASIN index lookup: %w", err)
		}
		for _, otherID := range ids {
			if otherID == book.ID || seen[otherID] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			other, err := store.GetBookByID(otherID)
			if err != nil {
				return nil, fmt.Errorf("CollectISBNASIN hydrate %s: %w", otherID, err)
			}
			if other == nil {
				continue // deleted between index scan and point lookup
			}
			// Deletion parity: GetAllBooksCore excludes MarkedForDeletion books,
			// so the scan path never sees them; match that here.
			if other.MarkedForDeletion != nil && *other.MarkedForDeletion {
				continue
			}
			matchField := matchFieldFor(derefStr(other.ISBN10), derefStr(other.ISBN13), derefStr(other.ASIN))
			if matchField == "" {
				continue // index over-returned (normalization); re-check keeps parity
			}
			seen[otherID] = true
			signals = emit(signals, otherID, matchField)
		}
		return signals, nil
	}

	// Fallback: O(N) GetAllBooksCore scan (original behavior).
	const batchSize = 500
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := store.GetAllBooksCore(batchSize, offset)
		if err != nil {
			return nil, fmt.Errorf("CollectISBNASIN get all books at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}

		for i := range batch {
			other := &batch[i]
			if other.ID == book.ID || seen[other.ID] {
				continue
			}
			matchField := matchFieldFor(derefStr(other.ISBN10), derefStr(other.ISBN13), derefStr(other.ASIN))
			if matchField == "" {
				continue
			}
			seen[other.ID] = true
			signals = emit(signals, other.ID, matchField)
		}

		if len(batch) < batchSize {
			break
		}
		offset += batchSize
	}

	return signals, nil
}

// ─── metadata-source-hash collector ──────────────────────────────────────────

// CollectMetaSrcHash looks for books that share the same metadata_source_hash
// — meaning the same external record (Audible, Google Books, etc.) was applied
// to both.  Emits SigMetaSrcHash (confidence 0.97) per matching book.
//
// Logic unchanged from checkExactMetadataSourceHash (engine.go:428-458);
// emission shape only.
func CollectMetaSrcHash(
	store MetaSrcHashStore,
	book *database.Book,
) ([]unified.Signal, error) {
	if book == nil {
		return nil, nil
	}
	if book.MetadataSourceHash == nil || *book.MetadataSourceHash == "" {
		return nil, nil
	}

	others, err := store.GetBooksByMetadataSourceHash(*book.MetadataSourceHash)
	if err != nil {
		return nil, fmt.Errorf("CollectMetaSrcHash: %w", err)
	}

	var signals []unified.Signal
	for i := range others {
		other := &others[i]
		if other.ID == book.ID {
			continue
		}
		signals = append(signals, unified.Signal{
			Kind:       unified.SigMetaSrcHash,
			Raw:        1.0,
			Confidence: 0.97, // same external record applied to both (SPEC 1 §3)
			Evidence: fmt.Sprintf("metadata source hash match %s: book %s ↔ %s",
				*book.MetadataSourceHash, book.ID, other.ID),
		})
	}

	return signals, nil
}
