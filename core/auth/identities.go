// file: core/auth/identities.go
// version: 0.1.0
// guid: 1d490109-f639-4607-96b7-07cdb9d2b11e
// last-edited: 2026-11-21

package auth

import (
	"context"
	"iter"
)

// Identities is the read-only identity view a module receives as
// module.Deps.Auth (05-authn §3.1, 06 §6.4.4, COR-21): lookups and the
// request principal, no credential, session or user mutation. *Service
// implements it.
type Identities interface {
	User(ctx context.Context, id UserID) (*User, error)
	UserByUsername(ctx context.Context, name string) (*User, error)
	Users(ctx context.Context, f UserFilter) iter.Seq2[*User, error]
	// PrincipalFrom is the package function PrincipalFrom, as a method so a
	// module holding only this view can read the request principal.
	PrincipalFrom(ctx context.Context) (*Principal, bool)
}

var _ Identities = (*Service)(nil)

// PrincipalFrom is Identities.PrincipalFrom: the principal the middleware bound
// to ctx (WithPrincipal), the same value the package function returns.
func (s *Service) PrincipalFrom(ctx context.Context) (*Principal, bool) {
	return PrincipalFrom(ctx)
}
