// file: internal/undo/user_data_test.go
// version: 1.2.0
// guid: 3f9c2d71-6a4e-4b8f-9e15-0d7a2c6b4e83
// last-edited: 2026-10-25

package undo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIsUserDataChangeType pins the user-data set: the types whose rows carry
// one user's own data. Changing it changes who may read those rows on every
// journal route, so it is a reviewed change, not a side effect.
func TestIsUserDataChangeType(t *testing.T) {
	var got []string
	for ct, userData := range changeTypeHoldsUserData {
		if userData {
			got = append(got, ct)
		}
	}
	assert.ElementsMatch(t, []string{
		"user_book_state_set",
		"user_state_follow",
		"user_content_follow",
		"repair_owner_apply",
		"master_list_elsewhere_create",
	}, got)

	for _, ct := range got {
		assert.True(t, IsUserDataChangeType(ct), ct)
	}
	for _, ct := range []string{ChangeTypeBookFileMove, ChangeTypeMergeCombineJournal, "metadata_update", "", "invented_type"} {
		assert.False(t, IsUserDataChangeType(ct), ct)
	}
}

// TestClassifyChangeType: every constant this package classifies is known,
// with its user-data answer; the library-data literals other packages journal
// are known and not user data; anything else is unknown, which is what
// repairs.Writer refuses.
func TestClassifyChangeType(t *testing.T) {
	for ct, want := range changeTypeHoldsUserData {
		userData, known := ClassifyChangeType(ct)
		assert.True(t, known, ct)
		assert.Equal(t, want, userData, ct)
		assert.Equal(t, want, IsUserDataChangeType(ct), ct)
	}
	for _, ct := range []string{"metadata_update", "series_merge", "series_delete", database.ChangeTypeBookFileRepoint} {
		userData, known := ClassifyChangeType(ct)
		assert.True(t, known, ct)
		assert.False(t, userData, ct)
	}
	for ct := range libraryDataLiteralChangeTypes {
		_, isConst := changeTypeHoldsUserData[ct]
		assert.False(t, isConst, "%s is a constant of this package: classify it there, not as a literal", ct)
	}
	for _, ct := range []string{"", "invented_type", "unclassified_batman_edit"} {
		userData, known := ClassifyChangeType(ct)
		assert.False(t, known, ct)
		assert.False(t, userData, ct)
	}
}

// TestUserDataChangeTypes_EveryConstantIsClassified: every package-level
// constant named ChangeType* in this package's non-test sources is a key of
// changeTypeHoldsUserData, so a new change type cannot reach the journal
// routes without someone deciding whether its rows are user data.
func TestUserDataChangeTypes_EveryConstantIsClassified(t *testing.T) {
	declared := declaredChangeTypeConsts(t, ".")
	require.NotEmpty(t, declared)
	classified := classifiedChangeTypeKeys(t, "user_data.go")

	var missing []string
	for name := range declared {
		if !classified[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "classify each in changeTypeHoldsUserData (user_data.go): true when its rows carry one user's own data")

	var stale []string
	for name := range classified {
		if !declared[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale, "keys of changeTypeHoldsUserData that name no ChangeType constant of this package")

	// Two constants with one value would collapse to one map entry, and the
	// count check below would then miss a constant.
	assert.Len(t, changeTypeHoldsUserData, len(classified))
}

// declaredChangeTypeConsts collects the names of the package-level constants
// called ChangeType* declared in dir's non-test Go files.
func declaredChangeTypeConsts(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		require.NoError(t, err, name)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					if strings.HasPrefix(id.Name, "ChangeType") {
						out[id.Name] = true
					}
				}
			}
		}
	}
	return out
}

// classifiedChangeTypeKeys returns the identifiers used as keys of the
// changeTypeHoldsUserData literal in file.
func classifiedChangeTypeKeys(t *testing.T, file string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	var lit *ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "changeTypeHoldsUserData" || len(vs.Values) != 1 {
			return true
		}
		lit, _ = vs.Values[0].(*ast.CompositeLit)
		return false
	})
	require.NotNil(t, lit, "changeTypeHoldsUserData literal not found in %s", file)
	out := map[string]bool{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		require.True(t, ok)
		id, ok := kv.Key.(*ast.Ident)
		require.True(t, ok, "every key of changeTypeHoldsUserData is a ChangeType constant, not a literal")
		require.False(t, out[id.Name], "%s is classified twice", id.Name)
		out[id.Name] = true
	}
	return out
}
