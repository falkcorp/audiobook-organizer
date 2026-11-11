// file: core/auth/export_test.go
// version: 1.1.0
// guid: 893eb259-d801-49c9-9efd-55a9ab60080d
// last-edited: 2026-11-11

package auth

import (
	"net/http"
	"time"
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
