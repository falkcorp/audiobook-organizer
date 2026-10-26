// file: internal/plugins/maintenance/dedupe_partial_chunk_delete_test.go
// version: 1.0.0
// guid: 4e8b2d71-6c3a-4f95-b0d8-1a7c5e9f3b26
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// dedupeChunk is the store's commit chunk for a bulk book_file delete
// (database deleteBookFilesChunk): a call over it commits chunk by chunk.
const dedupeChunk = 64

// chunkTwoFailStore is a PebbleStore whose bulk delete behaves as the real one
// does when its SECOND chunk gives up: the first dedupeChunk rows are deleted
// for real and returned with the error, the rest are untouched.
type chunkTwoFailStore struct {
	*database.PebbleStore
}

func (s *chunkTwoFailStore) DeleteBookFilesByIDs(refs []database.BookFileRef) ([]database.BookFile, error) {
	if len(refs) <= dedupeChunk {
		return s.PebbleStore.DeleteBookFilesByIDs(refs)
	}
	deleted, err := s.PebbleStore.DeleteBookFilesByIDs(refs[:dedupeChunk])
	if err != nil {
		return deleted, err
	}
	return deleted, fmt.Errorf("chunk 2 gave up: %w", database.ErrBookFileConflict)
}

func liveDeleteJournalRows(t *testing.T, s *database.PebbleStore, bookID string) (live, voided int) {
	t.Helper()
	changes, err := s.GetBookChanges(bookID)
	if err != nil {
		t.Fatalf("GetBookChanges: %v", err)
	}
	for _, c := range changes {
		if c.ChangeType != "book_file_delete" {
			continue
		}
		if c.Voided {
			voided++
		} else {
			live++
		}
	}
	return live, voided
}

// A book with more redundant rows than one commit chunk, whose second chunk
// fails: the rows the first chunk deleted are reported as deleted and counted,
// the book is recomputed, and only the rows that survive are reported failed.
func TestDedupeBookFileRows_PartialChunkedDeleteIsReportedAsPartial(t *testing.T) {
	const copies = 70 // one keeper, 69 redundant: chunk 1 deletes 64, chunk 2 (5) fails
	s, bookIDs := newDupStore(t, 1, copies)
	bookID := bookIDs[0]
	report := t.TempDir() + "/dupes.tsv"

	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: &chunkTwoFailStore{PebbleStore: s}}, root: t.TempDir()}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true, BookIDs: []string{bookID}, ReportPath: report})
	rep := &concurrentReporter{}
	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows must degrade, not abort: %v", err)
	}

	assertRowCounts(t, s, bookIDs, copies-dedupeChunk)
	summary := rep.loggedText()
	for _, want := range []string{
		"deleted 64 (salvaged fields on 0 keepers, recomputed 1 books)",
		", failed 1 ",
		"0 would delete, 64 deleted, 0 skipped, 5 failed",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q; logged:\n%s", want, summary)
		}
	}
	if live, voided := liveDeleteJournalRows(t, s, bookID); live != dedupeChunk || voided != copies-1-dedupeChunk {
		t.Errorf("journal rows = %d live, %d voided; want %d live (the deleted rows) and %d voided", live, voided, dedupeChunk, copies-1-dedupeChunk)
	}
}

// The remove_row_ids path has the same shape: 70 named rows, chunk 2 fails.
func TestRemoveRowIDs_PartialChunkedDeleteIsReportedAsPartial(t *testing.T) {
	s := newDupeReportStore(t)
	report := t.TempDir() + "/r.tsv"
	bookID := cfSeedBook(t, s, "Batman: Hush", "/lib/Gotham/Hush")
	keeper := cfSeedRow(t, s, bookID, "/lib/Gotham/Hush/00.m4b", 10, 60, "gotham-keeper")
	const named = 70
	ids := make([]string, 0, named)
	for i := range named {
		ids = append(ids, cfSeedRow(t, s, bookID, fmt.Sprintf("/lib/Gotham/Hush/%02d.m4b", i+1), 10, 60, ""))
	}

	p := &Plugin{deps: fakeDeps{store: &chunkTwoFailStore{PebbleStore: s}}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true, RemoveRowIDs: ids, ConfirmedDuplicate: true, ReportPath: report})
	rep := &concurrentReporter{}
	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows must degrade, not abort: %v", err)
	}

	left := cfRowIDs(t, s, bookID)
	if want := 1 + named - dedupeChunk; len(left) != want || !left[keeper] {
		t.Fatalf("rows after the run = %d (keeper kept: %v), want %d", len(left), left[keeper], want)
	}
	summary := rep.loggedText()
	for _, want := range []string{
		"70 named, 64 deleted, recomputed 1 books, failed 1 ",
		"0 would delete, 64 deleted, 0 skipped, 6 failed",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q; logged:\n%s", want, summary)
		}
	}
	if live, voided := liveDeleteJournalRows(t, s, bookID); live != dedupeChunk || voided != named-dedupeChunk {
		t.Errorf("journal rows = %d live, %d voided; want %d live and %d voided", live, voided, dedupeChunk, named-dedupeChunk)
	}
}
