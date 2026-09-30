// file: internal/organizer/library_copy_test.go
// version: 1.1.0
// guid: 93a1faee-be59-4abb-ab7d-eb7996f3665e
// last-edited: 2026-09-30

package organizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Replays the 2026-09-30 prod failure: a protected original (outside RootDir)
// whose metadata apply made a library copy -- same file hash, same version
// group, already at the organize target. Organizing the ORIGINAL used to hit
// the same-hash check, call the copy a foreign duplicate, fire OnCollision
// (filing a dedup candidate pairing the book with its own copy) and fail with
// "duplicate file already organized at ...".

type collisionSpy struct {
	mu    sync.Mutex
	calls [][2]string
}

func (s *collisionSpy) OnCollision(currentBookID, occupantPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, [2]string{currentBookID, occupantPath})
}

func (s *collisionSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type libraryCopyFixture struct {
	svc      *Service
	store    *database.PebbleStore
	root     string
	original *database.Book // as stored: FileHash set, outside root
	libCopy  *database.Book // as stored: under root, at the computed target
	hash     string
}

// newLibraryCopyFixture builds the original and a same-hash twin under root at
// the original's computed target. copyGroup is the twin's version group: the
// original's group ("vg-1") makes it the book's own library copy, anything
// else makes it an unrelated duplicate.
func newLibraryCopyFixture(t *testing.T, copyGroup string) *libraryCopyFixture {
	t.Helper()
	svc, store, root := setupInPlace(t)
	author, err := store.CreateAuthor("Some Author")
	if err != nil {
		t.Fatal(err)
	}
	hash := "library-copy-hash-1"
	content := filled(512, 3)
	group := "vg-1"

	srcPath := filepath.Join(t.TempDir(), "newbooks", "Title.mp3")
	orig := addInPlaceBook(t, store, "orig", "Title", srcPath, content, nil, 0)
	orig.Author = nil
	orig.AuthorID = &author.ID
	orig.FileHash = &hash
	orig.VersionGroupID = &group
	if _, err := store.UpdateBook(orig.ID, orig); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, "Some Author", "Title", "Title.mp3")
	cp := addInPlaceBook(t, store, "copy", "Title", target, content, nil, 0)
	cp.Author = nil
	cp.AuthorID = &author.ID
	cp.FileHash = &hash
	cp.VersionGroupID = &copyGroup
	organized := "organized"
	cp.LibraryState = &organized
	if _, err := store.UpdateBook(cp.ID, cp); err != nil {
		t.Fatal(err)
	}

	if hit, _ := store.GetBookByFileHash(hash); hit == nil || hit.ID != "copy" {
		t.Fatalf("fixture: the hash lookup must name the copy (the prod shape), got %+v", hit)
	}
	storedOrig, _ := store.GetBookByID("orig")
	storedCopy, _ := store.GetBookByID("copy")
	return &libraryCopyFixture{svc: svc, store: store, root: root, original: storedOrig, libCopy: storedCopy, hash: hash}
}

func (f *libraryCopyFixture) organizer(spy OrganizeHooks) *Organizer {
	org := NewOrganizer(&config.AppConfig)
	org.SetStore(f.store)
	org.SetHooks(spy)
	return org
}

func TestOrganizeBook_OwnLibraryCopyIsNotACollision(t *testing.T) {
	f := newLibraryCopyFixture(t, "vg-1")
	spy := &collisionSpy{}

	_, _, err := f.organizer(spy).OrganizeBook(f.original)

	var hasCopy *LibraryCopyExistsError
	if !errors.As(err, &hasCopy) {
		t.Fatalf("OrganizeBook(original) = %v; want *LibraryCopyExistsError", err)
	}
	if hasCopy.CopyID != "copy" || hasCopy.CopyPath != f.libCopy.FilePath {
		t.Errorf("error names copy %s at %s; want copy at %s", hasCopy.CopyID, hasCopy.CopyPath, f.libCopy.FilePath)
	}
	if !errors.Is(err, ErrLibraryCopyExists) {
		t.Errorf("error does not unwrap to ErrLibraryCopyExists: %v", err)
	}
	if strings.Contains(err.Error(), "duplicate file already organized") {
		t.Errorf("own library copy reported as a duplicate: %v", err)
	}
	if n := spy.count(); n != 0 {
		t.Errorf("OnCollision fired %d time(s) for the book's own library copy; want 0 (it files a self-pair dedup candidate)", n)
	}
	// Falling through to the target-exists branch would have written a
	// second library copy next to the first.
	dup := filepath.Join(filepath.Dir(f.libCopy.FilePath), "Title_copy1.mp3")
	if _, statErr := os.Stat(dup); statErr == nil {
		t.Errorf("a second library copy was written at %s", dup)
	}
}

func TestOrganizeBook_TrueDuplicateStillRefusedAndReported(t *testing.T) {
	f := newLibraryCopyFixture(t, "vg-unrelated")
	spy := &collisionSpy{}

	_, _, err := f.organizer(spy).OrganizeBook(f.original)

	want := "duplicate file already organized at: " + f.libCopy.FilePath
	if err == nil || err.Error() != want {
		t.Fatalf("OrganizeBook(original) = %v; want %q", err, want)
	}
	var hasCopy *LibraryCopyExistsError
	if errors.As(err, &hasCopy) {
		t.Errorf("an unrelated same-hash book was treated as this book's library copy")
	}
	if n := spy.count(); n != 1 {
		t.Fatalf("OnCollision fired %d time(s); want exactly 1 for a real duplicate", n)
	}
	if got := spy.calls[0]; got[0] != "orig" || got[1] != f.libCopy.FilePath {
		t.Errorf("OnCollision(%q, %q); want (orig, %q)", got[0], got[1], f.libCopy.FilePath)
	}
}

func TestOrganizeBook_UngroupedHashTwinIsStillADuplicate(t *testing.T) {
	// No version group on either side is not "the same group": two empty
	// groups must not exempt a real duplicate.
	f := newLibraryCopyFixture(t, "")
	empty := ""
	f.original.VersionGroupID = &empty
	spy := &collisionSpy{}

	_, _, err := f.organizer(spy).OrganizeBook(f.original)
	if err == nil || !strings.Contains(err.Error(), "duplicate file already organized at") {
		t.Fatalf("OrganizeBook = %v; want the duplicate refusal", err)
	}
	if n := spy.count(); n != 1 {
		t.Errorf("OnCollision fired %d time(s); want 1", n)
	}
}

// Preview and apply must act on the same row and report the same target. With
// the resolver wired, both land on the library copy, which is already at its
// target: the apply is an in-place no-op and nothing collides.
func TestPreviewAndOrganize_ProtectedOriginalResolveToSameLibraryCopy(t *testing.T) {
	f := newLibraryCopyFixture(t, "vg-1")
	spy := &collisionSpy{}
	f.svc.SetOrganizeHooks(spy)
	resolve := LibraryCopyResolver(func(b *database.Book) (*database.Book, bool) {
		if b.ID == "orig" {
			cp, err := f.store.GetBookByID("copy")
			return cp, err == nil && cp != nil
		}
		return b, true
	})

	preview := NewPreviewService(f.store)
	preview.ResolveLibraryCopy = resolve
	resp, err := preview.PreviewOrganize("orig")
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if resp.BookID != "copy" || resp.LibraryCopyOf != "orig" {
		t.Errorf("preview acts on %q (copy of %q); want copy of orig", resp.BookID, resp.LibraryCopyOf)
	}
	if resp.NeedsCopy {
		t.Errorf("preview proposes copying a book whose library copy exists: %+v", resp.Steps)
	}
	if resp.CurrentPath != f.libCopy.FilePath {
		t.Errorf("preview current_path = %s; want the copy's %s", resp.CurrentPath, f.libCopy.FilePath)
	}

	subject := ResolveOrganizeSubject(resolve, f.original)
	landing, err := f.svc.OrganizeOneBook(f.organizer(spy), subject, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook(resolved) = %v", err)
	}
	if !landing.InPlace {
		t.Errorf("organize of the library copy did not take the in-place path: %+v", landing)
	}
	if landing.Path != resp.TargetPath {
		t.Errorf("apply landed at %s but preview promised %s", landing.Path, resp.TargetPath)
	}
	if landing.Path != f.libCopy.FilePath {
		t.Errorf("apply moved the copy from %s to %s; it was already at its target", f.libCopy.FilePath, landing.Path)
	}
	if n := spy.count(); n != 0 {
		t.Errorf("OnCollision fired %d time(s); want 0", n)
	}
	if _, err := os.Stat(f.original.FilePath); err != nil {
		t.Errorf("protected original was touched: %v", err)
	}
}

func TestResolveOrganizeSubject_FallsBackToTheBook(t *testing.T) {
	b := &database.Book{ID: "b"}
	if got := ResolveOrganizeSubject(nil, b); got != b {
		t.Errorf("nil resolver: got %v", got)
	}
	noCopy := func(*database.Book) (*database.Book, bool) { return nil, false }
	if got := ResolveOrganizeSubject(noCopy, b); got != b {
		t.Errorf("protected with no copy must organize the original (which makes one); got %v", got)
	}
}

// library.organize batch: a book whose own library copy already exists is a
// skip (counted under library_copy_exists), not a failure.
func TestOrganizeBooks_OwnLibraryCopyCountsAsSkipped(t *testing.T) {
	f := newLibraryCopyFixture(t, "vg-1")
	stats := f.svc.organizeBooks(context.Background(), []database.Book{*f.original}, nil, &noopLogger{}, "")
	if stats.Failed != 0 || stats.Skipped != 1 {
		t.Errorf("stats failed=%d skipped=%d; want failed=0 skipped=1", stats.Failed, stats.Skipped)
	}
	if stats.Collisions[CollisionLibraryCopyExists] != 1 {
		t.Errorf("collisions = %v; want %s=1", stats.Collisions, CollisionLibraryCopyExists)
	}
}

func TestIsLibraryCopyOf(t *testing.T) {
	s := func(v string) *string { return &v }
	orig := &database.Book{ID: "o", FileHash: s("h-orig"), OriginalFileHash: s("h-src")}
	cases := []struct {
		name string
		cand *database.Book
		want bool
	}{
		{"same file hash", &database.Book{FileHash: s("h-orig")}, true},
		{"file hash diverged after a tag write, original hash kept", &database.Book{FileHash: s("h-new"), OriginalFileHash: s("h-orig")}, true},
		{"organized hash is the original's content", &database.Book{FileHash: s("h-new"), OrganizedFileHash: s("h-orig")}, true},
		{"matches the original's own original hash", &database.Book{FileHash: s("h-new"), OriginalFileHash: s("h-src")}, true},
		{"different edition", &database.Book{FileHash: s("h-mp3"), OriginalFileHash: s("h-mp3"), OrganizedFileHash: s("h-mp3")}, false},
		{"no hashes", &database.Book{}, false},
		{"empty strings never match", &database.Book{FileHash: s("")}, false},
	}
	for _, tc := range cases {
		if got := IsLibraryCopyOf(orig, tc.cand); got != tc.want {
			t.Errorf("%s: IsLibraryCopyOf = %v; want %v", tc.name, got, tc.want)
		}
	}
	if IsLibraryCopyOf(&database.Book{FileHash: s("")}, &database.Book{FileHash: s("")}) {
		t.Error("two empty hashes matched")
	}
}
