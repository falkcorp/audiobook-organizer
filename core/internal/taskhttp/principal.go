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
//
// CredentialID is stamped for a key-bound principal and only for one: the key's
// id when KeyRef reports a key, which covers core's api_key method and every
// compat surface built over a key whatever its method name. Every other
// principal (a session, a delegated session, an extension, a compat surface's
// principal that was built over no key, the auth-disabled one) is stamped none,
// so an actor's having a CredentialID is the one fact that says it is key-bound
// and no reader needs to know a method's name to tell. It is an identifier and
// never a secret. A key-bound principal with no key id is stamped with the empty
// id and AuthMethod "token", which the reader that needs an id refuses.
func actorOf(p *auth.Principal) tasks.Actor {
	a := tasks.Actor{AuthMethod: string(p.Method)}
	if p.Method == auth.MethodAPIKey {
		a.AuthMethod = "token" // tasks.Actor's vocabulary: session | token | system
	}
	if p.User != nil {
		a.UserID = string(p.User.ID)
	}
	if id, isKey := p.KeyRef(); isKey {
		a.CredentialID = id
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
//
// It asks Method's core-only list on purpose, not the principal's registered
// classification: the extension cell is the kernel's to widen
// (kernel/verified_method.go), and core's default refuses it
// (TestApprove_CoreDefaultRefusesWhatOnlyTheKernelWidens).
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
