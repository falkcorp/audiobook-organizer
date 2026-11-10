// file: internal/importer/path_service.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9b
// last-edited: 2026-11-09

package importer

import (
	"errors"
	"fmt"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

type ImportPathService struct {
	db database.ImportPathStore
}

func NewImportPathService(db database.ImportPathStore) *ImportPathService {
	return &ImportPathService{db: db}
}

var ErrImportPathEmpty = errors.New("import path cannot be empty")

// ValidatePath validates that an import path is not empty
func (ips *ImportPathService) ValidatePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return ErrImportPathEmpty
	}
	return nil
}

// CreateImportPath creates a new import path in the database
func (ips *ImportPathService) CreateImportPath(path, name string) (*database.ImportPath, error) {
	if err := ips.ValidatePath(path); err != nil {
		return nil, err
	}

	return ips.db.CreateImportPath(path, name)
}

// UpdateImportPathEnabled updates the enabled status of an import path
func (ips *ImportPathService) UpdateImportPathEnabled(id int, enabled bool) error {
	path, err := ips.db.GetImportPathByID(id)
	if err != nil || path == nil {
		return fmt.Errorf("import path not found")
	}

	path.Enabled = enabled
	return ips.db.UpdateImportPath(id, path)
}

// GetImportPath retrieves an import path by ID
func (ips *ImportPathService) GetImportPath(id int) (*database.ImportPath, error) {
	path, err := ips.db.GetImportPathByID(id)
	if err != nil || path == nil {
		return nil, fmt.Errorf("import path not found")
	}
	return path, nil
}
