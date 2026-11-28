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

	// SystemStart is a reviewed reason the OWNER module's system actor may start
	// this kind, past Start, when the executor itself began the start on the
	// system's behalf. A module system actor never holds core:admin or an
	// AuthRequired scope (SEC2-15, COR3-10), so a kind whose Start asks for one
	// (core:system.manage, as the settings.manage expansion does) could otherwise
	// never be started by the scheduler, by its owner's outbox consumer or by a
	// run of the system's own: the start would be refused ErrNotPermitted, a
	// scheduled fire would never run and an outbox message would dead-letter.
	//
	// It is not a Start requirement and no person gains anything from it: a person
	// or an API key that starts the kind is judged on Start alone, flag or not.
	// The exemption applies to a start only when ALL of these hold:
	//
	//   - the kind carries this reason;
	//   - the actor is the system actor of the module that owns the kind;
	//   - the start's origin is one the executor established itself, which no
	//     StartRequest field, option, header or context value can claim: the
	//     scheduler's tick (a kind that declares a Schedule), the relay's dispatch
	//     of one of the owner's outbox consumers, or an rc.Enqueue made by a
	//     running run of the SAME module that was itself started from one of those
	//     origins (a child of a person's run keeps the person's authority).
	//
	// The kernel checks at boot (P7) that the flag and these origins agree: a kind
	// that has one of them and no flag is refused, and so is a flag with none of
	// them. Empty = not granted.
	SystemStart string
}

// Approval says a Live run needs a person's recorded decision first.
type Approval struct {
	PlanRequired bool          // Live start must name a Preview run (plan_ref) of the same kind
	MaxPlanAge   time.Duration // 0 = 7 days (the v3 ops package's approval.go:29-30)
}

// DefaultMaxPlanAge is Approval.MaxPlanAge when it is zero. Name coined by F2-01
// (v3 kept the same constant, the v3 ops package's approval.go:30).
const DefaultMaxPlanAge = 7 * 24 * time.Hour
