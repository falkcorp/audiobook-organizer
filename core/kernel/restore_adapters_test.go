// file: core/kernel/restore_adapters_test.go
// version: 1.0.0
// guid: c37a5e82-1d96-4b40-a2f8-5e0b9c3d7a14
// last-edited: 2026-11-11

package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/falkcorp/mediacore/audit"
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
	for range 2 { // the second call finds the line already closed
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

// TestReapply_LastAdminKept: a role.unassigned line the store never saw would
// leave no active core:admin holder: rbac skips that line and reports the
// holder, and the kernel restricts that user through auth after the batch
// commits (SEC3-15, SEC4-12), so no lockout and no stale admin.
func TestReapply_LastAdminKept(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	const trustee rbac.RoleName = "custom.trustee"
	b := fx.roles.NewBatch()
	if err := fx.roles.PutRole(b, &rbac.Role{Name: trustee, Title: "Trustee", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:admin"}, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	bruce := fx.user("bruce-wayne", trustee)
	fx.key("key-batsignal", bruce, auth.KeyActive)
	fx.journalOnly(journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(bruce), Object: rxRoleRef(string(trustee))})
	fx.restart()

	res, _ := fx.boot(nil, nil)
	if res.Reapplied != 1 {
		t.Fatalf("re-applied %d", res.Reapplied)
	}
	u := fx.userRow(bruce)
	if !slices.Equal(u.Roles, []rbac.RoleName{trustee}) || !u.SignInRestricted {
		t.Errorf("roles %v, sign-in restricted %v: the last admin keeps the role and is restricted", u.Roles, u.SignInRestricted)
	}
	if got := fx.keyStatus("key-batsignal"); got != auth.KeyNeedsConfirmation {
		t.Errorf("key-batsignal = %q, want needs_confirmation (a restricted last admin's keys)", got)
	}
}

// TestReapply_TailInSequentialBatches: a tail longer than one batch is applied
// in order in several batches, each also writing the head, so the head never
// passes a line not yet applied.
func TestReapply_TailInSequentialBatches(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	robin := fx.user("tim-drake")
	n := tailBatch + 3
	for i := range n {
		id := fmt.Sprintf("key-robin-%03d", i)
		fx.key(id, robin, auth.KeyActive)
		fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef(id)})
	}
	fx.restart()
	batches := 0
	var heads []uint64
	res, _ := fx.boot(nil, func(in *reapplyInput) {
		inner := in.Apply.NewBatch
		in.Apply.NewBatch = func() *storage.Batch {
			batches++
			h, _ := journal.ReadHead(fx.ctx, fx.bks())
			heads = append(heads, h.Seq)
			return inner()
		}
	})
	if res.Reapplied != n || batches != 2 {
		t.Fatalf("re-applied %d of %d in %d batch(es), want 2 batches", res.Reapplied, n, batches)
	}
	if !slices.Equal(heads, []uint64{0, uint64(tailBatch)}) {
		t.Errorf("the head at the start of each batch = %v, want [0 %d]: it advances with each batch", heads, tailBatch)
	}
	if got := fx.head().Seq; got != uint64(n) {
		t.Errorf("head = %d, want %d", got, n)
	}
	for i := range n {
		if got := fx.keyStatus(fmt.Sprintf("key-robin-%03d", i)); got != auth.KeyRevoked {
			t.Fatalf("key %d = %q", i, got)
		}
	}
}

// TestClassifyStore: the classifier is a pure function of identity and
// content; every row of it, including the evidence rules for (c).
func TestClassifyStore(t *testing.T) {
	id := storage.StoreIdentity{Epoch: 3, DirFSID: 7, DirIno: 11}
	pair := journal.ServedBoot{N: 4, Nonce: "alfred"}
	loc := journal.Location{DBPath: "/gotham", InstallID: "wayne", Store: id, ServedBoot: pair, Head: journal.Head{Seq: 2}}
	line := func(seq uint64, k journal.Kind) journal.Line {
		return journal.Line{Seq: seq, Kind: k}
	}
	chain := func(lines ...journal.Line) *journal.Chain {
		c := &journal.Chain{HasHeader: true, Lines: lines}
		if n := len(lines); n > 0 {
			c.ClaimedEnd = lines[n-1].Seq
		}
		for _, l := range lines {
			if journal.IsRevocation(l.Kind) {
				c.LastRevocation = l.Seq
			}
		}
		return c
	}
	moved := id
	moved.Epoch++
	hook := line(3, journal.APIKeyRevoked)
	hook.RestoreSeq = 2
	base := classifyInput{Loc: loc, Describes: true, StorePair: pair, Head: journal.Head{Seq: 2}, Identity: id, Chain: chain(line(1, journal.APIKeyRevoked), line(2, journal.UserDisabled))}
	for _, c := range []struct {
		name string
		mut  func(*classifyInput)
		want storeClass
	}{
		{"same", func(*classifyInput) {}, classSame},
		{"restore boot ignores every signal", func(c *classifyInput) { c.RestoreBoot = true; c.StorePair.N = 1; c.Head.Seq = 0 }, classRestoreBoot},
		{"no record describes the store", func(c *classifyInput) { c.Describes = false; c.StorePair.N = 1 }, classSame},
		{"older n", func(c *classifyInput) { c.StorePair.N = 3 }, classReplaced},
		{"same n, another nonce", func(c *classifyInput) { c.StorePair.Nonce = "joker" }, classReplaced},
		{"no pair while the file has one", func(c *classifyInput) { c.StorePair = journal.ServedBoot{} }, classReplaced},
		{"pair ahead of the file", func(c *classifyInput) { c.StorePair.N = 9 }, classSame},
		{"a file with no pair", func(c *classifyInput) { c.Loc.ServedBoot = journal.ServedBoot{}; c.StorePair = journal.ServedBoot{} }, classSame},
		{"head below the anchor", func(c *classifyInput) { c.Head.Seq = 1 }, classReplaced},
		{"head above the anchor", func(c *classifyInput) { c.Head.Seq = 5 }, classSame},
		{"new generation, nothing behind", func(c *classifyInput) { c.Identity = moved }, classRelocated},
		{"new generation, head behind a revocation line", func(c *classifyInput) {
			c.Identity = moved
			c.Loc.Head.Seq = 0
			c.Head.Seq = 1
		}, classReplaced},
		{"new generation, a gap line after the head", func(c *classifyInput) {
			c.Identity = moved
			c.Chain = chain(line(1, journal.APIKeyRevoked), line(2, journal.Gap))
			c.Loc.Head.Seq = 1
			c.Head.Seq = 1
		}, classReplaced},
		{"new generation, a restore line after the head", func(c *classifyInput) {
			c.Identity = moved
			c.Chain = chain(line(1, journal.APIKeyRevoked), line(2, journal.Restore))
			c.Loc.Head.Seq = 1
			c.Head.Seq = 1
		}, classReplaced},
		{"new generation, an aborted restore line after the head", func(c *classifyInput) {
			c.Identity = moved
			c.Chain = chain(line(1, journal.APIKeyRevoked), line(2, journal.Restore), line(3, journal.RestoreAborted))
			c.Aborted = map[uint64]bool{2: true}
			c.Loc.Head.Seq = 1
			c.Head.Seq = 1
		}, classRelocated},
		{"new generation, only an aborted restore's hook line after the head", func(c *classifyInput) {
			c.Identity = moved
			c.Chain = chain(line(1, journal.APIKeyRevoked), line(2, journal.Restore), hook, line(4, journal.RestoreAborted))
			c.Aborted = map[uint64]bool{2: true}
			c.Loc.Head.Seq = 1
			c.Head.Seq = 1
		}, classRelocated},
		{"new generation, a store with no head and a journal with revocations", func(c *classifyInput) {
			c.Identity = moved
			c.Loc.Head.Seq = 0
			c.Head.Seq = 0
		}, classReplaced},
		{"an unreadable location file, a store that has journaled", func(c *classifyInput) { c.Describes, c.LocUnreadable = false, true }, classReplaced},
		{"an unreadable location file, a store that never has", func(c *classifyInput) {
			c.Describes, c.LocUnreadable, c.Head.Seq, c.StorePair = false, true, 0, journal.ServedBoot{}
		}, classSame},
	} {
		in := base
		c.mut(&in)
		if got, why := classifyStore(in); got != c.want {
			t.Errorf("%s: class %q (%s), want %q", c.name, got, why, c.want)
		}
	}
}

// TestRestoreHooks_ReceivePreSwapStoreAndRestoreSeq: the hooks run against the
// store the restore replaced, not only against the journal. A revocation the
// journal never saw (a store written outside auth's path) survives the restore
// because the pre-swap store held it ("the more restrictive of the restored and
// the pre-swap state, whatever the journal held"); an audit record the archive
// lacks is copied forward; and the audit records of the restore are filed under
// the restore line's seq, so a second restore never finds this one's.
func TestRestoreHooks_ReceivePreSwapStoreAndRestoreSeq(t *testing.T) {
	s := newRestoreScene(t, func(g *gotham) {
		k, err := g.auth.APIKeyByID(g.ctx, "key-batmobile")
		if err != nil {
			t.Fatal(err)
		}
		k.Status, k.RevokedAt, k.Version = auth.KeyRevoked, g.clock.now(), k.Version+1
		g.commit(func(b *storage.Batch) error { return g.auth.PutAPIKey(b, k) })
		if err := g.aud.Record(g.ctx, audit.Event{Action: "core.gotham.patrol", Outcome: audit.Success, Target: rxKeyRef("key-batmobile")}); err != nil {
			t.Fatal(err)
		}
	})
	res, hres := s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
	if !hres.Ran || res.RestoreLine == 0 {
		t.Fatalf("hooks ran %v, restore line %d", hres.Ran, res.RestoreLine)
	}
	if got := s.keyStatus("key-batmobile"); got != auth.KeyRevoked {
		t.Errorf("key-batmobile = %q: the pre-swap store revoked it and the journal never saw that", got)
	}
	find := func(prefix string) []audit.Record {
		var out []audit.Record
		for rec, err := range s.aud.Query(s.ctx, audit.Filter{ActionPrefix: prefix}) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, rec)
		}
		return out
	}
	if got := find("core.gotham.patrol"); len(got) != 1 {
		t.Errorf("the pre-swap store's audit record was copied forward %d times, want once", len(got))
	}
	applied := find("core.restore.apply")
	if len(applied) != 1 || applied[0].Target.ID != strconv.FormatUint(res.RestoreLine, 10) {
		t.Errorf("core.restore.apply records = %+v, want one for restore line %d", applied, res.RestoreLine)
	}
}
