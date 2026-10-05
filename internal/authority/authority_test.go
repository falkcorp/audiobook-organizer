// file: internal/authority/authority_test.go
// version: 1.0.0
// guid: 6f4c2a85-1e9b-4d37-a8c6-0b5e7d3f9a12
// last-edited: 2026-10-04

package authority

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestLeafImports keeps this package a leaf. authorcredit, the importer,
// metafetch, applygate and the maintenance fixers are its consumers; any of
// them in this package's import graph would be a cycle. Only the standard
// library, internal/database and internal/personname are allowed.
func TestLeafImports(t *testing.T) {
	allowed := map[string]bool{
		"github.com/falkcorp/audiobook-organizer/internal/database":   true,
		"github.com/falkcorp/audiobook-organizer/internal/personname": true,
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		af, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range af.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			if !strings.Contains(path, ".") {
				continue // standard library
			}
			require.True(t, allowed[path], "%s imports %s: internal/authority must stay a leaf (put build-side code in authoritybuild)", f, path)
		}
		checked++
	}
	require.Positive(t, checked)
}

func TestKeyPrefixes_AreRegisteredFamilies(t *testing.T) {
	registered := map[string]bool{}
	for _, f := range database.KeyFamilies() {
		registered[f.Prefix] = true
	}
	for _, p := range KeyPrefixes() {
		require.True(t, registered[p], "%s is not in internal/database/keyfamilies.go", p)
		require.False(t, strings.HasPrefix(p, "author"), "%s would nest under the author families", p)
	}
}
