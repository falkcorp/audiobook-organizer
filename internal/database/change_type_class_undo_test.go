// file: internal/database/change_type_class_undo_test.go
// version: 2.0.0
// guid: 3f9c2d71-6a4e-4b8f-9e15-0d7a2c6b4e83
// last-edited: 2026-10-30

package database_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// TestIsUserDataChangeType pins the user-data set: the types whose rows carry
// one user's own data. Changing it changes who may read those rows on every
// journal route, so it is a reviewed change, not a side effect.
func TestIsUserDataChangeType(t *testing.T) {
	var got []string
	for ct, userData := range database.ExportUndoClassified() {
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
		assert.True(t, database.IsUserDataChangeType(ct), ct)
	}
	for _, ct := range []string{undo.ChangeTypeBookFileMove, undo.ChangeTypeMergeCombineJournal, "metadata_update", "", "invented_type"} {
		assert.False(t, database.IsUserDataChangeType(ct), ct)
	}
}

// TestClassifyChangeType: every constant this package classifies is known,
// with its user-data answer; the library-data literals other packages journal
// are known and not user data; anything else is unknown, which is what
// repairs.Writer refuses.
func TestClassifyChangeType(t *testing.T) {
	for ct, want := range database.ExportUndoClassified() {
		userData, known := database.ClassifyChangeType(ct)
		assert.True(t, known, ct)
		assert.Equal(t, want, userData, ct)
		assert.Equal(t, want, database.IsUserDataChangeType(ct), ct)
	}
	for _, ct := range []string{"metadata_update", "series_merge", "series_delete", "book_file_repoint", database.ChangeTypeBookFileRepoint} {
		userData, known := database.ClassifyChangeType(ct)
		assert.True(t, known, ct)
		assert.False(t, userData, ct)
	}
	for ct := range database.ExportLiteralClassified() {
		_, isConst := database.ExportUndoClassified()[ct]
		assert.False(t, isConst, "%s is a constant of this package: classify it there, not as a literal", ct)
	}
	for _, ct := range []string{"", "invented_type", "unclassified_batman_edit"} {
		userData, known := database.ClassifyChangeType(ct)
		assert.False(t, known, ct)
		assert.False(t, userData, ct)
	}
}

// TestUserDataChangeTypes_EveryConstantIsClassified: the value of every
// package-level constant named ChangeType* in this package's non-test sources
// is a key of the journal's undo table (database.UndoClassifiedChangeTypes),
// and every key of that table is the value of one, so a new change type cannot
// reach the journal routes without someone deciding whether its rows are user
// data.
func TestUserDataChangeTypes_EveryConstantIsClassified(t *testing.T) {
	declared := declaredChangeTypeConsts(t, "../undo")
	require.NotEmpty(t, declared)
	table := database.ExportUndoClassified()

	values := map[string]string{} // value -> constant name
	for name, value := range declared {
		if prev, dup := values[value]; dup {
			t.Errorf("%s and %s have the value %q", prev, name, value)
		}
		values[value] = name
	}

	var missing []string
	for value, name := range values {
		if _, ok := table[value]; !ok {
			missing = append(missing, name+"="+value)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "classify each in undoChangeTypeHoldsUserData (internal/database/change_type_class.go): true when its rows carry one user's own data")

	var stale []string
	for value := range table {
		if _, ok := values[value]; !ok {
			stale = append(stale, value)
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale, "keys of undoChangeTypeHoldsUserData that no ChangeType constant of this package has as its value")
}

// declaredChangeTypeConsts collects the ChangeType* constants declared in
// dir's non-test Go files, name to value. A constant defined as another
// package's constant (the merge journal pointers) takes its value from
// aliasedChangeTypeConsts.
func declaredChangeTypeConsts(t *testing.T, dir string) map[string]string {
	t.Helper()
	aliased := map[string]string{
		"ChangeTypeMergeCombineJournal": merge.CombineJournalPointerType,
		"ChangeTypeMergeSiblingJournal": merge.SiblingJournalPointerType,
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	out := map[string]string{}
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
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !strings.HasPrefix(id.Name, "ChangeType") {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, err := strconv.Unquote(lit.Value)
						require.NoError(t, err, id.Name)
						out[id.Name] = v
						continue
					}
					v, ok := aliased[id.Name]
					require.True(t, ok, "%s is not a string literal: add its value to aliasedChangeTypeConsts", id.Name)
					out[id.Name] = v
				}
			}
		}
	}
	return out
}
