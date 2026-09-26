// file: internal/foldercover/foldercover_test.go
// version: 1.0.0
// guid: 8e3f1a52-4b6d-4c97-a0e8-6f2d9b7c3a15
// last-edited: 2026-09-26

package foldercover

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeStore struct {
	mu        sync.Mutex
	books     map[string]*database.Book
	files     map[string][]database.BookFile
	states    map[string][]database.MetadataFieldState
	statesErr error
	// beforeWrite runs inside ModifyBook before fn, to simulate a racing writer.
	beforeWrite func(*database.Book)
	writes      int
}

func (f *fakeStore) GetBookFiles(id string) ([]database.BookFile, error) { return f.files[id], nil }
func (f *fakeStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	return f.states[id], f.statesErr
}
func (f *fakeStore) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }
func (f *fakeStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.books[id]
	if b == nil {
		return nil, nil
	}
	cp := *b
	if f.beforeWrite != nil {
		f.beforeWrite(&cp)
	}
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return b, nil
		}
		return nil, err
	}
	f.writes++
	*b = cp
	return b, nil
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubEmbedded replaces the embedded-picture reader for one test.
func stubEmbedded(t *testing.T, data []byte) {
	t.Helper()
	orig := embeddedPicture
	embeddedPicture = func(string) ([]byte, error) { return data, nil }
	t.Cleanup(func() { embeddedPicture = orig })
}

// setup makes a book folder with one audio file and a cover.png.
func setup(t *testing.T) (*fakeStore, *database.Book, string, string) {
	t.Helper()
	root := t.TempDir()
	dir := t.TempDir()
	audio := filepath.Join(dir, "Book.m4b")
	if err := os.WriteFile(audio, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(dir, "cover.png"), 400, 400)
	book := &database.Book{ID: "b1", Title: "Book", FilePath: audio}
	st := &fakeStore{
		books: map[string]*database.Book{"b1": book},
		files: map[string][]database.BookFile{"b1": {{ID: "f1", BookID: "b1", FilePath: audio}}},
	}
	stubEmbedded(t, nil)
	return st, book, root, dir
}

func TestApplySetsFolderCover(t *testing.T) {
	st, book, root, _ := setup(t)
	plan := Apply(st, book, root)
	if plan.Outcome != OutcomeApplied || plan.Err != nil {
		t.Fatalf("Apply = %s %v", plan.Outcome, plan.Err)
	}
	if book.CoverURL == nil || !strings.HasPrefix(*book.CoverURL, "/api/v1/covers/local/") || !strings.HasSuffix(*book.CoverURL, ".png") {
		t.Fatalf("cover_url = %v", book.CoverURL)
	}
	name := strings.TrimPrefix(*book.CoverURL, "/api/v1/covers/local/")
	if _, err := os.Stat(filepath.Join(root, ".covers", name)); err != nil {
		t.Fatalf("stored cover missing: %v", err)
	}
}

func TestEvaluateWritesNothing(t *testing.T) {
	st, book, root, _ := setup(t)
	plan := Evaluate(st, book, root)
	if plan.Outcome != OutcomeWouldApply || plan.Candidate == nil {
		t.Fatalf("Evaluate = %s", plan.Outcome)
	}
	if st.writes != 0 || book.CoverURL != nil {
		t.Fatal("Evaluate wrote the book")
	}
	if _, err := os.Stat(filepath.Join(root, ".covers")); !os.IsNotExist(err) {
		t.Fatalf("Evaluate created .covers: %v", err)
	}
}

func TestNeverOverridesExistingCover(t *testing.T) {
	st, book, root, _ := setup(t)
	user := "https://example.invalid/user-picked.jpg"
	book.CoverURL = &user
	if plan := Apply(st, book, root); plan.Outcome != OutcomeHasCover {
		t.Fatalf("outcome = %s, want has_cover", plan.Outcome)
	}
	if *book.CoverURL != user || st.writes != 0 {
		t.Fatal("existing cover was overwritten")
	}
}

func TestNeverOverridesIDNamedCover(t *testing.T) {
	st, book, root, _ := setup(t)
	if err := os.MkdirAll(filepath.Join(root, "covers"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(root, "covers", "b1.png"), 300, 300)
	if plan := Apply(st, book, root); plan.Outcome != OutcomeHasCover || st.writes != 0 {
		t.Fatalf("outcome = %s writes=%d, want has_cover and no write", plan.Outcome, st.writes)
	}
}

func TestLockedCoverIsNeverTouched(t *testing.T) {
	st, book, root, _ := setup(t)
	st.states = map[string][]database.MetadataFieldState{
		"b1": {{BookID: "b1", Field: database.FieldKeyCoverURL, OverrideLocked: true}},
	}
	if plan := Apply(st, book, root); plan.Outcome != OutcomeLocked || st.writes != 0 {
		t.Fatalf("outcome = %s writes=%d, want locked and no write", plan.Outcome, st.writes)
	}
}

func TestUnreadableLocksFailClosed(t *testing.T) {
	st, book, root, _ := setup(t)
	st.statesErr = errors.New("boom")
	plan := Apply(st, book, root)
	if plan.Outcome != OutcomeLocksUnavailable || st.writes != 0 || plan.Err == nil {
		t.Fatalf("outcome = %s writes=%d err=%v", plan.Outcome, st.writes, plan.Err)
	}
}

func TestEmbeddedCoverWins(t *testing.T) {
	st, book, root, _ := setup(t)
	stubEmbedded(t, []byte{0xFF, 0xD8})
	if plan := Apply(st, book, root); plan.Outcome != OutcomeHasEmbedded || st.writes != 0 {
		t.Fatalf("outcome = %s writes=%d", plan.Outcome, st.writes)
	}
}

func TestRacedCoverIsNotOverwritten(t *testing.T) {
	st, book, root, _ := setup(t)
	other := "/api/v1/covers/local/other.jpg"
	st.beforeWrite = func(b *database.Book) { b.CoverURL = &other }
	plan := Apply(st, book, root)
	if plan.Outcome != OutcomeRaced || st.writes != 0 || book.CoverURL != nil {
		t.Fatalf("outcome = %s writes=%d cover=%v", plan.Outcome, st.writes, book.CoverURL)
	}
}

func TestSharedFolderNeedsStemMatch(t *testing.T) {
	st, book, root, dir := setup(t)
	// Another book's audio in the same folder: cover.png is ambiguous.
	if err := os.WriteFile(filepath.Join(dir, "Other.mp3"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if plan := Evaluate(st, book, root); plan.Outcome != OutcomeNoCandidate {
		t.Fatalf("shared folder: outcome = %s, want no_candidate", plan.Outcome)
	}
	writePNG(t, filepath.Join(dir, "BOOK.png"), 300, 300)
	plan := Evaluate(st, book, root)
	if plan.Outcome != OutcomeWouldApply || filepath.Base(plan.Candidate.Path) != "BOOK.png" {
		t.Fatalf("stem match: %s %+v", plan.Outcome, plan.Candidate)
	}
}

func TestDiscSubfoldersSearchParent(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	var files []database.BookFile
	for _, d := range []string{"Disc 1", "Disc 2"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, d, "01.mp3")
		if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, database.BookFile{ID: d, BookID: "b2", FilePath: p})
	}
	writePNG(t, filepath.Join(dir, "Folder.PNG"), 500, 500)
	book := &database.Book{ID: "b2", FilePath: dir}
	st := &fakeStore{books: map[string]*database.Book{"b2": book}, files: map[string][]database.BookFile{"b2": files}}
	stubEmbedded(t, nil)
	plan := Evaluate(st, book, root)
	if plan.Outcome != OutcomeWouldApply || filepath.Base(plan.Candidate.Path) != "Folder.PNG" {
		t.Fatalf("disc subfolders: %s %+v", plan.Outcome, plan.Candidate)
	}
}
