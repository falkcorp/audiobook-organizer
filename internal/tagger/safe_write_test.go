// file: internal/tagger/safe_write_test.go
// version: 1.1.1
// guid: 9f2b5e3a-7d41-4c08-b9e1-6a3f0d2c8b74
// last-edited: 2026-09-14

package tagger

import (
	"context"
	"errors"
	"testing"
)

// ---- test doubles ----

// staticPathChecker reports whether the path is in its protected set.
type staticPathChecker struct {
	protected map[string]bool
}

func (s *staticPathChecker) IsProtected(filePath string) bool {
	return s.protected[filePath]
}

// recordingImporter records ImportPath calls and returns a configured result.
type recordingImporter struct {
	calls      []string
	returnPath string
	returnErr  error
}

func (r *recordingImporter) ImportPath(_ context.Context, srcPath, _ string) (string, error) {
	r.calls = append(r.calls, srcPath)
	if r.returnPath != "" {
		return r.returnPath, r.returnErr
	}
	return srcPath, r.returnErr
}

// ---- tests for resolvePath ----

// TestResolvePath_NonProtected verifies that a non-protected path is returned
// unchanged without calling the importer.
func TestResolvePath_NonProtected(t *testing.T) {
	t.Parallel()

	checker := &staticPathChecker{protected: map[string]bool{"/deluge/books/foo.m4b": true}}
	importer := &recordingImporter{}
	deps := SafeWriteDeps{ProtectedCache: checker, Importer: importer}

	got, err := resolvePath(context.Background(), "/library/books/foo.m4b", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/library/books/foo.m4b" {
		t.Errorf("path = %q, want %q", got, "/library/books/foo.m4b")
	}
	if len(importer.calls) != 0 {
		t.Errorf("ImportPath called %d time(s); want 0", len(importer.calls))
	}
}

// TestResolvePath_ProtectedPathTriggersImport verifies that a protected path
// goes through ImportPath and returns the library copy.
func TestResolvePath_ProtectedPathTriggersImport(t *testing.T) {
	t.Parallel()

	protectedSrc := "/deluge/books/foo.m4b"
	libraryDest := "/library/books/foo.m4b"

	checker := &staticPathChecker{protected: map[string]bool{protectedSrc: true}}
	importer := &recordingImporter{returnPath: libraryDest}
	deps := SafeWriteDeps{ProtectedCache: checker, Importer: importer}

	got, err := resolvePath(context.Background(), protectedSrc, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != libraryDest {
		t.Errorf("path = %q, want %q", got, libraryDest)
	}
	if len(importer.calls) != 1 || importer.calls[0] != protectedSrc {
		t.Errorf("ImportPath calls = %v, want [%s]", importer.calls, protectedSrc)
	}
}

// TestResolvePath_ImportFailureSurfacesError verifies that an ImportPath
// error is propagated back to the caller.
func TestResolvePath_ImportFailureSurfacesError(t *testing.T) {
	t.Parallel()

	protectedSrc := "/deluge/books/bar.m4b"
	wantErr := errors.New("disk full")

	checker := &staticPathChecker{protected: map[string]bool{protectedSrc: true}}
	importer := &recordingImporter{returnErr: wantErr}
	deps := SafeWriteDeps{ProtectedCache: checker, Importer: importer}

	_, err := resolvePath(context.Background(), protectedSrc, deps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want to wrap %v", err, wantErr)
	}
}

// TestResolvePath_NilDeps verifies that zero-value deps return the original path.
func TestResolvePath_NilDeps(t *testing.T) {
	t.Parallel()

	path := "/library/books/qux.m4b"
	got, err := resolvePath(context.Background(), path, SafeWriteDeps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
}

// TestResolvePath_NilImporterProtectedPath verifies that a protected path with
// a nil Importer is refused: a protected file is never written in place.
func TestResolvePath_NilImporterProtectedPath(t *testing.T) {
	t.Parallel()

	protectedSrc := "/deluge/books/baz.m4b"
	checker := &staticPathChecker{protected: map[string]bool{protectedSrc: true}}
	deps := SafeWriteDeps{ProtectedCache: checker, Importer: nil}

	got, err := resolvePath(context.Background(), protectedSrc, deps)
	if !errors.Is(err, ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want ErrProtectedPathWrite", err)
	}
	if got != "" {
		t.Errorf("path = %q, want none", got)
	}
}

// TestResolvePathForWrite_Exported verifies the exported alias works.
func TestResolvePathForWrite_Exported(t *testing.T) {
	t.Parallel()

	checker := &staticPathChecker{protected: map[string]bool{"/del/a.m4b": true}}
	importer := &recordingImporter{returnPath: "/lib/a.m4b"}
	deps := SafeWriteDeps{ProtectedCache: checker, Importer: importer}

	got, err := ResolvePathForWrite(context.Background(), "/del/a.m4b", deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/lib/a.m4b" {
		t.Errorf("path = %q, want /lib/a.m4b", got)
	}
}
