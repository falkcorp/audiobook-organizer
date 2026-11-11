// file: core/kernel/restore_reapply_test.go
// version: 1.0.0
// guid: 8d3f1a62-5e94-4b07-a1c8-7f2e9b0d4c36
// last-edited: 2026-11-11

package kernel

import (
	"errors"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
)

// TestReapply_FirstBootCreatesMarkers: a store with no journal and no location
// file is the same store; the boot starts the journal, writes served_boot and
// then backup.location.json, runs no hook and appends no line.
func TestReapply_FirstBootCreatesMarkers(t *testing.T) {
	fx := newRxFx(t, nil)
	res, hres := fx.boot(nil, nil)
	if res.Class != classSame || hres.Ran || res.Reapplied != 0 || res.Deferred {
		t.Fatalf("result = %+v, hooks ran %v", res, hres.Ran)
	}
	if got := fx.pair(); got.N != 1 || got.Nonce == "" {
		t.Errorf("served_boot = %+v, want n 1 and a nonce", got)
	}
	loc := fx.location()
	if loc.ServedBoot != fx.pair() || loc.Journal != fx.journalPath() || loc.BackupDir != fx.backup || loc.Store != fx.db.Admin().StoreIdentity() {
		t.Errorf("location = %+v", loc)
	}
	if got := fx.kinds(); len(got) != 0 {
		t.Errorf("a first boot appended lines: %v", got)
	}
	if len(fx.health()) != 0 {
		t.Errorf("health = %v, want none (the fixture journal sits on the test's own device)", fx.health())
	}
}

// TestReapply_LaggingHeadReappliesTailOnly: a crash after Append and before the
// revoking batch commits leaves a line the store never saw. The same store at
// the next boot re-applies it, in a batch that also writes the head, runs no
// hook, appends no restore line and bumps nothing.
func TestReapply_LaggingHeadReappliesTailOnly(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	robin := fx.user("tim-drake")
	fx.key("key-redrobin", robin, auth.KeyActive)
	fx.key("key-spoiler", robin, auth.KeyActive)
	epoch := fx.userRow(robin).CredentialEpoch
	fx.revokeKey("key-spoiler") // committed, head 1
	seq := fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")})
	if got := fx.head().Seq; got != 1 || seq != 2 {
		t.Fatalf("head %d, line %d: the crash did not leave the head behind", got, seq)
	}
	fx.restart()

	res, hres := fx.boot(nil, nil)
	if res.Class != classSame || res.Reapplied != 1 || hres.Ran || len(res.Owed) != 0 {
		t.Fatalf("result = %+v, hooks ran %v", res, hres.Ran)
	}
	if fx.keyStatus("key-redrobin") != auth.KeyRevoked {
		t.Errorf("the journaled revocation was not re-applied: %q", fx.keyStatus("key-redrobin"))
	}
	if got := fx.head().Seq; got != 2 {
		t.Errorf("head = %d after the re-apply, want 2", got)
	}
	if got := fx.userRow(robin).CredentialEpoch; got != epoch {
		t.Errorf("epoch %d, want %d: a tail re-apply bumps nothing", got, epoch)
	}
	if n := len(fx.restoreLines()); n != 0 {
		t.Errorf("a same-store boot appended %d restore line(s)", n)
	}
	// And it is idempotent: the next boot finds nothing to do.
	fx.restart()
	if res, _ := fx.boot(nil, nil); res.Reapplied != 0 {
		t.Errorf("second boot re-applied %d line(s)", res.Reapplied)
	}
}

// TestPostDAGReconcilerOrder: the function refuses to run
// before P4d step 1 attached the overrides and the keyring opened (it would
// read the wrong directory, or verify nothing).
func TestPostDAGReconcilerOrder(t *testing.T) {
	fx := newRxFx(t, nil)
	for name, mut := range map[string]func(*reapplyInput){
		"no overrides": func(in *reapplyInput) { in.Overrides = false },
		"no keyring":   func(in *reapplyInput) { in.Keys = nil },
	} {
		_, err := reapplyRevocations(fx.ctx, fx.input(mut))
		if !errors.Is(err, errReapplyNotReady) {
			t.Errorf("%s: err = %v, want errReapplyNotReady", name, err)
		}
	}
	if _, err := os.Stat(fx.journalPath()); !errors.Is(err, os.ErrNotExist) || fx.pair().N != 0 {
		t.Errorf("a refused re-apply wrote something: journal %v, served_boot %+v", err, fx.pair())
	}
}
