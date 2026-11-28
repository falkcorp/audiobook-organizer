// file: core/kernel/diagnostics_wire.go
// version: 0.1.0
// guid: 407018dc-3f7d-4571-a4d9-82c321a615fd
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/healthhttp"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// The kernel's diagnostics adapters (seams.d/F8-03.md, "Follow-ups for
// F8-04" items 4 and 5): what diagnostics.Config and healthhttp.Deps take from
// the kernel's services. They are dark: nothing in boot builds them until the
// F8-04 fold mounts healthhttp and builds the Exporter. Each takes its
// service through a one-method seam, so this file adds no Kernel field and
// the fold passes the real services (auth.Service, rbac.Service, the
// executor, the RunStore, the router).

// diagnosticsUsers is the auth.Service method ViewerFor reads.
type diagnosticsUsers interface {
	User(ctx context.Context, id auth.UserID) (*auth.User, error)
}

// diagnosticsKeys is the auth.Service method ViewerFor reads a key through:
// the owner's keys, as the service lists them (never a secret).
type diagnosticsKeys interface {
	APIKeys(ctx context.Context, owner auth.UserID) iter.Seq2[*auth.APIKey, error]
}

// Each seam is asserted against the real service the F8-04 fold will pass, so
// a change to the service's method fails the build here, not at the fold. The
// test doubles satisfy the seams by shape alone and would not notice.
var (
	_ diagnosticsUsers = (*auth.Service)(nil)
	_ diagnosticsKeys  = (*auth.Service)(nil)
	_ checkerCompiler  = (*rbac.Service)(nil)
	_ exportStarter    = tasks.Client(nil)
	_ runRecords       = tasks.RunStore(nil)
	_ routeLister      = (*httpapi.Router)(nil)
)

// checkerCompiler is rbac.Service.Compile: it resolves the named roles'
// patterns and compiles them through authz.Compile.
type checkerCompiler interface {
	Compile(in rbac.CheckerInput) rbac.Checker
}

// errNoDiagnosticsViewer is ViewerFor's refusal of a run whose actor is not
// an active person: a system actor, an unknown user, or one who is disabled,
// pending or the _system attribution user. The run fails and writes nothing.
var errNoDiagnosticsViewer = errors.New("kernel: diagnostics run has no active person to read as")

// tokenMethod is tasks.Actor.AuthMethod of an API key's run (taskhttp's
// actorOf spells core's api_key method this way).
const tokenMethod = "token"

// nonKeyMethod reports whether method is one that is known never to be
// key-bound: the methods whose actor carries no CredentialID on purpose.
// "system" is the executor's own actor (a schedule, the relay, a module's
// start); the others are core's session, delegated-session and auth-disabled
// methods. Any other method name with no CredentialID is not known to be
// anything, and ViewerFor refuses it.
func nonKeyMethod(method string) bool {
	switch method {
	case string(auth.MethodSession), string(auth.MethodSessionDelegated), string(auth.MethodAuthDisabled), "system":
		return true
	}
	return false
}

// diagnosticsViewerFor is diagnostics.Config.ViewerFor (F8-03 obligation 4).
// It resolves the run's stamped actor to the person's CURRENT roles.
//
// Whether the run is key-bound is decided on the credential, never on the
// method's name: taskhttp's actorOf stamps CredentialID for every key-bound
// principal (core's api_key method and any module compat surface built over a
// key) and for no other.
//   - CredentialID present: the key is read again, whatever AuthMethod says. A
//     key that is missing, revoked, not active, expired or another user's
//     refuses the run; a live one narrows the viewer to the user's current
//     roles within the key's CURRENT scopes, compiled non-interactive, so a key
//     narrowed or revoked after the run started narrows or ends the run.
//   - No CredentialID and a known non-key method (session, session_delegated,
//     auth_disabled, system): the user's current roles, interactive only for
//     "session" (auth.Method.Interactive). There is no liveness check: a run
//     keeps going after its starter signs out.
//   - No CredentialID and "token", or any method not listed above: refused. A
//     token actor with no id is a run stamped before the id existed or an actor
//     assembled by hand, and there is no way to tell which key's scopes apply;
//     a method nobody classified may be a key-bound one this function cannot
//     recognise.
//
// So an administrator's API key never reaches a Sensitive scope
// (core:backup.restore and the rest) that only the live interactive checker
// admits. The start route's request-time snapshot (ExportParams.Perms) narrows
// what this returns, and never widens it.
//
// A nil users, keys or rbac yields a nil function, which diagnostics.New
// refuses, so boot fails closed.
func diagnosticsViewerFor(users diagnosticsUsers, keys diagnosticsKeys, rb checkerCompiler) func(context.Context, tasks.Actor) (diagnostics.Viewer, error) {
	if users == nil || keys == nil || rb == nil {
		return nil
	}
	return func(ctx context.Context, actor tasks.Actor) (diagnostics.Viewer, error) {
		if actor.UserID == "" {
			return diagnostics.Viewer{}, fmt.Errorf("%w: the actor names no user", errNoDiagnosticsViewer)
		}
		u, err := users.User(ctx, auth.UserID(actor.UserID))
		if err != nil {
			return diagnostics.Viewer{}, fmt.Errorf("%w: %w", errNoDiagnosticsViewer, err)
		}
		if u == nil || u.Status != auth.StatusActive {
			return diagnostics.Viewer{}, fmt.Errorf("%w: the user is not active", errNoDiagnosticsViewer)
		}
		in := rbac.CheckerInput{Roles: u.Roles}
		switch {
		case actor.CredentialID != "":
			scopes, err := currentKeyScopes(ctx, keys, u.ID, actor.CredentialID, time.Now())
			if err != nil {
				return diagnostics.Viewer{}, err
			}
			in.Scopes = scopes // non-interactive: a key is never an interactive credential
		case actor.AuthMethod == tokenMethod:
			return diagnostics.Viewer{}, fmt.Errorf("%w: no credential id", errNoDiagnosticsViewer)
		case nonKeyMethod(actor.AuthMethod):
			in.Interactive = auth.Method(actor.AuthMethod).Interactive()
		default:
			return diagnostics.Viewer{}, fmt.Errorf("%w: unrecognised method with no credential id", errNoDiagnosticsViewer)
		}
		c := rb.Compile(in)
		if c == nil {
			return diagnostics.Viewer{}, fmt.Errorf("%w: no checker", errNoDiagnosticsViewer)
		}
		return diagnostics.Viewer{ActorID: actor.UserID, Can: c.Has}, nil
	}
}

// currentKeyScopes reads key id as it is now and returns its scopes. It is
// auth's key rule applied to a stored key (auth.CheckCredentialFor,
// Service.Revalidate): the key must exist, be the owner's, be active and be
// unexpired, and every other state refuses. The scopes are never empty: a key
// with none is refused, because an empty list means "every role permission" to
// a checker and a key has no such meaning.
func currentKeyScopes(ctx context.Context, keys diagnosticsKeys, owner auth.UserID, id string, now time.Time) ([]rbac.Permission, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: no credential id", errNoDiagnosticsViewer)
	}
	var k *auth.APIKey
	for cand, err := range keys.APIKeys(ctx, owner) {
		if err != nil {
			return nil, fmt.Errorf("%w: credential read: %w", errNoDiagnosticsViewer, err)
		}
		if cand != nil && cand.ID == id {
			k = cand
			break
		}
	}
	switch {
	case k == nil:
		return nil, fmt.Errorf("%w: credential revoked", errNoDiagnosticsViewer)
	case k.Owner != owner:
		return nil, fmt.Errorf("%w: credential belongs to another user", errNoDiagnosticsViewer)
	case k.Status != auth.KeyActive:
		return nil, fmt.Errorf("%w: credential revoked", errNoDiagnosticsViewer)
	case !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt):
		return nil, fmt.Errorf("%w: credential expired", errNoDiagnosticsViewer)
	case len(k.Scopes) == 0:
		return nil, fmt.Errorf("%w: credential has no scope", errNoDiagnosticsViewer)
	}
	return slices.Clone(k.Scopes), nil
}

// exportStarter is tasks.Client.Start (the executor): it stamps the run's
// actor from the request principal in ctx (taskhttp.Principal, COR-21).
type exportStarter interface {
	Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error)
}

// diagnosticsEnqueuer is healthhttp.Enqueuer over the executor.
type diagnosticsEnqueuer struct{ tasks exportStarter }

var _ healthhttp.Enqueuer = diagnosticsEnqueuer{}

// errExportActor is a start whose params, requester and request principal do
// not name the same person. The executor stamps the run from the principal
// and the exporter refuses a run whose params name anyone else
// (diagnostics.ErrActorMismatch), so the mismatch is refused here, before a
// run that could only fail is queued.
var errExportActor = errors.New("kernel: diagnostics start names a different person than the request")

// EnqueueExport starts core.diagnostics.export in Live mode (a ReadOnly kind)
// for requestedBy, whose principal must be the one in ctx: the run's stamped
// actor is that principal, so it equals requestedBy.
func (e diagnosticsEnqueuer) EnqueueExport(ctx context.Context, p diagnostics.ExportParams, requestedBy string) (string, error) {
	if e.tasks == nil {
		return "", errors.New("kernel: diagnostics enqueuer has no executor")
	}
	pr, ok := auth.PrincipalFrom(ctx)
	if requestedBy == "" || p.ActorID != requestedBy || !ok || pr == nil || pr.User == nil || string(pr.User.ID) != requestedBy {
		return "", errExportActor
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("kernel: diagnostics params: %w", err)
	}
	ref, err := e.tasks.Start(ctx, diagnostics.ExportKind, tasks.StartRequest{
		Mode: tasks.ModeRequestLive, Params: raw, Trigger: tasks.TriggerManual,
	})
	if err != nil {
		return "", err
	}
	return string(ref.ID), nil
}

// runRecords is tasks.RunStore.Get. The download reads the stored record, not
// tasks.Client.Get: the client hides a run its caller may not view
// (core:tasks.view), and the route has already checked core:system.manage
// and limits the bundle to its own actor, so a downloader without
// core:tasks.view must still find their own run.
type runRecords interface {
	Get(ctx context.Context, id tasks.RunID) (tasks.RunRecord, error)
}

// diagnosticsRuns is healthhttp.Runs over the run store.
type diagnosticsRuns struct{ store runRecords }

var _ healthhttp.Runs = diagnosticsRuns{}

// Run reads one core.diagnostics.export run. An unknown ID and a run of any
// other kind are both healthhttp.ErrRunNotFound (404), so the route never
// tells a stranger what other runs exist. ActorID is the actor the executor
// stamped on the run, never the params. File is set only for a succeeded run,
// and only when its stored result names the run's own bundle file
// (diagnostics.BundleFile); a result naming any other file is an error.
func (r diagnosticsRuns) Run(ctx context.Context, id string) (healthhttp.Run, error) {
	if r.store == nil {
		return healthhttp.Run{}, errors.New("kernel: diagnostics runs has no run store")
	}
	rec, err := r.store.Get(ctx, tasks.RunID(id))
	switch {
	case errors.Is(err, tasks.ErrRunNotFound):
		return healthhttp.Run{}, healthhttp.ErrRunNotFound
	case err != nil:
		return healthhttp.Run{}, fmt.Errorf("kernel: diagnostics run %s: %w", id, err)
	case rec.Kind != diagnostics.ExportKind || rec.ID != tasks.RunID(id):
		return healthhttp.Run{}, healthhttp.ErrRunNotFound
	}
	out := healthhttp.Run{ActorID: rec.Actor.UserID}
	if rec.State != tasks.StateSucceeded {
		return out, nil
	}
	var res diagnostics.ExportResult
	if err := json.Unmarshal(rec.Result, &res); err != nil {
		return healthhttp.Run{}, fmt.Errorf("kernel: diagnostics run %s: result: %w", id, err)
	}
	if want := diagnostics.BundleFile(id); res.File != want {
		return healthhttp.Run{}, fmt.Errorf("kernel: diagnostics run %s: result names %q, not its bundle %q", id, res.File, want)
	}
	out.File = res.File
	return out, nil
}

// routeLister is httpapi.Router.Routes.
type routeLister interface {
	Routes() iter.Seq[httpapi.RouteInfo]
}

// diagnosticsRoutePatterns is diagnostics.Config.RoutePatterns: on each call
// it reads the router's routes and returns, for each, "METHOD PATTERN" (when
// the route has a method) and "PATTERN", once each, in route order. A route
// with no pattern is skipped. A nil router yields nil, which keeps no route
// value verbatim (every one is sanitized).
func diagnosticsRoutePatterns(r routeLister) func() []string {
	if r == nil {
		return nil
	}
	return func() []string {
		var out []string
		seen := map[string]bool{}
		add := func(s string) {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		for ri := range r.Routes() {
			if ri.Pattern == "" {
				continue
			}
			if ri.Method != "" {
				add(ri.Method + " " + ri.Pattern)
			}
			add(ri.Pattern)
		}
		return out
	}
}
