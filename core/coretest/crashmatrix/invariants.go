// file: core/coretest/crashmatrix/invariants.go
// version: 1.0.4
// guid: bc10682b-4c38-446d-a7dd-4fb4b2cc676b
// last-edited: 2026-11-08

package crashmatrix

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
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
//     the DB path holds one whole store, the one the journal's end names
//     (R1-21 review M5): the swap's end class (none, applied, back, failed)
//     and the store's module rows are together those of a legal run, the
//     fault-free run's, or, for a fixture with a staged swap, "back" with
//     the rows of the store the swap would have replaced, never a mix
//     (fixtureRun.legal). An applied end also leaves the original store
//     opening in previous/ (the only undo), and a restore that ended back
//     leaves the staged store opening in failed/ or staging.
//   - I3: the level sidecar equals the stamps after recovery, and at every
//     kill point it is never below a stamp (machine 2: sidecar first).
//   - I5: readiness within maxBoots boots, or the fixture's Refusal where
//     the fault-free boot refuses too (R1-21 review (d)).
//   - I6: every checkpoint record names a complete checkpoint directory
//     that opens, no partial one is left, a crash loop leaves no more
//     checkpoints than the legal runs leave between them (at least one),
//     and every checkpoint holds the fixture's module rows, no module row,
//     or those of a checkpoint a legal run took (none holds a row written
//     after it).

// terminal reports whether step is one of machine 1's end states.
func terminal(step string) bool {
	switch step {
	case restoreswap.StepApplied, restoreswap.StepSwappedBack, restoreswap.StepRejected, restoreswap.StepHookDone:
		return true
	}
	return false
}

// The end classes of a run's swap (I2): which store its journal says is
// live. endOpen (a journal not at an end) and endMixed (two journals of
// different classes) match no legal run.
const (
	endNone    = "none"    // no journal: no swap, or one rejected before it had an id
	endApplied = "applied" // applied or hook_done: the store the swap placed
	endBack    = "back"    // swapped_back or rejected: the store the swap would replace
	endFailed  = "failed"  // KindCheckpoint at failed: the restored store, refused to this binary
	endOpen    = "open"
	endMixed   = "mixed"
)

// endOf is the end class of journal j.
func endOf(j journal) string {
	switch {
	case j.last == restoreswap.StepApplied || j.last == restoreswap.StepHookDone:
		return endApplied
	case j.last == restoreswap.StepSwappedBack || j.last == restoreswap.StepRejected:
		return endBack
	case j.last == restoreswap.StepFailed && j.kind == restorejournal.KindCheckpoint:
		return endFailed
	}
	return endOpen
}

// endOfAll is the end class of every journal and the id that decides it.
func endOfAll(js map[string]journal) (end, id string) {
	end = endNone
	for _, jid := range slices.Sorted(maps.Keys(js)) {
		switch c := endOf(js[jid]); {
		case end == endNone:
			end, id = c, jid
		case c != end:
			return endMixed, ""
		}
	}
	return end, id
}

// outcome is what a run left on its filesystem.
type outcome struct {
	err         error
	end, endID  string // the swap's end class and its journal's id (endOfAll)
	store       bool
	rows        string // digest of every module row (m/)
	previous    string // an applied end: the module-row digest of the store in previous/, "" when none opens there
	stagedKept  bool   // a restore's back end: the staged store opens in failed/ or staging
	stamps      map[string]int
	sidecar     map[string]int
	journals    map[string]journal
	pending     bool
	checkpoints []string // the module-row digest of each complete checkpoint, sorted
	partial     []string // checkpoint directories without a record
	orphans     []string // records naming no complete directory
	broken      []string // checkpoints with a record whose store does not open
}

type journal struct {
	kind     restorejournal.Kind
	last     string
	inflight bool
}

// storageStamps is c/storage/level/<owner>'s prefix (05-storage §5S.3.7).
func storageStamps() []byte { return append([]byte("c/storage/"), keys.Join(keys.Str("level"))...) }

// zeroRows is the module-row digest of a store with no module row.
func zeroRows() string { return fmt.Sprintf("0 rows %x", sha256.New().Sum(nil)[:8]) }

type stampRecord struct {
	Level int `json:"level"`
}

// read inspects the store, its sidecar, the swap slot, the swap's undo and
// the checkpoints. in is the fixture's staged intent, nil when none.
func read(mem *vfs.MemFS, f Fixture, in *restorejournal.Intent) outcome {
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
	o.end, o.endID = endOfAll(o.journals)
	switch {
	case o.end == endApplied:
		var ok bool
		if ok, o.previous, _, o.err = readStore(mem, restorejournal.SetAsideDest(f.StateDir, db, o.endID, db), false); o.err != nil {
			return o
		} else if !ok {
			o.previous = ""
		}
	case o.end == endBack && in != nil && in.Kind != restorejournal.KindReset:
		for _, p := range []string{restorejournal.FailedDest(f.StateDir, db, o.endID, db), stagedStore(in)} {
			ok, _, _, err := readStore(mem, p, false)
			if err != nil {
				o.err = fmt.Errorf("the staged store at %s: %w", p, err)
				return o
			}
			o.stagedKept = o.stagedKept || ok
		}
	}
	o.checkpoints, o.partial, o.orphans, o.broken, o.err = readCheckpoints(mem, filepath.Join(filepath.Dir(db), ".mediacore-checkpoints"))
	return o
}

// stagedStore is the staged item a restore places at its DB path.
func stagedStore(in *restorejournal.Intent) string {
	for _, t := range in.Targets {
		if filepath.Clean(t.To) == filepath.Clean(in.DBPath) {
			return t.From
		}
	}
	return ""
}

// stagedIntent is the intent in the fixture's swap slot (pending.json, else
// the in-flight file), nil when the slot holds none or none that decodes.
func stagedIntent(mem vfs.FS, stateDir string) (*restorejournal.Intent, error) {
	path := restorejournal.PendingPath(stateDir)
	if _, err := mem.Stat(path); errors.Is(err, fs.ErrNotExist) {
		ids, err := restorejournal.InFlightIDs(mem, stateDir)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		path = filepath.Join(restorejournal.JournalDir(stateDir, ids[0]), restorejournal.InflightName)
	} else if err != nil {
		return nil, err
	}
	f, err := mem.Open(path)
	if err != nil {
		return nil, err
	}
	var in restorejournal.Intent
	derr := json.NewDecoder(f).Decode(&in)
	if err := f.Close(); err != nil {
		return nil, err
	}
	if derr != nil {
		return nil, nil // P0a moves it aside (an unusable intent): no swap to check
	}
	return &in, nil
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
	n, stampPrefix := 0, storageStamps()
	for valid := it.First(); valid; valid = it.Next() {
		k := it.Key()
		switch {
		case bytes.HasPrefix(k, []byte("m/")):
			v, verr := it.ValueAndErr()
			if verr != nil {
				return false, "", nil, errors.Join(verr, it.Close())
			}
			h.Write(fmt.Appendf(nil, "%q=%q\n", k, v)) // hash.Hash's Write never fails
			n++
		case stamps && bytes.HasPrefix(k, stampPrefix):
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
// intent is still pending. A directory with neither a journal nor an
// in-flight file holds no swap, as P0a and the in-flight probe read it
// (Stage's directory before its create, a removed swap's leftovers), and is
// left out; one with an in-flight file and no journal is kept, at step "",
// for I2 to report.
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
		if err != nil {
			return nil, false, fmt.Errorf("journal %s: %w", n, err)
		}
		j := journal{}
		if len(recs) > 0 {
			j.last = recs[len(recs)-1].Step
		}
		_, serr := fsys.Stat(fsys.PathJoin(p, restorejournal.InflightName))
		if serr != nil && !errors.Is(serr, fs.ErrNotExist) {
			return nil, false, fmt.Errorf("journal %s: %w", n, serr)
		}
		j.inflight = serr == nil
		if len(recs) == 0 && !j.inflight {
			continue
		}
		if n == flying {
			j.kind = kind // only an in-flight journal's kind matters: failed is legal for KindCheckpoint
		}
		out[n] = j
	}
	return out, pending, nil
}

// isCheckpointName reports whether n is in the checkpoint grammar of
// 05-storage §5S.3.12 "Where", <YYYYMMDD>T<HHMMSS>Z-<8 hex digits>.
func isCheckpointName(n string) bool {
	if len(n) != 25 || n[8] != 'T' || n[15] != 'Z' || n[16] != '-' {
		return false
	}
	for i := range len(n) {
		c := n[i]
		switch {
		case i == 8 || i == 15 || i == 16:
		case i < 16 && (c < '0' || c > '9'):
			return false
		case i > 16 && !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f'):
			return false
		}
	}
	return true
}

// readCheckpoints reads the checkpoint directory: the module-row digest of
// each complete checkpoint, the directories with no record, the records
// with no complete directory, and the checkpoints whose store does not open.
// Only names in the checkpoint grammar are read.
func readCheckpoints(fsys vfs.FS, dir string) (complete, partial, orphans, broken []string, err error) {
	names, err := fsys.List(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, nil, err
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, n := range names {
		base, _ := strings.CutSuffix(n, ".json")
		base, _ = strings.CutSuffix(base, ".tmp")
		if !isCheckpointName(base) {
			// The scratch area, the sweep's lock file, or a name no writer
			// makes: not a checkpoint (05-storage §5S.3.12 "Where").
			continue
		}
		switch {
		case strings.HasSuffix(n, ".json"):
			if base := strings.TrimSuffix(n, ".json"); !have[base] {
				orphans = append(orphans, n)
			}
		case !have[n+".json"]:
			partial = append(partial, n)
		default:
			ok, rows, _, err := readStore(fsys, fsys.PathJoin(dir, n, "db"), false)
			switch {
			case err != nil:
				broken = append(broken, fmt.Sprintf("%s (%v)", n, err))
			case !ok:
				broken = append(broken, n+" (no store)")
			default:
				complete = append(complete, rows)
			}
		}
	}
	slices.Sort(complete)
	return complete, partial, orphans, broken, nil
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
		// Between a restore's placements (step d) and its sidecar rewrite
		// (step e) the DB path holds the placed store and no sidecar; the
		// in-flight file exists, so the reader check refuses every opener
		// (05-storage §5S.3.7) and the sidecar's absence is legal.
		_, inflight, err := restoreswap.InFlight(fsys, fr.f.StateDir)
		if err != nil {
			fr.errorf("%s: I3: the swap slot at the kill point: %v", at, err)
		} else if !inflight && maxLevel(stamps) > 0 {
			fr.errorf("%s: I3: stamps %v with no level sidecar and no swap in flight", at, stamps)
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
		if fr.f.Refusal != nil {
			want = fmt.Sprintf("readiness or the refusal %v", fr.f.Refusal)
		}
		fr.errorf("%s: I5: no %s within %d boots: %v", at, want, maxBoots, ch.errs())
		return
	}
	if !ch.ready && !fr.census.refused(fr.f) {
		// R1-21 review (d): the declared refusal is a legal end only where
		// the fault-free boot refuses too; elsewhere it hides a wrong end.
		fr.errorf("%s: I5: the boot refused (%v), which the fault-free boot does not", at, ch.errs()[len(ch.errs())-1])
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
}

// checkStore asserts I2 and I3 on a chain that reached readiness with a
// Row.Retried writer's failure logged; I6 waits for the retry.
func (fr *fixtureRun) checkStore(at string, ch chain) {
	if ch.outcome.err != nil {
		fr.errorf("%s: reading the result: %v", at, ch.outcome.err)
		return
	}
	for _, msg := range fr.storeViolations(ch.outcome) {
		fr.errorf("%s: %s", at, msg)
	}
}

// violations lists o's breaches of I2, I3 and I6.
func (fr *fixtureRun) violations(o outcome) []string {
	return append(fr.storeViolations(o), fr.checkpointViolations(o)...)
}

// storeViolations lists o's breaches of I2 and I3: the journals, the store
// at the DB path, and its level sidecar.
func (fr *fixtureRun) storeViolations(o outcome) []string {
	var out []string
	for id, j := range o.journals {
		done := terminal(j.last)
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
		legal = legal || (l.end == o.end && l.store == o.store && l.rows == o.rows)
	}
	if !legal {
		out = append(out, fmt.Sprintf("I2: the swap ended %s with the DB path holding %s; the legal ends are %s", o.end, rowsOf(o), fr.legalEnds()))
	}
	switch {
	case o.end == endApplied && fr.hadStore && o.previous != fr.initial:
		held := o.previous
		if held == "" {
			held = "no store"
		}
		out = append(out, fmt.Sprintf("I2: journal %s applied, and previous/ holds %s, not the original store's %s: the only undo is gone", o.endID, held, fr.initial))
	case o.end == endBack && fr.intent != nil && fr.intent.Kind != restorejournal.KindReset && !o.stagedKept:
		out = append(out, fmt.Sprintf("I2: journal %s ended back, and no staged store opens in failed/ or staging", o.endID))
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
	return out
}

// checkpointViolations lists o's breaches of I6: the checkpoints.
func (fr *fixtureRun) checkpointViolations(o outcome) []string {
	var out []string
	if len(o.orphans) > 0 {
		out = append(out, fmt.Sprintf("I6: checkpoint records with no complete directory: %v", o.orphans))
	}
	for _, b := range o.broken {
		out = append(out, fmt.Sprintf("I6: checkpoint %s has a record but does not open: the rollback copy is unusable", b))
	}
	if len(o.partial) > 0 {
		out = append(out, fmt.Sprintf("I6: partial checkpoint directories left: %v", o.partial))
	}
	// A crash loop reuses one checkpoint: no more than the legal stores'
	// runs leave between them (a swapped-back restore's checkpoint stays,
	// decision D6, and the original store's boot may take its own), or one
	// after a kill that makes the next boot an upgrade boot (between the
	// fresh-install batch and its seed step, say). A checkpoint with no
	// module row cannot hold a row written after it.
	most, known := 0, map[string]bool{fr.initial: true, zeroRows(): true}
	for _, l := range fr.legal {
		most += len(l.checkpoints)
		for _, c := range l.checkpoints {
			known[c] = true
		}
	}
	most = max(most, 1)
	if len(o.checkpoints) > most {
		out = append(out, fmt.Sprintf("I6: %d checkpoints, where a crash loop reuses one (the legal runs leave %d)", len(o.checkpoints), most))
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

func (fr *fixtureRun) legalEnds() string {
	var s []string
	for _, l := range fr.legal {
		s = append(s, l.end+" with "+rowsOf(l))
	}
	return strings.Join(s, " or ")
}
