// file: internal/server/handlers/abs/stream.go
// version: 1.1.0
// guid: e2b19c74-3d05-4f81-a6c3-58790ed4b23f
// last-edited: 2026-08-17

package abs

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// fileNotFound answers the client's 404 unchanged and records WHY on the server.
//
// 🔴 FIVE DIFFERENT FAILURES USED TO SHARE ONE SILENT RETURN. The body stays
// "file not found" for every one of them — that is the protocol contract and the
// client can do nothing with a finer distinction — but the server could not tell
// them apart either, and nothing was logged at all. The reasons are genuinely
// different problems with different fixes:
//
//	no_ino             the request carried no ino segment
//	no_syncfile        no sync file with that ino belongs to this book
//	no_bookfile_row    the sync file's CurrentFileID matches no book_file row
//	abs_path_failed    filepath.Abs rejected the stored path
//	bytes_missing      the row and the path exist; the FILE does not
//
// This is not hypothetical tidiness. Diagnosing the live "can't find the file"
// reports on 2026-08-17 took four separate production probes precisely because the
// server had recorded 1,036 of these 404s without recording which kind any of them
// was. The answer turned out to be bytes_missing — 41.8% of book_file rows point at
// files that are not on disk — and one line of this log would have said so.
func fileNotFound(c *gin.Context, reason string, attrs ...any) {
	args := append([]any{
		"reason", reason,
		"item", c.Param("id"),
		"ino", c.Param("ino"),
		"path", c.Request.URL.Path,
	}, attrs...)
	slog.Warn("abs: file not found", args...)
	respondError(c, http.StatusNotFound, "file not found")
}

// Byte-serving paths. BOTH of these exist because the two target clients address
// audio completely differently (§1.8.3):
//
//	Absorb      derives /api/items/{itemId}/file/{ino} itself from the item and
//	            validates the segment count; a mismatch fails the entire download.
//	AudioBooth  has NO contentUrl field at all (zero repo-wide hits) and streams
//	            exclusively from /public/session/{sessionId}/track/{index}, which must
//	            be UNAUTHENTICATED and byte-ranged.
//
// All actual byte serving goes through httputil.ServeFileWithRange, which is already
// verified against a real 115 MB m4b: 206/416, If-Range, open-ended ranges, and
// SUFFIX ranges. Suffix ranges are not optional — iOS AVPlayer issues tail requests
// to locate `moov` in m4b files where it sits after `mdat`, so prefix-only Range
// support silently breaks playback (§1.6 item 9).

// ItemFile handles GET /api/items/:id/file/:ino.
func (h *Handler) ItemFile(c *gin.Context) {
	h.serveItemFile(c, false)
}

// ItemFileDownload handles GET /api/items/:id/file/:ino/download — the path
// AudioBooth's DownloadManager uses, with the Authorization header
// (DownloadManager.swift:598); the watch variant sends ?token= instead, which the ABS
// auth middleware already accepts on GETs.
func (h *Handler) ItemFileDownload(c *gin.Context) {
	h.serveItemFile(c, true)
}

func (h *Handler) serveItemFile(c *gin.Context, asAttachment bool) {
	book := h.resolveItem(c)
	if book == nil {
		return
	}
	ino := strings.TrimSpace(c.Param("ino"))
	if ino == "" {
		fileNotFound(c, "no_ino")
		return
	}

	// The ino is resolved WITHIN this book, never globally.
	//
	// That scoping is the security boundary, not decoration: ListSyncFilesForBook is
	// filtered to book.ID, so an ino belonging to another item does not resolve here.
	// The path handed to the file server therefore always comes from a store lookup
	// keyed by (book, file), never from a request path segment — which is exactly the
	// contract ServeFileWithRange documents, and this repo has a history of
	// path-injection findings.
	syncFiles, err := h.identity.ListSyncFilesForBook(book.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not resolve file")
		return
	}
	fileID := ""
	for i := range syncFiles {
		if syncFiles[i].SyncFileID == ino {
			fileID = syncFiles[i].CurrentFileID
			break
		}
	}
	if fileID == "" {
		// The ino is not one of this book's sync files. Either it belongs to another
		// item (which the book-scoped lookup is deliberately refusing to serve) or the
		// client is working from a stale item payload.
		fileNotFound(c, "no_syncfile", "book", book.ID, "syncfiles", len(syncFiles))
		return
	}

	files, err := h.library.GetBookFiles(book.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not resolve file")
		return
	}
	path := ""
	for i := range files {
		if files[i].ID == fileID {
			path = files[i].FilePath
			break
		}
	}
	if path == "" {
		// The sync file points at a book_file that is not among this book's rows, or
		// the row it names carries an empty path. Both are database inconsistencies
		// rather than anything the request did wrong.
		fileNotFound(c, "no_bookfile_row", "book", book.ID, "file_id", fileID, "book_files", len(files))
		return
	}

	h.serveAudio(c, path, asAttachment)
}

// PublicSessionTrack handles GET /public/session/:id/track/:index.
//
// 🔓 UNAUTHENTICATED BY PROTOCOL REQUIREMENT (§1.8.3).
// AudioBooth streams every byte of audio from this path and from nowhere else. Its
// AVURLAsset does now carry custom headers (issue #237 added
// AVURLAssetHTTPHeaderFieldsKey), which is why the surface needs no Cloudflare Access
// bypass — but the ABS protocol itself defines this route as public at the app layer,
// so requiring our bearer here would simply break playback.
//
// What guards it: the session id is a freshly minted 122-bit UUID that is never
// logged to a client-visible surface, is scoped to ONE book for ONE user, and stops
// resolving when the session expires or is closed. It is a capability URL, and it is
// the narrowest one the protocol permits.
func (h *Handler) PublicSessionTrack(c *gin.Context) {
	session, ok := h.sessions.get(strings.TrimSpace(c.Param("id")))
	if !ok {
		respondError(c, http.StatusNotFound, "session not found")
		return
	}
	// Tracks are 1-based (ABS indexes from 1), and Paths[0] is deliberately unused so
	// the client-visible index maps straight onto the slice.
	index, err := strconv.Atoi(strings.TrimSpace(c.Param("index")))
	if err != nil || index < 1 || index >= len(session.Paths) {
		respondError(c, http.StatusNotFound, "track not found")
		return
	}
	path := session.Paths[index]
	if path == "" {
		respondError(c, http.StatusNotFound, "track not found")
		return
	}
	h.serveAudio(c, path, false)
}

// serveAudio is the single place audio bytes leave this process.
//
// Concentrating it here means the Range/conditional-request behaviour, the
// Content-Type mapping and the path contract are identical on the authenticated and
// the public path — a divergence there is exactly the kind of bug that shows up as
// "seeking works over wifi but not in the car".
func (h *Handler) serveAudio(c *gin.Context, path string, asAttachment bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		fileNotFound(c, "abs_path_failed", "stored_path", path, "err", err)
		return
	}
	if asAttachment {
		// strconv.Quote gives a correctly-escaped quoted-string, so a filename holding
		// a space, a comma or a quote cannot split or terminate the header early.
		c.Header("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(abs)))
	}
	if err := httputil.ServeFileWithRange(c.Writer, c.Request, abs, httputil.Options{
		ContentType: mimeTypeForPath(abs),
	}); err != nil {
		// A missing or unreadable file is a 404, not a 500: the row exists, the bytes
		// do not, and a client should treat it as "not available" rather than retrying
		// a server fault forever.
		//
		// 🔴 THIS IS THE ONE THAT ACTUALLY FIRES. Measured 2026-08-17: 41.8% of
		// book_file rows point at a path with no file behind it, all of them under the
		// organizer's own destination tree. The served path is logged because it is the
		// single most useful fact for the next person — it says WHICH tree the row
		// pointed into, which is what separated "files are gone" from "the organizer
		// recorded destinations it never populated".
		fileNotFound(c, "bytes_missing", "served_path", abs, "err", err)
		return
	}
	// The body is already written; stop gin from letting any later middleware append
	// to it (an appended byte would corrupt a 206 response).
	c.Abort()
}
