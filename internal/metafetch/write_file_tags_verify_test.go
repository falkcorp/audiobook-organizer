// file: internal/metafetch/write_file_tags_verify_test.go
// version: 1.0.0
// guid: 6e3b0a94-7d15-4c28-b9f6-12a8d5c40e73
// last-edited: 2026-10-11

package metafetch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/stretchr/testify/require"
	taglib "go.senan.xyz/taglib"
)

func setVerifyAfterWriteForTest(t *testing.T, on bool) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.VerifyAfterWrite = on })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
}

func stageM4B(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "test_sample.m4b"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "book.m4b")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// The write-back's per-file tag write verifies after its own rename. A track
// number an MP4 integer atom cannot hold reads back as 0: the write-back must
// see tagger.ErrVerifyAfterWrite instead of counting the file as written.
func TestWriteFileTagsSafe_VerifiesAfterWrite(t *testing.T) {
	setVerifyAfterWriteForTest(t, true)
	svc := &Service{}

	path := stageM4B(t)
	require.NoError(t, svc.writeFileTagsSafe(context.Background(), path,
		map[string]any{"title": "Verified", "track": "3/12"}, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{}))
	got, err := taglib.ReadTags(path)
	require.NoError(t, err)
	require.Equal(t, []string{"Verified"}, got["TITLE"])

	err = svc.writeFileTagsSafe(context.Background(), stageM4B(t),
		map[string]any{"title": "Lossy", "track": "side A"}, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{})
	require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)

	setVerifyAfterWriteForTest(t, false)
	require.NoError(t, svc.writeFileTagsSafe(context.Background(), stageM4B(t),
		map[string]any{"title": "Lossy", "track": "side A"}, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{}))
}
