// file: internal/server/version_lifecycle_retired_test.go
// version: 1.2.0
// guid: 6d2a9c41-3b7e-4f08-a5d1-7e9b04c8f326
// last-edited: 2026-10-24

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seedRetiredVersion creates a live holder book and a retired version of it, with
// the file of a retired row on disk when withRow is set. It returns the holder
// ID, the version and the retired row's path ("" without a row).
func seedRetiredVersion(t *testing.T, store database.Store, withRow bool) (string, *database.BookVersion, string) {
	t.Helper()
	holder, err := store.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(t.TempDir(), "book.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	shellDir := t.TempDir()
	shell, err := store.CreateBook(&database.Book{Title: "Shell", FilePath: shellDir})
	if err != nil {
		t.Fatal(err)
	}
	retiredAt := time.Now()
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &retiredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !withRow {
		return holder.ID, ver, ""
	}
	path := filepath.Join(shellDir, "01.mp3")
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := &database.BookFile{BookID: shell.ID, FilePath: path}
	if err := store.CreateBookFile(row); err != nil {
		t.Fatal(err)
	}
	w, ok := database.AsCapability[database.RetiredBookFileWriter](store)
	if !ok {
		t.Fatal("the store cannot retire book files")
	}
	if err := w.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holder.ID, ver.ID); err != nil {
		t.Fatal(err)
	}
	return holder.ID, ver, path
}

// The trash route refuses a retired version with a 409, with or without a
// retired row, and leaves it retired. Without a row the store would let the
// write through (it refuses only a retired version that still holds rows), so
// this is the guard that keeps such a version out of the 14-day disk purge. The
// control is an active version of another book, which is trashed.
func TestHandleTrashVersion_RefusesRetiredVersion(t *testing.T) {
	for _, withRow := range []bool{true, false} {
		name := "with a retired row"
		if !withRow {
			name = "without any retired row"
		}
		t.Run(name, func(t *testing.T) {
			srv, store := setupVersionLifecycleServer(t)
			holderID, ver, _ := seedRetiredVersion(t, store, withRow)

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+holderID+"/versions/"+ver.ID, nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("trash of a retired version: expected 409, got %d: %s", w.Code, w.Body.String())
			}
			got, _ := store.GetBookVersion(ver.ID)
			if got.Status != database.BookVersionStatusRetired || got.TrashedAt != nil {
				t.Errorf("status=%s TrashedAt=%v, want the retired version untouched", got.Status, got.TrashedAt)
			}
		})
	}

	// Control: the same route trashes an ordinary version.
	srv, store := setupVersionLifecycleServer(t)
	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1.m4b", Format: "m4b"})
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("control: trash of an alt version: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusTrash {
		t.Errorf("control: status = %s, want trash", got.Status)
	}
}

// "Purge now" refuses a retired version with a 409 and deletes nothing: the
// retired row's file stays on disk, the version stays retired. (The control is
// TestHandlePurgeVersion_TrashedPurgesFilesKeepsRows, which purges a trashed
// version through the same route.)
func TestHandlePurgeVersion_RefusesRetiredVersion(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	holderID, ver, path := seedRetiredVersion(t, store, true)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+holderID+"/versions/"+ver.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("purge-now of a retired version: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("purge-now removed the retired row's file: %v", err)
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusRetired {
		t.Errorf("status = %s, want retired", got.Status)
	}
}

// The restore route answers a retired version with a 409 and leaves it
// retired. A retired version is never in the trash, so before the retired check
// moved ahead of the not-in-trash check this answered 400 "version is not in
// trash" and the 409 branch was dead. The controls pin both neighbours: a
// trashed version is restored (200), and an alt that is merely not in the trash
// keeps the 400, so the 409 is specific to the retired status.
func TestHandleRestoreVersion_RefusesRetiredVersion(t *testing.T) {
	for _, withRow := range []bool{true, false} {
		name := "with a retired row"
		if !withRow {
			name = "without any retired row"
		}
		t.Run(name, func(t *testing.T) {
			srv, store := setupVersionLifecycleServer(t)
			holderID, ver, _ := seedRetiredVersion(t, store, withRow)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+holderID+"/versions/"+ver.ID+"/restore", nil)
			w := httptest.NewRecorder()
			srv.router.ServeHTTP(w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("restore of a retired version: expected 409, got %d: %s", w.Code, w.Body.String())
			}
			got, _ := store.GetBookVersion(ver.ID)
			if got.Status != database.BookVersionStatusRetired {
				t.Errorf("status = %s, want the retired version untouched", got.Status)
			}
		})
	}

	srv, store := setupVersionLifecycleServer(t)
	_, _ = store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1.m4b", Format: "m4b"})
	trashedAt := time.Now()
	trashed, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	alt, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ver  string
		want int
	}{{trashed.ID, http.StatusOK}, {alt.ID, http.StatusBadRequest}} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+c.ver+"/restore", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("control: restore of version %s: expected %d, got %d: %s", c.ver, c.want, w.Code, w.Body.String())
		}
	}
}

// unavailablePathStore answers BookFilesAtPath the way a store does while the
// memdb is warming up or book_files is flagged incomplete.
type unavailablePathStore struct{ database.Store }

func (unavailablePathStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, fmt.Errorf("%w: injected", database.ErrBookFilesAtPathUnavailable)
}

// "Purge now" while the path index cannot answer is a 503 (nothing was deleted,
// the version is still in the trash, try again), not a 500 that reads as a
// server fault. Controls: the same purge on a working store answers 200 (the
// existing TestHandlePurgeVersion_TrashedPurgesFilesKeepsRows), and any other
// failure stays a 500 (the last case).
func TestHandlePurgeVersion_PathIndexUnavailableIsA503(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	bookDir := t.TempDir()
	audio := filepath.Join(bookDir, "book.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: filepath.Join(bookDir, "other.m4b")}); err != nil {
		t.Fatal(err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: "b1", FilePath: audio, VersionID: ver.ID, TrackNumber: 1}); err != nil {
		t.Fatal(err)
	}
	srv.store = unavailablePathStore{store}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("purge-now with the path index unavailable: expected 503, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(audio); err != nil {
		t.Errorf("the file was removed although the purge was refused: %v", err)
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusTrash {
		t.Errorf("status = %s, want the version left in the trash", got.Status)
	}
}

// A file that cannot be removed is a real failure: 500, and the version is left
// purging so the next attempt retries it and it cannot be restored half-deleted
// (an own row naming a directory that still has content; os.Remove refuses it as
// root or not).
func TestHandlePurgeVersion_UnremovableFileIsA500AndLeavesTheVersionPurging(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: filepath.Join(t.TempDir(), "book.m4b")}); err != nil {
		t.Fatal(err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	stuck := filepath.Join(t.TempDir(), "stuck")
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "inner.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: "b1", FilePath: stuck, VersionID: ver.ID, TrackNumber: 1}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("purge-now with an unremovable file: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusPurging || got.PurgedDate != nil {
		t.Errorf("status=%s PurgedDate=%v, want the version left purging", got.Status, got.PurgedDate)
	}

	// Restore of the half-purged version is refused with a 409 that says why,
	// and the version stays purging (the user is not handed an alt whose files
	// are partly gone).
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("restore of a purging version: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "purge") {
		t.Errorf("the 409 should say the version is being purged: %s", w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusPurging {
		t.Errorf("status = %s after the refused restore, want purging", got.Status)
	}

	// Trashing it again is refused the same way.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("trash of a purging version: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// Purge-now retries it: with the obstacle gone it finishes (200).
	if err := os.Remove(filepath.Join(stuck, "inner.m4b")); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("purge-now retry of a purging version: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s after the retry, want inactive_purged", got.Status)
	}
}

// A restore request that read the version as trash and writes after a purge
// claimed it is refused by the store and answered 409, not 500: the race the
// claim closes, driven through the handler by a store that claims the purge
// between the handler's read and its write.
type claimAfterReadStore struct {
	database.Store
	t *testing.T
}

func (s claimAfterReadStore) GetBookVersion(id string) (*database.BookVersion, error) {
	v, err := s.Store.GetBookVersion(id)
	if err != nil || v == nil || v.Status != database.BookVersionStatusTrash {
		return v, err
	}
	claimed := *v
	claimed.Status = database.BookVersionStatusPurging
	if err := s.Store.UpdateBookVersion(&claimed); err != nil {
		s.t.Fatal(err)
	}
	return v, nil
}

func TestHandleRestoreVersion_PurgeClaimedAfterTheReadIsA409(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1.m4b"}); err != nil {
		t.Fatal(err)
	}
	trashedAt := time.Now()
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.store = claimAfterReadStore{Store: store, t: t}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("restore racing a purge claim: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusPurging {
		t.Errorf("status = %s, want the purge claim to stand", got.Status)
	}
}

// The retired and purging refusals do not catch a plain alt: it keeps the 400
// (control for the two 409s above), and purge-now of an alt is still refused.
func TestHandlePurgeVersion_AltIsStillRefused(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Test Book", FilePath: "/tmp/b1.m4b"}); err != nil {
		t.Fatal(err)
	}
	alt, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+alt.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("purge-now of an alt: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
