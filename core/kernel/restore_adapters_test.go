// file: core/kernel/restore_adapters_test.go
// version: 1.0.0
// guid: c37a5e82-1d96-4b40-a2f8-5e0b9c3d7a14
// last-edited: 2026-11-11

package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// TestReapply_UnreadableLocationFile: backup.location.json holds the evidence
// that a store was replaced, so a file that exists and cannot be decoded is not
// "no marker": a store that has journaled before reads replaced (the hooks
// fail closed once and the file is rewritten); a store that never has reads
// the same.
func TestReapply_UnreadableLocationFile(t *testing.T) {
	g := newGotham(t)
	g.restart()
	if err := os.WriteFile(filepath.Join(g.state, journal.LocationFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, hres := g.boot(nil, nil)
	if res.Class != classReplaced || !hres.Ran || g.keyStatus("key-batmobile") != auth.KeyNeedsConfirmation {
		t.Fatalf("class %q (%s), hooks ran %v, batmobile %q", res.Class, res.Why, hres.Ran, g.keyStatus("key-batmobile"))
	}
	if loc := g.location(); loc.DBPath != g.dbPath || loc.ServedBoot != g.pair() {
		t.Errorf("the file was not rewritten: %+v", loc)
	}
	g.restart()
	if res, hres := g.boot(nil, nil); res.Class != classSame || hres.Ran {
		t.Errorf("after the rewrite: class %q, hooks ran %v", res.Class, hres.Ran)
	}

	fresh := newRxFx(t, nil)
	if err := os.WriteFile(filepath.Join(fresh.state, journal.LocationFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, hres := fresh.boot(nil, nil); res.Class != classSame || hres.Ran {
		t.Errorf("a store that never journaled: class %q, hooks ran %v", res.Class, hres.Ran)
	}
}

// TestRestoreAborted: step x's restore_aborted line names the restore line of
// this restore's journal ID, once; with no such line, or no journal, there is
// nothing to close; a journal that exists and cannot be read is an error the
// caller sees.
func TestRestoreAborted(t *testing.T) {
	s := newRestoreScene(t, nil)
	steps := &rxSteps{}
	res, _ := s.boot(archiveBoot("restore-1", "arch-1", s.previous, steps), nil)
	seq := res.RestoreLine
	for i := 0; i < 2; i++ { // the second call finds the line already closed
		if err := restoreAborted(s.ctx, s.comp, s.keys, "restore-1", s.clock.now()); err != nil {
			t.Fatal(err)
		}
	}
	var closed []uint64
	for _, l := range s.chain().Lines {
		if n, ok := l.AbortedSeq(); ok {
			closed = append(closed, n)
		}
	}
	if !slices.Equal(closed, []uint64{seq}) {
		t.Errorf("restore_aborted lines name %v, want exactly [%d]", closed, seq)
	}
	if err := restoreAborted(s.ctx, s.comp, s.keys, "another-restore", s.clock.now()); err != nil || len(s.chain().Lines) != int(seq)+1 {
		t.Errorf("an unknown restore ID: err %v, %d lines", err, len(s.chain().Lines))
	}

	unbound, err := journal.NewComponent(journal.ComponentOptions{StateDir: t.TempDir(), DBPath: "/x", Keyspace: s.bks(), Keys: s.keys, InstallID: s.install})
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreAborted(s.ctx, unbound, s.keys, "restore-1", s.clock.now()); err != nil {
		t.Errorf("a component with no journal bound: %v", err)
	}

	// A journal that exists and cannot be read.
	s.restart()
	if err := os.Remove(s.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.journalPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = s.comp.Bind(s.ctx, s.journalPath()) // fails, and leaves the component bound to the path
	if err := restoreAborted(s.ctx, s.comp, s.keys, "restore-1", s.clock.now()); err == nil {
		t.Error("an unreadable journal was reported as nothing to close")
	}
}

// TestReapply_RoleLinesThroughAuthUsers: a role.unassigned line the store never
// saw is re-applied through rbac, which stages the user's new role list into
// auth's record with the version it scanned; a user changed in between makes
// the batch fail with storage.ErrConflict.
func TestReapply_RoleLinesThroughAuthUsers(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	barbara := fx.user("barbara-gordon", "gotham.detective", "gotham.oracle")
	fx.journalOnly(journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(barbara), Object: rxRoleRef("gotham.oracle")})
	fx.restart()
	before := fx.userRow(barbara)

	res, _ := fx.boot(nil, nil)
	if res.Reapplied != 1 {
		t.Fatalf("re-applied %d", res.Reapplied)
	}
	got := fx.userRow(barbara)
	if !slices.Equal(got.Roles, []rbac.RoleName{"gotham.detective"}) || got.Version != before.Version+1 {
		t.Errorf("roles %v at version %d (was %d): the unassignment did not apply through auth's record", got.Roles, got.Version, before.Version)
	}

	// The version check: scan, change the user, stage, commit.
	users := &restoreUsers{store: fx.auth, now: fx.clock.now}
	for range users.ScanUsers(fx.ctx) {
	}
	fx.commit(func(b *storage.Batch) error {
		u := fx.userRow(barbara)
		u.DisplayName, u.Version = "Oracle", u.Version+1
		return fx.auth.PutUser(b, u)
	})
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	// auth's PutUser checks the version when it stages; a store that checked
	// at commit would report the same error there.
	err := users.StageRoles(fx.ctx, b, barbara, []rbac.RoleName{})
	if err == nil {
		err = b.Commit(fx.ctx, storage.Sync)
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Errorf("staging after the user changed: err = %v, want storage.ErrConflict", err)
	}
	if err := users.StageRoles(fx.ctx, fx.auth.NewBatch(), "u-nobody", nil); err == nil {
		t.Error("staging roles for a user that was never scanned succeeded")
	}
	ro := &restoreUsers{store: fx.auth, readOnly: true}
	if err := ro.StageRoles(fx.ctx, fx.auth.NewBatch(), barbara, nil); !errors.Is(err, storage.ErrReadOnly) {
		t.Errorf("a read-only users view staged: %v", err)
	}
}
