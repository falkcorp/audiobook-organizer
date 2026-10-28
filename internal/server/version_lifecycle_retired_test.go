// file: internal/server/version_lifecycle_retired_test.go
// version: 1.2.1
// guid: 6d2a9c41-3b7e-4f08-a5d1-7e9b04c8f326
// last-edited: 2026-10-28

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
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
	bookDir := libraryDir(t, "Hush")
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
	trashBook(t, store, "b1")
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

// trashBook puts book id in the trash. A live book serves every file of its
// trashed version that is outside the version's own .versions/{id}/ slot, and
// purge-now is refused with a 409 (TestHandlePurgeVersion_KeepsFileTheBookServes);
// the tests that use this are about what the rest of the purge does with a file,
// so the book is out of the way.
func trashBook(t *testing.T, store database.Store, id string) {
	t.Helper()
	yes := true
	if _, err := store.ModifyBook(id, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil }); err != nil {
		t.Fatal(err)
	}
}

// trashedVersionWithRow makes book b1 (a live book with a file of its own in the
// library) and a trashed version of it that owns one book_file row naming path.
func trashedVersionWithRow(t *testing.T, store database.Store, path string) *database.BookVersion {
	t.Helper()
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Batman: Year One", FilePath: filepath.Join(libraryDir(t, "Year One"), "book.m4b")}); err != nil {
		t.Fatal(err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: "b1", FilePath: path, VersionID: ver.ID, TrackNumber: 1}); err != nil {
		t.Fatal(err)
	}
	return ver
}

func postPurgeNow(t *testing.T, srv *Server, ver *database.BookVersion) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// A row naming a directory that still holds files is not a failure to retry
// forever: the directory is kept, the version is purged, and the answer says
// which path was kept (the review found this shape stranded the version in
// purging, refused for restore and hard delete, failing every night).
func TestHandlePurgeVersion_DirectoryRowIsKeptAndReported(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	stuck := filepath.Join(libraryDir(t, "Knightfall"), "Part 1")
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(stuck, "inner.m4b")
	if err := os.WriteFile(inner, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, stuck)
	trashBook(t, store, "b1")

	w := postPurgeNow(t, srv, ver)
	if w.Code != http.StatusOK {
		t.Fatalf("purge-now with a directory row: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(inner); err != nil {
		t.Errorf("the directory's content was removed: %v", err)
	}
	got, _ := store.GetBookVersion(ver.ID)
	if got.Status != database.BookVersionStatusInactivePurged || len(got.PurgeKept) != 1 || got.PurgeKept[0].Path != stuck {
		t.Fatalf("status=%s purge_kept=%+v, want inactive_purged with the directory kept", got.Status, got.PurgeKept)
	}
	if !strings.Contains(w.Body.String(), `"purge_kept"`) {
		t.Errorf("the answer does not carry the kept files: %s", w.Body.String())
	}
}

// A file that can never be removed (here the folder is read-only) ends the
// version in purge_failed, which is a terminal state with the file and the reason
// on the version, not an eternal purging: purge-now answers 409, restore and
// trash are refused, the trash list shows it with what failed, and purge-now
// retries it once the obstacle is gone.
func TestHandlePurgeVersion_UnremovableFileEndsPurgeFailedAndRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes files from a read-only folder")
	}
	srv, store := setupVersionLifecycleServer(t)
	dir := libraryDir(t, "Hush")
	audio := filepath.Join(dir, "hush.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, audio)
	trashBook(t, store, "b1")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	w := postPurgeNow(t, srv, ver)
	if w.Code != http.StatusConflict {
		t.Fatalf("purge-now with an unremovable file: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	got, _ := store.GetBookVersion(ver.ID)
	if got.Status != database.BookVersionStatusPurgeFailed || got.PurgedDate != nil || got.PurgeFailedAt == nil ||
		len(got.PurgeFailed) != 1 || got.PurgeFailed[0].Path != audio || got.PurgeFailed[0].Reason == "" {
		t.Fatalf("version = %+v, want purge_failed naming %s with a reason", got, audio)
	}
	if _, err := os.Stat(audio); err != nil {
		t.Errorf("the file is gone: %v", err)
	}

	// The trash page lists it, with the failure.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/trashed-versions", nil)
	lw := httptest.NewRecorder()
	srv.router.ServeHTTP(lw, req)
	if lw.Code != http.StatusOK || !strings.Contains(lw.Body.String(), `"purge_failed"`) || !strings.Contains(lw.Body.String(), ver.ID) {
		t.Fatalf("trashed-versions = %d %s, want the purge_failed version with its failure", lw.Code, lw.Body.String())
	}

	// Restore and trash are refused with a 409 that says why.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/versions/"+ver.ID+"/restore", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "purge") {
		t.Fatalf("restore of a purge_failed version: expected 409 naming the purge, got %d: %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/b1/versions/"+ver.ID, nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("trash of a purge_failed version: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusPurgeFailed {
		t.Errorf("status = %s after the refused writes, want purge_failed", got.Status)
	}

	// With the obstacle gone, purge-now retries it and clears the failure.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w = postPurgeNow(t, srv, ver)
	if w.Code != http.StatusOK {
		t.Fatalf("purge-now retry of a purge_failed version: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got, _ = store.GetBookVersion(ver.ID)
	if got.Status != database.BookVersionStatusInactivePurged || len(got.PurgeFailed) != 0 || got.PurgeFailedAt != nil {
		t.Errorf("after the retry: %+v, want inactive_purged with no failure recorded", got)
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Errorf("the file is still there after the retry: %v", err)
	}
}

// A purge_failed version can be hard-deleted: its record goes, its file stays.
func TestHandleHardDeleteVersion_FromPurgeFailed(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
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
	failed := claimed
	failed.Status = database.BookVersionStatusPurgeFailed
	failed.PurgeFailed = []database.PurgeFileNote{{Path: audio, Reason: "operation not permitted"}}
	if err := store.UpdateBookVersion(&failed); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/purged-versions/"+ver.ID, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hard delete of a purge_failed version: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gone, _ := store.GetBookVersion(ver.ID); gone != nil {
		t.Error("the version record is still there")
	}
	if _, err := os.Stat(audio); err != nil {
		t.Errorf("hard delete removed the file: %v", err)
	}
}

// The two list routes the trash page calls. The Trash tab shows trashed versions,
// purges that did not finish and purges that gave up; the Purged tab shows
// purged ones with the files a purge kept; an active or alt version is on
// neither.
func TestHandleListTrashedAndPurgedVersions(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Batman: Year One", FilePath: filepath.Join(libraryDir(t, "Year One"), "book.m4b")}); err != nil {
		t.Fatal(err)
	}
	mk := func(status string) *database.BookVersion {
		v, err := store.CreateBookVersion(&database.BookVersion{BookID: "b1", Status: status, Format: "m4b", Source: "imported"})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	trash := mk(database.BookVersionStatusTrash)
	purging := mk(database.BookVersionStatusTrash)
	failed := mk(database.BookVersionStatusTrash)
	for _, v := range []*database.BookVersion{purging, failed} {
		c := *v
		c.Status = database.BookVersionStatusPurging
		if err := store.UpdateBookVersion(&c); err != nil {
			t.Fatal(err)
		}
	}
	f := *failed
	f.Status = database.BookVersionStatusPurgeFailed
	f.PurgeFailed = []database.PurgeFileNote{{Path: "/lib/Gotham/Hush/hush.m4b", Reason: "read-only file system"}}
	if err := store.UpdateBookVersion(&f); err != nil {
		t.Fatal(err)
	}
	purged := mk(database.BookVersionStatusTrash)
	for _, st := range []string{database.BookVersionStatusPurging, database.BookVersionStatusInactivePurged} {
		c := *purged
		c.Status = st
		c.PurgeKept = []database.PurgeFileNote{{Path: "/lib/Gotham/Knightfall/part1.m4b", Reason: "another book names it"}}
		if err := store.UpdateBookVersion(&c); err != nil {
			t.Fatal(err)
		}
	}
	active := mk(database.BookVersionStatusActive)
	alt := mk(database.BookVersionStatusAlt)

	list := func(path string) (ids map[string]bool, count int, body string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d: %s", path, w.Code, w.Body.String())
		}
		var env struct {
			Data struct {
				Versions []database.BookVersion `json:"versions"`
				Count    int                    `json:"count"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
		ids = map[string]bool{}
		for _, v := range env.Data.Versions {
			ids[v.ID] = true
		}
		return ids, env.Data.Count, w.Body.String()
	}

	ids, count, _ := list("/api/v1/audiobooks/trashed-versions")
	if count != 3 || !ids[trash.ID] || !ids[purging.ID] || !ids[failed.ID] {
		t.Errorf("trashed-versions = %v (count %d), want the trashed, purging and purge_failed versions", ids, count)
	}
	if ids[purged.ID] || ids[active.ID] || ids[alt.ID] {
		t.Errorf("trashed-versions = %v lists a version that is not in the trash", ids)
	}
	ids, count, body := list("/api/v1/audiobooks/purged-versions")
	if count != 1 || !ids[purged.ID] {
		t.Errorf("purged-versions = %v (count %d), want only the purged version", ids, count)
	}
	if !strings.Contains(body, "another book names it") {
		t.Errorf("purged-versions does not carry the kept files: %s", body)
	}
}

// A merge or combine that holds the merge lock makes "Purge now" answer 409 with
// nothing started, rather than hang or delete behind it.
func TestHandlePurgeVersion_BehindARunningMergeIsA409(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	audio := filepath.Join(libraryDir(t, "Hush"), "hush.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, audio)
	prev := merge.InteractiveMergeLockWait
	merge.InteractiveMergeLockWait = 50 * time.Millisecond
	t.Cleanup(func() { merge.InteractiveMergeLockWait = prev })
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	w := postPurgeNow(t, srv, ver)
	if w.Code != http.StatusConflict {
		t.Fatalf("purge-now behind a merge: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(audio); err != nil {
		t.Errorf("the file was removed behind a running merge: %v", err)
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusTrash {
		t.Errorf("status = %s, want the version still in the trash", got.Status)
	}
}

// A library root that is not there (an unmounted library) is a 503 with nothing
// deleted and the version still in the trash.
func TestHandlePurgeVersion_LibraryRootNotReadyIsA503(t *testing.T) {
	srv, store := setupVersionLifecycleServer(t)
	audio := filepath.Join(libraryDir(t, "Hush"), "hush.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver := trashedVersionWithRow(t, store, audio)
	config.AppConfig.RootDir = filepath.Join(t.TempDir(), "not-mounted")

	w := postPurgeNow(t, srv, ver)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("purge-now with the library not mounted: expected 503, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(audio); err != nil {
		t.Errorf("the file was removed: %v", err)
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusTrash {
		t.Errorf("status = %s, want the version still in the trash", got.Status)
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
