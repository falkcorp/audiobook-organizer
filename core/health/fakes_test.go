// file: core/health/fakes_test.go
// version: 1.0.0
// guid: c2e5a8f1-6b93-4d07-8a4e-5d1f9b3c7e62
// last-edited: 2026-11-13

package health

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// fakeNotifier records what the Supervisor sends, in order.
type fakeNotifier struct {
	active   bool
	watchdog time.Duration
	failNext atomic.Bool

	mu     sync.Mutex
	events []string
	extend []time.Duration
	pings  atomic.Int32
}

func (f *fakeNotifier) record(e string) error {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
	if f.failNext.Load() {
		return errors.New("notify socket refused")
	}
	return nil
}

func (f *fakeNotifier) Ready() error          { return f.record("READY") }
func (f *fakeNotifier) Status(m string) error { return f.record("STATUS=" + m) }
func (f *fakeNotifier) Stopping() error       { return f.record("STOPPING") }
func (f *fakeNotifier) Extend(d time.Duration) error {
	f.mu.Lock()
	f.extend = append(f.extend, d)
	f.mu.Unlock()
	return f.record("EXTEND")
}
func (f *fakeNotifier) Watchdog() error {
	f.pings.Add(1)
	return f.record("WATCHDOG")
}
func (f *fakeNotifier) WatchdogInterval() time.Duration { return f.watchdog }
func (f *fakeNotifier) Active() bool                    { return f.active }

func (f *fakeNotifier) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeNotifier) extensions() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.extend...)
}
