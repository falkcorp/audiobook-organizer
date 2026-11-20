// file: core/library/perms_test.go
// version: 1.1.0
// guid: a9d57772-d002-4659-84b0-b9aa4f20fa62
// last-edited: 2026-11-19

package library

import "testing"

func TestCoverage_PermConstants(t *testing.T) {
	if DirMode != 0o775 {
		t.Errorf("DirMode = %o, want 775", DirMode)
	}
	if LedgerDirMode != 0o750 {
		t.Errorf("LedgerDirMode = %o, want 750", LedgerDirMode)
	}
	if FileMode != 0o664 {
		t.Errorf("FileMode = %o, want 664", FileMode)
	}
	if SecretFileMode != 0o600 {
		t.Errorf("SecretFileMode = %o, want 600", SecretFileMode)
	}
}
