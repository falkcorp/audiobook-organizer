// file: internal/dedup/book_dedup_primary_handoff_test.go
// version: 1.0.1
// guid: 9a2c7e41-5b3d-4f86-8e10-c4d7b2a95f63
// last-edited: 2026-10-17

package dedup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A split-book src that was its group's primary hands the flag on when it is
// soft-deleted into the keep.
func TestMergeSplitBookCluster_RetiredPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep", Primary: "true"})
	src := f.Book(t, vptest.Spec{ID: "src", Group: "g-src", Primary: "true"})
	sibling := f.Book(t, vptest.Spec{ID: "sib", Group: "g-src", Primary: "nil"})

	res, err := MergeSplitBookCluster(f.S, keep, []string{src}, "")
	require.NoError(t, err)
	require.Equal(t, 1, res.MergedSrcCount)
	f.RequireSinglePrimary(t, "g-src", sibling)
}
