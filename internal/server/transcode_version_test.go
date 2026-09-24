// file: internal/server/transcode_version_test.go
// version: 1.0.0
// guid: 2f8a6d19-7c4e-4b35-a1d0-9e3b5c7f2a68
// last-edited: 2026-09-24

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func transcodeOutput(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, "Author", name+".m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("m4b"), 0o644))
	return p
}

func noLog(string, string) {}

// The transcode of a group's primary: the organized output takes the role
// and every other member is explicit false.
func TestRecordTranscodedVersion_OutputOfPrimaryIsCrowned(t *testing.T) {
	f := vptest.New(t)
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "true"})
	other := f.Book(t, vptest.Spec{ID: "other", Group: "g", Primary: "nil"})
	b, err := f.S.GetBookByID(orig)
	require.NoError(t, err)

	nb, inPlace, err := recordTranscodedVersion(context.Background(), f.S, b,
		transcodeOutput(t, f.Root, "orig-out"), 128, f.Root, noLog)
	require.NoError(t, err)
	require.False(t, inPlace)
	f.RequireSinglePrimary(t, "g", nb.ID)
	require.Equal(t, "false", f.Flag(t, orig))
	require.Equal(t, "false", f.Flag(t, other))
	files, err := f.S.GetBookFiles(nb.ID)
	require.NoError(t, err)
	require.Len(t, files, 1, "the output gets its own book_file row")
}

// The transcode of a non-primary member leaves the group's healthy primary
// alone: before 2026-09-24 the output was written as a second primary.
func TestRecordTranscodedVersion_OutputOfNonPrimaryKeepsIncumbent(t *testing.T) {
	f := vptest.New(t)
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "false"})
	b, err := f.S.GetBookByID(orig)
	require.NoError(t, err)

	nb, _, err := recordTranscodedVersion(context.Background(), f.S, b,
		transcodeOutput(t, f.Root, "orig-out"), 128, f.Root, noLog)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", inc)
	require.Equal(t, "false", f.Flag(t, nb.ID))
}

// An output that cannot be shown (the original sits outside the library, so
// the output is not organized) does not take the primary.
func TestRecordTranscodedVersion_IneligibleOutputIsNotCrowned(t *testing.T) {
	f := vptest.New(t)
	orig := f.Book(t, vptest.Spec{ID: "orig", Group: "g", Primary: "true", Outside: true})
	lib := f.Book(t, vptest.Spec{ID: "lib", Group: "g", Primary: "false"})
	b, err := f.S.GetBookByID(orig)
	require.NoError(t, err)

	nb, _, err := recordTranscodedVersion(context.Background(), f.S, b,
		transcodeOutput(t, t.TempDir(), "orig-out"), 128, f.Root, noLog)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g", lib)
	require.Equal(t, "false", f.Flag(t, nb.ID))
}
