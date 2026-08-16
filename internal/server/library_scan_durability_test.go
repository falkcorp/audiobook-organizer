// file: internal/server/library_scan_durability_test.go
// version: 1.0.0
// guid: 8b2f47c1-0d59-4e6a-9c31-7fa4e2b85d60
// last-edited: 2026-08-16

package server

import (
	"testing"
	"time"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/require"
)

// TestLibraryScanCanOutliveItsOwnRuntime pins the two properties that together
// decided whether a full library scan could EVER finish. Both were wrong, and the
// combination is why `library.scan` had never once reached `completed`.
//
// Measured against production on 2026-08-16:
//
//   - 9 library.scan runs in 30 days, ZERO completed. 8 ended
//     `interrupted_dropped` — not crashes, but ResumeDrop discarding them by
//     policy on restart. The 9th was still running.
//   - That 9th run: 63,044 books at ~208 books/min = ~5h of work, against a 4h
//     Timeout. It was killed at 41% having been structurally guaranteed to die
//     ~57 minutes short from the moment it started.
//
// Neither number is arbitrary, so neither assertion hard-codes the current value:
// the test asserts the SHAPE (a scan may outlive a restart; the ceiling exceeds
// the measured runtime with margin) so tuning stays free and regressing does not.
func TestLibraryScanCanOutliveItsOwnRuntime(t *testing.T) {
	reg := w3decodeReg(t)
	require.NoError(t, (*Server).RegisterLibraryScanOp(&Server{}, reg))

	def, ok := reg.Def("library.scan")
	require.True(t, ok, "library.scan must be registered")

	require.Equal(t, opsregistry.ResumeRestart, def.ResumePolicy,
		"a restart must re-queue the scan, not discard it: under ResumeDrop, 8 of "+
			"the 9 scans in 30 days ended interrupted_dropped and none ever completed")

	// The measured full-library runtime was ~5h. Anything at or below that is a
	// guillotine rather than a safety margin, and a timeout kill is indistinguishable
	// from a hang. Genuine stalls are the liveness check-in's job, not this one's.
	const measuredFullScan = 5 * time.Hour
	require.Greater(t, def.Timeout, 2*measuredFullScan,
		"the wall-clock ceiling must clear the measured %s full-scan runtime with "+
			"real margin; 4h killed a healthy scan at 41%%", measuredFullScan)
}

// TestLibraryScanStillHasAStallDetector is the counterweight to the test above.
//
// Raising the timeout to 24h only stops being reckless if something ELSE still
// notices a scan that has genuinely wedged. That mechanism is the liveness
// check-in — a scan killed for "no progress for 5m13s" while it was actively
// logging is what made the manual mode necessary in the first place. If liveness
// were ever weakened to LivenessNone, the loose ceiling would silently become the
// only backstop and a hung scan would sit there for a day.
func TestLibraryScanStillHasAStallDetector(t *testing.T) {
	reg := w3decodeReg(t)
	require.NoError(t, (*Server).RegisterLibraryScanOp(&Server{}, reg))

	def, ok := reg.Def("library.scan")
	require.True(t, ok)

	require.NotEqual(t, opsregistry.LivenessNone, def.Liveness,
		"with a 24h ceiling, liveness is the only thing that catches a wedged scan")
	require.True(t, def.Cancellable,
		"a scan that may now run for up to 24h must remain cancellable by hand")
}
