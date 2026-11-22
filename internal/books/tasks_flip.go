// file: internal/books/tasks_flip.go
// version: 0.1.0
// guid: d1c0d49d-2451-48b9-8aa2-497d4bc34467
// last-edited: 2026-11-27

package books

import "sync/atomic"

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
