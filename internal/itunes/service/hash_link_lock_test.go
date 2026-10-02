// file: internal/itunes/service/hash_link_lock_test.go
// version: 1.0.0
// guid: 3a7d1e95-6c2f-4b48-9e03-b8f4a2c7d561
// last-edited: 2026-10-02

package itunesservice

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// The import's hash link moves a new, ungrouped book into the matched book's
// group, so it waits for a concurrent hand-off on that group and on the
// no-group sentinel it leaves.
func TestWriteHashValidation_LinkWaitsForGroupHolder(t *testing.T) {
	for _, held := range []string{"g", ""} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			f.Book(t, vptest.Spec{ID: "lib", Group: "g", Primary: "true"})
			n := f.Book(t, vptest.Spec{ID: "new", Primary: "nil"})
			b, err := f.S.GetBookByID(n)
			require.NoError(t, err)
			g := "g"
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(held) },
				func() error {
					dropped, err := writeHashValidation(f.S, n, b.FilePath, "deadbeef", itunes.ImportModeImport, &g, logger.New("test"))
					if dropped {
						t.Error("link dropped")
					}
					return err
				},
				func() bool { return f.GroupOf(t, n) == "" })
			require.Equal(t, "g", f.GroupOf(t, n))
			require.Equal(t, "false", f.Flag(t, n))
		})
	}
}
