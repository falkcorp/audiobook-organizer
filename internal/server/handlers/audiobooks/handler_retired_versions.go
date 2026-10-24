// file: internal/server/handlers/audiobooks/handler_retired_versions.go
// version: 1.1.0
// guid: 6818f543-1756-455b-9346-0581d8885fb8
// last-edited: 2026-10-23

// Retired-versions endpoints for the book detail page: the read that feeds the
// "Retired versions" panel, and the per-version restore, which answers 501 until
// restores are journaled. A retired version is a
// book_versions row of status "retired" that holds the retired book_file rows
// of a shell book (trashed or merged) that the retire-versions repair moved
// onto a live "holder" book. See docs and PLAN.md section 3 for the model.

package audiobookshandler

import (
	"net/http"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// retiredVersionReader is the version-row side of the store the listing
// needs. AudiobooksStore does not carry it (and is not widened for it: that
// would regenerate its mock), so it is resolved through database.AsCapability
// like the other optional store methods of this package.
type retiredVersionReader interface {
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
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
	ID                string `json:"id"`
	BookID            string `json:"book_id"`
	RetiredFromBookID string `json:"retired_from_book_id,omitempty"`
	RetiredFromTitle  string `json:"retired_from_title,omitempty"`
	ShellExists       bool   `json:"shell_exists"`
	// ShellInTrash is true when the shell still exists and is in the trash: the
	// state a restore needs it in, so the page can say why one is not offered.
	ShellInTrash       bool                 `json:"shell_in_trash"`
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

// retiredCapabilities resolves the store capabilities the listing needs and
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
// and cannot answer this.) A retired version with no rows left holds nothing to
// show or restore, so it is not listed.
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
	// The panel lists a version's files in playback order. The store already
	// returns disc, track, path order; sorting here keeps the listing's order
	// this handler's own guarantee rather than a store implementation detail.
	for _, rows := range rowsByVersion {
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].DiscNumber != rows[j].DiscNumber {
				return rows[i].DiscNumber < rows[j].DiscNumber
			}
			if rows[i].TrackNumber != rows[j].TrackNumber {
				return rows[i].TrackNumber < rows[j].TrackNumber
			}
			return rows[i].FilePath < rows[j].FilePath
		})
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
				view.ShellInTrash = shell.MarkedForDeletion != nil && *shell.MarkedForDeletion
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

// restoreNotJournaledMessage is the 501 body of RestoreRetiredVersion and the
// reason the web panel shows on its disabled Restore button.
const restoreNotJournaledMessage = "restoring a retired version is not available yet: restores will be available once they are journaled"

// RestoreRetiredVersion is the per-version restore route,
// POST /audiobooks/:id/retired-versions/:vid/restore. It answers 501 Not
// Implemented and changes nothing.
//
// The restore that would un-retire the version's rows and delete the version
// row must not run until it is journaled (PLAN.md section 6.4,
// TODO-RETIRED-RESTORE-JOURNAL): done directly it clears every row's VersionID
// (the torrent/ingest link, whose prior value lives only in the retire's
// journal), deletes the version row that carries RetireOpID, leaves no journal
// or activity entry, and cannot be undone. The route stays registered, with its
// permission guard, so the client contract exists and the real restore replaces
// this body; it deliberately reads nothing from the store either, so an
// unknown version or book is not told apart from a known one.
func (h *Handler) RestoreRetiredVersion(c *gin.Context) {
	httputil.RespondWithError(c, http.StatusNotImplemented, restoreNotJournaledMessage, "NOT_IMPLEMENTED")
}
