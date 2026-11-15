// file: core/archtest/f803b_probe.go
// version: 1.1.0
// guid: b2d6f0a8-4c17-4e93-9a5b-7d1e3c8f2a60
// last-edited: 2026-11-15

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// f803bProbeNote ends every finding of the probe pin.
const f803bProbeNote = "the F8-03b refusal's probe must be testing.Testing and nothing else; see seams.d/F8-03.md"

// f803bProbe pins the run-time refusal of diagnostics (round 10, N-3): the
// refusal is only as good as the probe that says "this is a test binary", and
// every in-process test passes the probe in, so a probe that always says "test
// binary" survives them all. The pin is syntactic, over the non-test files of
// diagnostics (and the files no tag set builds), and it reads names, so a rename
// fails the build instead of passing: every pinned symbol must be found.
//
//   - every call of newExporter ends with the argument testing.Testing (the
//     package "testing" under whatever name the file imports it);
//   - every call of redesignRefusal passes RedesignPendingF8_03b first and, second,
//     the parameter isTestBinary (inside newExporter), a method value testBinary,
//     or testing.Testing;
//   - isTestBinary is never assigned, and its only composite-literal value is the
//     identifier isTestBinary (newExporter's parameter);
//   - newExporter, redesignRefusal and testBinary are only ever called, never
//     passed around as values;
//   - the method testBinary has no function literal and returns only
//     recv.isTestBinary() or testing.Testing(), the last statement being
//     return testing.Testing().
//
// What it does not see: a different package passing its own value into an
// Exporter (the field is unexported), and the linker flag that makes
// testing.Testing true (F803bNoLinkerOverride looks for that in the repo's build
// paths). F8-03b deletes this file with the refusal.
func f803bProbe(files []string) ([]string, error) {
	var (
		out                                          []string
		newExporterCalls, refusalCalls, testBinaries int
	)
	const (
		fnNew     = "newExporter"
		fnRefuse  = "redesignRefusal"
		fnTestBin = "testBinary"
		field     = "isTestBinary"
	)
	for _, file := range files {
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %v", file, err)
		}
		testingNames := map[string]bool{}
		for _, imp := range af.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == "testing" {
				switch {
				case imp.Name == nil:
					testingNames["testing"] = true
				case imp.Name.Name != "_" && imp.Name.Name != ".":
					testingNames[imp.Name.Name] = true
				}
			}
		}
		// isTesting reports whether e is the selector testing.Testing.
		isTesting := func(e ast.Expr) bool {
			sel, ok := e.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Testing" {
				return false
			}
			id, ok := sel.X.(*ast.Ident)
			return ok && testingNames[id.Name]
		}
		report := func(pos token.Pos, what string) {
			out = append(out, fmt.Sprintf("%s: %s: %s", fset.Position(pos), what, f803bProbeNote))
		}
		// allowed holds the identifiers that name a pinned function in a place
		// the pin has judged: a declaration, the callee of a call, or the method
		// value handed to redesignRefusal.
		allowed := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				allowed[x.Name] = true
				if x.Name.Name == fnTestBin && x.Recv != nil {
					testBinaries++
					f803bProbeTestBinary(x, isTesting, field, report)
				}
			case *ast.CallExpr:
				callee, _ := x.Fun.(*ast.Ident)
				switch {
				case callee != nil && callee.Name == fnNew:
					newExporterCalls++
					allowed[callee] = true
					if len(x.Args) == 0 || !isTesting(x.Args[len(x.Args)-1]) {
						report(x.Pos(), fnNew+" must be called with testing.Testing as its last argument")
					}
				case callee != nil && callee.Name == fnRefuse:
					refusalCalls++
					allowed[callee] = true
					if len(x.Args) != 2 {
						report(x.Pos(), fnRefuse+" must be called with two arguments")
						break
					}
					if id, ok := x.Args[0].(*ast.Ident); !ok || id.Name != f803bMarker {
						report(x.Args[0].Pos(), fnRefuse+" must be called with "+f803bMarker+" as its first argument")
					}
					switch p := x.Args[1].(type) {
					case *ast.Ident:
						if p.Name != field {
							report(p.Pos(), fnRefuse+" must be given the probe "+field+", the method value "+fnTestBin+" or testing.Testing")
						}
					case *ast.SelectorExpr:
						if p.Sel.Name == fnTestBin {
							allowed[p.Sel] = true
						} else if !isTesting(p) {
							report(p.Pos(), fnRefuse+" must be given the probe "+field+", the method value "+fnTestBin+" or testing.Testing")
						}
					default:
						report(x.Args[1].Pos(), fnRefuse+" must be given the probe "+field+", the method value "+fnTestBin+" or testing.Testing")
					}
				}
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == fnTestBin {
					allowed[sel.Sel] = true
				}
			case *ast.AssignStmt:
				for _, l := range x.Lhs {
					if f803bNamed(l, field) {
						report(l.Pos(), field+" is assigned: it is set only by newExporter's composite literal")
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name == field {
					if v, ok := x.Value.(*ast.Ident); !ok || v.Name != field {
						report(x.Value.Pos(), field+" may only be set to newExporter's parameter "+field)
					}
				}
			}
			return true
		})
		ast.Inspect(af, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && !allowed[id] && (id.Name == fnNew || id.Name == fnRefuse || id.Name == fnTestBin) {
				report(id.Pos(), id.Name+" is used other than by calling it: a function value could carry a different probe")
			}
			return true
		})
	}
	if newExporterCalls == 0 || refusalCalls < 2 || testBinaries != 1 {
		out = append(out, fmt.Sprintf("archtest: the probe pin found %d call(s) of newExporter, %d of redesignRefusal and %d method(s) testBinary in %s (want at least 1, at least 2 and exactly 1): if F8-03b renamed or removed them, remove the F8-03b gate (core/archtest/f803b_*.go) with them; otherwise the pin would be vacuous: %s",
			newExporterCalls, refusalCalls, testBinaries, diagnosticsPath, f803bProbeNote))
	}
	sort.Strings(out)
	return uniq(out), nil
}

// f803bProbeTestBinary pins the body of the method testBinary.
func f803bProbeTestBinary(fd *ast.FuncDecl, isTesting func(ast.Expr) bool, field string, report func(token.Pos, string)) {
	if fd.Body == nil || len(fd.Body.List) == 0 {
		report(fd.Pos(), "testBinary has no body")
		return
	}
	recv := ""
	if len(fd.Recv.List) == 1 && len(fd.Recv.List[0].Names) == 1 {
		recv = fd.Recv.List[0].Names[0].Name
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			report(x.Pos(), "testBinary holds a function literal")
		case *ast.ReturnStmt:
			if len(x.Results) != 1 {
				report(x.Pos(), "testBinary must return one call")
				return true
			}
			call, ok := x.Results[0].(*ast.CallExpr)
			if !ok || len(call.Args) != 0 {
				report(x.Pos(), "testBinary must return recv."+field+"() or testing.Testing()")
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			viaField := isSel && sel.Sel.Name == field && recv != "" && f803bNamed(sel.X, recv)
			if !viaField && !isTesting(call.Fun) {
				report(x.Pos(), "testBinary must return recv."+field+"() or testing.Testing()")
			}
		}
		return true
	})
	last, ok := fd.Body.List[len(fd.Body.List)-1].(*ast.ReturnStmt)
	if !ok || len(last.Results) != 1 {
		report(fd.Body.Pos(), "testBinary must end with return testing.Testing()")
		return
	}
	if call, ok := last.Results[0].(*ast.CallExpr); !ok || !isTesting(call.Fun) {
		report(last.Pos(), "testBinary must end with return testing.Testing()")
	}
}

// f803bNamed reports whether e is the identifier name or a selector ending in it.
func f803bNamed(e ast.Expr, name string) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == name
	case *ast.SelectorExpr:
		return x.Sel.Name == name
	}
	return false
}

// f803bTestBinaryFlag is the linker symbol the go command sets to make
// testing.Testing true. It is assembled so this file does not contain it.
const f803bTestBinaryFlag = "testing." + "testBinary"

// F803bNoLinkerOverride fails t when a file of the repo names the linker symbol
// that makes testing.Testing true (-ldflags "-X testing.testBinary=1"), which
// would turn the run-time refusal of diagnostics off in a production binary.
//
// What it reads: every regular text file (no NUL byte in its first 8000 bytes,
// at most 4 MiB) of the repo above core (the module itself when core is not under
// a larger repo), by content and not by extension, so Makefiles, Dockerfiles,
// workflow and pipeline YAML, shell and Python with or without an extension,
// PowerShell, .conf, .env, .envrc, Makefile.local and Go sources are all read. It
// skips git, node_modules, dist, .worktrees, documentation (*.md, *.txt, *.rst,
// *.adoc, which names the flag to warn against it) and this guard's own files
// (f803b_probe*.go).
//
// What it does not see, and cannot: a flag that is not in a file of the repo
// (a CI repository variable or secret, a GOFLAGS or `go env -w` on a build host, a
// build argument supplied from outside, such as Dockerfile's
// -X main.version=${APP_VERSION} given a value with a space in it), a flag
// assembled at run time from pieces (a variable, a concatenation), a gitignored
// Makefile.local that exists only on the build host, and a person at a shell
// typing it. The guard is for a build path that sets it by accident or by copy;
// the probe pin and the real-binary test cover the code, not the linker.
func F803bNoLinkerOverride(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("F803bNoLinkerOverride: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("F803bNoLinkerOverride: %v", err)
	}
	findings, scanned, err := f803bLinkerFindings(f803bRepoRoot(root))
	if err != nil {
		t.Fatalf("F803bNoLinkerOverride: %v", err)
	}
	if scanned == 0 {
		t.Fatalf("F803bNoLinkerOverride: no build file under %s; the guard would be vacuous", f803bRepoRoot(root))
	}
	if len(findings) > 0 {
		t.Errorf("%d build path(s) set %s, which turns the F8-03b refusal off:\n  %s", len(findings), f803bTestBinaryFlag, strings.Join(findings, "\n  "))
	}
}

// f803bRepoRoot is the directory F803bNoLinkerOverride scans: the parent of the
// core module when core sits in a repo (the parent has its own go.mod), else the
// module itself.
func f803bRepoRoot(coreRoot string) string {
	parent := filepath.Dir(coreRoot)
	if filepath.Base(coreRoot) == "core" {
		if _, err := os.Stat(filepath.Join(parent, "go.mod")); err == nil {
			return parent
		}
	}
	return coreRoot
}

// f803bRepoText walks root and calls fn with the repo-relative slash path and the
// content of every regular text file that is not under a skipped directory, not
// documentation and not one of this guard's own files. It returns how many files
// it read.
func f803bRepoText(root string, fn func(rel string, body []byte)) (int, error) {
	skipDir := []string{".git", "node_modules", "dist", ".worktrees"}
	docExt := []string{".md", ".txt", ".rst", ".adoc"}
	const maxSize = 4 << 20
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if slices.Contains(skipDir, name) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, "f803b_probe") || slices.Contains(docExt, filepath.Ext(name)) || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxSize {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if slices.Contains(body[:min(len(body), 8000)], 0) {
			return nil
		}
		scanned++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fn(filepath.ToSlash(rel), body)
		return nil
	})
	return scanned, err
}

// f803bLinkerFindings walks root for text files that contain the linker symbol
// and returns one line per hit with the line number, plus how many files it read.
func f803bLinkerFindings(root string) ([]string, int, error) {
	var out []string
	scanned, err := f803bRepoText(root, func(rel string, body []byte) {
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, f803bTestBinaryFlag) {
				out = append(out, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
	})
	sort.Strings(out)
	return out, scanned, err
}

// f803bTagFlag finds a go command's -tags (or --tags, or -tags=) argument in a
// line of a build file: a quoted list, or an unquoted word. It must follow a
// space, a quote or a parenthesis, so --no-tags, rc-tags and git's --tags
// (whose next word is not a tag) do not match as tags; f803bTagWord keeps only
// words that are build-tag identifiers. Both are built on each call (03 R5: no
// package-level mutable variable).
func f803bTagFlag() *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\s"'(])--?tags(?:=|\s+)(?:"([^"]*)"|'([^']*)'|([^\s"'\\);]+))`)
}

func f803bTagWord() *regexp.Regexp {
	return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
}

// f803bTagsIn returns the build-tag lists named by -tags arguments in body, one
// sorted, deduplicated list per occurrence, skipping a make or shell variable
// ($(GO_BUILD_TAGS), ${TAGS}) and any word that is not a tag.
func f803bTagsIn(body string) [][]string {
	var out [][]string
	flag, word := f803bTagFlag(), f803bTagWord()
	for line := range strings.SplitSeq(body, "\n") {
		for _, m := range flag.FindAllStringSubmatch(line, -1) {
			list := m[1] + m[2] + m[3]
			var tags []string
			for _, w := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
				if word.MatchString(w) {
					tags = append(tags, w)
				}
			}
			slices.Sort(tags)
			if tags = slices.Compact(tags); len(tags) > 0 {
				out = append(out, tags)
			}
		}
	}
	return out
}

// F803bShippedTagSets returns the tag sets a binary of this repo is built with,
// for the real-binary test of the refusal: TagSets() (the one list of
// (GOOS, tag set) pairs CI and the Dockerfiles build, 03 §3.5.1 item 4) that apply to
// the core module, plus every distinct -tags list read from the repo's own
// build files (Makefile*, Dockerfile*, the workflow and pipeline YAML under
// .github/workflows and .woodpecker) that TagSets() does not already name. The
// second return is how many distinct lists the build files named, so a caller can
// tell that the reading found something. coreRoot is the core module's directory.
// Tags only: -race is not a tag, and the race tag set here is the tag.
func F803bShippedTagSets(coreRoot string) ([]TagSet, int, error) {
	key := func(tags []string) string {
		c := slices.Clone(tags)
		slices.Sort(c)
		return strings.Join(c, ",")
	}
	var out []TagSet
	have := map[string]bool{}
	for _, ts := range TagSets() {
		if ts.Patterns != nil {
			continue
		}
		out = append(out, ts)
		if ts.GOOS == "" {
			have[key(ts.Tags)] = true
		}
	}
	root := f803bRepoRoot(coreRoot)
	named := map[string][]string{}
	_, err := f803bRepoText(root, func(rel string, body []byte) {
		base := filepath.Base(rel)
		buildFile := strings.HasPrefix(base, "Makefile") || strings.HasPrefix(base, "Dockerfile") ||
			strings.HasPrefix(rel, ".github/workflows/") || strings.HasPrefix(rel, ".woodpecker/")
		if !buildFile {
			return
		}
		for _, tags := range f803bTagsIn(string(body)) {
			named[key(tags)] = tags
		}
	})
	if err != nil {
		return nil, 0, err
	}
	keys := make([]string, 0, len(named))
	for k := range named {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !have[k] {
			out = append(out, TagSet{Name: "build files: " + k, Tags: named[k]})
			have[k] = true
		}
	}
	return out, len(named), nil
}
