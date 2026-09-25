// file: internal/server/handlers/admindebug/handler.go
// version: 1.0.0
// guid: 64d11bf2-1597-4007-b152-e2803366a922
// last-edited: 2026-09-25

// Package admindebug is the admin-only debug API for one-off record fixes:
// read a book or book_file row in full, find what references a path, and edit
// individual fields of a book or book_file row with a preview, an audit row
// and an undo.
//
// It deliberately does NOT reuse the book PUT route: that route writes tags
// into the audio file and enqueues an iTunes write-back. Everything here goes
// through the same database.Store methods the app uses (ModifyBook,
// ModifyBookFile), so memdb, the search index and the book aggregates follow
// the edit, and nothing touches a file on disk.
package admindebug

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Store is the slice of database.Store this handler uses.
type Store interface {
	GetBookByID(id string) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	LiveBookIDsAtPath(path string) ([]string, error)
	BookAtPathIndexBuilt() (bool, error)
	SetRaw(key string, value []byte) error
	GetRaw(key string) ([]byte, error)
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
}

// ActivityRecorder writes the audit row. *activity.Service satisfies it.
type ActivityRecorder interface {
	Record(entry database.ActivityEntry) error
}

// PathGuard reports whether a local path is outside every iTunes library
// root (nil) or not. merge.CheckPathOutsideITunes in production.
type PathGuard func(path string) error

// Handler serves /admin/debug/*.
type Handler struct {
	store    Store
	activity ActivityRecorder
	guard    PathGuard
	now      func() time.Time
	// undoMu serialises undo calls so one edit cannot be undone twice by two
	// concurrent requests (the record's undone flag is read, then written).
	undoMu sync.Mutex
}

// New builds the handler. activity may be nil, in which case reads work and
// every apply is refused with 503: the audit row is a required rail.
func New(store Store, activity ActivityRecorder) *Handler {
	return &Handler{store: store, activity: activity, guard: merge.CheckPathOutsideITunes, now: time.Now}
}

// Register mounts the routes under /admin/debug on rg, behind RequireAdmin
// (the admin ROLE, stricter than any single permission: a custom role granted
// settings.manage does not pass). Production wiring and the tests both call
// this, so the tests exercise the real gate.
func (h *Handler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/admin/debug")
	g.Use(servermiddleware.RequireAdmin())
	g.GET("/books/:id", h.GetBook)
	g.GET("/book-files/:id", h.GetBookFile)
	g.GET("/lookup", h.Lookup)
	g.PATCH("/books/:id", h.PatchBook)
	g.PATCH("/book-files/:id", h.PatchBookFile)
	g.GET("/edits", h.ListEdits)
	g.POST("/edits/:edit_id/undo", h.UndoEdit)
}

// ── entity specs ────────────────────────────────────────────────────────────

const (
	entityBook     = "book"
	entityBookFile = "book_file"
)

var bookSpec = &entitySpec{
	name:   entityBook,
	fields: buildFieldIndex(reflect.TypeOf(database.Book{})),
	immutable: map[string]string{
		"id":         "the primary key",
		"created_at": "set by the store",
		"updated_at": "set by the store",
		"author":     "a loaded relation, not a stored column; edit author_id",
		"authors":    "a loaded relation, not a stored column",
		"series":     "a loaded relation, not a stored column; edit series_id",
	},
	localPaths:  map[string]bool{"file_path": true, "source_import_path": true},
	itunesOwned: map[string]bool{"itunes_path": true},
}

var bookFileSpec = &entitySpec{
	name:   entityBookFile,
	fields: buildFieldIndex(reflect.TypeOf(database.BookFile{})),
	immutable: map[string]string{
		"id":         "the primary key",
		"book_id":    "part of the primary key; moving a file between books is a merge, not a field edit",
		"created_at": "set by the store",
		"updated_at": "set by the store",
	},
	localPaths:  map[string]bool{"file_path": true, "deluge_original_path": true},
	itunesOwned: map[string]bool{"itunes_path": true},
}

func specFor(entity string) (*entitySpec, any) {
	switch entity {
	case entityBook:
		return bookSpec, (*database.Book)(nil)
	case entityBookFile:
		return bookFileSpec, (*database.BookFile)(nil)
	}
	return nil, nil
}

// ── errors mapped to status codes ──────────────────────────────────────────

// guardError is an iTunes-guard refusal (422).
type guardError struct{ msg string }

func (e *guardError) Error() string { return e.msg }

// changedSinceError is an undo refusal because the record moved on (409).
type changedSinceError struct{ fields []fieldConflict }

func (e *changedSinceError) Error() string {
	names := make([]string, len(e.fields))
	for i, f := range e.fields {
		names[i] = f.Field
	}
	return "record changed since the edit in field(s) " + strings.Join(names, ", ") + "; pass force=true to undo anyway"
}

type fieldConflict struct {
	Field     string          `json:"field"`
	EditAfter json.RawMessage `json:"edit_after"`
	Current   json.RawMessage `json:"current"`
}

// ── reads ───────────────────────────────────────────────────────────────────

type groupMember struct {
	ID                string  `json:"id"`
	Title             string  `json:"title"`
	LibraryState      *string `json:"library_state,omitempty"`
	IsPrimaryVersion  *bool   `json:"is_primary_version,omitempty"`
	FilePath          string  `json:"file_path"`
	MarkedForDeletion *bool   `json:"marked_for_deletion,omitempty"`
}

// GetBook returns the full book row, every book_file row, and the version
// group's members.
func (h *Handler) GetBook(c *gin.Context) {
	id := c.Param("id")
	book, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.InternalError(c, "read book", err)
		return
	}
	if book == nil {
		httputil.RespondWithNotFound(c, "book", id)
		return
	}
	files, err := h.store.GetBookFiles(id)
	if err != nil {
		httputil.InternalError(c, "read book files", err)
		return
	}
	resp := gin.H{"book": book, "book_files": files, "book_file_count": len(files)}
	if book.VersionGroupID != nil && *book.VersionGroupID != "" {
		members, err := h.store.GetBooksByVersionGroup(*book.VersionGroupID)
		if err != nil {
			httputil.InternalError(c, "read version group", err)
			return
		}
		out := make([]groupMember, 0, len(members))
		for _, m := range members {
			out = append(out, groupMember{
				ID: m.ID, Title: m.Title, LibraryState: m.LibraryState,
				IsPrimaryVersion: m.IsPrimaryVersion, FilePath: m.FilePath,
				MarkedForDeletion: m.MarkedForDeletion,
			})
		}
		resp["version_group"] = gin.H{"id": *book.VersionGroupID, "members": out}
	}
	c.JSON(http.StatusOK, resp)
}

// fileByID resolves a book_file from its ID alone through the book_file_id
// index (database.BookFileByFileIDReader, resolved through the decorator
// chain). ok=false means the response has been written.
func (h *Handler) fileByID(c *gin.Context, fileID string) (*database.BookFile, bool) {
	r, ok := database.AsCapability[database.BookFileByFileIDReader](h.store)
	if !ok {
		httputil.RespondWithError(c, http.StatusNotImplemented,
			"this store cannot look a book_file up by its id alone", "capability_missing")
		return nil, false
	}
	f, err := r.GetBookFileByFileID(fileID)
	if err != nil {
		httputil.InternalError(c, "look up book file", err)
		return nil, false
	}
	if f == nil {
		httputil.RespondWithNotFound(c, "book_file", fileID)
		return nil, false
	}
	return f, true
}

// GetBookFile returns the book_file row and the id of the book that owns it.
func (h *Handler) GetBookFile(c *gin.Context) {
	f, ok := h.fileByID(c, c.Param("id"))
	if !ok {
		return
	}
	resp := gin.H{"book_file": f, "book_id": f.BookID}
	if book, err := h.store.GetBookByID(f.BookID); err != nil {
		httputil.InternalError(c, "read owning book", err)
		return
	} else if book != nil {
		resp["book"] = gin.H{"id": book.ID, "title": book.Title, "duration": book.Duration,
			"file_size": book.FileSize, "library_state": book.LibraryState}
	}
	c.JSON(http.StatusOK, resp)
}

// Lookup answers "which books and book_files reference exactly this path?"
// from the path indexes. It never scans the library: when an index cannot
// give a complete answer it returns 503 rather than a short list.
func (h *Handler) Lookup(c *gin.Context) {
	p := c.Query("path")
	if p == "" {
		httputil.RespondWithBadRequest(c, "path query parameter is required")
		return
	}
	files, err := h.store.BookFilesAtPath(p)
	if err != nil {
		if errors.Is(err, database.ErrBookFilesAtPathUnavailable) {
			httputil.RespondWithServiceUnavailable(c, "book_file path index cannot answer completely: "+err.Error())
			return
		}
		httputil.InternalError(c, "look up book files at path", err)
		return
	}
	if files == nil {
		files = []database.BookFile{}
	}
	resp := gin.H{"path": p, "book_files": files}
	built, err := h.store.BookAtPathIndexBuilt()
	if err != nil {
		httputil.InternalError(c, "check book path index", err)
		return
	}
	if !built {
		// Until the book_atpath backfill has run, LiveBookIDsAtPath scans
		// every book. Answer the book_file half and say why the book half
		// is missing, instead of scanning the library per request.
		resp["book_ids"] = nil
		resp["book_ids_unavailable"] = "the book_atpath index is not built yet (run its backfill)"
		c.JSON(http.StatusOK, resp)
		return
	}
	bookIDs, err := h.store.LiveBookIDsAtPath(p)
	if err != nil {
		httputil.InternalError(c, "look up books at path", err)
		return
	}
	if bookIDs == nil {
		bookIDs = []string{}
	}
	resp["book_ids"] = bookIDs
	c.JSON(http.StatusOK, resp)
}

// ── edits ───────────────────────────────────────────────────────────────────

type diffEntry struct {
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type notApplied struct {
	Field     string          `json:"field"`
	Requested json.RawMessage `json:"requested"`
	Stored    json.RawMessage `json:"stored"`
}

// editRecord is the durable undo image of one applied edit. Only the edited
// fields are kept, never the whole row (a book_file row carries ~230 KB of
// fingerprint).
type editRecord struct {
	EditID     string      `json:"edit_id"`
	EntityType string      `json:"entity_type"`
	EntityID   string      `json:"entity_id"`
	BookID     string      `json:"book_id,omitempty"`
	Actor      string      `json:"actor"`
	CreatedAt  time.Time   `json:"created_at"`
	Changes    []diffEntry `json:"changes"` // Before = pre-edit, After = as stored after the edit
	UndoneAt   *time.Time  `json:"undone_at,omitempty"`
	UndoneBy   string      `json:"undone_by,omitempty"`
	UndoForced bool        `json:"undo_forced,omitempty"`
}

// editKeyPrefix is the Pebble prefix the undo images live under, written
// through the store's existing SetRaw (synced) and listed with
// ScanPrefixPage. Edit IDs sort newest-first.
const editKeyPrefix = "admin_debug_edit:"

func (h *Handler) newEditID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	inv := uint64(math.MaxInt64 - h.now().UnixNano())
	return fmt.Sprintf("%016x-%s", inv, hex.EncodeToString(b[:]))
}

func actorOf(c *gin.Context) string {
	if u, ok := servermiddleware.CurrentUser(c); ok {
		return u.Username + " (" + u.ID + ")"
	}
	return "unknown"
}

// PatchBook previews (default) or applies (apply=true) a field edit of a book.
func (h *Handler) PatchBook(c *gin.Context) {
	h.patch(c, entityBook, c.Param("id"), "")
}

// PatchBookFile previews (default) or applies (apply=true) a field edit of a
// book_file. The book's aggregates (duration, size) are recomputed by the
// store's ModifyBookFile, exactly as for any other file write.
func (h *Handler) PatchBookFile(c *gin.Context) {
	f, ok := h.fileByID(c, c.Param("id"))
	if !ok {
		return
	}
	h.patch(c, entityBookFile, f.ID, f.BookID)
}

// readRecord returns the stored record (a *Book or *BookFile), or nil.
func (h *Handler) readRecord(entity, id, bookID string) (any, error) {
	switch entity {
	case entityBook:
		b, err := h.store.GetBookByID(id)
		if b == nil || err != nil {
			return nil, err
		}
		return b, nil
	default:
		f, err := h.store.GetBookFileByID(bookID, id)
		if f == nil || err != nil {
			return nil, err
		}
		return f, nil
	}
}

// modifyRecord runs fn on the stored record under the store's row lock and
// writes the result. found=false when the row does not exist.
func (h *Handler) modifyRecord(entity, id, bookID string, fn func(rec any) error) (bool, error) {
	switch entity {
	case entityBook:
		b, err := h.store.ModifyBook(id, func(b *database.Book) error { return fn(b) })
		return b != nil, err
	default:
		f, err := h.store.ModifyBookFile(bookID, id, func(f *database.BookFile) error { return fn(f) })
		return f != nil, err
	}
}

// pathOf decodes a path field's JSON (string or *string); null is "".
func pathOf(raw json.RawMessage) string {
	var s *string
	if json.Unmarshal(raw, &s) != nil || s == nil {
		return ""
	}
	return *s
}

// guardPaths enforces the iTunes rule for the path fields in changes: the
// books/itunes/** tree and every configured iTunes root are never mutated,
// so a path field may neither be moved INTO one nor moved OUT of one (the
// latter would re-point an iTunes row at a file the app then treats as its
// own). Non-path fields of a row that lives under iTunes stay editable: that
// changes the database row only, never the file. current/next give the
// field's value now and after the edit.
func (h *Handler) guardPaths(spec *entitySpec, fields []string, current, next map[string]json.RawMessage) error {
	for _, name := range fields {
		if !spec.localPaths[name] {
			continue
		}
		for _, side := range []struct {
			label string
			val   string
		}{{"current", pathOf(current[name])}, {"new", pathOf(next[name])}} {
			if side.val == "" {
				continue
			}
			if err := h.guard(side.val); err != nil {
				return &guardError{msg: fmt.Sprintf("%s.%s: the %s value %q is refused by the iTunes guard: %v",
					spec.name, name, side.label, side.val, err)}
			}
		}
	}
	return nil
}

func (h *Handler) patch(c *gin.Context, entity, id, bookID string) {
	spec, zero := specFor(entity)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(c.Request.Body).Decode(&raw); err != nil {
		httputil.RespondWithBadRequest(c, "body must be a JSON object of field -> value: "+err.Error())
		return
	}
	plan, err := spec.validatePatch(zero, raw)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	requested := make(map[string]json.RawMessage, len(plan.fields))
	for _, name := range plan.fields {
		cv, err := spec.canonical(zero, name, plan.raw[name])
		if err != nil {
			httputil.RespondWithBadRequest(c, err.Error())
			return
		}
		requested[name] = cv
	}
	apply := c.Query("apply") == "true"

	if !apply {
		// Preview: a plain read, no Modify* call (a skipped ModifyBook still
		// schedules a search re-index through the prod decorator).
		rec, err := h.readRecord(entity, id, bookID)
		if err != nil {
			httputil.InternalError(c, "read record", err)
			return
		}
		if rec == nil {
			httputil.RespondWithNotFound(c, entity, id)
			return
		}
		diff, current, err := diffAgainst(spec, rec, plan.fields, requested)
		if err != nil {
			httputil.InternalError(c, "diff", err)
			return
		}
		if err := h.guardPaths(spec, plan.fields, current, requested); err != nil {
			httputil.RespondWithError(c, http.StatusUnprocessableEntity, err.Error(), "itunes_protected")
			return
		}
		c.JSON(http.StatusOK, gin.H{"dry_run": true, "entity_type": entity, "entity_id": id,
			"book_id": bookIDFor(entity, id, bookID), "diff": diff})
		return
	}

	if h.activity == nil {
		httputil.RespondWithServiceUnavailable(c, "the activity log is not available, so an edit cannot be audited; refusing to apply")
		return
	}

	var bookBefore *database.Book
	if entity == entityBookFile {
		bookBefore, _ = h.store.GetBookByID(bookID)
	}

	var diff []diffEntry
	found, err := h.modifyRecord(entity, id, bookID, func(rec any) error {
		d, current, err := diffAgainst(spec, rec, plan.fields, requested)
		if err != nil {
			return err
		}
		if err := h.guardPaths(spec, plan.fields, current, requested); err != nil {
			return err
		}
		diff = d
		if len(d) == 0 {
			return skipFor(entity)
		}
		for _, e := range d {
			if err := spec.setField(rec, e.Field, e.After); err != nil {
				return err
			}
		}
		return nil
	})
	var ge *guardError
	switch {
	case errors.As(err, &ge):
		httputil.RespondWithError(c, http.StatusUnprocessableEntity, ge.Error(), "itunes_protected")
		return
	case err != nil:
		httputil.InternalError(c, "apply edit", err)
		return
	case !found:
		httputil.RespondWithNotFound(c, entity, id)
		return
	}
	if len(diff) == 0 {
		c.JSON(http.StatusOK, gin.H{"dry_run": false, "entity_type": entity, "entity_id": id,
			"book_id": bookIDFor(entity, id, bookID), "diff": []diffEntry{}, "no_change": true})
		return
	}

	// Re-read: the store normalises some values on the way in (a file
	// duration that looks like milliseconds, nil-means-preserve on book
	// pointers), so what landed can differ from what was asked for. The undo
	// image and the response describe what is STORED.
	stored, err := h.readRecord(entity, id, bookID)
	if err != nil || stored == nil {
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"edit applied but the row could not be re-read; no undo image was saved", "reread_failed",
			map[string]any{"diff": diff, "error": fmt.Sprint(err)})
		return
	}
	var missed []notApplied
	for i, e := range diff {
		now, err := spec.fieldJSON(stored, e.Field)
		if err != nil {
			httputil.InternalError(c, "encode stored field", err)
			return
		}
		if !jsonEqual(now, e.After) {
			missed = append(missed, notApplied{Field: e.Field, Requested: e.After, Stored: now})
		}
		diff[i].After = now
	}

	rec := editRecord{
		EditID: h.newEditID(), EntityType: entity, EntityID: id,
		BookID: bookIDFor(entity, id, bookID), Actor: actorOf(c), CreatedAt: h.now().UTC(), Changes: diff,
	}
	if err := h.saveEdit(&rec); err != nil {
		// The edit is applied; hand back the before-image so it can be
		// reverted by hand.
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"edit applied but the undo image could not be saved: "+err.Error(), "undo_save_failed",
			map[string]any{"diff": diff})
		return
	}
	resp := gin.H{"dry_run": false, "entity_type": entity, "entity_id": id,
		"book_id": rec.BookID, "diff": diff, "edit_id": rec.EditID}
	if len(missed) > 0 {
		resp["not_applied"] = missed
	}
	if entity == entityBookFile {
		h.reindexBook(bookID)
		if after, err := h.store.GetBookByID(bookID); err == nil && after != nil {
			resp["book_aggregates"] = gin.H{"before": aggregatesOf(bookBefore), "after": aggregatesOf(after)}
		}
	}
	if err := h.audit(c, "admin_debug_edit", &rec, rec.Changes, nil); err != nil {
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"edit applied and undo image saved, but the audit row failed: "+err.Error(), "audit_failed",
			map[string]any{"edit_id": rec.EditID, "diff": diff})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// diffAgainst compares rec's current field values with requested. It returns
// the changed fields (Before = current) and every requested field's current
// value.
func diffAgainst(spec *entitySpec, rec any, fields []string, requested map[string]json.RawMessage) ([]diffEntry, map[string]json.RawMessage, error) {
	current := make(map[string]json.RawMessage, len(fields))
	diff := []diffEntry{}
	for _, name := range fields {
		cur, err := spec.fieldJSON(rec, name)
		if err != nil {
			return nil, nil, err
		}
		current[name] = cur
		if !jsonEqual(cur, requested[name]) {
			diff = append(diff, diffEntry{Field: name, Before: cur, After: requested[name]})
		}
	}
	return diff, current, nil
}

func skipFor(entity string) error {
	if entity == entityBook {
		return database.ErrSkipBookWrite
	}
	return database.ErrSkipBookFileWrite
}

func bookIDFor(entity, id, bookID string) string {
	if entity == entityBook {
		return id
	}
	return bookID
}

func aggregatesOf(b *database.Book) gin.H {
	if b == nil {
		return nil
	}
	return gin.H{"duration": b.Duration, "file_size": b.FileSize}
}

// reindexBook schedules a search re-index of the book after a file edit. The
// aggregate recompute inside the Pebble store writes the book through the
// INNER store, which the prod search decorator never sees; a skipped
// ModifyBook through the decorated store schedules the re-index and writes
// nothing (see indexedStore.ModifyBook).
func (h *Handler) reindexBook(bookID string) {
	_, _ = h.store.ModifyBook(bookID, func(*database.Book) error { return database.ErrSkipBookWrite })
}

func (h *Handler) audit(c *gin.Context, typ string, rec *editRecord, changes []diffEntry, extra map[string]any) error {
	fields := make([]string, len(changes))
	for i, ch := range changes {
		fields[i] = ch.Field
	}
	details := map[string]any{
		"edit_id":     rec.EditID,
		"actor":       actorOf(c),
		"entity_type": rec.EntityType,
		"entity_id":   rec.EntityID,
		"fields":      fields,
		"changes":     changes,
	}
	for k, v := range extra {
		details[k] = v
	}
	verb := "edit"
	if typ == "admin_debug_undo" {
		verb = "undo of edit " + rec.EditID
	}
	return h.activity.Record(database.ActivityEntry{
		Timestamp: h.now().UTC(),
		Tier:      "audit",
		Type:      typ,
		Level:     "info",
		Source:    "admin_debug",
		BookID:    rec.BookID,
		Summary:   fmt.Sprintf("admin debug %s: %s %s (%s)", verb, rec.EntityType, rec.EntityID, strings.Join(fields, ", ")),
		Details:   details,
	})
}

func (h *Handler) saveEdit(rec *editRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return h.store.SetRaw(editKeyPrefix+rec.EditID, b)
}

func (h *Handler) loadEdit(editID string) (*editRecord, error) {
	b, err := h.store.GetRaw(editKeyPrefix + editID)
	if err != nil || b == nil {
		return nil, err
	}
	var rec editRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("decode edit %s: %w", editID, err)
	}
	return &rec, nil
}

// ListEdits lists recent edits, newest first.
func (h *Handler) ListEdits(c *gin.Context) {
	limit := 50
	if s := c.Query("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			httputil.RespondWithBadRequest(c, "limit must be 1..500")
			return
		}
		limit = n
	}
	pairs, next, err := h.store.ScanPrefixPage(editKeyPrefix, c.Query("after"), limit)
	if err != nil {
		httputil.InternalError(c, "list edits", err)
		return
	}
	out := make([]editRecord, 0, len(pairs))
	for _, p := range pairs {
		var rec editRecord
		if err := json.Unmarshal(p.Value, &rec); err != nil {
			httputil.InternalError(c, "decode edit "+p.Key, err)
			return
		}
		out = append(out, rec)
	}
	c.JSON(http.StatusOK, gin.H{"edits": out, "next": next})
}

// UndoEdit restores the before-values of an applied edit. It is refused (409)
// when any edited field no longer holds the value the edit stored, unless
// force=true, and refused when the edit was already undone. The restore goes
// through the same iTunes guard and the same store write as the edit.
func (h *Handler) UndoEdit(c *gin.Context) {
	if h.activity == nil {
		httputil.RespondWithServiceUnavailable(c, "the activity log is not available, so an undo cannot be audited; refusing")
		return
	}
	h.undoMu.Lock()
	defer h.undoMu.Unlock()

	editID := c.Param("edit_id")
	rec, err := h.loadEdit(editID)
	if err != nil {
		httputil.InternalError(c, "read edit", err)
		return
	}
	if rec == nil {
		httputil.RespondWithNotFound(c, "edit", editID)
		return
	}
	if rec.UndoneAt != nil {
		httputil.RespondWithConflict(c, fmt.Sprintf("edit %s was already undone at %s by %s", editID, rec.UndoneAt.Format(time.RFC3339), rec.UndoneBy))
		return
	}
	spec, _ := specFor(rec.EntityType)
	if spec == nil {
		httputil.InternalError(c, "undo", fmt.Errorf("edit %s has unknown entity type %q", editID, rec.EntityType))
		return
	}
	force := c.Query("force") == "true"
	fields := make([]string, len(rec.Changes))
	restore := make(map[string]json.RawMessage, len(rec.Changes))
	for i, ch := range rec.Changes {
		fields[i] = ch.Field
		restore[ch.Field] = ch.Before
	}

	var conflicts []fieldConflict
	found, err := h.modifyRecord(rec.EntityType, rec.EntityID, rec.BookID, func(row any) error {
		current := make(map[string]json.RawMessage, len(fields))
		conflicts = nil
		for _, ch := range rec.Changes {
			cur, err := spec.fieldJSON(row, ch.Field)
			if err != nil {
				return err
			}
			current[ch.Field] = cur
			if !jsonEqual(cur, ch.After) {
				conflicts = append(conflicts, fieldConflict{Field: ch.Field, EditAfter: ch.After, Current: cur})
			}
		}
		if len(conflicts) > 0 && !force {
			return &changedSinceError{fields: conflicts}
		}
		if err := h.guardPaths(spec, fields, current, restore); err != nil {
			return err
		}
		for _, ch := range rec.Changes {
			if err := spec.setField(row, ch.Field, ch.Before); err != nil {
				return err
			}
		}
		return nil
	})
	var ce *changedSinceError
	var ge *guardError
	switch {
	case errors.As(err, &ce):
		httputil.RespondWithErrorFields(c, http.StatusConflict, ce.Error(), "changed_since_edit",
			map[string]any{"conflicts": ce.fields})
		return
	case errors.As(err, &ge):
		httputil.RespondWithError(c, http.StatusUnprocessableEntity, ge.Error(), "itunes_protected")
		return
	case err != nil:
		httputil.InternalError(c, "undo edit", err)
		return
	case !found:
		httputil.RespondWithNotFound(c, rec.EntityType, rec.EntityID)
		return
	}

	var missed []notApplied
	if stored, err := h.readRecord(rec.EntityType, rec.EntityID, rec.BookID); err == nil && stored != nil {
		for _, ch := range rec.Changes {
			now, ferr := spec.fieldJSON(stored, ch.Field)
			if ferr == nil && !jsonEqual(now, ch.Before) {
				missed = append(missed, notApplied{Field: ch.Field, Requested: ch.Before, Stored: now})
			}
		}
	}
	if rec.EntityType == entityBookFile {
		h.reindexBook(rec.BookID)
	}

	undoneAt := h.now().UTC()
	rec.UndoneAt = &undoneAt
	rec.UndoneBy = actorOf(c)
	rec.UndoForced = force && len(conflicts) > 0
	resp := gin.H{"edit_id": rec.EditID, "entity_type": rec.EntityType, "entity_id": rec.EntityID,
		"book_id": rec.BookID, "restored": rec.Changes, "forced": rec.UndoForced}
	if len(conflicts) > 0 {
		resp["overwrote_later_changes"] = conflicts
	}
	if len(missed) > 0 {
		resp["not_applied"] = missed
	}
	if err := h.saveEdit(rec); err != nil {
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"undo applied but the edit could not be marked undone: "+err.Error(), "undo_mark_failed", resp)
		return
	}
	undoChanges := make([]diffEntry, len(rec.Changes))
	for i, ch := range rec.Changes {
		undoChanges[i] = diffEntry{Field: ch.Field, Before: ch.After, After: ch.Before}
	}
	if err := h.audit(c, "admin_debug_undo", rec, undoChanges, map[string]any{"forced": rec.UndoForced}); err != nil {
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"undo applied, but the audit row failed: "+err.Error(), "audit_failed", resp)
		return
	}
	c.JSON(http.StatusOK, resp)
}
