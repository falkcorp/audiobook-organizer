// file: core/auth/restore_runkey_test.go
// version: 0.1.0
// guid: 8a666e1e-c8a3-485d-8a20-49ad1e941dbd
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// cancelAfterLines is a journal that cancels the run's context once it took a
// line: the batch that carries the lines then cannot commit, which is a process
// death between the journal line (durable at once) and the commit of the store
// rows (F4-08 B10j, crash-matrix row restrict_kept #0).
type cancelAfterLines struct {
	*journalFake
	cancel context.CancelFunc
}

func (j cancelAfterLines) Append(ctx context.Context, b *storage.Batch, r journal.Revocation) error {
	err := j.journalFake.Append(ctx, b, r)
	j.cancel()
	return err
}

// journaledIn is journal.Component.Journaled over the fake's lines.
func journaledIn(j *journalFake) func(journal.Revocation) (bool, error) {
	return func(r journal.Revocation) (bool, error) {
		j.mu.Lock()
		defer j.mu.Unlock()
		return slices.ContainsFunc(j.lines, func(l journal.Revocation) bool {
			return l.Kind == r.Kind && l.Subject == r.Subject && l.Object == r.Object && slices.Equal(l.Detail, r.Detail)
		}), nil
	}
}

// restrictedAdmin is bruce, an admin with a password, an active key and an SSO
// link: every door RestrictLastAdmins closes.
func restrictedAdmin(t *testing.T, f *fx) *auth.User {
	t.Helper()
	bruce := f.withPassword(f.user("bruce-wayne", rbac.RoleAdmin), "i-am-the-night-1")
	importKey(t, f, "key-cowl", bruce.ID, auth.KeyActive, "core:admin")
	f.linkIdentity(bruce, "google", "sub-bruce")
	f.jrnl.lines = nil
	return bruce
}

// wantFourEffects checks the four things a restricted last admin is, one at a
// time, so a restriction that lost any of them names which.
func wantFourEffects(t *testing.T, f *fx, id auth.UserID) {
	t.Helper()
	u := f.reload(id)
	if !u.SignInRestricted {
		t.Errorf("effect 1: SignInRestricted is not set: the user can sign in through an extension or compat")
	}
	if got := f.status("key-cowl"); got != auth.KeyNeedsConfirmation {
		t.Errorf("effect 2: the user's API key is %q, want needs_confirmation", got)
	}
	if f.hasIdentity("google", "sub-bruce") {
		t.Errorf("effect 3: the external-identity link is still there")
	}
	if u.HasPassword || !u.PasswordResetRequired {
		t.Errorf("effect 4: HasPassword=%v PasswordResetRequired=%v, want the hash blanked and a reset required", u.HasPassword, u.PasswordResetRequired)
	}
}

// TestRestrictLastAdmins_ACrashBetweenTheLinesAndTheCommitIsResumedOnce: the
// restriction journals its lines before it commits the rows they describe. A
// process death in between leaves the lines durable and the rows untouched. The
// resumed run (same restore, same run key) appends none of them again, and still
// restricts the user in full: a durable line is never taken for the restriction.
func TestRestrictLastAdmins_ACrashBetweenTheLinesAndTheCommitIsResumedOnce(t *testing.T) {
	f := newFx(t)
	bruce := restrictedAdmin(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	d := f.restoreDeps(nil, nil)
	d.Revocations = cancelAfterLines{f.jrnl, cancel}
	d.RunKey, d.Journaled = "restore:r-bane", journaledIn(f.jrnl)
	if _, err := auth.RestrictLastAdmins(ctx, d, []auth.UserID{bruce.ID}); err == nil {
		t.Fatal("the run with its context canceled after the lines committed")
	}
	if u := f.reload(bruce.ID); u.SignInRestricted || !u.HasPassword {
		t.Fatalf("the killed run left bruce restricted (%+v): the kill did not fall between the lines and the commit", u)
	}
	first := slices.Clone(f.jrnl.lines)
	if len(first) != 3 {
		t.Fatalf("the killed run journaled %d lines, want 3 (password, key, identity)", len(first))
	}

	resumed := f.restoreDeps(nil, nil)
	resumed.RunKey, resumed.Journaled = "restore:r-bane", journaledIn(f.jrnl)
	if _, err := auth.RestrictLastAdmins(context.Background(), resumed, []auth.UserID{bruce.ID}); err != nil {
		t.Fatal(err)
	}
	kinds := kindsBySubject(f.jrnl.lines, string(bruce.ID))
	if kinds[journal.PasswordChanged] != 1 || kinds[journal.IdentityUnlinked] != 1 || kinds[journal.APIKeyDisabled]+kindsBySubject(f.jrnl.lines, "key-cowl")[journal.APIKeyDisabled] != 1 {
		t.Errorf("after the resume the journal holds %d lines, want the 3 of the first run: %v", len(f.jrnl.lines), f.jrnl.lines)
	}
	if len(f.jrnl.lines) != 3 {
		t.Errorf("the resumed run appended %d more lines, want none", len(f.jrnl.lines)-3)
	}
	wantFourEffects(t, f, bruce.ID)
}

// TestRestrictLastAdmins_ADifferentRunJournalsItsOwnLines: the key is the run's,
// not the user's. A second restore that keeps the same admin restricts and
// journals again (its lines name another run).
func TestRestrictLastAdmins_ADifferentRunJournalsItsOwnLines(t *testing.T) {
	f := newFx(t)
	bruce := restrictedAdmin(t, f)
	for _, key := range []string{"restore:r-one", "restore:r-two"} {
		d := f.restoreDeps(nil, nil)
		d.RunKey, d.Journaled = key, journaledIn(f.jrnl)
		if _, err := auth.RestrictLastAdmins(context.Background(), d, []auth.UserID{bruce.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if n := kindsBySubject(f.jrnl.lines, string(bruce.ID))[journal.PasswordChanged]; n != 2 {
		t.Errorf("two restores journaled %d password.changed lines for bruce, want 2", n)
	}
	wantFourEffects(t, f, bruce.ID)
}

// TestRestrictLastAdmins_ALineThatCannotBeLookedUpFailsTheRun: a journal that
// cannot say whether it holds the line is an error, never "no": appending would
// duplicate a line it may hold.
func TestRestrictLastAdmins_ALineThatCannotBeLookedUpFailsTheRun(t *testing.T) {
	f := newFx(t)
	bruce := restrictedAdmin(t, f)
	boom := errors.New("the journal file is unreadable")
	d := f.restoreDeps(nil, nil)
	d.RunKey, d.Journaled = "restore:r-bane", func(journal.Revocation) (bool, error) { return false, boom }
	if _, err := auth.RestrictLastAdmins(context.Background(), d, []auth.UserID{bruce.ID}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup's", err)
	}
	if u := f.reload(bruce.ID); u.SignInRestricted {
		t.Error("a run that failed on the lookup restricted the user")
	}
}
