// file: internal/importer/path_service_test.go
// version: 1.0.1
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-09

package importer

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestImportPathService_CreateImportPath_Success(t *testing.T) {
	mockDB := &database.MockStore{
		CreateImportPathFunc: func(path, name string) (*database.ImportPath, error) {
			return &database.ImportPath{Path: path, Name: name}, nil
		},
	}
	service := NewImportPathService(mockDB)

	result, err := service.CreateImportPath("/import/folder", "test")

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result == nil {
		t.Error("expected result, got nil")
	}
}

func TestImportPathService_CreateImportPath_EmptyPath(t *testing.T) {
	service := NewImportPathService(&database.MockStore{})

	_, err := service.CreateImportPath("", "test")

	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestImportPathService_ValidatePath_Success(t *testing.T) {
	service := NewImportPathService(&database.MockStore{})

	err := service.ValidatePath("/valid/path")

	if err != nil {
		t.Errorf("expected no error for valid path, got %v", err)
	}
}

func TestImportPathService_ValidatePath_Empty(t *testing.T) {
	service := NewImportPathService(&database.MockStore{})

	err := service.ValidatePath("")

	if err == nil {
		t.Error("expected error for empty path")
	}
}
