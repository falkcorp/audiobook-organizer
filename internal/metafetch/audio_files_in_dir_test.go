// file: internal/metafetch/audio_files_in_dir_test.go
// version: 1.0.0
// guid: aead375c-cb1f-492f-893a-1fbd5d6ae32a
// last-edited: 2026-09-01

package metafetch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// The old implementation globbed a private 8-pattern list. Every assertion
// below names a file it could not find.
func TestAudioFilesInDirFindsTheConfiguredExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"Book.aax",  // configured, absent from the old glob list
		"Book.aaxc", // ditto
		"Book.aiff", // ditto
		"Book.mka",  // ditto
		"Book.oga",  // ditto
		"Book.wav",  // ditto
		"Book.mp3",  // was already found
		"cover.jpg", // never audio
	)

	got := baseNames(AudioFilesInDir(dir))
	for _, want := range []string{"Book.aax", "Book.aaxc", "Book.aiff", "Book.mka", "Book.oga", "Book.wav", "Book.mp3"} {
		if !contains(got, want) {
			t.Errorf("AudioFilesInDir did not return %q (got %v)", want, got)
		}
	}
	if contains(got, "cover.jpg") {
		t.Errorf("AudioFilesInDir returned a non-audio file (got %v)", got)
	}
}

// filepath.Glob is case-sensitive on Linux — production — so a file named
// with an uppercase extension was invisible to the old implementation. It is
// NOT invisible on macOS, where a case-insensitive filesystem hid the bug from
// every local test run.
func TestAudioFilesInDirIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "Chapter 01.MP3", "Chapter 02.AAX")
	got := baseNames(AudioFilesInDir(dir))
	if len(got) != 2 {
		t.Fatalf("expected both uppercase-extension files, got %v", got)
	}
}

// A directory whose name carries a glob metacharacter made every one of the
// old Glob patterns match nothing, so the book looked like it had no audio at
// all. "[Unabridged]" is a real folder shape in this library.
func TestAudioFilesInDirHandlesGlobMetacharactersInTheDirName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "The Hobbit [Unabridged]")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, "Book.m4b")
	if got := AudioFilesInDir(dir); len(got) != 1 {
		t.Fatalf("expected 1 file under a bracketed directory name, got %v", got)
	}
}

func TestAudioFilesInDirSkipsSubdirectoriesAndMissingDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Disc 1.mp3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := AudioFilesInDir(dir); len(got) != 0 {
		t.Fatalf("a directory named like an audio file was returned: %v", got)
	}
	if got := AudioFilesInDir(filepath.Join(dir, "nope")); got != nil {
		t.Fatalf("expected nil for a missing directory, got %v", got)
	}
}

func TestAudioFilesInDirNarrowsWithTheConfig(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.SupportedExtensions = []string{".mp3"} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	dir := t.TempDir()
	writeFiles(t, dir, "Book.mp3", "Book.aax")
	got := baseNames(AudioFilesInDir(dir))
	if len(got) != 1 || got[0] != "Book.mp3" {
		t.Fatalf("expected only the configured .mp3, got %v", got)
	}
}
