// file: internal/database/memdb_sync.go
// version: 1.1.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000005
// last-edited: 2026-08-06

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// Write-through helpers from PebbleStore → MemStore.
//
// Each helper is a no-op when the MemStore is not initialized. Errors are
// logged but do not propagate (Pebble remains the source of truth; a memdb
// sync miss is recoverable by re-running WarmFromPebble on restart).
//
// The functions in this file intentionally mirror the shape of chai_sync.go
// so the call-site changes in Phase 2 are a one-line addition.

// memSync runs fn inside a write transaction. Returns immediately if memdb
// is not initialized. Always commits on success; aborts and logs on error.
func (p *PebbleStore) memSync(op string, fn func(txn memTxn) error) {
	if p.mem() == nil {
		return
	}
	txn := p.mem().db.Txn(true)
	if err := fn(txn); err != nil {
		txn.Abort()
		slog.Warn("memdb sync failed (pebble still authoritative)",
			"op", op, "error", err)
		return
	}
	txn.Commit()
}

// memTxn aliases the memdb transaction type so callers don't have to import
// the memdb package directly.
type memTxn interface {
	Insert(table string, obj interface{}) error
	Delete(table string, obj interface{}) error
	DeleteAll(table, index string, args ...interface{}) (int, error)
	First(table, index string, args ...interface{}) (interface{}, error)
}

// ── Book ────────────────────────────────────────────────────────────────────

// UpsertBookToMemDB inserts or replaces a book and its associated relationships
// (book_authors flattened to rows, book_files reloaded from Pebble) in memdb.
func (p *PebbleStore) UpsertBookToMemDB(ctx context.Context, book *Book) {
	if book == nil {
		return
	}
	p.memSync("UpsertBook", func(txn memTxn) error {
		// Strip heavy fields (Description, BookSigV1, etc.) — memdb
		// only needs lightweight projections for indexed iteration.
		// Pebble retains the full Book; callers needing full payload
		// hit GetBookByID. See memdb_strip.go.
		if err := txn.Insert(memTableBooks, stripBookForMemdb(book)); err != nil {
			return fmt.Errorf("insert book: %w", err)
		}

		// book_authors: clear existing rows for this book, then reinsert from Pebble.
		if _, err := txn.DeleteAll(memTableBookAuthors, memIdxBookID, book.ID); err != nil {
			return fmt.Errorf("clear book_authors: %w", err)
		}
		if bas, baErr := p.GetBookAuthors(book.ID); baErr == nil {
			for i := range bas {
				ba := bas[i]
				if err := txn.Insert(memTableBookAuthors, &ba); err != nil {
					return fmt.Errorf("insert book_author: %w", err)
				}
			}
		}

		// book_narrators
		if _, err := txn.DeleteAll(memTableBookNarrators, memIdxBookID, book.ID); err != nil {
			return fmt.Errorf("clear book_narrators: %w", err)
		}
		if bns, bnErr := p.GetBookNarrators(book.ID); bnErr == nil {
			for i := range bns {
				bn := bns[i]
				if err := txn.Insert(memTableBookNarrators, &bn); err != nil {
					return fmt.Errorf("insert book_narrator: %w", err)
				}
			}
		}

		// book_files: clear and reload from Pebble
		if _, err := txn.DeleteAll(memTableBookFiles, memIdxBookID, book.ID); err != nil {
			return fmt.Errorf("clear book_files: %w", err)
		}
		files, fileErr := p.loadBookFilesForBookID(book.ID)
		if fileErr == nil {
			for i := range files {
				bf := files[i]
				if err := txn.Insert(memTableBookFiles, stripBookFileForMemdb(&bf)); err != nil {
					return fmt.Errorf("insert book_file: %w", err)
				}
			}
		}
		return nil
	})
}

// DeleteBookFromMemDB removes a book and all of its associated rows.
func (p *PebbleStore) DeleteBookFromMemDB(ctx context.Context, bookID string) {
	if bookID == "" {
		return
	}
	p.memSync("DeleteBook", func(txn memTxn) error {
		// Look up existing book object so we can call Delete with the same struct.
		obj, err := txn.First(memTableBooks, memIdxID, bookID)
		if err == nil && obj != nil {
			if err := txn.Delete(memTableBooks, obj); err != nil {
				return fmt.Errorf("delete book: %w", err)
			}
		}
		if _, err := txn.DeleteAll(memTableBookAuthors, memIdxBookID, bookID); err != nil {
			return fmt.Errorf("delete book_authors: %w", err)
		}
		if _, err := txn.DeleteAll(memTableBookNarrators, memIdxBookID, bookID); err != nil {
			return fmt.Errorf("delete book_narrators: %w", err)
		}
		if _, err := txn.DeleteAll(memTableBookFiles, memIdxBookID, bookID); err != nil {
			return fmt.Errorf("delete book_files: %w", err)
		}
		return nil
	})
}

// ── BookFile ───────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertBookFileToMemDB(bf *BookFile) {
	if bf == nil {
		return
	}
	p.memSync("UpsertBookFile", func(txn memTxn) error {
		return txn.Insert(memTableBookFiles, stripBookFileForMemdb(bf))
	})
}

func (p *PebbleStore) DeleteBookFileFromMemDB(fileID string) {
	if fileID == "" {
		return
	}
	p.memSync("DeleteBookFile", func(txn memTxn) error {
		obj, err := txn.First(memTableBookFiles, memIdxID, fileID)
		if err == nil && obj != nil {
			return txn.Delete(memTableBookFiles, obj)
		}
		return nil
	})
}

// DeleteBookFilesFromMemDB removes many book_file rows in ONE memdb write
// transaction.
//
// WHY this exists rather than looping DeleteBookFileFromMemDB: go-memdb serialises
// every writer behind a single global mutex, so N calls means N acquisitions of a
// lock that every other writer in the process is also queuing on. Deleting 15 rows
// for one book took 15 turns through that mutex; it now takes one. The saving is
// not the txn bookkeeping, it is the contention removed from unrelated writers.
//
// Semantics are otherwise identical to N successive DeleteBookFileFromMemDB calls:
// IDs that are absent from memdb are skipped silently (Pebble stays authoritative,
// and memdb is a derived cache that a later resync will correct anyway).
func (p *PebbleStore) DeleteBookFilesFromMemDB(fileIDs []string) {
	if len(fileIDs) == 0 {
		return
	}
	p.memSync("DeleteBookFiles", func(txn memTxn) error {
		for _, id := range fileIDs {
			if id == "" {
				continue
			}
			obj, err := txn.First(memTableBookFiles, memIdxID, id)
			if err != nil || obj == nil {
				// Not present (or unreadable). memSync aborts the WHOLE txn on a
				// returned error, so a single missing row must not be one — that
				// would drop the other N-1 deletes for no benefit.
				continue
			}
			if err := txn.Delete(memTableBookFiles, obj); err != nil {
				return fmt.Errorf("delete book_file %s: %w", id, err)
			}
		}
		return nil
	})
}

// ── Author ─────────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertAuthorToMemDB(a *Author) {
	if a == nil {
		return
	}
	p.memSync("UpsertAuthor", func(txn memTxn) error {
		return txn.Insert(memTableAuthors, a)
	})
}

func (p *PebbleStore) DeleteAuthorFromMemDB(id int) {
	p.memSync("DeleteAuthor", func(txn memTxn) error {
		obj, err := txn.First(memTableAuthors, memIdxID, id)
		if err == nil && obj != nil {
			return txn.Delete(memTableAuthors, obj)
		}
		return nil
	})
}

// ── Series ─────────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertSeriesToMemDB(s *Series) {
	if s == nil {
		return
	}
	p.memSync("UpsertSeries", func(txn memTxn) error {
		return txn.Insert(memTableSeries, s)
	})
}

func (p *PebbleStore) DeleteSeriesFromMemDB(id int) {
	p.memSync("DeleteSeries", func(txn memTxn) error {
		obj, err := txn.First(memTableSeries, memIdxID, id)
		if err == nil && obj != nil {
			return txn.Delete(memTableSeries, obj)
		}
		return nil
	})
}

// ── Narrator ───────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertNarratorToMemDB(n *Narrator) {
	if n == nil {
		return
	}
	p.memSync("UpsertNarrator", func(txn memTxn) error {
		return txn.Insert(memTableNarrators, n)
	})
}

// ── BookAuthor / BookNarrator (relationships) ──────────────────────────────

func (p *PebbleStore) ReplaceBookAuthorsInMemDB(bookID string, authors []BookAuthor) {
	if bookID == "" {
		return
	}
	p.memSync("ReplaceBookAuthors", func(txn memTxn) error {
		if _, err := txn.DeleteAll(memTableBookAuthors, memIdxBookID, bookID); err != nil {
			return err
		}
		for i := range authors {
			a := authors[i]
			if err := txn.Insert(memTableBookAuthors, &a); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *PebbleStore) ReplaceBookNarratorsInMemDB(bookID string, narrators []BookNarrator) {
	if bookID == "" {
		return
	}
	p.memSync("ReplaceBookNarrators", func(txn memTxn) error {
		if _, err := txn.DeleteAll(memTableBookNarrators, memIdxBookID, bookID); err != nil {
			return err
		}
		for i := range narrators {
			n := narrators[i]
			if err := txn.Insert(memTableBookNarrators, &n); err != nil {
				return err
			}
		}
		return nil
	})
}

// ── ImportPath ─────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertImportPathToMemDB(ip *ImportPath) {
	if ip == nil {
		return
	}
	p.memSync("UpsertImportPath", func(txn memTxn) error {
		return txn.Insert(memTableImportPaths, ip)
	})
}

func (p *PebbleStore) DeleteImportPathFromMemDB(id int) {
	p.memSync("DeleteImportPath", func(txn memTxn) error {
		obj, err := txn.First(memTableImportPaths, memIdxID, id)
		if err == nil && obj != nil {
			return txn.Delete(memTableImportPaths, obj)
		}
		return nil
	})
}

// ── AuthorAlias ────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertAuthorAliasToMemDB(aa *AuthorAlias) {
	if aa == nil {
		return
	}
	p.memSync("UpsertAuthorAlias", func(txn memTxn) error {
		return txn.Insert(memTableAuthorAliases, aa)
	})
}

func (p *PebbleStore) DeleteAuthorAliasFromMemDB(id int) {
	p.memSync("DeleteAuthorAlias", func(txn memTxn) error {
		obj, err := txn.First(memTableAuthorAliases, memIdxID, id)
		if err == nil && obj != nil {
			return txn.Delete(memTableAuthorAliases, obj)
		}
		return nil
	})
}

func (p *PebbleStore) DeleteAuthorAliasesByAuthorIDFromMemDB(authorID int) {
	p.memSync("DeleteAuthorAliasesByAuthor", func(txn memTxn) error {
		_, err := txn.DeleteAll(memTableAuthorAliases, memIdxAuthorID, authorID)
		return err
	})
}

// ── BlockedHash ────────────────────────────────────────────────────────────

func (p *PebbleStore) UpsertBlockedHashToMemDB(b *DoNotImport) {
	if b == nil {
		return
	}
	p.memSync("UpsertBlockedHash", func(txn memTxn) error {
		return txn.Insert(memTableBlockedHashes, b)
	})
}

func (p *PebbleStore) DeleteBlockedHashFromMemDB(hash string) {
	if hash == "" {
		return
	}
	p.memSync("DeleteBlockedHash", func(txn memTxn) error {
		obj, err := txn.First(memTableBlockedHashes, memIdxHash, hash)
		if err == nil && obj != nil {
			return txn.Delete(memTableBlockedHashes, obj)
		}
		return nil
	})
}

// ── Work ───────────────────────────────────────────────────────────────────
//
// Works are NOT mirrored into memdb (dropped in PR for I2 — 211K rows × ~590B
// = ~120MB heap saved). The write-through helpers are kept as no-op stubs so
// existing call sites compile without churn; Pebble remains source of truth.

func (p *PebbleStore) UpsertWorkToMemDB(w *Work) {}

func (p *PebbleStore) DeleteWorkFromMemDB(id string) {}

// ── Internal helpers ───────────────────────────────────────────────────────

// loadBookFilesForBookID returns every BookFile stored under
// book_file:<bookID>:<fileID>. Used by UpsertBookToMemDB to refresh the
// book_files rows after a book mutation.
func (p *PebbleStore) loadBookFilesForBookID(bookID string) ([]BookFile, error) {
	prefix := []byte(fmt.Sprintf("book_file:%s:", bookID))
	upper := append([]byte(nil), prefix...)
	upper[len(upper)-1] = ';'
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []BookFile
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, string(prefix)) {
			continue
		}
		var bf BookFile
		if err := json.Unmarshal(iter.Value(), &bf); err == nil {
			out = append(out, bf)
		}
	}
	return out, nil
}
