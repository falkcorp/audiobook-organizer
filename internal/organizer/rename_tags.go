// file: internal/organizer/rename_tags.go
// version: 1.3.0
// guid: 2e8f5a13-7b4c-4d91-a6e0-3c9d1b7f5e28
// last-edited: 2026-09-14

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
	prefix := dirPrefix(target)
	// In the protected-source copy case the rows still carry the source
	// folder (oldPath); the files tags go to are the same names under the
	// copy. Matching only rows under target found none, so a multi-file book
	// copied out of a protected source got no tags at all.
	oldPrefix := ""
	if oldPath != "" && filepath.Clean(oldPath) != filepath.Clean(target) {
		oldPrefix = dirPrefix(oldPath)
	}
	var out []tagTarget
	for _, bf := range files {
		p := filepath.Clean(bf.FilePath)
		switch {
		case strings.HasPrefix(p, prefix):
		case oldPrefix != "" && strings.HasPrefix(p, oldPrefix):
			p = filepath.Join(target, strings.TrimPrefix(p, oldPrefix))
		default:
			continue
		}
		if st, serr := os.Stat(p); serr != nil || st.IsDir() {
			continue
		}
		out = append(out, tagTarget{path: p, fileID: bf.ID})
	}
	return out
}

// dirPrefix is dir with exactly one trailing separator.
func dirPrefix(dir string) string {
	return strings.TrimSuffix(filepath.Clean(dir), string(os.PathSeparator)) + string(os.PathSeparator)
}

// writeTagsRecordingOld writes the changed tags to every target file and
// records one tag_write row per tag per file, carrying the value the file held
// BEFORE the write, so undo can put it back. The rows used to record OldValue
// "" for every tag, and the revert wrote that back -- which deletes the tag.
//
// A file whose current tags cannot be read is not written: a write that
// cannot be undone is not made silently. It returns the number of tags
// written across all files, and the number whose undo record could not be
// saved (each logged at Error): those writes happened and cannot be undone.
func (rs *RenameService) writeTagsRecordingOld(bookID, operationID, oldPath, target string, tagMeta map[string]any) (written, undoLost int) {
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
		// A key whose pre-write value the reader could not record (a
		// property holding several values) is not written to this file: its
		// undo row would carry "" and the write could never be undone.
		// filtered may be tagMeta itself, shared by every file, so the kept
		// keys go into a new map.
		recordable := make(map[string]any, len(filtered))
		for field, val := range filtered {
			if _, known := current[field]; !known {
				renameTagLog.Warn("not writing tag %s to %s: its current value cannot be recorded (a tag holding several values), so the write could not be undone",
					field, t.path)
				continue
			}
			recordable[field] = val
		}
		filtered = recordable
		if len(filtered) == 0 {
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
			// A tag the file does not carry is recorded as absent, so the
			// revert removes it. Every key here is known (filtered above).
			old := current[field]
			if old == "" {
				old = undo.TagAbsentValue
			}
			if err := rs.db.CreateOperationChange(&database.OperationChange{
				OperationID: operationID,
				BookID:      bookID,
				ChangeType:  undo.ChangeTypeTagWrite,
				FieldName:   name,
				OldValue:    old,
				NewValue:    fmt.Sprintf("%v", val),
			}); err != nil {
				undoLost++
				renameTagLog.Error("undo record for tag %s of %s (book %s, operation %s) was not saved; this tag write cannot be undone: %v",
					field, t.path, logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(operationID), err)
			}
		}
	}
	return written, undoLost
}
