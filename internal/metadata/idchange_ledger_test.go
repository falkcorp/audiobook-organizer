// file: internal/metadata/idchange_ledger_test.go
// version: 1.0.0
// guid: 4d7a1c85-6e29-4b03-9f71-0c8d5e2a3b64
// last-edited: 2026-09-19

package metadata

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ledgerStore records every metadata-history row BatchUpdateMetadata writes, so
// a test can assert on what the audit trail ended up claiming.
type ledgerStore struct {
	batchUpdateStore
	mu      sync.Mutex
	records []*database.MetadataChangeRecord
	failWit error // when set, ModifyBook fails with it
}

func (s *ledgerStore) RecordMetadataChange(rec *database.MetadataChangeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
	return nil
}

func (s *ledgerStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.failWit != nil {
		return nil, s.failWit
	}
	return s.batchUpdateStore.ModifyBook(id, fn)
}

func (s *ledgerStore) rows() []*database.MetadataChangeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*database.MetadataChangeRecord, len(s.records))
	copy(out, s.records)
	return out
}

// recordFor returns the single ledger row for field, failing if there is not
// exactly one.
func recordFor(t *testing.T, rows []*database.MetadataChangeRecord, field string) *database.MetadataChangeRecord {
	t.Helper()
	var hits []*database.MetadataChangeRecord
	for _, r := range rows {
		if r.Field == field {
			hits = append(hits, r)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 ledger row for %s, got %d (%+v)", field, len(hits), rows)
	}
	return hits[0]
}

// decodeIDPtr reads back the JSON-encoded nullable ID the ledger stores.
func decodeIDPtr(t *testing.T, s *string) *int {
	t.Helper()
	if s == nil {
		t.Fatalf("ledger value is nil, want JSON-encoded id or \"null\"")
	}
	var id *int
	if err := json.Unmarshal([]byte(*s), &id); err != nil {
		t.Fatalf("decode ledger value %q: %v", *s, err)
	}
	return id
}

// TestBatchUpdateMetadata_NoLedgerRowWhenTheBookWriteFails pins the ordering of
// the metadata-history write against the book write.
//
// recordIDChange used to run at resolution time, before ModifyBook. When the
// write then failed, the history kept a row saying the book's author changed
// while the book itself still carried the old id -- an audit trail describing a
// change that never happened. This is the same shape as the phantom-repair
// ledger fixed in #3437.
func TestBatchUpdateMetadata_NoLedgerRowWhenTheBookWriteFails(t *testing.T) {
	base := setupBatchPebbleStore(t)

	seeded, err := base.CreateBook(&database.Book{Title: "Ledger Probe", FilePath: "/books/ledger-fail.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	store := &ledgerStore{batchUpdateStore: base, failWit: fmt.Errorf("pebble: write failed")}

	errs, ok := BatchUpdateMetadata([]MetadataUpdate{{
		BookID:  seeded.ID,
		Updates: map[string]any{"author": "Resolved Author", "series": "Resolved Series"},
	}}, store, false)

	if ok != 0 {
		t.Fatalf("successCount = %d, want 0 when the write fails", ok)
	}
	if len(errs) == 0 {
		t.Fatalf("want an error for the failed write, got none")
	}
	if rows := store.rows(); len(rows) != 0 {
		t.Fatalf("the failed write left %d metadata-history row(s) behind: %+v", len(rows), rows)
	}
}

// TestBatchUpdateMetadata_LedgerPreviousValueComesFromTheStoredRow pins what
// the ledger names as the value that was replaced.
//
// The worker reads the book, then does store IO to resolve the author, so
// another writer can land a different AuthorID in that gap. The previous value
// has to be read under the write stripe -- the id actually replaced -- not the
// one this worker happened to read beforehand, or the history points at a value
// that was never there at write time.
func TestBatchUpdateMetadata_LedgerPreviousValueComesFromTheStoredRow(t *testing.T) {
	base := setupBatchPebbleStore(t)

	stale, err := base.CreateAuthor("Stale Author")
	if err != nil {
		t.Fatalf("CreateAuthor stale: %v", err)
	}
	racer, err := base.CreateAuthor("Racing Author")
	if err != nil {
		t.Fatalf("CreateAuthor racer: %v", err)
	}

	seeded, err := base.CreateBook(&database.Book{
		Title:    "Ledger Prev Probe",
		FilePath: "/books/ledger-prev.m4b",
		AuthorID: &stale.ID,
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	ledger := &ledgerStore{batchUpdateStore: base}
	race := &batchRaceStore{batchUpdateStore: ledger}
	race.onRead = func(bookID string) {
		if _, merr := base.ModifyBook(bookID, func(cur *database.Book) error {
			cur.AuthorID = &racer.ID
			return nil
		}); merr != nil {
			t.Errorf("concurrent author write failed: %v", merr)
		}
	}

	errs, ok := BatchUpdateMetadata([]MetadataUpdate{{
		BookID:  seeded.ID,
		Updates: map[string]any{"author": "Final Author"},
	}}, race, false)
	if len(errs) != 0 {
		t.Fatalf("BatchUpdateMetadata returned errors: %v", errs)
	}
	if ok != 1 {
		t.Fatalf("successCount = %d, want 1", ok)
	}

	final, err := base.GetBookByID(seeded.ID)
	if err != nil || final == nil {
		t.Fatalf("GetBookByID: %v (book %v)", err, final)
	}
	if final.AuthorID == nil {
		t.Fatalf("book ended with no AuthorID")
	}

	rec := recordFor(t, ledger.rows(), "author_id")
	prev := decodeIDPtr(t, rec.PreviousValue)
	next := decodeIDPtr(t, rec.NewValue)

	if prev == nil || *prev != racer.ID {
		t.Errorf("ledger previous author id = %v, want %d (the id on the stored row at write time, not the stale read %d)",
			prev, racer.ID, stale.ID)
	}
	if next == nil || *next != *final.AuthorID {
		t.Errorf("ledger new author id = %v, want %d (the id actually written)", next, *final.AuthorID)
	}
}
