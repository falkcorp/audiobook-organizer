// file: core/kernel/identity_seam.go
// version: 0.1.0
// guid: 3f8a1c5e-7d24-4b96-a0e3-5c9b72d41f08
// last-edited: 2026-11-21

package kernel

import (
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/rbac"
)

// IdentityServices are the two services the identity boot built, for
// coretest's harness (06 §6.4.8): it seeds a user and a role through their own
// APIs and mints a session for the user, so a test's request reaches stage 13
// as that principal. ok is false for a kernel whose app did not declare the
// identity boot (Config.IdentityBoot), which has neither.
//
// Like TaskClient it is a seam for the harness and nothing else: the kernel is
// reachable only through bootseam.Seams.Built, which a production boot never
// sets, and no module is handed these (a module's Deps.Auth and Deps.AuthAdmin
// are the narrowed views, identitiesOnly and adminOnly). It adds no route and
// grants no principal anything: the harness still needs a role and a user the
// services accept, under the same rules a caller of the services meets.
func (k *Kernel) IdentityServices() (a *auth.Service, r *rbac.Service, ok bool) {
	if k.ident == nil || k.ident.svc.Auth == nil || k.ident.svc.RBAC == nil {
		return nil, nil, false
	}
	return k.ident.svc.Auth, k.ident.svc.RBAC, true
}

// BackupHealth is the registry's state of core.backup (its restore and journal
// tokens), for the crash-matrix cell that compares what two restore paths leave
// behind (F4-08 B10j): the registry is the one place those tokens are computed,
// and a posture token never reaches an unauthenticated surface, so there is no
// HTTP call a test can make without minting an administrator. Like
// IdentityServices it is reachable only through bootseam.Seams.Built, and ok is
// false for a kernel with no identity boot or no core.backup component.
func (k *Kernel) BackupHealth() (health.ComponentState, bool) {
	if k.ident == nil || k.healthReg == nil {
		return health.ComponentState{}, false
	}
	for _, cs := range k.healthReg.Snapshot().Components {
		if cs.ID == componentBackup {
			return cs, true
		}
	}
	return health.ComponentState{}, false
}
