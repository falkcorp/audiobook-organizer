// file: internal/plugins/maintenance/author_split_apply_test.go
// version: 1.1.0
// guid: 4e8a1c73-b5d2-49f6-8a0e-d37c9f12b6a5
// last-edited: 2026-10-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// splitDeps drives runAuthorSplitScan with a real store, a steerable scan
// stand-down gate and a count of dedup cache invalidations. The apply calls the
// gate from pool workers, so every recorded field is behind mu.
type splitDeps struct {
	fakeDeps

	// renewOK answers RenewScanStandDown (1-based call number); nil = always true.
	renewOK    func(call int) bool
	acquireErr error

	mu            sync.Mutex
	acquires      []string // holder op ids
	releases      int
	renews        int
	invalidations int
}

func (d *splitDeps) InvalidateDedupCache() {
	d.mu.Lock()
	d.invalidations++
	d.mu.Unlock()
}

func (d *splitDeps) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	if d.acquireErr != nil {
		return nil, d.acquireErr
	}
	d.mu.Lock()
	d.acquires = append(d.acquires, holder)
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		d.releases++
		d.mu.Unlock()
	}, nil
}

func (d *splitDeps) RenewScanStandDown(string) bool {
	d.mu.Lock()
	d.renews++
	n := d.renews
	d.mu.Unlock()
	return d.renewOK == nil || d.renewOK(n)
}

func (d *splitDeps) snapshot() (acquires []string, releases, invalidations int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.acquires...), d.releases, d.invalidations
}

func newSplitDeps(store database.Store) *splitDeps {
	return &splitDeps{fakeDeps: fakeDeps{store: store}}
}

func runSplit(t *testing.T, d *splitDeps, body string) (*opIDReporter, error) {
	t.Helper()
	p := &Plugin{deps: d}
	rep := &opIDReporter{id: "op-split"}
	var raw json.RawMessage
	if body != "" {
		raw = json.RawMessage(body)
	}
	return rep, p.runAuthorSplitScan(context.Background(), raw, rep)
}

// mkSplitBooks creates n live books credited (junction + legacy primary) to the
// composite and returns their IDs.
func mkSplitBooks(t *testing.T, st database.Store, compositeID, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		b, err := st.CreateBook(&database.Book{
			Title:            fmt.Sprintf("split-apply-%d-%02d", compositeID, i),
			FilePath:         fmt.Sprintf("/split-apply/%d/%02d.m4b", compositeID, i),
			AuthorID:         new(compositeID),
			IsPrimaryVersion: new(true),
		})
		require.NoError(t, err)
		require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: compositeID, Role: "author"}}))
		ids[i] = b.ID
	}
	return ids
}

func creditCounts(t *testing.T, st database.Store, bookID string) map[int]int {
	t.Helper()
	credits, err := st.GetBookAuthors(bookID)
	require.NoError(t, err)
	out := map[int]int{}
	for _, c := range credits {
		out[c.AuthorID]++
	}
	return out
}

// The preview takes no gate and invalidates nothing: a daily scheduled preview
// must not park the scan.
func TestAuthorSplitScan_PreviewTakesNoStandDown(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	mkSplitBooks(t, st, composite.ID, 2)
	d := newSplitDeps(st)

	for _, body := range []string{"", `{}`, `{"dry_run":true}`} {
		_, err := runSplit(t, d, body)
		require.NoError(t, err)
	}
	acquires, _, inv := d.snapshot()
	require.Empty(t, acquires, "a preview took the scan stand-down")
	require.Zero(t, inv)
	require.Zero(t, d.renews, "a preview renewed a lease")
}

// An apply that finds nothing to split must not park a running scan.
func TestAuthorSplitScan_ApplyWithNothingToSplitTakesNoStandDown(t *testing.T) {
	st := newSplitRelinkStore(t)
	_, err := st.CreateAuthor("Alice Smith")
	require.NoError(t, err)
	d := newSplitDeps(st)

	rep, err := runSplit(t, d, `{"dry_run":false}`)
	require.NoError(t, err)
	acquires, _, inv := d.snapshot()
	require.Empty(t, acquires)
	require.Zero(t, inv, "nothing was written, so the cache is not dropped")
	require.Contains(t, strings.Join(rep.logs, "\n"), "Split 0 composite authors")
}

// The apply holds the stand-down under the op's own id for the whole pass,
// releases it, and drops the dedup cache exactly once.
func TestAuthorSplitScan_ApplyHoldsStandDownAndInvalidatesOnce(t *testing.T) {
	st := newSplitRelinkStore(t)
	c1, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	c2, err := st.CreateAuthor("Cara Reed & Dan Black")
	require.NoError(t, err)
	mkSplitBooks(t, st, c1.ID, 3)
	mkSplitBooks(t, st, c2.ID, 3)
	d := newSplitDeps(st)

	rep, err := runSplit(t, d, `{"dry_run":false}`)
	require.NoError(t, err)
	acquires, releases, inv := d.snapshot()
	require.Equal(t, []string{"op-split"}, acquires)
	require.Equal(t, 1, releases)
	require.Equal(t, 1, inv)
	require.Contains(t, strings.Join(rep.logs, "\n"), "Split 2 composite authors, updated 6 books (0 errors")
}

// A scan that does not park fails the apply before its first write.
func TestAuthorSplitScan_ApplyRefusesWhenScanDoesNotStandDown(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	books := mkSplitBooks(t, st, composite.ID, 2)
	d := newSplitDeps(st)
	d.acquireErr = errors.New("scan did not park within 5m0s")

	_, err = runSplit(t, d, `{"dry_run":false}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "a library scan is running")
	require.Contains(t, err.Error(), "maintenance.author-split-scan apply")

	alice, gerr := st.GetAuthorByName("Alice Smith")
	require.NoError(t, gerr)
	require.Nil(t, alice, "a part was created without the stand-down")
	kept, gerr := st.GetAuthorByID(composite.ID)
	require.NoError(t, gerr)
	require.NotNil(t, kept)
	require.Equal(t, map[int]int{composite.ID: 1}, creditCounts(t, st, books[0]))
	_, _, inv := d.snapshot()
	require.Zero(t, inv)
}

// A lapsed lease between composites stops the apply before the next composite's
// parts are created, and the error names where it stopped. The first composite's
// parts were written, so the cache is still dropped.
func TestAuthorSplitScan_ApplyStopsBeforeNextCompositeWhenLeaseIsLost(t *testing.T) {
	st := newSplitRelinkStore(t)
	c1, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	c2, err := st.CreateAuthor("Cara Reed & Dan Black")
	require.NoError(t, err)
	mkSplitBooks(t, st, c1.ID, 1)
	mkSplitBooks(t, st, c2.ID, 1)
	d := newSplitDeps(st)
	d.renewOK = func(call int) bool { return call == 1 } // only the first composite's beat

	_, err = runSplit(t, d, `{"dry_run":false}`)
	require.ErrorIs(t, err, registry.ErrScanStandDownLost)
	require.ErrorContains(t, err, "stopped before creating the parts of")

	dan, gerr := st.GetAuthorByName("Dan Black")
	require.NoError(t, gerr)
	require.Nil(t, dan, "a part of the second composite was created after the lease was lost")
	for _, c := range []*database.Author{c1, c2} {
		kept, gerr := st.GetAuthorByID(c.ID)
		require.NoError(t, gerr)
		require.NotNil(t, kept, "no composite may be deleted when the apply stops before the pool")
	}
	_, releases, inv := d.snapshot()
	require.Equal(t, 1, releases)
	require.Equal(t, 1, inv, "the first composite's parts were created; the cache must be dropped")
}

// A lapsed lease inside the pool stops it before the next book's write: not
// every book is relinked, the composite is not deleted, and the error says how
// far the apply got.
func TestAuthorSplitScan_ApplyStopsInPoolWhenLeaseIsLost(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	books := mkSplitBooks(t, st, composite.ID, 24)
	d := newSplitDeps(st)
	d.renewOK = func(call int) bool { return call <= 3 }

	_, err = runSplit(t, d, `{"dry_run":false}`)
	require.ErrorIs(t, err, registry.ErrScanStandDownLost)
	require.ErrorContains(t, err, "author-split-scan stopped after")

	kept, gerr := st.GetAuthorByID(composite.ID)
	require.NoError(t, gerr)
	require.NotNil(t, kept, "the composite was deleted by an apply that lost its lease")
	untouched := 0
	for _, id := range books {
		if creditCounts(t, st, id)[composite.ID] == 1 {
			untouched++
		}
	}
	require.Positive(t, untouched, "every book was relinked after the lease was lost")
	_, _, inv := d.snapshot()
	require.Equal(t, 1, inv, "books written before the stop changed credits; the cache must be dropped")
}

// CLAUDE.md concurrency rule: the apply runs on a bounded pool. Each credit
// write blocks until a second is in flight; a sequential loop never starts the
// second and the wait times out. Run under -race: the counters, the Label
// closure and the fakes are shared across workers.
func TestAuthorSplitScan_ApplyRelinksConcurrently(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	books := mkSplitBooks(t, st, composite.ID, 16)

	var inFlight, peak atomic.Int64
	both := make(chan struct{})
	var closeOnce sync.Once
	store := splitFaultStore{PebbleStore: st}
	store.beforeModifyBookAuthors = func(string) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 2 {
			closeOnce.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
		}
	}
	d := newSplitDeps(store)

	rep, err := runSplit(t, d, `{"dry_run":false}`)
	require.NoError(t, err)
	require.GreaterOrEqual(t, peak.Load(), int64(2), "the apply relinked one book at a time")
	require.LessOrEqual(t, peak.Load(), int64(authorSplitScanWorkers), "the pool must stay bounded")
	require.Contains(t, strings.Join(rep.logs, "\n"), "Split 1 composite authors, updated 16 books")
	alice, err := st.GetAuthorByName("Alice Smith")
	require.NoError(t, err)
	require.NotNil(t, alice)
	for _, id := range books {
		require.Equal(t, 1, creditCounts(t, st, id)[alice.ID])
	}
}

// A book credited to two composites is one work item, and a part the composites
// share is created once. Both substitutions land, the primary follows the
// composite it named, and each person is credited once.
func TestAuthorSplitScan_BookUnderTwoCompositesAndSharedPart(t *testing.T) {
	st := newSplitRelinkStore(t)
	c1, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	c2, err := st.CreateAuthor("Alice Smith & Cara Reed")
	require.NoError(t, err)
	only1 := mkSplitBooks(t, st, c1.ID, 3)
	only2 := mkSplitBooks(t, st, c2.ID, 3)
	both, err := st.CreateBook(&database.Book{
		Title: "split-apply-both", FilePath: "/split-apply/both.m4b",
		AuthorID: new(c1.ID), IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(both.ID, []database.BookAuthor{
		{BookID: both.ID, AuthorID: c1.ID, Role: "author"},
		{BookID: both.ID, AuthorID: c2.ID, Role: "author", Position: 1},
	}))

	_, err = runSplit(t, newSplitDeps(st), `{"dry_run":false}`)
	require.NoError(t, err)

	all, err := st.GetAllAuthors()
	require.NoError(t, err)
	byName := map[string]int{}
	for _, a := range all {
		byName[a.Name]++
	}
	require.Equal(t, map[string]int{"Alice Smith": 1, "Bob Jones": 1, "Cara Reed": 1}, byName,
		"each part exists exactly once and both composites are gone")
	alice, _ := st.GetAuthorByName("Alice Smith")
	bob, _ := st.GetAuthorByName("Bob Jones")
	cara, _ := st.GetAuthorByName("Cara Reed")

	require.Equal(t, map[int]int{alice.ID: 1, bob.ID: 1, cara.ID: 1}, creditCounts(t, st, both.ID))
	full, err := st.GetBookByID(both.ID)
	require.NoError(t, err)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, alice.ID, *full.AuthorID)
	for _, id := range only1 {
		require.Equal(t, map[int]int{alice.ID: 1, bob.ID: 1}, creditCounts(t, st, id))
	}
	for _, id := range only2 {
		require.Equal(t, map[int]int{alice.ID: 1, cara.ID: 1}, creditCounts(t, st, id))
	}
}

// The credit swap is a single ModifyBookAuthors, so a book another writer moved
// off the composite between the listing and the write is left as that writer
// left it: no parts are resurrected on it and its new credit survives. Here the
// other writer re-credits the book to a different author and repoints its
// primary just before the split's primary write.
func TestAuthorSplitScan_LeavesBookAnotherWriterMovedOffTheComposite(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	other, err := st.CreateAuthor("Zed Other")
	require.NoError(t, err)
	books := mkSplitBooks(t, st, composite.ID, 2)
	moved, kept := books[0], books[1]

	store := splitFaultStore{PebbleStore: st}
	store.beforeModifyBookAuthors = func(id string) {
		if id != moved {
			return
		}
		_, merr := st.ModifyBook(id, func(b *database.Book) error {
			b.AuthorID = new(other.ID)
			return nil
		})
		require.NoError(t, merr)
		require.NoError(t, st.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: other.ID, Role: "author"}}))
	}

	_, err = runSplit(t, newSplitDeps(store), `{"dry_run":false}`)
	require.NoError(t, err)

	require.Equal(t, map[int]int{other.ID: 1}, creditCounts(t, st, moved), "the other writer's credit was overwritten")
	full, err := st.GetBookByID(moved)
	require.NoError(t, err)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, other.ID, *full.AuthorID)

	alice, err := st.GetAuthorByName("Alice Smith")
	require.NoError(t, err)
	require.NotNil(t, alice)
	bob, err := st.GetAuthorByName("Bob Jones")
	require.NoError(t, err)
	require.Equal(t, map[int]int{alice.ID: 1, bob.ID: 1}, creditCounts(t, st, kept))
	gone, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "nothing credits the composite any more, so it is deleted")
}

// A book that credits the composite only through Book.AuthorID (no junction row,
// the default shape CreateBook leaves) or through both, with either of the two
// per-book writes failing on the first pass. Nothing may be lost: the composite
// survives the first pass because the book is still listed under it, and a
// healthy rerun credits both parts exactly once, repoints the primary and only
// then deletes the composite. The credits are written first on purpose: the
// legacy AuthorID is the only evidence of the composite on a legacy-only book,
// and repointing it before the credits were safe let the composite be deleted
// with parts[1:] never credited.
func TestAuthorSplitScan_NoCreditLostWhenEitherWriteFails(t *testing.T) {
	for _, shape := range []string{"legacy-only", "junction-and-legacy"} {
		for _, failing := range []string{"credits", "primary"} {
			t.Run(shape+"/fail-"+failing, func(t *testing.T) {
				st := newSplitRelinkStore(t)
				composite, err := st.CreateAuthor("Gina Rowe & Hal Pike")
				require.NoError(t, err)
				b, err := st.CreateBook(&database.Book{
					Title:            "split-crash-safe",
					FilePath:         "/split-crash-safe/book.m4b",
					AuthorID:         new(composite.ID),
					IsPrimaryVersion: new(true),
				})
				require.NoError(t, err)
				if shape == "junction-and-legacy" {
					require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: composite.ID, Role: "author"}}))
				}

				faulty := splitFaultStore{PebbleStore: st}
				if failing == "credits" {
					faulty.failCredits = b.ID
				} else {
					faulty.failPrimary = b.ID
				}
				rep, err := runSplit(t, newSplitDeps(faulty), `{"dry_run":false}`)
				require.NoError(t, err)

				kept, gerr := st.GetAuthorByID(composite.ID)
				require.NoError(t, gerr)
				require.NotNil(t, kept, "the composite was deleted while the book was only half split; logs: %v", rep.logs)
				gina, gerr := st.GetAuthorByName("Gina Rowe")
				require.NoError(t, gerr)
				require.NotNil(t, gina)
				hal, gerr := st.GetAuthorByName("Hal Pike")
				require.NoError(t, gerr)
				require.NotNil(t, hal)
				mid, gerr := st.GetBookByID(b.ID)
				require.NoError(t, gerr)
				require.NotNil(t, mid.AuthorID)
				if failing == "credits" {
					require.Equal(t, composite.ID, *mid.AuthorID, "the primary moved although the credits write failed")
				} else {
					// The credits landed first, so both parts are already credited.
					require.Equal(t, map[int]int{gina.ID: 1, hal.ID: 1}, creditCounts(t, st, b.ID))
				}

				_, err = runSplit(t, newSplitDeps(st), `{"dry_run":false}`)
				require.NoError(t, err)

				gone, gerr := st.GetAuthorByID(composite.ID)
				require.NoError(t, gerr)
				require.Nil(t, gone, "the rerun should finish the split")
				require.Equal(t, map[int]int{gina.ID: 1, hal.ID: 1}, creditCounts(t, st, b.ID))
				full, gerr := st.GetBookByID(b.ID)
				require.NoError(t, gerr)
				require.NotNil(t, full.AuthorID)
				require.Equal(t, gina.ID, *full.AuthorID)
			})
		}
	}
}

// Listing a composite's books can fall back to a Pebble scan, and the parts are
// already created by then, so the lease is checked before each composite's
// listing: a lease that lapsed after the first beat stops the apply there, names
// the composite, and deletes nothing.
func TestAuthorSplitScan_ApplyStopsBeforeListingWhenLeaseIsLost(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	books := mkSplitBooks(t, st, composite.ID, 2)
	d := newSplitDeps(st)
	d.renewOK = func(call int) bool { return call == 1 } // the part-creation beat only

	_, err = runSplit(t, d, `{"dry_run":false}`)
	require.ErrorIs(t, err, registry.ErrScanStandDownLost)
	require.ErrorContains(t, err, `stopped before listing books of "Alice Smith & Bob Jones"`)

	kept, gerr := st.GetAuthorByID(composite.ID)
	require.NoError(t, gerr)
	require.NotNil(t, kept)
	require.Equal(t, map[int]int{composite.ID: 1}, creditCounts(t, st, books[0]), "a book was relinked after the lease was lost")
	_, releases, inv := d.snapshot()
	require.Equal(t, 1, releases)
	require.Equal(t, 1, inv, "the parts were created before the stop; the cache must be dropped")
}
