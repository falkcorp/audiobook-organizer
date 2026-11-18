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
}

// Approval says a Live run needs a person's recorded decision first.
type Approval struct {
	PlanRequired bool          // Live start must name a Preview run (plan_ref) of the same kind
	MaxPlanAge   time.Duration // 0 = 7 days (the v3 ops package's approval.go:29-30)
}

// DefaultMaxPlanAge is Approval.MaxPlanAge when it is zero. Name coined by F2-01
// (v3 kept the same constant, the v3 ops package's approval.go:30).
const DefaultMaxPlanAge = 7 * 24 * time.Hour
