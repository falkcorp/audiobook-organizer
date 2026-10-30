// file: internal/server/handlers/dedup/itunes_flags.go
// version: 1.2.0
// guid: 7c1e5a94-3b82-4d6f-a0e9-52f8d4c1b673
// last-edited: 2026-10-30

package deduphandler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// candidateBook is the include_books payload for one side of a pair: the stored
// book exactly as before, plus the resolved author_name / series_name and
// is_itunes. The embedded pointer's fields are promoted into the JSON object
// (database.Book has no MarshalJSON), so a client that read book_a.title keeps
// reading it.
//
// author_name and series_name are the keys the audiobooks endpoints send
// (enrichedBookResponse), resolved by the same database.ResolveBookNames; the
// bare database.Book carries neither, which left the TypeScript Book's
// author_name undefined on every Dupes card.
type candidateBook struct {
	*database.Book
	AuthorName *string `json:"author_name,omitempty"`
	SeriesName *string `json:"series_name,omitempty"`
	// IsITunes is itunesguard.OwnershipWhy != "": a book iTunes persistent id,
	// a row iTunes persistent id or path, a live itunes external id, a path
	// inside an iTunes Media folder, or a path under books/itunes/**. It is
	// what the Duplicates lane's read-only "iTunes" chip shows; it is a label
	// and decides nothing (the merge guard has its own, narrower path rule).
	//
	// It is a pointer so "not settled" can be told from "not iTunes": when the
	// disk work for a page ran out of time (iTunesFlagBudget) the books it did
	// not reach carry no is_itunes key at all, and the client falls back to its
	// own check for them instead of reading a false it was never given.
	IsITunes *bool `json:"is_itunes,omitempty"`
}

// iTunesFlagStore is the store surface iTunesFlags reads. DedupStore embeds it
// so the two cannot drift apart.
type iTunesFlagStore interface {
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}

// iTunesFlagBudget bounds the time one listing page may spend deciding
// is_itunes. The path rules lstat / EvalSymlinks every file of every book on
// the page; on a hung network mount a single syscall never returns, and a list
// endpoint must not hang with it.
var iTunesFlagBudget = 2 * time.Second

// ownershipWhy is itunesguard.OwnershipWhy, a variable so a test can stand in
// a resolver that hangs.
var ownershipWhy = itunesguard.OwnershipWhy

var iTunesFlagsLog = logger.New("dedup.itunes-flags")

// iTunesFlags reports, per book id, whether the book is an iTunes copy by the
// same predicate every iTunes-hands-off writer uses (itunesguard.OwnershipWhy),
// for the books of ONE listing page.
//
// The returned map holds only the books that were SETTLED: a book missing from
// it was not decided because the disk work ran past iTunesFlagBudget (or ctx
// ended), and the caller leaves its flag absent rather than guessing false.
//
// The book_file rows of every book come from a single GetBookFilesForIDsCore
// call, not one read per book. The external-id read has no batch form in the
// store, so it is issued per book -- but only for a book the cheaper signals
// (book id, row ids and paths, path rules) did not already settle, and only
// once per distinct book on the page (the list can name one book on several
// pairs). OwnershipWhy is a plain OR over its signals, so evaluating it first
// without external ids and again with ONLY them when that came back empty gives
// the same answer as evaluating it once with all of them -- and the second
// pass is handed no paths, so the disk is not walked a second time.
//
// A read error is returned, never read as "not an iTunes book": a chip that
// silently went missing is exactly the failure this field exists to remove.
// A path whose location cannot be settled (OwnershipWhy's doubt) is reported
// as not-iTunes -- the chip claims only what was proven.
//
// The per-book work runs in one goroutine the caller waits on with a deadline.
// A syscall stuck on a dead mount cannot be cancelled, so on timeout that
// goroutine is abandoned: it stops at its next book boundary (it checks ctx
// before each book) and writes only into its own result struct. One goroutine,
// not a pool: the page is bounded, the work is I/O on one resolver cache, and a
// pool would only put more threads onto the mount that is hanging.
func iTunesFlags(ctx context.Context, store iTunesFlagStore, books []*database.Book) (map[string]bool, error) {
	ids := make([]string, 0, len(books))
	byID := make(map[string]*database.Book, len(books))
	for _, b := range books {
		if b == nil || byID[b.ID] != nil {
			continue
		}
		byID[b.ID] = b
		ids = append(ids, b.ID)
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	filesByID, err := store.GetBookFilesForIDsCore(ids)
	if err != nil {
		return nil, fmt.Errorf("read book files for the iTunes flag: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, iTunesFlagBudget)
	defer cancel()
	// Read the seam here, on the caller's goroutine: the worker may outlive the
	// request, and must not touch a package variable a test restores.
	run := &flagRun{flags: make(map[string]bool, len(ids)), ownershipWhy: ownershipWhy}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run.decide(ctx, store, ids, byID, filesByID)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	run.mu.Lock()
	defer run.mu.Unlock()
	if err := errors.Join(run.errs...); err != nil {
		return nil, err
	}
	if len(run.flags) < len(ids) {
		iTunesFlagsLog.Warn("dedup list: iTunes flag not settled for %d of %d books within %s; is_itunes left absent for them",
			len(ids)-len(run.flags), len(ids), iTunesFlagBudget)
	}
	out := make(map[string]bool, len(run.flags))
	maps.Copy(out, run.flags)
	return out, nil
}

// flagRun is the state the deciding goroutine shares with iTunesFlags' waiter.
type flagRun struct {
	mu    sync.Mutex
	flags map[string]bool
	errs  []error

	ownershipWhy func(res *repairs.PathResolver, id, pid string, paths []string, rows []itunesguard.Row, exts []database.ExternalIDMapping, countPathRef bool) (string, bool)
}

func (r *flagRun) decide(ctx context.Context, store iTunesFlagStore, ids []string, byID map[string]*database.Book, filesByID map[string][]database.BookFileCore) {
	res := repairs.NewPathResolver()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		b := byID[id]
		files := filesByID[id]
		paths := make([]string, 0, len(files)+1)
		paths = append(paths, b.FilePath)
		rows := make([]itunesguard.Row, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.FilePath)
			rows = append(rows, itunesguard.Row{ID: f.ID, ITunesPID: f.ITunesPersistentID, ITunesPath: f.ITunesPath})
		}
		pid := ""
		if b.ITunesPersistentID != nil {
			pid = *b.ITunesPersistentID
		}
		why, _ := r.ownershipWhy(res, id, pid, paths, rows, nil, true)
		if why == "" {
			exts, xerr := store.GetExternalIDsForBook(id)
			if xerr != nil {
				r.mu.Lock()
				r.errs = append(r.errs, fmt.Errorf("external ids of %s: %w", id, xerr))
				r.mu.Unlock()
				continue
			}
			// Paths and rows were already weighed above; only the external ids
			// are new, so no path is walked again.
			why, _ = r.ownershipWhy(res, id, "", nil, nil, exts, true)
		}
		r.mu.Lock()
		r.flags[id] = why != ""
		r.mu.Unlock()
	}
}
