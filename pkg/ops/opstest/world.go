// file: pkg/ops/opstest/world.go
// version: 1.0.0
// guid: 0c6d1f8e-2b47-4a95-b3e1-7d5c9a2f4e18
// last-edited: 2026-10-14

// Package opstest is the test harness for operations v3 definitions
// (sdk-api.md section 12). A World is a real in-memory store, a fake clock
// and an optional seeded scheduler; World.Run drives a definition through the
// production batch runner with fault injection; Conformance proves a
// definition keeps the runner's invariants:
//
//	opstest.Conformance(t, def, params)
//
// The store is the production store (database.NewPebbleStoreInMemory), not a
// fake: every run record transition, ledger commit, snapshot page, intent,
// book and book_file write goes through the same code the server runs, so
// the harness can be no more permissive than production. The harness only
// wraps it to record writes and history rows in commit order and to stop the
// world on a simulated crash.
//
// Only tests import this package.
package opstest

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// World is an in-memory store + fake clock + seeded scheduler. Its book data
// is reseeded by Reset, so one World (and the defs that captured its Books
// handle) serves many runs.
type World struct {
	t testing.TB

	seedBooks []database.Book
	seedFiles []database.BookFile

	store atomic.Pointer[database.PebbleStore]

	// gate orders writes against a crash: every recorded write holds it for
	// reading; a crash takes it for writing and sets frozen, so no write is
	// in flight at the crash and none is accepted after it.
	gate   sync.RWMutex
	frozen bool

	log   worldLog
	clock fakeClock
	keys  keyTable
	opSeq atomic.Int64

	books   *FakeBooks
	journal *Journal
	meta    *metaRecorder
	ops     *opsStore

	mu          sync.Mutex
	attempts    map[string]*attempt // op id -> the live attempt
	crashed     map[string]bool     // op ids a crash left running
	checkpoints map[string]json.RawMessage
}

// WorldOpt configures NewWorld.
type WorldOpt func(*World)

// SyntheticBooks seeds n books book-001..book-n, each with one book file
// file-001..file-n under an obviously synthetic path. NewWorld seeds 12 when
// no WorldOpt seeds anything.
func SyntheticBooks(n int) WorldOpt {
	return func(w *World) {
		for i := 1; i <= n; i++ {
			id := fmt.Sprintf("book-%03d", i)
			w.seedBooks = append(w.seedBooks, database.Book{ID: id, Title: fmt.Sprintf("Synthetic Title %03d", i),
				FilePath: "/library/synthetic/" + id, Format: "m4b"})
			w.seedFiles = append(w.seedFiles, database.BookFile{ID: fmt.Sprintf("file-%03d", i), BookID: id,
				FilePath: "/library/synthetic/" + id + "/part-1.m4b", Format: "m4b"})
		}
	}
}

// WithBooks seeds these books (and no synthetic ones).
func WithBooks(books ...database.Book) WorldOpt {
	return func(w *World) { w.seedBooks = append(w.seedBooks, books...) }
}

// WithBookFiles seeds these book files; their books must be seeded too.
func WithBookFiles(files ...database.BookFile) WorldOpt {
	return func(w *World) { w.seedFiles = append(w.seedFiles, files...) }
}

// NewWorld builds a World over a fresh in-memory store, seeded by opts.
func NewWorld(t testing.TB, opts ...WorldOpt) *World {
	t.Helper()
	w := &World{t: t, attempts: map[string]*attempt{}, crashed: map[string]bool{},
		checkpoints: map[string]json.RawMessage{}}
	for _, o := range opts {
		o(w)
	}
	if len(opts) == 0 {
		SyntheticBooks(12)(w)
	}
	w.books = &FakeBooks{w: w}
	w.journal = &Journal{w: w}
	w.meta = &metaRecorder{w: w}
	w.ops = &opsStore{w: w}
	w.keys.init()
	w.Reset()
	t.Cleanup(func() {
		if s := w.store.Swap(nil); s != nil {
			_ = s.Close()
		}
	})
	return w
}

// Books is the World's book and book_file store. Defs under test read
// through it (it implements the read and write interfaces ops use); a write
// made through it directly, rather than through the run's Writer, is
// recorded like any other, which is how the harness catches a def that
// bypasses the Writer.
func (w *World) Books() *FakeBooks { return w.books }

// Journal is the World's operation-change journal: the history rows the
// Writer records for non-book writes. A def that records history itself goes
// through it.
func (w *World) Journal() *Journal { return w.journal }

// Reset replaces the store with a fresh one seeded from the World's seed and
// clears every side value. It is a restart: anything a crash left frozen is
// gone. Call it between runs that must not see each other's writes.
func (w *World) Reset() {
	w.t.Helper()
	s, err := database.NewPebbleStoreInMemory("/opstest")
	if err != nil {
		w.t.Fatalf("opstest: open the in-memory store: %v", err)
	}
	for i := range w.seedBooks {
		b := w.seedBooks[i]
		if _, err := s.CreateBook(&b); err != nil {
			w.t.Fatalf("opstest: seed book %s: %v", b.ID, err)
		}
	}
	for i := range w.seedFiles {
		f := w.seedFiles[i]
		if err := s.CreateBookFile(&f); err != nil {
			w.t.Fatalf("opstest: seed book file %s: %v", f.ID, err)
		}
	}
	if old := w.store.Swap(s); old != nil {
		_ = old.Close()
	}
	w.gate.Lock()
	w.frozen = false
	w.gate.Unlock()
	w.books.reset()
	w.mu.Lock()
	clear(w.crashed)
	clear(w.checkpoints)
	w.mu.Unlock()
}

func (w *World) st() *database.PebbleStore { return w.store.Load() }

// admit is taken by every recorded write for its duration; it refuses once a
// crash froze the world. The caller runs release when its write is done.
func (w *World) admit() (release func(), err error) {
	w.gate.RLock()
	if w.frozen {
		w.gate.RUnlock()
		return nil, errWorldStopped
	}
	return w.gate.RUnlock, nil
}

// freeze stops the world: it waits for the writes in flight, then refuses
// every later one until boot.
func (w *World) freeze() {
	w.gate.Lock()
	w.frozen = true
	w.gate.Unlock()
}

// boot is a process start after a crash: the world accepts writes again and
// every run the crash left running or stopping is swept to interrupted, as
// the registry's boot sweep does.
func (w *World) boot() error {
	w.gate.Lock()
	w.frozen = false
	w.gate.Unlock()
	w.mu.Lock()
	ids := slices.Sorted(maps.Keys(w.crashed))
	clear(w.crashed)
	w.mu.Unlock()
	for _, id := range ids {
		rec, err := w.st().GetRunV3(id)
		if err != nil || rec == nil {
			return fmt.Errorf("opstest: boot sweep of %s: read the run: %v", id, err)
		}
		if _, err := w.st().TransitionRunV3(database.TransitionRequest{ID: id, ExpectState: rec.State,
			ExpectAttempt: rec.Attempt, ExpectFenceEpoch: rec.FenceEpoch, Event: state.EvBootSweep,
			InterruptedWhy: state.WhyCrash}); err != nil {
			return fmt.Errorf("opstest: boot sweep of %s: %w", id, err)
		}
	}
	return nil
}

// State is a canonical dump of the World's data: every book, its files and
// credits, and every side value and log. Two runs that left the same effects
// dump equal. Time stamps (every *_at field) are left out, and log entries
// are sorted, so the order concurrent items wrote in does not matter.
func (w *World) State() string {
	w.t.Helper()
	s := w.st()
	ids, err := s.ListBookIDs()
	if err != nil {
		w.t.Fatalf("opstest: list books: %v", err)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		book, err := s.GetBookByID(id)
		if err != nil || book == nil {
			w.t.Fatalf("opstest: read book %s: %v", id, err)
		}
		b.WriteString("book " + canonical(book) + "\n")
		files, err := s.GetBookFiles(id)
		if err != nil {
			w.t.Fatalf("opstest: read files of %s: %v", id, err)
		}
		sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })
		for i := range files {
			b.WriteString("file " + canonical(&files[i]) + "\n")
		}
		credits, err := s.GetBookAuthors(id)
		if err != nil {
			w.t.Fatalf("opstest: read credits of %s: %v", id, err)
		}
		if len(credits) > 0 {
			b.WriteString("credits " + id + " " + canonical(credits) + "\n")
		}
	}
	b.WriteString(w.books.dumpSide())
	return b.String()
}

// State leaves out every JSON field named *_at. The store stamps
// created_at and updated_at on every write (checked against a real book and
// book file), and any other time a def stamps comes from the run's clock,
// which differs between a run and its re-run, so a time can never be compared
// for idempotence. A def whose effect is a time stamp is compared on its other
// fields.

// canonical is v as JSON with its keys sorted and *_at fields left out.
func canonical(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	var m any
	if err := json.Unmarshal(raw, &m); err != nil {
		return string(raw)
	}
	strip(m)
	out, err := json.Marshal(m) // map keys are sorted by encoding/json
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func strip(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, inner := range x {
			if strings.HasSuffix(k, "_at") {
				delete(x, k)
				continue
			}
			strip(inner)
		}
	case []any:
		for _, inner := range x {
			strip(inner)
		}
	}
}

// ── the commit-ordered log ──────────────────────────────────────────────────

// worldLog numbers every recorded write, every history row and every
// harness marker (attempt start and end, fence revoked) with one global
// sequence, so "history after its write" and "no write after the fence" are
// comparisons of two numbers.
type worldLog struct {
	mu      sync.Mutex
	seq     int64
	attempt int // the attempt number writes are attributed to
	writes  []WriteRecord
	history []HistoryRow
}

// next reserves the next sequence number.
func (l *worldLog) next() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	return l.seq
}

func (l *worldLog) addWrite(r WriteRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.Seq == 0 {
		l.seq++
		r.Seq = l.seq
	}
	r.Attempt = l.attempt
	l.writes = append(l.writes, r)
}

func (l *worldLog) addHistory(h HistoryRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	h.Seq = l.seq
	h.Attempt = l.attempt
	l.history = append(l.history, h)
}

func (l *worldLog) setAttempt(n int) {
	l.mu.Lock()
	l.attempt = n
	l.mu.Unlock()
}

// window returns the writes and history rows numbered in (from, to], in
// sequence order.
func (l *worldLog) window(from, to int64) ([]WriteRecord, []HistoryRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var ws []WriteRecord
	for _, r := range l.writes {
		if r.Seq > from && r.Seq <= to {
			ws = append(ws, r)
		}
	}
	var hs []HistoryRow
	for _, h := range l.history {
		if h.Seq > from && h.Seq <= to {
			hs = append(hs, h)
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Seq < ws[j].Seq })
	sort.Slice(hs, func(i, j int) bool { return hs[i].Seq < hs[j].Seq })
	return ws, hs
}

// ── fake clock ──────────────────────────────────────────────────────────────

// fakeClock is the runner's clock: it starts at a fixed instant and moves one
// millisecond per reading, so nothing the runner computes depends on the
// host's speed.
type fakeClock struct{ ticks atomic.Int64 }

var clockBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (c *fakeClock) now() time.Time {
	return clockBase.Add(time.Duration(c.ticks.Add(1)) * time.Millisecond)
}

// ── exclusive keys ──────────────────────────────────────────────────────────

// keyTable is the registry's exclusive-key rule reproduced inside the
// harness: a run holds its def's Exclusive key from dispatch until its last
// goroutine returned, and a second run of the same key waits queued until
// then.
type keyTable struct {
	mu   sync.Mutex
	cond *sync.Cond
	held map[string]bool
}

func (k *keyTable) init() {
	k.cond = sync.NewCond(&k.mu)
	k.held = map[string]bool{}
}

// acquire takes key; when it is held it calls queued once and waits.
func (k *keyTable) acquire(key string, queued func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	signaled := false
	for k.held[key] {
		if !signaled && queued != nil {
			signaled = true
			queued()
		}
		k.cond.Wait()
	}
	k.held[key] = true
}

func (k *keyTable) release(key string) {
	k.mu.Lock()
	delete(k.held, key)
	k.mu.Unlock()
	k.cond.Broadcast()
}

// Intent is a write-ahead intent row (database.Intent).
type Intent = database.Intent

// stateOf reads a run's v3 state.
func (w *World) stateOf(opID string) (*database.RunRecord, error) {
	rec, err := w.st().GetRunV3(opID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("opstest: run %s not found", opID)
	}
	return rec, nil
}

// transition moves a run with the store's compare-and-set rule (state,
// attempt and fence epoch must be the ones just read).
func (w *World) transition(opID string, req database.TransitionRequest) (*database.RunRecord, error) {
	rec, err := w.stateOf(opID)
	if err != nil {
		return nil, err
	}
	req.ID, req.ExpectState, req.ExpectAttempt, req.ExpectFenceEpoch = opID, rec.State, rec.Attempt, rec.FenceEpoch
	return w.st().TransitionRunV3(req)
}

// mode is the run record's spelling of a mode.
func modeString(live bool) string {
	if live {
		return ops.Live.String()
	}
	return ops.Preview.String()
}

// attemptOf is the live attempt of opID, or nil.
func (w *World) attemptOf(opID string) *attempt {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts[opID]
}
