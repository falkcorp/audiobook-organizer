// file: internal/merge/primary_handoff_test.go
// version: 1.1.0
// guid: 4c1e8b27-6a9f-4d53-b0e2-7f3a5d91c846
// last-edited: 2026-09-24

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A participant that was its old group's primary and is pulled into another
// group by the merge hands its old group's flag on.
func TestMergeBooks_LeftGroupPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	b := f.Book(t, vptest.Spec{ID: "b", Group: "g-b", Primary: "true"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g-a", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g-a", Primary: "false"})

	res, err := NewService(f.S).MergeBooks([]string{b, a}, a)
	require.NoError(t, err)
	require.Equal(t, "g-b", res.VersionGroupID)

	f.RequireSinglePrimary(t, "g-b", a)
	f.RequireSinglePrimary(t, "g-a", sib)
}

// A combine that absorbs its version group's primary hands that group's flag
// on; undoing the combine restores the shell as a non-primary member.
func TestCombineBooks_AbsorbedPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	s := f.Book(t, vptest.Spec{ID: "s"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g-a", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g-a", Primary: "false"})

	svc := NewService(f.S)
	res, err := svc.CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.BooksDeleted)
	f.RequireSinglePrimary(t, "g-a", sib)

	_, err = svc.UndoCombine(res.JournalID)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g-a", sib)
	require.Equal(t, "false", f.Flag(t, a))
}
