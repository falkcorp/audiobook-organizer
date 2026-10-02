// file: internal/server/handlers/versions_group_lock_test.go
// version: 1.0.0
// guid: 5b2e9c47-1d8a-4f36-b0e7-3c6a9d1f8e24
// last-edited: 2026-10-02

package handlers_test

import (
	"net/http"
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
