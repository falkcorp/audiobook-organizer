// file: internal/plugins/maintenance/fragment_survivor_of_flag_holder_test.go
// version: 1.0.0
// guid: c644aa53-4421-45be-8ac3-b534f8ae7fe2
// last-edited: 2026-10-05

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// Item 6 of the #3762 re-review: a dedup merge whose survivor (k, the better
// recording, not organized) gave the group's flag to an organized sibling
// (ls) of the loser sends the loser's sync redirect to ls. The fixer must
// finish a run whose survivor was that loser into k, the book audio quality
// kept, not into ls.
func TestSurvivorOf_FlagHolderRedirectResolvesToSurvivor(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
	for id, format := range map[string]string{k: "m4b", l: "mp3", ls: "mp3"} {
		_, err := f.S.ModifyBook(id, func(b *database.Book) error { b.Format = format; return nil })
		require.NoError(t, err)
	}
	res, err := merge.NewService(f.S).MergeBooks([]string{l, k}, "")
	require.NoError(t, err)
	require.Equal(t, k, res.PrimaryID)
	require.Equal(t, ls, res.GroupPrimaryID)
	state, err := merge.ResolveSurvivor(f.S, l)
	require.NoError(t, err)
	require.Equal(t, ls, state, "the redirect leads to the flag holder")

	got, viaRedirect, err := survivorOf(f.S, newFragLibrary(), l)
	require.NoError(t, err)
	require.Equal(t, k, got, "the run finishes into the survivor")
	require.True(t, viaRedirect, "found through the MergeBooks redirect, a dedup loser")
}
