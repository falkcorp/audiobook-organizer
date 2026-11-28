// file: core/auth/reenable_journal_test.go
// version: 0.1.0
// guid: 0d7a4c95-3e18-4b62-a2f9-81c6e5b07d34
// last-edited: 2026-11-20

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// F4-08 Q6b (owner ruling 2026-11-20): an admin's re-enable is journaled as a
// user.enabled line, and a restore's replay applies it in journal order, clamped
// by the pre-swap store.

// at is a line time, in minutes after a fixed instant.
func at(min int) time.Time {
	return time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

func disabledLine(id auth.UserID, seq uint64, epoch int64, when time.Time) journal.Revocation {
	return journal.Revocation{Seq: seq, At: when, Kind: journal.UserDisabled, Subject: userRef(id), Epoch: epoch}
}

func enabledLine(id auth.UserID, seq uint64, epoch int64, when time.Time) journal.Revocation {
	return journal.Revocation{Seq: seq, At: when, Kind: journal.UserEnabled, Subject: userRef(id), Epoch: epoch}
}

// restorePair is a restored store and its pre-swap store, each holding
// harvey-dent with the given status.
func restorePair(t *testing.T, restored, preSwap auth.UserStatus) (live, prev *fx, id auth.UserID) {
	t.Helper()
	live, prev = newFx(t), newFx(t)
	for _, f := range []*fx{live, prev} {
		f.user("jim-gordon", roleCommissioner)
		f.user("harvey-dent", "gotham.detective")
	}
	id = "u-harvey-dent"
	live.edit(live.reload(id), func(u *auth.User) { u.Status = restored })
	prev.edit(prev.reload(id), func(u *auth.User) { u.Status = preSwap })
	return live, prev, id
}

// TestSetStatus_ReEnableIsJournaled: a re-enable writes a user.enabled line at
// the epoch it set, after the disable's lines and before its own epoch bump; a
// disable writes none.
func TestSetStatus_ReEnableIsJournaled(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	harvey := f.user("harvey-dent", "gotham.detective")
	m := f.session(gordon)
	if err := f.svc.SetStatus(ctx, m, harvey.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if n := kindsBySubject(f.jrnl.lines, string(harvey.ID))[journal.UserEnabled]; n != 0 {
		t.Fatalf("a disable journaled %d user.enabled lines", n)
	}
	if err := f.svc.SetStatus(ctx, m, harvey.ID, auth.StatusActive); err != nil {
		t.Fatal(err)
	}
	var got []journal.Kind
	var enabled journal.Revocation
	for _, r := range f.jrnl.lines {
		if r.Subject.ID != string(harvey.ID) {
			continue
		}
		got = append(got, r.Kind)
		if r.Kind == journal.UserEnabled {
			enabled = r
		}
	}
	want := []journal.Kind{journal.UserDisabled, journal.CredentialEpochBumped, journal.UserEnabled, journal.CredentialEpochBumped}
	if len(got) != len(want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lines = %v, want %v", got, want)
		}
	}
	if u := f.reload(harvey.ID); enabled.Epoch != u.CredentialEpoch || enabled.Epoch <= 0 {
		t.Errorf("user.enabled epoch %d, the user's %d", enabled.Epoch, u.CredentialEpoch)
	}
}

// TestPostRestore_ReEnableAfterRevocationSurvives (owner cell 1): a restore
// that re-applies a disable journaled after the archive also applies the
// re-enable journaled after it, so the user the admin re-enabled is not
// disabled again by the restore.
func TestPostRestore_ReEnableAfterRevocationSurvives(t *testing.T) {
	live, prev, id := restorePair(t, auth.StatusActive, auth.StatusActive)
	counts := live.mustPostRestore(covered(disabledLine(id, 8, 5, at(1)), enabledLine(id, 9, 6, at(2))), prev.st)
	if got := live.reload(id).Status; got != auth.StatusActive {
		t.Fatalf("re-enabled after the disable, restored as %q", got)
	}
	if counts[auth.CountUsersEnabled] != 1 {
		t.Errorf("counts %v, want %s 1", counts, auth.CountUsersEnabled)
	}
}

// TestPostRestore_RevocationAfterReEnableWins (owner cell 2): a disable
// journaled after a re-enable closes the user again, whatever the pre-swap
// store holds (a disable whose batch did not commit still narrows).
func TestPostRestore_RevocationAfterReEnableWins(t *testing.T) {
	for _, preSwap := range []auth.UserStatus{auth.StatusDisabled, auth.StatusActive} {
		t.Run(string(preSwap), func(t *testing.T) {
			live, prev, id := restorePair(t, auth.StatusActive, preSwap)
			live.mustPostRestore(covered(disabledLine(id, 8, 5, at(1)), enabledLine(id, 9, 6, at(2)), disabledLine(id, 10, 7, at(3))), prev.st)
			if got := live.reload(id).Status; got != auth.StatusDisabled {
				t.Fatalf("disabled after the re-enable, restored as %q", got)
			}
		})
	}
}

// TestPostRestore_ReEnableFollowsJournalOrderNotTheClock (owner cell 3): the
// lines' At is informational; with the times inverted the outcome is still the
// journal order's.
func TestPostRestore_ReEnableFollowsJournalOrderNotTheClock(t *testing.T) {
	t.Run("enable last in the journal, earliest by the clock", func(t *testing.T) {
		live, prev, id := restorePair(t, auth.StatusActive, auth.StatusActive)
		live.mustPostRestore(covered(disabledLine(id, 8, 5, at(30)), enabledLine(id, 9, 6, at(1))), prev.st)
		if got := live.reload(id).Status; got != auth.StatusActive {
			t.Fatalf("restored as %q, want the journal order's active", got)
		}
	})
	t.Run("disable last in the journal, earliest by the clock", func(t *testing.T) {
		live, prev, id := restorePair(t, auth.StatusActive, auth.StatusDisabled)
		live.mustPostRestore(covered(enabledLine(id, 8, 6, at(30)), disabledLine(id, 9, 7, at(1))), prev.st)
		if got := live.reload(id).Status; got != auth.StatusDisabled {
			t.Fatalf("restored as %q, want the journal order's disabled", got)
		}
	})
}

// TestPostRestore_MalformedReEnableFailsTheRestore (owner cell 4): a
// user.enabled line without a user subject or with a non-positive epoch fails
// the hook, which fails the boot (any hook error is refuse-to-serve), and
// stages nothing.
func TestPostRestore_MalformedReEnableFailsTheRestore(t *testing.T) {
	for name, edit := range map[string]func(*journal.Revocation){
		"no subject id":  func(r *journal.Revocation) { r.Subject.ID = "" },
		"a key subject":  func(r *journal.Revocation) { r.Subject.Kind = "apikey" },
		"another module": func(r *journal.Revocation) { r.Subject.Module = "books" },
		"zero epoch":     func(r *journal.Revocation) { r.Epoch = 0 },
		"negative epoch": func(r *journal.Revocation) { r.Epoch = -3 },
	} {
		t.Run(name, func(t *testing.T) {
			live, prev, id := restorePair(t, auth.StatusActive, auth.StatusActive)
			bad := enabledLine(id, 9, 6, at(2))
			edit(&bad)
			_, err := auth.PostRestore(context.Background(), live.restoreDeps(nil, prev.st), covered(disabledLine(id, 8, 5, at(1)), bad))
			if !errors.Is(err, auth.ErrBadInput) {
				t.Fatalf("PostRestore = %v, want ErrBadInput", err)
			}
			if got := live.reload(id).Status; got != auth.StatusActive {
				t.Errorf("a failed restore staged the disable: %q", got)
			}
		})
	}
	t.Run("same-store re-apply", func(t *testing.T) {
		f := newFx(t)
		f.user("harvey-dent", "gotham.detective")
		b := f.st.NewBatch()
		defer func() { _ = b.Close() }()
		bad := enabledLine("u-harvey-dent", 9, 0, at(2))
		if _, err := auth.StageReapply(context.Background(), f.restoreDeps(nil, nil), b, []journal.Revocation{bad}); !errors.Is(err, auth.ErrBadInput) {
			t.Fatalf("StageReapply = %v, want ErrBadInput", err)
		}
	})
}

// TestPostRestore_UncommittedReEnableIsClamped: an enable line whose batch
// never committed (written before the commit, the batch then failed) leaves
// the pre-swap row disabled; the pre-swap clamp after the replay closes the
// user again, so the line opens nothing.
func TestPostRestore_UncommittedReEnableIsClamped(t *testing.T) {
	live, prev, id := restorePair(t, auth.StatusActive, auth.StatusDisabled)
	live.mustPostRestore(covered(disabledLine(id, 8, 5, at(1)), enabledLine(id, 9, 6, at(2))), prev.st)
	if got := live.reload(id).Status; got != auth.StatusDisabled {
		t.Fatalf("an enable the pre-swap store never saw opened the user: %q", got)
	}
}

// TestPostRestore_NoPreSwapStoreIgnoresReEnable: a restore with no pre-swap
// store (host loss, a replaced store) has nothing to prove an enable committed,
// so the line is not applied and the disable before it stands.
func TestPostRestore_NoPreSwapStoreIgnoresReEnable(t *testing.T) {
	live, _, id := restorePair(t, auth.StatusActive, auth.StatusActive)
	counts := live.mustPostRestore(covered(disabledLine(id, 8, 5, at(1)), enabledLine(id, 9, 6, at(2))), nil)
	if got := live.reload(id).Status; got != auth.StatusDisabled {
		t.Fatalf("restored with no pre-swap store as %q, want disabled", got)
	}
	if counts[auth.CountUsersEnabled] != 0 {
		t.Errorf("counts %v, want no enable", counts)
	}
}

// TestStageReapply_IgnoresReEnable: the same-store re-apply (the tail past the
// store's head, the queue drain) holds only lines whose batches did not commit,
// so a user.enabled line there opens nothing.
func TestStageReapply_IgnoresReEnable(t *testing.T) {
	f := newFx(t)
	f.user("harvey-dent", "gotham.detective")
	id := auth.UserID("u-harvey-dent")
	f.edit(f.reload(id), func(u *auth.User) { u.Status = auth.StatusDisabled })
	f.commit(func(b *storage.Batch) error {
		_, err := auth.StageReapply(context.Background(), f.restoreDeps(nil, nil), b, []journal.Revocation{enabledLine(id, 9, 6, at(2))})
		return err
	})
	if got := f.reload(id).Status; got != auth.StatusDisabled {
		t.Fatalf("the same-store re-apply re-enabled the user: %q", got)
	}
}

// TestPostRestore_ReEnableOpensOnlyADisabledUser: a user an earlier line
// deleted stays deleted, and a pending invitee and _system are left as they
// are; the line never makes a user more open than a disabled user re-enabled.
func TestPostRestore_ReEnableOpensOnlyADisabledUser(t *testing.T) {
	t.Run("deleted by an earlier line", func(t *testing.T) {
		live, prev, id := restorePair(t, auth.StatusActive, auth.StatusActive)
		del := journal.Revocation{Seq: 8, At: at(1), Kind: journal.UserDeleted, Subject: userRef(id)}
		live.mustPostRestore(covered(del, enabledLine(id, 9, 6, at(2))), prev.st)
		if _, err := live.st.GetUser(context.Background(), id); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("a deleted user after a later enable: GetUser = %v, want ErrNotFound", err)
		}
	})
	t.Run("merged away", func(t *testing.T) {
		// A merged-away account stays disabled for good (SetStatus refuses to
		// enable it); the pre-swap store holds it active so that only the
		// replay's own rule, not the pre-swap clamp, can keep it closed.
		live, prev, id := restorePair(t, auth.StatusDisabled, auth.StatusActive)
		for _, f := range []*fx{live, prev} {
			f.edit(f.reload(id), func(u *auth.User) { u.MergedInto, u.MergedAt = "u-jim-gordon", f.clock() })
		}
		counts := live.mustPostRestore(covered(enabledLine(id, 9, 6, at(2))), prev.st)
		got := live.reload(id)
		if got.Status != auth.StatusDisabled || got.MergedInto != "u-jim-gordon" || counts[auth.CountUsersEnabled] != 0 {
			t.Fatalf("a merged-away user after an enable line: status %q marker %q, counts %v", got.Status, got.MergedInto, counts)
		}
	})
	t.Run("pending", func(t *testing.T) {
		live, prev, id := restorePair(t, auth.StatusPending, auth.StatusPending)
		counts := live.mustPostRestore(covered(enabledLine(id, 9, 6, at(2))), prev.st)
		if got := live.reload(id).Status; got != auth.StatusPending || counts[auth.CountUsersEnabled] != 0 {
			t.Fatalf("a pending invitee after an enable line: %q, counts %v", got, counts)
		}
	})
	t.Run("system", func(t *testing.T) {
		live, prev, _ := restorePair(t, auth.StatusActive, auth.StatusActive)
		sys := auth.UserID("u-system-oracle")
		for _, f := range []*fx{live, prev} {
			f.commit(func(b *storage.Batch) error {
				return f.st.PutUser(b, &auth.User{ID: sys, Username: "oracle-system", Status: auth.StatusSystem,
					CreatedAt: f.clock(), UpdatedAt: f.clock(), Version: 1})
			})
		}
		live.mustPostRestore(covered(enabledLine(sys, 9, 6, at(2))), prev.st)
		if got := live.reload(sys).Status; got != auth.StatusSystem {
			t.Fatalf("a system user after an enable line: %q", got)
		}
	})
}
