// file: internal/server/transcode_version_lock_test.go
// version: 1.0.0
// guid: 5d8c2b47-9a1e-4f36-8b70-e4a2c9f1d053
// last-edited: 2026-10-02

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// recordTranscodedVersion's group write waits for a concurrent hand-off on
// the original's group, and, for an ungrouped original, on the no-group
// sentinel it leaves.
func TestRecordTranscodedVersion_WaitsForGroupHolder(t *testing.T) {
	cases := []struct{ name, group, held string }{
		{"grouped original", "g", "g"},
		{"ungrouped original", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := vptest.New(t)
			orig := f.Book(t, vptest.Spec{ID: "orig", Group: tc.group, Primary: "true"})
			b, err := f.S.GetBookByID(orig)
			require.NoError(t, err)
			out := transcodeOutput(t, f.Root, "orig-out")
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(tc.held) },
				func() error {
					_, _, err := recordTranscodedVersion(context.Background(), f.S, b, out, 128, f.Root, noLog)
					return err
				},
				func() bool { return f.GroupOf(t, orig) == tc.group })
		})
	}
}
