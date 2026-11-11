// file: core/kernel/restore_broken_test.go
// version: 1.0.0
// guid: 9a4d6c28-3f71-4e05-b8d2-c15e7a0f3b49
// last-edited: 2026-11-11

package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
)

// brokenScene is gotham with one more journaled revocation the store never
// committed (line 2, key-redrobin), the journal closed: the shape in which a
// broken chain hides a revocation the store lacks.
func brokenScene(t *testing.T) *gotham {
	t.Helper()
	g := newGotham(t)
	uncommitted(g)
	g.restart()
	return g
}

func (fx *rxFx) latched() string {
	fx.t.Helper()
	fp, err := journal.ReadBrokenLatch(fx.ctx, fx.bks())
	if err != nil {
		fx.t.Fatal(err)
	}
	return fp
}

// TestBootReapply_ChainBreakFailsClosed: a broken chain with the store's head
// below the journal's end sets every key needs_confirmation and bumps every
// epoch, re-applies nothing from it, and reports revocation_journal_broken;
// with the head at the end it only reports. The fail-closed happens once per
// broken state, not at every restart.
func TestBootReapply_ChainBreakFailsClosed(t *testing.T) {
	t.Run("an edited line below the journal's end", func(t *testing.T) {
		g := brokenScene(t)
		epoch := g.userRow(g.robin).CredentialEpoch
		// The edit aims the forged line at another key: it must not be applied.
		editJournalLine(t, g.journalPath(), 2, "key-redrobin", "key-batmobile")

		res, _ := g.boot(nil, nil)
		if !res.FailedClosed || res.Reapplied != 0 || res.Broken == nil || res.Counts[auth.CountEpochsBumped] == 0 {
			t.Fatalf("failed closed %v, re-applied %d, broken %v, counts %v", res.FailedClosed, res.Reapplied, res.Broken, res.Counts)
		}
		for _, id := range []string{"key-redrobin", "key-batmobile"} {
			if got := g.keyStatus(id); got != auth.KeyNeedsConfirmation {
				t.Errorf("%s = %q, want needs_confirmation (and never revoked by a line that does not verify)", id, got)
			}
		}
		if g.keyStatus("key-spoiler") != auth.KeyRevoked {
			t.Errorf("key-spoiler = %q: a revoked key stays revoked", g.keyStatus("key-spoiler"))
		}
		bumped := g.userRow(g.robin).CredentialEpoch
		if bumped <= epoch || !g.hasHealth(journal.HealthBroken) || g.latched() == "" {
			t.Errorf("epoch %d -> %d, broken health %v, latch %q", epoch, bumped, g.hasHealth(journal.HealthBroken), g.latched())
		}

		// The same broken state fails closed once.
		g.restart()
		res, _ = g.boot(nil, nil)
		if res.FailedClosed || g.userRow(g.robin).CredentialEpoch != bumped || !g.hasHealth(journal.HealthBroken) {
			t.Errorf("second boot: failed closed %v, epoch %d (was %d), broken health %v", res.FailedClosed, g.userRow(g.robin).CredentialEpoch, bumped, g.hasHealth(journal.HealthBroken))
		}
		// A different broken state is a new failure, and fails closed again.
		f, err := os.OpenFile(g.journalPath(), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("{\"seq\":3,\"kind\":\"user.disabled\"}\n")
		if err = errors.Join(err, f.Close()); err != nil {
			t.Fatal(err)
		}
		g.restart()
		res, _ = g.boot(nil, nil)
		if !res.FailedClosed || g.userRow(g.robin).CredentialEpoch <= bumped {
			t.Errorf("a new broken state: failed closed %v, epoch %d (was %d)", res.FailedClosed, g.userRow(g.robin).CredentialEpoch, bumped)
		}
	})

	t.Run("a deleted line", func(t *testing.T) {
		g := brokenScene(t)
		dropJournalLine(t, g.journalPath(), 1) // line 1, which the store committed
		res, _ := g.boot(nil, nil)
		if !res.FailedClosed || g.keyStatus("key-batmobile") != auth.KeyNeedsConfirmation {
			t.Errorf("failed closed %v, batmobile %q", res.FailedClosed, g.keyStatus("key-batmobile"))
		}
	})

	t.Run("the head at the end of the journal only reports", func(t *testing.T) {
		g := newGotham(t) // head 1, journal end 1
		g.restart()
		epoch := g.userRow(g.robin).CredentialEpoch
		editJournalLine(t, g.journalPath(), 1, "key-spoiler", "key-batmobile")
		res, _ := g.boot(nil, nil)
		if res.FailedClosed || res.Broken == nil || !g.hasHealth(journal.HealthBroken) {
			t.Errorf("failed closed %v, broken %v, broken health %v", res.FailedClosed, res.Broken, g.hasHealth(journal.HealthBroken))
		}
		if g.keyStatus("key-batmobile") != auth.KeyActive || g.userRow(g.robin).CredentialEpoch != epoch {
			t.Errorf("a store that holds everything the journal claims was restricted: batmobile %q, epoch %d (was %d)", g.keyStatus("key-batmobile"), g.userRow(g.robin).CredentialEpoch, epoch)
		}
	})

	t.Run("a journal truncated below the head reports and re-applies nothing", func(t *testing.T) {
		g := newGotham(t)
		g.restart()
		truncateJournal(t, g.journalPath(), 0)
		res, _ := g.boot(nil, nil)
		if res.Broken == nil || res.FailedClosed || !g.hasHealth(journal.HealthBroken) {
			t.Errorf("broken %v, failed closed %v, broken health %v: truncation must be reported", res.Broken, res.FailedClosed, g.hasHealth(journal.HealthBroken))
		}
	})

	t.Run("a truncated journal is reported even when it cannot be opened for appends", func(t *testing.T) {
		g := newGotham(t)
		g.restart()
		truncateJournal(t, g.journalPath(), 0)
		// Another holder of the journal's lock: Bind fails with the lock, not
		// with the truncation, so only the boot's own reading can name it.
		held, err := journal.OpenFile(g.ctx, journal.FileOptions{Path: g.journalPath(), Keyspace: g.bks(), Keys: g.keys, InstallID: g.install, App: rxApp})
		if held == nil || !errors.Is(err, journal.ErrCorrupt) {
			t.Fatalf("setup: OpenFile = %v, %v, want a broken File holding the lock", held, err)
		}
		defer held.Close()
		res, _ := g.boot(nil, nil)
		if res.Broken == nil || errors.Is(res.Broken, journal.ErrLocked) || res.FailedClosed {
			t.Errorf("broken %v, failed closed %v: want the truncation reported, not the lock", res.Broken, res.FailedClosed)
		}
		if !g.hasHealth(journal.HealthBroken) {
			t.Errorf("health = %v, want revocation_journal_broken", g.health())
		}
	})

	t.Run("a journal that starts above the store's head", func(t *testing.T) {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		robin := fx.user("tim-drake")
		fx.key("key-redrobin", robin, auth.KeyActive)
		fx.restart()
		if err := os.Remove(fx.journalPath()); err != nil {
			t.Fatal(err)
		}
		hdr := journal.Header{InstallID: fx.install, App: rxApp, StartedAt: fx.clock.now()}
		if err := journal.CreateBased(fx.journalPath(), fx.keys, hdr, journal.Head{Seq: 5, MAC: "ra's-al-ghul"}); err != nil {
			t.Fatal(err)
		}
		res, _ := fx.boot(nil, nil)
		if !res.FailedClosed || fx.keyStatus("key-redrobin") != auth.KeyNeedsConfirmation {
			t.Errorf("failed closed %v, redrobin %q: lines between the head and the base are gone", res.FailedClosed, fx.keyStatus("key-redrobin"))
		}
		if !fx.hasHealth(journal.HealthBroken) {
			t.Errorf("health = %v, want revocation_journal_broken: the journal opens fine, only the boot knows the lines are gone", fx.health())
		}
	})

	t.Run("a verified chain that is not the one the store committed", func(t *testing.T) {
		g := newGotham(t)
		g.restart()
		// Another timeline of this install: the archive's store (the same
		// install ID) journals a different revocation at seq 1, under the same
		// keyring. The chain verifies; it is not the line this store committed.
		if err := g.vfs.Rename(g.archive, "/fork"); err != nil {
			t.Fatal(err)
		}
		fork := g.openDB("/fork")
		forkKS, _ := fork.Keyspace(journal.Owner)
		forkPath := filepath.Join(t.TempDir(), "fork.journal")
		comp, err := journal.NewComponent(journal.ComponentOptions{StateDir: t.TempDir(), DBPath: "/fork", Keyspace: forkKS, Keys: g.keys,
			InstallID: fork.Admin().InstallID(), App: rxApp, Clock: g.clock.now, Tick: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if err := comp.Bind(g.ctx, forkPath); err != nil {
			t.Fatal(err)
		}
		if _, err := comp.AppendLine(g.ctx, journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-batmobile"), At: g.clock.now()}); err != nil {
			t.Fatal(err)
		}
		if err := comp.Close(g.ctx); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(forkPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(g.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		res, _ := g.boot(nil, nil)
		if !res.FailedClosed || res.Broken == nil || res.Reapplied != 0 {
			t.Fatalf("failed closed %v, broken %v, re-applied %d", res.FailedClosed, res.Broken, res.Reapplied)
		}
		if got := g.keyStatus("key-batmobile"); got != auth.KeyNeedsConfirmation {
			t.Errorf("key-batmobile = %q: a line of another timeline is never applied", got)
		}
		if !g.hasHealth(journal.HealthBroken) {
			t.Errorf("health = %v, want revocation_journal_broken", g.health())
		}
	})

	t.Run("a verified chain ahead of the store when the journal cannot be opened for appends", func(t *testing.T) {
		g := brokenScene(t)
		// Another holder of the journal's lock: Bind fails, the chain still
		// verifies, and the revocation the store lacks must still be applied.
		held, err := journal.OpenFile(g.ctx, journal.FileOptions{Path: g.journalPath(), Keyspace: g.bks(), Keys: g.keys, InstallID: g.install, App: rxApp})
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		res, _ := g.boot(nil, nil)
		if !errors.Is(res.Broken, journal.ErrLocked) || res.FailedClosed {
			t.Fatalf("broken %v, failed closed %v, want the lock failure and no fail-closed", res.Broken, res.FailedClosed)
		}
		if res.Reapplied != 1 || g.keyStatus("key-redrobin") != auth.KeyRevoked || g.head().Seq != 2 {
			t.Errorf("re-applied %d, redrobin %q, head %d: a bind failure must not skip the verified tail", res.Reapplied, g.keyStatus("key-redrobin"), g.head().Seq)
		}
		if !g.hasHealth(journal.HealthBroken) && !g.hasHealth(journal.HealthUnwritable) {
			t.Errorf("health = %v, want the journal reported unusable", g.health())
		}
	})

	t.Run("a journal that cannot be read is not an empty one", func(t *testing.T) {
		g := newGotham(t)
		g.restart()
		epoch := g.userRow(g.robin).CredentialEpoch
		if err := os.Remove(g.journalPath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(g.journalPath(), 0o700); err != nil { // open succeeds, read fails: not "does not exist"
			t.Fatal(err)
		}
		res, _ := g.boot(nil, nil)
		if !res.FailedClosed || res.Broken == nil || g.keyStatus("key-redrobin") != auth.KeyNeedsConfirmation || g.userRow(g.robin).CredentialEpoch <= epoch {
			t.Errorf("failed closed %v, broken %v, redrobin %q, epoch %d (was %d)", res.FailedClosed, res.Broken, g.keyStatus("key-redrobin"), g.userRow(g.robin).CredentialEpoch, epoch)
		}
		// Even a store with no head cannot assume an unreadable journal is empty.
		fx := newRxFx(t, nil)
		fx.user("selina-kyle")
		if err := os.Mkdir(fx.journalPath(), 0o700); err != nil {
			t.Fatal(err)
		}
		if res, _ := fx.boot(nil, nil); !res.FailedClosed {
			t.Errorf("a store at head 0 with an unreadable journal did not fail closed: %+v", res)
		}
	})

	t.Run("a restore boot leaves the fail-closed to its hooks", func(t *testing.T) {
		s := newRestoreScene(t, uncommitted)
		editJournalLine(t, s.journalPath(), 2, "key-redrobin", "key-batmobile")
		steps := &rxSteps{}
		res, hres := s.boot(archiveBoot("restore-1", "arch-1", s.previous, steps), nil)
		if res.FailedClosed || !hres.Ran || res.RestoreLine != 0 {
			t.Fatalf("failed closed %v, hooks ran %v, restore line %d: a broken journal takes no restore line but the hooks still run", res.FailedClosed, hres.Ran, res.RestoreLine)
		}
		if got := s.keyStatus("key-batmobile"); got != auth.KeyNeedsConfirmation {
			t.Errorf("key-batmobile = %q: no coverage means every key needs confirmation", got)
		}
		if got := steps.log(); got != "restore_line:0,hooks_done,marker_written,applied" {
			t.Errorf("edges = %q", got)
		}
	})
}

// TestJournalReapplyVerifiesWithKeyring: with the keyring the secrets store
// opened at P4b, a line is applied only when its MAC verifies under a derive
// key the keyring gives: the genuine tail is re-applied, an edited line is not.
func TestJournalReapplyVerifiesWithKeyring(t *testing.T) {
	build := func(t *testing.T) *rxFx {
		fx := newRxFxSecrets(t)
		fx.boot(nil, nil)
		robin := fx.user("tim-drake")
		fx.key("key-redrobin", robin, auth.KeyActive)
		fx.key("key-spoiler", robin, auth.KeyActive)
		fx.revokeKey("key-spoiler")
		fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")})
		fx.restart()
		return fx
	}
	t.Run("genuine", func(t *testing.T) {
		fx := build(t)
		res, _ := fx.boot(nil, nil)
		if res.Reapplied != 1 || res.Broken != nil || fx.keyStatus("key-redrobin") != auth.KeyRevoked {
			t.Errorf("re-applied %d, broken %v, redrobin %q", res.Reapplied, res.Broken, fx.keyStatus("key-redrobin"))
		}
	})
	t.Run("edited", func(t *testing.T) {
		fx := build(t)
		editJournalLine(t, fx.journalPath(), 2, "key-redrobin", "key-spoiler")
		res, _ := fx.boot(nil, nil)
		var be *journal.Break
		if !errors.As(res.Broken, &be) || be.Reason != journal.BreakMAC || res.Reapplied != 0 || !res.FailedClosed {
			t.Errorf("broken %v, re-applied %d, failed closed %v, want a mac break", res.Broken, res.Reapplied, res.FailedClosed)
		}
		if got := fx.keyStatus("key-redrobin"); got != auth.KeyNeedsConfirmation {
			t.Errorf("key-redrobin = %q", got)
		}
	})
}

// TestJournalOfOtherInstallNotReplayed: after a factory reset or a deleted DB
// directory the backup directory keeps the old install's journal, whose MACs
// still verify under the same keyring. It is set aside, a new journal starts
// with a gap line after the store's head, nothing is re-applied from it, and
// revocation_journal_reset is reported.
func TestJournalOfOtherInstallNotReplayed(t *testing.T) {
	// The other install's journal, written with this one's keyring.
	foreignWith := func(t *testing.T, keys journal.Keys, revoke bool) []byte {
		other := newRxFx(t, keys)
		other.boot(nil, nil)
		if revoke {
			other.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-batmobile")})
		}
		other.restart()
		raw, err := os.ReadFile(other.journalPath())
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	foreign := func(t *testing.T, g *gotham) []byte { return foreignWith(t, g.keys, true) }
	t.Run("set aside", func(t *testing.T) {
		g := newGotham(t)
		g.restart()
		raw := foreign(t, g)
		if err := os.WriteFile(g.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		epoch := g.userRow(g.robin).CredentialEpoch
		res, hres := g.boot(nil, nil)
		if !res.Reset || res.Reapplied != 0 || res.FailedClosed || hres.Ran {
			t.Fatalf("reset %v, re-applied %d, failed closed %v, hooks ran %v", res.Reset, res.Reapplied, res.FailedClosed, hres.Ran)
		}
		if res.HeadBefore.Seq != 1 {
			t.Errorf("head_before = %+v: it is read before the gap line is appended", res.HeadBefore)
		}
		if g.keyStatus("key-batmobile") != auth.KeyActive || g.userRow(g.robin).CredentialEpoch != epoch {
			t.Errorf("another install's line was applied: batmobile %q", g.keyStatus("key-batmobile"))
		}
		asides, _ := filepath.Glob(g.journalPath() + ".other-install-*")
		if len(asides) != 1 {
			t.Fatalf("set-aside files = %v", asides)
		}
		if got, _ := os.ReadFile(asides[0]); string(got) != string(raw) {
			t.Error("the set-aside file is not the other install's journal, byte for byte")
		}
		c := g.chain()
		if c.Break != nil || c.Header.InstallID != g.install || c.Header.BaseSeq != 1 || len(c.Lines) != 1 || c.Lines[0].Kind != journal.Gap || c.Lines[0].Seq != 2 {
			t.Errorf("new journal: break %v, header %+v, lines %d", c.Break, c.Header, len(c.Lines))
		} else if c.Lines[0].Detail[1] != journal.GapOtherInstall {
			t.Errorf("gap detail = %v", c.Lines[0].Detail)
		}
		if !g.hasHealth(journal.HealthReset) || g.head().Seq != 2 {
			t.Errorf("health %v, head %d", g.health(), g.head().Seq)
		}
		// The new journal takes revocations as usual.
		if seq := g.revokeKey("key-redrobin"); seq != 3 {
			t.Errorf("next revocation seq = %d, want 3", seq)
		}
	})

	t.Run("a restore boot gets no coverage from a reset journal", func(t *testing.T) {
		s := newRestoreScene(t, nil)
		raw := foreign(t, s.gotham)
		if err := os.WriteFile(s.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		res, hres := s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
		if !res.Reset || !hres.Ran || res.Reapplied != 0 {
			t.Fatalf("reset %v, hooks ran %v, re-applied %d", res.Reset, hres.Ran, res.Reapplied)
		}
		if got := s.keyStatus("key-batmobile"); got != auth.KeyNeedsConfirmation {
			t.Errorf("key-batmobile = %q: nothing covers this restore", got)
		}
	})

	t.Run("that cannot be set aside is never replayed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		g := newGotham(t)
		g.restart()
		raw := foreign(t, g)
		if err := os.WriteFile(g.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(g.backup, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(g.backup, 0o700) })
		res, _ := g.boot(nil, nil)
		if res.Reset || res.Reapplied != 0 || res.Broken == nil {
			t.Fatalf("reset %v, re-applied %d, broken %v", res.Reset, res.Reapplied, res.Broken)
		}
		if g.keyStatus("key-batmobile") != auth.KeyNeedsConfirmation || !res.FailedClosed {
			t.Errorf("batmobile %q, failed closed %v: the store's own journal is gone, so it fails closed", g.keyStatus("key-batmobile"), res.FailedClosed)
		}
		if got, _ := os.ReadFile(g.journalPath()); string(got) != string(raw) {
			t.Error("the other install's journal was modified")
		}
	})

	t.Run("that cannot be set aside and ends below the store's head still fails closed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		g := newGotham(t)
		g.restart()
		// A journal of another install with no line at the store's head: it
		// neither verifies the store's chain nor holds anything to compare.
		raw := foreignWith(t, g.keys, false)
		if err := os.WriteFile(g.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(g.backup, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(g.backup, 0o700) })
		res, _ := g.boot(nil, nil)
		if res.Reset || res.Broken == nil || !res.FailedClosed || g.keyStatus("key-batmobile") != auth.KeyNeedsConfirmation {
			t.Errorf("reset %v, broken %v, failed closed %v, batmobile %q: the store's own journal is gone",
				res.Reset, res.Broken, res.FailedClosed, g.keyStatus("key-batmobile"))
		}
	})

	t.Run("that cannot be set aside leaves a store that never journaled alone", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		fx := newRxFx(t, nil)
		robin := fx.user("tim-drake")
		fx.key("key-batmobile", robin, auth.KeyActive)
		// Its seqs are not this store's: a line at seq 1 says nothing about a
		// store whose head is 0, so the store is not behind it.
		raw := foreignWith(t, fx.keys, true)
		if err := os.WriteFile(fx.journalPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fx.backup, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(fx.backup, 0o700) })
		res, _ := fx.boot(nil, nil)
		if res.Reset || res.Reapplied != 0 || res.Broken == nil || res.FailedClosed {
			t.Errorf("reset %v, re-applied %d, broken %v, failed closed %v", res.Reset, res.Reapplied, res.Broken, res.FailedClosed)
		}
		if got := fx.keyStatus("key-batmobile"); got != auth.KeyActive {
			t.Errorf("key-batmobile = %q: another install's line is never applied, and nothing was lost", got)
		}
		if !fx.hasHealth(journal.HealthBroken) {
			t.Errorf("health = %v, want revocation_journal_broken: the journal cannot be written", fx.health())
		}
	})
}

// moveScene is gotham with its journal at the default directory and a second
// directory to move it to.
func moveScene(t *testing.T) (*gotham, string) {
	t.Helper()
	g := newGotham(t)
	g.restart()
	return g, filepath.Join(t.TempDir(), "offsite")
}

func (fx *rxFx) viaDir(dir string) func(*reapplyInput) {
	return func(in *reapplyInput) {
		in.BackupDir, in.JournalPath = dir, filepath.Join(dir, "gotham_revocations.journal")
	}
}

// TestJournalReapplyUsesOverriddenBackupDir: with core.backup.dir set only in
// the stored settings (the overrides P4d step 1 attached), step 2 finds the
// journal under the recorded directory, verifies the whole chain, moves it to
// the effective one, records that, and removes the old file; a move that fails
// verification keeps the old file and reports the journal broken.
func TestJournalReapplyUsesOverriddenBackupDir(t *testing.T) {
	t.Run("moved", func(t *testing.T) {
		g, offsite := moveScene(t)
		old := g.journalPath()
		before, err := os.ReadFile(old)
		if err != nil {
			t.Fatal(err)
		}
		res, _ := g.boot(nil, g.viaDir(offsite))
		dest := filepath.Join(offsite, "gotham_revocations.journal")
		if res.Path != dest || res.Moved != old || res.Broken != nil {
			t.Fatalf("path %q, moved %q, broken %v", res.Path, res.Moved, res.Broken)
		}
		got, err := os.ReadFile(dest)
		if err != nil || string(got) != string(before) {
			t.Errorf("the moved journal differs from the original (%v)", err)
		}
		if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the old journal was left behind: %v", err)
		}
		if loc := g.location(); loc.Journal != dest || loc.BackupDir != offsite {
			t.Errorf("location = %+v", loc)
		}
		// The chain continues in the new place.
		g.journalPath() // (the fixture's default is the old one: read the new)
		g.revokeKey("key-batmobile")
		if c, err := journal.ReadFile(dest, g.keys); err != nil || c.Break != nil || c.IntactEnd() != 2 {
			t.Errorf("the moved chain: %v, %+v", err, c)
		}
	})

	t.Run("a move that fails verification keeps the old file", func(t *testing.T) {
		g, offsite := moveScene(t)
		// The destination already holds another journal.
		if err := os.MkdirAll(offsite, 0o700); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(offsite, "gotham_revocations.journal")
		if err := os.WriteFile(dest, []byte("not a journal\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res, _ := g.boot(nil, g.viaDir(offsite))
		if res.Path != g.journalPath() || res.Moved != "" {
			t.Fatalf("path %q, moved %q: the old file must stay in use", res.Path, res.Moved)
		}
		if !g.hasHealth(journal.HealthBroken) || g.location().Journal != g.journalPath() {
			t.Errorf("health %v, location journal %q", g.health(), g.location().Journal)
		}
		if got, _ := os.ReadFile(dest); string(got) != "not a journal\n" {
			t.Error("the move overwrote the destination")
		}
	})
}

// TestJournalReapplyWithUISetBackupDir: the same with the value written to the
// stored settings layer as the Settings write path stores it (no route and no
// booted App): a directory set at runtime takes effect at the next boot, the
// journal moves once, a later boot with the same value moves nothing, and a
// restore boot never moves it (the restore line and its hook lines go where
// the journal is until the restore is applied).
func TestJournalReapplyWithUISetBackupDir(t *testing.T) {
	g, offsite := moveScene(t)
	other := filepath.Join(t.TempDir(), "nas")
	dest := filepath.Join(offsite, "gotham_revocations.journal")

	if res, _ := g.boot(nil, g.viaDir(offsite)); res.Path != dest || res.Moved == "" {
		t.Fatalf("first boot with the new directory: path %q, moved %q", res.Path, res.Moved)
	}
	g.restart()
	if res, _ := g.boot(nil, g.viaDir(offsite)); res.Path != dest || res.Moved != "" {
		t.Errorf("the next boot with the same value: path %q, moved %q, want no move", res.Path, res.Moved)
	}

	// A restore boot with yet another directory: the journal stays at the
	// recorded location, and the restore line lands there.
	g.restart()
	steps := &rxSteps{}
	prev := g.snapshot("previous")
	g.swapIn(g.archive)
	res, _ := g.boot(archiveBoot("restore-1", "arch-1", prev, steps), g.viaDir(other))
	if res.Path != dest || res.Moved != "" {
		t.Fatalf("a restore boot: path %q, moved %q, want the recorded location and no move", res.Path, res.Moved)
	}
	c, err := journal.ReadFile(dest, g.keys)
	if err != nil || c.Break != nil || !slicesContainsKind(c, journal.Restore) {
		t.Errorf("the restore line is not in the journal at the recorded location: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "gotham_revocations.journal")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a restore boot created a journal in the new directory: %v", err)
	}

	// The first boot after applied moves it.
	g.restart()
	if res, _ := g.boot(nil, g.viaDir(other)); res.Path != filepath.Join(other, "gotham_revocations.journal") || res.Moved != dest {
		t.Errorf("after applied: path %q, moved %q", res.Path, res.Moved)
	}
}

func slicesContainsKind(c *journal.Chain, k journal.Kind) bool {
	for _, l := range c.Lines {
		if l.Kind == k {
			return true
		}
	}
	return false
}
