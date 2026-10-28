// file: internal/server/version_lifecycle_swap_test.go
// version: 1.2.0
// guid: 6b1d8e3f-7a24-4c95-8e0b-d2f4a9c1b753
// last-edited: 2026-10-28

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

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
	// bookPath is the book's file_path a refused or failed trash must leave
	// alone; rootAud unless a test changed the book's path first.
	bookPath string
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
	return &swapTrashFixture{t: t, srv: srv, store: store, book: book, dir: dir, rootAud: rootAud, active: active, bookPath: rootAud}
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
	if b, _ := f.store.GetBookByID(f.book.ID); b.FilePath != f.bookPath {
		f.t.Errorf("book file_path = %q, want %q", b.FilePath, f.bookPath)
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
	f.add(database.BookVersionStatusAlt, 48*time.Hour, "Knightfall.mp3")
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
	f.add(database.BookVersionStatusTrash, time.Hour, "Cry for Blood.mp3")
	f.add(database.BookVersionStatusPurging, time.Hour, "Battle for the Cowl.mp3")
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
	f.bookPath = f.dir // what requireUntouched compares the book's path with
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

// A regular file where the leaving version's slot must be: the preflight sees
// that the slot is not a folder and the trash is refused (409) before anything
// is written. (It used to pass the preflight and fail inside the swap, which is
// no longer how a swap failure is injected.)
func TestHandleTrashVersion_SlotThatIsAFileIsRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	if err := os.MkdirAll(versions.VersionsDir(f.dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versions.VersionSlotDir(f.dir, f.active.ID), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedTrash("is a file, not a folder")
}

// (d) The swap fails after the preflight passed: the version is not trashed,
// the answer says so, and the versions are back as they were because nothing had
// moved yet. The failure is a .versions folder the process may not write to: the
// leaving version's slot cannot be created.
func TestHandleTrashVersion_SwapFailureDoesNotTrash(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	versionsDir := versions.VersionsDir(f.dir)
	if err := os.Chmod(versionsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(versionsDir, 0o755) })
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

// failingFileStore fails the book_file write of one row, so a swap that has moved
// its files cannot repoint that row: RunVersionSwap returns ErrSwapIncomplete.
type failingFileStore struct {
	database.Store
	failFileID string
}

func (s *failingFileStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if fileID == s.failFileID {
		return nil, errors.New("injected: book_file write failed")
	}
	return s.Store.ModifyBookFile(bookID, fileID, fn)
}

// The swap moves the files and then cannot write a row: RunVersionSwap returns
// ErrSwapIncomplete and the versions are left in swapping_out/swapping_in. The
// answer is its own code, says the books' versions are stuck and need repair, and
// does not invite a retry; the version is not trashed. A retried DELETE (with the
// database healthy again) is refused (409) for BOTH versions and trashes neither:
// the leaving version is no longer 'active', so without the lifecycle's guard
// the retry went straight to MarkTrashed, and 14 days later the purge deleted
// the files the book plays.
func TestHandleTrashVersion_SwapIncompleteDoesNotTrashAndRetryIsRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	var toRow string
	rows, err := f.store.GetBookFiles(f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.VersionID == alt.ID {
			toRow = r.ID
		}
	}
	if toRow == "" {
		t.Fatal("no book_file row for the alt")
	}
	healthy := f.srv.store
	f.srv.store = &failingFileStore{Store: healthy, failFileID: toRow}

	w := f.trash(f.active.ID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != codeVersionSwapIncomplete {
		t.Errorf("code = %q, want %q: %s", code, codeVersionSwapIncomplete, w.Body.String())
	}
	body := strings.ToLower(w.Body.String())
	for _, want := range []string{"moved files", "stuck mid-swap", "needs repair"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer does not say %q: %s", want, w.Body.String())
		}
	}
	for _, invites := range []string{"try again", "can be trashed again", "retry"} {
		if strings.Contains(body, invites) {
			t.Errorf("the answer invites a retry (%q): %s", invites, w.Body.String())
		}
	}
	for _, id := range []string{f.active.ID, alt.ID} {
		if got := f.version(id); got.Status == database.BookVersionStatusTrash || got.TrashedAt != nil {
			t.Errorf("version %s was trashed after an incomplete swap: status=%s", id, got.Status)
		}
	}
	if got := f.version(f.active.ID); got.Status != database.BookVersionStatusSwappingOut {
		t.Errorf("leaving version status = %s, want swapping_out", got.Status)
	}
	if got := f.version(alt.ID); got.Status != database.BookVersionStatusSwappingIn {
		t.Errorf("entering version status = %s, want swapping_in", got.Status)
	}

	// The database is healthy again; the client retries the DELETE.
	f.srv.store = healthy
	disk := diskTree(t, f.dir)
	for _, id := range []string{f.active.ID, alt.ID} {
		w := f.trash(id)
		if w.Code != http.StatusConflict {
			t.Fatalf("retry of %s: expected 409, got %d: %s", id, w.Code, w.Body.String())
		}
		if code := errorCodeOf(t, w); code != codeVersionSwapping {
			t.Errorf("retry of %s: code = %q, want %q", id, code, codeVersionSwapping)
		}
		if got := f.version(id); got.Status == database.BookVersionStatusTrash || got.TrashedAt != nil {
			t.Errorf("the retry trashed %s: status=%s TrashedAt=%v", id, got.Status, got.TrashedAt)
		}
	}
	if got := diskTree(t, f.dir); got != disk {
		t.Errorf("the retry changed the disk:\nbefore:\n%s\nafter:\n%s", disk, got)
	}
}

// diskTree describes every entry under roots (type, link target, content), so
// "nothing moved on disk" is a comparison of two strings.
func diskTree(t *testing.T, roots ...string) string {
	t.Helper()
	var lines []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, _ := os.Readlink(p)
				lines = append(lines, fmt.Sprintf("%s -> %s", p, target))
			case d.IsDir():
				lines = append(lines, p+"/")
			default:
				b, _ := os.ReadFile(p)
				lines = append(lines, fmt.Sprintf("%s = %q", p, b))
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// requireRefusedTrash trashes the active version and expects the 409
// NO_SWAP_TARGET that names why, with the disk (the book's folder and watch)
// and every version exactly as they were, and the version not trashed.
func (f *swapTrashFixture) requireRefusedTrash(why string, watch ...string) {
	f.t.Helper()
	roots := append([]string{f.dir}, watch...)
	diskBefore, before := diskTree(f.t, roots...), f.snapshot()

	w := f.trash(f.active.ID)
	if w.Code != http.StatusConflict {
		f.t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(f.t, w); code != codeNoSwapTarget {
		f.t.Errorf("code = %q, want %q", code, codeNoSwapTarget)
	}
	if !strings.Contains(w.Body.String(), why) {
		f.t.Errorf("the 409 does not give the reason %q: %s", why, w.Body.String())
	}
	if got := diskTree(f.t, roots...); got != diskBefore {
		f.t.Errorf("the disk changed:\nbefore:\n%s\nafter:\n%s", diskBefore, got)
	}
	f.requireUntouched(before)
	if got := f.version(f.active.ID); got.Status != database.BookVersionStatusActive || got.TrashedAt != nil {
		f.t.Errorf("status=%s TrashedAt=%v, want active and unstamped", got.Status, got.TrashedAt)
	}
}

// addAltRow makes an alt version whose one file row names path (the file is
// whatever the test put there).
func (f *swapTrashFixture) addAltRow(path string) *database.BookVersion {
	f.t.Helper()
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "deluge",
		IngestDate: time.Now().Add(-time.Hour),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, VersionID: v.ID, FilePath: path, Format: "m4b"}); err != nil {
		f.t.Fatal(err)
	}
	return v
}

// Twin rows: the alt's row names the active version's own file. The swap used
// to rename that file into the active version's slot, call the move up to the
// root done, trash the active version, and the purge then deleted the only copy.
func TestHandleTrashVersion_TwinPathRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.addAltRow(f.rootAud)
	f.requireRefusedTrash("same file")
}

// The alt's row is a symlink, in its own slot, to the active version's file: the
// pre-flight passed (stat follows the link), the swap moved the target and its
// third step failed with no way back, leaving the book swapping_out/swapping_in
// and every later trash a 409.
func TestHandleTrashVersion_SymlinkAltRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	alt := f.addAltRow(filepath.Join(f.dir, "placeholder"))
	link := filepath.Join(versions.VersionSlotDir(f.dir, alt.ID), "The Long Halloween.m4b")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.rootAud, link); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.store.GetBookFiles(f.book.ID)
	for _, r := range rows {
		if r.VersionID == alt.ID {
			r.FilePath = link
			if err := f.store.UpdateBookFile(r.ID, &r); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.requireRefusedTrash("same file")
}

// The book row names a folder the active file is not in: the entering audio
// would go into the stale folder.
func TestHandleTrashVersion_StaleBookPathRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	stale := filepath.Join(filepath.Dir(f.dir), "Arkham", "The Long Halloween.m4b")
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { b.FilePath = stale; return nil }); err != nil {
		t.Fatal(err)
	}
	f.bookPath = stale
	f.requireRefusedTrash("book path is stale", filepath.Dir(stale))
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Errorf("the stale folder was created: %v", err)
	}
}

// An empty file_path makes the book's folder ".", the process's working
// directory: the entering audio would be moved there.
func TestHandleTrashVersion_EmptyBookPathRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { b.FilePath = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	f.bookPath = ""
	cwd := t.TempDir()
	t.Chdir(cwd)
	f.requireRefusedTrash("no file path", cwd)
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("the working directory has %d entries, want none", len(entries))
	}
}

// An alt whose file is in a download folder (or any folder but its own slot) is
// not the swap's to take.
func TestHandleTrashVersion_AltOutsideBookFolderRefused(t *testing.T) {
	f := newSwapTrashFixture(t)
	dl := filepath.Join(t.TempDir(), "downloads", "Hush.m4b")
	if err := os.MkdirAll(filepath.Dir(dl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dl, []byte("download"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.addAltRow(dl)
	f.requireRefusedTrash("not inside its own slot", filepath.Dir(dl))
}

// A step-3 failure (the entering file cannot be taken out of its slot: the same
// shape as an alt on another filesystem, EXDEV, or a permission error) is rolled
// back: the leaving file is moved back, the statuses are restored, the book
// keeps its active version, and the version is not trashed. It used to leave the
// book swapping_out/swapping_in with no active index, and every later trash a 409.
func TestHandleTrashVersion_Step3FailureRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSwapTrashFixture(t)
	alt, _ := f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	altSlot := versions.VersionSlotDir(f.dir, alt.ID)
	if err := os.Chmod(altSlot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(altSlot, 0o755) })
	diskBefore, before := diskTree(t, f.dir), f.snapshot()

	w := f.trash(f.active.ID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if code := errorCodeOf(t, w); code != codeVersionSwapFailed {
		t.Errorf("code = %q, want %q", code, codeVersionSwapFailed)
	}
	if got := diskTree(t, f.dir); got != diskBefore {
		t.Errorf("the files were not put back:\nbefore:\n%s\nafter:\n%s", diskBefore, got)
	}
	f.requireSameStatuses(before, false)
	if got, _ := f.store.GetActiveVersionForBook(f.book.ID); got == nil || got.ID != f.active.ID {
		t.Errorf("active version = %v, want %s: the book is left without one", got, f.active.ID)
	}
	if got := f.version(f.active.ID); got.Status == database.BookVersionStatusTrash || got.TrashedAt != nil {
		t.Errorf("the version was trashed despite the failed swap: status=%s", got.Status)
	}

	// Not stuck: once the obstacle is gone the same trash goes through.
	if err := os.Chmod(altSlot, 0o755); err != nil {
		t.Fatal(err)
	}
	if w := f.trash(f.active.ID); w.Code != http.StatusOK {
		t.Fatalf("retry: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The purge's last line of defence: when none of the files the book serves
// exists on disk the guard cannot tell what is served, and the purge refuses (409)
// instead of deleting. Here the file the swap put in the book's folder is gone
// (the shape the twin-path bug left behind).
func TestHandlePurgeVersion_NothingServedExistsRefuses(t *testing.T) {
	f := newSwapTrashFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Year One.mp3")
	if w := f.trash(f.active.ID); w.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", w.Code, w.Body.String())
	}
	slotFile := filepath.Join(versions.VersionSlotDir(f.dir, f.active.ID), "The Long Halloween.m4b")
	if err := os.Remove(filepath.Join(f.dir, "Year One.mp3")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+f.book.ID+"/versions/"+f.active.ID+"/purge-now", nil)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("purge-now: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown") {
		t.Errorf("the 409 does not say what is unknown: %s", w.Body.String())
	}
	if _, err := os.Stat(slotFile); err != nil {
		t.Errorf("the purge deleted the trashed version's file although nothing the book serves exists: %v", err)
	}
	if got := f.version(f.active.ID); got.Status != database.BookVersionStatusTrash {
		t.Errorf("status = %s, want trash (restorable)", got.Status)
	}
}

// A trash that fails after the swap went through says so: the book now plays the
// other version, and a bare 500 would read as "nothing changed".
func TestRespondTrashFailedAfterSwap_NamesTheVersionSwappedTo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		swappedTo string
		want      bool
	}{{"01ALT", true}, {"", false}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodDelete, "/", nil)
		(&Server{}).respondTrashFailedAfterSwap(c, tc.swappedTo, errors.New("write failed"))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code = %d, want 500", w.Code)
		}
		got, _ := decodeBody(t, w)["swapped_to"].(string)
		if tc.want && got != tc.swappedTo {
			t.Errorf("swapped_to = %q, want %q in %s", got, tc.swappedTo, w.Body.String())
		}
		if !tc.want && got != "" {
			t.Errorf("swapped_to = %q for a trash that swapped nothing: %s", got, w.Body.String())
		}
	}
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
