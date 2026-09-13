// file: internal/database/partial_read.go
// version: 1.0.0
// guid: 5b7e2c91-3f4d-4a8e-9c61-d02f7a3e8b54
// last-edited: 2026-09-12

package database

import (
	"errors"
	"fmt"
	"strings"
)

// UnreadableMembersError is returned by a list read that walked its index to
// the end but could not read some members. Unlike a failed read, the call ALSO
// returns every member it could read: a caller that must act on the complete
// set treats it like any other error (err != nil), while a caller that can use
// a partial result — a listing endpoint, a revoke-everything sweep — can
// errors.As it, use the rows, and surface Count().
//
// It is never returned for an index-scan error: a truncated scan does not know
// how many members it missed, so that stays a plain error with nil rows.
type UnreadableMembersError struct {
	Op   string   // the store method, e.g. "ListAPIKeysForUser u1"
	IDs  []string // member ids that could not be read, in scan order
	Errs []error  // the per-member errors, parallel to IDs
}

func (e *UnreadableMembersError) add(id string, err error) {
	e.IDs = append(e.IDs, id)
	e.Errs = append(e.Errs, err)
}

// orNil returns e as an error when it holds at least one member, else nil, so
// a list can end in `return out, unreadable.orNil()`.
func (e *UnreadableMembersError) orNil() error {
	if e == nil || len(e.IDs) == 0 {
		return nil
	}
	return e
}

// Count is the number of members that could not be read.
func (e *UnreadableMembersError) Count() int { return len(e.IDs) }

func (e *UnreadableMembersError) Error() string {
	msgs := make([]string, len(e.Errs))
	for i, err := range e.Errs {
		msgs[i] = err.Error()
	}
	return fmt.Sprintf("%s: %d unreadable member(s): %s", e.Op, len(e.IDs), strings.Join(msgs, "; "))
}

func (e *UnreadableMembersError) Unwrap() []error { return e.Errs }

// UnreadableMemberCount reports how many members err says could not be read,
// and false when err carries no *UnreadableMembersError (nil, or a hard
// failure such as a scan error, whose rows must not be used).
func UnreadableMemberCount(err error) (int, bool) {
	var u *UnreadableMembersError
	if errors.As(err, &u) {
		return u.Count(), true
	}
	return 0, false
}
