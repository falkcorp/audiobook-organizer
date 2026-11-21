// file: core/storage/restorejournal/staging_test.go
// version: 0.1.0
// guid: 55ef337d-8ff0-4af6-b66f-47245b79a0b1
// last-edited: 2026-11-21

package restorejournal

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// TestStagingDestStaysOnOwnFilesystem: StagingDest is SetAsideDest's twin. A
// state-directory target is staged in the state directory's private
// directory, a DB-side one in the DB parent's (StagingDir's state/), any
// other beside itself; the state directory's rule comes first, as
// SetAsideDest's does. Each staged file lies under the same root as the
// target's set-aside, so the rename onto the target and the set-aside of the
// target stay on one filesystem.
func TestStagingDestStaysOnOwnFilesystem(t *testing.T) {
	state := "/srv/gotham/state"
	db := "/mnt/batcave/storage/main.pebble"
	for _, tc := range []struct{ target, label, want string }{
		{state + "/.encryption_key", "encryption_key", state + "/.mediacore-restore/r1/staging/state/encryption_key"},
		{state + "/keys/bat.keyring", "keyring", state + "/.mediacore-restore/r1/staging/state/keyring"},
		{"/mnt/batcave/storage/config.yaml", "gap_fill", "/mnt/batcave/storage/.mediacore-restore/r1/staging/state/gap_fill"},
		{"/media/arkham/oracle.key", "oracle", "/media/arkham/.mediacore-restore/r1/staging/state/oracle"},
	} {
		got := StagingDest(state, db, "r1", tc.target, tc.label)
		if got != tc.want {
			t.Errorf("StagingDest(%s) = %s, want %s", tc.target, got, tc.want)
		}
		if a, s := asideRootOf(SetAsideDest(state, db, "r1", tc.target)), asideRootOf(got); filepath.Dir(a) != filepath.Dir(s) {
			t.Errorf("%s: staged under %s, set aside under %s: not one filesystem's root", tc.target, s, a)
		}
	}
	if got, want := StagingDest(state, db, "r1", "/mnt/batcave/storage/config.yaml", "gap_fill"),
		filepath.Join(StagingDir(db, "r1"), "state", "gap_fill"); got != want {
		t.Errorf("a DB-side state file staged at %s, want StagingDir's state/ %s", got, want)
	}
	// A DB under the state directory (the compiled default), and the state
	// directory being the DB's parent: a state file under the state
	// directory is a state-directory target, and its label never lands on
	// the archive's staging/db.
	for _, d := range []string{state + "/storage/main.pebble", state + "/main.pebble"} {
		got := StagingDest(state, d, "r1", state+"/db", "db")
		if want := state + "/.mediacore-restore/r1/staging/state/db"; got != want {
			t.Errorf("DB %s: a state file labelled db staged at %s, want %s", d, got, want)
		}
		if got == filepath.Join(StagingDir(d, "r1"), "db") {
			t.Errorf("DB %s: the state file labelled db collides with the archive's store", d)
		}
	}
}

// asideRootOf is the path up to and including the .mediacore-restore or
// restore element of p, the root of a swap's private namespace.
func asideRootOf(p string) string {
	for d := p; filepath.Dir(d) != d; d = filepath.Dir(d) {
		if b := filepath.Base(d); b == asideDir || b == restoreDir {
			return d
		}
	}
	return ""
}

// TestAsideRootsHoldEveryStagingDest: every StagingDest of a restore's
// targets lies in one of AsideRoots (the state directory's private
// directory added for a target under it), and StagingRoots is AsideRoots
// without the journal directory, so a refused stager's cleanup reaches every
// file it staged and never removes restore/<id>.
func TestAsideRootsHoldEveryStagingDest(t *testing.T) {
	for _, l := range []struct{ name, state, db string }{
		{"apart", "/srv/gotham/state", "/mnt/batcave/storage/main.pebble"},
		{"DB under the state directory", "/srv/gotham/state", "/srv/gotham/state/storage/main.pebble"},
		{"state directory is the DB's parent", "/srv/gotham/state", "/srv/gotham/state/main.pebble"},
	} {
		targets := []string{l.state + "/.encryption_key", filepath.Dir(l.db) + "/config.yaml", "/media/arkham/oracle.key"}
		roots := AsideRoots(l.state, l.db, "r1", targets)
		staging := StagingRoots(l.state, l.db, "r1", targets)
		for i, tgt := range targets {
			dest := StagingDest(l.state, l.db, "r1", tgt, []string{"encryption_key", "gap_fill", "oracle"}[i])
			if !slices.ContainsFunc(roots, func(r string) bool { return InTree(dest, r) }) {
				t.Errorf("%s: %s staged at %s, in none of AsideRoots %v", l.name, tgt, dest, roots)
			}
			if !slices.ContainsFunc(staging, func(r string) bool { return InTree(dest, r) }) {
				t.Errorf("%s: %s staged at %s, in none of StagingRoots %v", l.name, tgt, dest, staging)
			}
		}
		if !slices.ContainsFunc(staging, func(r string) bool { return InTree(StagingDir(l.db, "r1"), r) }) {
			t.Errorf("%s: StagingRoots %v miss the store's StagingDir", l.name, staging)
		}
		if slices.Contains(staging, JournalDir(l.state, "r1")) {
			t.Errorf("%s: StagingRoots %v hold restore/r1, which Stage owns", l.name, staging)
		}
		if roots[0] != JournalDir(l.state, "r1") {
			t.Errorf("%s: AsideRoots %v do not start with the journal directory", l.name, roots)
		}
	}
	// Only a target under the state directory adds its private directory.
	if got := AsideRoots("/srv/gotham/state", "/mnt/batcave/storage/main.pebble", "r1", []string{"/mnt/batcave/storage/config.yaml"}); len(got) != 2 {
		t.Errorf("AsideRoots with a DB-side target alone = %v, want the journal directory and the DB parent's", got)
	}
}

// TestValidateTargetsAdmitsStateSideStaging: a staged item in the state
// directory's private directory (StagingDest) passes ValidateTargets in
// every layout, where one inside restore/ is refused.
func TestValidateTargetsAdmitsStateSideStaging(t *testing.T) {
	for _, l := range []struct{ name, state, db string }{
		{"apart", "/srv/gotham/state", "/mnt/batcave/storage/main.pebble"},
		{"DB under the state directory", "/srv/gotham/state", "/srv/gotham/state/storage/main.pebble"},
		{"state directory is the DB's parent", "/srv/gotham/state", "/srv/gotham/state/main.pebble"},
	} {
		key := l.state + "/.encryption_key"
		in := Intent{ID: "r1", Kind: KindCheckpoint, DBPath: l.db, Targets: []Target{
			{From: filepath.Join(StagingDir(l.db, "r1"), "db"), To: l.db},
			{From: StagingDest(l.state, l.db, "r1", key, "encryption_key"), To: key},
		}}
		if err := ValidateTargets(l.state, in, nil); err != nil {
			t.Errorf("%s: ValidateTargets = %v, want the state-side staging admitted", l.name, err)
		}
		in.Targets[1].From = filepath.Join(JournalDir(l.state, "r1"), "staging", "encryption_key")
		if err := ValidateTargets(l.state, in, nil); !errors.Is(err, ErrTargetsRefused) {
			t.Errorf("%s: a staged item inside restore/ = %v, want ErrTargetsRefused", l.name, err)
		}
	}
}

// TestValidatePassesStagingAcrossFilesystems: with the state directory on
// one device and the store on another, every rename a checkpoint restore
// makes passes Validate when each state file is staged at its StagingDest,
// and the old layout (every state file beside the store) is refused, which
// is what a packaged install's make rollback-restore met.
func TestValidatePassesStagingAcrossFilesystems(t *testing.T) {
	state, db := "/srv/gotham/state", "/mnt/batcave/storage/main.pebble"
	key := state + "/.encryption_key"
	p := fakeProbe{dev: map[string]uint64{}}
	for _, d := range []string{"/srv/gotham", "/mnt/batcave/storage"} {
		p.dev[d] = 1
	}
	for _, d := range []string{state, key, state + "/.mediacore-restore", state + "/.mediacore-restore/r1",
		state + "/.mediacore-restore/r1/staging", state + "/.mediacore-restore/r1/staging/state",
		state + "/.mediacore-restore/r1/staging/state/encryption_key"} {
		p.dev[d] = 2
	}
	staged := StagingDest(state, db, "r1", key, "encryption_key")
	in := Intent{ID: "r1", Kind: KindCheckpoint, DBPath: db, Targets: []Target{{From: staged, To: key}}}
	if err := Validate(p, append(SetAsides(state, in, nil), in.Targets...)); err != nil {
		t.Fatalf("Validate of the state-side staging = %v, want it to pass", err)
	}
	beside := filepath.Join(StagingDir(db, "r1"), "state", "encryption_key")
	for _, d := range []string{filepath.Dir(filepath.Dir(filepath.Dir(beside))), filepath.Dir(filepath.Dir(beside)), filepath.Dir(beside), beside} {
		p.dev[d] = 1
	}
	if err := Validate(p, []Target{{From: beside, To: key}}); !errors.Is(err, ErrRenameRefused) {
		t.Fatalf("Validate of a state file staged beside the store = %v, want ErrRenameRefused", err)
	}
}
