// file: internal/versionprimary/signals_test.go
// version: 1.0.0
// guid: 6867c1e8-4791-4a48-a289-aeb5326fa25b
// last-edited: 2026-09-24

package versionprimary

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type stubFiles map[string][]database.BookFile

func (s stubFiles) GetBookFiles(id string) ([]database.BookFile, error) { return s[id], nil }

type stubChapters map[string]int

func (s stubChapters) GetChaptersForBook(id string) ([]database.Chapter, error) {
	return make([]database.Chapter, s[id]), nil
}

func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoader_EligibilityInputsComeFromActiveBookFiles(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	good := touch(t, filepath.Join(root, "Author", "Book", "Book.m4b"))
	ua := touch(t, filepath.Join(root, "Unknown Author", "Book", "Book.m4b"))
	outside := touch(t, filepath.Join(other, "Book.m4b"))
	files := stubFiles{
		// Book.FilePath points nowhere; the active row is what counts.
		"GOOD": {{BookID: "GOOD", FilePath: good, BitrateKbps: 64}, {BookID: "GOOD", FilePath: filepath.Join(root, "gone.m4b"), Missing: true}},
		"UA":   {{BookID: "UA", FilePath: ua}},
		"OUT":  {{BookID: "OUT", FilePath: outside}},
		"MISS": {{BookID: "MISS", FilePath: filepath.Join(root, "Author", "missing.m4b")}},
		"NONE": {{BookID: "NONE", FilePath: good, Missing: true}},
	}
	probed := 0
	l := Loader{Files: files, Chapters: stubChapters{}, RootDir: root,
		Probe: func(context.Context, string) (int, error) { probed++; return 5, nil }}

	load := func(id string) Signals {
		t.Helper()
		s, err := l.Load(context.Background(), &database.Book{ID: id, FilePath: "/stale/path.m4b"}, true)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := load("GOOD")
	if s.ActiveFiles != 1 || s.FilesMissingOnDisk != 0 || !s.AllUnderRoot || !s.SingleM4B ||
		s.Chapters != 5 || s.ChapterSource != ChapterSourceProbe || s.BitrateKbps != 64 || s.UnknownAuthorPath {
		t.Fatalf("GOOD = %+v", s)
	}
	if s := load("UA"); !s.UnknownAuthorPath || !s.AllUnderRoot {
		t.Fatalf("UA = %+v", s)
	}
	if s := load("OUT"); s.AllUnderRoot {
		t.Fatalf("OUT = %+v", s)
	}
	if s := load("MISS"); s.FilesMissingOnDisk != 1 || Tier(s) != TierNone {
		t.Fatalf("MISS = %+v", s)
	}
	if s := load("NONE"); s.ActiveFiles != 0 || s.AllUnderRoot {
		t.Fatalf("NONE = %+v", s)
	}
	if probed != 3 { // GOOD, UA, OUT; never MISS or NONE
		t.Fatalf("probed %d files, want 3", probed)
	}
}

func TestLoader_ChapterSourceFallsBackToTableThenUnknown(t *testing.T) {
	root := t.TempDir()
	p := touch(t, filepath.Join(root, "A", "B.m4b"))
	files := stubFiles{"T": {{FilePath: p}}, "U": {{FilePath: p}}}
	l := Loader{Files: files, Chapters: stubChapters{"T": 7}, RootDir: root,
		Probe: func(context.Context, string) (int, error) { return 0, errors.New("ffprobe failed") }}
	s, err := l.Load(context.Background(), &database.Book{ID: "T"}, true)
	if err != nil || s.ChapterSource != ChapterSourceTable || s.Chapters != 7 || Tier(s) != TierM4BChapters {
		t.Fatalf("T = %+v err=%v", s, err)
	}
	s, err = l.Load(context.Background(), &database.Book{ID: "U"}, true)
	if err != nil || s.ChapterSource != ChapterSourceUnknown || Tier(s) != TierM4BNoChapters {
		t.Fatalf("U = %+v err=%v", s, err)
	}
	// A nil prober behaves like a failed probe.
	l.Probe = nil
	if s, _ := l.Load(context.Background(), &database.Book{ID: "T"}, true); s.ChapterSource != ChapterSourceTable {
		t.Fatalf("nil prober: %+v", s)
	}
}

func TestLoader_EmptyRootMakesNothingEligible(t *testing.T) {
	root := t.TempDir()
	p := touch(t, filepath.Join(root, "A.m4b"))
	l := Loader{Files: stubFiles{"X": {{FilePath: p}}}, Chapters: stubChapters{}}
	s, err := l.Load(context.Background(), &database.Book{ID: "X"}, true)
	if err != nil || s.AllUnderRoot {
		t.Fatalf("empty root: %+v err=%v", s, err)
	}
}
