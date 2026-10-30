// file: internal/server/version_lifecycle_purge_race_test.go
// version: 1.1.0
// guid: 4f1b6a93-7c28-4d05-b3e8-0a9d5c2e7f16
// last-edited: 2026-10-29

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// staleFirstRead is a store whose first read of one version returns the copy a
// handler would have read a moment before another request changed it; every
// later read, and every write, is the real store's. It is how a test puts the
// "state changed after the handler's read" window into an HTTP request.
type staleFirstRead struct {
	database.Store
	id    string
	stale database.BookVersion
	mu    sync.Mutex
	used  bool
}

// Unwrap opts the wrapper into database.AsCapability, so the capabilities the
// purge needs from the real store (the live-file sampler behind the mount check,
// the raw KV) resolve through it as they do through the production decorators.
func (s *staleFirstRead) Unwrap() database.Store { return s.Store }

func (s *staleFirstRead) GetBookVersion(id string) (*database.BookVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.id && !s.used {
		s.used = true
		c := s.stale
		return &c, nil
	}
	return s.Store.GetBookVersion(id)
}

// A purge-now that claimed a purge_failed version after the hard-delete handler
// read it: the hard delete is refused with 409, the record stays, and the
// purge's files are not stranded.
func TestHandleHardDeleteVersion_RefusedWhileAPurgeIsRunning(t *testing.T) {
	_, store := setupVersionLifecycleServer(t)
	audio := filepath.Join(libraryDir(t, "Hush"), "hush.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, audio)
	claimed := *ver
	claimed.Status = database.BookVersionStatusPurging
	if err := store.UpdateBookVersion(&claimed); err != nil {
		t.Fatal(err)
	}
	seen := claimed
	seen.Status = database.BookVersionStatusPurgeFailed // what the handler read, before the retry claimed it
	srv := newTestServer(t, &staleFirstRead{Store: store, id: ver.ID, stale: seen})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/purged-versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("hard delete of a version whose purge is running: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got == nil || got.Status != database.BookVersionStatusPurging {
		t.Errorf("the record = %+v, want it kept, still purging", got)
	}
}

// Purge-now on a version another purge finished after the handler read it as
// trash: 409 saying it was already purged, not that it was restored.
func TestHandlePurgeVersion_AlreadyPurgedSaysSoNotRestored(t *testing.T) {
	_, store := setupVersionLifecycleServer(t)
	audio := filepath.Join(libraryDir(t, "Hush"), "hush.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, audio)
	seenTrash := *ver
	purged := *ver
	purged.Status = database.BookVersionStatusPurging
	if err := store.UpdateBookVersion(&purged); err != nil {
		t.Fatal(err)
	}
	purged.Status = database.BookVersionStatusInactivePurged
	if err := store.UpdateBookVersion(&purged); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, &staleFirstRead{Store: store, id: ver.ID, stale: seenTrash})

	w := postPurgeNow(t, srv, ver)

	if w.Code != http.StatusConflict {
		t.Fatalf("purge-now on an already purged version: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "already purged") {
		t.Errorf("answer %s does not say the version was already purged", body)
	}
	if strings.Contains(body, "restored while the purge was starting") {
		t.Errorf("answer %s says the version was restored, which it was not", body)
	}
}

// The control: a restore that landed after the read is still worded as one.
func TestHandlePurgeVersion_RestoredAfterTheReadStillSaysRestored(t *testing.T) {
	_, store := setupVersionLifecycleServer(t)
	audio := filepath.Join(libraryDir(t, "Hush"), "hush.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, audio)
	seenTrash := *ver
	restored := *ver
	restored.Status = database.BookVersionStatusAlt
	if err := store.UpdateBookVersion(&restored); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, &staleFirstRead{Store: store, id: ver.ID, stale: seenTrash})

	w := postPurgeNow(t, srv, ver)

	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "restored while the purge was starting") {
		t.Fatalf("purge-now after a restore: got %d: %s", w.Code, w.Body.String())
	}
}

// A purging version whose live book serves no file that exists on disk (its path
// is stale: the book's own file is missing) has a way out. The restore and hard
// delete refusals say how it ends; each Purge now is counted; the third makes it
// purge_failed (nothing deleted) and the answer says so; and hard delete then works.
func TestHandlePurgeVersion_PurgingVersionWhoseBookServesNothingEndsAsPurgeFailed(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	// A live book whose own file, book.m4b, is never written: it serves nothing that
	// exists on disk. The version owes one file, in its own slot.
	bookDir := libraryDir(t, "Year One")
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Batman: Year One", FilePath: filepath.Join(bookDir, "book.m4b")}); err != nil {
		t.Fatal(err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported"})
	if err != nil {
		t.Fatal(err)
	}
	slotFile := filepath.Join(versions.VersionSlotDir(bookDir, ver.ID), "part1.m4b")
	if err := os.MkdirAll(filepath.Dir(slotFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slotFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	claimed := *ver
	claimed.Status = database.BookVersionStatusPurging
	if err := store.UpdateBookVersion(&claimed); err != nil {
		t.Fatal(err)
	}
	wantAfter := "becomes purge_failed after 3 attempts in a row"

	restore := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, restore)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "book that serves no file that exists on disk") || !strings.Contains(w.Body.String(), wantAfter) {
		t.Errorf("restore of a purging version: %d %s, want 409 saying how it ends", w.Code, w.Body.String())
	}
	del := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/purged-versions/"+ver.ID, nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w
	}
	if w := del(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "book that serves no file that exists on disk") || !strings.Contains(w.Body.String(), wantAfter) {
		t.Errorf("hard delete of a purging version: %d %s, want 409 saying how it ends", w.Code, w.Body.String())
	}

	for n := 1; n <= versions.PurgeUndecidedEscalateAfter; n++ {
		w := postPurgeNow(t, srv, ver)
		if w.Code != http.StatusConflict {
			t.Fatalf("purge-now %d: expected 409, got %d: %s", n, w.Code, w.Body.String())
		}
		last := n == versions.PurgeUndecidedEscalateAfter
		if got := strings.Contains(w.Body.String(), "now purge_failed"); got != last {
			t.Errorf("purge-now %d: answer %s, says purge_failed = %v, want %v", n, w.Body.String(), got, last)
		}
	}
	if got, _ := store.GetBookVersion(ver.ID); got == nil || got.Status != database.BookVersionStatusPurgeFailed {
		t.Fatalf("version = %+v, want purge_failed after %d refused attempts", got, versions.PurgeUndecidedEscalateAfter)
	}
	if _, err := os.Stat(slotFile); err != nil {
		t.Errorf("the slot file was touched: %v", err)
	}
	if w := del(); w.Code != http.StatusOK {
		t.Fatalf("hard delete of the escalated version: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(slotFile); err != nil {
		t.Errorf("hard delete removed the file named in purge_failed: %v", err)
	}
}
