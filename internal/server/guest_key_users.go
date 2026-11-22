// file: internal/server/guest_key_users.go
// version: 0.1.0
// guid: 5f27b3d9-6c80-4e14-a92d-d0e8b41c7a36
// last-edited: 2026-11-22

package server

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// The account reads the guest phone-app key code makes (mint's revoke-race
// re-read, the admin Reissue route, the Phone app keys panel) go through this
// one seam, so they read the record that is the account's: books' own store at
// HEAD, core's user record under core identity (after M3 books' store holds no
// users, and every read of it answers "no such user").
//
// It is the three UserReader methods the code used, plus a context: under core
// identity the read is judged against the caller the ctx carries.
type guestKeyUsers interface {
	// GetUserByID is the account, or (nil, nil) when there is none.
	GetUserByID(ctx context.Context, id string) (*database.User, error)
	// GetUserByVerifiedEmail is the account that is the verified holder of
	// email, or (nil, nil) when there is none. An address with more than one
	// verified holder is an error, not a pick: the caller refuses.
	GetUserByVerifiedEmail(ctx context.Context, email string) (*database.User, error)
	// ListUsers is every account.
	ListUsers(ctx context.Context) ([]database.User, error)
}

// guestAdminReader is the actor-checked read of core's user records the guest
// phone-app key routes read OTHER accounts through: core's AdminUser and
// AdminUsers, which re-check the principal of ctx (core:users.manage and an
// interactive sign-in), so holding it grants nothing, and the records carry no
// credential material. It is deliberately not auth.Identities, which answers any
// caller: a route that reads other accounts is judged against the caller it
// serves. A request reads its OWN record through the principal it carries
// (Bridge.FromCorePrincipal), never through this. The method names are those of
// AccountAdmin, which auth.Admin embeds, so the Identity.Admin handle satisfies it
// as it is.
type guestAdminReader interface {
	AdminUser(ctx context.Context, id coreauth.UserID) (*coreauth.User, error)
	AdminUsers(ctx context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error]
}

// guestAccounts is the reader of other accounts under core identity: core's
// actor-checked Admin handle (legacyauth.Identity.Admin, module.Deps.AuthAdmin),
// whose AdminUser and AdminUsers re-check the principal of the ctx they are
// given. nil refuses those reads (fail closed) and never falls back to books'
// user store, which holds no accounts under core identity. It returns an
// untyped nil, not a nil Admin boxed in the interface, so callers' nil checks
// hold.
func (s *Server) guestAccounts() guestAdminReader {
	if s.identity == nil || s.identity.Admin == nil {
		return nil
	}
	return s.identity.Admin
}

// guestUsers is the seam for this server: core's record when it runs under
// core identity, books' store otherwise.
func (s *Server) guestUsers() guestKeyUsers {
	if s.identity != nil {
		return coreGuestUsers{admin: s.guestAccounts(), bridge: s.identity.Bridge}
	}
	return storeGuestUsers{r: s.storeForWiring()}
}

// guestUsersForList is the seam for a request that reads MANY accounts (the Phone
// app keys panel asks, per row, who holds an address verified). Under core
// identity every call of the actor-checked reader is a judged read of core's
// whole user list, so the panel would make one per row; here the list is read
// once for the request and every question is answered from it. HEAD's store
// answers each row from an index, so it is unchanged.
func (s *Server) guestUsersForList() guestKeyUsers {
	if s.identity == nil || s.guestAccounts() == nil {
		return s.guestUsers()
	}
	return coreGuestUsers{admin: &adminUsersSnapshot{inner: s.guestAccounts()}, bridge: s.identity.Bridge}
}

// adminUsersSnapshot is guestAdminReader over one read of the inner
// reader's list, for the life of one request. The first call is judged by core
// against the caller of that call's ctx; a later call reuses it, which is sound
// only because one snapshot serves one request (one caller), never the server.
type adminUsersSnapshot struct {
	inner guestAdminReader
	once  sync.Once
	list  []*coreauth.User
	err   error
}

func (a *adminUsersSnapshot) load(ctx context.Context) {
	a.once.Do(func() {
		for u, err := range a.inner.AdminUsers(ctx, coreauth.UserFilter{}) {
			if err != nil {
				a.err = err
				return
			}
			if u != nil {
				a.list = append(a.list, u)
			}
		}
	})
}

func (a *adminUsersSnapshot) AdminUser(ctx context.Context, id coreauth.UserID) (*coreauth.User, error) {
	a.load(ctx)
	if a.err != nil {
		return nil, a.err
	}
	for _, u := range a.list {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, coreauth.ErrNotFound
}

func (a *adminUsersSnapshot) AdminUsers(ctx context.Context, f coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(yield func(*coreauth.User, error) bool) {
		a.load(ctx)
		if a.err != nil {
			yield(nil, a.err)
			return
		}
		for _, u := range a.list {
			if (f.Status != "" && u.Status != f.Status) || (f.Role != "" && !slices.Contains(u.Roles, f.Role)) {
				continue
			}
			if !yield(u, nil) {
				return
			}
		}
	}
}

// storeGuestUsers is HEAD's reads, unchanged: the store ignores the context.
type storeGuestUsers struct{ r database.UserReader }

func (u storeGuestUsers) GetUserByID(_ context.Context, id string) (*database.User, error) {
	return u.r.GetUserByID(id)
}
func (u storeGuestUsers) GetUserByVerifiedEmail(_ context.Context, email string) (*database.User, error) {
	return u.r.GetUserByVerifiedEmail(email)
}
func (u storeGuestUsers) ListUsers(context.Context) ([]database.User, error) { return u.r.ListUsers() }

// Why a core read is refused. Each is an error, so every caller treats the
// answer as "could not be checked" and refuses.
var (
	errGuestNoPrincipal    = errors.New("the request carries no principal to read accounts as")
	errGuestNoAdminReader  = errors.New("this server was given no actor-checked reader of core's accounts")
	errGuestEmailAmbiguous = errors.New("more than one account holds this address verified")
)

// coreGuestUsers reads core's user record.
//
//   - The CALLER's own record is read through the principal the request already
//     carries (Bridge.FromCorePrincipal): the lookup every request makes, which
//     also refuses an account that is no longer active. This is the path the
//     guest's own claim takes; it needs no admin right and exposes no one else.
//   - Every other record is read through the actor-checked reader (admin,
//     guestAdminReader): core re-checks the ctx principal on each call
//     (core:users.manage, interactive), so a caller without the right reads
//     nothing. With no reader the read refuses, never falling back to books'
//     store.
type coreGuestUsers struct {
	admin  guestAdminReader
	bridge *legacyauth.Bridge
}

func (u coreGuestUsers) GetUserByID(ctx context.Context, id string) (*database.User, error) {
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok {
		return nil, errGuestNoPrincipal
	}
	if p.User != nil && string(p.User.ID) == id {
		return u.own(ctx)
	}
	if u.admin == nil {
		return nil, errGuestNoAdminReader
	}
	cu, err := u.admin.AdminUser(ctx, coreauth.UserID(id))
	switch {
	case errors.Is(err, coreauth.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	case cu == nil:
		return nil, nil
	}
	return legacyUserOf(cu), nil
}

// own is the caller's own record, fresh: FromCorePrincipal re-reads it from
// core on every call and refuses one that is not active, so a deactivation is
// seen here.
func (u coreGuestUsers) own(ctx context.Context) (*database.User, error) {
	fresh, err := u.bridge.FromCorePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	got, ok := auth.UserFromContext(fresh)
	if !ok || got == nil {
		return nil, errGuestNoPrincipal
	}
	return got, nil
}

func (u coreGuestUsers) GetUserByVerifiedEmail(ctx context.Context, email string) (*database.User, error) {
	want := coreauth.NormalizeEmail(email)
	if want == "" {
		return nil, nil
	}
	if u.admin == nil {
		return nil, errGuestNoAdminReader
	}
	var holder *coreauth.User
	for cu, err := range u.admin.AdminUsers(ctx, coreauth.UserFilter{}) {
		if err != nil {
			return nil, err
		}
		if cu == nil || !cu.EmailVerified || coreauth.NormalizeEmail(cu.Email) != want {
			continue
		}
		if holder != nil {
			// Core can hold two verified holders after M3 (no preflight, no
			// index): HEAD's rule is the ONE holder, so a second one refuses.
			return nil, errGuestEmailAmbiguous
		}
		holder = cu
	}
	if holder == nil {
		return nil, nil
	}
	return legacyUserOf(holder), nil
}

func (u coreGuestUsers) ListUsers(ctx context.Context) ([]database.User, error) {
	if u.admin == nil {
		return nil, errGuestNoAdminReader
	}
	var out []database.User
	for cu, err := range u.admin.AdminUsers(ctx, coreauth.UserFilter{}) {
		if err != nil {
			return nil, fmt.Errorf("listing core accounts: %w", err)
		}
		if cu != nil {
			out = append(out, *legacyUserOf(cu))
		}
	}
	return out, nil
}

// legacyUserOf is HEAD's user record of a core user: what the guest key code
// shows and checks (identity, address, status, roles). No hash, epoch or version.
func legacyUserOf(u *coreauth.User) *database.User {
	roles := make([]string, len(u.Roles))
	for i, r := range u.Roles {
		roles[i] = string(r)
	}
	return &database.User{
		ID: string(u.ID), Username: u.Username, Email: u.Email, Roles: roles,
		Status: string(u.Status), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// requestValuesCtx is a background context that answers context values from a
// request's context as well: cancellation, deadline and error are the
// background's, the values (the principal and checker core bound) are the
// request's. A reissue worker uses it so its reads of other accounts, under core
// identity, are judged against the admin who asked.
type requestValuesCtx struct {
	context.Context
	vals context.Context
}

func (c requestValuesCtx) Value(key any) any {
	if v := c.vals.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}

// withRequestValues returns bg carrying req's values. bg still decides when it
// ends; a cancelled request does not end it.
func withRequestValues(bg, req context.Context) context.Context {
	return requestValuesCtx{Context: bg, vals: req}
}
