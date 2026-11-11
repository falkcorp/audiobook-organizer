// file: core/internal/taskhttp/principal.go
// version: 1.0.0
// guid: 7fb7534f-53c4-457d-91e0-655ddbd009cf
// last-edited: 2026-11-11

package taskhttp

import (
	"context"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Principal resolves the caller of a tasks.Client method from a request
// context: the actor to stamp on its runs and the checker its permissions are
// tested with (06 COR-21). It has the type of the executor's
// Options.Principal; the kernel passes it there (F2-16). ok is false outside a
// request, where the executor acts as the request's own actor.
//
// A principal with no checker resolves to a checker that holds nothing,
// never to "no identity": the executor reads no identity as a kernel-internal
// call and skips permission checks, and a half-built principal must not turn
// into that.
func Principal(ctx context.Context) (tasks.Actor, rbac.Checker, bool) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return tasks.Actor{}, nil, false
	}
	who := p.Perms
	if who == nil {
		who = holdsNothing{}
	}
	return actorOf(p), who, true
}

// actorOf is the actor a principal's runs are stamped with. The method is the
// credential's class, never a header the client sent.
func actorOf(p *auth.Principal) tasks.Actor {
	a := tasks.Actor{AuthMethod: string(p.Method)}
	if p.Method == auth.MethodAPIKey {
		a.AuthMethod = "token" // tasks.Actor's vocabulary: session | token | system
	}
	if p.User != nil {
		a.UserID = string(p.User.ID)
	}
	return a
}

// DefaultVerifiedMethod is core's verified-method rule for approve and decide
// until F4-05 supplies the rest (05-tasks 5T.3.7, 05-rbac 4.10): an
// interactive session verifies; a delegated session, an API key, the
// auth-disabled principal and every method nobody classified do not. The
// two cells that need identity data this package cannot see (an app-level
// authenticator extension whose MethodSpec.Interactive is true, and an API
// key whose scopes name the requirement explicitly) are Deps.VerifiedMethod's.
func DefaultVerifiedMethod(p *auth.Principal, _ rbac.Requirement) bool {
	return p != nil && p.Method.Interactive()
}

// holdsNothing is the checker of a principal that carries none.
type holdsNothing struct{}

func (holdsNothing) Has(rbac.Permission) bool        { return false }
func (holdsNothing) Satisfies(rbac.Requirement) bool { return false }
func (holdsNothing) Confined() bool                  { return true }
func (holdsNothing) Concrete() []rbac.Permission     { return nil }

// permission converts a scope name.
func permission(s string) rbac.Permission { return rbac.Permission(s) }
