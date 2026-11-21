// file: core/kernel/restore_followups_test.go
// version: 0.1.0
// guid: ac3728d4-e1f0-4d66-aa2d-537c724ca602
// last-edited: 2026-11-20

package kernel

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
)

// The F4-11 follow-ups F4-08 owns, at P4d step 2 (TODO F4-11-F4-08-REFUSE and
// -QUEUE): another app's journal, the queue's health token, and the journal's
// causes going to the log rather than to health.

// componentAs replaces fx's journal component with one built for app, as a
// process restart of a build with another archive prefix would.
func (fx *rxFx) componentAs(app string) {
	fx.t.Helper()
	_ = fx.comp.Close(fx.ctx)
	comp, err := journal.NewComponent(journal.ComponentOptions{
		StateDir: fx.state, DBPath: fx.dbPath, Keyspace: fx.bks(), Keys: fx.keys, InstallID: fx.install, App: app,
		Clock: fx.clock.now, Tick: time.Hour, Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.comp = comp
	fx.t.Cleanup(func() { _ = comp.Close(fx.ctx) })
}

// TestReapply_AnotherAppsJournalIsReportedBrokenAndItsLinesStillApply: the boot of
// a component built for another app than the journal's header names. Bind refuses
// the journal (journal.ErrOtherApp) and nothing is written to it; the boot reports
// it broken. The install matches and the lines verify under the store's keyring,
// so they are this store's revocations: the tail is still re-applied, narrowing,
// and the boot does not fail closed (the store lacks nothing the journal holds).
// The cause, with the journal's path, is in the log; the health Detail names no
// path.
func TestReapply_AnotherAppsJournalIsReportedBrokenAndItsLinesStillApply(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner := fx.user("barbara")
	fx.key("key-oracle", owner, auth.KeyActive)
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-oracle")})
	fx.restart()
	fx.componentAs("bludhaven")
	before, err := os.ReadFile(fx.journalPath())
	if err != nil {
		t.Fatal(err)
	}
	epoch := fx.userRow(owner).CredentialEpoch

	var logs syncBuffer
	res, _ := fx.boot(nil, func(in *reapplyInput) { in.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	if !errors.Is(res.Broken, journal.ErrOtherApp) || !fx.hasHealth(journal.HealthBroken) || fx.hasHealth(journal.HealthUnwritable) {
		t.Errorf("broken %v, health %v: another app's journal is reported broken, not unwritable", res.Broken, fx.health())
	}
	if res.Reapplied != 1 || fx.keyStatus("key-oracle") != auth.KeyRevoked {
		t.Errorf("re-applied %d, key-oracle %q: the verified line of this install is still applied", res.Reapplied, fx.keyStatus("key-oracle"))
	}
	if res.FailedClosed || fx.userRow(owner).CredentialEpoch != epoch {
		t.Errorf("failed closed %v, epoch %d -> %d: the store lacks nothing the journal holds", res.FailedClosed, epoch, fx.userRow(owner).CredentialEpoch)
	}
	after, err := os.ReadFile(fx.journalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("another app's journal was written to by the boot")
	}
	if !strings.Contains(logs.String(), fx.journalPath()) || !strings.Contains(logs.String(), "another app") {
		t.Errorf("the log does not carry the cause with the journal's path:\n%s", logs.String())
	}
	for _, h := range fx.comp.Health() {
		if strings.Contains(h.Detail, fx.backup) || strings.Contains(h.Detail, string(filepath.Separator)) {
			t.Errorf("%s Detail %q names a path", h.Token, h.Detail)
		}
	}
}

// TestReapply_TheJournalsCauseGoesToTheLog: a chain that does not verify is
// recorded by the classifier's untrusted step, not by Bind; the cause and the
// journal's path reach the log there, and the health Detail is the fixed one.
func TestReapply_TheJournalsCauseGoesToTheLog(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	owner := fx.user("barbara")
	fx.key("key-spoiler", owner, auth.KeyActive)
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-spoiler")})
	fx.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-spoiler")})
	editJournalLine(t, fx.journalPath(), 2, "key-spoiler", "key-batmobile")
	fx.restart()

	var logs syncBuffer
	res, _ := fx.boot(nil, func(in *reapplyInput) { in.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	if res.Broken == nil || !fx.hasHealth(journal.HealthBroken) {
		t.Fatalf("broken %v, health %v: want a broken journal", res.Broken, fx.health())
	}
	var brokenLog string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "the revocation journal is broken\"") {
			brokenLog = l
		}
	}
	if !strings.Contains(brokenLog, fx.journalPath()) || !strings.Contains(brokenLog, "chain breaks") {
		t.Errorf("no log line records the break with the journal's path:\n%s", logs.String())
	}
	for _, h := range fx.comp.Health() {
		if strings.Contains(h.Detail, "chain breaks") || strings.Contains(h.Detail, string(filepath.Separator)) {
			t.Errorf("%s Detail %q carries the cause's text", h.Token, h.Detail)
		}
	}
}

// TestReapply_AWaitingQueueRaisesTheQueuedToken: revocation_journal_queued follows
// reapplyResult.Unapplied, not Queued. It is raised by the boot that queued a
// line, still raised by a later boot of the same build that queued nothing new
// (Queued 0, Unapplied 1), and cleared once a build that knows the kind drains
// the queue. It is info: the journal is neither broken
// nor unwritable for it.
func TestReapply_AWaitingQueueRaisesTheQueuedToken(t *testing.T) {
	fx := newRxFx(t, nil)
	fx.boot(nil, nil)
	if fx.hasHealth(journal.HealthQueued) {
		t.Fatalf("health %v with an empty queue", fx.health())
	}
	sel := fx.user("selina")
	fx.journalOnly(journal.Revocation{Kind: journal.UserDisabled, Subject: rxUserRef(sel), Epoch: 1})
	fx.restart()

	res, _ := fx.boot(nil, uqOldBuild)
	if res.Queued != 1 || res.Unapplied != 1 || !fx.hasHealth(journal.HealthQueued) {
		t.Errorf("queuing boot: queued %d, unapplied %d, health %v; want the queued token", res.Queued, res.Unapplied, fx.health())
	}
	if fx.hasHealth(journal.HealthBroken) || fx.hasHealth(journal.HealthUnwritable) {
		t.Errorf("health %v: a waiting queue is not a broken or unwritable journal", fx.health())
	}
	for _, h := range fx.comp.Health() {
		if h.Token == journal.HealthQueued && (h.Severity != journal.SeverityInfo || !strings.Contains(h.Detail, "1") || strings.Contains(h.Detail, string(journal.UserDisabled))) {
			t.Errorf("queued token %+v: want info with the count only", h)
		}
	}

	fx.restart()
	res, _ = fx.boot(nil, uqOldBuild)
	if res.Queued != 0 || res.Unapplied != 1 || !fx.hasHealth(journal.HealthQueued) {
		t.Errorf("second boot of the older build: queued %d, unapplied %d, health %v; want the token from Unapplied", res.Queued, res.Unapplied, fx.health())
	}

	fx.restart()
	res, _ = fx.boot(nil, nil)
	if res.Drained != 1 || res.Unapplied != 0 || fx.hasHealth(journal.HealthQueued) {
		t.Errorf("draining boot: drained %d, unapplied %d, health %v; want the token cleared", res.Drained, res.Unapplied, fx.health())
	}
}
