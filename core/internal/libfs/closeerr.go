// file: core/internal/libfs/closeerr.go
// version: 1.0.0
// guid: b1ca024e-5dbb-4992-a22f-4c790410b48d
// last-edited: 2026-11-11

package libfs

import (
	"errors"
	"io"
	"os"
)

// joinErr is first with second attached. A nil second leaves first itself (its
// type and identity), a nil first yields second, so a cleanup that goes right
// costs the caller nothing it could notice.
func joinErr(first, second error) error {
	switch {
	case second == nil:
		return first
	case first == nil:
		return second
	}
	return errors.Join(first, second)
}

// CloseAfter closes c on the way out of a failure that is being returned and
// keeps both: err unchanged when the close is clean, err joined with the close
// failure when it is not. A descriptor that will not close is evidence about
// the disk the caller should not lose behind the error it already has.
func CloseAfter(c io.Closer, err error) error { return joinErr(err, c.Close()) }

// rollbackFailed describes an undo (a removal of a file just created) that did
// not work, or is nil when it did.
func rollbackFailed(op, name string, err error) error {
	if err == nil {
		return nil
	}
	return &os.PathError{Op: op, Path: name, Err: err}
}
