// file: core/archtest/f803b_gate.go
// version: 1.3.0
// guid: 4e7a1c92-6b35-4d08-a9f1-2c5d8e03b764
// last-edited: 2026-11-15

package archtest

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// diagnosticsPath is the package that holds the bundle Exporter and the
	// marker constant.
	diagnosticsPath = mediacorePath + "/health/diagnostics"
	// healthhttpPath is the package that serves the diagnostics routes.
	healthhttpPath = mediacorePath + "/internal/healthhttp"
	// f803bMarker is the constant in diagnostics that says the redactor is
	// still the one the round 7 review of F8-03 rejected.
	f803bMarker = "RedesignPendingF8_03b"
	// f803bBlocked ends every finding: the text a developer reads when F8-04
	// (or anything else) tries to mount the diagnostics routes.
	f803bBlocked = "mount is blocked until F8-03b replaces the path redactor; see seams.d/F8-03.md"
	// f803bConstraintNote ends every finding of the build-constraint rule.
	f803bConstraintNote = "no non-test file of diagnostics or healthhttp may carry a build constraint while the F8-03b refusal stands: a tagged copy of New, Build or the marker would run without it; see seams.d/F8-03.md"
)

// F803bGate fails t while diagnostics.RedesignPendingF8_03b is true and a
// non-test file imports core/internal/healthhttp or constructs the bundle
// Exporter (diagnostics.New, or a diagnostics.Exporter composite literal or
// new(diagnostics.Exporter)); it fails t outright when the marker is false in any
// file or declared more than once (false is not a valid state: the only way out
// is the F8-03b PR, which deletes the marker, the run-time refusal and this gate
// together, in one reviewed change); it fails t when a non-test file of
// diagnostics or healthhttp carries a build constraint; and it fails t when the
// run-time refusal's probe is anything but testing.Testing (f803bProbe).
//
// This is the early signal, with a file and a line. The refusal that holds is in
// diagnostics itself: New and Build return ErrRedesignPendingF8_03b outside a
// test binary while the marker is true, from any module, so the root module, a
// wrapper or a constructor inside diagnostics is stopped at run time even though
// this guard scans only core and matches only the names it knows.
//
// Removal is the F8-03b PR's reviewed act and nothing else: a change that
// deletes the marker, the refusal and this guard together passes by
// construction, because there is nothing left to guard, and a reviewer sees that
// diff. What the guard refuses while the marker stands, exactly:
//
//   - the marker false in any file of diagnostics, built or not, under any tag
//     set (it reads GoFiles and IgnoredGoFiles of the tag-set union), the marker
//     declared more than once (a build-tagged pair, one of them false, is two
//     declarations), missing, without a value or not the literal true or false;
//   - any build constraint (a //go:build or // +build line, or a _GOOS or _GOARCH
//     file name suffix) in a non-test file of diagnostics or of healthhttp, so no
//     tagged copy of New, Build or the marker can exist beside the refusal;
//   - the refusal's probe changed (f803bProbe), in any file of diagnostics.
//
// It does not look for anything a rename of a symbol the guard has no name for
// could hide: the names it reads are the marker and the ones the probe pin reads,
// and each fails the build or the guard when it is not found. The run-time
// refusal is checked in real binaries under every tag set the repo ships with
// by TestRedesignRefusal_RealBinary in diagnostics.
//
// The F8-03 round 7 review found that the path redactor in
// core/health/diagnostics leaks usernames and directory trees in realistic
// inputs (a volume named "Macintosh HD", a profile "C:\Users\Bruce Wayne",
// "-I/Users/..." build flags, "~bruce/", "smb://" paths), and that no further
// patch to a token scanner can close that class. The PR merged with the
// diagnostics routes dark; F8-04 mounts them, so F8-04 cannot land until F8-03b
// replaces the scanner and deletes the marker. Test files are exempt, as are
// diagnostics itself and, for the import rule, healthhttp itself.
//
// The guard counts imports and references, not calls, so an alias, a blank
// import, a dot import or a function value is caught. It is syntactic, so it
// also parses the files that build constraints exclude under every tag set
// (go list's IgnoredGoFiles). It does not look for
// //go:linkname: reaching either package that way is deliberate work, not a
// slip, and the review of the F8-04 change is where it would show.
//
// It loads core, the module above the working directory, over the whole
// tag-set union.
func F803bGate(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("F803bGate: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("F803bGate: %v", err)
	}
	findings, err := F803bGateFindings(root)
	if err != nil {
		t.Fatalf("F803bGate: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d breach(es) of the F8-03b gate:\n  %s", len(findings), strings.Join(findings, "\n  "))
	}
}

// F803bGateFindings loads the core module at dir over TagSets() and returns one
// line per breach of F803bGate, sorted; each line ends with the blocked message
// (or, for the refusal's probe, the probe message). It returns an error when the
// load fails, when diagnostics is not among the loaded packages, when the marker
// is not a plain true or false constant of diagnostics (it was renamed or
// removed without removing this guard: F8-03b deletes both together), when the
// marker is false in any file of the package, built or not (false is not a valid
// state, whatever else is in the tree: no symbol name is consulted, so renaming or
// re-receiving the scanner cannot defeat it), when it is declared more than once,
// or when no non-test file was scanned: each would make the guard pass without
// looking.
func F803bGateFindings(dir string) ([]string, error) {
	return f803bFindings(dir, f803bReadMarker)
}

// f803bFindings is F803bGateFindings with the reader of the marker passed in, so
// a test can run the gate over the real core tree with the marker read as false
// (no file is edited and nothing is mutable at run time).
func f803bFindings(dir string, readMarker func(files []string) (bool, error)) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	diag, ok := pkgs[diagnosticsPath]
	if !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", diagnosticsPath, len(pkgs))
	}
	pending, err := readMarker(slices.Concat(diag.GoFiles, diag.IgnoredGoFiles))
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, f803bFalseError("")
	}
	var out []string
	for _, path := range []string{diagnosticsPath, healthhttpPath} {
		p, ok := pkgs[path]
		if !ok {
			continue
		}
		for _, f := range slices.Concat(p.GoFiles, p.IgnoredGoFiles) {
			why, err := f803bConstraint(f)
			if err != nil {
				return nil, err
			}
			if why != "" {
				out = append(out, fmt.Sprintf("%s: %s in %s: %s", f, why, path, f803bConstraintNote))
			}
		}
	}
	scanned := 0
	for _, p := range pkgs {
		scanned += len(p.GoFiles)
		for _, f := range slices.Concat(p.GoFiles, p.IgnoredGoFiles) {
			found, err := f803bUses(f, p.ImportPath)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file under %s; the guard would be vacuous", dir)
	}
	probe, err := f803bProbe(slices.Concat(diag.GoFiles, diag.IgnoredGoFiles))
	if err != nil {
		return nil, err
	}
	out = append(out, probe...)
	sort.Strings(out)
	return uniq(out), nil
}

// f803bFalseError is the error for a marker that is false: file names the
// declaration when the reader saw it ("" when a test injected the value).
func f803bFalseError(file string) error {
	where := ""
	if file != "" {
		where = " (declared false in " + file + ")"
	}
	return fmt.Errorf("archtest: %s.%s is false%s: false is not a valid state. The marker has two states, true and deleted; F8-03b deletes it, ErrRedesignPendingF8_03b with redesignRefusal, and this guard (core/archtest/f803b_*.go) together, in its own reviewed PR. Setting it false, in any file or under any build tag, opens the run-time refusal and the healthhttp/Exporter ban at once", diagnosticsPath, f803bMarker, where)
}

// f803bReadMarker reads diagnostics.RedesignPendingF8_03b from the package's
// non-test files (the caller passes GoFiles and IgnoredGoFiles, so a file that
// only some tag set, or none, builds is read too) and returns true. The marker is
// read from source so the guard needs no import of diagnostics (archtest imports
// no package it guards) and so a fixture tree can carry either value. It is an
// error when the marker is missing, has no value or is not the literal true or
// false, when it is false in any file, and when it is declared more than once
// (a build-tagged pair is two declarations, and a reader that kept the last would
// believe whichever file it parsed last).
func f803bReadMarker(files []string) (bool, error) {
	var decls []string
	var falseIn string
	for _, file := range files {
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return false, fmt.Errorf("parse %s: %v", file, err)
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if name.Name != f803bMarker {
						continue
					}
					if i >= len(vs.Values) {
						return false, fmt.Errorf("archtest: %s.%s has no value in %s", diagnosticsPath, f803bMarker, file)
					}
					id, ok := vs.Values[i].(*ast.Ident)
					if !ok || (id.Name != "true" && id.Name != "false") {
						return false, fmt.Errorf("archtest: %s.%s must be the literal true or false (%s)", diagnosticsPath, f803bMarker, file)
					}
					decls = append(decls, file)
					if id.Name == "false" && falseIn == "" {
						falseIn = file
					}
				}
			}
		}
	}
	if len(decls) == 0 {
		return false, fmt.Errorf("archtest: %s.%s is not declared in the %d non-test file(s) of the package: if F8-03b removed it, remove the F8-03b gate (core/archtest/f803b_*.go) with it; otherwise the guard would be vacuous", diagnosticsPath, f803bMarker, len(files))
	}
	if falseIn != "" {
		return false, f803bFalseError(falseIn)
	}
	if len(decls) > 1 {
		return false, fmt.Errorf("archtest: %s.%s is declared %d times (%s): the marker is one constant in one file; a second declaration (a build-tagged pair, say) lets a build see a value this guard did not", diagnosticsPath, f803bMarker, len(decls), strings.Join(decls, ", "))
	}
	return true, nil
}

// f803bKnownOS and f803bKnownArch return the GOOS and GOARCH values the go
// command reads from a file name (go/build's syslist, which is not exported), a
// fresh slice on every call (03 R5: no package-level mutable variable); a drift
// test compares them with `go tool dist list`.
func f803bKnownOS() []string {
	return []string{"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos"}
}

func f803bKnownArch() []string {
	return []string{"386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be", "loong64", "mips", "mipsle", "mips64", "mips64le", "mips64p32", "mips64p32le", "ppc", "ppc64", "ppc64le", "riscv", "riscv64", "s390", "s390x", "sparc", "sparc64", "wasm"}
}

// f803bConstraint reports why the go command may leave file out of a build: a
// //go:build or // +build line before the package clause, or a _GOOS, _GOARCH or
// _GOOS_GOARCH suffix on the file name (the go command's own rule, goodOSArchFile:
// everything up to the first underscore is ignored). It returns "" when the file is
// unconstrained.
func f803bConstraint(file string) (string, error) {
	if why := f803bSuffixConstraint(filepath.Base(file)); why != "" {
		return why, nil
	}
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		return "", fmt.Errorf("parse %s: %v", file, err)
	}
	for _, cg := range af.Comments {
		if cg.Pos() >= af.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				return fmt.Sprintf("build constraint %q at %s", strings.TrimSpace(c.Text), fset.Position(c.Pos())), nil
			}
		}
	}
	return "", nil
}

// f803bSuffixConstraint returns a description when name ends in _GOOS, _GOARCH or
// _GOOS_GOARCH before ".go" (and an optional _test), and "" otherwise.
func f803bSuffixConstraint(name string) string {
	stem, _, _ := strings.Cut(name, ".")
	i := strings.Index(stem, "_")
	if i < 0 {
		return ""
	}
	l := strings.Split(stem[i:], "_")
	if n := len(l); n > 0 && l[n-1] == "test" {
		l = l[:n-1]
	}
	n := len(l)
	if n >= 2 && slices.Contains(f803bKnownOS(), l[n-2]) && slices.Contains(f803bKnownArch(), l[n-1]) {
		return fmt.Sprintf("file name suffix _%s_%s", l[n-2], l[n-1])
	}
	if n >= 1 && (slices.Contains(f803bKnownOS(), l[n-1]) || slices.Contains(f803bKnownArch(), l[n-1])) {
		return fmt.Sprintf("file name suffix _%s", l[n-1])
	}
	return ""
}

// f803bUses parses one non-test file of package pkg and reports its import of
// healthhttp (unless pkg is healthhttp) and, unless pkg is diagnostics, every
// reference to diagnostics.New and every diagnostics.Exporter composite literal
// or new(diagnostics.Exporter).
func f803bUses(file, pkg string) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	var out []string
	names := map[string]bool{}
	dot := false
	for _, imp := range af.Imports {
		// The parser only accepts a well-formed quoted import path.
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		switch {
		case p == healthhttpPath && pkg != healthhttpPath:
			out = append(out, fmt.Sprintf("%s: import of %s: %s", fset.Position(imp.Pos()), healthhttpPath, f803bBlocked))
		case p == diagnosticsPath && pkg != diagnosticsPath:
			switch {
			case imp.Name == nil:
				names["diagnostics"] = true
			case imp.Name.Name == ".":
				dot = true
			case imp.Name.Name != "_":
				names[imp.Name.Name] = true
			}
		}
	}
	if len(names) == 0 && !dot {
		return out, nil
	}
	report := func(pos token.Pos, what string) {
		out = append(out, fmt.Sprintf("%s: %s: %s", fset.Position(pos), what, f803bBlocked))
	}
	// isDiag reports whether e names the diagnostics identifier want: a selector
	// through the import's name, or a bare identifier under a dot import.
	isDiag := func(e ast.Expr, want string) (pos token.Pos, ok bool) {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			if id, isID := x.X.(*ast.Ident); isID && names[id.Name] && x.Sel.Name == want {
				return x.Pos(), true
			}
		case *ast.Ident:
			if dot && x.Name == want {
				return x.Pos(), true
			}
		}
		return 0, false
	}
	sels := map[*ast.Ident]bool{}
	ast.Inspect(af, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			sels[x.Sel] = true
			if pos, ok := isDiag(x, "New"); ok {
				report(pos, "reference to "+diagnosticsPath+".New, which builds the bundle Exporter")
			}
		case *ast.CompositeLit:
			if pos, ok := isDiag(x.Type, "Exporter"); ok {
				report(pos, "composite literal of "+diagnosticsPath+".Exporter")
			}
		case *ast.CallExpr:
			if fn, ok := x.Fun.(*ast.Ident); ok && fn.Name == "new" && len(x.Args) == 1 {
				if pos, ok := isDiag(x.Args[0], "Exporter"); ok {
					report(pos, "new("+diagnosticsPath+".Exporter)")
				}
			}
		}
		return true
	})
	if dot {
		ast.Inspect(af, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "New" && !sels[id] {
				report(id.Pos(), "reference to "+diagnosticsPath+".New (dot import), which builds the bundle Exporter")
			}
			return true
		})
	}
	return out, nil
}
