// file: internal/versionprimary/lock_groups_test.go
// version: 1.2.0
// guid: 9d4a2c71-5e8f-4b36-a1d0-7f3e6b2c9a48
// last-edited: 2026-10-02

package versionprimary

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// LockGroups takes a stripe once even when several groups hash onto it (a
// second Lock on the same mutex would self-deadlock), takes the no-group
// sentinel for "", and the release frees every stripe it took.
func TestLockGroups_SharedStripeAndRelease(t *testing.T) {
	a := "group-a"
	b := ""
	for i := 0; i < 10000 && b == ""; i++ {
		if c := fmt.Sprintf("group-%d", i); c != a && groupStripe(c) == groupStripe(a) {
			b = c
		}
	}
	if b == "" {
		t.Fatal("no second group on group-a's stripe")
	}
	done := make(chan struct{})
	go func() {
		unlock := LockGroups(a, b, "", a)
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LockGroups deadlocked on two groups sharing a stripe")
	}
	for _, g := range []string{a, b, ""} {
		unlock := LockGroup(g) // would block forever if a stripe leaked
		unlock()
	}
}

// seqReader returns, per call, the next group from seq for every book (the
// last one repeats), so a test can make a book move between LockBookGroups'
// read and its locked re-read.
type seqReader struct {
	seq   []string
	calls int
}

func (r *seqReader) GetBookByID(id string) (*database.Book, error) {
	i := r.calls
	if i >= len(r.seq) {
		i = len(r.seq) - 1
	}
	r.calls++
	g := r.seq[i]
	return &database.Book{ID: id, VersionGroupID: &g}, nil
}

// A book that moved between the read and the locked re-read makes
// LockBookGroups drop the locks and read again; once two reads agree it
// returns the agreed group, as stored (padding kept), with its stripe held.
func TestLockBookGroups_RereadsAfterAMoveAndKeepsTheRawID(t *testing.T) {
	r := &seqReader{seq: []string{"g-old", "g-new ", "g-new "}}
	unlock, groups, err := LockBookGroups(r, []string{"b1"})
	if err != nil {
		t.Fatalf("LockBookGroups: %v", err)
	}
	if got := groups["b1"]; got != "g-new " {
		t.Fatalf("group = %q, want the raw stored %q", got, "g-new ")
	}
	if groupLocks[groupStripe("g-new")].TryLock() {
		groupLocks[groupStripe("g-new")].Unlock()
		unlock()
		t.Fatal("the agreed group's stripe is not held")
	}
	unlock()
	if !groupLocks[groupStripe("g-new")].TryLock() {
		t.Fatal("release left the stripe locked")
	}
	groupLocks[groupStripe("g-new")].Unlock()
}

// A book that never holds still exhausts the retries: ErrMembershipChanged,
// and no stripe is left locked.
func TestLockBookGroups_GivesUpWithErrMembershipChanged(t *testing.T) {
	seq := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		seq = append(seq, fmt.Sprintf("g-%d", i))
	}
	r := &seqReader{seq: seq}
	unlock, _, err := LockBookGroups(r, []string{"b1"})
	if !errors.Is(err, ErrMembershipChanged) {
		t.Fatalf("err = %v, want ErrMembershipChanged", err)
	}
	if unlock != nil {
		t.Fatal("a failed LockBookGroups returned a release")
	}
	for _, g := range seq[:10] {
		if !groupLocks[groupStripe(g)].TryLock() {
			t.Fatalf("stripe of %s left locked", g)
		}
		groupLocks[groupStripe(g)].Unlock()
	}
}
