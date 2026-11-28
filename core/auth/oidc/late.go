// file: core/auth/oidc/late.go
// version: 0.1.0
// guid: c41a8e07-3b95-4d62-a7f1-0e5d9b2c6a38
// last-edited: 2026-11-21

package oidc

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/falkcorp/mediacore/auth"
)

// ErrResolverUnbound is what LateResolver.Resolve answers before the kernel has
// bound a Resolver (the identity boot's P5 has not run, or the kernel is closing
// or never declared the identity boot). It is a refusal: no claim is admitted,
// linked or created while it is returned, and a caller that cannot name it must
// still treat any error as no sign-in. Match it with errors.Is.
var ErrResolverUnbound = errors.New("oidc: the account resolver is not bound")

// ErrResolverBound is Bind's refusal of a second resolver over one still bound.
var ErrResolverBound = errors.New("oidc: the account resolver is already bound")

// LateResolver is the account resolver an app hands an identity extension before
// the kernel can build the real one. An extension (ABO's cfaccess) is built
// before app.New runs, but the Resolver needs the identity stores and the auth
// service P5 builds, so the app keeps this handle, gives it to the extension and
// to app.Options.AccountResolver, and the kernel binds the Resolver it builds.
// It has the Resolve signature of *Resolver, so it stands wherever one does.
//
// Fail closed: until Bind, and again after Unbind, Resolve returns
// ErrResolverUnbound and nothing else. It never falls back to admitting.
// Safe for concurrent use; a sign-in in flight when Unbind runs finishes on the
// Resolver it started with.
type LateResolver struct {
	cur atomic.Pointer[Resolver]
}

// NewLateResolver returns an unbound handle.
func NewLateResolver() *LateResolver { return &LateResolver{} }

// Resolve is Resolver.Resolve on the bound Resolver, or ErrResolverUnbound.
func (l *LateResolver) Resolve(ctx context.Context, pol Policy, c Claims) (*auth.User, error) {
	r := l.cur.Load()
	if r == nil {
		return nil, ErrResolverUnbound
	}
	return r.Resolve(ctx, pol, c)
}

// Bind makes r the resolver Resolve answers with. It refuses nil (which would
// read as bound) and a second Bind over a bound one (ErrResolverBound): two
// owners of one handle is a boot bug, not a replacement.
func (l *LateResolver) Bind(r *Resolver) error {
	if r == nil {
		return errors.New("oidc: cannot bind a nil resolver")
	}
	if !l.cur.CompareAndSwap(nil, r) {
		return fmt.Errorf("%w: unbind it first", ErrResolverBound)
	}
	return nil
}

// Unbind returns the handle to its unbound, refusing state, when r is the
// resolver bound. It is the owner's call (the kernel's close): a Resolver that
// is not the bound one leaves the binding alone, so a stale owner cannot unbind
// the next boot's.
func (l *LateResolver) Unbind(r *Resolver) { l.cur.CompareAndSwap(r, nil) }
