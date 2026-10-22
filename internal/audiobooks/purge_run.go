// file: internal/audiobooks/purge_run.go
// version: 1.1.0
// guid: 4c9d2f87-6b13-4e50-a8d4-1f7e3a0c9b62
// last-edited: 2026-10-22

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// PurgeOptions are the parameters of one purge pass (RunPurge).
type PurgeOptions struct {
	// DeleteFiles removes a purged book's own files from disk (it never
	// applies to a book that owns book_file rows: those are not purged).
	DeleteFiles bool
	// OlderThanDays limits the pass to books trashed at least that long ago.
	OlderThanDays *int
	// DryRun classifies every book exactly as the real pass would and changes
	// nothing: no row, tombstone, external ID or listening state is touched.
	DryRun bool
	// Progress, when set, is called with (books decided, books total) every
	// purgeProgressEvery books and at the end. It runs inside worker
	// goroutines, concurrently: it must be safe for that.
	Progress func(done, total int)
}

// purgeProgressEvery is how often (in books) RunPurge reports progress.
const purgeProgressEvery = 200

// purgeEnv is what a purge pass reads once and every worker then only reads.
type purgeEnv struct {
	// probe answers "does a user have listening state on this book" with the
	// users listed once per pass.
	probe *merge.UserStateProbe
	// undo holds the retired shells of combines that can still be undone.
	undo *merge.UndoShellIndex
}

// newPurgeEnv builds a pass's shared read state. Both reads fail closed: a
// purge that cannot list users or the combine journals does not run, because
// "cannot tell" must never read as "nothing is held".
func (svc *AudiobookService) newPurgeEnv() (purgeEnv, error) {
	probe, err := merge.NewUserStateProbe(svc.store)
	if err != nil {
		return purgeEnv{}, fmt.Errorf("cannot list users to check their listening state: %w", err)
	}
	undo, err := svc.newUndoShellIndex()
	if err != nil {
		return purgeEnv{}, err
	}
	return purgeEnv{probe: probe, undo: undo}, nil
}

// newUndoShellIndex reads the combine journals once. It fails closed: a store
// that cannot read them cannot say which shells are held, and "cannot tell"
// must not read as "none are".
func (svc *AudiobookService) newUndoShellIndex() (*merge.UndoShellIndex, error) {
	kv, ok := any(svc.store).(interface {
		GetRaw(key string) ([]byte, error)
		ScanPrefix(prefix string) ([]database.KVPair, error)
	})
	if !ok {
		return nil, errors.New("the store cannot read combine journals, so shells whose undo is still possible cannot be told apart")
	}
	return merge.NewUndoShellIndex(kv)
}

// purgeGate is the purge's side-effect-free, lock-free classifier: the checks
// that decide a book is NOT purged whatever else is true of it. It reports the
// outcome and blocked=true when one applies. Both a real pass (purgeOne) and a
// preview (previewOne) call it, so a preview's counts cannot disagree with the
// run that follows it.
//
// Order matters for the counts: a book that owns book_file rows is counted as
// owning files even if it is also a held shell. Fail closed: an unreadable
// file list is not proof there are no rows.
func (svc *AudiobookService) purgeGate(book *database.Book, env purgeEnv) (purgeOutcome, bool) {
	ownedFiles, err := svc.store.GetBookFiles(book.ID)
	if err != nil {
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: not purged: cannot read its book_file rows: %v", book.ID, err)}}, true
	}
	if len(ownedFiles) > 0 {
		return purgeOutcome{kind: purgeOwnsFiles}, true
	}
	if _, held := env.undo.Holds(book); held {
		return purgeOutcome{kind: purgeKeptUndo}, true
	}
	return purgeOutcome{}, false
}

// previewOne is purgeOne without a side effect: the gate, then the listening-
// state check (which, as in the real pass, either carries to a listed sibling,
// or keeps the book). It answers what the real pass would do with this book
// right now.
func (svc *AudiobookService) previewOne(book *database.Book, env purgeEnv) purgeOutcome {
	if o, blocked := svc.purgeGate(book, env); blocked {
		return o
	}
	has, err := env.probe.Has(book.ID)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeFailed, errs: []string{fmt.Sprintf("%s: would not be purged: %s: %v", book.ID, stateCheckFailureReason(err), err)}}
	case !has:
		return purgeOutcome{kind: purgePurged}
	}
	sibling, err := svc.liveSiblingFor(book)
	switch {
	case err != nil:
		return purgeOutcome{kind: purgeCarryFailed, errs: []string{fmt.Sprintf("%s: would not be purged, listening state kept on it: cannot look for a live version to carry it to: %v", book.ID, err)}}
	case sibling == "":
		return purgeOutcome{kind: purgeKeptHasProgress}
	}
	return purgeOutcome{kind: purgeCarried}
}

// RunPurge is one purge pass over the soft-deleted books (optionally only
// those trashed at least OlderThanDays ago), or, with DryRun, a preview of it.
// PurgeSoftDeletedBooks (the nightly job) is RunPurge with the zero options
// besides its two parameters.
//
// What is purgeable is unchanged by this function: a book is purged only when
// it owns no book_file row, is not the shell of a combine whose undo can still
// run, and has no listening state it cannot carry to a live sibling. See
// purgeOne.
//
// The books are processed by a bounded worker pool, one version group per
// worker at a time (purgePartitions), so two workers never carry state into
// or delete from the same group concurrently. The carries, and each book's
// state check plus row delete, still run one at a time: each holds the
// global merge lock, which is what keeps a merge revert from putting state
// back on a book between its check and its delete. The file-row check and
// the on-disk file removal run outside it, in parallel.
func (svc *AudiobookService) RunPurge(ctx context.Context, opts PurgeOptions) (*PurgeResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var cutoff *time.Time
	if opts.OlderThanDays != nil && *opts.OlderThanDays > 0 {
		ts := time.Now().AddDate(0, 0, -*opts.OlderThanDays)
		cutoff = &ts
	}

	books, err := svc.store.ListSoftDeletedBooks(1_000_000, 0, cutoff)
	if err != nil {
		return nil, err
	}

	result := &PurgeResult{
		Attempted: len(books),
		DryRun:    opts.DryRun,
		Errors:    []string{},
	}
	// Users and combine journals are read once per pass, not once per book.
	var env purgeEnv
	if len(books) > 0 {
		env, err = svc.newPurgeEnv()
		if err != nil {
			return nil, fmt.Errorf("purge refused: %w", err)
		}
	}

	var mu sync.Mutex
	var decided atomic.Int64
	total := len(books)
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for _, part := range purgePartitions(books) {
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			for i := range part {
				if ctx.Err() != nil {
					return nil
				}
				var out purgeOutcome
				if opts.DryRun {
					out = svc.previewOne(&part[i], env)
				} else {
					out = svc.purgeOne(&part[i], opts.DeleteFiles, env)
				}
				mu.Lock()
				result.add(part[i].ID, out)
				mu.Unlock()
				if n := int(decided.Add(1)); opts.Progress != nil && n%purgeProgressEvery == 0 {
					opts.Progress(n, total)
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// The workers report per-book outcomes in result and return nil;
		// an error here is a bug, reported rather than dropped.
		result.Errors = append(result.Errors, fmt.Sprintf("purge worker failed: %v", err))
	}
	if err := ctx.Err(); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("purge stopped early: %v", err))
	}
	result.sortIDs()
	if opts.Progress != nil {
		opts.Progress(int(decided.Load()), total)
	}

	if result.Purged > 0 {
		svc.InvalidateListCache()
	}
	svc.rememberPurge(result)
	return result, nil
}

// Summary is the one-line account of a pass, for the log and the activity
// entry: every outcome the pass counted.
func (r *PurgeResult) Summary() string {
	if r.DryRun {
		return fmt.Sprintf("Purge preview of %d soft-deleted books: %d purgeable now (%d of them would carry listening state to a sibling), %d kept: still own book_file rows, %d kept: undo of a combine still possible, %d kept: listening state with no listed sibling, %d could not be decided (carry_failed=%d, failed=%d)",
			r.Attempted, r.Purgeable, r.CarriedToSibling, r.SkippedOwnsFiles, r.KeptUndoPossible, r.KeptHasProgress, r.CarryFailed+r.Failed, r.CarryFailed, r.Failed)
	}
	return fmt.Sprintf("Purged %d of %d soft-deleted books (%d files deleted); kept %d: still own book_file rows, %d: undo of a combine still possible, %d: listening state with no listed sibling; carried listening state to a sibling for %d; %d carry failed, %d failed",
		r.Purged, r.Attempted, r.FilesDeleted, r.SkippedOwnsFiles, r.KeptUndoPossible, r.KeptHasProgress, r.CarriedToSibling, r.CarryFailed, r.Failed)
}

// Capped returns a copy of r with each ID list and Errors cut to at most n
// entries (Truncated set when anything was cut), so the result can be stored
// as an operation result: on prod the owns-files list alone is ~50k ids. The
// counts are exact either way.
func (r *PurgeResult) Capped(n int) *PurgeResult {
	c := *r
	cut := func(s []string) []string {
		if len(s) > n {
			c.Truncated = true
			return s[:n:n]
		}
		return s
	}
	c.SkippedOwnsFilesIDs = cut(r.SkippedOwnsFilesIDs)
	c.CarriedToSiblingIDs = cut(r.CarriedToSiblingIDs)
	c.KeptHasProgressIDs = cut(r.KeptHasProgressIDs)
	c.KeptUndoPossibleIDs = cut(r.KeptUndoPossibleIDs)
	c.CarryFailedIDs = cut(r.CarryFailedIDs)
	c.Errors = cut(r.Errors)
	return &c
}

// PurgeBreakdown is the trash's purgeable-versus-blocked split as the last
// purge pass measured it. The trash listing shows it; it is not recomputed
// per request (classifying every trashed book reads each one's file rows).
type PurgeBreakdown struct {
	// ComputedAt is when the pass that produced this finished.
	ComputedAt time.Time `json:"computed_at"`
	// FromPreview is true when a dry run produced it, false when a real pass
	// did (the figures are then what REMAINED).
	FromPreview bool `json:"from_preview"`
	// Total is the number of books the pass looked at (or, after a real pass,
	// that remain in the trash).
	Total int `json:"total"`
	// Purgeable is how many a real pass would purge now.
	Purgeable int `json:"purgeable"`
	// Blocked, by reason.
	OwnsFiles         int `json:"owns_files"`
	UndoPossible      int `json:"undo_possible"`
	HasListeningState int `json:"has_listening_state"`
	Failed            int `json:"failed"`
}

// breakdownOf converts a pass's result into the listing's figures. After a real
// pass the books it purged are gone, so the figures describe what is left: the
// books that failed or were interrupted can be purged by the next pass, the
// rest stay blocked.
func breakdownOf(r *PurgeResult, at time.Time) PurgeBreakdown {
	b := PurgeBreakdown{
		ComputedAt:        at,
		FromPreview:       r.DryRun,
		OwnsFiles:         r.SkippedOwnsFiles,
		UndoPossible:      r.KeptUndoPossible,
		HasListeningState: r.KeptHasProgress,
		Failed:            r.Failed + r.CarryFailed,
	}
	if r.DryRun {
		b.Total = r.Attempted
		b.Purgeable = r.Purgeable
	} else {
		b.Total = r.Attempted - r.Purged
		b.Purgeable = r.Failed + r.CarryFailed
	}
	return b
}

// rememberPurge records the pass's breakdown for LastPurgeBreakdown. A pass
// that stopped early (cancelled, timed out) is not remembered: its figures
// cover only part of the trash.
func (svc *AudiobookService) rememberPurge(r *PurgeResult) {
	if purgeStoppedEarly(r) {
		return
	}
	b := breakdownOf(r, time.Now().UTC())
	svc.purgeBreakdownMu.Lock()
	svc.purgeBreakdown = &b
	svc.purgeBreakdownMu.Unlock()
}

// purgeStoppedEarly reports whether r carries the "stopped early" error
// RunPurge adds when its context ended mid-pass.
func purgeStoppedEarly(r *PurgeResult) bool {
	for _, e := range r.Errors {
		if strings.HasPrefix(e, "purge stopped early") {
			return true
		}
	}
	return false
}

// LastPurgeBreakdown returns the most recent pass's breakdown, or nil when no
// pass has run since the server started.
func (svc *AudiobookService) LastPurgeBreakdown() *PurgeBreakdown {
	svc.purgeBreakdownMu.Lock()
	defer svc.purgeBreakdownMu.Unlock()
	if svc.purgeBreakdown == nil {
		return nil
	}
	b := *svc.purgeBreakdown
	return &b
}
