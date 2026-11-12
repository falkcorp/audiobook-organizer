// file: core/internal/backuphttp/errors.go
// version: 1.0.1
// guid: 6608474f-18e9-44e0-a1fe-7233ad5bc90b
// last-edited: 2026-11-12

package backuphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ---- response bodies ------------------------------------------------------

// listResponse is GET /backups.
type listResponse struct {
	Backups []archiveDTO `json:"backups"`
}

// archiveDTO is one catalogue entry. It names the archive's file (a plain
// name), never a path, and carries a manifest's reason and versions when the
// archive has one; install IDs, key IDs and MACs stay out.
type archiveDTO struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Size        int64      `json:"size"`
	CreatedAt   string     `json:"created_at"`
	Format      string     `json:"format"`
	Provenance  string     `json:"provenance"`
	Reason      string     `json:"reason,omitempty"`
	AppVersion  string     `json:"app_version,omitempty"`
	CoreVersion string     `json:"core_version,omitempty"`
	Verified    *verifyDTO `json:"verified,omitempty"`
}

// verifyDTO is a verification's result.
type verifyDTO struct {
	ArchiveID string `json:"archive_id,omitempty"`
	At        string `json:"at"`
	Level     string `json:"level"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
}

// createResponse is the 202 of POST /backups.
type createResponse struct {
	TaskID string `json:"task_id"`
}

// pendingDTO is the staged restore.
type pendingDTO struct {
	ArchiveID     string   `json:"archive_id"`
	Kind          string   `json:"kind"`
	StagedAt      string   `json:"staged_at"`
	ExpiresAt     string   `json:"expires_at"`
	StagedByBuild string   `json:"staged_by_build,omitempty"`
	RequestedBy   string   `json:"requested_by,omitempty"`
	Warnings      []string `json:"warnings"`
}

// restoreResponse is the 202 of a staged restore or revert: the swap happens
// at the next start (05-backup §5B.3.4 step 9), so a restart is always owed.
type restoreResponse struct {
	Pending         pendingDTO `json:"pending"`
	RestartRequired bool       `json:"restart_required"`
}

func archiveOf(a backup.Archive) archiveDTO {
	d := archiveDTO{ID: string(a.ID), Name: a.Name, Size: a.Size, CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
		Format: a.Format, Provenance: a.Provenance.String()}
	if m := a.Manifest; m != nil {
		d.Reason, d.AppVersion, d.CoreVersion = m.Reason, m.AppVersion, m.CoreVersion
	}
	if a.Verified != nil {
		v := verifyOf("", *a.Verified)
		d.Verified = &v
	}
	return d
}

func verifyOf(id backup.ArchiveID, v backup.VerifyResult) verifyDTO {
	return verifyDTO{ArchiveID: string(id), At: v.At.UTC().Format(time.RFC3339), Level: v.Level.String(), OK: v.OK, Detail: v.Detail}
}

func pendingOf(p backup.PendingRestore) pendingDTO {
	return pendingDTO{
		ArchiveID: string(p.ArchiveID), Kind: kindName(p.Kind),
		StagedAt: p.StagedAt.UTC().Format(time.RFC3339), ExpiresAt: p.ExpiresAt.UTC().Format(time.RFC3339),
		StagedByBuild: p.StagedByBuild, RequestedBy: p.RequestedBy, Warnings: append([]string{}, p.Warnings...),
	}
}

func kindName(k journal.RestoreKind) string {
	switch k {
	case journal.KindArchive:
		return "archive"
	case journal.KindRevert:
		return "revert"
	case journal.KindCheckpoint:
		return "checkpoint"
	case journal.KindExternal:
		return "external"
	}
	return "invalid"
}

// json writes v as application/json. It encodes before writing any header, so
// an encode failure is still a clean 500.
func (a *api) json(ctx context.Context, w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		a.logf(ctx, "backuphttp: encode response", err)
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		a.logf(ctx, "backuphttp: write response", err)
	}
}

// fail writes err through httpapi: an *httpapi.Error as its status, a plain
// error as the 500 with the cause logged and withheld from the body.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpapi.WriteError(w, r, err, a.log)
}

// refuse answers an error a collaborator returned: its mapped problem, else a
// 500 whose cause is logged only.
func (a *api) refuse(w http.ResponseWriter, r *http.Request, err error) {
	if pe, ok := mapError(err); ok {
		a.fail(w, r, pe)
		return
	}
	a.fail(w, r, err)
}

// ---- request decoding -----------------------------------------------------

// maxBody bounds a JSON request: a handful of short members.
const maxBody = 4 << 10

// createRequest is the body of POST /backups.
type createRequest struct {
	Reason             string `json:"reason"`
	IncludeDerived     bool   `json:"include_derived"`
	IncludeRecreatable bool   `json:"include_recreatable"`
}

// verifyRequest is the body of POST /backups/{id}/verify.
type verifyRequest struct {
	Level string `json:"level"`
}

// restoreRequest is the body of a restore or revert: backup.RestoreOptions on
// the wire.
type restoreRequest struct {
	Verify                  string `json:"verify"`
	AllowWeakVerify         bool   `json:"allow_weak_verify"`
	Foreign                 bool   `json:"foreign"`
	ReplaceConfig           bool   `json:"replace_config"`
	AcceptUnreadableSecrets bool   `json:"accept_unreadable_secrets"`
	Restart                 bool   `json:"restart"`
}

// options is the request as backup.RestoreOptions; verify defaults to open.
func (q restoreRequest) options() (backup.RestoreOptions, error) {
	level, err := parseLevel(q.Verify, backup.VerifyOpen, "/body/verify")
	if err != nil {
		return backup.RestoreOptions{}, err
	}
	return backup.RestoreOptions{Verify: level, AllowWeakVerify: q.AllowWeakVerify, Foreign: q.Foreign,
		ReplaceConfig: q.ReplaceConfig, AcceptUnreadableSecrets: q.AcceptUnreadableSecrets, Restart: q.Restart}, nil
}

// parseLevel reads a verification level's name, def when it is empty.
func parseLevel(s string, def backup.VerifyLevel, at string) (backup.VerifyLevel, error) {
	switch s {
	case "":
		return def, nil
	case "checksum":
		return backup.VerifyChecksum, nil
	case "entries":
		return backup.VerifyEntries, nil
	case "open":
		return backup.VerifyOpen, nil
	}
	return backup.VerifyInvalid, httpapi.Invalid(httpapi.Field(at, "invalid_enum", "must be checksum, entries or open"))
}

// isJSON reports whether a Content-Type header names application/json.
func isJSON(h string) bool {
	mt, _, _ := strings.Cut(h, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "application/json")
}

func malformed(detail string) *httpapi.Error {
	return &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: detail}
}

// errTooLarge is the 413 of a body past its bound.
func errTooLarge() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Detail: "the body is too large"}
}

// decodeOptional reads r's body strictly into dst. An empty body leaves dst at
// its zero value (every member of these requests has a default); a body needs
// Content-Type application/json, is bounded, takes no unknown member and no
// trailing data. fields names the accepted members for the unknown_field
// answer. The body is not echoed, nor is encoding/json's text, which quotes
// members.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any, fields string) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			return errTooLarge()
		}
		return malformed("the body could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if !isJSON(r.Header.Get("Content-Type")) {
		return &httpapi.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Detail: "Content-Type must be application/json"}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
			return httpapi.Invalid(httpapi.Field("/body/"+te.Field, "invalid_type", "has the wrong JSON type"))
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &httpapi.Error{Status: http.StatusBadRequest, Code: "unknown_field",
				Detail: "the body has a member this route does not accept",
				Fields: []httpapi.FieldError{httpapi.Field("/body", "unknown_field", "only "+fields+" are accepted")}}
		}
		return malformed("the body is not a JSON object")
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return malformed("trailing data after the JSON value")
	}
	return nil
}

// archiveID is the path's {id}, or a 404 archive_not_found for one that no
// archive can have (a shape the package never issues).
func archiveID(r *http.Request) (backup.ArchiveID, error) {
	id := r.PathValue("id")
	if !backup.ValidArchiveID(id) {
		return "", errNotFound()
	}
	return backup.ArchiveID(id), nil
}

func errNotFound() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "archive_not_found", Detail: "no archive has that ID"}
}

// maxReasonRunes bounds a create's free-text reason.
const maxReasonRunes = 128

// cleanReason defaults the reason to "manual" and refuses control characters
// and an over-long text.
func cleanReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "manual", nil
	}
	if len([]rune(s)) > maxReasonRunes || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", httpapi.Invalid(httpapi.Field("/body/reason", "invalid", fmt.Sprintf("must be at most %d characters with no control character", maxReasonRunes)))
	}
	return s, nil
}

// maxNameBytes bounds an import's file name.
const maxNameBytes = 200

// cleanName is the import's file name: a plain name, with no path separator,
// no "..", no NUL or control character, at most 200 bytes. Anything else is a
// 400 invalid_name; the offending text is not echoed.
func cleanName(s string) (string, error) {
	bad := func(why string) (string, error) {
		return "", &httpapi.Error{Status: http.StatusBadRequest, Code: "invalid_name", Detail: "name must be a plain file name: " + why}
	}
	switch {
	case s == "":
		return bad("it is required")
	case len(s) > maxNameBytes:
		return bad("it is too long")
	case strings.ContainsAny(s, `/\`):
		return bad("it has a path separator")
	case strings.Contains(s, ".."):
		return bad(`it contains ".."`)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return bad("it has a control character")
	}
	return s, nil
}

// dispositionOf is the Content-Disposition of a download: the archive's own
// file name reduced to letters, digits, dot, dash and underscore, so no
// header or path character can come out of it.
func dispositionOf(a backup.Archive) string {
	name := a.Name
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
	name = strings.TrimLeft(name, ".")
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	if name == "" {
		name = string(a.ID) + ".archive"
	}
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}

// ---- the service's refusals -----------------------------------------------

// mapError maps the sentinels of the backup service to the statuses and codes
// 05-backup §5B.5 fixes. Every text here is fixed: the errors' own text can
// carry a path or a key ID and is never sent. A second result of false means
// the error is not a refusal, and the router answers it 500 with the cause
// logged and withheld.
func mapError(err error) (*httpapi.Error, bool) {
	p := func(status int, code, detail string) (*httpapi.Error, bool) {
		return &httpapi.Error{Status: status, Code: code, Detail: detail}, true
	}
	if uf, ok := errors.AsType[*storage.ErrUpgradeFloor](err); ok {
		detail := "the archive is below this build's upgrade floor; nothing was staged"
		if uf.InstallFirst != "" {
			detail += "; install " + uf.InstallFirst + " first"
		}
		return p(http.StatusConflict, "archive_incompatible", detail)
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return errTooLarge(), true
	}
	switch {
	case errors.Is(err, backup.ErrNotFound):
		return p(http.StatusNotFound, "archive_not_found", "no archive has that ID")
	case errors.Is(err, backup.ErrNoUndoPoint):
		return p(http.StatusNotFound, "no_undo_point", "there is no applied restore to undo")
	case errors.Is(err, backup.ErrForeignArchive):
		return p(http.StatusForbidden, "foreign_archive_requires_flag", "the archive was not made by this install; restoring it needs foreign")
	case errors.Is(err, backup.ErrProtectedByFloor):
		return p(http.StatusConflict, "protected_by_floor", "a retention floor protects the archive")
	case errors.Is(err, backup.ErrChecksumMismatch), errors.Is(err, backup.ErrManifest):
		return p(http.StatusConflict, "archive_verification_failed", "the archive does not verify; nothing was staged")
	case errors.Is(err, backup.ErrNoChecksum):
		return p(http.StatusBadRequest, "archive_has_no_checksum", "the archive has no checksum to verify against")
	case errors.Is(err, backup.ErrWrongApp), errors.Is(err, backup.ErrBackupTooNew):
		return p(http.StatusConflict, "archive_incompatible", "the archive belongs to another app or needs a newer build; nothing was staged")
	case errors.Is(err, backup.ErrUnreadableSecrets):
		return p(http.StatusConflict, "unreadable_secrets", "the keyring lacks keys the archive's secrets need; accept_unreadable_secrets stages it anyway")
	case errors.Is(err, restorejournal.ErrSwapPending):
		return p(http.StatusConflict, "swap_pending", "a restore or reset is already staged or running")
	case errors.Is(err, backup.ErrBackupInProgress):
		return p(http.StatusConflict, "backup_in_progress", "another backup or restore is running")
	case errors.Is(err, backup.ErrInsufficientSpace):
		return p(http.StatusInsufficientStorage, "insufficient_space", "there is not enough disk space")
	case errors.Is(err, backup.ErrRestoreStale):
		return p(http.StatusConflict, "restore_stale", "the staged restore expired or was staged by another build; stage it again")
	case errors.Is(err, restorejournal.ErrRenameRefused):
		return p(http.StatusConflict, "rename_refused", "a file the restore must replace cannot be swapped by rename here; nothing was staged")
	case errors.Is(err, backup.ErrCrossDevice):
		return p(http.StatusInternalServerError, "staging_cross_device", "the backup directory is not on the database's filesystem")
	}
	return nil, false
}
