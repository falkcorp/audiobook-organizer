// file: core/storage/restorejournal/moves_test.go
// version: 1.0.0
// guid: 5e088bbd-043c-44d1-bf72-4165bf7f6cf8
// last-edited: 2026-11-07

package restorejournal

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestSetAsideDestStaysOnOwnFilesystem: the one set-aside rule (05-storage
// §5S.3.15 "Stage"): a state-directory target to restore/<id>/previous/, a
// DB-side one to <db parent>/.mediacore-restore/<id>/previous/, any other
// beside itself; failed/ follows the same rule (decision D3).
func TestSetAsideDestStaysOnOwnFilesystem(t *testing.T) {
	state := "/srv/gotham/state"
	db := "/mnt/batcave/storage/main.pebble"
	for _, tc := range []struct{ target, want string }{
		{state + "/config.boot.json", state + "/restore/r1/previous/config.boot.json"},
		{db, "/mnt/batcave/storage/.mediacore-restore/r1/previous/main.pebble"},
		{db + ".storage-levels", "/mnt/batcave/storage/.mediacore-restore/r1/previous/main.pebble.storage-levels"},
		{"/mnt/batcave/storage/aux/books/ol", "/mnt/batcave/storage/.mediacore-restore/r1/previous/aux/books/ol"},
		{"/media/arkham/openlibrary-dumps/oldb", "/media/arkham/openlibrary-dumps/.mediacore-restore/r1/previous/oldb"},
	} {
		if got := SetAsideDest(state, db, "r1", tc.target); got != tc.want {
			t.Errorf("SetAsideDest(%s) = %s, want %s", tc.target, got, tc.want)
		}
		want := strings.Replace(tc.want, "/previous/", "/failed/", 1)
		if got := FailedDest(state, db, "r1", tc.target); got != want {
			t.Errorf("FailedDest(%s) = %s, want %s", tc.target, got, want)
		}
	}
	// A DB under the state directory (the compiled default) is a
	// state-directory target.
	inState := state + "/storage/main.pebble"
	if got, want := SetAsideDest(state, inState, "r1", inState), state+"/restore/r1/previous/storage/main.pebble"; got != want {
		t.Errorf("default-layout DB set aside to %s, want %s", got, want)
	}
}

// TestSetAsidesDerivesStepC: the set-aside list of a restore intent (D1): the
// DB directory, its core and legacy sidecars, then every other placement's
// live path, each once.
func TestSetAsidesDerivesStepC(t *testing.T) {
	state, db := "/srv/gotham/state", "/srv/gotham/state/storage/main.pebble"
	in := Intent{ID: "r2", Kind: KindArchive, DBPath: db, Targets: []Target{
		{From: "/stage/db", To: db},
		{From: "/stage/config.boot.json", To: state + "/config.boot.json"},
	}}
	got := SetAsides(state, in, []string{".storage-format"})
	var froms []string
	for _, m := range got {
		froms = append(froms, m.From)
		if m.To != SetAsideDest(state, db, "r2", m.From) {
			t.Errorf("%s set aside to %s, want SetAsideDest", m.From, m.To)
		}
	}
	want := []string{db, db + ".storage-levels", db + ".migration-checkpoint", db + ".storage-format", state + "/config.boot.json"}
	if !reflect.DeepEqual(froms, want) {
		t.Fatalf("set-aside list = %v, want %v", froms, want)
	}
}

// fakeProbe is a device map over paths; a path absent from it does not
// exist, and readOnly lists unwritable directories.
type fakeProbe struct {
	dev      map[string]uint64
	readOnly map[string]bool
}

func (f fakeProbe) ItemDev(p string) (uint64, error) { return f.DirDev(p) }
func (f fakeProbe) DirDev(p string) (uint64, error) {
	if d, ok := f.dev[p]; ok {
		return d, nil
	}
	return 0, os.ErrNotExist
}
func (f fakeProbe) Writable(dir string) error {
	if f.readOnly[dir] {
		return os.ErrPermission
	}
	return nil
}

// TestStageRefusesUnrenamableTargets: DATA-23's pre-check refuses a mount
// point, a single-file bind mount, a destination on another filesystem and
// an unwritable parent, each with ErrRenameRefused naming the path, and
// skips an absent source.
func TestStageRefusesUnrenamableTargets(t *testing.T) {
	base := map[string]uint64{"/": 1, "/srv": 1, "/srv/db": 1, "/srv/db/main.pebble": 1, "/srv/state": 1}
	with := func(extra map[string]uint64) map[string]uint64 {
		out := map[string]uint64{}
		maps.Copy(out, base)
		maps.Copy(out, extra)
		return out
	}
	ok := Target{From: "/srv/db/main.pebble", To: "/srv/db/.mediacore-restore/r/previous/main.pebble"}
	if err := Validate(fakeProbe{dev: base}, []Target{ok, {From: "/srv/db/absent", To: "/srv/db/x"}}); err != nil {
		t.Fatalf("a renamable target and an absent one: %v", err)
	}
	for name, tc := range map[string]struct {
		p    fakeProbe
		m    Target
		path string
	}{
		"mount point":     {fakeProbe{dev: with(map[string]uint64{"/srv/db/main.pebble": 2})}, ok, "/srv/db/main.pebble"},
		"bind-mount file": {fakeProbe{dev: with(map[string]uint64{"/srv/db/main.pebble.storage-levels": 3})}, Target{From: "/srv/db/main.pebble.storage-levels", To: "/srv/db/.x/l"}, "storage-levels"},
		"other device":    {fakeProbe{dev: with(map[string]uint64{"/mnt": 4})}, Target{From: "/srv/db/main.pebble", To: "/mnt/previous/main.pebble"}, "/mnt/previous"},
		"read-only":       {fakeProbe{dev: base, readOnly: map[string]bool{"/srv/db": true}}, ok, "/srv/db"},
	} {
		err := Validate(tc.p, []Target{tc.m})
		if !errors.Is(err, ErrRenameRefused) || !strings.Contains(err.Error(), tc.path) {
			t.Errorf("%s: Validate = %v, want ErrRenameRefused naming %s", name, err, tc.path)
		}
	}
}

// TestOSProbeReadOnlyParent: the real probe refuses an unwritable parent
// (skipped as root, which access(2) always admits).
func TestOSProbeReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("access(2) admits root; no unix modes on windows")
	}
	dir := t.TempDir()
	parent := filepath.Join(dir, "cave")
	if err := os.MkdirAll(filepath.Join(parent, "main.pebble"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	err := Validate(OSProbe(), []Target{{From: filepath.Join(parent, "main.pebble"), To: filepath.Join(dir, "previous", "main.pebble")}})
	if !errors.Is(err, ErrRenameRefused) {
		t.Fatalf("read-only parent: %v, want ErrRenameRefused", err)
	}
	if err := Validate(OSProbe(), []Target{{From: filepath.Join(dir, "cave"), To: filepath.Join(dir, "x", "cave")}}); err != nil {
		t.Fatalf("a renamable directory: %v", err)
	}
}

// TestCreateIntentFSInMemory: the one create works over an in-memory
// filesystem too, refuses a second intent and an in-flight swap, and the
// probe lists the in-flight id.
func TestCreateIntentFSInMemory(t *testing.T) {
	fs := vfs.NewMem()
	state := "/gotham/state"
	in := Intent{ID: "alfred", Kind: KindReset, DBPath: "/gotham/state/storage/main.pebble"}
	if err := CreateIntentFS(fs, state, in); err != nil {
		t.Fatal(err)
	}
	if err := CreateIntentFS(fs, state, Intent{ID: "joker"}); !errors.Is(err, ErrSwapPending) {
		t.Fatalf("second intent: %v, want ErrSwapPending", err)
	}
	if err := fs.MkdirAll(JournalDir(state, "alfred"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(PendingPath(state), filepath.Join(JournalDir(state, "alfred"), InflightName)); err != nil {
		t.Fatal(err)
	}
	if ids, err := InFlightIDs(fs, state); err != nil || len(ids) != 1 || ids[0] != "alfred" {
		t.Fatalf("InFlightIDs = %v, %v", ids, err)
	}
	if err := CreateIntentFS(fs, state, Intent{ID: "joker"}); !errors.Is(err, ErrSwapPending) {
		t.Fatalf("intent while in flight: %v, want ErrSwapPending", err)
	}
	if err := CreateIntentFS(fs, state, Intent{ID: "../x"}); err == nil {
		t.Fatal("an id that is not one path segment was accepted")
	}
}
