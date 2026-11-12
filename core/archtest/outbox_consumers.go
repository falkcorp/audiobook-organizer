// file: core/archtest/outbox_consumers.go
// version: 1.0.0
// guid: e8c615dd-3909-454c-9478-279f8e12a079
// last-edited: 2026-11-12

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// outboxPath is the package that declares the consumer constructors.
const outboxPath = mediacorePath + "/outbox"

// outboxCoreOnly are the constructors only core calls: a handler that runs in
// the relay's process, outside a task run. A module's consumer is a task run
// (ConsumeIntoTask, ConsumeIntoTaskRaw), so its work is bounded, audited,
// cancelable and visible in the run history (05-events §5E.6, §5E.7).
func outboxCoreOnly() map[string]bool {
	return map[string]bool{"Consume": true, "ConsumeTx": true, "ConsumeRaw": true}
}

// outboxModulePackage reports whether importPath is a module's package inside
// the core module: the examples under examples/. Every other package of core
// is core's own and may register a core consumer.
func outboxModulePackage(importPath string) bool {
	rel, ok := Rel(mediacorePath, importPath)
	return ok && (rel == "examples" || strings.HasPrefix(rel, "examples/"))
}

// OutboxModuleConsumersAreTasks fails t when a module's non-test file
// references outbox.Consume, outbox.ConsumeTx or outbox.ConsumeRaw
// (05-events §5E.6, §5E.7; 06 §6.4.10): those run a handler in the relay, and
// a module's consumers go through a task run instead. Every reference counts,
// not only calls: a call, a function value, a generic instantiation, through
// any import alias or a dot import, and a //go:linkname naming it. Test files
// are exempt. Outbox.Register refuses a module owner's plain consumer at boot
// as well (ErrModuleHandler); this guard fails the build before that.
//
// It loads core, the module above the working directory, over the whole
// tag-set union. Modules outside core (the application's own module) are not
// loaded here; F6-03 extends the guard to the root module with the manifest
// wiring.
func OutboxModuleConsumersAreTasks(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("OutboxModuleConsumersAreTasks: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("OutboxModuleConsumersAreTasks: %v", err)
	}
	findings, err := OutboxModuleConsumerFindings(root)
	if err != nil {
		t.Fatalf("OutboxModuleConsumersAreTasks: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d reference(s) to a core-only outbox constructor in a module package (05-events §5E.6: a module's consumer is ConsumeIntoTask):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// OutboxModuleConsumerFindings loads the core module at dir over TagSets() and
// returns one line per reference OutboxModuleConsumersAreTasks reports,
// sorted. It returns an error when the load fails, when outbox is not among
// the loaded packages, or when no non-test file of a module package was
// scanned: each would make the guard pass without looking.
func OutboxModuleConsumerFindings(dir string) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	if _, ok := pkgs[outboxPath]; !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", outboxPath, len(pkgs))
	}
	var out []string
	scanned := 0
	for _, p := range pkgs {
		if !outboxModulePackage(p.ImportPath) {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := outboxConsumerRefs(f)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file of a module package under %s; the guard would be vacuous", dir)
	}
	sort.Strings(out)
	return uniq(out), nil
}

// outboxConsumerRefs parses one file and reports every reference to a
// core-only outbox constructor: a selector through the import's name (its
// alias or "outbox"), any identifier of a guarded name in a file that
// dot-imports outbox, and a //go:linkname naming one.
func outboxConsumerRefs(file string) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	guarded := outboxCoreOnly()
	names := map[string]bool{}
	dot := false
	for _, imp := range af.Imports {
		// The parser only accepts a well-formed quoted import path.
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != outboxPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["outbox"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	var out []string
	report := func(pos token.Pos, name string) {
		out = append(out, fmt.Sprintf("%s: %s.%s in a module package", fset.Position(pos), outboxPath, name))
	}
	if len(names) > 0 || dot {
		sels := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			sels[sel.Sel] = true
			if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] && guarded[sel.Sel.Name] {
				report(sel.Pos(), sel.Sel.Name)
			}
			return true
		})
		if dot {
			ast.Inspect(af, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && guarded[id.Name] && !sels[id] {
					report(id.Pos(), id.Name)
				}
				return true
			})
		}
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if !ok {
				continue
			}
			for field := range strings.FieldsSeq(rest) {
				if name, ok := strings.CutPrefix(field, outboxPath+"."); ok && guarded[name] {
					report(c.Pos(), name)
					break
				}
			}
		}
	}
	return out, nil
}
