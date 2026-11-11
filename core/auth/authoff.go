// file: core/auth/authoff.go
// version: 1.0.0
// guid: 5c18e7a4-92b3-4d60-8f1a-b7d3e04c6a29
// last-edited: 2026-11-11

package auth

import (
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
)

// Auth-off mode (05-authn §4.11).

// AuthMode is core.auth.mode.
type AuthMode string

// The two modes. The zero value of Settings.Mode is ModeRequired.
const (
	ModeRequired AuthMode = "required"
	ModeDisabled AuthMode = "disabled"
)

// ErrAuthDisabledRemote refuses to start auth-off mode on an install a remote
// caller can reach (§4.11, SEC-30), unless core.auth.disabled_allow_remote.
var ErrAuthDisabledRemote = errors.New("auth: core.auth.mode=disabled with a non-loopback listener or a trusted proxy requires core.auth.disabled_allow_remote=true")

// Reach is what the kernel knows about who can connect (Deps.Reach).
type Reach struct {
	// NonLoopbackListener: some listener is bound to an address other than
	// loopback.
	NonLoopbackListener bool
	// TrustedProxy: a trusted proxy or tunnel connector is configured; a
	// local proxy makes remote callers look local.
	TrustedProxy bool
}

// Posture is the auth-off state health reports (§4.11): Disabled is whether
// stage 12 binds the synthetic principal, DisabledRemote whether it does so
// while remote-reachable (health token auth_disabled_remote, authenticated
// endpoints only).
type Posture struct {
	Disabled       bool
	DisabledRemote bool
}

// Posture returns the mode the service was built in. It is fixed at New:
// core.auth.mode is not reloadable, so a live flip to "disabled" can never
// skip the start-up refusal.
func (s *Service) Posture() Posture {
	return Posture{Disabled: s.authOff, DisabledRemote: s.authOff && s.offRemote}
}

// offGate is the auth-off principal's checker, built once from the frozen
// catalog.
type offGate struct {
	mu    sync.Mutex
	perms rbac.Checker
}

// checkMode validates Settings.Mode and, for "disabled", applies the
// start-up reachability rule (§4.11). It sets nothing on a refusal.
func (s *Service) checkMode(st Settings) error {
	switch st.Mode {
	case "", ModeRequired:
		return nil
	case ModeDisabled:
	default:
		return fmt.Errorf("%w: core.auth.mode %q is neither %q nor %q", ErrBadInput, st.Mode, ModeRequired, ModeDisabled)
	}
	allow := s.opts.DefaultDisabledAllowRemote
	if st.DisabledAllowRemote != nil {
		allow = *st.DisabledAllowRemote
	}
	// Unknown reach is treated as remote: an app that cannot say who can
	// connect does not get auth off for free.
	remote := true
	if s.d.Reach != nil {
		r := s.d.Reach()
		remote = r.NonLoopbackListener || r.TrustedProxy
	}
	if remote && !allow {
		return ErrAuthDisabledRemote
	}
	s.authOff, s.offRemote = true, remote
	return nil
}

// authDisabledPrincipal is §4.11's auth-off binding: with core.auth.mode
// disabled, a synthetic principal {Method: auth_disabled, Perms:
// authz.AuthDisabled(catalog)} with no user, no admin flag and no scope that
// is AuthRequired or Sensitive; presented credentials are not looked at
// (§4.2 step 0). In required mode it binds nothing.
func (s *Service) authDisabledPrincipal(ClientInfo) (*Principal, bool) {
	if !s.authOff {
		return nil, false
	}
	return &Principal{Method: MethodAuthDisabled, Perms: s.offPerms(), Assurance: AssuranceNone}, true
}

// offPerms is the auth-off checker over the frozen catalog. Before the
// catalog freezes it is a checker that holds nothing, and is not cached.
func (s *Service) offPerms() rbac.Checker {
	s.off.mu.Lock()
	defer s.off.mu.Unlock()
	if s.off.perms != nil {
		return s.off.perms
	}
	cat := s.d.RBAC.Catalog()
	c := authz.AuthDisabled(cat)
	if cat != nil {
		s.off.perms = c
	}
	return c
}
