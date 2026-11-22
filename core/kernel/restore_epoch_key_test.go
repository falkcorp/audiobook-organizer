// file: core/kernel/restore_epoch_key_test.go
// version: 0.1.0
// guid: 0b1f6d1a-7c3e-4e1c-9f1d-5a0c6b9a4d77
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// The credential-epoch bump of a restore is the sign-out of every session. The
// marker that lets a re-run skip it is only as strong as its key: a restored
// archive can carry any marker the store ever held, so the key must be one the
// archive cannot contain (F4-08 B10j, review finding on 9aa237608).

// TestHookRunKey_IsTheLiveHostsRestoreIDOrRunIDAndNothingElse: the key is built from
// the restore intent's ID (crypto/rand, kept in the restore journal under the state
// directory) and the restore line's seq, or, for a run with no intent, from the run
// ID of the state directory (crypto/rand, restore_hook_run.json). A run with neither
// has no key, so it can never skip: not on a journal seq, which restarts after a
// journal reset or a host loss.
func TestHookRunKey_IsTheLiveHostsRestoreIDOrRunIDAndNothingElse(t *testing.T) {
	if got := hookRunKey(hookEnv{RestoreID: "r-dent", RestoreSeq: 11}); got != "restore:r-dent:seq:11" {
		t.Errorf("a staged restore keys %q, want restore:r-dent:seq:11", got)
	}
	if hookRunKey(hookEnv{RestoreID: "r-dent", RestoreSeq: 11}) == hookRunKey(hookEnv{RestoreID: "r-other", RestoreSeq: 11}) {
		t.Error("two restores with the same restore line seq share a key: the key does not carry the restore's ID")
	}
	if got := hookRunKey(hookEnv{HostRun: "run:00ff"}); got != "run:00ff" {
		t.Errorf("a replaced store with no line keys %q, want its run ID", got)
	}
	if got := hookRunKey(hookEnv{HostRun: "run:00ff", RestoreSeq: 11}); got != "run:00ff" {
		t.Errorf("a replaced store with a restore line keys %q, want its run ID and not the seq", got)
	}
	if hookRunKey(hookEnv{HostRun: "run:00ff", RestoreSeq: 11}) == hookRunKey(hookEnv{HostRun: "run:00ee", RestoreSeq: 11}) {
		t.Error("two runs with the same restore line seq share a key: the key does not carry the run's ID")
	}
	if got := hookRunKey(hookEnv{RestoreID: "r-dent", RestoreSeq: 11, HostRun: "run:00ff"}); got != "restore:r-dent:seq:11" {
		t.Errorf("a run with both keys %q, want the restore intent's", got)
	}
	for name, env := range map[string]hookEnv{
		"a run with a restore line and no ID": {RestoreSeq: 11},
		"nothing":                             {},
	} {
		if got := hookRunKey(env); got != "" {
			t.Errorf("%s keys %q, want none: a run with no live-host ID must never skip", name, got)
		}
	}
}

// runAuthHook runs auth's restore hook (the first of the list) for env.
func (fx *rxFx) runAuthHook(env hookEnv) {
	fx.t.Helper()
	env.Journal = fx.comp
	hooks, err := restoreHookList(fx.stores())(env)
	if err != nil {
		fx.t.Fatal(err)
	}
	// auth's PostRestore is the first hook.
	if _, err := hooks[0](context.Background(), journal.RestoreContext{Kind: journal.KindExternal}); err != nil {
		fx.t.Fatalf("auth restore hook: %v", err)
	}
}

func (fx *rxFx) stageMarker(key string) {
	fx.t.Helper()
	fx.commit(func(b *storage.Batch) error {
		return fx.auth.StageRestoreEpochRun(b, auth.RestoreEpochRun{RunKey: key, Bumped: 1})
	})
}

// TestRestoreHook_AnArchivedMarkerNeverSkipsTheBump: each of the stored markers an
// archive could carry bumps the epoch of a restore it does not belong to, and the
// restore it does belong to still resumes without a second bump.
func TestRestoreHook_AnArchivedMarkerNeverSkipsTheBump(t *testing.T) {
	t.Run("an archive taken after restore R1, restored as R2 with the same restore line seq", func(t *testing.T) {
		fx := newRxFx(t, newRxKeys())
		dick := fx.user("dick-grayson")
		start := fx.userRow(dick).CredentialEpoch
		fx.runAuthHook(hookEnv{RestoreID: "r-one", RestoreSeq: 11})
		if got := fx.userRow(dick).CredentialEpoch; got != start+1 {
			t.Fatalf("restore R1 left epoch %d, want %d", got, start+1)
		}
		// The store the archive holds carries R1's marker; R2 is another restore
		// (another intent ID) whose restore line got the same seq.
		fx.runAuthHook(hookEnv{RestoreID: "r-two", RestoreSeq: 11})
		if got := fx.userRow(dick).CredentialEpoch; got != start+2 {
			t.Errorf("restore R2 left epoch %d, want %d: R1's marker skipped R2's bump", got, start+2)
		}
	})

	t.Run("a replaced or external store whose stored marker matches an old-style key", func(t *testing.T) {
		for name, env := range map[string]hookEnv{
			"seq":                {RestoreSeq: 11},
			"a state-dir run ID": {RestoreSeq: 11, HostRun: "run:00112233445566778899aabbccddeeff"},
			"neither":            {},
		} {
			fx := newRxFx(t, newRxKeys())
			dick := fx.user("dick-grayson")
			start := fx.userRow(dick).CredentialEpoch
			fx.stageMarker("seq:11")
			fx.runAuthHook(env)
			fx.stageMarker("run:2026-11-21T00:00:00Z-ab12")
			fx.runAuthHook(env)
			if got := fx.userRow(dick).CredentialEpoch; got != start+2 {
				t.Errorf("%s: epoch %d after two runs over matching stored markers, want %d: a run with no restore ID skipped", name, got, start+2)
			}
		}
	})

	t.Run("a marker keyed on the ID a store row held does not skip the bump of a run with its own ID", func(t *testing.T) {
		// A build before F4-08 B10m kept the run ID in c/backup/restore_run, so a
		// marker an archive carries can be keyed on that row's ID. This build's run
		// ID is the state directory's, minted by another run, so it never matches.
		fx := newRxFx(t, newRxKeys())
		dick := fx.user("dick-grayson")
		start := fx.userRow(dick).CredentialEpoch
		fx.stageMarker("run:0011223344556677")
		fx.runAuthHook(hookEnv{HostRun: "run:8899aabbccddeeff"})
		if got := fx.userRow(dick).CredentialEpoch; got != start+1 {
			t.Errorf("epoch %d, want %d: a marker keyed on a store row's run ID skipped the bump", got, start+1)
		}
	})

	t.Run("the same host run resumed after a kill past the commit", func(t *testing.T) {
		fx := newRxFx(t, newRxKeys())
		dick := fx.user("dick-grayson")
		start := fx.userRow(dick).CredentialEpoch
		env := hookEnv{HostRun: "run:8899aabbccddeeff", RestoreSeq: 11}
		fx.runAuthHook(env)
		fx.runAuthHook(env)
		if got := fx.userRow(dick).CredentialEpoch; got != start+1 {
			t.Errorf("a resumed run left epoch %d, want %d", got, start+1)
		}
	})

	t.Run("the same restore resumed after a kill past the commit", func(t *testing.T) {
		fx := newRxFx(t, newRxKeys())
		dick := fx.user("dick-grayson")
		start := fx.userRow(dick).CredentialEpoch
		env := hookEnv{RestoreID: "r-one", RestoreSeq: 11}
		fx.runAuthHook(env)
		fx.runAuthHook(env)
		if got := fx.userRow(dick).CredentialEpoch; got != start+1 {
			t.Errorf("a resumed restore left epoch %d, want %d", got, start+1)
		}
	})
}
