// file: core/auth/users_filter_test.go
// version: 1.0.0
// guid: 7a3ebb7b-d64b-42ae-9dcc-313ff47c1bf2
// last-edited: 2026-11-28

package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// FilterUsers is the one account-list rule Service.Users and the offline
// open's account read (kernel.Offline.Users) list through: the accounts the
// filter selects, in scan order, and every row error passed on in its place
// with the list going on after it, so a caller sees every failure and decides.

var (
	errRowA = errors.New("row a does not decode")
	errRowB = errors.New("row b does not decode")
)

// scanOf yields the rows in order, a nil user with its error for an error row,
// and counts how many it handed out.
func scanOf(pulled *int, rows ...any) func(func(*auth.User, error) bool) {
	return func(yield func(*auth.User, error) bool) {
		for _, r := range rows {
			*pulled++
			var ok bool
			switch v := r.(type) {
			case *auth.User:
				ok = yield(v, nil)
			case error:
				ok = yield(nil, v)
			}
			if !ok {
				return
			}
		}
	}
}

// listed renders a list as user IDs and "err:<message>" for each error row.
func listed(seq func(func(*auth.User, error) bool)) []string {
	var out []string
	for u, err := range seq {
		if err != nil {
			out = append(out, "err:"+err.Error())
			continue
		}
		out = append(out, string(u.ID))
	}
	return out
}

func TestFilterUsers_SelectsByFilterAndPassesEveryRowError(t *testing.T) {
	bruce := &auth.User{ID: "u-bruce", Status: auth.StatusActive, Roles: []rbac.RoleName{rbac.RoleAdmin}}
	alfred := &auth.User{ID: "u-alfred", Status: auth.StatusDisabled, Roles: []rbac.RoleName{rbac.RoleViewer}}
	dick := &auth.User{ID: "u-dick", Status: auth.StatusActive, Roles: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}}
	sys := &auth.User{ID: "_system", Status: auth.StatusSystem}
	rows := []any{bruce, errRowA, alfred, dick, sys, errRowB}
	a, b := "err:"+errRowA.Error(), "err:"+errRowB.Error()
	for _, tc := range []struct {
		name string
		f    auth.UserFilter
		want []string
	}{
		{name: "every account, the system user included", f: auth.UserFilter{}, want: []string{"u-bruce", a, "u-alfred", "u-dick", "_system", b}},
		{name: "a role", f: auth.UserFilter{Role: rbac.RoleViewer}, want: []string{a, "u-alfred", "u-dick", b}},
		{name: "a status", f: auth.UserFilter{Status: auth.StatusActive}, want: []string{"u-bruce", a, "u-dick", b}},
		{name: "the system status", f: auth.UserFilter{Status: auth.StatusSystem}, want: []string{a, "_system", b}},
		{name: "status and role", f: auth.UserFilter{Status: auth.StatusActive, Role: rbac.RoleEditor}, want: []string{a, "u-dick", b}},
		{name: "nothing selected, the errors still passed", f: auth.UserFilter{Status: auth.StatusPending}, want: []string{a, b}},
	} {
		pulled := 0
		if got := listed(auth.FilterUsers(scanOf(&pulled, rows...), tc.f)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: FilterUsers = %v, want %v", tc.name, got, tc.want)
		}
		if pulled != len(rows) {
			t.Errorf("%s: the scan was pulled %d times, want all %d rows", tc.name, pulled, len(rows))
		}
	}

	// A consumer that stops at an error row stops the scan there.
	pulled := 0
	for _, err := range auth.FilterUsers(scanOf(&pulled, rows...), auth.UserFilter{}) {
		if err != nil {
			break
		}
	}
	if pulled != 2 {
		t.Errorf("stopping at the first error pulled %d rows, want 2", pulled)
	}
}

// Service.Users over a store holding an undecodable user row: the row comes
// out as ErrUndecodableRecord in its place, and the accounts after it are still
// listed.
func TestServiceUsers_AnUndecodableRowIsAnErrorAndTheListGoesOn(t *testing.T) {
	f := newFx(t)
	f.user("bruce", rbac.RoleAdmin)
	f.user("dick", rbac.RoleViewer)
	// "u-ace" sorts before every fixture account, so the accounts after the
	// bad row are what shows the list going on.
	bad := keys.Join(keys.Str("user"), keys.Str("u-ace"))
	f.commit(func(b *storage.Batch) error {
		b.Bind(f.authKS)
		if err := b.Expect(f.authKS, bad, nil); err != nil {
			return err
		}
		return b.In(f.authKS).Set(bad, []byte("not a user record"))
	})
	var ids []string
	var rowErrs []error
	for u, err := range f.svc.Users(context.Background(), auth.UserFilter{}) {
		if err != nil {
			rowErrs = append(rowErrs, err)
			continue
		}
		ids = append(ids, string(u.ID))
	}
	if len(rowErrs) != 1 || !errors.Is(rowErrs[0], auth.ErrUndecodableRecord) {
		t.Errorf("row errors = %v, want exactly one ErrUndecodableRecord", rowErrs)
	}
	if !slices.Contains(ids, "u-bruce") || !slices.Contains(ids, "u-dick") {
		t.Errorf("accounts = %v, want u-bruce and u-dick after the bad row", ids)
	}
}
