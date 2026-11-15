// file: core/archtest/f803b_probe.go
// version: 1.5.0
// guid: b2d6f0a8-4c17-4e93-9a5b-7d1e3c8f2a60
// last-edited: 2026-11-15

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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
// line of a build file: a quoted list, or an unquoted word (a make variable
// reference counts as one word). It must follow a space, a quote or a
// parenthesis, so --no-tags and rc-tags do not match. Built on each call (03 R5:
// no package-level mutable variable).
func f803bTagFlag() *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\s"'(])--?tags(?:=|\s+)(?:"([^"]*)"|'([^']*)'|((?:\$[({][^)}]*[)}]|[^\s"'\\);])+))`)
}

func f803bTagWord() *regexp.Regexp {
	return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
}

func f803bMakeAssign() *regexp.Regexp {
	return regexp.MustCompile(`^(?:export\s+)*([A-Za-z_][A-Za-z0-9_.]*)\s*(\?=|::=|:=|\+=|=)\s*(.*?)\s*$`)
}

func f803bMakeRef() *regexp.Regexp {
	return regexp.MustCompile(`\$[({]([^(){}$]*)[)}]`)
}

// f803bMakeVars returns NAME to value for the variable assignments of a
// Makefile body: `=`, `:=`, `::=`, `?=` (only when the name is not yet set) and
// `+=` (appended with a space), at the start of a line (a recipe line starts with
// a tab), with a trailing ` # comment` removed. A value is kept as written;
// f803bExpand resolves the references in it.
func f803bMakeVars(body string) (map[string]string, error) {
	env := newF803bMakeEnv()
	// A nil read function reads no file: an include line is skipped, as if the
	// included file did not exist.
	err := f803bMakeVarsInto(env, "(body)", body, nil, 0)
	return env.vars, err
}

// f803bMakeEnv is the variable state of one make run: each variable's text and
// whether it is a simple variable (assigned with := or ::=, expanded when
// assigned) or a recursive one (= and ?=, expanded when used).
type f803bMakeEnv struct {
	vars   map[string]string
	simple map[string]bool
}

func newF803bMakeEnv() *f803bMakeEnv {
	return &f803bMakeEnv{vars: map[string]string{}, simple: map[string]bool{}}
}

func f803bMakeInclude() *regexp.Regexp {
	return regexp.MustCompile(`^(?:-include|sinclude|include)\s+(.*?)\s*$`)
}

// f803bMakeRefused matches the directives the make model does not read and so
// refuses: conditionals (both branches would be applied), define/endef (a
// multi-line variable), undefine, and override (which makes a later plain
// assignment a no-op in make).
func f803bMakeRefused() *regexp.Regexp {
	return regexp.MustCompile(`^(?:export\s+)*(ifeq|ifneq|ifdef|ifndef|else|endif|define|endef|undefine|override)(?:\s|\(|$)`)
}

// f803bMakeTargetVar matches a target-specific variable assignment
// (`target: VAR := x`), which applies to one target's recipe only; the model has
// no per-target scope, so it refuses the line.
func f803bMakeTargetVar() *regexp.Regexp {
	return regexp.MustCompile(`^[^\s#:=][^:=]*:\s*(?:(?:export|override|private)\s+)*[A-Za-z_][A-Za-z0-9_.]*\s*(?:\?=|::=|:=|\+=|=)`)
}

// f803bMakeLines joins backslash-continued lines the way make does (the break
// and the next line's leading blanks become one space) and returns each logical
// line with the number of its first physical line.
func f803bMakeLines(body string) (lines []string, nums []int) {
	phys := strings.Split(body, "\n")
	for i := 0; i < len(phys); i++ {
		start, line := i+1, phys[i]
		for strings.HasSuffix(line, "\\") && i+1 < len(phys) {
			i++
			line = strings.TrimRight(strings.TrimSuffix(line, "\\"), " \t") + " " + strings.TrimLeft(phys[i], " \t")
		}
		lines = append(lines, line)
		nums = append(nums, start)
	}
	return lines, nums
}

// f803bMakeVarsInto applies the assignments of body to env, in order, and reads
// an included file at the line that includes it. The model, and what it refuses:
//
// Modelled (make's own rules): make reads a makefile top to bottom. An `include`,
// `-include` or `sinclude` line reads the named file in place (a file that does
// not exist is skipped, which is what `-include Makefile.local` relies on). Every
// assignment, in either file, happens when it is read, so a later assignment
// wins, `?=` yields to any earlier assignment and `+=` appends. `=` and `?=` make
// a recursive variable, expanded when a recipe uses it, so it sees the final
// values; `:=` and `::=` make a simple variable, expanded at its line with the
// values set so far, and `+=` to a simple variable expands its text at its line
// too. A name `:=` cannot expand at its line (a make function, or a variable not
// set yet, which make would read as empty) is not guessed: the variable is
// poisoned, and any -tags list that uses it fails the derivation naming the
// file and line. A backslash-continued line is one line. The result in env is the
// state after the whole makefile, which is what a recipe sees.
//
// Refused with an error naming file:line and the construct (the model has no
// reading of them, so they cannot be proved unrelated to a -tags list):
// ifeq/ifneq/ifdef/ifndef/else/endif, define/endef, undefine, override, a
// target-specific assignment, an include name with a glob character, and an
// include of a file that exists but that the file walker skipped, or lies outside
// the repo.
//
// read returns the body of an included file by the name written in the include
// line, whether it exists, and an error when it exists but was not read. Include
// names are expanded with the variables set so far; a name that cannot be
// expanded is an error, since the file it names might exist. label names body in
// messages.
func f803bMakeVarsInto(env *f803bMakeEnv, label, body string, read func(name string) (string, bool, error), depth int) error {
	assign, include, refused, targetVar := f803bMakeAssign(), f803bMakeInclude(), f803bMakeRefused(), f803bMakeTargetVar()
	lines, nums := f803bMakeLines(body)
	for i, line := range lines {
		where := fmt.Sprintf("%s:%d", label, nums[i])
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		line = strings.TrimLeft(line, " ")
		if m := refused.FindStringSubmatch(line); m != nil {
			return fmt.Errorf("%s: %q is not modelled (the derivation cannot tell whether it affects a -tags list); write the tag list without it", where, m[1])
		}
		if targetVar.MatchString(line) {
			return fmt.Errorf("%s: a target-specific variable assignment is not modelled: %s", where, strings.TrimSpace(line))
		}
		if m := include.FindStringSubmatch(line); m != nil {
			if read == nil {
				continue
			}
			names, bad := f803bExpand(m[1], env.vars)
			if len(bad) > 0 {
				return fmt.Errorf("%s: include %s: cannot resolve %s", where, m[1], strings.Join(bad, ", "))
			}
			if strings.ContainsAny(names, "*?[") {
				return fmt.Errorf("%s: include %s: a glob include is not modelled", where, names)
			}
			for name := range strings.FieldsSeq(names) {
				inc, ok, err := read(name)
				if err != nil {
					return fmt.Errorf("%s: include %s: %v", where, name, err)
				}
				if !ok {
					continue
				}
				if depth >= 8 {
					return fmt.Errorf("%s: include %s: nested deeper than 8 levels", where, name)
				}
				if err := f803bMakeVarsInto(env, name, inc, read, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		m := assign.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, op, val := m[1], m[2], m[3]
		if k := strings.Index(val, " #"); k >= 0 {
			val = strings.TrimSpace(val[:k])
		}
		// immediate is val expanded now, or a poison reference that no later
		// expansion can resolve (its name holds spaces and a colon).
		immediate := func() string {
			out, bad := f803bExpand(val, env.vars)
			if len(bad) > 0 {
				return fmt.Sprintf("$(F803b immediate expansion at %s: %s cannot be resolved there)", where, strings.Join(bad, ", "))
			}
			return out
		}
		switch op {
		case "?=":
			if _, set := env.vars[name]; !set {
				env.vars[name] = val
				env.simple[name] = false
			}
		case "+=":
			if env.simple[name] {
				env.vars[name] = strings.TrimSpace(env.vars[name] + " " + immediate())
			} else {
				env.vars[name] = strings.TrimSpace(env.vars[name] + " " + val)
			}
		case ":=", "::=":
			env.vars[name] = immediate()
			env.simple[name] = true
		default:
			env.vars[name] = val
			env.simple[name] = false
		}
	}
	return nil
}

// f803bGoreleaserSets reads the release build's tag sets from a .goreleaser.yml
// body: one TagSet per (build, goos), carrying the build's `tags:` list and its
// `env: CGO_ENABLED=` value, plus one per `overrides:` entry (its own tags, env
// and goos) and one per GOFLAGS=-tags=... in a build's, an override's or the
// top-level `env:` (a GOFLAGS tag list is a build the release can make when no
// -tags argument replaces it, so it is built as its own set). goreleaser builds
// every goos listed, so the release binary of each is a shipped set. It returns a
// description ("where: why", one per problem) for everything it does not model
// and so refuses: a template ({{ ... }}) in tags, goos, env, flags or an override,
// a build with no goos (goreleaser's default list is not read), `flags:` that name
// -tags, an env entry other than GOFLAGS that names -tags, CGO_ENABLED anywhere
// but a build's or override's own `env:` (the top-level one), CGO_ENABLED that is
// not 0 or 1, a tag that is not a build tag identifier, and a top-level
// `includes:` (a file this reading does not follow).
func f803bGoreleaserSets(rel, body string) (sets []TagSet, bad []string) {
	type override struct {
		Goos   string   `yaml:"goos"`
		Goarch string   `yaml:"goarch"`
		Env    []string `yaml:"env"`
		Tags   []string `yaml:"tags"`
		Flags  []string `yaml:"flags"`
	}
	var cfg struct {
		Includes any      `yaml:"includes"`
		Env      []string `yaml:"env"`
		Builds   []struct {
			ID        string     `yaml:"id"`
			Env       []string   `yaml:"env"`
			Goos      []string   `yaml:"goos"`
			Tags      []string   `yaml:"tags"`
			Flags     []string   `yaml:"flags"`
			Overrides []override `yaml:"overrides"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		return nil, []string{rel + ": " + err.Error()}
	}
	if cfg.Includes != nil {
		bad = append(bad, rel+": includes: names files this reading does not follow")
	}
	// envTags reads an env list: its CGO_ENABLED value, the tag lists a GOFLAGS
	// entry names, and the problems.
	envTags := func(env []string, allowCGO bool) (cgo string, goflags [][]string, problems []string) {
		for _, e := range env {
			switch {
			case strings.HasPrefix(e, "CGO_ENABLED="):
				if !allowCGO {
					problems = append(problems, fmt.Sprintf("%q: a top-level CGO_ENABLED is not modelled (set it in the build's env)", e))
					continue
				}
				cgo = strings.TrimPrefix(e, "CGO_ENABLED=")
				if cgo != "0" && cgo != "1" {
					problems = append(problems, fmt.Sprintf("CGO_ENABLED=%q is not 0 or 1", cgo))
				}
			case strings.HasPrefix(e, "GOFLAGS=") && strings.Contains(e, "-tags"):
				lists, unres := f803bTagsIn(strings.TrimPrefix(e, "GOFLAGS="), nil)
				goflags = append(goflags, lists...)
				for _, u := range unres {
					problems = append(problems, "GOFLAGS: "+u)
				}
			case strings.Contains(e, "-tags"):
				problems = append(problems, fmt.Sprintf("env %q names -tags outside GOFLAGS", e))
			}
		}
		return cgo, goflags, problems
	}
	check := func(env, goos, tags, flags []string) (problems []string) {
		for _, v := range slices.Concat(env, goos, tags, flags) {
			if strings.Contains(v, "{{") {
				problems = append(problems, fmt.Sprintf("%q is a template, which this reading does not evaluate", v))
			}
		}
		for _, f := range flags {
			if strings.Contains(f, "-tags") {
				problems = append(problems, fmt.Sprintf("flags %q name -tags; put the list under tags:", f))
			}
		}
		for _, w := range tags {
			if !f803bTagWord().MatchString(w) {
				problems = append(problems, fmt.Sprintf("tag %q is not a build tag", w))
			}
		}
		return problems
	}
	_, topFlags, topProblems := envTags(cfg.Env, false)
	if len(topProblems) > 0 {
		bad = append(bad, rel+": top-level env: "+strings.Join(topProblems, "; "))
	}
	if len(cfg.Env) > 0 {
		if p := check(cfg.Env, nil, nil, nil); len(p) > 0 {
			bad = append(bad, rel+": top-level env: "+strings.Join(p, "; "))
		}
	}
	seen := map[string]bool{}
	add := func(name, goos, cgo string, tags []string) {
		tags = slices.Clone(tags)
		slices.Sort(tags)
		tags = slices.Compact(tags)
		k := goos + "/" + cgo + "/" + strings.Join(tags, ",")
		if !seen[k] {
			seen[k] = true
			sets = append(sets, TagSet{Name: name, GOOS: goos, CGOEnabled: cgo, Tags: tags})
		}
	}
	for i, b := range cfg.Builds {
		id := b.ID
		if id == "" {
			id = strconv.Itoa(i)
		}
		where := rel + " build " + id
		cgo, flagLists, problems := envTags(b.Env, true)
		problems = append(problems, check(b.Env, b.Goos, b.Tags, b.Flags)...)
		if len(b.Goos) == 0 {
			problems = append(problems, "names no goos (goreleaser's default list is not read here)")
		}
		if len(problems) > 0 {
			bad = append(bad, where+": "+strings.Join(problems, "; "))
			continue
		}
		for _, goos := range b.Goos {
			add("goreleaser "+id+" "+goos, goos, cgo, b.Tags)
			for _, l := range slices.Concat(flagLists, topFlags) {
				add("goreleaser "+id+" "+goos+" GOFLAGS", goos, cgo, l)
			}
		}
		for j, ov := range b.Overrides {
			owhere := fmt.Sprintf("%s override %d", where, j)
			ocgo, oflagLists, oproblems := envTags(ov.Env, true)
			oproblems = append(oproblems, check(ov.Env, []string{ov.Goos}, ov.Tags, ov.Flags)...)
			if len(oproblems) > 0 {
				bad = append(bad, owhere+": "+strings.Join(oproblems, "; "))
				continue
			}
			goses := b.Goos
			if ov.Goos != "" {
				goses = []string{ov.Goos}
			}
			tags, c := b.Tags, cgo
			if len(ov.Tags) > 0 {
				tags = ov.Tags
			}
			if ocgo != "" {
				c = ocgo
			}
			for _, goos := range goses {
				add(fmt.Sprintf("goreleaser %s override %d %s", id, j, goos), goos, c, tags)
				for _, l := range slices.Concat(oflagLists, flagLists, topFlags) {
					add(fmt.Sprintf("goreleaser %s override %d %s GOFLAGS", id, j, goos), goos, c, l)
				}
			}
		}
	}
	return sets, bad
}

// f803bExpand replaces every $(NAME) and ${NAME} in text with the value of NAME
// in vars, repeatedly (a value may name another variable, to a depth of 8). It
// returns the names it could not resolve: a name that is not in vars, a make
// function call ($(shell ...), $(if ...): a name with a space, comma or colon),
// a name still unresolved after 8 rounds, and a reference it left because vars is
// nil.
func f803bExpand(text string, vars map[string]string) (string, []string) {
	ref := f803bMakeRef()
	var bad []string
	for depth := 0; depth < 8 && ref.MatchString(text); depth++ {
		bad = bad[:0]
		text = ref.ReplaceAllStringFunc(text, func(m string) string {
			name := ref.FindStringSubmatch(m)[1]
			if v, ok := vars[name]; ok && !strings.ContainsAny(name, " ,:") {
				return v
			}
			bad = append(bad, name)
			return m
		})
		if len(bad) > 0 {
			return text, slices.Clone(bad)
		}
	}
	if ref.MatchString(text) {
		return text, []string{"(nested deeper than 8 levels)"}
	}
	return text, nil
}

// f803bTagsIn returns the build-tag lists named by -tags arguments in body, one
// sorted, deduplicated list per occurrence, and a description of every -tags
// argument it could not reduce to a concrete list ("line N: -tags <text>: why").
// A make variable reference is resolved from vars (a nil vars resolves none, for
// a file that is not a Makefile); a shell variable ($TAG, ${TAG}, $$TAG), a
// GitHub expression (${{ ... }}), backticks, a make function call, or a word that
// is not a build tag identifier is reported, not dropped. An argument whose first
// word starts with a dash is not a tag list (git's `--tags --always`) and is
// skipped, as is a comment line.
func f803bTagsIn(body string, vars map[string]string) (lists [][]string, unresolved []string) {
	flag, word := f803bTagFlag(), f803bTagWord()
	n := 0
	for line := range strings.SplitSeq(body, "\n") {
		n++
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range flag.FindAllStringSubmatch(line, -1) {
			text := m[1] + m[2] + m[3]
			if first, _, _ := strings.Cut(strings.TrimSpace(text), " "); strings.HasPrefix(first, "-") {
				continue
			}
			expanded, bad := f803bExpand(text, vars)
			if len(bad) > 0 {
				unresolved = append(unresolved, fmt.Sprintf("line %d: -tags %s: cannot resolve %s", n, text, strings.Join(bad, ", ")))
				continue
			}
			var tags []string
			ok := true
			for _, w := range strings.FieldsFunc(expanded, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
				if !word.MatchString(w) {
					unresolved = append(unresolved, fmt.Sprintf("line %d: -tags %s: %q is not a build tag (a shell or workflow variable, a function call, or a spelling this reading does not know)", n, text, w))
					ok = false
					break
				}
				tags = append(tags, w)
			}
			slices.Sort(tags)
			if tags = slices.Compact(tags); ok && len(tags) > 0 {
				lists = append(lists, tags)
			}
		}
	}
	return lists, unresolved
}

// F803bShippedTagSets returns the tag sets a binary of this repo is built with,
// for the real-binary test of the refusal: TagSets() (the one list of
// (GOOS, tag set) pairs CI and the Dockerfiles build, 03 §3.5.1 item 4) that apply to
// the core module, plus every distinct -tags list read from the repo's own
// build files (Makefile*, Dockerfile*, the workflow and pipeline YAML under
// .github/workflows and .woodpecker) that TagSets() does not already name, plus
// the release build: the repo root's .goreleaser.yml, one set per (build, goos)
// with the build's `tags:` and `env: CGO_ENABLED=` (f803bGoreleaserSets).
//
// A make variable in a tag list ($(GO_BUILD_TAGS)) is resolved from the
// assignments of the Makefile that holds it, else from the other Makefiles when
// they agree on one value; `?=` counts as the default. Make's own rule is
// followed for included files (f803bMakeVarsInto): an `include` or `-include`
// line reads the named file, when it exists, at that line, so `-include
// Makefile.local` after `GO_BUILD_TAGS := pprof` lets an existing Makefile.local
// override it and an assignment after the include line overrides Makefile.local.
// The second return is how many distinct lists the build files named. Any -tags
// argument or goreleaser build it cannot reduce to a concrete list (an undefined
// or computed variable, a shell or workflow variable, a make function, a
// goreleaser template) is an error that names each one: such a list would
// otherwise be dropped and its set never built. Not covered: a Makefile.local
// that is absent from the checkout (it is gitignored, so CI and a fresh clone
// never have one; the test sees the one on the machine that runs it), a variable
// set on the make command line or in the environment, a list a script assembles
// at run time, and goreleaser configuration in a file other than the root
// .goreleaser.yml or .goreleaser.yaml. coreRoot is the core module's directory.
// Tags only: -race is not a tag, and the race set here is the tag.
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
	type buildFile struct{ rel, body string }
	var files []buildFile
	bodies := map[string]string{} // every text file read, by repo-relative slash path
	var goreleaser []buildFile
	_, err := f803bRepoText(f803bRepoRoot(coreRoot), func(rel string, body []byte) {
		base := filepath.Base(rel)
		bodies[rel] = string(body)
		if rel == ".goreleaser.yml" || rel == ".goreleaser.yaml" {
			goreleaser = append(goreleaser, buildFile{rel, string(body)})
		}
		if strings.HasPrefix(base, "Makefile") || strings.HasPrefix(base, "Dockerfile") ||
			strings.HasPrefix(rel, ".github/workflows/") || strings.HasPrefix(rel, ".woodpecker/") {
			files = append(files, buildFile{rel, string(body)})
		}
	})
	if err != nil {
		return nil, 0, err
	}
	// Each Makefile's own variables (its included files read in place, see
	// f803bMakeVarsInto), and the values the Makefiles agree on.
	var unresolved []string
	repoRoot := f803bRepoRoot(coreRoot)
	own := map[string]map[string]string{}
	agreed := map[string]string{}
	conflict := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(filepath.Base(f.rel), "Makefile") {
			continue
		}
		env := newF803bMakeEnv()
		// make resolves a relative include against its working directory, which
		// for a Makefile is the directory it sits in (make is run there, or with
		// -C): the includer's own directory, and not the directory of an included
		// file or the repo root.
		base := path.Dir(f.rel)
		read := func(name string) (string, bool, error) {
			rel := path.Clean(path.Join(base, name))
			if path.IsAbs(name) {
				rel = ""
			}
			if body, ok := bodies[rel]; ok && rel != "" {
				return body, true, nil
			}
			disk := name
			if !path.IsAbs(name) {
				disk = filepath.Join(repoRoot, filepath.FromSlash(rel))
			}
			if _, err := os.Stat(disk); err == nil {
				return "", false, fmt.Errorf("the file exists but was not read (outside the repo, a documentation extension, a skipped directory, a binary or over 4 MiB)")
			}
			return "", false, nil
		}
		if err := f803bMakeVarsInto(env, f.rel, f.body, read, 0); err != nil {
			unresolved = append(unresolved, err.Error())
		}
		vars := env.vars
		own[f.rel] = vars
		for name, val := range own[f.rel] {
			if prev, seen := agreed[name]; seen && prev != val {
				conflict[name] = true
			}
			agreed[name] = val
		}
	}
	named := map[string][]string{}
	for _, f := range files {
		var vars map[string]string
		if mine, isMake := own[f.rel]; isMake {
			vars = map[string]string{}
			for name, val := range agreed {
				if !conflict[name] {
					vars[name] = val
				}
			}
			maps.Copy(vars, mine)
		}
		lists, bad := f803bTagsIn(f.body, vars)
		for _, tags := range lists {
			named[key(tags)] = tags
		}
		for _, b := range bad {
			unresolved = append(unresolved, f.rel+" "+b)
		}
	}
	for _, g := range goreleaser {
		gsets, gbad := f803bGoreleaserSets(g.rel, g.body)
		unresolved = append(unresolved, gbad...)
		for _, ts := range gsets {
			// A TagSets() entry with the same GOOS, CGO setting and tags is the
			// same build; do not list it twice.
			if !slices.ContainsFunc(out, func(o TagSet) bool {
				return o.Patterns == nil && o.GOOS == ts.GOOS && o.CGOEnabled == ts.CGOEnabled && key(o.Tags) == key(ts.Tags)
			}) {
				out = append(out, ts)
			}
		}
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return nil, 0, fmt.Errorf("archtest: %d -tags argument(s) or release build(s) in the repo's build files cannot be reduced to a concrete tag set, so the real-binary test of the F8-03b refusal would never build them: teach f803bTagsIn (or f803bGoreleaserSets) the spelling or write the list out:\n  %s", len(unresolved), strings.Join(unresolved, "\n  "))
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
