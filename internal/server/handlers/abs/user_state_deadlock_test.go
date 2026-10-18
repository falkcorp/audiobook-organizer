// file: internal/server/handlers/abs/user_state_deadlock_test.go
// version: 1.0.0
// guid: 0630076b-d80b-4cf5-b741-2fcc6a0bf393
// last-edited: 2026-10-18

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
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// fixedUsersStore answers ListUsers with one user of a chosen id, so the
// merge paths fan out over the same user id the ABS harness writes as.
type fixedUsersStore struct {
	*database.PebbleStore
	userID string
}

func (s fixedUsersStore) ListUsers() ([]database.User, error) {
	return []database.User{{ID: s.userID}}, nil
}

// Every listening-state writer runs at once on the SAME (user, book) keys
// under -race, with a watchdog: ABS progress PATCHes, the web reading
// heartbeat, readstatus' recompute, the combine undo's put-back
// (merge.RestoreFollowedProgress under merge.LockMergeRMW, what UndoCombine
// runs) and the merge follow in both directions (LockUserBookStatePair under
// merge.LockMergeRMW). None may deadlock.
//
// The ABS harness writes through its fake library and the others through a
// Pebble store: the user-state stripes and the merge lock are process-wide
// and keyed by (user id, book id) strings, so the lock acquisition orders
// interleave exactly as in production; which store each writer then writes is
// not what a deadlock depends on.
func TestUserStateLock_ProgressAllWritersNoDeadlock(t *testing.T) {
	w := newWriteHarness(t)
	user, book := w.userID, w.bookID
	const other = "01USERSTATEDEADLOCKOTHER0"

	ps, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	for _, id := range []string{book, other} {
		if _, err := ps.CreateBook(&database.Book{ID: id, Title: id, FilePath: "/tmp/" + id, Format: "m4b"}); err != nil {
			t.Fatal(err)
		}
	}
	store := fixedUsersStore{PebbleStore: ps, userID: user}
	at := time.Now().Add(-time.Hour).UTC()
	if err := ps.SetUserPositionAt(user, other, "o", 50, at); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	reading := handlers.NewReadingHandler(ps)
	heartbeat := func(i int) error {
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
		AbsorbedPositions:   []database.UserPosition{{SegmentID: "o", PositionSeconds: 60, UpdatedAt: at}},
		SurvivorStateBefore: &database.UserBookState{UserID: user, BookID: book, Status: database.UserBookStatusInProgress, LastActivityAt: at},
	}}

	const rounds = 25
	var wg sync.WaitGroup
	errs := make(chan error, 8*rounds)
	run := func(name string, f func(i int) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := f(i); err != nil {
					errs <- fmt.Errorf("%s: %w", name, err)
					return
				}
			}
		}()
	}
	run("abs progress", func(i int) error {
		code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": float64(100 + i)})
		if code != http.StatusOK {
			return fmt.Errorf("PATCH = %d %s", code, raw)
		}
		return nil
	})
	run("heartbeat", heartbeat)
	run("recompute", func(int) error {
		_, err := readstatus.RecomputeUserBookState(ps, user, book)
		return err
	})
	run("combine undo put-back", func(int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		_, err := merge.RestoreFollowedProgress(store, book, other, false, restore)
		return err
	})
	run("follow other->book", func(int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		return merge.FollowMerge(store, nil, book, []string{other})
	})
	run("follow book->other", func(int) error {
		merge.LockMergeRMW()
		defer merge.UnlockMergeRMW()
		return merge.FollowMerge(store, nil, other, []string{book})
	})
	run("mark as", func(i int) error {
		status := database.UserBookStatusFinished
		if i%2 == 0 {
			status = ""
		}
		_, err := readstatus.SetManualStatus(ps, user, book, status)
		return err
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		buf := make([]byte, 1<<22)
		n := runtime.Stack(buf, true)
		t.Fatalf("listening-state writers deadlocked; goroutines:\n%s", buf[:n])
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
