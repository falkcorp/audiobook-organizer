// file: internal/versionprimary/lock_groups_test.go
// version: 1.0.0
// guid: 9d4a2c71-5e8f-4b36-a1d0-7f3e6b2c9a48
// last-edited: 2026-10-02

package versionprimary

import (
	"fmt"
	"testing"
	"time"
)

// LockGroups takes a stripe once even when several groups hash onto it (a
// second Lock on the same mutex would self-deadlock), skips "", and the
// release frees every stripe it took.
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
	for _, g := range []string{a, b} {
		unlock := LockGroup(g) // would block forever if a stripe leaked
		unlock()
	}
}
