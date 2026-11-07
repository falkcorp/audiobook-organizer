// file: internal/util/path.go
// version: 1.1.0
// guid: 7a3b2c1d-4e5f-6789-abcd-ef0123456789
// last-edited: 2026-05-01

package library

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafeJoin joins root with parts, cleans the result, and returns an error if
// the resolved path would escape root. Use this whenever joining paths that
// include values from user input, the database, or the filesystem.
func SafeJoin(root string, parts ...string) (string, error) {
	elems := append([]string{root}, parts...)
	joined := filepath.Join(elems...)
	cleaned := filepath.Clean(joined)
	cleanRoot := filepath.Clean(root)
	if cleaned != cleanRoot && !strings.HasPrefix(cleaned, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root %q", cleaned, cleanRoot)
	}
	return cleaned, nil
}

// WithinRoot reports whether path is equal to root or is directly contained
// within root. Both arguments are cleaned before comparison.
func WithinRoot(path, root string) bool {
	c := filepath.Clean(path)
	r := filepath.Clean(root)
	return c == r || strings.HasPrefix(c, r+string(filepath.Separator))
}
