// file: internal/arch/production_go_files_test.go
// version: 0.1.0
// guid: 4402482e-bb4c-4cf0-8777-9a795f373417
// last-edited: 2026-11-22

package arch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// productionGoFiles is every non-test Go file of the module, path relative to the
// repository root (slash-separated) to source: the input of the guards that read
// the syntax of the whole tree.
func productionGoFiles(t *testing.T) map[string][]byte {
	t.Helper()
	root := repoRoot(t)
	skip := map[string]bool{
		".git": true, "node_modules": true, "web": true, ".worktrees": true,
		"third_party": true, ".standards": true, "vendor": true, "testdata": true,
	}
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
