// file: internal/server/handlers/audiobooks/handler_retired_versions.go
// version: 1.0.0
// guid: 6818f543-1756-455b-9346-0581d8885fb8
// last-edited: 2026-10-23

// Retired-versions endpoints for the book detail page: the read that feeds the
// "Retired versions" panel and the per-version restore. A retired version is a
// book_versions row of status "retired" that holds the retired book_file rows
// of a shell book (trashed or merged) that the retire-versions repair moved
// onto a live "holder" book. See docs and PLAN.md section 3 for the model.

package audiobookshandler

import (
	"errors"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// retiredVersionReader is the version-row side of the store these endpoints
// need. AudiobooksStore does not carry it (and is not widened for it: that
// would regenerate its mock), so it is resolved through database.AsCapability
// like the other optional store methods of this package.
type retiredVersionReader interface {
	GetBookVersion(id string) (*database.BookVersion, error)
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
}

// retiredVersionDeleter removes the version row once a restore has emptied it.
type retiredVersionDeleter interface {
	DeleteBookVersion(id string) error
}

// retiredVersionFile is the per-row slice of a retired book_file the panel
// shows. It is deliberately not the whole BookFile: the listing with every
// field is GET /audiobooks/:id/files?include_retired=true.
type retiredVersionFile struct {
	ID               string `json:"id"`
	FilePath         string `json:"file_path"`
	OriginalFilename string `json:"original_filename,omitempty"`
	Format           string `json:"format,omitempty"`
	TrackNumber      int    `json:"track_number,omitempty"`
	DiscNumber       int    `json:"disc_number,omitempty"`
	Duration         int    `json:"duration,omitempty"`
	FileSize         int64  `json:"file_size,omitempty"`
	Missing          bool   `json:"missing"`
}

// retiredVersionView is one entry of the retired-versions listing.
type retiredVersionView struct {
	ID                 string               `json:"id"`
	BookID             string               `json:"book_id"`
	RetiredFromBookID  string               `json:"retired_from_book_id,omitempty"`
	RetiredFromTitle   string               `json:"retired_from_title,omitempty"`
	ShellExists        bool                 `json:"shell_exists"`
	RetireOpID         string               `json:"retire_op_id,omitempty"`
	RetiredAt          *time.Time           `json:"retired_at,omitempty"`
	Source             string               `json:"source,omitempty"`
	SourceOriginalPath string               `json:"source_original_path,omitempty"`
	IngestDate         time.Time            `json:"ingest_date"`
	FileCount          int                  `json:"file_count"`
	TotalSize          int64                `json:"total_size"`
	TotalDuration      int                  `json:"total_duration"`
	Files              []retiredVersionFile `json:"files"`
}

func retiredFileView(f database.BookFile) retiredVersionFile {
	return retiredVersionFile{
		ID:               f.ID,
		FilePath:         f.FilePath,
		OriginalFilename: f.OriginalFilename,
		Format:           f.Format,
		TrackNumber:      f.TrackNumber,
		DiscNumber:       f.DiscNumber,
		Duration:         f.Duration,
		FileSize:         f.FileSize,
		Missing:          f.Missing,
	}
}

// retiredCapabilities resolves the store capabilities both endpoints need and
// fails closed when one is missing: an empty list would read as "this book has
// no retired versions", which is a claim the handler cannot make without them.
func (h *Handler) retiredCapabilities(c *gin.Context) (retiredVersionReader, database.RetiredBookFileReader, bool) {
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return nil, nil, false
	}
	versions, ok := database.AsCapability[retiredVersionReader](h.store)
	if !ok {
		httputil.RespondWithInternalError(c, "this store cannot read book versions")
		return nil, nil, false
	}
	files, ok := database.AsCapability[database.RetiredBookFileReader](h.store)
	if !ok {
		httputil.RespondWithInternalError(c, "this store cannot read retired book files")
		return nil, nil, false
	}
	return versions, files, true
}

// ListRetiredVersions returns the retired versions a book holds, each with its
// retired file rows. GET /audiobooks/:id/retired-versions.
//
// :id is the HOLDER, the live book the rows were moved onto, because that is
// the page the panel is on. (GetRetiredVersionsFromBook is keyed by the shell
// and cannot answer this.) A retired version with no rows left has already been
// restored (the restore deletes the row, and a restore that failed between its
// two steps leaves an empty one), so it is not listed: it holds nothing to
// show or restore.
func (h *Handler) ListRetiredVersions(c *gin.Context) {
	bookID := c.Param("id")
	versions, files, ok := h.retiredCapabilities(c)
	if !ok {
		return
	}
	book, err := h.store.GetBookByID(bookID)
	if err != nil {
		httputil.InternalError(c, "failed to get book", err)
		return
	}
	if book == nil {
		httputil.RespondWithNotFound(c, "audiobook", bookID)
		return
	}
	all, err := versions.GetBookVersionsByBookID(bookID)
	if err != nil {
		httputil.InternalError(c, "failed to get book versions", err)
		return
	}
	retiredRows, err := files.GetRetiredBookFiles(bookID)
	if err != nil {
		httputil.InternalError(c, "failed to get retired book files", err)
		return
	}
	rowsByVersion := make(map[string][]database.BookFile)
	for _, f := range retiredRows {
		rowsByVersion[f.VersionID] = append(rowsByVersion[f.VersionID], f)
	}

	out := make([]retiredVersionView, 0)
	for _, v := range all {
		if v.Status != database.BookVersionStatusRetired {
			continue
		}
		rows := rowsByVersion[v.ID]
		if len(rows) == 0 {
			continue
		}
		view := retiredVersionView{
			ID:                 v.ID,
			BookID:             v.BookID,
			RetiredFromBookID:  v.RetiredFromBookID,
			RetireOpID:         v.RetireOpID,
			RetiredAt:          v.RetiredAt,
			Source:             v.Source,
			SourceOriginalPath: v.SourceOriginalPath,
			IngestDate:         v.IngestDate,
			FileCount:          len(rows),
			Files:              make([]retiredVersionFile, 0, len(rows)),
		}
		for _, f := range rows {
			view.TotalSize += f.FileSize
			view.TotalDuration += f.Duration
			view.Files = append(view.Files, retiredFileView(f))
		}
		if v.RetiredFromBookID != "" {
			shell, err := h.store.GetBookByID(v.RetiredFromBookID)
			if err != nil {
				httputil.InternalError(c, "failed to get the book a version was retired from", err)
				return
			}
			if shell != nil {
				view.ShellExists = true
				view.RetiredFromTitle = shell.Title
			}
		}
		out = append(out, view)
	}
	// Newest retirement first; a version without a stamp sorts last.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].RetiredAt, out[j].RetiredAt
		switch {
		case a == nil:
			return false
		case b == nil:
			return true
		default:
			return a.After(*b)
		}
	})
	httputil.RespondWithOK(c, gin.H{"versions": out, "count": len(out)})
}

// RestoreRetiredVersion un-retires one retired version: its rows go back under
// the shell book they were retired out of, and the now-empty version row is
// deleted. POST /audiobooks/:id/retired-versions/:vid/restore.
//
// STUB, not the finished undo (PLAN.md section 6.4). Three things PR 5/7 own:
//   - It is not journaled: nothing is written to the operation-change journal,
//     so this restore cannot itself be undone and the retire's journal is not
//     marked reverted.
//   - Each row's prior VersionID comes back empty (UnretireBookFiles' map is
//     nil). The real value is in the retire's journal; survivors' rows carried
//     none, so this is right for them and loses the value for the rest.
//   - The shell must still exist and be in the trash. A purged shell needs
//     the journaled recreate (RevertOperationForBooks), which is not built, so
//     that case answers 409 rather than guessing. The shell stays in the trash:
//     "restore as separate book" is the owner un-trashing it afterwards.
//
// A retried restore is safe: rows already moved are simply absent from the
// retired set, and the empty version row is deleted on the retry.
func (h *Handler) RestoreRetiredVersion(c *gin.Context) {
	bookID := c.Param("id")
	versionID := c.Param("vid")
	versions, files, ok := h.retiredCapabilities(c)
	if !ok {
		return
	}
	writer, ok := database.AsCapability[database.RetiredBookFileWriter](h.store)
	if !ok {
		httputil.RespondWithInternalError(c, "this store cannot restore retired book files")
		return
	}
	deleter, ok := database.AsCapability[retiredVersionDeleter](h.store)
	if !ok {
		httputil.RespondWithInternalError(c, "this store cannot delete book versions")
		return
	}

	ver, err := versions.GetBookVersion(versionID)
	if err != nil {
		httputil.InternalError(c, "failed to get book version", err)
		return
	}
	if ver == nil {
		httputil.RespondWithNotFound(c, "version", versionID)
		return
	}
	if ver.BookID != bookID {
		httputil.RespondWithBadRequest(c, "version/book mismatch")
		return
	}
	if ver.Status != database.BookVersionStatusRetired {
		httputil.RespondWithBadRequest(c, "version is not a retired version")
		return
	}
	if ver.RetiredFromBookID == "" {
		httputil.RespondWithBadRequest(c, "version records no book it was retired from")
		return
	}
	shell, err := h.store.GetBookByID(ver.RetiredFromBookID)
	if err != nil {
		httputil.InternalError(c, "failed to get the book this version was retired from", err)
		return
	}
	if shell == nil {
		httputil.RespondWithConflict(c, "the book this version was retired from no longer exists; restoring a purged book needs the journaled undo, which is not available yet")
		return
	}
	if shell.MarkedForDeletion == nil || !*shell.MarkedForDeletion {
		httputil.RespondWithConflict(c, "the book this version was retired from is not in the trash; restore only returns files to a trashed book")
		return
	}

	retiredRows, err := files.GetRetiredBookFiles(bookID)
	if err != nil {
		httputil.InternalError(c, "failed to get retired book files", err)
		return
	}
	var fileIDs []string
	for _, f := range retiredRows {
		if f.VersionID == versionID {
			fileIDs = append(fileIDs, f.ID)
		}
	}
	if len(fileIDs) > 0 {
		moves := []database.BookFileMove{{FileIDs: fileIDs, SourceBookID: bookID}}
		if err := writer.UnretireBookFiles(moves, ver.RetiredFromBookID, versionID, nil); err != nil {
			if errors.Is(err, database.ErrRetiredVersionMismatch) ||
				errors.Is(err, database.ErrBookFileNotRetired) ||
				errors.Is(err, database.ErrBookFileOwnerMissing) {
				httputil.RespondWithConflict(c, "the retired files changed since they were listed: "+err.Error())
				return
			}
			httputil.InternalError(c, "failed to restore retired version", err)
			return
		}
	}
	if err := deleter.DeleteBookVersion(versionID); err != nil {
		httputil.InternalError(c, "files restored but the empty version row could not be removed", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"version_id":       versionID,
		"restored_to_book": ver.RetiredFromBookID,
		"restored_files":   len(fileIDs),
	})
}
