// file: internal/server/handlers/versions_data_loss_pebble_test.go
// version: 1.0.0
// guid: 5c0e2a7d-9b41-4f6e-8a3c-1d7f4b2e6a95
// last-edited: 2026-09-13

package handlers_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// Regression tests for the version handlers' data-loss findings of the
// 2026-09-13 audit, on a real PebbleStore. Faults are injected by wrapping the
// store and overriding one method; every other call reaches Pebble.

// failMoveStore fails every book_file move.
type failMoveStore struct{ *database.PebbleStore }

func (failMoveStore) MoveBookFilesToBook([]string, string, string) error {
	return errors.New("injected move failure")
}

// failExtIDWriteStore fails every external-ID write: the reassign the handler
// uses now, and the create the old DeleteRaw-then-create sequence used.
type failExtIDWriteStore struct{ *database.PebbleStore }

func (failExtIDWriteStore) ReassignExternalID(string, string, string) error {
	return errors.New("injected external-ID write failure")
}

func (failExtIDWriteStore) CreateExternalIDMapping(*database.ExternalIDMapping) error {
	return errors.New("injected external-ID write failure")
}

// failBookWriteStore fails every write to one book, through either write path.
type failBookWriteStore struct {
	*database.PebbleStore
	failID string
}

func (s failBookWriteStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.failID {
		return nil, errors.New("injected write failure")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func (s failBookWriteStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	if id == s.failID {
		return nil, errors.New("injected write failure")
	}
	return s.PebbleStore.UpdateBook(id, b)
}

func mustCreateBook(t *testing.T, store *database.PebbleStore, b *database.Book) *database.Book {
	t.Helper()
	out, err := store.CreateBook(b)
	if err != nil {
		t.Fatalf("create book %q: %v", b.Title, err)
	}
	return out
}

// primariesIn returns the IDs of the live primaries of a version group.
func primariesIn(t *testing.T, store *database.PebbleStore, groupID string) []string {
	t.Helper()
	members, err := store.GetBooksByVersionGroup(groupID)
	if err != nil {
		t.Fatalf("read group %s: %v", groupID, err)
	}
	var out []string
	for _, m := range members {
		if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			out = append(out, m.ID)
		}
	}
	return out
}

func versionsCall(id, body string) (*gin.Context, interface{ Result() *http.Response }, func() (int, string)) {
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/"+id, body, gin.Params{{Key: "id", Value: id}})
	return c, w, func() (int, string) { return w.Code, w.Body.String() }
}

// Finding 1: SplitVersion wrote the new book and the source back from the
// copies read before MoveBookFilesToBook, reverting the aggregate recount the
// move had just written. Both books must end with the totals of the rows they
// hold.
func TestSplitVersion_AggregatesSurviveThePathWrites(t *testing.T) {
	store := openSplitStore(t)
	dur, size := 300, int64(30)
	src := mustCreateBook(t, store, &database.Book{Title: "Source", FilePath: "/lib/Src", Format: "mp3", Duration: &dur, FileSize: &size})
	var ids []string
	for _, p := range []string{"/lib/Src/a/01.mp3", "/lib/Src/a/02.mp3", "/lib/Src/b/03.mp3"} {
		f := &database.BookFile{BookID: src.ID, FilePath: p, Format: "mp3", Duration: 100, FileSize: 10}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}

	c, _, res := versionsCall(src.ID, `{"segment_ids":["`+ids[0]+`","`+ids[1]+`"]}`)
	handlers.NewVersionsHandler(store).SplitVersion(c)
	if code, body := res(); code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, body)
	}

	moved, err := store.GetBookFileByPath("/lib/Src/a/01.mp3")
	if err != nil || moved == nil || moved.BookID == src.ID {
		t.Fatalf("row did not move: %+v %v", moved, err)
	}
	created, err := store.GetBookByID(moved.BookID)
	if err != nil || created == nil {
		t.Fatalf("new book: %v", err)
	}
	if created.Duration == nil || *created.Duration != 200 || created.FileSize == nil || *created.FileSize != 20 {
		t.Fatalf("new book totals reverted: duration=%v size=%v, want 200/20", created.Duration, created.FileSize)
	}
	if created.FilePath != "/lib/Src/a" {
		t.Fatalf("new book path %q, want /lib/Src/a", created.FilePath)
	}
	source, err := store.GetBookByID(src.ID)
	if err != nil || source == nil {
		t.Fatalf("source: %v", err)
	}
	if source.Duration == nil || *source.Duration != 100 || source.FileSize == nil || *source.FileSize != 10 {
		t.Fatalf("source totals reverted: duration=%v size=%v, want 100/10", source.Duration, source.FileSize)
	}
	// The group SplitVersion minted has exactly one primary: the source.
	if got := primariesIn(t, store, *source.VersionGroupID); len(got) != 1 || got[0] != src.ID {
		t.Fatalf("minted group primaries = %v, want only the source %s", got, src.ID)
	}
}

// seedPIDFile gives a two-file source whose first file carries an iTunes PID
// mapping, and returns the source, the file IDs and the PID.
func seedPIDFile(t *testing.T, store *database.PebbleStore) (*database.Book, []string, string) {
	t.Helper()
	src, ids := seedSplitBook(t, store, "/lib/Box", "/lib/Box/01 - First.mp3", "/lib/Box/02 - Second.mp3")
	const pid = "PID0001"
	if err := store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: pid, BookID: src.ID, FilePath: "/lib/Box/01 - First.mp3",
	}); err != nil {
		t.Fatal(err)
	}
	return src, ids, pid
}

// assertPIDOnSource checks the mapping still belongs to src through both the
// primary key and the reverse index.
func assertPIDOnSource(t *testing.T, store *database.PebbleStore, src *database.Book, pid string) {
	t.Helper()
	owner, err := store.GetBookByExternalID("itunes", pid)
	if err != nil || owner != src.ID {
		t.Fatalf("PID %s owner = %q, %v; want the source %s", pid, owner, err, src.ID)
	}
	maps, err := store.GetExternalIDsForBook(src.ID)
	if err != nil || len(maps) != 1 || maps[0].ExternalID != pid {
		t.Fatalf("source's external IDs = %+v, %v; want [%s]", maps, err, pid)
	}
}

// Finding 2a: SplitSegmentsToBooks ignored a MoveBookFilesToBook error and
// still moved the file's PIDs to the new book, so the PID named a book that
// holds no file. A move error must stop the split before the reassign.
func TestSplitSegmentsToBooks_MoveErrorStopsPIDReassign(t *testing.T) {
	pebble := openSplitStore(t)
	src, ids, pid := seedPIDFile(t, pebble)

	c, _, res := versionsCall(src.ID, `{"segment_ids":["`+ids[0]+`"]}`)
	handlers.NewVersionsHandler(failMoveStore{pebble}).SplitSegmentsToBooks(c)
	if code, body := res(); code < 400 {
		t.Fatalf("a failed move must fail the request, got %d: %s", code, body)
	}
	assertPIDOnSource(t, pebble, src, pid)
	files, err := pebble.GetBookFiles(src.ID)
	if err != nil || len(files) != 2 {
		t.Fatalf("source rows = %d, %v; want 2", len(files), err)
	}
}

// Finding 2b: the reassign deleted the old reverse key and then created the
// mapping under the new book, swallowing a create error: the ID ended indexed
// under neither book. A failed reassign must leave the mapping on the source,
// and the request must fail.
func TestSplitSegmentsToBooks_ExtIDWriteFailureKeepsOldID(t *testing.T) {
	pebble := openSplitStore(t)
	src, ids, pid := seedPIDFile(t, pebble)

	c, _, res := versionsCall(src.ID, `{"segment_ids":["`+ids[0]+`"]}`)
	handlers.NewVersionsHandler(failExtIDWriteStore{pebble}).SplitSegmentsToBooks(c)
	code, body := res()
	if code < 400 || !strings.Contains(body, "external IDs") {
		t.Fatalf("a failed reassign must fail the request naming it, got %d: %s", code, body)
	}
	assertPIDOnSource(t, pebble, src, pid)
}

// Finding 3a: linking two books that are each their group's primary left the
// resulting group with two primaries. The target group's primary must stay the
// only one, and the incoming group's members must all join.
func TestLinkAudiobookVersion_TwoGroupsEndWithOnePrimary(t *testing.T) {
	store := openSplitStore(t)
	a := mustCreateBook(t, store, &database.Book{Title: "A", FilePath: "/lib/A", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	a2 := mustCreateBook(t, store, &database.Book{Title: "A2", FilePath: "/lib/A2", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	b := mustCreateBook(t, store, &database.Book{Title: "B", FilePath: "/lib/B", VersionGroupID: new("g2"), IsPrimaryVersion: new(true)})
	b2 := mustCreateBook(t, store, &database.Book{Title: "B2", FilePath: "/lib/B2", VersionGroupID: new("g2"), IsPrimaryVersion: new(false)})

	c, _, res := versionsCall(a.ID, `{"other_id":"`+b.ID+`"}`)
	handlers.NewVersionsHandler(store).LinkAudiobookVersion(c)
	if code, body := res(); code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, body)
	}
	if got := primariesIn(t, store, "g1"); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("g1 primaries = %v, want only %s", got, a.ID)
	}
	for _, id := range []string{a.ID, a2.ID, b.ID, b2.ID} {
		bk, err := store.GetBookByID(id)
		if err != nil || bk == nil || bk.VersionGroupID == nil || *bk.VersionGroupID != "g1" {
			t.Fatalf("book %s not in g1 after the link: %+v %v", id, bk, err)
		}
	}
}

// Finding 3b: a soft-deleted book was linked like any other. It must be
// refused before any write.
func TestLinkAudiobookVersion_SoftDeletedRefused(t *testing.T) {
	store := openSplitStore(t)
	a := mustCreateBook(t, store, &database.Book{Title: "A", FilePath: "/lib/A"})
	gone := mustCreateBook(t, store, &database.Book{Title: "Gone", FilePath: "/lib/Gone", MarkedForDeletion: new(true)})

	c, _, res := versionsCall(a.ID, `{"other_id":"`+gone.ID+`"}`)
	handlers.NewVersionsHandler(store).LinkAudiobookVersion(c)
	if code, body := res(); code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", code, body)
	}
	for _, id := range []string{a.ID, gone.ID} {
		bk, err := store.GetBookByID(id)
		if err != nil || bk == nil || bk.VersionGroupID != nil {
			t.Fatalf("book %s was written by a refused link: %+v %v", id, bk, err)
		}
	}
}

// Finding 4: a write that failed part-way through left the group with a
// wrong primary count. The old handler wrote every member in the order the
// group read returns them (primaries first), so with the current primary
// first, a failure on the member after it left the group with none: the old
// primary demoted, the new one never reached. The request must fail and the
// group must still have exactly one primary.
func TestSetAudiobookPrimary_DemoteFailureLeavesOnePrimary(t *testing.T) {
	pebble := openSplitStore(t)
	cur := mustCreateBook(t, pebble, &database.Book{Title: "Current", FilePath: "/lib/Current", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{Title: "One", FilePath: "/lib/One", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	mustCreateBook(t, pebble, &database.Book{Title: "Two", FilePath: "/lib/Two", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	members, err := pebble.GetBooksByVersionGroup("g1")
	if err != nil || len(members) != 3 || members[0].ID != cur.ID {
		t.Fatalf("setup: want the primary first in the group read, got %+v, %v", members, err)
	}
	// The member read right after the primary fails every write; the one
	// after it is the book being made primary.
	failing, next := members[1], members[2]

	c, _, res := versionsCall(next.ID, "")
	handlers.NewVersionsHandler(failBookWriteStore{PebbleStore: pebble, failID: failing.ID}).SetAudiobookPrimary(c)
	if code, body := res(); code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", code, body)
	}
	if got := primariesIn(t, pebble, "g1"); len(got) != 1 || got[0] != cur.ID {
		t.Fatalf("g1 primaries = %v, want only the unchanged %s", got, cur.ID)
	}
}
