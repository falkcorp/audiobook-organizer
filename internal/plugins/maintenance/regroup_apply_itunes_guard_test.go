// file: internal/plugins/maintenance/regroup_apply_itunes_guard_test.go
// version: 1.0.1
// guid: 0c4f7a92-6d1b-4e38-8a25-9b3e1f6c7d40
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// markITunes gives book id an iTunes persistent id (synthetic).
func markITunes(t *testing.T, f *vptest.Fixture, id string) {
	t.Helper()
	pid := "PID-" + id
	_, err := f.S.ModifyBook(id, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
	require.NoError(t, err)
}

// The hand-off of a fresh group would demote an iTunes member (nil flag, a
// second incumbent): the iTunes guard refuses it before the link, so no
// book is linked and no flag is written.
func TestApplyVersionGroup_HandOffNeverWritesAnITunesMember(t *testing.T) {
	f := vptest.New(t)
	withLibraryRoot(t, f.Root)
	it := f.Book(t, vptest.Spec{ID: "it", Primary: "nil", State: "imported"})
	b := f.Book(t, vptest.Spec{ID: "b", Primary: "true"})
	markITunes(t, f, it)

	err := ApplyVersionGroup(f.S)(context.Background(), versionGroupItem(t, "/lib/vg", []string{it, b}))
	require.ErrorIs(t, err, versionprimary.ErrWriteRefused)
	require.ErrorIs(t, err, itunesguard.ErrITunesMember)
	require.Equal(t, "nil", f.Flag(t, it), "the iTunes member's flag is never written")
	require.Equal(t, "true", f.Flag(t, b))
	for _, id := range []string{it, b} {
		got, gerr := f.S.GetBookByID(id)
		require.NoError(t, gerr)
		require.Nil(t, got.VersionGroupID, "refused before the link: %s is not linked", id)
	}
}

// An iTunes book joining a reused group with a primary would be written
// explicit false: refused before the link, so nothing is written at all.
func TestApplyVersionGroup_ITunesJoinerIsRefusedBeforeTheLink(t *testing.T) {
	f := vptest.New(t)
	withLibraryRoot(t, f.Root)
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "false"})
	it := f.Book(t, vptest.Spec{ID: "it", Primary: "true"})
	markITunes(t, f, it)

	err := ApplyVersionGroup(f.S)(context.Background(), versionGroupItem(t, "/lib/vg", []string{a, it}))
	require.ErrorIs(t, err, itunesguard.ErrITunesMember)
	got, gerr := f.S.GetBookByID(it)
	require.NoError(t, gerr)
	require.Nil(t, got.VersionGroupID, "nothing linked")
	require.Equal(t, "true", f.Flag(t, it))
	f.RequireSinglePrimary(t, "g", inc)
}
