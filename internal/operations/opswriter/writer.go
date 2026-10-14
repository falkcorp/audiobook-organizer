// file: internal/operations/opswriter/writer.go
// version: 1.3.0
// guid: 52ea77b5-ab1a-4c42-b133-c362543494c0
// last-edited: 2026-10-13

package opswriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// ErrStandDownLost is the default error a write is refused with once the
// run's scan stand-down lease is gone (Config.LeaseLostErr overrides it).
var ErrStandDownLost = errors.New("opswriter: scan stand-down lease lost; remaining writes not made")

// ErrNotJournaled refuses a write whose history row could not be recorded
// because the Writer has no operation journal: the write is not made.
var ErrNotJournaled = errors.New("opswriter: no operation journal; the write was not made")

// ErrNoHistory refuses a Live ModifyBook when the Writer has no metadata
// history recorder: the field changes would commit with nothing recording
// them, so neither undo nor an audit could see them.
var ErrNoHistory = errors.New("opswriter: no metadata history recorder; the write was not made")

// repointDetail is a repoint intent's Planned detail.
type repointDetail struct {
	BookID string `json:"book_id"`
	From   string `json:"from"` // database.BookFileLocation JSON
	To     string `json:"to"`
}

// pkgLog is the subsystem logger for this package's own warnings.
var pkgLog = logger.New("opswriter")

// BookStore is the book write primitive.
type BookStore interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// BookFileStore is the book_file surface. It has no delete method on purpose.
type BookFileStore interface {
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	// GetBookFileByPath names the row holding a path's single-owner key, so a
	// repoint's history can hand the key back on revert.
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
}

// AuthorStore is the book credit surface.
type AuthorStore interface {
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
}

// Config builds a Writer. Nil stores make the matching methods refuse.
type Config struct {
	OpID string
	Mode ops.Mode
	// Fence is the run's write permit check (Fence.Check); nil = unfenced.
	Fence func() error
	// Intents is the write-ahead store. Nil writes no intents: the
	// internal/repairs wrapper journals each step first itself.
	Intents IntentStore
	Books   BookStore
	Files   BookFileStore
	Authors AuthorStore
	// History records book-field rows (metadata history).
	History *BookHistory
	// Journal records every other history row (operation changes).
	Journal ChangeJournal
	// JournaledBook reports whether the run has operation change rows for a
	// book (its metadata batch then gets the apply_op_journaled marker).
	JournaledBook func(bookID string) bool
	// LeaseLostErr is the error a write wraps once the stand-down lease is
	// gone. Default ErrStandDownLost.
	LeaseLostErr error
}

// Writer is the fenced, journaling write path (ops.Writer). Safe for
// concurrent use by a run's workers.
type Writer struct {
	cfg     Config
	intents *intents
	fence   atomic.Pointer[func() error]
	// leaseRenew renews the held stand-down lease and reports whether it is
	// still held; leaseValid reports, WITHOUT renewing, whether it is.
	leaseRenew atomic.Pointer[func() bool]
	leaseValid atomic.Pointer[func() bool]
	leaseLost  atomic.Bool
	writes     atomic.Int64
}

var _ ops.Writer = (*Writer)(nil)

// New builds a Writer.
func New(cfg Config) *Writer {
	if cfg.Mode == 0 {
		cfg.Mode = ops.Live
	}
	if cfg.LeaseLostErr == nil {
		cfg.LeaseLostErr = ErrStandDownLost
	}
	if cfg.History == nil {
		cfg.History = NewBookHistory(nil, "", "", "", nil)
	}
	w := &Writer{cfg: cfg, intents: &intents{store: cfg.Intents, opID: cfg.OpID}}
	if cfg.Fence != nil {
		f := cfg.Fence
		w.fence.Store(&f)
	}
	return w
}

// SetFence installs the run's write permit check; nil clears it.
func (w *Writer) SetFence(check func() error) {
	if check == nil {
		w.fence.Store(nil)
		return
	}
	w.fence.Store(&check)
}

// HasFence reports whether a fence is installed.
func (w *Writer) HasFence() bool { return w.fence.Load() != nil }

// SetLease installs the scan stand-down renewal every write calls; nil
// removes it. Installing resets the lost latch.
//
// WHY EVERY WRITE RENEWS: a lease renewed only once per row lost a 315-book
// row in prod (2026-10-01): it wrote for 5m34s without a renewal and was
// reaped. Renewing on each write keeps a row that makes progress alive however
// long it is, while a wedged write never renews and its lease still lapses.
func (w *Writer) SetLease(renew func() bool) {
	storeFunc(&w.leaseRenew, renew)
	w.leaseLost.Store(false)
}

// SetLeaseValid installs the non-renewing lease check a fenced write consults
// to tell a lost lease from a cancel (nil removes it). A fenced write must not
// renew: a canceled run that keeps calling the Writer would otherwise keep its
// lease, and the parked scan, alive.
func (w *Writer) SetLeaseValid(valid func() bool) { storeFunc(&w.leaseValid, valid) }

func storeFunc(p *atomic.Pointer[func() bool], fn func() bool) {
	if fn == nil {
		p.Store(nil)
		return
	}
	p.Store(&fn)
}

// Gate is step 1 of every write: it checks the run's fence, then renews the
// stand-down lease. A revoked fence refuses with an error wrapping what the
// check returned (ErrFenced) and never renews: a stopped run that keeps
// retrying must not keep the scan parked. Once a renewal fails the lease is
// gone for good, so every later write is refused without trying again; that
// error wraps Config.LeaseLostErr. Either way the write is not made.
func (w *Writer) Gate(what string) error {
	renew := w.leaseRenew.Load()
	if f := w.fence.Load(); f != nil {
		if err := (*f)(); err != nil {
			// The registry also revokes the fence when the stand-down lease
			// is lost; report that loss too, so callers that stop on it still
			// see it behind the fence. Decided from the latched loss or the
			// non-renewing check, never by renewing.
			valid := w.leaseValid.Load()
			if renew != nil && (w.leaseLost.Load() || (valid != nil && !(*valid)())) {
				w.leaseLost.Store(true)
				return fmt.Errorf("%w: %s not written: %w", w.cfg.LeaseLostErr, what, err)
			}
			return fmt.Errorf("%s not written: %w", what, err)
		}
	}
	if renew == nil {
		return nil
	}
	if w.leaseLost.Load() || !(*renew)() {
		w.leaseLost.Store(true)
		return fmt.Errorf("%w: %s not written", w.cfg.LeaseLostErr, what)
	}
	return nil
}

// fenceRevoked reports whether the run's fence refuses, without renewing the
// lease.
func (w *Writer) fenceRevoked() bool {
	f := w.fence.Load()
	return f != nil && (*f)() != nil
}

// Writes is how many writes committed.
func (w *Writer) Writes() int { return int(w.writes.Load()) }

// History is the writer's book-field history recorder.
func (w *Writer) History() *BookHistory { return w.cfg.History }

// Planned returns a Preview run's plan lines and how many were dropped past
// the in-memory cap.
func (w *Writer) Planned() ([]database.Intent, int) { return w.intents.plannedLines() }

// begin runs steps 1 and 2: the gate, then (Live) the intent or (Preview) the
// plan line and ErrPreview. It returns the intent's seq.
func (w *Writer) begin(ctx context.Context, what, subject, kind, op, detail string, journaled bool) (uint64, error) {
	if err := w.check(ctx, what); err != nil {
		return 0, err
	}
	return w.intent(what, subject, kind, op, detail, journaled)
}

// check is step 1 alone: the item's context, then the fence and lease gate.
// Every method runs it before it reads anything, so a fenced run gets
// ErrFenced, never a stale-read error such as ErrChangedSincePlan.
func (w *Writer) check(ctx context.Context, what string) error {
	if ctx != nil && ctx.Err() != nil {
		if w.fenceRevoked() {
			// The run is being stopped: the registry revokes the fence before
			// it cancels (a run timeout revokes it too). Report the fence, so
			// the caller sees ErrFenced, and note it for the batch runner.
			err := w.Gate(what)
			ops.NoteRefusal(ctx, err)
			return err
		}
		err := fmt.Errorf("%s not written: %w", what, context.Cause(ctx))
		// Canceled outright (no fence to revoke): still a refusal the batch
		// runner must hear of. An item's own deadline (ItemTimeout) is not.
		if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			ops.NoteRefusal(ctx, err)
		}
		return err
	}
	if err := w.Gate(what); err != nil {
		// Fenced or lease lost: flag it on the item's outcome, through the
		// item's context, even if the op swallows the error (ops.NoteRefusal).
		ops.NoteRefusal(ctx, err)
		return err
	}
	return nil
}

// intent is step 2: the plan line (Preview) or the write-ahead row (Live).
// A write whose history goes to the operation journal (journaled) is refused
// before anything is written when no journal is wired: a committed write
// nothing records could never be reverted.
func (w *Writer) intent(what, subject, kind, op, detail string, journaled bool) (uint64, error) {
	if w.cfg.Mode == ops.Preview {
		w.intents.plan(subject, kind, op, detail)
		return 0, ops.ErrPreview
	}
	if journaled && w.cfg.Journal == nil {
		return 0, fmt.Errorf("%w: %s not written", ErrNotJournaled, what)
	}
	seq, err := w.intents.put(subject, kind, op, detail)
	if err != nil {
		return 0, fmt.Errorf("%s not written: %w", what, err)
	}
	return seq, nil
}

// finish is step 5: it clears the intent, and only when the write committed
// and every history row describing it was recorded (recorded). Every other
// outcome -- a refused or failed write, a panic in the store or the callback,
// a write whose durability is unknown, a history row that could not be
// recorded -- leaves the intent, so recovery lists the subject as "may be
// partially applied" instead of trusting a record that is not there. A
// refused write that changed nothing is reported the same cautious way; the
// Writer cannot tell it from a partial one at this level. A clear failure is
// logged, not returned: the write committed, and the leftover intent only
// makes recovery cautious.
func (w *Writer) finish(seq uint64, recorded bool) {
	if !recorded {
		// seq 0: no intent store (internal/repairs journals first itself),
		// so there is no intent to leave, and nothing to say.
		if seq != 0 {
			pkgLog.Warn("intent %d of op %s left in place: the write's history is incomplete",
				seq, logger.SanitizeLogValue(w.cfg.OpID))
		}
		return
	}
	if err := w.intents.clear(seq); err != nil {
		pkgLog.Warn("intent %d of op %s not cleared (the write committed): %s",
			seq, logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(err.Error()))
	}
}

// ModifyBook runs fn on the book row inside the store's ModifyBook, so a
// check fn makes and the write are one atomic step (fn may be retried), and
// records one history row per tracked field that changed. The previous value
// is captured inside the critical section, on every invocation, so a retried
// callback leaves the values of the attempt that committed.
func (w *Writer) ModifyBook(ctx context.Context, bookID string, fn func(*database.Book) error) (ops.Changed, error) {
	if w.cfg.Books == nil {
		return ops.Changed{}, errors.New("opswriter: no book store")
	}
	what := "book " + bookID
	if err := w.check(ctx, what); err != nil {
		return ops.Changed{}, err
	}
	if w.cfg.Mode == ops.Live && !w.cfg.History.recording() {
		return ops.Changed{}, fmt.Errorf("%w: %s not written", ErrNoHistory, what)
	}
	seq, err := w.intent(what, "book:"+bookID, "modify", "modify_book", "", false)
	if err != nil {
		return ops.Changed{}, err
	}
	var before, after *database.Book
	written, err := w.cfg.Books.ModifyBook(bookID, func(row *database.Book) error {
		snap, serr := database.SnapshotBook(row)
		if serr != nil {
			return serr
		}
		if ferr := fn(row); ferr != nil {
			return ferr
		}
		post, perr := database.SnapshotBook(row)
		if perr != nil {
			return perr
		}
		before, after = snap, post
		return nil
	})
	if err != nil {
		return ops.Changed{}, err
	}
	if written == nil {
		return ops.Changed{}, fmt.Errorf("book %s vanished before the write", bookID)
	}
	w.writes.Add(1)
	journaled := w.cfg.JournaledBook != nil && w.cfg.JournaledBook(bookID)
	fields, recorded := w.cfg.History.RecordDiff(bookID, before, after, journaled)
	w.finish(seq, recorded)
	return ops.Changed{Fields: fields}, nil
}

// ModifyBookFile runs fn on one book_file row inside the store's
// ModifyBookFile. "fn returned nil" means write, changed or not. One history
// row per changed field, only when something changed.
func (w *Writer) ModifyBookFile(ctx context.Context, bookID, fileID string, fn func(*database.BookFile) error) (ops.Changed, error) {
	if w.cfg.Files == nil {
		return ops.Changed{}, errors.New("opswriter: no book_file store")
	}
	seq, err := w.begin(ctx, "book_file "+fileID, "book_file:"+fileID, "modify", "modify_book_file", bookID, true)
	if err != nil {
		return ops.Changed{}, err
	}
	var before, after map[string]json.RawMessage
	written, err := w.cfg.Files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		b, berr := fieldMap(f)
		if berr != nil {
			return berr
		}
		if ferr := fn(f); ferr != nil {
			return ferr
		}
		a, aerr := fieldMap(f)
		if aerr != nil {
			return aerr
		}
		before, after = b, a
		return nil
	})
	if err != nil {
		return ops.Changed{}, err
	}
	if written == nil {
		return ops.Changed{}, fmt.Errorf("%w: book_file %s vanished", ops.ErrChangedSincePlan, fileID)
	}
	w.writes.Add(1)
	var rows []ops.HistoryRow
	var fields []string
	for _, k := range sortedKeys(before, after) {
		if string(before[k]) == string(after[k]) {
			continue
		}
		fields = append(fields, "file."+k)
		rows = append(rows, ops.HistoryRow{BookID: bookID, Field: "file." + k + ":" + fileID,
			Old: string(before[k]), New: string(after[k])})
	}
	w.finish(seq, w.journalRows(ChangeTypeBookFileModify, rows))
	return ops.Changed{Fields: fields}, nil
}

// RepointBookFile points book_file fileID of bookID at the location to, only
// while the row still names expect's path and Missing flag
// (database.SameFileLocation). Nothing on disk moves and no row is deleted. The
// write is a compare-and-set against the location read here; the history row
// (after the write) carries the whole location before and after, plus the row
// that held to.Path's single-owner key, in the change type the operation
// revert reverses (database.ChangeTypeBookFileRepoint).
func (w *Writer) RepointBookFile(ctx context.Context, bookID, fileID string, expect, to database.BookFileLocation) error {
	if w.cfg.Files == nil {
		return errors.New("opswriter: no book_file store")
	}
	what := "book_file " + fileID
	if err := w.check(ctx, what); err != nil {
		return err
	}
	cur, err := w.cfg.Files.GetBookFileByID(bookID, fileID)
	if err != nil {
		return fmt.Errorf("read book_file %s: %w", fileID, err)
	}
	if cur == nil {
		return fmt.Errorf("%w: book_file %s is no longer on book %s", ops.ErrChangedSincePlan, fileID, bookID)
	}
	was := database.BookFileLocationOf(cur)
	if !database.SameFileLocation(was, expect) {
		return fmt.Errorf("%w: book_file %s is at %q, not %q", ops.ErrChangedSincePlan, fileID, was.Path, expect.Path)
	}
	to.KeyOwnerBook, to.KeyOwnerRow = "", ""
	owner, err := w.cfg.Files.GetBookFileByPath(to.Path)
	if err != nil {
		return fmt.Errorf("read the row at %s: %w", to.Path, err)
	}
	if owner != nil && owner.ID != fileID {
		to.KeyOwnerBook, to.KeyOwnerRow = owner.BookID, owner.ID
	}
	// The intent carries both locations, so a crash between the write and
	// the history row still leaves what a revert needs.
	detail, err := json.Marshal(repointDetail{BookID: bookID, From: was.Encode(), To: to.Encode()})
	if err != nil {
		return fmt.Errorf("%s not written: %w", what, err)
	}
	seq, err := w.begin(ctx, what, "book_file:"+fileID, "repoint", "repoint_book_file", string(detail), true)
	if err != nil {
		return err
	}
	written, err := w.cfg.Files.ModifyBookFile(bookID, fileID, func(f *database.BookFile) error {
		if database.BookFileLocationOf(f) != was {
			return ops.ErrChangedSincePlan
		}
		to.Apply(f)
		return nil
	})
	if err != nil {
		return err
	}
	if written == nil {
		return fmt.Errorf("%w: book_file %s vanished", ops.ErrChangedSincePlan, fileID)
	}
	w.writes.Add(1)
	w.finish(seq, w.journalRows(database.ChangeTypeBookFileRepoint, []ops.HistoryRow{{BookID: bookID,
		Field: "book_file:" + fileID, Old: was.Encode(), New: to.Encode()}}))
	return nil
}

// ModifyBookAuthors runs fn on a book's credit list inside the store's
// ModifyBookAuthors and records the list before and after when it changed.
func (w *Writer) ModifyBookAuthors(ctx context.Context, bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) (ops.Changed, error) {
	if w.cfg.Authors == nil {
		return ops.Changed{}, errors.New("opswriter: no author store")
	}
	seq, err := w.begin(ctx, "credits of book "+bookID, "book:"+bookID, "modify", "modify_book_authors", "", true)
	if err != nil {
		return ops.Changed{}, err
	}
	var before, after []database.BookAuthor
	if _, err := w.cfg.Authors.ModifyBookAuthors(bookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		b := slices.Clone(cur)
		next, ferr := fn(slices.Clone(cur))
		if ferr != nil {
			return nil, ferr
		}
		before, after = b, slices.Clone(next)
		return next, nil
	}); err != nil {
		return ops.Changed{}, err
	}
	w.writes.Add(1)
	if slices.Equal(before, after) {
		w.finish(seq, true)
		return ops.Changed{}, nil
	}
	oldJSON, oerr := json.Marshal(before)
	newJSON, nerr := json.Marshal(after)
	if oerr != nil || nerr != nil {
		return ops.Changed{Fields: []string{"authors"}}, errors.Join(oerr, nerr)
	}
	w.finish(seq, w.journalRows(ChangeTypeBookAuthorsModify, []ops.HistoryRow{{BookID: bookID, Field: "authors",
		Old: string(oldJSON), New: string(newJSON)}}))
	return ops.Changed{Fields: []string{"authors"}}, nil
}

// Effect wraps a write the typed surface does not cover: the gate and the
// intent before fn, the history rows fn returns after it. When fn fails the
// intent is left in place -- fn may have done part of its work -- and the
// rows it returned are still recorded.
func (w *Writer) Effect(ctx context.Context, e ops.EffectSpec, fn func() ([]ops.HistoryRow, error)) error {
	subject := string(e.Subject.Type) + ":" + e.Subject.ID
	seq, err := w.begin(ctx, "effect "+e.Name+" on "+subject, subject, "effect", e.Name, string(e.Resource), true)
	if err != nil {
		return err
	}
	rows, ferr := fn()
	recorded := w.journalRows(ChangeTypeEffectPrefix+e.Name, rows)
	if ferr != nil {
		return ferr
	}
	w.writes.Add(1)
	w.finish(seq, recorded)
	return nil
}

// journalRows records history rows as operation changes under the run's op
// id (step 4, after the write). A row that cannot be recorded is logged and
// counted failed; the write it describes already committed. It reports
// whether every row was recorded.
func (w *Writer) journalRows(changeType string, rows []ops.HistoryRow) bool {
	if len(rows) == 0 {
		return true
	}
	h := w.cfg.History
	if w.cfg.Journal == nil {
		h.failed.Add(int64(len(rows)))
		pkgLog.Warn("op %s: %d %s history rows not recorded: no change journal wired",
			logger.SanitizeLogValue(w.cfg.OpID), len(rows), changeType)
		return false
	}
	now := time.Now().UTC()
	all := true
	for _, r := range rows {
		err := w.cfg.Journal.CreateOperationChange(&database.OperationChange{
			OperationID: w.cfg.OpID, BookID: r.BookID, ChangeType: changeType, FieldName: r.Field,
			OldValue: r.Old, NewValue: r.New, CreatedAt: now, Source: h.source,
		})
		if err != nil {
			h.failed.Add(1)
			pkgLog.Warn("op %s: history row not recorded (the write committed): book_id=%s field=%s err=%s",
				logger.SanitizeLogValue(w.cfg.OpID), logger.SanitizeLogValue(r.BookID),
				logger.SanitizeLogValue(r.Field), logger.SanitizeLogValue(err.Error()))
			all = false
			continue
		}
		h.rows.Add(1)
	}
	return all
}

// fieldMap is a row's fields by JSON name, for a field-level diff.
func fieldMap(v any) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func sortedKeys(a, b map[string]json.RawMessage) []string {
	set := maps.Clone(a)
	if set == nil {
		set = map[string]json.RawMessage{}
	}
	maps.Copy(set, b)
	keys := make([]string, 0, len(set))
	for k := range set {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
