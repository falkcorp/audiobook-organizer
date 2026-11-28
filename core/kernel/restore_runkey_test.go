// file: core/kernel/restore_runkey_test.go
// version: 1.0.0
// guid: 3df002d8-99b5-4e9c-a63e-2bb11b85eab5
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// restoreAuditTargets are the target IDs of the store's core.restore.apply
// records, sorted: one per restore run that wrote its records.
func restoreAuditTargets(fx *rxFx) []string {
	fx.t.Helper()
	var ids []string
	for rec, err := range fx.aud.Query(fx.ctx, audit.Filter{ActionPrefix: "core.restore.apply"}) {
		if err != nil {
			fx.t.Fatal(err)
		}
		ids = append(ids, rec.Target.ID)
	}
	slices.Sort(ids)
	return ids
}

// stateRunID is the ID in <state dir>/restore_hook_run.json, "" when there is no
// such file: what the next boot of a run that was killed finds again.
func (fx *rxFx) stateRunID() string {
	fx.t.Helper()
	raw, err := os.ReadFile(filepath.Join(fx.state, journal.HookRunFile))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		fx.t.Fatal(err)
	}
	var rec struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		fx.t.Fatal(err)
	}
	return rec.ID
}

// legacyRunRow is the key a build before F4-08 B10m kept its run ID under in
// c/backup/ (the journal package's own copy of it is private on purpose: nothing
// in this build reads or writes it but the clearing).
const legacyRunRow = "restore_run"

// putLegacyRunRow plants the row an older build, or an archive taken by one, holds.
func (fx *rxFx) putLegacyRunRow(id string) {
	fx.t.Helper()
	ks := fx.bks()
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.In(ks).Set([]byte(legacyRunRow), []byte(`{"id":"`+id+`","at":"2026-11-12T01:00:00Z"}`)); err != nil {
		fx.t.Fatal(err)
	}
	if err := b.Commit(fx.ctx, storage.Sync); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *rxFx) hasLegacyRunRow() bool {
	fx.t.Helper()
	_, err := fx.bks().Get(fx.ctx, []byte(legacyRunRow))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		fx.t.Fatal(err)
	}
	return err == nil
}

// auditApplyHook is audit's restore hook (the third of the list) for env.
func (fx *rxFx) runAuditHook(env hookEnv) error {
	fx.t.Helper()
	env.Journal = fx.comp
	hooks, err := restoreHookList(fx.stores())(env)
	if err != nil {
		fx.t.Fatal(err)
	}
	_, err = hooks[2](context.Background(), journal.RestoreContext{Kind: journal.KindArchive})
	return err
}

// TestRestoreRunKey_TwoRestoresWithNoLineBothWriteTheirAuditRecords: a replaced
// store whose journal line could not be written runs the hooks with restore seq
// 0. Audit keyed its records on that seq, so a second such restore, from an
// archive taken after the first, found the first's records and wrote none of its
// own: the audit trail said one restore happened where two had. Each run now
// carries its own ID, from the state directory, and the second run's key is a new
// one, so its epoch bump is not skipped either.
func TestRestoreRunKey_TwoRestoresWithNoLineBothWriteTheirAuditRecords(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	owner := fx.user("barbara")
	fx.key("key-oracle", owner, auth.KeyActive)
	epoch := fx.userRow(owner).CredentialEpoch

	brokenKeyring(fx)
	res, hres := fx.boot(nil, nil)
	if res.RestoreLine != 0 || !hres.Ran {
		t.Fatalf("first run: restore line %d, hooks ran %v, want no line and a run", res.RestoreLine, hres.Ran)
	}
	firstRun := res.HostRun
	first := restoreAuditTargets(fx)
	if len(first) != 1 || first[0] != firstRun || !strings.HasPrefix(first[0], "run:") {
		t.Fatalf("after the first run the apply records are %v, want one keyed on the run ID %q", first, firstRun)
	}
	if id := fx.stateRunID(); id != "" {
		t.Errorf("the state directory still holds run ID %q after a finished run, want it removed", id)
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch+1 {
		t.Fatalf("after the first run the epoch is %d, want %d", got, epoch+1)
	}

	// An archive taken after that restore carries its audit records and its
	// credential-epoch marker; a later revocation puts the journal ahead of it, and
	// copying it back is a second replaced store, with the keyring still unable to
	// take the line.
	fx.keys.(*rxKeys).active = "d1"
	archive := fx.snapshot("archive-after-the-first-restore")
	fx.revokeKey("key-oracle")
	fx.restart()
	fx.swapIn(archive)
	brokenKeyring(fx)
	res, hres = fx.boot(nil, nil)
	if res.Class != classReplaced || res.RestoreLine != 0 || !hres.Ran {
		t.Fatalf("second run: class %q (%s), restore line %d, hooks ran %v, want a replaced store, no line and a run", res.Class, res.Why, res.RestoreLine, hres.Ran)
	}
	if res.HostRun == firstRun {
		t.Errorf("the second run reused the first run's ID %q", firstRun)
	}
	second := restoreAuditTargets(fx)
	if len(second) != 2 || second[0] == second[1] || !slices.Contains(second, first[0]) || !slices.Contains(second, res.HostRun) {
		t.Errorf("after the second run the apply records are %v, want the first's %v and one under %q", second, first, res.HostRun)
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch+2 {
		t.Errorf("after the second run the epoch is %d, want %d: the first run's marker, which the archive carries, skipped the second run's bump", got, epoch+2)
	}
	if id := fx.stateRunID(); id != "" {
		t.Errorf("the state directory still holds run ID %q after the second run, want it removed", id)
	}
}

// TestRestoreRunKey_AReRunOfTheSameRestoreWritesNothingTwice: the hooks of a
// restore with no journal line fail after audit has written its records and auth
// has bumped the epoch. The boot that retries them finds the same run ID, so
// neither is done a second time, and the ID goes once the run completes.
func TestRestoreRunKey_AReRunOfTheSameRestoreWritesNothingTwice(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	owner := fx.user("barbara")
	epoch := fx.userRow(owner).CredentialEpoch
	brokenKeyring(fx)
	boom := errors.New("the last hook failed")
	var runs []string
	failing := func(env hookEnv) ([]journal.RestoreHook, error) {
		runs = append(runs, env.HostRun)
		real, err := restoreHookList(fx.stores())(env)
		if err != nil {
			return nil, err
		}
		return append(real, func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }), nil
	}
	if _, _, err := fx.tryBoot(nil, nil, failing); !errors.Is(err, boom) {
		t.Fatalf("boot err = %v, want the hook's", err)
	}
	if len(runs) != 1 || !strings.HasPrefix(runs[0], "run:") {
		t.Fatalf("run IDs handed to the hooks = %q, want one run ID", runs)
	}
	if id := fx.stateRunID(); id != runs[0] {
		t.Fatalf("stored run ID = %q, want %q: a re-run could not find it", id, runs[0])
	}
	if fx.hasLegacyRunRow() {
		t.Error("the run ID was written to the store's c/backup/ keyspace, where an archive carries it")
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{runs[0]}) {
		t.Fatalf("apply records after the failed run = %v, want one under %q", got, runs[0])
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch+1 {
		t.Fatalf("epoch %d after the failed run, want %d", got, epoch+1)
	}

	fx.restart()
	brokenKeyring(fx) // the line still cannot be written
	res, hres := fx.boot(nil, func(*reapplyInput) {})
	if res.RestoreLine != 0 || !hres.Ran {
		t.Fatalf("re-run: restore line %d, hooks ran %v, want no line and a run", res.RestoreLine, hres.Ran)
	}
	if res.HostRun != runs[0] {
		t.Errorf("the re-run's ID = %q, want the failed run's %q", res.HostRun, runs[0])
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{runs[0]}) {
		t.Errorf("apply records after the re-run = %v, want still the one under %q", got, runs[0])
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch+1 {
		t.Errorf("epoch %d after the re-run, want %d: the same run bumped twice", got, epoch+1)
	}
	if id := fx.stateRunID(); id != "" {
		t.Errorf("stored run ID after the completed re-run = %q, want it removed", id)
	}
}

// TestRestoreRunKey_AReplacedRunWithALineKeysOnItsRunIDNotItsSeq: a replaced store
// whose restore line was journaled used to key audit on the line's seq, which a
// journal reset or a host loss makes the seq of another restore. It keys on the
// run ID of the state directory, which no archive carries; and the ID is the same
// on the re-run of that run (same line) and another one for the next.
func TestRestoreRunKey_AReplacedRunWithALineKeysOnItsRunIDNotItsSeq(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	boom := errors.New("the last hook failed")
	failing := func(env hookEnv) ([]journal.RestoreHook, error) {
		real, err := restoreHookList(fx.stores())(env)
		if err != nil {
			return nil, err
		}
		return append(real, func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }), nil
	}
	res, _, err := fx.tryBoot(nil, nil, failing)
	if !errors.Is(err, boom) {
		t.Fatalf("boot err = %v, want the hook's", err)
	}
	if res.RestoreLine == 0 || !strings.HasPrefix(res.HostRun, "run:") {
		t.Fatalf("restore line %d, run ID %q, want a line and a run ID", res.RestoreLine, res.HostRun)
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{res.HostRun}) {
		t.Fatalf("apply records = %v, want one under the run ID %q, not the line's seq %d", got, res.HostRun, res.RestoreLine)
	}

	fx.restart()
	again, hres := fx.boot(nil, nil)
	if !hres.Ran || again.RestoreLine != res.RestoreLine {
		t.Fatalf("resume: hooks ran %v, line %d, want the same line %d resumed", hres.Ran, again.RestoreLine, res.RestoreLine)
	}
	if again.HostRun != res.HostRun {
		t.Errorf("the resumed run's ID = %q, want %q: the same line is the same run", again.HostRun, res.HostRun)
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{res.HostRun}) {
		t.Errorf("apply records after the resume = %v, want still one under %q", got, res.HostRun)
	}
	if id := fx.stateRunID(); id != "" {
		t.Errorf("run ID %q left after the resumed run completed", id)
	}
}

// TestRestoreRunKey_AResumedReplacedRunAppendsNoSecondRestrictionLine: a replaced
// store's tail keeps its last admin, and the boot is killed right after the
// restriction's journal lines and commit (nothing the head waits for is done). The
// next boot re-applies the same line, keeps the admin again and restricts again:
// under the same run ID it finds its lines and appends none; under no ID, or one of
// its own, it would journal every line twice.
func TestRestoreRunKey_AResumedReplacedRunAppendsNoSecondRestrictionLine(t *testing.T) {
	s := newKaScene(t)
	fx := s.fx
	archive := fx.snapshot("archive")
	fx.revokeKey(s.keys[s.bruce]) // line 1, committed: the head the anchor records
	fx.journalOnly(s.removal)     // line 2: keeps bruce, the only admin
	fx.restart()
	fx.swapIn(archive) // head 0, below the anchor: a replaced store

	passwordLines := func() int {
		n := 0
		for _, l := range fx.chain().Lines {
			if l.Kind == journal.PasswordChanged && l.Subject.ID == s.bruce {
				n++
			}
		}
		return n
	}
	res, _, err := fx.tryBoot(nil, uqWith(uqTail(1), crashAtRestriction(1, true)), nil)
	if err == nil {
		t.Fatal("the boot survived the crash after the restriction")
	}
	if res.Class != classReplaced || res.RestoreLine == 0 || !strings.HasPrefix(res.HostRun, "run:") {
		t.Fatalf("class %q, restore line %d, run ID %q: want a replaced store with a line and a run ID", res.Class, res.RestoreLine, res.HostRun)
	}
	if got := passwordLines(); got != 1 {
		t.Fatalf("the killed boot journaled %d password.changed lines for bruce, want 1", got)
	}
	var detail string
	for _, l := range fx.chain().Lines {
		if l.Kind == journal.PasswordChanged && l.Subject.ID == s.bruce && len(l.Detail) > 0 {
			detail = l.Detail[len(l.Detail)-1]
		}
	}
	if detail != auth.RunKeyDetail+res.HostRun {
		t.Fatalf("the restriction's line carries %q, want %q: it is not keyed on the run", detail, auth.RunKeyDetail+res.HostRun)
	}

	fx.restart()
	again, hres := fx.boot(nil, uqTail(1))
	if !hres.Ran || again.HostRun != res.HostRun {
		t.Fatalf("resume: hooks ran %v, run ID %q, want a run under %q", hres.Ran, again.HostRun, res.HostRun)
	}
	if got := passwordLines(); got != 1 {
		t.Errorf("after the resume the journal holds %d password.changed lines for bruce, want still 1: the resumed run journaled its restriction again", got)
	}
	if !s.bruceRestricted() {
		t.Error("bruce is not restricted after the resumed run")
	}
}

// TestRestoreRunKey_ARestoreBootKeysOnItsIntentAndStoresNoRunID: a restore boot's
// key is its intent's ID with the line's seq, the audit records carry it, and no
// run ID is written to the state directory for it.
func TestRestoreRunKey_ARestoreBootKeysOnItsIntentAndStoresNoRunID(t *testing.T) {
	s := newRestoreScene(t, nil)
	res, hres := s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
	if res.RestoreLine == 0 || !hres.Ran {
		t.Fatalf("restore line %d, hooks ran %v", res.RestoreLine, hres.Ran)
	}
	if res.HostRun != "" {
		t.Errorf("a restore boot minted run ID %q, want none", res.HostRun)
	}
	got := restoreAuditTargets(s.rxFx)
	want := []string{"restore:restore-1:seq:" + strconv.FormatUint(res.RestoreLine, 10)}
	if !slices.Equal(got, want) {
		t.Errorf("apply records = %v, want %v", got, want)
	}
	if id := s.stateRunID(); id != "" {
		t.Errorf("a restore boot left run ID %q in the state directory", id)
	}
}

// TestRestoreRunKey_AuditKeysOnTheLiveHostKeyNotTheSeq: an archive taken after
// restore R1, restored as R2 whose restore line got the same seq (journal seqs start
// again after a reset or a host loss), is another restore. Audit keyed on the seq
// found R1's records in the archive and wrote none of R2's.
func TestRestoreRunKey_AuditKeysOnTheLiveHostKeyNotTheSeq(t *testing.T) {
	fx := newRxFx(t, newRxKeys())
	for _, env := range []hookEnv{
		{RestoreID: "r-one", RestoreSeq: 11},
		{RestoreID: "r-two", RestoreSeq: 11},
	} {
		if err := fx.runAuditHook(env); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"restore:r-one:seq:11", "restore:r-two:seq:11"}
	if got := restoreAuditTargets(fx); !slices.Equal(got, want) {
		t.Errorf("apply records = %v, want %v: R2's records were skipped for R1's", got, want)
	}
	// The same restore again (a re-run after a crash) adds none.
	if err := fx.runAuditHook(hookEnv{RestoreID: "r-two", RestoreSeq: 11}); err != nil {
		t.Fatal(err)
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, want) {
		t.Errorf("apply records after R2's re-run = %v, want %v", got, want)
	}
}

// TestRestoreRunKey_AuditRefusesARunWithNoLiveHostKey: with neither a restore ID
// nor a run ID, audit would fall back to the restore line's seq, which an archive
// can repeat, so the hook writes nothing and fails.
func TestRestoreRunKey_AuditRefusesARunWithNoLiveHostKey(t *testing.T) {
	fx := newRxFx(t, newRxKeys())
	for name, env := range map[string]hookEnv{
		"a line and nothing else": {RestoreSeq: 11},
		"nothing":                 {},
	} {
		if err := fx.runAuditHook(env); err == nil {
			t.Errorf("%s: audit's hook ran with no live-host key", name)
		}
	}
	if got := restoreAuditTargets(fx); len(got) != 0 {
		t.Errorf("apply records = %v, want none written without a key", got)
	}
}

// TestRestoreRunKey_AStaleRowInTheStoreIsIgnoredAndCleared: c/backup/restore_run is
// where an older build kept its run ID, and an archive taken by one carries it. A
// replaced run with that row in its store keys on its own state-directory ID, never
// on the row's, and the row is deleted so that no build can take it for a key.
func TestRestoreRunKey_AStaleRowInTheStoreIsIgnoredAndCleared(t *testing.T) {
	const stale = "run:00000000deadbeef"
	fx := replacedByCopy(t, "anchor")
	fx.putLegacyRunRow(stale)
	// The marker an older build's run left in the archive, keyed on that ID.
	fx.stageMarker(stale)
	owner := fx.user("barbara")
	epoch := fx.userRow(owner).CredentialEpoch

	brokenKeyring(fx)
	res, hres := fx.boot(nil, nil)
	if !hres.Ran || res.RestoreLine != 0 {
		t.Fatalf("hooks ran %v, restore line %d, want a run with no line", hres.Ran, res.RestoreLine)
	}
	if res.HostRun == stale || !strings.HasPrefix(res.HostRun, "run:") {
		t.Fatalf("the run's ID is %q: the store row's %q was honoured", res.HostRun, stale)
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{res.HostRun}) {
		t.Errorf("apply records = %v, want one under %q, not %q", got, res.HostRun, stale)
	}
	if got := fx.userRow(owner).CredentialEpoch; got != epoch+1 {
		t.Errorf("epoch %d, want %d: the marker keyed on the store row's ID skipped the bump", got, epoch+1)
	}
	if fx.hasLegacyRunRow() {
		t.Error("the stale row is still in c/backup/ after the run")
	}
}

// TestRestoreRunKey_ABootThatOwesNoRunClearsWhatARunLeft: a run ID file no run is
// using (a kill between the markers and the removal, or a build that did not know
// the file finishing the run) is removed by the boot that has nothing to run, so
// no later run can find it; so is a store row of an older build. A restore boot
// ignores a leftover file, and removes it with the end of its own run.
func TestRestoreRunKey_ABootThatOwesNoRunClearsWhatARunLeft(t *testing.T) {
	leftover := func(fx *rxFx) string {
		id, err := journal.EnsureHookRun(fx.state, "owed:7/ab", fx.clock.now())
		if err != nil {
			fx.t.Fatal(err)
		}
		fx.putLegacyRunRow("run:00000000deadbeef")
		return id
	}
	t.Run("a plain boot", func(t *testing.T) {
		fx := newRxFx(t, nil)
		fx.boot(nil, nil)
		id := leftover(fx)
		fx.restart()
		if _, hres := fx.boot(nil, nil); hres.Ran {
			t.Fatal("a plain boot ran the hooks")
		}
		if got := fx.stateRunID(); got != "" {
			t.Errorf("a plain boot left run ID %q (the leftover %q) in the state directory", got, id)
		}
		if fx.hasLegacyRunRow() {
			t.Error("a plain boot left the legacy row in the store")
		}
	})
	t.Run("a restore boot", func(t *testing.T) {
		s := newRestoreScene(t, nil)
		id := leftover(s.rxFx)
		res, hres := s.boot(archiveBoot("restore-1", "arch-1", s.previous, &rxSteps{}), nil)
		if !hres.Ran || res.HostRun != "" {
			t.Fatalf("hooks ran %v, run ID %q, want a run keyed on the intent", hres.Ran, res.HostRun)
		}
		for _, got := range restoreAuditTargets(s.rxFx) {
			if got == id {
				t.Errorf("the restore keyed its records on the leftover run ID %q", id)
			}
		}
		if got := s.stateRunID(); got != "" {
			t.Errorf("a restore boot left run ID %q in the state directory", got)
		}
		if s.hasLegacyRunRow() {
			t.Error("a restore boot left the legacy row in the store")
		}
	})
}

// TestRestoreRunKey_Rollback: what an older build, which looks for the run ID in
// the store (journal's c/backup/restore_run), sees and does around a crash of this
// one, and what this one does when it comes back.
//
// A run this build crashed in has no row in the store: the older build finds none,
// mints its own, and runs the hooks again. That is a run under another key, so it
// bumps again and narrows, and audit writes a second record set: duplicates in the
// trail, never a missing record. It finishes by clearing its own row and leaves
// restore_hook_run.json, which it does not know, where it is. This build, back,
// must never take that file for a new run's key.
func TestRestoreRunKey_Rollback(t *testing.T) {
	t.Run("this build crashes mid run, an older one resumes, this one comes back", func(t *testing.T) {
		fx := replacedByCopy(t, "anchor")
		owner := fx.user("barbara")
		fx.key("key-oracle", owner, auth.KeyActive)
		brokenKeyring(fx)
		boom := errors.New("the last hook failed")
		failing := func(env hookEnv) ([]journal.RestoreHook, error) {
			real, err := restoreHookList(fx.stores())(env)
			if err != nil {
				return nil, err
			}
			return append(real, func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }), nil
		}
		res, _, err := fx.tryBoot(nil, nil, failing)
		if !errors.Is(err, boom) {
			t.Fatalf("boot err = %v, want the hook's", err)
		}
		crashed := res.HostRun
		// What the older build looks for is not there, so it cannot take this run's
		// ID for its own and writes its own set of records.
		if fx.hasLegacyRunRow() {
			t.Fatal("this build left a c/backup/restore_run row an older build would resume under")
		}
		if got := fx.stateRunID(); got != crashed {
			t.Fatalf("state-directory run ID %q, want %q", got, crashed)
		}

		// The older build's run, as far as it concerns this one: its own row, its
		// own records under its key, the markers written at the end of its hooks
		// (which move the location file's content marker), its row cleared.
		const olderRun = "run:00000000a1b2c3d4"
		if err := fx.runAuditHook(hookEnv{HostRun: olderRun}); err != nil {
			t.Fatal(err)
		}
		fx.restart()
		loc := fx.location()
		fx.setLocation(func(l *journal.Location) {
			l.ServedBoot = journal.ServedBoot{N: loc.ServedBoot.N + 1, Nonce: "marker-of-the-older-build"}
		})
		if got := restoreAuditTargets(fx); !slices.Equal(got, sortedCopy(crashed, olderRun)) {
			t.Fatalf("after the older build's run the apply records are %v, want %v: a second set, never a missing one", got, sortedCopy(crashed, olderRun))
		}
		if got := fx.stateRunID(); got != crashed {
			t.Fatalf("the older build touched the file it does not know: %q", got)
		}

		// This build comes back to a store that is replaced again with no line: the
		// leftover file names a run that has finished, so the new run gets its own.
		fx.restart()
		brokenKeyring(fx)
		fx.setLocation(func(l *journal.Location) {
			l.ServedBoot = journal.ServedBoot{N: loc.ServedBoot.N + 2, Nonce: "marker-of-the-older-build-2"}
		})
		back, hres := fx.boot(nil, nil)
		if !hres.Ran || back.RestoreLine != 0 {
			t.Fatalf("hooks ran %v, restore line %d, want a run with no line", hres.Ran, back.RestoreLine)
		}
		if back.HostRun == crashed || back.HostRun == olderRun {
			t.Errorf("the returning build keyed its run on %q, an ID of a run that finished", back.HostRun)
		}
	})

	t.Run("an older build crashes mid run, this one resumes", func(t *testing.T) {
		// The older build's run left its row in the store and the markers unwritten.
		// This build is a replaced store with no line again: its ID is its own, the row
		// is not the key, and the row is gone once the run is.
		const olderRun = "run:00000000a1b2c3d4"
		fx := replacedByCopy(t, "anchor")
		fx.putLegacyRunRow(olderRun)
		brokenKeyring(fx)
		res, hres := fx.boot(nil, nil)
		if !hres.Ran || res.RestoreLine != 0 {
			t.Fatalf("hooks ran %v, restore line %d, want a run with no line", hres.Ran, res.RestoreLine)
		}
		if res.HostRun == olderRun {
			t.Errorf("the run is keyed on the older build's row %q", olderRun)
		}
		if fx.hasLegacyRunRow() {
			t.Error("the older build's row survived the run")
		}
	})
}

func sortedCopy(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
