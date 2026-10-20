// file: internal/server/handlers/abs/user_state_deadlock_test.go
// version: 2.0.1
// guid: 0630076b-d80b-4cf5-b741-2fcc6a0bf393
// last-edited: 2026-10-20

package abs_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// fixedUsersStore answers ListUsers with a chosen set of user ids, so the
// merge paths and the backfill fan out over the same user id the ABS harness
// writes as (plus a second user, so the multi-user stripe spans are real).
type fixedUsersStore struct {
	*database.PebbleStore
	ids []string
}

func (s fixedUsersStore) ListUsers() ([]database.User, error) {
	out := make([]database.User, 0, len(s.ids))
	for _, id := range s.ids {
		out = append(out, database.User{ID: id})
	}
	return out, nil
}

// quietReporter is a maintenance.ProgressReporter that records nothing.
type quietReporter struct{}

func (quietReporter) SetTotal(int)                {}
func (quietReporter) Increment()                  {}
func (quietReporter) Log(string, string, *string) {}

// Every listening-state writer runs at once on overlapping (user, book) keys
// under -race, with a watchdog. None may deadlock. The writers:
//
//   - ABS progress PATCH (the ABS harness's fake library);
//   - the real web reading heartbeat (handlers.ReadingHandler.SetPosition)
//     on the survivor and on an absorbed book;
//   - a real merge.Service.CombineBooks of three books followed by its
//     UndoCombine: the combine's N-stripe span and the undo's put-back,
//     both under the merge lock;
//   - merge.FollowBookIDChange in both orders (syncRepointMu, not the merge
//     lock, so it contends with the combine's span for real);
//   - merge.FollowAbsorbedJournaled under merge.LockMergeRMW;
//   - merge.RestoreFollowedProgress under merge.LockMergeRMW and
//     merge.FollowMerge in both directions under merge.LockMergeRMW;
//   - merge.DiscardUserStateThenHardDelete (takes the merge lock itself);
//   - the backfill-itunes-positions maintenance job (a NumCPU pool, one
//     stripe per book);
//   - readstatus.SetManualStatus and readstatus.RebuildUserBookState.
//
// The combine, undo, journaled follow and discard can fail on the state the
// other writers leave behind (a pending repair naming the book, a book
// already combined). Those failures are logged and counted, not fatal: this
// test asserts the lock order, and a writer that returns an error has still
// taken and released its locks. The writers that cannot fail on a
// concurrent state change (PATCH, heartbeat, mark-as, the follows) fail the
// test on any error.
//
// The ABS harness writes through its fake library and the others through a
// Pebble store: the user-state stripes and the merge lock are process-wide
// and keyed by (user id, book id) strings, so the lock acquisition orders
// interleave exactly as in production; which store each writer then writes is
// not what a deadlock depends on.
func TestUserStateLock_ProgressAllWritersNoDeadlock(t *testing.T) {
	w := newWriteHarness(t)
	user, survivor := w.userID, w.bookID
	const second = "_local"
	absA, absB, x := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	t.Setenv(jobs.ITunesPositionBackfillUserIDEnv, user)

	ps, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	bookmark, plays := int64(90000), 2
	for _, b := range []struct{ id, path string }{
		{survivor, "/tmp/deadlock/s"}, {absA, "/tmp/deadlock/a"}, {absB, "/tmp/deadlock/b"}, {x, "/tmp/deadlock/x"},
	} {
		if _, err := ps.CreateBook(&database.Book{
			ID: b.id, Title: b.id, FilePath: b.path, Format: "mp3",
			ITunesBookmark: &bookmark, ITunesPlayCount: &plays,
		}); err != nil {
			t.Fatal(err)
		}
		if err := ps.CreateBookFile(&database.BookFile{
			ID: ulid.Make().String(), BookID: b.id, FilePath: b.path + "/01.mp3", Format: "mp3",
		}); err != nil {
			t.Fatal(err)
		}
	}
	ids := database.AsSyncIdentityStore(ps)
	for _, id := range []string{survivor, absA} {
		if _, err := ids.MintOrGetSyncID(id); err != nil {
			t.Fatal(err)
		}
	}
	store := fixedUsersStore{PebbleStore: ps, ids: []string{user, second}}
	at := time.Now().Add(-time.Hour).UTC()
	for _, u := range store.ids {
		for _, p := range []struct {
			book string
			sec  float64
			at   time.Time
		}{{absA, 50, at}, {survivor, 20, at.Add(-time.Minute)}, {x, 30, at}} {
			if err := ps.SetUserPositionAt(u, p.book, "seed", p.sec, p.at); err != nil {
				t.Fatal(err)
			}
		}
	}

	gin.SetMode(gin.TestMode)
	reading := handlers.NewReadingHandler(store)
	heartbeat := func(book string, i int) error {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book+"/position",
			bytes.NewBufferString(fmt.Sprintf(`{"segment_id":"hb","position_seconds":%d}`, 10+i)))
		req.Header.Set("Content-Type", "application/json")
		c.Request = req.WithContext(auth.WithUser(context.Background(), &database.User{ID: user}))
		c.Params = gin.Params{{Key: "id", Value: book}}
		reading.SetPosition(c)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("heartbeat = %d %s", rec.Code, rec.Body.String())
		}
		return nil
	}
	restore := []merge.CombineUserProgress{{
		UserID:              user,
		AbsorbedPositions:   []database.UserPosition{{SegmentID: "seed", PositionSeconds: 60, UpdatedAt: at}},
		SurvivorStateBefore: &database.UserBookState{UserID: user, BookID: survivor, Status: database.UserBookStatusInProgress, LastActivityAt: at},
	}}
	backfill, err := maintenance.Get("backfill-itunes-positions")
	if err != nil {
		t.Fatal(err)
	}

	const rounds = 25
	var wg sync.WaitGroup
	var combines, undos, journaled, discards, softErrs atomic.Int64
	soft := func(what string, i int, err error) {
		softErrs.Add(1)
		t.Logf("%s %d (soft): %v", what, i, err)
	}
	errs := make(chan error, 16*rounds)
	run := func(name string, f func(i int) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				if err := f(i); err != nil {
					errs <- fmt.Errorf("%s: %w", name, err)
					return
				}
			}
		}()
	}

	svc := merge.NewService(store)
	run("combine+undo", func(i int) error {
		res, err := svc.CombineBooks([]string{survivor, absA, absB}, survivor, nil)
		if err != nil {
			soft("combine", i, err)
			return nil
		}
		combines.Add(1)
		if _, err := svc.UndoCombine(res.JournalID); err != nil {
			soft("undo", i, err)
			return nil
		}
		undos.Add(1)
		return nil
	})
	run("heartbeat survivor", func(i int) error { return heartbeat(survivor, i) })
	run("heartbeat absA", func(i int) error { return heartbeat(absA, i) })
	run("abs progress", func(i int) error {
		code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": float64(100 + i)})
		if code != http.StatusOK {
			return fmt.Errorf("PATCH = %d %s", code, raw)
		}
		return nil
	})
	run("version-link follow survivor<->absA", func(i int) error {
		from, to := survivor, absA
		if i%2 == 1 {
			from, to = absA, survivor
		}
		if _, err := ids.MintOrGetSyncID(from); err != nil {
			return err
		}
		merge.FollowBookIDChange(store, from, to)
		return nil
	})
	run("journaled follow x->survivor", func(i int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		if _, _, err := merge.FollowAbsorbedJournaled(store, survivor, x, nil, nil); err != nil {
			soft("journaled follow", i, err)
			return nil
		}
		journaled.Add(1)
		return nil
	})
	run("combine undo put-back", func(int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		_, err := merge.RestoreFollowedProgress(store, survivor, x, false, restore)
		return err
	})
	run("follow x->survivor", func(int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		return merge.FollowMerge(store, nil, survivor, []string{x})
	})
	run("follow survivor->x", func(int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		return merge.FollowMerge(store, nil, x, []string{survivor})
	})
	// del is a no-op so x survives for the other writers: the production del
	// (audiobooks.purgeDeleteRow) reaches no user-state lock, so the discard's
	// lock surface is the merge lock plus every user's stripe on x, held from
	// the first read through del, which this exercises in full.
	run("discard x", func(i int) error {
		if _, err := merge.DiscardUserStateThenHardDelete(store, x, nil, func() error { return nil }); err != nil {
			soft("discard", i, err)
			return nil
		}
		discards.Add(1)
		return nil
	})
	run("itunes position backfill", func(int) error {
		return backfill.Run(context.Background(), store, quietReporter{}, false)
	})
	run("mark as", func(i int) error {
		status := database.UserBookStatusFinished
		if i%2 == 0 {
			status = ""
		}
		_, err := readstatus.SetManualStatus(store, user, survivor, status)
		return err
	})
	run("rebuild", func(i int) error {
		_, err := readstatus.RebuildUserBookState(store, user, survivor, i%2 == 0)
		return err
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		buf := make([]byte, 1<<22)
		n := runtime.Stack(buf, true)
		t.Fatalf("listening-state writers deadlocked; goroutines:\n%s", buf[:n])
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	t.Logf("combines=%d undos=%d journaled=%d discards=%d soft errors=%d",
		combines.Load(), undos.Load(), journaled.Load(), discards.Load(), softErrs.Load())
}
