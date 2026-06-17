// file: internal/fileops/service.go
// version: 1.2.0
// guid: b8c9d0e1-f2a3-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-06-17

package fileops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
)

// ErrPathNotAllowed is returned by BrowseDirectory when the requested path
// is outside the configured allowlist.
var ErrPathNotAllowed = errors.New("path not in allowed directories")

// defaultBrowseAllowPrefixes covers common Linux desktop/server and Docker layouts.
var defaultBrowseAllowPrefixes = []string{
	"/home",
	"/media",
	"/mnt",
	"/audiobooks",
	"/data",
}

type FilesystemService struct {
	db database.ImportPathStore
}

func NewFilesystemService(db database.ImportPathStore) *FilesystemService {
	return &FilesystemService{db: db}
}

// isAllowedPath checks if absPath is within allowed prefixes or import paths.
func isAllowedPath(absPath string, importPaths []database.ImportPath) bool {
	allowed := make([]string, 0, len(defaultBrowseAllowPrefixes)+len(importPaths)+1)
	allowed = append(allowed, defaultBrowseAllowPrefixes...)

	if config.AppConfig.RootDir != "" {
		allowed = append(allowed, config.AppConfig.RootDir)
	}

	for _, importPath := range importPaths {
		if importPath.Path != "" {
			allowed = append(allowed, importPath.Path)
		}
	}

	for _, prefix := range allowed {
		if prefix == "" {
			continue
		}
		prefix = strings.TrimRight(prefix, string(os.PathSeparator))
		if absPath == prefix || strings.HasPrefix(absPath, prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// IsAllowedPath reports whether absPath lies within the configured allow-list
// (default prefixes + RootDir + registered import paths). Exported so callers in
// other packages that perform filesystem operations on request-supplied paths
// can reuse the same gate BrowseDirectory uses.
func IsAllowedPath(absPath string, importPaths []database.ImportPath) bool {
	return isAllowedPath(absPath, importPaths)
}

// ValidateUserPath resolves path to an absolute form and verifies it is within
// the allow-list, returning ErrPathNotAllowed otherwise. It is the one-call
// guard for handlers/services that accept a filesystem path from a request and
// then read, write, hash, or scan it (go/path-injection).
func ValidateUserPath(db database.ImportPathStore, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	importPaths, err := db.GetAllImportPaths()
	if err != nil {
		return "", fmt.Errorf("failed to check allowed paths: %w", err)
	}
	if !isAllowedPath(absPath, importPaths) {
		return "", ErrPathNotAllowed
	}
	return absPath, nil
}

type FileInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size,omitempty"`
	ModTime  int64  `json:"mod_time,omitempty"`
	Excluded bool   `json:"excluded"`
}

type BrowseResult struct {
	Path     string         `json:"path"`
	Items    []FileInfo     `json:"items"`
	Count    int            `json:"count"`
	DiskInfo map[string]any `json:"disk_info"`
}

func (fs *FilesystemService) BrowseDirectory(_ context.Context, path string) (*BrowseResult, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// Security: Check if path is in allowed directories
	importPaths, err := fs.db.GetAllImportPaths()
	if err != nil {
		return nil, fmt.Errorf("failed to check allowed paths: %w", err)
	}

	if !isAllowedPath(absPath, importPaths) {
		return nil, ErrPathNotAllowed
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	items := []FileInfo{}
	for _, entry := range entries {
		sp, err := safepath.Join(absPath, entry.Name())
		if err != nil {
			// Skip entries that resolve outside the allowed path
			continue
		}
		fullPath := sp.String()
		info, err := entry.Info()
		if err != nil {
			continue
		}

		excluded := false
		if entry.IsDir() {
			spExcl, err := safepath.Join(fullPath, ".jabexclude")
			if err == nil {
				if _, err := os.Stat(spExcl.String()); err == nil {
					excluded = true
				}
			}
		}

		item := FileInfo{
			Name:     entry.Name(),
			Path:     fullPath,
			IsDir:    entry.IsDir(),
			Excluded: excluded,
		}

		if !entry.IsDir() {
			item.Size = info.Size()
			item.ModTime = info.ModTime().Unix()
		}

		items = append(items, item)
	}

	diskInfo := map[string]any{}
	if stat, err := os.Stat(absPath); err == nil {
		diskInfo = map[string]any{
			"exists":   true,
			"readable": stat.Mode().Perm()&0400 != 0,
			"writable": stat.Mode().Perm()&0200 != 0,
		}
	}

	return &BrowseResult{
		Path:     absPath,
		Items:    items,
		Count:    len(items),
		DiskInfo: diskInfo,
	}, nil
}

func (fs *FilesystemService) CreateExclusion(_ context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Security: only allow exclusion files inside allowed directories (same gate
	// as BrowseDirectory) before touching the filesystem (go/path-injection).
	importPaths, err := fs.db.GetAllImportPaths()
	if err != nil {
		return fmt.Errorf("failed to check allowed paths: %w", err)
	}
	if !isAllowedPath(absPath, importPaths) {
		return ErrPathNotAllowed
	}

	stat, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("path not found or inaccessible: %w", err)
	}

	if !stat.IsDir() {
		return fmt.Errorf("path must be a directory")
	}

	sp, err := safepath.Join(absPath, ".jabexclude")
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	return os.WriteFile(sp.String(), []byte(""), 0644)
}

func (fs *FilesystemService) RemoveExclusion(_ context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Security: same allow-list gate as CreateExclusion (go/path-injection).
	importPaths, err := fs.db.GetAllImportPaths()
	if err != nil {
		return fmt.Errorf("failed to check allowed paths: %w", err)
	}
	if !isAllowedPath(absPath, importPaths) {
		return ErrPathNotAllowed
	}

	sp, err := safepath.Join(absPath, ".jabexclude")
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	if err := os.Remove(sp.String()); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("exclusion not found")
		}
		return fmt.Errorf("failed to remove exclusion: %w", err)
	}
	return nil
}
