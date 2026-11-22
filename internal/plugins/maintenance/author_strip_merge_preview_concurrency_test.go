// file: internal/plugins/maintenance/author_strip_merge_preview_concurrency_test.go
// version: 1.0.1
// guid: 6dbf31bb-cfeb-47c8-8fb6-db77013c18cb
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// overlapDB records how many GetBooksByAuthorIDForRelinkCore calls are in flight
// at once and holds each one open long enough for a second to arrive, so a run
// that reads its plans one after another never shows more than one.
type overlapDB struct {
	*database.PebbleStore
	mu       *sync.Mutex
	inFlight *int
	maxSeen  *int
}

func (d overlapDB) GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error) {
	d.mu.Lock()
	*d.inFlight++
	*d.maxSeen = max(*d.maxSeen, *d.inFlight)
	d.mu.Unlock()
	time.Sleep(25 * time.Millisecond)
	defer func() {
		d.mu.Lock()
		*d.inFlight--
		d.mu.Unlock()
	}()
	return d.PebbleStore.GetBooksByAuthorIDForRelinkCore(authorID)
}

// stripPreviewWorld is n numbered rows, each folded into its own real author
// ("001-147 Harvey Dent" into "Harvey Dent", ...), one book apiece. The first
// row's book has its author locked by the user, so the preview must hold that
// merge and count the others as mergeable.
func stripPreviewWorld(t *testing.T, names []string) *database.PebbleStore {
	t.Helper()
	st := newRelinkTrashedStore(t)
	for i, name := range names {
		from := mustAuthorNamed(t, st, "001-147 "+name)
		into := mustAuthorNamed(t, st, name)
		book := mkRelinkTrashedBook(t, st, "Knightfall "+name, from.ID, false)
		mkRelinkTrashedBook(t, st, "Hush "+name, into.ID, false)
		if i == 0 {
			lockBookAuthor(t, st, book)
		}
	}
	return st
}

var stripPreviewNames = []string{
	"Harvey Dent", "Oswald Cobblepot", "Edward Nygma", "Pamela Isley",
	"Victor Fries", "Jonathan Crane", "Waylon Jones", "Floyd Lawton",
}

// A report-only run reads each merge's books and locks for every plan, and those
// reads are independent (the preview writes nothing), so they run on a bounded
// pool rather than one plan at a time. The totals are the serial run's: one merge
// held by its locked book, the rest mergeable, none failed.
func TestAuthorStripMerge_PreviewReadsPlansConcurrentlyAndCountsThemAsTheSerialRunDid(t *testing.T) {
	st := stripPreviewWorld(t, stripPreviewNames)
	var mu sync.Mutex
	inFlight, maxSeen := 0, 0

	summary := runStripMergePreview(t, overlapDB{PebbleStore: st, mu: &mu, inFlight: &inFlight, maxSeen: &maxSeen})

	wantSummary(t, summary, "merges-held-user-locked=1 ", "failed=0 ", "merged=0 ", "books-touched=0 ", "mergeable=8 ")
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, maxSeen, 2, "the preview read its plans one at a time")
	require.LessOrEqual(t, maxSeen, authorStripPreviewParallelism(), "the preview ran more reads at once than its pool allows")
}

// The pool is bounded by the cores, not by the number of plans, and never below
// two so a one-core host still overlaps the store's waits.
func TestAuthorStripPreviewParallelism_IsBoundedByTheCores(t *testing.T) {
	require.GreaterOrEqual(t, authorStripPreviewParallelism(), 2)
	require.LessOrEqual(t, authorStripPreviewParallelism(), max(runtime.NumCPU(), 4))
}

// An apply is NOT pooled: its writes keep the plan order the undo ledger and the
// held counts depend on, so the same world applied reads one plan at a time.
func TestAuthorStripMerge_ApplyStaysSequential(t *testing.T) {
	st := stripPreviewWorld(t, stripPreviewNames[:4])
	var mu sync.Mutex
	inFlight, maxSeen := 0, 0

	_ = runStripMergeOnStore(t, overlapDB{PebbleStore: st, mu: &mu, inFlight: &inFlight, maxSeen: &maxSeen})

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, maxSeen, "the apply overlapped its plans")
}

// Many more plans than workers: the pool never has more reads in flight than
// authorStripPreviewParallelism, however long the plan list is, and still reads
// every plan.
func TestPreviewAuthorStripPlans_NeverRunsMoreReadsThanThePoolAllows(t *testing.T) {
	st := newRelinkTrashedStore(t)
	var mu sync.Mutex
	inFlight, maxSeen := 0, 0
	db := overlapDB{PebbleStore: st, mu: &mu, inFlight: &inFlight, maxSeen: &maxSeen}
	env := authorStripMergeEnv{
		Progress: func(int, int, string) {},
		Store:    t6g1HeadStore{OpsStore: db},
		Writer:   t6g1HeadWriter{db},
	}
	plans := make([]authorStripPlan, 3*authorStripPreviewParallelism())
	for i := range plans {
		plans[i] = authorStripPlan{from: database.Author{ID: 1000 + i, Name: "Alfred Pennyworth"}, reason: "junk"}
	}

	out := previewAuthorStripPlans(context.Background(), env, plans, map[int]bool{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Len(t, out, len(plans))
	for i, pv := range out {
		require.NoError(t, pv.err, "plan %d", i)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, authorStripPreviewParallelism(), maxSeen, "the pool should fill, and never exceed, its size")
}
