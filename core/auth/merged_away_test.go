// file: core/auth/merged_away_test.go
// version: 0.1.0
// guid: 37b4e21b-cd50-4f9f-bc89-b6dd6eb4fdc8
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// The merged-into marker (User.MergedInto, F4-08 B10l): an account an admin
// user merge folded into another stays disabled. HEAD refused to reactivate
// such an account (handlers.ReactivateUser); SetStatus is core's one enable
// path, so the refusal lives there and not in a handler.

// TestSetStatus_EnableRefusesMergedAwayAccount: a disabled user is enabled when
// it carries no marker and refused, unchanged, when it does.
func TestSetStatus_EnableRefusesMergedAwayAccount(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.user("dick-grayson", "gotham.detective")
	for _, u := range []*auth.User{jason, dick} {
		f.edit(u, func(c *auth.User) { c.Status = auth.StatusDisabled })
	}
	f.edit(jason, func(c *auth.User) { c.MergedInto, c.MergedAt = dick.ID, f.clock() })
	admin := f.signedIn(bruce)

	if err := f.svc.SetStatus(ctx, admin, dick.ID, auth.StatusActive); err != nil {
		t.Fatalf("enable an unmarked disabled user: %v", err)
	}
	if got := f.reload(dick.ID); got.Status != auth.StatusActive {
		t.Fatalf("unmarked user after enable: %s", got.Status)
	}

	before := f.reload(jason.ID)
	wantErr(t, "enable a merged-away user", f.svc.SetStatus(ctx, admin, jason.ID, auth.StatusActive), auth.ErrMergedAway)
	after := f.reload(jason.ID)
	if after.Status != auth.StatusDisabled || after.Version != before.Version || after.CredentialEpoch != before.CredentialEpoch {
		t.Fatalf("a refused enable wrote: status %s version %d->%d epoch %d->%d",
			after.Status, before.Version, after.Version, before.CredentialEpoch, after.CredentialEpoch)
	}
	if after.MergedInto != dick.ID || after.MergedAt.IsZero() {
		t.Fatalf("the marker did not survive the refused enable: %+v", after)
	}
	// Disabling it (already disabled) is still the no-op it was.
	if err := f.svc.SetStatus(ctx, admin, jason.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("disable a merged-away user: %v", err)
	}
}
