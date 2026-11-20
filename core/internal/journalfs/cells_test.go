// file: core/internal/journalfs/cells_test.go
// version: 1.0.0
// guid: 02bc10f1-4901-4d10-9b06-e5540d68cde6
// last-edited: 2026-11-19

package journalfs_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/journalfs"
)

// after is a boot that restarts over a crash clone: a fresh journalfs adapter
// (the lock set is per boot) over the filesystem a crash just before one
// durable operation left.
func (e *env) after(c bootseamtest.Clone) *env {
	e.t.Helper()
	fsys, err := journalfs.From(bootseam.With(context.Background(), &bootseam.Seams{FS: c.FS}))
	if err != nil {
		e.t.Fatal(err)
	}
	return &env{t: e.t, st: e.st, rec: bootseamtest.New(c.FS, bootseamtest.Options{}), fsys: fsys}
}

// crashRun runs setup and then run over a crashable filesystem that takes a
// clone before every kill point, and returns the env and the clones run took
// (the setup's own are dropped: only the site's kill points are the cells).
func crashRun(t *testing.T, st store, setup, run func(e *env)) (*env, []bootseamtest.Clone) {
	t.Helper()
	e := newEnv(t, st, bootseamtest.Options{Clones: true})
	if setup != nil {
		setup(e)
	}
	skip := len(e.rec.Clones())
	run(e)
	return e, e.rec.Clones()[skip:]
}

func exists(t *testing.T, fs vfs.FS, path string) bool {
	t.Helper()
	_, err := fs.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// intactTo fails unless the journal at path reads as an intact chain whose
// last seq is want.
func (e *env) intactTo(path string, want uint64) {
	e.t.Helper()
	c, err := journal.ReadFile(path, keys{}, journal.WithFS(e.fsys))
	if err != nil || c.Break != nil || c.IntactEnd() != want {
		e.t.Fatalf("%s reads as %+v, %v; want an intact chain to seq %d", path, c, err, want)
	}
}

// TestKill_LocationFileIsNeverTorn: a crash before any single durable step of
// WriteLocation leaves backup.location.json either absent, the earlier
// contents or the new ones, whole, and never an error from ReadLocation; the
// next WriteLocation then succeeds over whatever the crash left, a torn
// temporary file included.
func TestKill_LocationFileIsNeverTorn(t *testing.T) {
	st := newStore(t)
	older, newer := location("batgirl"), location("robin")
	for _, replacing := range []bool{false, true} {
		_, clones := crashRun(t, st, func(e *env) {
			if replacing {
				if err := journal.WriteLocation(stateDir, older, journal.WithFS(e.fsys)); err != nil {
					t.Fatal(err)
				}
			}
		}, func(e *env) {
			if err := journal.WriteLocation(stateDir, newer, journal.WithFS(e.fsys)); err != nil {
				t.Fatal(err)
			}
		})
		if len(clones) < 4 {
			t.Fatalf("WriteLocation (replacing=%v) took %d kill points, want at least 4", replacing, len(clones))
		}
		for _, c := range clones {
			t.Run(c.Event.Label+"/"+kindName(c.Event.Kind), func(t *testing.T) {
				e := (&env{t: t, st: st}).after(c)
				got, ok, err := journal.ReadLocation(stateDir, journal.WithFS(e.fsys))
				if err != nil {
					t.Fatalf("ReadLocation after a crash before %s: %v", c.Event.Label, err)
				}
				switch {
				case ok && got == newer, ok && replacing && got == older, !ok && !replacing:
				default:
					t.Fatalf("ReadLocation = %+v, %v; want absent, the earlier file or the new one", got, ok)
				}
				if err := journal.WriteLocation(stateDir, newer, journal.WithFS(e.fsys)); err != nil {
					t.Fatalf("the next WriteLocation: %v", err)
				}
				if got, ok, err := journal.ReadLocation(stateDir, journal.WithFS(e.fsys)); err != nil || !ok || got != newer {
					t.Fatalf("after the next write ReadLocation = %+v, %v, %v; want the new file", got, ok, err)
				}
			})
		}
	}
}

// TestKill_TornLocationTempIsIgnoredAndReplaced: a temporary file a crash left
// half written is never read as the location, and the next WriteLocation
// replaces it rather than appending to it.
func TestKill_TornLocationTempIsIgnoredAndReplaced(t *testing.T) {
	st := newStore(t)
	e := newEnv(t, st, bootseamtest.Options{})
	if err := journal.WriteLocation(stateDir, location("batgirl"), journal.WithFS(e.fsys)); err != nil {
		t.Fatal(err)
	}
	e.plant(stateDir+"/"+journal.LocationFile+".tmp", `{"backup_dir":"/gotham/back`)
	got, ok, err := journal.ReadLocation(stateDir, journal.WithFS(e.fsys))
	if err != nil || !ok || got != location("batgirl") {
		t.Fatalf("ReadLocation with a torn temporary file beside it = %+v, %v, %v; want the earlier file", got, ok, err)
	}
	if err := journal.WriteLocation(stateDir, location("robin"), journal.WithFS(e.fsys)); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := journal.ReadLocation(stateDir, journal.WithFS(e.fsys)); err != nil || !ok || got != location("robin") {
		t.Fatalf("ReadLocation after the write = %+v, %v, %v; want the new file", got, ok, err)
	}
	if exists(t, e.rec.FS(), stateDir+"/"+journal.LocationFile+".tmp") {
		t.Fatal("the torn temporary file survived the next write")
	}
}

// TestKill_MoveFileLeavesTheSourceIntact: a crash before any single durable
// step of MoveFile, the copy's write and fsync included, leaves the source
// journal whole at its old path, and the destination either absent or a whole
// copy; a rerun of MoveFile over what the crash left finishes the move.
func TestKill_MoveFileLeavesTheSourceIntact(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, primed, func(e *env) {
		if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
			t.Fatal(err)
		}
	})
	if len(clones) < 4 {
		t.Fatalf("MoveFile took %d kill points, want at least 4", len(clones))
	}
	for _, c := range clones {
		t.Run(c.Event.Label+"/"+kindName(c.Event.Kind), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			e.intactTo(journalAt, 2)
			if exists(t, e.rec.FS(), movedTo) {
				e.intactTo(movedTo, 2)
			}
			if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
				t.Fatalf("rerunning MoveFile: %v", err)
			}
			e.intactTo(movedTo, 2)
			if exists(t, e.rec.FS(), movedTo+".moving") {
				t.Fatal("a temporary copy survived the rerun")
			}
		})
	}
}

// TestKill_MoveFileMidCopySourceStillOpens: the crash that matters most, the
// one inside the copy (the copy's write is durable, its fsync is not), leaves
// a source that OpenFile still opens and appends to.
func TestKill_MoveFileMidCopySourceStillOpens(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, primed, func(e *env) {
		if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
			t.Fatal(err)
		}
	})
	var mid *bootseamtest.Clone
	for i, c := range clones {
		if c.Event.Label == journal.LabelMoveCopy && c.Event.Kind == errorfs.OpFileSync {
			mid = &clones[i]
		}
	}
	if mid == nil {
		t.Fatal("no kill point before the copy's fsync")
	}
	e := (&env{t: t, st: st}).after(*mid)
	j, err := e.open(journalAt)
	if err != nil {
		t.Fatalf("OpenFile of the source after a crash mid-copy: %v", err)
	}
	if err := e.appendUncommitted(j); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	e.intactTo(journalAt, 3)
}

// TestKill_AppendNeverLosesAnAcknowledgedLine: a crash before any single
// durable step of an Append leaves a journal that OpenFile opens, holding
// every line acknowledged before it (seq 2) and at most the line in flight.
func TestKill_AppendNeverLosesAnAcknowledgedLine(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, func(e *env) { primed(e); e.holdOpen(journalAt) }, func(e *env) {
		if err := e.appendUncommitted(e.held); err != nil {
			t.Fatal(err)
		}
	})
	if len(clones) == 0 {
		t.Fatal("Append took no kill point")
	}
	for _, c := range clones {
		t.Run(c.Event.Label+"/"+kindName(c.Event.Kind), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			j, err := e.open(journalAt)
			if err != nil {
				t.Fatalf("OpenFile after a crash before %s: %v", c.Event.Label, err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			cs, err := journal.ReadFile(journalAt, keys{}, journal.WithFS(e.fsys))
			if err != nil || cs.Break != nil || cs.IntactEnd() < 2 || cs.IntactEnd() > 3 {
				t.Fatalf("chain = %+v, %v; want intact to seq 2 or 3", cs, err)
			}
		})
	}
}

// TestKill_FirstOpenRecovers: a crash before any single durable step of the
// first OpenFile (create, header, directory fsync) leaves either no journal
// or one the next OpenFile accepts and appends to.
func TestKill_FirstOpenRecovers(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, nil, func(e *env) {
		j := e.mustOpen(journalAt)
		if err := e.appendUncommitted(j); err != nil {
			t.Fatal(err)
		}
	})
	if len(clones) < 3 {
		t.Fatalf("the first open took %d kill points, want at least 3", len(clones))
	}
	for _, c := range clones {
		t.Run(c.Event.Label+"/"+kindName(c.Event.Kind), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			j, err := e.open(journalAt)
			if err != nil {
				t.Fatalf("OpenFile after a crash before %s: %v", c.Event.Label, err)
			}
			if err := e.appendUncommitted(j); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			e.intactTo(journalAt, 1)
		})
	}
}

// TestKill_CutOfATornTailIsAtomic: the cut of an incomplete last line is a
// truncate, which the in-memory file performs as an atomic replace; a crash
// before any single durable step of it leaves a journal OpenFile still opens
// whole at seq 2, cut or not yet cut.
func TestKill_CutOfATornTailIsAtomic(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, func(e *env) { primed(e); e.tearTail(journalAt) }, func(e *env) {
		j := e.mustOpen(journalAt)
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if len(clones) < 4 {
		t.Fatalf("the cut took %d kill points, want at least 4", len(clones))
	}
	for _, c := range clones {
		t.Run(c.Event.Label+"/"+kindName(c.Event.Kind), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			j, err := e.open(journalAt)
			if err != nil {
				t.Fatalf("OpenFile after a crash before %s: %v", c.Event.Label, err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			e.intactTo(journalAt, 2)
			if exists(t, e.rec.FS(), journalAt+".truncating") {
				// The cut's temporary file is only ever replaced, never read.
				t.Log("a temporary file of the cut survived the crash; the next cut replaces it")
			}
		})
	}
}

// TestMemFS_RefusesNonRegularJournals: a directory at the journal's name is
// not a journal: OpenFile fails with the open error ("is a directory", the
// message open(2) gives for EISDIR), and MoveFile refuses a destination that
// is a directory, as the same checks do over the real filesystem.
func TestMemFS_RefusesNonRegularJournals(t *testing.T) {
	e := newEnv(t, newStore(t), noOpts())
	if err := e.rec.Mem().MkdirAll(journalAt, 0o700); err != nil {
		t.Fatal(err)
	}
	if j, err := e.open(journalAt); err == nil || !strings.Contains(err.Error(), "is a directory") {
		if j != nil {
			_ = j.Close()
		}
		t.Fatalf("OpenFile of a directory: err = %v, want an \"is a directory\" open error", err)
	}
	other := stateDir + "/other.journal"
	j := e.mustOpen(other)
	if err := e.appendUncommitted(j); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.MoveFile(other, journalAt, keys{}, journal.WithFS(e.fsys)); !errors.Is(err, journal.ErrMoveRefused) || !errors.Is(err, journal.ErrNotRegular) {
		t.Fatalf("MoveFile onto a directory: err = %v, want ErrMoveRefused wrapping ErrNotRegular", err)
	}
}
