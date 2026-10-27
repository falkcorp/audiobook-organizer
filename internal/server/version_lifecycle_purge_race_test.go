// file: internal/server/version_lifecycle_purge_race_test.go
// version: 1.0.0
// guid: 4f1b6a93-7c28-4d05-b3e8-0a9d5c2e7f16
// last-edited: 2026-10-27

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
