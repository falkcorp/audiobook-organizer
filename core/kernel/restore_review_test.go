// file: core/kernel/restore_review_test.go
// version: 1.3.0
// guid: 6b2e9d47-3a15-4c80-b7f4-0d9a5e1c8b36
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// These are the independent review's probes of the first F4-11 round, kept as
// tests. Each one failed on the commit that was reviewed: a revoked credential
// was live again after a boot that reported success. The numbering is the
// review's (BLOCKER 1-3, MAJOR 1-2).

// rxPasswordHash is a stored hash in a format the store accepts, for a user
// whose password a revocation line has to blank. It is no one's password.
var rxPasswordHash = "$2a$10$" + strings.Repeat("w", 53)

// disableUser is what auth does for a user disable: the row and the journal
// line in one batch, committed.
func (fx *rxFx) disableUser(id string) {
	fx.t.Helper()
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	u := *fx.userRow(id)
	u.Status, u.CredentialEpoch, u.Version = auth.StatusDisabled, u.CredentialEpoch+1, u.Version+1
	if err := fx.auth.PutUser(b, &u); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.comp.Append(fx.ctx, b, journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(id), Epoch: u.CredentialEpoch, At: fx.clock.now()}); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
}

// passwordUser is user with a stored password hash.
func (fx *rxFx) passwordUser(name string, roles ...rbac.RoleName) string {
	fx.t.Helper()
	id := fx.user(name, roles...)
	u := *fx.userRow(id)
	u.Version++
	fx.commit(func(b *storage.Batch) error { return fx.auth.SetPassword(b, &u, rxPasswordHash) })
	return id
}

// revokeKeyGapOnly is a revocation whose journal Append failed: the key is
// revoked in the store with its journal_gap/ record, and the journal holds no
// line for it.
func (fx *rxFx) revokeKeyGapOnly(id string) {
	fx.t.Helper()
	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	k, err := fx.auth.APIKeyByID(fx.ctx, id)
	if err != nil {
		fx.t.Fatal(err)
	}
	k.Status, k.Version = auth.KeyRevoked, k.Version+1
	if err := fx.auth.PutAPIKey(b, k); err != nil {
		fx.t.Fatal(err)
	}
	if err := fx.comp.Gap(b, fx.clock.now()); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
}

// forgeHeadMAC rewrites the store's committed journal_head with another mac:
// the store's head is then not the line the journal holds at that seq.
func (fx *rxFx) forgeHeadMAC(mac string) {
	fx.t.Helper()
	ks := fx.bks()
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := b.GetForUpdate(fx.ctx, ks, []byte("journal_head")); err != nil {
		fx.t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"seq": fx.head().Seq, "mac": mac})
	if err != nil {
		fx.t.Fatal(err)
	}
	if err := b.In(ks).Set([]byte("journal_head"), raw); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
}

// crashAtBatch makes the n-th batch the re-apply opens (1-based) fail the way a
// process death would end it: nothing of that batch or any later one commits.
func crashAtBatch(n int) func(*reapplyInput) {
	return func(in *reapplyInput) {
		inner, calls := in.Apply.NewBatch, 0
		in.Apply.NewBatch = func() *storage.Batch {
			calls++
			b := inner()
			if calls >= n {
				_ = b.Close() // a closed batch refuses every stage and commit
			}
			return b
		}
	}
}

// ---------------------------------------------------------------------------
// BLOCKER 1: auth's and rbac's revocations of one user.

// TestReapply_AuthAndRoleLinesOfOneUserBothTakeEffect: an offboarding is two
// journal lines for one user (auth's and rbac's). Staged into one batch, rbac's
// write of the user row (built from a snapshot read before auth's) replaced
// auth's, so the head moved past a disable, a delete or a password blank that
// never took effect. Each is applied in a batch of its own, rbac's reading what
// auth's committed.
func TestReapply_AuthAndRoleLinesOfOneUserBothTakeEffect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first journal.Kind
		check func(t *testing.T, fx *rxFx, id string)
	}{
		{"user.disabled", journal.UserDisabled, func(t *testing.T, fx *rxFx, id string) {
			if u := fx.userRow(id); u.Status != auth.StatusDisabled || u.CredentialEpoch != 1 || !slices.Equal(u.Roles, []rbac.RoleName{"viewer"}) {
				t.Errorf("user = status %q epoch %d roles %v, want disabled at epoch 1 with [viewer]", u.Status, u.CredentialEpoch, u.Roles)
			}
		}},
		{"user.deleted", journal.UserDeleted, func(t *testing.T, fx *rxFx, id string) {
			if u, err := fx.auth.GetUser(fx.ctx, auth.UserID(id)); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("GetUser = %+v, %v: the deleted user came back", u, err)
			}
		}},
		{"password.changed", journal.PasswordChanged, func(t *testing.T, fx *rxFx, id string) {
			if u := fx.userRow(id); u.HasPassword || !u.PasswordResetRequired || !slices.Equal(u.Roles, []rbac.RoleName{"viewer"}) {
				t.Errorf("user = password %v, reset required %v, roles %v, want a blanked password and [viewer]", u.HasPassword, u.PasswordResetRequired, u.Roles)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			id := fx.passwordUser("oracle", "operator", "viewer")
			// A restore to an older copy, or a crash after both fsyncs: the store saw neither line.
			fx.journalOnly(journal.Revocation{Kind: tc.first, Subject: rxUserRef(id), Epoch: 1})
			fx.journalOnly(journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(id), Object: rxRoleRef("operator")})
			fx.restart()
			res, _, err := fx.tryBoot(nil, nil, nil)
			if err != nil || res.Reapplied != 2 {
				t.Fatalf("boot: %v, re-applied %d, want 2", err, res.Reapplied)
			}
			tc.check(t, fx, id)
			if got := fx.head().Seq; got != 2 {
				t.Errorf("head = %d, want 2", got)
			}
		})
	}
}

// TestReapply_LastAdminGuardSeesTheDisableBeforeIt: the journal disabled the
// other admin and then took this one's admin role. Applied in journal order
// the second line would leave no active admin, so it is skipped and the user
// restricted; with both staged against one snapshot the guard saw the other
// admin still active and let it through.
func TestReapply_LastAdminGuardSeesTheDisableBeforeIt(t *testing.T) {
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
	selina := fx.user("selina-kyle", trustee)
	fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(selina), Epoch: 1})
	fx.journalOnly(journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(bruce), Object: rxRoleRef(string(trustee))})
	fx.restart()
	if res, _ := fx.boot(nil, nil); res.Reapplied != 2 {
		t.Fatalf("re-applied %d, want 2", res.Reapplied)
	}
	if u := fx.userRow(selina); u.Status != auth.StatusDisabled {
		t.Errorf("selina = %q, want disabled", u.Status)
	}
	if u := fx.userRow(bruce); !slices.Equal(u.Roles, []rbac.RoleName{trustee}) || !u.SignInRestricted {
		t.Errorf("bruce = roles %v, sign-in restricted %v: the last active admin keeps the role and is restricted", u.Roles, u.SignInRestricted)
	}
}

// TestReapply_TailAppliesOneChunkAfterAnother: with one line to a batch the
// lines of a chunk that are auth's and rbac's are still applied auth first,
// and the head moves with the last write of each chunk only.
func TestReapply_TailAppliesOneChunkAfterAnother(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	id := fx.passwordUser("oracle", "operator", "viewer")
	fx.journalOnly(journal.Revocation{Kind: journal.PasswordChanged, Subject: rxUserRef(id), Epoch: 1})
	fx.journalOnly(journal.Revocation{Kind: journal.RoleUnassigned, Subject: rxUserRef(id), Object: rxRoleRef("operator")})
	fx.restart()
	var heads []uint64
	res, _ := fx.boot(nil, func(in *reapplyInput) {
		in.TailBatch = 1
		inner := in.Apply.NewBatch
		in.Apply.NewBatch = func() *storage.Batch {
			heads = append(heads, fx.head().Seq)
			return inner()
		}
	})
	// chunk 1 (the password line): auth's batch, then rbac's with the head;
	// chunk 2 (the role line): the same.
	if res.Reapplied != 2 || !slices.Equal(heads, []uint64{0, 0, 1, 1}) {
		t.Errorf("re-applied %d, the head at the start of each batch = %v, want 2 and [0 0 1 1]", res.Reapplied, heads)
	}
}

// ---------------------------------------------------------------------------
// BLOCKER 2: the replaced verdict survives a crash.

// replacedByCopy builds the review's Probe B store: a revocation journaled
// after the archive, one that only has its gap record, and the archive copied
// back over the live store in place. evidence says which evidence of the
// replacement the boot has: "anchor" leaves the location file's committed head
// above the store's (b); "journal" lowers that anchor and records another
// store generation, so only the journal's line after the store's head says it
// (c).
func replacedByCopy(t *testing.T, evidence string) *rxFx {
	t.Helper()
	fx := newRxFx(t, nil)
	fx.boot(nil, nil) // served_boot 1
	owner := fx.user("alfred")
	fx.key("key-spoiler", owner, auth.KeyActive)
	fx.key("key-gap", owner, auth.KeyActive)
	archive := fx.snapshot("archive")
	fx.revokeKey("key-spoiler") // line 1, head 1
	fx.revokeKeyGapOnly("key-gap")
	fx.restart() // the anchor says head 1
	fx.swapIn(archive)
	if evidence == "journal" {
		fx.setLocation(func(l *journal.Location) {
			l.Head = journal.Head{}
			l.Store.Epoch++
		})
	}
	return fx
}

// TestReapply_ReplacedVerdictSurvivesACrash: a store copied over by an older
// one is detected from the store's head below the anchor, or from the journal's
// lines after the store's head. The re-apply moves the head to the journal's
// end, which erases both; so the verdict is journaled first (the external
// restore line, resumed by the next boot), and the head moves only with it. A
// crash at any point must leave the next boot reading a replaced store, whose
// hooks fail closed: the key whose revocation the journal never held is not
// active again.
func TestReapply_ReplacedVerdictSurvivesACrash(t *testing.T) {
	for _, evidence := range []string{"anchor", "journal"} {
		for crash := 0; crash <= 2; crash++ {
			t.Run(fmt.Sprintf("%s evidence, crash before batch %d", evidence, crash), func(t *testing.T) {
				fx := replacedByCopy(t, evidence)
				if crash > 0 {
					if _, _, err := fx.tryBoot(nil, func(in *reapplyInput) {
						in.TailBatch = 1
						crashAtBatch(crash)(in)
					}, nil); err == nil {
						t.Fatalf("the boot survived a crash before batch %d", crash)
					}
					fx.restart() // the process was killed and started again
				}
				res, hres := fx.boot(nil, nil)
				if res.Class != classReplaced || !hres.Ran {
					t.Fatalf("class %q (%s), hooks ran %v: the next boot did not read a replaced store", res.Class, res.Why, hres.Ran)
				}
				if got := fx.keyStatus("key-gap"); got == auth.KeyActive {
					t.Errorf("key-gap = %q: a revoked key is live again", got)
				}
				if got := fx.keyStatus("key-spoiler"); got != auth.KeyRevoked {
					t.Errorf("key-spoiler = %q, want revoked (the journal's line)", got)
				}
				if n := len(fx.restoreLines()); n != 1 {
					t.Errorf("%d restore line(s), want exactly one: a retried boot never duplicates it", n)
				}
			})
		}
	}
}

// TestReapply_RestoreLineBeforeTheTail: on a replaced store the external
// restore line is the first thing the boot writes and its head is what the last
// re-apply batch commits, so the head never passes the line and is never moved
// without it.
func TestReapply_RestoreLineBeforeTheTail(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	var sawLine bool
	var headAtStart uint64
	in := fx.input(func(in *reapplyInput) {
		inner := in.Apply.NewBatch
		in.Apply.NewBatch = func() *storage.Batch {
			if headAtStart == 0 {
				headAtStart = fx.head().Seq
			}
			sawLine = sawLine || len(fx.restoreLines()) == 1
			return inner()
		}
	})
	res, err := reapplyRevocations(fx.ctx, in) // step 2 alone: the hooks add lines of their own
	if err != nil {
		t.Fatal(err)
	}
	if !sawLine || headAtStart != 0 {
		t.Errorf("restore line present when the re-apply started: %v, head then %d: want true and 0", sawLine, headAtStart)
	}
	lines := fx.restoreLines()
	if len(lines) != 1 || res.RestoreLine != lines[0].Seq || fx.head().Seq != lines[0].Seq {
		t.Errorf("restore lines %v, result line %d, head %d: the head should be the restore line's", lines, res.RestoreLine, fx.head().Seq)
	}
}

// brokenKeyring makes the journal unable to seal a new line while every line it
// holds still verifies: the active derive key becomes one the keyring cannot
// give (the head of a rotation that lost its new key).
func brokenKeyring(fx *rxFx) {
	fx.t.Helper()
	fx.keys.(*rxKeys).active = "d2"
}

// TestReapply_ReplacedVerdictHeldWhenTheLineCannotBeWritten: when the journal
// cannot take the restore line, the head is held back until the hooks have run,
// so a boot whose hooks then fail still leaves a store the next boot reads as
// replaced (its hooks fail closed), and one whose hooks succeed commits it.
func TestReapply_ReplacedVerdictHeldWhenTheLineCannotBeWritten(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	brokenKeyring(fx)
	boom := errors.New("the auth hook failed")
	var lateAppend error
	res, _, err := fx.tryBoot(nil, nil, func(env hookEnv) ([]journal.RestoreHook, error) {
		return []journal.RestoreHook{func(ctx context.Context, _ journal.RestoreContext) (map[string]int, error) {
			// The keyring is back by the time a hook writes a line, so only the
			// boot's own refusal keeps that line's seq from becoming the head.
			fx.keys.(*rxKeys).active = "d1"
			_, lateAppend = fx.comp.AppendLine(ctx, journal.GapLine(fx.clock.now(), journal.GapAppendFailed))
			return nil, boom
		}}, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("boot err = %v, want the hook's", err)
	}
	if lateAppend == nil {
		t.Error("a line appended while the head is held was accepted: its seq would be staged as the head")
	}
	if res.RestoreLine != 0 || !res.Deferred || res.Held.Seq == 0 {
		t.Fatalf("result = line %d, deferred %v, held head %d: want no line, deferred, a held head", res.RestoreLine, res.Deferred, res.Held.Seq)
	}
	if got := fx.head().Seq; got != 0 {
		t.Fatalf("head = %d after the boot whose hooks failed, want 0: the verdict is not durable, so the head stays", got)
	}
	if fx.location().Head.Seq != 1 {
		t.Fatalf("the anchor moved: %+v", fx.location())
	}

	fx.restart()
	fx.keys.(*rxKeys).active = "d1" // the keyring is back
	res, hres := fx.boot(nil, nil)
	if res.Class != classReplaced || !hres.Ran {
		t.Fatalf("next boot: class %q (%s), hooks ran %v: the held verdict was lost", res.Class, res.Why, hres.Ran)
	}
	if got := fx.keyStatus("key-gap"); got == auth.KeyActive {
		t.Errorf("key-gap = %q: a revoked key is live again", got)
	}
}

// TestReapply_HeldHeadCommitsAfterTheHooks: a replaced store whose restore line
// could not be written and whose hooks succeed commits the held head, so the
// next boot is the same store.
func TestReapply_HeldHeadCommitsAfterTheHooks(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	brokenKeyring(fx)
	res, hres := fx.boot(nil, nil)
	if res.Class != classReplaced || !hres.Ran || res.RestoreLine != 0 {
		t.Fatalf("class %q, hooks ran %v, restore line %d", res.Class, hres.Ran, res.RestoreLine)
	}
	if got := fx.head().Seq; got != res.Held.Seq || got != 1 {
		t.Errorf("head = %d, held %d: the hooks completed, so the held head is committed", got, res.Held.Seq)
	}
	// The hold ends with the hooks: once the fault is over the journal takes
	// lines again and health stops reporting it unwritable.
	fx.keys.(*rxKeys).active = "d1"
	if _, err := fx.comp.AppendLine(fx.ctx, journal.GapLine(fx.clock.now(), journal.GapAppendFailed)); err != nil {
		t.Errorf("a line after the hooks completed and the head was committed: %v", err)
	}
	if fx.hasHealth(journal.HealthUnwritable) {
		t.Errorf("health %v still reports the journal unwritable after the hold ended", fx.health())
	}
	fx.restart()
	if res, hres := fx.boot(nil, nil); res.Class != classSame || hres.Ran {
		t.Errorf("next boot: class %q (%s), hooks ran %v, want the same store", res.Class, res.Why, hres.Ran)
	}
}

// ---------------------------------------------------------------------------
// BLOCKER 3: an untrusted journal.

// TestReapply_UntrustedChainStillAppliesItsVerifiedPrefix: a chain whose last
// line was edited still holds authentic lines before the edit. A user disabled
// by one of them is disabled again, whatever else the boot does about the line
// it cannot trust.
func TestReapply_UntrustedChainStillAppliesItsVerifiedPrefix(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	harvey := fx.user("harvey-dent")
	fx.key("key-two-face", harvey, auth.KeyActive)
	fx.key("key-coin", harvey, auth.KeyActive)
	archive := fx.snapshot("archive") // head 0, harvey active
	fx.disableUser(harvey)            // line 1
	fx.revokeKey("key-coin")          // line 2
	fx.restart()                      // anchor 2
	editJournalLine(t, fx.journalPath(), 2, "key-coin", "key-xxxx")
	fx.swapIn(archive)
	res, _ := fx.boot(nil, nil)
	if res.Broken == nil || !res.FailedClosed {
		t.Fatalf("broken %v, failed closed %v: the chain does not verify and the store lacks what it held", res.Broken, res.FailedClosed)
	}
	if res.Reapplied != 1 {
		t.Errorf("re-applied %d line(s), want 1 (the verified prefix)", res.Reapplied)
	}
	if got := fx.userRow(harvey).Status; got != auth.StatusDisabled {
		t.Errorf("harvey = %q: the verified user.disabled line was dropped with the broken one after it", got)
	}
}

// TestReapply_ForkedChainAppliesNothing: a journal that verifies but is not the
// one the store committed is not this store's history, so none of it is
// applied; the store fails closed instead.
func TestReapply_ForkedChainAppliesNothing(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner := fx.user("alfred")
	fx.key("key-one", owner, auth.KeyActive)
	fx.key("key-two", owner, auth.KeyActive)
	fx.revokeKey("key-one") // line 1, head 1
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-two")})
	fx.forgeHeadMAC(strings.Repeat("0", 64))
	fx.restart()
	res, _ := fx.boot(nil, nil)
	if res.Broken == nil || !res.FailedClosed || res.Reapplied != 0 {
		t.Fatalf("broken %v, failed closed %v, re-applied %d: want a fail-closed boot that applied nothing", res.Broken, res.FailedClosed, res.Reapplied)
	}
	if got := fx.keyStatus("key-two"); got == auth.KeyRevoked {
		t.Errorf("key-two = %q: a line of a journal that is not this store's was applied", got)
	}
}

// TestReapply_TruncatedBelowTheAnchorIsDetected: a journal cut at a line
// boundary verifies, and a store replaced by an older copy has a head below
// the cut, so nothing in the chain says lines are gone. The location file's
// committed head does: the boot reports the journal broken and fails closed.
func TestReapply_TruncatedBelowTheAnchorIsDetected(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	harvey := fx.user("harvey-dent")
	fx.key("key-coin", harvey, auth.KeyActive)
	archive := fx.snapshot("archive") // head 0
	fx.revokeKey("key-coin")          // line 1
	fx.disableUser(harvey)            // line 2
	epoch := fx.userRow(harvey).CredentialEpoch
	fx.restart() // anchor 2
	truncateJournal(t, fx.journalPath(), 1)
	fx.swapIn(archive)
	res, _ := fx.boot(nil, nil)
	if res.Broken == nil || !res.FailedClosed {
		t.Fatalf("broken %v, failed closed %v: the truncation was silent", res.Broken, res.FailedClosed)
	}
	if !fx.hasHealth(journal.HealthBroken) {
		t.Errorf("health %v lacks %s", fx.health(), journal.HealthBroken)
	}
	if got := fx.keyStatus("key-coin"); got != auth.KeyRevoked {
		t.Errorf("key-coin = %q, want revoked (line 1 survives the cut)", got)
	}
	if got := fx.userRow(harvey).CredentialEpoch; got <= epoch-1 {
		t.Errorf("harvey's epoch = %d: the fail-closed fallback bumps every credential epoch", got)
	}
}

// TestReapply_TruncationWithoutAnAnchorIsReported: with no location file to
// hold the anchor (deleted, or this is the first boot to write one), a journal
// that ends below the store's own head is still reported broken: the store has
// seen lines the journal no longer has. Nothing is left to re-apply, so the
// boot reports it and does not need to fail closed.
func TestReapply_TruncationWithoutAnAnchorIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock bool // another holder of the journal's lock: Bind fails with that, not the truncation
	}{
		{"journal opens for appends", false},
		{"journal locked by another holder", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			harvey := fx.user("harvey-dent")
			fx.key("key-coin", harvey, auth.KeyActive)
			fx.revokeKey("key-coin") // line 1
			fx.disableUser(harvey)   // line 2
			fx.restart()
			if err := os.Remove(filepath.Join(fx.state, journal.LocationFile)); err != nil {
				t.Fatal(err)
			}
			truncateJournal(t, fx.journalPath(), 1)
			if tc.lock {
				held, err := journal.OpenFile(fx.ctx, journal.FileOptions{Path: fx.journalPath(), Keyspace: fx.bks(), Keys: fx.keys, InstallID: fx.install, App: rxApp})
				if held == nil || err == nil {
					t.Fatalf("setup: OpenFile = %v, %v, want a broken File holding the lock", held, err)
				}
				defer func() { _ = held.Close() }()
			}
			res, _ := fx.boot(nil, nil)
			if res.Broken == nil || errors.Is(res.Broken, journal.ErrLocked) || !fx.hasHealth(journal.HealthBroken) {
				t.Errorf("broken %v, health %v: a journal below the store's head went unreported (or the lock was reported instead)", res.Broken, fx.health())
			}
			if res.Reapplied != 0 {
				t.Errorf("re-applied %d lines of a journal that holds nothing past the head", res.Reapplied)
			}
		})
	}
}

// TestCheckAnchor: the location file's committed head against the chain, for a
// file that describes the store.
func TestCheckAnchor(t *testing.T) {
	line := func(seq uint64, mac string) journal.Line { return journal.Line{Seq: seq, MAC: mac} }
	chain := func() *journal.Chain {
		return &journal.Chain{HasHeader: true, Header: journal.Header{MAC: "h"}, Lines: []journal.Line{line(1, "m1"), line(2, "m2")}, ClaimedEnd: 2}
	}
	in := reapplyInput{}
	loc := func(seq uint64, mac string) journal.Location {
		return journal.Location{Head: journal.Head{Seq: seq, MAC: mac}}
	}
	for _, tc := range []struct {
		name      string
		chain     *journal.Chain
		loc       journal.Location
		describes bool
		broken    bool
		claimed   uint64
	}{
		{"the anchor is on the chain", chain(), loc(2, "m2"), true, false, 2},
		{"the anchor is below the end", chain(), loc(1, "m1"), true, false, 2},
		{"no anchor yet", chain(), loc(0, ""), true, false, 2},
		{"another store's file", chain(), loc(9, "zz"), false, false, 2},
		{"the journal ends below the anchor", chain(), loc(3, "m3"), true, true, 3},
		{"the line at the anchor is another", chain(), loc(2, "other"), true, true, 2},
		{"an anchor with no mac is a seq check only", chain(), loc(2, ""), true, false, 2},
		{"an empty journal below the anchor", &journal.Chain{}, loc(1, "m1"), true, true, 1},
		{"the anchor is below the base", &journal.Chain{HasHeader: true, Header: journal.Header{BaseSeq: 5, BaseMAC: "b"}, ClaimedEnd: 5}, loc(3, "m3"), true, false, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := in.checkAnchor(tc.chain, tc.loc, tc.describes)
			if (got.Break != nil) != tc.broken || got.ClaimedEnd != tc.claimed {
				t.Errorf("break %v, claimed end %d; want broken %v, claimed end %d", got.Break, got.ClaimedEnd, tc.broken, tc.claimed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MAJOR 1: a planted symlink.

// TestReapply_SymlinkedJournalIsNotFollowed: the journal sits on a share other
// hosts write to. A symlink planted at its path must not make the boot open,
// cut or extend whatever it points at; the journal reads as unusable and the
// target is untouched.
func TestReapply_SymlinkedJournalIsNotFollowed(t *testing.T) {
	fx := newRxFx(t, nil)
	victim := filepath.Join(t.TempDir(), "victim.txt")
	const secret = "a-line-someone-else-owns-without-a-newline"
	if err := os.WriteFile(victim, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, fx.journalPath()); err != nil {
		t.Fatal(err)
	}
	res, _ := fx.boot(nil, nil)
	if got, err := os.ReadFile(victim); err != nil || string(got) != secret {
		t.Errorf("the target = %q, %v: the boot wrote through the symlink", got, err)
	}
	if res.Broken == nil || !fx.hasHealth(journal.HealthUnwritable) && !fx.hasHealth(journal.HealthBroken) {
		t.Errorf("broken %v, health %v: a symlinked journal is reported", res.Broken, fx.health())
	}
}

// TestReapply_SymlinkedForeignJournalIsNotRenamed: a journal path that is not a
// regular file is never set aside: the rename would take whatever the planted
// name points at out of the share's reach.
func TestReapply_SymlinkedForeignJournalIsNotRenamed(t *testing.T) {
	fx := newRxFx(t, nil)
	in := fx.input(nil)
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fx.journalPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := in.setAside(fx.ctx, fx.journalPath(), journal.Head{}); err == nil {
		t.Error("setAside renamed a symlink")
	}
	if fi, err := os.Lstat(fx.journalPath()); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the planted name = %v, %v: it should be untouched", fi, err)
	}
}

// TestReapply_EmptyLocationFileReadsAsReplaced: backup.location.json that
// decodes to nothing ({} or null) is not a record of this store and not an
// absent file either: a store with a journal head next to it reads as replaced,
// exactly as for a file that cannot be decoded.
func TestReapply_EmptyLocationFileReadsAsReplaced(t *testing.T) {
	for _, body := range []string{"{}", "null"} {
		t.Run(body, func(t *testing.T) {
			fx := newRxFx(t, nil)
			fx.boot(nil, nil)
			owner := fx.user("alfred")
			fx.key("key-spoiler", owner, auth.KeyActive)
			fx.revokeKey("key-spoiler")
			fx.restart()
			if err := os.WriteFile(filepath.Join(fx.state, journal.LocationFile), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			res, _ := fx.boot(nil, nil)
			if res.Class != classReplaced {
				t.Errorf("class %q (%s) with a location file of %s, want replaced", res.Class, res.Why, body)
			}
		})
	}
}

// TestReapply_RoleStoreRefusesASecondWriteInTheReapplyBatch: rbacstore derives
// a role's write from its committed bytes, so a second write to one role in the
// kernel's indexed re-apply batch would silently replace the first.
func TestReapply_RoleStoreRefusesASecondWriteInTheReapplyBatch(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	const trustee rbac.RoleName = "custom.trustee"
	seed := fx.roles.NewBatch()
	if err := fx.roles.PutRole(seed, &rbac.Role{Name: trustee, Title: "Trustee", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:admin"}, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(fx.ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	b := fx.auth.NewBatch() // the batch the re-apply stages into: it reads its own writes
	defer func() { _ = b.Close() }()
	if err := fx.roles.PutRole(b, &rbac.Role{Name: trustee, Title: "Trustee", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:admin"}, Version: 2}); err != nil {
		t.Fatalf("the first write: %v", err)
	}
	err := fx.roles.PutRole(b, &rbac.Role{Name: trustee, Title: "Warden", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:admin"}, Version: 2})
	if !errors.Is(err, rbacstore.ErrDoubleWrite) {
		t.Fatalf("the second write of one role in one batch: err = %v, want rbacstore.ErrDoubleWrite", err)
	}
}
