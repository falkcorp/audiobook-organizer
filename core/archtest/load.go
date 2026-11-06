// file: core/archtest/load.go
// version: 1.0.0
// guid: 62e56953-af8e-4e5b-a936-1a80e6b6953d
// last-edited: 2026-11-05

package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Package is one package of the loaded module, read as the union over the
// tag sets it was loaded with.
type Package struct {
	// ImportPath is the full import path.
	ImportPath string
	// Dir is the package directory.
	Dir string
	// GoFiles are the absolute paths of its non-test Go files (GoFiles and
	// CgoFiles) under any loaded tag set.
	GoFiles []string
	// TestGoFiles are the absolute paths of its _test.go files (in-package and
	// external) under any loaded tag set.
	TestGoFiles []string
	// Imports are the non-test imports, sorted and unique.
	Imports []string
	// TestImports are the imports of its test files (in-package and external),
	// sorted and unique.
	TestImports []string
}

// LoadOptions selects what Load lists.
type LoadOptions struct {
	// Dir is the module root (the directory holding go.mod).
	Dir string
	// Patterns are the go list patterns; nil means ./... (which honours the
	// module's ignore directives, so an npm tree is never listed).
	Patterns []string
	// TagSets are the sets to union over; nil means DefaultTagSet().
	TagSets []TagSet
	// EmbedOutputs are module-relative paths of build outputs that may be
	// absent (the module's committed in-image list, 03 §3.2 item 4). A
	// "pattern X: no matching files found" error is ignored only when X,
	// normalised (an all: prefix stripped, joined with the package directory
	// relative to the module root), is one of them or lies under one.
	EmbedOutputs []string
}

// listPackage is the subset of go list -json that Load reads.
type listPackage struct {
	ImportPath string
	Dir        string
	ForTest    string
	Standard   bool
	Module     *struct {
		Path string
		Main bool
	}
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	Imports      []string
	TestImports  []string
	XTestImports []string
	Error        *listError
	DepsErrors   []*listError
}

type listError struct {
	ImportStack []string
	Err         string
}

var errLoad = errors.New("archtest: load")

// embedMissingRe matches go list's error for an embed pattern with no files.
func embedMissingRe() *regexp.Regexp {
	return regexp.MustCompile(`^pattern (\S+): no matching files found$`)
}

// Load runs `go list -e -deps -test -json` once per tag set and returns the
// main module's packages keyed by import path, each the union of what the sets
// saw. Standard-library, dependency and synthetic test packages are filtered
// out. Every package error fails the load except the tolerated missing embed
// output of LoadOptions.EmbedOutputs (03 §3.5.1 item 4).
func Load(opts LoadOptions) (map[string]*Package, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("%w: LoadOptions.Dir is empty", errLoad)
	}
	sets := opts.TagSets
	if sets == nil {
		sets = DefaultTagSet()
	}
	patterns := opts.Patterns
	if patterns == nil {
		patterns = []string{"./..."}
	}

	type result struct {
		pkgs []listPackage
		err  error
	}
	results := make([]result, len(sets))
	var wg sync.WaitGroup
	// One go list per tag set: a fixed, small fan-out (the length of the one
	// tag-set list), not a library-scale loop.
	for i, set := range sets {
		pats := patterns
		if set.Patterns != nil {
			if !patternsExist(opts.Dir, set.Patterns) {
				continue
			}
			pats = set.Patterns
		}
		wg.Add(1)
		go func(i int, set TagSet, pats []string) {
			defer wg.Done()
			pkgs, err := goList(opts.Dir, set, pats)
			results[i] = result{pkgs, err}
		}(i, set, pats)
	}
	wg.Wait()

	out := map[string]*Package{}
	var problems []string
	for i, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("%w (tag set %s): %v", errLoad, sets[i].Name, r.err)
		}
		problems = append(problems, packageErrors(opts, sets[i], r.pkgs)...)
		for _, lp := range r.pkgs {
			if lp.Standard || lp.ForTest != "" || strings.HasSuffix(lp.ImportPath, ".test") ||
				lp.Module == nil || !lp.Module.Main {
				continue
			}
			p := out[lp.ImportPath]
			if p == nil {
				p = &Package{ImportPath: lp.ImportPath, Dir: lp.Dir}
				out[lp.ImportPath] = p
			}
			p.GoFiles = appendAbs(p.GoFiles, lp.Dir, lp.GoFiles, lp.CgoFiles)
			p.TestGoFiles = appendAbs(p.TestGoFiles, lp.Dir, lp.TestGoFiles, lp.XTestGoFiles)
			p.Imports = append(p.Imports, lp.Imports...)
			p.TestImports = append(p.TestImports, lp.TestImports...)
			p.TestImports = append(p.TestImports, lp.XTestImports...)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%w: go list reported package errors:\n  %s", errLoad, strings.Join(uniq(problems), "\n  "))
	}
	for _, p := range out {
		p.GoFiles = uniq(p.GoFiles)
		p.TestGoFiles = uniq(p.TestGoFiles)
		p.Imports = uniq(p.Imports)
		p.TestImports = uniq(p.TestImports)
	}
	return out, nil
}

// packageErrors returns every Error/DepsErrors entry that is not a tolerated
// missing embed output.
func packageErrors(opts LoadOptions, set TagSet, pkgs []listPackage) []string {
	re := embedMissingRe()
	tolerated := map[string]bool{} // importPath + "\x00" + err
	var problems []string
	for _, lp := range pkgs {
		if lp.Error == nil {
			continue
		}
		if m := re.FindStringSubmatch(lp.Error.Err); m != nil && embedOutputListed(opts, lp, m[1]) {
			tolerated[lp.ImportPath+"\x00"+lp.Error.Err] = true
			continue
		}
		problems = append(problems, fmt.Sprintf("[%s] %s: %s", set.Name, lp.ImportPath, lp.Error.Err))
	}
	for _, lp := range pkgs {
		for _, de := range lp.DepsErrors {
			if de == nil {
				continue
			}
			if n := len(de.ImportStack); n > 0 && tolerated[de.ImportStack[n-1]+"\x00"+de.Err] {
				continue
			}
			problems = append(problems, fmt.Sprintf("[%s] %s (dependency error): %s", set.Name, lp.ImportPath, de.Err))
		}
	}
	return problems
}

// embedOutputListed normalises an embed pattern of main-module package lp
// (the pattern joined with the package's path relative to the module root,
// read from the import path so a symlinked temp dir cannot skew it) and
// reports whether it names a listed output.
func embedOutputListed(opts LoadOptions, lp listPackage, pattern string) bool {
	if lp.Module == nil || !lp.Module.Main {
		return false
	}
	relDir, ok := Rel(lp.Module.Path, lp.ImportPath)
	if !ok {
		return false
	}
	pattern = strings.TrimPrefix(pattern, "all:")
	norm := path.Clean(path.Join(relDir, pattern))
	for _, o := range opts.EmbedOutputs {
		o = path.Clean(strings.TrimPrefix(o, "./"))
		if norm == o || strings.HasPrefix(norm, o+"/") {
			return true
		}
	}
	return false
}

func goList(dir string, set TagSet, patterns []string) ([]listPackage, error) {
	args := []string{"list", "-e", "-deps", "-test",
		"-json=ImportPath,Dir,ForTest,Standard,Module,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Imports,TestImports,XTestImports,EmbedPatterns,EmbedFiles,Error,DepsErrors"}
	if len(set.Tags) > 0 {
		args = append(args, "-tags", strings.Join(set.Tags, ","))
	}
	args = append(args, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if set.GOOS != "" {
		cmd.Env = append(cmd.Env, "GOOS="+set.GOOS)
	}
	if set.CGOEnabled != "" {
		cmd.Env = append(cmd.Env, "CGO_ENABLED="+set.CGOEnabled)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	var pkgs []listPackage
	dec := json.NewDecoder(&stdout)
	for {
		var p listPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// patternsExist reports whether every pattern's directory exists under dir.
func patternsExist(dir string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/...")
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

func appendAbs(dst []string, dir string, lists ...[]string) []string {
	for _, l := range lists {
		for _, f := range l {
			dst = append(dst, filepath.Join(dir, f))
		}
	}
	return dst
}

func uniq(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	sort.Strings(s)
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// ModuleRoot walks up from dir to the nearest directory holding a go.mod.
func ModuleRoot(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w: no go.mod above %s", errLoad, dir)
		}
		dir = parent
	}
}
