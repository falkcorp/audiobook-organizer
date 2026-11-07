// file: tools/deadcode/surface/main.go
// version: 1.0.0
// guid: 3c7e9a14-52d8-4b60-a1f3-8e0d6b24c957
// last-edited: 2026-11-07

// Command surface writes a synthetic `main` package that references a module's
// public Go API. scripts/deadcode-ratchet.py copies core/ to a scratch
// directory, has this command add the package there, and gives it to the core
// run as one more main, so everything a spec'd SDK member reaches counts as
// live (06 §6.4.5: app.App.Handler is "for coretest and embedding"; no main of
// ours calls it, but an embedder will).
//
// Why a scratch copy and not `-overlay`: deadcode loads through go/packages,
// which parses sources straight from disk and ignores an overlay supplied in
// GOFLAGS ("zz_sdksurface/main.go: no such file or directory"), so the package
// has to exist as a real file. The real tree is never written.
//
// The surface is every exported func, and every exported method of every
// exported type, in the module's public packages. Public means: not a main
// package, and no path element (below the module) named internal, examples,
// testdata or archtest, and not the kernel tree (kernel is the engine behind
// the SDK, not SDK; its callers are what the ratchet is judging). *_test.go
// files are never read, so test-only code stays dead.
//
// References are method expressions and function values, each stored in its own
// package-level variable, so no instance is ever constructed and the generated
// main compiles without knowing any constructor:
//
//	var ( s0 = p0.Func; s1 = (*p0.Type).Method; s2 = p1.Generic[struct{}] )
//
// They are deliberately NOT collected into a []any. Converting a func value to
// an interface makes its signature's types runtime types, and deadcode then
// treats every exported method of everything those types reach as reachable
// through reflection (fmt reaches reflect.Value.MethodByName in any program),
// which would silently mark exported methods such as Kernel.Lifecycle live.
// A store into a typed global takes the function's address, which is all RTA
// needs to root it, and converts nothing.
//
// Generics: a generic func or a method of a generic type cannot be referenced
// without type arguments. It is instantiated with struct{} for every type
// parameter when each parameter's constraint is `any`, `interface{}` or
// `comparable` (struct{} satisfies all three). Any other constraint cannot be
// satisfied without understanding it, so that member is SKIPPED and listed in
// a comment of the generated file; what only it reaches stays unreachable.
// Interface methods are not enumerated: they have no body, and the concrete
// methods that implement them are enumerated on their own types.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// pkg is the part of `go list -json` the generator needs.
type pkg struct {
	ImportPath string
	Name       string
	Dir        string
	GoFiles    []string // non-test, after build constraints
}

// excludedElems are path elements (below the module path) that take a package
// out of the surface.
var excludedElems = map[string]bool{"internal": true, "examples": true, "testdata": true, "archtest": true}

// isPublic reports whether a package's import path, relative to module, is SDK
// surface. rel == "" is the module's root package.
func isPublic(rel string) bool {
	if rel == "" {
		return true
	}
	parts := strings.Split(rel, "/")
	if parts[0] == "kernel" {
		return false
	}
	for _, p := range parts {
		if excludedElems[p] {
			return false
		}
	}
	return true
}

// member is one reference in the surface slice.
type member struct{ expr string }

// trivialConstraint reports whether struct{} satisfies the constraint
// expression: any, interface{} or comparable.
func trivialConstraint(e ast.Expr) bool {
	switch c := e.(type) {
	case *ast.Ident:
		return c.Name == "any" || c.Name == "comparable"
	case *ast.InterfaceType:
		return c.Methods == nil || len(c.Methods.List) == 0
	}
	return false
}

// typeParams returns the number of type parameters of a field list and whether
// every constraint is trivial. A nil list is (0, true).
func typeParams(fl *ast.FieldList) (n int, trivial bool) {
	trivial = true
	if fl == nil {
		return 0, true
	}
	for _, f := range fl.List {
		k := len(f.Names)
		if k == 0 {
			k = 1
		}
		n += k
		if !trivialConstraint(f.Type) {
			trivial = false
		}
	}
	return n, trivial
}

func instantiation(n int) string {
	if n == 0 {
		return ""
	}
	return "[" + strings.TrimSuffix(strings.Repeat("struct{},", n), ",") + "]"
}

// recvBase unwraps a receiver type expression to its base type name, and
// reports whether it was a pointer receiver. ok is false for shapes it does not
// understand.
func recvBase(e ast.Expr) (name string, ok bool) {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name, true
		default:
			return "", false
		}
	}
}

// generate renders the synthetic main for pkgs (already filtered to the surface)
// and returns it with the members it skipped. alias numbering follows the
// sorted import path, so the output is deterministic.
func generate(pkgs []pkg) (src []byte, skipped []string, err error) {
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ImportPath < pkgs[j].ImportPath })
	var imports, refs []string
	for i, p := range pkgs {
		alias := "p" + strconv.Itoa(i)
		fset := token.NewFileSet()
		type tinfo struct {
			n       int
			trivial bool
		}
		types := map[string]tinfo{}
		var funcs []*ast.FuncDecl
		for _, f := range p.GoFiles {
			file, perr := parser.ParseFile(fset, filepath.Join(p.Dir, f), nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil, nil, perr
			}
			for _, d := range file.Decls {
				switch d := d.(type) {
				case *ast.GenDecl:
					for _, s := range d.Specs {
						if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.IsExported() {
							n, triv := typeParams(ts.TypeParams)
							types[ts.Name.Name] = tinfo{n, triv}
						}
					}
				case *ast.FuncDecl:
					funcs = append(funcs, d)
				}
			}
		}
		var mine []string
		for _, fd := range funcs {
			if !fd.Name.IsExported() {
				continue
			}
			if fd.Recv == nil {
				n, triv := typeParams(fd.Type.TypeParams)
				id := p.ImportPath + "." + fd.Name.Name
				if !triv {
					skipped = append(skipped, id+" (generic, non-trivial constraint)")
					continue
				}
				mine = append(mine, alias+"."+fd.Name.Name+instantiation(n))
				continue
			}
			base, ok := recvBase(fd.Recv.List[0].Type)
			ti, exported := types[base]
			if !ok || !exported {
				continue // method of an unexported (or unrecognised) type
			}
			id := p.ImportPath + "." + base + "." + fd.Name.Name
			if !ti.trivial {
				skipped = append(skipped, id+" (generic type, non-trivial constraint)")
				continue
			}
			// (*T).M is valid for value and pointer receivers alike.
			mine = append(mine, "(*"+alias+"."+base+instantiation(ti.n)+")."+fd.Name.Name)
		}
		if len(mine) == 0 {
			imports = append(imports, "_ "+strconv.Quote(p.ImportPath))
			continue
		}
		imports = append(imports, alias+" "+strconv.Quote(p.ImportPath))
		sort.Strings(mine)
		refs = append(refs, mine...)
	}
	sort.Strings(skipped)

	var b bytes.Buffer
	b.WriteString("// Code generated by tools/deadcode/surface; DO NOT EDIT.\n\n")
	b.WriteString("// Command sdksurface is the synthetic main of the deadcode ratchet's core run.\n")
	if len(skipped) > 0 {
		b.WriteString("//\n// Skipped (not referenced):\n//\n")
		for _, s := range skipped {
			b.WriteString("//\t" + s + "\n")
		}
	}
	b.WriteString("package main\n\nimport (\n")
	for _, im := range imports {
		b.WriteString("\t" + im + "\n")
	}
	b.WriteString(")\n\nvar (\n")
	for i, r := range refs {
		b.WriteString("\ts" + strconv.Itoa(i) + " = " + r + "\n")
	}
	b.WriteString(")\n\nfunc main() {}\n")
	out, ferr := format.Source(b.Bytes())
	if ferr != nil {
		return nil, nil, fmt.Errorf("generated source does not parse: %w\n%s", ferr, b.String())
	}
	return out, skipped, nil
}

// listPublic runs `go list -json -e ./...` in dir (the caller's environment
// applies: GOOS, GOARCH, CGO_ENABLED) and returns the public, non-main
// packages of the module.
func listPublic(dir string) (module string, pkgs []pkg, err error) {
	cmd := exec.Command("go", "list", "-e", "-json=ImportPath,Name,Dir,GoFiles,Module", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("go list: %w\n%s", err, stderr.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p struct {
			pkg
			Module *struct{ Path string }
		}
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return "", nil, err
		}
		if p.Module == nil {
			return "", nil, fmt.Errorf("package %s has no module", p.ImportPath)
		}
		module = p.Module.Path
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, module), "/")
		if p.Name == "main" || !isPublic(rel) {
			continue
		}
		pkgs = append(pkgs, p.pkg)
	}
	if module == "" {
		return "", nil, errors.New("go list found no packages")
	}
	return module, pkgs, nil
}

func main() {
	dir := flag.String("dir", ".", "module directory (cwd of go list)")
	out := flag.String("out", "", "directory that receives main.go; created, must not exist (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "surface: -out is required")
		os.Exit(2)
	}
	if err := run(*dir, *out); err != nil {
		fmt.Fprintln(os.Stderr, "surface:", err)
		os.Exit(1)
	}
}

func run(dir, out string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists: the synthetic package would shadow it", out)
	}
	_, pkgs, err := listPublic(dir)
	if err != nil {
		return err
	}
	src, skipped, err := generate(pkgs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "main.go"), src, 0o644); err != nil {
		return err
	}
	fmt.Printf("%d packages, %d skipped\n", len(pkgs), len(skipped))
	return nil
}
