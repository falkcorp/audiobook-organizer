// file: core/internal/updaterhttp/routes.go
// version: 1.0.0
// guid: 3e8a5c21-7d49-4b06-a1f3-90c2d7e4b518
// last-edited: 2026-11-18

// Package updaterhttp is the HTTP surface of core's updater (05-updater
// §5U.10): the status read and the three routes that only start a task (check,
// apply, rollback), registered on httpapi's core group (/api/core/v1).
//
// The updater is received as an updaterhttp-local seam (Updater is two
// methods, Starter one per task), never as the kernel's wiring. Every handler
// repeats the permission and guard checks of its route, so a mounting mistake
// never opens one, and a collaborator that is nil answers 503 only after those
// checks, so an unauthorised caller learns nothing about availability. No
// route installs anything on the request goroutine: apply and rollback enqueue
// their task, whose body is the only code that replaces the binary.
//
// The package is dark until F8-04 mounts it: nothing imports it before then,
// and the gin /api/v1/update/* routes keep answering as before.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package updaterhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/updater"
)

// Updater is the part of *updater.Updater the routes read: the status, and the
// apply preconditions that need no network (an apply the updater would refuse
// is answered here with its reason instead of as a failed task).
type Updater interface {
	Status() updater.Status
	Prepare(version string) (updater.Target, error)
}

// Starter enqueues the three updater tasks and returns the run's ID.
// requestedBy is the user ID of the principal that asked. The routes never
// check, download or swap on the request goroutine.
type Starter interface {
	StartCheck(ctx context.Context, requestedBy string) (taskID string, err error)
	StartApply(ctx context.Context, p updater.ApplyParams, requestedBy string) (taskID string, err error)
	StartRollback(ctx context.Context, requestedBy string) (taskID string, err error)
}

// Deps are what Register needs. Every field may be nil at registration; a
// collaborator a route needs that is nil answers 503 after the route's
// permission and guards.
type Deps struct {
	Updater Updater
	Starter Starter
	// Log receives the cause of an internal failure, which is withheld from
	// the body. nil logs nothing.
	Log *slog.Logger
}

// The scopes the routes need (05-rbac catalog).
const (
	permSystemManage rbac.Permission = "core:system.manage"
	permAdmin        rbac.Permission = rbac.CoreAdmin
)

// maxBody bounds the apply request's JSON body: one short member.
const maxBody = 1 << 10

// gate is a route's permission and guards, which both the router (through
// Route.Perm and Route.Guards) and the handler (through authorise) read from
// the one table.
type gate struct {
	perm        rbac.Permission
	interactive bool
}

type routeDef struct {
	method, path, name, summary, doc string
	gate                             gate
	maxBody                          int64
	raw                              httpapi.RawDoc
	handler                          http.HandlerFunc
}

type api struct {
	upd Updater
	st  Starter
	log *slog.Logger
}

// Register registers the updater routes on g, which must be the router's
// CoreGroup().
func Register(g *httpapi.Group, d Deps) {
	for _, def := range (&api{upd: d.Updater, st: d.Starter, log: d.Log}).routeTable() {
		httpapi.HandleRaw(g, httpapi.Route{
			Method: def.method, Path: def.path, Name: def.name,
			Perm:   rbac.Need(def.gate.perm),
			Guards: httpapi.Guards{Interactive: def.gate.interactive},
			Tags:   []string{"updater"}, MaxBody: def.maxBody,
			Summary: def.summary, Doc: def.doc,
		}, def.raw, def.handler)
	}
}

// routeTable is the route table of 05-updater §5U.10. Register and the tests
// both read it, so the table the router enforces is the table the tests drive.
func (a *api) routeTable() []routeDef {
	manage := gate{perm: permSystemManage}
	admin := gate{perm: permAdmin, interactive: true}
	return []routeDef{
		{method: http.MethodGet, path: "/updater", name: "getUpdater", gate: manage,
			summary: "Read the updater's status",
			raw:     httpapi.RawDoc{Success: httpapi.BodyOf[updater.StatusView]()}, handler: a.wrap(manage, a.status)},
		{method: http.MethodPost, path: "/updater/check", name: "checkUpdater", gate: manage,
			summary: "Enqueue a check for a newer release",
			doc:     "202 {task_id}: the check runs as a task, never on this request. It only records what it finds; nothing is installed.",
			raw:     httpapi.RawDoc{Success: httpapi.BodyOf[startResponse](), Status: http.StatusAccepted}, handler: a.wrap(manage, a.check)},
		{method: http.MethodPost, path: "/updater/apply", name: "applyUpdate", gate: admin,
			summary: "Enqueue the install of a release", maxBody: maxBody,
			doc: `Body (optional) {version?}: a release tag such as v1.2.3, or empty for the release the last check offered. This needs core:admin and an ` +
				`interactive session. 422 invalid_version for a malformed tag; 409 updater_off, unversioned_build, local_build_needs_version, no_update, ` +
				`not_newer. 202 {task_id}: the install runs as a task and restarts the service when it is done.`,
			raw: httpapi.RawDoc{Request: httpapi.BodyOf[applyRequest](), Success: httpapi.BodyOf[startResponse](), Status: http.StatusAccepted}, handler: a.wrap(admin, a.apply)},
		{method: http.MethodPost, path: "/updater/rollback", name: "rollbackUpdate", gate: admin,
			summary: "Enqueue the return to the previous binary",
			doc: "This needs core:admin and an interactive session. 202 {task_id}: the rollback runs as a task, checks that the previous binary can read the " +
				"store, and restarts the service when it is done.",
			raw: httpapi.RawDoc{Success: httpapi.BodyOf[startResponse](), Status: http.StatusAccepted}, handler: a.wrap(admin, a.rollback)},
	}
}

// startResponse is the 202 of check, apply and rollback.
type startResponse struct {
	TaskID string `json:"task_id"`
}

// applyRequest is the body of POST /updater/apply.
type applyRequest struct {
	Version string `json:"version"`
}

// caller is the authorised principal a handler acts for.
type caller struct{ subject string }

// wrap makes the handler a http.Handler that first repeats the route's
// permission and guards (authorise), so a mounting mistake never opens it.
func (a *api) wrap(g gate, h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		subject, err := authorise(p, g)
		if err != nil {
			httpapi.WriteError(w, r, err, a.log)
			return
		}
		h(w, r, caller{subject: subject})
	}
}

// authorise repeats stage 13's judgement of the route's permission and guards
// (httpapi.Authorize, over the same Perm and Guards Register declares), so a
// handler mounted without the router refuses exactly what the router would,
// in the same order: a principal (401), the interactive guard (403
// interactive_required), then the permission (403 permission_denied). The one
// rule of its own comes after Authorize admits: every route names an actor, so
// a principal with no user (the auth-disabled one) is answered 403
// interactive_required (httpapi.UserRequired, F4-08 ruling G5(a)). It passes
// no rbac catalog, so a non-interactive principal missing a Sensitive scope is
// told permission_denied where the router says interactive_required; the
// router judges first, so a caller never sees the difference.
func authorise(p *auth.Principal, g gate) (string, error) {
	subject := ""
	if p != nil && p.User != nil {
		subject = string(p.User.ID)
	}
	d := httpapi.Authorize(httpapi.AuthzRequest{
		Perm: rbac.Need(g.perm), Guards: httpapi.Guards{Interactive: g.interactive}, Principal: p,
	}, httpapi.AuthzOptions{OwnerRequired: true})
	if d.Refusal != nil {
		return "", d.Refusal
	}
	if subject == "" {
		return "", httpapi.UserRequired(p)
	}
	return subject, nil
}

func unavailable() error {
	return httpapi.Unavailable("the updater is not available", 5*time.Second)
}

// ---- handlers --------------------------------------------------------------

func (a *api) status(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.upd == nil {
		httpapi.WriteError(w, r, unavailable(), a.log)
		return
	}
	a.json(r.Context(), w, http.StatusOK, a.upd.Status().View())
}

func (a *api) check(w http.ResponseWriter, r *http.Request, c caller) {
	if a.st == nil {
		httpapi.WriteError(w, r, unavailable(), a.log)
		return
	}
	id, err := a.st.StartCheck(r.Context(), c.subject)
	a.started(w, r, id, err)
}

func (a *api) apply(w http.ResponseWriter, r *http.Request, c caller) {
	var req applyRequest
	if err := decodeOptional(w, r, &req); err != nil {
		httpapi.WriteError(w, r, err, a.log)
		return
	}
	if a.st == nil || a.upd == nil {
		httpapi.WriteError(w, r, unavailable(), a.log)
		return
	}
	version := strings.TrimSpace(req.Version)
	// The refusals that need no network are answered here, so an apply the
	// task would only refuse never becomes a failed run. The task repeats
	// every one of them before it downloads anything.
	if _, err := a.upd.Prepare(version); err != nil {
		httpapi.WriteError(w, r, mapPrepare(err), a.log)
		return
	}
	id, err := a.st.StartApply(r.Context(), updater.ApplyParams{Version: version}, c.subject)
	a.started(w, r, id, err)
}

func (a *api) rollback(w http.ResponseWriter, r *http.Request, c caller) {
	if a.st == nil {
		httpapi.WriteError(w, r, unavailable(), a.log)
		return
	}
	id, err := a.st.StartRollback(r.Context(), c.subject)
	a.started(w, r, id, err)
}

// started answers the enqueue of a task: 202 with its ID, or the error, which
// is a 500 whose cause is logged and withheld. A swap staged in the slot (a
// restore or a reset) is not known here: the task refuses it before it
// downloads anything (restore_pending, restore_inflight), and its run says so.
func (a *api) started(w http.ResponseWriter, r *http.Request, id string, err error) {
	if err != nil {
		httpapi.WriteError(w, r, err, a.log)
		return
	}
	a.json(r.Context(), w, http.StatusAccepted, startResponse{TaskID: id})
}

// ---- errors ----------------------------------------------------------------

func conflict(code, detail string) *httpapi.Error {
	return &httpapi.Error{Status: http.StatusConflict, Code: code, Detail: detail}
}

// mapPrepare maps updater.Updater.Prepare's refusals to their answers. The
// text of an unknown error is not echoed: WriteError logs it and answers 500.
func mapPrepare(err error) error {
	switch {
	case errors.Is(err, updater.ErrInvalidVersion):
		return httpapi.Invalid(httpapi.Field("/body/version", "invalid_version", "must be a release tag such as v1.2.3"))
	case errors.Is(err, updater.ErrDisabled):
		return conflict("updater_off", "the updater is off")
	case errors.Is(err, updater.ErrUnversioned):
		return conflict("unversioned_build", "this build has no release version, so it is never compared with a release")
	case errors.Is(err, updater.ErrLocalBuildNeedsVersion):
		return conflict("local_build_needs_version", "this is a local build: name the release to install")
	case errors.Is(err, updater.ErrNoUpdate):
		return conflict("no_update", "no newer release is known; check first, or name a release")
	case errors.Is(err, updater.ErrNotNewer):
		return conflict("not_newer", "that release is not newer than the running version")
	}
	return err
}

// ---- encoding --------------------------------------------------------------

// json writes v as application/json. It encodes before writing any header, so
// an encode failure is still a clean 500.
func (a *api) json(ctx context.Context, w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		a.logf(ctx, "updaterhttp: encode response", err)
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		a.logf(ctx, "updaterhttp: write response", err)
	}
}

func (a *api) logf(ctx context.Context, msg string, err error) {
	if a.log != nil {
		a.log.ErrorContext(ctx, msg, "err", err)
	}
}

// decodeOptional reads r's body strictly into dst. An empty body leaves dst at
// its zero value; a body needs Content-Type application/json, is bounded, takes
// no unknown member and no trailing data. The body is not echoed, nor is
// encoding/json's text, which quotes members.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			return &httpapi.Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Detail: "the body is too large"}
		}
		return malformed("the body could not be read")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); !strings.EqualFold(strings.TrimSpace(mt), "application/json") {
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
				Fields: []httpapi.FieldError{httpapi.Field("/body", "unknown_field", "only version is accepted")}}
		}
		return malformed("the body is not a JSON object")
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return malformed("trailing data after the JSON value")
	}
	return nil
}

func malformed(detail string) *httpapi.Error {
	return &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: detail}
}
