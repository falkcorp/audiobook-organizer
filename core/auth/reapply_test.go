// file: core/auth/reapply_test.go
// version: 1.0.0
// guid: 5b8c3e17-9d02-4a64-b1f5-e7a02c9d6318
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// TestStageReapply_NarrowsIntoTheCallersBatch: the same-store tail re-apply
// stages the journaled revocations into the caller's batch (nothing is written
// until it commits), changes only what the lines name, runs no hook (no epoch
// bump, no session or one-time-token deletion) and appends no journal line.
func TestStageReapply_NarrowsIntoTheCallersBatch(t *testing.T) {
	ctx := context.Background()
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	barbara := f.user("barbara-gordon", "gotham.detective")
	importKey(t, f, "key-redbird", dick.ID, auth.KeyActive, "gotham:patrol")
	importKey(t, f, "key-batgirl", barbara.ID, auth.KeyActive, "gotham:patrol")
	sess, err := f.svc.IssueSession(ctx, barbara, auth.OriginPassword, false, from("192.0.2.40"))
	if err != nil {
		t.Fatal(err)
	}
	epoch := f.reload(barbara.ID).CredentialEpoch

	b := f.st.NewBatch()
	defer func() { _ = b.Close() }()
	counts, err := auth.StageReapply(ctx, f.restoreDeps(nil, nil), b, []journal.Revocation{
		{Kind: journal.APIKeyRevoked, Subject: keyRef("key-redbird")},
		{Kind: journal.UserDisabled, Subject: userRef(dick.ID)},
		{Kind: journal.RestoreAborted},                                   // names no row
		{Kind: journal.RoleDeleted, Subject: keyRef("gotham.detective")}, // rbac's
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.status("key-redbird"); got != auth.KeyActive {
		t.Fatalf("the key is %q before the batch commits: StageReapply wrote into the store", got)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if f.status("key-redbird") != auth.KeyRevoked || f.reload(dick.ID).Status == auth.StatusActive {
		t.Errorf("the journaled revocations did not apply: key %q, user %q", f.status("key-redbird"), f.reload(dick.ID).Status)
	}
	if f.status("key-batgirl") != auth.KeyActive || f.reload(barbara.ID).Status != auth.StatusActive {
		t.Errorf("a row no line named changed: key %q, user %q", f.status("key-batgirl"), f.reload(barbara.ID).Status)
	}
	if got := f.reload(barbara.ID).CredentialEpoch; got != epoch {
		t.Errorf("epoch %d, want %d: a tail re-apply bumps nothing", got, epoch)
	}
	if _, err := f.st.SessionByToken(ctx, auth.Token(sess.Token)); err != nil {
		t.Errorf("a session no line named was deleted: %v", err)
	}
	if counts[auth.CountKeysRevoked] != 1 || counts[auth.CountSignInsDeleted] != 0 || counts[auth.CountEpochsBumped] != 0 {
		t.Errorf("counts = %v", counts)
	}
	if len(f.jrnl.lines) != 0 {
		t.Errorf("StageReapply appended %d journal line(s); the lines are already journaled", len(f.jrnl.lines))
	}
}

// TestStageReapply_IdempotentAndNarrowingOnly: a second pass over the same
// lines changes nothing, and a line never raises a key or user the store
// already restricted further.
func TestStageReapply_IdempotentAndNarrowingOnly(t *testing.T) {
	ctx := context.Background()
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	importKey(t, f, "key-redbird", dick.ID, auth.KeyRevoked, "gotham:patrol")
	importKey(t, f, "key-nightwing", dick.ID, auth.KeyNeedsConfirmation, "gotham:patrol")
	lines := []journal.Revocation{
		{Kind: journal.APIKeyRevoked, Subject: keyRef("key-redbird")},
		{Kind: journal.APIKeyDisabled, Subject: keyRef("key-nightwing")}, // less restrictive than the stored status
		{Kind: journal.APIKeyRevoked, Subject: keyRef("key-unknown")},
	}
	for pass := 1; pass <= 2; pass++ {
		b := f.st.NewBatch()
		counts, err := auth.StageReapply(ctx, f.restoreDeps(nil, nil), b, lines)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		_ = b.Close()
		if len(counts) != 0 {
			t.Errorf("pass %d: counts = %v, want none", pass, counts)
		}
		if f.status("key-redbird") != auth.KeyRevoked || f.status("key-nightwing") != auth.KeyNeedsConfirmation {
			t.Errorf("pass %d: redbird %q, nightwing %q", pass, f.status("key-redbird"), f.status("key-nightwing"))
		}
	}
}

// TestStageReapply_RequiresItsInputs: a missing store or batch is an error, not
// a silent no-op.
func TestStageReapply_RequiresItsInputs(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	if _, err := auth.StageReapply(ctx, auth.RestoreDeps{}, f.st.NewBatch(), nil); err == nil {
		t.Error("StageReapply without a Store succeeded")
	}
	if _, err := auth.StageReapply(ctx, f.restoreDeps(nil, nil), nil, nil); err == nil {
		t.Error("StageReapply without a batch succeeded")
	}
}
