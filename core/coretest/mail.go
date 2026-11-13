// file: core/coretest/mail.go
// version: 1.1.0
// guid: f9c4dc21-8e64-488e-9040-21beec4f10f5
// last-edited: 2026-11-13

package coretest

import (
	"context"
	"sync"

	"github.com/falkcorp/mediacore/integrations/notify/mail"
)

// MailRecorder is a mail.Sender that keeps every message it is asked to send and
// returns Err (when set) instead of recording.
type MailRecorder struct {
	mu   sync.Mutex
	sent []mail.Message
	// Err, when non-nil, is returned by Send and nothing is recorded.
	Err error
}

// Send records msg, or returns r.Err.
func (r *MailRecorder) Send(_ context.Context, msg mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.sent = append(r.sent, msg)
	return nil
}

// Sent returns a copy of the recorded messages.
func (r *MailRecorder) Sent() []mail.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mail.Message(nil), r.sent...)
}

// SetErr changes the error Send returns.
func (r *MailRecorder) SetErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Err = err
}
