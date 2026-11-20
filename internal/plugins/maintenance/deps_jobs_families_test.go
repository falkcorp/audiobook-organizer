// file: internal/plugins/maintenance/deps_jobs_families_test.go
// version: 0.1.0
// guid: 5a1c8e92-4d37-4b60-9f2e-0c6b3d7a1e58
// last-edited: 2026-11-20

package maintenance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mergeKeyPrefixes reads the merge package's source for every string constant
// that is a raw key family: a value that starts with "merge" and ends in ":".
// The constants are unexported, so the source is the only place a test outside
// the package can read them from.
func mergeKeyPrefixes(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "merge")
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	re := regexp.MustCompile(`^merge[:_][a-z0-9:_-]*:$`)
	found := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		require.NoError(t, err)
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, sp := range g.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING || i >= len(vs.Names) {
						continue
					}
					val, err := strconv.Unquote(lit.Value)
					require.NoError(t, err)
					if re.MatchString(val) {
						found[val] = vs.Names[i].Name
					}
				}
			}
		}
	}
	return found
}

// TestJobsRawFamiliesCoverEveryMergePrefix: every raw key family the merge
// package declares is routed by jobsRawFamilies, and no routed merge family has
// stopped existing. The prefixes are copied into jobsRawFamilies; this is what
// keeps the copy equal to the constants.
func TestJobsRawFamiliesCoverEveryMergePrefix(t *testing.T) {
	declared := mergeKeyPrefixes(t)
	require.NotEmpty(t, declared, "the scan found no merge key family: the pattern or the path is wrong")
	routed := map[string]bool{}
	for _, f := range jobsRawFamilies {
		routed[f.prefix] = true
	}
	for prefix, name := range declared {
		require.True(t, routed[prefix], "merge.%s (%q) has no family in jobsRawFamilies: a job writing it is refused", name, prefix)
	}
	for prefix := range routed {
		if strings.HasPrefix(prefix, "merge") {
			_, ok := declared[prefix]
			require.True(t, ok, "jobsRawFamilies routes %q, which no merge constant declares any more", prefix)
		}
	}
}
