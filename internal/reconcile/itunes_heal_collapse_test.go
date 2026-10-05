// file: internal/reconcile/itunes_heal_collapse_test.go
// version: 1.0.0
// guid: 0c7e4b29-6a13-4d58-91f2-8e3b5d6a2c74
// last-edited: 2026-10-05

package reconcile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// healFixture seeds k (group H) and l (group G, with sibling ls), gives k's
// and l's files the same stored print, and returns their file paths.
func healFixture(t *testing.T) (f *vptest.Fixture, k, l, ls, kPath, lPath string) {
	t.Helper()
	f = vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k = f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l = f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls = f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	print := healTestFrames(400, 7)
	paths := map[string]string{}
	for _, id := range []string{k, l} {
		files, err := f.S.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1)
		files[0].AcoustIDFingerprint = print
		files[0].AcoustIDFPVersion = fingerprint.PrintEncodingVersion
		require.NoError(t, f.S.UpdateBookFile(files[0].ID, &files[0]))
		paths[id] = files[0].FilePath
	}
	return f, k, l, ls, paths[k], paths[l]
}

// S6: the heal's collapse goes through merge.Service, so the duplicate's
// version siblings follow it into the kept book's group (MergeBooks item 6)
// instead of being left in a group of their own.
func TestResolveAmbiguousByDB_CollapseUnitesVersionGroups(t *testing.T) {
	f, k, l, ls, kPath, lPath := healFixture(t)
	src, n := resolveAmbiguousByDB(context.Background(), f.S, []string{kPath, lPath})
	require.Equal(t, kPath, src)
	require.Equal(t, 1, n)
	lb, err := f.S.GetBookByID(l)
	require.NoError(t, err)
	require.True(t, lb.IsSoftDeleted(), "the duplicate is retired")
	require.Equal(t, "H", f.GroupOf(t, ls), "the duplicate's sibling joins the kept book's group")
	f.RequireSinglePrimary(t, "H", k)
}

// S6: the legacy path's shared-audio-path refusal is kept: a duplicate whose
// audio path is the kept book's is not retired, and nothing is written.
func TestResolveAmbiguousByDB_CollapseRefusesSharedAudioPath(t *testing.T) {
	f, k, l, ls, kPath, lPath := healFixture(t)
	_, err := f.S.ModifyBook(l, func(b *database.Book) error {
		b.FilePath = kPath
		return nil
	})
	require.NoError(t, err)
	snap := func() map[string]database.Book {
		out := map[string]database.Book{}
		for _, id := range []string{k, l, ls} {
			b, err := f.S.GetBookByID(id)
			require.NoError(t, err)
			out[id] = *b
		}
		return out
	}
	before := snap()
	src, n := resolveAmbiguousByDB(context.Background(), f.S, []string{kPath, lPath})
	require.Empty(t, src)
	require.Zero(t, n)
	require.Equal(t, before, snap(), "a refused collapse writes nothing")
}
