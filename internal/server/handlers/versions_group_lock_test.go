// file: internal/server/handlers/versions_group_lock_test.go
// version: 1.1.0
// guid: 5b2e9c47-1d8a-4f36-b0e7-3c6a9d1f8e24
// last-edited: 2026-10-02

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// The link takes the SHARED version-group locks: while another holder (a
// hand-off, itunes.regroup's recheck) has the target group's stripe, the link
// writes nothing and does not answer; it links once the stripe is released.
func TestVersionsHandler_LinkWaitsForSharedGroupLock(t *testing.T) {
	store := openSplitStore(t)
	store.WaitForWarmup()
	g, tr := "g-locked", true
	b1, err := store.CreateBook(&database.Book{Title: "One", VersionGroupID: &g, IsPrimaryVersion: &tr})
	if err != nil {
		t.Fatalf("create b1: %v", err)
	}
	b2, err := store.CreateBook(&database.Book{Title: "Two"})
	if err != nil {
		t.Fatalf("create b2: %v", err)
	}

	unlock := versionprimary.LockGroup(g)
	h := handlers.NewVersionsHandler(store)
	c, w := newVersionsCtx(http.MethodPost, "/audiobooks/"+b1.ID+"/versions", `{"other_id":"`+b2.ID+`"}`, gin.Params{{Key: "id", Value: b1.ID}})
	done := make(chan struct{})
	go func() {
		h.LinkAudiobookVersion(c)
		close(done)
	}()
	select {
	case <-done:
		unlock()
		t.Fatalf("link answered (%d) while group %s's shared lock was held", w.Code, g)
	case <-time.After(300 * time.Millisecond):
	}
	if got, _ := store.GetBookByID(b2.ID); got == nil || (got.VersionGroupID != nil && *got.VersionGroupID != "") {
		unlock()
		t.Fatalf("b2 joined a group while the lock was held: %+v", got)
	}
	unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("link still blocked after the lock was released")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("link status %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookByID(b2.ID); got == nil || got.VersionGroupID == nil || *got.VersionGroupID != g {
		t.Fatalf("b2 not linked into %s: %+v", g, got)
	}
}

// seedGroupedSource creates a source book in group g (nil for none) holding
// three rows and returns it with the row ids.
func seedGroupedSource(t *testing.T, store *database.PebbleStore, g *string) (*database.Book, []string) {
	t.Helper()
	tr := true
	b := &database.Book{Title: "Source", FilePath: "/lib/Src", Format: "mp3"}
	if g != nil {
		b.VersionGroupID, b.IsPrimaryVersion = g, &tr
	}
	src, err := store.CreateBook(b)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	var ids []string
	for _, p := range []string{"/lib/Src/a/01.mp3", "/lib/Src/a/02.mp3", "/lib/Src/b/03.mp3"} {
		f := &database.BookFile{BookID: src.ID, FilePath: p, Format: "mp3", Duration: 100, FileSize: 10}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatalf("create file: %v", err)
		}
		ids = append(ids, f.ID)
	}
	return src, ids
}

// splitCtx is a SplitVersion request moving rows ids off src.
func splitCtx(src string, ids ...string) (*gin.Context, *httptest.ResponseRecorder) {
	return newVersionsCtx(http.MethodPost, "/audiobooks/"+src+"/split",
		`{"segment_ids":["`+strings.Join(ids, `","`)+`"]}`, gin.Params{{Key: "id", Value: src}})
}

// The new member of a split lands in the source's group.
func newMemberOf(t *testing.T, store *database.PebbleStore, srcID, movedPath string) *database.Book {
	t.Helper()
	moved, err := store.GetBookFileByPath(movedPath)
	if err != nil || moved == nil || moved.BookID == srcID {
		t.Fatalf("row %s did not move: %+v %v", movedPath, moved, err)
	}
	b, err := store.GetBookByID(moved.BookID)
	if err != nil || b == nil {
		t.Fatalf("read new member: %+v %v", b, err)
	}
	return b
}

// The split takes the SHARED version-group locks of the source's group: while
// another holder has that stripe, the split writes nothing and does not
// answer; it splits once the stripe is released.
func TestVersionsHandler_SplitWaitsForSharedGroupLock(t *testing.T) {
	store := openSplitStore(t)
	store.WaitForWarmup()
	g := "g-split-locked"
	src, ids := seedGroupedSource(t, store, &g)

	unlock := versionprimary.LockGroup(g)
	c, w := splitCtx(src.ID, ids[0], ids[1])
	done := make(chan struct{})
	go func() {
		handlers.NewVersionsHandler(store).SplitVersion(c)
		close(done)
	}()
	select {
	case <-done:
		unlock()
		t.Fatalf("split answered (%d) while group %s's shared lock was held", w.Code, g)
	case <-time.After(300 * time.Millisecond):
	}
	members, err := store.GetBooksByVersionGroup(g)
	if err != nil || len(members) != 1 {
		unlock()
		t.Fatalf("group %s has %d members (%v) while the lock was held; want the source alone", g, len(members), err)
	}
	unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("split still blocked after the lock was released")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("split status %d: %s", w.Code, w.Body.String())
	}
	if nb := newMemberOf(t, store, src.ID, "/lib/Src/a/01.mp3"); nb.VersionGroupID == nil || *nb.VersionGroupID != g {
		t.Fatalf("new member group = %v, want %s", nb.VersionGroupID, g)
	}
}

// groupsOnFirstModify is a store on which another writer puts the source into
// group other just before the split's first ModifyBook of it: after
// LockBookGroups read the source groupless, before the mint.
type groupsOnFirstModify struct {
	*database.PebbleStore
	srcID, other string
	fired        bool
}

func (s *groupsOnFirstModify) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.srcID && !s.fired {
		s.fired = true
		g := s.other
		if _, err := s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID = &g
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// A groupless source grouped by another writer after the locked read is
// refused with 409 split_source_grouped: that group is not locked here, so
// the split must not mint over it or split into it.
func TestVersionsHandler_SplitRefusesSourceGroupedMidStart(t *testing.T) {
	store := openSplitStore(t)
	store.WaitForWarmup()
	src, ids := seedGroupedSource(t, store, nil)
	ws := &groupsOnFirstModify{PebbleStore: store, srcID: src.ID, other: "g-other-writer"}

	c, w := splitCtx(src.ID, ids[0])
	handlers.NewVersionsHandler(ws).SplitVersion(c)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "split_source_grouped") {
		t.Fatalf("status %d body %s; want 409 split_source_grouped", w.Code, w.Body.String())
	}
	got, err := store.GetBookByID(src.ID)
	if err != nil || got == nil || got.VersionGroupID == nil || *got.VersionGroupID != "g-other-writer" {
		t.Fatalf("source = %+v, %v; want left in the other writer's group", got, err)
	}
	files, err := store.GetBookFiles(src.ID)
	if err != nil || len(files) != 3 {
		t.Fatalf("source rows = %d, %v; want all 3 unmoved", len(files), err)
	}
}

// Group ids are not normalised on write. A padded stored id is the source's
// group as stored: the new member must land in exactly that id, not a
// trimmed copy that names a different group.
func TestVersionsHandler_SplitKeepsAPaddedGroupID(t *testing.T) {
	store := openSplitStore(t)
	store.WaitForWarmup()
	g := "g-padded "
	src, ids := seedGroupedSource(t, store, &g)

	c, w := splitCtx(src.ID, ids[0], ids[1])
	handlers.NewVersionsHandler(store).SplitVersion(c)
	if w.Code != http.StatusOK {
		t.Fatalf("split status %d: %s", w.Code, w.Body.String())
	}
	if nb := newMemberOf(t, store, src.ID, "/lib/Src/a/01.mp3"); nb.VersionGroupID == nil || *nb.VersionGroupID != g {
		t.Fatalf("new member group = %q, want the source's stored %q", derefGroup(nb.VersionGroupID), g)
	}
}

// A whitespace-only stored group is no group: the source is minted a group
// and the split succeeds, rather than refusing forever with a 409.
func TestVersionsHandler_SplitTreatsABlankGroupAsNone(t *testing.T) {
	store := openSplitStore(t)
	store.WaitForWarmup()
	g := "   "
	src, ids := seedGroupedSource(t, store, &g)

	c, w := splitCtx(src.ID, ids[0], ids[1])
	handlers.NewVersionsHandler(store).SplitVersion(c)
	if w.Code != http.StatusOK {
		t.Fatalf("split status %d: %s", w.Code, w.Body.String())
	}
	got, err := store.GetBookByID(src.ID)
	if err != nil || got == nil || strings.TrimSpace(derefGroup(got.VersionGroupID)) == "" {
		t.Fatalf("source = %+v, %v; want a minted group", got, err)
	}
	if nb := newMemberOf(t, store, src.ID, "/lib/Src/a/01.mp3"); derefGroup(nb.VersionGroupID) != *got.VersionGroupID {
		t.Fatalf("new member group = %q, want the source's minted %q", derefGroup(nb.VersionGroupID), *got.VersionGroupID)
	}
}

func derefGroup(g *string) string {
	if g == nil {
		return ""
	}
	return *g
}
