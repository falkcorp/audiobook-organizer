// file: core/coretest/crashmatrix/invariants.go
// version: 1.0.0
// guid: bc10682b-4c38-446d-a7dd-4fb4b2cc676b
// last-edited: 2026-11-08

package crashmatrix

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The R1 subset of 05-storage §5S.3.16's invariants, checked after every
// broken boot's successors (and I3's first half at every kill point):
//
//   - I1 and I4 are the cutover's (no row lost or doubled by a cutover step;
//     the level sidecar above PrevLevels from M7's sidecar write on). R1 has
//     no cutover step and no level step above PrevLevels, so both are
//     vacuous until F3-04 adds the steps and their checks here.
//   - I2: every swap journal ends terminal (applied, swapped_back, rejected,
//     hook_done), or at failed for KindCheckpoint; restore/<id>/inflight.json
//     exists exactly while it is not terminal; no pending.json is left; and
//     the DB path holds one whole store: its module rows are the fault-free
//     run's, or, for a fixture with a staged swap, those of the store the
//     swap would have replaced (the swap rejected or swapped back), never a
//     mix (fixtureRun.legal).
//   - I3: the level sidecar equals the stamps after recovery, and at every
//     kill point it is never below a stamp (machine 2: sidecar first).
//   - I5: readiness within maxBoots boots, or the fixture's WantRefused.
//   - I6: every checkpoint record names a complete checkpoint directory, no
//     partial one is left, a crash loop leaves no more checkpoints than the
//     fault-free run, and every checkpoint holds the module rows of one the
//     fault-free run took (none holds a row written after it).

// terminal is machine 1's end states.
var terminal = map[string]bool{
	restoreswap.StepApplied: true, restoreswap.StepSwappedBack: true,
	restoreswap.StepRejected: true, restoreswap.StepHookDone: true,
}

// outcome is what a run left on its filesystem.
type outcome struct {
	err         error
	store       bool
	rows        string // digest of every module row (m/)
	stamps      map[string]int
	sidecar     map[string]int
	journals    map[string]journal
	pending     bool
	checkpoints []string // the module-row digest of each complete checkpoint, sorted
	partial     []string // checkpoint directories without a record
	orphans     []string // records naming no complete directory
}

type journal struct {
	kind     restorejournal.Kind
	last     string
	inflight bool
}

// storageStamps is c/storage/level/<owner>'s prefix (05-storage §5S.3.7).
var storageStamps = append([]byte("c/storage/"), keys.Join(keys.Str("level"))...)

// zeroRows is the module-row digest of a store with no module row.
var zeroRows = fmt.Sprintf("0 rows %x", sha256.New().Sum(nil)[:8])

type stampRecord struct {
	Level int `json:"level"`
}

// read inspects the store, its sidecar, the swap slot and the checkpoints.
func read(mem *vfs.MemFS, f Fixture) outcome {
	var o outcome
	db := f.dbPath()
	o.store, o.rows, o.stamps, o.err = readStore(mem, db, true)
	if o.err != nil {
		return o
	}
	if o.store {
		if o.sidecar, _, o.err = ipebble.ReadLevels(mem, db); o.err != nil {
			return o
		}
	}
	if o.journals, o.pending, o.err = readSlot(mem, f.StateDir); o.err != nil {
		return o
	}
	o.checkpoints, o.partial, o.orphans, o.err = readCheckpoints(mem, filepath.Join(filepath.Dir(db), ".mediacore-checkpoints"))
	return o
}

// readStore opens the store at path read-only: its module rows' digest and,
// with stamps, its level stamps. ok is false when no store is there.
func readStore(fsys vfs.FS, path string, stamps bool) (ok bool, rows string, levels map[string]int, err error) {
	if _, err := fsys.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, "", nil, nil
	}
	eng, err := ipebble.Open(path, ipebble.Options{FS: fsys, ReadOnly: true, ErrorIfNotExists: true})
	if errors.Is(err, cpebble.ErrDBDoesNotExist) {
		return false, "", nil, nil // the directory, before Pebble's first MANIFEST
	}
	if err != nil {
		return false, "", nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	defer func() { err = errors.Join(err, eng.Close(10*time.Second)) }()
	it, err := eng.DB().NewIter(nil)
	if err != nil {
		return false, "", nil, err
	}
	h := sha256.New()
	levels = map[string]int{}
	n := 0
	for valid := it.First(); valid; valid = it.Next() {
		k := it.Key()
		switch {
		case bytes.HasPrefix(k, []byte("m/")):
			v, verr := it.ValueAndErr()
			if verr != nil {
				return false, "", nil, errors.Join(verr, it.Close())
			}
			fmt.Fprintf(h, "%q=%q\n", k, v)
			n++
		case stamps && bytes.HasPrefix(k, storageStamps):
			segs, serr := keys.Split(k[len("c/storage/"):])
			v, verr := it.ValueAndErr()
			if serr != nil || verr != nil || len(segs) != 2 {
				return false, "", nil, errors.Join(fmt.Errorf("stamp key %q unreadable", k), serr, verr, it.Close())
			}
			rec, derr := codec.JSON[stampRecord](1).Decode(v)
			if derr != nil {
				return false, "", nil, errors.Join(derr, it.Close())
			}
			levels[string(segs[1])] = rec.Level
		}
	}
	if err := it.Close(); err != nil {
		return false, "", nil, err
	}
	return true, fmt.Sprintf("%d rows %x", n, h.Sum(nil)[:8]), levels, nil
}

// readSlot reads every journal under <state_dir>/restore/ and whether an
// intent is still pending.
func readSlot(fsys vfs.FS, stateDir string) (map[string]journal, bool, error) {
	dir := restorejournal.Dir(stateDir)
	names, err := fsys.List(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	out := map[string]journal{}
	pending := false
	flying, kind, _, _, err := restoreswap.InFlightJournal(fsys, stateDir)
	if err != nil {
		return nil, false, err
	}
	for _, n := range names {
		p := fsys.PathJoin(dir, n)
		fi, err := fsys.Stat(p)
		if err != nil {
			return nil, false, err
		}
		if !fi.IsDir() {
			pending = pending || p == restorejournal.PendingPath(stateDir)
			continue
		}
		recs, err := restoreswap.ReadJournal(fsys, stateDir, n)
		if errors.Is(err, fs.ErrNotExist) {
			continue // a directory with no journal: the stager's, or a removed swap's leftovers
		}
		if err != nil {
			return nil, false, fmt.Errorf("journal %s: %w", n, err)
		}
		j := journal{}
		if len(recs) > 0 {
			j.last = recs[len(recs)-1].Step
		}
		_, serr := fsys.Stat(fsys.PathJoin(p, restorejournal.InflightName))
		j.inflight = serr == nil
		if n == flying {
			j.kind = kind // only an in-flight journal's kind matters: failed is legal for KindCheckpoint
		}
		out[n] = j
	}
	return out, pending, nil
}

// readCheckpoints reads the checkpoint directory: the module-row digest of
// each complete checkpoint, the directories with no record, and the records
// with no complete directory.
func readCheckpoints(fsys vfs.FS, dir string) (complete, partial, orphans []string, err error) {
	names, err := fsys.List(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, n := range names {
		switch {
		case strings.HasSuffix(n, ".json"):
			if base := strings.TrimSuffix(n, ".json"); !have[base] {
				orphans = append(orphans, n)
			}
		case !have[n+".json"]:
			partial = append(partial, n)
		default:
			ok, rows, _, err := readStore(fsys, fsys.PathJoin(dir, n, "db"), false)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("checkpoint %s: %w", n, err)
			}
			if !ok {
				orphans = append(orphans, n+" (no db)")
				continue
			}
			complete = append(complete, rows)
		}
	}
	slices.Sort(complete)
	return complete, partial, orphans, nil
}

// atKillPoint is I3's first half on a crash clone: the level sidecar is
// never below a committed stamp.
func (fr *fixtureRun) atKillPoint(at string, fsys vfs.FS) {
	db := fr.f.dbPath()
	ok, _, stamps, err := readStore(fsys, db, true)
	if err != nil {
		fr.errorf("%s: I3: the store at the kill point: %v", at, err)
		return
	}
	if !ok {
		return
	}
	side, sideOK, err := ipebble.ReadLevels(fsys, db)
	if err != nil {
		fr.errorf("%s: I3: the level sidecar at the kill point: %v", at, err)
		return
	}
	if !sideOK {
		if len(stamps) > 0 && maxLevel(stamps) > 0 {
			fr.errorf("%s: I3: stamps %v with no level sidecar", at, stamps)
		}
		return
	}
	for o, l := range stamps {
		if side[o] < l {
			fr.errorf("%s: I3: the level sidecar has %s at %d, below its stamp %d (machine 2 writes the sidecar first)", at, o, side[o], l)
		}
	}
}

func maxLevel(m map[string]int) int {
	n := 0
	for _, v := range m {
		n = max(n, v)
	}
	return n
}

// check asserts I2, I3, I5 and I6 on a run that broke a boot at at.
func (fr *fixtureRun) check(at string, ch chain) {
	if !ch.ended(fr.f) {
		want := "readiness"
		if fr.f.WantRefused != nil {
			want = fmt.Sprintf("the refusal %v", fr.f.WantRefused)
		}
		fr.errorf("%s: I5: no %s within %d boots: %v", at, want, maxBoots, ch.errs())
		return
	}
	o := ch.outcome
	if o.err != nil {
		fr.errorf("%s: reading the result: %v", at, o.err)
		return
	}
	for _, msg := range fr.violations(o) {
		fr.errorf("%s: %s", at, msg)
	}
	if fr.f.Check != nil {
		if err := fr.f.Check(ch.mem); err != nil {
			fr.errorf("%s: %v", at, err)
		}
	}
}

// violations lists o's breaches of I2, I3 and I6.
func (fr *fixtureRun) violations(o outcome) []string {
	var out []string
	for id, j := range o.journals {
		done := terminal[j.last]
		failed := j.last == restoreswap.StepFailed && j.kind == restorejournal.KindCheckpoint
		switch {
		case !done && !failed:
			out = append(out, fmt.Sprintf("I2: journal %s stopped at %q, which is not terminal", id, j.last))
		case j.inflight == done:
			out = append(out, fmt.Sprintf("I2: journal %s at %q with inflight.json present=%v", id, j.last, j.inflight))
		}
	}
	if o.pending {
		out = append(out, "I2: pending.json is still staged after recovery")
	}
	legal := false
	for _, l := range fr.legal {
		legal = legal || (l.store == o.store && l.rows == o.rows)
	}
	if !legal {
		out = append(out, fmt.Sprintf("I2: the DB path holds %s; the legal stores hold %s", rowsOf(o), fr.legalRows()))
	}
	if o.store {
		want := map[string]int{}
		maps.Copy(want, o.stamps)
		for k := range o.sidecar {
			want[k] += 0
		}
		for k := range want {
			if o.sidecar[k] != o.stamps[k] {
				out = append(out, fmt.Sprintf("I3: after recovery the level sidecar %v differs from the stamps %v", o.sidecar, o.stamps))
				break
			}
		}
	}
	if len(o.orphans) > 0 {
		out = append(out, fmt.Sprintf("I6: checkpoint records with no complete directory: %v", o.orphans))
	}
	if len(o.partial) > 0 {
		out = append(out, fmt.Sprintf("I6: partial checkpoint directories left: %v", o.partial))
	}
	// A crash loop leaves at most one checkpoint: the fault-free run's, or,
	// after a kill that makes the next boot an upgrade boot (between the
	// fresh-install batch and its seed step, say), the one that boot takes.
	most, known := 1, map[string]bool{fr.initial: true}
	for _, l := range fr.legal {
		most = max(most, len(l.checkpoints))
		for _, c := range l.checkpoints {
			known[c] = true
		}
	}
	if len(o.checkpoints) > most {
		out = append(out, fmt.Sprintf("I6: %d checkpoints, where a crash loop reuses one (the fault-free run leaves %d)", len(o.checkpoints), most))
	}
	for _, c := range o.checkpoints {
		if !known[c] {
			out = append(out, fmt.Sprintf("I6: a checkpoint holds %s: neither the fixture's rows nor a fault-free checkpoint's, so it holds a row written after it", c))
		}
	}
	return out
}

func rowsOf(o outcome) string {
	if !o.store {
		return "no store"
	}
	return o.rows
}

func (fr *fixtureRun) legalRows() string {
	var s []string
	for _, l := range fr.legal {
		s = append(s, rowsOf(l))
	}
	return strings.Join(s, " or ")
}
