// file: internal/server/handlers/organize_library_copy_test.go
// version: 1.1.0
// guid: 68af52f3-742d-4b0c-99ba-e3a73e2e0443
// last-edited: 2026-09-30

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// End-to-end replay of the 2026-09-30 prod failure through the two HTTP
// handlers the book-detail page calls: GET preview-organize, then POST
// organize, on a protected original (outside RootDir) whose metadata apply
// already made a library copy at the organize target. Both calls used to act
// on the original; the apply failed with 500 "duplicate file already
// organized at <the copy>" and the collision hook filed a dedup candidate
// pairing the book with its own copy. Real store, real organize and preview
// services; only the resolver is a stand-in for metafetch's.

type handlerCollisionSpy struct {
	mu    sync.Mutex
	calls int
}

func (s *handlerCollisionSpy) OnCollision(string, string) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
}

func TestOrganizeHandlers_ProtectedOriginalActsOnItsLibraryCopy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	root := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             root,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	store.UseMemDB = true

	author, err := store.CreateAuthor("Harmon Example")
	if err != nil {
		t.Fatal(err)
	}
	hash := "protected-copy-hash"
	group := "vg-protected"
	organized := "organized"
	imported := "imported"
	content := bytes.Repeat([]byte{5}, 256)

	mk := func(id, path string, state *string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateBook(&database.Book{
			ID: id, Title: "Welcome to the Rift", FilePath: path, FileHash: &hash,
			AuthorID: &author.ID, VersionGroupID: &group, LibraryState: state,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := store.CreateBookFile(&database.BookFile{ID: id + "-f", BookID: id, FilePath: path, FileSize: int64(len(content))}); err != nil {
			t.Fatalf("create file row %s: %v", id, err)
		}
	}
	origPath := filepath.Join(t.TempDir(), "newbooks", "Welcome to the Rift.mp3")
	copyPath := filepath.Join(root, "Harmon Example", "Welcome to the Rift", "Welcome to the Rift.mp3")
	mk("orig", origPath, &imported)
	mk("copy", copyPath, &organized) // created second: the hash index names the copy, as in prod
	if hit, _ := store.GetBookByFileHash(hash); hit == nil || hit.ID != "copy" {
		t.Fatalf("fixture: hash lookup must name the copy, got %+v", hit)
	}

	resolveCalls := 0
	resolve := organizer.LibraryCopyResolver(func(b *database.Book) (*database.Book, bool) {
		resolveCalls++
		if b.ID == "orig" {
			cp, err := store.GetBookByID("copy")
			return cp, err == nil && cp != nil
		}
		return b, true
	})
	spy := &handlerCollisionSpy{}
	orgSvc := organizer.NewService(store)
	orgSvc.SetOrganizeHooks(spy)
	previewSvc := organizer.NewPreviewService(store)
	previewSvc.ResolveLibraryCopy = resolve
	h := handlers.NewOrganizeHandler(store, nil, previewSvc, orgSvc, nil, nil, false)
	h.SetLibraryCopyResolver(resolve)

	call := func(method string, fn gin.HandlerFunc) (int, map[string]any) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/audiobooks/orig/organize", nil)
		c.Params = gin.Params{{Key: "id", Value: "orig"}}
		fn(c)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		data, _ := body["data"].(map[string]any)
		if data == nil {
			data = body
		}
		return w.Code, data
	}

	code, preview := call(http.MethodGet, h.PreviewOrganize)
	if code != http.StatusOK {
		t.Fatalf("preview: HTTP %d %v", code, preview)
	}
	code, applied := call(http.MethodPost, h.OrganizeBook)
	if code != http.StatusOK {
		t.Fatalf("apply: HTTP %d %v -- the prod failure was 500 'duplicate file already organized'", code, applied)
	}

	if preview["book_id"] != "copy" || preview["library_copy_of"] != "orig" {
		t.Errorf("preview acts on %v (copy of %v); want the library copy of orig", preview["book_id"], preview["library_copy_of"])
	}
	if applied["book_id"] != "copy" || applied["original_book_id"] != "orig" {
		t.Errorf("apply acted on %v (original %v); want the library copy of orig", applied["book_id"], applied["original_book_id"])
	}
	if preview["target_path"] != applied["new_path"] {
		t.Errorf("preview promised %v, apply landed at %v", preview["target_path"], applied["new_path"])
	}
	if applied["new_path"] != copyPath || applied["message"] != "already organized" {
		t.Errorf("apply = %v; want the already-organized no-op at %s", applied, copyPath)
	}
	if preview["needs_copy"] != false {
		t.Errorf("preview proposes a copy although the library copy exists: %v", preview["steps"])
	}
	if spy.calls != 0 {
		t.Errorf("collision hook fired %d time(s); want 0", spy.calls)
	}
	// Preview resolves once. Apply resolves to build its lock set {orig,
	// copy} and once more under the lock to confirm the set, then acts on that
	// under-lock result: a third resolution in the core could pick a copy it
	// does not hold.
	if resolveCalls != 3 {
		t.Errorf("resolver ran %d time(s); want preview 1 + apply 2 (lock set, under-lock check)", resolveCalls)
	}
	if _, err := os.Stat(origPath); err != nil {
		t.Errorf("protected original was touched: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(copyPath))
	if len(entries) != 1 {
		t.Errorf("library folder holds %d entries; a second copy was written", len(entries))
	}
	if cp, _ := store.GetBookByID("copy"); cp == nil || cp.FilePath != copyPath {
		t.Errorf("library copy row changed: %+v", cp)
	}
}

// Without a resolver (metafetch not wired) the handler still organizes the
// requested book, and a book whose content is already in the library as
// another version of itself is a 409 naming the copy -- never a 500, never a
// second copy.
func TestOrganizeBook_LibraryCopyExistsIsA409(t *testing.T) {
	store := &organizeStoreFake{book: &database.Book{ID: "orig", FilePath: "/import/x.mp3"}}
	svc := &organizeSvcSpy{err: &organizer.LibraryCopyExistsError{BookID: "orig", CopyID: "copy", CopyPath: "/library/x.mp3"}}
	w, body := organizeBook(t, store, svc)
	if w.Code != http.StatusConflict {
		t.Fatalf("HTTP %d; want 409: %s", w.Code, w.Body.String())
	}
	if body["category"] != "library_copy_exists" || body["copy_book_id"] != "copy" {
		t.Errorf("body = %v", body)
	}
	if svc.createCalls != 0 {
		t.Errorf("CreateOrganizedVersion called %d time(s)", svc.createCalls)
	}
}
