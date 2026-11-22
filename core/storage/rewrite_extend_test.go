// file: core/storage/rewrite_extend_test.go
// version: 1.0.0
// guid: ba0bcd44-54ab-4848-9503-76af26da1bae
// last-edited: 2026-11-22

package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// extendLog is the boot's ExtendStart (OpenOptions.ExtendStart) as a
// recorder of the step names it was called with.
type extendLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *extendLog) record(step string) {
	l.mu.Lock()
	l.steps = append(l.steps, step)
	l.mu.Unlock()
}

func (l *extendLog) count(step string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.steps {
		if s == step {
			n++
		}
	}
	return n
}

// batmanRows is n rows of the joker keyspace, each about 110 bytes.
func batmanRows(n int) []string {
	kv := make([]string, 0, 2*n)
	for i := range n {
		kv = append(kv, fmt.Sprintf("batman:%05d", i), strings.Repeat("robin", 20))
	}
	return kv
}

// copyRun is what runCopy saw of one M8 copy.
type copyRun struct {
	stats        RewriteCopyStats
	during       int // stepRewriteCopy requests made inside RewriteCopy
	afterReturn  int // requests the log held when RewriteCopy returned
	atBootEnd    int // requests the log held when the boot was over
	compactCalls int
}

// runCopy boots a store holding rows keys of its own beside the two of
// headStore, with M8's copy cut into tables of tableBytes and one ExtendStart
// request per everyKeys keys, and runs RewriteCompact and RewriteCopy inside
// M8's Run.
func runCopy(t *testing.T, rows, tableBytes, everyKeys int) copyRun {
	t.Helper()
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	e.seedRaw(batmanRows(rows)...)
	e.opts.dirIdentity = copyIdentities(e).of
	e.opts.rewriteTableBytes = int64(tableBytes)
	e.opts.rewriteExtendKeys = int64(everyKeys)
	e.opts.rewriteExtendBytes = 1 << 40 // keys alone pace the requests here
	var log extendLog
	e.opts.ExtendStart = log.record
	var r copyRun
	e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
		if err := db.RewriteCompact(ctx); err != nil {
			return err
		}
		r.compactCalls = log.count(stepRewriteCompact)
		before := log.count(stepRewriteCopy)
		var err error
		r.stats, err = db.RewriteCopy(ctx, RewriteDir(e.path), "wayne.rewrite")
		r.afterReturn = log.count(stepRewriteCopy)
		r.during = r.afterReturn - before
		if err != nil {
			return err
		}
		return markDone(ctx, db, "wayne.rewrite")
	})}
	e.open()
	time.Sleep(20 * time.Millisecond) // a request from a stray goroutine would land by now
	r.atBootEnd = log.count(stepRewriteCopy)
	return r
}

// TestRewriteCopyExtendsStartAtProgressPoints: M8's copy asks the service
// manager for start time repeatedly while it copies, at progress points: at
// every new table and every everyKeys keys, so the count follows the amount
// of work (twice the keys, about twice the requests), and nothing is asked
// for once RewriteCopy has returned (05-health §5H.3.4: per step, never from
// a timer; the copy of the first flip ran 46 to 69 minutes against a 10
// minute budget and was killed).
func TestRewriteCopyExtendsStartAtProgressPoints(t *testing.T) {
	const tableBytes, everyKeys = 4 << 10, 25
	small := runCopy(t, 300, tableBytes, everyKeys)
	big := runCopy(t, 600, tableBytes, everyKeys)

	for name, r := range map[string]copyRun{"300 rows": small, "600 rows": big} {
		if r.stats.Tables < 3 || r.stats.Keys < 300 {
			t.Fatalf("%s: copy stats = %+v, want several tables and every key", name, r.stats)
		}
		// One request per new table, and no more than everyKeys keys between
		// two requests (a new table restarts that count).
		if want := max(r.stats.Tables, int(r.stats.Keys)/everyKeys); r.during < want {
			t.Errorf("%s: %d requests during the copy of %d keys in %d tables, want at least %d (a table, or every %d keys)",
				name, r.during, r.stats.Keys, r.stats.Tables, want, everyKeys)
		}
		if r.atBootEnd != r.afterReturn {
			t.Errorf("%s: the log held %d copy requests when RewriteCopy returned and %d at the end of the boot: something extended after the copy",
				name, r.afterReturn, r.atBootEnd)
		}
	}
	// Twice the data: the requests tied to the data grow with it (the fixed
	// tail of the copy is the same in both).
	dTables := big.stats.Tables - small.stats.Tables
	if got := big.during - small.during; got < dTables {
		t.Errorf("twice the rows added %d requests, want at least the %d more tables", got, dTables)
	}
	if big.during <= small.during {
		t.Errorf("600 rows made %d requests, 300 rows made %d: the count must grow with the work", big.during, small.during)
	}
}

// TestRewriteCopyExtendsStartInsideALongTable: with every key in one table,
// the requests still come every everyKeys keys, so a single table that takes
// long to copy is not one long unextended stretch; twice the keys, twice the
// requests (to within the fixed tail's one or two).
func TestRewriteCopyExtendsStartInsideALongTable(t *testing.T) {
	const everyKeys = 25
	small := runCopy(t, 300, 1<<30, everyKeys)
	big := runCopy(t, 600, 1<<30, everyKeys)
	if small.stats.Tables != 1 || big.stats.Tables != 1 {
		t.Fatalf("tables = %d and %d, want one each", small.stats.Tables, big.stats.Tables)
	}
	for name, r := range map[string]copyRun{"300 rows": small, "600 rows": big} {
		if want := int(r.stats.Keys) / everyKeys; r.during < want {
			t.Errorf("%s: %d requests for %d keys in one table, want at least %d (every %d keys)", name, r.during, r.stats.Keys, want, everyKeys)
		}
	}
	if want := int(big.stats.Keys-small.stats.Keys) / everyKeys; big.during-small.during < want {
		t.Errorf("300 more keys added %d requests, want at least %d", big.during-small.during, want)
	}
}

// stallFS holds Create of any path ending in suffix, once armed, until
// release is closed; blocked is closed when the first Create is held.
type stallFS struct {
	vfs.FS
	suffix  string
	armed   atomic.Bool
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stallFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	if s.armed.Load() && strings.HasSuffix(name, s.suffix) {
		s.once.Do(func() { close(s.blocked) })
		<-s.release
	}
	return s.FS.Create(name, cat)
}

// TestRewriteCopyStalledStepGetsNoMoreStartTime: a copy that stops advancing
// (here, creating its second table blocks) makes no further ExtendStart
// request however long it stays stuck, so the service manager's deadline runs
// out and kills it; once it moves again the requests resume.
func TestRewriteCopyStalledStepGetsNoMoreStartTime(t *testing.T) {
	fs := &stallFS{FS: vfs.NewMem(), suffix: "rewrite-1.sst", blocked: make(chan struct{}), release: make(chan struct{})}
	e := headStore(t, fs, cpOwners()...)
	e.seedRaw(batmanRows(300)...)
	e.opts.dirIdentity = copyIdentities(e).of
	e.opts.rewriteTableBytes = 4 << 10
	e.opts.rewriteExtendKeys = 25
	e.opts.rewriteExtendBytes = 1 << 40
	var log extendLog
	e.opts.ExtendStart = log.record
	e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
		fs.armed.Store(true)
		if _, err := db.RewriteCopy(ctx, RewriteDir(e.path), "wayne.rewrite"); err != nil {
			return err
		}
		return markDone(ctx, db, "wayne.rewrite")
	})}
	booted := make(chan error, 1)
	go func() {
		_, err := e.tryOpen()
		booted <- err
	}()
	select {
	case <-fs.blocked:
	case err := <-booted:
		t.Fatalf("the copy never reached its second table: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the copy never reached its second table")
	}
	stuck := log.count(stepRewriteCopy)
	if stuck == 0 {
		t.Fatal("the copy asked for no start time before it stalled")
	}
	time.Sleep(200 * time.Millisecond)
	if got := log.count(stepRewriteCopy); got != stuck {
		t.Errorf("a stalled copy made %d more ExtendStart requests while stuck", got-stuck)
	}
	close(fs.release)
	if err := <-booted; err != nil {
		t.Fatalf("boot after the stall cleared: %v", err)
	}
	if got := log.count(stepRewriteCopy); got <= stuck {
		t.Errorf("the copy made no ExtendStart request after it moved again (%d, was %d)", got, stuck)
	}
}

// seedFlushes writes flushes disjoint ranges of rowsEach rows, one flush each, so Compact has
// several files to split its work over.
func seedFlushes(e *env, flushes, rowsEach int) {
	e.t.Helper()
	db, err := cpebble.Open(e.path, &cpebble.Options{FS: e.fs, Logger: quiet{}})
	if err != nil {
		e.t.Fatal(err)
	}
	for f := range flushes {
		for i := range rowsEach {
			k := fmt.Sprintf("batman:%02d:%05d", f, i)
			if err := db.Set([]byte(k), []byte(strings.Repeat("robin", 40)), cpebble.NoSync); err != nil {
				e.t.Fatal(err)
			}
		}
		if err := db.Flush(); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		e.t.Fatal(err)
	}
}

// TestRewriteCompactExtendsStartWhileTablesAreWritten: the flush and the
// compaction, which Pebble gives no progress of, ask for start time at their
// start and again as each starts a new output table; a store that holds more
// disjoint data asks more often, and nothing is asked for after RewriteCompact
// returned.
func TestRewriteCompactExtendsStartWhileTablesAreWritten(t *testing.T) {
	run := func(flushes int) (during, atBootEnd int) {
		e := headStore(t, vfs.NewMem(), cpOwners()...)
		seedFlushes(e, flushes, 100)
		e.opts.engineTargetFile = 8 << 10 // every compaction output table is a few dozen rows
		var log extendLog
		e.opts.ExtendStart = log.record
		e.opts.DataSteps = []DataStep{m8Step(func(ctx context.Context, db *DB) error {
			if err := db.RewriteCompact(ctx); err != nil {
				return err
			}
			during = log.count(stepRewriteCompact)
			return markDone(ctx, db, "wayne.rewrite")
		})}
		e.open()
		time.Sleep(20 * time.Millisecond)
		return during, log.count(stepRewriteCompact)
	}
	few, fewEnd := run(2)
	many, manyEnd := run(8)
	t.Logf("compaction requests: %d for 2 flushed ranges, %d for 8", few, many)
	if few < 2 {
		t.Errorf("a compaction of 2 flushed tables made %d requests, want its start and at least one new table", few)
	}
	if many <= few {
		t.Errorf("8 flushed tables made %d requests, 2 made %d: the count must grow with the work", many, few)
	}
	if fewEnd != few || manyEnd != many {
		t.Errorf("requests after RewriteCompact returned: %d and %d", fewEnd-few, manyEnd-many)
	}
}

// TestRunWithProgress: the helper that turns an opaque blocking call's
// progress signals into ExtendStart requests. One request per signal, made on
// the caller's goroutine; none while the work is stuck; a signal after the
// work returned is dropped; the work's error comes back and its panic is
// re-raised.
func TestRunWithProgress(t *testing.T) {
	t.Run("one request per signal", func(t *testing.T) {
		progress := make(chan struct{})
		release := make(chan struct{})
		var n atomic.Int32
		errc := make(chan error, 1)
		go func() {
			errc <- runWithProgress(func() error { <-release; return nil }, progress, func() { n.Add(1) })
		}()
		for i := 1; i <= 5; i++ {
			progress <- struct{}{} // unbuffered: returns once the loop took it
			deadline := time.Now().Add(5 * time.Second)
			for n.Load() < int32(i) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := n.Load(); got != int32(i) {
				t.Fatalf("after %d signals the work was extended %d times", i, got)
			}
		}
		close(release)
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a stalled work is not extended", func(t *testing.T) {
		progress := make(chan struct{}, 1)
		release := make(chan struct{})
		var n atomic.Int32
		errc := make(chan error, 1)
		go func() {
			errc <- runWithProgress(func() error { <-release; return nil }, progress, func() { n.Add(1) })
		}()
		time.Sleep(300 * time.Millisecond)
		if got := n.Load(); got != 0 {
			t.Errorf("a work that made no progress was extended %d times", got)
		}
		close(release)
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
		if got := n.Load(); got != 0 {
			t.Errorf("finishing extended the work %d times", got)
		}
	})
	t.Run("the error and the panic come back", func(t *testing.T) {
		progress := make(chan struct{})
		want := fmt.Errorf("joker laughed")
		if got := runWithProgress(func() error { return want }, progress, func() {}); got != want {
			t.Errorf("error = %v, want %v", got, want)
		}
		defer func() {
			if r := recover(); r != "two-face flipped" {
				t.Errorf("recovered %v, want the work's panic", r)
			}
		}()
		_ = runWithProgress(func() error { panic("two-face flipped") }, progress, func() {})
		t.Error("the work's panic was swallowed")
	})
}
