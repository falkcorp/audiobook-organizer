// file: internal/tools/ollama_daemon_test.go
// version: 1.0.0
// guid: e1f2a3b4-c5d6-7890-efab-890123456789
// last-edited: 2026-06-15

package tools_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOllamaDaemon_AdoptAlivePID(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "ollama.pid")

	// Write our own PID — we know this process is alive.
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644))

	d := tools.NewOllamaDaemon(tools.OllamaDaemonConfig{
		BinPath:      "/usr/bin/true",
		PIDFile:      pidFile,
		Port:         19999,
		ReadyTimeout: 500,
	})
	// If PID is alive, EnsureRunningOrAdopt should adopt without launching.
	err := d.EnsureRunningOrAdopt(t.Context())
	assert.NoError(t, err)
}

func TestOllamaDaemon_StalePIDFileCleared(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "ollama.pid")
	// PID 99999999 almost certainly doesn't exist.
	require.NoError(t, os.WriteFile(pidFile, []byte("99999999"), 0o644))

	d := tools.NewOllamaDaemon(tools.OllamaDaemonConfig{
		BinPath:      "/usr/bin/false", // exits immediately with code 1
		PIDFile:      pidFile,
		Port:         19998,
		ReadyTimeout: 500, // 500ms — fail fast, don't wait 15s
	})
	// With a dead PID and /usr/bin/false as binary:
	// 1. Detect stale PID and remove pidFile
	// 2. Attempt to start /usr/bin/false (succeeds to launch, exits immediately)
	// 3. waitReady times out (no HTTP server)
	// 4. Synchronous cleanup removes the rewritten PID file and kills the child
	// 5. Return error
	err := d.EnsureRunningOrAdopt(t.Context())
	assert.Error(t, err)
	// Stale PID file must be cleaned up synchronously before EnsureRunningOrAdopt returns.
	_, statErr := os.Stat(pidFile)
	assert.True(t, os.IsNotExist(statErr), "stale PID file should be removed")
}
