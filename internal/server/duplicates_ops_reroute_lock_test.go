// file: internal/server/duplicates_ops_reroute_lock_test.go
// version: 1.0.0
// guid: 511d8e1c-2f2f-49f7-a5f0-1e48bce72f0f
// last-edited: 2026-09-14

// A1#10: applyBookMergeReroute used to copy the losers' iTunes stats onto the
// keep book itself -- GetBookByID + full-row UpdateBook, outside
// mergeSerializeMu, with a write failure only logged -- and then call
// merge.Service.MergeBooks. Each test below fails against that code:
//
//   - a failed carry write let the merge soft-delete the losers anyway;
//   - a write to the keep book between the carry's read and its UpdateBook was
//     reverted;
//   - a loser already consumed by another merge still had its stats copied onto
//     a second keeper whose own merge was then refused.
package server

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
)

// rerouteFaultStore wraps a real PebbleStore. failKeepWrite makes the FIRST
// write to keepID (UpdateBook or ModifyBook, whichever comes first) fail;
// onFirstKeepRead runs once, right after the first GetBookByID(keepID).
type rerouteFaultStore struct {
	*database.PebbleStore
	keepID          string
	failKeepWrite   bool
	keepWrites      atomic.Int32
	onFirstKeepRead func()
	readOnce        sync.Once
}

var errInjectedKeepWrite = errors.New("injected keep-book write failure")

func (s *rerouteFaultStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id == s.keepID && s.onFirstKeepRead != nil {
		s.readOnce.Do(s.onFirstKeepRead)
	}
	return b, err
}

func (s *rerouteFaultStore) firstKeepWriteFails(id string) bool {
	return id == s.keepID && s.failKeepWrite && s.keepWrites.Add(1) == 1
}

func (s *rerouteFaultStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	if s.firstKeepWriteFails(id) {
		return nil, errInjectedKeepWrite
	}
	return s.PebbleStore.UpdateBook(id, b)
}

func (s *rerouteFaultStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.firstKeepWriteFails(id) {
		return nil, errInjectedKeepWrite
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func newRerouteStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createRerouteBook(t *testing.T, store *database.PebbleStore, title string, rating *int) string {
	t.Helper()
	id := ulid.Make().String()
	if _, err := store.CreateBook(&database.Book{
		ID: id, Title: title, Format: "m4b", FilePath: "/tmp/" + id + ".m4b",
		ITunesRating: rating,
	}); err != nil {
		t.Fatalf("CreateBook %s: %v", title, err)
	}
	return id
}

func mustGetRerouteBook(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID %s: %v (nil=%v)", id, err, b == nil)
	}
	return b
}

func isSoftDeleted(b *database.Book) bool {
	return b.MarkedForDeletion != nil && *b.MarkedForDeletion
}

// A carry that cannot be written must fail the merge with the loser still live
// and still holding its stats -- not log a warning and soft-delete it.
func TestApplyBookMergeReroute_CarryWriteFailureAbortsBeforeSoftDelete(t *testing.T) {
	inner := newRerouteStore(t)
	keepID := createRerouteBook(t, inner, "Keep", nil)
	loserID := createRerouteBook(t, inner, "Loser", new(80))

	store := &rerouteFaultStore{PebbleStore: inner, keepID: keepID, failKeepWrite: true}
	err := applyBookMergeReroute(merge.NewService(store), keepID, []string{loserID})
	if !errors.Is(err, errInjectedKeepWrite) {
		t.Fatalf("applyBookMergeReroute err = %v, want the injected keep-write failure", err)
	}

	loser := mustGetRerouteBook(t, inner, loserID)
	if isSoftDeleted(loser) {
		t.Fatal("loser was soft-deleted although the survivor's carry write failed -- its iTunes stats are now on a row headed for purge")
	}
	if loser.ITunesRating == nil || *loser.ITunesRating != 80 {
		t.Fatalf("loser rating = %v, want 80 untouched", loser.ITunesRating)
	}
}

// A write to the keep book that lands after the merge read it must survive the
// merge. The old carry wrote back the full row it read, reverting that write.
func TestApplyBookMergeReroute_ConcurrentKeepEditSurvives(t *testing.T) {
	inner := newRerouteStore(t)
	keepID := createRerouteBook(t, inner, "Keep", nil)
	loserID := createRerouteBook(t, inner, "Loser", new(80))

	store := &rerouteFaultStore{PebbleStore: inner, keepID: keepID}
	store.onFirstKeepRead = func() {
		// Stands in for a metadata apply or user edit committing to the keep
		// row between the merge path's read and its write.
		if _, err := inner.ModifyBook(keepID, func(b *database.Book) error {
			b.Title = "Edited Mid-Merge"
			return nil
		}); err != nil {
			t.Errorf("concurrent edit: %v", err)
		}
	}
	if err := applyBookMergeReroute(merge.NewService(store), keepID, []string{loserID}); err != nil {
		t.Fatalf("applyBookMergeReroute: %v", err)
	}

	keep := mustGetRerouteBook(t, inner, keepID)
	if keep.Title != "Edited Mid-Merge" {
		t.Fatalf("keep title = %q, want the concurrent edit to survive the merge (stale full-row write reverted it)", keep.Title)
	}
	if keep.ITunesRating == nil || *keep.ITunesRating != 80 {
		t.Fatalf("keep rating = %v, want 80 carried from the loser", keep.ITunesRating)
	}
	if isSoftDeleted(keep) || !isSoftDeleted(mustGetRerouteBook(t, inner, loserID)) {
		t.Fatal("merge outcome wrong: want keep live and loser soft-deleted")
	}
}

// A loser another merge already consumed must not donate its stats to a second
// keeper: the second merge is refused, and it must be refused before any carry.
func TestApplyBookMergeReroute_ConsumedLoserDoesNotCarry(t *testing.T) {
	store := newRerouteStore(t)
	keepA := createRerouteBook(t, store, "Keep A", nil)
	keepB := createRerouteBook(t, store, "Keep B", nil)
	loserID := createRerouteBook(t, store, "Loser", new(80))
	ms := merge.NewService(store)

	if err := applyBookMergeReroute(ms, keepA, []string{loserID}); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	err := applyBookMergeReroute(ms, keepB, []string{loserID})
	var sd *merge.SoftDeletedInputError
	if !errors.As(err, &sd) {
		t.Fatalf("second merge err = %v, want SoftDeletedInputError for the consumed loser", err)
	}
	if b := mustGetRerouteBook(t, store, keepB); b.ITunesRating != nil {
		t.Fatalf("keep B rating = %d, want nil: a refused merge copied the consumed loser's stats onto it", *b.ITunesRating)
	}
}

// Two merges racing for the same loser: exactly one wins, and only the winner's
// keeper ends up with the loser's stats. Run with -race.
func TestApplyBookMergeReroute_ConcurrentMergesShareLoser(t *testing.T) {
	store := newRerouteStore(t)
	keepers := []string{
		createRerouteBook(t, store, "Keep A", nil),
		createRerouteBook(t, store, "Keep B", nil),
	}
	loserID := createRerouteBook(t, store, "Loser", new(80))
	ms := merge.NewService(store)

	errs := make([]error, len(keepers))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, k := range keepers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = applyBookMergeReroute(ms, k, []string{loserID})
		}()
	}
	close(start)
	wg.Wait()

	succeeded, carried := 0, 0
	for i, k := range keepers {
		if errs[i] == nil {
			succeeded++
		} else if sd := (*merge.SoftDeletedInputError)(nil); !errors.As(errs[i], &sd) {
			t.Fatalf("merge into %s: unexpected error %v", k, errs[i])
		}
		b := mustGetRerouteBook(t, store, k)
		if b.ITunesRating != nil {
			carried++
			if errs[i] != nil {
				t.Fatalf("keeper %s holds the loser's rating but its merge was refused", k)
			}
		}
	}
	if succeeded != 1 || carried != 1 {
		t.Fatalf("succeeded=%d carried=%d, want exactly one merge to win and carry", succeeded, carried)
	}
}
