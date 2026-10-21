// file: internal/plugins/maintenance/fragment_hash_copy_alias_test.go
// version: 1.0.0
// guid: 8d1f3a56-2e97-4c04-b6a8-f09e5d7c1b32
// last-edited: 2026-10-21

//go:build linux || darwin

package maintenance

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// A child whose file is a hard link of the parent's track is the same file,
// not a second copy: held as skipped_copy_unproven, and the row id spells the
// kind once ("held-copy-unproven:<child>", not "held-skipped-copy-unproven:").
func TestFragmentFixer_HashCopyGate_AHardLinkIsHeldWithAWellFormedRowID(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	childPath := f.path("lib/Stray c3/stray-c3.mp3")
	require.NoError(t, os.Remove(childPath))
	require.NoError(t, os.Link(f.path("lib/Alpha Omnibus/track-03.mp3"), childPath))

	res := f.plan(t, "op-plan")
	r := hcOnlyRow(t, res)
	require.Equal(t, "held-copy-unproven:"+child, r.RowID)
	require.NotContains(t, r.RowID, "held-skipped")
	requireHeld(t, f, r, fragSkipCopyUnproven, child)
}
