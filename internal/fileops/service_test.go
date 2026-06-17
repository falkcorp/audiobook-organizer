// file: internal/fileops/service_test.go
// version: 1.2.0
// guid: c9d0e1f2-a3b4-5c6d-7e8f-9a0b1c2d3e4f
// last-edited: 2026-06-17

package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

func TestFilesystemService_BrowseDirectory_Empty(t *testing.T) {
	mockStore := new(mocks.MockImportPathStore)
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{}, nil)
	fs := NewFilesystemService(mockStore)

	_, err := fs.BrowseDirectory(context.Background(), "")

	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestFilesystemService_BrowseDirectory_InvalidPath(t *testing.T) {
	mockStore := new(mocks.MockImportPathStore)
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{}, nil)
	fs := NewFilesystemService(mockStore)

	_, err := fs.BrowseDirectory(context.Background(), "/nonexistent/path/that/does/not/exist")

	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestFilesystemService_CreateExclusion_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-exclusion")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockStore := new(mocks.MockImportPathStore)
	// tmpDir must be in the allow-list for the exclusion gate to permit the write.
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{{Path: tmpDir}}, nil)
	fs := NewFilesystemService(mockStore)

	err = fs.CreateExclusion(context.Background(), tmpDir)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	excludeFile := filepath.Join(tmpDir, ".jabexclude")
	if _, err := os.Stat(excludeFile); err != nil {
		t.Errorf("expected .jabexclude file to exist, got %v", err)
	}
}

// TestFilesystemService_CreateExclusion_RejectsDisallowedPath verifies the
// allow-list gate: a path outside the configured import paths is rejected with
// ErrPathNotAllowed before any filesystem write (go/path-injection).
func TestFilesystemService_CreateExclusion_RejectsDisallowedPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-exclusion-denied")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockStore := new(mocks.MockImportPathStore)
	// Empty allow-list (and tmpDir is not under a default prefix / RootDir).
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{}, nil)
	fs := NewFilesystemService(mockStore)

	err = fs.CreateExclusion(context.Background(), tmpDir)
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("expected ErrPathNotAllowed, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, ".jabexclude")); statErr == nil {
		t.Error("exclusion file must NOT be created for a disallowed path")
	}
}

func TestFilesystemService_RemoveExclusion_NotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-remove-exclusion")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockStore := new(mocks.MockImportPathStore)
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{{Path: tmpDir}}, nil)
	fs := NewFilesystemService(mockStore)

	err = fs.RemoveExclusion(context.Background(), tmpDir)
	if err == nil {
		t.Error("expected error for nonexistent exclusion")
	}
}
