// file: core/internal/pebble/pebble_test.go
// version: 1.0.0
// guid: 81185b91-9ab7-450b-a642-3ca390d9545c
// last-edited: 2026-11-07

package pebble

import (
	"errors"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// fakeEngine counts the gate's WAL syncs and its one close.
type fakeEngine struct {
	syncs  atomic.Int32
	closes atomic.Int32
}

func (f *fakeEngine) SyncWAL() error { f.syncs.Add(1); return nil }
func (f *fakeEngine) Close() error   { f.closes.Add(1); return nil }

// TestCloseWaitsForLeases (05-storage §5S.10, synctest): Close refuses new
// leases at once, waits for held ones, syncs the WAL before and after the
// wait and closes the engine only with no lease held; a lease held past the
// timeout leaves the engine open with an error, and a later Close finishes.
func TestCloseWaitsForLeases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		eng := &fakeEngine{}
		g := newGate(eng, true)
		if err := g.acquire(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- g.close(10 * time.Second) }()
		synctest.Wait()
		if err := g.acquire(); !errors.Is(err, ErrClosing) {
			t.Fatalf("acquire while closing = %v, want ErrClosing", err)
		}
		if eng.closes.Load() != 0 {
			t.Fatal("engine closed while a lease was held")
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if eng.closes.Load() != 0 {
			t.Fatal("engine closed before the lease was released")
		}
		g.release()
		if err := <-done; err != nil {
			t.Fatalf("close: %v", err)
		}
		if eng.closes.Load() != 1 {
			t.Fatalf("engine closed %d times, want 1", eng.closes.Load())
		}
		if eng.syncs.Load() < 2 {
			t.Fatalf("%d WAL syncs, want one before and one after the drain", eng.syncs.Load())
		}
		if err := g.close(time.Second); err != nil {
			t.Fatalf("second close = %v, want nil", err)
		}
	})
}

func TestCloseTimeoutLeavesEngineOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		eng := &fakeEngine{}
		g := newGate(eng, true)
		if err := g.acquire(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		err := g.close(3 * time.Second)
		if !errors.Is(err, ErrClosing) {
			t.Fatalf("close with a held lease = %v, want ErrClosing", err)
		}
		if waited := time.Since(start); waited != 3*time.Second {
			t.Fatalf("close waited %s, want the 3s timeout", waited)
		}
		if eng.closes.Load() != 0 {
			t.Fatal("engine closed under a held lease")
		}
		g.release()
		if err := g.close(3 * time.Second); err != nil {
			t.Fatalf("close after the release = %v", err)
		}
		if eng.closes.Load() != 1 {
			t.Fatalf("engine closed %d times, want 1", eng.closes.Load())
		}
	})
}

func TestGateConcurrentLeases(t *testing.T) {
	eng := &fakeEngine{}
	g := newGate(eng, true)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 200 {
				if err := g.acquire(); err != nil {
					return
				}
				g.release()
			}
		})
	}
	wg.Wait()
	if err := g.close(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if g.inFlight() != 0 || eng.closes.Load() != 1 {
		t.Fatalf("in flight %d, closes %d", g.inFlight(), eng.closes.Load())
	}
}

func TestOpenWithoutFormatThenRatchet(t *testing.T) {
	fs := vfs.NewMem()
	e, err := Open("gotham", Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	if e.FormatMajorVersion() >= Format {
		t.Fatalf("a new store opened at format %d, want below the pin %d until the ratchet", e.FormatMajorVersion(), Format)
	}
	if !e.NeedsRatchet() {
		t.Fatal("NeedsRatchet false below the pin")
	}
	if err := e.Ratchet(); err != nil {
		t.Fatal(err)
	}
	if e.FormatMajorVersion() != Format || e.NeedsRatchet() {
		t.Fatalf("format after the ratchet = %d", e.FormatMajorVersion())
	}
	if err := e.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	// Reopening keeps the ratcheted format.
	e2, err := Open("gotham", Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e2.Close(time.Second) }()
	if e2.FormatMajorVersion() != Format {
		t.Fatalf("reopened format %d, want %d", e2.FormatMajorVersion(), Format)
	}
}

func TestReadOnlyEngineRefusesWritesAndRatchet(t *testing.T) {
	fs := vfs.NewMem()
	e, err := Open("arkham", Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DB().Set([]byte("k"), []byte("v"), cpebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	ro, err := Open("arkham", Options{FS: fs, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close(time.Second) }()
	if err := ro.DB().Set([]byte("k2"), []byte("v"), cpebble.Sync); err == nil {
		t.Fatal("write through a read-only engine succeeded")
	}
	if err := ro.Ratchet(); err == nil {
		t.Fatal("ratchet of a read-only engine succeeded")
	}
	if ro.NeedsRatchet() {
		t.Fatal("a read-only engine reports a ratchet to do")
	}
}

func TestLeaseRefusedAfterClose(t *testing.T) {
	e, err := Open("batcave", Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Lease(); !errors.Is(err, ErrClosing) {
		t.Fatalf("lease after close = %v, want ErrClosing", err)
	}
}

func TestLevelsSidecarRoundTrip(t *testing.T) {
	fs := vfs.NewMem()
	if err := fs.MkdirAll("wayne", 0o755); err != nil {
		t.Fatal(err)
	}
	db := "wayne/main.pebble"
	if _, ok, err := ReadLevels(fs, db); err != nil || ok {
		t.Fatalf("absent sidecar: ok=%v err=%v", ok, err)
	}
	want := map[string]int{"core": 0, "hello": 2, "batcave": 1}
	if err := WriteLevels(fs, db, want); err != nil {
		t.Fatal(err)
	}
	b, ok, err := ReadFile(fs, db+LevelsSuffix)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if string(b) != "batcave 1\ncore 0\nhello 2\n" {
		t.Fatalf("sidecar content %q, want sorted owner lines", b)
	}
	got, ok, err := ReadLevels(fs, db)
	if err != nil || !ok || !maps.Equal(got, want) {
		t.Fatalf("read back %v ok=%v err=%v", got, ok, err)
	}
	if err := WriteFileAtomic(fs, db+LevelsSuffix, []byte("core two\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadLevels(fs, db); err == nil || !ok {
		t.Fatalf("unreadable sidecar: ok=%v err=%v, want an error", ok, err)
	}
	entries, err := fs.List("wayne")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if name != "main.pebble"+LevelsSuffix {
			t.Fatalf("stray file %q beside the sidecar", name)
		}
	}
}

func TestIntSidecarRoundTrip(t *testing.T) {
	fs := vfs.NewMem()
	if err := WriteInt(fs, "db.storage-format", 2); err != nil {
		t.Fatal(err)
	}
	n, ok, err := ReadInt(fs, "db.storage-format")
	if err != nil || !ok || n != 2 {
		t.Fatalf("ReadInt = %d %v %v", n, ok, err)
	}
}

func TestGuardDecide(t *testing.T) {
	levels := Guard{StampSource: "stamp", SidecarSource: "sidecar", Compare: MinReader}
	cases := []struct {
		name                  string
		stamp, sidecar, sup   int
		stampOK, sidecarOK    bool
		wantStore             int
		wantSource            string
		wantTooNew, wantDiffs bool
	}{
		{"absent is zero", 0, 0, 0, false, false, 0, "stamp", false, false},
		{"stamp within", 1, 1, 1, true, true, 1, "stamp", false, false},
		{"sidecar ahead refuses", 1, 2, 1, true, true, 2, "sidecar", true, true},
		{"stamp too new", 3, 0, 2, true, false, 3, "stamp", true, false},
		{"older store passes", 1, 1, 4, true, true, 1, "stamp", false, false},
	}
	for _, c := range cases {
		d := levels.Decide(c.stamp, c.stampOK, c.sidecar, c.sidecarOK, c.sup)
		if d.Store != c.wantStore || d.Source != c.wantSource || d.TooNew != c.wantTooNew || d.Disagree != c.wantDiffs {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
	legacy := Guard{StampSource: "legacy-stamp", SidecarSource: "legacy-stamp", Compare: LegacyExact}
	if d := legacy.Decide(2, true, 0, false, 1); !d.TooNew {
		t.Error("legacy projection above the value this build writes passed")
	}
}
