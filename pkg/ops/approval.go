// file: pkg/ops/approval.go
// version: 1.0.0
// guid: 98193377-faef-431f-876a-f2bdeefd04a8
// last-edited: 2026-10-13

package ops

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// Approval says a Live run needs a person's decision recorded first. Every
// Fixer has Approval{PlanRequired: true}; any other def that writes may opt
// in. The API that records approvals (plan_ref, the approver's verified auth
// method) is 05-PR9; this PR declares the type so definitions compile.
type Approval struct {
	// PlanRequired: the Live request must name a Preview run of the same def
	// whose result holds the rows; only listed rows are applied.
	PlanRequired bool
	// MaxPlanAge refuses a plan older than this (default 7 days).
	MaxPlanAge time.Duration
	// Approver must hold this permission AND have authenticated with a
	// verified method. The unsigned Cloudflare email header is never identity.
	Approver auth.Permission
}

// DefaultMaxPlanAge is Approval.MaxPlanAge when it is zero.
const DefaultMaxPlanAge = 7 * 24 * time.Hour
