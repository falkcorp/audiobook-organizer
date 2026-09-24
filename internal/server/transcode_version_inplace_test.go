// file: internal/server/transcode_version_inplace_test.go
// version: 1.0.0
// guid: f1e58660-8576-49f2-8dee-c84f15c18012
// last-edited: 2026-09-24

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// failingCreateStore refuses to create book rows, which sends
// recordTranscodedVersion down its in-place fallback: the original row is
// rewritten to the output instead of a new version being recorded.
type failingCreateStore struct {
	*database.PebbleStore
}

func (failingCreateStore) CreateBook(*database.Book) (*database.Book, error) {
	return nil, errors.New("create refused by test")
}

// In-place fallback of the group's primary: the rewritten row keeps the role
// through versionprimary.Crown, which also demotes a second member that had
// been left explicit true. Without the hand-off the group keeps two primaries.
func TestRecordTranscodedVersion_InPlaceFallbackOfPrimaryRecrownsIt(t *testing.T) {
	f := vptest.New(t)
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "true"})
	stray := f.Book(t, vptest.Spec{ID: "stray", Group: "g", Primary: "true"})
	b, err := f.S.GetBookByID(orig)
	require.NoError(t, err)

	out := transcodeOutput(t, f.Root, "orig-out")
	rewritten, inPlace, err := recordTranscodedVersion(context.Background(),
		failingCreateStore{f.S}, b, out, 128, f.Root, noLog)
	require.NoError(t, err)
	require.True(t, inPlace, "the create failure must take the in-place fallback")
	require.Equal(t, orig, rewritten.ID)
	require.Equal(t, out, rewritten.FilePath)

	f.RequireSinglePrimary(t, "g", orig)
	require.Equal(t, "false", f.Flag(t, stray))
}

// In-place fallback of a non-primary member of a group with no primary: the
// row keeps its explicit false and versionprimary.EnsureSinglePrimary elects
// one, so the group is not left with none (which hides the book from ABS).
func TestRecordTranscodedVersion_InPlaceFallbackOfNonPrimaryHandsGroupOn(t *testing.T) {
	f := vptest.New(t)
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "false"})
	f.Book(t, vptest.Spec{ID: "lib", Group: "g", Primary: "nil"})
	b, err := f.S.GetBookByID(orig)
	require.NoError(t, err)

	rewritten, inPlace, err := recordTranscodedVersion(context.Background(),
		failingCreateStore{f.S}, b, transcodeOutput(t, f.Root, "orig-out"), 128, f.Root, noLog)
	require.NoError(t, err)
	require.True(t, inPlace, "the create failure must take the in-place fallback")
	require.Equal(t, orig, rewritten.ID)

	f.RequireSinglePrimary(t, "g", "")
}
