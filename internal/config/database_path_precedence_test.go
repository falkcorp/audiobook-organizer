// file: internal/config/database_path_precedence_test.go
// version: 1.1.0
// guid: 5b2e9c14-7a38-4d6f-9e21-c0a4b7d3e582
// last-edited: 2026-10-19

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateParentDirExists_ReportsTheRealError guards the message that made
// the outage take longer than it should have.
//
// Every stat failure used to be reported as "does not exist". The first guess at
// the console was a permissions problem, and the message gave no way to confirm
// or rule it out — the sentence was the same either way. A non-directory parent
// is the cheapest errno to provoke portably (ENOTDIR); EACCES needs a non-root
// runner and would skip in CI.
func TestValidateParentDirExists_ReportsTheRealError(t *testing.T) {
	dir := t.TempDir()

	notADir := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := validateParentDirExists(filepath.Join(notADir, "child", "audiobooks.pebble"), "database_path")
	if err == nil {
		t.Fatal("expected an error when the parent is not a directory")
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error says %q — the parent is a FILE, not missing. Reporting every "+
			"stat failure as \"does not exist\" is what sent the 2026-09-09 outage "+
			"looking for a missing directory that was sitting right there.", err)
	}

	// The genuinely-missing case must still say so, or the fix has just traded
	// one wrong message for another.
	err = validateParentDirExists(filepath.Join(dir, "nope", "audiobooks.pebble"), "database_path")
	if err == nil {
		t.Fatal("expected an error when the parent is missing")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error says %q, want it to report a missing directory", err)
	}

	// And a real directory must still pass.
	if err := validateParentDirExists(filepath.Join(dir, "audiobooks.pebble"), "database_path"); err != nil {
		t.Fatalf("a valid parent directory was rejected: %v", err)
	}
}
