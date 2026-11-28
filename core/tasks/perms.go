// file: core/tasks/perms.go
// version: 1.1.0
// guid: 9cf11d04-c704-493a-9c9a-568cf39ef46a
// last-edited: 2026-11-18

package tasks

import (
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// Permissions is who may act on a definition's runs. Each field is an
// rbac.Requirement (05-rbac §3.1; zero value invalid there, so "zero" below
// means "field omitted, default applied by tasks").
type Permissions struct {
	Start   rbac.Requirement // required; own-namespace or core scopes only (05-rbac §4 R3 rule)
	Cancel  rbac.Requirement // omitted = Start. The run's own actor may always cancel it.
	Approve rbac.Requirement // required iff Approval != nil (always for Fixer)
	View    rbac.Requirement // omitted = rbac.Need("core:tasks.view")

	// StartableByViewers is a reviewed reason allowing Start to be satisfied by
	// a view-class scope (05-rbac PermissionSpec.Class). Empty = refused (SEC-9).
	StartableByViewers string

	// OwnerRouteOnly is a reviewed reason this kind is started only through its
	// owner's own route, never through the generic POST /definitions/{kind}/runs.
	// The owner's route enforces gates a definition cannot state (an interactive
	// session, a refusal answered before a run exists), and the generic route
	// would enqueue the kind without them. Empty = startable through both. The
	// definitions listing reports can_start false for such a kind.
	OwnerRouteOnly string

	// OutboxSystemStart is a reviewed reason the OWNER module's system actor may
	// start this kind from one of the owner's own outbox consumers
	// (outbox.ConsumeIntoTask) although that actor cannot hold Start. A module
	// system actor never holds core:admin or an AuthRequired scope (SEC2-15,
	// COR3-10), so a kind whose Start asks for one (core:system.manage, as the
	// settings.manage expansion does) could otherwise never be started from the
	// outbox: every message would be refused (ErrNotPermitted is not permanent)
	// and dead-letter after the retry budget.
	//
	// It is not a Start requirement and no person gains anything from it: the
	// rule is the system acting on a message that is already committed, not a
	// user acting. Only the start staged by the outbox's relay batch
	// (Executor.StartIn with Trigger Outbox) honours it, and only for the module
	// that owns the kind. Client.Start, the HTTP route, rc.Enqueue, a schedule,
	// a window and a TaskStarts entry of another module all keep checking Start
	// as before. The kernel refuses at boot (P7) a ConsumeIntoTask whose kind its
	// owner's system actor cannot start under this rule, so the failure is the
	// boot's, never a dead letter. Empty = not granted.
	OutboxSystemStart string
}

// Approval says a Live run needs a person's recorded decision first.
type Approval struct {
	PlanRequired bool          // Live start must name a Preview run (plan_ref) of the same kind
	MaxPlanAge   time.Duration // 0 = 7 days (the v3 ops package's approval.go:29-30)
}

// DefaultMaxPlanAge is Approval.MaxPlanAge when it is zero. Name coined by F2-01
// (v3 kept the same constant, the v3 ops package's approval.go:30).
const DefaultMaxPlanAge = 7 * 24 * time.Hour
