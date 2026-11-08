// file: core/storage/levels_test.go
// version: 1.0.0
// guid: b2f27289-7885-482a-90f4-69b89cc07b94
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

func raise(v, to int) Migration {
	return Migration{Version: v, Name: fmt.Sprintf("raise-%d", to), Compat: Breaking, RaisesLevel: to,
		Up: func(context.Context, *MigrationTx) error { return nil }}
}

// upgrade boots e once with a step-free build at level 0, then switches it
// to owners.
func upgrade(t *testing.T, e *env, owners ...OwnerSpec) {
	t.Helper()
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = owners
}

// TestRaiseWritesSidecarBeforeStamp (05-storage §5S.10): a level step writes
// the sidecar before it commits the stamp; a crash between the two leaves a
// sidecar ahead of the stamp, which an older build refuses and this build's
// next boot completes.
func TestRaiseWritesSidecarBeforeStamp(t *testing.T) {
	fs := vfs.NewCrashableMem()
	e := newEnvFS(t, fs, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}})
	upgrade(t, e, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{raise(1, 1)}}})
	crashed := errors.New("power cut")
	var sawSidecar, sawStamp int
	var clone *vfs.MemFS
	e.opts.hooks.beforeStampCommit = func() error {
		side, _, err := ipebble.ReadLevels(fs, e.path)
		if err != nil {
			return err
		}
		sawSidecar = side["wayne"]
		clone = fs.CrashClone(vfs.CrashCloneCfg{})
		return crashed
	}
	db, err := e.tryOpen()
	if !errors.Is(err, crashed) || db != nil {
		t.Fatalf("boot = %v, want the simulated crash", err)
	}
	if sawSidecar != 1 {
		t.Fatalf("sidecar before the stamp commit = %d, want 1", sawSidecar)
	}
	// The crashed store: an older build refuses it by the sidecar.
	older := newEnvFS(t, clone, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}})
	older.path, older.opts.Path = e.path, e.path
	_, err = older.tryOpen()
	var lt *LevelTooNewError
	if !errors.As(err, &lt) || lt.Source != "sidecar" || lt.Store != 1 {
		t.Fatalf("older build on the crashed store = %v, want a sidecar refusal", err)
	}
	// This build's next boot re-runs the step and stamps.
	again := newEnvFS(t, clone, e.opts.Owners...)
	again.path, again.opts.Path = e.path, e.path
	db = again.open()
	sawStamp, _ = stamp(t, db, "wayne")
	if sawStamp != 1 {
		t.Fatalf("stamp after the retry = %d, want 1", sawStamp)
	}
}

// TestRaiseNeverLowersLevel (05-storage §5S.10, DATA2-22): two level steps in
// either order leave the max.
func TestRaiseNeverLowersLevel(t *testing.T) {
	for _, order := range [][2]int{{3, 2}, {2, 3}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			e := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}})
			upgrade(t, e, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 3,
				Migrations: []Migration{raise(1, order[0]), raise(2, order[1])}}})
			db := e.open()
			if l, _ := stamp(t, db, "wayne"); l != 3 {
				t.Fatalf("level = %d, want 3", l)
			}
			side, _, err := ipebble.ReadLevels(e.fs, e.path)
			if err != nil || side["wayne"] != 3 {
				t.Fatalf("sidecar = %v, %v", side, err)
			}
		})
	}
}

// TestLevelsArePerOwner (05-storage §5S.10): a module's raise does not stop a
// build without that module; a core raise stops a build at a lower core
// level.
func TestLevelsArePerOwner(t *testing.T) {
	tasks := OwnerSpec{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true}}
	e := newEnv(t, tasks, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true}})
	upgrade(t, e, tasks, OwnerSpec{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{raise(1, 1)}}})
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	// A build without wayne boots; wayne stays dormant.
	e.opts.Owners = []OwnerSpec{tasks}
	if err := e.open().Close(); err != nil {
		t.Fatalf("a build without wayne refused a wayne raise: %v", err)
	}
	// A core raise.
	e.opts.Owners = []OwnerSpec{{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{raise(1, 1)}}}}
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	e.opts.Owners = []OwnerSpec{tasks}
	_, err := e.tryOpen()
	var lt *LevelTooNewError
	if !errors.As(err, &lt) || lt.Owner != "core" {
		t.Fatalf("a build at core level 0 on a core-1 store = %v", err)
	}
}

// projectionGuard is a books-like legacy projection over gotham's rows:
// a format stamp row and a marker row in gotham's claims.
func projectionGuard() *LegacyGuardProjection {
	return &LegacyGuardProjection{
		StampKey: "joker_format", MarkerKey: "joker_marker", SidecarSuffix: ".storage-format", LegacyMaxFormat: 1,
		Stage: func(ctx context.Context, r LegacyRange, b *Batch, value int, marker bool) error {
			w := b.In(r)
			if err := w.Set([]byte("joker_format"), []byte(strconv.Itoa(value))); err != nil {
				return err
			}
			if marker {
				return w.Set([]byte("joker_marker"), []byte("1"))
			}
			return w.Delete([]byte("joker_marker"))
		},
	}
}

func projectionClaims() []Claim {
	return append(gothamClaims(), Claim{Key: "joker_format", Mode: ClaimReadWrite}, Claim{Key: "joker_marker", Mode: ClaimReadWrite})
}

// headOpen is what a pre-guard build (HEAD) does at open: it reads only the
// legacy format stamp row (and its sidecar) and refuses a value above its
// SupportedStorageFormat (StorageFormatTooNewError, storage_format.go:44-49).
func headOpen(t *testing.T, e *env, supported int) error {
	t.Helper()
	side, _, err := ipebble.ReadInt(e.fs, e.path+".storage-format")
	if err != nil {
		return err
	}
	row := 0
	db, err := cpebble.Open(e.path, &cpebble.Options{FS: e.fs, ReadOnly: true, Logger: quiet{}})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if v, closer, err := db.Get([]byte("joker_format")); err == nil {
		row, _ = strconv.Atoi(string(v))
		_ = closer.Close()
	}
	if v := max(side, row); v > supported {
		return fmt.Errorf("StorageFormatTooNewError: store format %d, binary supports %d", v, supported)
	}
	return nil
}

// TestLegacyProjectionRefusesPreGuardBuild (05-storage §5S.10, OPS2-10):
// after a level step the projection is LegacyMaxFormat+1, staged through the
// owner's own rows and written to its sidecar, so a pre-guard open refuses;
// before any level step it is 1, which a pre-guard build accepts. The marker
// set by the raise is cleared at step 9 once no Breaking step is pending.
func TestLegacyProjectionRefusesPreGuardBuild(t *testing.T) {
	ctx := context.Background()
	g := projectionGuard()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g}})
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	if err := headOpen(t, e, g.LegacyMaxFormat); err != nil {
		t.Fatalf("a pre-guard build refused a store no level step ran on: %v", err)
	}
	e.opts.Owners = []OwnerSpec{{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g, Level: 1,
		Migrations: []Migration{raise(1, 1)}}}}
	db := e.open()
	lr, _ := db.Legacy("gotham")
	if v, err := lr.Get(ctx, []byte("joker_format")); err != nil || string(v) != "2" {
		t.Fatalf("projection row = %q, %v; want LegacyMaxFormat+1 = 2", v, err)
	}
	if _, err := lr.Get(ctx, []byte("joker_marker")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marker not cleared at step 9: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := headOpen(t, e, g.LegacyMaxFormat); err == nil {
		t.Fatal("a pre-guard build accepted a raised store")
	}
	// A core build that would write projection 1 treats a raised projection
	// as too new.
	e.opts.Owners = []OwnerSpec{{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g}}}
	_, err := e.tryOpen()
	if _, ok := errors.AsType[*LevelTooNewError](err); !ok {
		t.Fatalf("a level-0 core build on a raised store = %v", err)
	}
}

// TestUnreadableProjectionSidecarRefusesBoot (review MINOR-3): the legacy
// projection's sidecar is the projection guard's only input, so one that
// exists but cannot be read, or does not parse, refuses the boot instead of
// letting a store a newer build converted pass (and be rewritten to this
// build's value at step 9). An absent sidecar is not an error.
func TestUnreadableProjectionSidecarRefusesBoot(t *testing.T) {
	g := projectionGuard()
	owner := OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g}}
	side := func(e *env) string { return e.path + g.SidecarSuffix }
	t.Run("too-new-control", func(t *testing.T) {
		e := newEnv(t, owner)
		if err := e.open().Close(); err != nil {
			t.Fatal(err)
		}
		if err := ipebble.WriteFileAtomic(e.fs, side(e), []byte("99\n")); err != nil {
			t.Fatal(err)
		}
		_, err := e.tryOpen()
		if lt, ok := errors.AsType[*LevelTooNewError](err); !ok || lt.Source != "legacy-stamp" {
			t.Fatalf("boot with projection 99 = %v, want a legacy-stamp refusal", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		e := newEnv(t, owner)
		if err := e.open().Close(); err != nil {
			t.Fatal(err)
		}
		e.opts.FS = FS{fs: openFails{FS: e.fs, path: side(e)}}
		if _, err := e.tryOpen(); !errors.Is(err, errScarecrow) {
			t.Fatalf("boot with an unreadable projection sidecar = %v, want the read error", err)
		}
	})
	t.Run("garbled", func(t *testing.T) {
		e := newEnv(t, owner)
		if err := e.open().Close(); err != nil {
			t.Fatal(err)
		}
		if err := ipebble.WriteFileAtomic(e.fs, side(e), []byte("batarang\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tryOpen(); err == nil || !strings.Contains(err.Error(), "not an integer") {
			t.Fatalf("boot with a garbled projection sidecar = %v, want a refusal", err)
		}
		if b, _, _ := ipebble.ReadFile(e.fs, side(e)); string(b) != "batarang\n" {
			t.Fatalf("the refused boot rewrote the sidecar to %q", b)
		}
	})
	t.Run("absent", func(t *testing.T) {
		e := newEnv(t, owner)
		if err := e.open().Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.fs.Remove(side(e)); err != nil {
			t.Fatal(err)
		}
		if err := e.open().Close(); err != nil {
			t.Fatalf("boot without a projection sidecar: %v", err)
		}
	})
}
