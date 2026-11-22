// file: core/auth/merge_lifecycle_test.go
// version: 0.1.0
// guid: f726ecd9-6a8e-4d64-8f4b-801e19bfefbc
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// A committed merge fires Lifecycle.UserMergedAway once for the source, with
// the address the source held before the merge cleared it and the target it
// went into. The step fires only once the store shows the merge: staged but
// not committed, nothing fires.
func TestUserMerge_FiresUserMergedAwayAfterTheCommit(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.signable(f.userWithEmail("jason-todd", "Jason.Todd@example.test", false, "gotham.detective"))
	dick := f.signable(f.user("dick-grayson", "gotham.detective"))
	rec := f.lifecycle()
	actor := f.signedIn(bruce)
	ctx := auth.WithPrincipal(context.Background(), actor)

	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	after, err := f.svc.StageUserMerge(ctx, b, f.planMerge(actor, jason.ID, dick.ID))
	if err != nil {
		t.Fatal(err)
	}
	after(ctx) // before the commit: the store does not show the merge
	rec.wantNone(t, "an AfterCommit run before the commit")
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	after(ctx)
	if d, m := rec.removals(); d != 0 || m != 1 {
		t.Fatalf("callbacks: deleted=%d merged=%d, want 0 and 1", d, m)
	}
	got := rec.merged[0]
	if got.UserID != jason.ID || got.MergedInto != dick.ID || got.Email != "Jason.Todd@example.test" {
		t.Fatalf("UserMergedAway = %+v, want %s into %s holding the address the merge cleared", got, jason.ID, dick.ID)
	}
	if f.reload(jason.ID).Email != "" {
		t.Fatal("the merge did not clear the source's address; the payload's address must be the one held before")
	}
}
