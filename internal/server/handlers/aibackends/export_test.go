// file: internal/server/handlers/aibackends/export_test.go
// version: 1.0.0
// guid: 8c4e1b7d-2a95-4d03-b6f8-3e7a9c1d5b42
// last-edited: 2026-10-18

package aibackendshandler

import "github.com/falkcorp/audiobook-organizer/internal/aidispatch"

// WithAttribution makes EndpointsStatus report a from a instead of the
// process-wide ledger, so a test reads a ledger it owns. Production wiring
// leaves the default, so the option lives in a _test file and is not compiled
// into the server binary.
func WithAttribution(a *aidispatch.Attribution) Option {
	return func(h *Handler) {
		if a != nil {
			h.attribution = a
		}
	}
}
