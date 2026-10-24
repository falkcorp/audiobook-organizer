// file: internal/merge/itunes_database_only_test.go
// version: 1.0.0
// guid: 2f6a9c13-8b47-4e05-a1d9-5c3e7b0f8a62
// last-edited: 2026-10-22

package merge

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// Owner decision 2026-10-22: a merge may make DATABASE-ONLY changes to an
// iTunes book it did not select (its version_group_id and primary flag), and
// still refuses anything that would move, rename, retag or delete an iTunes
// file. These tests use a real file under a real configured iTunes media root
// so "the file is untouched" is checked on disk, not inferred.

type itunesCopy struct {
	id, path string
	sum      [sha256.Size]byte
}

// seedITunesCopy turns the fixture book id (created NoFile) into an iTunes
// copy: its FilePath and one book_file row point at a real file under media.
func seedITunesCopy(t *testing.T, f *vptest.Fixture, id, media string) itunesCopy {
	t.Helper()
	dir := filepath.Join(media, "Author", "Book "+id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, id+".m4b")
	body := []byte("iTunes audio bytes for " + id)
	require.NoError(t, os.WriteFile(p, body, 0o644))
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		b.FilePath = p
		b.Title = "Tagged title " + id
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, f.S.CreateBookFile(&database.BookFile{
		ID: "bf-" + id, BookID: id, FilePath: p, Format: "m4b", ITunesPersistentID: "PID" + id,
	}))
	return itunesCopy{id: id, path: p, sum: sha256.Sum256(body)}
}

// requireITunesCopyUntouched asserts the copy kept its row (everything but the
// group id, primary flag and update stamp), its book_file rows and its file.
func requireITunesCopyUntouched(t *testing.T, f *vptest.Fixture, c itunesCopy, before storeSnap) {
	t.Helper()
	after := snapshotBooks(t, f, c.id)[c.id]
	b, a := before.Book, after.Book
	b.VersionGroupID, a.VersionGroupID = nil, nil
	b.IsPrimaryVersion, a.IsPrimaryVersion = nil, nil
	b.UpdatedAt, a.UpdatedAt = nil, nil
	require.Equal(t, b, a, "only version_group_id and the primary flag may change")
	require.Equal(t, before.Files, after.Files, "its book_file rows must not change")
	require.Equal(t, c.path, after.Book.FilePath)
	body, err := os.ReadFile(c.path)
	require.NoError(t, err, "the iTunes file must still exist")
	require.Equal(t, c.sum, sha256.Sum256(body), "the iTunes file must be byte-identical")
}

func itunesMediaRoot(t *testing.T) string {
	t.Helper()
	media := filepath.Join(t.TempDir(), "iTunes Media")
	require.NoError(t, os.MkdirAll(media, 0o755))
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = media })
	return media
}

// A non-selected iTunes copy in the LOSER's version group (the 2026-10-21 prod
// refusal) is re-homed into the survivor's group: the merge succeeds, the copy
// gets the new group id and nothing else.
func TestMergeBooks_NonSelectedITunesSiblingChangesOnlyItsGroup(t *testing.T) {
	f := siblingFixture(t)
	media := itunesMediaRoot(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false"})
	f.Book(t, vptest.Spec{ID: "it", Group: "G", Primary: "true", State: "organized_source", NoFile: true})
	c := seedITunesCopy(t, f, "it", media)
	before := snapshotBooks(t, f, "it")["it"]

	res, err := NewService(f.S).MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	require.Len(t, res.MovedSiblings, 1)
	require.Equal(t, "it", res.MovedSiblings[0].BookID)

	require.Equal(t, "H", f.GroupOf(t, "it"), "the iTunes copy joined the survivor's group")
	require.Equal(t, "false", f.Flag(t, "it"), "and was demoted to a non-primary version")
	requireITunesCopyUntouched(t, f, c, before)
}

// A non-selected iTunes copy already in the survivor's group is demoted when
// the survivor takes the flag: the flag changes, nothing else does.
func TestMergeBooks_NonSelectedITunesGroupMemberChangesOnlyItsFlag(t *testing.T) {
	f := siblingFixture(t)
	media := itunesMediaRoot(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "false"})
	f.Book(t, vptest.Spec{ID: "it", Group: "H", Primary: "true", State: "organized_source", NoFile: true})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	c := seedITunesCopy(t, f, "it", media)
	before := snapshotBooks(t, f, "it")["it"]

	_, err := NewService(f.S).MergeBooks([]string{l, k}, k)
	require.NoError(t, err)

	require.Equal(t, "H", f.GroupOf(t, "it"))
	require.Equal(t, "false", f.Flag(t, "it"))
	requireITunesCopyUntouched(t, f, c, before)
}

// An iTunes book the caller SELECTED is a participant: it would be merged away,
// so it is still refused, with nothing written, whatever its group.
func TestMergeBooks_SelectedITunesBookIsStillRefused(t *testing.T) {
	f := siblingFixture(t)
	media := itunesMediaRoot(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	f.Book(t, vptest.Spec{ID: "it", Group: "G", Primary: "true", NoFile: true})
	c := seedITunesCopy(t, f, "it", media)
	before := snapshotBooks(t, f, k, "it")

	svc := NewService(f.S)
	res, err := svc.MergeBooks([]string{"it", k}, k)
	require.Nil(t, res)
	require.ErrorIs(t, err, ErrITunesProtected)
	require.Equal(t, before, snapshotBooks(t, f, k, "it"), "a refused merge writes nothing")
	requireNoSiblingJournal(t, svc)
	body, rerr := os.ReadFile(c.path)
	require.NoError(t, rerr)
	require.Equal(t, c.sum, sha256.Sum256(body))
}

// A combine moves book_file rows (and renumbers files) onto the survivor, which
// is a file-level change: an iTunes book among its inputs stays refused.
func TestCombineBooks_ITunesInputIsStillRefused(t *testing.T) {
	f := siblingFixture(t)
	media := itunesMediaRoot(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	f.Book(t, vptest.Spec{ID: "it", Group: "G", Primary: "true", NoFile: true})
	seedITunesCopy(t, f, "it", media)
	before := snapshotBooks(t, f, k, "it")

	_, err := NewService(f.S).CombineBooks([]string{"it", k}, k, nil)
	require.ErrorIs(t, err, ErrITunesProtected)
	require.Equal(t, before, snapshotBooks(t, f, k, "it"), "a refused combine writes nothing")
}
