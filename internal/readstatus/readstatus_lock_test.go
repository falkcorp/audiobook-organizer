// file: internal/readstatus/readstatus_lock_test.go
// version: 1.1.0
// guid: 08b94ace-eddd-4afc-96fb-fe716d31a803
// last-edited: 2026-10-18

package readstatus

import (
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SetManualStatus waits for the per-(user, book) user-state stripe.
func TestSetManualStatus_HoldsUserStateLock(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	unlock := database.LockUserBookState("u1", "b1")
	done := make(chan error, 1)
	go func() {
		_, err := SetManualStatus(st, "u1", "b1", database.UserBookStatusAbandoned)
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("SetManualStatus ran while the lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetManualStatus never ran after the lock was released")
	}
	s, err := st.GetUserBookState("u1", "b1")
	if err != nil || s == nil || s.Status != database.UserBookStatusAbandoned {
		t.Fatalf("state = %+v, %v", s, err)
	}
}

// stripedClientWrite runs write as a client that takes the (user, book)
// user-state stripe first, as every listening-state writer does, and waits up
// to 200ms for it to land. Started from inside a read of the writer under
// test: one that holds the stripe across that read keeps the client waiting
// until it is done, so the write lands on top; one that does not lets it land
// at once, between its read and its write, where its write erases it. The
// returned wait joins the client and returns its error.
func stripedClientWrite(user, book string, write func() error) (wait func() error) {
	done := make(chan error, 1)
	go func() {
		unlock := database.LockUserBookState(user, book)
		defer unlock()
		done <- write()
	}()
	var err error
	landed := false
	select {
	case err = <-done:
		landed = true
	case <-time.After(200 * time.Millisecond):
	}
	return func() error {
		if !landed {
			err = <-done
		}
		return err
	}
}

// hookedStateStore runs onRead once, after the first GetUserBookState of
// (user, book) has read the row -- between the writer's read and its write.
type hookedStateStore struct {
	database.Store
	once   sync.Once
	onRead func()
}

func (s *hookedStateStore) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	st, err := s.Store.GetUserBookState(userID, bookID)
	if s.onRead != nil {
		s.once.Do(s.onRead)
	}
	return st, err
}

// hideWrite is a locked read-decide-write that sets the hide flag, as the
// ABS "remove from continue listening" path does.
func hideWrite(store database.Store, user, book string) func() error {
	return func() error {
		cur, err := store.GetUserBookState(user, book)
		if err != nil {
			return err
		}
		next := database.UserBookState{UserID: user, BookID: book}
		if cur != nil {
			next = *cur
		}
		next.HideFromContinueListening = true
		return store.SetUserBookState(&next)
	}
}

// RecomputeUserBookState waits for the user-state stripe.
func TestRecomputeUserBookState_HoldsUserStateLock(t *testing.T) {
	store := setupReadTestStore(t)
	if err := store.SetUserPosition("u1", "b1", "s1", 300); err != nil {
		t.Fatal(err)
	}
	unlock := database.LockUserBookState("u1", "b1")
	done := make(chan error, 1)
	go func() {
		_, err := RecomputeUserBookState(store, "u1", "b1")
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("RecomputeUserBookState ran while the lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RecomputeUserBookState never ran after the lock was released")
	}
}

// A locked read-decide-write landing between the recompute's read of the
// state and its write is not lost: the recompute holds the stripe, so the
// client waits and writes on top. Before 2026-10-18 the recompute took no
// stripe and wrote back the copy it read, erasing the client's hide flag.
func TestRecomputeUserBookState_UserStateRaceKeepsLockedWrite(t *testing.T) {
	base := setupReadTestStore(t)
	if err := base.SetUserPosition("u1", "b1", "s1", 300); err != nil {
		t.Fatal(err)
	}
	if _, err := RecomputeUserBookState(base, "u1", "b1"); err != nil {
		t.Fatal(err)
	}
	var wait func() error
	hooked := &hookedStateStore{Store: base}
	hooked.onRead = func() { wait = stripedClientWrite("u1", "b1", hideWrite(base, "u1", "b1")) }
	if _, err := RecomputeUserBookState(hooked, "u1", "b1"); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	st, err := base.GetUserBookState("u1", "b1")
	if err != nil || st == nil {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if !st.HideFromContinueListening {
		t.Fatal("the client's hide flag, written between the recompute's read and write, was lost")
	}
	if st.ProgressPct != 16 {
		t.Fatalf("ProgressPct = %d, want 16 (the recompute's own write)", st.ProgressPct)
	}
}

// RecomputeUserBookStateLocked takes no stripe: a caller holding it (the web
// heartbeat, the iTunes seed) does not self-deadlock.
func TestRecomputeUserBookStateLocked_UnderCallersLock(t *testing.T) {
	store := setupReadTestStore(t)
	done := make(chan error, 1)
	go func() {
		defer database.LockUserBookState("u1", "b1")()
		if err := store.SetUserPosition("u1", "b1", "s1", 300); err != nil {
			done <- err
			return
		}
		_, err := RecomputeUserBookStateLocked(store, "u1", "b1")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RecomputeUserBookStateLocked deadlocked under its caller's stripe")
	}
}

// RebuildUserBookState waits for the user-state stripe.
func TestRebuildUserBookState_HoldsUserStateLock(t *testing.T) {
	store := setupReadTestStore(t)
	unlock := database.LockUserBookState("u1", "b1")
	done := make(chan error, 1)
	go func() {
		_, err := RebuildUserBookState(store, "u1", "b1", true)
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("RebuildUserBookState ran while the lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RebuildUserBookState never ran after the lock was released")
	}
}
