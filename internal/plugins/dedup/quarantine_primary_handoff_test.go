// file: internal/plugins/dedup/quarantine_primary_handoff_test.go
// version: 1.0.0
// guid: 2b8f6c31-9d4e-4a07-8e53-f1c7a02d6b94
// last-edited: 2026-09-24

package dedup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A quarantined artifact that counted as its group's primary (nil flag) is
// followed by the invariant check: the group ends with exactly one live
// explicit primary and no other live member left nil.
func TestQuarantineChapterArtifacts_GroupEndsWithOnePrimary(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	p := &Plugin{store: f.S}

	var ids []string
	for i := range 6 {
		ids = append(ids, mkBook(t, f.S, "Opening Credits", i, 30))
	}
	vg := "vg-q"
	_, err := f.S.ModifyBook(ids[0], func(b *database.Book) error {
		b.VersionGroupID = &vg
		b.IsPrimaryVersion = nil
		return nil
	})
	require.NoError(t, err)
	real := f.Book(t, vptest.Spec{ID: "real", Group: vg, Primary: "true"})
	extra := f.Book(t, vptest.Spec{ID: "extra", Group: vg, Primary: "nil"})
	// Long enough not to be an artifact itself.
	for _, id := range []string{real, extra} {
		_, err := f.S.ModifyBook(id, func(b *database.Book) error { b.Title = "The Real Book " + id; return nil })
		require.NoError(t, err)
	}

	require.NoError(t, runQuarantine(t, p, true))
	require.True(t, isMarkedDeleted(t, f.S, ids[0]))
	f.RequireSinglePrimary(t, vg, real)
	require.Equal(t, "false", f.Flag(t, extra))
}
