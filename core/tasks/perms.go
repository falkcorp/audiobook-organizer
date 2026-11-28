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
	Start rbac.Requirement // required; own-namespace or core scopes only (05-rbac §4 R3 rule)

	// Manage is the standing right over this kind's existing runs, required of
	// every kind (owner ruling 2026-11-22: HEAD's v1 operations control routes
	// are gated behind settings.manage, and a kind states its own equivalent;
	// v3's cancel admits the starter or scan.trigger plus the definition's
	// permissions, and that arm narrows to this rule at the cutover). A
	// caller must satisfy Manage AND the operation's own requirement to cancel
	// or discard a run (Cancel, omitted = Start) or to decide or approve one
	// (Approve, for decide omitted = Start). Neither the run's own actor nor a
	// holder of core:tasks.manage alone passes without it. It must be a scoped
	// requirement (rbac.AllOf, rbac.AnyOf or rbac.Need) of own-namespace or core
	// scopes: Public and AnyAuthenticated would satisfy anyone, so a kind that
	// names one is refused at registration, as a kind that names none is. It
	// gates no start.
	Manage rbac.Requirement

	Cancel  rbac.Requirement // omitted = Start; ANDed with Manage (no run's-own-actor arm, owner ruling 2026-11-22)
	Approve rbac.Requirement // required iff Approval != nil (always for Fixer); ANDed with Manage
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
