// file: internal/server/handlers/versions_data_loss_pebble_test.go
// version: 1.2.0
// guid: 5c0e2a7d-9b41-4f6e-8a3c-1d7f4b2e6a95
// last-edited: 2026-09-14

package handlers_test

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

// primariesIn returns the IDs of the live primaries of a version group, read
// the way the store and memdb read the flag: a nil IsPrimaryVersion counts as
// primary (PebbleStore's summary filter, the memdb summaries). Counting only
// explicit trues hid a nil member that the library still shows as a primary.
func primariesIn(t *testing.T, store *database.PebbleStore, groupID string) []string {
	t.Helper()
	members, err := store.GetBooksByVersionGroup(groupID)
	if err != nil {
		t.Fatalf("read group %s: %v", groupID, err)
	}
	var out []string
	for _, m := range members {
		if m.IsPrimaryVersion == nil || *m.IsPrimaryVersion {
			out = append(out, m.ID)
		}
	}
	return out
}

func mustGetBook(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("read book %s: %+v %v", id, b, err)
	}
	return b
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

// Round 2, item 1: the demote loop skipped a member whose flag is nil, reading
// nil as not-primary, but the store counts nil as primary: the group ended
// with two. Every member that is not an explicit false must be demoted.
func TestSetAudiobookPrimary_NilFlaggedMemberIsDemoted(t *testing.T) {
	pebble := openSplitStore(t)
	mustCreateBook(t, pebble, &database.Book{ID: "np-cur", Title: "Current", FilePath: "/lib/Current", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{ID: "np-nil", Title: "Nil", FilePath: "/lib/Nil", VersionGroupID: new("g1")})
	target := mustCreateBook(t, pebble, &database.Book{ID: "np-target", Title: "Target", FilePath: "/lib/Target", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	if b := mustGetBook(t, pebble, "np-nil"); b.IsPrimaryVersion != nil {
		t.Fatalf("setup: want a nil flag on np-nil, got %v", *b.IsPrimaryVersion)
	}

	c, _, res := versionsCall(target.ID, "")
	handlers.NewVersionsHandler(pebble).SetAudiobookPrimary(c)
	if code, body := res(); code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, body)
	}
	if got := primariesIn(t, pebble, "g1"); len(got) != 1 || got[0] != target.ID {
		t.Fatalf("g1 primaries (nil counts as primary) = %v, want only %s", got, target.ID)
	}
}

// Round 2, item 1: the undo must give each demoted member back the exact flag
// it had -- a nil member demoted to false and then "restored" to true would be
// a different state from the one the request started in.
func TestSetAudiobookPrimary_RollbackRestoresNilFlag(t *testing.T) {
	pebble := openSplitStore(t)
	mustCreateBook(t, pebble, &database.Book{ID: "rb-a", Title: "A", FilePath: "/lib/A", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{ID: "rb-b", Title: "B", FilePath: "/lib/B", VersionGroupID: new("g1")})
	mustCreateBook(t, pebble, &database.Book{ID: "rb-c", Title: "C", FilePath: "/lib/C", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	mustCreateBook(t, pebble, &database.Book{ID: "rb-d", Title: "D", FilePath: "/lib/D", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})

	// Demotes run in ID order: rb-a, rb-b, then rb-c fails.
	c, _, res := versionsCall("rb-d", "")
	handlers.NewVersionsHandler(failBookWriteStore{PebbleStore: pebble, failID: "rb-c"}).SetAudiobookPrimary(c)
	if code, body := res(); code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", code, body)
	}
	if b := mustGetBook(t, pebble, "rb-b"); b.IsPrimaryVersion != nil {
		t.Fatalf("rb-b flag after the rollback = %v, want nil as before", *b.IsPrimaryVersion)
	}
	if b := mustGetBook(t, pebble, "rb-a"); b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
		t.Fatal("rb-a lost its primary flag in the rollback")
	}
	if b := mustGetBook(t, pebble, "rb-d"); b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
		t.Fatal("rb-d kept its promotion after the rollback")
	}
}

// promoteRendezvous holds each set-primary promotion of one of targets until
// every target's promotion has landed (or 300ms pass). Two calls that are not
// serialized then always interleave the same way: both promote, then each
// demotes the other's book.
type promoteRendezvous struct {
	*database.PebbleStore
	targets map[string]bool
	mu      sync.Mutex
	arrived map[string]bool
}

func (s *promoteRendezvous) hold(id string, b *database.Book) {
	if !s.targets[id] || b == nil || b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
		return
	}
	s.mu.Lock()
	if s.arrived[id] {
		s.mu.Unlock()
		return
	}
	s.arrived[id] = true
	s.mu.Unlock()
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		n := len(s.arrived)
		s.mu.Unlock()
		if n == len(s.targets) {
			return
		}
	}
}

func (s *promoteRendezvous) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	b, err := s.PebbleStore.ModifyBook(id, fn)
	if err == nil {
		s.hold(id, b)
	}
	return b, err
}

func (s *promoteRendezvous) UpdateBook(id string, in *database.Book) (*database.Book, error) {
	b, err := s.PebbleStore.UpdateBook(id, in)
	if err == nil {
		s.hold(id, b)
	}
	return b, err
}

// Round 2, item 5: two concurrent set-primary calls on one group each promoted
// their book and demoted every other member, including the other call's
// promotion, leaving no primary. Calls on one group must be serialized.
func TestSetAudiobookPrimary_ConcurrentCallsLeaveOnePrimary(t *testing.T) {
	pebble := openSplitStore(t)
	mustCreateBook(t, pebble, &database.Book{ID: "cc-z", Title: "Z", FilePath: "/lib/Z", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{ID: "cc-x", Title: "X", FilePath: "/lib/X", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	mustCreateBook(t, pebble, &database.Book{ID: "cc-y", Title: "Y", FilePath: "/lib/Y", VersionGroupID: new("g1"), IsPrimaryVersion: new(false)})
	store := &promoteRendezvous{PebbleStore: pebble, targets: map[string]bool{"cc-x": true, "cc-y": true}, arrived: map[string]bool{}}
	h := handlers.NewVersionsHandler(store)

	var wg sync.WaitGroup
	for _, id := range []string{"cc-x", "cc-y"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, res := versionsCall(id, "")
			h.SetAudiobookPrimary(c)
			if code, body := res(); code != http.StatusOK {
				t.Errorf("set-primary %s: want 200, got %d: %s", id, code, body)
			}
		}()
	}
	wg.Wait()
	if got := primariesIn(t, pebble, "g1"); len(got) != 1 {
		t.Fatalf("g1 primaries after two concurrent set-primary calls = %v, want exactly one", got)
	}
}

// Round 2, item 3: a link that failed part-way left an incoming member moved
// out of its old group, and when that member was the old group's primary, the
// books left behind had none. The written members must be put back.
func TestLinkAudiobookVersion_FailureRollsBackIncomingMembers(t *testing.T) {
	pebble := openSplitStore(t)
	mustCreateBook(t, pebble, &database.Book{ID: "lk-a", Title: "A", FilePath: "/lib/A", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{ID: "lk-c", Title: "C", FilePath: "/lib/C", VersionGroupID: new("g2"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{ID: "lk-d", Title: "D", FilePath: "/lib/D", VersionGroupID: new("g2"), IsPrimaryVersion: new(false)})

	// The winner (lk-a) is written first, then lk-c, then lk-d fails.
	c, _, res := versionsCall("lk-a", `{"other_id":"lk-c"}`)
	handlers.NewVersionsHandler(failBookWriteStore{PebbleStore: pebble, failID: "lk-d"}).LinkAudiobookVersion(c)
	if code, body := res(); code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", code, body)
	}
	if got := primariesIn(t, pebble, "g2"); len(got) != 1 || got[0] != "lk-c" {
		t.Fatalf("g2 primaries = %v, want lk-c back as its primary", got)
	}
	if got := primariesIn(t, pebble, "g1"); len(got) != 1 || got[0] != "lk-a" {
		t.Fatalf("g1 primaries = %v, want only lk-a", got)
	}
}

// Round 2, item 4: SplitVersion moved the files but left their iTunes PIDs
// on the source, unlike the other three move paths. The PID must follow its
// file to the new book.
func TestSplitVersion_MovesTheMovedFilesPIDs(t *testing.T) {
	pebble := openSplitStore(t)
	src, ids, pid := seedPIDFile(t, pebble)

	c, _, res := versionsCall(src.ID, `{"segment_ids":["`+ids[0]+`"]}`)
	handlers.NewVersionsHandler(pebble).SplitVersion(c)
	if code, body := res(); code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, body)
	}
	moved, err := pebble.GetBookFileByPath("/lib/Box/01 - First.mp3")
	if err != nil || moved == nil || moved.BookID == src.ID {
		t.Fatalf("row did not move: %+v %v", moved, err)
	}
	owner, err := pebble.GetBookByExternalID("itunes", pid)
	if err != nil || owner != moved.BookID {
		t.Fatalf("PID %s owner = %q, %v; want the new book %s", pid, owner, err, moved.BookID)
	}
	if left, err := pebble.GetExternalIDsForBook(src.ID); err != nil || len(left) != 0 {
		t.Fatalf("source still indexes %+v, %v; want none", left, err)
	}
}

// staleExtIDStore adds, to forBook's external-ID listing, a mapping whose
// primary row names another book: a stale reverse key.
type staleExtIDStore struct {
	*database.PebbleStore
	forBook string
	stale   database.ExternalIDMapping
}

func (s staleExtIDStore) GetExternalIDsForBook(id string) ([]database.ExternalIDMapping, error) {
	out, err := s.PebbleStore.GetExternalIDsForBook(id)
	if err != nil || id != s.forBook {
		return out, err
	}
	return append(out, s.stale), nil
}

// Round 2, item 7: a mapping indexed under the source that belongs to another
// book made the reassign return an error, and the split answered 500 after the
// files had already moved. Such a mapping is not the source's to move: it is
// skipped, the split succeeds, and the source's own PID still moves.
func TestSplitSegmentsToBooks_OtherBooksMappingDoesNotFailTheSplit(t *testing.T) {
	pebble := openSplitStore(t)
	src, ids, pid := seedPIDFile(t, pebble)
	store := staleExtIDStore{PebbleStore: pebble, forBook: src.ID, stale: database.ExternalIDMapping{
		Source: "itunes", ExternalID: "PIDSTALE", BookID: "someone-else", FilePath: "/lib/Box/01 - First.mp3",
	}}

	c, _, res := versionsCall(src.ID, `{"segment_ids":["`+ids[0]+`"]}`)
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if code, body := res(); code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, body)
	}
	moved, err := pebble.GetBookFileByPath("/lib/Box/01 - First.mp3")
	if err != nil || moved == nil || moved.BookID == src.ID {
		t.Fatalf("row did not move: %+v %v", moved, err)
	}
	if owner, err := pebble.GetBookByExternalID("itunes", pid); err != nil || owner != moved.BookID {
		t.Fatalf("PID %s owner = %q, %v; want the new book %s", pid, owner, err, moved.BookID)
	}
}

// groupOnFirstModify moves book id into group before the first ModifyBook on
// it runs the caller's closure: a link that lands between set-primary's
// GetBookByID (which saw no group) and its write.
type groupOnFirstModify struct {
	*database.PebbleStore
	id, group string
	once      sync.Once
}

func (s *groupOnFirstModify) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.id {
		var err error
		s.once.Do(func() {
			_, err = s.PebbleStore.ModifyBook(id, func(cur *database.Book) error {
				cur.VersionGroupID = &s.group
				f := false
				cur.IsPrimaryVersion = &f
				return nil
			})
		})
		if err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// #3399 follow-up: set-primary on a book with no group decided "ungrouped"
// from a read made before ModifyBook, and the closure did not re-check it. A
// book grouped in between was flagged primary without the group's existing
// primary being demoted: two primaries. The eligibility must be decided on the
// row ModifyBook hands the closure.
func TestSetAudiobookPrimary_GroupedAfterReadLeavesOnePrimary(t *testing.T) {
	pebble := openSplitStore(t)
	mustCreateBook(t, pebble, &database.Book{ID: "ug-old", Title: "Old", FilePath: "/lib/Old", VersionGroupID: new("g1"), IsPrimaryVersion: new(true)})
	mustCreateBook(t, pebble, &database.Book{ID: "ug-nil", Title: "Nil", FilePath: "/lib/Nil", VersionGroupID: new("g1")})
	mustCreateBook(t, pebble, &database.Book{ID: "ug-target", Title: "Target", FilePath: "/lib/Target"})

	c, _, res := versionsCall("ug-target", "")
	handlers.NewVersionsHandler(&groupOnFirstModify{PebbleStore: pebble, id: "ug-target", group: "g1"}).SetAudiobookPrimary(c)
	if code, body := res(); code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", code, body)
	}
	if got := primariesIn(t, pebble, "g1"); len(got) != 1 || got[0] != "ug-target" {
		t.Fatalf("g1 primaries (nil counts as primary) = %v, want only ug-target", got)
	}
}

// A book soft-deleted between the read and the write must not be flagged
// primary by the ungrouped path.
type deleteOnFirstModify struct {
	*database.PebbleStore
	id   string
	once sync.Once
}

func (s *deleteOnFirstModify) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.id {
		var err error
		s.once.Do(func() {
			_, err = s.PebbleStore.ModifyBook(id, func(cur *database.Book) error {
				del := true
				cur.MarkedForDeletion = &del
				return nil
			})
		})
		if err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func TestSetAudiobookPrimary_UngroupedDeletedAfterReadRefused(t *testing.T) {
	pebble := openSplitStore(t)
	mustCreateBook(t, pebble, &database.Book{ID: "ud-target", Title: "Target", FilePath: "/lib/Target", IsPrimaryVersion: new(false)})

	c, _, res := versionsCall("ud-target", "")
	handlers.NewVersionsHandler(&deleteOnFirstModify{PebbleStore: pebble, id: "ud-target"}).SetAudiobookPrimary(c)
	if code, body := res(); code == http.StatusOK {
		t.Fatalf("want a refusal, got %d: %s", code, body)
	}
	if b := mustGetBook(t, pebble, "ud-target"); isTrue(b.IsPrimaryVersion) {
		t.Fatalf("a deleted book was flagged primary")
	}
}

func isTrue(p *bool) bool { return p != nil && *p }
