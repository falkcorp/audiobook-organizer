// file: core/coretest/crashmatrix/run.go
// version: 1.0.3
// guid: 20df00da-4b40-4e5b-9987-4ef6293f76dd
// last-edited: 2026-11-08

package crashmatrix

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Scenario is one boot to break. Build boots the current binary in process
// through inj.New; Fixtures are the starting states; PrevLevels is the
// previous release's supported vector (its --print-storage-levels output),
// against which I4 checks the sidecars at every kill point (05-storage
// §5S.3.16; decision D10: 06 §6.4.8's copy of the type omitted it).
type Scenario struct {
	// Build boots once and returns New's error. It runs concurrently with
	// itself on other filesystems, so it reports through its error, never
	// t.Fatal.
	Build      func(t testing.TB, inj *Injector) error
	Fixtures   []Fixture
	PrevLevels storage.Levels
	// Rows are the app's own writer-table rows, checked with core's (Rows):
	// ABO's instance registers its boot writers here.
	Rows []Row
}

// maxBoots is I5's bound: the broken boot's successors reach readiness
// within this many boots in all (a swapped-back restore exits once).
const maxBoots = 3

// errnos are the error mode's returns, one run each (R10-6, DATA10-4).
func errnos() []error { return []error{syscall.ENOSPC, syscall.EACCES} }

// Run breaks the boot at every durable write, rename and fsync between P0a
// and readiness in two modes: a kill, and an error return (ENOSPC, then
// EACCES) from that one operation with the boot continuing, so a non-crash
// failure must also leave a legal state (R10-6, DATA10-4). It reboots the
// current build until readiness, then asserts I1–I6 (invariants.go); the
// boots after each break are killed again at their swap kill points, one
// level deep (nested, R1-21 review M6). It
// fails on any durable operation that carries no writer-table label, and
// after each fixture it diffs the fixture's real directories against what
// they held before, so a file written past the boot filesystem fails the
// run too (COR10-7).
func Run(t *testing.T, s Scenario) {
	t.Helper()
	run(t, t, s, modeFull)
}

// Observed boots each fixture once (the census, no fault) and returns every
// label its durable operations carried, failing t on an unlabelled or
// unregistered operation and on a real-disk write, as Run does: the input
// of TestBootCrashMatrixCoversEveryWriter (06 §6.5.1; Uncovered).
func Observed(t *testing.T, s Scenario) map[string]bool {
	t.Helper()
	return run(t, t, s, modeCensus)
}

type mode int

const (
	modeCensus mode = iota // the fault-free boot chain only
	modeFull               // census, every kill and every error
)

// reporter is where the kit reports: *testing.T in Run, a recorder in the
// kit's own tests.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

// maxReports bounds the failures one fixture reports: the first few name
// the defect; thousands of copies of it bury the log.
const maxReports = 25

func run(tb testing.TB, rep reporter, s Scenario, m mode) map[string]bool {
	rep.Helper()
	registered := map[string]bool{}
	for _, r := range append(Rows(), s.Rows...) {
		for _, l := range r.Labels {
			registered[l] = true
		}
	}
	observed := map[string]bool{}
	for _, f := range s.Fixtures {
		fr := &fixtureRun{tb: tb, rep: rep, s: s, f: f, registered: registered, observed: observed, nestedSeen: map[[sha256.Size]byte]bool{}}
		fr.run(m)
	}
	return observed
}

// fixtureRun is one fixture's census, kills and errors.
type fixtureRun struct {
	tb         testing.TB
	rep        reporter
	s          Scenario
	f          Fixture
	registered map[string]bool

	mu       sync.Mutex
	observed map[string]bool
	reports  int
	census   chain
	legal    []outcome
	initial  string                 // the fixture store's module-row digest (no store: zero rows)
	hadStore bool                   // a store was at the DB path before the first boot
	intent   *restorejournal.Intent // the fixture's staged swap, nil when none
	stats    stats
	// nestedSeen holds the content key (stateKey) of every nested clone
	// already booted: most nested kill points of one fixture leave a state
	// another already left (hello's restore-archive: 272 distinct of 5,048),
	// and a boot is a function of its filesystem, so one of each is booted.
	nestedSeen map[[sha256.Size]byte]bool
}

type stats struct {
	boots, kills, errorRuns, events, pebbleKills int
	nested, nestedDup                            int // nested kill points booted, and those that left a state already booted
}

func (fr *fixtureRun) errorf(format string, args ...any) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	fr.reports++
	switch {
	case fr.reports < maxReports:
		fr.rep.Errorf("crashmatrix: fixture %s: "+format, append([]any{fr.f.Name}, args...)...)
	case fr.reports == maxReports:
		fr.rep.Errorf("crashmatrix: fixture %s: further failures not shown", fr.f.Name)
	}
}

func (fr *fixtureRun) run(m mode) {
	start := time.Now()
	before, err := snapshot(fr.f)
	if err != nil {
		fr.errorf("%v", err)
		return
	}
	defer func() {
		after, err := snapshot(fr.f)
		if err != nil {
			fr.errorf("%v", err)
			return
		}
		if d := before.diff(after); len(d) > 0 {
			sort.Strings(d)
			fr.errorf("tree diff: the boot wrote to the real disk, past the boot filesystem (COR10-7): %s", strings.Join(d, ", "))
		}
	}()
	mem, err := load(fr.f)
	if err != nil {
		fr.errorf("%v", err)
		return
	}
	if fr.hadStore, fr.initial, _, err = readStore(mem, fr.f.dbPath(), false); err != nil {
		fr.errorf("reading the fixture's store: %v", err)
		return
	}
	if fr.initial == "" {
		fr.initial = zeroRows()
	}
	if fr.intent, err = stagedIntent(mem, fr.f.StateDir); err != nil {
		fr.errorf("reading the fixture's staged intent: %v", err)
		return
	}
	fr.census = fr.chain(mem, func(int) bootseamtest.Options { return bootseamtest.Options{Clones: m == modeFull} })
	if !fr.census.ended(fr.f) {
		fr.errorf("the fault-free boot does not reach readiness within %d boots: %v", maxBoots, fr.census.errs())
		return
	}
	if fr.census.outcome.err != nil {
		fr.errorf("reading the fault-free boot's result: %v", fr.census.outcome.err)
		return
	}
	fr.legal = append(fr.legal, fr.census.outcome)
	// The fault-free run is the yardstick: it is held to the same rules
	// first, so a wrong end there cannot make itself legal.
	for _, msg := range fr.violations(fr.census.outcome) {
		fr.errorf("the fault-free boot: %s", msg)
	}
	if m == modeCensus {
		return
	}
	if !fr.untouched() {
		return
	}
	fr.matrix()
	fr.rep.Logf("crashmatrix: fixture %s: %d boots (%d kill points, %d of them Pebble's; %d error runs; %d nested kill points booted, %d more that left a state already booted) over %d census operations in %s",
		fr.f.Name, fr.stats.boots, fr.stats.kills, fr.stats.pebbleKills, fr.stats.errorRuns, fr.stats.nested, fr.stats.nestedDup, fr.stats.events, time.Since(start).Round(time.Millisecond))
}

// untouched adds the second legal store of a fixture with a staged or
// in-flight swap: the one the swap would replace, as a boot leaves it when
// the swap is rejected or swapped back (I2: the DB path holds one of the
// two, never a mix). It boots the fixture with its swap slot removed.
func (fr *fixtureRun) untouched() bool {
	mem, err := load(fr.f)
	if err != nil {
		fr.errorf("%v", err)
		return false
	}
	slot := restorejournal.Dir(fr.f.StateDir)
	if _, err := mem.Stat(slot); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err := errors.Join(mem.RemoveAll(slot), syncTree(mem, string(filepath.Separator))); err != nil {
		fr.errorf("removing the swap slot: %v", err)
		return false
	}
	u := fr.chain(mem, func(int) bootseamtest.Options { return bootseamtest.Options{} })
	if !u.ready {
		fr.errorf("the store the swap would replace does not boot to readiness on its own: %v", u.errs())
		return false
	}
	// With its slot removed the run has no journal; its store is what a
	// rejected or swapped-back swap leaves live (I2's class "back").
	u.outcome.end = endBack
	fr.legal = append(fr.legal, u.outcome)
	return true
}

// matrix runs every kill and every error of the census, GOMAXPROCS at a
// time: each on its own filesystem, so no run touches another's state.
func (fr *fixtureRun) matrix() {
	var jobs []func()
	for b, br := range fr.census.boots {
		fr.stats.events += len(br.events)
		for _, c := range br.clones {
			fr.stats.kills++
			if c.Event.Pebble() {
				fr.stats.pebbleKills++
			}
			jobs = append(jobs, func() { fr.kill(b, c) })
		}
		for _, ev := range br.events {
			if ev.Pebble() || ev.Label == bootseamtest.LabelNone {
				continue // decision D12; an unlabelled operation already failed the census
			}
			for _, errno := range errnos() {
				fr.stats.errorRuns++
				jobs = append(jobs, func() { fr.inject(b, ev, errno) })
			}
		}
	}
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for _, job := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			job()
		}()
	}
	wg.Wait()
}

// kill reboots the crash clone taken before census boot b's operation, and
// kills the first reboot again at each of its nested kill points.
func (fr *fixtureRun) kill(b int, c bootseamtest.Clone) {
	c.FS.TestingSetDiskUsage(diskUsage())
	at := fmt.Sprintf("kill before boot %d's %s", b+1, describe(c.Event))
	fr.atKillPoint(at, c.FS)
	ch := fr.chain(c.FS, func(boot int) bootseamtest.Options {
		if boot == 0 {
			return bootseamtest.Options{CloneIf: nested}
		}
		return bootseamtest.Options{}
	})
	fr.check(at, ch)
	fr.killNested(at, ch, func(i int) string { return fmt.Sprintf("reboot %d", i+1) })
}

// nested reports a nested kill point (R1-21 review M6): an operation of the
// swap's writers in a boot that runs after the matrix broke an earlier
// one, the reboot after a kill or the rest of a boot after an injected
// error. Those boots run the recovery writers no fault-free boot reaches
// (step x after a failed P0a, a reset's move-back, a rejected reset, the
// slot cleanup), so the matrix breaks them there.
func nested(ev bootseamtest.Event) bool {
	return !ev.Pebble() && strings.HasPrefix(ev.Label, "swap.")
}

// killNested reboots each nested clone ch took, one level deep: a nested
// run takes no clones of its own. boot names ch's ith boot for a report. A
// clone whose content an earlier nested clone of the fixture had is not
// booted again (fixtureRun.nestedSeen); the first one's report stands for
// both.
func (fr *fixtureRun) killNested(at string, ch chain, boot func(i int) string) {
	for i, br := range ch.boots {
		for _, c := range br.clones {
			nat := fmt.Sprintf("%s, then kill before %s's %s", at, boot(i), describe(c.Event))
			key, err := stateKey(c.FS)
			if err != nil {
				fr.errorf("%s: reading the nested clone: %v", nat, err)
				continue
			}
			fr.mu.Lock()
			seen := fr.nestedSeen[key]
			if seen {
				fr.stats.nestedDup++
			} else {
				fr.nestedSeen[key] = true
				fr.stats.nested++
			}
			fr.mu.Unlock()
			if seen {
				continue
			}
			c.FS.TestingSetDiskUsage(diskUsage())
			fr.atKillPoint(nat, c.FS)
			fr.check(nat, fr.chain(c.FS, func(int) bootseamtest.Options { return bootseamtest.Options{} }))
		}
	}
}

// stateKey is a content key of fs: every directory and file under the root
// in sorted order, with each file's bytes. Two filesystems with one key
// boot alike.
func stateKey(fs vfs.FS) ([sha256.Size]byte, error) {
	h := sha256.New()
	var walk func(dir string) error
	walk = func(dir string) error {
		names, err := fs.List(dir)
		if err != nil {
			return err
		}
		slices.Sort(names)
		for _, n := range names {
			p := filepath.Join(dir, n)
			fi, err := fs.Stat(p)
			if err != nil {
				return err
			}
			if fi.IsDir() {
				fmt.Fprintf(h, "d %q\n", p)
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			f, err := fs.Open(p)
			if err != nil {
				return err
			}
			b, err := io.ReadAll(f)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "f %q %d\n", p, len(b))
			h.Write(b)
		}
		return nil
	}
	var key [sha256.Size]byte
	if err := walk(string(filepath.Separator)); err != nil {
		return key, err
	}
	copy(key[:], h.Sum(nil))
	return key, nil
}

// inject reruns the fixture with errno returned from census boot b's
// operation ev, the boot going on.
func (fr *fixtureRun) inject(b int, ev bootseamtest.Event, errno error) {
	mem, err := load(fr.f)
	if err != nil {
		fr.errorf("%v", err)
		return
	}
	var mu sync.Mutex
	hit := false
	// The nested kill points are those after the error: the rest of the
	// broken boot and its first reboot (R1-21 review M6). The ones before
	// it are the census's own.
	afterHit := func(got bootseamtest.Event) bool {
		mu.Lock()
		defer mu.Unlock()
		return hit && nested(got)
	}
	ch := fr.chain(mem, func(boot int) bootseamtest.Options {
		switch boot {
		case b:
		case b + 1:
			return bootseamtest.Options{CloneIf: afterHit}
		default:
			return bootseamtest.Options{}
		}
		return bootseamtest.Options{CloneIf: afterHit, Fail: func(got bootseamtest.Event) error {
			if got.Pebble() || got.Label != ev.Label || got.Ordinal != ev.Ordinal || got.Kind != ev.Kind {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			if hit {
				return nil
			}
			hit = true
			return errno
		}}
	})
	at := fmt.Sprintf("%v from boot %d's %s", errno, b+1, describe(ev))
	if !hit {
		fr.errorf("%s: the operation was not reached on the rerun: the boot is not deterministic", at)
		return
	}
	fr.check(at, ch)
	fr.killNested(at, ch, func(i int) string { return fmt.Sprintf("boot %d", i+1) })
}

// describe names an operation for a report.
func describe(ev bootseamtest.Event) string {
	label := ev.Label
	if label == bootseamtest.LabelNone {
		label = "(unlabelled)"
	}
	return fmt.Sprintf("%s #%d (%s %s)", label, ev.Ordinal, opName(ev.Kind), ev.Path)
}

// opName names a durable operation's kind (errorfs.OpKind has no String).
func opName(k errorfs.OpKind) string {
	switch k {
	case errorfs.OpCreate:
		return "create"
	case errorfs.OpLink:
		return "link"
	case errorfs.OpRemove:
		return "remove"
	case errorfs.OpRemoveAll:
		return "remove-all"
	case errorfs.OpRename:
		return "rename"
	case errorfs.OpReuseForWrite:
		return "reuse-for-write"
	case errorfs.OpMkdirAll:
		return "mkdir"
	case errorfs.OpFileWrite, errorfs.OpFileWriteAt:
		return "write"
	case errorfs.OpFileSync, errorfs.OpFileSyncData, errorfs.OpFileSyncTo:
		return "sync"
	}
	return fmt.Sprintf("op%d", k)
}

// chain is the boots of one run: the first, and each restart after a
// failed boot, up to maxBoots.
type chain struct {
	mem     *vfs.MemFS
	boots   []bootRun
	ready   bool
	outcome outcome
}

type bootRun struct {
	events []bootseamtest.Event
	clones []bootseamtest.Clone
	err    error
}

func (c chain) errs() []error {
	var out []error
	for _, b := range c.boots {
		out = append(out, b.err)
	}
	return out
}

// ended reports whether the chain reached a legal end: readiness, or the
// refusal the fixture declares.
func (c chain) ended(f Fixture) bool {
	return c.ready || c.refused(f)
}

func (c chain) refused(f Fixture) bool {
	return f.Refusal != nil && len(c.boots) > 0 && errors.Is(c.boots[len(c.boots)-1].err, f.Refusal)
}

func (fr *fixtureRun) chain(mem *vfs.MemFS, opts func(boot int) bootseamtest.Options) chain {
	c := chain{mem: mem}
	for boot := range maxBoots {
		o := opts(boot)
		o.Stores = []string{fr.f.dbPath()}
		rec := bootseamtest.New(c.mem, o)
		err := fr.boot(rec)
		if errors.Is(err, errPanicked) {
			// The process died: what it leaves is a crash at that operation
			// (PLAN §2.3; Pebble panics on a failed marker directory sync,
			// atomicfs.Marker.Move). Its open store is abandoned, as a dead
			// process's is; the restart sees the crash clone.
			c.mem = c.mem.CrashClone(vfs.CrashCloneCfg{})
			c.mem.TestingSetDiskUsage(diskUsage())
		}
		events := rec.Events()
		fr.observe(events)
		c.boots = append(c.boots, bootRun{events: events, clones: rec.Clones(), err: err})
		fr.mu.Lock()
		fr.stats.boots++
		fr.mu.Unlock()
		if err == nil {
			c.ready = true
			break
		}
		if fr.f.Refusal != nil && errors.Is(err, fr.f.Refusal) {
			break
		}
	}
	c.outcome = read(c.mem, fr.f, fr.intent)
	return c
}

// errPanicked: the boot panicked, which the kit treats as the process
// dying at that point.
var errPanicked = errors.New("crashmatrix: the boot panicked")

// boot is one boot of Build over rec, to readiness and a clean stop.
func (fr *fixtureRun) boot(rec *bootseamtest.Recorder) (err error) {
	inj := newInjector(fr.f, rec, nil)
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", errPanicked, p)
		}
	}()
	berr := fr.s.Build(fr.tb, inj)
	ferr := inj.finish(context.Background())
	if berr != nil {
		return berr // finish only closed what New left open
	}
	return ferr
}

// observe checks one boot's operations against the writer table (06
// §6.5.1): every operation carries a label, and every label is a row's.
func (fr *fixtureRun) observe(events []bootseamtest.Event) {
	var bad []string
	fr.mu.Lock()
	for _, ev := range events {
		fr.observed[ev.Label] = true
		switch {
		case ev.Label == bootseamtest.LabelNone:
			bad = append(bad, "unlabelled durable operation: "+describe(ev))
		case !fr.registered[ev.Label]:
			bad = append(bad, "label not in the writer table (Rows): "+describe(ev))
		}
	}
	fr.mu.Unlock()
	for _, b := range uniqStrings(bad) {
		fr.errorf("%s", b)
	}
}

func uniqStrings(in []string) []string {
	slices.Sort(in)
	return slices.Compact(in)
}
