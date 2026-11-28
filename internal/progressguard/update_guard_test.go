// file: internal/progressguard/update_guard_test.go
// version: 0.1.0
// guid: b7d41c8e-2f95-4a3d-9e60-5c1a8f3d27b4
// last-edited: 2026-11-27

package progressguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoSetThenNote: a kind that reports a position and a status line calls
// rc.Progress().Update once. Set followed by Note is two updates, and the
// executor throttles progress writes, so the Set's due tick restarts the window
// and the note reaches the stream only at the window's end (F5-18 P1 round 2).
// The scan reads the repo's non-test Go sources, so a kind added later and a
// closure reverted to the pair both fail here.
func TestNoSetThenNote(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("no go.mod above the test's directory")
		}
		root = parent
	}
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", ".worktrees", ".claude", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "Progress().Set(") {
				continue
			}
			for j := i + 1; j < len(lines) && j <= i+4; j++ {
				if strings.Contains(lines[j], "Progress().Note(") {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d: Progress().Set followed by Progress().Note at line %d; use Progress().Update", rel, i+1, j+1)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d Go files under %s: the walk is not seeing the repo", scanned, root)
	}
}
