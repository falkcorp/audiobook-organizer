// file: core/storage/restorejournal/targets_test.go
// version: 1.0.0
// guid: 44005809-e493-40e3-9682-2e9208803d7a
// last-edited: 2026-11-08

package restorejournal

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestValidateTargets (05-storage §5S.3.15 "Stage"; R1-21 review m4, m5): a
// restore places a store at its DB path and a reset sets it aside to its
// SetAsideDest; no live path is relative, shared, in or holding restore/,
// holding the state directory, inside a swap's private directory or holding
// one (which takes in one target nested in another); a restore's staged
// item may lie in staging/ but not in restore/.
func TestValidateTargets(t *testing.T) {
	state, db := "/gotham/state", "/batcave/db/main.pebble"
	staged := filepath.Join(StagingDir(db, "r-archive"), "main.pebble")
	restore := func(targets ...Target) Intent {
		return Intent{ID: "r-archive", Kind: KindArchive, DBPath: db, Targets: targets}
	}
	reset := func(froms ...string) Intent {
		in := Intent{ID: "z-reset", Kind: KindReset, Reset: ResetFactory, DBPath: db}
		for _, p := range froms {
			in.Targets = append(in.Targets, Target{From: p, To: SetAsideDest(state, db, in.ID, p)})
		}
		return in
	}
	place := func(to string) Target { return Target{From: staged, To: to} }
	for _, tc := range []struct {
		name string
		in   Intent
		ok   bool
	}{
		{"restore the store", restore(place(db)), true},
		{"restore the store and a state file", restore(place(db), Target{From: staged + "-conf", To: filepath.Join(state, "config.boot.json")}), true},
		{"reset the store, its sidecar and a state file", reset(db, db+LevelsSuffix, filepath.Join(state, "config.boot.json"), "/media/arkham/oldb"), true},

		{"restore places nothing at db", restore(place(db + "-old")), false},
		{"restore places db twice", restore(place(db), Target{From: staged + "-2", To: db}), false},
		{"restore relative to", restore(place("main.pebble")), false},
		{"restore relative from", restore(Target{From: "staging/main.pebble", To: db}), false},
		{"restore staged item in restore/", restore(Target{From: filepath.Join(Dir(state), "r-archive", "main.pebble"), To: db}), false},
		{"restore staged item holds the state dir", restore(place(db), Target{From: "/gotham", To: "/batcave/db/gotham"}), false},
		{"restore target is restore/", restore(place(db), Target{From: staged + "-2", To: Dir(state)}), false},
		{"restore target inside restore/", restore(place(db), Target{From: staged + "-2", To: PendingPath(state)}), false},
		{"restore target holds the state dir", restore(place(db), Target{From: staged + "-2", To: "/gotham"}), false},
		{"restore target is the state dir", restore(place(db), Target{From: staged + "-2", To: state}), false},
		{"restore target in another swap's undo", restore(place(db), Target{From: staged + "-2", To: "/batcave/db/.mediacore-restore/r-older/previous/x"}), false},
		{"restore target in a foreign undo", restore(place(db), Target{From: staged + "-2", To: "/media/arkham/.mediacore-restore/r-older/previous/oldb"}), false},
		{"restore target holds the DB parent's undo", restore(place(db), Target{From: staged + "-2", To: "/batcave"}), false},
		{"restore targets nested", restore(place(db), Target{From: staged + "-2", To: "/media/arkham"}, Target{From: staged + "-3", To: "/media/arkham/oldb"}), false},

		{"reset leaves db", reset(db + LevelsSuffix), false},
		{"reset sets a path aside twice", reset(db, db), false},
		{"reset to off the one rule", Intent{ID: "z-reset", Kind: KindReset, DBPath: db, Targets: []Target{{From: db, To: "/batcave/elsewhere"}}}, false},
		{"reset holds the state dir", reset(db, "/gotham"), false},
		{"reset holds restore/", reset(db, Dir(state)), false},
		{"reset in restore/", reset(db, filepath.Join(Dir(state), "r-older", "journal.json")), false},
		{"reset holds the DB parent", reset(db, "/batcave/db"), false},
		{"reset in an undo", reset(db, "/batcave/db/.mediacore-restore/r-older/previous/main.pebble"), false},
		{"reset targets nested", reset(db, "/media/arkham", "/media/arkham/oldb"), false},
	} {
		err := ValidateTargets(state, tc.in, nil)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v, want accepted", tc.name, err)
		case !tc.ok && !errors.Is(err, ErrTargetsRefused):
			t.Errorf("%s: %v, want ErrTargetsRefused", tc.name, err)
		}
	}
}
