// file: internal/remux/protected_skip_test.go
// version: 1.0.0
// guid: 6c1f0a93-2d7e-4b58-9e14-a83b5d27c0f6
// last-edited: 2026-09-14

package remux

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prefixChecker protects every path under one directory, the shape of
// deluge.ProtectedPathCache for a single save_path.
type prefixChecker struct{ dir string }

func (p prefixChecker) IsProtected(path string) bool {
	return strings.HasPrefix(path, p.dir+string(os.PathSeparator))
}

// A protected directory under RootDir (a Deluge save_path, the iTunes
// library) is walked like any other, and before 2026-09-14 both passes probed
// its files and ran ffmpeg on every one taglib could not read, renaming the
// output over the original: a seeding torrent's file rewritten in place.
//
// The observable is the counters, not the bytes. A candidate must be a file
// taglib cannot read, and every file ffmpeg can read taglib reads too (it
// sniffs content: MP3 and ADTS under a .m4b name both parse), so ffmpeg fails
// on any usable fixture and leaves it byte-identical with or without the
// guard. The counters do discriminate: the guard runs before the probe, so a
// protected file is reported skipped_protected and never reaches ffmpeg,
// while without the guard it is probed, handed to ffmpeg, and counted failed.
func TestMalformedPasses_SkipProtectedFiles(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	passes := []struct {
		name string
		run  func(c ProtectedChecker, progress func(int, int, string)) error
	}{
		{"remux", func(c ProtectedChecker, progress func(int, int, string)) error {
			r := New(&MockStore{})
			r.SetProtectedChecker(c)
			return r.RemuxMalformedFiles(context.Background(), progress)
		}},
		{"transcode", func(c ProtectedChecker, progress func(int, int, string)) error {
			tr := NewTranscoder(&MockStore{})
			tr.SetProtectedChecker(c)
			return tr.TranscodeMalformedFiles(context.Background(), progress)
		}},
	}
	for _, p := range passes {
		t.Run(p.name, func(t *testing.T) {
			root := t.TempDir()
			withRemuxAppDirs(t, root, true)
			seeding := filepath.Join(root, "deluge-seeding")
			protectedFile := filepath.Join(seeding, "Book", "book.m4b")
			libraryFile := filepath.Join(root, "Author", "Book", "book.m4b")
			content := []byte("not a real m4b")
			for _, f := range []string{protectedFile, libraryFile} {
				if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f, content, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			var lastProcessed, lastTotal int
			var lastMsg string
			if err := p.run(prefixChecker{dir: seeding}, func(processed, total int, msg string) {
				lastProcessed, lastTotal, lastMsg = processed, total, msg
			}); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}

			if !strings.Contains(lastMsg, "skipped_protected=1") || !strings.Contains(lastMsg, "failed=1 ") {
				t.Errorf("progress %q: want skipped_protected=1 and failed=1 (only the library file reaches ffmpeg)", lastMsg)
			}
			if lastTotal != 2 || lastProcessed != lastTotal {
				t.Errorf("processed=%d total=%d, want 2/2: the two walks must agree", lastProcessed, lastTotal)
			}
			got, err := os.ReadFile(protectedFile)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("protected file %s changed", protectedFile)
			}
		})
	}
}
