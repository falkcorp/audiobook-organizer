// file: core/kernel/restore_runkey_test.go
// version: 1.0.0
// guid: 3df002d8-99b5-4e9c-a63e-2bb11b85eab5
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
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

// TestRestoreRunKey_TwoRestoresWithNoLineBothWriteTheirAuditRecords: a replaced
// store whose journal line could not be written runs the hooks with restore seq
// 0. Audit keyed its records on that seq, so a second such restore, from an
// archive taken after the first, found the first's records and wrote none of its
// own: the audit trail said one restore happened where two had. Each run now
// carries its own stored ID.
func TestRestoreRunKey_TwoRestoresWithNoLineBothWriteTheirAuditRecords(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	owner := fx.user("barbara")
	fx.key("key-oracle", owner, auth.KeyActive)

	brokenKeyring(fx)
	res, hres := fx.boot(nil, nil)
	if res.RestoreLine != 0 || !hres.Ran {
		t.Fatalf("first run: restore line %d, hooks ran %v, want no line and a run", res.RestoreLine, hres.Ran)
	}
	first := restoreAuditTargets(fx)
	if len(first) != 1 || !strings.HasPrefix(first[0], "run:") {
		t.Fatalf("after the first run the apply records are %v, want one keyed on a run ID", first)
	}
	if id, err := journal.HookRunID(fx.ctx, fx.bks()); err != nil || id != "" {
		t.Errorf("the run ID after a finished run = %q, %v, want it cleared", id, err)
	}

	// An archive taken after that restore carries its audit records; a later
	// revocation puts the journal ahead of it, and copying it back is a second
	// replaced store, with the keyring still unable to take the line.
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
	second := restoreAuditTargets(fx)
	if len(second) != 2 || second[0] == second[1] || !slices.Contains(second, first[0]) {
		t.Errorf("after the second run the apply records are %v, want the first's %v and one of its own", second, first)
	}
	if id, err := journal.HookRunID(fx.ctx, fx.bks()); err != nil || id != "" {
		t.Errorf("the run ID after the second run = %q, %v, want it cleared", id, err)
	}
}

// TestRestoreRunKey_AReRunOfTheSameRestoreWritesNothingTwice: the hooks of a
// restore with no journal line fail after audit has written its records. The
// boot that retries them finds the same run ID, so audit's records are not
// written a second time, and the ID goes once the run completes.
func TestRestoreRunKey_AReRunOfTheSameRestoreWritesNothingTwice(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	brokenKeyring(fx)
	boom := errors.New("the last hook failed")
	var keys []string
	failing := func(env hookEnv) ([]journal.RestoreHook, error) {
		keys = append(keys, env.RunKey)
		real, err := restoreHookList(fx.stores())(env)
		if err != nil {
			return nil, err
		}
		return append(real, func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }), nil
	}
	if _, _, err := fx.tryBoot(nil, nil, failing); !errors.Is(err, boom) {
		t.Fatalf("boot err = %v, want the hook's", err)
	}
	if len(keys) != 1 || keys[0] == "" {
		t.Fatalf("run keys handed to the hooks = %q, want one non-empty ID", keys)
	}
	if id, err := journal.HookRunID(fx.ctx, fx.bks()); err != nil || id != keys[0] {
		t.Fatalf("stored run ID = %q, %v, want %q: a re-run could not find it", id, err, keys[0])
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{keys[0]}) {
		t.Fatalf("apply records after the failed run = %v, want one under %q", got, keys[0])
	}

	fx.restart()
	brokenKeyring(fx) // the line still cannot be written
	res, hres := fx.boot(nil, func(*reapplyInput) {})
	if res.RestoreLine != 0 || !hres.Ran {
		t.Fatalf("re-run: restore line %d, hooks ran %v, want no line and a run", res.RestoreLine, hres.Ran)
	}
	if got := restoreAuditTargets(fx); !slices.Equal(got, []string{keys[0]}) {
		t.Errorf("apply records after the re-run = %v, want still the one under %q", got, keys[0])
	}
	if id, err := journal.HookRunID(fx.ctx, fx.bks()); err != nil || id != "" {
		t.Errorf("stored run ID after the completed re-run = %q, %v, want it cleared", id, err)
	}
}

// TestRestoreRunKey_ARestoreWithALineKeysOnItsSeq: a run that has its restore
// line keeps keying on the seq, as every earlier build did, and stores no run ID.
func TestRestoreRunKey_ARestoreWithALineKeysOnItsSeq(t *testing.T) {
	fx := replacedByCopy(t, "anchor")
	res, hres := fx.boot(nil, nil)
	if res.RestoreLine == 0 || !hres.Ran {
		t.Fatalf("restore line %d, hooks ran %v, want a line and a run", res.RestoreLine, hres.Ran)
	}
	got := restoreAuditTargets(fx)
	if len(got) != 1 || strings.HasPrefix(got[0], "run:") {
		t.Errorf("apply records = %v, want one keyed on the restore line's seq", got)
	}
	if id, err := journal.HookRunID(fx.ctx, fx.bks()); err != nil || id != "" {
		t.Errorf("a run with a line stored the run ID %q, %v", id, err)
	}
}
