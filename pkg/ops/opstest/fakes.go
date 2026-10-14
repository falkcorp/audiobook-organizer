// file: pkg/ops/opstest/fakes.go
// version: 1.0.1
// guid: 5e2a9c71-4d08-4f3b-8a6e-1b9d7c3f2a54
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// errWorldStopped refuses every write after a simulated crash.
var errWorldStopped = errors.New("opstest: the world stopped (simulated crash); write refused")

// ── books ───────────────────────────────────────────────────────────────────

// FakeBooks is the World's book and book_file store. Reads and writes go to
// the World's real in-memory store; every write that commits is recorded, in
// commit order, as a WriteRecord. It also keeps two kinds of side data a def
// can write as the effect of an Effect: per-subject values and append-only
// logs.
//
// It implements the Writer's store interfaces (opswriter.BookStore,
// BookFileStore, AuthorStore). It has no delete, like the Writer.
type FakeBooks struct {
	w *World

	mu     sync.Mutex
	values map[string]map[string]string // subject -> field -> value
	logs   map[string][]string
}

var (
	_ opswriter.BookStore     = (*FakeBooks)(nil)
	_ opswriter.BookFileStore = (*FakeBooks)(nil)
	_ opswriter.AuthorStore   = (*FakeBooks)(nil)
)

func (b *FakeBooks) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values = map[string]map[string]string{}
	b.logs = map[string][]string{}
}

// GetBookByID reads one book; nil when it does not exist.
func (b *FakeBooks) GetBookByID(id string) (*database.Book, error) { return b.w.st().GetBookByID(id) }

// AllBooks returns every book, sorted by id.
func (b *FakeBooks) AllBooks() ([]database.Book, error) {
	ids, err := b.w.st().ListBookIDs()
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	out := make([]database.Book, 0, len(ids))
	for _, id := range ids {
		book, err := b.w.st().GetBookByID(id)
		if err != nil {
			return nil, err
		}
		if book != nil {
			out = append(out, *book)
		}
	}
	return out, nil
}

// Page returns up to limit books with an id above after, in id order, and
// the cursor for the next page ("" at the end). It reads the store each
// call, so a book inserted while a run pages shows up on the pages after it.
func (b *FakeBooks) Page(after string, limit int) ([]database.Book, string, error) {
	all, err := b.AllBooks()
	if err != nil {
		return nil, "", err
	}
	i := sort.Search(len(all), func(i int) bool { return all[i].ID > after })
	end := min(i+max(limit, 1), len(all))
	page := all[i:end]
	if end >= len(all) || len(page) == 0 {
		return page, "", nil
	}
	return page, page[len(page)-1].ID, nil
}

// GetBookFiles returns a book's files.
func (b *FakeBooks) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return b.w.st().GetBookFiles(bookID)
}

// AllBookFiles returns every file of every book, sorted by file id.
func (b *FakeBooks) AllBookFiles() ([]database.BookFile, error) {
	books, err := b.AllBooks()
	if err != nil {
		return nil, err
	}
	var out []database.BookFile
	for _, book := range books {
		fs, err := b.w.st().GetBookFiles(book.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetBookFileByID implements opswriter.BookFileStore.
func (b *FakeBooks) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	return b.w.st().GetBookFileByID(bookID, fileID)
}

// GetBookFileByPath implements opswriter.BookFileStore.
func (b *FakeBooks) GetBookFileByPath(path string) (*database.BookFile, error) {
	return b.w.st().GetBookFileByPath(path)
}

// GetBookAuthors returns a book's credit list.
func (b *FakeBooks) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	return b.w.st().GetBookAuthors(bookID)
}

// ModifyBook implements opswriter.BookStore: the store's own ModifyBook,
// recorded when it commits. The record's sequence number is taken inside the
// store's critical section, so two writes of one book are numbered in the
// order they committed.
func (b *FakeBooks) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	release, err := b.w.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	var seq int64
	var changed []string
	out, err := b.w.st().ModifyBook(id, func(row *database.Book) error {
		before := fieldsOf(row)
		if ferr := fn(row); ferr != nil {
			return ferr
		}
		changed = diffFields(before, fieldsOf(row))
		seq = b.w.log.next()
		return nil
	})
	if err != nil || out == nil || seq == 0 {
		return out, err
	}
	b.w.log.addWrite(WriteRecord{Seq: seq, Subject: ops.Subject{Type: ops.SubjectBook, ID: id}, BookID: id,
		Op: "modify_book", Changed: changed})
	return out, nil
}

// ModifyBookFile implements opswriter.BookFileStore, recorded like ModifyBook.
func (b *FakeBooks) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	release, err := b.w.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	var seq int64
	var changed []string
	out, err := b.w.st().ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		before := fieldsOf(f)
		if ferr := fn(f); ferr != nil {
			return ferr
		}
		changed = diffFields(before, fieldsOf(f))
		seq = b.w.log.next()
		return nil
	})
	if err != nil || out == nil || seq == 0 {
		return out, err
	}
	b.w.log.addWrite(WriteRecord{Seq: seq, Subject: ops.Subject{Type: ops.SubjectBookFile, ID: fileID}, BookID: bookID,
		Op: "modify_book_file", Changed: changed})
	return out, nil
}

// ModifyBookAuthors implements opswriter.AuthorStore, recorded like
// ModifyBook.
func (b *FakeBooks) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	release, err := b.w.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	var seq int64
	var changed []string
	out, err := b.w.st().ModifyBookAuthors(bookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		next, ferr := fn(cur)
		if ferr != nil {
			return nil, ferr
		}
		if !slices.Equal(cur, next) {
			changed = []string{"authors"}
		}
		seq = b.w.log.next()
		return next, nil
	})
	if err != nil || seq == 0 {
		return out, err
	}
	b.w.log.addWrite(WriteRecord{Seq: seq, Subject: ops.Subject{Type: ops.SubjectBook, ID: bookID}, BookID: bookID,
		Op: "modify_book_authors", Changed: changed})
	return out, nil
}

// InsertBook creates a book (InsertBelowCursor's insert, or a def's). It is
// recorded as a write.
func (b *FakeBooks) InsertBook(book database.Book) error {
	release, err := b.w.admit()
	if err != nil {
		return err
	}
	defer release()
	if _, err := b.w.st().CreateBook(&book); err != nil {
		return err
	}
	b.w.log.addWrite(WriteRecord{Subject: ops.Subject{Type: ops.SubjectBook, ID: book.ID}, BookID: book.ID,
		Op: "insert_book", Changed: []string{"id"}})
	return nil
}

// PutValue sets a side value of a subject: the stand-in for a write the
// typed Writer surface does not cover (a file reflink, an index entry). A
// def makes it inside rc.Writer().Effect. Changed is empty when the value
// was already set.
func (b *FakeBooks) PutValue(sub ops.Subject, field, value string) error {
	release, err := b.w.admit()
	if err != nil {
		return err
	}
	defer release()
	key := string(sub.Type) + ":" + sub.ID
	bookID := b.bookOf(sub)
	b.mu.Lock()
	m := b.values[key]
	if m == nil {
		m = map[string]string{}
		b.values[key] = m
	}
	old, had := m[field]
	m[field] = value
	var changed []string
	if !had || old != value {
		changed = []string{field}
	}
	r := WriteRecord{Subject: sub, BookID: bookID, Op: "put_value", Changed: changed}
	b.w.log.addWrite(r) // inside b.mu: two writes of one value are numbered in commit order
	b.mu.Unlock()
	return nil
}

// Value reads a side value.
func (b *FakeBooks) Value(sub ops.Subject, field string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[string(sub.Type)+":"+sub.ID][field]
}

// AppendLog appends an entry to a named append-only log. Every append
// changes the log; it is how a test models a side effect that is not
// idempotent.
func (b *FakeBooks) AppendLog(name, entry string) error {
	release, err := b.w.admit()
	if err != nil {
		return err
	}
	defer release()
	b.mu.Lock()
	b.logs[name] = append(b.logs[name], entry)
	b.w.log.addWrite(WriteRecord{Subject: ops.Subject{Type: "log", ID: name}, Op: "append_log",
		Changed: []string{"entries"}})
	b.mu.Unlock()
	return nil
}

// Log returns a named log's entries in append order.
func (b *FakeBooks) Log(name string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.logs[name])
}

// bookOf is the book a subject belongs to ("" when unknown): a book
// subject is its own book, and a book_file subject's book is looked up among
// the World's files (a scan; Worlds are small). Callers resolve it before
// taking the side-data lock.
func (b *FakeBooks) bookOf(sub ops.Subject) string {
	switch sub.Type {
	case ops.SubjectBook:
		return sub.ID
	case ops.SubjectBookFile:
		files, err := b.AllBookFiles()
		if err != nil {
			return ""
		}
		for _, f := range files {
			if f.ID == sub.ID {
				return f.BookID
			}
		}
		return ""
	default:
		return ""
	}
}

func (b *FakeBooks) dumpSide() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var s strings.Builder
	for _, k := range slices.Sorted(maps.Keys(b.values)) {
		m := b.values[k]
		for _, f := range slices.Sorted(maps.Keys(m)) {
			fmt.Fprintf(&s, "value %s %s=%q\n", k, f, m[f])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(b.logs)) {
		entries := slices.Clone(b.logs[k])
		slices.Sort(entries)
		fmt.Fprintf(&s, "log %s %q\n", k, entries)
	}
	return s.String()
}

// fieldsOf is a row's fields by JSON name.
func fieldsOf(v any) map[string]string {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, x := range m {
		out[k] = string(x)
	}
	return out
}

// diffFields lists the fields that differ, sorted.
func diffFields(a, b map[string]string) []string {
	var out []string
	for k, v := range b {
		if a[k] != v {
			out = append(out, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ── history ─────────────────────────────────────────────────────────────────

// Journal is the World's operation-change journal (opswriter.ChangeJournal),
// recorded as HistoryRows in commit order.
type Journal struct{ w *World }

var _ opswriter.ChangeJournal = (*Journal)(nil)

// CreateOperationChange records one operation change row.
func (j *Journal) CreateOperationChange(c *database.OperationChange) error {
	release, err := j.w.admit()
	if err != nil {
		return err
	}
	defer release()
	if err := j.w.st().CreateOperationChange(c); err != nil {
		return err
	}
	j.w.log.addHistory(HistoryRow{BookID: c.BookID, Field: c.FieldName, Old: c.OldValue, New: c.NewValue,
		Kind: "journal:" + c.ChangeType})
	return nil
}

// metaRecorder is the World's metadata-history recorder
// (opswriter.HistoryRecorder): book-field rows.
type metaRecorder struct{ w *World }

var _ opswriter.HistoryRecorder = (*metaRecorder)(nil)

func (m *metaRecorder) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	release, err := m.w.admit()
	if err != nil {
		return err
	}
	defer release()
	if err := m.w.st().RecordMetadataChange(r); err != nil {
		return err
	}
	h := HistoryRow{BookID: r.BookID, Field: r.Field, Kind: "metadata:" + r.ChangeType}
	if r.PreviousValue != nil {
		h.Old = *r.PreviousValue
	}
	if r.NewValue != nil {
		h.New = *r.NewValue
	}
	m.w.log.addHistory(h)
	return nil
}

// ── the run store ───────────────────────────────────────────────────────────

// opsStore is the runner's chunk store and the Writer's intent store: the
// real store's methods (so the CAS, terminal-run and mark rules are the
// production ones), refused after a crash and observed on commit.
type opsStore struct{ w *World }

var (
	_ database.OpsV3ChunkStore = (*opsStore)(nil)
	_ opswriter.IntentStore    = (*opsStore)(nil)
)

func (s *opsStore) write(fn func(*database.PebbleStore) error) error {
	release, err := s.w.admit()
	if err != nil {
		return err
	}
	defer release()
	return fn(s.w.st())
}

func (s *opsStore) PutSnapshotPageV3(id string, page int, keys []string) error {
	return s.write(func(p *database.PebbleStore) error { return p.PutSnapshotPageV3(id, page, keys) })
}

func (s *opsStore) GetSnapshotPageV3(id string, page int) ([]string, bool, error) {
	return s.w.st().GetSnapshotPageV3(id, page)
}

func (s *opsStore) PutChunkOffsetsV3(id string, offsets []int32) error {
	return s.write(func(p *database.PebbleStore) error { return p.PutChunkOffsetsV3(id, offsets) })
}

func (s *opsStore) GetChunkOffsetsV3(id string) ([]int32, bool, error) {
	return s.w.st().GetChunkOffsetsV3(id)
}

func (s *opsStore) CommitLedgerV3(id string, b database.LedgerBatch) error {
	if err := s.write(func(p *database.PebbleStore) error { return p.CommitLedgerV3(id, b) }); err != nil {
		return err
	}
	if a := s.w.attemptOf(id); a != nil {
		a.observeCommit(b.Ledger)
	}
	return nil
}

func (s *opsStore) GetLedgerV3(id string) (*database.Ledger, error) { return s.w.st().GetLedgerV3(id) }

func (s *opsStore) ListMarksV3(id string) (map[string][]string, error) {
	return s.w.st().ListMarksV3(id)
}

func (s *opsStore) PutIntentV3(id string, in database.Intent) error {
	return s.write(func(p *database.PebbleStore) error { return p.PutIntentV3(id, in) })
}

func (s *opsStore) ClearIntentV3(id string, seq uint64) error {
	return s.write(func(p *database.PebbleStore) error { return p.ClearIntentV3(id, seq) })
}

func (s *opsStore) ListIntentsV3(id string) ([]database.Intent, error) {
	return s.w.st().ListIntentsV3(id)
}

// ── the run's Writer ────────────────────────────────────────────────────────

// trackedWriter is the run's Writer as an item sees it: the production
// opswriter.Writer, with every call counted in flight. A fault that revokes
// the fence first waits for the calls in flight to finish (drain), so a
// write that passed the fence check before the revoke is committed before
// the revoke is logged, and a write logged after it can only be one that
// started after it. It embeds the interface, so a method added to ops.Writer
// later passes straight through (untracked until it is added here).
//
// A Writer callback (ModifyBook's fn, Effect's fn) must not call the Writer
// again: the nested call would wait on a drain that waits on its caller.
type trackedWriter struct {
	ops.Writer
	drain *sync.RWMutex
}

func (t *trackedWriter) ModifyBook(ctx context.Context, bookID string, fn func(*database.Book) error) (ops.Changed, error) {
	t.drain.RLock()
	defer t.drain.RUnlock()
	return t.Writer.ModifyBook(ctx, bookID, fn)
}

func (t *trackedWriter) ModifyBookFile(ctx context.Context, bookID, fileID string, fn func(*database.BookFile) error) (ops.Changed, error) {
	t.drain.RLock()
	defer t.drain.RUnlock()
	return t.Writer.ModifyBookFile(ctx, bookID, fileID, fn)
}

func (t *trackedWriter) RepointBookFile(ctx context.Context, bookID, fileID string, expect, to database.BookFileLocation) error {
	t.drain.RLock()
	defer t.drain.RUnlock()
	return t.Writer.RepointBookFile(ctx, bookID, fileID, expect, to)
}

func (t *trackedWriter) ModifyBookAuthors(ctx context.Context, bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) (ops.Changed, error) {
	t.drain.RLock()
	defer t.drain.RUnlock()
	return t.Writer.ModifyBookAuthors(ctx, bookID, fn)
}

func (t *trackedWriter) Effect(ctx context.Context, e ops.EffectSpec, fn func() ([]ops.HistoryRow, error)) error {
	t.drain.RLock()
	defer t.drain.RUnlock()
	return t.Writer.Effect(ctx, e, fn)
}

// ── scan stand-down ─────────────────────────────────────────────────────────

// standDown is the fake scan stand-down: a lease that holds until LoseLeaseAt
// says it was lost. The Writer renews it on every write, as in production.
type standDown struct {
	mu   sync.Mutex
	held bool
	lost bool
}

func (s *standDown) renew() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held && !s.lost
}

func (s *standDown) isHeld() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

func (s *standDown) lose() {
	s.mu.Lock()
	s.lost = true
	s.mu.Unlock()
}

type standDownHold struct {
	sd   *standDown
	w    *opswriter.Writer
	once sync.Once
}

func (h *standDownHold) Release() {
	h.once.Do(func() {
		h.sd.mu.Lock()
		h.sd.held = false
		h.sd.mu.Unlock()
		if h.w != nil {
			h.w.SetLease(nil)
			h.w.SetLeaseValid(nil)
		}
	})
}

// ── host ────────────────────────────────────────────────────────────────────

// worldHost is the executor's half of a run context (ops.Host).
type worldHost struct {
	a *attempt
}

func (h *worldHost) Progress(done int64, total ops.Total, note string) {
	if done < 0 {
		return
	}
	p := ops.ProgressSnapshot{Done: done, Current: note}
	if n, ok := total.Value(); ok {
		p.Total = &n
	}
	h.a.sink.Published(p)
}

func (h *worldHost) Touch() {}

func (h *worldHost) Phase(string) {}

func (h *worldHost) Checkpoint(raw json.RawMessage) error {
	release, err := h.a.w.admit()
	if err != nil {
		return err
	}
	defer release()
	h.a.w.mu.Lock()
	h.a.w.checkpoints[h.a.opID] = slices.Clone(raw)
	h.a.w.mu.Unlock()
	return nil
}

func (h *worldHost) Restore() (json.RawMessage, bool, error) {
	h.a.w.mu.Lock()
	defer h.a.w.mu.Unlock()
	raw, ok := h.a.w.checkpoints[h.a.opID]
	return slices.Clone(raw), ok, nil
}

func (h *worldHost) Enqueue(def ops.Definition, _ json.RawMessage, _ ops.EnqueueConfig) (ops.RunRef, error) {
	return ops.RunRef{}, fmt.Errorf("opstest: %s: child runs are not supported by the harness", def.ID())
}

func (h *worldHost) Await(context.Context, ops.RunRef) (ops.Outcome, error) {
	return ops.Outcome{}, errors.New("opstest: child runs are not supported by the harness")
}

func (h *worldHost) StandDown(context.Context) (ops.Hold, error) {
	sd := h.a.sd
	sd.mu.Lock()
	sd.held, sd.lost = true, false
	sd.mu.Unlock()
	if h.a.writer != nil {
		h.a.writer.SetLease(sd.renew)
		h.a.writer.SetLeaseValid(sd.renew)
	}
	return &standDownHold{sd: sd, w: h.a.writer}, nil
}

// ── progress ────────────────────────────────────────────────────────────────

// ProgressSink is where a run's progress goes. The default records every
// snapshot the runner publishes (after its ledger commit) and those a Task
// sets. A test may replace it, per attempt, to model a different reporter;
// the mutation tests replace it with one that counts in-flight items.
type ProgressSink interface {
	// Published receives every snapshot the runner publishes.
	Published(p ops.ProgressSnapshot)
	// ItemStarted is called when an item starts, from its worker.
	ItemStarted(chunk, item int)
	// Snapshots is what the Outcome records as Progress.
	Snapshots() []ops.ProgressSnapshot
}

type recordingSink struct {
	mu    sync.Mutex
	snaps []ops.ProgressSnapshot
}

func (s *recordingSink) Published(p ops.ProgressSnapshot) {
	s.mu.Lock()
	s.snaps = append(s.snaps, p)
	s.mu.Unlock()
}

func (s *recordingSink) ItemStarted(int, int) {}

func (s *recordingSink) Snapshots() []ops.ProgressSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.snaps)
}

// toOps converts the runner's snapshot to the SDK's (the two encode the same
// JSON; a pkg/ops test pins it).
func toOps(p database.ProgressSnapshot) ops.ProgressSnapshot {
	out := ops.ProgressSnapshot{Phase: p.Phase, Phases: slices.Clone(p.Phases), Done: p.Done, Unit: p.Unit,
		Failed: p.Failed, Counters: maps.Clone(p.Counters), RatePerS: p.RatePerS, Current: p.Current,
		UpdatedAt: p.UpdatedAt}
	if p.Total != nil {
		n := *p.Total
		out.Total = &n
	}
	if p.ETA != nil {
		e := *p.ETA
		out.ETA = &e
	}
	if p.Chunks != nil {
		out.Chunks = &ops.ChunkProgress{Done: p.Chunks.Done, Total: p.Chunks.Total, Size: p.Chunks.Size}
	}
	for _, wr := range p.Workers {
		out.Workers = append(out.Workers, ops.WorkerProgress{Worker: wr.Worker, Chunk: wr.Chunk, Item: wr.Item,
			ItemIndex: wr.ItemIndex, ChunkSize: wr.ChunkSize, LeasedAt: wr.LeasedAt, Heartbeat: wr.Heartbeat,
			Stuck: wr.Stuck})
	}
	return out
}

// ── seeded scheduler ────────────────────────────────────────────────────────

// scheduler is Seed's deterministic interleaving: a baton only one worker
// holds. A worker gives it up when it reaches its next gate (before taking a
// chunk, before each item) or exits, and the scheduler hands it to one of
// the waiting workers, chosen by the seeded source, only once every worker
// is either waiting or gone. So which worker takes which chunk and the order
// items run in are a function of the seed alone.
//
// It serializes the items, which hides races between them from -race:
// Conformance never seeds; Seed is for tests of ordering.
type scheduler struct {
	mu      sync.Mutex
	rng     *rand.Rand
	workers int
	holder  int
	waiting map[int]chan struct{}
	gone    map[int]bool
}

func newScheduler(seed int64, workers int) *scheduler {
	return &scheduler{rng: rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15)), workers: workers,
		holder: -1, waiting: map[int]chan struct{}{}, gone: map[int]bool{}}
}

// gate gives up the baton (if w holds it) and waits for it.
func (s *scheduler) gate(w int) {
	ch := make(chan struct{})
	s.mu.Lock()
	if s.holder == w {
		s.holder = -1
	}
	s.waiting[w] = ch
	s.handOff()
	s.mu.Unlock()
	<-ch
}

// exit gives up the baton for good. Idempotent.
func (s *scheduler) exit(w int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone[w] {
		return
	}
	s.gone[w] = true
	if s.holder == w {
		s.holder = -1
	}
	s.handOff()
}

func (s *scheduler) handOff() {
	if s.holder != -1 || len(s.waiting) == 0 || len(s.waiting)+len(s.gone) < s.workers {
		return
	}
	ids := slices.Sorted(maps.Keys(s.waiting))
	pick := ids[s.rng.IntN(len(ids))]
	ch := s.waiting[pick]
	delete(s.waiting, pick)
	s.holder = pick
	close(ch)
}
