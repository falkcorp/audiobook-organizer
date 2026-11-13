// file: core/internal/healthhttp/routes.go
// version: 1.0.0
// guid: 6c5017dc-52e8-43c1-b15d-bf6ff14ef910
// last-edited: 2026-11-13

// Package healthhttp is the HTTP surface of core's health (05-health §5H.3.3):
// the authenticated health snapshot, system info and system status, the
// diagnostics start and download routes on httpapi's core group
// (/api/core/v1), and the two public probes, /livez and /readyz, as
// Router.Infra routes.
//
// It receives the registry, the sysinfo reader, the run lookup and the
// enqueuer as healthhttp-local seams, never as the kernel's wiring. Every
// handler repeats the permission and guard checks of its route, so a mounting
// mistake never opens one, and a collaborator that is nil answers 503 only
// after those checks.
//
// The package is dark until F8-04 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package healthhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/health/sysinfo"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
)

// Registry is the part of health.Registry the routes read.
type Registry interface {
	Snapshot() health.Snapshot
	Status() health.StatusPayload
}

// Run is a diagnostics run as the download needs it: who asked and which file
// holds the bundle ("" while the run has none).
type Run struct {
	ActorID string
	File    string
}

// ErrRunNotFound is Runs.Run's answer for an ID no run has.
var ErrRunNotFound = errors.New("healthhttp: no such diagnostics run")

// Runs looks up a core.diagnostics.export run.
type Runs interface {
	Run(ctx context.Context, id string) (Run, error)
}

// Enqueuer enqueues core.diagnostics.export for the start-time actor and
// returns the run's ID. The route never builds a bundle on the request
// goroutine.
type Enqueuer interface {
	EnqueueExport(ctx context.Context, p diagnostics.ExportParams, requestedBy string) (runID string, err error)
}

// Recorder is the audit log's synchronous write: a download is recorded before
// the first byte and a failed write fails the request (audit.Logger.Record).
type Recorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// Deps are what Register needs. Every field may be nil at registration; a
// collaborator a route needs that is nil answers 503 after the route's
// permission and guards.
type Deps struct {
	Health Registry
	// Info collects the host reading (sysinfo.Collect over the library roots).
	Info     func(ctx context.Context) (sysinfo.Info, error)
	Runs     Runs
	Enqueuer Enqueuer
	// Bundles is the os.Root on core.diagnostics.dir.
	Bundles *os.Root
	Audit   Recorder
	// Redactor redacts component Detail text; nil means log.NewRedactor(nil).
	Redactor *log.Redactor
	// App and Version identify the build in the health body.
	App, Version string
	// Log receives the cause of an internal failure, which is withheld from
	// the body. nil logs nothing.
	Log *slog.Logger
}

// The scopes the routes need (05-rbac catalog).
const (
	permSystemManage rbac.Permission = "core:system.manage"
	permSystemStatus rbac.Permission = "core:system.status"
)

// actionDownload is the audit action of a bundle download (05-rbac §7.1).
const actionDownload = "core.diagnostics.download"

// routeDownload is the pattern the audit record names.
const routeDownload = "GET /api/core/v1/diagnostics/{run}"

// maxBody bounds the start request's JSON body.
const maxBody = 4 << 10

// maxSections bounds how many sections a start request may name.
const maxSections = 64

type api struct {
	reg     Registry
	info    func(context.Context) (sysinfo.Info, error)
	runs    Runs
	enq     Enqueuer
	root    *os.Root
	audit   Recorder
	red     *log.Redactor
	app     string
	version string
	log     *slog.Logger
}

func newAPI(d Deps) *api {
	a := &api{reg: d.Health, info: d.Info, runs: d.Runs, enq: d.Enqueuer, root: d.Bundles, audit: d.Audit,
		red: d.Redactor, app: d.App, version: d.Version, log: d.Log}
	if a.red == nil {
		a.red = log.NewRedactor(nil)
	}
	return a
}

// gate is a route's permission and guards, which both the router (through
// Route.Perm and Route.Guards) and the handler (through authorise) read from
// the one table.
type gate struct {
	perm        rbac.Permission
	interactive bool
	// anon lets a principal with no user (the auth-disabled one) through, as
	// the router does for a scope that is not AuthRequired (system.status).
	anon bool
}

type routeDef struct {
	method, path, name, summary, doc string
	gate                             gate
	maxBody                          int64
	rate                             httpapi.RateClass
	raw                              httpapi.RawDoc
	handler                          http.HandlerFunc
}

// Register registers the health routes on g, which must be the router's
// CoreGroup().
func Register(g *httpapi.Group, d Deps) {
	for _, def := range newAPI(d).routeTable() {
		httpapi.HandleRaw(g, httpapi.Route{
			Method: def.method, Path: def.path, Name: def.name,
			Perm:   rbac.Need(def.gate.perm),
			Guards: httpapi.Guards{Interactive: def.gate.interactive},
			Tags:   []string{"health"}, MaxBody: def.maxBody, RateClass: def.rate,
			Summary: def.summary, Doc: def.doc,
		}, def.raw, def.handler)
	}
}

// RegisterProbes registers /livez and /readyz as exact Public infra patterns
// (05-health §5H.3.3). Router.Infra puts their first segments in the router's
// NonSPA set, so "/livez/" and "/READYZ" answer 404 and never the SPA's
// index.html.
func RegisterProbes(r *httpapi.Router, reg *health.Registry) {
	r.Infra("GET /livez", rbac.Public, health.LiveHandler(reg))
	r.Infra("GET /readyz", rbac.Public, health.ReadyHandler(reg))
}

// routeTable is the route table of 05-health §5H.3.3. Register and the tests
// both read it, so the table the router enforces is the table the tests drive.
func (a *api) routeTable() []routeDef {
	manage := gate{perm: permSystemManage}
	manageInteractive := gate{perm: permSystemManage, interactive: true}
	status := gate{perm: permSystemStatus, anon: true}
	return []routeDef{
		{method: http.MethodGet, path: "/health", name: "getHealth", gate: manage,
			summary: "Read every component's state",
			doc: "The full snapshot: each component's status, reason token, redacted detail text and last check duration. " +
				"Operator data, so it needs core:system.manage and a credential.",
			raw: httpapi.RawDoc{Success: httpapi.BodyOf[healthResponse]()}, handler: a.wrap(manage, a.health)},
		{method: http.MethodGet, path: "/system/info", name: "getSystemInfo", gate: manage,
			summary: "Read the host and process facts",
			raw:     httpapi.RawDoc{Success: httpapi.BodyOf[sysinfo.Info]()}, handler: a.wrap(manage, a.systemInfo)},
		{method: http.MethodGet, path: "/system/status", name: "getSystemStatus", gate: status,
			summary: "Read readiness",
			doc:     "The /readyz body for an authenticated caller: {ready, not_ready[]} with fixed tokens only.",
			raw:     httpapi.RawDoc{Success: httpapi.BodyOf[health.StatusPayload]()}, handler: a.wrap(status, a.systemStatus)},
		{method: http.MethodPost, path: "/diagnostics", name: "startDiagnostics", gate: manage,
			summary: "Enqueue a diagnostics bundle", maxBody: maxBody, rate: httpapi.RateExpensive,
			doc: `Body (optional) {sections?: ["<moduleID>.<name>", ...]}; no sections means every section the caller may read. ` +
				`202 {run_id}: the bundle is built as a task, never on this request, and fetched with GET /diagnostics/{run}.`,
			raw: httpapi.RawDoc{Request: httpapi.BodyOf[startRequest](), Success: httpapi.BodyOf[startResponse](), Status: http.StatusAccepted}, handler: a.wrap(manage, a.start)},
		{method: http.MethodGet, path: "/diagnostics/{run}", name: "downloadDiagnostics", gate: manageInteractive,
			summary: "Download a diagnostics bundle", rate: httpapi.RateExpensive,
			doc: "Needs core:system.manage and an interactive session, and the caller must be the run's actor or hold core:admin (404 otherwise). " +
				"Sections the caller's current permissions no longer satisfy are left out and listed. 409 bundle_not_ready while the run has no file; " +
				"410 bundle_expired once the file is pruned. Audited as core.diagnostics.download before the stream starts.",
			raw: httpapi.RawDoc{}, handler: a.wrap(manageInteractive, a.download)},
	}
}

// caller is the authorised principal a handler acts for.
type caller struct {
	p       *auth.Principal
	subject string // the user's ID, "" for the auth-disabled principal on a status route
}

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

// wrap makes the handler first repeat the route's permission and guards.
func (a *api) wrap(g gate, h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		subject, err := authorise(p, g)
		if err != nil {
			httpapi.WriteError(w, r, err, a.log)
			return
		}
		h(w, r, caller{p: p, subject: subject})
	}
}

// holds reports whether p holds perm by both of the checker's answers (a scope
// held, and a requirement for it satisfied).
func holds(p *auth.Principal, perm rbac.Permission) bool {
	return p.Perms != nil && p.Perms.Has(perm) && p.Perms.Satisfies(rbac.Need(perm))
}

// authorise is the repetition of the route's permission and guards, in the
// order stage 13 refuses them: a principal (401), the permission (403), an
// interactive credential (403).
func authorise(p *auth.Principal, g gate) (string, error) {
	subject := ""
	if p != nil && p.User != nil {
		subject = string(p.User.ID)
	}
	switch {
	case p == nil || (!g.anon && subject == ""):
		return "", &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
	case !holds(p, g.perm):
		return "", httpapi.Forbidden(g.perm)
	case g.interactive && !p.Method.MayChangeCredentials():
		return "", &httpapi.Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: "this route needs an interactive session"}
	}
	return subject, nil
}

func unavailable(what string) error {
	return httpapi.Unavailable(what+" is not available", 5*time.Second)
}

// ---- response bodies ------------------------------------------------------

// healthResponse is GET /health.
type healthResponse struct {
	App        string         `json:"app"`
	Version    string         `json:"version"`
	Ready      bool           `json:"ready"`
	Live       bool           `json:"live"`
	Components []componentDTO `json:"components"`
	Startup    []string       `json:"startup_degraded"`
}

// componentDTO is one component. Detail is pattern-redacted free text; Reason
// is the fixed token.
type componentDTO struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason,omitempty"`
	Detail     string  `json:"detail,omitempty"`
	Critical   bool    `json:"critical"`
	DurationMs float64 `json:"duration_ms"`
	At         string  `json:"at,omitempty"`
}

// startRequest is the body of POST /diagnostics.
type startRequest struct {
	Sections []string `json:"sections,omitempty"`
}

// startResponse is the 202 of POST /diagnostics.
type startResponse struct {
	RunID string `json:"run_id"`
}

// ---- reads ----------------------------------------------------------------

func (a *api) health(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.reg == nil {
		a.fail(w, r, unavailable("health"))
		return
	}
	s := a.reg.Snapshot()
	out := healthResponse{App: a.app, Version: a.version, Ready: s.Ready, Live: s.Live,
		Components: make([]componentDTO, 0, len(s.Components)), Startup: append([]string{}, s.Startup...)}
	for _, c := range s.Components {
		d := componentDTO{ID: string(c.ID), Status: c.Result.Status.String(), Reason: c.Result.Reason,
			Critical: c.Critical, DurationMs: float64(c.Duration) / float64(time.Millisecond)}
		d.Detail = a.redact(c.Result.Detail)
		if !c.Result.At.IsZero() {
			d.At = c.Result.At.UTC().Format(time.RFC3339)
		}
		out.Components = append(out.Components, d)
	}
	a.json(r.Context(), w, http.StatusOK, out)
}

// redact is the pattern layer for detail text. A redactor that panics gives the
// fixed "<redacted:error>" and never the text.
func (a *api) redact(s string) (out string) {
	if s == "" {
		return ""
	}
	defer func() {
		if recover() != nil {
			out = "<redacted:error>"
		}
	}()
	out, _ = a.red.StringKinds("detail", s)
	return out
}

func (a *api) systemInfo(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.info == nil {
		a.fail(w, r, unavailable("system info"))
		return
	}
	info, err := a.info(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	info.App = a.app
	a.json(r.Context(), w, http.StatusOK, info)
}

func (a *api) systemStatus(w http.ResponseWriter, r *http.Request, _ caller) {
	if a.reg == nil {
		a.fail(w, r, unavailable("health"))
		return
	}
	a.json(r.Context(), w, http.StatusOK, a.reg.Status())
}

// ---- diagnostics ------------------------------------------------------------

func (a *api) start(w http.ResponseWriter, r *http.Request, c caller) {
	var req startRequest
	if err := decodeOptional(w, r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(req.Sections) > maxSections {
		a.fail(w, r, httpapi.Invalid(httpapi.Field("/body/sections", "too_many", "name at most 64 sections")))
		return
	}
	for _, n := range req.Sections {
		if !diagnostics.ValidSectionName(n) {
			a.fail(w, r, httpapi.Invalid(httpapi.Field("/body/sections", "invalid", `each section is "<moduleID>.<name>"`)))
			return
		}
	}
	if a.enq == nil {
		a.fail(w, r, unavailable("diagnostics"))
		return
	}
	if c.subject == "" {
		// The run's actor must be a user: the download is limited to it.
		a.fail(w, r, &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"})
		return
	}
	id, err := a.enq.EnqueueExport(audit.WithActor(r.Context(), c.actor(r)), diagnostics.ExportParams{ActorID: c.subject, Sections: req.Sections}, c.subject)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.json(r.Context(), w, http.StatusAccepted, startResponse{RunID: id})
}

// download streams the bundle of run, re-filtered to what the caller's current
// permissions satisfy. The order matters: the route's permission and
// interactive guard (authorise), then the run (404 for an unknown run and for
// someone else's, so a run's existence is not told to a stranger), then the
// file (409, 410), then the audit record, written before the first byte.
func (a *api) download(w http.ResponseWriter, r *http.Request, c caller) {
	if a.runs == nil || a.root == nil || a.audit == nil {
		a.fail(w, r, unavailable("diagnostics"))
		return
	}
	id := r.PathValue("run")
	if !diagnostics.ValidRunID(id) {
		a.fail(w, r, errRunNotFound())
		return
	}
	run, err := a.runs.Run(r.Context(), id)
	switch {
	case errors.Is(err, ErrRunNotFound):
		a.fail(w, r, errRunNotFound())
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	if run.ActorID == "" || (run.ActorID != c.subject && !holds(c.p, rbac.CoreAdmin)) {
		a.fail(w, r, errRunNotFound())
		return
	}
	if run.File == "" {
		a.fail(w, r, &httpapi.Error{Status: http.StatusConflict, Code: "bundle_not_ready", Detail: "the run has not produced its bundle"})
		return
	}
	b, err := diagnostics.OpenBundle(a.root, run.File)
	switch {
	case errors.Is(err, diagnostics.ErrExpired):
		a.fail(w, r, &httpapi.Error{Status: http.StatusGone, Code: "bundle_expired", Detail: "the bundle is past its retention and was removed"})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	defer func() {
		// A read-only file's close failure changes nothing for the response;
		// it is logged for the operator.
		if err := b.Close(); err != nil {
			a.logf(r.Context(), "healthhttp: close bundle", err)
		}
	}()

	ctx := audit.WithActor(r.Context(), c.actor(r))
	ev := audit.Event{
		Action: actionDownload, Outcome: audit.Success, Actor: c.actor(r), Route: routeDownload,
		RequestID: httpapi.RequestID(r.Context()), Reason: "download_requested",
		Details: map[string]any{"run_id": id, "run_actor": run.ActorID},
	}
	if err := a.audit.Record(ctx, ev); err != nil {
		a.logf(ctx, "healthhttp: audit record", err)
		a.fail(w, r, unavailable("the audit log"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+id+`.zip"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	omitted, err := b.Stream(w, func(p rbac.Permission) bool { return holds(c.p, p) })
	if err != nil {
		// The status is sent: the client sees a truncated body, the operator
		// sees why.
		a.logf(ctx, "healthhttp: stream bundle", err)
		return
	}
	if len(omitted) > 0 {
		a.logf(ctx, "healthhttp: bundle sections left out at download: "+strings.Join(omitted, ","), nil)
	}
}

func errRunNotFound() *httpapi.Error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "run_not_found", Detail: "no diagnostics run has that ID"}
}

// ---- plumbing ---------------------------------------------------------------

// json writes v as application/json. It encodes before writing any header, so
// an encode failure is still a clean 500.
func (a *api) json(ctx context.Context, w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		a.logf(ctx, "healthhttp: encode response", err)
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil {
		a.logf(ctx, "healthhttp: write response", err)
	}
}

// fail writes err through httpapi: an *httpapi.Error as its status, a plain
// error as the 500 with the cause logged and withheld from the body.
func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpapi.WriteError(w, r, err, a.log)
}

func (a *api) logf(ctx context.Context, msg string, err error) {
	if a.log == nil {
		return
	}
	if err == nil {
		a.log.InfoContext(ctx, msg)
		return
	}
	a.log.ErrorContext(ctx, msg, "error", err)
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

// decodeOptional reads r's body strictly into dst. An empty body leaves dst at
// its zero value; a body needs Content-Type application/json, is bounded, takes
// no unknown member and no trailing data. Neither the body nor encoding/json's
// text, which quotes members, is echoed.
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
	mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if !strings.EqualFold(strings.TrimSpace(mt), "application/json") {
		return &httpapi.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Detail: "Content-Type must be application/json"}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &httpapi.Error{Status: http.StatusBadRequest, Code: "unknown_field",
				Detail: "the body has a member this route does not accept",
				Fields: []httpapi.FieldError{httpapi.Field("/body", "unknown_field", "only sections is accepted")}}
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
