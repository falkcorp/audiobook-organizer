// file: internal/maintenance/jobs/lost_update_test.go
// version: 1.0.0
// guid: 3b88da9b-5958-44c8-b57a-183be0ee6123
// last-edited: 2026-09-15

package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// lostUpdateRows is the stored table behind newLostUpdateStore. It models the
// OTHER writer the way internal/reconcile/elect_primaries_lost_update_test.go
// does: Duration=4242 lands on the stored row right after every raw
// GetBookByID (which returns a copy, as the real store does) and right before
// every ModifyBook takes the row. A job that reads the row and writes the
// WHOLE row back reverts it; one that writes through ModifyBook keeps it.
type lostUpdateRows struct {
	mu   sync.Mutex
	rows map[string]*database.Book
}

func (r *lostUpdateRows) landDuration(id string) {
	if b := r.rows[id]; b != nil {
		d := 4242
		b.Duration = &d
	}
}

func (r *lostUpdateRows) get(id string) *database.Book {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b := r.rows[id]; b != nil {
		cp := *b
		return &cp
	}
	return nil
}

func newLostUpdateStore(books ...database.Book) (*database.MockStore, *lostUpdateRows) {
	r := &lostUpdateRows{rows: map[string]*database.Book{}}
	for i := range books {
		b := books[i]
		r.rows[b.ID] = &b
	}
	m := &database.MockStore{}
	m.GetAllBooksCoreFunc = func(limit, offset int) ([]database.BookCore, error) {
		if offset > 0 {
			return nil, nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		out := make([]database.BookCore, 0, len(r.rows))
		for _, b := range r.rows {
			out = append(out, b.Core())
		}
		return out, nil
	}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		b := r.rows[id]
		if b == nil {
			return nil, nil
		}
		cp := *b
		r.landDuration(id)
		return &cp, nil
	}
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		cp := *b
		r.rows[id] = &cp
		return &cp, nil
	}
	m.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.landDuration(id)
		b := r.rows[id]
		if b == nil {
			return nil, nil
		}
		cp := *b
		if err := fn(&cp); err != nil {
			if errors.Is(err, database.ErrSkipBookWrite) {
				return b, nil
			}
			return nil, err
		}
		r.rows[id] = &cp
		out := cp
		return &out, nil
	}
	m.DeleteSeriesFunc = func(int) error { return nil }
	return m, r
}

func assertDurationKept(t *testing.T, site string, got *database.Book) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: book vanished from the store", site)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the %s write: got %v, want 4242 (the concurrent writer's value)", site, got.Duration)
	}
}

// TestNormalizePrimaryFlags_DoesNotRevertConcurrentColumns pins the
// lost-update fix (audit A1#15) on normalize-primary-flags: it must set only
// IsPrimaryVersion, so a Duration another writer commits between its read and
// its write survives.
func TestNormalizePrimaryFlags_DoesNotRevertConcurrentColumns(t *testing.T) {
	store, rows := newLostUpdateStore(database.Book{ID: "solo", Title: "Solo"})
	j := &normalizePrimaryFlagsJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, false); err != nil {
		t.Fatalf("normalize-primary-flags: %v", err)
	}
	got := rows.get("solo")
	if got == nil || got.IsPrimaryVersion == nil || !*got.IsPrimaryVersion {
		t.Fatalf("flag not written as primary: %+v", got)
	}
	assertDurationKept(t, "normalize-primary-flags", got)
}

// TestFixAuthorNarratorSwap_DoesNotRevertConcurrentColumns pins the same fix
// on fix-author-narrator-swap: it must clear only AuthorID.
func TestFixAuthorNarratorSwap_DoesNotRevertConcurrentColumns(t *testing.T) {
	authorID := 7
	name := "Stephen King"
	store, rows := newLostUpdateStore(database.Book{ID: "swap", Title: "Swapped", AuthorID: &authorID, Narrator: &name})
	store.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: id, Name: name}, nil
	}
	j := &fixAuthorNarratorSwapJob{}
	if err := j.Run(context.Background(), store, &nopReporter{}, false); err != nil {
		t.Fatalf("fix-author-narrator-swap: %v", err)
	}
	got := rows.get("swap")
	if got == nil || got.AuthorID != nil {
		t.Fatalf("AuthorID not cleared by the swap fix: %+v", got)
	}
	assertDurationKept(t, "fix-author-narrator-swap", got)
}

// TestCsMergeSeriesGroup_DoesNotRevertConcurrentColumns pins the fix on
// cleanup-series' merge path: it must repoint only SeriesID.
func TestCsMergeSeriesGroup_DoesNotRevertConcurrentColumns(t *testing.T) {
	from := 7
	store, rows := newLostUpdateStore(database.Book{ID: "m1", Title: "Merged", SeriesID: &from})
	members := database.SeriesBooksMap{from: {{ID: "m1"}}}
	merged, refused, err := csMergeSeriesGroup(store, 1, []int{from}, map[int]int{from: 1}, members)
	if err != nil {
		t.Fatalf("csMergeSeriesGroup: %v", err)
	}
	if merged != 1 || refused != 0 {
		t.Fatalf("merged=%d refused=%d, want 1/0", merged, refused)
	}
	got := rows.get("m1")
	if got == nil || got.SeriesID == nil || *got.SeriesID != 1 {
		t.Fatalf("SeriesID not repointed by the merge: %+v", got)
	}
	assertDurationKept(t, "cleanup-series merge", got)
}

// TestCsUnlinkAndDeleteSeries_DoesNotRevertConcurrentColumns pins the fix on
// cleanup-series' unlink path: it must clear only SeriesID and SeriesSequence.
func TestCsUnlinkAndDeleteSeries_DoesNotRevertConcurrentColumns(t *testing.T) {
	sid := 9
	seq := 2
	store, rows := newLostUpdateStore(database.Book{ID: "u1", Title: "Unlinked", SeriesID: &sid, SeriesSequence: &seq})
	members := database.SeriesBooksMap{sid: {{ID: "u1"}}}
	if err := csUnlinkAndDeleteSeries(store, []database.BookCore{{ID: "u1"}}, sid, members); err != nil {
		t.Fatalf("csUnlinkAndDeleteSeries: %v", err)
	}
	got := rows.get("u1")
	if got == nil || got.SeriesID != nil || got.SeriesSequence != nil {
		t.Fatalf("series link not cleared by the unlink: %+v", got)
	}
	assertDurationKept(t, "cleanup-series unlink", got)
}
