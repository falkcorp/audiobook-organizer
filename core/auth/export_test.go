// file: core/auth/export_test.go
// version: 1.1.0
// guid: 893eb259-d801-49c9-9efd-55a9ab60080d
// last-edited: 2026-11-11

package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// Test-only views of a Service's sign-in protections for the external test
// package (compiled into this package's tests only).

// SetLoginSleep replaces the sign-in throttle's delay function.
func (s *Service) SetLoginSleep(fn func(time.Duration)) { s.throttle.SetSleep(fn) }

// LoginThrottleOf is the service's sign-in throttle.
func LoginThrottleOf(s *Service) *Throttle { return s.throttle }

// LoginCeilingOf is the service's login ceilings.
func LoginCeilingOf(s *Service) *LoginCeiling { return s.ceiling }

// SourceKeyOf, SaturatedKeyOf and ConnectorKeyOf are c's sign-in keys.
func SourceKeyOf(c ClientInfo) string    { return c.sourceKey() }
func SaturatedKeyOf(c ClientInfo) string { return c.saturatedKey() }
func ConnectorKeyOf(c ClientInfo) string { return c.connectorKey() }

// OwnerSatisfied is §4.8's owner rule for p (Service.ownerSatisfied).
func (s *Service) OwnerSatisfied(p *Principal) bool { return s.ownerSatisfied(p) }

// ClientOf is the ClientInfo the service builds for r when no resolver is
// wired (Service.client).
func (s *Service) ClientOf(r *http.Request) ClientInfo { return s.client(r) }

// NewMergePlanForTest is a UserMergePlan a test assembles by hand, for the
// refusals a real PlanUserMerge would stop before it could return a plan (the
// plan is opaque to every other package).
func NewMergePlanForTest(source, target UserID, srcVersion, tgtVersion int64, rows ...MergeRow) UserMergePlan {
	return UserMergePlan{source: source, target: target, srcVersion: srcVersion, tgtVersion: tgtVersion, rows: rows}
}

// DisplaceUnverifiedHolders is Service.displaceUnverifiedHolders: no accept
// binds an address yet (inviteBindsEmail), so the helper B10t's binding calls
// is exercised directly over a batch the test commits.
func (s *Service) DisplaceUnverifiedHolders(ctx context.Context, b *storage.Batch, email string, newUser UserID) ([]UserID, error) {
	return s.displaceUnverifiedHolders(ctx, b, email, newUser)
}

// NewUnmergePlanForTest is a UserUnmergePlan a test assembles by hand over the
// users' versions, for the refusals a real PlanUserUnmerge would stop before it
// could return a plan.
func NewUnmergePlanForTest(rec UnmergeRecord, srcVersion, tgtVersion int64, rows ...MergeRow) UserUnmergePlan {
	return UserUnmergePlan{rec: rec, srcVersion: srcVersion, tgtVersion: tgtVersion, rows: rows}
}
