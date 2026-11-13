// file: core/integrations/notify/notifytest/fake.go
// version: 1.0.0
// guid: 4e82c6b1-7a39-4f05-b2d8-61c9f0e3a755
// last-edited: 2026-11-13

// Package notifytest is the conformance kit and the fakes of the notify kind
// (05-notify section 9): RuleStoreSuite runs every case a notify.RuleStore
// must pass, and FakeNotifier is a channel that records what it is sent and
// fails on script.
//
// It is test support (03 R4, MOD5-6): it may import any core package and no
// core non-test package imports it. The notify package's own tests use it in
// place of coretest, which imports the kernel notify is part of (the W27 rule);
// coretest re-exports FakeNotifier for everyone else.
package notifytest

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/notify"
)

// Call is one Send a FakeNotifier received.
type Call struct {
	Notification notify.Notification
	Target       notify.Target
	At           time.Time
}

// FakeNotifier is a notify channel for tests. Its zero value accepts every
// notification. Safe for concurrent use.
type FakeNotifier struct {
	// Script, when set, decides each Send: it gets the 1-based number of the
	// call (counting every call to this FakeNotifier) and the notification,
	// and its error is Send's. nil is success. It runs without the lock held.
	Script func(call int, n notify.Notification) error
	// SendFn, when set, replaces the whole of Send after the call is recorded
	// (a test that must block, or inspect the context). Script is not
	// consulted then.
	SendFn func(ctx context.Context, n notify.Notification, t notify.Target) error

	mu    sync.Mutex
	calls []Call
	built atomic.Int64
	now   func() time.Time
}

// Calls returns a copy of every recorded call, in order.
func (f *FakeNotifier) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Sent is the number of calls recorded.
func (f *FakeNotifier) Sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// IDs lists the notification ID of each call, in order.
func (f *FakeNotifier) IDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Notification.ID
	}
	return out
}

// Built is how many instances the Descriptor has built.
func (f *FakeNotifier) Built() int64 { return f.built.Load() }

// SetClock sets the time recorded in Call.At; the default is time.Now.
func (f *FakeNotifier) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// Descriptor is the adapter that builds instances sharing this FakeNotifier:
// every instance of the adapter records into the one call list.
func (f *FakeNotifier) Descriptor(m integrations.Meta) integrations.Descriptor {
	return notify.Adapter(m, func(_ context.Context, env integrations.Env, _ integrations.InstanceConfig) (notify.Notifier, error) {
		f.built.Add(1)
		return &instance{f: f, ref: env.Ref}, nil
	})
}

// Instance returns a Notifier over f that is not built by a Registry, for a
// test that calls a channel directly.
func (f *FakeNotifier) Instance(ref integrations.Ref) notify.Notifier {
	return &instance{f: f, ref: ref}
}

type instance struct {
	f   *FakeNotifier
	ref integrations.Ref
}

func (*instance) Caps() notify.Caps { return notify.Caps{} }
func (*instance) Close() error      { return nil }
func (*instance) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}

func (i *instance) Send(ctx context.Context, n notify.Notification, t notify.Target) error {
	f := i.f
	f.mu.Lock()
	at := time.Now()
	if f.now != nil {
		at = f.now()
	}
	f.calls = append(f.calls, Call{Notification: n, Target: t, At: at})
	call := len(f.calls)
	script, sendFn := f.Script, f.SendFn
	f.mu.Unlock()
	switch {
	case sendFn != nil:
		return sendFn(ctx, n, t)
	case script != nil:
		return script(call, n)
	}
	return nil
}

// Fail is an *integrations.Error of class, as an adapter returns it, with the
// remote status and a Retry-After (zero for none). For FakeNotifier.Script.
func Fail(ref integrations.Ref, class integrations.Class, status int, retryAfter time.Duration) error {
	return &integrations.Error{Ref: ref, Op: "send", Class: class, Status: status, RetryAfter: retryAfter}
}
