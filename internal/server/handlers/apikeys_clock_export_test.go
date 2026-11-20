// file: internal/server/handlers/apikeys_clock_export_test.go
// version: 0.1.0
// guid: d3f74379-b8a7-481a-bbc3-5d45cc6d5410
// last-edited: 2026-11-20

package handlers

import "time"

// SetAPIKeyClockForTest pins the clock h counts a new key's expiry from.
func SetAPIKeyClockForTest(h *APIKeyHandler, now func() time.Time) {
	h.now = now
}
