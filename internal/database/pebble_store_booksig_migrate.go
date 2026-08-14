// file: internal/database/pebble_store_booksig_migrate.go
// version: 1.1.0
// guid: 6a4e2f18-7d05-4b93-8c61-9f2a0e75d3b8
// last-edited: 2026-08-13

package database

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// Migration of legacy inline book signatures into the `book_sig:` sidecar.
//
// #2387 introduced the sidecar with FALLBACK-FIRST reads (hydrateBookSig leaves
// a book untouched when no sidecar key exists), so every one of the 67,824
// existing rows kept working un-migrated. That is what let the read path ship
// safely — and it is also why the ~580 MB/startup saving is not realized until
// the data actually moves. This file is the primitive that moves it.
//
// WHY NOT JUST CALL UpdateBook
//
// UpdateBook already strips the row and writes the sidecar, so
// GetBookByID -> UpdateBook would migrate a book correctly. It is still the
// wrong tool for a whole-library pass:
//
//   - It writes a book_ver: CoW snapshot per call, and that snapshot
//     DELIBERATELY keeps the full inline signature (it is
//     booksig_recovery_audit's safety net — see UpdateBook's comment). Doing
//     that 67,824 times writes roughly 1.5 GB of fresh snapshot data, in a
//     migration whose entire purpose is to stop paying for those bytes.
//   - It bumps UpdatedAt on every book, so the whole library looks modified:
//     search-index dirty set, memdb sync and aggregate recompute all churn.
//
// WHY THIS IS INVISIBLE TO MEMDB
//
// stripBookForMemdb nils exactly these five fields, so removing them from the
// stored row changes nothing the memdb ever held. No invalidation is needed and
// none is performed.

// The five stored JSON keys that carry a book signature. These MUST stay in
// sync with Book's struct tags (store.go); TestBookSigJSONKeysMatchStructTags
// asserts that by reflection, so a rename cannot silently turn this migration
// into a no-op that still reports success.
var bookSigJSONKeys = [...]string{
	"book_sig_v1",
	"book_sig_v1_mask",
	"book_sig_segments",
	"book_sig_built_at",
	"book_sig_coverage_pct",
}

// BookSigMigrateOutcome classifies what happened to one book.
type BookSigMigrateOutcome int

const (
	// BookSigMigrateNotCandidate: the row carries no inline signature. Either
	// it was already migrated, or the book never had a signature. No write.
	BookSigMigrateNotCandidate BookSigMigrateOutcome = iota
	// BookSigMigrateMigrated: inline signature moved to a new sidecar key and
	// the row rewritten without it, in one batch.
	BookSigMigrateMigrated
	// BookSigMigrateStrippedOnly: the row carried an inline signature AND a
	// sidecar already existed, so no NEW sidecar was created. The existing one
	// is authoritative (see bookSigSidecar.applyTo) FIELD BY FIELD: values it
	// already holds are kept — writing stale inline values over a newer sidecar
	// would be a silent downgrade — while fields it is MISSING are filled from
	// inline, so stripping the row cannot delete a field that lived only there.
	// A sidecar that already covers every inline field is not rewritten at all.
	BookSigMigrateStrippedOnly
	// BookSigMigrateSkippedRaced: the row changed under us between read and
	// commit, so nothing was written. See the CAS note on
	// MigrateBookSigToSidecar — skipping is always safe here.
	BookSigMigrateSkippedRaced
)

func (o BookSigMigrateOutcome) String() string {
	switch o {
	case BookSigMigrateNotCandidate:
		return "not_candidate"
	case BookSigMigrateMigrated:
		return "migrated"
	case BookSigMigrateStrippedOnly:
		return "stripped_only"
	case BookSigMigrateSkippedRaced:
		return "skipped_raced"
	}
	return fmt.Sprintf("unknown(%d)", int(o))
}

// BookSigMigrateStore is a narrow capability interface, deliberately kept OUT
// of database.Store — same rationale as SearchIndexDirtyStore: adding methods
// to database.Store forces every implementation and generated mock to grow with
// it. Reach it through AsBookSigMigrateStore, which looks through the
// indexedStore decorator.
type BookSigMigrateStore interface {
	// MigrateBookSigToSidecar moves one book's inline signature into its
	// `book_sig:` sidecar. Safe to call on an already-migrated book (returns
	// BookSigMigrateNotCandidate) and safe to re-run after a partial pass.
	MigrateBookSigToSidecar(id string, dryRun bool) (BookSigMigrateOutcome, error)
}

// AsBookSigMigrateStore returns s as a BookSigMigrateStore if the underlying
// store supports it (true for *PebbleStore), or nil otherwise. Callers MUST
// nil-check the result. Use this instead of `store.(*PebbleStore)`: prod always
// installs the Bleve indexedStore decorator, and a bare assertion against a
// wrapped store is indistinguishable from an unsupported backend — which is how
// several ops silently no-opped in prod (see AsPebbleStore's comment).
func AsBookSigMigrateStore(s any) BookSigMigrateStore {
	if s == nil {
		return nil
	}
	if ms, ok := AsCapability[BookSigMigrateStore](s); ok {
		return ms
	}
	return nil
}

// getRowCopy reads key and returns an owned copy of the value, or (nil, nil)
// when the key is absent. Pebble's Get hands back a slice valid only until the
// closer runs, so every byte we intend to keep — or later compare — must be
// copied out first.
func (p *PebbleStore) getRowCopy(key []byte) ([]byte, error) {
	val, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return bytes.Clone(val), nil
}

// stripBookSigJSONKeys removes the five signature keys from a stored row's JSON
// object, preserving every other key EXACTLY as stored.
//
// It deliberately does NOT round-trip through the Book struct. json.Unmarshal
// into Book silently drops any field present in the stored JSON but absent from
// the struct, so `json.Marshal(stripBookSigForRow(&book))` would rewrite the row
// with more removed than the five fields we intend to move. UpdateBook already
// has that property for rows it touches; a migration sweeping all 67,824 rows
// should not inherit it. Operating on map[string]json.RawMessage means unknown
// fields survive byte-for-byte.
//
// changed reports whether any of the five keys was actually present, so callers
// can distinguish "nothing to do" from "stripped".
func stripBookSigJSONKeys(row []byte) (out []byte, changed bool, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(row, &obj); err != nil {
		return nil, false, fmt.Errorf("unmarshal row object: %w", err)
	}
	for _, k := range bookSigJSONKeys {
		if _, ok := obj[k]; ok {
			delete(obj, k)
			changed = true
		}
	}
	if !changed {
		return row, false, nil
	}
	out, err = json.Marshal(obj)
	if err != nil {
		return nil, false, fmt.Errorf("marshal stripped row: %w", err)
	}
	return out, true, nil
}

// mergeBookSigSidecar decodes an existing sidecar payload and fills only its
// NIL fields from inline. Returns filled=false (and out=nil) when the sidecar
// already covers every field inline has, so the caller can leave the key alone
// rather than rewriting it byte-for-byte.
//
// The asymmetry is the point: a non-nil sidecar value is never replaced, so a
// newer sidecar cannot be downgraded by a stale inline copy; a nil one is
// always filled, so stripping the row cannot drop a field that lived only
// inline. Both directions of loss are closed.
func mergeBookSigSidecar(existing []byte, inline bookSigSidecar) (out []byte, filled bool, err error) {
	var cur bookSigSidecar
	if err := json.Unmarshal(existing, &cur); err != nil {
		return nil, false, fmt.Errorf("unmarshal existing sidecar: %w", err)
	}
	if cur.V1 == nil && inline.V1 != nil {
		cur.V1, filled = inline.V1, true
	}
	if cur.Mask == nil && inline.Mask != nil {
		cur.Mask, filled = inline.Mask, true
	}
	if cur.Segments == nil && inline.Segments != nil {
		cur.Segments, filled = inline.Segments, true
	}
	if cur.BuiltAt == nil && inline.BuiltAt != nil {
		cur.BuiltAt, filled = inline.BuiltAt, true
	}
	if cur.CoveragePct == nil && inline.CoveragePct != nil {
		cur.CoveragePct, filled = inline.CoveragePct, true
	}
	if !filled {
		return nil, false, nil
	}
	out, err = json.Marshal(cur)
	if err != nil {
		return nil, false, fmt.Errorf("marshal merged sidecar: %w", err)
	}
	return out, true, nil
}

// MigrateBookSigToSidecar moves book id's inline signature into the
// `book_sig:<id>` sidecar and rewrites the row without it, in ONE Pebble batch
// so there is never a moment where the row is stripped and the sidecar is
// missing. That pairing is load-bearing: a book with all five fields nil is
// EXACTLY the shape booksig_recovery_audit classifies as "never had a
// signature" rather than as damage, so a half-applied migration would be
// undetectable by the very op written to find signature loss, and dedup would
// simply stop matching that book.
//
// dryRun classifies without writing anything.
//
// CONCURRENCY / LOST-UPDATE
//
// PebbleStore has no per-book write serialization — none of its mutexes guard
// the book read-modify-write, and UpdateBook already races with itself. A naive
// read-then-write here would be worse than losing a signature: read the row at
// T0, a concurrent UpdateBook lands at T1, our batch at T2 reverts the ENTIRE
// T1 update (title, path, everything).
//
// The reframe that makes this tractable: every UpdateBook already strips the
// row it writes, so the library migrates organically and this op only
// accelerates it. Skipping any individual book is therefore always acceptable.
// So we re-read the raw row immediately before committing and write only if it
// is byte-identical to what we read; otherwise we report
// BookSigMigrateSkippedRaced and move on. Pebble offers no true CAS, so the
// window is not zero — but it is a byte compare rather than the whole
// unmarshal/strip/marshal span, and a skip costs nothing because a later run
// (or any UpdateBook) picks the book up.
func (p *PebbleStore) MigrateBookSigToSidecar(id string, dryRun bool) (BookSigMigrateOutcome, error) {
	if id == "" {
		return BookSigMigrateNotCandidate, fmt.Errorf("empty book id")
	}
	rowKey := []byte("book:" + id)

	before, err := p.getRowCopy(rowKey)
	if err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("read book row %q: %w", id, err)
	}
	// The book was deleted between ListBookIDs and now. Nothing to migrate;
	// this is not an error and must not be counted as one.
	if before == nil {
		return BookSigMigrateNotCandidate, nil
	}

	stripped, changed, err := stripBookSigJSONKeys(before)
	if err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("strip book row %q: %w", id, err)
	}
	if !changed {
		return BookSigMigrateNotCandidate, nil
	}

	// Read the five values for the sidecar payload. This unmarshal is used ONLY
	// to source values, never to rewrite the row (see stripBookSigJSONKeys).
	var book Book
	if err := json.Unmarshal(before, &book); err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("unmarshal book row %q: %w", id, err)
	}
	sig, ok := bookSigOf(&book)
	if !ok {
		// The keys were present but every value decoded to nil — e.g. all five
		// stored as explicit JSON null. There is no signature to preserve, so
		// stripping the row alone is correct and creating a sidecar would
		// manufacture an empty one.
		return p.commitBookSigMigration(id, rowKey, before, stripped, nil, dryRun, BookSigMigrateStrippedOnly)
	}

	// Sidecar already present? Then it is authoritative (bookSigSidecar.applyTo)
	// and the inline copy is stale by construction: post-#2387 every UpdateBook
	// writes a fresh sidecar AND strips the row, so a row still carrying inline
	// data alongside a sidecar means the sidecar is the newer value. Overwriting
	// it from the stale inline copy would be a silent downgrade.
	existing, err := p.getRowCopy(bookSigKey(id))
	if err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("read book signature sidecar %q: %w", id, err)
	}
	if existing != nil {
		// ...but "authoritative" is per-FIELD, not per-record. bookSigOf/
		// writeBookSigToBatch write a sidecar whenever ANY of the five is
		// non-nil, so a sidecar holding a strict subset of the row's fields is
		// constructible — most plausibly by a rollback to a pre-#2387 binary,
		// which writes inline again while an older sidecar lingers. Stripping
		// all five inline keys and leaving that sidecar alone would delete the
		// fields it lacks from BOTH places: exactly the undetectable loss this
		// whole function exists to prevent.
		//
		// So fill, don't skip. Present sidecar values still win; only nil ones
		// are sourced from inline. That cannot downgrade a newer sidecar and
		// cannot lose an inline field. Counted as stripped_only either way —
		// the row-side effect is the same.
		merged, filled, err := mergeBookSigSidecar(existing, sig)
		if err != nil {
			return BookSigMigrateNotCandidate, fmt.Errorf("merge book signature sidecar %q: %w", id, err)
		}
		if !filled {
			merged = nil // sidecar already covers every inline field; leave it untouched.
		}
		return p.commitBookSigMigration(id, rowKey, before, stripped, merged, dryRun, BookSigMigrateStrippedOnly)
	}

	payload, err := json.Marshal(sig)
	if err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("marshal book signature sidecar %q: %w", id, err)
	}
	return p.commitBookSigMigration(id, rowKey, before, stripped, payload, dryRun, BookSigMigrateMigrated)
}

// commitBookSigMigration performs the CAS re-read and the single-batch write.
// sidecar may be nil, meaning "write the stripped row only, create no sidecar
// key" — the stripped_only path. It NEVER deletes a sidecar key.
func (p *PebbleStore) commitBookSigMigration(
	id string,
	rowKey, before, stripped, sidecar []byte,
	dryRun bool,
	outcome BookSigMigrateOutcome,
) (BookSigMigrateOutcome, error) {
	if dryRun {
		return outcome, nil
	}

	after, err := p.getRowCopy(rowKey)
	if err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("re-read book row %q: %w", id, err)
	}
	if after == nil || !bytes.Equal(before, after) {
		return BookSigMigrateSkippedRaced, nil
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	if sidecar != nil {
		if err := batch.Set(bookSigKey(id), sidecar, nil); err != nil {
			return BookSigMigrateNotCandidate, fmt.Errorf("stage sidecar %q: %w", id, err)
		}
	}
	if err := batch.Set(rowKey, stripped, nil); err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("stage stripped row %q: %w", id, err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return BookSigMigrateNotCandidate, fmt.Errorf("commit book signature migration %q: %w", id, err)
	}
	return outcome, nil
}
