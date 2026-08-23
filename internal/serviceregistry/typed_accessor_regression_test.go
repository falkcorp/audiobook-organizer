// file: internal/serviceregistry/typed_accessor_regression_test.go
// version: 1.0.0
// guid: 77383469-1c73-49a2-898a-076cd8201792
// last-edited: 2026-08-23

package serviceregistry_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestNoRawGetForAccessorOwnedTypes is the regression guard for ARCH-8
// (TASK-087): config.GetConfig(c) and plugin.GetEventBus(c) exist so that a
// call site can no longer pair a valid key with the wrong type — e.g.
// Get[*plugin.EventBus](c, KeyConfig) type-checks today and only panics at
// the type assertion inside Get at runtime; the accessors make that pairing
// impossible to express. That guarantee is a COMPILE-TIME property, and
// nothing else in this package's tests protects it: the mutation tests pin
// the (pre-existing) Needs panic, not this. Without this guard, someone can
// add a new bare serviceregistry.Get[*config.Config](...) or
// serviceregistry.Get[*plugin.EventBus](...) call site next month, it will
// build and run fine, and the accessors' reason for existing quietly erodes
// with no test noticing.
//
// It scans raw source text rather than parsing the AST because the thing
// being checked is syntactic — "does this exact fully-qualified generic
// instantiation appear" — and every call site in this repo already writes
// it as one literal token sequence (gofmt keeps `Get[*pkg.Type](...)` on one
// line), so a parse buys no additional precision here over what
// mock_store_override_test.go needed a parser for (a structural "does the
// method body check its own field" property, which text matching cannot
// express). A false negative from reformatting is not a risk: gofmt is
// enforced in CI, and gofmt does not break this token sequence across
// lines.
func TestNoRawGetForAccessorOwnedTypes(t *testing.T) {
	root := moduleRoot(t)

	// The marker a deliberate raw Get call may carry to opt out of this
	// guard. Used exactly once today, in internal/plugin/register_test.go,
	// to compare GetEventBus's result against a direct Get[T] call under
	// the same key -- that call is the point of the test, not a missed
	// conversion.
	const allowMarker = "serviceregistry-guard:allow-raw-get"

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`serviceregistry\.Get\[\s*\*config\.Config\s*\]`),
		regexp.MustCompile(`serviceregistry\.Get\[\s*\*plugin\.EventBus\s*\]`),
	}

	skipDirs := map[string]bool{
		".git":         true,
		".worktrees":   true,
		"node_modules": true,
		"web":          true, // frontend TS/JS tree, no Go source
		"third_party":  true,
	}

	var (
		goFiles    int
		violations []string
	)
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Exclude this guard file itself: it necessarily mentions the
		// pattern text (as regexp source, not as literal Go code) and a
		// self-match would be a false positive, not a finding.
		if strings.HasSuffix(path, "typed_accessor_regression_test.go") {
			return nil
		}
		goFiles++

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, allowMarker) {
				continue
			}
			for _, re := range patterns {
				if re.MatchString(line) {
					violations = append(violations, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}

	// Positive control: without this, a broken root (moduleRoot resolved to
	// the wrong directory, a filter that excluded everything) would report
	// "0 violations" and pass while scanning nothing. This repo has
	// thousands of .go files; a few hundred is a safe, cheap floor.
	if goFiles < 200 {
		t.Fatalf("scanned only %d .go files under %s; the walk is broken, not the "+
			"code (expected 1000+). Check moduleRoot() and skipDirs.", goFiles, root)
	}

	if len(violations) > 0 {
		t.Errorf("%d call site(s) instantiate serviceregistry.Get[T] directly with "+
			"*config.Config or *plugin.EventBus instead of using config.GetConfig(c) / "+
			"plugin.GetEventBus(c) (ARCH-8, TASK-087):\n\t%s\n\n"+
			"Convert the call site to the typed accessor. If this is a deliberate "+
			"comparison against the raw call (not a missed conversion), add a "+
			"trailing comment containing %q on that line instead.",
			len(violations), strings.Join(violations, "\n\t"), allowMarker)
	}
}

// moduleRoot returns the directory containing this test file's module, i.e.
// the directory holding go.mod. Derived from runtime.Caller rather than
// os.Getwd (`go test` sets the working directory to the package under test,
// not the module root) or shelling out to `git rev-parse --show-toplevel`
// (an extra process, and a dependency on git being on PATH that a pure
// runtime.Caller walk up to go.mod does not need).
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate module root")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %s", thisFile)
		}
		dir = parent
	}
}
