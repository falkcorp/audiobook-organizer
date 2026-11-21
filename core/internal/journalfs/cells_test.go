// file: core/internal/journalfs/cells_test.go
// version: 1.0.2
// guid: 02bc10f1-4901-4d10-9b06-e5540d68cde6
// last-edited: 2026-11-19

package journalfs_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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
// over the filesystem a crash just before one durable operation left (a new
// filesystem is a new lock set: nothing survives the crash).
func (e *env) after(c bootseamtest.Clone) *env {
	e.t.Helper()
	fsys, err := journalfs.From(bootseam.With(context.Background(), &bootseam.Seams{FS: c.FS, Shared: &bootseam.Shared{}}))
	if err != nil {
		e.t.Fatal(err)
	}
	return &env{t: e.t, st: e.st, rec: bootseamtest.New(c.FS, bootseamtest.Options{}), fsys: fsys}
}

// mixedSeeds are the generator seeds of the mixed-persistence clones every
// cell takes before each kill point, beside the minimal clone. The minimal
// clone keeps only what was synced, which for a sequence whose only directory
// fsync is its last operation is always the state before the call: a cell
// that ran on it alone could not tell an atomic replace from an in-place
// rewrite, or a copy fsynced before its rename from one fsynced after. The
// mixed ones keep each unsynced directory entry and data block with
// probability one half, which is where a torn file, an entry without its data
// and a rename that outlived its contents appear. The seeds are fixed but a
// run is not reproducible from them: Pebble's CrashClone draws from the
// generator while ranging over a directory's children, a Go map, so which
// entry gets which draw changes from run to run. Sixteen seeds make a cell
// that needs one particular draw (an entry kept, its data not) miss it with
// probability 0.75^16, under 1%; a failure in a mixed clone may not recur on a
// rerun.
var mixedSeeds = []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

// crashRun runs setup and then run over a crashable filesystem that takes a
// minimal clone and a mixed clone per seed before every kill point, and
// returns the env and the clones run took (the setup's own are dropped: only
// the site's kill points are the cells), then one more: the filesystem a crash
// just after run returned would leave (Recorder.Settled), which is where what
// the call acknowledged must already be durable.
func crashRun(t *testing.T, st store, setup, run func(e *env)) (*env, []bootseamtest.Clone) {
	t.Helper()
	e := newEnv(t, st, bootseamtest.Options{Clones: true, MixedSeeds: mixedSeeds})
	if setup != nil {
		setup(e)
	}
	skip := len(e.rec.Clones())
	run(e)
	return e, append(e.rec.Clones()[skip:], e.rec.Settled())
}

// isSettled reports whether c is the settled clone (the state after the last
// operation returned) rather than a kill point inside the call.
func isSettled(c bootseamtest.Clone) bool { return c.Event.Label == bootseamtest.LabelSettled }

// killPoints is the number of clones in cs that are crashes inside the call:
// the settled clone is always there and says nothing about how many durable
// steps the call took, so a guard on how many it took must not count it.
func killPoints(cs []bootseamtest.Clone) int {
	n := 0
	for _, c := range cs {
		if !isSettled(c) {
			n++
		}
	}
	return n
}

// cloneName is a subtest name for c: the operation it precedes, and which
// kind of clone it is.
func cloneName(c bootseamtest.Clone) string {
	kind := "minimal"
	switch {
	case c.Event.Label == bootseamtest.LabelSettled:
		return "settled"
	case c.Mixed:
		kind = fmt.Sprintf("mixed-%d", c.Seed)
	}
	return c.Event.Label + "/" + kindName(c.Event.Kind) + "/" + kind
}

// reached counts the outcomes a cell's clones came to, so the cell can fail
// when an arm it is written for is never taken: an arm no clone reaches checks
// nothing.
type reached map[string]int

// require fails unless every named outcome was reached at least once.
func (r reached) require(t *testing.T, outcomes ...string) {
	t.Helper()
	for _, o := range outcomes {
		if r[o] == 0 {
			t.Errorf("no clone reached the outcome %q (reached %v): the arm the cell is written for was never taken", o, map[string]int(r))
		}
	}
}

func exists(t *testing.T, fs vfs.FS, path string) bool {
	t.Helper()
	_, err := fs.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// readAll is the content of path on fsys.
func readAll(fsys vfs.FS, path string) ([]byte, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
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

// intactToOneOf is intactTo for a file whose end the crash left open between
// several chains (the unsynced append kept or lost): it returns the one it is.
func (e *env) intactToOneOf(path string, want ...uint64) uint64 {
	e.t.Helper()
	c, err := journal.ReadFile(path, keys{}, journal.WithFS(e.fsys))
	if err != nil || c.Break != nil {
		e.t.Fatalf("%s reads as %+v, %v; want an intact chain", path, c, err)
	}
	if !slices.Contains(want, c.IntactEnd()) {
		e.t.Fatalf("%s is intact to seq %d; want one of %v", path, c.IntactEnd(), want)
	}
	return c.IntactEnd()
}

// TestKill_LocationFileIsNeverTorn: a crash before any single durable step of
// WriteLocation, or just after it returned, leaves backup.location.json either
// absent, the earlier contents or the new ones, whole, and never an error from
// ReadLocation; the next WriteLocation then succeeds over whatever the crash
// left, a torn temporary file included. The crash states are the minimal one,
// mixed-persistence ones (mixedSeeds) and the settled one, and the cell fails
// unless every outcome it is written for was reached: the new file (so the
// rename was seen to outlive the crash, with its data), the older file or none,
// and a temporary file a crash left (so the next write's replace is tested).
func TestKill_LocationFileIsNeverTorn(t *testing.T) {
	st := newStore(t)
	older, newer := location("batgirl"), location("robin")
	for _, replacing := range []bool{false, true} {
		got := reached{}
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
		if killPoints(clones) < 4*(1+len(mixedSeeds)) {
			t.Fatalf("WriteLocation (replacing=%v) took %d crash states, want at least %d", replacing, killPoints(clones), 4*(1+len(mixedSeeds)))
		}
		for _, c := range clones {
			t.Run(fmt.Sprintf("replacing=%v/%s", replacing, cloneName(c)), func(t *testing.T) {
				e := (&env{t: t, st: st}).after(c)
				loc, ok, err := journal.ReadLocation(stateDir, journal.WithFS(e.fsys))
				if err != nil {
					t.Fatalf("ReadLocation after a crash before %s: %v", c.Event.Label, err)
				}
				switch {
				case ok && loc == newer:
					if !isSettled(c) {
						got["newer"]++ // the settled clone always holds it: it proves nothing about a crash inside the call
					}
				case ok && replacing && loc == older:
					got["older"]++
				case !ok && !replacing:
					got["absent"]++
				default:
					t.Fatalf("ReadLocation = %+v, %v; want absent, the earlier file or the new one", loc, ok)
				}
				if isSettled(c) {
					// WriteLocation returned: the new file is what a crash now finds.
					if !ok || loc != newer {
						t.Fatalf("after WriteLocation returned, a crash left %+v, %v; want the new file", loc, ok)
					}
					got["settled"]++
				}
				if exists(t, e.rec.FS(), stateDir+"/"+journal.LocationFile+".tmp") {
					got["temp left"]++
				}
				if err := journal.WriteLocation(stateDir, newer, journal.WithFS(e.fsys)); err != nil {
					t.Fatalf("the next WriteLocation: %v", err)
				}
				if loc, ok, err := journal.ReadLocation(stateDir, journal.WithFS(e.fsys)); err != nil || !ok || loc != newer {
					t.Fatalf("after the next write ReadLocation = %+v, %v, %v; want the new file", loc, ok, err)
				}
			})
		}
		if replacing {
			got.require(t, "older", "newer", "temp left", "settled")
		} else {
			got.require(t, "absent", "newer", "temp left", "settled")
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
// step of MoveFile, the copy's write and fsync included, or just after it
// returned, leaves the source journal whole at its old path, and the
// destination either absent or a whole copy; a rerun of MoveFile over what the
// crash left finishes the move. The cell fails unless both a destination and
// none, and a temporary copy a crash left, were reached: a destination that
// exists only in the settled state would leave the arm that checks it whole
// untested for every crash inside the call.
func TestKill_MoveFileLeavesTheSourceIntact(t *testing.T) {
	st := newStore(t)
	got := reached{}
	_, clones := crashRun(t, st, primed, func(e *env) {
		if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
			t.Fatal(err)
		}
	})
	if killPoints(clones) < 4*(1+len(mixedSeeds)) {
		t.Fatalf("MoveFile took %d crash states, want at least %d", killPoints(clones), 4*(1+len(mixedSeeds)))
	}
	for _, c := range clones {
		t.Run(cloneName(c), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			e.intactTo(journalAt, 2)
			dest := exists(t, e.rec.FS(), movedTo)
			switch {
			case isSettled(c):
				// MoveFile returned: the destination is what a crash now finds.
				// The clone is not counted in the arms below: it always holds it.
				if !dest {
					t.Fatal("after MoveFile returned, a crash lost the destination")
				}
				got["settled"]++
			case dest:
				got["destination"]++
			default:
				got["no destination"]++
			}
			if dest {
				e.intactTo(movedTo, 2)
			}
			if !isSettled(c) && exists(t, e.rec.FS(), movedTo+".moving") {
				got["temp copy left"]++
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
	got.require(t, "destination", "no destination", "temp copy left", "settled")
}

// TestKill_MoveFileMidCopySourceStillOpens: the crash that matters most, the
// one inside the copy (the copy's write is not durable until its fsync),
// leaves a source that OpenFile still opens and appends to, whatever of the
// copy the crash kept: nothing, its entry without its data, or both. The cell
// fails unless a crash kept the copy's entry (the premise that the source is
// not the only file there).
func TestKill_MoveFileMidCopySourceStillOpens(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, primed, func(e *env) {
		if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
			t.Fatal(err)
		}
	})
	got := reached{}
	for _, c := range clones {
		if c.Event.Label != journal.LabelMoveCopy || c.Event.Kind != errorfs.OpFileSync {
			continue
		}
		t.Run(cloneName(c), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			if exists(t, e.rec.FS(), movedTo+".moving") {
				got["copy entry kept"]++
			} else {
				got["no copy"]++
			}
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
		})
	}
	if len(got) == 0 {
		t.Fatal("no kill point before the copy's fsync")
	}
	got.require(t, "copy entry kept", "no copy")
}

// TestKill_AppendNeverLosesAnAcknowledgedLine: a crash before the durable step
// of an Append (its fsync) leaves a journal that OpenFile opens, holding every
// line acknowledged before it (seq 2) and the line in flight or not; and a
// crash just after Append returned, the settled clone, holds the line it
// acknowledged (seq 3). The cell fails unless the Append took its kill point
// (a call with no fsync takes none) and both a lost and a kept line were
// reached inside it.
func TestKill_AppendNeverLosesAnAcknowledgedLine(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, func(e *env) { primed(e); e.holdOpen(journalAt) }, func(e *env) {
		if err := e.appendUncommitted(e.held); err != nil {
			t.Fatal(err)
		}
	})
	if killPoints(clones) < 1+len(mixedSeeds) {
		t.Fatalf("Append took %d crash states, want at least %d (its fsync is the kill point)", killPoints(clones), 1+len(mixedSeeds))
	}
	got := reached{}
	for _, c := range clones {
		t.Run(cloneName(c), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			cs, err := journal.ReadFile(journalAt, keys{}, journal.WithFS(e.fsys))
			if err != nil || cs.Break != nil || cs.IntactEnd() < 2 || cs.IntactEnd() > 3 {
				t.Fatalf("chain = %+v, %v; want intact to seq 2 or 3", cs, err)
			}
			switch {
			case isSettled(c):
				// Append returned: its line is durable.
				if cs.IntactEnd() != 3 {
					t.Fatalf("after Append returned, a crash left the chain at seq %d; want 3, the line it acknowledged", cs.IntactEnd())
				}
				got["settled"]++
			case cs.IntactEnd() == 2:
				got["line lost"]++
			default:
				got["line kept"]++
			}
			j, err := e.open(journalAt)
			if err != nil {
				t.Fatalf("OpenFile after a crash before %s: %v", c.Event.Label, err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	got.require(t, "line lost", "line kept", "settled")
}

// TestKill_FirstOpenRecovers: a crash before any single durable step of the
// first OpenFile (create, header, directory fsync) and of the append after it,
// or just after it, leaves either no journal or one the next OpenFile accepts
// and appends to. The unsynced append may have survived (a mixed clone) or
// not, so the chain after the next append is at seq 1 or 2; the cell fails
// unless both were reached (and the journal started with no header too). A
// crash just after the open and its append returned, the settled clone, holds
// the journal with the line the append acknowledged (seq 1): the directory
// fsync made the file's name durable. The settled clone is not counted in the
// arms: it always holds the journal.
func TestKill_FirstOpenRecovers(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, nil, func(e *env) {
		j := e.mustOpen(journalAt)
		if err := e.appendUncommitted(j); err != nil {
			t.Fatal(err)
		}
	})
	if killPoints(clones) < 4*(1+len(mixedSeeds)) {
		t.Fatalf("the first open took %d crash states, want at least %d", killPoints(clones), 4*(1+len(mixedSeeds)))
	}
	got := reached{}
	for _, c := range clones {
		t.Run(cloneName(c), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			switch {
			case isSettled(c):
				cs, err := journal.ReadFile(journalAt, keys{}, journal.WithFS(e.fsys))
				if err != nil || cs.Break != nil || cs.IntactEnd() != 1 {
					t.Fatalf("after the first open and its append returned, a crash left %+v, %v; want a journal intact to seq 1", cs, err)
				}
				got["settled"]++
			case !exists(t, e.rec.FS(), journalAt):
				got["no journal"]++
			}
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
			switch end := e.intactToOneOf(journalAt, 1, 2); {
			case isSettled(c):
			case end == 1:
				got["append lost"]++
			default:
				got["append kept"]++
			}
		})
	}
	got.require(t, "no journal", "append lost", "append kept", "settled")
}

// TestKill_CutOfATornTailIsAtomic: the cut of an incomplete last line is a
// truncate; a crash before any single durable step of it, mixed-persistence
// states included, or just after it, leaves a journal OpenFile still opens
// whole at seq 2, cut or not yet cut. A real truncate leaves the old length or
// the new one and never a third; the cell fails unless it saw both the torn
// tail still there and the cut done, and a temporary file of the cut left.
func TestKill_CutOfATornTailIsAtomic(t *testing.T) {
	st := newStore(t)
	_, clones := crashRun(t, st, func(e *env) { primed(e); e.tearTail(journalAt) }, func(e *env) {
		j := e.mustOpen(journalAt)
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if killPoints(clones) < 4*(1+len(mixedSeeds)) {
		t.Fatalf("the cut took %d crash states, want at least %d", killPoints(clones), 4*(1+len(mixedSeeds)))
	}
	got := reached{}
	for _, c := range clones {
		t.Run(cloneName(c), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			if raw, err := readAll(e.rec.FS(), journalAt); err != nil {
				t.Fatal(err)
			} else if len(raw) > 0 && raw[len(raw)-1] == '\n' {
				got["cut done"]++
			} else {
				got["torn tail"]++
			}
			if exists(t, e.rec.FS(), journalAt+".truncating") {
				// The cut's temporary file is only ever replaced, never read.
				got["temp left"]++
			}
			j, err := e.open(journalAt)
			if err != nil {
				t.Fatalf("OpenFile after a crash before %s: %v", c.Event.Label, err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			e.intactTo(journalAt, 2)
		})
	}
	got.require(t, "torn tail", "cut done", "temp left")
}

// TestMemFS_MoveFileOntoItsVerifiedCopy: a destination that is already a
// regular file holding the source's chain is the copy an earlier run finished
// (a crash between the rename and the record update): MoveFile reads it
// through the filesystem it was given, finds it verified and writes nothing,
// and a regular file that is not that copy is refused. Both reads (the
// destination's Lstat and ReadFile) must be the memory filesystem's, since the
// host's disk holds neither file.
func TestMemFS_MoveFileOntoItsVerifiedCopy(t *testing.T) {
	e := newEnv(t, newStore(t), noOpts())
	primed(e)
	if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
		t.Fatal(err)
	}
	before := len(e.rec.Events())
	if err := journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)); err != nil {
		t.Fatalf("a repeated move onto the verified copy: %v", err)
	}
	if evs := e.since(before); len(evs) != 0 {
		t.Fatalf("a repeated move onto the verified copy wrote %v; want nothing", summarize(evs))
	}
	e.intactTo(movedTo, 2)

	notACopy := movedTo + ".other"
	e.plant(notACopy, "not a journal\n")
	if err := journal.MoveFile(journalAt, notACopy, keys{}, journal.WithFS(e.fsys)); !errors.Is(err, journal.ErrMoveRefused) {
		t.Fatalf("a move onto a regular file that is not the source's copy: err = %v, want ErrMoveRefused", err)
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

// TestKill_TrimLeavesAWholeJournal: a crash before any single durable step of
// File.Trim, or just after it returned, leaves the journal at its name either as
// it was or trimmed, whole in both cases: an intact chain to the same last
// line (5, which is the store's committed head, so OpenFile accepts it), never
// a torn or half-replaced file. A temporary file the crash left is cleared by the
// next trim, which then finishes the cut, and the journal takes a line after it.
// The cell fails unless every outcome it is written for was reached: the old
// journal and the trimmed one (the rename seen to outlive a crash, with its data),
// a temporary file left, and the settled state.
func TestKill_TrimLeavesAWholeJournal(t *testing.T) {
	st := newStore(t) // its head is 5 and no other test's journal shares it
	got := reached{}
	_, clones := crashRun(t, st, func(e *env) { committedLines(e, 5) }, func(e *env) {
		j := e.mustOpen(journalAt)
		res, err := j.Trim(context.Background(), 3)
		if err != nil || res.Base != 3 || res.Dropped != 3 || res.Kept != 2 {
			t.Fatalf("Trim = %+v, %v", res, err)
		}
	})
	if killPoints(clones) < 4*(1+len(mixedSeeds)) {
		t.Fatalf("Trim took %d crash states, want at least %d (the copy's write and fsync, the rename, the directory fsync)", killPoints(clones), 4*(1+len(mixedSeeds)))
	}
	for _, c := range clones {
		t.Run(cloneName(c), func(t *testing.T) {
			e := (&env{t: t, st: st}).after(c)
			e.intactTo(journalAt, 5)
			ch, err := journal.ReadFile(journalAt, keys{}, journal.WithFS(e.fsys))
			if err != nil {
				t.Fatal(err)
			}
			switch ch.Header.BaseSeq {
			case 0:
				if len(ch.Lines) != 5 || ch.Header.ChainBase {
					t.Fatalf("the old journal reads as %d lines, chain_base %v", len(ch.Lines), ch.Header.ChainBase)
				}
				if isSettled(c) {
					t.Fatal("after Trim returned, a crash brought the old journal back")
				}
				got["old journal"]++
			case 3:
				if len(ch.Lines) != 2 || ch.Lines[0].Seq != 4 || !ch.Header.ChainBase {
					t.Fatalf("the trimmed journal reads as %d lines from seq %d, chain_base %v", len(ch.Lines), ch.Lines[0].Seq, ch.Header.ChainBase)
				}
				if !isSettled(c) {
					got["trimmed journal"]++
				}
			default:
				t.Fatalf("base_seq %d is neither 0 (old) nor 3 (trimmed)", ch.Header.BaseSeq)
			}
			if isSettled(c) {
				got["settled"]++
			} else if exists(t, e.rec.FS(), journalAt+".trimming") {
				got["temp left"]++
			}

			j, err := e.open(journalAt)
			if err != nil {
				t.Fatalf("OpenFile after the crash: %v", err)
			}
			t.Cleanup(func() { _ = j.Close() })
			res, err := j.Trim(context.Background(), 3)
			if err != nil || res.Base != 3 || res.Kept != 2 {
				t.Fatalf("the rerun of Trim = %+v, %v", res, err)
			}
			if exists(t, e.rec.FS(), journalAt+".trimming") {
				t.Fatal("a temporary file survived the rerun")
			}
			if err := e.appendUncommitted(j); err != nil {
				t.Fatalf("a line after the rerun: %v", err)
			}
			e.intactTo(journalAt, 6)
		})
	}
	got.require(t, "old journal", "trimmed journal", "temp left", "settled")
}
