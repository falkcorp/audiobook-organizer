// file: internal/server/organize_library_copy_wiring_test.go
// version: 1.0.0
// guid: 96b14120-70fb-45b5-93bd-c9e1c9449b82
// last-edited: 2026-09-30

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The single-book organize handler exactly as production builds it
// (Server.newOrganizeHandler), resolving through the REAL
// metafetch.Service.ExistingLibraryCopyOfFile against a real store, with the
// original under a protected (iTunes) tree. Guards three mutants: no resolver
// wired, a resolver that always answers (book, true), and a resolver that
// accepts any library sibling (a different edition).

type orgWiringFixture struct {
	srv    *Server
	store  *database.PebbleStore
	root   string
	itunes string
	author int
}

func newOrgWiringFixture(t *testing.T) *orgWiringFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	base := t.TempDir()
	root, itunes := filepath.Join(base, "library"), filepath.Join(base, "itunes")
	for _, d := range []string{root, itunes} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config.AppConfig = config.Config{
		RootDir:              root,
		FolderNamingPattern:  "{author}/{title}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	}
	// isProtectedPath treats the iTunes library's directory as protected.
	config.AppConfig.ITunes.LibraryReadPath = filepath.Join(itunes, "iTunes Library.xml")

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
	srv := &Server{
		store:                store,
		organizeService:      NewOrganizeService(store),
		metadataFetchService: metafetch.NewService(store),
	}
	return &orgWiringFixture{srv: srv, store: store, root: root, itunes: itunes, author: author.ID}
}

// book writes content at path and creates the row and its one file row.
func (f *orgWiringFixture) book(t *testing.T, id, path string, content []byte, b database.Book) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	b.ID, b.FilePath, b.Title, b.AuthorID = id, path, "Welcome to the Rift", &f.author
	if _, err := f.store.CreateBook(&b); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{ID: id + "-f", BookID: id, FilePath: path, FileSize: int64(len(content))}); err != nil {
		t.Fatalf("create file row %s: %v", id, err)
	}
}

func (f *orgWiringFixture) call(t *testing.T, method, id string, preview bool) (int, map[string]any) {
	t.Helper()
	h := f.srv.newOrganizeHandler()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/audiobooks/"+id+"/organize", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	if preview {
		h.PreviewOrganize(c)
	} else {
		h.OrganizeBook(c)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if data, ok := body["data"].(map[string]any); ok {
		return w.Code, data
	}
	return w.Code, body
}

func strp(s string) *string { return &s }

// The book's own library copy, whose FileHash has since diverged (tags were
// written and a scan re-hashed it) but whose OriginalFileHash is still the
// original's: preview and apply both act on the copy, apply is a no-op.
func TestOrganizeWiring_ProtectedOriginalUsesItsOwnLibraryCopy(t *testing.T) {
	f := newOrgWiringFixture(t)
	content := bytes.Repeat([]byte{7}, 300)
	group := "vg-rift"
	origPath := filepath.Join(f.itunes, "Music", "Welcome to the Rift.mp3")
	copyPath := filepath.Join(f.root, "Harmon Example", "Welcome to the Rift", "Welcome to the Rift.mp3")
	f.book(t, "orig", origPath, content, database.Book{FileHash: strp("h-orig"), VersionGroupID: &group, LibraryState: strp("imported")})
	f.book(t, "copy", copyPath, content, database.Book{
		FileHash: strp("h-after-tag-write"), OriginalFileHash: strp("h-orig"),
		VersionGroupID: &group, LibraryState: strp("organized"),
	})

	code, preview := f.call(t, http.MethodGet, "orig", true)
	if code != http.StatusOK {
		t.Fatalf("preview HTTP %d: %v", code, preview)
	}
	code, applied := f.call(t, http.MethodPost, "orig", false)
	if code != http.StatusOK {
		t.Fatalf("apply HTTP %d: %v", code, applied)
	}
	if preview["book_id"] != "copy" || applied["book_id"] != "copy" || applied["original_book_id"] != "orig" {
		t.Errorf("preview acts on %v, apply on %v (original %v); both must act on the library copy",
			preview["book_id"], applied["book_id"], applied["original_book_id"])
	}
	if preview["target_path"] != applied["new_path"] || applied["new_path"] != copyPath {
		t.Errorf("preview target %v, apply landed %v; want both %s", preview["target_path"], applied["new_path"], copyPath)
	}
	if applied["message"] != "already organized" {
		t.Errorf("apply message %v; want the already-organized no-op", applied["message"])
	}
	if books, _ := f.store.GetBooksByVersionGroup(group); len(books) != 2 {
		t.Errorf("version group has %d rows; organize must not have made another copy", len(books))
	}
}

// Same version group, different content: the sibling is another EDITION, not
// this book's copy. Organize copies the protected book itself and leaves the
// other edition exactly where it was.
func TestOrganizeWiring_DifferentEditionIsNotTheLibraryCopy(t *testing.T) {
	f := newOrgWiringFixture(t)
	group := "vg-rift"
	origPath := filepath.Join(f.itunes, "Music", "Welcome to the Rift.m4b")
	editionPath := filepath.Join(f.root, "Harmon Example", "Welcome to the Rift (mp3)", "Welcome to the Rift.mp3")
	f.book(t, "orig", origPath, bytes.Repeat([]byte{1}, 300), database.Book{FileHash: strp("h-m4b"), VersionGroupID: &group, LibraryState: strp("imported")})
	f.book(t, "mp3", editionPath, bytes.Repeat([]byte{2}, 300), database.Book{
		FileHash: strp("h-mp3"), OriginalFileHash: strp("h-mp3"), OrganizedFileHash: strp("h-mp3"),
		VersionGroupID: &group, LibraryState: strp("organized"),
	})

	// The metadata apply's own resolver still accepts the edition (its rule
	// is unchanged); organize's does not.
	if _, ok := f.srv.organizeLibraryCopyResolver()(mustBook(t, f.store, "orig")); ok {
		t.Fatalf("organize resolver accepted a different edition as the library copy")
	}

	code, preview := f.call(t, http.MethodGet, "orig", true)
	if code != http.StatusOK {
		t.Fatalf("preview HTTP %d: %v", code, preview)
	}
	if preview["book_id"] != "orig" || preview["needs_copy"] != true {
		t.Errorf("preview = book %v needs_copy %v; want the m4b itself, copied", preview["book_id"], preview["needs_copy"])
	}
	code, applied := f.call(t, http.MethodPost, "orig", false)
	if code != http.StatusOK {
		t.Fatalf("apply HTTP %d: %v", code, applied)
	}
	if applied["book_id"] == "mp3" || applied["original_book_id"] != "orig" {
		t.Errorf("apply acted on %v (original %v); want a new copy of orig", applied["book_id"], applied["original_book_id"])
	}
	if applied["new_path"] == editionPath || applied["new_path"] != preview["target_path"] {
		t.Errorf("apply landed %v, preview promised %v, edition at %s", applied["new_path"], preview["target_path"], editionPath)
	}
	if _, err := os.Stat(editionPath); err != nil {
		t.Errorf("the mp3 edition was moved: %v", err)
	}
	if ed := mustBook(t, f.store, "mp3"); ed.FilePath != editionPath {
		t.Errorf("the mp3 edition's row moved to %s", ed.FilePath)
	}
}

func mustBook(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return b
}
