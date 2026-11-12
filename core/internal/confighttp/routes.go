// file: core/internal/confighttp/routes.go
// version: 1.0.1
// guid: a168e9f4-eec0-4f65-bc30-ad205a114b82
// last-edited: 2026-11-11

// Package confighttp is the HTTP surface of core's settings (05-config
// §1.3.4, F4-05): GET /api/core/v1/settings/schema, GET
// /api/core/v1/settings and PATCH /api/core/v1/settings, registered on
// httpapi's core group. Each route is a thin adapter over *config.Store,
// which owns every rule about a key: its section permission, its protection
// level, the operator lock, validation and the audit record of a commit. This
// package fills config.Actor from the request's principal, filters the reads
// by the sections the caller may read, and maps Apply's refusals to the
// statuses the spec fixes. It adds no reach of its own.
//
// Secrets never leave: a secret key shows only whether a value is set, where
// it comes from and who wrote it last (never a value, a mask, a hash or a
// fingerprint), and no error body, log line or audit detail of this package
// carries a submitted value.
//
// The package is dark until F4-08 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package confighttp

import (
	"context"
	"encoding/json"
	"iter"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Store is what the routes call on *config.Store, which satisfies it.
type Store interface {
	// Apply validates and commits a patch (config.Store.Apply).
	Apply(ctx context.Context, p config.Patch, a config.Actor) (config.Result, error)
	// Schema renders the settings schema over the sections include accepts.
	Schema(include func(section config.Path) bool) (json.RawMessage, error)
	// Keys iterates a snapshot of every key's state.
	Keys() iter.Seq2[config.Path, config.KeyState]
	// Lookup reads one key's state.
	Lookup(p config.Path) (config.KeyState, bool)
}

// SectionReadPerm is the permission that lets a caller read a section
// (05-config §1.3.4: "per section read"), false for a section it does not
// know. The kernel supplies it from the sections' registrations, including
// any config.Permissions(read, write) a section declares.
//
// *config.Store exposes no such accessor (the section's permissions are
// unexported), so the route cannot derive the filter itself: a copy of
// config's default rule would be wrong for exactly the sections that
// override it, and this filter decides what a caller may see.
type SectionReadPerm func(section config.Path) (rbac.Permission, bool)

// Deps are what Register needs. Every field may be nil at registration; a nil
// Store answers 503 and a nil ReadPerm makes every section unreadable (fail
// closed), so a mounting mistake never widens a read.
type Deps struct {
	Store    Store
	ReadPerm SectionReadPerm
	// Log receives the cause of an internal failure, which is withheld from
	// the body. nil logs nothing.
	Log *slog.Logger
}

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	store Store
	read  SectionReadPerm
	log   *slog.Logger
}

// Register registers the three settings routes on g, which must be the
// router's CoreGroup().
//
// Every route declares rbac.AnyAuthenticated: the permission is per section
// (read for the GETs, write for PATCH) and per key (its protection), which
// only the store can judge, and 05-rbac §4.4's "core routes that serve
// module resources add per-resource dynamic checks" is this case.
//
// PATCH carries no Guards.Interactive, on purpose. Protection is per key
// (config.Actor.Interactive is checked for a key that declares it), and 05-authn §4.11
// says a Guards.Interactive route is refused with authentication off while
// settings keep their auth-off reach (05-config §1.3.7: the auth-disabled
// principal changes any key below ProtectAdmin and none at or above it). A
// route-wide guard would close the first and add nothing to the second.
//
// PATCH is registered with httpapi.HandleRaw, because the spec's 409 and 403
// carry members httpapi.Error has no place for (locked_by, need); it decodes
// as httpapi's Handle would (application/json, a bounded strict body).
func Register(g *httpapi.Group, d Deps) {
	a := &api{store: d.Store, read: d.ReadPerm, log: d.Log}
	tags := []string{"settings"}
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/settings/schema", Name: "getSettingsSchema", Perm: rbac.AnyAuthenticated, Tags: tags,
		Summary: "The settings schema (JSON Schema 2020-12), one property per section the caller may read",
		Doc:     "Vendor keywords: x-mediacore-reload, x-mediacore-protect, x-mediacore-secret, x-mediacore-fail-closed; secret keys carry writeOnly.",
	}, a.getSchema)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/settings", Name: "getSettings", Perm: rbac.AnyAuthenticated, Tags: tags,
		Summary: "Every key of the sections the caller may read, as {path: key state}",
		Doc:     "A secret key shows only whether a value is set, its source and who wrote it; never a value, a mask or a hash.",
	}, a.getSettings)
	httpapi.HandleRaw(g, httpapi.Route{
		Method: http.MethodPatch, Path: "/settings", Name: "updateSettings", Perm: rbac.AnyAuthenticated, Tags: tags,
		MaxBody: maxBody,
		Summary: "Store and unset settings in one commit",
		Doc: "Body {set: {path: value}, unset: [path]}. 400 unknown_key; 403 permission_denied (need: key to the section's write permission) or protection_required (need: key to the protection level); " +
			"409 locked (locked_by: key to the operator layer); 422 validation_failed (one error per key); 500 on a storage failure. " +
			"200 carries restart_pending and post_commit_error when present. A secret key's value is a JSON string and is never returned.",
	}, httpapi.RawDoc{Request: httpapi.BodyOf[SettingsRequest](), Success: httpapi.BodyOf[SettingsResponse]()}, http.HandlerFunc(a.patch))
}

// noReq is the request of a route with no input.
type noReq struct{}

// guard is every handler's first line. It is not a permission check (each
// section's read permission is checked per section below, the write
// permission and protection by Apply): it makes a handler fail closed when it
// was mounted without what it needs, a principal (401) or a store (503).
func (a *api) guard(p *auth.Principal) error {
	switch {
	case p == nil:
		return unauthenticated()
	case p.Perms == nil || !p.Perms.Satisfies(rbac.AnyAuthenticated):
		// AnyAuthenticated, as stage 13 will apply it (F4-08): a principal
		// with no checker, or a Confined key (05-rbac 4.10), never satisfies it.
		return &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "this route needs a signed-in caller"}
	case a.store == nil:
		return httpapi.Unavailable("settings are not available", 5*time.Second)
	}
	return nil
}

// canRead is the filter of a section: its read permission, held by p. A
// section with no known read permission, and a principal with no checker, read
// nothing.
func (a *api) canRead(p *auth.Principal) func(section config.Path) bool {
	return func(section config.Path) bool {
		if a.read == nil || p.Perms == nil {
			return false
		}
		perm, ok := a.read(section)
		return ok && p.Perms.Has(perm)
	}
}

func (a *api) getSchema(_ context.Context, in *httpapi.In[noReq]) (json.RawMessage, error) {
	if err := a.guard(in.Principal); err != nil {
		return nil, err
	}
	return a.store.Schema(a.canRead(in.Principal))
}

func (a *api) getSettings(_ context.Context, in *httpapi.In[noReq]) (SettingsView, error) {
	if err := a.guard(in.Principal); err != nil {
		return nil, err
	}
	readable := a.canRead(in.Principal)
	// Whether a section is readable is asked once per section, not once per
	// key.
	seen := map[config.Path]bool{}
	out := SettingsView{}
	for p, ks := range a.store.Keys() {
		ok, asked := seen[ks.Section]
		if !asked {
			ok = readable(ks.Section)
			seen[ks.Section] = ok
		}
		if ok {
			out[string(p)] = keyView(ks)
		}
	}
	return out, nil
}

// patch is PATCH /settings: one Store.Apply for the whole body.
func (a *api) patch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		p = nil
	}
	if err := a.guard(p); err != nil {
		fail(w, r, err, a.log)
		return
	}
	subject, named := subjectOf(p)
	if !named {
		fail(w, r, unauthenticated(), a.log)
		return
	}
	patch, derr := decodeRequest(w, r)
	if derr != nil {
		fail(w, r, derr, a.log)
		return
	}
	res, err := a.store.Apply(bindActor(ctx, p, subject, r), patch, actorOf(p, subject))
	if err != nil {
		if pb, mapped := mapApplyError(err, a.store, patch); mapped {
			writeProblem(w, r, *pb, a.log)
			return
		}
		fail(w, r, err, a.log)
		return
	}
	writeJSON(ctx, w, http.StatusOK, "application/json", response(res), a.log)
}

// actorOf is config.Actor for p (05-config §1.3.3; 05-http-api's "filled from
// the request's auth context"): Interactive is the method's
// MayChangeCredentials, Owner is the owner assurance an OwnerVerifier proved,
// and Can asks the principal's checker. A principal with no checker holds
// nothing.
func actorOf(p *auth.Principal, subject string) config.Actor {
	return config.Actor{
		Subject:     subject,
		Interactive: p.Method.MayChangeCredentials(),
		Owner:       p.Assurance&auth.AssuranceOwner != 0,
		Can: func(perm string) bool {
			return p.Perms != nil && p.Perms.Has(rbac.Permission(perm))
		},
	}
}

// bindActor binds the audit actor config's committer will name in the
// core.settings.update record. The committer refuses a commit whose
// audit.ActorFrom(ctx).UserID differs from Actor.Subject, so a context that
// already carries the request's actor for this subject keeps it (it holds the
// user agent stage 12 saw), and any other context gets one built from the
// principal: the auth-off principal has no user, and its subject
// (authDisabledSubject) is the one thing the two must agree on.
func bindActor(ctx context.Context, p *auth.Principal, subject string, r *http.Request) context.Context {
	if a, ok := audit.ActorFrom(ctx); ok && a.UserID == subject {
		return ctx
	}
	a := audit.Actor{UserID: subject, Method: string(p.Method), CredentialID: p.CredentialID, UserAgent: userAgent(r)}
	if p.User != nil {
		a.Username = p.User.Username
	}
	if p.ClientAddr.IsValid() {
		a.ClientAddr = p.ClientAddr.String()
	}
	return audit.WithActor(ctx, a)
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

// response is the 200 body of a PATCH. Hook errors are reduced to their
// section: their text is code outside this package.
func response(res config.Result) SettingsResponse {
	out := SettingsResponse{Generation: res.Generation, Applied: paths(res.Applied), RestartPending: paths(res.RestartPending)}
	if len(res.RestartPending) == 0 {
		out.RestartPending = nil
	}
	if res.PostCommit != nil {
		for _, pe := range collect[*config.PostCommitError](res.PostCommit) {
			out.PostCommitError = append(out.PostCommitError, PostCommitFailure{Section: string(pe.Section), Message: postCommitMessage})
		}
		if len(out.PostCommitError) == 0 {
			out.PostCommitError = []PostCommitFailure{{Message: postCommitMessage}}
		}
	}
	return out
}

// postCommitMessage is the fixed text of a post-commit failure.
const postCommitMessage = "the change is committed, but an after-commit hook of this section failed"

// paths is ps as strings, never nil.
func paths(ps []config.Path) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, string(p))
	}
	return out
}
