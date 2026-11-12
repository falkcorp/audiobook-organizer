// file: core/internal/storagehttp/routes.go
// version: 1.0.1
// guid: fc881e0c-487c-4da6-a3d2-a0f548fc9d0a
// last-edited: 2026-11-11

// Package storagehttp is the HTTP surface of the factory reset (05-storage
// §5S.3.15, 05-rbac §6.2, F4-05): POST /api/core/v1/system/factory-reset,
// registered on httpapi's core group. The route only stages: nothing
// destructive runs before the restart that follows, and the staging itself
// (the preflight, the confirmation check against the fresh library count, the
// hook's plan, the intent) is the kernel's function, which this package
// receives as the storagehttp-local Stager and never as *storage.Admin or the
// swapper (COR8-2).
//
// The one storage.ResetRequest flows unchanged from the body to Stager:
// the route decodes it strictly, defaults only what the spec says it defaults
// (kind factory, delete_library_files false), and passes it as decoded.
//
// The package is dark until F4-08 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package storagehttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Stager stages a factory reset: the kernel's staging function
// (kernel.stageReset), which validates the request, runs the app's reset
// hook's plan and the preflight, compares the confirmation with the fresh
// library root and count, writes the one swap intent and requests the restart.
// requestedBy is the user ID of the principal that asked.
type Stager interface {
	StageReset(ctx context.Context, requestedBy string, req storage.ResetRequest) (storage.ResetStaged, error)
}

// Recorder is the audit log's synchronous write: a reset is a state change, so
// its record is written before the reset is staged and a failed write fails
// the request (audit.Logger.Record).
type Recorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// Deps are what Register needs. Every field may be nil at registration; a nil
// Stager or Audit answers 503 before anything is staged (fail closed), and a
// nil OwnerRequired means the owner is required.
type Deps struct {
	Stager Stager
	Audit  Recorder
	// OwnerRequired reports core.auth.owner.required (05-authn §4.8, SEC4-14):
	// true when an OwnerVerifier is configured and an owner identity is set.
	// When false the route falls back to Guards.Interactive plus core:admin, as
	// 05-authn says every owner-guarded route does, because no principal could
	// hold the owner assurance.
	OwnerRequired func() bool
	// Log receives the cause of an internal failure, which is withheld from the
	// body. nil logs nothing.
	Log *slog.Logger
}

// api is what the handler shares. Its fields are fixed at Register.
type api struct {
	stager        Stager
	audit         Recorder
	ownerRequired func() bool
	log           *slog.Logger
}

// route is the pattern the audit record names.
const route = "POST /api/core/v1/system/factory-reset"

// actionFactoryReset is the audit action 05-rbac §4.10 fixes for the reset.
const actionFactoryReset = "core.system.factory_reset"

// resetMessage is the fixed text of the 200 answer.
const resetMessage = "factory reset staged; it completes when the service restarts"

// FactoryResetResponse is the 200 body: the staged intent's ID, a fixed
// message and, only when delete_library_files was requested, the library root
// and top-level entry count the wipe will remove (the confirmed values).
type FactoryResetResponse struct {
	ResetID          string `json:"reset_id"`
	Message          string `json:"message"`
	LibraryRoot      string `json:"library_root,omitempty"`
	LibraryFileCount *int   `json:"library_file_count,omitempty"`
}

// Register registers POST /system/factory-reset on g, which must be the
// router's CoreGroup(). The route needs core:admin, Guards.Interactive and
// Guards.Owner (05-rbac §6.2, 05-authn §4.8); the handler repeats each check
// so a mounting mistake or a missing stage 13 never opens it.
//
// It is registered with httpapi.HandleRaw because library_files_confirm_required
// carries the current library_root and library_file_count as problem members,
// which httpapi.Error has no place for.
func Register(g *httpapi.Group, d Deps) {
	a := &api{stager: d.Stager, audit: d.Audit, ownerRequired: d.OwnerRequired, log: d.Log}
	httpapi.HandleRaw(g, httpapi.Route{
		Method: http.MethodPost, Path: "/system/factory-reset", Name: "factoryReset",
		Perm: rbac.Need(rbac.CoreAdmin), Guards: httpapi.Guards{Interactive: true, Owner: true},
		Tags: []string{"system"}, MaxBody: maxBody,
		Summary: "Stage a factory reset; it runs when the service restarts",
		Doc: `Body {kind: "factory" (default) or "store", confirm: "RESET", delete_library_files?: bool (default false), library_root?, library_file_count?}. ` +
			`422 confirm_required without the confirm; 422 library_files_confirm_required, carrying the current library_root and library_file_count, when delete_library_files is true and ` +
			`they do not match the preflight; 422 reset_request_invalid; 400 reset_refused; 409 swap_pending. Nothing is staged on any refusal. ` +
			`200 {reset_id, message} plus library_root and library_file_count when files will be deleted. Files deleted by a reset are not restored.`,
	}, httpapi.RawDoc{Request: httpapi.BodyOf[storage.ResetRequest](), Success: httpapi.BodyOf[FactoryResetResponse]()}, http.HandlerFunc(a.factoryReset))
}

// factoryReset is the handler: guards, then the body, then the audit record,
// then the one StageReset call.
func (a *api) factoryReset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.PrincipalFrom(ctx)
	subject, err := a.guard(p)
	if err != nil {
		fail(w, r, err, a.log)
		return
	}
	req, err := decodeRequest(w, r)
	if err == nil {
		err = req.Validate()
	}
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	actor := actorOf(p, subject, r)
	ctx = audit.WithActor(ctx, actor)
	// The record is written before the reset is staged: a staged reset the audit
	// log never saw would wipe the store at the next restart unrecorded, so a
	// failed write stops the request with nothing staged. A reset that then fails
	// to stage gets a second, failure record below.
	ev := audit.Event{
		Action: actionFactoryReset, Outcome: audit.Success, Actor: actor, Route: route,
		RequestID: httpapi.RequestID(r.Context()), Reason: "reset_requested", Details: detailsOf(req),
	}
	if err := a.audit.Record(ctx, ev); err != nil {
		a.logf(ctx, "storagehttp: audit record", err)
		fail(w, r, httpapi.Unavailable("the audit log is not available", 5*time.Second), a.log)
		return
	}
	staged, err := a.stager.StageReset(ctx, subject, req)
	if err != nil {
		a.recordFailure(ctx, ev, err)
		a.refuse(w, r, err)
		return
	}
	res := FactoryResetResponse{ResetID: staged.ID, Message: resetMessage}
	if req.DeleteLibraryFiles {
		count := staged.LibraryFileCount
		res.LibraryRoot, res.LibraryFileCount = staged.LibraryRoot, &count
	}
	writeJSON(ctx, w, http.StatusOK, "application/json", res, a.log)
}

// guard is the handler's repetition of the route's permission and guards, in
// the order stage 13 would refuse them: a principal that names a user (401),
// core:admin (403), an interactive credential (403) and, while the owner is
// required, the owner assurance (403); only then, so an unauthorised caller
// learns nothing about availability, a missing Stager or Audit (503). It returns the subject the reset is
// requested by, the user's ID.
//
// The auth-disabled principal has no user and is refused here: Guards.Interactive
// refuses it at stage 13 (05-authn §4.11), and a reset with no one to name must
// not be staged.
func (a *api) guard(p *auth.Principal) (string, error) {
	switch {
	case p == nil || p.User == nil || p.User.ID == "":
		return "", &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
	case p.Perms == nil || !p.Perms.Has(rbac.CoreAdmin):
		return "", httpapi.Forbidden(rbac.CoreAdmin)
	case !p.Method.MayChangeCredentials():
		return "", &httpapi.Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: "this route needs an interactive session"}
	case (a.ownerRequired == nil || a.ownerRequired()) && p.Assurance&auth.AssuranceOwner == 0:
		return "", &httpapi.Error{Status: http.StatusForbidden, Code: "owner_required", Detail: "this route needs the deployment owner"}
	case a.stager == nil || a.audit == nil:
		return "", httpapi.Unavailable("factory reset is not available", 5*time.Second)
	}
	return string(p.User.ID), nil
}

// refuse answers a refusal: a mapped problem, else whatever fail makes of it.
func (a *api) refuse(w http.ResponseWriter, r *http.Request, err error) {
	if pb, ok := mapResetError(err); ok {
		writeProblem(w, r, *pb, a.log)
		return
	}
	fail(w, r, err, a.log)
}

// recordFailure writes the failure record of a reset that was recorded as
// requested and then refused or failed at staging. A write that fails here is
// logged: the request already answers the staging error, and the first record
// stands.
func (a *api) recordFailure(ctx context.Context, ev audit.Event, cause error) {
	ev.Outcome, ev.Reason = audit.Failure, "staging_refused"
	// A refusal maps to a problem; anything else is a failure of staging.
	if pb, ok := mapResetError(cause); !ok || pb == nil {
		ev.Outcome, ev.Reason = audit.Error, "staging_failed"
	}
	if err := a.audit.Record(ctx, ev); err != nil {
		a.logf(ctx, "storagehttp: audit failure record", err)
	}
}

// logf logs err, which a collaborator wrote, only to the operator's log.
func (a *api) logf(ctx context.Context, msg string, err error) {
	if a.log != nil {
		a.log.ErrorContext(ctx, msg, "error", err)
	}
}

// detailsOf is the record's details: the request's kind, flag, root and
// count (05-rbac §4.10), never the confirm token.
func detailsOf(req storage.ResetRequest) map[string]any {
	d := map[string]any{"kind": string(req.Kind), "delete_library_files": req.DeleteLibraryFiles}
	if req.DeleteLibraryFiles {
		d["library_root"], d["library_file_count"] = req.LibraryRoot, req.LibraryFileCount
	}
	return d
}

// actorOf is the audit actor for p, built from the principal so the record
// names the user whatever stage 12 bound.
func actorOf(p *auth.Principal, subject string, r *http.Request) audit.Actor {
	a := audit.Actor{UserID: subject, Username: p.User.Username, Method: string(p.Method), CredentialID: p.CredentialID, UserAgent: userAgent(r)}
	if p.ClientAddr.IsValid() {
		a.ClientAddr = p.ClientAddr.String()
	}
	return a
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
