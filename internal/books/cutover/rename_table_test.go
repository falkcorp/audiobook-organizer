// file: internal/books/cutover/rename_table_test.go
// version: 1.0.0
// guid: 3b8c8f9e-e2d4-4c55-8d7b-6a0f0e1f8a52
// last-edited: 2026-11-11

package cutover

import (
	"flag"
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

var update = flag.Bool("update", false, "rewrite testdata/rename_table.golden")

const goldenPath = "testdata/rename_table.golden"

// The golden carries the repo's version header, which the generator emits:
// bump goldenVersion and goldenEdited with every change to the table, and
// regenerate with -update.
const (
	goldenVersion = "1.0.0"
	goldenGUID    = "7024f723-334e-4210-95d6-bcd11fc9cd7d"
	goldenEdited  = "2026-11-11"
)

// repoRoot is the module root, found from the test's working directory (the
// package directory); os.Getwd, not runtime.Caller, because CI builds with
// -trimpath.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "books")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no module root above the test's directory")
		}
		dir = parent
	}
}

// getenvScan lists the os.Getenv and os.LookupEnv literals outside the
// packages that own env names (05-config §1.12.4 "Not config at all"): core/,
// internal/config/ and this package, tests and the web tree excluded. A call
// whose argument is a constant is resolved by the constant's name across the
// repo; a call whose argument is not a constant at all is reported by file.
func getenvScan(t testing.TB, root string) (literals, dynamic []string) {
	t.Helper()
	skipDir := map[string]bool{
		".git": true, ".worktrees": true, "node_modules": true, "web": true, "vendor": true, ".standards": true,
		"core": true, filepath.Join("internal", "config"): true, filepath.Join("internal", "books", "cutover"): true,
	}
	type parsed struct {
		rel  string
		file *ast.File
	}
	var files []parsed
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel != "." && (skipDir[rel] || skipDir[d.Name()] && !strings.Contains(rel, string(filepath.Separator))) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // a file that does not parse is the compiler's to report
		}
		files = append(files, parsed{rel: rel, file: f})
		return nil
	})
	if err != nil {
		t.Fatalf("scan for os.Getenv literals: %v", err)
	}
	consts := map[string][]string{}
	for _, p := range files {
		for _, decl := range p.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							consts[name.Name] = append(consts[name.Name], s)
						}
					}
				}
			}
		}
	}
	litSet, dynSet := map[string]bool{}, map[string]bool{}
	for _, p := range files {
		ast.Inspect(p.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv") {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
				return true
			}
			switch a := call.Args[0].(type) {
			case *ast.BasicLit:
				if s, err := strconv.Unquote(a.Value); err == nil {
					litSet[s] = true
				}
			case *ast.Ident:
				resolve(consts, a.Name, litSet, dynSet, p.rel)
			case *ast.SelectorExpr:
				resolve(consts, a.Sel.Name, litSet, dynSet, p.rel)
			default:
				dynSet[p.rel] = true
			}
			return true
		})
	}
	for s := range litSet {
		literals = append(literals, s)
	}
	for s := range dynSet {
		dynamic = append(dynamic, s)
	}
	sort.Strings(literals)
	sort.Strings(dynamic)
	return literals, dynamic
}

func resolve(consts map[string][]string, name string, lits, dyn map[string]bool, rel string) {
	vals, ok := consts[name]
	if !ok {
		dyn[rel] = true
		return
	}
	for _, v := range vals {
		lits[v] = true
	}
}

// FormatTable renders the table as the golden's text.
func formatTable(t testing.TB, tab *RenameTable, literals, dynamic []string) string {
	t.Helper()
	var b strings.Builder
	counts := map[Class]int{}
	top, twoTarget, subs, virt, removed, process := 0, 0, 0, 0, 0, 0
	for _, r := range tab.Rows {
		switch {
		case r.Top:
			top++
			counts[r.Class]++
			if len(r.Targets) == 2 {
				twoTarget++
			}
		case r.Removed:
			removed++
		case r.Process:
			process++
		case strings.ContainsAny(r.Head, "[]<>"):
			virt++
		default:
			subs++
		}
	}
	fmt.Fprintf(&b, "# file: internal/books/cutover/%s\n# version: %s\n# guid: %s\n# last-edited: %s\n#\n", goldenPath, goldenVersion, goldenGUID, goldenEdited)
	fmt.Fprintln(&b, "# Rename table (05-config §1.12.3), generated from the frozen HEAD Config by rename_table.go.")
	fmt.Fprintln(&b, "# Regenerate: go test ./internal/books/cutover -run TestRenameTable_Golden -update")
	fmt.Fprintf(&b, "# top-level rows: %d (k=%d s=%d i=%d o=%d x=%d); rows with two targets: %d\n", top,
		counts[ClassKey], counts[ClassSecret], counts[ClassInput], counts[ClassEnv], counts[ClassDropped], twoTarget)
	fmt.Fprintf(&b, "# sub-key rows: %d; virtual rows: %d; removed-key rows: %d; process-name rows: %d; rows: %d\n", subs, virt, removed, process, len(tab.Rows))
	fmt.Fprintf(&b, "# os.Getenv literals outside core/, internal/config/ and this package (05-config §1.12.4 \"Not config at all\"): %d\n", len(literals))
	for _, l := range literals {
		fmt.Fprintf(&b, "#   getenv %s\n", l)
	}
	for _, d := range dynamic {
		fmt.Fprintf(&b, "#   getenv with a non-constant name in %s\n", d)
	}
	for _, r := range tab.Rows {
		fmt.Fprintf(&b, "\n## %s\n", r.Head)
		fmt.Fprintf(&b, "top=%v stored=%s class=%s blob=%s admin=%v fail-closed=%v url=%v\n", r.Top || r.Removed || r.Process, r.Stored, r.Class, dash(r.BlobField), r.Admin, r.FailClosed, r.URL)
		for _, tg := range r.Targets {
			flags := ""
			for _, f := range []struct {
				on   bool
				name string
			}{{tg.Coined, " coined"}, {tg.Dest, " destination"}, {tg.Passthrough, " passthrough"}, {tg.Shared, " shared"}} {
				if f.on {
					flags += f.name
				}
			}
			env := "-"
			if p := tg.ConfigPath(); p != "" && r.Class != ClassDropped {
				env = CanonicalEnv(p)
			}
			list := ""
			if tg.List != "" {
				list = " list=" + tg.List
			}
			fmt.Fprintf(&b, "target %s %s convert=%s%s%s env=%s\n", tg.Kind, tg.Path, dash(tg.Convert), list, flags, env)
		}
		if r.Note != "" && !r.Removed {
			fmt.Fprintf(&b, "note %s\n", r.Note)
		}
		for _, e := range r.Retired {
			kind := "env"
			if e.File {
				kind = "file"
			}
			msg := ""
			if e.Message != "" {
				msg = " message=" + strconv.Quote(truncate(e.Message, 56))
			}
			rf := ""
			if e.Refuse {
				rf = " Refuse"
			}
			fmt.Fprintf(&b, "retired %s %s -> %s %s%s%s\n", kind, e.Name, dash(string(e.Canonical)), e.Verdict, rf, msg)
		}
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestRenameTable_Golden(t *testing.T) {
	literals, dynamic := getenvScan(t, repoRoot(t))
	got := formatTable(t, Table(), literals, dynamic)
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to create it)", err)
	}
	if string(want) != got {
		t.Fatalf("the rename table differs from %s (an os.Getenv literal outside internal/config that is not listed is the usual cause: decide whether it becomes a config key in the flip or stays its reader's own variable, 05-config §1.12.4, then regenerate with -update):\n%s", goldenPath, firstDiff(string(want), got))
	}
}

// firstDiff names the first differing line of two texts.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  golden: %q\n  actual: %q", i+1, wl, gl)
		}
	}
	return "(no line differs)"
}
