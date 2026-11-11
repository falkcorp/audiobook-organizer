// file: core/archtest/testsupport.go
// version: 1.0.0
// guid: 36bf7c73-6249-49ca-b278-d8e11cfe3605
// last-edited: 2026-11-10

package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CoreTestSupport reports whether rel, a package path relative to the core
// module root, is one of 03 R4's test-support packages: coretest and archtest
// and their sub-packages, and every <pkg>/<pkg>test conformance suite (e.g.
// tasks/taskstest, storage/index/indextest). Only test binaries link them.
// The layering table (core/internal/archtest_layers_test.go) and the
// guards that exempt test-only code share this one rule.
func CoreTestSupport(rel string) bool {
	for _, root := range []string{"coretest", "archtest"} {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	if i := strings.LastIndex(rel, "/"); i > 0 {
		parent, last := rel[:i], rel[i+1:]
		return last == parent[strings.LastIndex(parent, "/")+1:]+"test"
	}
	return false
}

// moduleSourceFiles returns the absolute paths of every non-test Go file of
// the module rooted at root, whatever its build tags: it walks the
// directories, as the go tool lays out packages, and reads no build
// constraint. It skips testdata, vendor and node_modules directories, hidden
// and underscore directories, nested modules (a directory below root holding
// a go.mod), and every directory whose path relative to root testSupport
// reports (nil: none) along with its sub-directories.
func moduleSourceFiles(root string, testSupport func(rel string) bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if name == "testdata" || name == "vendor" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return fs.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return fs.SkipDir
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if testSupport != nil && testSupport(filepath.ToSlash(rel)) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// resolveRoot returns dir made absolute with its symlinks resolved.
func resolveRoot(dir string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}
