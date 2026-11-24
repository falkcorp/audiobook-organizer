// file: core/kernel/health_auth.go
// version: 0.1.0
// guid: 4f1b8d27-3a96-4c5e-8b02-d7e6a91c3f58
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health"
)

// componentAuth is core.auth, the component of 05-health section 5H.3.1 that
// carries auth's one posture token. It is registered only by the identity boot
// (identityBootDeclared), once auth.Service exists.
const componentAuth health.ComponentID = "core.auth"

// reasonAuthDisabledRemote is auth_disabled_remote (05-authn section 4.11):
// core.auth.mode is disabled and the install is remote-reachable (a non-loopback
// listener or a trusted proxy, with core.auth.disabled_allow_remote true), so
// every caller who can connect acts as the auth-disabled principal. A posture
// token: it reaches the authenticated degraded field and never /health or
// /readyz (health.PublicTokens generalises it).
const reasonAuthDisabledRemote = "auth_disabled_remote"

// detailAuthDisabledRemote is the token's fixed Detail. It names no listener,
// proxy or address: Detail is operator text and reachability is the very fact
// being reported, not how to reach it.
const detailAuthDisabledRemote = "core.auth.mode is disabled and the install is remote-reachable: anyone who can connect acts as the auth-disabled principal"

// authProbe is core.auth's probe: Degraded with auth_disabled_remote while auth
// runs disabled and remote-reachable, Up otherwise (auth required, or disabled
// on a loopback-only install, which is not a posture to report). auth's Posture
// is fixed when the service is built (core.auth.mode is not reloadable), so the
// probe reads nothing live and a scheduled probe cannot make it stale.
func authProbe(posture func() auth.Posture) func(context.Context) health.Result {
	return func(context.Context) health.Result {
		if posture().DisabledRemote {
			return health.Result{Status: health.StatusDegraded, Reason: reasonAuthDisabledRemote, Detail: detailAuthDisabledRemote}
		}
		return health.Result{Status: health.StatusUp}
	}
}

// registerAuthHealth registers core.auth on the App's registry with authProbe
// over the service P5 built. It is neither Critical nor Readiness: a posture
// token never takes the process out of rotation.
func (k *Kernel) registerAuthHealth() error {
	if k.healthReg == nil {
		return errors.New("kernel: identity: no health registry is built")
	}
	if k.ident == nil || k.ident.svc.Auth == nil {
		return errors.New("kernel: identity: core.auth health needs the auth service")
	}
	if _, err := k.healthReg.Register(health.Check{
		ID: componentAuth, Title: "Authentication", Probe: authProbe(k.ident.svc.Auth.Posture),
	}); err != nil {
		return fmt.Errorf("kernel: identity: health: %w", err)
	}
	return nil
}
