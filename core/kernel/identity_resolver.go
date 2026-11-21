// file: core/kernel/identity_resolver.go
// version: 0.1.0
// guid: 8e2b6f04-91c3-4a75-b0d8-3f6c1a7e5d92
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/auth/oidc"
)

// bindAccountResolver builds the OIDC account resolver for an app that handed
// the kernel a late handle (Config.AccountResolver) and binds it. P5 calls it
// after the identity services exist; no handle builds nothing.
//
// Accounts is the *auth.Service itself, not a copy or a second lock: a sign-in
// that creates an account takes the same e-mail lock every other creator
// (CreateUser, the bootstrap redeem, an invite accept) holds, and reports the
// new user to the service that decides whether first-user setup is still open.
// Roles is the rbac service the auth service was built over, so a first
// sign-in claims its role in the catalog stage 13 reads. Audit, the store and
// the batch are the identity boot's own, so a resolution's writes commit with
// its audit record.
//
// The resolver is not wired into the identity routes: only the app's extension
// reaches it, through the handle, and the handle refuses (ErrResolverUnbound)
// until this runs and again after closeCore unbinds it.
func (k *Kernel) bindAccountResolver(ctx context.Context) error {
	h := k.cfg.AccountResolver
	id := k.ident
	if h == nil {
		return nil
	}
	if id == nil || id.svc.Auth == nil || id.svc.RBAC == nil || id.users == nil || k.audit == nil {
		return fmt.Errorf("kernel: identity: the account resolver needs the identity stores and services")
	}
	r, err := oidc.NewResolver(oidc.ResolverDeps{
		Store:    id.users,
		NewBatch: id.users.NewBatch,
		Audit:    k.audit,
		Roles:    id.svc.RBAC,
		Accounts: id.svc.Auth,
		Log:      k.coreLog("auth"),
		Clock:    bootClock(ctx),
	})
	if err != nil {
		return fmt.Errorf("kernel: identity: build the account resolver: %w", err)
	}
	if err := h.Bind(r); err != nil {
		return fmt.Errorf("kernel: identity: bind the account resolver: %w", err)
	}
	id.resolver = r
	return nil
}

// unbindAccountResolver returns the app's handle to refusing. closeCore calls
// it first, so a sign-in that starts once shutdown has begun is refused rather
// than resolved against stores that are closing. A no-op when none was bound.
func (k *Kernel) unbindAccountResolver() {
	if k.ident == nil || k.ident.resolver == nil || k.cfg.AccountResolver == nil {
		return
	}
	k.cfg.AccountResolver.Unbind(k.ident.resolver)
}
