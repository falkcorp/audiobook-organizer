// file: core/audit/actions_test.go
// version: 1.0.0
// guid: 0f93b6a2-8c4d-4e17-a5d9-3e2b7c1f4a60
// last-edited: 2026-11-10

package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// coreActionGrammar is 05-rbac §7.1's grammar, written out here and not
// through ValidAction so the test checks the function against the spec.
func coreActionGrammar() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
}

// TestAuditAction_Grammar (05-rbac §7.1): the action table has the 64 core
// actions, sorted and unique, every one of them and every action named in an
// audit.Event literal under core/ matches <ns>.<noun>.<verb>, and ValidAction
// agrees with the spec's regexp.
func TestAuditAction_Grammar(t *testing.T) {
	re := coreActionGrammar()
	table := CoreActions()
	if len(table) != 64 {
		t.Errorf("CoreActions has %d actions, want the 64 of §7.1", len(table))
	}
	if !slices.IsSorted(table) {
		t.Error("CoreActions is not sorted")
	}
	if len(slices.Compact(slices.Clone(table))) != len(table) {
		t.Error("CoreActions repeats an action")
	}
	for _, a := range table {
		if !re.MatchString(a) || !ValidAction(a) {
			t.Errorf("table action %q breaks the grammar", a)
		}
		if !strings.HasPrefix(a, "core.") {
			t.Errorf("table action %q is outside the core namespace", a)
		}
	}
	// The table is a copy: a caller cannot change it.
	table[0] = "mutated"
	if CoreActions()[0] == "mutated" {
		t.Error("CoreActions returns its backing array")
	}

	// Every action literal of an Event in core/ (production code: the tests of
	// this package use invalid actions on purpose).
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if root, err = archtest.ModuleRoot(root); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, lit := range eventActionLiterals(t, root) {
		found++
		if !re.MatchString(lit.action) {
			t.Errorf("%s: Event action %q breaks the grammar", lit.pos, lit.action)
		}
		if strings.HasPrefix(lit.action, "core.") && !slices.Contains(CoreActions(), lit.action) {
			t.Errorf("%s: Event action %q is a core action missing from §7.1's table", lit.pos, lit.action)
		}
	}
	t.Logf("checked %d Event action literal(s) in core/", found)

	// ValidAction agrees with the regexp on the edge cases.
	for s, want := range map[string]bool{
		"core.auth.login": true, "books.user.merge": true, "a.b.c": true, "a1_.b_2.c3": true,
		"": false, "core": false, "core.auth": false, "core.auth.login.x": false, "Core.auth.login": false,
		"core.auth.": false, "core..login": false, "1core.auth.login": false, "core.auth.log-in": false,
		"core.auth.login ": false, "core.auth.lôgin": false, "_core.auth.login": false,
	} {
		if got := ValidAction(s); got != want || re.MatchString(s) != want {
			t.Errorf("ValidAction(%q) = %v, regexp %v, want %v", s, got, re.MatchString(s), want)
		}
	}
}

type actionLit struct{ action, pos string }

// eventActionLiterals parses the non-test Go files under root and returns the
// string literal of every `Action:` field of a composite literal of type
// Event or audit.Event.
func eventActionLiterals(t *testing.T, root string) []actionLit {
	t.Helper()
	var out []actionLit
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "testdata" || n == "vendor" || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !isEventType(cl.Type) {
				return true
			}
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Action" {
					continue
				}
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						rel, _ := filepath.Rel(root, path)
						out = append(out, actionLit{action: s, pos: rel + ":" + strconv.Itoa(fset.Position(bl.Pos()).Line)})
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	return out
}

// isEventType matches the type expression Event and audit.Event.
func isEventType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name == "Event"
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && x.Name == "audit" && e.Sel.Name == "Event"
	}
	return false
}

// TestAuditAction_ScannerSeesLiterals keeps the scan honest: it must find an
// Event literal, or TestAuditAction_Grammar would pass over nothing.
func TestAuditAction_ScannerSeesLiterals(t *testing.T) {
	dir := t.TempDir()
	src := "package x\nimport \"example.test/audit\"\nvar _ = audit.Event{Action: \"core.Bad.Name\"}\nvar _ = Event{Action: \"core.ok.name\"}\nvar _ = Other{Action: \"nope\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got := eventActionLiterals(t, dir)
	if len(got) != 2 || got[0].action != "core.Bad.Name" || got[1].action != "core.ok.name" {
		t.Errorf("scanner found %+v, want the two Event literals", got)
	}
}

// TestNoGlobals (03 R5): core/audit holds no package-level mutable state and
// no init function. archtest.NoInit runs the module's rule over this package.
func TestNoGlobals(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if root, err = archtest.ModuleRoot(root); err != nil {
		t.Fatal(err)
	}
	archtest.NoInit(t, root, "./audit/...")
}
