// file: core/kernel/restore_replaced_test.go
// version: 1.1.0
// guid: 1b7e4d93-a6c2-4f58-8e01-d93a5c7f2b64
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
)

// gotham is an install with some history: an archive of it taken at journal
// head 0 and a later revocation, so the live store and the archive differ.
type gotham struct {
	*rxFx
	robin    string // Tim Drake, the owner of the keys
	archive  string // snapshot at head 0, served_boot n 1
	archive2 string // a second copy of it, for a second restore
}

func newGotham(t *testing.T) *gotham {
	t.Helper()
	fx := newRxFx(t, nil)
	fx.boot(nil, nil) // served_boot 1
	g := &gotham{rxFx: fx, robin: fx.user("tim-drake")}
	fx.key("key-redrobin", g.robin, auth.KeyActive)
	fx.key("key-spoiler", g.robin, auth.KeyActive)
	fx.key("key-batmobile", g.robin, auth.KeyActive)
	g.archive, g.archive2 = fx.snapshot("archive"), fx.snapshot("archive2")
	fx.revokeKey("key-spoiler") // journal line 1, head 1
	fx.restart()
	fx.boot(nil, nil) // served_boot 2, anchor head 1
	return g
}

// externalLine is the one external restore line the journal holds.
func (fx *rxFx) externalLine() journal.Line {
	fx.t.Helper()
	var found []journal.Line
	for _, l := range fx.restoreLines() {
		if info, ok := l.RestoreInfo(); ok && info.Kind == journal.RestoreExternal {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		fx.t.Fatalf("the journal holds %d external restore lines, want 1: %v", len(found), fx.kinds())
	}
	return found[0]
}

// TestReplacedStoreRunsHooks: an older archive's DB directory copied into place
// is detected at boot by its content marker, or by its store generation with
// journal evidence, also when its head equals the journal's end: the tail is
// re-applied and the hooks run once with KindExternal and Revocations nil, so
// every key needs confirmation and every epoch moves; a store relocated, with
// no such evidence, runs none.
func TestReplacedStoreRunsHooks(t *testing.T) {
	t.Run("older content marker, head behind the journal", func(t *testing.T) {
		g := newGotham(t)
		epoch := g.userRow(g.robin).CredentialEpoch
		g.swapIn(g.archive)
		headBefore := g.head()

		res, hres := g.boot(nil, nil)
		if res.Class != classReplaced || !strings.Contains(res.Why, "served_boot") || !hres.Ran {
			t.Fatalf("class %q (%s), hooks ran %v", res.Class, res.Why, hres.Ran)
		}
		if headBefore.Seq != 0 || res.HeadBefore != headBefore {
			t.Errorf("head_before = %+v, want the archive's (none): it is read before any line is appended", res.HeadBefore)
		}
		if res.Reapplied != 1 || g.keyStatus("key-spoiler") != auth.KeyRevoked {
			t.Errorf("the tail was not re-applied: %d line(s), spoiler %q", res.Reapplied, g.keyStatus("key-spoiler"))
		}
		for _, id := range []string{"key-redrobin", "key-batmobile"} {
			if got := g.keyStatus(id); got != auth.KeyNeedsConfirmation {
				t.Errorf("%s = %q, want needs_confirmation (Revocations is nil for a replaced store)", id, got)
			}
		}
		if got := g.userRow(g.robin).CredentialEpoch; got <= epoch {
			t.Errorf("epoch %d, want it bumped above %d", got, epoch)
		}
		line := g.externalLine()
		info, _ := line.RestoreInfo()
		if info.Boundary != headBefore.Seq || info.Source != "" || info.RestoreID != "" || res.RestoreLine != line.Seq || res.RestoreBoundary != headBefore.Seq {
			t.Errorf("restore line = %+v, result line %d boundary %d", info, res.RestoreLine, res.RestoreBoundary)
		}
		if !g.hooked(line.Seq) {
			t.Error("restore_hooked was not recorded after the hooks")
		}
		if loc := g.location(); loc.ServedBoot != g.pair() || g.pair().N <= 2 {
			t.Errorf("markers: location %+v, store %+v: step 2 writes both for a replaced store", loc.ServedBoot, g.pair())
		}

		// Once: the next boot is the same store and runs nothing.
		g.restart()
		res, hres = g.boot(nil, nil)
		if res.Class != classSame || hres.Ran || len(res.Owed) != 0 || len(g.restoreLines()) != 1 {
			t.Errorf("second boot: class %q, hooks ran %v, owed %d, %d restore line(s)", res.Class, hres.Ran, len(res.Owed), len(g.restoreLines()))
		}
	})

	t.Run("older content marker, head equal to the journal's end", func(t *testing.T) {
		g := newGotham(t)
		late := g.snapshot("late") // after the revocation: head 1 == the journal's end
		g.restart()
		g.boot(nil, nil) // the pair moves past the snapshot's
		g.swapIn(late)
		if g.head().Seq != 1 || g.chain().IntactEnd() != 1 {
			t.Fatalf("head %d, journal end %d, want both 1", g.head().Seq, g.chain().IntactEnd())
		}
		res, hres := g.boot(nil, nil)
		if res.Class != classReplaced || !hres.Ran || res.Reapplied != 0 {
			t.Fatalf("class %q (%s), hooks ran %v, re-applied %d", res.Class, res.Why, hres.Ran, res.Reapplied)
		}
		if got := g.keyStatus("key-redrobin"); got != auth.KeyNeedsConfirmation {
			t.Errorf("key-redrobin = %q, want needs_confirmation", got)
		}
	})

	t.Run("another generation with journal evidence", func(t *testing.T) {
		g := newGotham(t)
		g.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")}) // never reached the store
		g.restart()
		g.setLocation(func(l *journal.Location) { l.Store.Epoch += 3 })
		res, hres := g.boot(nil, nil)
		if res.Class != classReplaced || !strings.Contains(res.Why, "identity") || !hres.Ran {
			t.Fatalf("class %q (%s), hooks ran %v", res.Class, res.Why, hres.Ran)
		}
		if g.keyStatus("key-redrobin") != auth.KeyRevoked || g.keyStatus("key-batmobile") != auth.KeyNeedsConfirmation {
			t.Errorf("redrobin %q, batmobile %q", g.keyStatus("key-redrobin"), g.keyStatus("key-batmobile"))
		}
		g.externalLine()
	})

	t.Run("another generation without evidence is relocated", func(t *testing.T) {
		g := newGotham(t)
		g.setLocation(func(l *journal.Location) { l.Store.Epoch += 3; l.Store.DirIno = 99 })
		before := len(g.kinds())
		res, hres := g.boot(nil, nil)
		if res.Class != classRelocated || hres.Ran || len(res.Owed) != 0 {
			t.Fatalf("class %q (%s), hooks ran %v", res.Class, res.Why, hres.Ran)
		}
		if got := g.location().Store; got != g.db.Admin().StoreIdentity() {
			t.Errorf("recorded identity %+v, want the new one %+v", got, g.db.Admin().StoreIdentity())
		}
		if g.keyStatus("key-redrobin") != auth.KeyActive || len(g.kinds()) != before {
			t.Errorf("a relocated store was restricted or got lines: redrobin %q, lines %d -> %d", g.keyStatus("key-redrobin"), before, len(g.kinds()))
		}
		if g.pair().N != 3 {
			t.Errorf("served_boot n = %d, want 3: a relocated boot still advances the pair", g.pair().N)
		}
	})

	t.Run("the same store, whatever its head", func(t *testing.T) {
		g := newGotham(t)
		res, hres := g.boot(nil, nil)
		if res.Class != classSame || hres.Ran || g.keyStatus("key-redrobin") != auth.KeyActive {
			t.Errorf("class %q (%s), hooks ran %v", res.Class, res.Why, hres.Ran)
		}
	})

	t.Run("a location file for another store describes nothing", func(t *testing.T) {
		g := newGotham(t)
		g.setLocation(func(l *journal.Location) {
			l.DBPath, l.Journal = "/gotham/other.pebble", "/elsewhere/gotham_revocations.journal"
			l.ServedBoot.N += 9
			l.Head.Seq += 9
		})
		res, hres := g.boot(nil, nil)
		if res.Class != classSame || hres.Ran || res.Moved != "" || res.Path != g.journalPath() {
			t.Errorf("class %q (%s), hooks ran %v: another store's marker is no evidence", res.Class, res.Why, hres.Ran)
		}
		if loc := g.location(); loc.DBPath != g.dbPath {
			t.Errorf("the file was not rewritten for this store: %+v", loc)
		}
	})
}

// TestReplacedStore_RunsPostRestoreHook: the hook a replaced store runs gets
// KindExternal, no previous store, no coverage and the boundary head_before.
func TestReplacedStore_RunsPostRestoreHook(t *testing.T) {
	g := newGotham(t)
	g.swapIn(g.archive)
	var got []journal.RestoreContext
	real := restoreHookList(g.stores())
	spy := func(env hookEnv) ([]journal.RestoreHook, error) {
		hooks, err := real(env)
		for i := range hooks {
			inner := hooks[i]
			hooks[i] = func(ctx context.Context, rc journal.RestoreContext) (map[string]int, error) {
				got = append(got, rc)
				return inner(ctx, rc)
			}
		}
		return hooks, err
	}
	res, _, err := g.tryBoot(nil, nil, spy)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d hooks ran, want auth, rbac and audit", len(got))
	}
	for i, rc := range got {
		if rc.Kind != journal.KindExternal || rc.Revocations != nil || rc.Previous != nil || rc.Foreign || rc.Boundary != res.HeadBefore.Seq || rc.AuditHighWater != 0 {
			t.Errorf("hook %d context = %+v", i, rc)
		}
	}
	if got[1].Counts[auth.CountEpochsBumped] == 0 && got[2].Counts[auth.CountEpochsBumped] == 0 {
		t.Errorf("auth's counts did not reach the later hooks: %v / %v", got[1].Counts, got[2].Counts)
	}
}

// TestReplacedStore_TruncatedJournalKeysNeedConfirmation: a store copied back
// to an older generation whose journal was truncated after the archive, and
// verifies cleanly, still has every key set to needs_confirmation, and the boot
// reports the journal broken: the store that held the head is gone, but
// backup.location.json's committed head still says the journal had more.
func TestReplacedStore_TruncatedJournalKeysNeedConfirmation(t *testing.T) {
	g := newGotham(t)
	// Cut the revocation line off the journal: header only, a clean chain.
	g.restart()
	truncateJournal(t, g.journalPath(), 0)
	g.swapIn(g.archive)
	res, hres := g.boot(nil, nil)
	if res.Class != classReplaced || !hres.Ran || res.Broken == nil || res.Reapplied != 0 {
		t.Fatalf("class %q (%s), hooks ran %v, broken %v, re-applied %d: want a replaced store and a journal reported below the anchor", res.Class, res.Why, hres.Ran, res.Broken, res.Reapplied)
	}
	if !g.hasHealth(journal.HealthBroken) {
		t.Errorf("health %v lacks %s", g.health(), journal.HealthBroken)
	}
	for _, id := range []string{"key-redrobin", "key-batmobile", "key-spoiler"} {
		if got := g.keyStatus(id); got != auth.KeyNeedsConfirmation {
			t.Errorf("%s = %q, want needs_confirmation (the journal that said spoiler was revoked is gone)", id, got)
		}
	}
}

// truncateJournal keeps the header and the first n lines of the journal file.
func truncateJournal(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	keep := strings.Join(lines[:1+n], "")
	if err := os.WriteFile(path, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReplacedStore_CrashBeforeHooksResumes: a crash after the external restore
// line and before the hooks finish is resumed by the next boot, which neither
// appends a second line nor skips the hooks, though the markers already read it
// as the same store.
func TestReplacedStore_CrashBeforeHooksResumes(t *testing.T) {
	g := newGotham(t)
	g.swapIn(g.archive)
	boom := errors.New("the hook crashed")
	failing := func(hookEnv) ([]journal.RestoreHook, error) {
		return []journal.RestoreHook{func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }}, nil
	}
	res, _, err := g.tryBoot(nil, nil, failing)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the hook's", err)
	}
	line := g.externalLine()
	if g.hooked(line.Seq) || res.RestoreLine != line.Seq {
		t.Fatalf("hooked %v after a failing hook, result line %d, journal line %d", g.hooked(line.Seq), res.RestoreLine, line.Seq)
	}
	g.restart()
	res, hres := g.boot(nil, nil)
	if res.Class != classSame || !hres.Ran || len(res.Owed) != 1 || res.RestoreLine != line.Seq {
		t.Fatalf("resume: class %q, hooks ran %v, owed %d, line %d (want %d)", res.Class, hres.Ran, len(res.Owed), res.RestoreLine, line.Seq)
	}
	if !g.hooked(line.Seq) || len(g.restoreLines()) != 1 {
		t.Errorf("hooked %v, %d restore line(s): the resume appended or skipped", g.hooked(line.Seq), len(g.restoreLines()))
	}
	if got := g.keyStatus("key-redrobin"); got != auth.KeyNeedsConfirmation {
		t.Errorf("key-redrobin = %q after the resumed hooks", got)
	}
	g.restart()
	if _, hres := g.boot(nil, nil); hres.Ran {
		t.Error("a third boot ran the hooks again")
	}
}

// TestCommittedHeadAnchorDetectsInPlaceCopy: a DB directory copied file by
// file over the live one (same directory identity, same marker) after a later
// revocation reads replaced, because head_before is below the anchor; and the
// anchor never leads the store: a revocation whose commit failed does not
// move it. (The journal's half is in backup/journal.)
func TestCommittedHeadAnchorDetectsInPlaceCopy(t *testing.T) {
	g := newGotham(t)
	same := g.snapshot("same") // head 1, the pair the file will still carry
	g.revokeKey("key-batmobile")
	g.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")}) // the commit failed
	g.restart()                                                                                       // the anchor is written at shutdown
	loc := g.location()
	if loc.Head.Seq != 2 || loc.Head != g.head() {
		t.Fatalf("anchor %+v, store head %+v: it must equal the committed head and never lead it (the journal ends at %d)", loc.Head, g.head(), g.chain().IntactEnd())
	}
	if loc.ServedBoot != g.pair() {
		t.Fatalf("the pair moved: file %+v, store %+v", loc.ServedBoot, g.pair())
	}

	g.swapIn(same)
	res, hres := g.boot(nil, nil)
	if res.Class != classReplaced || !strings.Contains(res.Why, "anchor") || !hres.Ran {
		t.Fatalf("class %q (%s), hooks ran %v: an in-place copy that kept the pair must read replaced", res.Class, res.Why, hres.Ran)
	}
	if g.keyStatus("key-batmobile") != auth.KeyRevoked || g.keyStatus("key-redrobin") != auth.KeyRevoked {
		t.Errorf("the tail after the copy's head was not re-applied: batmobile %q, redrobin %q", g.keyStatus("key-batmobile"), g.keyStatus("key-redrobin"))
	}
}

// TestReplacedStore_CrashBeforeMarkersReusesLine: a crash after the external
// restore line and before step 2 wrote the markers reads replaced again at the
// next boot; that boot finds the line that still owes its hooks and runs them,
// and never appends a second line.
func TestReplacedStore_CrashBeforeMarkersReusesLine(t *testing.T) {
	g := newGotham(t)
	g.swapIn(g.archive)
	boom := errors.New("the hook crashed")
	failing := func(hookEnv) ([]journal.RestoreHook, error) {
		return []journal.RestoreHook{func(context.Context, journal.RestoreContext) (map[string]int, error) { return nil, boom }}, nil
	}
	if _, _, err := g.tryBoot(nil, nil, failing); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	line := g.externalLine()
	g.restart()
	// The markers step 2 wrote are lost, as by a crash a moment earlier.
	g.setLocation(func(l *journal.Location) { l.ServedBoot = journal.ServedBoot{N: 99, Nonce: "ra's"} })
	res, hres := g.boot(nil, nil)
	if res.Class != classReplaced || !hres.Ran || len(res.Owed) != 1 || res.RestoreLine != line.Seq {
		t.Fatalf("class %q, hooks ran %v, owed %d, line %d (want %d)", res.Class, hres.Ran, len(res.Owed), res.RestoreLine, line.Seq)
	}
	if n := len(g.restoreLines()); n != 1 || !g.hooked(line.Seq) {
		t.Errorf("%d restore line(s), hooked %v", n, g.hooked(line.Seq))
	}
}
