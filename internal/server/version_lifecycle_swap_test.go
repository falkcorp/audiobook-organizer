// file: internal/server/version_lifecycle_swap_test.go
// version: 1.0.0
// guid: 6b1d8e3f-7a24-4c95-8e0b-d2f4a9c1b753
// last-edited: 2026-10-28

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// swapTrashFixture is a book ("The Long Halloween") in the library whose active
// version plays one file at the book folder.
type swapTrashFixture struct {
	t       *testing.T
	srv     *Server
	store   database.Store
	book    *database.Book
	dir     string
	rootAud string
	active  *database.BookVersion
}

func newSwapTrashFixture(t *testing.T) *swapTrashFixture {
	t.Helper()
	srv, store := setupVersionLifecycleServer(t)
	dir := libraryDir(t, "The Long Halloween")
	rootAud := filepath.Join(dir, "The Long Halloween.m4b")
	if err := os.WriteFile(rootAud, []byte("active"), 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: rootAud, Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
		IngestDate: time.Now().Add(-100 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		BookID: book.ID, VersionID: active.ID, FilePath: rootAud, Format: "m4b",
	}); err != nil {
		t.Fatal(err)
	}
	return &swapTrashFixture{t: t, srv: srv, store: store, book: book, dir: dir, rootAud: rootAud, active: active}
}

// add makes a version of the book in status, ingested ago ago; a name also gives
// it one file of that name in its slot.
func (f *swapTrashFixture) add(status string, ago time.Duration, name string) (*database.BookVersion, string) {
	f.t.Helper()
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: status, Format: "mp3", Source: "deluge",
		IngestDate: time.Now().Add(-ago),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	path := ""
	if name != "" {
		path = filepath.Join(versions.VersionSlotDir(f.dir, v.ID), name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("alt-"+v.ID), 0o644); err != nil {
			f.t.Fatal(err)
		}
		if err := f.store.CreateBookFile(&database.BookFile{
			BookID: f.book.ID, VersionID: v.ID, FilePath: path, Format: "mp3",
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	return v, path
}

func (f *swapTrashFixture) trash(versionID string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/audiobooks/"+f.book.ID+"/versions/"+versionID, nil)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	return w
}

func (f *swapTrashFixture) version(id string) *database.BookVersion {
	f.t.Helper()
	v, err := f.store.GetBookVersion(id)
	if err != nil || v == nil {
		f.t.Fatalf("get version %s: %v", id, err)
	}
	return v
}

// snapshot records every version's status and row version, so a refusal can be
// shown to have written nothing.
func (f *swapTrashFixture) snapshot() map[string][2]any {
	f.t.Helper()
	all, err := f.store.GetBookVersionsByBookID(f.book.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	out := make(map[string][2]any, len(all))
	for _, v := range all {
		out[v.ID] = [2]any{v.Status, v.Version}
	}
	return out
}

func (f *swapTrashFixture) requireUntouched(before map[string][2]any) {
	f.t.Helper()
	f.requireSameStatuses(before, true)
}

// requireSameStatuses checks every version has the status it had; with
// rowVersions it also checks no version was written (a rolled-back swap writes
// each twice, so it compares statuses only).
func (f *swapTrashFixture) requireSameStatuses(before map[string][2]any, rowVersions bool) {
	f.t.Helper()
	after := f.snapshot()
	if len(after) != len(before) {
		f.t.Fatalf("versions: %d -> %d", len(before), len(after))
	}
	for id, b := range before {
		if after[id][0] != b[0] || (rowVersions && after[id][1] != b[1]) {
			f.t.Errorf("version %s: status/row-version %v -> %v, want untouched", id, b, after[id])
		}
	}
	if b, _ := f.store.GetBookByID(f.book.ID); b.FilePath != f.rootAud {
		f.t.Errorf("book file_path = %s, want %s", b.FilePath, f.rootAud)
	}
	if _, err := os.Stat(f.rootAud); err != nil {
		f.t.Errorf("the active version's file moved: %v", err)
	}
}

func errorCodeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return body.Code
}

// (a) Trashing the active version swaps the newest eligible alt in first, then
// trashes it: the alt's files are the book's now, the old active version's are
// in its slot, and the book's file_path follows.
func TestHandleTrashVersion_ActiveSwapsFirst(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, 48*time.Hour, "Old.mp3")
	target, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")

	w := f.trash(f.active.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got, _ := findKey(decodeBody(t, w), "swapped_to").(string); got != target.ID {
		t.Errorf("swapped_to = %q, want %s in %s", got, target.ID, w.Body.String())
	}

	old := f.version(f.active.ID)
	if old.Status != database.BookVersionStatusTrash || old.TrashedAt == nil {
		t.Errorf("old active: status=%s TrashedAt=%v, want trash and stamped", old.Status, old.TrashedAt)
	}
	if got := f.version(target.ID); got.Status != database.BookVersionStatusActive {
		t.Errorf("target status = %s, want active", got.Status)
	}
	if got, _ := f.store.GetActiveVersionForBook(f.book.ID); got == nil || got.ID != target.ID {
		t.Errorf("active version of the book = %v, want %s", got, target.ID)
	}

	newRoot := filepath.Join(f.dir, "Year One.mp3")
	if b, _ := f.store.GetBookByID(f.book.ID); b.FilePath != newRoot {
		t.Errorf("book file_path = %s, want %s", b.FilePath, newRoot)
	}
	if _, err := os.Stat(newRoot); err != nil {
		t.Errorf("the new active version's file is not at the book folder: %v", err)
	}
	oldSlot := filepath.Join(versions.VersionSlotDir(f.dir, f.active.ID), "The Long Halloween.m4b")
	if _, err := os.Stat(oldSlot); err != nil {
		t.Errorf("the trashed version's file is not in its slot: %v", err)
	}
	if _, err := os.Stat(f.rootAud); !os.IsNotExist(err) {
		t.Errorf("the trashed version's file is still at the book folder: %v", err)
	}
	// The file rows follow the files, so what the purge deletes is the slot.
	rows, _ := f.store.GetBookFiles(f.book.ID)
	for _, r := range rows {
		if r.VersionID == f.active.ID && r.FilePath != oldSlot {
			t.Errorf("trashed version's row names %s, want %s", r.FilePath, oldSlot)
		}
	}
}

// After swap-then-trash the trashed version's purge leaves the book's playable
// file alone: the case that lost data before the swap came first.
func TestHandleTrashVersion_ActiveSwapsFirst_PurgeKeepsBookFile(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	if w := f.trash(f.active.ID); w.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+f.book.ID+"/versions/"+f.active.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("purge-now: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "Year One.mp3")); err != nil {
		t.Errorf("the purge removed the file the book plays: %v", err)
	}
	if _, err := os.Stat(filepath.Join(versions.VersionSlotDir(f.dir, f.active.ID), "The Long Halloween.m4b")); !os.IsNotExist(err) {
		t.Errorf("the trashed version's own file survived its purge: %v", err)
	}
}

// (b) The only version: 409 with its own code, nothing written, nothing moved.
func TestHandleTrashVersion_OnlyVersionRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	before := f.snapshot()

	w := f.trash(f.active.ID)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != codeNoSwapTarget {
		t.Errorf("code = %q, want %q", code, codeNoSwapTarget)
	}
	f.requireUntouched(before)
	if got := f.version(f.active.ID); got.Status != database.BookVersionStatusActive || got.TrashedAt != nil {
		t.Errorf("status=%s TrashedAt=%v, want active and unstamped", got.Status, got.TrashedAt)
	}
}

// (c) Every other version is trashed, retired, purging or has no files: 409.
func TestHandleTrashVersion_NoEligibleOtherVersionRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusTrash, time.Hour, "Trashed.mp3")
	f.add(database.BookVersionStatusPurging, time.Hour, "Purging.mp3")
	f.add(database.BookVersionStatusInactivePurged, time.Hour, "")
	f.add(database.BookVersionStatusAlt, time.Minute, "") // an alt, but it owns no files
	before := f.snapshot()

	w := f.trash(f.active.ID)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != codeNoSwapTarget {
		t.Errorf("code = %q, want %q", code, codeNoSwapTarget)
	}
	f.requireUntouched(before)
}

// A retired version of the book itself is not a candidate either, even a newer
// one than the only alt.
func TestHandleTrashVersion_RetiredVersionNeverSwappedIn(t *testing.T) {
	f := newSwapTrashFixture(t)
	retiredAt := time.Now()
	shell, err := f.store.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(f.dir, "shell")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &retiredAt, IngestDate: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot()
	w := f.trash(f.active.ID)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	f.requireUntouched(before)
}

// A multi-file book keeps its folder in FilePath; the swap would put the entering
// version's files in the author's folder, so the trash is refused (409) and
// nothing moves.
func TestHandleTrashVersion_FolderBookRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error {
		b.FilePath = f.dir
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.rootAud = f.dir // what requireUntouched compares the book's path with
	before := f.snapshot()

	w := f.trash(f.active.ID)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != codeNoSwapTarget {
		t.Errorf("code = %q, want %q", code, codeNoSwapTarget)
	}
	f.requireUntouched(before)
	if _, err := os.Stat(filepath.Join(f.dir, "The Long Halloween.m4b")); err != nil {
		t.Errorf("the active version's file moved: %v", err)
	}
}

// (d) The swap fails: the version is not trashed, the answer says so, and the
// versions are back as they were because nothing had moved yet.
func TestHandleTrashVersion_SwapFailureDoesNotTrash(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	// A regular file where the .versions folder must be, so the leaving
	// version's slot cannot be created. The alt's file is moved aside with the
	// folder and its row follows it, so the preflight still passes and the
	// failure is the swap's own, before anything has moved.
	versionsDir := versions.VersionsDir(f.dir)
	moved := versionsDir + ".aside"
	if err := os.Rename(versionsDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionsDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.store.GetBookFiles(f.book.ID)
	for _, r := range rows {
		if r.VersionID == alt.ID {
			r.FilePath = filepath.Join(moved, alt.ID, "Year One.mp3")
			if err := f.store.UpdateBookFile(r.ID, &r); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := f.snapshot()

	w := f.trash(f.active.ID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != codeVersionSwapFailed {
		t.Errorf("code = %q, want %q", code, codeVersionSwapFailed)
	}
	got := f.version(f.active.ID)
	if got.Status == database.BookVersionStatusTrash || got.TrashedAt != nil {
		t.Errorf("the version was trashed despite the failed swap: status=%s TrashedAt=%v", got.Status, got.TrashedAt)
	}
	// Nothing had moved, so the swap put the statuses back (each version was
	// written twice, marked and restored, so only the statuses are compared).
	f.requireSameStatuses(before, false)
}

// (e) A version that is not the active one is trashed as before: no swap, the
// active version stays, no swapped_to.
func TestHandleTrashVersion_NonActiveUnchanged(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt, altFile := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	before := f.snapshot()

	w := f.trash(alt.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := f.version(alt.ID); got.Status != database.BookVersionStatusTrash || got.TrashedAt == nil {
		t.Errorf("alt: status=%s TrashedAt=%v, want trash and stamped", got.Status, got.TrashedAt)
	}
	if got := f.version(f.active.ID); got.Status != database.BookVersionStatusActive || got.Version != before[f.active.ID][1] {
		t.Errorf("active version was touched: status=%s row version %v -> %d", got.Status, before[f.active.ID][1], got.Version)
	}
	if _, err := os.Stat(altFile); err != nil {
		t.Errorf("the alt's file moved: %v", err)
	}
	if _, err := os.Stat(f.rootAud); err != nil {
		t.Errorf("the active file moved: %v", err)
	}
	if findKey(decodeBody(t, w), "swapped_to") != nil {
		t.Errorf("swapped_to present for a non-active version: %s", w.Body.String())
	}
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return m
}

// findKey looks a key up at the top of the body or inside its "data" envelope.
func findKey(m map[string]any, key string) any {
	if v, ok := m[key]; ok {
		return v
	}
	if d, ok := m["data"].(map[string]any); ok {
		return d[key]
	}
	return nil
}

// A running merge or combine holds the lock for the whole wait: nothing is
// swapped or trashed, for the active version or any other.
func TestHandleTrashVersion_MergeBusyWritesNothing(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	before := f.snapshot()

	prev := merge.InteractiveMergeLockWait
	merge.InteractiveMergeLockWait = 30 * time.Millisecond
	t.Cleanup(func() { merge.InteractiveMergeLockWait = prev })
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()

	for _, id := range []string{f.active.ID, alt.ID} {
		w := f.trash(id)
		if w.Code != http.StatusConflict {
			t.Fatalf("trash %s: expected 409, got %d: %s", id, w.Code, w.Body.String())
		}
	}
	f.requireUntouched(before)
}
