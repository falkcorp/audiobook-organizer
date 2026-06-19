// file: internal/plugins/dedup/quarantine_chapter_artifacts_test.go
// version: 1.0.0
// guid: 9c2e7a14-5b80-4d36-8f21-3a6e0c9d5b18
// last-edited: 2026-06-19

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mkBook creates a book with one file of the given duration, at a unique path.
func mkBook(t *testing.T, pebble *database.PebbleStore, title string, idx, durationSec int) string {
	t.Helper()
	path := fmt.Sprintf("/audio/%s-%d.m4b", title, idx)
	created, err := pebble.CreateBook(&database.Book{Title: title, FilePath: path})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := pebble.CreateBookFile(&database.BookFile{
		BookID: created.ID, FilePath: path, Duration: durationSec, FileSize: 1 << 20,
	}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	return created.ID
}

func isMarkedDeleted(t *testing.T, pebble *database.PebbleStore, id string) bool {
	t.Helper()
	b, err := pebble.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s): %v", id, err)
	}
	return b.MarkedForDeletion != nil && *b.MarkedForDeletion
}

func TestQuarantineChapterArtifacts(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := &Plugin{store: pebble}

	// 5 short single-file "Opening Credits" books → chapter artifacts (title collides ≥5).
	var artifactIDs []string
	for i := 0; i < 5; i++ {
		artifactIDs = append(artifactIDs, mkBook(t, pebble, "Opening Credits", i, 30))
	}
	// A long single-file book with the SAME title → NOT an artifact (not short).
	longID := mkBook(t, pebble, "Opening Credits", 99, 36000)
	// A unique-title short single-file book → NOT an artifact (no collision).
	uniqueID := mkBook(t, pebble, "A Real Short Story", 0, 30)

	run := func(apply bool) {
		body := `{}`
		if apply {
			body = `{"apply":true}`
		}
		if err := p.runQuarantineChapterArtifacts(context.Background(), json.RawMessage(body), &fakeReporter{}); err != nil {
			t.Fatalf("run(apply=%v): %v", apply, err)
		}
	}

	// Dry-run writes nothing.
	run(false)
	for _, id := range artifactIDs {
		if isMarkedDeleted(t, pebble, id) {
			t.Fatalf("dry-run soft-deleted %s — must write nothing", id)
		}
	}

	// Apply soft-deletes only the 5 colliding short single-file books.
	run(true)
	for _, id := range artifactIDs {
		if !isMarkedDeleted(t, pebble, id) {
			t.Errorf("expected artifact %s to be soft-deleted", id)
		}
	}
	if isMarkedDeleted(t, pebble, longID) {
		t.Error("long book (same title) must NOT be quarantined")
	}
	if isMarkedDeleted(t, pebble, uniqueID) {
		t.Error("unique-title book must NOT be quarantined")
	}
}

// TestQuarantineChapterArtifacts_UnscannedIdents: unscanned (duration=0) segments
// like "Big Finish Ident" are the dominant offenders. They are caught only when the
// title collides with >= MinTitleCollisionsUnscanned (10) books; fewer unscanned
// copies of a genuine book are left alone.
func TestQuarantineChapterArtifacts_UnscannedIdents(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := &Plugin{store: pebble}

	var identIDs []string
	for i := 0; i < 10; i++ { // 10 unscanned "Big Finish Ident" → over the unscanned bar
		identIDs = append(identIDs, mkBook(t, pebble, "Big Finish Ident", i, 0))
	}
	var nicheIDs []string
	for i := 0; i < 5; i++ { // 5 unscanned "Niche Title" → under the unscanned bar (10)
		nicheIDs = append(nicheIDs, mkBook(t, pebble, "Niche Title", i, 0))
	}

	if err := p.runQuarantineChapterArtifacts(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, id := range identIDs {
		if !isMarkedDeleted(t, pebble, id) {
			t.Errorf("expected unscanned ident %s (10 collisions) to be quarantined", id)
		}
	}
	for _, id := range nicheIDs {
		if isMarkedDeleted(t, pebble, id) {
			t.Errorf("5 unscanned copies (< 10) must NOT be quarantined: %s", id)
		}
	}
}
