// file: internal/arch/core_key_names_test.go
// version: 1.0.0
// guid: abe6650e-8365-489d-8e43-6c768e14351c
// last-edited: 2026-11-05

package arch

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/archtest"
)

// coreReservedFamilies are core's own keyspaces; core literals may name them.
var coreReservedFamilies = []string{"c/", "m/"}

// reviewedAppLiterals are ABO row-type and param-key literals that no family
// prefix carries (03 R1(c), MOD7-2). Adding one is a reviewed change.
var reviewedAppLiterals = []string{"legacy_op_id"}

// appNames is the name set of 03 R1(c).
type appNames struct {
	families []string        // KeyFamilies() prefixes minus c/ and m/
	exact    map[string]bool // setting/preference sub-keys, op IDs, reviewed literals
	folded   []string        // constant prefixes folded from constant + variable keys
	allow    map[string]bool // the one reviewed allowlist
}

// keyNameFinding is one failing literal and why.
type keyNameFinding struct {
	pos, lit, why string
}

// match applies 03 R1(c)'s one match rule to lit. It returns the reason the
// literal fails, or "" when it passes.
func (n appNames) match(lit string) string {
	if n.allow[lit] {
		return ""
	}
	for _, p := range n.families {
		if strings.HasPrefix(lit, p) && (len(lit) == len(p) || lit[len(p)] != ' ') {
			return fmt.Sprintf("begins with the books key family %q", p)
		}
	}
	if n.exact[lit] {
		return fmt.Sprintf("is the app name %q", lit)
	}
	for i := 0; i < len(lit); i++ {
		if (lit[i] == '.' || lit[i] == ':') && n.exact[lit[:i]] {
			return fmt.Sprintf("begins with the app name %q followed by %q", lit[:i], lit[i])
		}
	}
	for _, f := range n.folded {
		if strings.HasPrefix(lit, f) {
			return fmt.Sprintf("begins with the folded app key prefix %q", f)
		}
	}
	return ""
}

// scanLiterals returns every string literal and every struct-tag value (each
// key:"value" pair's value up to its first ',') of file f, with positions.
func scanLiterals(fset *token.FileSet, f *ast.File) [][2]string {
	var out [][2]string
	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.Field:
			if x.Tag != nil {
				if tag, err := strconv.Unquote(x.Tag.Value); err == nil {
					for _, v := range structTagValues(tag) {
						out = append(out, [2]string{fset.Position(x.Tag.Pos()).String(), v})
					}
				}
				// The tag is not also scanned as a plain literal.
				if x.Type != nil {
					ast.Inspect(x.Type, func(n ast.Node) bool { return collectLit(fset, n, &out) })
				}
				return false
			}
		case *ast.BasicLit:
			collectLit(fset, x, &out)
		}
		return true
	})
	return out
}

func collectLit(fset *token.FileSet, n ast.Node, out *[][2]string) bool {
	if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		if s, err := strconv.Unquote(bl.Value); err == nil {
			*out = append(*out, [2]string{fset.Position(bl.Pos()).String(), s})
		}
	}
	return true
}

// structTagValues splits a struct tag into its values, each up to its first
// ',' (the reflect.StructTag grammar: key:"value" pairs separated by spaces).
func structTagValues(tag string) []string {
	var out []string
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		v, err := strconv.Unquote(tag[:i+1])
		if err != nil {
			break
		}
		tag = tag[i+1:]
		v, _, _ = strings.Cut(v, ",")
		out = append(out, v)
	}
	return out
}

// findAppKeyNames scans files and returns the failing literals.
func findAppKeyNames(t *testing.T, n appNames, files []string) []keyNameFinding {
	t.Helper()
	fset := token.NewFileSet()
	var out []keyNameFinding
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, l := range scanLiterals(fset, f) {
			if why := n.match(l[1]); why != "" {
				out = append(out, keyNameFinding{l[0], l[1], why})
			}
		}
	}
	return out
}

// loadAppNames builds the name set from ABO's live sources.
func loadAppNames(t *testing.T, root string) appNames {
	t.Helper()
	n := appNames{exact: map[string]bool{}, allow: map[string]bool{}}
	reserved := map[string]bool{}
	for _, r := range coreReservedFamilies {
		reserved[r] = true
	}
	for _, f := range database.KeyFamilies() {
		if !reserved[f.Prefix] {
			n.families = append(n.families, f.Prefix)
		}
	}
	for _, l := range readListFile(t, filepath.Join(root, "internal", "server", "testdata", "op_ids.golden")) {
		n.exact[l] = true
	}
	for _, l := range reviewedAppLiterals {
		n.exact[l] = true
	}
	exact, folded := settingKeys(t, root)
	for _, k := range exact {
		n.exact[k] = true
	}
	n.folded = folded
	for _, l := range readListFile(t, filepath.Join(root, "internal", "arch", "core_key_names_allow.txt")) {
		n.allow[l] = true
	}
	return n
}

// readListFile returns the non-blank, non-# lines of path, trimmed.
func readListFile(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// settingKeyArg returns the index of the key argument of a settings or
// preference store method, or -1 when name is not one.
func settingKeyArg(name string) int {
	switch {
	case name == "GetSetting" || name == "SetSetting" || name == "DeleteSetting":
		return 0
	case strings.Contains(name, "UserPreference") && strings.Contains(name, "ForUser"):
		return 1
	case strings.Contains(name, "UserPreference"):
		return 0
	}
	return -1
}

// strVal is a resolved string expression: exact, or a folded prefix.
type strVal struct {
	s      string
	folded bool
	ok     bool
}

// pkgScope holds what the resolver knows about one package.
type pkgScope struct {
	consts  map[string]ast.Expr      // package-level string constants
	funcs   map[string]*ast.FuncDecl // package-level functions
	imports map[string]string        // file-local import name -> path, per file (merged; collisions are rare)
}

// settingKeys folds the setting and preference sub-keys from every non-test
// call site of GetSetting/SetSetting/DeleteSetting/…UserPreference… in the
// root module: a literal or a constant is an exact key; a constant + variable
// (directly, through a one-line helper, or through fmt.Sprintf) folds to the
// constant prefix. A key built from a variable alone cannot be resolved and is
// counted, not guessed.
func settingKeys(t *testing.T, root string) (exact, folded []string) {
	t.Helper()
	pkgs, err := archtest.Load(archtest.LoadOptions{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scopes := map[string]*pkgScope{}
	files := map[string][]*ast.File{}
	for ip, p := range pkgs {
		sc := &pkgScope{consts: map[string]ast.Expr{}, funcs: map[string]*ast.FuncDecl{}, imports: map[string]string{}}
		scopes[ip] = sc
		for _, file := range p.GoFiles {
			f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			files[ip] = append(files[ip], f)
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				name := path[strings.LastIndex(path, "/")+1:]
				if imp.Name != nil {
					name = imp.Name.Name
				}
				sc.imports[name] = path
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.GenDecl:
					if d.Tok != token.CONST {
						continue
					}
					for _, s := range d.Specs {
						vs := s.(*ast.ValueSpec)
						for i, nm := range vs.Names {
							if i < len(vs.Values) {
								sc.consts[nm.Name] = vs.Values[i]
							}
						}
					}
				case *ast.FuncDecl:
					if d.Recv == nil {
						sc.funcs[d.Name.Name] = d
					}
				}
			}
		}
	}

	var resolve func(ip string, e ast.Expr, locals map[string]ast.Expr, depth int) strVal
	resolve = func(ip string, e ast.Expr, locals map[string]ast.Expr, depth int) strVal {
		if depth > 8 {
			return strVal{}
		}
		sc := scopes[ip]
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil {
					return strVal{s: s, ok: true}
				}
			}
		case *ast.ParenExpr:
			return resolve(ip, x.X, locals, depth+1)
		case *ast.Ident:
			if v, ok := locals[x.Name]; ok {
				return resolve(ip, v, nil, depth+1)
			}
			if v, ok := sc.consts[x.Name]; ok {
				return resolve(ip, v, nil, depth+1)
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if path, ok := sc.imports[id.Name]; ok && scopes[path] != nil {
					if v, ok := scopes[path].consts[x.Sel.Name]; ok {
						return resolve(path, v, nil, depth+1)
					}
				}
			}
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return strVal{}
			}
			l := resolve(ip, x.X, locals, depth+1)
			if !l.ok {
				return strVal{}
			}
			if l.folded {
				return l
			}
			r := resolve(ip, x.Y, locals, depth+1)
			if r.ok && !r.folded {
				return strVal{s: l.s + r.s, ok: true}
			}
			if l.s == "" {
				return strVal{}
			}
			return strVal{s: l.s + r.s, folded: true, ok: true}
		case *ast.CallExpr:
			// fmt.Sprintf("prefix%…", …) folds to its constant prefix.
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" && sel.Sel.Name == "Sprintf" && len(x.Args) > 0 {
					if f := resolve(ip, x.Args[0], locals, depth+1); f.ok && !f.folded {
						if pre, _, found := strings.Cut(f.s, "%"); found && pre != "" {
							return strVal{s: pre, folded: true, ok: true}
						} else if !found {
							return f
						}
					}
					return strVal{}
				}
			}
			// A one-line helper: func k(x T) string { return <expr> }.
			var fd *ast.FuncDecl
			fip := ip
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				fd = sc.funcs[fn.Name]
			case *ast.SelectorExpr:
				if id, ok := fn.X.(*ast.Ident); ok {
					if path, ok := sc.imports[id.Name]; ok && scopes[path] != nil {
						fd, fip = scopes[path].funcs[fn.Sel.Name], path
					}
				}
			}
			if fd != nil && fd.Body != nil && len(fd.Body.List) == 1 {
				if ret, ok := fd.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
					return resolve(fip, ret.Results[0], nil, depth+1)
				}
			}
		}
		return strVal{}
	}

	exactSet, foldedSet := map[string]bool{}, map[string]bool{}
	unresolved := 0
	for ip, fs := range files {
		for _, f := range fs {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				locals := localStringAssigns(fd.Body)
				ast.Inspect(fd.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					arg := settingKeyArg(sel.Sel.Name)
					if arg < 0 || arg >= len(call.Args) {
						return true
					}
					v := resolve(ip, call.Args[arg], locals, 0)
					switch {
					case !v.ok || v.s == "":
						unresolved++
					case v.folded:
						foldedSet[v.s] = true
					default:
						exactSet[v.s] = true
					}
					return true
				})
			}
		}
	}
	for k := range exactSet {
		exact = append(exact, k)
	}
	for k := range foldedSet {
		folded = append(folded, k)
	}
	sort.Strings(exact)
	sort.Strings(folded)
	t.Logf("setting/preference keys: %d exact %q, %d folded prefixes %q, %d call sites keyed by a variable alone",
		len(exact), exact, len(folded), folded, unresolved)
	return exact, folded
}

// localStringAssigns maps each local name that a function body assigns or
// declares exactly once to that expression (x := "lit", const x = …), so a
// key held in a local is resolved; names assigned more than once are dropped.
func localStringAssigns(body *ast.BlockStmt) map[string]ast.Expr {
	seen := map[string]int{}
	vals := map[string]ast.Expr{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok {
					seen[id.Name]++
					if len(x.Rhs) == len(x.Lhs) {
						vals[id.Name] = x.Rhs[i]
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range x.Names {
				seen[id.Name]++
				if i < len(x.Values) {
					vals[id.Name] = x.Values[i]
				}
			}
		}
		return true
	})
	for k, c := range seen {
		if c != 1 {
			delete(vals, k)
		}
	}
	return vals
}

// TestCoreHoldsNoAppKeyNames is 03 R1(c): no string literal or struct-tag value
// in a non-test core/ file, over the tag union, names a books key family, an
// ABO setting or preference key, an op ID or a reviewed app literal. A copied
// format passes R1(a) and R1(b); this catches it.
func TestCoreHoldsNoAppKeyNames(t *testing.T) {
	root := repoRoot(t)
	names := loadAppNames(t, root)

	// Positive controls on the name set: each source contributed.
	for _, want := range []string{"book:", "library:"} {
		if !containsStr(names.families, want) {
			t.Fatalf("the name set is broken: family %q missing", want)
		}
	}
	for _, want := range []string{"config_blob", "diagnostics.export", "legacy_op_id"} {
		if !names.exact[want] {
			t.Fatalf("the name set is broken: %q missing", want)
		}
	}
	if !containsStr(names.folded, "scheduled_task_last_tick.") {
		t.Fatalf("the name set is broken: folded prefix scheduled_task_last_tick. missing (have %v)", names.folded)
	}
	for _, r := range coreReservedFamilies {
		if containsStr(names.families, r) {
			t.Fatalf("core's reserved family %q is in the name set", r)
		}
	}

	pkgs, err := archtest.Load(archtest.LoadOptions{Dir: filepath.Join(root, "core"), TagSets: archtest.TagSets()})
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, p := range pkgs {
		files = append(files, p.GoFiles...)
	}
	if len(files) == 0 {
		t.Fatal("the loader is broken, not the code: no non-test core file")
	}
	if found := findAppKeyNames(t, names, files); len(found) > 0 {
		var lines []string
		for _, f := range found {
			lines = append(lines, fmt.Sprintf("%s: %q %s", f.pos, f.lit, f.why))
		}
		t.Errorf("core holds app key names (03 R1(c); core never decodes an app's rows — an app hands them to a core importer). "+
			"Exceptions go in internal/arch/core_key_names_allow.txt with a reason:\n  %s", strings.Join(lines, "\n  "))
	}
}

// TestCoreHoldsNoAppKeyNames_Cases are 03 R1(c)'s listed cases, run through
// the same parser, collector and match rule against the live name set.
func TestCoreHoldsNoAppKeyNames_Cases(t *testing.T) {
	names := loadAppNames(t, repoRoot(t))
	cases := []struct {
		name, src string
		fail      bool
		why       string // substring of the reason, when fail
	}{
		{"error string with a family word passes", `var _ = errors.New("library: root escapes")`, false, ""},
		{"family key built from a prefix fails", `func k(id string) string { return "library:" + id }`, true, `family "library:"`},
		{"core reserved family passes", `const p = "c/tasks/"`, false, ""},
		{"core kind spelling an op ID fails", `var _ = tasks.Kind("diagnostics.export")`, true, `app name "diagnostics.export"`},
		{"op ID followed by a dot fails", `const k = "library.organize.extra"`, true, `app name "library.organize" followed by '.'`},
		{"op ID with a longer name is its own key", `const k = "library.organize-when-scanned"`, true, `is the app name "library.organize-when-scanned"`},
		{"folded constant prefix fails", `const k = "scheduled_task_last_tick.x"`, true, `folded app key prefix "scheduled_task_last_tick."`},
		{"struct tag value fails", "type T struct { ID string `json:\"legacy_op_id,omitempty\"` }", true, `app name "legacy_op_id"`},
		{"allowlisted literal passes", `const k = "invite:global"`, false, ""},
		{"setting key fails", `const k = "config_blob"`, true, `app name "config_blob"`},
	}
	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(dir, fmt.Sprintf("case%d.go", i))
			if err := os.WriteFile(file, []byte("package fixture\n\n"+tc.src+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			found := findAppKeyNames(t, names, []string{file})
			if !tc.fail {
				if len(found) != 0 {
					t.Fatalf("want pass, got %+v", found)
				}
				return
			}
			if len(found) != 1 || !strings.Contains(found[0].why, tc.why) {
				t.Fatalf("want one finding containing %q, got %+v", tc.why, found)
			}
		})
	}

	// "library.organize-when-scanned" is judged as its own key: with it out of
	// the set, the op ID library.organize does not catch it by prefix.
	without := names
	without.exact = map[string]bool{}
	for k, v := range names.exact {
		if k != "library.organize-when-scanned" {
			without.exact[k] = v
		}
	}
	if why := without.match("library.organize-when-scanned"); why != "" {
		t.Fatalf("library.organize-when-scanned was caught by another name: %s", why)
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
