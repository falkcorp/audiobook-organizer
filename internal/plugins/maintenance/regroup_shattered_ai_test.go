// file: internal/plugins/maintenance/regroup_shattered_ai_test.go
// version: 1.0.0
// guid: 3a9f6c04-8e21-4b57-9d0a-6f2e7c1b5a48
// last-edited: 2026-07-13

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seedShatterBook creates a single-file book at a chapter-shatter path so the dry-run
// op's scan groups it into a review hold.
func seedShatterBook(t *testing.T, s *database.PebbleStore, path string) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{})
	if err != nil || b == nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path}); err != nil {
		t.Fatalf("CreateBookFile(%s): %v", path, err)
	}
	return b.ID
}

// countBooksAndFiles returns the TOTAL live book and book-file counts across the
// whole store (not just a seeded subset), so the zero-mutation assertion proves the
// dry-run neither deleted nor CREATED any book/file rows.
func countBooksAndFiles(t *testing.T, s *database.PebbleStore) (books, files int) {
	t.Helper()
	ids, err := s.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	for _, id := range ids {
		books++
		bf, _ := s.GetBookFiles(id)
		files += len(bf)
	}
	return books, files
}

// The dry-run op must write review-queue rows and make ZERO book/file mutations, and
// re-running it must be idempotent (same review-row count, not doubled).
func TestRegroupShatteredAI_DryRun_WritesHolds_ZeroMutations_Idempotent(t *testing.T) {
	s := regroupStore(t)

	// A chapter-shatter book folder: 6 single-file chapter shells under one book folder.
	base := "/lib/Adrian Tchaikovsky/Cage of Souls"
	var ids []string
	for i := 1; i <= 6; i++ {
		ids = append(ids, seedShatterBook(t, s, base+"/Cage of Souls - "+itoa(i)+"/01.mp3"))
	}
	// A lone genuine single-file book that must NOT produce a hold.
	ids = append(ids, seedShatterBook(t, s, "/lib/Andy Weir/The Martian/The Martian.m4b"))

	beforeBooks, beforeFiles := countBooksAndFiles(t, s)

	p := &Plugin{deps: fakeDeps{store: s}}
	rep := &fakeReporter{}

	// Run #1.
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #1: %v", err)
	}

	// ZERO book/file mutations.
	afterBooks, afterFiles := countBooksAndFiles(t, s)
	if afterBooks != beforeBooks || afterFiles != beforeFiles {
		t.Fatalf("dry-run mutated books/files: books %d→%d, files %d→%d",
			beforeBooks, afterBooks, beforeFiles, afterFiles)
	}

	// Exactly one review hold (the chapter-shatter folder; the lone single is skipped).
	items1, total1, err := s.ListReviewItems(database.ReviewFilter{})
	if err != nil {
		t.Fatalf("ListReviewItems: %v", err)
	}
	if total1 != 1 {
		t.Fatalf("run #1 wrote %d review items, want 1: %+v", total1, items1)
	}
	hold := items1[0]
	if hold.Kind != "regroup.multidisc" {
		t.Errorf("hold kind = %q, want regroup.multidisc", hold.Kind)
	}
	if hold.FolderRef != base {
		t.Errorf("hold folderRef = %q, want %q", hold.FolderRef, base)
	}
	if hold.Status != database.ReviewStatusPending {
		t.Errorf("hold status = %q, want pending", hold.Status)
	}
	// Payload carries the 6 member book IDs and the derived survivor title.
	var payload regroupPayload
	if err := json.Unmarshal([]byte(hold.Payload), &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if len(payload.MemberBookIDs) != 6 || len(payload.Files) != 6 {
		t.Errorf("payload members=%d files=%d, want 6/6", len(payload.MemberBookIDs), len(payload.Files))
	}
	if payload.SurvivorTitle != "Cage of Souls" {
		t.Errorf("payload survivorTitle = %q, want 'Cage of Souls'", payload.SurvivorTitle)
	}
	if payload.Confidence != "high" {
		t.Errorf("payload confidence = %q, want high", payload.Confidence)
	}

	// Run #2 — UPSERT idempotency: the same folder must NOT create a second row.
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	_, total2, err := s.ListReviewItems(database.ReviewFilter{})
	if err != nil {
		t.Fatalf("ListReviewItems #2: %v", err)
	}
	if total2 != 1 {
		t.Fatalf("run #2 review items = %d, want 1 (upsert must not duplicate)", total2)
	}

	// Books/files still untouched after the second run.
	after2Books, after2Files := countBooksAndFiles(t, s)
	if after2Books != beforeBooks || after2Files != beforeFiles {
		t.Fatalf("run #2 mutated books/files: books %d→%d, files %d→%d",
			beforeBooks, after2Books, beforeFiles, after2Files)
	}
}

// A human-rejected hold must not be resurfaced (status preserved) on a re-scan.
func TestRegroupShatteredAI_DryRun_PreservesDecision(t *testing.T) {
	s := regroupStore(t)
	base := "/lib/Brandon Sanderson/Elantris"
	for i := 1; i <= 5; i++ {
		seedShatterBook(t, s, base+"/Elantris - "+itoa(i)+"/01.mp3")
	}

	p := &Plugin{deps: fakeDeps{store: s}}
	rep := &fakeReporter{}
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #1: %v", err)
	}
	items, _, _ := s.ListReviewItems(database.ReviewFilter{})
	if len(items) != 1 {
		t.Fatalf("want 1 hold, got %d", len(items))
	}
	// Human rejects it.
	if _, err := s.SetReviewItemStatus(items[0].ID, database.ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// Re-scan must leave the rejected item rejected (no resurfacing).
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	after, total, _ := s.ListReviewItems(database.ReviewFilter{})
	if total != 1 {
		t.Fatalf("re-scan changed item count to %d, want 1", total)
	}
	if after[0].Status != database.ReviewStatusRejected {
		t.Errorf("re-scan resurfaced a rejected hold: status=%q", after[0].Status)
	}
}
