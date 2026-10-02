// file: internal/plugins/metafetch/asin_backfill_queue_wiring_test.go
// version: 1.0.0
// guid: 5b1e8c37-2f04-4d9a-8e6b-c03a71f2d945
// last-edited: 2026-10-02

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A metadata apply queues a book whose fresh no-match markers would make the
// scheduled walk skip it for 30 days. The queue is lost (stopped before its
// flush, as on a restart). The markers were cleared on Add, so the next full
// walk searches the book again and fills it.
func TestASINBackfillQueue_LostQueueStillReachesScheduledWalk(t *testing.T) {
	fa := rrFake()
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})
	fresh, _ := json.Marshal(asinMissMarker{At: time.Now(), Outcome: asinOutcomeNoMatch})
	for _, key := range []string{asinMissKey(b.ID), isbnMissKey(b.ID)} {
		if err := s.SetRaw(key, fresh); err != nil {
			t.Fatal(err)
		}
	}

	if res := runASIN(t, p, `{"dry_run":true}`).res(t); res.SkippedRecentMiss != 1 {
		t.Fatalf("precondition: a fresh marker must make the walk skip the book: %+v", res)
	}

	enqueued := 0
	q := metafetch.NewASINBackfillQueue(func(context.Context, []string) (string, error) {
		enqueued++
		return "op", nil
	}, nil, func(id string) { clearBackfillMarkers(s, id) }, time.Hour)
	q.Add(b.ID)
	q.Stop() // the in-memory queue is gone before it flushed
	if enqueued != 0 {
		t.Fatalf("stopped queue enqueued %d runs", enqueued)
	}

	for _, key := range []string{asinMissKey(b.ID), isbnMissKey(b.ID)} {
		if raw, err := s.GetRaw(key); err != nil || len(raw) != 0 {
			t.Fatalf("marker %s survived Add (err %v)", key, err)
		}
	}
	res := runASIN(t, p, `{"dry_run":false}`).res(t)
	if res.SkippedRecentMiss != 0 || res.MatchedWritten != 1 {
		t.Fatalf("the scheduled walk did not re-check the cleared book: %+v", res)
	}
	if got := asinOf(t, s, b.ID); got != rrProduct().ASIN {
		t.Fatalf("asin = %q, want %q", got, rrProduct().ASIN)
	}
}

// clearBackfillMarkers does not issue a delete for a marker that is absent.
func TestClearBackfillMarkers_SkipsAbsentKeys(t *testing.T) {
	var deleted []string
	st := &database.MockStore{
		GetRawFunc: func(key string) ([]byte, error) {
			if key == asinMissKey("b1") {
				return []byte(`{}`), nil
			}
			if key == isbnMissKey("b2") {
				return nil, errors.New("read failed")
			}
			return nil, nil
		},
		DeleteRawFunc: func(key string) error { deleted = append(deleted, key); return nil },
	}
	clearBackfillMarkers(st, "b1")
	clearBackfillMarkers(st, "b2")
	want := fmt.Sprint([]string{asinMissKey("b1"), isbnMissKey("b2")})
	if got := fmt.Sprint(deleted); got != want {
		t.Fatalf("deleted = %s, want %s (present marker, and an unreadable one)", got, want)
	}
}

// failingBookStore fails GetBookByID for one id.
type failingBookStore struct {
	*database.PebbleStore
	failID string
}

func (f failingBookStore) GetBookByID(id string) (*database.Book, error) {
	if id == f.failID {
		return nil, errors.New("pebble: read failed")
	}
	return f.PebbleStore.GetBookByID(id)
}

// One unreadable id in a book_ids run is logged and counted; the other books
// are still processed.
func TestASINBackfill_BookIDsLoadErrorSkipsOnlyThatBook(t *testing.T) {
	fa := rrFake()
	_, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Pierce Brown")
	b := mkBook(t, s, &database.Book{Title: "Red Rising", AuthorID: &aid, ISBN13: new(isbnRR)})
	p := New(failingBookStore{PebbleStore: s, failID: "broken"}, nil)
	p.newAudible = func() audibleIdentitySearcher { return fa }

	params := fmt.Sprintf(`{"dry_run":false,"book_ids":["broken",%q]}`, b.ID)
	res := runASIN(t, p, params).res(t)
	if res.LoadFailed != 1 || res.MatchedWritten != 1 {
		t.Fatalf("result = %+v, want load_failed=1 matched_written=1", res)
	}
	if raw, _ := s.GetRaw(asinMissKey("broken")); raw != nil {
		t.Fatal("a load failure wrote a no-match marker")
	}
}

// Stop is safe on the typed-nil plugin Build returns and on a plugin with no
// service.
func TestPluginStop_NilSafe(t *testing.T) {
	var p *Plugin
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (&Plugin{}).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
