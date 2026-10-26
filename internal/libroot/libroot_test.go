// file: internal/libroot/libroot_test.go
// version: 1.0.0
// guid: 8e1c5a73-d2b4-4f69-a0e8-3b7f9d1c6a25
// last-edited: 2026-10-25

package libroot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMounted(t *testing.T) {
	populated := t.TempDir()
	if err := os.Mkdir(filepath.Join(populated, "Gotham"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		root    string
		wantErr string
	}{
		{"unset", "", "not configured"},
		{"missing directory", filepath.Join(t.TempDir(), "nope"), "unreadable"},
		{"empty directory (unmounted share)", t.TempDir(), "is empty"},
		{"populated directory", populated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Mounted(tc.root)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Mounted(%q) = %v, want nil", tc.root, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Mounted(%q) = %v, want an error containing %q", tc.root, err, tc.wantErr)
			}
		})
	}
}

// ReportPath creates the report directory, and that alone makes an unmounted
// (empty) root look populated. Callers must therefore run Mounted first; this
// pins the hazard so the ordering rule in the doc comments has a test behind it.
func TestReportPathMakesAnEmptyRootLookMounted(t *testing.T) {
	root := t.TempDir()
	if err := Mounted(root); err == nil {
		t.Fatal("empty root must read as unmounted before any report dir exists")
	}
	if _, err := ReportPath(root, "", "x.tsv"); err != nil {
		t.Fatalf("ReportPath: %v", err)
	}
	if err := Mounted(root); err != nil {
		t.Fatalf("expected the created %s dir to make the root non-empty: %v", ReportDirName, err)
	}
}

func TestReportPath(t *testing.T) {
	root := t.TempDir()
	got, err := ReportPath(root, "", "backfill-file-hashes-op1.tsv")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ReportDirName, "backfill-file-hashes-op1.tsv"); got != want {
		t.Fatalf("ReportPath = %q, want %q", got, want)
	}
	if st, err := os.Stat(filepath.Dir(got)); err != nil || !st.IsDir() {
		t.Fatalf("report dir not created: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(got)); len(ents) != 0 {
		t.Fatalf("the write probe must be removed, found %d entries", len(ents))
	}

	explicit := filepath.Join(t.TempDir(), "sub", "r.tsv")
	if got, err := ReportPath("", explicit, "ignored.tsv"); err != nil || got != explicit {
		t.Fatalf("explicit path: got %q, %v", got, err)
	}

	if _, err := ReportPath("", "", "r.tsv"); err == nil {
		t.Fatal("no root and no explicit path must fail")
	}
	if _, err := ReportPath("relative/root", "", "r.tsv"); err == nil {
		t.Fatal("a relative root must fail")
	}
}

func TestReportPathUnwritableDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := ReportPath(root, "", "r.tsv"); err == nil {
		t.Fatal("a read-only report dir must be refused before any work")
	}
}
