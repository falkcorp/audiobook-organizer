// file: internal/plugins/maintenance/batch_sweep.go
// version: 0.1.0
// guid: ccc31c18-3bab-412d-afbf-cefd4d12e998
// last-edited: 2026-11-22

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Per-batch child runs for the long whole-library jobs (F5-18h; O24, O25, O37).
//
// A whole-library job that declares books/book_files as Effects holds the whole
// kind for as long as it runs, which for transcribe-file-intros is days: every
// other writer of those rows queues behind it. A ref lock cannot narrow an
// Effects hold, so the parent declares no row writes (tasks.ReadOnly) and
// enqueues one short child run per batch of batchSweepSize books through
// rc.Enqueue. The child declares the same Effects as the job always did, but
// its Common.Subjects names only that batch's book and book_file refs, so the
// dispatcher holds just those rows while it runs and a writer of any other book
// is admitted.
//
// Choices (the F5-18h brief asked for them to be stated):
//
//   - One child in flight at a time. The children of one parent are strictly
//     sequential. Parallelism lives inside a child (whisper slots, ffmpeg
//     workers, the migrate page pool), which is where the per-item cost is; a
//     second child would only multiply those pools, and for the whisper kind it
//     would oversubscribe the endpoints the first child already saturates. The
//     price is a barrier at every batch boundary (the slowest file of a batch
//     idles the rest of the pool); the owner's 100 books per batch keeps that
//     under a few minutes.
//   - The parent never blocks past its attempt. rc.Await is not yield-aware
//     (TODO-AWAIT-YIELD) and a ResumeContinue parent cannot yield around it, so
//     the wait runs in a goroutine bounded by the attempt's deadline minus
//     batchYieldMargin (as optimize.go does). When the bound expires with the
//     child still running the parent checkpoints the child as in flight and
//     yields with tasks.ErrContinueInPlace; the next attempt re-attaches to the
//     same child and never enqueues it twice.
//   - The cursor is the LAST COMPLETED BOOK ID, not a batch index. The list a
//     restarted attempt plans from can differ from the one the run began with
//     (only_missing shrinks as books finish), so an index would skip books; an
//     id never does. The cursor, the child in flight and the rolled-up result
//     are written by one Checkpoint, so a restart cannot count a batch twice
//     or lose one.
//   - A failed child ends the sweep with that child's error (fail-fast, like
//     HEAD's file-intro loop). The jobs are idempotent, so a later run does
//     what is left; carrying on would only spend the other batches against
//     whatever just failed.
//   - Cancel stops the sweep after the current child. The kernel does not
//     cascade a parent's cancel to its children, so the child in flight finishes
//     (its result is not folded into the parent); no further child starts.

// batchSweepSize is the number of books in one child run (owner decision,
// 2026-11-18).
const batchSweepSize = 100

// batchYieldMargin is how long before its deadline an attempt stops waiting on
// a child, or starting one, and yields. It is optimize.go's margin.
const batchYieldMargin = 5 * time.Minute

// batchBook is one book of a batch and the book_file rows a child may write.
type batchBook struct {
	ID    string   `json:"id"`
	Files []string `json:"files,omitempty"`
}

// batchScope is the part of a child's params that names its batch. It is also
// everything the child's Common.Subjects reads: the dispatcher holds exactly
// these rows while the child runs, and the child's core refuses to write any
// row that is not in it.
type batchScope struct {
	// ParentRunID is the run id the parent's results are stamped with (the
	// children have run ids of their own; clear_run_id and the stats key name
	// the parent's).
	ParentRunID string `json:"parent_run_id"`
	// Batch is the batch's index in the parent's attempt. It is also what keeps
	// two children's params from being byte-identical (the default queued-run
	// dedupe would fold them into one).
	Batch int         `json:"batch"`
	Books []batchBook `json:"books"`
}

var errBatchScope = errors.New("batch scope")

// validate refuses a scope that would hold nothing (and so overlap nothing, the
// dispatcher's reading of an empty write set is "no rows") or more than a batch.
func (s batchScope) validate() error {
	switch {
	case s.ParentRunID == "":
		return fmt.Errorf("%w: parent_run_id is empty", errBatchScope)
	case len(s.Books) == 0:
		return fmt.Errorf("%w: no books", errBatchScope)
	case len(s.Books) > batchSweepSize:
		return fmt.Errorf("%w: %d books, a batch is at most %d", errBatchScope, len(s.Books), batchSweepSize)
	}
	for _, b := range s.Books {
		if b.ID == "" {
			return fmt.Errorf("%w: a book with no id", errBatchScope)
		}
	}
	return nil
}

func bookFileRef(id string) entity.Ref {
	return tasks.ResID(writer.Module, string(ops.ResBookFiles), id)
}

// subjects is the child's write set: the batch's book rows and book_file rows,
// plus extra (a kind-wide ref a child must keep holding, see the wav child).
func (s batchScope) subjects(extra ...entity.Ref) ([]entity.Ref, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	out := make([]entity.Ref, 0, len(s.Books)*2+len(extra))
	for _, b := range s.Books {
		out = append(out, bookRef(b.ID))
		for _, f := range b.Files {
			out = append(out, bookFileRef(f))
		}
	}
	return append(out, extra...), nil
}

// bookIDs is the batch's books in the order the parent planned them.
func (s batchScope) bookIDs() []string {
	ids := make([]string, len(s.Books))
	for i, b := range s.Books {
		ids[i] = b.ID
	}
	return ids
}

// fileOwners maps each file the batch may write to the book that owns it. A
// file under another book's id is not in the scope.
func (s batchScope) fileOwners() map[string]string {
	m := make(map[string]string)
	for _, b := range s.Books {
		for _, f := range b.Files {
			m[f] = b.ID
		}
	}
	return m
}

// inScope reports that file id of book bookID is one the batch holds.
func inScope(owners map[string]string, bookID, fileID string) bool {
	owner, ok := owners[fileID]
	return ok && owner == bookID
}

// errBatchOutOfScope is a batch child's refusal to write a row its Subjects do
// not hold.
var errBatchOutOfScope = fmt.Errorf("%w: the row is not one the batch holds", errBatchScope)

// scopedFileWriter is a batch child's UpdateBookFile: the write goes through only
// for a file the batch holds under the book being processed.
type scopedFileWriter struct {
	w      t6g4IntroMigrateWrites
	owners map[string]string
	bookID string
}

func (s scopedFileWriter) UpdateBookFile(id string, f *database.BookFile) error {
	if !inScope(s.owners, s.bookID, id) {
		return fmt.Errorf("%w: book_file %s of book %s", errBatchOutOfScope, id, s.bookID)
	}
	return s.w.UpdateBookFile(id, f)
}

// batchSubjects builds a child's Common.Subjects from its params type.
func batchSubjects[P any](scopeOf func(P) batchScope, extra ...entity.Ref) tasks.SubjectsFunc {
	return func(raw json.RawMessage) ([]entity.Ref, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("%w: decode params: %v", errBatchScope, err)
		}
		return scopeOf(p).subjects(extra...)
	}
}

// batchInFlight is the child the parent enqueued and has not seen end. Last is
// the final book of its batch: the cursor value once it succeeds.
type batchInFlight struct {
	RunID string `json:"run_id"`
	Batch int    `json:"batch"`
	Last  string `json:"last"`
}

// batchState is the sweep's checkpoint: everything a restarted attempt needs.
type batchState[R any] struct {
	// After is the last book of the last completed batch ("" before the first).
	After string `json:"after"`
	// Batches is how many batches have completed.
	Batches  int            `json:"batches"`
	InFlight *batchInFlight `json:"in_flight,omitempty"`
	// Rollup is the per-batch results folded together.
	Rollup R `json:"rollup"`
	// Steps only ever grows (a child enqueued, a child done, a re-attach), so an
	// attempt that yields with its child still running has still moved the
	// progress the kernel's yield proof measures.
	Steps int `json:"steps"`
}

// batchHost is what the sweep needs from the run: a seam over rc, so the cursor,
// cancel and rollup are tested without an executor.
type batchHost struct {
	Ctx context.Context
	// Enqueue starts one child with these params and returns its run id.
	Enqueue func(params any) (string, error)
	// Await waits for the child to end, or for ctx. It returns ctx's cause when
	// ctx ends first.
	Await      func(ctx context.Context, runID string) (tasks.Outcome, error)
	Checkpoint func(v any) error
	Restore    func(v any) (bool, error)
	Deadline   func() time.Time
	Progress   func(done, total int, msg string)
	Log        func(level slog.Level, msg string, attrs ...slog.Attr)
	Now        func() time.Time
}

// batchPlan is one kind's sweep.
type batchPlan[R any] struct {
	// IDs is every book the sweep may visit. Order does not matter: the sweep
	// sorts it, because the cursor is an id.
	IDs  []string
	Size int
	// Books resolves a batch's ids to the rows its child may write.
	Books func(ids []string) []batchBook
	// Params builds the child's params for batch number batch.
	Params func(batch int, books []batchBook) any
	// Seed is the rollup of a fresh sweep.
	Seed R
	// Merge folds one child's result into the rollup.
	Merge func(acc *R, child R)
	// Flush, when set, sees the rollup after every folded batch and at the end
	// (done). transcribe-file-intros persists its counters here.
	Flush func(acc R, done bool)
}

var errBatchChild = errors.New("batch child failed")

// runBatchSweep walks plan.IDs in ascending order, one child per plan.Size
// books, and returns the rollup. It returns tasks.ErrContinueInPlace (with the
// state checkpointed) when the attempt's deadline is near.
func runBatchSweep[R any](h batchHost, plan batchPlan[R]) (R, error) {
	size := plan.Size
	if size < 1 {
		size = batchSweepSize
	}
	st := batchState[R]{Rollup: plan.Seed}
	var restored batchState[R]
	ok, err := h.Restore(&restored)
	if err != nil {
		return plan.Seed, fmt.Errorf("restore batch sweep checkpoint: %w", err)
	}
	if ok {
		st = restored
		h.Log(slog.LevelInfo, "Resuming batch sweep", slog.String("after", st.After), slog.Int("batches_done", st.Batches))
	}

	ids := slices.Clone(plan.IDs)
	sort.Strings(ids)
	ids = slices.Compact(ids)
	pending := func() []string {
		i := sort.SearchStrings(ids, st.After)
		for i < len(ids) && ids[i] <= st.After {
			i++
		}
		return ids[i:]
	}
	totalBatches := func() int { return st.Batches + (len(pending())+size-1)/size }

	startSteps := st.Steps
	save := func() error {
		if err := h.Checkpoint(st); err != nil {
			return fmt.Errorf("checkpoint batch sweep: %w", err)
		}
		return nil
	}
	report := func(msg string) {
		n := totalBatches()
		h.Progress(st.Steps, max(2*n, st.Steps), msg)
	}
	finish := func(e error) (R, error) { return st.Rollup, e }

	// await waits for the in-flight child and folds it in. It returns
	// (yield, err): yield means the attempt must end with the child still
	// running (already checkpointed).
	await := func() (bool, error) {
		fl := st.InFlight
		waitCtx, cancel := h.Ctx, context.CancelFunc(func() {})
		if dl := h.Deadline(); !dl.IsZero() {
			waitCtx, cancel = context.WithDeadline(h.Ctx, dl.Add(-batchYieldMargin))
		}
		out, werr := h.Await(waitCtx, fl.RunID)
		expired := werr != nil && h.Ctx.Err() == nil && errors.Is(waitCtx.Err(), context.DeadlineExceeded)
		cancel()
		switch {
		case werr != nil && h.Ctx.Err() != nil:
			// The attempt itself is ending (cancel, stop, timeout). The child stays
			// in flight in the last checkpoint.
			return false, context.Cause(h.Ctx)
		case expired:
			if err := save(); err != nil {
				return false, err
			}
			h.Log(slog.LevelInfo, "Batch sweep yielding with a child still running: the attempt's deadline is near",
				slog.String("child", fl.RunID), slog.Int("batch", fl.Batch))
			return true, nil
		case werr != nil:
			return false, fmt.Errorf("await batch %d (run %s): %w", fl.Batch, fl.RunID, werr)
		case out.State != tasks.StateSucceeded:
			return false, fmt.Errorf("%w: batch %d (run %s) ended %s: %s", errBatchChild, fl.Batch, fl.RunID, out.State, out.Err)
		}
		var child R
		if err := json.Unmarshal(out.Result, &child); err != nil {
			return false, fmt.Errorf("%w: batch %d (run %s): its result does not decode: %v", errBatchChild, fl.Batch, fl.RunID, err)
		}
		plan.Merge(&st.Rollup, child)
		st.After = fl.Last
		st.Batches++
		st.InFlight = nil
		st.Steps++
		if err := save(); err != nil {
			return false, err
		}
		if plan.Flush != nil {
			plan.Flush(st.Rollup, false)
		}
		report(fmt.Sprintf("Batch %d/%d done", st.Batches, totalBatches()))
		return false, nil
	}

	report("Starting batch sweep")
	if st.InFlight != nil {
		// The earlier attempt enqueued this child and did not see it end: wait on
		// that run (one that finished meanwhile answers at once) and never enqueue
		// it again. The re-attach counts as a step: a child still running at the
		// next deadline has finished nothing.
		st.Steps++
		if err := save(); err != nil {
			return finish(err)
		}
		h.Log(slog.LevelInfo, "Re-attaching to the batch in flight", slog.String("child", st.InFlight.RunID), slog.Int("batch", st.InFlight.Batch))
		yield, err := await()
		if err != nil {
			return finish(err)
		}
		if yield {
			return finish(tasks.ErrContinueInPlace)
		}
	}

	for {
		if h.Ctx.Err() != nil {
			return finish(context.Cause(h.Ctx))
		}
		rest := pending()
		if len(rest) == 0 {
			break
		}
		if dl := h.Deadline(); !dl.IsZero() && st.Steps > startSteps && dl.Sub(h.Now()) < batchYieldMargin {
			if err := save(); err != nil {
				return finish(err)
			}
			h.Log(slog.LevelInfo, "Batch sweep yielding before the next batch: the attempt's deadline is near", slog.Int("batches_done", st.Batches))
			return finish(tasks.ErrContinueInPlace)
		}
		batchIDs := rest[:min(size, len(rest))]
		books := plan.Books(batchIDs)
		batch := st.Batches
		runID, err := h.Enqueue(plan.Params(batch, books))
		if err != nil {
			return finish(fmt.Errorf("enqueue batch %d: %w", batch, err))
		}
		st.InFlight = &batchInFlight{RunID: runID, Batch: batch, Last: batchIDs[len(batchIDs)-1]}
		st.Steps++
		// The run id is recorded before the wait: a crash from here re-attaches.
		if err := save(); err != nil {
			return finish(err)
		}
		report(fmt.Sprintf("Batch %d/%d running (%d books)", batch+1, totalBatches(), len(batchIDs)))
		yield, err := await()
		if err != nil {
			return finish(err)
		}
		if yield {
			return finish(tasks.ErrContinueInPlace)
		}
	}
	if plan.Flush != nil {
		plan.Flush(st.Rollup, true)
	}
	h.Progress(max(st.Steps, 1), max(st.Steps, 1), fmt.Sprintf("Done — %d batches", st.Batches))
	return finish(nil)
}

// newBatchHost binds a sweep to a run: children are enqueued as child, and the
// wait runs in its own goroutine because rc.Await takes no context (see
// optimize.go for the same shape).
func newBatchHost(rc *tasks.RC, base t6g4Base, child tasks.Kind) batchHost {
	return batchHost{
		Ctx: rc.Context(),
		Enqueue: func(params any) (string, error) {
			ref, err := rc.Enqueue(child, params)
			return string(ref.ID), err
		},
		Await: func(ctx context.Context, runID string) (tasks.Outcome, error) {
			type awaited struct {
				out tasks.Outcome
				err error
			}
			done := make(chan awaited, 1)
			go func() {
				// A panic here is outside the run body's recover and would end the
				// process: report it as the wait's error.
				defer func() {
					if r := recover(); r != nil {
						done <- awaited{err: fmt.Errorf("await child run %s panicked: %v", runID, r)}
					}
				}()
				out, err := rc.Await(tasks.RunRef{ID: tasks.RunID(runID)})
				done <- awaited{out, err}
			}()
			select {
			case r := <-done:
				return r.out, r.err
			case <-ctx.Done():
				return tasks.Outcome{}, context.Cause(ctx)
			}
		},
		Checkpoint: rc.Checkpoint,
		Restore:    rc.Restore,
		Deadline:   rc.Deadline,
		Progress:   base.Progress,
		Log:        base.Log,
		Now:        time.Now,
	}
}
