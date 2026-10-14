// file: pkg/ops/opstest/world.go
// version: 1.1.0
// guid: 0c6d1f8e-2b47-4a95-b3e1-7d5c9a2f4e18
// last-edited: 2026-10-14

// Package opstest is the test harness for operations v3 definitions
// (sdk-api.md section 12). A World is a real in-memory store, a fake clock
// and an optional seeded scheduler; World.Run drives a definition through a
// real operations registry with fault injection; Conformance proves a
// definition keeps the runner's invariants:
//
//	opstest.Conformance(t, def, params)
//
// Nothing of the executor is re-implemented here. Each run is enqueued on a
// real registry.Registry (Register, Start, EnqueueOp), so dispatch, the
// exclusive key, the fence, cancel, abandonment, the scan stand-down, the
// Writer (built by the registry's own newV3Writer), the reporter, the commit
// cadence of the chunk ledger and the boot sweep that resumes a crashed run
// are the production code paths. The harness reaches in only through
// registry.RunnerHooks (Options.RunnerHooksForTest), to observe and to inject
// faults, and through the store the registry runs over (worldStore), to
// record writes in commit order and to stop the world on a simulated crash.
//
// The store is the production store (database.NewPebbleStoreInMemory), not a
// fake: every run record transition, ledger commit, snapshot page, intent,
// book and book_file write goes through the same code the server runs.
//
// Where a run differs from production, and why:
//
//   - The runner's clock is fake (fakeClock): it steps 1ms a reading, so the
//     ledger's commit throttle keeps completions folded but uncommitted the
//     way a busy server does, and a crash loses them. CrashMidChunk and
//     InsertBelowCursor step past the throttle instead, so every fold
//     commits: their checks need every other chunk recorded.
//   - Worker count and chunk size come from the run's options (the Job
//     hook), and the registry's watchdog and sweep run hourly: the harness
//     makes the faults itself.
//   - The World supplies the DirtySet compare-and-delete the Pebble store
//     does not have yet (02 PR 14 adds it), so a DirtySet def runs here
//     while production still refuses it (registry.ErrDirtySetUnsupported).
//   - worldStore has no Unwrap and leaves out the warm-up gate, stand-down
//     persistence and v1 operation rows; TestWorldStoreServesEveryCapability
//     fails on any capability the registry resolves that it does not serve.
//   - history_follows_write pairs a history row with the Writer call it was
//     made inside (a write of its book between the call's start and the
//     row); a row made outside every call falls back to any earlier write of
//     its book in the attempt.
//   - A write committed outside every attempt (a goroutine a def leaked,
//     writing after its run ended) is reported through the World's
//     testing.TB at the next Reset and at cleanup, as no_write_after_fence.
//
// Only tests import this package (internal/arch guards it).
package opstest

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// World is an in-memory store + fake clock + seeded scheduler. Its book data
// is reseeded by Reset, so one World (and the defs that captured its Books
// handle) serves many runs.
type World struct {
	t testing.TB

	seedBooks []database.Book
	seedFiles []database.BookFile
	seedDirty map[string][]byte

	store atomic.Pointer[database.PebbleStore]
	// cur is the generation now running: the gate a write made straight
	// through Books() or Journal() passes. A crash kills it; the next run or
	// resume starts a new one.
	cur atomic.Pointer[generation]
	// dirtyMu orders the dirty-set writes the World makes (MarkDirty and the
	// runner's compare-and-delete).
	dirtyMu sync.Mutex

	log   worldLog
	clock fakeClock

	books   *FakeBooks
	journal *Journal

	mu      sync.Mutex
	crashed map[string]bool // op ids a crash left running, not yet resumed
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

// WithDirty seeds a DirtySet: one entry prefix+id per id, marked at epoch 1
// (the 8-byte big-endian mark epoch ops.DirtySet reads).
func WithDirty(prefix string, ids ...string) WorldOpt {
	return func(w *World) {
		if w.seedDirty == nil {
			w.seedDirty = map[string][]byte{}
		}
		for _, id := range ids {
			w.seedDirty[prefix+id] = dirtyEpoch(1)
		}
	}
}

func dirtyEpoch(n uint64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	return b
}

// NewWorld builds a World over a fresh in-memory store, seeded by opts.
func NewWorld(t testing.TB, opts ...WorldOpt) *World {
	t.Helper()
	w := &World{t: t, crashed: map[string]bool{}}
	for _, o := range opts {
		o(w)
	}
	if len(w.seedBooks) == 0 && len(w.seedDirty) == 0 {
		SyntheticBooks(12)(w)
	}
	w.books = &FakeBooks{w: w}
	w.journal = &Journal{w: w}
	w.Reset()
	t.Cleanup(func() {
		w.checkOrphans()
		if s := w.store.Swap(nil); s != nil {
			_ = s.Close()
		}
	})
	return w
}

// Books is the World's book and book_file store. Defs under test read
// through it; a write made through it directly, rather than through the
// run's Writer, is recorded like any other, which is how the harness catches
// a def that bypasses the Writer.
func (w *World) Books() *FakeBooks { return w.books }

// Journal is the World's operation-change journal: the history rows a def
// records itself go through it (the Writer records its own through the
// registry's store).
func (w *World) Journal() *Journal { return w.journal }

// MarkDirty marks a DirtySet entry (prefix+id) at epoch, as the dirty-set
// marker does: a re-mark while an item runs keeps the entry dirty.
func (w *World) MarkDirty(prefix, id string, epoch uint64) error {
	release, err := w.cur.Load().admit()
	if err != nil {
		return err
	}
	defer release()
	w.dirtyMu.Lock()
	defer w.dirtyMu.Unlock()
	return w.st().SetRaw(prefix+id, dirtyEpoch(epoch))
}

// Dirty lists the DirtySet entries left under prefix, as ids.
func (w *World) Dirty(prefix string) []string {
	var out []string
	after := ""
	for {
		page, next, err := w.st().ScanPrefixPage(prefix, after, 256)
		if err != nil {
			w.t.Fatalf("opstest: scan %s: %v", prefix, err)
		}
		for _, kv := range page {
			out = append(out, strings.TrimPrefix(kv.Key, prefix))
		}
		if next == "" {
			return out
		}
		after = next
	}
}

// Reset replaces the store with a fresh one seeded from the World's seed. It
// is a restart: anything a crash left is gone. Call it between runs that
// must not see each other's writes. It first reports every write no run
// accounts for (see checkOrphans).
func (w *World) Reset() {
	w.t.Helper()
	w.checkOrphans()
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
	for k, v := range w.seedDirty {
		if err := s.SetRaw(k, v); err != nil {
			w.t.Fatalf("opstest: seed dirty entry %s: %v", k, err)
		}
	}
	if old := w.store.Swap(s); old != nil {
		_ = old.Close()
	}
	w.cur.Store(newGeneration(w))
	w.books.reset()
	w.mu.Lock()
	clear(w.crashed)
	w.mu.Unlock()
}

func (w *World) st() *database.PebbleStore { return w.store.Load() }

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
// harness marker (attempt start and end, fence revoked, a Writer call's
// start and end) with one global sequence, so "history after its write" and
// "no write after the fence" are comparisons of two numbers. Entries carry
// no attempt number: an Outcome stamps its own copies with its attempt
// (two runs of one registry can be live at once, so a global "current
// attempt" would misattribute).
type worldLog struct {
	mu      sync.Mutex
	seq     int64
	writes  []WriteRecord
	history []HistoryRow
	// windows are the (start, end] spans of every attempt that ended;
	// checked and checkedHistory count the writes and history rows
	// checkOrphans already looked at.
	windows        [][2]int64
	checked        int
	checkedHistory int
	// clears are the DirtySet entries the runner deleted (the item's
	// durable completion for a DirtySet source), by sequence number.
	clears []clearRec
}

type clearRec struct {
	seq int64
	key string
}

func (l *worldLog) addClear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.clears = append(l.clears, clearRec{seq: l.seq, key: key})
}

// clearedIn is the set of DirtySet keys deleted in (from, to].
func (l *worldLog) clearedIn(from, to int64) map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]bool{}
	for _, c := range l.clears {
		if c.seq > from && c.seq <= to {
			out[c.key] = true
		}
	}
	return out
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
	l.writes = append(l.writes, r)
}

func (l *worldLog) addHistory(h HistoryRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	h.Seq = l.seq
	l.history = append(l.history, h)
}

func (l *worldLog) addWindow(from, to int64) {
	l.mu.Lock()
	l.windows = append(l.windows, [2]int64{from, to})
	l.mu.Unlock()
}

// window returns the writes and history rows numbered in (from, to], in
// sequence order, stamped with attempt.
func (l *worldLog) window(from, to int64, attempt int) ([]WriteRecord, []HistoryRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var ws []WriteRecord
	for _, r := range l.writes {
		if r.Seq > from && r.Seq <= to {
			r.Attempt = attempt
			ws = append(ws, r)
		}
	}
	var hs []HistoryRow
	for _, h := range l.history {
		if h.Seq > from && h.Seq <= to {
			h.Attempt = attempt
			hs = append(hs, h)
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Seq < ws[j].Seq })
	sort.Slice(hs, func(i, j int) bool { return hs[i].Seq < hs[j].Seq })
	return ws, hs
}

// orphans returns the writes and history rows not yet checked that lie in
// no ended attempt's window (writes the harness made itself excepted).
func (l *worldLog) orphans() ([]WriteRecord, []HistoryRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	inWindow := func(seq int64) bool {
		for _, win := range l.windows {
			if seq > win[0] && seq <= win[1] {
				return true
			}
		}
		return false
	}
	var ws []WriteRecord
	for _, r := range l.writes[l.checked:] {
		if !r.harness && !inWindow(r.Seq) {
			ws = append(ws, r)
		}
	}
	var hs []HistoryRow
	for _, h := range l.history[l.checkedHistory:] {
		if !inWindow(h.Seq) {
			hs = append(hs, h)
		}
	}
	l.checked, l.checkedHistory = len(l.writes), len(l.history)
	return ws, hs
}

// checkOrphans is no_write_after_fence for what no assertion window sees: a
// write or history row committed while no attempt of any run was live.
// Everything a def commits belongs to an attempt, from the moment the
// registry starts it to its terminal transition (or its crash); a write or
// row outside them all came from a goroutine the def left behind, after its
// run ended, and the fence (which stops writes through the Writer) did not
// stop it. Run by Reset and at the World's cleanup, reported through the
// World's testing.TB.
func (w *World) checkOrphans() {
	ws, hs := w.log.orphans()
	for _, r := range ws {
		w.t.Errorf("opstest: %s: a write committed outside every run attempt: %s %s:%s (seq %d); a goroutine the "+
			"def left behind wrote after its run ended, past the fence", AssertNoWriteAfterFence, r.Op,
			r.Subject.Type, r.Subject.ID, r.Seq)
	}
	for _, h := range hs {
		w.t.Errorf("opstest: %s: a history row was recorded outside every run attempt: book %s field %s (seq %d); "+
			"a goroutine the def left behind recorded it after its run ended, past the fence",
			AssertNoWriteAfterFence, h.BookID, h.Field, h.Seq)
	}
}

// ── fake clock ──────────────────────────────────────────────────────────────

// fakeClock is the runner's clock: it starts at a fixed instant and moves by
// a step per reading, so nothing the runner computes depends on the host's
// speed. A step of 1ms keeps the production commit throttle (2s) from ever
// firing inside a small run, so completions stay folded and uncommitted
// until the final commit, the state a crash must survive; a step above the
// throttle makes every completion commit at once (CrashMidChunk's strong
// form needs every other chunk recorded).
type fakeClock struct{ ns atomic.Int64 }

var clockBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (c *fakeClock) now(step time.Duration) time.Time {
	return clockBase.Add(time.Duration(c.ns.Add(int64(step))))
}

// Intent is a write-ahead intent row (database.Intent).
type Intent = database.Intent

// ── recorded writes ─────────────────────────────────────────────────────────

// modifyBook is the store's ModifyBook under g's gate, recorded when it
// commits. The record's sequence number is taken inside the store's critical
// section, so two writes of one book are numbered in the order they
// committed.
func (w *World) modifyBook(g *generation, id string, fn func(*database.Book) error) (*database.Book, error) {
	release, err := g.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	var seq int64
	var changed []string
	out, err := w.st().ModifyBook(id, func(row *database.Book) error {
		before := fieldsOf(row)
		if ferr := fn(row); ferr != nil {
			return ferr
		}
		changed = diffFields(before, fieldsOf(row))
		seq = w.log.next()
		return nil
	})
	if err != nil || out == nil || seq == 0 {
		return out, err
	}
	w.log.addWrite(WriteRecord{Seq: seq, Subject: ops.Subject{Type: ops.SubjectBook, ID: id}, BookID: id,
		Op: "modify_book", Changed: changed})
	return out, nil
}

func (w *World) modifyBookFile(g *generation, bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	release, err := g.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	var seq int64
	var changed []string
	out, err := w.st().ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		before := fieldsOf(f)
		if ferr := fn(f); ferr != nil {
			return ferr
		}
		changed = diffFields(before, fieldsOf(f))
		seq = w.log.next()
		return nil
	})
	if err != nil || out == nil || seq == 0 {
		return out, err
	}
	w.log.addWrite(WriteRecord{Seq: seq, Subject: ops.Subject{Type: ops.SubjectBookFile, ID: fileID}, BookID: bookID,
		Op: "modify_book_file", Changed: changed})
	return out, nil
}

func (w *World) modifyBookAuthors(g *generation, bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	release, err := g.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	var seq int64
	var changed []string
	out, err := w.st().ModifyBookAuthors(bookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		next, ferr := fn(cur)
		if ferr != nil {
			return nil, ferr
		}
		if !slices.Equal(cur, next) {
			changed = []string{"authors"}
		}
		seq = w.log.next()
		return next, nil
	})
	if err != nil || seq == 0 {
		return out, err
	}
	w.log.addWrite(WriteRecord{Seq: seq, Subject: ops.Subject{Type: ops.SubjectBook, ID: bookID}, BookID: bookID,
		Op: "modify_book_authors", Changed: changed})
	return out, nil
}

// recordMetadataChange is a book-field history row (the Writer's
// HistoryRecorder), recorded in commit order.
func (w *World) recordMetadataChange(g *generation, r *database.MetadataChangeRecord) error {
	release, err := g.admit()
	if err != nil {
		return err
	}
	defer release()
	if err := w.st().RecordMetadataChange(r); err != nil {
		return err
	}
	h := HistoryRow{BookID: r.BookID, Field: r.Field, Kind: "metadata:" + r.ChangeType}
	if r.PreviousValue != nil {
		h.Old = *r.PreviousValue
	}
	if r.NewValue != nil {
		h.New = *r.NewValue
	}
	w.log.addHistory(h)
	return nil
}

// createOperationChange is an operation-journal row, recorded in commit
// order.
func (w *World) createOperationChange(g *generation, c *database.OperationChange) error {
	release, err := g.admit()
	if err != nil {
		return err
	}
	defer release()
	if err := w.st().CreateOperationChange(c); err != nil {
		return err
	}
	w.log.addHistory(HistoryRow{BookID: c.BookID, Field: c.FieldName, Old: c.OldValue, New: c.NewValue,
		Kind: "journal:" + c.ChangeType})
	return nil
}
