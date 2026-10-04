// file: internal/database/memdb_census.go
// version: 1.1.0
// guid: 1e761a4e-5430-4277-ab3b-531d3b193ad1
// last-edited: 2026-10-04

package database

import "fmt"

// retiredAndSignalCensus counts retired books (soft-deleted, or merged into
// another book) and the book_file rows they own, plus the fingerprint and
// transcript counts. Pointer reads only, no JSON — the same cost class as
// ListBookIDs. It refuses to answer from a memdb that lost rows on warmup,
// because a short count here would read as "nothing to reclaim".
func (m *MemStore) retiredAndSignalCensus() (RetiredCensus, SignalCensus, error) {
	var retired RetiredCensus
	var signals SignalCensus
	if err := m.requireTablesComplete("db census retired/signal counts", memTableBooks, memTableBookFiles); err != nil {
		return retired, signals, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()

	books, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return retired, signals, fmt.Errorf("memdb census books: %w", err)
	}
	for obj := books.Next(); obj != nil; obj = books.Next() {
		b := obj.(*Book)
		if b.IntroTranscribedAt != nil {
			signals.BooksWithTranscript++
		}
		soft := bookIsSoftDeleted(b)
		merged := b.MergedIntoBookID != nil && *b.MergedIntoBookID != ""
		if soft {
			retired.SoftDeletedBooks++
		}
		if merged {
			retired.MergedBooks++
		}
		if !soft && !merged {
			continue
		}
		retired.RetiredBooks++
		files, err := txn.Get(memTableBookFiles, memIdxBookID, b.ID)
		if err != nil {
			return retired, signals, fmt.Errorf("memdb census files of %s: %w", b.ID, err)
		}
		for f := files.Next(); f != nil; f = files.Next() {
			retired.RetiredBookFiles++
		}
	}

	files, err := txn.Get(memTableBookFiles, memIdxID)
	if err != nil {
		return retired, signals, fmt.Errorf("memdb census book files: %w", err)
	}
	for obj := files.Next(); obj != nil; obj = files.Next() {
		f := obj.(*BookFile)
		if f.AcoustIDFingerprintDurationSec > 0 {
			signals.FilesWithFingerprint++
		}
		if f.IntroTranscribedAt != nil {
			signals.FilesWithTranscript++
		}
	}
	return retired, signals, nil
}

// bookRowExists reports whether memdb's books table has a row with this id.
// It refuses (error) when memdb lost book rows at warmup, since a missing row
// would then read as an orphan.
func (m *MemStore) bookRowExists(id string) (bool, error) {
	if err := m.requireTablesComplete("db census history orphans", memTableBooks); err != nil {
		return false, err
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	obj, err := txn.First(memTableBooks, memIdxID, id)
	if err != nil {
		return false, fmt.Errorf("memdb census book %s: %w", id, err)
	}
	return obj != nil, nil
}
