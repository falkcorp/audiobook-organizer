// file: internal/database/memdb_metadata_hash.go
// version: 1.1.1
// guid: 5b8e0f3a-2c71-4d96-b0e4-7a19c3d52e84
// last-edited: 2026-10-10

package database

import "fmt"

// GetBooksByMetadataSourceHash is the memdb twin of PebbleStore's method of the
// same name: every live (not soft-deleted), unmerged book whose
// MetadataSourceHash equals hash, read through memIdxMetadataSourceHash.
//
// WHY. Every metadata apply runs the MATCH-4 duplicate check
// (metafetch checkMetadataSourceHashDuplicates), which calls this. The Pebble
// version walks and JSON-decodes every book row, so on a production-sized
// library each apply paid a whole-table scan for a lookup that returns zero
// or one row. On 2026-09-13 a single-book apply spent ~35s before its cover
// download with no log line; this scan is on that path.
//
// Fails closed: when memdb has lost book rows (requireTablesComplete), it
// returns an error and the caller falls back to the authoritative Pebble scan
// rather than trusting an index that may be missing the duplicate.
func (m *MemStore) GetBooksByMetadataSourceHash(hash string) ([]Book, error) {
	if err := m.requireTablesComplete("books by metadata_source_hash", memTableBooks); err != nil {
		return nil, err
	}
	if hash == "" {
		// The index skips empty hashes, and the Pebble scan can only match an
		// empty hash on a row whose pointer is set to "" -- never a real
		// duplicate. Nothing to return.
		return nil, nil
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	iter, err := txn.Get(memTableBooks, memIdxMetadataSourceHash, hash)
	if err != nil {
		return nil, fmt.Errorf("memdb books by metadata_source_hash: %w", err)
	}
	var books []Book
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if bookIsSoftDeleted(b) || b.MergedIntoBookID != nil {
			continue
		}
		if b.MetadataSourceHash == nil || *b.MetadataSourceHash != hash {
			continue
		}
		books = append(books, *b)
	}
	return books, nil
}

// GetBooksByMetadataSourceHashInMemory is GetBooksByMetadataSourceHash
// answered from memdb only: it never falls back to the Pebble scan. It
// returns an error wrapping ErrMemDBNotReady when memdb is disabled
// (UseMemDB=false) or not published yet (warmup running or failed), and
// memdb's ErrMemdbIncomplete error when memdb has lost book rows.
//
// WHY. A caller holding a book's write stripe (a ModifyBook callback, such
// as the version twin fixer's under-lock guard) must not run the ~35s scan
// the fallback is (pebble_store_book_lock.go: nothing slow runs under a
// stripe). Such a caller runs the full lookup before taking the stripe and
// this one under it, and treats an error as "cannot tell": fail closed.
func (p *PebbleStore) GetBooksByMetadataSourceHashInMemory(hash string) ([]Book, error) {
	if !p.UseMemDB {
		return nil, fmt.Errorf("%w: memdb is disabled on this store (UseMemDB=false)", ErrMemDBNotReady)
	}
	m := p.memOrRefuse("GetBooksByMetadataSourceHashInMemory")
	if m == nil {
		return nil, fmt.Errorf("%w: memdb is not published yet (warmup still running, or it failed)", ErrMemDBNotReady)
	}
	return m.GetBooksByMetadataSourceHash(hash)
}
