// file: internal/maintenance/jobs/fix_file_modes_test.go
// version: 1.1.0
// guid: 1f6e3a58-2d94-4c07-b8e5-9a3c7d1f4b62
// last-edited: 2026-08-14

//go:build unix

package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fixModesFixture: one 0600 file (broken — must be repaired), one 0644 file
// (healthy — must be untouched; deliberately NOT 0664, because a job that
// chmods EVERYTHING to 0664 is indistinguishable from a correctly-gated one
// when the bystander already wears the target mode — that exact blindness
// let a gate-inverting mutation survive), and one recorded path that no
// longer exists (must be skipped without failing).
func fixModesFixture(t *testing.T) (*database.MockStore, string, string) {
	t.Helper()
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.m4b")
	healthy := filepath.Join(dir, "healthy.m4b")
	for _, p := range []string{broken, healthy} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(healthy, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &database.MockStore{}
	m.GetAllBookFilesCoreFunc = func() ([]database.BookFileCore, error) {
		return []database.BookFileCore{
			{FilePath: broken},
			{FilePath: healthy},
			{FilePath: filepath.Join(dir, "vanished.m4b")},
		}, nil
	}
	return m, broken, healthy
}

func TestFixFileModes_DryRunTouchesNothing(t *testing.T) {
	store, broken, _ := fixModesFixture(t)
	j := &fixFileModesJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	info, _ := os.Stat(broken)
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("dry-run changed mode to %o", got)
	}
}

func TestFixFileModes_RepairsOnly0600(t *testing.T) {
	store, broken, healthy := fixModesFixture(t)
	j := &fixFileModesJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if info, _ := os.Stat(broken); info.Mode().Perm() != 0o664 {
		t.Fatalf("broken file mode = %o, want 664", info.Mode().Perm())
	}
	if info, _ := os.Stat(healthy); info.Mode().Perm() != 0o644 {
		t.Fatalf("healthy (0644) file mode changed to %o — the job must touch ONLY exactly-0600 files", info.Mode().Perm())
	}
}
