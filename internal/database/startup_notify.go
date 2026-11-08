// file: internal/database/startup_notify.go
// version: 2.0.0
// guid: 439e418b-9ebb-4fdc-b44f-92b10ad28a37
// last-edited: 2026-11-08

package database

import "sync/atomic"

// startExtender is the func serve injected with SetStartExtender; nil until
// then, so every other command, child mode and every test runs without one.
var startExtender atomic.Pointer[func(step string)]

// SetStartExtender injects the function the store's long startup steps call
// to ask the service manager for more start time: serve passes
// app.ExtendStart, which sends EXTEND_TIMEOUT_USEC over NOTIFY_SOCKET (a no-op
// without it, never an error). The store opens before app.New builds the App
// (R1-23 moves the open inside it), so the stateless app.ExtendStart is the
// only handle that exists yet. A nil f removes the extender. It is safe to
// call while a step runs; call it before opening the store.
func SetStartExtender(f func(step string)) {
	if f == nil {
		startExtender.Store(nil)
		return
	}
	startExtender.Store(&f)
}

// extendStartTimeout asks the service manager, through the injected extender,
// for more start time. It is called right before a startup step that does
// real work and can legitimately take long: opening the store (WAL replay)
// and applying each pending migration. Under TimeoutStartSec alone a long
// migration run would be killed part-way. It is called per step, never from a
// timer, so a step that hangs is still killed once its own budget runs out.
// It does nothing when no extender was injected.
func extendStartTimeout(step string) {
	if f := startExtender.Load(); f != nil {
		(*f)(step)
	}
}
