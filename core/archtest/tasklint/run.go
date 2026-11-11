// file: core/archtest/tasklint/run.go
// version: 1.3.0
// guid: 3353e3e5-8aaf-477a-bffd-f8c6bf6de79c
// last-edited: 2026-11-10

package tasklint

import (
	"bufio"
	"errors"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// Options configures Run: the packages to check and the analyzer settings of
// the calling module.
type Options struct {
	// Packages are go list patterns, resolved from the working directory.
	Packages []string
	// ModulePath is the calling module; only its packages are reported.
	ModulePath string
	// StorePackages are the module's store packages (Config.StorePackages).
	StorePackages []string
	// ExemptDir is a directory of entry files, read as one union: each line
	// names a file (relative to Root) whose writer-bypass and store-capture
	// findings are exempt (05-tasks §5T.8.4, the dark-conversion ratchet).
	// "" means no exemptions (core's case).
	ExemptDir string
	// ListFuncs is Config.ListFuncs: books' list functions count as
	// registration roots until the F5 flip.
	ListFuncs bool
	// LegacyDefinitions is Config.LegacyDefinitions: only books sets it, and
	// F5-18 deletes it with HEAD's engine.
	LegacyDefinitions []string
	// Constructors is Config.Constructors: the allowlist of functions that
	// may carry //tasklint:constructor.
	Constructors []string
	// Root is the directory finding paths and entry lines are relative to;
	// "" means the module root above the working directory.
	Root string
}

// Finding is one diagnostic with its file relative to Options.Root.
type Finding struct {
	File     string // slash-separated, relative to Root
	Line     int
	Column   int
	Category string
	Message  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: [%s] %s", f.File, f.Line, f.Column, f.Category, f.Message)
}

// exemptable reports the two rules the dark-conversion ratchet exempts.
func exemptable(category string) bool {
	return category == CatWriterBypass || category == CatStoreCapture
}

// Run loads opts.Packages, runs the analyzer with opts' settings and fails t
// on every finding except missing-help warnings (logged) and writer-bypass or
// store-capture findings in a file the exemption union lists. Every union line
// must name an existing file and no file may be listed twice.
func Run(t testing.TB, opts Options) {
	t.Helper()
	if opts.ModulePath == "" || len(opts.Packages) == 0 {
		t.Fatalf("tasklint.Run: ModulePath and Packages are required")
		return
	}
	root := opts.Root
	if root == "" {
		var err error
		if root, err = moduleRoot(); err != nil {
			t.Fatalf("tasklint.Run: %v", err)
			return
		}
	}
	var exempt map[string]string
	if opts.ExemptDir != "" {
		u, problems, err := ReadUnion(opts.ExemptDir)
		if err != nil {
			t.Fatalf("tasklint.Run: %v", err)
			return
		}
		for _, p := range problems {
			t.Errorf("tasklint exemptions: %s", p)
		}
		for file, owner := range u {
			if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil || st.IsDir() {
				t.Errorf("stale exemption: %s (in %s) names no existing file; delete the line", file, owner)
			}
		}
		exempt = u
	}
	cfg := DefaultConfig()
	cfg.ModulePath = opts.ModulePath
	cfg.StorePackages = opts.StorePackages
	cfg.LegacyDefinitions = opts.LegacyDefinitions
	cfg.ListFuncs = opts.ListFuncs
	cfg.Constructors = opts.Constructors
	findings, npkgs, err := Analyze(cfg, root, opts.Packages)
	if err != nil {
		t.Fatalf("tasklint.Run: %v", err)
		return
	}
	failing, exempted, warnings := 0, 0, 0
	for _, f := range findings {
		switch {
		case f.Category == CatMissingHelp:
			warnings++
			t.Log(f.String())
		case exemptable(f.Category) && exempt[f.File] != "":
			exempted++
		default:
			failing++
			t.Errorf("%s", f.String())
		}
	}
	t.Logf("tasklint: %d package(s), %d finding(s): %d failing, %d exempt (%s), %d warning(s)",
		npkgs, len(findings), failing, exempted, opts.ExemptDir, warnings)
}

// Analyze runs the analyzer over patterns and returns its findings, sorted,
// with files relative to root, and the number of packages loaded. The
// unregistered-kind rule runs once over every loaded package together, so a
// kind is registered when any of them registers it: patterns must therefore
// include each package that calls Registrar.Tasks for the kinds checked.
func Analyze(cfg Config, root string, patterns []string) ([]Finding, int, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, err
	}
	pkgs, err := packages.Load(&packages.Config{Mode: packages.LoadSyntax}, patterns...)
	if err != nil {
		return nil, 0, fmt.Errorf("load: %w", err)
	}
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, 0, fmt.Errorf("%d package error(s):\n  %s", len(loadErrs), strings.Join(loadErrs, "\n  "))
	}
	if len(pkgs) == 0 {
		return nil, 0, errors.New("no package matched; the check would be vacuous")
	}
	cfg.wholeProgram = true
	graph, err := checker.Analyze([]*analysis.Analyzer{New(cfg)}, pkgs, nil)
	if err != nil {
		return nil, 0, err
	}
	var out []Finding
	add := func(fset *token.FileSet, p token.Pos, category, msg string) {
		if !p.IsValid() { // a finding about Options, not a source position
			out = append(out, Finding{File: "(Options)", Category: category, Message: msg})
			return
		}
		pos := fset.Position(p)
		file := pos.Filename
		if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
			file = rel
		}
		out = append(out, Finding{File: filepath.ToSlash(file), Line: pos.Line, Column: pos.Column,
			Category: category, Message: msg})
	}
	for _, act := range graph.Roots {
		if act.Err != nil {
			return nil, 0, fmt.Errorf("%s: %w", act.Package.PkgPath, act.Err)
		}
		for _, d := range act.Diagnostics {
			add(act.Package.Fset, d.Pos, d.Category, d.Message)
		}
	}
	// packages.Load gives every package of one call the same FileSet.
	var units []*unit
	for _, p := range pkgs {
		if !under(p.PkgPath, cfg.ModulePath) || under(p.PkgPath, cfg.TasksPath) || under(p.PkgPath, cfg.TasksTestPath) {
			continue
		}
		u := &unit{path: p.PkgPath, info: p.TypesInfo}
		for _, f := range p.Syntax {
			if !strings.HasSuffix(p.Fset.File(f.Pos()).Name(), "_test.go") {
				u.files = append(u.files, f)
			}
		}
		units = append(units, u)
	}
	for _, f := range unregisteredKinds(&cfg, units, true) {
		add(pkgs[0].Fset, f.pos, CatUnregisteredKind, f.msg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Category < out[j].Category
	})
	return out, len(pkgs), nil
}

// moduleRoot returns the directory of the go.mod above the working directory.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}

// ParseEntry reads one entry file's lines: blank lines and lines starting with
// # are skipped; every other line must be a clean, relative, slash-separated
// path.
func ParseEntry(text string) (lines []string, problems []string) {
	sc := bufio.NewScanner(strings.NewReader(text))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, `\`) || path.IsAbs(line) || path.Clean(line) != line || line == "." || strings.HasPrefix(line, "../") || line == ".." {
			problems = append(problems, fmt.Sprintf("line %d: %q is not a clean relative path", n, line))
			continue
		}
		lines = append(lines, line)
	}
	return lines, problems
}

// ReadUnion reads every regular file of dir as an entry file and returns the
// union as path -> owning entry file name. A path in two entry files, or twice
// in one, is a problem: the entry files are disjoint.
func ReadUnion(dir string) (map[string]string, []string, error) {
	files, err := readEntryFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	return union(files)
}

func readEntryFiles(dir string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read exemption directory: %w", err)
	}
	out := map[string]string{}
	for _, e := range ents {
		if e.IsDir() {
			return nil, fmt.Errorf("exemption directory %s holds a directory %s; entry files only", dir, e.Name())
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// union folds entry-file texts (name -> text) into path -> owner.
func union(files map[string]string) (map[string]string, []string, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	u := map[string]string{}
	var problems []string
	for _, n := range names {
		lines, ps := ParseEntry(files[n])
		for _, p := range ps {
			problems = append(problems, n+": "+p)
		}
		for _, l := range lines {
			if owner, dup := u[l]; dup {
				problems = append(problems, fmt.Sprintf("%s lists %s, already listed by %s; entry files are disjoint", n, l, owner))
				continue
			}
			u[l] = n
		}
	}
	return u, problems, nil
}

// ShrinkViolations compares entry files at the base (name -> text) with the
// current ones: an entry file absent at the base, or a line absent from the
// same file at the base, is a violation; a removed file or line is fine. A nil
// base is the directory's genesis (baseEntries decides when that holds) and
// passes; an empty, non-nil base makes every current file an addition.
func ShrinkViolations(base, head map[string]string) []string {
	if base == nil {
		return nil
	}
	var out []string
	names := make([]string, 0, len(head))
	for n := range head {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		baseText, ok := base[n]
		if !ok {
			out = append(out, fmt.Sprintf("entry file %s was added; the exemption list may only shrink", n))
			continue
		}
		had := map[string]bool{}
		bl, _ := ParseEntry(baseText)
		for _, l := range bl {
			had[l] = true
		}
		hl, _ := ParseEntry(head[n])
		for _, l := range hl {
			if !had[l] {
				out = append(out, fmt.Sprintf("%s gained %s; the exemption list may only shrink", n, l))
			}
		}
	}
	return out
}

// ShrinkBaseEnv names the environment variable that gives CheckShrinkOnly its
// base commit explicitly. CI sets it to merge_group.base_sha in the merge
// queue and to the first parent of the PR's merge ref on a pull request, and
// fetches that commit at depth 1 (.github/workflows/ci.yml).
const ShrinkBaseEnv = "TASKLINT_SHRINK_BASE"

// CheckShrinkOnly fails t when the entry files in dir (relative to the working
// directory) gained a file or a line since the base (05-tasks §5T.8.4: the
// exemption list may only shrink). The base is ShrinkBaseEnv's commit when it
// is set, else `git merge-base HEAD origin/main`, else, in a shallow checkout
// where no merge base is reachable, origin/main itself (baseEntries). Without
// git or a base it skips with a message, and fails when CI is "true".
func CheckShrinkOnly(t testing.TB, dir string) {
	t.Helper()
	base, err := baseEntries(dir)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("tasklint shrink-only: %v (CI needs the base commit in the checkout)", err)
			return
		}
		t.Skipf("tasklint shrink-only: %v; skipped outside CI", err)
		return
	}
	head, err := readEntryFiles(dir)
	if err != nil {
		t.Fatalf("tasklint shrink-only: %v", err)
		return
	}
	for _, v := range ShrinkViolations(base, head) {
		t.Errorf("%s", v)
	}
}

// baseEntries returns dir's entry files at the base commit (CheckShrinkOnly
// names it), or nil at the directory's genesis.
//
// The fallback to origin/main in a shallow checkout is the last resort: the
// shallow boundary cuts HEAD's parents off, so no merge base is reachable, and
// a PR merge ref or merge-queue commit contains origin/main. It is exact only
// while main has not moved since that commit was made; a line main dropped in
// between reads as gained, and rerunning the job does not help, since a rerun
// tests the same commit against main's newer tip. CI therefore passes the
// exact base through ShrinkBaseEnv, and the fallback applies only where it
// cannot (a push to main, where origin/main is HEAD).
//
// When the directory is absent at the base, it is new or recreated. Genesis
// (nil, so nothing is a violation) holds only when the directory never existed
// in the base's history and the base's tests of the working directory's
// package do not call CheckShrinkOnly yet, which is the change that introduces
// both the ratchet and its list. Otherwise the base is empty and every entry
// file counts as added: a recreated directory fails, and so does one created
// after the ratchet was in place.
func baseEntries(dir string) (map[string]string, error) {
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
			}
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	mb, err := shrinkBase(git)
	if err != nil {
		return nil, err
	}
	prefix, err := git("rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	rel := path.Clean(prefix + filepath.ToSlash(dir))
	list, err := git("ls-tree", "--full-tree", "--name-only", mb, rel+"/")
	if err != nil {
		return nil, err
	}
	if list == "" {
		existed, err := git("rev-list", "-1", mb, "--", rel)
		if err != nil {
			return nil, err
		}
		callers, err := gitGrepAny(mb, "CheckShrinkOnly(", ":(glob)"+prefix+"*_test.go")
		if err != nil {
			return nil, err
		}
		if existed == "" && !callers {
			return nil, nil
		}
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for p := range strings.SplitSeq(list, "\n") {
		text, err := git("show", mb+":"+p)
		if err != nil {
			return nil, err
		}
		out[path.Base(p)] = text
	}
	return out, nil
}

// shrinkBase resolves CheckShrinkOnly's base commit: ShrinkBaseEnv's commit,
// else the merge base of HEAD and origin/main, else origin/main in a shallow
// checkout.
func shrinkBase(git func(args ...string) (string, error)) (string, error) {
	if b := strings.TrimSpace(os.Getenv(ShrinkBaseEnv)); b != "" {
		sha, err := git("rev-parse", "--verify", "--quiet", b+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("%s=%s is not a commit in this checkout (fetch it: git fetch --no-tags --depth=1 origin %s): %w", ShrinkBaseEnv, b, b, err)
		}
		return sha, nil
	}
	mb, err := git("merge-base", "HEAD", "origin/main")
	if err == nil {
		return mb, nil
	}
	if shallow, serr := git("rev-parse", "--is-shallow-repository"); serr != nil || shallow != "true" {
		return "", err
	}
	sha, ferr := git("rev-parse", "--verify", "--quiet", "origin/main^{commit}")
	if ferr != nil {
		return "", fmt.Errorf("shallow checkout without %s or origin/main (fetch it: git fetch --no-tags --depth=1 origin main): %w", ShrinkBaseEnv, ferr)
	}
	return sha, nil
}

// gitGrepAny reports whether any file of commit matching pathspec contains the
// fixed string text. git grep exits 1 when nothing matches, which is not an
// error here; any other failure is.
func gitGrepAny(commit, text, pathspec string) (bool, error) {
	err := exec.Command("git", "grep", "-q", "-F", text, commit, "--", pathspec).Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git grep %s in %s: %w", text, commit, err)
}
