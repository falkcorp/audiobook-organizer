// file: core/backup/journal/component_test.go
// version: 1.0.0
// guid: 91b4e7d2-3c58-4a06-b2f1-7d0e5a8c4f69
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// bound is a component bound to fx's journal path.
func (fx *fixture) bound(t *testing.T) *Component {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	c := fx.component(t)
	must(t, c.Bind(context.Background(), fx.path))
	return c
}

// setServed writes the store's served_boot pair the way a boot does.
func (fx *fixture) setServed(t *testing.T, p ServedBoot) {
	t.Helper()
	ctx := context.Background()
	b := fx.ks.NewBatch()
	if _, err := b.GetForUpdate(ctx, fx.ks, []byte(servedBootKey)); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	raw := []byte(`{"n":` + itoa(p.N) + `,"nonce":"` + p.Nonce + `"}`)
	must(t, b.Set([]byte(servedBootKey), raw))
	must(t, b.Commit(ctx, storage.Sync))
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

func hasHealth(hs []Health, token string) (Health, bool) {
	for _, h := range hs {
		if h.Token == token {
			return h, true
		}
	}
	return Health{}, false
}

// TestRevocationStagesJournalHead: with the identity flip and before the
// backup flip, a key revoke commits with c/backup/journal_head staged through
// GetForUpdate, and an append failure commits with its journal_gap/ record. The
// test drives the journal component directly (the kernel half is F4-08's).
func TestRevocationStagesJournalHead(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	c := fx.component(t)

	// Before the component is bound to a file, Append refuses, Gap works, and
	// the revocation still commits: a disk problem never blocks a security
	// action.
	at0 := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/joker"), []byte("disabled")))
	if err := c.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, ErrNotBound) {
		t.Fatalf("Append before Bind: err = %v, want ErrNotBound", err)
	}
	must(t, c.Gap(b, at0))
	must(t, b.Commit(ctx, storage.Sync))
	if _, err := fx.ks.Get(ctx, gapKey(at0)); err != nil {
		t.Fatalf("the gap record of the unbound append: %v", err)
	}

	// Bound: the open gap is adopted, and the next Append closes it in the
	// same write.
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	must(t, c.Bind(ctx, fx.path))
	if h, ok := hasHealth(c.Health(), HealthUnwritable); !ok || h.Severity != SeverityDegraded {
		t.Fatalf("health with an open gap = %+v, want %s degraded", c.Health(), HealthUnwritable)
	}
	rb := fx.revoke(t, c, "penguin")
	if !sliceHas(rb.Participants(), Owner) {
		t.Errorf("the revoking batch does not carry the c/backup/ handle: %v", rb.Participants())
	}
	if h := fx.head(t); h.Seq != 0 {
		t.Fatalf("head = %+v before the revoking batch committed, want none (staged, not written)", h)
	}
	must(t, rb.Commit(ctx, storage.Sync))
	chain := fx.chain(t)
	if chain.Break != nil || len(chain.Lines) != 2 || chain.Lines[0].Kind != Gap || chain.Lines[1].Seq != 2 {
		t.Fatalf("chain = %+v (break %v), want a closing gap line and the revocation", chain.Lines, chain.Break)
	}
	if h := fx.head(t); h.Seq != 2 || h.MAC != chain.Lines[1].MAC {
		t.Errorf("head = %+v, want seq 2 with the revocation line's mac", h)
	}
	if _, err := fx.ks.Get(ctx, gapKey(at0)); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the closed gap's record: err = %v, want deleted in the revoking batch", err)
	}
	if _, ok := hasHealth(c.Health(), HealthUnwritable); ok {
		t.Errorf("health still reports %s after the gap closed: %+v", HealthUnwritable, c.Health())
	}

	// The head is staged through GetForUpdate: a batch that appended before
	// another committed conflicts on commit (and its retry appends again).
	first := fx.revoke(t, c, "riddler")
	second := fx.revoke(t, c, "bane")
	must(t, second.Commit(ctx, storage.Sync))
	if err := first.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("the stale revoking batch: err = %v, want ErrConflict", err)
	}
	if h := fx.head(t); h.Seq != 4 {
		t.Errorf("head = %+v, want seq 4 (never moved back)", h)
	}

	// An append failure: the revocation commits with its journal_gap/ record
	// and no head.
	c.file.f = &failingFile{lineFile: c.file.f, failWrite: true}
	at1 := time.Date(2026, 11, 10, 3, 5, 0, 0, time.UTC)
	fb := fx.auth.NewBatch()
	must(t, fb.Set([]byte("user/scarecrow"), []byte("disabled")))
	if err := c.Append(ctx, fb, Revocation{Kind: UserDisabled, At: at1}); err == nil {
		t.Fatal("Append succeeded on a failing disk")
	}
	must(t, c.Gap(fb, at1))
	must(t, fb.Commit(ctx, storage.Sync))
	if v, err := fx.auth.Get(ctx, []byte("user/scarecrow")); err != nil || string(v) != "disabled" {
		t.Errorf("the revocation did not commit on the append failure: %q, %v", v, err)
	}
	if v, err := fx.ks.Get(ctx, gapKey(at1)); err != nil || !strings.Contains(string(v), "append_failed") {
		t.Errorf("the gap record: %q, %v", v, err)
	}
	if h := fx.head(t); h.Seq != 4 {
		t.Errorf("head = %+v after the failed append, want it unchanged at seq 4", h)
	}
}

func sliceHas(owners []storage.Owner, o storage.Owner) bool {
	return slices.Contains(owners, o)
}

// TestOfflineRevocationJournaled: an offline revoking write (for example
// `users reset-password` bumping an epoch) appends as the serving path does,
// with the head staged. The offline open's component re-applies nothing, runs
// no hook and appends no `restore` or `gap` line: the journal gains the one
// revocation line and the anchor moves at shutdown, nothing else.
func TestOfflineRevocationJournaled(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	pair := ServedBoot{N: 3, Nonce: "00ff"}
	fx.setServed(t, pair)
	// A serving boot left the journal with one line and a file in step with it.
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	must(t, j.Close())
	fx.locate(t, fx.head(t), pair)
	before := fx.lines(t)

	c := fx.bound(t)
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/penguin/epoch"), []byte("2")))
	if err := c.Append(ctx, b, Revocation{Kind: CredentialEpochBumped, Epoch: 2, At: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC),
		Subject: entity.Ref{Module: "core", Kind: "user", ID: "penguin"}}); err != nil {
		t.Fatal(err)
	}
	must(t, b.Commit(ctx, storage.Sync))
	must(t, c.Close(ctx))

	after := fx.lines(t)
	if len(after) != len(before)+1 || !strings.Contains(after[len(after)-1], `"kind":"credential_epoch.bumped"`) {
		t.Fatalf("the offline write added %d line(s): %q", len(after)-len(before), after[len(before):])
	}
	chain := fx.chain(t)
	if chain.Break != nil {
		t.Fatalf("chain break: %v", chain.Break)
	}
	for _, l := range chain.Lines {
		if l.Kind == Restore || l.Kind == Gap || l.Kind == RestoreAborted {
			t.Errorf("the offline open appended a %s line", l.Kind)
		}
	}
	if h := fx.head(t); h.Seq != 2 || h.MAC != chain.Lines[1].MAC {
		t.Errorf("head = %+v, want seq 2 (staged with the revocation)", h)
	}
	loc := fx.location(t)
	if loc.Head != fx.head(t) {
		t.Errorf("anchor head = %+v after shutdown, want the committed head %+v", loc.Head, fx.head(t))
	}
	if loc.ServedBoot != pair {
		t.Errorf("served_boot in the file = %+v, want it untouched %+v: an offline open writes no marker", loc.ServedBoot, pair)
	}
	if got, err := ReadServedBoot(ctx, fx.ks); err != nil || got != pair {
		t.Errorf("served_boot in the store = %+v, %v; want untouched", got, err)
	}
}

// TestLocationFileModeAndAtomicity: the journal and backup.location.json are
// 0600; a crash between the temp file and the rename leaves the previous
// backup.location.json intact, and the next write replaces the stale temp
// file the crash left.
func TestLocationFileModeAndAtomicity(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	must(t, j.Close())
	if st, err := os.Stat(fx.path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("journal mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}

	prev := fx.locate(t, Head{Seq: 4, MAC: "ab"}, ServedBoot{N: 2, Nonce: "ff"})
	path := filepath.Join(fx.dir, LocationFile)
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("location mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	original, err := os.ReadFile(path)
	must(t, err)

	next := prev
	next.Head = Head{Seq: 9, MAC: "cd"}
	next.ServedBoot = ServedBoot{N: 3, Nonce: "ee"}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the simulated crash did not happen")
			}
		}()
		_ = writeLocation(fx.dir, next, func(string, string) error { panic("the Batcave lost power") })
	}()

	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("after a crash before the rename the location file = %q, %v; want the previous content %q", got, err, original)
	}
	if got := fx.location(t); got != prev {
		t.Errorf("ReadLocation = %+v, want the previous %+v", got, prev)
	}
	stale, err := os.ReadFile(path + ".tmp")
	if err != nil || !strings.Contains(string(stale), `"seq":9`) {
		t.Fatalf("the crash left the temp file %q, %v; want the unrenamed new content", stale, err)
	}

	must(t, WriteLocation(fx.dir, next))
	if got := fx.location(t); got != next {
		t.Errorf("after the next write ReadLocation = %+v, want %+v", got, next)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temp file survived a completed write: %v", err)
	}

	// A rename that fails (not a crash) leaves the file and no temp behind.
	if err := writeLocation(fx.dir, prev, func(string, string) error { return errors.New("read-only filesystem") }); err == nil {
		t.Error("WriteLocation reported success although the rename failed")
	}
	if got := fx.location(t); got != next {
		t.Errorf("a failed rename changed the file: %+v", got)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed rename left the temp file: %v", err)
	}
}

// TestLocation_ReadWriteDescribes: the file round-trips, a missing one reads
// as absent, a malformed one is an error, and Describes compares db_path and
// install_id only.
func TestLocation_ReadWriteDescribes(t *testing.T) {
	dir := t.TempDir()
	if l, ok, err := ReadLocation(dir); ok || err != nil || l != (Location{}) {
		t.Fatalf("ReadLocation of a missing file = %+v, %v, %v", l, ok, err)
	}
	want := Location{BackupDir: "/gotham/backups", Journal: "/gotham/backups/x_revocations.journal", DBPath: "/gotham/db", InstallID: installID,
		Store: storage.StoreIdentity{Epoch: 2, DirFSID: 3, DirIno: 4}, ServedBoot: ServedBoot{N: 5, Nonce: "aa"}, Head: Head{Seq: 6, MAC: "bb"}}
	must(t, WriteLocation(dir, want))
	raw, err := os.ReadFile(filepath.Join(dir, LocationFile))
	must(t, err)
	for _, member := range []string{`"backup_dir"`, `"journal"`, `"db_path"`, `"install_id"`, `"store":{"epoch":2,"dir_fsid":3,"dir_ino":4}`, `"served_boot":{"n":5,"nonce":"aa"}`, `"head":{"seq":6,"mac":"bb"}`} {
		if !strings.Contains(string(raw), member) {
			t.Errorf("the file lacks %s: %s", member, raw)
		}
	}
	if got, ok, err := ReadLocation(dir); err != nil || !ok || got != want {
		t.Fatalf("round trip = %+v, %v, %v", got, ok, err)
	}
	if !want.Describes("/gotham/db", installID) || want.Describes("/arkham/db", installID) || want.Describes("/gotham/db", "arkham-install") || (Location{}).Describes("", "") {
		t.Error("Describes compares db_path and install_id, and an empty file describes nothing")
	}
	must(t, os.WriteFile(filepath.Join(dir, LocationFile), []byte("{not json"), 0o600))
	if _, ok, err := ReadLocation(dir); ok || err == nil {
		t.Errorf("a malformed file: ok %v, err %v; want an error", ok, err)
	}
}

// TestCommittedHeadAnchorDetectsInPlaceCopy: the journal component's half of
// the anchor. It follows the committed head only: never ahead of the store (a
// revocation whose batch did not commit does not move it), never lowered (a
// store copied back from an older state leaves the anchor above its head, which
// is what the classifier's rule (b) reads), only for a file that describes this
// store, and not at all while frozen.
func TestCommittedHeadAnchorDetectsInPlaceCopy(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.locate(t, Head{}, ServedBoot{N: 1, Nonce: "aa"})
	c := fx.bound(t)

	fx.commitRevokeVia(t, c, "joker") // line 1, committed
	// The next Append syncs the anchor first (reading committed head 1) ...
	uncommitted := fx.revoke(t, c, "penguin") // line 2 written, never committed
	if got := fx.location(t).Head; got.Seq != 1 || got != fx.head(t) {
		t.Fatalf("anchor = %+v, want the committed head %+v", got, fx.head(t))
	}
	// ... and a revocation whose commit failed never moves it.
	must(t, uncommitted.Close())
	must(t, c.SyncAnchor(ctx))
	if got := fx.location(t).Head; got.Seq != 1 {
		t.Errorf("anchor = %+v after a batch that never committed, want seq 1: the anchor is never ahead of the store", got)
	}

	// Frozen: nothing rewrites it, however the head moved.
	c.SetFrozen(true)
	fx.commitRevokeVia(t, c, "riddler")
	fx.commitRevokeVia(t, c, "bane")
	must(t, c.SyncAnchor(ctx))
	if _, err := c.AdvanceServedBoot(ctx); err != nil {
		t.Fatal(err)
	}
	if loc := fx.location(t); loc.Head.Seq != 1 || loc.ServedBoot.N != 1 {
		t.Errorf("a frozen anchor was rewritten: %+v", loc)
	}
	// A line written but not committed while the anchor lags the head (line 5)
	// must not carry the anchor past the store's committed head when it thaws.
	orphan := fx.revoke(t, c, "scarecrow")
	c.SetFrozen(false)
	must(t, c.SyncAnchor(ctx))
	must(t, orphan.Close())
	// The uncommitted line 2 stays in the file as an orphan; the next Append
	// numbers past it, so riddler is line 3 and bane line 4.
	if got := fx.location(t).Head; got != fx.head(t) || got.Seq != 4 {
		t.Errorf("thawed anchor = %+v, want the committed head %+v (seq 4), not the file's tail %+v", got, fx.head(t), c.Tail())
	}

	// Never lowered: a store whose head dropped (copied back in place) leaves
	// the anchor where it was.
	high := fx.location(t)
	high.Head = Head{Seq: 99, MAC: "ff"}
	must(t, WriteLocation(fx.dir, high))
	must(t, c.SyncAnchor(ctx))
	if got := fx.location(t).Head; got.Seq != 99 {
		t.Errorf("anchor = %+v, want it never lowered below 99", got)
	}

	// A file that describes another store, or none at all, is left alone.
	other := fx.location(t)
	other.DBPath = "/arkham/db"
	other.Head = Head{Seq: 1, MAC: "aa"}
	must(t, WriteLocation(fx.dir, other))
	must(t, c.SyncAnchor(ctx))
	if got := fx.location(t); got != other {
		t.Errorf("a file describing another store was rewritten: %+v", got)
	}
	must(t, os.Remove(filepath.Join(fx.dir, LocationFile)))
	must(t, c.SyncAnchor(ctx))
	if _, err := os.Stat(filepath.Join(fx.dir, LocationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the anchor created the location file: %v (only the boot writes it whole)", err)
	}
}

// commitRevokeVia revokes user through the component and commits.
func (fx *fixture) commitRevokeVia(t *testing.T, c *Component, user string) {
	t.Helper()
	must(t, fx.revoke(t, c, user).Commit(context.Background(), storage.Sync))
}

// TestComponent_AdvanceServedBoot: n = max(the store's n, the file's n) + 1
// with a fresh nonce, the file's n counting only when the file describes this
// store; the pair lands in the file in the same hold when it describes this
// store and the component is not frozen.
func TestComponent_AdvanceServedBoot(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	c := fx.bound(t)

	// No file: the store gets the pair, no file is created.
	p1, err := c.AdvanceServedBoot(ctx)
	must(t, err)
	if p1.N != 1 || len(p1.Nonce) != 32 {
		t.Fatalf("first pair = %+v, want n 1 with a 128-bit nonce", p1)
	}
	if got, err := ReadServedBoot(ctx, fx.ks); err != nil || got != p1 {
		t.Fatalf("store pair = %+v, %v; want %+v", got, err, p1)
	}
	if _, err := os.Stat(filepath.Join(fx.dir, LocationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("AdvanceServedBoot created the location file: %v", err)
	}

	// A file that describes this store with a higher n wins; the pair and the
	// committed head go into the file.
	fx.locate(t, Head{}, ServedBoot{N: 9, Nonce: "old"})
	p2, err := c.AdvanceServedBoot(ctx)
	must(t, err)
	if p2.N != 10 || p2.Nonce == p1.Nonce {
		t.Fatalf("second pair = %+v, want n 10 and a new nonce", p2)
	}
	if loc := fx.location(t); loc.ServedBoot != p2 {
		t.Errorf("file pair = %+v, want %+v", loc.ServedBoot, p2)
	}

	// A file describing another store does not count and is not touched.
	other := fx.location(t)
	other.DBPath = "/arkham/db"
	other.ServedBoot = ServedBoot{N: 500, Nonce: "arkham"}
	must(t, WriteLocation(fx.dir, other))
	p3, err := c.AdvanceServedBoot(ctx)
	must(t, err)
	if p3.N != 11 {
		t.Errorf("pair = %+v, want n 11: another store's n 500 must not count", p3)
	}
	if got := fx.location(t); got != other {
		t.Errorf("a file describing another store was rewritten: %+v", got)
	}

	// Frozen: the store moves, the file does not; WriteLocation still writes.
	fx.locate(t, Head{}, ServedBoot{N: 11, Nonce: "x"})
	c.SetFrozen(true)
	p4, err := c.AdvanceServedBoot(ctx)
	must(t, err)
	if p4.N != 12 || fx.location(t).ServedBoot.N != 11 {
		t.Errorf("frozen: pair %+v, file n %d; want 12 and the file untouched at 11", p4, fx.location(t).ServedBoot.N)
	}
	must(t, c.WriteLocation(ctx, LocationDesc{BackupDir: "/b", Journal: "/b/j", Store: storage.StoreIdentity{Epoch: 3}}, p4))
	if loc := fx.location(t); loc.ServedBoot != p4 || loc.DBPath != fx.dbPath || loc.InstallID != installID || loc.Store.Epoch != 3 || loc.Journal != "/b/j" {
		t.Errorf("the explicit write while frozen = %+v", loc)
	}
}

// TestComponent_TickAndClose: the one-minute tick (here shortened) rewrites the
// anchor without any Append, Close stops it and rewrites once more at shutdown,
// and Close is safe twice.
func TestComponent_TickAndClose(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.locate(t, Head{}, ServedBoot{N: 1, Nonce: "aa"})
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	c := fx.component(t)
	c.o.Tick = 5 * time.Millisecond
	must(t, c.Bind(ctx, fx.path))
	c.Start()
	c.Start() // a second Start is a no-op
	fx.commitRevokeVia(t, c, "joker")

	deadline := time.Now().Add(5 * time.Second)
	for fx.location(t).Head.Seq != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the tick never moved the anchor: %+v", fx.location(t))
		}
		time.Sleep(5 * time.Millisecond)
	}
	fx.commitRevokeVia(t, c, "penguin")
	must(t, c.Close(ctx))
	if got := fx.location(t).Head; got.Seq != 2 {
		t.Errorf("anchor after Close = %+v, want seq 2 (rewritten at shutdown)", got)
	}
	must(t, c.Close(ctx))
	if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrClosed) {
		t.Errorf("Bind after Close: err = %v, want ErrClosed", err)
	}
	// Close released the journal's lock.
	j := fx.open(t)
	must(t, j.Close())
}

// TestComponent_Health: the tokens the journal's state calls for.
func TestComponent_Health(t *testing.T) {
	ctx := context.Background()

	t.Run("healthy", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.bound(t)
		if hs := c.Health(); len(hs) != 0 {
			t.Errorf("health = %+v, want none", hs)
		}
	})
	t.Run("a broken chain is revocation_journal_broken", func(t *testing.T) {
		fx := newFixture(t)
		j := fx.open(t)
		fx.commitRevoke(t, j, "joker")
		must(t, j.Close())
		lines := fx.lines(t)
		fx.write(t, lines[0], strings.Replace(lines[1], `"id":"joker"`, `"id":"alfred"`, 1))
		c := fx.component(t)
		if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Bind = %v, want ErrCorrupt", err)
		}
		if h, ok := hasHealth(c.Health(), HealthBroken); !ok || h.Severity != SeverityDegraded {
			t.Errorf("health = %+v, want %s degraded", c.Health(), HealthBroken)
		}
		if _, ok := hasHealth(c.Health(), HealthUnwritable); ok {
			t.Errorf("a broken chain is not also unwritable: %+v", c.Health())
		}
		// Gap still works on the broken journal.
		b := fx.auth.NewBatch()
		must(t, c.Gap(b, time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)))
		must(t, b.Commit(ctx, storage.Sync))
	})
	t.Run("another install is revocation_journal_broken until the boot sets it aside", func(t *testing.T) {
		fx := newFixture(t)
		must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
		o := fx.opts()
		o.InstallID = "arkham-install"
		oj, err := OpenFile(ctx, o)
		must(t, err)
		must(t, oj.Close())
		c := fx.component(t)
		if err := c.Bind(ctx, fx.path); !errors.Is(err, ErrOtherInstall) {
			t.Fatalf("Bind = %v, want ErrOtherInstall", err)
		}
		if _, ok := hasHealth(c.Health(), HealthBroken); !ok {
			t.Errorf("health = %+v, want %s", c.Health(), HealthBroken)
		}
	})
	t.Run("an unwritable disk is revocation_journal_unwritable", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.bound(t)
		c.file.f = &truncFails{failingFile{lineFile: c.file.f, failSync: true}}
		if err := c.Append(ctx, fx.auth.NewBatch(), Revocation{Kind: UserDeleted}); err == nil {
			t.Fatal("Append succeeded")
		}
		if h, ok := hasHealth(c.Health(), HealthUnwritable); !ok || h.Severity != SeverityDegraded {
			t.Errorf("health = %+v, want %s degraded", c.Health(), HealthUnwritable)
		}
		if _, ok := hasHealth(c.Health(), HealthBroken); ok {
			t.Errorf("a failed write is not a broken chain: %+v", c.Health())
		}
	})
	t.Run("reset and a boot-time break", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.bound(t)
		c.NoteReset()
		c.NoteBroken(errors.New("the chain breaks at seq 3"))
		hs := c.Health()
		if h, ok := hasHealth(hs, HealthReset); !ok || h.Severity != SeverityInfo {
			t.Errorf("health = %+v, want %s info", hs, HealthReset)
		}
		if h, ok := hasHealth(hs, HealthBroken); !ok || !strings.Contains(h.Detail, "seq 3") {
			t.Errorf("health = %+v, want %s naming the break", hs, HealthBroken)
		}
	})
	t.Run("colocation by device", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.bound(t)
		c.o.DirDev = func(string) (uint64, error) { return 42, nil }
		if h, ok := hasHealth(c.Health(), HealthColocated); !ok || h.Severity != SeverityInfo {
			t.Errorf("same device: health = %+v, want %s info", c.Health(), HealthColocated)
		}
		devs := map[string]uint64{filepath.Dir(fx.path): 1, fx.dbPath: 2}
		c.o.DirDev = func(p string) (uint64, error) { return devs[p], nil }
		if _, ok := hasHealth(c.Health(), HealthColocated); ok {
			t.Errorf("two devices: health = %+v, want no colocation token", c.Health())
		}
		c.o.DirDev = func(string) (uint64, error) { return 0, errors.New("no such directory") }
		if _, ok := hasHealth(c.Health(), HealthColocated); ok {
			t.Errorf("a stat failure: health = %+v, want no token and no error", c.Health())
		}
	})
}

// TestNewComponent_RefusesMissingOptions: a caller's mistake is an error, and
// the constructor does no file I/O.
func TestNewComponent_RefusesMissingOptions(t *testing.T) {
	fx := newFixture(t)
	good := ComponentOptions{StateDir: fx.dir, DBPath: fx.dbPath, Keyspace: fx.ks, Keys: fx.keys, InstallID: installID}
	for name, mut := range map[string]func(*ComponentOptions){
		"no state dir":     func(o *ComponentOptions) { o.StateDir = "" },
		"no db path":       func(o *ComponentOptions) { o.DBPath = "" },
		"another keyspace": func(o *ComponentOptions) { o.Keyspace = fx.auth },
		"no keys":          func(o *ComponentOptions) { o.Keys = nil },
		"no install ID":    func(o *ComponentOptions) { o.InstallID = "" },
	} {
		o := good
		mut(&o)
		if c, err := NewComponent(o); err == nil || c != nil {
			t.Errorf("%s: NewComponent = %v, %v; want an error", name, c, err)
		}
	}
	c, err := NewComponent(good)
	must(t, err)
	if entries, _ := os.ReadDir(fx.dir); len(entries) != 1 { // only "storage"
		t.Errorf("NewComponent wrote into the state directory: %v", entries)
	}
	if c.Path() != "" || c.Tail() != (Head{}) {
		t.Errorf("an unbound component has path %q, tail %+v", c.Path(), c.Tail())
	}
	if _, err := c.AppendLine(context.Background(), Revocation{Kind: Gap}); !errors.Is(err, ErrNotBound) {
		t.Errorf("AppendLine before Bind: err = %v, want ErrNotBound", err)
	}
}
