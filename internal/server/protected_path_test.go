// file: internal/server/protected_path_test.go
// version: 1.0.0

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsProtectedPath_FailedDir(t *testing.T) {
	// isProtectedPath is now a method on *Server (SERVER-GLOBAL-STORE-AUDIT
	// phase 3a). A zero-value Server is fine for this test — only the
	// ".failed/" string-match branch is exercised; s.Ops() returns nil
	// so the import-path branch is skipped.
	s := &Server{}
	cases := []struct {
		path     string
		expected bool
	}{
		{"/library/.failed/Author/Book/book.m4b", true},
		{"/library/.failed/book.m4b", true},
		{"/library/Author/Book/book.m4b", false},
		{"/library/.failedish/book.m4b", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, s.isProtectedPath(tc.path), "path: %s", tc.path)
	}
}
