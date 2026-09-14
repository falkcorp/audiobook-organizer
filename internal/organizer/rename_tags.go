// file: internal/organizer/rename_tags.go
// version: 1.0.0
// guid: 2e8f5a13-7b4c-4d91-a6e0-3c9d1b7f5e28
// last-edited: 2026-09-13

package organizer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	enhanced "github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

var renameTagLog = logger.New("organizer-rename-tags")

// defaultWriteTags is RenameService.WriteTags when the caller sets none.
func defaultWriteTags(path string, tags map[string]any) error {
	return enhanced.WriteMetadataToFile(path, tags, fileops.OperationConfig{VerifyChecksums: true})
}

// tagTarget is one audio file ApplyRename writes tags to, with the book_file
// row it belongs to ("" when the book has no row for it).
type tagTarget struct {
	path   string
	fileID string
}

// tagWriteTargets lists the files ApplyRename writes tags to. A single-file
// book's target is the file itself. A multi-file book's FilePath is its
// folder, so the targets are the book's book_file rows under that folder --
// writing to the folder path itself fails on every such book. oldPath is the
// book's path before the rename: in the protected-source copy case the target
// is the fresh copy, and the book_file row still carries the source path.
func (rs *RenameService) tagWriteTargets(bookID, oldPath, target string) []tagTarget {
	info, err := os.Stat(target)
	if err != nil {
		renameTagLog.Warn("not writing tags: %s cannot be read: %v", target, err)
		return nil
	}
	files, ferr := rs.db.GetBookFiles(bookID)
	if ferr != nil {
		renameTagLog.Warn("not writing tags to book %s: its book_file rows cannot be read: %v", logger.SanitizeLogValue(bookID), ferr)
		return nil
	}
	if !info.IsDir() {
		t := tagTarget{path: target}
		for _, bf := range files {
			if bf.FilePath == target || bf.FilePath == oldPath {
				t.fileID = bf.ID
				break
			}
		}
		return []tagTarget{t}
	}
	prefix := strings.TrimSuffix(target, string(os.PathSeparator)) + string(os.PathSeparator)
	var out []tagTarget
	for _, bf := range files {
		if !strings.HasPrefix(filepath.Clean(bf.FilePath), prefix) {
			continue
		}
		if st, serr := os.Stat(bf.FilePath); serr != nil || st.IsDir() {
			continue
		}
		out = append(out, tagTarget{path: bf.FilePath, fileID: bf.ID})
	}
	return out
}

// writeTagsRecordingOld writes the changed tags to every target file and
// records one tag_write row per tag per file, carrying the value the file held
// BEFORE the write, so undo can put it back. The rows used to record OldValue
// "" for every tag, and the revert wrote that back -- which deletes the tag.
//
// A file whose current tags cannot be read is not written: a write that
// cannot be undone is not made silently. It returns the number of tags
// written across all files.
func (rs *RenameService) writeTagsRecordingOld(bookID, operationID, oldPath, target string, tagMeta map[string]any) int {
	written := 0
	for _, t := range rs.tagWriteTargets(bookID, oldPath, target) {
		if rs.IsProtectedPath(t.path) {
			continue
		}
		filtered := rs.FilterUnchangedTags(t.path, tagMeta)
		if len(filtered) == 0 {
			continue
		}
		if rs.ReadCurrentTags == nil {
			renameTagLog.Warn("not writing tags to %s: no tag reader is configured, so the write could not be undone", t.path)
			continue
		}
		current, err := rs.ReadCurrentTags(t.path)
		if err != nil {
			renameTagLog.Warn("not writing tags to %s: its current tags could not be read, so the write could not be undone: %v", t.path, err)
			continue
		}
		write := rs.WriteTags
		if write == nil {
			write = defaultWriteTags
		}
		if err := write(t.path, filtered); err != nil {
			// Tag write failure is non-fatal; continue with the rename.
			renameTagLog.Warn("rename tag write failed for book %s file %s: %v", logger.SanitizeLogValue(bookID), t.path, err)
			continue
		}
		written += len(filtered)
		if operationID == "" {
			continue
		}
		for field, val := range filtered {
			name := field
			if t.fileID != "" {
				name = undo.TagWriteField(field, t.fileID)
			}
			_ = rs.db.CreateOperationChange(&database.OperationChange{
				OperationID: operationID,
				BookID:      bookID,
				ChangeType:  undo.ChangeTypeTagWrite,
				FieldName:   name,
				OldValue:    current[field],
				NewValue:    fmt.Sprintf("%v", val),
			})
		}
	}
	return written
}
