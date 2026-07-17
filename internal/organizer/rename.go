// file: internal/organizer/rename.go
// version: 1.2.0
// guid: e5f6a7b8-c9d0-e1f2-a3b4-c5d6e7f8a9b0
// last-edited: 2026-07-17

package organizer

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	enhanced "github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// RenameService handles preview and execution of file rename + tag write operations.
type RenameService struct {
	db Store

	// IsProtectedPath checks whether a file path is protected (e.g. import/iTunes).
	// Set by the server package after construction.
	IsProtectedPath func(string) bool

	// ResolveAuthorAndSeriesNames resolves author and series names from a book.
	// Returns (authorName, seriesName).
	// Set by the server package after construction.
	ResolveAuthorAndSeriesNames func(*database.Book) (string, string)

	// FilterUnchangedTags filters out tags that already match on-disk values.
	// Breaks the metafetch import cycle.
	FilterUnchangedTags func(filePath string, tags map[string]interface{}) map[string]interface{}

	// ComputeITunesPath computes the iTunes-compatible path for a file.
	// Breaks the metafetch import cycle.
	ComputeITunesPath func(filePath string) string
}

// NewRenameService creates a new RenameService.
func NewRenameService(db Store) *RenameService {
	return &RenameService{
		db: db,
		// Defaults that can be overridden by server package
		IsProtectedPath: func(string) bool { return false },
		ResolveAuthorAndSeriesNames: func(book *database.Book) (string, string) {
			authorName := ""
			if book.Author != nil {
				authorName = book.Author.Name
			}
			seriesName := ""
			if book.Series != nil {
				seriesName = book.Series.Name
			}
			return authorName, seriesName
		},
		FilterUnchangedTags: func(_ string, tags map[string]interface{}) map[string]interface{} {
			return tags // default: no filtering
		},
		ComputeITunesPath: func(_ string) string { return "" },
	}
}

// TagChange describes a single metadata field difference between current file tags
// and the proposed database-driven values.
type TagChange struct {
	Field    string `json:"field"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}

// RenamePreview is the response for a rename preview request.
type RenamePreview struct {
	BookID       string      `json:"book_id"`
	CurrentPath  string      `json:"current_path"`
	ProposedPath string      `json:"proposed_path"`
	TagChanges   []TagChange `json:"tag_changes"`
}

// RenameApplyResult is the response for a rename apply request.
type RenameApplyResult struct {
	BookID      string `json:"book_id"`
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	TagsWritten int    `json:"tags_written"`
	Message     string `json:"message"`
}

// PreviewRename computes what a rename + tag write would do without executing it.
func (rs *RenameService) PreviewRename(bookID string) (*RenamePreview, error) {
	book, err := rs.db.GetBookByID(bookID)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found: %s", bookID)
	}

	org := NewOrganizer(&config.AppConfig)
	proposedPath, err := org.GenerateTargetPath(book)
	if err != nil {
		return nil, fmt.Errorf("failed to compute proposed path: %w", err)
	}

	// Resolve author and narrator names for tag comparison
	authorName, _ := rs.ResolveAuthorAndSeriesNames(book)
	narratorStr := rs.resolveNarratorNames(bookID, book)

	// Build tag changes by comparing current file state to proposed metadata
	tagChanges := rs.computeTagChanges(book, authorName, narratorStr)

	return &RenamePreview{
		BookID:       bookID,
		CurrentPath:  book.FilePath,
		ProposedPath: proposedPath,
		TagChanges:   tagChanges,
	}, nil
}

// ApplyRename executes the rename: write tags via ffmpeg, move/rename file, update DB.
// Records OperationChanges for undo support.
func (rs *RenameService) ApplyRename(bookID, operationID string) (*RenameApplyResult, error) {
	book, err := rs.db.GetBookByID(bookID)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found: %s", bookID)
	}

	org := NewOrganizer(&config.AppConfig)
	proposedPath, err := org.GenerateTargetPath(book)
	if err != nil {
		return nil, fmt.Errorf("failed to compute proposed path: %w", err)
	}

	oldPath := book.FilePath
	tagsWritten := 0

	// Build tag metadata for later use
	authorName, _ := rs.ResolveAuthorAndSeriesNames(book)
	narratorStr := rs.resolveNarratorNames(bookID, book)
	tagMeta := rs.BuildTagMetadata(book, authorName, narratorStr)

	// Step 1: If source is in a protected path (import path / iTunes), we must
	// copy/hardlink to the organized library FIRST, then write tags to the copy.
	// We never modify files in protected locations.
	sourceIsProtected := rs.IsProtectedPath(oldPath)
	tagWriteTarget := oldPath

	if sourceIsProtected && oldPath != proposedPath {
		// Copy/hardlink to organized library before writing tags
		if err := rs.hardlinkOrCopy(oldPath, proposedPath); err != nil {
			return nil, fmt.Errorf("failed to copy protected file to library: %w", err)
		}
		slog.Info("rename copied protected file → (source untouched)", "oldPath", oldPath, "proposedPath", proposedPath)
		tagWriteTarget = proposedPath // Write tags to the copy, not the original
	}

	// Write tags to the target file (never the protected source).
	//
	// Only write fields that actually changed. Without this filter,
	// every organize pass would rewrite every tag on every file
	// regardless of whether the DB value and the on-disk value
	// already match — wasted disk I/O, wasted mtime churn (which
	// poisons the scanner's mtime skip cache), and unnecessary
	// noise in any backup/sync watcher. The filter reads the
	// current file's tags, compares each key, and drops matches.
	// When nothing remains we skip the write entirely.
	if len(tagMeta) > 0 && !rs.IsProtectedPath(tagWriteTarget) {
		filtered := rs.FilterUnchangedTags(tagWriteTarget, tagMeta)
		if len(filtered) == 0 {
			slog.Debug("rename all tags match, skipping write for", "tagWriteTarget", tagWriteTarget)
		} else {
			opConfig := fileops.OperationConfig{VerifyChecksums: true}
			if err := enhanced.WriteMetadataToFile(tagWriteTarget, filtered, opConfig); err != nil {
				slog.Warn("rename tag write failed for", "bookID", bookID, "err", err)
				// Tag write failure is non-fatal; continue with rename
			} else {
				tagsWritten = len(filtered)
				// Record tag write changes for undo (only the
				// fields we actually wrote).
				if operationID != "" {
					for field, val := range filtered {
						_ = rs.db.CreateOperationChange(&database.OperationChange{
							OperationID: operationID,
							BookID:      bookID,
							ChangeType:  "tag_write",
							FieldName:   field,
							OldValue:    "", // original tag values not easily recoverable
							NewValue:    fmt.Sprintf("%v", val),
						})
					}
				}
			}
		}
	} else if rs.IsProtectedPath(tagWriteTarget) {
		slog.Info("rename skipping tag write for protected path", "tagWriteTarget", tagWriteTarget)
	}

	// Step 2: Move/rename file if path differs and we haven't already handled it
	if oldPath != proposedPath {
		if sourceIsProtected {
			// Already copied in Step 1 above — just record the change
		} else {
			// Not a protected path: do a normal move/rename
			if err := rs.moveFile(oldPath, proposedPath); err != nil {
				return nil, fmt.Errorf("failed to move file: %w", err)
			}
		}

		// Record file move/copy for undo
		if operationID != "" {
			changeType := "file_move"
			if sourceIsProtected {
				changeType = "file_copy" // Indicates source was preserved
			}
			_ = rs.db.CreateOperationChange(&database.OperationChange{
				OperationID: operationID,
				BookID:      bookID,
				ChangeType:  changeType,
				FieldName:   "file_path",
				OldValue:    oldPath,
				NewValue:    proposedPath,
			})
		}

		// Step 3: Update DB with new file path
		book.FilePath = proposedPath
		book.LibraryState = stringPtr("organized")
		if _, err := rs.db.UpdateBook(bookID, book); err != nil {
			slog.Error("rename DB update failed for , rolling back", "bookID", bookID, "err", err)
			if !sourceIsProtected {
				if rbErr := rs.moveFile(proposedPath, oldPath); rbErr != nil {
					slog.Error("rename rollback failed! File at , DB expects", "proposedPath", proposedPath, "oldPath", oldPath, "rbErr", rbErr)
					return nil, fmt.Errorf("DB update failed and rollback failed: %w", err)
				}
			} else {
				// Just remove the copy, original is still safe
				_ = os.Remove(proposedPath)
			}
			return nil, fmt.Errorf("DB update failed (rolled back): %w", err)
		}

		// Update corresponding BookFile records with the new path
		if bookFiles, bfErr := rs.db.GetBookFiles(bookID); bfErr == nil {
			for _, bf := range bookFiles {
				if bf.FilePath == oldPath {
					bf.FilePath = proposedPath
					bf.ITunesPath = rs.ComputeITunesPath(proposedPath)
					if ufErr := rs.db.UpdateBookFile(bf.ID, &bf); ufErr != nil {
						slog.Warn("rename failed to update book_file path", "bf", bf.ID, "ufErr", ufErr)
					}
				}
			}
		}

		// Record library_state change for undo
		if operationID != "" {
			_ = rs.db.CreateOperationChange(&database.OperationChange{
				OperationID: operationID,
				BookID:      bookID,
				ChangeType:  "metadata_update",
				FieldName:   "library_state",
				OldValue:    stringOrDefault(book.LibraryState, ""),
				NewValue:    "organized",
			})
		}
	}

	return &RenameApplyResult{
		BookID:      bookID,
		OldPath:     oldPath,
		NewPath:     proposedPath,
		TagsWritten: tagsWritten,
		Message:     fmt.Sprintf("Renamed and updated %s", filepath.Base(proposedPath)),
	}, nil
}

// resolveNarratorNames resolves narrator names from the database.
func (rs *RenameService) resolveNarratorNames(bookID string, book *database.Book) string {
	var narratorNames []string
	bookNarrators, err := rs.db.GetBookNarrators(bookID)
	if err == nil && len(bookNarrators) > 0 {
		for _, bn := range bookNarrators {
			if narrator, nerr := rs.db.GetNarratorByID(bn.NarratorID); nerr == nil && narrator != nil {
				narratorNames = append(narratorNames, narrator.Name)
			}
		}
	} else if book.Narrator != nil && *book.Narrator != "" {
		narratorNames = append(narratorNames, *book.Narrator)
	}
	return strings.Join(narratorNames, " & ")
}

// computeTagChanges shows what tags will be written to the file.
// Since reading current file tags at preview time would be expensive and require
// ffprobe, we show the proposed values that will be written by the apply step.
func (rs *RenameService) computeTagChanges(book *database.Book, authorName, narratorStr string) []TagChange {
	var changes []TagChange

	// Title + Album
	if book.Title != "" {
		changes = append(changes, TagChange{
			Field:    "title",
			Current:  "",
			Proposed: book.Title,
		})
		changes = append(changes, TagChange{
			Field:    "album",
			Current:  "",
			Proposed: book.Title,
		})
	}

	// Artist (author)
	if authorName != "" {
		changes = append(changes, TagChange{
			Field:    "artist",
			Current:  "",
			Proposed: authorName,
		})
	}

	// Album artist / composer (narrator)
	if narratorStr != "" {
		changes = append(changes, TagChange{
			Field:    "album_artist",
			Current:  "",
			Proposed: narratorStr,
		})
	}

	// Genre
	changes = append(changes, TagChange{
		Field:    "genre",
		Current:  "",
		Proposed: "Audiobook",
	})

	// Year
	year := 0
	if book.AudiobookReleaseYear != nil && *book.AudiobookReleaseYear > 0 {
		year = *book.AudiobookReleaseYear
	} else if book.PrintYear != nil && *book.PrintYear > 0 {
		year = *book.PrintYear
	}
	if year > 0 {
		changes = append(changes, TagChange{
			Field:    "year",
			Current:  "",
			Proposed: fmt.Sprintf("%d", year),
		})
	}

	return changes
}

// BuildTagMetadata constructs the metadata map for WriteMetadataToFile.
func (rs *RenameService) BuildTagMetadata(book *database.Book, authorName, narratorStr string) map[string]interface{} {
	meta := map[string]interface{}{
		"title": book.Title,
		"album": book.Title,
		"genre": "Audiobook",
	}
	if authorName != "" {
		meta["artist"] = authorName
	}
	if narratorStr != "" {
		meta["album_artist"] = narratorStr
		meta["composer"] = narratorStr
	}
	year := 0
	if book.AudiobookReleaseYear != nil && *book.AudiobookReleaseYear > 0 {
		year = *book.AudiobookReleaseYear
	} else if book.PrintYear != nil && *book.PrintYear > 0 {
		year = *book.PrintYear
	}
	if year > 0 {
		meta["year"] = fmt.Sprintf("%d", year)
	}
	return meta
}

// moveFile moves a file, handling cross-filesystem moves with copy+delete.
func (rs *RenameService) moveFile(src, dst string) error {
	// Ensure destination directory exists
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return fmt.Errorf("failed to create destination directory: %w", err)
		}
	}

	// Try rename first (same filesystem). safeRename refuses to overwrite an
	// existing destination — a bare os.Rename would silently REPLACE it,
	// destroying another book's bytes on a path collision.
	err := safeRename(src, dst)
	if err == nil {
		return nil
	}
	if os.IsExist(err) {
		return fmt.Errorf("destination already exists, refusing to overwrite: %s", dst)
	}

	// Fallback: copy + delete for cross-filesystem moves
	return rs.copyAndDelete(src, dst)
}

// copyAndDelete copies src to dst then removes src. Used for cross-filesystem moves.
func (rs *RenameService) copyAndDelete(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer srcFile.Close()

	tmpDst := dst + ".tmp"
	dstFile, err := os.Create(tmpDst)
	if err != nil {
		return fmt.Errorf("failed to create temp destination: %w", err)
	}
	defer func() { _ = dstFile.Close() }()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err := dstFile.Sync(); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to sync: %w", err)
	}
	if err := dstFile.Close(); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to close: %w", err)
	}
	// safeRename: refuse to overwrite a destination that appeared during the
	// copy (bare os.Rename would silently replace it).
	if err := safeRename(tmpDst, dst); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to finalize: %w", err)
	}

	// Remove source only after successful copy
	if err := os.Remove(src); err != nil {
		slog.Warn("rename failed to remove source after copy", "error", err)
	}
	return nil
}

// hardlinkOrCopy creates a hardlink at dst pointing to src. If hardlinking fails
// (e.g. cross-filesystem), falls back to a copy. The source file is NEVER removed.
func (rs *RenameService) hardlinkOrCopy(src, dst string) error {
	// Ensure destination directory exists
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return fmt.Errorf("failed to create destination directory: %w", err)
		}
	}

	// Try hardlink first. os.Link fails with EEXIST if dst exists — treat
	// that as a terminal collision instead of falling through to the copy
	// path, whose finalize rename would silently overwrite the existing file.
	if err := os.Link(src, dst); err == nil {
		return nil
	} else if os.IsExist(err) {
		return fmt.Errorf("destination already exists, refusing to overwrite: %s", dst)
	}

	// Fallback: copy (never deletes source)
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer srcFile.Close()

	tmpDst := dst + ".tmp"
	dstFile, err := os.Create(tmpDst)
	if err != nil {
		return fmt.Errorf("failed to create temp destination: %w", err)
	}
	defer func() { _ = dstFile.Close() }()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to copy file: %w", err)
	}
	if err := dstFile.Sync(); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to sync: %w", err)
	}
	if err := dstFile.Close(); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to close: %w", err)
	}
	// safeRename: refuse to overwrite a destination that appeared during the
	// copy (bare os.Rename would silently replace it).
	if err := safeRename(tmpDst, dst); err != nil {
		_ = os.Remove(tmpDst)
		return fmt.Errorf("failed to finalize: %w", err)
	}
	return nil
}

// stringPtr returns a pointer to the given string.
func stringPtr(s string) *string {
	return &s
}

// stringOrDefault returns the string pointed to by p, or def if p is nil.
func stringOrDefault(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}
