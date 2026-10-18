// file: internal/security/safepath/safepath.go
// version: 1.0.1
// guid: 2f8e4b29-9c1f-4f8b-8b6a-2c3f4e5a6b7c
// last-edited: 2026-10-18
package safepath

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafePath is an opaque type representing a path that has been validated to
// lie within a known-safe root directory. Obtain one via Join or Validate;
// never construct directly from a string.
type SafePath struct{ s string }

// String returns the validated absolute path.
func (p SafePath) String() string { return p.s }

// Join validates that joining root with parts does not escape root, then
// returns a SafePath. Returns an error if any part would escape root.
func Join(root string, parts ...string) (SafePath, error) {
	// Reject absolute path components in parts for security and clarity.
	for _, part := range parts {
		if filepath.IsAbs(part) {
			return SafePath{}, fmt.Errorf("safepath: absolute path component %q not allowed", part)
		}
	}

	elems := append([]string{root}, parts...)
	joined := filepath.Join(elems...)
	cleaned := filepath.Clean(joined)
	cleanRoot := filepath.Clean(root)
	if cleaned != cleanRoot && !strings.HasPrefix(cleaned, cleanRoot+string(filepath.Separator)) {
		return SafePath{}, fmt.Errorf("safepath: %q escapes root %q", cleaned, cleanRoot)
	}
	return SafePath{s: cleaned}, nil
}

// Validate asserts that an already-resolved path lies within root and wraps
// it as a SafePath. Use when the path was resolved by an external mechanism
// (e.g., os.Readlink) and needs validation.
func Validate(root, path string) (SafePath, error) {
	cleanRoot := filepath.Clean(root)
	cleaned := filepath.Clean(path)
	if cleaned != cleanRoot && !strings.HasPrefix(cleaned, cleanRoot+string(filepath.Separator)) {
		return SafePath{}, fmt.Errorf("safepath: %q escapes root %q", cleaned, cleanRoot)
	}
	return SafePath{s: cleaned}, nil
}
