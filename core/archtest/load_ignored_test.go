// file: core/archtest/load_ignored_test.go
// version: 1.0.0
// guid: 2f9d4b86-1c37-4a50-9e84-7b3a6d0c5e12
// last-edited: 2026-11-15

package archtest

import (
	"path/filepath"
	"testing"
)

// TestLoadReportsFilesNoTagSetBuilds (round 9, n-2): a non-test file that build
// constraints exclude under every loaded tag set is in IgnoredGoFiles, a file
// that one tag set compiles is in GoFiles and not in IgnoredGoFiles, and no test
// file is in either list.
func TestLoadReportsFilesNoTagSetBuilds(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.27.0\n",
		"a/a.go":         "package a\n",
		"a/never.go":     "//go:build neverbuilt\n\npackage a\n",
		"a/bench.go":     "//go:build bench\n\npackage a\n",
		"a/x_test.go":    "//go:build neverbuilt\n\npackage a\n",
		"a/other_ign.go": "//go:build ignore\n\npackage main\n",
	})
	pkgs, err := Load(LoadOptions{Dir: root, TagSets: TagSets()})
	if err != nil {
		t.Fatal(err)
	}
	p := pkgs["example.com/m/a"]
	if p == nil {
		t.Fatalf("package a not loaded: %v", pkgs)
	}
	base := func(fs []string) (out []string) {
		for _, f := range fs {
			out = append(out, filepath.Base(f))
		}
		return out
	}
	if got, want := base(p.IgnoredGoFiles), []string{"never.go", "other_ign.go"}; !equalStrings(got, want) {
		t.Errorf("IgnoredGoFiles = %v, want %v", got, want)
	}
	if got, want := base(p.GoFiles), []string{"a.go", "bench.go"}; !equalStrings(got, want) {
		t.Errorf("GoFiles = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
