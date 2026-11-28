// file: internal/books/tasks_flip.go
// version: 0.1.0
// guid: d1c0d49d-2451-48b9-8aa2-497d4bc34467
// last-edited: 2026-11-27

package books

import (
	"sync/atomic"

	"github.com/falkcorp/mediacore/outbox"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
)

// ABO's tasks flip (F5-18) is ONE decision, tasksFlipOn, and everything the flip
// changes in this package reads it: the manifest (books.level-3, M4 and M5 in
// the cutover list, the retired ops-family and syslog: claims), the retirement
// of HEAD's ops recorder (Register), and from commit 2 on the kernel's tasks
// switch (app.Options.TasksBoot) and books' task registrations. They are tied to one predicate for the reason the identity
// flip's are (identity_flip.go): core's executor without M4 runs over an empty
// run table and HEAD's history is lost to it; M4 without core's executor
// retires HEAD's run rows while HEAD's engine still writes them.
//
// The tasks flip is built over the identity flip (cutover.NewFlip refuses Tasks
// without Identity). Identity has been on for good since F4-08 B10g, so the tasks
// flip can no longer ride its switch and has its own.
//
// In production the predicate is OFF until the F5-18 switch commit, which turns
// it on for good by editing tasksFlipOn and deletes the selector below. Until
// then the only way to turn it on is the test-only selector, and an archtest
// (internal/arch/tasks_flip_guard_test.go) pins that nothing outside a test
// calls it and nothing outside this file touches the variable.

// tasksFlip is the predicate's backing state. It is never written outside
// SetTasksFlipForTest (internal/arch/tasks_flip_guard_test.go).
var tasksFlip atomic.Bool

// tasksFlipOn reports whether this build runs ABO's tasks on core's executor. A
// runtime read, not a constant: every consumer reaches it through this one
// function, so the selector below moves them all together and none can keep a
// private copy.
func tasksFlipOn() bool { return tasksFlip.Load() }

// SetTasksFlipForTest turns the tasks predicate on or off and returns the
// function that puts the previous value back. It is for tests of the flipped
// wiring, which run the real Module on core's executor before the switch commit
// turns the flip on. The value is process-global: a test that sets it must not
// run in parallel with one that reads the manifest, Options or Register, and
// restores it with t.Cleanup(restore).
func SetTasksFlipForTest(on bool) (restore func()) {
	prev := tasksFlip.Swap(on)
	return func() { tasksFlip.Store(prev) }
}

// relocationFollowUpOn reports whether books runs the durable relocation
// follow-up (F6-03): the outbox consumer that starts books.deluge.path-update
// and the staging that feeds it. The consumer starts a task run, so it exists
// only where that kind is registered, which is the tasks flip; the identity flip
// supplies the outbox and the catalog topic. One predicate gates the manifest's
// consumer name, the consumer's registration and the store's staging, so none
// can be wired without the others (a message staged for no consumer waits for
// one, and a consumer without its kind dead-letters every message).
func relocationFollowUpOn() bool { return identityOn() && tasksFlipOn() }

// requireRelocationWiring is Register's boot check for the follow-up: with it
// wired (relocationFollowUpOn) the store must have the relocation outbox
// installed, or an organized copy would stage nothing and its torrents would
// never move (audiobooks.RequireRelocationOutbox). Unwired, nothing is asked.
func requireRelocationWiring(store any) error {
	if !relocationFollowUpOn() {
		return nil
	}
	return audiobooks.RequireRelocationOutbox(store)
}

// relocationOutboxSetter is the one method installRelocationOutbox calls on the
// store (*database.PebbleStore).
type relocationOutboxSetter interface{ SetRelocationOutbox(outbox.Writer) }

// installRelocationOutbox is Register's one install of the relocation outbox:
// the organized copy of a single-book organize (CreateBookWithRelocation) stages
// books.book.relocated.v1 in the batch that creates the copy, and no other write
// stages it. It installs only while the consumer that delivers the message is
// registered (relocationFollowUpOn): a message staged for no consumer would wait
// for one, and the follow-up would not run.
//
// That makes "the store has a relocation outbox" the follow-up's predicate by
// construction, and the organize request's two Deluge arms read it as such: the
// request's direct NotifyDelugeAfterOrganize runs when this installed nothing
// (the tasks switch is off), and does not run when it did
// (organizer.Service.StagesRelocationFollowUp, which asks the store). It reports
// whether it installed.
func installRelocationOutbox(store relocationOutboxSetter, out outbox.Writer) bool {
	if !relocationFollowUpOn() || out == (outbox.Writer{}) {
		return false
	}
	store.SetRelocationOutbox(out)
	return true
}
