// file: internal/server/handlers/reading_user_state_lock_test.go
// version: 1.0.0
// guid: 1b1689c7-8d0a-41db-af90-d643d4648337
// last-edited: 2026-10-18

package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The heartbeat with no authenticated user writes as "_local".
const heartbeatUser = "_local"

func newHeartbeatStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// postHeartbeat sends one POST /audiobooks/b1/position and returns the code.
func postHeartbeat(h *ReadingHandler, segment string, seconds string) int {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/b1/position",
		bytes.NewBufferString(`{"segment_id":"`+segment+`","position_seconds":`+seconds+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "b1"}}
	h.SetPosition(c)
	return rec.Code
}

// The heartbeat waits for the per-(user, book) user-state stripe.
func TestReadingHeartbeat_HoldsUserStateLock(t *testing.T) {
	h := NewReadingHandler(newHeartbeatStore(t))
	unlock := database.LockUserBookState(heartbeatUser, "b1")
	done := make(chan int, 1)
	go func() { done <- postHeartbeat(h, "s1", "120") }()
	select {
	case code := <-done:
		unlock()
		t.Fatalf("heartbeat finished (%d) while the user-state lock was held", code)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("heartbeat = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat never finished after the lock was released")
	}
}

// A locked read-decide-write that replaces the book's whole position set from
// what it read (the shape of the merge undo's put-back) is not undone by a
// heartbeat fired in the middle of it: the heartbeat waits for the stripe and
// lands on top. Before 2026-10-18 the heartbeat took no stripe, wrote at
// once, and the replace erased its position -- listening progress removed.
func TestReadingHeartbeat_UserStateRaceNotErasedByLockedReplace(t *testing.T) {
	store := newHeartbeatStore(t)
	if err := store.SetUserPosition(heartbeatUser, "b1", "s1", 100); err != nil {
		t.Fatal(err)
	}
	h := NewReadingHandler(store)

	read := make(chan struct{})
	replaced := make(chan error, 1)
	go func() {
		unlock := database.LockUserBookState(heartbeatUser, "b1")
		defer unlock()
		have, err := store.ListUserPositionsForBook(heartbeatUser, "b1")
		if err != nil {
			replaced <- err
			return
		}
		close(read)
		time.Sleep(200 * time.Millisecond) // the heartbeat fires here
		replaced <- store.ReplaceUserPositions(heartbeatUser, "b1", have)
	}()
	<-read
	if code := postHeartbeat(h, "s2", "500"); code != http.StatusOK {
		t.Fatalf("heartbeat = %d", code)
	}
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	got, err := store.ListUserPositionsForBook(heartbeatUser, "b1")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.SegmentID == "s2" && p.PositionSeconds == 500 {
			return
		}
	}
	t.Fatalf("the heartbeat's s2=500 position was erased by the replace: %+v", got)
}

// hookedReadingStore runs onRead once, after the first GetUserBookState has
// read the row: inside the heartbeat's recompute, between its read and its
// write.
type hookedReadingStore struct {
	ReadingStore
	once   sync.Once
	onRead func()
}

func (s *hookedReadingStore) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	st, err := s.ReadingStore.GetUserBookState(userID, bookID)
	if s.onRead != nil {
		s.once.Do(s.onRead)
	}
	return st, err
}

// A locked write landing between the heartbeat recompute's read of the state
// and its write is kept: the client waits for the heartbeat's stripe and
// writes on top. Unlocked, the recompute wrote back the copy it read and
// erased the client's hide flag.
func TestReadingHeartbeat_UserStateRaceKeepsLockedWrite(t *testing.T) {
	store := newHeartbeatStore(t)
	if code := postHeartbeat(NewReadingHandler(store), "s1", "100"); code != http.StatusOK {
		t.Fatalf("seed heartbeat = %d", code)
	}
	var wait func() error
	hooked := &hookedReadingStore{ReadingStore: store}
	hooked.onRead = func() {
		done := make(chan error, 1)
		go func() {
			defer database.LockUserBookState(heartbeatUser, "b1")()
			cur, err := store.GetUserBookState(heartbeatUser, "b1")
			if err != nil || cur == nil {
				done <- err
				return
			}
			next := *cur
			next.HideFromContinueListening = true
			done <- store.SetUserBookState(&next)
		}()
		var landedErr error
		landed := false
		select {
		case landedErr = <-done:
			landed = true
		case <-time.After(200 * time.Millisecond):
		}
		wait = func() error {
			if landed {
				return landedErr
			}
			return <-done
		}
	}
	if code := postHeartbeat(NewReadingHandler(hooked), "s1", "200"); code != http.StatusOK {
		t.Fatalf("heartbeat = %d", code)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	st, err := store.GetUserBookState(heartbeatUser, "b1")
	if err != nil || st == nil {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if !st.HideFromContinueListening {
		t.Fatal("the client's hide flag, written between the heartbeat recompute's read and write, was lost")
	}
}
