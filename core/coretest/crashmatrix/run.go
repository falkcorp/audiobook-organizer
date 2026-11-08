// file: core/coretest/crashmatrix/run.go
// version: 1.0.0
// guid: 20df00da-4b40-4e5b-9987-4ef6293f76dd
// last-edited: 2026-11-08

package crashmatrix

import (
	"context"
	"errors"
	"fmt"
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
var errnos = []error{syscall.ENOSPC, syscall.EACCES}

// Run breaks the boot at every durable write, rename and fsync between P0a
// and readiness in two modes: a kill, and an error return (ENOSPC, then
// EACCES) from that one operation with the boot continuing, so a non-crash
// failure must also leave a legal state (R10-6, DATA10-4). It reboots the
// current build until readiness, then asserts I1–I6 (invariants.go). It
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
		fr := &fixtureRun{tb: tb, rep: rep, s: s, f: f, registered: registered, observed: observed}
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
	initial  string // the fixture store's module-row digest (no store: zero rows)
	stats    stats
}

type stats struct {
	boots, kills, errorRuns, events, pebbleKills int
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
	if _, fr.initial, _, err = readStore(mem, fr.f.dbPath(), false); err != nil {
		fr.errorf("reading the fixture's store: %v", err)
		return
	}
	if fr.initial == "" {
		fr.initial = zeroRows
	}
	fr.census = fr.chain(mem, func(int) bootseamtest.Options { return bootseamtest.Options{Clones: m == modeFull} })
	if !fr.census.ended(fr.f) {
		fr.errorf("the fault-free boot does not reach readiness within %d boots: %v", maxBoots, fr.census.errs())
		return
	}
	fr.legal = append(fr.legal, fr.census.outcome)
	if m == modeCensus {
		return
	}
	fr.matrix()
	fr.rep.Logf("crashmatrix: fixture %s: %d boots (%d kill points, %d of them Pebble's; %d error runs) over %d census operations in %s",
		fr.f.Name, fr.stats.boots, fr.stats.kills, fr.stats.pebbleKills, fr.stats.errorRuns, fr.stats.events, time.Since(start).Round(time.Millisecond))
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
			for _, errno := range errnos {
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

// kill reboots the crash clone taken before census boot b's operation.
func (fr *fixtureRun) kill(b int, c bootseamtest.Clone) {
	c.FS.TestingSetDiskUsage(diskUsage)
	at := fmt.Sprintf("kill before boot %d's %s", b+1, describe(c.Event))
	fr.atKillPoint(at, c.FS)
	ch := fr.chain(c.FS, func(int) bootseamtest.Options { return bootseamtest.Options{} })
	fr.check(at, ch)
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
	ch := fr.chain(mem, func(boot int) bootseamtest.Options {
		if boot != b {
			return bootseamtest.Options{}
		}
		return bootseamtest.Options{Fail: func(got bootseamtest.Event) error {
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

// ended reports whether the chain reached the fixture's legal end:
// readiness, or the refusal the fixture declares.
func (c chain) ended(f Fixture) bool {
	if f.WantRefused != nil {
		return len(c.boots) > 0 && errors.Is(c.boots[len(c.boots)-1].err, f.WantRefused)
	}
	return c.ready
}

func (fr *fixtureRun) chain(mem *vfs.MemFS, opts func(boot int) bootseamtest.Options) chain {
	c := chain{mem: mem}
	for boot := range maxBoots {
		o := opts(boot)
		o.Stores = []string{fr.f.dbPath()}
		rec := bootseamtest.New(mem, o)
		err := fr.boot(rec)
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
		if fr.f.WantRefused != nil && errors.Is(err, fr.f.WantRefused) {
			break
		}
	}
	c.outcome = read(mem, fr.f)
	return c
}

// boot is one boot of Build over rec, to readiness and a clean stop.
func (fr *fixtureRun) boot(rec *bootseamtest.Recorder) (err error) {
	inj := newInjector(fr.f, rec)
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("crashmatrix: the boot panicked: %v", p)
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
