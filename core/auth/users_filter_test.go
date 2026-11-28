// file: core/auth/users_filter_test.go
// version: 1.0.0
// guid: 7a3ebb7b-d64b-42ae-9dcc-313ff47c1bf2
// last-edited: 2026-11-28

package auth_test

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Service.Users and the offline open's account read (kernel.Offline.Users)
// list through one rule, auth.FilterUsers. Over one store, for every filter,
// the service's list and FilterUsers over the store's scan are the same
// accounts in the same order, so neither can drift from the other.
func TestFilterUsers_IsServiceUsersRule(t *testing.T) {
	f := newFx(t)
	f.user("bruce", rbac.RoleAdmin)
	f.user("alfred", rbac.RoleViewer)
	f.user("dick", rbac.RoleEditor, rbac.RoleViewer)
	ctx := context.Background()
	ids := func(seq func(func(*auth.User, error) bool)) []auth.UserID {
		var out []auth.UserID
		for u, err := range seq {
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			out = append(out, u.ID)
		}
		return out
	}
	for _, flt := range []auth.UserFilter{
		{},
		{Role: rbac.RoleViewer},
		{Role: rbac.RoleAdmin},
		{Status: auth.StatusActive},
		{Status: auth.StatusDisabled},
		{Status: auth.StatusActive, Role: rbac.RoleEditor},
	} {
		svc := ids(f.svc.Users(ctx, flt))
		raw := ids(auth.FilterUsers(f.st.ScanUsers(ctx), flt))
		if !slices.Equal(svc, raw) {
			t.Errorf("filter %+v: Service.Users = %v, FilterUsers over the scan = %v", flt, svc, raw)
		}
	}
	if got := ids(f.svc.Users(ctx, auth.UserFilter{Role: rbac.RoleViewer})); len(got) != 2 {
		t.Errorf("the viewer filter selects %v, want alfred and dick", got)
	}
}
