// file: internal/readstatus/readstatus_lock_test.go
// version: 1.0.0
// guid: 08b94ace-eddd-4afc-96fb-fe716d31a803
// last-edited: 2026-10-05

package readstatus

import (
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
