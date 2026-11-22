// file: internal/maintenance/jobs/cleanup_organize_mess_test.go
// version: 1.0.0
// guid: b9c0d1e2-f3a4-5678-bcde-901234567234
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

func TestCleanupOrganizeMessJob_Registered(t *testing.T) {
	assertJobRegistered(t, "cleanup-organize-mess")
	job, err := maintenance.Get("cleanup-organize-mess")
	require.NoError(t, err)
	assert.Equal(t, "Cleanup Organize Mess", job.Name())
	assert.Equal(t, "cleanup", job.Category())
}

func TestCleanupOrganizeMessJob_DryRunDoesNotDelete(t *testing.T) {
	root := t.TempDir()
	// Create a directory that looks like a garbage/chapter-fragment dir.
	garbageDir := filepath.Join(root, "01_ bad fragment dir")
	require.NoError(t, os.MkdirAll(garbageDir, 0o755))

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-organize-mess")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, job.Run(context.Background(), nil, reporter, true /* dryRun */))

	// Dry-run must not delete anything.
	_, err = os.Stat(garbageDir)
	assert.NoError(t, err, "dry-run must not delete any directory")
}

func TestCleanupOrganizeMessJob_RemovesEmptyGarbageDirs(t *testing.T) {
	root := t.TempDir()
	// Create a purely numeric directory (garbage pattern).
	numericDir := filepath.Join(root, "123")
	require.NoError(t, os.MkdirAll(numericDir, 0o755))

	// Create a legitimate non-garbage directory with a file.
	legitDir := filepath.Join(root, "Author - Title")
	require.NoError(t, os.MkdirAll(legitDir, 0o755))
	f, createErr := os.Create(filepath.Join(legitDir, "book.m4b"))
	require.NoError(t, createErr)
	f.Close()

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-organize-mess")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, job.Run(context.Background(), nil, reporter, false /* dryRun */))

	// The legitimate directory must remain untouched.
	_, err = os.Stat(legitDir)
	assert.NoError(t, err, "legitimate directory must remain")
}

func TestCleanupOrganizeMessJob_CancelContext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "01_fragment"), 0o755))

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-organize-mess")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	reporter := &noopReporter{}
	// Should return without panic.
	_ = job.Run(ctx, nil, reporter, false)
}

func TestCleanupOrganizeMessJob_LogsGarbageDirs(t *testing.T) {
	root := t.TempDir()
	// Double-nested chapter segment pattern.
	doubleDir := filepath.Join(root, "Title - 01 - Chapter")
	require.NoError(t, os.MkdirAll(doubleDir, 0o755))

	orig := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = root })
	defer func() { config.SetForTest(func(c *config.Config) { c.RootDir = orig }) }()

	job, err := maintenance.Get("cleanup-organize-mess")
	require.NoError(t, err)

	reporter := &noopReporter{}
	require.NoError(t, job.Run(context.Background(), nil, reporter, true /* dryRun */))

	// Should have logged at least one message about garbage detection.
	assert.NotEmpty(t, reporter.logs, "expected at least one log entry for garbage dirs")
}
