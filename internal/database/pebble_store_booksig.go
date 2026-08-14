// file: internal/database/pebble_store_booksig.go
// version: 1.0.0
// guid: 4c81f0a7-5e93-4d2b-9a16-7f30c8e51b42
// last-edited: 2026-08-13

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Book-signature sidecar: the five BookSig* fields live under their own
// `book_sig:<id>` key instead of inline in the `book:<id>` row.
//
// WHY
//
// Measured on production 2026-08-13 (warmup byte accounting, #2383):
//
//	phase_mb[books]                          = 729
//	discarded_mb[books]                      = 583
//	discarded_field_mb[book_sig_v1_and_mask] = 580
//
// 80% of every byte the `books` warmup phase reads is a signature that
// stripBookForMemdb nils out the instant it finishes decoding (memdb_strip.go).
// Startup pays ~580 MB of Pebble reads, JSON decode and allocation for data
// it discards by design. The warmup scan is bounded ["book:", "book;") and '_'
// (0x5F) sorts above ';' (0x3B), so `book_sig:` keys fall outside that range —
// the scan stops seeing these bytes entirely rather than skipping them.
//
// WHY IT IS ALSO SAFER, NOT ONLY FASTER
//
// Today the only thing between a bare memdb round-trip write and 67,824 wiped
// signatures is the preserve-guard in UpdateBook ("if book.BookSigV1 == nil {
// book.BookSigV1 = oldBook.BookSigV1 }"). That guard exists because the wipe
// already happened once; booksig_recovery_audit is the op written to undo it,
// and the guard's own comment asks the next author to "keep both in sync".
//
// With a sidecar, "the incoming Book carries no signature" means "do not touch
// the book_sig: key". Not writing is the default, so the wipe stops depending
// on anyone remembering a guard. The guard stays anyway — belt and braces, and
// it is what keeps the re-serialized row correct for legacy inline data.
//
// WHY ALL FIVE FIELDS, NOT JUST THE TWO BIG ONES
//
// Only BookSigV1 (~22 KB base64) and BookSigV1Mask (~700 B) are large;
// Segments, BuiltAt and CoveragePct are scalars worth nothing in bytes. They
// move anyway because splitting them would manufacture a state the codebase
// treats as damage: booksig_recovery_audit classifies a book as wiped on
// `BookSigBuiltAt != nil && BookSigV1 == nil` and its remedy is restoring
// book_ver: snapshot data over the live row. If BuiltAt stayed inline while V1
// moved, every reader that does not hydrate would present that exact signature
// and the audit would "recover" healthy books en masse.
//
// Keeping the five together also matches the partition the code already
// enforces in three places — stripBookForMemdb nils all five, UpdateBook's
// preserve-guard restores all five, restoreRecoverableFields restores all five.
// The sidecar boundary is that same partition, not a fourth one.

// bookSigKeyPrefix is deliberately NOT under "book:". The warmup scan, and
// GetAllBooksFullFrom's non-memdb branch, iterate byte ranges anchored on
// "book:"; a "book:sig:<id>" layout would land inside them and the row would
// still be read on every startup, which is the entire cost being removed here.
const bookSigKeyPrefix = "book_sig:"

func bookSigKey(id string) []byte {
	return []byte(bookSigKeyPrefix + id)
}

// bookSigSidecar is the stored payload. Field names are short because this key
// is written once per signature build and read on every full-fidelity book
// fetch; they are independent of Book's JSON tags and nothing outside this file
// serializes it.
type bookSigSidecar struct {
	V1          *string    `json:"v1,omitempty"`
	Mask        *string    `json:"mask,omitempty"`
	Segments    *int       `json:"segments,omitempty"`
	BuiltAt     *time.Time `json:"built_at,omitempty"`
	CoveragePct *int       `json:"coverage_pct,omitempty"`
}

// bookSigOf projects a Book's five signature fields. ok is false when all five
// are nil, which is what "this book has no signature" looks like and is also
// what a memdb-stripped or BookCore-projected Book always looks like. Callers
// use ok to decide whether to write anything at all — see writeBookSigToBatch.
func bookSigOf(b *Book) (bookSigSidecar, bool) {
	if b == nil {
		return bookSigSidecar{}, false
	}
	s := bookSigSidecar{
		V1:          b.BookSigV1,
		Mask:        b.BookSigV1Mask,
		Segments:    b.BookSigSegments,
		BuiltAt:     b.BookSigBuiltAt,
		CoveragePct: b.BookSigCoveragePct,
	}
	empty := s.V1 == nil && s.Mask == nil && s.Segments == nil &&
		s.BuiltAt == nil && s.CoveragePct == nil
	return s, !empty
}

// applyTo writes the sidecar's five fields onto b, replacing whatever was
// there. The sidecar is authoritative when it exists: a book whose signature
// was rebuilt has a fresh sidecar and a stale inline copy only if it predates
// the migration, and in that ordering the sidecar is the newer value.
func (s bookSigSidecar) applyTo(b *Book) {
	if b == nil {
		return
	}
	b.BookSigV1 = s.V1
	b.BookSigV1Mask = s.Mask
	b.BookSigSegments = s.Segments
	b.BookSigBuiltAt = s.BuiltAt
	b.BookSigCoveragePct = s.CoveragePct
}

// stripBookSigForRow returns a shallow copy of b with the five signature fields
// cleared, for marshalling the `book:<id>` row. All five are pointers with
// `omitempty`, so nilling them removes the keys from the JSON rather than
// writing nulls — the row genuinely shrinks.
//
// This is a copy, not an in-place mutation, because CreateBook and UpdateBook
// both return the *Book they were handed and callers go on reading the
// signature off it (acoustid's synthesizeBookSignatureForBook sets the five
// fields, calls UpdateBook, and its caller may keep using the struct).
// Clearing in place would hand back a book that had just been saved with a
// signature yet appeared to have none.
func stripBookSigForRow(b *Book) *Book {
	if b == nil {
		return nil
	}
	cp := *b
	cp.BookSigV1 = nil
	cp.BookSigV1Mask = nil
	cp.BookSigSegments = nil
	cp.BookSigBuiltAt = nil
	cp.BookSigCoveragePct = nil
	return &cp
}

// hydrateBookSig fills b's signature fields from the sidecar.
//
// FALLBACK-FIRST: when no sidecar key exists, b is left exactly as it was, so a
// legacy row that still carries the signature inline keeps working untouched
// and un-migrated. That is what lets the read path ship before any data moves.
//
// A read error is returned, never swallowed. Treating a Pebble failure as "no
// signature" would hand the caller a Book that is byte-identical to a wiped one
// — and booksig_recovery_audit reads exactly that shape as damage requiring a
// snapshot restore. Failing loudly is the only safe option here.
func (p *PebbleStore) hydrateBookSig(b *Book) error {
	if b == nil || b.ID == "" {
		return nil
	}
	val, closer, err := p.db.Get(bookSigKey(b.ID))
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get book signature sidecar %q: %w", b.ID, err)
	}
	defer closer.Close()

	var s bookSigSidecar
	if err := json.Unmarshal(val, &s); err != nil {
		return fmt.Errorf("unmarshal book signature sidecar %q: %w", b.ID, err)
	}
	s.applyTo(b)
	return nil
}

// writeBookSigToBatch stages the sidecar write alongside the caller's other
// writes so the row and its signature land in ONE Pebble batch — there is no
// window in which a book exists with a half-written signature.
//
// A book with no signature writes no key and DELETES nothing. That asymmetry is
// deliberate and is the structural half of the wipe fix: every caller that
// sourced its *Book from a memdb projection or a BookCore round-trip arrives
// here with all five fields nil, and the correct response to that is to leave
// the stored signature alone. Clearing a signature is not something any current
// caller does; if one ever needs to, it must delete the key explicitly rather
// than get it for free by passing nil.
func writeBookSigToBatch(batch *pebble.Batch, b *Book) error {
	sig, ok := bookSigOf(b)
	if !ok {
		return nil
	}
	data, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("marshal book signature sidecar %q: %w", b.ID, err)
	}
	return batch.Set(bookSigKey(b.ID), data, nil)
}
