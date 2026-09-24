// file: internal/dedup/book_dedup_primary_handoff_test.go
// version: 1.0.0
// guid: 9a2c7e41-5b3d-4f86-8e10-c4d7b2a95f63
// last-edited: 2026-09-24

package dedup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A merge loser that was its version group's primary hands the flag to the
// group's remaining live member instead of leaving the group with none.
func TestMergeBooks_RetiredPrimaryHandsOnInItsGroup(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep", Group: "g-keep", Primary: "true"})
	loser := f.Book(t, vptest.Spec{ID: "loser", Group: "g-old", Primary: "true"})
	sibling := f.Book(t, vptest.Spec{ID: "sib", Group: "g-old", Primary: "false"})

	res, err := MergeBooks(context.Background(), f.S, "op-1", keep, []string{loser}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Errors)
	require.Equal(t, 1, res.MergedCount)

	f.RequireSinglePrimary(t, "g-old", sibling)
	f.RequireSinglePrimary(t, "g-keep", keep)
}

// Keep and loser in one group: the loser was primary, the keep inherits it,
// and the trailing keep-book write does not revert it.
func TestMergeBooks_RetiredPrimaryHandsToKeepInSameGroup(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep", Group: "g", Primary: "false"})
	loser := f.Book(t, vptest.Spec{ID: "loser", Group: "g", Primary: "true"})

	_, err := MergeBooks(context.Background(), f.S, "op-1", keep, []string{loser}, nil)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", keep)
}

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
