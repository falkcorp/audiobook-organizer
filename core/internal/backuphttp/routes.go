// file: core/internal/backuphttp/routes.go
// version: 1.0.3
// guid: 0a0db04f-4bc6-4082-bcec-81463f47c6bb
// last-edited: 2026-11-12

// Package backuphttp is the HTTP surface of core's backup and restore
// (05-backup §5B.3.8): the catalogue (list, get, verify, import, download,
// delete, rewrap), the create route that only enqueues a task, and the staged
// restore routes (restore, revert, cancel, pending), all registered on
// httpapi's core group (/api/core/v1).
//
// The service is received as backuphttp-local seams (Catalogue and Restorer
// are backup's own interfaces, Rewrapper and Enqueuer are one method each),
// never as the kernel's wiring and never as *storage.Admin. Every handler
// repeats the permission and guard checks of its route, so a mounting mistake
// never opens one, and a collaborator that is nil answers 503 only after those
// checks, so an unauthorised caller learns nothing about availability.
//
// The package is dark until F8-04 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package backuphttp

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Catalogue is the archive-directory half of backup.Service: list, verify,
// import, download, delete.
type Catalogue = backup.Catalogue

// Restorer is the restore half of backup.Service: stage, cancel, report and
// undo.
type Restorer = backup.Restorer

// Rewrapper re-creates an Own archive under the active derivation key
// (backup.Service.Rewrap).
type Rewrapper interface {
	Rewrap(ctx context.Context, id backup.ArchiveID) (backup.Archive, error)
}

// Enqueuer enqueues the core.backup.create task and returns its ID. The route
// never runs a backup on the request goroutine (05-backup §5B.6). requestedBy
// is the user ID of the principal that asked.
type Enqueuer interface {
	EnqueueCreate(ctx context.Context, o backup.CreateOptions, requestedBy string) (taskID string, err error)
}

// Recorder is the audit log's synchronous write: create, import and download
// are recorded before the action and a failed write fails the request
// (audit.Logger.Record).
type Recorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// Deps are what Register needs. Every field may be nil at registration; a
// collaborator a route needs that is nil answers 503 after the route's
// permission and guards, and a nil OwnerRequired means the owner is required.
type Deps struct {
	Catalogue Catalogue
	Restorer  Restorer
	Rewrapper Rewrapper
	Enqueuer  Enqueuer
	Audit     Recorder
	// OwnerRequired reports core.auth.owner.required (05-authn §4.8, SEC4-14).
	// When false the owner-guarded routes fall back to Guards.Interactive plus
	// core:admin, because no principal could hold the owner assurance.
	OwnerRequired func() bool
	// MaxImportBytes bounds an import's body; zero is 64 GiB.
	MaxImportBytes int64
	// Log receives the cause of an internal failure, which is withheld from
	// the body. nil logs nothing.
	Log *slog.Logger
}

// defaultMaxImportBytes bounds an import when Deps.MaxImportBytes is zero.
const defaultMaxImportBytes int64 = 64 << 30

// The scopes the routes need (05-rbac catalog). core:backup.restore is
// declared Sensitive and AuthRequired in the catalog; a route names it like
// any other scope and the checker refuses it to a non-interactive principal.
const (
	permSystemView    rbac.Permission = "core:system.view"
	permSystemManage  rbac.Permission = "core:system.manage"
	permBackupRestore rbac.Permission = "core:backup.restore"
)

// The audit actions 05-backup §5B.3.8 fixes for the routes that record. The
// restore routes record nothing here: the service audits core.restore.stage
// itself.
const (
	actionCreate   = "core.backup.create"
	actionImport   = "core.backup.import"
	actionDownload = "core.backup.download"
)

// The route patterns the audit records name.
const (
	routeCreate   = "POST /api/core/v1/backups"
	routeImport   = "POST /api/core/v1/backups/import"
	routeDownload = "GET /api/core/v1/backups/{id}/download"
)

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	cat           Catalogue
	rst           Restorer
	rw            Rewrapper
	enq           Enqueuer
	audit         Recorder
	ownerRequired func() bool
	maxImport     int64
	log           *slog.Logger
}

func newAPI(d Deps) *api {
	a := &api{cat: d.Catalogue, rst: d.Restorer, rw: d.Rewrapper, enq: d.Enqueuer, audit: d.Audit,
		ownerRequired: d.OwnerRequired, maxImport: d.MaxImportBytes, log: d.Log}
	if a.maxImport <= 0 {
		a.maxImport = defaultMaxImportBytes
	}
	return a
}

// gate is a route's permission and guards, which both the router (through
// Route.Perm and Route.Guards) and the handler (through authorise) read from
// the one table.
type gate struct {
	perm        rbac.Permission
	interactive bool
	owner       bool
	// anon lets a principal with no user (the auth-disabled one) through the
	// view routes, as the router does; every other route needs a user to name
	// as the actor.
	anon bool
}

// routeDef is one row of the route table.
type routeDef struct {
	method, path, name, summary, doc string
	gate                             gate
	maxBody                          int64
	rate                             httpapi.RateClass
	raw                              httpapi.RawDoc
	handler                          http.HandlerFunc
}

// Register registers the backup routes on g, which must be the router's
// CoreGroup(). Each route is registered with httpapi.HandleRaw because
// download streams, import reads its own body, pending answers 204 or 200 and
// restore answers 202.
func Register(g *httpapi.Group, d Deps) {
	for _, def := range newAPI(d).routeTable() {
		httpapi.HandleRaw(g, httpapi.Route{
			Method: def.method, Path: def.path, Name: def.name,
			Perm:   rbac.Need(def.gate.perm),
			Guards: httpapi.Guards{Interactive: def.gate.interactive, Owner: def.gate.owner},
			Tags:   []string{"backup"}, MaxBody: def.maxBody, RateClass: def.rate,
			Summary: def.summary, Doc: def.doc,
		}, def.raw, def.handler)
	}
}

// routeTable is the route table of 05-backup §5B.3.8. Register and the tests
// both read it, so the table the router enforces is the table the tests drive.
func (a *api) routeTable() []routeDef {
	view := gate{perm: permSystemView, anon: true}
	manage := gate{perm: permSystemManage}
	manageInteractive := gate{perm: permSystemManage, interactive: true}
	download := gate{perm: rbac.CoreAdmin, interactive: true, owner: true}
	restore := gate{perm: permBackupRestore, interactive: true, owner: true}
	pending := gate{perm: permBackupRestore}
	const idDoc = "404 archive_not_found for an ID no archive has. "
	return []routeDef{
		{method: http.MethodGet, path: "/backups", name: "listBackups", gate: view,
			summary: "List the archives in the backup directory",
			raw:     httpapi.RawDoc{Success: httpapi.BodyOf[listResponse]()}, handler: a.wrap(view, a.list)},
		{method: http.MethodGet, path: "/backups/{id}", name: "getBackup", gate: view,
			summary: "Read one archive", doc: idDoc,
			raw: httpapi.RawDoc{Success: httpapi.BodyOf[archiveDTO]()}, handler: a.wrap(view, a.get)},
		{method: http.MethodPost, path: "/backups/{id}/verify", name: "verifyBackup", gate: manage,
			summary: "Verify an archive", maxBody: maxBody, rate: httpapi.RateExpensive,
			doc: `Body (optional) {level: "checksum", "entries" (default) or "open"}. ` + idDoc +
				`409 archive_verification_failed when the archive does not verify; 400 archive_has_no_checksum.`,
			raw: httpapi.RawDoc{Request: httpapi.BodyOf[verifyRequest](), Success: httpapi.BodyOf[verifyDTO]()}, handler: a.wrap(manage, a.verify)},
		{method: http.MethodPost, path: "/backups", name: "createBackup", gate: manage,
			summary: "Enqueue a backup task", maxBody: maxBody,
			doc: `Body (optional) {reason?, include_derived?, include_recreatable?}. Audited as core.backup.create before the task is enqueued. ` +
				`202 {task_id}: the backup runs as a task, never on this request.`,
			raw: httpapi.RawDoc{Request: httpapi.BodyOf[createRequest](), Success: httpapi.BodyOf[createResponse](), Status: http.StatusAccepted}, handler: a.wrap(manage, a.create)},
		{method: http.MethodDelete, path: "/backups/{id}", name: "deleteBackup", gate: manageInteractive,
			summary: "Delete an archive",
			doc:     idDoc + "409 protected_by_floor for an archive a retention floor protects.",
			raw:     httpapi.RawDoc{Status: http.StatusNoContent}, handler: a.wrap(manageInteractive, a.remove)},
		{method: http.MethodPost, path: "/backups/import", name: "importBackup", gate: manageInteractive,
			summary: "Import an archive", maxBody: -1, rate: httpapi.RateExpensive,
			doc: `The archive is the raw request body, or the "file" part of a multipart/form-data body. The "name" query parameter is required and checked ` +
				`(a plain name: no path separator, no ".."), and is only a suggestion: the archive is stored under a name the service chooses, which the ` +
				`response gives. The body is bounded (413 body_too_large). Audited as core.backup.import before the archive is read. ` +
				`201 with the catalogue entry; its provenance says whether it is this install's.`,
			raw: httpapi.RawDoc{Success: httpapi.BodyOf[archiveDTO](), Status: http.StatusCreated, Query: []string{"name"}}, handler: a.wrap(manageInteractive, a.importArchive)},
		{method: http.MethodGet, path: "/backups/{id}/download", name: "downloadBackup", gate: download,
			summary: "Download an archive as stored", rate: httpapi.RateExpensive,
			doc: idDoc + "An archive is credential-equivalent, so this needs core:admin, an interactive session and the owner. " +
				"Audited as core.backup.download before the stream starts.",
			raw: httpapi.RawDoc{}, handler: a.wrap(download, a.download)},
		{method: http.MethodPost, path: "/backups/{id}/restore", name: "restoreBackup", gate: restore,
			summary: "Stage a restore; it runs when the service restarts", maxBody: maxBody,
			doc: `Body (optional) {verify?: "checksum", "entries" or "open" (default), allow_weak_verify?, foreign?, replace_config?, accept_unreadable_secrets?, restart?}. ` +
				idDoc + `A foreign restore also needs core:admin (403 otherwise); 403 foreign_archive_requires_flag for a foreign archive without foreign; ` +
				`409 archive_verification_failed, archive_incompatible, unreadable_secrets, swap_pending, rename_refused, backup_in_progress; 507 insufficient_space. ` +
				`202 {pending, restart_required: true}. The service audits core.restore.stage.`,
			raw: httpapi.RawDoc{Request: httpapi.BodyOf[restoreRequest](), Success: httpapi.BodyOf[restoreResponse](), Status: http.StatusAccepted}, handler: a.wrap(restore, a.restore)},
		{method: http.MethodPost, path: "/restore/revert", name: "revertRestore", gate: restore,
			summary: "Stage the undo of the last applied restore", maxBody: maxBody,
			doc: `Body (optional) the restore body without foreign (ignored). 404 no_undo_point when no applied restore has a kept previous store; 409 swap_pending. ` +
				`202 {pending, restart_required: true}.`,
			raw: httpapi.RawDoc{Request: httpapi.BodyOf[restoreRequest](), Success: httpapi.BodyOf[restoreResponse](), Status: http.StatusAccepted}, handler: a.wrap(restore, a.revert)},
		{method: http.MethodDelete, path: "/restore/pending", name: "cancelRestore", gate: restore,
			summary: "Cancel the staged restore",
			raw:     httpapi.RawDoc{Status: http.StatusNoContent}, handler: a.wrap(restore, a.cancel)},
		{method: http.MethodGet, path: "/restore/pending", name: "getPendingRestore", gate: pending,
			summary: "Read the staged restore",
			doc:     "200 the pending restore, or 204 when none is staged.",
			raw:     httpapi.RawDoc{Success: httpapi.BodyOf[pendingDTO]()}, handler: a.wrap(pending, a.pending)},
		{method: http.MethodPost, path: "/backups/{id}/rewrap", name: "rewrapBackup", gate: manageInteractive,
			summary: "Re-create an archive under the active derivation key", rate: httpapi.RateExpensive,
			doc: idDoc + "409 protected_by_floor never applies here: the original is deleted only when no floor protects it.",
			raw: httpapi.RawDoc{Success: httpapi.BodyOf[archiveDTO]()}, handler: a.wrap(manageInteractive, a.rewrap)},
	}
}

// caller is the authorised principal a handler acts for.
type caller struct {
	p       *auth.Principal
	subject string // the user's ID, "" for the auth-disabled principal on a view route
}

// actor is the audit actor for c, built from the principal so the record names
// the user whatever stage 12 bound.
func (c caller) actor(r *http.Request) audit.Actor {
	a := audit.Actor{UserID: c.subject, Method: string(c.p.Method), CredentialID: c.p.CredentialID, UserAgent: userAgent(r)}
	if c.p.User != nil {
		a.Username = c.p.User.Username
	}
	if c.p.ClientAddr.IsValid() {
		a.ClientAddr = c.p.ClientAddr.String()
	}
	return a
}

// wrap makes the handler a http.Handler that first repeats the route's
// permission and guards (authorise), so a mounting mistake never opens it.
func (a *api) wrap(g gate, h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		subject, err := a.authorise(p, g)
		if err != nil {
			httpapi.WriteError(w, r, err, a.log)
			return
		}
		h(w, r, caller{p: p, subject: subject})
	}
}

// holds reports whether p holds perm, by both of the checker's answers (a
// scope held, and a requirement for it satisfied: core:admin holds nothing
// Sensitive for a non-interactive principal).
func holds(p *auth.Principal, perm rbac.Permission) bool {
	return p.Perms != nil && p.Perms.Has(perm) && p.Perms.Satisfies(rbac.Need(perm))
}

// authorise is the repetition of the route's permission and guards, in the
// order stage 13 refuses them: a principal (401), the permission (403), an
// interactive credential (403) and, while the owner is required, the owner
// assurance (403) or, once it is not, core:admin. Only the handler's own
// 503 for a missing collaborator comes after it, so an unauthorised caller
// learns nothing about availability.
func (a *api) authorise(p *auth.Principal, g gate) (string, error) {
	subject := ""
	if p != nil && p.User != nil {
		subject = string(p.User.ID)
	}
	ownerNeeded := a.ownerRequired == nil || a.ownerRequired()
	switch {
	case p == nil || (!g.anon && subject == ""):
		return "", &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
	case !holds(p, g.perm):
		return "", httpapi.Forbidden(g.perm)
	case (g.interactive || g.owner) && !p.Method.MayChangeCredentials():
		return "", &httpapi.Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: "this route needs an interactive session"}
	case g.owner && ownerNeeded && p.Assurance&auth.AssuranceOwner == 0:
		return "", &httpapi.Error{Status: http.StatusForbidden, Code: "owner_required", Detail: "this route needs the deployment owner"}
	case g.owner && !ownerNeeded && !holds(p, rbac.CoreAdmin):
		return "", httpapi.Forbidden(rbac.CoreAdmin)
	}
	return subject, nil
}

// unavailable is the 503 of a collaborator that is not wired.
func unavailable(what string) error {
	return httpapi.Unavailable(what+" is not available", 5*time.Second)
}

// ---- reads ----------------------------------------------------------------

func (a *api) list(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.cat == nil {
		a.fail(w, r, unavailable("backup"))
		return
	}
	arcs, err := a.cat.List(r.Context())
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	out := listResponse{Backups: make([]archiveDTO, 0, len(arcs))}
	for _, ar := range arcs {
		out.Backups = append(out.Backups, archiveOf(ar))
	}
	a.json(r.Context(), w, http.StatusOK, out)
}

func (a *api) get(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.cat == nil {
		a.fail(w, r, unavailable("backup"))
		return
	}
	id, err := archiveID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ar, err := a.find(r.Context(), id)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	a.json(r.Context(), w, http.StatusOK, archiveOf(ar))
}

// find is the catalogue entry of id: the catalogue has no single-entry read,
// and List never hashes files (05-backup §5B.3.5).
func (a *api) find(ctx context.Context, id backup.ArchiveID) (backup.Archive, error) {
	arcs, err := a.cat.List(ctx)
	if err != nil {
		return backup.Archive{}, err
	}
	for _, ar := range arcs {
		if ar.ID == id {
			return ar, nil
		}
	}
	return backup.Archive{}, backup.ErrNotFound
}

func (a *api) pending(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.rst == nil {
		a.fail(w, r, unavailable("restore"))
		return
	}
	p, err := a.rst.Pending(r.Context())
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	if p == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.json(r.Context(), w, http.StatusOK, pendingOf(*p))
}

// ---- catalogue writes -----------------------------------------------------

func (a *api) verify(w http.ResponseWriter, r *http.Request, c caller) {
	var req verifyRequest
	if err := decodeOptional(w, r, &req, "level"); err != nil {
		a.fail(w, r, err)
		return
	}
	level, err := parseLevel(req.Level, backup.VerifyEntries, "/body/level")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.cat == nil {
		a.fail(w, r, unavailable("backup"))
		return
	}
	id, err := archiveID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.cat.Verify(audit.WithActor(r.Context(), c.actor(r)), id, level)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	a.json(r.Context(), w, http.StatusOK, verifyOf(id, res))
}

func (a *api) create(w http.ResponseWriter, r *http.Request, c caller) {
	var req createRequest
	if err := decodeOptional(w, r, &req, "reason, include_derived and include_recreatable"); err != nil {
		a.fail(w, r, err)
		return
	}
	reason, err := cleanReason(req.Reason)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.enq == nil || a.audit == nil {
		a.fail(w, r, unavailable("backup"))
		return
	}
	ctx := audit.WithActor(r.Context(), c.actor(r))
	ev, err := a.recordFirst(ctx, r, c, actionCreate, routeCreate, "create_requested",
		map[string]any{"reason": reason, "include_derived": req.IncludeDerived, "include_recreatable": req.IncludeRecreatable})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	taskID, err := a.enq.EnqueueCreate(ctx, backup.CreateOptions{Reason: reason, IncludeDerived: req.IncludeDerived, IncludeRecreatable: req.IncludeRecreatable}, c.subject)
	if err != nil {
		a.recordFailure(ctx, ev, err)
		a.refuse(w, r, err)
		return
	}
	a.json(ctx, w, http.StatusAccepted, createResponse{TaskID: taskID})
}

func (a *api) remove(w http.ResponseWriter, r *http.Request, c caller) {
	if a.cat == nil {
		a.fail(w, r, unavailable("backup"))
		return
	}
	id, err := archiveID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.cat.Delete(audit.WithActor(r.Context(), c.actor(r)), id); err != nil {
		a.refuse(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) rewrap(w http.ResponseWriter, r *http.Request, c caller) {
	if a.rw == nil {
		a.fail(w, r, unavailable("backup"))
		return
	}
	id, err := archiveID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ar, err := a.rw.Rewrap(audit.WithActor(r.Context(), c.actor(r)), id)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	a.json(r.Context(), w, http.StatusOK, archiveOf(ar))
}

// importArchive reads the archive from the body (raw, or the "file" part of a
// multipart body) through a bounded reader and hands it to Catalogue.Import
// with the validated name, which the service ignores (it stores the archive
// under a name of its own). Nothing is buffered here: the service streams it to
// disk.
func (a *api) importArchive(w http.ResponseWriter, r *http.Request, c caller) {
	if a.cat == nil || a.audit == nil {
		a.fail(w, r, unavailable("backup import"))
		return
	}
	name, err := cleanName(r.URL.Query().Get("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if r.ContentLength > a.maxImport {
		a.fail(w, r, errTooLarge())
		return
	}
	var src io.Reader = http.MaxBytesReader(w, r.Body, a.maxImport)
	// A Content-Type that does not parse leaves it unknown whether the body is a
	// multipart wrapper or the archive itself; the request is refused rather
	// than guessed at.
	mt, params, perr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if perr != nil && r.Header.Get("Content-Type") != "" {
		a.fail(w, r, &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_upload", Detail: "the Content-Type header could not be parsed"})
		return
	}
	if mt == "multipart/form-data" {
		part, err := filePart(src, params["boundary"])
		if err != nil {
			a.fail(w, r, err)
			return
		}
		src = part
	}
	ctx := audit.WithActor(r.Context(), c.actor(r))
	ev, err := a.recordFirst(ctx, r, c, actionImport, routeImport, "import_requested", map[string]any{"name": name})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ar, err := a.cat.Import(ctx, name, src)
	if err != nil {
		a.recordFailure(ctx, ev, err)
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "import")+string(ar.ID))
	a.json(ctx, w, http.StatusCreated, archiveOf(ar))
}

// filePart finds the multipart body's "file" part. Reading the part headers
// reads none of its content.
func filePart(body io.Reader, boundary string) (io.Reader, error) {
	if boundary == "" {
		return nil, &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_upload", Detail: "the multipart body has no boundary"}
	}
	mr := multipart.NewReader(body, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, &httpapi.Error{Status: http.StatusBadRequest, Code: "file_required", Detail: `the multipart body has no "file" part`}
		}
		if err != nil {
			if pe, ok := mapError(err); ok {
				return nil, pe
			}
			return nil, &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_upload", Detail: "the multipart body could not be read"}
		}
		if part.FormName() == "file" {
			return part, nil
		}
	}
}

// download streams the archive as stored. The audit record is written before
// anything is looked up or opened, so a taken archive is never unrecorded; the
// archive is never buffered.
func (a *api) download(w http.ResponseWriter, r *http.Request, c caller) {
	if a.cat == nil || a.audit == nil {
		a.fail(w, r, unavailable("backup download"))
		return
	}
	id, err := archiveID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := audit.WithActor(r.Context(), c.actor(r))
	ev, err := a.recordFirst(ctx, r, c, actionDownload, routeDownload, "download_requested", map[string]any{"archive_id": string(id)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ar, err := a.find(ctx, id)
	if err != nil {
		a.recordFailure(ctx, ev, err)
		a.refuse(w, r, err)
		return
	}
	rc, err := a.cat.Download(ctx, id)
	if err != nil {
		a.recordFailure(ctx, ev, err)
		a.refuse(w, r, err)
		return
	}
	defer func() {
		if err := rc.Close(); err != nil {
			a.logf(ctx, "backuphttp: close archive", err)
		}
	}()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", dispositionOf(ar))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		// The status is sent: the client sees a truncated body, the operator
		// sees why.
		a.logf(ctx, "backuphttp: stream archive", err)
	}
}

// ---- restore --------------------------------------------------------------

func (a *api) restore(w http.ResponseWriter, r *http.Request, c caller) {
	var req restoreRequest
	if err := decodeOptional(w, r, &req, "verify, allow_weak_verify, foreign, replace_config, accept_unreadable_secrets and restart"); err != nil {
		a.fail(w, r, err)
		return
	}
	// A foreign restore plants another install's data, so it also needs
	// core:admin (05-backup §5B.3.8); the body says whether it is one.
	if req.Foreign && !holds(c.p, rbac.CoreAdmin) {
		a.fail(w, r, httpapi.Forbidden(rbac.CoreAdmin))
		return
	}
	opts, err := req.options()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.rst == nil {
		a.fail(w, r, unavailable("restore"))
		return
	}
	id, err := archiveID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.rst.StageRestore(a.restoreCtx(r, c), id, opts)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	a.json(r.Context(), w, http.StatusAccepted, restoreResponse{Pending: pendingOf(p), RestartRequired: true})
}

func (a *api) revert(w http.ResponseWriter, r *http.Request, c caller) {
	var req restoreRequest
	if err := decodeOptional(w, r, &req, "verify, allow_weak_verify, foreign, replace_config, accept_unreadable_secrets and restart"); err != nil {
		a.fail(w, r, err)
		return
	}
	opts, err := req.options()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// The undo point is this install's own store: the foreign flag means nothing
	// here and is never passed on, so it cannot buy what it would on a restore.
	opts.Foreign = false
	if a.rst == nil {
		a.fail(w, r, unavailable("restore"))
		return
	}
	p, err := a.rst.RevertLastRestore(a.restoreCtx(r, c), opts)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	a.json(r.Context(), w, http.StatusAccepted, restoreResponse{Pending: pendingOf(p), RestartRequired: true})
}

func (a *api) cancel(w http.ResponseWriter, r *http.Request, c caller) {
	if a.rst == nil {
		a.fail(w, r, unavailable("restore"))
		return
	}
	if err := a.rst.CancelRestore(audit.WithActor(r.Context(), c.actor(r))); err != nil {
		a.refuse(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// restoreCtx is the context a restore route hands the service: the audit actor
// and the proof the route established. authorise has already required an
// interactive credential and the owner's assurance (or, once the owner is not
// required, core:admin), so the proof is owner-level; Admin says whether the
// person also holds core:admin, which a foreign restore needs. The service's
// gate (backup.ErrRestoreNotAuthorized) is the one every caller, this route and
// the stage-restore task alike, passes.
func (a *api) restoreCtx(r *http.Request, c caller) context.Context {
	ctx := audit.WithActor(r.Context(), c.actor(r))
	return backup.WithRestoreProof(ctx, backup.RestoreProof{Owner: true, Admin: holds(c.p, rbac.CoreAdmin)})
}

// ---- audit ----------------------------------------------------------------

// recordFirst writes the audit record of an action before the action runs. A
// failed write stops the request with the action not taken.
func (a *api) recordFirst(ctx context.Context, r *http.Request, c caller, action, route, reason string, details map[string]any) (audit.Event, error) {
	ev := audit.Event{
		Action: action, Outcome: audit.Success, Actor: c.actor(r), Route: route,
		RequestID: httpapi.RequestID(r.Context()), Reason: reason, Details: details,
	}
	if err := a.audit.Record(ctx, ev); err != nil {
		a.logf(ctx, "backuphttp: audit record", err)
		return ev, unavailable("the audit log")
	}
	return ev, nil
}

// recordFailure writes the second record of an action that was recorded as
// requested and then refused or failed. A write that fails here is logged: the
// request already answers the action's error, and the first record stands.
func (a *api) recordFailure(ctx context.Context, ev audit.Event, cause error) {
	ev.Outcome, ev.Reason = audit.Failure, "refused"
	if !isRefusal(cause) {
		ev.Outcome, ev.Reason = audit.Error, "failed"
	}
	if err := a.audit.Record(ctx, ev); err != nil {
		a.logf(ctx, "backuphttp: audit failure record", err)
	}
}

// logf logs err, which a collaborator wrote, only to the operator's log.
func (a *api) logf(ctx context.Context, msg string, err error) {
	if a.log != nil {
		a.log.ErrorContext(ctx, msg, "error", err)
	}
}

// userAgent is the request's User-Agent cut to the 256 bytes an audit actor
// keeps, on a rune boundary.
func userAgent(r *http.Request) string {
	ua := r.UserAgent()
	if len(ua) > 256 {
		ua = strings.ToValidUTF8(ua[:256], "")
	}
	return ua
}
