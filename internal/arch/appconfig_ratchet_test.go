// file: internal/arch/appconfig_ratchet_test.go
// version: 1.0.0
// guid: c41e7a92-6d05-4b38-8f1c-2a9d70e5b643
// last-edited: 2026-11-20

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// appConfigImport is the façade package whose package-level accessors the ratchet
// counts.
const appConfigImport = "github.com/falkcorp/audiobook-organizer/internal/config"

// appConfigAccessors are the package-level names the config façade keeps for the
// code that reads or writes settings without an injected accessor
// (05-config 1.12.1 item 3): config.AppConfig, config.Snapshot, config.Mutate.
// MutateChecked is the same write with its refusal returned and is not counted: a
// package that moves a Mutate to it is going the right way, and the ratchet must
// not fail that change.
var appConfigAccessors = map[string]bool{"AppConfig": true, "Snapshot": true, "Mutate": true}

// appConfigRatchet is, per package directory (relative to the module root, "." for
// main), the number of uses of config.AppConfig, config.Snapshot and config.Mutate
// in its non-test files on 2026-11-20, the day the config flip made them a façade.
// internal/config itself (the façade) is not counted.
//
// The ceilings may only go DOWN, like internal/logger's slogRatchet: the test does
// not fail when a count improves, so lowering a ceiling by hand in the PR that
// lowers the count is the rule. Never add an entry and never raise one. A package
// moves to an injected accessor when it is touched anyway; when the map is empty
// the façade, AppConfig, Mutate and Snapshot are deleted.
// APPCONFIG_RATCHET_PRINT=1 go test ./internal/arch -run TestAppConfigRatchet -v
// prints the current map.
var appConfigRatchet = map[string]int{
	"cmd":                                 53,
	"internal/ai":                         6,
	"internal/appdirs":                    1,
	"internal/audiobooks":                 29,
	"internal/batch":                      2,
	"internal/books":                      12,
	"internal/dedup":                      11,
	"internal/deluge":                     4,
	"internal/fileops":                    2,
	"internal/importer":                   3,
	"internal/itunes":                     3,
	"internal/itunes/service":             2,
	"internal/maintenance/jobs":           19,
	"internal/merge":                      5,
	"internal/metabatch":                  3,
	"internal/metadata":                   6,
	"internal/metafetch":                  64,
	"internal/organizer":                  27,
	"internal/plugins/acoustid":           13,
	"internal/plugins/dedup":              10,
	"internal/plugins/deluge":             4,
	"internal/plugins/maintenance":        38,
	"internal/plugins/metafetch":          1,
	"internal/reconcile":                  8,
	"internal/remux":                      2,
	"internal/scanner":                    35,
	"internal/scheduler":                  78,
	"internal/search":                     1,
	"internal/server":                     173,
	"internal/server/handlers":            25,
	"internal/server/handlers/aibackends": 2,
	"internal/server/handlers/audiobooks": 4,
	"internal/server/handlers/dedup":      3,
	"internal/server/handlers/metadata":   3,
	"internal/server/handlers/operations": 1,
	"internal/server/handlers/repairs":    1,
	"internal/server/handlers/system":     17,
	"internal/sysinfo":                    2,
	"internal/tagger":                     2,
	"internal/testutil":                   3,
	"internal/transcribe":                 7,
	"internal/updater":                    5,
	"internal/usermerge":                  2,
	"internal/versions":                   1,
}

// configLocalName is the name the file imports the config façade under, or ""
// when it does not import it (or imports it for side effects or by dot).
func configLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p != appConfigImport {
			continue
		}
		name := "config"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			return ""
		}
		return name
	}
	return ""
}

// countAccessorUses is the counted accessors' uses in f.
func countAccessorUses(f *ast.File) int {
	local := configLocalName(f)
	if local == "" {
		return 0
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && appConfigAccessors[sel.Sel.Name] {
				n++
			}
		}
		return true
	})
	return n
}

// countAppConfigUses returns the uses of the counted accessors per package
// directory under the module root.
func countAppConfigUses(t *testing.T) map[string]int {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	counts := map[string]int{}
	fset := token.NewFileSet()
	walk := func(dir string) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "testdata", "vendor", ".worktrees":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			pkg := filepath.ToSlash(filepath.Dir(rel))
			if pkg == "internal/config" {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			if n := countAccessorUses(f); n > 0 {
				counts[pkg] += n
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		walk(dir)
	}
	// main.go at the module root.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(root, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if n := countAccessorUses(f); n > 0 {
			counts["."] += n
		}
	}
	return counts
}

// TestAppConfigRatchet (05-config 1.12.1 item 3): no package uses config.AppConfig,
// config.Snapshot or config.Mutate more than its ceiling, and no package outside
// the map uses them at all. New code reads settings through the injected
// accessor, or through MutateChecked where it must write.
func TestAppConfigRatchet(t *testing.T) {
	counts := countAppConfigUses(t)
	if os.Getenv("APPCONFIG_RATCHET_PRINT") == "1" {
		var pkgs []string
		for p := range counts {
			pkgs = append(pkgs, p)
		}
		sort.Strings(pkgs)
		for _, p := range pkgs {
			t.Logf("\t%q: %d,", p, counts[p])
		}
	}
	var pkgs []string
	for p := range counts {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		ceiling, listed := appConfigRatchet[p]
		switch {
		case !listed:
			t.Errorf("%s uses config.AppConfig/Snapshot/Mutate %d time(s) and is not in the ratchet: take the setting through an injected accessor (never add an entry)", p, counts[p])
		case counts[p] > ceiling:
			t.Errorf("%s uses config.AppConfig/Snapshot/Mutate %d times, above its ceiling %d: the ceiling may only go down", p, counts[p], ceiling)
		}
	}
	for p, ceiling := range appConfigRatchet {
		if counts[p] == 0 {
			t.Errorf("%s is in the ratchet (ceiling %d) but no longer uses the accessors: delete the entry", p, ceiling)
		}
	}
}
