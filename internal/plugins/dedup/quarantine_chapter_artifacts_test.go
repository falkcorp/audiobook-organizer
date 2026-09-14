// file: internal/plugins/dedup/quarantine_chapter_artifacts_test.go
// version: 1.2.0
// guid: 9c2e7a14-5b80-4d36-8f21-3a6e0c9d5b18
// last-edited: 2026-09-13

package dedup

import (
	"context"
	"encoding/json"
	"errors"
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
	// FileSize must be realistic for the duration. The CONS-18 duration-sanity
	// gate (normalizeBookFileDuration, applied on BookFile write) treats a
	// duration whose implied bitrate is < 4 kbps as milliseconds and rewrites
	// it to seconds. A flat 1 MiB made the 10h "long" fixture imply ~0.2 kbps,
	// so it was clobbered 36000→36s and then wrongly quarantined as short.
	// Derive ~128 kbps (16000 bytes/sec) plus a 1 MiB floor for zero-duration.
	fileSize := int64(durationSec)*16000 + 1<<20
	if err := pebble.CreateBookFile(&database.BookFile{
		BookID: created.ID, FilePath: path, Duration: durationSec, FileSize: fileSize,
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
	for i := range 5 {
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
	for i := range 10 { // 10 unscanned "Big Finish Ident" → over the unscanned bar
		identIDs = append(identIDs, mkBook(t, pebble, "Big Finish Ident", i, 0))
	}
	var nicheIDs []string
	for i := range 5 { // 5 unscanned "Niche Title" → under the unscanned bar (10)
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

func runQuarantine(t *testing.T, p *Plugin, apply bool) error {
	t.Helper()
	body := "{}"
	if apply {
		body = `{"apply":true}`
	}
	return p.runQuarantineChapterArtifacts(context.Background(), json.RawMessage(body), &fakeReporter{})
}

// A version group's primary is never quarantined while the group has other
// live members: on main it was, leaving the group with no primary.
func TestQuarantineChapterArtifacts_SkipsVersionGroupPrimary(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := &Plugin{store: pebble}
	var ids []string
	for i := range 6 {
		ids = append(ids, mkBook(t, pebble, "Opening Credits", i, 30))
	}
	sibling := mkBook(t, pebble, "The Real Book", 0, 36000)
	vg := "vg-primary-artifact"
	yes, no := true, false
	for id, prim := range map[string]*bool{ids[0]: &yes, sibling: &no} {
		if _, err := pebble.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID = &vg
			b.IsPrimaryVersion = prim
			return nil
		}); err != nil {
			t.Fatalf("seed group: %v", err)
		}
	}
	if err := runQuarantine(t, p, true); err != nil {
		t.Fatalf("run: %v", err)
	}
	if isMarkedDeleted(t, pebble, ids[0]) {
		t.Fatal("the version group's primary was quarantined; the group now has no primary")
	}
	for _, id := range ids[1:] {
		if !isMarkedDeleted(t, pebble, id) {
			t.Errorf("non-primary artifact %s should still be quarantined", id)
		}
	}
}

// Books under the active iTunes library are never mutated.
func TestQuarantineChapterArtifacts_SkipsITunesLibrary(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := &Plugin{store: pebble}
	var ids []string
	for i := range 6 {
		path := fmt.Sprintf("/lib/books/itunes/Credits/Opening Credits-%d.mp3", i)
		created, err := pebble.CreateBook(&database.Book{Title: "Opening Credits", FilePath: path})
		if err != nil {
			t.Fatal(err)
		}
		if err := pebble.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: path, Duration: 30, FileSize: 30*16000 + 1<<20}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	if err := runQuarantine(t, p, true); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, id := range ids {
		if isMarkedDeleted(t, pebble, id) {
			t.Fatalf("book %s under books/itunes/ was quarantined", id)
		}
	}
}

// failingWriteStore fails every book write, through either write method.
type failingWriteStore struct{ *database.PebbleStore }

var errQuarantineTestWrite = errors.New("pebble: disk full")

func (failingWriteStore) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, errQuarantineTestWrite
}
func (failingWriteStore) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, errQuarantineTestWrite
}

// A failed soft-delete fails the op. On main it was logged and the op
// reported a clean finish.
func TestQuarantineChapterArtifacts_WriteErrorsFailTheOp(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	for i := range 5 {
		mkBook(t, pebble, "Opening Credits", i, 30)
	}
	p := &Plugin{store: failingWriteStore{pebble}}
	if err := runQuarantine(t, p, true); err == nil {
		t.Fatal("5 failed soft-deletes were reported as success")
	}
	if err := runQuarantine(t, p, false); err != nil {
		t.Fatalf("dry-run writes nothing and must not fail: %v", err)
	}
}
