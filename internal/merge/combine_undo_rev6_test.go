// file: internal/merge/combine_undo_rev6_test.go
// version: 1.0.0
// guid: 5e0dd46d-1455-44fc-94b7-899dc9c35055
// last-edited: 2026-10-18

package merge

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Round-6 review of #4064: the survivor put-back decides under the
// user-state stripe it writes under.

// stripedClientWrite runs write as a client that takes the (user, book)
// user-state stripe first, as the ABS handlers do. It is started from inside
// a store read of the restore under test and waits up to 200ms for the write
// to land. A restore that holds the stripe across that read keeps the client
// waiting until it is done, so the write lands on top of the restore; one
// that does not lets it land at once, between the read and the restore's
// write. The returned wait joins the client and returns its error; call it
// before asserting.
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
