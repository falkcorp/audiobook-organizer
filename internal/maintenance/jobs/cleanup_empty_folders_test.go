// file: internal/maintenance/jobs/cleanup_empty_folders_test.go
// version: 1.1.0
// guid: e6f7a8b9-c0d1-2345-efab-678901234f01
// last-edited: 2026-05-05

// Shared test helpers (noopReporter, blank jobs import) live in testhelpers_test.go.
package jobs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupEmptyFoldersJob_Registered(t *testing.T) {
	job, err := maintenance.Get("cleanup-empty-folders")
	require.NoError(t, err)
	assert.Equal(t, "cleanup-empty-folders", job.ID())
	assert.Equal(t, "Cleanup Empty Folders", job.Name())
	assert.Equal(t, "cleanup", job.Category())
	assert.True(t, job.CanResume())
}

func TestCleanupEmptyFoldersJob_DryRunDoesNotDelete(t *testing.T) {
	root := t.TempDir()
	// Create a nested empty directory structure.
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-empty-folders")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, job.Run(context.Background(), nil, reporter, true /* dryRun */))

	// Directories must still exist.
	_, err = os.Stat(deep)
	assert.NoError(t, err, "dry-run must not delete any directory")
}

func TestCleanupEmptyFoldersJob_RemovesEmptyDirs(t *testing.T) {
	root := t.TempDir()
	// Create a nested empty directory and a non-empty one.
	emptyDeep := filepath.Join(root, "empty", "child")
	require.NoError(t, os.MkdirAll(emptyDeep, 0o755))

	nonEmptyDir := filepath.Join(root, "nonempty")
	require.NoError(t, os.MkdirAll(nonEmptyDir, 0o755))
	f, err := os.Create(filepath.Join(nonEmptyDir, "file.m4b"))
	require.NoError(t, err)
	f.Close()

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-empty-folders")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, job.Run(context.Background(), nil, reporter, false /* dryRun */))

	// Empty directories should be gone.
	_, err = os.Stat(emptyDeep)
	assert.True(t, os.IsNotExist(err), "child of empty dir should be removed")
	_, err = os.Stat(filepath.Join(root, "empty"))
	assert.True(t, os.IsNotExist(err), "empty parent dir should be removed")

	// Non-empty directory and its file must remain.
	_, err = os.Stat(nonEmptyDir)
	assert.NoError(t, err, "non-empty directory must remain")
}

func TestCleanupEmptyFoldersJob_BottomUpOrder(t *testing.T) {
	// Verify that a parent dir is removed when its only child is empty and removed first.
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-empty-folders")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, job.Run(context.Background(), nil, reporter, false))

	// Both child and parent should be removed because child was removed first.
	_, err = os.Stat(parent)
	assert.True(t, os.IsNotExist(err), "parent should be removed after child is removed (bottom-up)")
}

func TestCleanupEmptyFoldersJob_CancelContext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-empty-folders")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	reporter := &noopReporter{}
	// Should return without error (or context.Canceled is acceptable).
	_ = job.Run(ctx, nil, reporter, false)
}
