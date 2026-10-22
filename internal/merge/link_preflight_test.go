// file: internal/merge/link_preflight_test.go
// version: 1.0.0
// guid: 7b1c9e03-4a6d-4d58-8f2a-0e3b5c8d1f46
// last-edited: 2026-10-22

package merge

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePreflightStore struct {
	*fakeGuardStore
	groups map[string][]database.Book
}

func (f *fakePreflightStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	return f.groups[gid], nil
}

func preflightBook(id, path, group string, trashed bool) *database.Book {
	b := &database.Book{ID: id, FilePath: path}
	if group != "" {
		b.VersionGroupID = &group
	}
	if trashed {
		now := time.Now()
		b.MarkedForDeletion = new(true)
		b.MarkedForDeletionAt = &now
	}
	return b
}

func newPreflightStore(books ...*database.Book) *fakePreflightStore {
	g := &fakeGuardStore{books: map[string]*database.Book{}, files: map[string][]database.BookFile{}}
	groups := map[string][]database.Book{}
	for _, b := range books {
		g.books[b.ID] = b
		if b.VersionGroupID != nil {
			groups[*b.VersionGroupID] = append(groups[*b.VersionGroupID], *b)
		}
	}
	return &fakePreflightStore{fakeGuardStore: g, groups: groups}
}

func TestPreflightLink_TrashedKeepIsRefused(t *testing.T) {
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", true),
		preflightBook("l", "/srv/managed/b.m4b", "", false))
	err := PreflightLink(st, "keep", []string{"l"})
	var sd *SoftDeletedInputError
	require.ErrorAs(t, err, &sd)
	assert.True(t, sd.AsPrimary)
	assert.True(t, IsRefusal(err))
}

// A trashed loser is the op's to skip, so it is not a refusal here, and an
// iTunes path on it does not matter (it is never merged).
func TestPreflightLink_TrashedLoserIsNotARefusal(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", false),
		preflightBook("live", "/srv/managed/b.m4b", "", false),
		preflightBook("trashed", testITunesMedia+"/Author/c.m4b", "", true))
	require.NoError(t, PreflightLink(st, "keep", []string{"trashed", "live"}))
}

func TestPreflightLink_MissingLoserIsNotFound(t *testing.T) {
	st := newPreflightStore(preflightBook("keep", "/srv/managed/a.m4b", "", false))
	var nf *BookNotFoundError
	require.ErrorAs(t, PreflightLink(st, "keep", []string{"ghost"}), &nf)
	assert.Equal(t, "ghost", nf.BookID)
}

func TestPreflightLink_ITunesLoserIsRefused(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", false),
		preflightBook("itunes", testITunesMedia+"/Author/b.m4b", "", false))
	err := PreflightLink(st, "keep", []string{"itunes"})
	require.Error(t, err)
	assert.True(t, IsRefusal(err))
}

// 2026-10-21 prod: the refused book was an iTunes copy in the LOSER's version
// group that nobody selected; the service re-homes it into the survivor's group.
func TestPreflightLink_ITunesSiblingOfLoserGroupIsRefused(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "G1", false),
		preflightBook("loser", "/srv/managed/b.m4b", "G2", false),
		preflightBook("itunes-sibling", testITunesMedia+"/Author/c.m4b", "G2", false))
	err := PreflightLink(st, "keep", []string{"loser"})
	var prot *ITunesProtectedError
	require.ErrorAs(t, err, &prot)
	assert.Equal(t, "itunes-sibling", prot.BookID)
}

func TestPreflightLink_CleanRequestPasses(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "G1", false),
		preflightBook("loser", "/srv/managed/b.m4b", "G2", false),
		preflightBook("sibling", "/srv/managed/c.m4b", "G2", false))
	require.NoError(t, PreflightLink(st, "keep", []string{"loser", "keep", "loser"}))
}
