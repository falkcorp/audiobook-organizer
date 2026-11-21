// file: internal/arch/viper_gone_test.go
// version: 0.1.0
// guid: 3f8a2c61-7d4e-4b19-9e05-c2a6d8b1f473
// last-edited: 2026-11-20

package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// viperImport is the config library HEAD's settings load read through. After
// the config flip (F3-07) core config's layers are the only source of a
// setting: a value read through viper would come from HEAD's flat names that
// config check reports as retired, or, with nothing populating the global
// viper any more, silently be the zero value. No production file of the root
// module imports it.
const viperImport = "github.com/spf13/viper"

// viperTestOracles are the only files allowed to import viper: tests that
// hold M2's reading of HEAD's config.yaml against viper's own decoding, the
// way HEAD read that file. The cutover still imports the file once, so the
// oracle is the evidence that the import keeps HEAD's values; it is test-only,
// so viper stays in go.mod as a test dependency.
var viperTestOracles = map[string]string{
	"internal/books/cutover/head_gapfill_viper_test.go":             "M2 gap-fill differential against viper's cast of HEAD's config.yaml",
	"internal/books/cutover/config_cutover_unrepresentable_test.go": "M2 refuses what viper would have read differently",
}

// TestNoViperImports fails while a Go file of the root module other than the
// test oracles imports viper, naming each file, and while an oracle no
// longer does (so the allowance cannot outlive its reason).
func TestNoViperImports(t *testing.T) {
	root := repoRootForViper(t)
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			switch {
			case rel == "core", rel == "web": // their own modules
				return filepath.SkipDir
			case d.Name() == ".git", d.Name() == "node_modules", d.Name() == "vendor", d.Name() == ".worktrees":
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil { // a nested module
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == viperImport {
				rel, _ := filepath.Rel(root, path)
				found = append(found, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	var bad []string
	for _, f := range found {
		if _, ok := viperTestOracles[f]; !ok {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		t.Errorf("%d files import %s; read the setting through core config (the config.Facade projection, or the module's own section) instead:\n%s",
			len(bad), viperImport, strings.Join(bad, "\n"))
	}
	for f, why := range viperTestOracles {
		if !slices.Contains(found, f) {
			t.Errorf("%s is allowed to import viper (%s) but no longer does: remove it from viperTestOracles", f, why)
		}
	}
}

// repoRootForViper is the root module's directory: internal/arch's parent's
// parent, from the working directory go test runs the package in (CI builds
// with -trimpath, so runtime.Caller paths are not usable).
func repoRootForViper(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
