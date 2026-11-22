// file: core/auth/lifecycle_accept_fields_test.go
// version: 0.1.0
// guid: 337b25b9-970a-4058-b771-9e910aad07e8
// last-edited: 2026-11-22

package auth_test

import (
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// The InviteAccepted event carries the accept's own time and the account's name
// (F4-08 B10x2), so a module that records the accept needs no read of the account
// it was just handed and no clock of its own: both are the values the account row
// was written with, with or without an address, whoever created the invite.
func TestLifecycle_InviteAcceptedCarriesTheAcceptTimeAndUsername(t *testing.T) {
	f := newFx(t)
	rec := f.lifecycle()
	bruce := f.user("bruce", rbac.RoleAdmin)

	for _, tc := range []struct {
		name string
		spec auth.InviteSpec
		pass string
	}{
		{"with an address", auth.InviteSpec{Username: "cassandra-cain", Email: "Cass@Example.test", Role: "gotham.detective"}, "orphan-of-cain"},
		{"no address", auth.InviteSpec{Username: "barbara-gordon", Role: "gotham.detective"}, "oracle-1234"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(rec.invites)
			tok, _ := f.invite(f.session(bruce), tc.spec)
			u, _, err := f.accept(tok, tc.pass)
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.invites) != before+1 {
				t.Fatalf("InviteAccepted ran %d times, want one more than %d", len(rec.invites), before)
			}
			e := rec.invites[before]
			got := f.reload(u.ID)
			if e.AcceptedAt.IsZero() {
				t.Fatalf("InviteAccepted.AcceptedAt is zero: %+v", e)
			}
			if !e.AcceptedAt.Equal(got.CreatedAt) {
				t.Fatalf("InviteAccepted.AcceptedAt = %v, the created account's CreatedAt = %v", e.AcceptedAt, got.CreatedAt)
			}
			if e.Username == "" || e.Username != got.Username || e.Username != tc.spec.Username {
				t.Fatalf("InviteAccepted.Username = %q, the created account's = %q, the invite's = %q", e.Username, got.Username, tc.spec.Username)
			}
		})
	}
}
