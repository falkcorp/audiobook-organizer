// file: internal/merge/combine_preflight_test.go
// version: 1.0.0
// guid: 6a1f8c32-5d79-4e04-b2a8-0c9e3d7f1b58
// last-edited: 2026-10-26

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreflightCombine_CleanRequestPasses(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", false),
		preflightBook("b", "/srv/managed/b.m4b", "", false),
		preflightBook("c", "/srv/managed/c.m4b", "", false))
	require.NoError(t, PreflightCombine(st, "keep", []string{"b", "c"}))
}

func TestPreflightCombine_MissingBookIsNotFound(t *testing.T) {
	st := newPreflightStore(preflightBook("keep", "/srv/managed/a.m4b", "", false))
	var nf *BookNotFoundError
	require.ErrorAs(t, PreflightCombine(st, "keep", []string{"ghost"}), &nf)
	assert.Equal(t, "ghost", nf.BookID)
	require.ErrorAs(t, PreflightCombine(st, "ghost", []string{"keep"}), &nf)
	assert.Equal(t, "ghost", nf.BookID)
}

// Unlike a link, a combine refuses a trashed participant outright: combining it
// would re-stamp its deletion time and journal an undo that restores a book the
// user had deleted.
func TestPreflightCombine_TrashedParticipantIsRefused(t *testing.T) {
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", false),
		preflightBook("b", "/srv/managed/b.m4b", "", true),
		preflightBook("trashed-keep", "/srv/managed/c.m4b", "", true))
	var sd *SoftDeletedInputError
	err := PreflightCombine(st, "keep", []string{"b"})
	require.ErrorAs(t, err, &sd)
	assert.Equal(t, "b", sd.BookID)
	assert.False(t, sd.AsPrimary)
	assert.True(t, IsRefusal(err))

	require.ErrorAs(t, PreflightCombine(st, "trashed-keep", []string{"keep"}), &sd)
	assert.True(t, sd.AsPrimary)
}

func TestPreflightCombine_ITunesBookIsRefused(t *testing.T) {
	withITunesConfig(t, func(c *config.ITunesConfig) { c.MediaRoot = testITunesMedia })
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", false),
		preflightBook("itunes", testITunesMedia+"/Author/b.m4b", "", false))
	err := PreflightCombine(st, "keep", []string{"itunes"})
	var prot *ITunesProtectedError
	require.ErrorAs(t, err, &prot)
	assert.Equal(t, "itunes", prot.BookID)
	assert.True(t, IsRefusal(err))
}

func TestPreflightCombine_MalformedRequestIsAPlainError(t *testing.T) {
	st := newPreflightStore(
		preflightBook("keep", "/srv/managed/a.m4b", "", false),
		preflightBook("b", "/srv/managed/b.m4b", "", false))
	require.Error(t, PreflightCombine(st, "", []string{"b"}))
	require.Error(t, PreflightCombine(st, "keep", nil), "a combine needs at least two books")
	require.ErrorContains(t, PreflightCombine(st, "keep", []string{"keep"}), "duplicate book id keep")
	require.ErrorContains(t, PreflightCombine(st, "keep", []string{"b", "b"}), "duplicate book id b")
}
