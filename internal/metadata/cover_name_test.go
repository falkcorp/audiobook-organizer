// file: internal/metadata/cover_name_test.go
// version: 1.0.0
// guid: aa2d3d21-1e19-44a1-9bba-3557328a5caf
// last-edited: 2026-09-12

package metadata

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cover filename is built from a validated id and an allow-listed
// extension only. Traversal in either part, and any extension outside
// coverExtensions, is refused.
func TestCoverFileName_RejectsTraversalAndUnlistedExtensions(t *testing.T) {
	for _, ext := range coverExtensions {
		name, err := coverFileName("01J8ZK3Q9V", ext)
		require.NoError(t, err, ext)
		assert.Equal(t, "01J8ZK3Q9V"+ext, name)
	}
	bad := []struct{ id, ext string }{
		{"../etc", ".jpg"},
		{"..", ".jpg"},
		{"a/b", ".png"},
		{"a..b", ".jpg"},
		{"01J8ZK3Q9V", "/../x.jpg"},
		{"01J8ZK3Q9V", ".jpg/../../x"},
		{"01J8ZK3Q9V", "../"},
		{"01J8ZK3Q9V", ".exe"},
		{"01J8ZK3Q9V", ".svg"},
		{"01J8ZK3Q9V", ".JPG"},
		{"01J8ZK3Q9V", ""},
	}
	for _, tc := range bad {
		_, err := coverFileName(tc.id, tc.ext)
		assert.Error(t, err, "id %q ext %q must be refused", tc.id, tc.ext)
	}
}

// coverPathIn only returns a direct child of the covers directory.
func TestCoverPathIn_KeepsTheFileInsideCoversDir(t *testing.T) {
	dir := t.TempDir()
	p, err := coverPathIn(dir, "b1.jpg")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "b1.jpg"), p)
	for _, name := range []string{"../b1.jpg", "sub/b1.jpg", "/etc/passwd", ".", "..", ""} {
		_, err := coverPathIn(dir, name)
		assert.Error(t, err, "name %q must be refused", name)
	}
}
