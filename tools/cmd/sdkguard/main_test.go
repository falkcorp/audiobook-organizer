// file: tools/cmd/sdkguard/main_test.go
// version: 1.0.0
// guid: 9b4d1e37-6a02-45c8-bf19-8e73a5c0d264
// last-edited: 2026-08-20

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFilterInternal(t *testing.T) {
	got := filterInternal([]string{
		"fmt",
		"github.com/falkcorp/audiobook-organizer/internal/util",
		"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk",
		"github.com/falkcorp/audiobook-organizer/internal/auth",
		// Duplicates must collapse: `go list -deps` over multiple packages
		// reports a shared dependency once per root.
		"github.com/falkcorp/audiobook-organizer/internal/util",
		"  github.com/falkcorp/audiobook-organizer/internal/cache  ",
		"",
		"github.com/openai/openai-go/v3",
	})
	want := []string{
		"github.com/falkcorp/audiobook-organizer/internal/auth",
		"github.com/falkcorp/audiobook-organizer/internal/cache",
		"github.com/falkcorp/audiobook-organizer/internal/util",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterInternal() = %v, want %v", got, want)
	}
}

// TestFilterInternalRejectsPkgLookalike guards the prefix match: internal/ must
// be matched as a path segment of this module, not as a substring. A third-party
// package with "internal" in its path is not our concern.
func TestFilterInternalRejectsPkgLookalike(t *testing.T) {
	got := filterInternal([]string{
		"github.com/someoneelse/audiobook-organizer/internal/database",
		"github.com/falkcorp/audiobook-organizer/pkg/internal/thing",
	})
	if len(got) != 0 {
		t.Errorf("filterInternal() = %v, want empty", got)
	}
}

func TestMissingFrom(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []string
	}{
		{"disjoint", []string{"x", "y"}, []string{"z"}, []string{"x", "y"}},
		{"identical", []string{"x", "y"}, []string{"x", "y"}, nil},
		{"partial", []string{"x", "y"}, []string{"y"}, []string{"x"}},
		{"a empty", nil, []string{"y"}, nil},
		{"b empty", []string{"x"}, nil, []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingFrom(tt.a, tt.b); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingFrom(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestSnapshotRoundTrip asserts the header block survives -update. If it did
// not, regenerating would silently strip the file's own documentation and
// version header, which is how a tracked ratchet file decays into a bare list
// nobody can interpret.
func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "internal-deps.txt")
	header := []string{"# file: x", "# version: 1.0.0", ""}
	pkgs := []string{
		"github.com/falkcorp/audiobook-organizer/internal/auth",
		"github.com/falkcorp/audiobook-organizer/internal/cache",
	}

	if err := writeSnapshot(path, header, pkgs); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	gotHeader, gotPkgs, err := readSnapshot(path)
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if !reflect.DeepEqual(gotHeader, header) {
		t.Errorf("header = %q, want %q", gotHeader, header)
	}
	if !reflect.DeepEqual(gotPkgs, pkgs) {
		t.Errorf("pkgs = %v, want %v", gotPkgs, pkgs)
	}
}

// TestReadSnapshotMissingFile: a missing snapshot must not be an error, so the
// first -update can bootstrap the file rather than requiring someone to hand-
// create it.
func TestReadSnapshotMissingFile(t *testing.T) {
	header, pkgs, err := readSnapshot(filepath.Join(t.TempDir(), "absent.txt"))
	if err != nil {
		t.Fatalf("readSnapshot on missing file: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("pkgs = %v, want empty", pkgs)
	}
	if !reflect.DeepEqual(header, defaultHeader) {
		t.Error("missing file should yield the default header")
	}
}

// TestReadSnapshotSkipsTrailingComments checks that a comment written below the
// package list is ignored rather than parsed as a package name.
func TestReadSnapshotSkipsTrailingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.txt")
	body := "# header\n\ngithub.com/a\n# a trailing note\ngithub.com/b\n\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, pkgs, err := readSnapshot(path)
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if want := []string{"github.com/a", "github.com/b"}; !reflect.DeepEqual(pkgs, want) {
		t.Errorf("pkgs = %v, want %v", pkgs, want)
	}
}

// TestAllowedRootsAreInternalPackages keeps allowedRoots honest: an entry that
// does not match the internal prefix can never be compared against anything
// directInternalImports returns, so it would be dead configuration.
func TestAllowedRootsAreInternalPackages(t *testing.T) {
	if len(allowedRoots) == 0 {
		t.Fatal("allowedRoots is empty")
	}
	for pkg := range allowedRoots {
		if len(filterInternal([]string{pkg})) != 1 {
			t.Errorf("allowedRoots entry %q is not a project-local internal package", pkg)
		}
	}
}
