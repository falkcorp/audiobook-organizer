// file: scripts/config_cow_codemod/main.go
// version: 1.0.0
// guid: a03e5894-f2b4-4107-8491-ff300593b2d9
// last-edited: 2026-11-21

//go:build config_cow_codemod

// Command config_cow_codemod is the syntax half of scripts/config_cow_codemod.py:
// it parses Go files with go/ast, finds every use of the package-global
// config.AppConfig (or bare AppConfig inside internal/config), classifies each
// one, and prints one JSON object per site on stdout. It never edits a file;
// the Python driver applies the edits it proposes and prints the rest as the
// manual list. See docs/plans/platform-core/config-cow.md.
//
// It is behind a build tag so `go build ./...` and `go vet ./...` never see it:
//
//	go run -tags config_cow_codemod ./scripts/config_cow_codemod [-test-writes] < files
//
// File paths arrive one per line on stdin.
//
// Classification works on syntax only, deliberately: a type-checked pass would
// need every package to build, and the point of the codemod is to be re-run on
// a tree mid-sweep, where half the packages do not. Where syntax cannot tell a
// read from a write (a method call that might have a pointer receiver), the
// site is refused, not guessed.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const configImport = "github.com/falkcorp/audiobook-organizer/internal/config"

// Site is one use of AppConfig.
type Site struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	// Kind is the classification; Auto says whether Edits are proposed.
	Kind   string `json:"kind"`
	Auto   bool   `json:"auto"`
	Detail string `json:"detail,omitempty"`
	// Text is the source of the enclosing chain, for the manual list.
	Text  string `json:"text"`
	Edits []Edit `json:"edits,omitempty"`
}

// Edit replaces the bytes [Start, End) of File with New.
type Edit struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	New   string `json:"new"`
}

// Kinds. The auto ones are exact rewrites; every other kind is printed for a
// human.
const (
	kindRead       = "read"         // config.AppConfig.X... as a value -> config.Get().X...
	kindValue      = "value-copy"   // config.AppConfig as a whole value -> config.Snapshot()
	kindTestSet    = "test-set"     // _test.go: config.AppConfig.X = v -> config.SetForTest(func(c){c.X = v})
	kindTestRepl   = "test-replace" // _test.go: config.AppConfig = v -> config.ReplaceForTest(v)
	kindAddr       = "pointer-handoff"
	kindAddrField  = "field-address"
	kindWrite      = "write"
	kindMethodCall = "method-call"
	kindMutBuiltin = "mutating-builtin"
	kindMutSort    = "mutating-sort"
	kindSliceExpr  = "slice-expr"
)

// readOnlyMethods are config methods a caller may invoke on a snapshot: value
// receivers, or pointer receivers whose bodies only read. The driver passes the
// list (READONLY_METHODS in scripts/config_cow_codemod.py), and
// internal/config's TestReadOnlyMethodsDoNotMutate holds each name to it.
var readOnlyMethods = map[string]bool{}

// sortMutators are the sort and slices functions that reorder or overwrite
// their first argument in place.
var sortMutators = map[string]map[string]bool{
	"sort": {"Strings": true, "Ints": true, "Float64s": true, "Slice": true, "SliceStable": true, "Sort": true, "Stable": true},
	"slices": {"Sort": true, "SortFunc": true, "SortStableFunc": true, "Reverse": true, "Delete": true, "DeleteFunc": true,
		"Insert": true, "Replace": true, "Compact": true, "CompactFunc": true, "Grow": true, "Clip": true},
}

func main() {
	testWrites := flag.Bool("test-writes", false, "propose rewrites for simple writes in _test.go files")
	readOnly := flag.String("readonly-methods", "", "comma-separated config methods known not to mutate their receiver")
	flag.Parse()
	for _, m := range strings.Split(*readOnly, ",") {
		if m = strings.TrimSpace(m); m != "" {
			readOnlyMethods[m] = true
		}
	}
	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	failed := false
	for sc.Scan() {
		path := strings.TrimSpace(sc.Text())
		if path == "" {
			continue
		}
		sites, err := analyzeFile(path, *testWrites)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config_cow_codemod: %s: %v\n", path, err)
			failed = true
			continue
		}
		for _, s := range sites {
			if err := enc.Encode(s); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
	}
	if failed {
		os.Exit(1)
	}
}

// rootMatcher reports whether n (whose parent is parent, or nil when unknown)
// is the AppConfig root in this file: the selector <local>.AppConfig in an
// importing file, or the bare identifier AppConfig inside package config.
type rootMatcher func(n, parent ast.Node) bool

func matcherFor(f *ast.File, path string) rootMatcher {
	if f.Name.Name == "config" && isConfigPackageDir(path) {
		return func(n, parent ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Name != "AppConfig" {
				return false
			}
			switch p := parent.(type) {
			case *ast.SelectorExpr:
				if p.Sel == id {
					return false // x.AppConfig: a field, not the package var
				}
			case *ast.ValueSpec:
				return false // the declaration itself
			case *ast.KeyValueExpr:
				if p.Key == id {
					return false
				}
			}
			// Obj is nil for a reference to a var declared in another file of
			// the package; a local of the same name resolves to its own Decl.
			return id.Obj == nil || isPackageLevel(f, id.Obj)
		}
	}
	local := localName(f)
	if local == "" || local == "_" || local == "." {
		return nil
	}
	return func(n, _ ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "AppConfig" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		// id.Obj != nil means a local variable shadows the import name.
		return ok && id.Name == local && id.Obj == nil
	}
}

func isConfigPackageDir(path string) bool {
	dir := filepath.ToSlash(filepath.Dir(path))
	return dir == "internal/config" || strings.HasSuffix(dir, "/internal/config")
}

// isPackageLevel reports whether obj is declared at file scope (the package var
// AppConfig) rather than a local of the same name.
func isPackageLevel(f *ast.File, obj *ast.Object) bool {
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok {
			for _, s := range g.Specs {
				if vs, ok := s.(*ast.ValueSpec); ok && obj.Decl == vs {
					return true
				}
			}
		}
	}
	return false
}

func analyzeFile(path string, testWrites bool) ([]Site, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	match := matcherFor(f, path)
	if match == nil {
		return nil, nil
	}
	isTest := strings.HasSuffix(path, "_test.go")
	inPkg := f.Name.Name == "config" && isConfigPackageDir(path)
	// The names the rewrites spell, qualified unless inside package config.
	q := ""
	if !inPkg {
		q = localName(f) + "."
	}
	getCall, snapCall, setCall, replCall, param := q+"Get()", q+"Snapshot()", q+"SetForTest", q+"ReplaceForTest", "*"+q+"Config"
	if inPkg {
		// Inside package config, Snapshot itself is one of the readers being
		// rewritten (`return AppConfig`); spelling a value copy as Snapshot()
		// there would make it call itself.
		snapCall = "*Get()"
	}

	off := func(p token.Pos) int { return fset.Position(p).Offset }
	hoister := newTempNamer(f)
	text := func(n ast.Node) string { return string(src[off(n.Pos()):off(n.End())]) }

	var sites []Site
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		var par ast.Node
		if len(stack) >= 2 {
			par = stack[len(stack)-2]
		}
		if !match(n, par) {
			return true
		}
		root := n.(ast.Expr)
		// Climb the access chain: field selectors, index expressions, parens.
		top := ast.Node(root)
		depth := 0
		i := len(stack) - 2
		for ; i >= 0; i-- {
			p := stack[i]
			switch pp := p.(type) {
			case *ast.SelectorExpr:
				if pp.X != top {
					goto done
				}
				depth++
			case *ast.IndexExpr:
				if pp.X != top {
					goto done
				}
			case *ast.ParenExpr:
			default:
				goto done
			}
			top = p
		}
	done:
		var parent ast.Node
		if i >= 0 {
			parent = stack[i]
		}
		pos := fset.Position(root.Pos())
		s := Site{File: path, Line: pos.Line, Col: pos.Column, Text: text(top)}
		rootEdit := func(repl string) []Edit {
			return []Edit{{Start: off(root.Pos()), End: off(root.End()), New: repl}}
		}
		switch p := parent.(type) {
		case *ast.UnaryExpr:
			if p.Op == token.AND {
				if depth == 0 {
					s.Kind = kindAddr
					s.Detail = "&AppConfig keeps reading the live global; take func() *Config or Get() per use"
				} else {
					s.Kind = kindAddrField
					s.Detail = "address of a field: a write through it would mutate a published snapshot"
				}
				s.Text = text(p)
				break
			}
			s.Kind, s.Auto, s.Edits = readKind(depth, rootEdit, getCall, snapCall)
		case *ast.AssignStmt:
			if !inLHS(p, top) {
				s.Kind, s.Auto, s.Edits = readKind(depth, rootEdit, getCall, snapCall)
				break
			}
			s.Kind = kindWrite
			s.Text = text(p)
			if isTest && testWrites {
				if k, edits, why := testWriteEdits(p, stmtParent(stack, i), hoister, depth, match, off, src, setCall, replCall, param, getCall, snapCall); edits != nil {
					s.Kind, s.Auto, s.Edits = k, true, edits
				} else {
					s.Detail = why
				}
			} else if !isTest {
				s.Detail = "write outside a test: route through Mutate/MutateChecked"
			} else {
				s.Detail = "test write (re-run with --test-writes to rewrite the simple shapes)"
			}
		case *ast.IncDecStmt:
			s.Kind, s.Detail, s.Text = kindWrite, "increment/decrement", text(p)
			if isTest && testWrites {
				if k, edits, why := testWriteEdits(p, stmtParent(stack, i), hoister, depth, match, off, src, setCall, replCall, param, getCall, snapCall); edits != nil {
					s.Kind, s.Auto, s.Edits, s.Detail = k, true, edits, ""
				} else {
					s.Detail = why
				}
			}
		case *ast.RangeStmt:
			if p.Key == top || p.Value == top {
				s.Kind, s.Detail, s.Text = kindWrite, "range assigns into the config", text(p)
			} else {
				s.Kind, s.Auto, s.Edits = readKind(depth, rootEdit, getCall, snapCall)
			}
		case *ast.CallExpr:
			if p.Fun == top {
				// config.AppConfig.X.M(): M may have a pointer receiver, which
				// would mutate the published snapshot through an implicit &.
				if sel, ok := top.(*ast.SelectorExpr); ok && depth >= 1 && readOnlyMethods[sel.Sel.Name] {
					s.Kind, s.Auto, s.Edits = kindRead, true, rootEdit(getCall)
					s.Detail = "read-only method " + sel.Sel.Name
					break
				}
				s.Kind = kindMethodCall
				s.Detail = "method or func-field call; refuse: a pointer-receiver method would mutate the snapshot"
				s.Text = text(p)
				break
			}
			if k, why := mutatingCall(p, top); k != "" {
				s.Kind, s.Detail, s.Text = k, why, text(p)
				break
			}
			s.Kind, s.Auto, s.Edits = readKind(depth, rootEdit, getCall, snapCall)
		case *ast.SliceExpr:
			if p.X == top {
				s.Kind, s.Detail, s.Text = kindSliceExpr, "re-slicing a config slice/array aliases the snapshot's backing array", text(p)
				break
			}
			s.Kind, s.Auto, s.Edits = readKind(depth, rootEdit, getCall, snapCall)
		default:
			s.Kind, s.Auto, s.Edits = readKind(depth, rootEdit, getCall, snapCall)
		}
		sites = append(sites, s)
		return true
	})
	return sites, nil
}

// localName is the name the file imports internal/config under, or "" when
// it does not import it.
func localName(f *ast.File) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == configImport {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return "config"
		}
	}
	return ""
}

// readKind is the rewrite for a site used as a value.
func readKind(depth int, rootEdit func(string) []Edit, getCall, snapCall string) (string, bool, []Edit) {
	if depth == 0 {
		// The whole Config as a value: a Snapshot() copy is the same shallow
		// copy, taken from an immutable snapshot instead of the live global.
		return kindValue, true, rootEdit(snapCall)
	}
	return kindRead, true, rootEdit(getCall)
}

func inLHS(a *ast.AssignStmt, n ast.Node) bool {
	for _, l := range a.Lhs {
		if l == n {
			return true
		}
	}
	return false
}

// mutatingCall reports whether top is the first argument of a call that
// mutates it in place.
func mutatingCall(c *ast.CallExpr, top ast.Node) (string, string) {
	if len(c.Args) == 0 || c.Args[0] != top {
		return "", ""
	}
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		switch fn.Name {
		case "append":
			return kindMutBuiltin, "append to a config slice can write into the snapshot's spare capacity"
		case "copy", "clear", "delete":
			return kindMutBuiltin, fn.Name + " writes into the snapshot's backing storage"
		}
	case *ast.SelectorExpr:
		if id, ok := fn.X.(*ast.Ident); ok && sortMutators[id.Name][fn.Sel.Name] {
			return kindMutSort, id.Name + "." + fn.Sel.Name + " reorders the snapshot's slice in place"
		}
	}
	return "", ""
}

// testWriteEdits proposes the rewrite for a write statement in a test: the
// whole statement moves inside one SetForTest closure, with every AppConfig
// root in it (targets and right-hand side alike) replaced by the closure's
// parameter c. That is exact: the closure runs synchronously, under the write
// lock, on a copy of the config the old statement read and wrote, so
//
//	config.AppConfig.A = config.AppConfig.B   ->  c.A = c.B
//	config.AppConfig.N += 2                   ->  c.N += 2
//	x, config.AppConfig.A = 1, 2              ->  x, c.A = 1, 2  (x is captured)
//
// A function literal in the statement may run after the closure returns, so a
// root inside one is not c: it becomes Get() (or Snapshot() for a whole
// value), the live read the old code made whenever the literal ran.
//
// A right-hand side that CALLS something is hoisted out of the closure:
//
//	config.AppConfig.RootDir = t.TempDir()  ->  cowV1 := t.TempDir()
//	                                            config.SetForTest(func(c *config.Config) { c.RootDir = cowV1 })
//
// because the closure runs under the config write lock, and a callee that
// reaches Mutate, SetForTest or any lock-taking config getter would deadlock
// there. The old statement evaluated its right-hand side unlocked; hoisting
// keeps that. Roots in a hoisted right-hand side become live Get() reads. A
// call on the left (config.AppConfig.M[key()] = v), or a call in a statement
// that cannot be split in two (an if/for/switch header), is refused.
//
// A plain `config.AppConfig = v` becomes ReplaceForTest(v): v is an argument,
// evaluated before ReplaceForTest takes the lock.
func testWriteEdits(stmt ast.Stmt, parent ast.Node, hoister *tempNamer, depth int, match rootMatcher, off func(token.Pos) int, src []byte, setCall, replCall, param, getCall, snapCall string) (string, []Edit, string) {
	if a, ok := stmt.(*ast.AssignStmt); ok && a.Tok == token.ASSIGN && len(a.Lhs) == 1 && depth == 0 && isRootOnly(a.Lhs[0], match) && !mentions(a.Rhs[0], match) {
		rhs := string(src[off(a.Rhs[0].Pos()):off(a.Rhs[0].End())])
		return kindTestRepl, []Edit{{Start: off(a.Pos()), End: off(a.End()), New: replCall + "(" + rhs + ")"}}, ""
	}
	a, isAssign := stmt.(*ast.AssignStmt)
	if isAssign {
		for _, l := range a.Lhs {
			if hasCall(l) {
				return "", nil, "a call on the left-hand side; it would run under the config write lock"
			}
		}
	}
	if !isAssign || !rhsHasCall(a) {
		name := closureParam(stmt, match, nil)
		body := replaceRoots(stmt, match, off, src, getCall, snapCall, name)
		return kindTestSet, []Edit{{Start: off(stmt.Pos()), End: off(stmt.End()),
			New: fmt.Sprintf("%s(func(%s %s) { %s })", setCall, name, param, body)}}, ""
	}
	switch parent.(type) {
	case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
	default:
		return "", nil, "the right-hand side calls something and the statement cannot be split to hoist it out of the lock"
	}
	// Hoist: one fresh temporary per value the right-hand side yields.
	n := len(a.Lhs)
	if len(a.Rhs) == len(a.Lhs) {
		n = len(a.Rhs)
	}
	if a.Tok != token.ASSIGN {
		n = 1 // op-assignment: one target, one operand
	}
	temps := hoister.names(stmt.Pos(), n)
	var rhs []string
	for _, r := range a.Rhs {
		rhs = append(rhs, replaceRootsLive(r, match, off, src, getCall, snapCall))
	}
	hoist := fmt.Sprintf("%s := %s", strings.Join(temps, ", "), strings.Join(rhs, ", "))
	name := closureParam(stmt, match, temps)
	var lhs []string
	for _, l := range a.Lhs {
		lhs = append(lhs, replaceRoots(l, match, off, src, getCall, snapCall, name))
	}
	assign := fmt.Sprintf("%s %s %s", strings.Join(lhs, ", "), a.Tok, strings.Join(temps, ", "))
	indent := lineIndent(src, off(stmt.Pos()))
	return kindTestSet, []Edit{{Start: off(stmt.Pos()), End: off(stmt.End()),
		New: fmt.Sprintf("%s\n%s%s(func(%s %s) { %s })", hoist, indent, setCall, name, param, assign)}}, ""
}

// stmtParent is the node enclosing stack[i] (the statement), or nil.
func stmtParent(stack []ast.Node, i int) ast.Node {
	if i >= 1 {
		return stack[i-1]
	}
	return nil
}

// hasCall reports whether e calls anything outside a function literal (a
// literal's body does not run where it is written). A conversion such as
// int64(x) is syntactically a call too; hoisting one is harmless.
func hasCall(e ast.Node) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			found = true
		}
		return !found
	})
	return found
}

func rhsHasCall(a *ast.AssignStmt) bool {
	for _, r := range a.Rhs {
		if hasCall(r) {
			return true
		}
	}
	return false
}

// tempNamer hands out hoisting temporaries for one file: names spelled
// nowhere in the file and never handed out twice, so two hoisted statements in
// one block cannot redeclare each other's temporary. A statement visited once
// per root it contains gets the same names each time, so its sites propose
// identical (de-duplicated) edits.
type tempNamer struct {
	used  map[string]bool
	byPos map[token.Pos][]string
	next  int
}

func newTempNamer(f *ast.File) *tempNamer {
	t := &tempNamer{used: map[string]bool{}, byPos: map[token.Pos][]string{}}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			t.used[id.Name] = true
		}
		return true
	})
	return t
}

func (t *tempNamer) names(at token.Pos, n int) []string {
	if got, ok := t.byPos[at]; ok {
		return got
	}
	var out []string
	for len(out) < n {
		t.next++
		if name := fmt.Sprintf("cowV%d", t.next); !t.used[name] {
			t.used[name] = true
			out = append(out, name)
		}
	}
	t.byPos[at] = out
	return out
}

// lineIndent is the leading whitespace of the line holding offset.
func lineIndent(src []byte, offset int) string {
	start := offset
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

// replaceRootsLive returns e's source with every root replaced by the live
// read: getCall before a selector or index, snapCall for a whole value.
func replaceRootsLive(e ast.Node, match rootMatcher, off func(token.Pos) int, src []byte, getCall, snapCall string) string {
	base := off(e.Pos())
	text := []byte(string(src[base:off(e.End())]))
	type span struct{ start, end int }
	var roots []span
	var stack []ast.Node
	ast.Inspect(e, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		if match(n, parent) {
			roots = append(roots, span{off(n.Pos()) - base, off(n.End()) - base})
			return false
		}
		stack = append(stack, n)
		return true
	})
	for i := len(roots) - 1; i >= 0; i-- {
		r := roots[i]
		repl := snapCall
		if r.end < len(text) && (text[r.end] == '.' || text[r.end] == '[') {
			repl = getCall
		}
		text = append(text[:r.start], append([]byte(repl), text[r.end:]...)...)
	}
	return string(text)
}

// closureParam picks the closure's parameter name: c, unless the statement
// already uses that identifier (a loop variable `c` in the right-hand side
// would otherwise be shadowed by the closure parameter, which at best fails to
// compile and at worst compiles and reads the config instead).
func closureParam(stmt ast.Node, match rootMatcher, reserved []string) string {
	used := map[string]bool{}
	for _, r := range reserved {
		used[r] = true
	}
	ast.Inspect(stmt, func(n ast.Node) bool {
		if n != nil && match(n, nil) {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			used[id.Name] = true
		}
		return true
	})
	for _, name := range []string{"c", "cfg", "cc", "conf"} {
		if !used[name] {
			return name
		}
	}
	for i := 2; ; i++ {
		if name := fmt.Sprintf("c%d", i); !used[name] {
			return name
		}
	}
}

// replaceRoots returns stmt's source with every AppConfig root replaced: by the
// closure parameter (name) when a selector or index follows it, by *name when
// it stands alone, and inside
// a function literal by the live getCall / snapCall.
func replaceRoots(stmt ast.Node, match rootMatcher, off func(token.Pos) int, src []byte, getCall, snapCall, name string) string {
	base := off(stmt.Pos())
	text := []byte(string(src[base:off(stmt.End())]))
	type span struct {
		start, end int
		inFunc     bool
	}
	var roots []span
	var stack []ast.Node
	funcDepth := 0
	ast.Inspect(stmt, func(n ast.Node) bool {
		if n == nil {
			if _, ok := stack[len(stack)-1].(*ast.FuncLit); ok {
				funcDepth--
			}
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		if match(n, parent) {
			roots = append(roots, span{off(n.Pos()) - base, off(n.End()) - base, funcDepth > 0})
			return false
		}
		stack = append(stack, n)
		if _, ok := n.(*ast.FuncLit); ok {
			funcDepth++
		}
		return true
	})
	for i := len(roots) - 1; i >= 0; i-- {
		r := roots[i]
		field := r.end < len(text) && (text[r.end] == '.' || text[r.end] == '[')
		var repl string
		switch {
		case r.inFunc && field:
			repl = getCall
		case r.inFunc:
			repl = snapCall
		case field:
			repl = name
		default:
			repl = "*" + name
		}
		text = append(text[:r.start], append([]byte(repl), text[r.end:]...)...)
	}
	return string(text)
}

func isRootOnly(e ast.Expr, match rootMatcher) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return match(e, nil)
		}
		e = p.X
	}
}

func mentions(n ast.Node, match rootMatcher) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if x != nil && match(x, nil) {
			found = true
		}
		return !found
	})
	return found
}
