// file: internal/telemetry/contract/declared_families_test.go
// version: 1.0.0
// guid: c98c5717-1401-41f2-8a41-3b568da2ac3d
// last-edited: 2026-10-10

package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// repoRoot is the module root, seen from this package's directory.
const repoRoot = "../../.."

// scannedDirs are the trees whose non-test Go files may declare a family; the
// same scope as the brief's constructor count.
var scannedDirs = []string{"internal", "pkg", "cmd"}

const (
	promPkg     = "github.com/prometheus/client_golang/prometheus"
	promautoPkg = "github.com/prometheus/client_golang/prometheus/promauto"
)

var constructorRE = regexp.MustCompile(`^New(Counter|Gauge|Histogram|Summary|Untyped)(Vec|Func)?$`)

// otelInstrumentRE matches the OTel metric.Meter instrument methods.
var otelInstrumentRE = regexp.MustCompile(`^(Int64|Float64)(Counter|UpDownCounter|Histogram|Gauge|ObservableCounter|ObservableUpDownCounter|ObservableGauge)$`)

// descHelpers are functions that wrap prometheus.NewDesc with a fixed name
// prefix. A call to one with a literal name declares a family; the NewDesc
// inside the helper's own body is not resolved (its name is a parameter).
// nameArg and typeArg are argument indexes; typeArg holds a
// prometheus.<X>Value identifier.
var descHelpers = map[string]struct {
	prefix           string
	nameArg, typeArg int
}{
	"newPebbleDesc": {prefix: "audiobook_organizer_", nameArg: 0, typeArg: 2},
}

type declared struct {
	name, typ, pos string
}

// TestDeclaredFamiliesInGolden closes the gap assertion (B) of
// TestSeriesContract cannot: a family that is declared but never seeded
// exports nothing, so the scrape never shows it. This test parses every
// non-test Go file under internal/, pkg/ and cmd/ and requires:
//
//   - every client_golang constructor (prometheus./promauto.New<Type>[Vec|Func],
//     and NewDesc wrappers listed in descHelpers) to have a golden row with
//     source client_golang and the same type, and the reverse;
//   - every OTel instrument call (meter.Int64Counter(...) and siblings) to be
//     declared in ourInstruments (and so, via TestInstrumentNames, in the
//     golden with source otel).
//
// A name that is not a string literal cannot be checked and fails.
func TestDeclaredFamiliesInGolden(t *testing.T) {
	families, instruments, problems := scanDeclarations(t)
	for _, p := range problems {
		t.Error(p)
	}
	t.Logf("declared client_golang families: %d; OTel instrument calls: %d", len(families), len(instruments))

	golden := map[string]goldenRow{}
	for _, r := range mustGolden(t) {
		golden[r.name] = r
	}
	seen := map[string]bool{}
	for _, d := range families {
		if seen[d.name] {
			t.Errorf("%s: family %s is declared more than once", d.pos, d.name)
		}
		seen[d.name] = true
		r, ok := golden[d.name]
		switch {
		case !ok:
			t.Errorf("%s: family %s (%s) is declared but not in the contract: add a %s row and a seedingTable row in this PR",
				d.pos, d.name, d.typ, goldenPath)
		case r.source != sourceClientGolang:
			t.Errorf("%s: family %s is a client_golang constructor but its golden row says source %s", d.pos, d.name, r.source)
		case r.typ != d.typ:
			t.Errorf("%s: family %s is declared as %s, golden says %s", d.pos, d.name, d.typ, r.typ)
		}
	}
	for _, r := range golden {
		if r.source == sourceClientGolang && !seen[r.name] {
			t.Errorf("golden row %s (source client_golang) has no constructor under %v: remove the row and its seedingTable row, or restore the family",
				r.name, scannedDirs)
		}
	}

	declaredOTel := map[string]bool{}
	for _, s := range ourInstruments {
		declaredOTel[s.name] = true
	}
	for _, d := range instruments {
		if !declaredOTel[d.name] {
			t.Errorf("%s: OTel instrument %q is created but not declared in ourInstruments: declare it there and add its %s row (source otel) and a seedingTable row in this PR",
				d.pos, d.name, goldenPath)
		}
	}
}

// scanDeclarations parses the scanned trees.
func scanDeclarations(t *testing.T) (families, instruments []declared, problems []string) {
	t.Helper()
	fset := token.NewFileSet()
	for _, dir := range scannedDirs {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if e.Name() == "testdata" || e.Name() == "vendor" || strings.HasPrefix(e.Name(), ".") {
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
			fam, inst, probs := scanFile(fset, f)
			families = append(families, fam...)
			instruments = append(instruments, inst...)
			problems = append(problems, probs...)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
	slices.SortFunc(families, func(a, b declared) int { return strings.Compare(a.name, b.name) })
	return families, instruments, problems
}

func scanFile(fset *token.FileSet, f *ast.File) (families, instruments []declared, problems []string) {
	promNames, promautoNames := map[string]bool{}, map[string]bool{}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		local := filepath.Base(path)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		switch path {
		case promPkg:
			promNames[local] = true
		case promautoPkg:
			promautoNames[local] = true
		}
	}
	pos := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		rel, err := filepath.Rel(repoRoot, p.Filename)
		if err != nil {
			rel = p.Filename
		}
		return fmt.Sprintf("%s:%d", rel, p.Line)
	}

	for _, decl := range f.Decls {
		fn, _ := decl.(*ast.FuncDecl)
		_, inHelper := descHelpers[funcName(fn)]
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if h, ok := descHelpers[fun.Name]; ok {
					families, problems = appendHelper(families, problems, call, h.prefix, h.nameArg, h.typeArg, pos(call))
				}
			case *ast.SelectorExpr:
				sel := fun.Sel.Name
				pkg, _ := fun.X.(*ast.Ident)
				isProm := pkg != nil && promNames[pkg.Name]
				isPromauto := (pkg != nil && promautoNames[pkg.Name]) || isPromautoWith(fun.X, promautoNames)
				switch {
				case (isProm || isPromauto) && constructorRE.MatchString(sel):
					name, err := optsName(call)
					if err != nil {
						problems = append(problems, fmt.Sprintf("%s: %s.%s: %v", pos(call), exprString(fun.X), sel, err))
						return true
					}
					families = append(families, declared{name: name, typ: strings.ToLower(constructorRE.FindStringSubmatch(sel)[1]), pos: pos(call)})
				case isProm && sel == "NewDesc" && !inHelper:
					problems = append(problems, fmt.Sprintf("%s: prometheus.NewDesc outside a descHelpers wrapper: its family cannot be checked statically; add the wrapper to descHelpers in %s",
						pos(call), "declared_families_test.go"))
				case !isProm && !isPromauto && otelInstrumentRE.MatchString(sel):
					if len(call.Args) == 0 {
						return true
					}
					name, ok := stringLit(call.Args[0])
					if !ok {
						problems = append(problems, fmt.Sprintf("%s: OTel instrument %s with a non-literal name cannot be checked statically", pos(call), sel))
						return true
					}
					instruments = append(instruments, declared{name: name, typ: sel, pos: pos(call)})
				}
			}
			return true
		})
	}
	return families, instruments, problems
}

func funcName(fn *ast.FuncDecl) string {
	if fn == nil {
		return ""
	}
	return fn.Name.Name
}

// isPromautoWith reports whether x is promauto.With(reg).
func isPromautoWith(x ast.Expr, promautoNames map[string]bool) bool {
	call, ok := x.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "With" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && promautoNames[pkg.Name]
}

// optsName joins Namespace, Subsystem and Name of the constructor's Opts
// literal the way prometheus.BuildFQName does.
func optsName(call *ast.CallExpr) (string, error) {
	if len(call.Args) == 0 {
		return "", fmt.Errorf("no Opts argument")
	}
	arg := call.Args[0]
	if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
		arg = u.X
	}
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		return "", fmt.Errorf("Opts is not a composite literal; the family name cannot be checked statically")
	}
	parts := map[string]string{}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return "", fmt.Errorf("Opts literal has positional fields")
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			continue
		}
		switch key.Name {
		case "Namespace", "Subsystem", "Name":
			v, ok := stringLit(kv.Value)
			if !ok {
				return "", fmt.Errorf("Opts.%s is not a string literal; the family name cannot be checked statically", key.Name)
			}
			parts[key.Name] = v
		}
	}
	if parts["Name"] == "" {
		return "", fmt.Errorf("Opts has no Name")
	}
	var out []string
	for _, k := range []string{"Namespace", "Subsystem", "Name"} {
		if parts[k] != "" {
			out = append(out, parts[k])
		}
	}
	return strings.Join(out, "_"), nil
}

func appendHelper(families []declared, problems []string, call *ast.CallExpr, prefix string, nameArg, typeArg int, pos string) ([]declared, []string) {
	if len(call.Args) <= max(nameArg, typeArg) {
		return families, append(problems, fmt.Sprintf("%s: helper call has too few arguments", pos))
	}
	name, ok := stringLit(call.Args[nameArg])
	if !ok {
		return families, append(problems, fmt.Sprintf("%s: helper call with a non-literal name cannot be checked statically", pos))
	}
	typ := ""
	if sel, ok := call.Args[typeArg].(*ast.SelectorExpr); ok {
		switch sel.Sel.Name {
		case "CounterValue":
			typ = "counter"
		case "GaugeValue":
			typ = "gauge"
		case "UntypedValue":
			typ = "untyped"
		}
	}
	if typ == "" {
		return families, append(problems, fmt.Sprintf("%s: helper call type argument is not prometheus.{Counter,Gauge,Untyped}Value", pos))
	}
	return append(families, declared{name: prefix + name, typ: typ, pos: pos}), problems
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

func exprString(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "promauto.With(...)"
}
