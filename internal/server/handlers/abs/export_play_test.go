// file: internal/server/handlers/abs/export_play_test.go
// version: 1.0.0
// guid: 5d2c8e4a-7b13-4f69-a0d5-9e1c3b7a6f28
// last-edited: 2026-10-18

package abs

// SessionTimeListening exposes a session's running listened total to the
// abs_test package. /sync answers a bare "OK" like real ABS, so the
// accumulation semantics of §1.8.4 are otherwise unobservable from outside.
// It lives in a _test file so it is not compiled into the server binary.
func (h *Handler) SessionTimeListening(sessionID string) float64 {
	s, ok := h.sessions.get(sessionID)
	if !ok {
		return 0
	}
	_, total, _ := s.snapshot()
	return total
}
