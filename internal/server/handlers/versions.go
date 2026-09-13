// file: internal/server/handlers/versions.go
// version: 1.4.0
// guid: 7e3c1a92-4b8d-4f60-9a2e-1c0d5f8b6a47
// last-edited: 2026-09-13

package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// VersionBookReader reads a book and its version-group siblings.
type VersionBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// VersionBookWriter creates and updates books when splitting or merging groups.
type VersionBookWriter interface {
	CreateBook(book *database.Book) (*database.Book, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// VersionBookFileStore reads book files and moves them between books.
type VersionBookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
}

// VersionBookAuthorStore reads and replaces a book's author links.
type VersionBookAuthorStore interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// VersionExternalIDStore reads and creates external ID mappings.
type VersionExternalIDStore interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	CreateExternalIDMapping(mapping *database.ExternalIDMapping) error
}

// VersionRawKVDeleter deletes a raw key. The narrowest and most dangerous piece
// here, which is exactly why it is its own declaration.
type VersionRawKVDeleter interface {
	DeleteRaw(key string) error
}

// VersionBookPathChecker reports which live books sit at a path. The split
// endpoints use it to refuse a new book on a path another book owns.
type VersionBookPathChecker interface {
	LiveBookIDsAtPath(path string) ([]string, error)
}

// VersionBookDeleter deletes a book. The one-book split uses it to remove the
// book it just created when moving the files into it fails.
type VersionBookDeleter interface {
	DeleteBook(id string) error
}

// VersionsStore is the narrow database interface VersionsHandler requires.
// It lists only the database.Store methods the version-grouping handlers
// actually call, including the external-ID methods used by
// reassignExternalIDsForFiles.
//
// Split into the 6 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type VersionsStore interface {
	VersionBookReader
	VersionBookWriter
	VersionBookFileStore
	VersionBookAuthorStore
	VersionExternalIDStore
	VersionRawKVDeleter
	VersionBookPathChecker
	VersionBookDeleter
}

// VersionsHandler handles audiobook version-group endpoints: listing, linking,
// setting primary, fetching a group, and split/move operations on segments.
type VersionsHandler struct {
	store VersionsStore
}

// NewVersionsHandler constructs a VersionsHandler backed by the given store.
func NewVersionsHandler(store VersionsStore) *VersionsHandler {
	return &VersionsHandler{store: store}
}

// ListAudiobookVersions lists all versions of an audiobook
func (h *VersionsHandler) ListAudiobookVersions(c *gin.Context) {
	id := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := h.store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	if book.VersionGroupID == nil {
		httputil.RespondWithOK(c, gin.H{"versions": []any{book}})
		return
	}

	books, err := h.store.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to fetch versions")
		return
	}

	httputil.RespondWithOK(c, gin.H{"versions": books})
}

// LinkAudiobookVersion links an audiobook as another version
func (h *VersionsHandler) LinkAudiobookVersion(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		OtherID string `json:"other_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book1, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	book2, err := h.store.GetBookByID(req.OtherID)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", req.OtherID)
		return
	}

	versionGroupID := ""
	if book1.VersionGroupID != nil {
		versionGroupID = *book1.VersionGroupID
	} else if book2.VersionGroupID != nil {
		versionGroupID = *book2.VersionGroupID
	} else {
		versionGroupID = ulid.Make().String()
	}

	book1.VersionGroupID = &versionGroupID
	book2.VersionGroupID = &versionGroupID

	if _, err := h.store.UpdateBook(id, book1); err != nil {
		httputil.RespondWithInternalError(c, "failed to update audiobook")
		return
	}

	if _, err := h.store.UpdateBook(req.OtherID, book2); err != nil {
		httputil.RespondWithInternalError(c, "failed to update other audiobook")
		return
	}

	httputil.RespondWithOK(c, gin.H{"version_group_id": versionGroupID})
}

// SetAudiobookPrimary sets an audiobook as the primary version
func (h *VersionsHandler) SetAudiobookPrimary(c *gin.Context) {
	id := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	if book.VersionGroupID == nil {
		primaryFlag := true
		book.IsPrimaryVersion = &primaryFlag
		if _, err := h.store.UpdateBook(id, book); err != nil {
			httputil.RespondWithInternalError(c, "failed to update audiobook")
			return
		}
		httputil.RespondWithOK(c, gin.H{"message": "audiobook set as primary"})
		return
	}

	books, err := h.store.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to fetch versions")
		return
	}

	for i := range books {
		primaryFlag := books[i].ID == id
		books[i].IsPrimaryVersion = &primaryFlag
		if _, err := h.store.UpdateBook(books[i].ID, &books[i]); err != nil {
			httputil.RespondWithInternalError(c, "failed to update version")
			return
		}
	}

	httputil.RespondWithOK(c, gin.H{"message": "audiobook set as primary"})
}

// GetVersionGroup gets all audiobooks in a version group
func (h *VersionsHandler) GetVersionGroup(c *gin.Context) {
	groupID := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	books, err := h.store.GetBooksByVersionGroup(groupID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to fetch version group")
		return
	}

	httputil.RespondWithOK(c, gin.H{"audiobooks": books})
}

// SplitVersion moves selected segments from a book into a new version (a new book
// in the same version group).
func (h *VersionsHandler) SplitVersion(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		SegmentIDs []string `json:"segment_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.SegmentIDs) == 0 {
		httputil.RespondWithBadRequest(c, "segment_ids must not be empty")
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// 1. Get source book
	sourceBook, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	// 2. Ensure source book has a version group
	versionGroupID := ""
	if sourceBook.VersionGroupID != nil && *sourceBook.VersionGroupID != "" {
		versionGroupID = *sourceBook.VersionGroupID
	} else {
		versionGroupID = ulid.Make().String()
		sourceBook.VersionGroupID = &versionGroupID
		if _, err := h.store.UpdateBook(id, sourceBook); err != nil {
			httputil.RespondWithInternalError(c, "failed to update source book version group")
			return
		}
	}

	// 3. Count existing versions to determine suffix
	existingVersions, err := h.store.GetBooksByVersionGroup(versionGroupID)
	if err != nil {
		// The member count names the new row ("Version N"). An unreadable
		// group must fail the split, not number the row as if it were empty.
		httputil.InternalError(c, "failed to read version group", err)
		return
	}
	versionNum := len(existingVersions) + 1

	// 4. Create new book entry (copy metadata from source, but NOT FilePath —
	// the new version's path will be derived from its segments after they're moved)
	newTitle := fmt.Sprintf("%s (Version %d)", sourceBook.Title, versionNum)
	primaryFlag := false
	newBook := &database.Book{
		Title:            newTitle,
		AuthorID:         sourceBook.AuthorID,
		SeriesID:         sourceBook.SeriesID,
		SeriesSequence:   sourceBook.SeriesSequence,
		FilePath:         "", // Will be set from segments below
		Format:           sourceBook.Format,
		WorkID:           sourceBook.WorkID,
		Narrator:         sourceBook.Narrator,
		Language:         sourceBook.Language,
		Publisher:        sourceBook.Publisher,
		VersionGroupID:   &versionGroupID,
		IsPrimaryVersion: &primaryFlag,
	}

	createdBook, err := h.store.CreateBook(newBook)
	if err != nil {
		httputil.InternalError(c, "failed to create new version", err)
		return
	}

	// 5. Move files to the new book (DB-only, does NOT touch files on disk)
	if err := h.store.MoveBookFilesToBook(req.SegmentIDs, sourceBook.ID, createdBook.ID); err != nil {
		httputil.InternalError(c, "failed to move files", err)
		return
	}

	// 6. Derive the new book's FilePath from its files.
	// For multi-file books, FilePath is the common parent directory.
	// For single-file books, FilePath is the file path itself.
	newFiles, _ := h.store.GetBookFiles(createdBook.ID)
	if len(newFiles) > 0 {
		if len(newFiles) == 1 {
			createdBook.FilePath = newFiles[0].FilePath
		} else {
			createdBook.FilePath = filesCommonDir(newFiles)
		}
		h.store.UpdateBook(createdBook.ID, createdBook)
	}

	// 7. Also update the source book's FilePath from its remaining files
	remainingFiles, _ := h.store.GetBookFiles(sourceBook.ID)
	if len(remainingFiles) > 0 {
		if len(remainingFiles) == 1 {
			sourceBook.FilePath = remainingFiles[0].FilePath
		} else {
			sourceBook.FilePath = filesCommonDir(remainingFiles)
		}
		h.store.UpdateBook(sourceBook.ID, sourceBook)
	}

	httputil.RespondWithOK(c, gin.H{
		"book":             createdBook,
		"version_group_id": versionGroupID,
		"segments_moved":   len(req.SegmentIDs),
	})
}

// SplitSegmentsToBooks splits selected segments out of a multi-file book into
// independent new books (one per segment), extracting titles from filenames.
// Unlike SplitVersion, the new books are NOT version-linked to the source.
func (h *VersionsHandler) SplitSegmentsToBooks(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		SegmentIDs []string `json:"segment_ids" binding:"required"`
		// AsOneBook moves the selected files into ONE new standalone book
		// instead of one book per file. See splitSegmentsToOneBook.
		AsOneBook bool `json:"as_one_book"`
		// Title names the new book when AsOneBook is set; empty means
		// "<source title> (split)". Ignored otherwise (titles come from the
		// file names).
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.SegmentIDs) == 0 {
		httputil.RespondWithBadRequest(c, "segment_ids must not be empty")
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	sourceBook, err := h.store.GetBookByID(id)
	if err != nil || sourceBook == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	if req.AsOneBook {
		h.splitSegmentsToOneBook(c, sourceBook, req.SegmentIDs, req.Title)
		return
	}

	// Build a lookup of file ID → BookFile
	allFiles, err := h.store.GetBookFiles(sourceBook.ID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to list book files")
		return
	}
	fileMap := make(map[string]database.BookFile, len(allFiles))
	for _, f := range allFiles {
		fileMap[f.ID] = f
	}

	// Create one new book per selected file
	var createdBooks []any
	for _, fileID := range req.SegmentIDs {
		f, ok := fileMap[fileID]
		if !ok {
			continue
		}

		// Extract title from file name
		// e.g. "01 ASoIaF 1 - A Game of Thrones.m4b" → "A Game of Thrones"
		title := extractTitleFromSegmentFilename(filepath.Base(f.FilePath))
		if title == "" {
			title = sourceBook.Title + " (split)"
		}

		// BookFile.Duration is SECONDS by convention; only ~2% of rows are
		// milliseconds (iTunes importer). Normalize per row on the file's implied
		// bitrate instead of dividing unconditionally, which was turning correct
		// values into near-zero.
		durationSec := database.NormalizeDurationSec(f.FileSize, f.Duration)
		newBook := &database.Book{
			Title:     title,
			AuthorID:  sourceBook.AuthorID,
			SeriesID:  sourceBook.SeriesID,
			FilePath:  f.FilePath,
			Format:    f.Format,
			Narrator:  sourceBook.Narrator,
			Language:  sourceBook.Language,
			Publisher: sourceBook.Publisher,
			Duration:  &durationSec,
			FileSize:  &f.FileSize,
		}

		created, createErr := h.store.CreateBook(newBook)
		if createErr != nil {
			slog.Warn("splitSegmentsToBooks failed to create book for file", "fileID", logger.SanitizeLogValue(fileID), "createErr", createErr)
			continue
		}

		// Copy book_authors from source
		if authors, aErr := h.store.GetBookAuthors(sourceBook.ID); aErr == nil && len(authors) > 0 {
			var newAuthors []database.BookAuthor
			for _, ba := range authors {
				newAuthors = append(newAuthors, database.BookAuthor{
					BookID:   created.ID,
					AuthorID: ba.AuthorID,
					Role:     ba.Role,
				})
			}
			_ = h.store.SetBookAuthors(created.ID, newAuthors)
		}

		// Move the file to the new book
		_ = h.store.MoveBookFilesToBook([]string{fileID}, sourceBook.ID, created.ID)

		// Reassign external ID mappings (iTunes PIDs) that belong to the moved file
		h.reassignExternalIDsForFiles(sourceBook.ID, created.ID, []database.BookFile{f})

		createdBooks = append(createdBooks, created)
	}

	// Update source book's FilePath from remaining files
	remainingFiles, _ := h.store.GetBookFiles(sourceBook.ID)
	if len(remainingFiles) > 0 {
		if len(remainingFiles) == 1 {
			sourceBook.FilePath = remainingFiles[0].FilePath
		} else {
			sourceBook.FilePath = filesCommonDir(remainingFiles)
		}
		h.store.UpdateBook(sourceBook.ID, sourceBook)
	}

	httputil.RespondWithOK(c, gin.H{
		"created_books": createdBooks,
		"count":         len(createdBooks),
	})
}

// splitSegmentsToOneBook is SplitSegmentsToBooks with as_one_book: the
// selected book_file rows move, as a set, into ONE new book. The new book is
// standalone -- no version group, not primary -- and inherits the source's
// author, series, narrator, language and publisher the way the per-file split
// does. Nothing moves on disk: the rows keep their paths, and the new book's
// FilePath is derived from them.
//
// The move is one MoveBookFilesToBook call, which rewrites every row under
// the new book in one atomic batch (a row is never on both books) and
// recomputes the aggregates -- duration, size, file count -- of BOTH books
// once. The new book is therefore created with no Duration/FileSize and gets
// them from that recompute rather than a hand sum here.
//
// Refused with 400, before anything is written: a selected ID that is not a
// file of the source (an unknown ID must not silently shrink the split), and
// a selection of every file (the source would be left with none -- that is a
// rename of the book, not a split).
func (h *VersionsHandler) splitSegmentsToOneBook(c *gin.Context, sourceBook *database.Book, segmentIDs []string, title string) {
	allFiles, err := h.store.GetBookFiles(sourceBook.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files", err)
		return
	}
	fileMap := make(map[string]database.BookFile, len(allFiles))
	for _, f := range allFiles {
		fileMap[f.ID] = f
	}

	seen := make(map[string]bool, len(segmentIDs))
	var ids []string
	var selected []database.BookFile
	var unknown []string
	for _, fid := range segmentIDs {
		if seen[fid] {
			continue
		}
		seen[fid] = true
		f, ok := fileMap[fid]
		if !ok {
			unknown = append(unknown, fid)
			continue
		}
		ids = append(ids, fid)
		selected = append(selected, f)
	}
	if len(unknown) > 0 {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("segment_ids not on book %s: %s", sourceBook.ID, strings.Join(unknown, ", ")))
		return
	}
	if len(selected) == len(allFiles) {
		httputil.RespondWithBadRequest(c, "segment_ids selects every file of the book; a split must leave the source at least one file")
		return
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = sourceBook.Title + " (split)"
	}
	newPath, ok := h.splitTargetPath(c, sourceBook, selected)
	if !ok {
		return
	}
	created, err := h.store.CreateBook(&database.Book{
		Title:     title,
		AuthorID:  sourceBook.AuthorID,
		SeriesID:  sourceBook.SeriesID,
		FilePath:  newPath,
		Format:    sourceBook.Format,
		Narrator:  sourceBook.Narrator,
		Language:  sourceBook.Language,
		Publisher: sourceBook.Publisher,
	})
	if err != nil {
		httputil.InternalError(c, "failed to create book", err)
		return
	}

	// Create and move are two writes (CreateBook and the file-row batch are
	// separate commits), so a failed move leaves a book with no files. Delete
	// it before answering, so a retry never piles up empty books; the move
	// batch is atomic, so no row points at it. Authors are copied only after
	// the move, so the cleanup has nothing else to undo (DeleteBook removes
	// book_authors anyway).
	if err := h.store.MoveBookFilesToBook(ids, sourceBook.ID, created.ID); err != nil {
		if dErr := h.store.DeleteBook(created.ID); dErr != nil {
			versionsLog.Error("split-to-one-book: move into %s failed (%v) and deleting it failed: %v", created.ID, err, dErr)
			httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
				fmt.Sprintf("failed to move files into the new book; no file was moved, and the empty new book %s could not be deleted (%v): %v",
					created.ID, dErr, err),
				"split_move_failed", map[string]any{"created_book_id": created.ID})
			return
		}
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"failed to move files into the new book; no file was moved and the new book was deleted: "+err.Error(),
			"split_move_failed", nil)
		return
	}

	if authors, aErr := h.store.GetBookAuthors(sourceBook.ID); aErr != nil {
		versionsLog.Warn("split-to-one-book: could not read authors of %s to copy onto %s: %v",
			logger.SanitizeLogValue(sourceBook.ID), created.ID, aErr)
	} else if len(authors) > 0 {
		newAuthors := make([]database.BookAuthor, 0, len(authors))
		for _, ba := range authors {
			newAuthors = append(newAuthors, database.BookAuthor{BookID: created.ID, AuthorID: ba.AuthorID, Role: ba.Role})
		}
		if sErr := h.store.SetBookAuthors(created.ID, newAuthors); sErr != nil {
			versionsLog.Warn("split-to-one-book: could not copy authors onto %s: %v", created.ID, sErr)
		}
	}

	h.reassignExternalIDsForFiles(sourceBook.ID, created.ID, selected)

	// The source keeps its remaining files; point its FilePath at them.
	// Re-read both books: the move's aggregate recompute has just written
	// their Duration/FileSize, and writing back the rows read before it would
	// put the old totals back.
	if remaining, rErr := h.store.GetBookFiles(sourceBook.ID); rErr != nil {
		versionsLog.Warn("split-to-one-book: could not list remaining files of %s: %v", logger.SanitizeLogValue(sourceBook.ID), rErr)
	} else if len(remaining) > 0 {
		if fresh, gErr := h.store.GetBookByID(sourceBook.ID); gErr == nil && fresh != nil {
			if len(remaining) == 1 {
				fresh.FilePath = remaining[0].FilePath
			} else {
				fresh.FilePath = filesCommonDir(remaining)
			}
			if _, uErr := h.store.UpdateBook(fresh.ID, fresh); uErr != nil {
				versionsLog.Warn("split-to-one-book: could not update path of %s: %v", logger.SanitizeLogValue(fresh.ID), uErr)
			}
		}
	}
	if fresh, gErr := h.store.GetBookByID(created.ID); gErr == nil && fresh != nil {
		created = fresh
	}

	httputil.RespondWithOK(c, gin.H{
		"book":           created,
		"source_book_id": sourceBook.ID,
		"segments_moved": len(ids),
	})
}

// splitTargetPath picks the FilePath of the book splitSegmentsToOneBook is
// about to create, or answers the request and returns ok=false when there is
// no path the new book can own.
//
// A book's FilePath is also its single-valued book:path:<path> lookup key
// (GetBookByFilePath, which the scanner, the organizer's collision checks,
// the iTunes import and autoscan all read), so the new book must never be
// given a path another live book sits on:
//
//   - One file: the file's own path, the scanner's convention for a
//     single-file book (createSingleFileBookFile).
//   - More than one: the ONE folder they are all in. Files from two folders
//     are refused rather than given their common ancestor: that ancestor is
//     typically the source book's own folder, and the new book would take the
//     source's lookup key (the book:path: index is last-writer-wins).
//   - Refused when that path is the source's FilePath, or when any live book
//     already sits there (LiveBookIDsAtPath, the multi-valued index, not the
//     single key). A lookup error refuses too: LiveBookIDsAtPath fails closed,
//     and treating its error as "free" would re-open exactly this hole.
//
// Every refusal happens before anything is written.
func (h *VersionsHandler) splitTargetPath(c *gin.Context, sourceBook *database.Book, selected []database.BookFile) (string, bool) {
	newPath := selected[0].FilePath
	if len(selected) > 1 {
		newPath = filepath.Dir(selected[0].FilePath)
		dirs := []string{newPath}
		for _, f := range selected[1:] {
			if d := filepath.Dir(f.FilePath); !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
		if len(dirs) > 1 {
			httputil.RespondWithErrorFields(c, http.StatusBadRequest,
				"the selected files are in more than one folder; a book's path is one folder, so split one folder at a time",
				"split_files_span_folders", map[string]any{"folders": dirs})
			return "", false
		}
	}
	if filepath.Clean(newPath) == filepath.Clean(sourceBook.FilePath) {
		httputil.RespondWithErrorFields(c, http.StatusBadRequest,
			fmt.Sprintf("the new book's path %q is the source book's own path; two books cannot share it", newPath),
			"split_path_taken", map[string]any{"path": newPath, "book_ids": []string{sourceBook.ID}})
		return "", false
	}
	occupants, err := h.store.LiveBookIDsAtPath(newPath)
	if err != nil {
		httputil.InternalError(c, "could not check whether the new book's path is free; nothing was written", err)
		return "", false
	}
	if len(occupants) > 0 {
		httputil.RespondWithErrorFields(c, http.StatusBadRequest,
			fmt.Sprintf("another book already has the path %q; two books cannot share it", newPath),
			"split_path_taken", map[string]any{"path": newPath, "book_ids": occupants})
		return "", false
	}
	return newPath, true
}

var versionsLog = logger.New("handlers.versions")

// MoveSegments moves segments from one book to another within the same version group.
func (h *VersionsHandler) MoveSegments(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		SegmentIDs   []string `json:"segment_ids" binding:"required"`
		TargetBookID string   `json:"target_book_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.SegmentIDs) == 0 {
		httputil.RespondWithBadRequest(c, "segment_ids must not be empty")
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// 1. Get source and target books
	sourceBook, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	targetBook, err := h.store.GetBookByID(req.TargetBookID)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", req.TargetBookID)
		return
	}

	// 2. Verify both books are in the same version group
	if sourceBook.VersionGroupID == nil || targetBook.VersionGroupID == nil {
		httputil.RespondWithBadRequest(c, "both books must be in a version group")
		return
	}
	if *sourceBook.VersionGroupID != *targetBook.VersionGroupID {
		httputil.RespondWithBadRequest(c, "books must be in the same version group")
		return
	}

	// 3. Verify the files belong to the source book
	sourceFiles, err := h.store.GetBookFiles(id)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to list source book files")
		return
	}
	sourceFileMap := make(map[string]bool, len(sourceFiles))
	for _, f := range sourceFiles {
		sourceFileMap[f.ID] = true
	}
	for _, segID := range req.SegmentIDs {
		if !sourceFileMap[segID] {
			httputil.RespondWithBadRequest(c, fmt.Sprintf("file %s does not belong to source book", segID))
			return
		}
	}

	// 4. Collect the files being moved (for external ID reassignment)
	var movedFiles []database.BookFile
	movedSet := make(map[string]bool, len(req.SegmentIDs))
	for _, sid := range req.SegmentIDs {
		movedSet[sid] = true
	}
	for _, f := range sourceFiles {
		if movedSet[f.ID] {
			movedFiles = append(movedFiles, f)
		}
	}

	// 5. Move files
	if err := h.store.MoveBookFilesToBook(req.SegmentIDs, id, req.TargetBookID); err != nil {
		httputil.InternalError(c, "failed to move files", err)
		return
	}

	// 6. Reassign external ID mappings (iTunes PIDs) for moved files
	h.reassignExternalIDsForFiles(id, req.TargetBookID, movedFiles)

	httputil.RespondWithOK(c, gin.H{
		"segments_moved": len(req.SegmentIDs),
		"source_book_id": id,
		"target_book_id": req.TargetBookID,
	})
}

// reassignExternalIDsForFiles reassigns external ID mappings (iTunes PIDs) from a
// source book to a target book for the given moved files. Reimplemented from the
// server-package *Server.reassignExternalIDsForFiles, backed by the narrow
// VersionsStore interface.
func (h *VersionsHandler) reassignExternalIDsForFiles(sourceBookID, targetBookID string, files []database.BookFile) {
	if h.store == nil {
		return
	}

	mappings, err := h.store.GetExternalIDsForBook(sourceBookID)
	if err != nil || len(mappings) == 0 {
		return
	}

	// Build lookup sets from the moved files
	movedPaths := make(map[string]bool, len(files))
	movedPIDs := make(map[string]bool, len(files))
	for _, f := range files {
		if f.FilePath != "" {
			movedPaths[f.FilePath] = true
		}
		if f.ITunesPersistentID != "" {
			movedPIDs[f.ITunesPersistentID] = true
		}
	}

	// Collect only the mappings that belong to the moved files
	var toMove []database.ExternalIDMapping
	for _, m := range mappings {
		if (m.FilePath != "" && movedPaths[m.FilePath]) ||
			(m.ExternalID != "" && movedPIDs[m.ExternalID]) {
			toMove = append(toMove, m)
		}
	}
	if len(toMove) == 0 {
		return
	}

	// Reassign each mapping: delete old reverse key, update primary, add new reverse key
	for _, m := range toMove {
		oldReverseKey := fmt.Sprintf("ext_id:book:%s:%s:%s", sourceBookID, m.Source, m.ExternalID)
		_ = h.store.DeleteRaw(oldReverseKey)

		m.BookID = targetBookID
		if createErr := h.store.CreateExternalIDMapping(&m); createErr != nil {
			slog.Warn("reassignExternalIDsForFiles failed to reassign to", "source", m.Source, "externalID", m.ExternalID, "targetBookID", logger.SanitizeLogValue(targetBookID), "createErr", createErr)
		}
	}

	slog.Info("reassigned external ID mapping(s) from book to", "toMove_count", len(toMove), "sourceBookID", logger.SanitizeLogValue(sourceBookID), "targetBookID", logger.SanitizeLogValue(targetBookID))
}

// filesCommonDir returns the common parent directory of the given files.
// Copied (pure, unexported) from the server package, which keeps its own copy
// because it is also used by server.go.
func filesCommonDir(files []database.BookFile) string {
	if len(files) == 0 {
		return ""
	}
	common := filepath.Dir(files[0].FilePath)
	for _, f := range files[1:] {
		fDir := filepath.Dir(f.FilePath)
		for common != fDir && !strings.HasPrefix(fDir, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
			if common == "/" || common == "." {
				return common
			}
		}
	}
	return common
}

// extractTitleFromSegmentFilename extracts a probable book title from a segment
// filename. Copied (pure, unexported) from the server package, which keeps its
// own copy because it is also used by server.go.
func extractTitleFromSegmentFilename(filename string) string {
	// Strip extension
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Try to find title after " - " separator (common pattern)
	if _, after, ok := strings.Cut(name, " - "); ok {
		title := strings.TrimSpace(after)
		if title != "" {
			return title
		}
	}

	// Try after " – " (em dash)
	if _, after, ok := strings.Cut(name, " – "); ok {
		title := strings.TrimSpace(after)
		if title != "" {
			return title
		}
	}

	// Strip leading track numbers like "01 ", "01. "
	stripped := regexp.MustCompile(`^\d{1,3}[\s.\-]+`).ReplaceAllString(name, "")
	if stripped != "" {
		return strings.TrimSpace(stripped)
	}

	return name
}
