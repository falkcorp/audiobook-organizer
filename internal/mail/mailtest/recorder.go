// file: internal/mail/mailtest/recorder.go
// version: 1.0.0
// guid: f9c4dc21-8e64-488e-9040-21beec4f10f5
// last-edited: 2026-10-19

// Package mailtest provides a recording mail.Sender for tests.
package mailtest

import (
	"context"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/mail"
)

// Recorder is a mail.Sender that keeps every message it is asked to send and
// returns Err (when set) instead of recording.
type Recorder struct {
	mu   sync.Mutex
	sent []mail.Message
	// Err, when non-nil, is returned by Send and nothing is recorded.
	Err error
}

// Send records msg, or returns r.Err.
func (r *Recorder) Send(_ context.Context, msg mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.sent = append(r.sent, msg)
	return nil
}

// Sent returns a copy of the recorded messages.
func (r *Recorder) Sent() []mail.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mail.Message(nil), r.sent...)
}

// SetErr changes the error Send returns.
func (r *Recorder) SetErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Err = err
}
