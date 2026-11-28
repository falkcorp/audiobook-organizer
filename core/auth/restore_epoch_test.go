// file: core/auth/restore_epoch_test.go
// version: 0.2.0
// guid: 3d5b3d3e-6b0a-4c77-a0a4-9b4fd2f6a1c2
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"
)

// errKilled is the process death the wrappers below stand for.
var errKilled = errors.New("the process died here")

// epochKill is the store of a run that dies at one point of PostRestore's step 3:
// before the marker is staged (failMarker) or while the user's bump is staged
// (failPut, a user's ID). The batch that carries the bumps never commits.
type epochKill struct {
	auth.Store
	failMarker bool
	failPut    auth.UserID
}

func (k epochKill) StageRestoreEpochRun(b *storage.Batch, r auth.RestoreEpochRun) error {
	if k.failMarker {
		return errKilled
	}
	return k.Store.StageRestoreEpochRun(b, r)
}

func (k epochKill) PutUser(b *storage.Batch, u *auth.User) error {
	if k.failPut != "" && u.ID == k.failPut {
		return errKilled
	}
	return k.Store.PutUser(b, u)
}

// afterMarker is a store that runs hook once the marker is staged, as the step's
// last act before its commit.
type afterMarker struct {
	auth.Store
	hook func()
}

func (a *afterMarker) StageRestoreEpochRun(b *storage.Batch, r auth.RestoreEpochRun) error {
	if err := a.Store.StageRestoreEpochRun(b, r); err != nil {
		return err
	}
	if h := a.hook; h != nil {
		a.hook = nil
		h()
	}
	return nil
}

// epochFixture is dick with a session (one sign-in for step 3 to delete) and
// the epoch he starts at.
func epochFixture(t *testing.T) (*fx, *auth.User, int64) {
	t.Helper()
	f := newFx(t)
	dick := f.user("dick-grayson", "gotham.detective")
	if _, err := f.svc.IssueSession(context.Background(), dick, auth.OriginPassword, false, from("192.0.2.40")); err != nil {
		t.Fatal(err)
	}
	return f, dick, dick.CredentialEpoch
}

// postRestoreRun is one run of the hook for the restore named key, over store
// (nil: the fixture's own).
func postRestoreRun(t *testing.T, f *fx, store auth.Store, key string) (auth.RestoreCounts, error) {
	t.Helper()
	d := f.restoreDeps(nil, nil)
	if store != nil {
		d.Store = store
	}
	d.RunKey = key
	return auth.PostRestore(context.Background(), d, uncovered())
}

func mustRun(t *testing.T, f *fx, key string) auth.RestoreCounts {
	t.Helper()
	counts, err := postRestoreRun(t, f, nil, key)
	if err != nil {
		t.Fatalf("PostRestore(%s): %v", key, err)
	}
	return counts
}

// TestPostRestore_AResumedRunBumpsTheEpochOnce (F4-08 B10j, crash-matrix row
// kernel.hooks.rbac #1): the bump is epoch+1, so a run that is killed and then
// booted again must tell its own committed bump from none.
func TestPostRestore_AResumedRunBumpsTheEpochOnce(t *testing.T) {
	// The fault-free run is what the others must equal.
	ref, refDick, start := epochFixture(t)
	want := mustRun(t, ref, "seq:11")
	if got := ref.reload(refDick.ID).CredentialEpoch; got != start+1 {
		t.Fatalf("the fault-free run left the epoch at %d, want %d", got, start+1)
	}
	if want[auth.CountEpochsBumped] < 1 || want[auth.CountSignInsDeleted] != 1 {
		t.Fatalf("the fault-free counts %v are empty of what the test compares", want)
	}

	t.Run("killed before the commit, then resumed", func(t *testing.T) {
		for name, kill := range map[string]func(dick auth.UserID, st auth.Store) auth.Store{
			"before the marker is staged":   func(_ auth.UserID, st auth.Store) auth.Store { return epochKill{Store: st, failMarker: true} },
			"while a user's bump is staged": func(id auth.UserID, st auth.Store) auth.Store { return epochKill{Store: st, failPut: id} },
		} {
			f, dick, start := epochFixture(t)
			if _, err := postRestoreRun(t, f, kill(dick.ID, f.st), "seq:11"); !errors.Is(err, errKilled) {
				t.Fatalf("%s: the killed run returned %v", name, err)
			}
			if got := f.reload(dick.ID).CredentialEpoch; got != start {
				t.Fatalf("%s: the killed run left epoch %d, want the untouched %d: its batch committed in part", name, got, start)
			}
			counts := mustRun(t, f, "seq:11")
			if got := f.reload(dick.ID).CredentialEpoch; got != start+1 {
				t.Errorf("%s: after the resume the epoch is %d, want %d", name, got, start+1)
			}
			if counts[auth.CountEpochsBumped] != want[auth.CountEpochsBumped] || counts[auth.CountSignInsDeleted] != want[auth.CountSignInsDeleted] {
				t.Errorf("%s: resumed counts %v, want the fault-free %v", name, counts, want)
			}
		}
	})

	t.Run("a commit that conflicts after the marker is staged retries and bumps once", func(t *testing.T) {
		// A write to a user lands between the step's reads and its commit, so the
		// commit fails with storage.ErrConflict and the step runs again. The marker
		// staged by the failed attempt was never committed, so the retry must still
		// bump: a marker committed beside the bumps would make it skip.
		f, dick, start := epochFixture(t)
		st := &afterMarker{Store: f.st, hook: func() {
			f.edit(dick, func(u *auth.User) { u.UpdatedAt = u.UpdatedAt.Add(time.Second) })
		}}
		counts, err := postRestoreRun(t, f, st, "seq:11")
		if err != nil {
			t.Fatal(err)
		}
		if got := f.reload(dick.ID).CredentialEpoch; got != start+1 {
			t.Errorf("epoch %d after a retried step, want %d", got, start+1)
		}
		if counts[auth.CountEpochsBumped] != want[auth.CountEpochsBumped] {
			t.Errorf("counts %v, want the fault-free %v", counts, want)
		}
	})

	t.Run("killed after the commit, then resumed", func(t *testing.T) {
		f, dick, start := epochFixture(t)
		first := mustRun(t, f, "seq:11")
		second := mustRun(t, f, "seq:11")
		if got := f.reload(dick.ID).CredentialEpoch; got != start+1 {
			t.Fatalf("the resume bumped again: epoch %d, want %d", got, start+1)
		}
		for _, c := range []auth.RestoreCounts{first, second} {
			if c[auth.CountEpochsBumped] != want[auth.CountEpochsBumped] || c[auth.CountSignInsDeleted] != want[auth.CountSignInsDeleted] {
				t.Errorf("counts %v, want the fault-free %v (the resume reports what the first run committed)", c, want)
			}
		}
	})

	t.Run("a resumed run still deletes the sign-ins that are there", func(t *testing.T) {
		f, dick, start := epochFixture(t)
		mustRun(t, f, "seq:11")
		sess, err := f.svc.IssueSession(context.Background(), f.reload(dick.ID), auth.OriginPassword, false, from("192.0.2.41"))
		if err != nil {
			t.Fatal(err)
		}
		counts := mustRun(t, f, "seq:11")
		if _, err := f.st.SessionByToken(context.Background(), auth.Token(sess.Token)); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a sign-in that exists survived the resumed run: %v", err)
		}
		if got := f.reload(dick.ID).CredentialEpoch; got != start+1 {
			t.Errorf("epoch %d, want %d", got, start+1)
		}
		if counts[auth.CountSignInsDeleted] != want[auth.CountSignInsDeleted]+1 {
			t.Errorf("sign-ins deleted %d, want the first run's %d plus the one now deleted", counts[auth.CountSignInsDeleted], want[auth.CountSignInsDeleted])
		}
	})

	t.Run("another restore bumps again, and so does one after an old marker", func(t *testing.T) {
		f, dick, start := epochFixture(t)
		mustRun(t, f, "seq:11")
		mustRun(t, f, "seq:19")
		if got := f.reload(dick.ID).CredentialEpoch; got != start+2 {
			t.Fatalf("a different run left epoch %d, want %d: the old marker was taken for this run's", got, start+2)
		}
		run, ok, err := f.st.RestoreEpochRun(context.Background())
		if err != nil || !ok || run.RunKey != "seq:19" {
			t.Fatalf("marker %+v ok=%v err=%v, want the latest run's", run, ok, err)
		}
		mustRun(t, f, "seq:11")
		if got := f.reload(dick.ID).CredentialEpoch; got != start+3 {
			t.Errorf("a run whose key an earlier run used, behind a newer marker, left epoch %d, want %d", got, start+3)
		}
	})

	t.Run("a run with no key has nothing to mark and bumps every time", func(t *testing.T) {
		f, dick, start := epochFixture(t)
		mustRun(t, f, "")
		mustRun(t, f, "")
		if got := f.reload(dick.ID).CredentialEpoch; got != start+2 {
			t.Errorf("epoch %d, want %d", got, start+2)
		}
		if _, ok, err := f.st.RestoreEpochRun(context.Background()); err != nil || ok {
			t.Errorf("a run with no key wrote a marker: ok=%v err=%v", ok, err)
		}
	})
}
