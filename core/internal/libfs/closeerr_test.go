// file: core/internal/libfs/closeerr_test.go
// version: 1.0.0
// guid: f1fff6e6-b190-4632-aed2-b6dd13366393
// last-edited: 2026-11-11

package libfs

import (
	"errors"
	"io/fs"
	"testing"
)

type failingCloser struct{ err error }

func (f failingCloser) Close() error { return f.err }

func TestJoinErrKeepsTheFirstErrorItselfWhenTheSecondIsNil(t *testing.T) {
	first := &fs.PathError{Op: "open", Path: "Batcave", Err: fs.ErrNotExist}
	if got := joinErr(first, nil); got != error(first) {
		t.Errorf("joinErr(first, nil) = %#v, want the first error itself", got)
	}
	second := errors.New("the winch jammed")
	if got := joinErr(nil, second); got != second {
		t.Errorf("joinErr(nil, second) = %v", got)
	}
	both := joinErr(first, second)
	if !errors.Is(both, fs.ErrNotExist) || !errors.Is(both, second) {
		t.Errorf("joinErr(first, second) = %v, want both reachable", both)
	}
	if joinErr(nil, nil) != nil {
		t.Error("joinErr(nil, nil) is not nil")
	}
}

func TestCloseAfterKeepsTheFailureAndTheCloseError(t *testing.T) {
	cause := &fs.PathError{Op: "stat", Path: "Batcave", Err: fs.ErrPermission}
	if got := CloseAfter(failingCloser{}, cause); got != error(cause) {
		t.Errorf("a clean close changed the error: %#v", got)
	}
	jam := errors.New("EIO")
	got := CloseAfter(failingCloser{err: jam}, cause)
	if !errors.Is(got, fs.ErrPermission) || !errors.Is(got, jam) {
		t.Errorf("CloseAfter = %v, want the cause and the close error", got)
	}
	if got := CloseAfter(failingCloser{err: jam}, nil); !errors.Is(got, jam) {
		t.Errorf("a close error with no earlier failure = %v, want the close error", got)
	}
}

func TestRollbackFailedNamesTheUndoThatDidNotWork(t *testing.T) {
	if rollbackFailed("remove", "Batcave/tmp", nil) != nil {
		t.Error("a rollback that worked produced an error")
	}
	err := rollbackFailed("remove", "Batcave/tmp", fs.ErrPermission)
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "remove" || pe.Path != "Batcave/tmp" || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("rollbackFailed = %#v", err)
	}
}
