// file: internal/operations/registry/liveness_internal_test.go
// version: 1.0.0
// guid: 5e70b3c8-91a4-42d6-8f13-c07e5a9bd248
// last-edited: 2026-08-16

package registry

import (
	"testing"
	"time"
)

// TestShouldWarnNeverReported pins the policy for flagging an op that finished
// successfully without ever reporting.
//
// The watchdog cannot catch this case: the op completed inside its
// ProgressTimeout, so nothing was ever struck. That is exactly how library.scan
// stayed broken from 2026-05-11 -- it finished quietly while the library was
// small enough to scan in under five minutes, and only surfaced once it grew
// past that. The warning has to fire on success or it does not fire in time.
func TestShouldWarnNeverReported(t *testing.T) {
	cases := []struct {
		name         string
		duration     time.Duration
		everReported bool
		want         bool
	}{
		{
			name:     "long run that never reported is the defect we are hunting",
			duration: 5 * time.Minute,
			want:     true,
		},
		{
			name:     "exactly at the grace boundary warns",
			duration: neverReportedGrace,
			want:     true,
		},
		{
			// Most ops are sub-second and have nothing worth reporting. Warning
			// on those would bury the real signal on the first day.
			name:     "short run that never reported is ordinary brevity",
			duration: time.Second,
			want:     false,
		},
		{
			name:         "long run that reported is healthy, however long it took",
			duration:     4 * time.Hour,
			everReported: true,
			want:         false,
		},
		{
			name:         "short run that reported is healthy",
			duration:     time.Second,
			everReported: true,
			want:         false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWarnNeverReported(tc.duration, tc.everReported); got != tc.want {
				t.Errorf("shouldWarnNeverReported(%s, everReported=%v) = %v, want %v",
					tc.duration, tc.everReported, got, tc.want)
			}
		})
	}
}
