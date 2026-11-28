// file: internal/server/legacyauth/legacyauth_test.go
// version: 0.1.0
// guid: 7b1d5e38-9c62-4a04-8e7f-3d2a6c9b1f05
// last-edited: 2026-11-20

package legacyauth

import (
	"context"
	"errors"
	"slices"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestNew_RefusesAMissingTable(t *testing.T) {
	if _, err := New(nil, lookup(bruce()), nil); err == nil {
		t.Error("New without an atom table")
	}
	if _, err := New(atomTable(), nil, nil); err == nil {
		t.Error("New without a user lookup")
	}
}

// legacyValues reports what FromCorePrincipal filled into ctx.
func legacyValues(ctx context.Context) (u *database.User, perms []auth.Permission, m auth.Method) {
	u, _ = auth.UserFromContext(ctx)
	for p := range auth.PermissionsFromContext(ctx) {
		perms = append(perms, p)
	}
	slices.Sort(perms)
	return u, perms, auth.MethodFromContext(ctx)
}

// TestFromCorePrincipal_Refusals (05-http-api §8.3 RequireAuth row): every
// refusal fills nothing. Each case binds a checker that satisfies
// everything, so only the check under test can refuse.
func TestFromCorePrincipal_Refusals(t *testing.T) {
	b := newBridge(t, nil)
	noUser := principal(coreauth.MethodSession, laxChecker{})
	noUser.User = nil
	cases := map[string]struct {
		ctx   context.Context
		users UserLookup
		want  error
	}{
		"no principal":              {bound(nil, laxChecker{}), nil, ErrUnauthenticated},
		"a principal with no user":  {bound(noUser, laxChecker{}), nil, ErrUnauthenticated},
		"an unlisted extension":     {as("ext:penguin", laxChecker{}), nil, ErrUnmappedMethod},
		"an unlisted compat method": {as("books.riddler", laxChecker{}), nil, ErrUnmappedMethod},
		"a prefix of a mapped name": {as("books.ab", laxChecker{}), nil, ErrUnmappedMethod},
		"a mapped name and a tail":  {as("ext:cfaccess2", laxChecker{}), nil, ErrUnmappedMethod},
		"the empty method":          {as("", laxChecker{}), nil, ErrUnmappedMethod},
		"no checker bound":          {bound(principal(coreauth.MethodSession, laxChecker{}), nil), nil, ErrNoChecker},
		"the lookup fails":          {as(coreauth.MethodSession, laxChecker{}), failingLookup, ErrUnauthenticated},
		"the lookup finds nobody":   {as(coreauth.MethodSession, laxChecker{}), nilLookup, ErrUnauthenticated},
		"the lookup finds another":  {as(coreauth.MethodSession, laxChecker{}), constLookup(&database.User{ID: "u-joker", Status: "active"}), ErrUnauthenticated},
		"the legacy record is off":  {as(coreauth.MethodSession, laxChecker{}), constLookup(&database.User{ID: bruceID, Status: "disabled"}), ErrUnauthenticated},
		"the legacy record pending": {as(coreauth.MethodSession, laxChecker{}), constLookup(&database.User{ID: bruceID, Status: "pending"}), ErrUnauthenticated},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bb := b
			if tc.users != nil {
				bb = &Bridge{atoms: b.atoms, users: tc.users}
			}
			got, err := bb.FromCorePrincipal(tc.ctx)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if u, perms, m := legacyValues(got); u != nil || perms != nil || m != auth.MethodNone {
				t.Errorf("a refusal filled user=%v perms=%v method=%q", u, perms, m)
			}
		})
	}
}

// failingLookup fails while still returning a matching record, so only the
// error check can refuse.
func failingLookup(context.Context, coreauth.UserID) (*database.User, error) {
	return bruce(), errors.New("store closed")
}

func nilLookup(context.Context, coreauth.UserID) (*database.User, error) { return nil, nil }

func constLookup(u *database.User) UserLookup {
	return func(context.Context, coreauth.UserID) (*database.User, error) { return u, nil }
}

// TestFromCorePrincipal_AuthDisabledFillsNothing (SEC5-9): HEAD binds no
// user, permission or method with auth off, so the auth-disabled principal
// fills nothing, even under a checker that satisfies every atom.
func TestFromCorePrincipal_AuthDisabledFillsNothing(t *testing.T) {
	got, err := newBridge(t, nil).FromCorePrincipal(as(coreauth.MethodAuthDisabled, laxChecker{}))
	if err != nil {
		t.Fatal(err)
	}
	if u, perms, m := legacyValues(got); u != nil || perms != nil || m != auth.MethodNone {
		t.Errorf("the auth-disabled principal filled user=%v perms=%v method=%q", u, perms, m)
	}
}

// TestFromCorePrincipal_FillsHEADsValues: the user is the legacy record, the
// atoms are exactly those whose route requirement the bound checker
// satisfies (never a table row of a kind no checker evaluates), and the
// method keeps its name.
func TestFromCorePrincipal_FillsHEADsValues(t *testing.T) {
	b := newBridge(t, nil)
	viewer := setChecker{"books:view", "core:tasks.view", "core:users.view", "books:scan"}
	for _, m := range []coreauth.Method{coreauth.MethodSession, coreauth.MethodSessionDelegated, coreauth.MethodAPIKey} {
		got, err := b.FromCorePrincipal(as(m, viewer))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		u, perms, lm := legacyValues(got)
		if u == nil || u.ID != bruceID {
			t.Errorf("%s: user = %v", m, u)
		}
		if want := []auth.Permission{"library.view", "scan.trigger"}; !slices.Equal(perms, want) {
			t.Errorf("%s: atoms = %v, want %v (users.manage's read side is no grant of the atom)", m, perms, want)
		}
		if string(lm) != string(m) {
			t.Errorf("method = %q, want %q", lm, m)
		}
	}
	got, err := b.FromCorePrincipal(as(coreauth.MethodSession, laxChecker{}))
	if err != nil {
		t.Fatal(err)
	}
	_, perms, _ := legacyValues(got)
	if want := []auth.Permission{"library.edit_metadata", "library.view", "scan.trigger", "users.manage"}; !slices.Equal(perms, want) {
		t.Errorf("atoms under a checker that satisfies everything = %v, want the checkable rows only %v", perms, want)
	}
}

// TestCheck_Refusals (05-http-api §8.3 in-handler row, 05-rbac §4.11,
// COR6-1): no principal, no checker, an unknown atom or one whose entry no
// checker evaluates, a lacking scope and the auth-disabled principal are
// each refused, under a checker that satisfies everything wherever the case
// allows.
func TestCheck_Refusals(t *testing.T) {
	b := newBridge(t, nil)
	cases := map[string]struct {
		ctx  context.Context
		atom string
		want error
	}{
		"no principal":                 {bound(nil, laxChecker{}), "library.view", ErrUnauthenticated},
		"no checker":                   {bound(principal(coreauth.MethodSession, laxChecker{}), nil), "library.view", rbac.ErrDenied},
		"an unknown atom":              {as(coreauth.MethodSession, laxChecker{}), "batcave.flood", ErrUnknownAtom},
		"an entry of Public":           {as(coreauth.MethodSession, laxChecker{}), "oracle.public", ErrUnknownAtom},
		"an entry of AnyAuthenticated": {as(coreauth.MethodSession, laxChecker{}), "oracle.anyone", ErrUnknownAtom},
		"an entry of LegacyAtom":       {as(coreauth.MethodSession, laxChecker{}), "oracle.legacy", ErrUnknownAtom},
		"an entry of LegacyCheck":      {as(coreauth.MethodSession, laxChecker{}), "oracle.check", ErrUnknownAtom},
		"a zero entry":                 {as(coreauth.MethodSession, laxChecker{}), "oracle.zero", ErrUnknownAtom},
		"an entry of Delegated":        {as(coreauth.MethodSession, laxChecker{}), "oracle.delegate", ErrUnknownAtom},
		"a lacking scope":              {as(coreauth.MethodSession, setChecker{"books:view"}), "library.view", rbac.ErrDenied},
		"the auth-disabled principal":  {as(coreauth.MethodAuthDisabled, laxChecker{}), "library.view", ErrAuthDisabledDenied},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := b.Check(tc.ctx, tc.atom, false); !errors.Is(err, tc.want) {
				t.Errorf("Check = %v, want %v", err, tc.want)
			}
			if b.Can(tc.ctx, tc.atom) {
				t.Error("Can holds")
			}
		})
	}
	if !errors.Is(ErrAuthDisabledDenied, rbac.ErrDenied) {
		t.Error("the auth-disabled refusal does not match rbac.ErrDenied")
	}
}

// TestCheck_ReadSide (05-rbac §6.1): a read-side site judges users.manage by
// core:users.view alone; its route requirement still needs the full AllOf.
func TestCheck_ReadSide(t *testing.T) {
	b := newBridge(t, nil)
	ctx := as(coreauth.MethodSession, setChecker{"core:users.view"})
	if !b.CanReadSide(ctx, "users.manage") {
		t.Error("core:users.view does not pass the read side of users.manage")
	}
	if b.Can(ctx, "users.manage") {
		t.Error("core:users.view passes users.manage's route requirement")
	}
	if !b.Can(as(coreauth.MethodSession, setChecker{"core:users.manage", "core:roles.view"}), "users.manage") {
		t.Error("the full users.manage scopes do not pass it")
	}
	if b.CanReadSide(as(coreauth.MethodAuthDisabled, laxChecker{}), "users.manage") {
		t.Error("the auth-disabled principal passes a read-side site")
	}
}
